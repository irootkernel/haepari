//go:build integration

package controller

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// The child publishes the durable phase and stops there. Reaping it is the
// restart boundary; the suite does not sleep to decide the outcome.
func interruptAtCleaning(t ginkgo.GinkgoTInterface, root, jobID string) {
	t.Helper()
	executable, err := os.Executable()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	child := exec.Command(executable, "restart-phase-fixture", root, jobID, "cleaning")
	child.Env = []string{"PATH=/usr/bin:/bin", "GORACE=atexit_sleep_ms=0"}
	gomega.Expect(child.Start()).To(gomega.Succeed())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	gomega.Eventually(func() string {
		raw, _ := os.ReadFile(filepath.Join(root, "restart-phase-reached"))
		return string(raw)
	}, 5*time.Second, 25*time.Millisecond).Should(gomega.Equal("cleaning"))
	gomega.Expect(child.Process.Kill()).To(gomega.Succeed())
	gomega.Expect(child.Wait()).To(gomega.HaveOccurred())
}

func resumeCleaningSettlement(t ginkgo.GinkgoTInterface, db *sql.DB, s *runtimeServer, jobID string) (*runtimeServer, managed.Result) {
	t.Helper()
	gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
	fresh, err := newRuntime(s.root, s.ledger, s.store)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
	fresh.workers.Wait()
	result, err := fresh.ledger.Result(jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	again, err := fresh.ledger.Result(jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(again).To(gomega.Equal(result))
	gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
	var finishes int
	gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='finished'`, jobID).Scan(&finishes)).To(gomega.Succeed())
	gomega.Expect(finishes).To(gomega.Equal(1))
	receipt, err := fresh.ledger.Snapshot(jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(receipt.GlobalOccupancy).To(gomega.BeZero())
	gomega.Expect(receipt.JobState).To(gomega.Equal("finished"))
	return fresh, result
}

var _ = ginkgo.Describe("Task021ResultReason", ginkgo.Label("integration"), func() {
	ginkgo.It("projects required_output_absent when restarted local settlement resumes positive absence", func() {
		t := ginkgo.GinkgoT()
		count := filepath.Join(t.TempDir(), "native-runs")
		db, s, r, _, _ := cancellationRuntime(t, `printf once > once.txt; printf x >> "$NATIVE_COUNT"`, func(job *managed.JobSpec) {
			job.Schema = "haepari.job/v2"
			job.Deadlines.CleanupSeconds = 15
			job.Workload.Environment = map[string]string{"NATIVE_COUNT": count}
		})
		interruptAtCleaning(t, s.root.Path, r.JobID)
		before, err := s.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(before["collect"].EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "results/output.txt", Reason: "required_output_absent"}))
		gomega.Expect(before["collect"].ProtectedEvidence).To(gomega.BeEmpty())
		gomega.Expect(before["cleanup"].Completed).To(gomega.BeFalse())
		identity, err := s.ledger.LocalIdentity(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fresh, result := resumeCleaningSettlement(t, db, s, r.JobID)
		gomega.Expect(result.Schema).To(gomega.Equal("haepari.result/v2"))
		gomega.Expect(result.Reason).To(gomega.Equal("required_output_absent"))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("pass")))
		gomega.Expect(result.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("partial")))
		gomega.Expect(result.EvidenceGaps).To(gomega.Equal(before["collect"].EvidenceGaps))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("available"))
		gomega.Expect(result.ProtectedEvidence).To(gomega.BeEmpty())
		after, err := fresh.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after["collect"]).To(gomega.Equal(before["collect"]))
		gomega.Expect(after["cleanup"].Deadline).To(gomega.Equal(before["cleanup"].Deadline))
		gomega.Expect(fresh.ledger.LocalIdentity(r.JobID)).To(gomega.Equal(identity))
		gomega.Expect(os.ReadFile(count)).To(gomega.Equal([]byte("x")))
		_, err = os.Lstat(filepath.Join(s.root.Path, "jobs", r.JobID, "workspace"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})

	ginkgo.It("keeps evidence_retained when restarted local settlement resumes an uncollected v1 output", func() {
		t := ginkgo.GinkgoT()
		count := filepath.Join(t.TempDir(), "native-runs")
		db, s, r, _, _ := cancellationRuntime(t, `printf once > once.txt; printf x >> "$NATIVE_COUNT"`, func(job *managed.JobSpec) {
			job.Deadlines.CleanupSeconds = 15
			job.Workload.Environment = map[string]string{"NATIVE_COUNT": count}
		})
		interruptAtCleaning(t, s.root.Path, r.JobID)
		before, err := s.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(before["collect"].EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "results/output.txt", Reason: "required_output_uncollected"}))
		gomega.Expect(before["collect"].ProtectedEvidence).NotTo(gomega.BeEmpty())
		_, result := resumeCleaningSettlement(t, db, s, r.JobID)
		gomega.Expect(result.Schema).To(gomega.Equal("haepari.result/v1"))
		gomega.Expect(result.Reason).To(gomega.Equal("evidence_retained"))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("partial")))
		gomega.Expect(result.EvidenceGaps).To(gomega.Equal(before["collect"].EvidenceGaps))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("failed")))
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
		gomega.Expect(result.ProtectedEvidence).To(gomega.Equal(before["collect"].ProtectedEvidence))
		gomega.Expect(os.ReadFile(count)).To(gomega.Equal([]byte("x")))
		gomega.Expect(os.ReadFile(filepath.Join(s.root.Path, "jobs", r.JobID, "workspace/source/once.txt"))).To(gomega.Equal([]byte("once")))
	})

	ginkgo.It("projects run_deadline when a VM workload reaches its durable run deadline", func() {
		t := ginkgo.GinkgoT()
		gate := t.TempDir()
		// The workload blocks on a private fifo after the output exists.
		// The durable run deadline stops it; this spec does not sleep.
		f := nativeVMFixture(t, `mkdir -p "$GATE" && rm -f "$GATE/hold" && mkfifo "$GATE/hold" && mkdir -p ../results && printf x > ../results/output.txt && printf started > "$GATE/started" && exec cat "$GATE/hold"`)
		f.job.Deadlines.RunSeconds = 3
		f.job.Workload.Environment = map[string]string{"GATE": gate}
		native := f.s.sshCommand
		starts := 0
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			if vmTransportRequest(input).Operation == "start" {
				starts++
			}
			return native(ctx, exe, args, input)
		}
		result := f.run("vm-run-deadline")
		gomega.Expect(os.ReadFile(filepath.Join(gate, "started"))).To(gomega.Equal([]byte("started")))
		phases, err := f.s.ledger.LocalPhases(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["run"].Deadline.IsZero()).To(gomega.BeFalse())
		gomega.Expect(time.Now().Before(phases["run"].Deadline)).To(gomega.BeFalse())
		gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("timed_out")))
		gomega.Expect(result.Reason).To(gomega.Equal("run_deadline"))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("complete")))
		gomega.Expect(result.EvidenceGaps).To(gomega.BeEmpty())
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("available"))
		gomega.Expect(result.ProtectedEvidence).To(gomega.BeEmpty())
		gomega.Expect(starts).To(gomega.Equal(1))
		receipt, err := f.s.ledger.Snapshot(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(receipt.GlobalOccupancy).To(gomega.BeZero())
		again, err := f.s.ledger.Result(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(again).To(gomega.Equal(result))
		gomega.Expect(os.ReadFile(filepath.Join(f.s.root.Path, "jobs", result.JobID, "artifacts/results/output.txt"))).To(gomega.Equal([]byte("x")))
	})

	ginkgo.It("projects required_output_absent when VM settlement records positive absence", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "exit 0")
		f.job.Schema = "haepari.job/v2"
		native := f.s.sshCommand
		starts := 0
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			if vmTransportRequest(input).Operation == "start" {
				starts++
			}
			return native(ctx, exe, args, input)
		}
		result := f.run("vm-positive-absence")
		gomega.Expect(result.Schema).To(gomega.Equal("haepari.result/v2"))
		gomega.Expect(result.Reason).To(gomega.Equal("required_output_absent"))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("pass")))
		gomega.Expect(*result.NativeExitCode).To(gomega.BeZero())
		gomega.Expect(result.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("partial")))
		gomega.Expect(result.EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "results/output.txt", Reason: "required_output_absent"}))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("available"))
		gomega.Expect(result.ProtectedEvidence).To(gomega.BeEmpty())
		gomega.Expect(starts).To(gomega.Equal(1))
		receipt, err := f.s.ledger.Snapshot(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(receipt.GlobalOccupancy).To(gomega.BeZero())
		again, err := f.s.ledger.Result(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(again).To(gomega.Equal(result))
		_, err = os.Lstat(filepath.Join(f.guest, result.JobID))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})

	ginkgo.It("keeps guest_evidence_retained when positive VM absence cleanup fails", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "exit 0")
		f.job.Schema = "haepari.job/v2"
		native := f.s.sshCommand
		starts := 0
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			q := vmTransportRequest(input)
			if q.Operation == "start" {
				starts++
			}
			if q.Operation == "cleanup" {
				return utm.CommandResult{}, errors.New("cleanup unavailable")
			}
			return native(ctx, exe, args, input)
		}
		result := f.run("vm-absence-cleanup-failed")
		gomega.Expect(result.EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "results/output.txt", Reason: "required_output_absent"}))
		gomega.Expect(result.Reason).To(gomega.Equal("guest_evidence_retained"))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("partial")))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("failed")))
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
		gomega.Expect(result.ProtectedEvidence).NotTo(gomega.BeEmpty())
		gomega.Expect(starts).To(gomega.Equal(1))
		receipt, err := f.s.ledger.Snapshot(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(receipt.GlobalOccupancy).To(gomega.BeZero())
		again, err := f.s.ledger.Result(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(again).To(gomega.Equal(result))
	})

	ginkgo.It("keeps guest_evidence_retained when a VM required output still has protected bytes", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; ln -s /etc/hosts ../results/output.txt")
		f.job.Schema = "haepari.job/v2"
		native := f.s.sshCommand
		starts := 0
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			if vmTransportRequest(input).Operation == "start" {
				starts++
			}
			return native(ctx, exe, args, input)
		}
		result := f.run("vm-protected-bytes")
		gomega.Expect(result.Reason).To(gomega.Equal("guest_evidence_retained"))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("partial")))
		gomega.Expect(result.EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "results/output.txt", Reason: "required_output_uncollected"}))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("failed")))
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
		gomega.Expect(result.ProtectedEvidence).NotTo(gomega.BeEmpty())
		gomega.Expect(starts).To(gomega.Equal(1))
		receipt, err := f.s.ledger.Snapshot(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(receipt.GlobalOccupancy).To(gomega.BeZero())
		again, err := f.s.ledger.Result(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(again).To(gomega.Equal(result))
		_, err = os.Lstat(filepath.Join(f.guest, result.JobID, "workspace/results/output.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})

	ginkgo.It("projects evidence_retained when fresh local positive absence cleanup fails", func() {
		t := ginkgo.GinkgoT()
		count := filepath.Join(t.TempDir(), "native-runs")
		db, s, r, job, object := cancellationRuntime(t, `printf once > once.txt; printf x >> "$NATIVE_COUNT"`, func(job *managed.JobSpec) {
			job.Schema = "haepari.job/v2"
			job.Deadlines.CleanupSeconds = 15
			job.Workload.Environment = map[string]string{"NATIVE_COUNT": count}
		})
		_, release, done := cancellationBarrier(t, s, r, job, object, "cleaning")
		before, err := s.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(before["collect"].EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "results/output.txt", Reason: "required_output_absent"}))
		gomega.Expect(before["collect"].ProtectedEvidence).To(gomega.BeEmpty())
		gomega.Expect(before["cleanup"].Completed).To(gomega.BeFalse())
		root := filepath.Join(s.root.Path, "jobs", r.JobID)
		workspace, saved := filepath.Join(root, "workspace"), filepath.Join(root, "saved")
		gomega.Expect(os.Rename(workspace, saved)).To(gomega.Succeed())
		release()
		awaitCancellation(t, done)
		result, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Reason).To(gomega.Equal("evidence_retained"))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("partial")))
		gomega.Expect(result.EvidenceGaps).To(gomega.Equal(before["collect"].EvidenceGaps))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("failed")))
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
		gomega.Expect(result.ProtectedEvidence).To(gomega.ContainElement(managed.ProtectedEvidence{Path: "workspace", Reason: "cleanup_incomplete", RecoveryReference: "job:" + r.JobID}))
		gomega.Expect(os.ReadFile(count)).To(gomega.Equal([]byte("x")))
		gomega.Expect(os.ReadFile(filepath.Join(saved, "source/once.txt"))).To(gomega.Equal([]byte("once")))
		receipt, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(receipt.GlobalOccupancy).To(gomega.BeZero())
		gomega.Expect(receipt.JobState).To(gomega.Equal("finished"))
		again, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(again).To(gomega.Equal(result))
		var finishes int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='finished'`, r.JobID).Scan(&finishes)).To(gomega.Succeed())
		gomega.Expect(finishes).To(gomega.Equal(1))
	})

	ginkgo.It("keeps an empty reason when resumed manual settlement has only an unobserved human gap", func() {
		t := ginkgo.GinkgoT()
		count := filepath.Join(t.TempDir(), "native-runs")
		db, s, r, job, object := cancellationRuntime(t, `mkdir -p ../results; cat input.txt > ../results/output.txt; printf x >> "$NATIVE_COUNT"`, func(job *managed.JobSpec) {
			job.Mode = "manual"
			job.Manual = &managed.Manual{ScenarioDocument: "source/input.txt", RequiredScenarios: []string{"check"}}
			job.Deadlines.RunSeconds = 0
			job.Deadlines.ManualSeconds = 20
			job.Workload.Environment = map[string]string{"NATIVE_COUNT": count}
		})
		reached := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		t.Cleanup(unblock)
		c, err := s.registerLocal(r)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		c.beforePhase = func(phase string) {
			if phase == "settling" {
				close(reached)
				<-release
			}
		}
		done := make(chan error, 1)
		go func() {
			defer ginkgo.GinkgoRecover()
			defer s.unregisterLocal(c)
			done <- s.executeLocalControlled(c, job, object, time.Now().Add(5*time.Second))
		}()
		gomega.Eventually(func() string { snap, _ := s.ledger.Snapshot(r.JobID); return snap.JobState }, 5*time.Second, 10*time.Millisecond).Should(gomega.Equal("awaiting_manual"))
		ready, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		record := managed.ManualResult{Schema: "haepari.manual-result/v1", JobID: r.JobID, InstallationID: s.installationID(), ProfileID: job.Environment.ProfileID, ProfileDigest: job.Environment.ProfileDigest, ExpectedRevision: ready.JobRevision, Observer: "synthetic-unobserved-restart", Scenarios: []managed.ManualScenario{{ID: "check", Status: "unobserved", EvidenceReferences: []string{"fixture:check"}}}}
		w := vmMutationHTTP(s, "/v1/jobs/"+r.JobID+"/manual/complete", map[string]any{"result": record, "expected_revision": ready.JobRevision})
		gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
		gomega.Eventually(reached, 5*time.Second).Should(gomega.BeClosed())
		before, err := s.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(before["collect"].EvidenceGaps).To(gomega.Equal([]managed.EvidenceGap{{Path: job.Manual.ScenarioDocument, Reason: manualObservationGapReason}}))
		gomega.Expect(before["collect"].ProtectedEvidence).To(gomega.BeEmpty())
		gomega.Expect(before["cleanup"].Completed).To(gomega.BeTrue())
		gomega.Expect(before["cleanup"].Outcome).To(gomega.Equal("complete"))
		identity, err := s.ledger.LocalIdentity(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		s.detachWorkers()
		unblock()
		gomega.Eventually(done, 5*time.Second).Should(gomega.Receive(gomega.MatchError(errControllerDetached)))
		gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
		fresh, err := newRuntime(s.root, s.ledger, s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
		fresh.workers.Wait()
		result, err := fresh.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Reason).To(gomega.BeEmpty())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("inconclusive")))
		gomega.Expect(result.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("partial")))
		gomega.Expect(result.EvidenceGaps).To(gomega.Equal(before["collect"].EvidenceGaps))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(result.ProtectedEvidence).To(gomega.BeEmpty())
		after, err := fresh.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after["collect"]).To(gomega.Equal(before["collect"]))
		gomega.Expect(after["cleanup"].Deadline).To(gomega.Equal(before["cleanup"].Deadline))
		gomega.Expect(fresh.ledger.LocalIdentity(r.JobID)).To(gomega.Equal(identity))
		gomega.Expect(os.ReadFile(count)).To(gomega.Equal([]byte("x")))
		receipt, err := fresh.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(receipt.GlobalOccupancy).To(gomega.BeZero())
		again, err := fresh.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(again).To(gomega.Equal(result))
		gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
		var finishes int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='finished'`, r.JobID).Scan(&finishes)).To(gomega.Succeed())
		gomega.Expect(finishes).To(gomega.Equal(1))
	})

	ginkgo.It("keeps required_output_absent when resumed manual preparation deadline has positive absence", func() {
		t := ginkgo.GinkgoT()
		db, s, r, job, object := cancellationRuntime(t, "sleep 5", func(job *managed.JobSpec) {
			job.Schema = "haepari.job/v2"
			job.Mode = "manual"
			job.Manual = &managed.Manual{ScenarioDocument: "source/input.txt", RequiredScenarios: []string{"check"}}
			job.Deadlines.RunSeconds = 0
			job.Deadlines.ManualSeconds = 30
			job.Deadlines.PrepareSeconds = 1
			job.Deadlines.CleanupSeconds = 15
		})
		reached := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		t.Cleanup(unblock)
		c, err := s.registerLocal(r)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		c.beforePhase = func(phase string) {
			if phase == "settling" {
				close(reached)
				<-release
			}
		}
		done := make(chan error, 1)
		go func() {
			defer ginkgo.GinkgoRecover()
			defer s.unregisterLocal(c)
			err := s.executeLocalControlled(c, job, object, time.Now().Add(time.Second))
			if err != nil {
				s.failed(r.JobID, err)
			}
			done <- err
		}()
		select {
		case <-reached:
		case err := <-done:
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		case <-time.After(8 * time.Second):
			ginkgo.Fail("preparation ended neither at settling nor with a phase error")
		}
		before, err := s.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(before["collect"].EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "results/output.txt", Reason: "required_output_absent"}))
		gomega.Expect(before["cleanup"].Outcome).To(gomega.Equal("complete"))
		s.detachWorkers()
		unblock()
		gomega.Eventually(done, 5*time.Second).Should(gomega.Receive(gomega.HaveOccurred()))
		gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
		fresh, err := newRuntime(s.root, s.ledger, s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
		fresh.workers.Wait()
		result, err := fresh.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("timed_out")))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(result.Reason).To(gomega.Equal("required_output_absent"))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		events, err := fresh.ledger.EventsAfter(r.JobID, 0, 1000)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		aborted := 0
		for _, event := range events.Events {
			if event.Kind == "manual_aborted" {
				aborted++
				gomega.Expect(string(event.Payload)).To(gomega.ContainSubstring("prepare_deadline"))
			}
		}
		gomega.Expect(aborted).To(gomega.Equal(1))
		gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
		var finishes int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='finished'`, r.JobID).Scan(&finishes)).To(gomega.Succeed())
		gomega.Expect(finishes).To(gomega.Equal(1))
	})
})
