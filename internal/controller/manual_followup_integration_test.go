//go:build integration

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func manualGuestFixture(f *vmJobFixture, marker string) {
	f.job.Schema = "haepari.job/v2"
	f.job.Mode = "manual"
	f.job.Manual = &managed.Manual{ScenarioDocument: "source/input.txt", RequiredScenarios: []string{"check"}}
	f.job.Deadlines.RunSeconds = 0
	f.job.Deadlines.ManualSeconds = 30
	f.job.Deadlines.CollectSeconds = 10
	f.job.Setup = &managed.Setup{Argv: []string{"/bin/sh", "-c", "printf setup >> '" + marker + "'"}}
	f.job.Workload.Argv[2] += "; printf smoke >> '" + marker + "'"
}

func manualGuestComplete(s *runtimeServer, job managed.JobSpec, receipt managed.Receipt, status string) {
	record := managed.ManualResult{Schema: "haepari.manual-result/v1", JobID: receipt.JobID, InstallationID: s.installationID(), ProfileID: job.Environment.ProfileID, ProfileDigest: job.Environment.ProfileDigest, ExpectedRevision: receipt.JobRevision, Observer: "synthetic-manual-guest", Scenarios: []managed.ManualScenario{{ID: "check", Status: status, EvidenceReferences: []string{"fixture:check"}}}}
	w := vmMutationHTTP(s, "/v1/jobs/"+receipt.JobID+"/manual/complete", map[string]any{"result": record, "expected_revision": receipt.JobRevision})
	gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
}

var _ = ginkgo.Describe("Task019ManualPhysicalEvidence", ginkgo.Label("integration"), func() {
	for _, fault := range []string{"known-absence", "uncollected", "unproven"} {
		fault := fault
		ginkgo.It("preserves physical "+fault+" beside an unobserved human assertion", func() {
			f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; cat input.txt > ../results/output.txt")
			marker := filepath.Join(f.guest, "manual-marker")
			manualGuestFixture(f, marker)
			original := f.s.sshCommand
			var collectionFailed atomic.Bool
			var cleanups atomic.Int32
			f.s.sshCommand = func(ctx context.Context, binary string, args []string, input []byte) (utm.CommandResult, error) {
				q := vmTransportRequest(input)
				if q.Operation == "cleanup" {
					cleanups.Add(1)
				}
				if fault != "known-absence" && q.Operation == "file" && q.Path == "results/output.txt" {
					collectionFailed.Store(true)
					return utm.CommandResult{}, utm.ErrGuestUncertain
				}
				if fault == "unproven" && collectionFailed.Load() && q.Operation == "state" {
					return utm.CommandResult{}, utm.ErrGuestUncertain
				}
				return original(ctx, binary, args, input)
			}
			r, err := f.reserve(f.admit("manual-physical"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			done := make(chan error, 1)
			go func() {
				defer ginkgo.GinkgoRecover()
				done <- f.s.executeVM(r, f.job, f.object, time.Now().Add(10*time.Second))
			}()
			gomega.Eventually(func() string { r, _ := f.s.ledger.Snapshot(r.JobID); return r.JobState }, 10*time.Second, 20*time.Millisecond).Should(gomega.Equal("awaiting_manual"))
			output := filepath.Join(f.guest, r.JobID, "workspace/results/output.txt")
			gomega.Expect(os.ReadFile(output)).To(gomega.Equal([]byte("sealed")))
			if fault == "known-absence" {
				gomega.Expect(os.Remove(output)).To(gomega.Succeed())
			}
			ready, err := f.s.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			manualGuestComplete(f.s, f.job, ready, "unobserved")
			var executionErr error
			gomega.Eventually(done, 15*time.Second).Should(gomega.Receive(&executionErr))
			phases, err := f.s.ledger.LocalPhases(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(phases["collect"].EvidenceGaps).To(gomega.ContainElement(gomega.HaveField("Reason", manualObservationGapReason)))
			physicalReason := "required_output_uncollected"
			if fault == "known-absence" {
				physicalReason = "required_output_absent"
			}
			gomega.Expect(phases["collect"].EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "results/output.txt", Reason: physicalReason}))
			if fault == "unproven" {
				gomega.Expect(errors.Is(executionErr, runner.ErrUnproven)).To(gomega.BeTrue())
				f.s.failed(r.JobID, executionErr)
				current, err := f.s.ledger.Snapshot(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(current.JobState).To(gomega.Equal("reconciling"))
				gomega.Expect(current.ResourceOccupancy).To(gomega.Equal(1))
				gomega.Expect(cleanups.Load()).To(gomega.BeZero())
				gomega.Expect(os.ReadFile(output)).To(gomega.Equal([]byte("sealed")))
				_, err = f.s.ledger.Result(r.JobID)
				gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
				// Restore observation of the same owned bytes; ordinary recovery
				// completes collection without another preparation or human record.
				f.s.sshCommand = original
				fresh, err := newRuntime(f.s.root, f.s.ledger, f.s.store)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				fresh.sshCommand, fresh.utmCommand, fresh.vmLeases = original, f.s.utmCommand, f.s.vmLeases
				gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.Succeed())
				fresh.workers.Wait()
			} else {
				gomega.Expect(executionErr).NotTo(gomega.HaveOccurred())
			}
			result, err := f.s.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
			if fault == "uncollected" {
				gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("failed")))
				gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
				gomega.Expect(result.ProtectedEvidence).NotTo(gomega.BeEmpty())
				gomega.Expect(cleanups.Load()).To(gomega.BeZero())
				gomega.Expect(os.ReadFile(output)).To(gomega.Equal([]byte("sealed")))
			} else {
				gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
			}
			current, err := f.s.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current.ResourceOccupancy).To(gomega.BeZero())
			gomega.Expect(os.ReadFile(marker)).To(gomega.Equal([]byte("setupsmoke")))
			gomega.Expect(f.control.effects).To(gomega.BeEmpty())
		})
	}
})

// Fault only the observed guest response, retaining its native identity,
// protocol, transport binding and all original on-disk preparation bytes.
func manualGuestReplyFault(reply utm.CommandResult, change func(*runner.GuestReply)) (utm.CommandResult, error) {
	var envelope managed.Envelope
	if err := json.Unmarshal([]byte(reply.Stdout), &envelope); err != nil {
		return reply, err
	}
	if !envelope.OK {
		return reply, managed.ErrBlocked
	}
	if envelope.Operation == "runner.guest-transport" {
		view, err := runner.DecodeGuestTransportReply(envelope.Data)
		if err != nil {
			return reply, err
		}
		change(&view.Guest)
		envelope.Data, _ = json.Marshal(view)
	} else {
		view, err := runner.DecodeGuestReply(envelope.Data)
		if err != nil {
			return reply, err
		}
		change(&view)
		envelope.Data, _ = json.Marshal(view)
	}
	raw, err := json.Marshal(envelope)
	reply.Stdout = string(raw)
	return reply, err
}

var _ = ginkgo.Describe("Task019ManualGuestRecovery", ginkgo.Label("integration"), func() {
	ginkgo.It("blocks incomplete guest proof then resumes the sole preparation and original awaiting deadline", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; cat input.txt > ../results/output.txt")
		marker := filepath.Join(f.guest, "manual-recovery-marker")
		manualGuestFixture(f, marker)
		// Native preparation includes authenticated runner transfer and setup/smoke;
		// its allowance is independent of the original human observation window.
		f.job.Deadlines.PrepareSeconds = 30
		original := f.s.sshCommand
		var prepares, starts atomic.Int32
		f.s.sshCommand = func(ctx context.Context, binary string, args []string, input []byte) (utm.CommandResult, error) {
			switch vmTransportRequest(input).Operation {
			case "prepare":
				prepares.Add(1)
			case "start":
				starts.Add(1)
			}
			return original(ctx, binary, args, input)
		}
		r, err := f.reserve(f.admit("manual-guest-recovery"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		done := make(chan error, 1)
		prepareWindow := time.Duration(f.job.Deadlines.PrepareSeconds) * time.Second
		prepareDeadline := time.Now().Add(prepareWindow)
		go func() {
			defer ginkgo.GinkgoRecover()
			done <- f.s.executeVM(r, f.job, f.object, prepareDeadline)
		}()
		gomega.Eventually(func() string { r, _ := f.s.ledger.Snapshot(r.JobID); return r.JobState }, prepareWindow+supervisorObservationSlack, 20*time.Millisecond).Should(gomega.Equal("awaiting_manual"))
		phases, err := f.s.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["prepare"].Completed).To(gomega.BeTrue())
		gomega.Expect(phases["prepare"].Outcome).To(gomega.Equal("complete"))
		gomega.Expect(phases["prepare"].BudgetSeconds).To(gomega.Equal(int64(30)))
		gomega.Expect(phases["prepare"].Deadline).To(gomega.BeTemporally("==", prepareDeadline))
		gomega.Expect(phases["prepare"].ObservedAt.After(phases["prepare"].Deadline)).To(gomega.BeFalse())
		close(f.s.detached)
		gomega.Eventually(done, 5*time.Second).Should(gomega.Receive(gomega.MatchError(errControllerDetached)))
		f.s.workers.Wait()
		before, err := f.s.ledger.ManualSession(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		requestPath := filepath.Join(f.guest, r.JobID, "request.json")
		request, err := os.ReadFile(requestPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.s.ledger.MarkInterrupted()).To(gomega.Succeed())
		fresh, err := newRuntime(f.s.root, f.s.ledger, f.s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		observed := f.s.sshCommand
		fresh.sshCommand, fresh.utmCommand, fresh.vmLeases = observed, f.s.utmCommand, f.s.vmLeases
		for _, fault := range []string{"unobservable", "live-worker", "partial-preparation"} {
			var injected atomic.Bool
			fresh.sshCommand = func(ctx context.Context, binary string, args []string, input []byte) (utm.CommandResult, error) {
				if vmTransportRequest(input).Operation != "state" {
					return observed(ctx, binary, args, input)
				}
				if fault == "unobservable" {
					return utm.CommandResult{}, utm.ErrGuestUncertain
				}
				reply, err := observed(ctx, binary, args, input)
				if err != nil {
					return reply, err
				}
				return manualGuestReplyFault(reply, func(view *runner.GuestReply) {
					if view.Control == nil {
						return
					}
					injected.Store(true)
					if fault == "live-worker" {
						view.Control.Stopped = false
					} else {
						view.Request = nil
						view.Control.Phase = "prepare"
					}
				})
			}
			gomega.Expect(errors.Is(fresh.tryRecoverInterrupted(r.JobID, true, nil), managed.ErrBlocked)).To(gomega.BeTrue(), fault)
			if fault != "unobservable" {
				gomega.Expect(injected.Load()).To(gomega.BeTrue(), fault)
			}
			current, err := fresh.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current.JobState).To(gomega.Equal("reconciling"))
			gomega.Expect(current.ResourceOccupancy).To(gomega.Equal(1))
			gomega.Expect(os.ReadFile(marker)).To(gomega.Equal([]byte("setupsmoke")))
			gomega.Expect(os.ReadFile(requestPath)).To(gomega.Equal(request))
		}
		fresh.sshCommand = observed
		gomega.Eventually(func() error { return fresh.tryRecoverInterrupted(r.JobID, true, nil) }, 5*time.Second, 20*time.Millisecond).Should(gomega.Succeed())
		gomega.Eventually(func() string { r, _ := fresh.ledger.Snapshot(r.JobID); return r.JobState }, 5*time.Second, 20*time.Millisecond).Should(gomega.Equal("awaiting_manual"))
		after, err := fresh.ledger.ManualSession(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after.PrepareDeadline).To(gomega.Equal(before.PrepareDeadline))
		gomega.Expect(after.Deadline).To(gomega.Equal(before.Deadline))
		gomega.Expect(after.Identity).To(gomega.Equal(before.Identity))
		gomega.Expect(after.Completion).To(gomega.BeNil())
		ready, err := fresh.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		manualGuestComplete(fresh, f.job, ready, "pass")
		fresh.workers.Wait()
		result, err := fresh.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(prepares.Load()).To(gomega.Equal(int32(1)))
		gomega.Expect(starts.Load()).To(gomega.Equal(int32(1)))
		gomega.Expect(os.ReadFile(marker)).To(gomega.Equal([]byte("setupsmoke")))
		gomega.Expect(f.control.effects).To(gomega.BeEmpty())
	})
})

var _ = ginkgo.Describe("Task019ManualCommittedPreparationRecovery", ginkgo.Label("integration"), func() {
	for _, jobID := range []string{"cancel-job", strings.Repeat("j", 48), strings.Repeat("j", 95)} {
		jobID := jobID
		ginkgo.It("preserves original preparation across hold gaps for ID length "+strconv.Itoa(len(jobID)), func() {
			db, old, r, job, object := cancellationRuntimeJobKind(ginkgo.GinkgoT(), "mkdir -p ../results; cat input.txt > ../results/output.txt; printf x >> counter", "local", jobID, func(j *managed.JobSpec) {
				j.Mode = "manual"
				j.Manual = &managed.Manual{ScenarioDocument: "source/input.txt", RequiredScenarios: []string{"check"}}
				j.Deadlines.RunSeconds = 0
				j.Deadlines.ManualSeconds = 20
			})
			_, err := db.Exec(`CREATE TEMP TRIGGER manual_hold_gap BEFORE UPDATE OF state ON managed_jobs WHEN NEW.state='awaiting_manual' BEGIN SELECT RAISE(FAIL,'manual hold gap'); END`)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			deadline := time.Now().Add(4 * time.Second)
			gomega.Expect(old.executeLocal(r, job, object, deadline)).To(gomega.HaveOccurred())
			old.workers.Wait()
			phases, err := old.ledger.LocalPhases(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(phases["prepare"].Completed).To(gomega.BeTrue())
			gomega.Expect(phases["prepare"].Outcome).To(gomega.Equal("complete"))
			gomega.Expect(phases["prepare"].ObservedAt.After(phases["prepare"].Deadline)).To(gomega.BeFalse())
			before, err := old.ledger.ManualSession(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(before.Deadline.IsZero()).To(gomega.BeTrue())
			gomega.Expect(before.Preparation.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("pass")))
			if remaining := time.Until(deadline); remaining > 0 {
				<-time.After(remaining)
			}
			gomega.Expect(old.ledger.MarkInterrupted()).To(gomega.Succeed())
			if jobID != "cancel-job" {
				attempt, err := newRuntime(old.root, old.ledger, old.store)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(attempt.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.Succeed())
				gomega.Eventually(func() string { r, _ := old.ledger.Snapshot(r.JobID); return r.JobState }, 5*time.Second, 10*time.Millisecond).Should(gomega.Equal("reconciling"))
				attempt.workers.Wait()
				unheld, err := old.ledger.ManualSession(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(unheld.Deadline.IsZero()).To(gomega.BeTrue())
			}
			_, err = db.Exec(`DROP TRIGGER manual_hold_gap`)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if remaining := time.Until(deadline); remaining > 0 {
				<-time.After(remaining)
			}
			gomega.Expect(old.ledger.MarkInterrupted()).To(gomega.Succeed())
			fresh, err := newRuntime(old.root, old.ledger, old.store)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.Succeed())
			if jobID != "cancel-job" {
				// An unexpected hold failure quarantines the resource. Repeated
				// recovery can preserve preparation but must not clear quarantine
				// or open a human window without independent reconciliation.
				fresh.workers.Wait()
				blocked, err := fresh.ledger.Snapshot(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(blocked.JobState).To(gomega.Equal("reconciling"))
				gomega.Expect(blocked.ResourceAvailability).To(gomega.Equal("quarantined"))
				var prepared int
				gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='ready'`, r.JobID).Scan(&prepared)).To(gomega.Succeed())
				gomega.Expect(prepared).To(gomega.Equal(3))
				m, err := fresh.ledger.ManualSession(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(m.PrepareDeadline).To(gomega.Equal(before.PrepareDeadline))
				gomega.Expect(m.Deadline.IsZero()).To(gomega.BeTrue())
				gomega.Expect(m.Preparation).To(gomega.Equal(before.Preparation))
				gomega.Expect(os.ReadFile(filepath.Join(old.root.Path, "jobs", r.JobID, "workspace/source/counter"))).To(gomega.Equal([]byte("x")))
				return
			}
			gomega.Eventually(func() string { r, _ := fresh.ledger.Snapshot(r.JobID); return r.JobState }, 5*time.Second, 10*time.Millisecond).Should(gomega.Equal("awaiting_manual"))
			m, err := fresh.ledger.ManualSession(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(m.PrepareDeadline).To(gomega.Equal(before.PrepareDeadline))
			gomega.Expect(m.Deadline.After(deadline)).To(gomega.BeTrue())
			gomega.Expect(m.AbortReason).To(gomega.BeEmpty())
			gomega.Expect(os.ReadFile(filepath.Join(old.root.Path, "jobs", r.JobID, "workspace/source/counter"))).To(gomega.Equal([]byte("x")))
			ready, err := fresh.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			manualGuestComplete(fresh, job, ready, "pass")
			fresh.workers.Wait()
			result, err := fresh.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		})
	}
})

var _ = ginkgo.Describe("Task019ManualOrderedCancellation", ginkgo.Label("integration"), func() {
	ginkgo.It("refuses a human PASS after durable cancellation with the awaiting worker detached", func() {
		_, old, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "mkdir -p ../results; cat input.txt > ../results/output.txt", func(j *managed.JobSpec) {
			j.Mode = "manual"
			j.Manual = &managed.Manual{ScenarioDocument: "source/input.txt", RequiredScenarios: []string{"check"}}
			j.Deadlines.RunSeconds = 0
			j.Deadlines.ManualSeconds = 20
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
		current, err := old.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = old.ledger.Cancel(managed.CancelRequest{JobID: r.JobID, ExpectedRevision: current.JobRevision, MutationKey: "ordered-manual-cancel"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		current, err = old.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		record := managed.ManualResult{Schema: "haepari.manual-result/v1", JobID: r.JobID, InstallationID: old.installationID(), ProfileID: job.Environment.ProfileID, ProfileDigest: job.Environment.ProfileDigest, ExpectedRevision: current.JobRevision, Observer: "synthetic-ordered-cancel", Scenarios: []managed.ManualScenario{{ID: "check", Status: "pass", EvidenceReferences: []string{"fixture:check"}}}}
		w := vmMutationHTTP(old, "/v1/jobs/"+r.JobID+"/manual/complete", map[string]any{"result": record, "expected_revision": current.JobRevision})
		gomega.Expect(w.Code).NotTo(gomega.Equal(200))
		gomega.Expect(w.Body.String()).To(gomega.ContainSubstring("RECOVERY_BLOCKED"))
		m, err := old.ledger.ManualSession(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(m.Completion).To(gomega.BeNil())
		after, err := old.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(current))
		gomega.Expect(after.ResourceOccupancy).To(gomega.Equal(1))
		gomega.Expect(old.ledger.MarkInterrupted()).To(gomega.Succeed())
		fresh, err := newRuntime(old.root, old.ledger, old.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.Succeed())
		fresh.workers.Wait()
		result, err := fresh.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Cancelled))
		gomega.Expect(result.NativeExitCode).To(gomega.BeNil())
		gomega.Expect(result.NativeSignal).To(gomega.BeEmpty())
		m, err = fresh.ledger.ManualSession(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(m.Completion).To(gomega.BeNil())
		gomega.Expect(m.Preparation.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("pass")))
		events, err := fresh.ledger.EventsAfter(r.JobID, 0, 1000)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, event := range events.Events {
			gomega.Expect(event.Kind).NotTo(gomega.Equal("manual_completed"))
		}
		after, err = fresh.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after.ResourceOccupancy).To(gomega.BeZero())
	})
})

var _ = ginkgo.Describe("Task019ManualVMNoStartRecovery", ginkgo.Label("integration"), func() {
	ginkgo.It("atomically retires a staged manual VM deadline before settlement and restart", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; cat input.txt > ../results/output.txt")
		marker := filepath.Join(f.guest, "no-start-marker")
		manualGuestFixture(f, marker)
		db, err := sqlite.Open(f.s.root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		_, err = db.Exec(`CREATE TRIGGER manual_stop_gap BEFORE INSERT ON managed_events WHEN NEW.kind='phase_intent' AND json_extract(NEW.payload_json,'$.phase')='collect' BEGIN SELECT RAISE(FAIL,'manual stop gap'); END`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		deadline := time.Now().Add(5 * time.Second)
		original := f.s.sshCommand
		f.s.sshCommand = func(ctx context.Context, binary string, args []string, input []byte) (utm.CommandResult, error) {
			reply, err := original(ctx, binary, args, input)
			if err == nil && vmTransportRequest(input).Operation == "prepare" {
				if remaining := time.Until(deadline); remaining > 0 {
					<-time.After(remaining)
				}
			}
			return reply, err
		}
		r, err := f.reserve(f.admit("manual-stop-gap"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.s.executeVM(r, f.job, f.object, deadline)).To(gomega.HaveOccurred())
		f.s.workers.Wait()
		before, err := f.s.ledger.ManualSession(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(before.Stopped).To(gomega.BeTrue())
		gomega.Expect(before.Completion).To(gomega.BeNil())
		gomega.Expect(before.Outcome).NotTo(gomega.BeNil())
		gomega.Expect(before.Outcome.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("timed_out")))
		gomega.Expect(before.AbortReason).To(gomega.Equal("prepare_deadline"))
		gomega.Expect(before.PreparationSteps).To(gomega.HaveLen(2))
		outcome, err := f.s.ledger.LocalOutcome(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(outcome).To(gomega.Equal(*before.Outcome))
		gomega.Expect(outcome.Started).To(gomega.BeFalse())
		phases, err := f.s.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["prepare"].Outcome).To(gomega.Equal("late"))
		gomega.Expect(phases["prepare"].AdapterSteps).To(gomega.Equal(before.PreparationSteps))
		_, err = db.Exec(`DROP TRIGGER manual_stop_gap`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.s.ledger.MarkInterrupted()).To(gomega.Succeed())
		fresh, err := newRuntime(f.s.root, f.s.ledger, f.s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fresh.sshCommand, fresh.utmCommand, fresh.vmLeases = original, f.s.utmCommand, f.s.vmLeases
		gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.Succeed())
		fresh.workers.Wait()
		result, err := fresh.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("timed_out")))
		gomega.Expect(result.Reason).To(gomega.Equal("prepare_deadline"))
		gomega.Expect(result.NativeExitCode).To(gomega.BeNil())
		gomega.Expect(result.NativeSignal).To(gomega.BeEmpty())
		current, err := fresh.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.ResourceOccupancy).To(gomega.BeZero())
		_, err = os.Stat(marker)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		gomega.Expect(f.control.effects).To(gomega.BeEmpty())
	})
})

var _ = ginkgo.Describe("Task019ManualVMPreparationStepsRecovery", ginkgo.Label("integration"), func() {
	ginkgo.It("retains original adapter steps across a native exit before preparation stop commit", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; cat input.txt > ../results/output.txt")
		f.job.Deadlines.PrepareSeconds = 15
		marker := filepath.Join(f.guest, "steps-marker")
		manualGuestFixture(f, marker)
		db, err := sqlite.Open(f.s.root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		_, err = db.Exec(`CREATE TRIGGER manual_steps_gap BEFORE INSERT ON managed_events WHEN NEW.kind='manual_preparation_stopped' BEGIN SELECT RAISE(FAIL,'manual steps gap'); END`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		originalSSH := f.s.sshCommand
		var watches, cancels atomic.Int32
		f.s.sshCommand = func(ctx context.Context, binary string, args []string, input []byte) (utm.CommandResult, error) {
			switch vmTransportRequest(input).Operation {
			case "watch":
				watches.Add(1)
			case "cancel":
				cancels.Add(1)
			}
			return originalSSH(ctx, binary, args, input)
		}
		r, err := f.reserve(f.admit("manual-steps-gap"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.s.executeVM(r, f.job, f.object, time.Now().Add(15*time.Second))).To(gomega.HaveOccurred())
		f.s.workers.Wait()
		before, err := f.s.ledger.ManualSession(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(before.PreparationSteps).To(gomega.HaveLen(2))
		gomega.Expect(before.Preparation).To(gomega.BeNil())
		phases, err := f.s.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["prepare"].Completed).To(gomega.BeTrue())
		gomega.Eventually(func() bool { q, err := f.s.vmBinding(r.JobID); return err == nil && f.s.vmWorkerStopped(q) }, 5*time.Second, 20*time.Millisecond).Should(gomega.BeTrue())
		watchCount, cancelCount := watches.Load(), cancels.Load()
		if remaining := time.Until(before.PrepareDeadline); remaining > 0 {
			<-time.After(remaining)
		}
		_, err = db.Exec(`DROP TRIGGER manual_steps_gap`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.s.ledger.MarkInterrupted()).To(gomega.Succeed())
		fresh, err := newRuntime(f.s.root, f.s.ledger, f.s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fresh.sshCommand, fresh.utmCommand, fresh.vmLeases = f.s.sshCommand, f.s.utmCommand, f.s.vmLeases
		gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.Succeed())
		gomega.Eventually(func() string { r, _ := fresh.ledger.Snapshot(r.JobID); return r.JobState }, 5*time.Second, 10*time.Millisecond).Should(gomega.Equal("awaiting_manual"))
		gomega.Expect(watches.Load()).To(gomega.Equal(watchCount))
		gomega.Expect(cancels.Load()).To(gomega.Equal(cancelCount))
		fresh.cancelMu.Lock()
		recoveredSteps := append([]managed.ResultStep(nil), fresh.localRuns[r.JobID].manualSteps...)
		fresh.cancelMu.Unlock()
		gomega.Expect(recoveredSteps).To(gomega.Equal(before.PreparationSteps))
		phases, err = fresh.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["prepare"].AdapterSteps).To(gomega.Equal(before.PreparationSteps))
		ready, err := fresh.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		manualGuestComplete(fresh, f.job, ready, "pass")
		fresh.workers.Wait()
		result, err := fresh.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		gomega.Expect(result.Timings.AdapterSteps).To(gomega.HaveLen(len(before.PreparationSteps) + 2))
		gomega.Expect(result.Timings.AdapterSteps[:len(before.PreparationSteps)]).To(gomega.Equal(before.PreparationSteps))
		gomega.Expect(os.ReadFile(marker)).To(gomega.Equal([]byte("setupsmoke")))
	})
})
