//go:build integration

package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func buildTransportVerifier(root string) (string, string) {
	binary := filepath.Join(root, "verifier")
	cmd := exec.Command("make", "build", "OUTPUT="+binary)
	cmd.Dir = "../.."
	output, err := cmd.CombinedOutput()
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), string(output))
	data, err := os.ReadFile(binary)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	sum := sha256.Sum256(data)
	return binary, "sha256:" + hex.EncodeToString(sum[:])
}
func productTransportCommand(binary, verb string, q any) managed.Envelope {
	raw, err := json.Marshal(q)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	cmd := exec.Command(binary, "runner", verb, "--stdio")
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	cmd.Stdin = bytes.NewReader(raw)
	var output, diagnostic bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &diagnostic
	err = cmd.Run()
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), diagnostic.String())
	envelope, err := managed.DecodeEnvelope(output.Bytes())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(envelope.OK).To(gomega.BeTrue(), diagnostic.String())
	return envelope
}

var _ = ginkgo.Describe("Task037TransportVerifier", ginkgo.Label("integration"), func() {
	ginkgo.It("uses a different pinned verifier for original normal cleanup health and refuses its use for effects", func() {
		q, parent := guestFixture("exit 0")
		defer os.RemoveAll(parent)
		binary, digest := buildTransportVerifier(parent)
		q.RunnerDigest = digest
		q.Schema = "haepari.guest-request/v2"
		envelope := productTransportCommand(binary, "guest", q)
		var prepared GuestReply
		gomega.Expect(json.Unmarshal(envelope.Data, &prepared)).To(gomega.Succeed())
		q.Request = *prepared.Request
		q.Root = prepared.Root
		q.Files = nil
		q.Inventory = nil
		q.Operation = "retire"
		_, err := Guest(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q.Operation = "cleanup"
		_, err = Guest(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		self, err := os.Executable()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		data, err := os.ReadFile(self)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		sum := sha256.Sum256(data)
		currentDigest := "sha256:" + hex.EncodeToString(sum[:])
		gomega.Expect(currentDigest).NotTo(gomega.Equal(digest))
		control := filepath.Join(parent, ".haepari-control-"+q.Request.Identity.JobID+".json")
		before, err := os.ReadFile(control)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q.Operation = "health"
		q.Deadline = time.Now().Add(10 * time.Second)
		request := GuestTransportRequest{Schema: "haepari.guest-transport-request/v1", Guest: q, ExecutorDigest: currentDigest}
		r, err := GuestTransport(request)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(r.Guest.Reusable).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(control)).To(gomega.Equal(before))
		for _, op := range []string{"prepare", "start", "cleanup", "file"} {
			request.Guest.Operation = op
			_, err = GuestTransport(request)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(os.ReadFile(control)).To(gomega.Equal(before))
		}
	})
	ginkgo.It("preserves the original attestation and recovery digest with a new health verifier and refuses a still-present root", func() {
		q, parent := retainedNativeGuest()
		defer os.RemoveAll(parent)
		binary, digest := buildTransportVerifier(parent)
		gomega.Expect(digest).NotTo(gomega.Equal(q.RecoveryDigest))
		envelope, err := nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var reply GuestRecoveryReply
		gomega.Expect(json.Unmarshal(envelope.Data, &reply)).To(gomega.Succeed())
		q.Attestation = reply.Attestation
		q.Operation = "health"
		q.Deadline = time.Now().Add(10 * time.Second)
		request := GuestTransportRequest{Schema: "haepari.guest-transport-request/v1", Guest: q.Guest, ExecutorDigest: digest, Recovery: &q}
		raw, _ := json.Marshal(request)
		cmd := exec.Command(binary, "runner", "guest-transport", "--stdio")
		cmd.Stdin = bytes.NewReader(raw)
		output, err := cmd.Output()
		gomega.Expect(err).To(gomega.HaveOccurred())
		negative, e := managed.DecodeEnvelope(output)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(negative.OK).To(gomega.BeFalse())
		q.Operation = "cleanup"
		q.Deadline = time.Now().Add(time.Minute)
		q.MutationKey = "transport-cleanup"
		q.CollectionDigest = managed.RecoveryDigest(q.Basis.Digests)
		_, err = nativeGuestProtocol(q, true, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		attestation := filepath.Join(parent, ".haepari-attestation-"+q.Guest.Request.Identity.JobID+".json")
		before, err := os.ReadFile(attestation)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q.Operation = "health"
		q.Deadline = time.Now().Add(10 * time.Second)
		request.Guest = q.Guest
		envelope = productTransportCommand(binary, "guest-transport", request)
		var health GuestTransportReply
		gomega.Expect(json.Unmarshal(envelope.Data, &health)).To(gomega.Succeed())
		gomega.Expect(health.Recovery).NotTo(gomega.BeNil())
		gomega.Expect(health.Recovery.Reusable).To(gomega.BeTrue())
		gomega.Expect(health.Recovery.RecoveryDigest).To(gomega.Equal(q.RecoveryDigest))
		gomega.Expect(health.Recovery.Attestation).To(gomega.Equal(q.Attestation))
		gomega.Expect(os.ReadFile(attestation)).To(gomega.Equal(before))
	})
})
