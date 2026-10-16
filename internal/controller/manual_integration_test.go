//go:build integration

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

var _ = ginkgo.Describe("Task019NativeManual", ginkgo.Label("integration"), func() {
	for _, action := range []string{"pass", "fail", "unobserved", "cancel", "deadline", "setup-fail", "prepare-deadline", "cancel-before-start"} {
		action := action
		ginkgo.It("keeps native guest preparation separate from human "+action, func() {
			f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; cat input.txt > ../results/output.txt")
			marker := filepath.Join(f.guest, "counter")
			f.job.Schema = "haepari.job/v2"
			f.job.Mode = "manual"
			f.job.Manual = &managed.Manual{ScenarioDocument: "source/input.txt", RequiredScenarios: []string{"check"}}
			f.job.Deadlines.RunSeconds = 0
			f.job.Deadlines.ManualSeconds = 10
			if action == "deadline" {
				f.job.Deadlines.ManualSeconds = 1
			}
			f.job.Setup = &managed.Setup{Argv: []string{"/bin/sh", "-c", "printf setup >> '" + marker + "'"}}
			if action == "prepare-deadline" {
				f.job.Deadlines.PrepareSeconds = 3
				f.job.Setup.Argv[2] += "; exec /bin/sleep 20"
			}
			if action == "cancel-before-start" {
				original := f.s.sshCommand
				var cancelled atomic.Bool
				f.s.sshCommand = func(ctx context.Context, binary string, args []string, input []byte) (utm.CommandResult, error) {
					reply, err := original(ctx, binary, args, input)
					request := vmTransportRequest(input)
					if err == nil && request.Operation == "prepare" && request.Request.Identity.JobID == "manual-native" && !cancelled.Swap(true) {
						r, e := f.s.ledger.Snapshot("manual-native")
						if e != nil {
							return reply, e
						}
						_, e = f.s.requestCancel(managed.CancelRequest{JobID: r.JobID, ExpectedRevision: r.JobRevision, MutationKey: "cancel-before-native-start"})
						if e != nil {
							return reply, e
						}
					}
					return reply, err
				}
			}
			if action == "setup-fail" {
				f.job.Setup.Argv[2] += "; exit 7"
			}
			f.job.Workload.Argv[2] += "; printf smoke >> '" + marker + "'"
			r, err := f.reserve(f.admit("manual-native"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			done := make(chan error, 1)
			go func() {
				done <- f.s.executeVM(r, f.job, f.object, time.Now().Add(time.Duration(f.job.Deadlines.PrepareSeconds)*time.Second))
			}()
			if action != "setup-fail" && action != "prepare-deadline" && action != "cancel-before-start" {
				gomega.Eventually(func() string { r, _ := f.s.ledger.Snapshot("manual-native"); return r.JobState }, 10*time.Second, 20*time.Millisecond).Should(gomega.Equal("awaiting_manual"))
				_, err = f.s.ledger.Result("manual-native")
				gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
				r, err = f.s.ledger.Snapshot("manual-native")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(r.ResourceOccupancy).To(gomega.Equal(1))
				if action == "cancel" {
					_, err = f.s.requestCancel(managed.CancelRequest{JobID: r.JobID, ExpectedRevision: r.JobRevision, MutationKey: "manual-native-cancel"})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				} else if action != "deadline" {
					record := managed.ManualResult{Schema: "haepari.manual-result/v1", JobID: r.JobID, InstallationID: f.s.installationID(), ProfileID: f.job.Environment.ProfileID, ProfileDigest: f.job.Environment.ProfileDigest, ExpectedRevision: r.JobRevision, Observer: "synthetic-native-fixture", Scenarios: []managed.ManualScenario{{ID: "check", Status: action, EvidenceReferences: []string{"fixture:check"}}}}
					w := vmMutationHTTP(f.s, "/v1/jobs/"+r.JobID+"/manual/complete", map[string]any{"result": record, "expected_revision": r.JobRevision})
					gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
				}
			}
			var executionErr error
			gomega.Eventually(done, 15*time.Second).Should(gomega.Receive(&executionErr))
			gomega.Expect(executionErr).NotTo(gomega.HaveOccurred())
			result, err := f.s.ledger.Result("manual-native")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			expected := managed.Incomplete
			if action == "pass" {
				expected = managed.Pass
			}
			if action == "fail" {
				expected = managed.Fail
			}
			if action == "cancel" || action == "cancel-before-start" {
				expected = managed.Cancelled
			}
			gomega.Expect(result.FinalVerdict).To(gomega.Equal(expected))
			if action == "deadline" || action == "prepare-deadline" {
				gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("timed_out")))
				reason := "manual_deadline"
				if action == "prepare-deadline" {
					reason = "prepare_deadline"
				}
				gomega.Expect(result.Reason).To(gomega.Equal(reason))
				gomega.Expect(result.NativeExitCode).To(gomega.BeNil())
				gomega.Expect(result.NativeSignal).To(gomega.BeEmpty())
			}
			if action == "fail" {
				m, err := f.s.ledger.ManualSession(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(m.Preparation.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("pass")))
				gomega.Expect(m.Preparation.NativeExitCode).NotTo(gomega.BeNil())
				gomega.Expect(*m.Preparation.NativeExitCode).To(gomega.BeZero())
				gomega.Expect(m.Preparation.NativeSignal).To(gomega.BeEmpty())
				gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("fail")))
				gomega.Expect(result.NativeExitCode).To(gomega.BeNil())
				gomega.Expect(result.NativeSignal).To(gomega.BeEmpty())
			}
			if action == "cancel-before-start" {
				phases, err := f.s.ledger.LocalPhases(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(phases["prepare"].Outcome).To(gomega.Equal("failed"))
				gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("cancelled")))
				gomega.Expect(result.Reason).To(gomega.Equal("cancel_before_start"))
				gomega.Expect(result.NativeExitCode).To(gomega.BeNil())
				gomega.Expect(result.NativeSignal).To(gomega.BeEmpty())
			}
			gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
			contents, err := os.ReadFile(marker)
			if action == "cancel-before-start" {
				gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			} else {
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			if action == "cancel-before-start" {
				gomega.Expect(contents).To(gomega.BeEmpty())
			} else if action == "prepare-deadline" {
				gomega.Expect(string(contents)).To(gomega.Equal("setup"))
			} else if action == "setup-fail" {
				gomega.Expect(string(contents)).To(gomega.Equal("setup"))
				gomega.Expect(result.NativeExitCode).NotTo(gomega.BeNil())
				gomega.Expect(*result.NativeExitCode).To(gomega.Equal(7))
			} else {
				gomega.Expect(string(contents)).To(gomega.Equal("setupsmoke"))
			}
			f.job.Mode = "auto"
			f.job.Manual = nil
			f.job.Setup = nil
			f.job.Deadlines.PrepareSeconds = 10
			f.job.Deadlines.RunSeconds = 10
			f.job.Deadlines.ManualSeconds = 0
			gomega.Expect(f.run("manual-native-successor").FinalVerdict).To(gomega.Equal(managed.Pass))
			gomega.Expect(f.control.effects).To(gomega.BeEmpty())
		})
	}

	ginkgo.It("stops live manual preparation before workload when the credential reference is missing", func() {
		t := ginkgo.GinkgoT()
		missing := filepath.Join(t.TempDir(), "synthetic-live-credential")
		marker := filepath.Join(t.TempDir(), "forbidden-auth")
		_, s, r, job, object := cancellationRuntime(t, "printf workload-ran > '"+marker+"'", func(job *managed.JobSpec) {
			job.Schema = "haepari.job/v2"
			job.Mode = "manual"
			job.Manual = &managed.Manual{ScenarioDocument: "source/input.txt", RequiredScenarios: []string{"check"}}
			job.Deadlines.RunSeconds = 0
			job.Deadlines.ManualSeconds = 30
			job.Effects = managed.Effects{Mode: "live", AuthorizationReference: "synthetic-live-authorization"}
			job.Setup = &managed.Setup{Argv: []string{"/bin/sh", "-c", `test -f "$1" || exit 3; printf forbidden-auth > "$2"`, "setup", missing, marker}}
		})
		c, err := s.registerLocal(r)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer s.unregisterLocal(c)
		gomega.Expect(s.executeLocalControlled(c, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		result, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(result.NativeExitCode).NotTo(gomega.BeNil())
		gomega.Expect(*result.NativeExitCode).To(gomega.Equal(3))
		_, err = os.Stat(marker)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		_, err = os.Stat(missing)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
})

var _ = ginkgo.Describe("Task019ManualDeadlineCommitRestart", ginkgo.Label("integration"), func() {
	ginkgo.It("settles the original expired manual deadline after interruption between intent and abort commits", func() {
		_, old, receipt, job, object := cancellationRuntime(ginkgo.GinkgoT(), "mkdir -p ../results; cat input.txt > ../results/output.txt", func(job *managed.JobSpec) {
			job.Mode = "manual"
			job.Manual = &managed.Manual{ScenarioDocument: "source/input.txt", RequiredScenarios: []string{"check"}}
			job.Deadlines.RunSeconds = 0
			job.Deadlines.ManualSeconds = 1
		})
		done := make(chan error, 1)
		go func() {
			defer ginkgo.GinkgoRecover()
			done <- old.executeLocal(receipt, job, object, time.Now().Add(5*time.Second))
		}()
		gomega.Eventually(func() string { r, _ := old.ledger.Snapshot(receipt.JobID); return r.JobState }, 5*time.Second, 10*time.Millisecond).Should(gomega.Equal("awaiting_manual"))
		close(old.detached)
		gomega.Eventually(done, 5*time.Second).Should(gomega.Receive(gomega.MatchError(errControllerDetached)))
		old.workers.Wait()
		manual, err := old.ledger.ManualSession(receipt.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		if remaining := time.Until(manual.Deadline); remaining > 0 {
			<-time.After(remaining)
		}
		current, err := old.ledger.Snapshot(receipt.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// The worker is detached: expiry must refuse a new human PASS before
		// abandonment is committed, rather than relying only on its final verdict.
		completion := managed.ManualResult{Schema: "haepari.manual-result/v1", JobID: receipt.JobID, InstallationID: old.installationID(), ProfileID: job.Environment.ProfileID, ProfileDigest: job.Environment.ProfileDigest, ExpectedRevision: current.JobRevision, Observer: "synthetic-late-check", Scenarios: []managed.ManualScenario{{ID: "check", Status: "pass", EvidenceReferences: []string{"fixture:check"}}}}
		w := vmMutationHTTP(old, "/v1/jobs/"+receipt.JobID+"/manual/complete", map[string]any{"result": completion, "expected_revision": current.JobRevision})
		gomega.Expect(w.Code).NotTo(gomega.Equal(200))
		gomega.Expect(w.Body.String()).To(gomega.ContainSubstring("RECOVERY_BLOCKED"))
		gomega.Expect(w.Body.String()).To(gomega.ContainSubstring("deadline"))
		unchanged, err := old.ledger.Snapshot(receipt.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(unchanged).To(gomega.Equal(current))
		gomega.Expect(unchanged.ResourceOccupancy).To(gomega.Equal(1))
		unchangedManual, err := old.ledger.ManualSession(receipt.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(unchangedManual).To(gomega.Equal(manual))
		// The first commit is durable; the abort commit never ran in the old worker.
		_, err = old.ledger.ManualDeadline(manual.Identity, current.JobRevision, "manual_deadline")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(old.ledger.MarkInterrupted()).To(gomega.Succeed())
		resumed, err := newRuntime(old.root, old.ledger, old.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(resumed.tryRecoverInterrupted(receipt.JobID, true, nil)).To(gomega.Succeed())
		resumed.workers.Wait()
		result, err := resumed.ledger.Result(receipt.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("timed_out")))
		gomega.Expect(result.Reason).To(gomega.Equal("manual_deadline"))
		gomega.Expect(result.NativeExitCode).To(gomega.BeNil())
		gomega.Expect(result.NativeSignal).To(gomega.BeEmpty())
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		current, err = resumed.ledger.Snapshot(receipt.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.ResourceOccupancy).To(gomega.BeZero())
		events, err := resumed.ledger.EventsAfter(receipt.JobID, 0, 1000)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		count := 0
		for _, event := range events.Events {
			if event.Kind == "manual_abandonment_intent" {
				count++
			}
		}
		gomega.Expect(count).To(gomega.Equal(1))
		// Retirement remains irreversible after settlement; the expired
		// original record cannot create human completion or renew the deadline.
		w = vmMutationHTTP(resumed, "/v1/jobs/"+receipt.JobID+"/manual/complete", map[string]any{"result": completion, "expected_revision": completion.ExpectedRevision})
		gomega.Expect(w.Code).NotTo(gomega.Equal(200))
		gomega.Expect(w.Body.String()).To(gomega.ContainSubstring("RECOVERY_BLOCKED"))
		retired, err := resumed.ledger.ManualSession(receipt.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(retired.Completion).To(gomega.BeNil())
		gomega.Expect(retired.Deadline).To(gomega.Equal(manual.Deadline))
		gomega.Expect(retired.AbortReason).To(gomega.Equal("manual_deadline"))
	})
})

var _ = ginkgo.Describe("Task019ManualMissingPreparationProof", ginkgo.Label("integration"), func() {
	ginkgo.It("retains occupied reconciliation and owned evidence until the original missing receipt is restored", func() {
		_, old, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "mkdir -p ../results; cat input.txt > ../results/output.txt", func(job *managed.JobSpec) {
			job.Mode = "manual"
			job.Manual = &managed.Manual{ScenarioDocument: "source/input.txt", RequiredScenarios: []string{"check"}}
			job.Deadlines.RunSeconds = 0
			job.Deadlines.ManualSeconds = 20
		})
		done := make(chan error, 1)
		go func() {
			defer ginkgo.GinkgoRecover()
			done <- old.executeLocal(r, job, object, time.Now().Add(5*time.Second))
		}()
		gomega.Eventually(func() string { r, _ := old.ledger.Snapshot(r.JobID); return r.JobState }, 5*time.Second, 10*time.Millisecond).Should(gomega.Equal("awaiting_manual"))
		close(old.detached)
		gomega.Eventually(done, 5*time.Second).Should(gomega.Receive(gomega.MatchError(errControllerDetached)))
		old.workers.Wait()
		root := filepath.Join(old.root.Path, "jobs", r.JobID)
		view, err := runner.Inspect(filepath.Join(root, "request.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(view.Intent).NotTo(gomega.BeNil())
		owner := view.Intent.Supervisor
		gomega.Eventually(func() bool {
			process, err := platform.ObserveProcess(owner.PID)
			return err != nil || process.Zombie || process.Identity != owner
		}, 5*time.Second, 10*time.Millisecond).Should(gomega.BeTrue())
		receipt := filepath.Join(root, "exit.json")
		raw, err := os.ReadFile(receipt)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.Remove(receipt)).To(gomega.Succeed())
		gomega.Expect(old.ledger.MarkInterrupted()).To(gomega.Succeed())
		fresh, err := newRuntime(old.root, old.ledger, old.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.MatchError(managed.ErrBlocked))
		current, err := fresh.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.JobState).To(gomega.Equal("reconciling"))
		gomega.Expect(current.ResourceOccupancy).To(gomega.Equal(1))
		gomega.Expect(os.ReadFile(filepath.Join(root, "workspace/results/output.txt"))).To(gomega.Equal([]byte("sealed")))
		completion := managed.ManualResult{Schema: "haepari.manual-result/v1", JobID: r.JobID, InstallationID: fresh.installationID(), ProfileID: job.Environment.ProfileID, ProfileDigest: job.Environment.ProfileDigest, ExpectedRevision: current.JobRevision, Observer: "synthetic-missing-proof", Scenarios: []managed.ManualScenario{{ID: "check", Status: "pass", EvidenceReferences: []string{"fixture:check"}}}}
		w := vmMutationHTTP(fresh, "/v1/jobs/"+r.JobID+"/manual/complete", map[string]any{"result": completion, "expected_revision": current.JobRevision})
		gomega.Expect(w.Code).NotTo(gomega.Equal(200))
		plan, err := fresh.planJobRecovery(r.JobID, "reconcile")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.PlanToken).To(gomega.BeEmpty())
		competitor, err := fresh.ledger.AdmitSealed(mustJSONScheduling(job), job.Input.InventoryDigest, object, "missing-proof-competitor", "missing-proof-competitor")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = fresh.ledger.Reserve(managed.ReserveRequest{JobID: competitor.JobID, ExpectedJobRevision: competitor.JobRevision, ExpectedConfigRevision: current.ResourceConfigRevision, ExpectedAvailabilityRevision: current.ResourceAvailabilityRevision, MutationKey: "missing-proof-reserve"})
		gomega.Expect(err).To(gomega.MatchError(managed.ErrBusy))
		// Restore only the exact test-owned immutable receipt; recovery observes it.
		gomega.Expect(os.WriteFile(receipt, raw, 0600)).To(gomega.Succeed())
		gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.Succeed())
		gomega.Eventually(func() string { r, _ := fresh.ledger.Snapshot(r.JobID); return r.JobState }, 5*time.Second, 10*time.Millisecond).Should(gomega.Equal("awaiting_manual"))
		current, err = fresh.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		completion.ExpectedRevision = current.JobRevision
		w = vmMutationHTTP(fresh, "/v1/jobs/"+r.JobID+"/manual/complete", map[string]any{"result": completion, "expected_revision": current.JobRevision})
		gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
		fresh.workers.Wait()
		result, err := fresh.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		_, err = fresh.ledger.Cancel(managed.CancelRequest{JobID: competitor.JobID, ExpectedRevision: competitor.JobRevision, MutationKey: "missing-proof-owned-queued-cancel"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})
})

var _ = ginkgo.Describe("Task019ManualSettlementFence", ginkgo.Label("integration"), func() {
	ginkgo.It("refuses forged PASS without aligned fully observed human authority and commits only the restored record", func() {
		db, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "mkdir -p ../results; cat input.txt > ../results/output.txt", func(job *managed.JobSpec) {
			job.Mode = "manual"
			job.Manual = &managed.Manual{ScenarioDocument: "source/input.txt", RequiredScenarios: []string{"check"}}
			job.Deadlines.RunSeconds = 0
			job.Deadlines.ManualSeconds = 20
		})
		c, err := s.registerLocal(r)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		reached, release := make(chan struct{}), make(chan struct{})
		var released atomic.Bool
		unblock := func() {
			if !released.Swap(true) {
				close(release)
			}
		}
		defer unblock()
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
		gomega.Eventually(func() string { r, _ := s.ledger.Snapshot(r.JobID); return r.JobState }, 5*time.Second, 10*time.Millisecond).Should(gomega.Equal("awaiting_manual"))
		ready, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		record := managed.ManualResult{Schema: "haepari.manual-result/v1", JobID: r.JobID, InstallationID: s.installationID(), ProfileID: job.Environment.ProfileID, ProfileDigest: job.Environment.ProfileDigest, ExpectedRevision: ready.JobRevision, Observer: "synthetic-settlement-fence", Scenarios: []managed.ManualScenario{{ID: "check", Status: "pass", EvidenceReferences: []string{"fixture:check"}}}}
		w := vmMutationHTTP(s, "/v1/jobs/"+r.JobID+"/manual/complete", map[string]any{"result": record, "expected_revision": ready.JobRevision})
		gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
		gomega.Eventually(reached, 5*time.Second).Should(gomega.BeClosed())
		// Every other native/collection/cleanup/verification gate is satisfied. Only
		// human authority is varied below, so removing its fence would admit PASS.
		gomega.Expect(s.beginPhase(c, "verify", 0, time.Time{})).To(gomega.Succeed())
		gomega.Expect(s.endPhase(c, "verify", managed.LocalPhase{Outcome: "complete"})).To(gomega.Succeed())
		current, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		collect := c.phases["collect"]
		result := managed.Result{Schema: managed.ResultSchema(job), JobID: r.JobID, ResourceID: r.ResourceID, ResourceRevision: r.ResourceConfigRevision, LeaseGeneration: r.LeaseGeneration, JobState: "finished", ResourceAvailability: "available", WorkloadOutcome: "pass", EvidenceCompleteness: "complete", CleanupOutcome: "verified", FinalVerdict: managed.Pass, Artifacts: collect.Artifacts, Logs: collect.Logs}
		var original []byte
		gomega.Expect(db.QueryRow(`SELECT session_json FROM managed_manual_sessions WHERE job_id=?`, r.JobID).Scan(&original)).To(gomega.Succeed())
		for _, variant := range []string{"no-completion", "unobserved", "mismatched-outcome"} {
			var manual managed.ManualSession
			gomega.Expect(json.Unmarshal(original, &manual)).To(gomega.Succeed())
			switch variant {
			case "no-completion":
				manual.Completion = nil
			case "unobserved":
				manual.Unobserved = true
			case "mismatched-outcome":
				manual.Outcome.WorkloadOutcome = "fail"
			}
			_, err = db.Exec(`UPDATE managed_manual_sessions SET session_json=? WHERE job_id=?`, mustJSONScheduling(manual), r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = s.ledger.SettleLocal(r.JobID, current.JobRevision, result)
			gomega.Expect(err).To(gomega.MatchError(managed.ErrBlocked), variant)
			after, err := s.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(after).To(gomega.Equal(current))
		}
		_, err = db.Exec(`UPDATE managed_manual_sessions SET session_json=? WHERE job_id=?`, original, r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		unblock()
		gomega.Eventually(done, 5*time.Second).Should(gomega.Receive(gomega.BeNil()))
		actual, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(actual.FinalVerdict).To(gomega.Equal(managed.Pass))
		s.workers.Wait()
	})
})
