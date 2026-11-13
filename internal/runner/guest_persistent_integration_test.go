//go:build integration

package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

// Model a mount-device renumbering in original evidence, not a real reboot.
// Persistent descriptor observations are unchanged; all native receipt bindings
// are rewritten consistently with the synthetic historical request.
func remappedPreviousBootGuest() (GuestRecoveryRequest, string) {
	q, parent := retainedPreviousBootGuestWithProtocol("haepari.guest-request/v4")
	root := filepath.Join(parent, q.Guest.Request.Identity.JobID)
	path := filepath.Join(parent, ".haepari-control-"+q.Guest.Request.Identity.JobID+".json")
	var c GuestControl
	raw, err := os.ReadFile(path)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(json.Unmarshal(raw, &c)).To(gomega.Succeed())
	remap := func(p *managed.RetainedPath) { p.Device += 123; p.ObservedBoot = q.Guest.BootID }
	remap(c.Root)
	remap(c.WorkspaceRoot)
	remap(&c.Preparation.Control)
	remap(&c.Preparation.Runner)
	c.Preparation.Workspace = c.WorkspaceRoot
	q.Guest.Root, q.Guest.Preparation = c.Root, c.Preparation
	q.Guest.Request.LogDevice += 123
	c.NativeRequest = &q.Guest.Request
	write := func(path string, value any) {
		b, e := json.Marshal(value)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.WriteFile(path, b, 0600)).To(gomega.Succeed())
	}
	write(path, c)
	write(filepath.Join(root, "request.json"), q.Guest.Request)
	var intent Intent
	raw, err = os.ReadFile(filepath.Join(root, "intent.json"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(json.Unmarshal(raw, &intent)).To(gomega.Succeed())
	intent.Binding = q.Guest.Request.Binding()
	write(filepath.Join(root, "intent.json"), intent)
	var start Start
	raw, err = os.ReadFile(filepath.Join(root, "start.json"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(json.Unmarshal(raw, &start)).To(gomega.Succeed())
	start.Binding = q.Guest.Request.Binding()
	write(filepath.Join(root, "start.json"), start)
	return q, parent
}

func attestPersistentGuest(q GuestRecoveryRequest) GuestRecoveryRequest {
	envelope, err := nativeGuestProtocol(q, true, "")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var reply GuestRecoveryReply
	gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
	q.Basis, q.Operation, q.MutationKey = reply.Basis, "attest", "persistent-recovery"
	q.Evidence = &managed.RecoveryEvidence{Source: "machine-observed", Observer: "synthetic-device-remap", ObservedAt: time.Now().UTC(), Mechanism: "guest-reboot-observed", Digest: managed.RecoveryDigest(*q.Basis), Reference: "job:" + q.Guest.Request.Identity.JobID}
	envelope, err = nativeGuestProtocol(q, true, "")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
	q.Attestation = reply.Attestation
	return q
}

var _ = ginkgo.Describe("PersistentGuestIdentity", ginkgo.Label("integration"), func() {
	ginkgo.It("binds preparation descriptors and recovers across a changed device through apply, log/output reads, cleanup and health", func() {
		q, parent := remappedPreviousBootGuest()
		defer os.RemoveAll(parent)
		gomega.Expect(q.Guest.Root.Persistent.Valid()).To(gomega.BeTrue())
		gomega.Expect(q.Guest.Preparation.Control.Persistent.Valid()).To(gomega.BeTrue())
		gomega.Expect(q.Guest.Preparation.Runner.Persistent.Valid()).To(gomega.BeTrue())
		gomega.Expect(q.Guest.Preparation.Workspace.Persistent.Valid()).To(gomega.BeTrue())
		gomega.Expect(q.Guest.Request.LogIdentity.Valid()).To(gomega.BeTrue())
		before, err := os.ReadFile(filepath.Join(parent, q.Guest.Request.Identity.JobID, "request.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q = attestPersistentGuest(q)
		for _, area := range []string{"workspace", "log"} {
			q.Operation, q.Guest.Area, q.Guest.Path = "file", area, "count.txt"
			expected := []byte("once")
			if area == "log" {
				q.Guest.Path, expected = "output.log", []byte("native-log")
			}
			envelope, e := nativeGuestProtocol(q, true, "")
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			var reply GuestRecoveryReply
			gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
			gomega.Expect(reply.Guest.Output).To(gomega.Equal(expected))
		}
		gomega.Expect(os.ReadFile(filepath.Join(parent, q.Guest.Request.Identity.JobID, "request.json"))).To(gomega.Equal(before))
		q.Operation, q.CollectionDigest = "cleanup", managed.RecoveryDigest("collected-original-bytes")
		_, err = nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q.Operation = "health"
		envelope, err := nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var reply GuestRecoveryReply
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		gomega.Expect(reply.Reusable).To(gomega.BeTrue())
	})

	ginkgo.It("refuses same-boot device drift and missing, unsupported or foreign preparation evidence", func() {
		q, parent := remappedPreviousBootGuest()
		defer os.RemoveAll(parent)
		// The matching policy still refuses a different device on this same boot.
		f, err := os.Open(filepath.Join(parent, q.Guest.Request.Identity.JobID))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer f.Close()
		currentBoot, err := platform.BootIdentity()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		p := *q.Guest.Root
		p.ObservedBoot = currentBoot
		gomega.Expect(retainedDescriptorMatches(int(f.Fd()), p)).To(gomega.BeFalse())
		for _, change := range []func(*GuestRequest){
			func(g *GuestRequest) { g.Preparation = nil },
			func(g *GuestRequest) { g.Request.LogIdentity = platform.FileIdentity{} },
			func(g *GuestRequest) { g.Root.Persistent = platform.FileIdentity{} },
			func(g *GuestRequest) { g.Preparation.Control.Persistent.VolumeUUID = "unsupported" },
			func(g *GuestRequest) { g.Preparation.Runner.Persistent.FileID++ },
			func(g *GuestRequest) { g.Preparation.Workspace.Persistent.CreatedNanos++ },
		} {
			raw, e := json.Marshal(q)
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			var changed GuestRecoveryRequest
			gomega.Expect(json.Unmarshal(raw, &changed)).To(gomega.Succeed())
			change(&changed.Guest)
			_, e = nativeGuestProtocol(changed, true, "")
			gomega.Expect(e).To(gomega.HaveOccurred())
		}
		// Both host and guest lacking persistent evidence is legacy, never adoption.
		path := filepath.Join(parent, ".haepari-control-"+q.Guest.Request.Identity.JobID+".json")
		raw, err := os.ReadFile(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var c GuestControl
		gomega.Expect(json.Unmarshal(raw, &c)).To(gomega.Succeed())
		c.Preparation, q.Guest.Preparation = nil, nil
		raw, err = json.Marshal(c)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.WriteFile(path, raw, 0600)).To(gomega.Succeed())
		_, err = nativeGuestProtocol(q, true, "")
		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	ginkgo.DescribeTable("rejects copied/replaced objects before plan and after apply without deleting original evidence", func(target string, applied bool) {
		q, parent := remappedPreviousBootGuest()
		defer os.RemoveAll(parent)
		if applied {
			q = attestPersistentGuest(q)
		}
		root := filepath.Join(parent, q.Guest.Request.Identity.JobID)
		path := filepath.Join(root, target)
		if target == "root" {
			path = root
		}
		if target == "control" {
			path = filepath.Join(parent, ".haepari-control-"+q.Guest.Request.Identity.JobID+".json")
		}
		gomega.Expect(os.Rename(path, path+".original")).To(gomega.Succeed())
		if target == "root" || target == "workspace" {
			gomega.Expect(os.Mkdir(path, 0700)).To(gomega.Succeed())
		} else {
			data, e := os.ReadFile(path + ".original")
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			gomega.Expect(os.WriteFile(path, data, 0700)).To(gomega.Succeed())
		}
		operations := []string{"probe"}
		if applied {
			operations = []string{"file", "cleanup", "health"}
		}
		for _, operation := range operations {
			q.Operation, q.Guest.Path, q.CollectionDigest = operation, "count.txt", managed.RecoveryDigest("collected")
			_, err := nativeGuestProtocol(q, true, "")
			gomega.Expect(err).To(gomega.HaveOccurred())
			_, err = os.Stat(path + ".original")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
	},
		ginkgo.Entry("root before plan", "root", false), ginkgo.Entry("root after apply", "root", true),
		ginkgo.Entry("workspace before plan", "workspace", false), ginkgo.Entry("workspace after apply", "workspace", true),
		ginkgo.Entry("control before plan", "control", false), ginkgo.Entry("control after apply", "control", true),
		ginkgo.Entry("runner before plan", "haepari", false), ginkgo.Entry("runner after apply", "haepari", true),
		ginkgo.Entry("log before plan", "output.log", false), ginkgo.Entry("log after apply", "output.log", true))

	ginkgo.It("revalidates original request, receipt and runner digests between plan and apply", func() {
		for _, name := range []string{"guest.json", "request.json", "intent.json", "start.json", "haepari"} {
			q, parent := remappedPreviousBootGuest()
			defer os.RemoveAll(parent)
			envelope, err := nativeGuestProtocol(q, true, "")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var reply GuestRecoveryReply
			gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
			q.Basis, q.Operation, q.MutationKey = reply.Basis, "attest", "changed-original-digest"
			q.Evidence = &managed.RecoveryEvidence{Source: "machine-observed", Observer: "synthetic-device-remap", ObservedAt: time.Now().UTC(), Mechanism: "guest-reboot-observed", Digest: managed.RecoveryDigest(*q.Basis), Reference: "job:" + q.Guest.Request.Identity.JobID}
			path := filepath.Join(parent, q.Guest.Request.Identity.JobID, name)
			data, err := os.ReadFile(path)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			// Keep the object and valid JSON intact; only original receipt bytes change.
			changed := append(data, '\n')
			gomega.Expect(os.WriteFile(path, changed, 0700)).To(gomega.Succeed())
			_, err = nativeGuestProtocol(q, true, "")
			gomega.Expect(err).To(gomega.HaveOccurred(), name)
			gomega.Expect(os.ReadFile(path)).To(gomega.Equal(changed))
			_, err = os.Stat(filepath.Join(parent, ".haepari-attestation-"+q.Guest.Request.Identity.JobID+".json"))
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		}
	})

	ginkgo.It("refuses a live worker lock even when all persistent identities match", func() {
		q, parent := remappedPreviousBootGuest()
		defer os.RemoveAll(parent)
		f, err := os.OpenFile(filepath.Join(parent, q.Guest.Request.Identity.JobID, "run.lock"), os.O_RDWR, 0)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer f.Close()
		gomega.Expect(unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)).To(gomega.Succeed())
		defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_, err = nativeGuestProtocol(q, true, "")
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
