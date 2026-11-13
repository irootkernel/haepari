//go:build integration

package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// Synthetic historical records omit fields that did not exist when prepared.
// No installed or archived user state is read or migrated by these fixtures.
func withoutPreparationIdentity(raw []byte) []byte {
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	gomega.Expect(d.Decode(&value)).To(gomega.Succeed())
	var strip func(any)
	strip = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for _, key := range []string{"preparation", "persistent_identity", "observed_boot", "log_identity"} {
				delete(v, key)
			}
			for _, child := range v {
				strip(child)
			}
		case []any:
			for _, child := range v {
				strip(child)
			}
		}
	}
	strip(value)
	raw, err := json.Marshal(value)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return raw
}

func historicalGuestRecords(parent string) {
	gomega.Expect(filepath.WalkDir(parent, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) == ".json" {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(path, withoutPreparationIdentity(raw), 0600)
		}
		return nil
	})).To(gomega.Succeed())
}

func guestArchiveDigests(parent string) map[string][32]byte {
	files := map[string][32]byte{}
	gomega.Expect(filepath.WalkDir(parent, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[path] = sha256.Sum256(raw)
		}
		return nil
	})).To(gomega.Succeed())
	return files
}

var _ = ginkgo.Describe("GuestRecoveryCompatibilityNative", ginkgo.Label("integration"), func() {
	ginkgo.DescribeTable("never upgrades a historical completed preparation on replay", func(schema string) {
		guest, parent := guestFixture("printf once")
		defer os.RemoveAll(parent)
		guest.Schema = schema
		_, err := nativeGuestProtocol(guest, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		historicalGuestRecords(parent)
		request := filepath.Join(parent, guest.Request.Identity.JobID, "request.json")
		before, err := os.ReadFile(request)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		envelope, err := nativeGuestProtocol(guest, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var reply GuestReply
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		gomega.Expect(reply.Preparation).To(gomega.BeNil())
		gomega.Expect(reply.Control.Preparation).To(gomega.BeNil())
		gomega.Expect(os.ReadFile(request)).To(gomega.Equal(before))
		_, err = os.Stat(filepath.Join(parent, guest.Request.Identity.JobID, "intent.json"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	}, ginkgo.Entry("v1", "haepari.guest-request/v1"), ginkgo.Entry("v2", "haepari.guest-request/v2"))

	ginkgo.DescribeTable("leaves a legacy prior-boot orphan archived and unverified for every recovery operation", func(schema string) {
		q, parent := retainedPreviousBootGuestWithProtocol(schema)
		defer os.RemoveAll(parent)
		historicalGuestRecords(parent)
		raw, err := json.Marshal(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// Unmarshal into a fresh value so omitted fields cannot retain new identity.
		var historical GuestRecoveryRequest
		gomega.Expect(json.Unmarshal(withoutPreparationIdentity(raw), &historical)).To(gomega.Succeed())
		q = historical
		root := filepath.Join(parent, q.Guest.Request.Identity.JobID)
		for _, name := range []string{"intent.json", "start.json"} {
			path := filepath.Join(root, name)
			data, e := os.ReadFile(path)
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			var record map[string]json.RawMessage
			gomega.Expect(json.Unmarshal(data, &record)).To(gomega.Succeed())
			record["request_digest"], e = json.Marshal(q.Guest.Request.Binding().RequestDigest)
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			data, e = json.Marshal(record)
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			gomega.Expect(os.WriteFile(path, data, 0600)).To(gomega.Succeed())
		}
		before := guestArchiveDigests(parent)
		for _, operation := range []string{"probe", "attest", "state", "file", "cleanup", "health"} {
			q.Operation = operation
			_, err = nativeGuestProtocol(q, true, "")
			gomega.Expect(err).To(gomega.HaveOccurred(), operation)
			gomega.Expect(guestArchiveDigests(parent)).To(gomega.Equal(before), operation)
		}
	}, ginkgo.Entry("v1", "haepari.guest-request/v1"), ginkgo.Entry("v2", "haepari.guest-request/v2"))
})
