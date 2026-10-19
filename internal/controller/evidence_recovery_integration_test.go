//go:build integration

package controller

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func protectedLocalFixture() (*runtimeServer, managed.Receipt, managed.Result) {
	return protectedLocalResourceFixture(nil)
}

func protectedLocalResourceFixture(configure func(*managed.ResourceSpec)) (*runtimeServer, managed.Receipt, managed.Result) {
	_, s, r, job, object := cancellationRuntimeConfigured(ginkgo.GinkgoT(), "mkdir -p ../results; printf recoverable > ../results/output.txt", "local", "cancel-job", configure)
	c, err := s.registerLocal(r)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer s.unregisterLocal(c)
	c.beforePhase = func(phase string) {
		if phase == "collecting" {
			gomega.Expect(os.WriteFile(filepath.Join(s.root.Path, "jobs", r.JobID, "artifacts"), []byte("collection destination fault"), 0600)).To(gomega.Succeed())
		}
	}
	gomega.Expect(s.executeLocalControlled(c, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
	result, err := s.ledger.Result(r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
	gomega.Expect(result.ProtectedEvidence).NotTo(gomega.BeEmpty())
	gomega.Expect(os.Remove(filepath.Join(s.root.Path, "jobs", r.JobID, "artifacts"))).To(gomega.Succeed())
	return s, r, result
}

var _ = ginkgo.Describe("Task012EvidenceRecovery", ginkgo.Label("integration"), func() {
	for _, action := range []string{"recollect", "dispose"} {
		action := action
		ginkgo.It("retains protected evidence across restart and applies explicit "+action+" without rewriting the verdict", func() {
			s, r, original := protectedLocalFixture()
			fresh, err := newRuntime(s.root, s.ledger, s.store)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
			blocked, err := fresh.planResourceRecovery(r.ResourceID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(blocked.PlanToken).To(gomega.BeEmpty())
			_, err = fresh.purgeJob(r.JobID, 100)
			gomega.Expect(err).To(gomega.HaveOccurred())
			plan, err := fresh.planEvidenceRecovery(r.ResourceID, action)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
			if action == "dispose" {
				gomega.Expect(plan.EvidenceLoss).To(gomega.Equal(original.ProtectedEvidence))
			}
			q := managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "evidence-" + action}
			w := recoveryHTTP(fresh, "/v1/resources/"+r.ResourceID+"/recover", q)
			gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
			var envelope struct {
				Data managed.EvidenceRecoveryReceipt `json:"data"`
			}
			gomega.Expect(json.Unmarshal(w.Body.Bytes(), &envelope)).To(gomega.Succeed())
			gomega.Expect(envelope.Data.Complete).To(gomega.BeTrue())
			gomega.Expect(envelope.Data.Jobs[0].Stage).To(gomega.Equal("resolved"))
			if action == "recollect" {
				gomega.Expect(envelope.Data.Jobs[0].Artifacts).To(gomega.HaveLen(1))
			}
			current, err := fresh.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current).To(gomega.Equal(original))
			_, err = os.Stat(filepath.Join(s.root.Path, "jobs", r.JobID, "workspace"))
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			w = recoveryHTTP(fresh, "/v1/resources/"+r.ResourceID+"/recover", q)
			gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
			health, err := fresh.planResourceRecovery(r.ResourceID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(health.PlanToken).NotTo(gomega.BeEmpty())
			w = recoveryHTTP(fresh, "/v1/resources/"+r.ResourceID+"/recover", managed.RecoveryRequest{PlanToken: health.PlanToken, MutationKey: "health-" + action})
			gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
			status, err := fresh.ledger.ResourceStatus(r.ResourceID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(status.Availability).To(gomega.Equal("available"))
			current, err = fresh.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current.FinalVerdict).To(gomega.Equal(managed.Incomplete))
			if action == "recollect" {
				manifest, err := fresh.artifactManifest(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(manifest.Supplementary).To(gomega.HaveLen(1))
				gomega.Expect(manifest.Files).To(gomega.HaveLen(2))
			}
		})
	}
	ginkgo.It("withholds reuse while an interrupted supplementary collector has a live native owner", func() {
		s, r, _ := protectedLocalFixture()
		snap, jobs, err := s.evidenceRecoveryBasis(r.ResourceID, "recollect")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		owner, err := platform.ObserveProcess(os.Getpid())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		b, err := s.ledger.BulkSnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		now := time.Now().UTC()
		jobs[0].CollectDeadline = now.Add(time.Duration(b.Spec.Deadlines.CollectSeconds) * time.Second)
		jobs[0].CleanupDeadline = jobs[0].CollectDeadline.Add(time.Duration(b.Spec.Deadlines.CleanupSeconds) * time.Second)
		q := managed.EvidenceRecoveryIntent{Schema: "haepari.evidence-recovery-intent/v1", ResourceID: r.ResourceID, ResourceConfigRevision: snap.Resource.ConfigRevision, ResourceAvailabilityRevision: snap.Resource.AvailabilityRevision, SnapshotDigest: managed.RecoveryDigest(snap), ClientDigest: managed.RecoveryDigest("fixture"), MutationKey: "blocked-collector", Action: "recollect", Owner: owner.Identity, Jobs: jobs, RecordedAt: now}
		gomega.Expect(s.ledger.BeginEvidenceRecovery(q)).To(gomega.Succeed())
		fresh, err := newRuntime(s.root, s.ledger, s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fresh.resumeEvidenceRecovery(q)).To(gomega.MatchError(managed.ErrBlocked))
		status, err := fresh.ledger.ResourceStatus(r.ResourceID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Availability).To(gomega.Equal("recovering"))
		gomega.Expect(os.Stat(filepath.Join(s.root.Path, "jobs", r.JobID, "workspace/results/output.txt"))).Error().NotTo(gomega.HaveOccurred())
	})
})
