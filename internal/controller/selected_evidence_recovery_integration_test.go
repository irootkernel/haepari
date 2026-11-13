//go:build integration

package controller

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// Both Jobs run real local workloads in an isolated fixture. The availability
// fault setup permits creating a second retained Job on the same resource.
func selectedEvidenceFixture(configure func(*managed.ResourceSpec)) (*sql.DB, *runtimeServer, managed.Receipt, managed.Receipt) {
	db, s, first, job, object := cancellationRuntimeConfigured(ginkgo.GinkgoT(), "mkdir -p ../results; printf recoverable > ../results/output.txt", "local", "first-retained", configure)
	run := func(r managed.Receipt) {
		c, err := s.registerLocal(r)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer s.unregisterLocal(c)
		c.beforePhase = func(phase string) {
			if phase == "collecting" {
				gomega.Expect(os.WriteFile(filepath.Join(s.root.Path, "jobs", r.JobID, "artifacts"), []byte("collection fault"), 0600)).To(gomega.Succeed())
			}
		}
		gomega.Expect(s.executeLocalControlled(c, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		gomega.Expect(os.Remove(filepath.Join(s.root.Path, "jobs", r.JobID, "artifacts"))).To(gomega.Succeed())
	}
	run(first)
	_, err := db.Exec(`UPDATE managed_resources SET availability='available' WHERE id=?`, first.ResourceID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	status, err := s.ledger.ResourceStatus(first.ResourceID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	raw, err := json.Marshal(job)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	next, err := s.ledger.AdmitSealed(raw, job.Input.InventoryDigest, object, "second-key", "second-retained")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	next, err = s.ledger.Reserve(managed.ReserveRequest{JobID: next.JobID, ExpectedJobRevision: next.JobRevision, ExpectedConfigRevision: status.ConfigRevision, ExpectedAvailabilityRevision: status.AvailabilityRevision, MutationKey: "reserve-second"})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	run(next)
	return db, s, first, next
}

var _ = ginkgo.Describe("SelectedEvidenceRecovery", ginkgo.Label("integration"), func() {
	for _, version := range []string{"v1", "v2"} {
		ginkgo.It("collects only the selected Job with durable finite deadlines and restart replay on resource "+version, func() {
			var configure func(*managed.ResourceSpec)
			if version == "v2" {
				configure = task027ResourceBudgets
			}
			_, s, first, target := selectedEvidenceFixture(configure)
			original, err := s.ledger.Result(target.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			// Unrelated ownership corruption and a live worker must not block selection.
			input := filepath.Join(s.root.Path, "jobs", first.JobID, "input.json")
			gomega.Expect(os.WriteFile(input, []byte(`{}`), 0600)).To(gomega.Succeed())
			release := make(chan struct{})
			gomega.Expect(s.startJobWorker(first.JobID, func() { <-release })).To(gomega.BeTrue())
			defer func() { close(release); s.workers.Wait() }()
			_, err = s.planEvidenceRecovery(target.ResourceID, "recollect")
			gomega.Expect(err).To(gomega.HaveOccurred())
			response := recoveryHTTP(s, "/v1/resources/"+target.ResourceID+"/recover", managed.RecoveryRequest{Plan: true, Action: "recollect", JobID: target.JobID})
			gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
			var envelope struct {
				Data resourceRecoveryPlan `json:"data"`
			}
			gomega.Expect(json.Unmarshal(response.Body.Bytes(), &envelope)).To(gomega.Succeed())
			plan := envelope.Data
			token, err := s.readRecoveryToken(plan.PlanToken)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(token.TargetJobID).To(gomega.Equal(target.JobID))
			gomega.Expect(token.ExpiresAt.Sub(plan.ObservedAt)).To(gomega.BeNumerically("<", 121*time.Second))
			q := managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "selected-collect"}
			response = recoveryHTTP(s, "/v1/resources/"+target.ResourceID+"/recover", q)
			gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
			intent, receipt := task027SavedRecovery(s, target.ResourceID, q)
			gomega.Expect(intent.Jobs).To(gomega.HaveLen(1))
			gomega.Expect(intent.Jobs[0].JobID).To(gomega.Equal(target.JobID))
			collect, cleanup := 3*time.Second, 2*time.Second
			if version == "v2" {
				collect, cleanup = 90*time.Second, 90*time.Second
			}
			gomega.Expect(intent.Jobs[0].CollectDeadline).To(gomega.Equal(intent.RecordedAt.Add(collect)))
			gomega.Expect(intent.Jobs[0].CleanupDeadline).To(gomega.Equal(intent.RecordedAt.Add(collect + cleanup)))
			gomega.Expect(receipt.Complete).To(gomega.BeTrue())
			gomega.Expect(receipt.Jobs).To(gomega.HaveLen(1))
			gomega.Expect(receipt.Jobs[0].Stage).To(gomega.Equal("resolved"))
			gomega.Expect(receipt.Jobs[0].Artifacts).To(gomega.HaveLen(1))
			bytes, err := os.ReadFile(filepath.Join(s.root.Path, "jobs", target.JobID, receipt.Jobs[0].ArtifactRoot, "results/output.txt"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(string(bytes)).To(gomega.Equal("recoverable"))
			gomega.Expect(os.Stat(filepath.Join(s.root.Path, "jobs", target.JobID, "workspace"))).Error().To(gomega.MatchError(os.ErrNotExist))
			gomega.Expect(os.Stat(filepath.Join(s.root.Path, "jobs", first.JobID, "workspace/results/output.txt"))).Error().NotTo(gomega.HaveOccurred())
			snap, err := s.ledger.ResourceRecoverySnapshot(target.ResourceID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(snap.Resource.Availability).To(gomega.Equal("quarantined"))
			for _, j := range snap.Jobs {
				gomega.Expect(j.EvidenceResolved).To(gomega.Equal(j.JobID == target.JobID))
			}
			health, err := s.planResourceRecovery(target.ResourceID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(health.PlanToken).To(gomega.BeEmpty())
			current, err := s.ledger.Result(target.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current).To(gomega.Equal(original))
			fresh, err := newRuntime(s.root, s.ledger, s.store)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			// Key rotation invalidates the token, but permanent exact replay still wins.
			_, err = fresh.readRecoveryToken(q.PlanToken)
			gomega.Expect(err).To(gomega.HaveOccurred())
			response = recoveryHTTP(fresh, "/v1/resources/"+target.ResourceID+"/recover", q)
			gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
			replayIntent, replayReceipt := task027SavedRecovery(fresh, target.ResourceID, q)
			gomega.Expect(replayIntent).To(gomega.Equal(intent))
			gomega.Expect(replayReceipt).To(gomega.Equal(receipt))
			other, err := fresh.planSelectedEvidenceRecovery(target.ResourceID, "recollect", target.JobID)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(other.PlanToken).To(gomega.BeEmpty())
			q.PlanToken += "changed"
			gomega.Expect(recoveryHTTP(fresh, "/v1/resources/"+target.ResourceID+"/recover", q).Code).To(gomega.Equal(400))
		})
	}
	ginkgo.It("preserves resource occupancy and adapter fences at plan and apply, plus target liveness and ownership", func() {
		db, s, _, r := selectedEvidenceFixture(nil)
		path := "/v1/resources/" + r.ResourceID + "/recover"
		plan, err := s.planSelectedEvidenceRecovery(r.ResourceID, "recollect", r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		apply := managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "fenced-select"}
		selected := managed.RecoveryRequest{Plan: true, Action: "recollect", JobID: r.JobID}
		assertBlocked := func() {
			gomega.Expect(recoveryHTTP(s, path, selected).Code).To(gomega.Equal(409))
			gomega.Expect(recoveryHTTP(s, path, apply).Code).To(gomega.Equal(409))
		}
		_, err = db.Exec(`UPDATE managed_resources SET occupancy=1 WHERE id=?`, r.ResourceID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		assertBlocked()
		_, err = db.Exec(`UPDATE managed_resources SET occupancy=0 WHERE id=?`, r.ResourceID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		spec := vmControllerSpec(ginkgo.GinkgoT(), r.ResourceID, 1, false)
		seedControllerHistoricalPower(db, r, spec, nil, managed.PowerStartReady)
		snap, err := s.ledger.ResourceRecoverySnapshot(r.ResourceID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(snap.AdapterIntents).To(gomega.HaveLen(1))
		assertBlocked()
		_, err = db.Exec(`UPDATE managed_power_transitions SET state='reconciled'`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		release := make(chan struct{})
		gomega.Expect(s.startJobWorker(r.JobID, func() { <-release })).To(gomega.BeTrue())
		assertBlocked()
		close(release)
		s.workers.Wait()
		// A different signed target cannot reuse another Job's observation digest.
		token, err := s.readRecoveryToken(plan.PlanToken)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		token.TargetJobID = "first-retained"
		q := managed.RecoveryRequest{PlanToken: s.signRecoveryToken(token), MutationKey: "retargeted"}
		gomega.Expect(recoveryHTTP(s, path, q).Code).To(gomega.Equal(409))
		token.TargetJobID = r.JobID
		token.ExpiresAt = time.Now().Add(-time.Second)
		q.PlanToken = s.signRecoveryToken(token)
		gomega.Expect(recoveryHTTP(s, path, q).Code).To(gomega.Equal(409))
		// Unrelated changes in the full snapshot still invalidate the original token.
		_, err = db.Exec(`UPDATE managed_resources SET availability_revision=availability_revision+1 WHERE id=?`, r.ResourceID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(recoveryHTTP(s, path, apply).Code).To(gomega.Equal(409))
		gomega.Expect(os.WriteFile(filepath.Join(s.root.Path, "jobs", r.JobID, "input.json"), []byte(`{}`), 0600)).To(gomega.Succeed())
		assertBlocked()
		var count int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_evidence_recovery`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.BeZero())
	})

	ginkgo.It("rejects absent, resolved, foreign, malformed and unsupported selections", func() {
		db, s, _, r := selectedEvidenceFixture(nil)
		path := "/v1/resources/" + r.ResourceID + "/recover"
		for _, q := range []managed.RecoveryRequest{
			{Plan: true, Action: "recollect", JobID: "missing-job"},
			{Plan: true, Action: "recollect", JobID: "../bad"},
			{Plan: true, Action: "dispose", JobID: r.JobID},
			{Plan: true, JobID: r.JobID},
			{Plan: true, Action: "attest-guest-stopped", JobID: r.JobID},
			{Plan: true, Action: "attest-power-stopped", JobID: r.JobID},
			{PlanToken: "token", MutationKey: "key", JobID: r.JobID},
		} {
			gomega.Expect(recoveryHTTP(s, path, q).Code).NotTo(gomega.Equal(200))
		}
		gomega.Expect(recoveryHTTP(s, "/v1/jobs/"+r.JobID+"/recover", managed.RecoveryRequest{Plan: true, JobID: r.JobID}).Code).NotTo(gomega.Equal(200))
		spec := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "foreign-resource", Kind: "local", ExecutionMode: "managed", Capacity: 1, Revision: 1, Adapter: managed.Adapter{QualificationReference: "fixture"}, Cleanup: &managed.CleanupPolicy{PolicyReference: "owned"}}
		raw, err := json.Marshal(spec)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(s.ledger.RegisterResource(raw)).To(gomega.Succeed())
		_, err = db.Exec(`UPDATE managed_resources SET availability='quarantined' WHERE id=?`, spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// Foreign selection cannot be admitted even on a quarantined empty resource.
		_, err = s.ledger.ResourceRecoverySnapshot(spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(recoveryHTTP(s, "/v1/resources/"+spec.ID+"/recover", managed.RecoveryRequest{Plan: true, Action: "recollect", JobID: r.JobID}).Code).NotTo(gomega.Equal(200))
	})
})
