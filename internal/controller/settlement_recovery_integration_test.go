//go:build integration

package controller

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// A real child controller dies after publishing the durable phase intent.
// Reaping its owned handle proves the old filesystem worker cannot resume.
func restartPhaseFixture(path, jobID, phase string) int {
	root, err := state.Resolve(path)
	if err != nil {
		return 90
	}
	db, err := sqlite.Open(root.DBPath)
	if err != nil {
		return 90
	}
	defer db.Close()
	l, err := managed.NewLedger(db, 1)
	if err != nil {
		return 90
	}
	store, err := snapshot.OpenStore(root.Path)
	if err != nil {
		return 90
	}
	s, err := newRuntime(root, l, store)
	if err != nil {
		return 90
	}
	basis, err := l.RecoverySnapshot(jobID)
	if err != nil {
		return 90
	}
	object, err := l.Input(jobID)
	if err != nil {
		return 90
	}
	c, err := s.registerLocal(basis.Receipt)
	if err != nil {
		return 90
	}
	c.beforePhase = func(name string) {
		if name == phase {
			if os.WriteFile(filepath.Join(root.Path, "restart-phase-reached"), []byte(name), 0600) != nil {
				os.Exit(90)
			}
			select {}
		}
	}
	if s.executeLocalControlled(c, basis.Spec, object, time.Now().Add(5*time.Second)) != nil {
		return 90
	}
	return 91
}

var _ = ginkgo.Describe("Task011SettlementRecovery", ginkgo.Label("integration"), func() {
	for _, phase := range []string{"collecting", "cleaning"} {
		ginkgo.It("retains a live "+phase+" owner and consumes its interrupted intent after proven controller death", func() {
			t := ginkgo.GinkgoT()
			db, s, r, _, _ := cancellationRuntime(t, "mkdir -p ../results; printf done > ../results/output.txt")
			executable, err := os.Executable()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			child := exec.Command(executable, "restart-phase-fixture", s.root.Path, r.JobID, phase)
			child.Env = []string{"PATH=/usr/bin:/bin", "GORACE=atexit_sleep_ms=0"}
			gomega.Expect(child.Start()).To(gomega.Succeed())
			t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
			gomega.Eventually(func() string {
				raw, _ := os.ReadFile(filepath.Join(s.root.Path, "restart-phase-reached"))
				return string(raw)
			}, 5*time.Second).Should(gomega.Equal(phase))
			original, err := s.ledger.LocalPhases(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			name := "collect"
			if phase == "cleaning" {
				name = "cleanup"
			}
			gomega.Expect(original[name].Completed).To(gomega.BeFalse())
			gomega.Expect(original[name].ControllerOwner.PID).To(gomega.Equal(child.Process.Pid))
			gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
			fresh, err := newRuntime(s.root, s.ledger, s.store)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			before, err := fresh.ledger.RecoverySnapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			attested := recoveryObservation("fixture-stop", before, "job:"+r.JobID)
			attested.Source = "operator-attested"
			plan, err := fresh.planJobRecovery(r.JobID, "attest-stopped")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(plan.PlanToken).To(gomega.BeEmpty())
			gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, false, &attested)).To(gomega.MatchError(managed.ErrBlocked))
			gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
			still, err := fresh.ledger.RecoverySnapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(still).To(gomega.Equal(before))
			gomega.Expect(still.Receipt.GlobalOccupancy).To(gomega.Equal(1))
			gomega.Expect(child.Process.Kill()).To(gomega.Succeed())
			gomega.Expect(child.Wait()).To(gomega.HaveOccurred())
			if phase == "cleaning" {
				// Crossing the original deadline must not grant a fresh cleanup budget.
				time.Sleep(time.Until(original[name].Deadline) + 10*time.Millisecond)
			}
			gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
			gomega.Eventually(func() string { v, _ := fresh.ledger.Snapshot(r.JobID); return v.JobState }, 3*time.Second).Should(gomega.Equal("finished"))
			result, err := fresh.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
			gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
			gomega.Expect(os.Stat(filepath.Join(s.root.Path, "jobs", r.JobID, "workspace"))).Error().NotTo(gomega.HaveOccurred())
			terminal, err := fresh.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(terminal.GlobalOccupancy).To(gomega.BeZero())
			var payload []byte
			gomega.Expect(db.QueryRow(`SELECT payload_json FROM managed_events WHERE kind='phase_reconciled' AND json_extract(payload_json,'$.record.phase')=?`, name).Scan(&payload)).To(gomega.Succeed())
			var compensation managed.PhaseResolutionRequest
			gomega.Expect(json.Unmarshal(payload, &compensation)).To(gomega.Succeed())
			gomega.Expect(compensation.OriginalDigest).To(gomega.Equal(managed.RecoveryDigest(original[name])))
			gomega.Expect(compensation.Record.Deadline).To(gomega.Equal(original[name].Deadline))
			gomega.Expect(compensation.Evidence.Source).To(gomega.Equal("machine-observed"))
			if phase == "collecting" {
				gomega.Expect(result.Reason).To(gomega.Equal("evidence_retained"))
				gomega.Expect(compensation.Record.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("partial")))
				gomega.Expect(result.ProtectedEvidence).To(gomega.ContainElement(managed.ProtectedEvidence{Path: "workspace", Reason: "collection_interrupted", RecoveryReference: "job:" + r.JobID}))
			} else {
				gomega.Expect(result.Reason).To(gomega.Equal("cleanup_deadline"))
				gomega.Expect(compensation.Record.Outcome).To(gomega.Equal("late"))
				gomega.Expect(result.ProtectedEvidence).To(gomega.ContainElement(managed.ProtectedEvidence{Path: "workspace", Reason: "cleanup_interrupted", RecoveryReference: "job:" + r.JobID}))
			}
			raw, err := fresh.ledger.LocalPhases(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(raw[name]).To(gomega.Equal(original[name]))
			gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
			var finishes int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE kind='finished'`).Scan(&finishes)).To(gomega.Succeed())
			gomega.Expect(finishes).To(gomega.Equal(1))
		})
	}
	ginkgo.It("keeps an explicit attestation visible even when resuming already machine-proven stopped work", func() {
		_, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "mkdir -p ../results; printf done > ../results/output.txt")
		_, release, done := cancellationBarrier(ginkgo.GinkgoT(), s, r, job, object, "settling")
		s.detachWorkers()
		release()
		gomega.Eventually(done, 3*time.Second).Should(gomega.Receive(gomega.MatchError(errControllerDetached)))
		gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
		fresh, err := newRuntime(s.root, s.ledger, s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		plan, err := fresh.planJobRecovery(r.JobID, "attest-stopped")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.Snapshot.Stopped).To(gomega.BeTrue())
		evidence := recoveryObservation("fixture-scoped-stop", plan.Snapshot, "job:"+r.JobID)
		evidence.Source = "operator-attested"
		w := recoveryHTTP(fresh, "/v1/jobs/"+r.JobID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "attested-stopped", Evidence: &evidence})
		gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
		gomega.Eventually(func() string { snap, _ := fresh.ledger.Snapshot(r.JobID); return snap.JobState }, 3*time.Second).Should(gomega.Equal("finished"))
		result, err := fresh.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("pass")))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.Observations.OperatorAttested).To(gomega.BeTrue())
	})
	ginkgo.It("consumes durable settlement without another collection, cleanup or terminal capacity return", func() {
		db, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "mkdir -p ../results; printf done > ../results/output.txt")
		_, release, done := cancellationBarrier(ginkgo.GinkgoT(), s, r, job, object, "settling")
		phases, err := s.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["collect"].Outcome).To(gomega.Equal("complete"))
		gomega.Expect(phases["cleanup"].Outcome).To(gomega.Equal("complete"))
		s.detachWorkers()
		release()
		gomega.Eventually(done, 3*time.Second).Should(gomega.Receive(gomega.MatchError(errControllerDetached)))
		gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
		fresh, err := newRuntime(s.root, s.ledger, s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
		gomega.Eventually(func() string { snap, _ := fresh.ledger.Snapshot(r.JobID); return snap.JobState }, 3*time.Second).Should(gomega.Equal("finished"))
		result, err := fresh.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		gomega.Expect(result.Artifacts).To(gomega.Equal(phases["collect"].Artifacts))
		original, err := fresh.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(original["collect"]).To(gomega.Equal(phases["collect"]))
		gomega.Expect(original["cleanup"]).To(gomega.Equal(phases["cleanup"]))
		gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
		var finishes int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE kind='finished'`).Scan(&finishes)).To(gomega.Succeed())
		gomega.Expect(finishes).To(gomega.Equal(1))
		snap, err := fresh.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(snap.GlobalOccupancy).To(gomega.BeZero())
		// The test alone creates an idle quarantine. Resource recovery must
		// preserve the immutable Job result and permanent mutation identity.
		gomega.Expect(db.Exec(`UPDATE managed_resources SET availability='quarantined',availability_reason='fixture',availability_revision=availability_revision+1 WHERE id=?`, r.ResourceID)).Error().NotTo(gomega.HaveOccurred())
		path := "/v1/resources/" + r.ResourceID + "/recover"
		w := recoveryHTTP(fresh, path, managed.RecoveryRequest{Plan: true})
		gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
		var plan resourceRecoveryPlan
		recoveryData(w, &plan)
		gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
		q := managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "resource-health"}
		w = recoveryHTTP(fresh, path, q)
		gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
		var first managed.ResourceStatus
		recoveryData(w, &first)
		gomega.Expect(first.Availability).To(gomega.Equal("available"))
		w = recoveryHTTP(fresh, path, q)
		gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
		var replay managed.ResourceStatus
		recoveryData(w, &replay)
		gomega.Expect(replay).To(gomega.Equal(first))
		same, err := fresh.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(same).To(gomega.Equal(result))
		gomega.Expect(db.Exec(`UPDATE managed_resources SET availability='quarantined',availability_reason='fixture',availability_revision=availability_revision+1 WHERE id=?`, r.ResourceID)).Error().NotTo(gomega.HaveOccurred())
		basis, err := fresh.ledger.ResourceRecoverySnapshot(r.ResourceID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		evidence := recoveryObservation("fixture-health", basis, "resource:"+r.ResourceID)
		digest := managed.RecoveryDigest("pending-health")
		intent := managed.ResourceRecoveryIntent{Schema: "haepari.resource-recovery-intent/v1", ResourceID: r.ResourceID, SnapshotDigest: managed.RecoveryDigest(basis), ClientDigest: digest, MutationKey: "pending-health", Evidence: evidence}
		started, err := fresh.ledger.BeginResourceRecovery(intent)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(started.Availability).To(gomega.Equal("recovering"))
		fresh, err = newRuntime(s.root, s.ledger, s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
		resumed, found, err := fresh.ledger.ReplayResourceRecovery("pending-health", digest)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(found).To(gomega.BeTrue())
		gomega.Expect(resumed.Availability).To(gomega.Equal("available"))
		gomega.Expect(resumed.Mutation).To(gomega.Equal(started.Mutation))
		gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
		var observations int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_availability_receipts WHERE mutation_key='pending-health-complete'`).Scan(&observations)).To(gomega.Succeed())
		gomega.Expect(observations).To(gomega.Equal(1))
	})
})
