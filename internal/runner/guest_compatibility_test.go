package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("GuestRecoveryCompatibility", ginkgo.Label("unit"), func() {
	ginkgo.It("preserves the historical native request bytes and binding digest", func() {
		const original = `{"schema":"haepari.runner-request/v1","identity":{"JobID":"job-original","ResourceID":"local","ConfigRevision":1,"Slot":0,"LeaseGeneration":1,"RunID":"run-original","Nonce":"nonce"},"boot_id":"old-boot","work_dir":"/tmp/original/workspace","argv":["/bin/true"],"environment":[],"deadline":"2026-01-01T00:00:00Z","run_seconds":5,"termination_seconds":2,"log_device":1,"log_inode":2}`
		var q Request
		gomega.Expect(json.Unmarshal([]byte(original), &q)).To(gomega.Succeed())
		gomega.Expect(q.Validate("/tmp/original")).To(gomega.Succeed())
		raw, err := json.Marshal(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(raw)).To(gomega.Equal(original))
		sum := sha256.Sum256([]byte(original))
		gomega.Expect(q.Binding().RequestDigest).To(gomega.Equal("sha256:" + hex.EncodeToString(sum[:])))
	})

	ginkgo.DescribeTable("decodes historical recovery request/reply without adding preparation authority", func(version string) {
		q := GuestRecoveryRequest{Schema: "haepari.guest-recovery-request/" + version, Guest: GuestRequest{Schema: "haepari.guest-request/" + version}}
		raw, err := json.Marshal(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		decoded, err := DecodeGuestRecoveryRequest(raw)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(decoded).To(gomega.Equal(q))
		r := GuestRecoveryReply{Schema: "haepari.guest-recovery-reply/" + version, Guest: GuestReply{Schema: "haepari.guest-reply/" + version}}
		raw, err = json.Marshal(r)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		reply, err := DecodeGuestRecoveryReply(raw)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(reply).To(gomega.Equal(r))
		gomega.Expect(reply.Guest.Preparation).To(gomega.BeNil())
	}, ginkgo.Entry("v1", "v1"), ginkgo.Entry("v2", "v2"))

	ginkgo.It("rejects preparation fields in frozen protocols and admits only the new versioned capability", func() {
		for _, version := range []string{"v1", "v2", "v3", "v4"} {
			for _, schema := range []string{GuestPreparationSchema, "", "haepari.guest-preparation/v2"} {
				p := &GuestPreparation{Schema: schema}
				q := GuestRecoveryRequest{Schema: "haepari.guest-recovery-request/" + version, Guest: GuestRequest{Schema: "haepari.guest-request/" + version, Preparation: p}}
				raw, err := json.Marshal(q)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				_, requestErr := DecodeGuestRecoveryRequest(raw)
				r := GuestRecoveryReply{Schema: "haepari.guest-recovery-reply/" + version, Guest: GuestReply{Schema: "haepari.guest-reply/" + version, Preparation: p}}
				raw, err = json.Marshal(r)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				_, replyErr := DecodeGuestRecoveryReply(raw)
				guestRaw, err := json.Marshal(r.Guest)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				_, guestErr := DecodeGuestReply(guestRaw)
				for _, decodeErr := range []error{requestErr, replyErr, guestErr} {
					if schema == GuestPreparationSchema && (version == "v3" || version == "v4") {
						gomega.Expect(decodeErr).NotTo(gomega.HaveOccurred())
					} else {
						gomega.Expect(decodeErr).To(gomega.MatchError(ErrInvalid))
					}
				}
			}
		}
	})
})
