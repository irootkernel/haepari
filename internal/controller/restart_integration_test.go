//go:build integration

package controller

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task011Restart", ginkgo.Label("integration"), func() {
	ginkgo.It("refuses attested stop of an observed live supervisor before any import, cleanup or capacity return", func() {
		t := ginkgo.GinkgoT()
		db, s, r, job, object := cancellationRuntime(t, "printf once > once.txt; while [ ! -f ../allow ]; do /bin/sleep 0.02; done; mkdir -p ../results; printf done > ../results/output.txt")
		allow := filepath.Join(s.root.Path, "jobs", r.JobID, "workspace", "allow")
		t.Cleanup(func() { _ = os.WriteFile(allow, nil, 0600) })
		_, release, done := cancellationBarrier(t, s, r, job, object, "running")
		request := filepath.Join(s.root.Path, "jobs", r.JobID, "request.json")
		gomega.Eventually(func() bool { v, e := runner.Inspect(request); return e == nil && v.Start != nil }, 3*time.Second).Should(gomega.BeTrue())
		s.detachWorkers()
		release()
		gomega.Eventually(done, 3*time.Second).Should(gomega.Receive(gomega.MatchError(errControllerDetached)))
		gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
		fresh, err := newRuntime(s.root, s.ledger, s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		before, err := fresh.ledger.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		plan, err := fresh.planJobRecovery(r.JobID, "attest-stopped")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.PlanToken).To(gomega.BeEmpty())
		gomega.Expect(plan.Unknowns).To(gomega.ContainElement("a live execution or filesystem owner contradicts termination attestation"))
		evidence := recoveryObservation("fixture-stop", before, "job:"+r.JobID)
		evidence.Source = "operator-attested"
		// A missing start receipt is uncertainty, even while the exact native
		// supervisor can still run its workload. It cannot authorize no-start.
		startPath := filepath.Join(s.root.Path, "jobs", r.JobID, "start.json")
		start, err := os.ReadFile(startPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		t.Cleanup(func() { _ = os.WriteFile(startPath, start, 0600) })
		gomega.Expect(os.Remove(startPath)).To(gomega.Succeed())
		noStart, err := fresh.planJobRecovery(r.JobID, "attest-no-start")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(noStart.PlanToken).To(gomega.BeEmpty())
		gomega.Expect(fresh.applyPlannedJobRecovery(noStart, &evidence, "live-no-start", managed.RecoveryDigest(noStart))).To(gomega.MatchError(managed.ErrBlocked))
		gomega.Expect(os.WriteFile(startPath, start, 0600)).To(gomega.Succeed())
		gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, false, &evidence)).To(gomega.MatchError(managed.ErrBlocked))
		applied := fresh.applyPlannedJobRecovery(plan, &evidence, "live-attestation", managed.RecoveryDigest(plan))
		if errors.Is(applied, managed.ErrStale) {
			// Native receipts can change after this saved plan. Prove that the
			// observation fence rejected it before the live-owner fence, with
			// no ledger change; direct recovery above must still block the owner.
			current, err := fresh.planJobRecovery(r.JobID, plan.Action)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(managed.RecoveryDigest(current.Snapshot)).To(gomega.Equal(managed.RecoveryDigest(plan.Snapshot)))
			gomega.Expect(managed.RecoveryDigest(current.Observation)).NotTo(gomega.Equal(managed.RecoveryDigest(plan.Observation)))
		} else {
			gomega.Expect(applied).To(gomega.MatchError(managed.ErrBlocked))
		}
		after, err := fresh.ledger.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
		gomega.Expect(after.Receipt.GlobalOccupancy).To(gomega.Equal(1))
		// Already imported ownership also survives loss of native files.
		native, err := runner.Inspect(request)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// An earlier attested proof is provenance, not permission to ignore
		// newly observed live ownership when reconciliation resumes.
		priorAttested := before
		priorAttested.Stopped, priorAttested.Attestation = true, &evidence
		gomega.Expect(recoveryOwnerAlive(priorAttested, native, true)).To(gomega.BeTrue())
		intent, err := json.Marshal(native.Intent)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fresh.ledger.RecordRunnerReceipt(before.Identity, "intent", intent)).To(gomega.Succeed())
		intentPath := filepath.Join(s.root.Path, "jobs", r.JobID, "intent.json")
		t.Cleanup(func() { _ = os.WriteFile(intentPath, intent, 0600) })
		gomega.Expect(os.Remove(intentPath)).To(gomega.Succeed())
		gomega.Expect(os.Remove(startPath)).To(gomega.Succeed())
		imported, err := fresh.ledger.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		lost, err := fresh.planJobRecovery(r.JobID, "attest-no-start")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(lost.PlanToken).To(gomega.BeEmpty())
		gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, false, &evidence)).To(gomega.MatchError(managed.ErrBlocked))
		// Exercise the full public apply fence as well as direct startup recovery.
		token := fresh.signRecoveryToken(recoveryToken{fresh.installationID(), r.JobID, lost.Action, managed.RecoveryDigest(lost.Snapshot), managed.RecoveryDigest(lost.Observation), time.Now().Add(time.Minute)})
		w := recoveryHTTP(fresh, "/v1/jobs/"+r.JobID+"/recover", managed.RecoveryRequest{PlanToken: token, MutationKey: "lost-live-owner", Evidence: &evidence})
		// Apply re-observes the exact native state before the liveness fence.
		// A changed observation can reject this synthetic token as stale first.
		var refused response
		gomega.Expect(json.Unmarshal(w.Body.Bytes(), &refused)).To(gomega.Succeed())
		gomega.Expect(w.Code).To(gomega.Equal(409), w.Body.String())
		gomega.Expect(refused.OK).To(gomega.BeFalse())
		gomega.Expect(refused.Error).NotTo(gomega.BeNil())
		gomega.Expect(refused.Error.Code).To(gomega.BeElementOf("RECOVERY_BLOCKED", "STALE_REVISION"))
		unchanged, err := fresh.ledger.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(unchanged).To(gomega.Equal(imported))
		gomega.Expect(os.WriteFile(intentPath, intent, 0600)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(startPath, start, 0600)).To(gomega.Succeed())
		// Represent an earlier controller's attested stop through the ledger's
		// normal receipt API. Newly visible native liveness must still win.
		outcome := managed.LocalOutcome{Started: true, WorkloadOutcome: "inconclusive"}
		_, err = fresh.ledger.BeginJobRecovery(managed.JobRecoveryRequest{Identity: imported.Identity, SnapshotDigest: managed.RecoveryDigest(imported), MutationKey: "prior-attested-stop", Action: "stopped", NextState: "collecting", Evidence: evidence, Outcome: &outcome})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fresh.ledger.MarkInterrupted()).To(gomega.Succeed())
		prior, err := fresh.ledger.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(prior.Stopped).To(gomega.BeTrue())
		gomega.Expect(prior.Attestation).NotTo(gomega.BeNil())
		for _, action := range []string{"reconcile", "attest-stopped"} {
			plan, err := fresh.planJobRecovery(r.JobID, action)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(plan.PlanToken).To(gomega.BeEmpty())
		}
		gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.MatchError(managed.ErrBlocked))
		retained, err := fresh.ledger.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(retained).To(gomega.Equal(prior))
		gomega.Expect(os.Stat(filepath.Join(s.root.Path, "jobs", r.JobID, "workspace", "source", "once.txt"))).Error().NotTo(gomega.HaveOccurred())
		gomega.Expect(os.WriteFile(allow, nil, 0600)).To(gomega.Succeed())
		gomega.Eventually(func() bool {
			v, e := runner.Inspect(request)
			return e == nil && v.Exit != nil && v.Exit.TerminationProven
		}, 3*time.Second).Should(gomega.BeTrue())
		gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
		gomega.Eventually(func() string { v, _ := fresh.ledger.Snapshot(r.JobID); return v.JobState }, 3*time.Second).Should(gomega.Equal("finished"))
		result, err := fresh.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.Observations.OperatorAttested).To(gomega.BeTrue())
		var runs int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_runs`).Scan(&runs)).To(gomega.Succeed())
		gomega.Expect(runs).To(gomega.Equal(1))
	})
	ginkgo.It("settles interrupted ready preparation without a run claim or requiring outputs of an unentered workload", func() {
		db, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "exit 99")
		_, release, done := cancellationBarrier(ginkgo.GinkgoT(), s, r, job, object, "ready")
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
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(result.Reason).To(gomega.Equal("prepare_failed"))
		gomega.Expect(result.NativeExitCode).To(gomega.BeNil())
		var runs int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_runs`).Scan(&runs)).To(gomega.Succeed())
		gomega.Expect(runs).To(gomega.BeZero())
	})
	ginkgo.It("blocks a conflicting native observation without imports and reconciles a later stopped receipt without another start", func() {
		db, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "/bin/sleep 1; mkdir -p ../results; printf done > ../results/output.txt")
		_, release, done := cancellationBarrier(ginkgo.GinkgoT(), s, r, job, object, "running")
		request := filepath.Join(s.root.Path, "jobs", r.JobID, "request.json")
		gomega.Eventually(func() bool { v, e := runner.Inspect(request); return e == nil && v.Start != nil }, 3*time.Second, 25*time.Millisecond).Should(gomega.BeTrue())
		s.detachWorkers()
		release()
		gomega.Eventually(done, 3*time.Second).Should(gomega.Receive(gomega.MatchError(errControllerDetached)))
		gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
		fresh, err := newRuntime(s.root, s.ledger, s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		plan, err := fresh.planJobRecovery(r.JobID, "reconcile")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
		var before, after int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&before)).To(gomega.Succeed())
		intentPath := filepath.Join(s.root.Path, "jobs", r.JobID, "intent.json")
		raw, err := os.ReadFile(intentPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.GinkgoT().Cleanup(func() { _ = os.WriteFile(intentPath, raw, 0600) })
		var intent runner.Intent
		gomega.Expect(json.Unmarshal(raw, &intent)).To(gomega.Succeed())
		intent.Supervisor.StartedSeconds--
		changed, _ := json.Marshal(intent)
		gomega.Expect(os.WriteFile(intentPath, changed, 0600)).To(gomega.Succeed())
		w := recoveryHTTP(fresh, "/v1/jobs/"+r.JobID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "conflicting-observation"})
		gomega.Expect(w.Body.String()).To(gomega.ContainSubstring("STALE_REVISION"))
		// Startup has no saved token to reject: the live PID itself must not
		// authorize recovery when the recorded birth identity differs.
		live, err := platform.ObserveProcess(intent.Supervisor.PID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(live.Identity.PID).To(gomega.Equal(intent.Supervisor.PID))
		gomega.Expect(live.Identity.StartedSeconds).NotTo(gomega.Equal(intent.Supervisor.StartedSeconds))
		gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.MatchError(managed.ErrBlocked))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&after)).To(gomega.Succeed())
		gomega.Expect(after).To(gomega.Equal(before))
		gomega.Expect(os.WriteFile(intentPath, raw, 0600)).To(gomega.Succeed())
		gomega.Eventually(func() bool {
			v, e := runner.Inspect(request)
			return e == nil && v.Exit != nil && v.Exit.TerminationProven
		}, 3*time.Second, 25*time.Millisecond).Should(gomega.BeTrue())
		gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
		gomega.Eventually(func() string { v, _ := fresh.ledger.Snapshot(r.JobID); return v.JobState }, 3*time.Second).Should(gomega.Equal("finished"))
		result, err := fresh.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_runs WHERE job_id=?`, r.JobID).Scan(&after)).To(gomega.Succeed())
		gomega.Expect(after).To(gomega.Equal(1))
	})
	ginkgo.It("detaches and reattaches one native supervisor without replaying the workload", func() {
		t := ginkgo.GinkgoT()
		db, s, r, job, object := cancellationRuntime(t, "printf once >> once.txt; /bin/sleep 1; mkdir -p ../results; printf done > ../results/output.txt", func(j *managed.JobSpec) {
			j.Workload.RequiredOutputs = []string{"source/once.txt", "results/output.txt"}
		})
		_, release, done := cancellationBarrier(t, s, r, job, object, "running")
		requestPath := filepath.Join(s.root.Path, "jobs", r.JobID, "request.json")
		var native runner.Observation
		gomega.Eventually(func() bool { v, e := runner.Inspect(requestPath); native = v; return e == nil && v.Start != nil }, 3*time.Second, 25*time.Millisecond).Should(gomega.BeTrue())
		identity := native.Intent.Supervisor
		s.detachWorkers()
		release()
		gomega.Eventually(done, 3*time.Second).Should(gomega.Receive(gomega.MatchError(errControllerDetached)))
		gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
		fresh, err := newRuntime(s.root, s.ledger, s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
		live, err := platform.ObserveProcess(identity.PID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(live.Identity).To(gomega.Equal(identity))
		gomega.Eventually(func() string { snap, _ := fresh.ledger.Snapshot(r.JobID); return snap.JobState }, 5*time.Second, 25*time.Millisecond).Should(gomega.Equal("finished"))
		result, err := fresh.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("available"))
		var starts, finishes int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='run_intent'`, r.JobID).Scan(&starts)).To(gomega.Succeed())
		gomega.Expect(starts).To(gomega.Equal(1))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='finished'`, r.JobID).Scan(&finishes)).To(gomega.Succeed())
		gomega.Expect(finishes).To(gomega.Equal(1))
		data, err := os.ReadFile(filepath.Join(s.root.Path, "jobs", r.JobID, "artifacts/results/output.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("done"))
		data, err = os.ReadFile(filepath.Join(s.root.Path, "jobs", r.JobID, "artifacts/source/once.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("once"))
	})
	ginkgo.It("does not treat the current controller incarnation as a stopped unproven collector", func() {
		owner, err := platform.ObserveProcess(os.Getpid())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		p := managed.LocalPhase{ControllerOwner: &owner.Identity, Completed: true, Outcome: "unproven"}
		gomega.Expect(controllerOwnerStopped(p)).To(gomega.BeFalse())
		p.ControllerOwner = nil
		gomega.Expect(controllerOwnerStopped(p)).To(gomega.BeFalse())
		p.Completed, p.Outcome = true, "complete"
		gomega.Expect(controllerOwnerStopped(p)).To(gomega.BeTrue())
	})
	ginkgo.It("proves stopped phase ownership for a prior boot, reused PID identity and reaped owner", func() {
		owner, err := platform.ObserveProcess(os.Getpid())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		previous := owner.Identity
		previous.BootID = "previous-boot-fixture"
		p := managed.LocalPhase{ControllerOwner: &previous, Completed: true, Outcome: "unproven"}
		gomega.Expect(controllerOwnerStopped(p)).To(gomega.BeTrue())
		previous = owner.Identity
		previous.StartedSeconds--
		gomega.Expect(controllerOwnerStopped(p)).To(gomega.BeTrue())

		cmd := exec.Command("/bin/sleep", "30")
		gomega.Expect(cmd.Start()).To(gomega.Succeed())
		defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
		child, err := platform.ObserveProcess(cmd.Process.Pid)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		p.ControllerOwner = &child.Identity
		gomega.Expect(controllerOwnerStopped(p)).To(gomega.BeFalse())
		gomega.Expect(cmd.Process.Kill()).To(gomega.Succeed())
		gomega.Expect(cmd.Wait()).To(gomega.HaveOccurred())
		gomega.Expect(controllerOwnerStopped(p)).To(gomega.BeTrue())
	})
	ginkgo.It("retains an ambiguous run claim without a native intent", func() {
		_, s, r, _, _ := cancellationRuntime(ginkgo.GinkgoT(), "true")
		r, err := s.ledger.AdvanceLocal(r.JobID, r.JobRevision, "preparing", "ready", "ambiguous-ready")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = s.ledger.StartLocal(r.JobID, r.JobRevision, "ambiguous-run")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
		before, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = s.tryRecoverInterrupted(r.JobID, true, nil)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(errors.Is(err, managed.ErrNotFound)).To(gomega.BeFalse())
		after, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
		gomega.Expect(after.ResourceOccupancy).To(gomega.Equal(1))
	})
})
