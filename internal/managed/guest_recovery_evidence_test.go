package managed

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("GuestRecoveryEvidenceBounds", ginkgo.Label("unit"), func() {
	ginkgo.It("accepts full required-output inventory plus independently bounded native evidence", func() {
		digest := RecoveryDigest("boundary")[7:]
		q := EvidenceRecoveryIntent{Action: "recollect", MutationKey: "boundary", Jobs: []EvidenceRecoveryJob{{JobID: "job", ResultDigest: RecoveryDigest("result"), Outputs: []string{"results/out.txt"}}}}
		r := EvidenceRecoveryReceipt{Schema: "haepari.evidence-recovery-receipt/v1", MutationKey: q.MutationKey, Jobs: []EvidenceRecoveryOutcome{{JobID: "job", ResultDigest: q.Jobs[0].ResultDigest, Stage: "collected", ArtifactRoot: "recovery-boundary", Artifacts: []ResultArtifact{{Path: "results/out.txt", SizeBytes: 1 << 30, SHA256: digest}}}}, Observation: RecoveryEvidence{Source: "machine-observed", Observer: "fixture", ObservedAt: time.Now().UTC(), Mechanism: "native", Digest: RecoveryDigest("evidence"), Reference: "resource:fixture"}}
		r.Jobs[0].GuestArtifacts = []ResultArtifact{{Path: "output.log", SizeBytes: GuestRecoveryLogBytes, SHA256: digest}}
		for _, name := range []string{"guest.json", "request.json", "intent.json", "start.json", "outcome.json", "exit.json"} {
			r.Jobs[0].GuestArtifacts = append(r.Jobs[0].GuestArtifacts, ResultArtifact{Path: name, SizeBytes: GuestRecoveryReceiptBytes, SHA256: digest})
		}
		// This is durable inventory validation, not a claim of streaming 1 GiB.
		gomega.Expect(validEvidenceReceipt(q, r)).To(gomega.BeTrue())
		for i := range r.Jobs[0].GuestArtifacts {
			r.Jobs[0].GuestArtifacts[i].SizeBytes++
			gomega.Expect(validEvidenceReceipt(q, r)).To(gomega.BeFalse())
			r.Jobs[0].GuestArtifacts[i].SizeBytes--
		}
		r.Jobs[0].Artifacts[0].SizeBytes++
		gomega.Expect(validEvidenceReceipt(q, r)).To(gomega.BeFalse())
		r.Jobs[0].Artifacts[0].SizeBytes--
		q.Jobs[0].GuestLogLimitBytes = 8
		r.Jobs[0].GuestArtifacts[0].SizeBytes = 8
		gomega.Expect(validEvidenceReceipt(q, r)).To(gomega.BeTrue())
		r.Jobs[0].GuestArtifacts[0].SizeBytes++
		gomega.Expect(validEvidenceReceipt(q, r)).To(gomega.BeFalse())
	})
	ginkgo.It("uses historical log default and refuses unsupported native file budgets", func() {
		limit, valid := GuestRecoveryArtifactLimit("output.log", 0)
		gomega.Expect(valid).To(gomega.BeTrue())
		gomega.Expect(limit).To(gomega.Equal(GuestRecoveryLogBytes))
		for _, name := range []string{"haepari", "../exit.json", "workspace/output.log"} {
			_, valid = GuestRecoveryArtifactLimit(name, 0)
			gomega.Expect(valid).To(gomega.BeFalse())
		}
		for _, limit := range []int64{-1, GuestRecoveryLogBytes + 1} {
			_, valid = GuestRecoveryArtifactLimit("output.log", limit)
			gomega.Expect(valid).To(gomega.BeFalse())
		}
	})
})
