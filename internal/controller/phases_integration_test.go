//go:build integration

package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func expectPhaseTimings(result managed.Result, names ...string) {
	gomega.Expect(result.Timings).NotTo(gomega.BeNil())
	actual := []string{}
	for _, phase := range result.Timings.Phases {
		actual = append(actual, phase.Phase)
		start, err := time.Parse(time.RFC3339Nano, phase.StartedAt)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		end, err := time.Parse(time.RFC3339Nano, phase.EndedAt)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(end.Before(start)).To(gomega.BeFalse())
	}
	gomega.Expect(slices.Equal(actual, names)).To(gomega.BeTrue(), "entered phases in contract order")
	gomega.Expect(result.Timings.AdapterSteps).To(gomega.Equal([]managed.ResultStep{}))
}

var _ = ginkgo.Describe("Task010PhaseSettlement", ginkgo.Label("integration"), func() {
	ginkgo.It("settles a stopped preparation deadline after owned files and removes its workspace without claiming a run", func() {
		db, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "exit 0")
		c, err := s.registerLocal(r)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer s.unregisterLocal(c)
		workspace := filepath.Join(s.root.Path, "jobs", r.JobID, "workspace")
		c.beforeInputCopy = func() error {
			if err := os.Mkdir(workspace, 0700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(workspace, "partial-input"), []byte("owned"), 0600); err != nil {
				return err
			}
			// Inject the copy worker's deadline error, then observe its actual
			// completion. A sleeping worker would test uncertain termination.
			return context.DeadlineExceeded
		}
		gomega.Expect(s.executeLocalControlled(c, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		result, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Reason).To(gomega.Equal("prepare_deadline"))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("inconclusive")))
		gomega.Expect(result.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("complete")))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("available"))
		_, err = os.Lstat(workspace)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		phases, err := s.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["prepare"].Completed).To(gomega.BeTrue())
		gomega.Expect(phases["prepare"].Outcome).To(gomega.Equal("late"))
		gomega.Expect(phases["cleanup"].Outcome).To(gomega.Equal("complete"))
		expectPhaseTimings(result, "prepare", "collect", "cleanup", "verify")
		current, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.GlobalOccupancy).To(gomega.BeZero())
		gomega.Expect(current.ResourceOccupancy).To(gomega.BeZero())
		var runs, returns int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_runs WHERE job_id=?`, r.JobID).Scan(&runs)).To(gomega.Succeed())
		gomega.Expect(runs).To(gomega.BeZero())
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='finished'`, r.JobID).Scan(&returns)).To(gomega.Succeed())
		gomega.Expect(returns).To(gomega.Equal(1))
	})

	ginkgo.It("bounds a blocked supervisor observer and closes its later log without inventing termination", func() {
		log, err := os.CreateTemp(ginkgo.GinkgoT().TempDir(), "output-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer log.Close()
		release := make(chan struct{})
		s := &runtimeServer{}
		defer func() { close(release); s.workers.Wait() }()
		entered := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			_, returnedLog, err := observeWithinRunBudgetWithWorker(time.Now().Add(-supervisorObservationSlack+20*time.Millisecond), 0, s.startWorker, s.detached, func() (bool, *os.File, error) {
				close(entered)
				<-release
				return true, log, nil
			})
			if returnedLog != nil {
				err = errors.New("unproven observer returned a live log")
			}
			result <- err
		}()
		gomega.Eventually(entered, "2s").Should(gomega.BeClosed())
		gomega.Eventually(result, "2s").Should(gomega.Receive(gomega.MatchError(runner.ErrUnproven)))
		// Let the worker finish only after the caller has received uncertainty.
		release <- struct{}{}
		gomega.Eventually(func() error { _, err := log.Stat(); return err }, "2s").Should(gomega.MatchError(os.ErrClosed))
	})

	ginkgo.It("settles a completed preparation failure after owned files without requiring unentered workload outputs", func() {
		t := ginkgo.GinkgoT()
		db, s, r, job, object := cancellationRuntime(t, "exit 0")
		sealed, err := s.store.Read(object)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.Chmod(filepath.Join(sealed.Root, "source/input.txt"), 0600)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(sealed.Root, "source/input.txt"), []byte("corrupt"), 0600)).To(gomega.Succeed())
		gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		result, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Reason).To(gomega.Equal("prepare_failed"))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("complete")))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("available"))
		var runs int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_runs WHERE job_id=?`, r.JobID).Scan(&runs)).To(gomega.Succeed())
		gomega.Expect(runs).To(gomega.BeZero())
		phases, err := s.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["prepare"].Outcome).To(gomega.Equal("failed"))
		gomega.Expect(phases["collect"].EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("complete")))
		gomega.Expect(phases["cleanup"].Outcome).To(gomega.Equal("complete"))
		expectPhaseTimings(result, "prepare", "collect", "cleanup", "verify")
	})

	for _, fault := range []string{"collect_deadline", "cleanup_deadline", "cleanup_failure", "verify_failure"} {
		fault := fault
		ginkgo.It("preserves a native failure and owned evidence through "+fault, func() {
			t := ginkgo.GinkgoT()
			db, s, r, job, object := cancellationRuntime(t, "mkdir -p ../results; printf failed > ../results/output.txt; exit 7", func(j *managed.JobSpec) {
				j.Deadlines.CollectSeconds, j.Deadlines.CleanupSeconds = 1, 1
			})
			c, err := s.registerLocal(r)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer s.unregisterLocal(c)
			workspace := filepath.Join(s.root.Path, "jobs", r.JobID, "workspace")
			defer os.Chmod(workspace, 0700)
			c.beforePhase = func(phase string) {
				if phase == "collecting" && fault == "collect_deadline" || phase == "cleaning" && fault == "cleanup_deadline" {
					time.Sleep(1100 * time.Millisecond)
				}
				if phase == "cleaning" {
					// The exact manifest and recovery obligations already exist
					// durably before any deletion can start.
					phases, err := s.ledger.LocalPhases(r.JobID)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Expect(phases["collect"].Completed).To(gomega.BeTrue())
					if fault == "collect_deadline" {
						gomega.Expect(phases["collect"].ProtectedEvidence).NotTo(gomega.BeEmpty())
					} else {
						gomega.Expect(phases["collect"].Artifacts).To(gomega.HaveLen(1))
					}
					if fault == "cleanup_failure" {
						gomega.Expect(os.Chmod(workspace, 0000)).To(gomega.Succeed())
					}
				}
				if phase == "settling" && fault == "verify_failure" {
					// Test-only loss of the termination receipt must block return,
					// even after collection and cleanup have completed.
					_, err := db.Exec(`UPDATE managed_local_outcomes SET proof_json=NULL WHERE job_id=?`, r.JobID)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
			}
			err = s.executeLocalControlled(c, job, object, time.Now().Add(5*time.Second))
			if fault == "verify_failure" {
				gomega.Expect(err).To(gomega.MatchError(managed.ErrBlocked))
				s.failed(r.JobID, err)
				current, err := s.ledger.Snapshot(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(current.JobState).To(gomega.Equal("reconciling"))
				gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
				gomega.Expect(current.ResourceAvailability).To(gomega.Equal("quarantined"))
				return
			}
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			result, err := s.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("fail")))
			gomega.Expect(*result.NativeExitCode).To(gomega.Equal(7))
			gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
			gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("failed")))
			gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
			gomega.Expect(result.ProtectedEvidence).NotTo(gomega.BeEmpty())
			expectPhaseTimings(result, "prepare", "run", "collect", "cleanup", "verify")
			current, err := s.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current.GlobalOccupancy).To(gomega.BeZero())
			gomega.Expect(os.Chmod(workspace, 0700)).To(gomega.Succeed())
			_, err = os.Stat(filepath.Join(workspace, "results/output.txt"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if fault == "collect_deadline" {
				gomega.Expect(result.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("partial")))
				gomega.Expect(result.Reason).To(gomega.Equal("evidence_retained"))
				gomega.Expect(result.ProtectedEvidence[0].Reason).To(gomega.Equal("collection_deadline"))
			} else {
				gomega.Expect(result.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("complete")))
				if fault == "cleanup_deadline" {
					gomega.Expect(result.Reason).To(gomega.Equal("cleanup_deadline"))
				} else {
					gomega.Expect(result.Reason).To(gomega.BeEmpty())
				}
			}
		})
	}

	ginkgo.It("retains timely missing required output with its distinct evidence reason", func() {
		_, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "exit 7")
		gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		result, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Reason).To(gomega.Equal("evidence_retained"))
		gomega.Expect(result.ProtectedEvidence).To(gomega.Equal([]managed.ProtectedEvidence{{Path: "workspace", Reason: "required_output_uncollected", RecoveryReference: "job:" + r.JobID}}))
		gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("fail")))
		gomega.Expect(*result.NativeExitCode).To(gomega.Equal(7))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
	})

	ginkgo.It("retains occupancy and a foreign preexisting Job path instead of claiming preparation ownership", func() {
		t := ginkgo.GinkgoT()
		_, s, r, job, object := cancellationRuntime(t, "exit 0")
		root := filepath.Join(s.root.Path, "jobs", r.JobID)
		gomega.Expect(os.Mkdir(root, 0700)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(root, "foreign"), []byte("keep"), 0600)).To(gomega.Succeed())
		err := s.executeLocal(r, job, object, time.Now().Add(5*time.Second))
		gomega.Expect(err).To(gomega.MatchError(managed.ErrBlocked))
		s.failed(r.JobID, err)
		data, err := os.ReadFile(filepath.Join(root, "foreign"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("keep"))
		current, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
		gomega.Expect(current.ResourceAvailability).To(gomega.Equal("quarantined"))
	})

	ginkgo.It("does not start a filesystem worker after its phase deadline", func() {
		called := false
		s := &runtimeServer{}
		_, status := boundedOwnedPhase(s, time.Now().Add(-time.Second), func() error { called = true; return nil })
		gomega.Expect(status).To(gomega.Equal(phaseLate))
		gomega.Expect(called).To(gomega.BeFalse())
	})
})
