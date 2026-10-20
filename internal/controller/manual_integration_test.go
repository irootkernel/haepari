//go:build integration

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// manualObserverSlack is a named finite test-only observation allowance. It
// never grants new phase authority: every deadline below stays the admitted
// immutable one, and this slack only bounds how long a test observer waits
// for an already-decided outcome, covering scheduling jitter and the native
// five-second worker-stop probe.
const manualObserverSlack = 10 * time.Second

// manualNativeSettlementBudgets admits the native manual family's generous
// physical settlement budgets before publication. The retired incidental
// five-second fixture budgets turned an ordinary slow native exchange into a
// legitimate phase expiry before settlement, defeating the scenario's
// purpose; product clocks and the deliberate short deadlines stay unchanged.
func manualNativeSettlementBudgets(job *managed.JobSpec) {
	job.Deadlines.PrepareSeconds = 30
	job.Deadlines.CollectSeconds = 30
	job.Deadlines.CleanupSeconds = 10
}

// manualNativeDeadlineLaunch starts one native manual Job whose human
// deadline is already expiring, applying the family settlement budgets and
// any explicit reconfiguration before admission.
func manualNativeDeadlineLaunch(f *vmJobFixture, id, marker string, configure func(*managed.JobSpec)) (managed.Receipt, chan error, time.Time) {
	f.job.Schema = "haepari.job/v2"
	f.job.Mode = "manual"
	f.job.Manual = &managed.Manual{ScenarioDocument: "source/input.txt", RequiredScenarios: []string{"check"}}
	f.job.Deadlines.RunSeconds = 0
	f.job.Deadlines.ManualSeconds = 1
	manualNativeSettlementBudgets(&f.job)
	if configure != nil {
		configure(&f.job)
	}
	f.job.Setup = &managed.Setup{Argv: []string{"/bin/sh", "-c", "printf setup >> '" + marker + "'"}}
	f.job.Workload.Argv[2] += "; printf smoke >> '" + marker + "'"
	r, err := f.reserve(f.admit(id))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	prepareDeadline := time.Now().Add(time.Duration(f.job.Deadlines.PrepareSeconds) * time.Second)
	done := make(chan error, 1)
	go func() {
		defer ginkgo.GinkgoRecover()
		done <- f.s.executeVM(r, f.job, f.object, prepareDeadline)
	}()
	return r, done, prepareDeadline
}

// manualNativeAwaitHumanStage observes awaiting_manual within the remaining
// original prepare deadline and captures the original saved human deadline.
func manualNativeAwaitHumanStage(f *vmJobFixture, r managed.Receipt, prepareDeadline time.Time) time.Time {
	gomega.Eventually(func() string { s, _ := f.s.ledger.Snapshot(r.JobID); return s.JobState }, time.Until(prepareDeadline)+manualObserverSlack, 20*time.Millisecond).Should(gomega.Equal("awaiting_manual"))
	_, err := f.s.ledger.Result(r.JobID)
	gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
	session, err := f.s.ledger.ManualSession(r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(session.Deadline).NotTo(gomega.BeZero())
	return session.Deadline
}

// manualNativeSettleWait receives the settlement result within the captured
// original human deadline plus the admitted physical budgets and one named
// observation allowance. Test observation only: no new human or run budget.
func manualNativeSettleWait(f *vmJobFixture, done chan error, humanDeadline time.Time) error {
	horizon := time.Until(humanDeadline) + time.Duration(f.job.Deadlines.CollectSeconds+f.job.Deadlines.CleanupSeconds)*time.Second + manualObserverSlack
	var executionErr error
	gomega.Eventually(done, horizon).Should(gomega.Receive(&executionErr))
	return executionErr
}

// manualNativeFailureContext attaches the durable phase and manual-session
// state to an unexpected execution failure; reporting evidence, never an
// acceptance relaxation.
func manualNativeFailureContext(f *vmJobFixture, jobID string, err error) error {
	if err == nil {
		return nil
	}
	phases, phaseErr := f.s.ledger.LocalPhases(jobID)
	session, sessionErr := f.s.ledger.ManualSession(jobID)
	return fmt.Errorf("%w; settlement context: phases=%+v (%v), manual=%+v (%v)", err, phases, phaseErr, session, sessionErr)
}

type manualNativePreparationWitness struct {
	session       managed.ManualSession
	phase         managed.LocalPhase
	request, exit []byte
}

func manualNativeCapturePreparation(f *vmJobFixture, jobID string, prepareDeadline time.Time, native bool) manualNativePreparationWitness {
	session, err := f.s.ledger.ManualSession(jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	phases, err := f.s.ledger.LocalPhases(jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	phase := phases["prepare"]
	gomega.Expect(phase.Completed).To(gomega.BeTrue())
	gomega.Expect(phase.Outcome).To(gomega.Equal("complete"))
	gomega.Expect(phase.Identity).To(gomega.Equal(session.Identity))
	gomega.Expect(phase.Deadline).To(gomega.BeTemporally("==", prepareDeadline))
	gomega.Expect(session.PrepareDeadline).To(gomega.BeTemporally("==", prepareDeadline))
	gomega.Expect(session.Stopped).To(gomega.BeTrue())
	gomega.Expect(session.Preparation).NotTo(gomega.BeNil())
	gomega.Expect(session.Preparation.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("pass")))
	w := manualNativePreparationWitness{session: session, phase: phase}
	if native {
		requestPath := filepath.Join(f.guest, jobID, "request.json")
		request, err := runner.ReadRequest(requestPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(request.Identity).To(gomega.Equal(session.Identity))
		gomega.Expect(request.Deadline).To(gomega.BeTemporally("==", prepareDeadline))
		view, err := runner.Inspect(requestPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(view.Exit).NotTo(gomega.BeNil())
		gomega.Expect(view.Exit.TerminationProven).To(gomega.BeTrue())
		gomega.Expect(view.Exit.Outcome).To(gomega.Equal(*session.Preparation))
		w.request, err = os.ReadFile(requestPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		w.exit, err = os.ReadFile(filepath.Join(f.guest, jobID, "exit.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
	return w
}

func (w manualNativePreparationWitness) expectPreserved(f *vmJobFixture, jobID string, retained bool) {
	session, err := f.s.ledger.ManualSession(jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(session.Identity).To(gomega.Equal(w.session.Identity))
	gomega.Expect(session.PrepareDeadline).To(gomega.Equal(w.session.PrepareDeadline))
	gomega.Expect(session.Deadline).To(gomega.Equal(w.session.Deadline))
	gomega.Expect(session.Preparation).To(gomega.Equal(w.session.Preparation))
	phases, err := f.s.ledger.LocalPhases(jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(phases["prepare"]).To(gomega.Equal(w.phase))
	if retained {
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, jobID, "request.json"))).To(gomega.Equal(w.request))
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, jobID, "exit.json"))).To(gomega.Equal(w.exit))
	}
}

func manualNativeExpectOneAbandonment(f *vmJobFixture, jobID string) {
	events, err := f.s.ledger.EventsAfter(jobID, 0, 1000)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	count := 0
	for _, event := range events.Events {
		if event.Kind == "manual_abandonment_intent" {
			count++
		}
	}
	gomega.Expect(count).To(gomega.Equal(1))
}

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
			// Preparation/human separation must still reach real physical
			// settlement, so the family admits generous native budgets before
			// publication. Product clocks and the deliberate short deadlines
			// below stay unchanged; only these fixture budgets widen.
			manualNativeSettlementBudgets(&f.job)
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
			// One original prepare deadline, captured before launch and used
			// by every observer below; the execution path never re-derives it.
			prepareDeadline := time.Now().Add(time.Duration(f.job.Deadlines.PrepareSeconds) * time.Second)
			done := make(chan error, 1)
			go func() {
				defer ginkgo.GinkgoRecover()
				done <- f.s.executeVM(r, f.job, f.object, prepareDeadline)
			}()
			var humanDeadline time.Time
			var preparation manualNativePreparationWitness
			if action != "setup-fail" && action != "prepare-deadline" && action != "cancel-before-start" {
				gomega.Eventually(func() string { r, _ := f.s.ledger.Snapshot("manual-native"); return r.JobState }, time.Until(prepareDeadline)+manualObserverSlack, 20*time.Millisecond).Should(gomega.Equal("awaiting_manual"))
				_, err = f.s.ledger.Result("manual-native")
				gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
				r, err = f.s.ledger.Snapshot("manual-native")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(r.ResourceOccupancy).To(gomega.Equal(1))
				session, err := f.s.ledger.ManualSession("manual-native")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				humanDeadline = session.Deadline
				preparation = manualNativeCapturePreparation(f, r.JobID, prepareDeadline, false)
				gomega.Expect(humanDeadline).NotTo(gomega.BeZero())
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
			// Test observation only, never new phase authority: pre-human
			// abort paths still owe collection, cleanup and native termination
			// after the original prepare deadline, while the human stage
			// settles within its captured original deadline plus the
			// admitted physical budgets.
			horizon := time.Until(prepareDeadline) + time.Duration(f.job.Deadlines.CollectSeconds+2*f.job.Deadlines.CleanupSeconds)*time.Second + manualObserverSlack
			if !humanDeadline.IsZero() {
				horizon = time.Until(humanDeadline) + time.Duration(f.job.Deadlines.CollectSeconds+f.job.Deadlines.CleanupSeconds)*time.Second + manualObserverSlack
			}
			gomega.Eventually(done, horizon).Should(gomega.Receive(&executionErr))
			executionErr = manualNativeFailureContext(f, "manual-native", executionErr)
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
			phases, err := f.s.ledger.LocalPhases("manual-native")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(phases["collect"].BudgetSeconds).To(gomega.Equal(f.job.Deadlines.CollectSeconds))
			gomega.Expect(phases["cleanup"].BudgetSeconds).To(gomega.Equal(f.job.Deadlines.CleanupSeconds))
			if !humanDeadline.IsZero() {
				settled, err := f.s.ledger.ManualSession("manual-native")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(settled.Deadline).To(gomega.Equal(humanDeadline))
				preparation.expectPreserved(f, r.JobID, false)
			}
			if action == "deadline" {
				manualNativeExpectOneAbandonment(f, r.JobID)
			}
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

	ginkgo.It("settles the expired human deadline when a native collection exchange outlasts the retired fixture budget", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; cat input.txt > ../results/output.txt")
		marker := filepath.Join(f.guest, "counter")
		const jobID = "manual-native-collect-delay"
		original := f.s.sshCommand
		var seamMu sync.Mutex
		collectDeadlines := []time.Time{}
		f.s.sshCommand = func(ctx context.Context, binary string, args []string, input []byte) (utm.CommandResult, error) {
			q := vmTransportRequest(input)
			if q.Request.Identity.JobID == jobID && q.Operation == "file" {
				// One original exchange slower than the retired incidental
				// five-second fixture budget, still inside the admitted one.
				time.Sleep(5500 * time.Millisecond)
				seamMu.Lock()
				collectDeadlines = append(collectDeadlines, q.Deadline)
				seamMu.Unlock()
			}
			return original(ctx, binary, args, input)
		}
		r, done, prepareDeadline := manualNativeDeadlineLaunch(f, jobID, marker, nil)
		humanDeadline := manualNativeAwaitHumanStage(f, r, prepareDeadline)
		preparation := manualNativeCapturePreparation(f, jobID, prepareDeadline, true)
		gomega.Expect(manualNativeFailureContext(f, jobID, manualNativeSettleWait(f, done, humanDeadline))).NotTo(gomega.HaveOccurred())
		result, err := f.s.ledger.Result(jobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("timed_out")))
		gomega.Expect(result.Reason).To(gomega.Equal("manual_deadline"))
		gomega.Expect(result.NativeExitCode).To(gomega.BeNil())
		gomega.Expect(result.NativeSignal).To(gomega.BeEmpty())
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(result.Artifacts).To(gomega.HaveLen(1))
		data, err := os.ReadFile(filepath.Join(f.s.root.Path, "jobs", jobID, "artifacts/results/output.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("sealed"))
		gomega.Expect(result.Logs).To(gomega.HaveLen(1))
		phases, err := f.s.ledger.LocalPhases(jobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["prepare"].Outcome).To(gomega.Equal("complete"))
		// The only failed evidence is the unobserved human record; physical
		// collection stays complete on its original admitted deadline.
		gomega.Expect(phases["collect"].Outcome).To(gomega.Equal("failed"))
		gomega.Expect(phases["collect"].EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("partial")))
		gomega.Expect(phases["collect"].EvidenceGaps).To(gomega.HaveLen(1))
		gomega.Expect(phases["collect"].EvidenceGaps[0].Reason).To(gomega.Equal(manualObservationGapReason))
		gomega.Expect(phases["cleanup"].Outcome).To(gomega.Equal("complete"))
		gomega.Expect(phases["verify"].Outcome).To(gomega.Equal("complete"))
		seamMu.Lock()
		observed := append([]time.Time(nil), collectDeadlines...)
		seamMu.Unlock()
		gomega.Expect(observed).To(gomega.HaveLen(2))
		for _, deadline := range observed {
			gomega.Expect(deadline).To(gomega.BeTemporally("==", phases["collect"].Deadline))
		}
		session, err := f.s.ledger.ManualSession(jobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(session.Deadline).To(gomega.Equal(humanDeadline))
		gomega.Expect(session.AbortReason).To(gomega.Equal("manual_deadline"))
		preparation.expectPreserved(f, jobID, false)
		manualNativeExpectOneAbandonment(f, jobID)
		gomega.Expect(session.Preparation.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("pass")))
		gomega.Expect(session.Preparation.NativeExitCode).NotTo(gomega.BeNil())
		gomega.Expect(*session.Preparation.NativeExitCode).To(gomega.BeZero())
		current, err := f.s.ledger.Snapshot(jobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.ResourceOccupancy).To(gomega.BeZero())
		contents, err := os.ReadFile(marker)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(contents)).To(gomega.Equal("setupsmoke"))
		f.job.Mode = "auto"
		f.job.Manual = nil
		f.job.Setup = nil
		f.job.Deadlines.PrepareSeconds = 10
		f.job.Deadlines.RunSeconds = 10
		f.job.Deadlines.ManualSeconds = 0
		gomega.Expect(f.run(jobID + "-successor").FinalVerdict).To(gomega.Equal(managed.Pass))
		gomega.Expect(f.control.effects).To(gomega.BeEmpty())
	})

	ginkgo.It("settles the expired human deadline when a native cleanup exchange outlasts the retired fixture budget", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; cat input.txt > ../results/output.txt")
		marker := filepath.Join(f.guest, "counter")
		const jobID = "manual-native-cleanup-delay"
		original := f.s.sshCommand
		var seamMu sync.Mutex
		cleanupDeadlines := []time.Time{}
		f.s.sshCommand = func(ctx context.Context, binary string, args []string, input []byte) (utm.CommandResult, error) {
			q := vmTransportRequest(input)
			if q.Request.Identity.JobID == jobID && q.Operation == "cleanup" {
				// One original exchange slower than the retired incidental
				// five-second fixture budget, still inside the admitted one.
				time.Sleep(5500 * time.Millisecond)
				seamMu.Lock()
				cleanupDeadlines = append(cleanupDeadlines, q.Deadline)
				seamMu.Unlock()
			}
			return original(ctx, binary, args, input)
		}
		r, done, prepareDeadline := manualNativeDeadlineLaunch(f, jobID, marker, nil)
		humanDeadline := manualNativeAwaitHumanStage(f, r, prepareDeadline)
		preparation := manualNativeCapturePreparation(f, jobID, prepareDeadline, true)
		gomega.Expect(manualNativeFailureContext(f, jobID, manualNativeSettleWait(f, done, humanDeadline))).NotTo(gomega.HaveOccurred())
		result, err := f.s.ledger.Result(jobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("timed_out")))
		gomega.Expect(result.Reason).To(gomega.Equal("manual_deadline"))
		gomega.Expect(result.NativeExitCode).To(gomega.BeNil())
		gomega.Expect(result.NativeSignal).To(gomega.BeEmpty())
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(result.Artifacts).To(gomega.HaveLen(1))
		data, err := os.ReadFile(filepath.Join(f.s.root.Path, "jobs", jobID, "artifacts/results/output.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("sealed"))
		gomega.Expect(result.Logs).To(gomega.HaveLen(1))
		phases, err := f.s.ledger.LocalPhases(jobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["prepare"].Outcome).To(gomega.Equal("complete"))
		gomega.Expect(phases["collect"].Outcome).To(gomega.Equal("failed"))
		gomega.Expect(phases["collect"].EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("partial")))
		gomega.Expect(phases["collect"].EvidenceGaps).To(gomega.HaveLen(1))
		gomega.Expect(phases["collect"].EvidenceGaps[0].Reason).To(gomega.Equal(manualObservationGapReason))
		gomega.Expect(phases["cleanup"].Outcome).To(gomega.Equal("complete"))
		gomega.Expect(phases["verify"].Outcome).To(gomega.Equal("complete"))
		seamMu.Lock()
		observed := append([]time.Time(nil), cleanupDeadlines...)
		seamMu.Unlock()
		gomega.Expect(observed).To(gomega.HaveLen(1))
		gomega.Expect(observed[0]).To(gomega.BeTemporally("==", phases["cleanup"].Deadline))
		session, err := f.s.ledger.ManualSession(jobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(session.Deadline).To(gomega.Equal(humanDeadline))
		gomega.Expect(session.AbortReason).To(gomega.Equal("manual_deadline"))
		preparation.expectPreserved(f, jobID, false)
		manualNativeExpectOneAbandonment(f, jobID)
		gomega.Expect(session.Preparation.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("pass")))
		current, err := f.s.ledger.Snapshot(jobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.ResourceOccupancy).To(gomega.BeZero())
		contents, err := os.ReadFile(marker)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(contents)).To(gomega.Equal("setupsmoke"))
		f.job.Mode = "auto"
		f.job.Manual = nil
		f.job.Setup = nil
		f.job.Deadlines.PrepareSeconds = 10
		f.job.Deadlines.RunSeconds = 10
		f.job.Deadlines.ManualSeconds = 0
		gomega.Expect(f.run(jobID + "-successor").FinalVerdict).To(gomega.Equal(managed.Pass))
		gomega.Expect(f.control.effects).To(gomega.BeEmpty())
	})

	ginkgo.It("retains an unproven original collection without cleanup authority after its admitted budget expires", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; cat input.txt > ../results/output.txt")
		marker := filepath.Join(f.guest, "counter")
		const jobID = "manual-native-collect-expired"
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseHold := func() { releaseOnce.Do(func() { close(release) }) }
		defer releaseHold()
		original := f.s.sshCommand
		var cleanupCalls atomic.Int32
		var heldArtifact atomic.Bool
		held := make(chan struct{})
		f.s.sshCommand = func(ctx context.Context, binary string, args []string, input []byte) (utm.CommandResult, error) {
			q := vmTransportRequest(input)
			if q.Request.Identity.JobID == jobID && q.Operation == "cleanup" {
				cleanupCalls.Add(1)
			}
			// Hold exactly the first original artifact read. The log read
			// stays free so the controller's own settlement goroutine is
			// never blocked by this seam.
			if q.Request.Identity.JobID == jobID && q.Operation == "file" && q.Area != "log" && heldArtifact.CompareAndSwap(false, true) {
				<-release
				defer close(held)
			}
			return original(ctx, binary, args, input)
		}
		r, done, prepareDeadline := manualNativeDeadlineLaunch(f, jobID, marker, func(job *managed.JobSpec) {
			job.Deadlines.CollectSeconds = 2
		})
		humanDeadline := manualNativeAwaitHumanStage(f, r, prepareDeadline)
		preparation := manualNativeCapturePreparation(f, jobID, prepareDeadline, true)
		executionErr := manualNativeSettleWait(f, done, humanDeadline)
		gomega.Expect(executionErr).To(gomega.MatchError(runner.ErrUnproven))
		// The abandoned original collector finishes its expired exchange
		// unchanged after release; every owned callback and worker joins
		// before the final native evidence snapshots.
		releaseHold()
		gomega.Eventually(held, manualObserverSlack).Should(gomega.BeClosed())
		f.s.workers.Wait()
		gomega.Expect(cleanupCalls.Load()).To(gomega.BeZero())
		phases, err := f.s.ledger.LocalPhases(jobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["collect"].Outcome).To(gomega.Equal("unproven"))
		gomega.Expect(phases["collect"].BudgetSeconds).To(gomega.Equal(int64(2)))
		_, opened := phases["cleanup"]
		gomega.Expect(opened).To(gomega.BeFalse(), "an unproven collection must not open cleanup")
		f.s.failed(jobID, executionErr)
		snap, err := f.s.ledger.RecoverySnapshot(jobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(snap.Receipt.JobState).To(gomega.Equal("reconciling"))
		status, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Occupancy).To(gomega.Equal(1))
		gomega.Expect(status.Availability).To(gomega.Equal("quarantined"))
		_, err = f.s.ledger.Result(jobID)
		gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
		_, err = f.reserve(f.admit(jobID + "-successor"))
		gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
		session, err := f.s.ledger.ManualSession(jobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(session.Deadline).To(gomega.Equal(humanDeadline))
		gomega.Expect(session.AbortReason).To(gomega.Equal("manual_deadline"))
		preparation.expectPreserved(f, jobID, true)
		manualNativeExpectOneAbandonment(f, jobID)
		gomega.Expect(session.Preparation.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("pass")))
		data, err := os.ReadFile(filepath.Join(f.guest, jobID, "workspace/results/output.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("sealed"))
		contents, err := os.ReadFile(marker)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(contents)).To(gomega.Equal("setupsmoke"))
		gomega.Expect(f.control.effects).To(gomega.BeEmpty())
	})

	ginkgo.It("retains an unproven original cleanup after its admitted budget expires", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; cat input.txt > ../results/output.txt")
		marker := filepath.Join(f.guest, "counter")
		const jobID = "manual-native-cleanup-expired"
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseHold := func() { releaseOnce.Do(func() { close(release) }) }
		defer releaseHold()
		original := f.s.sshCommand
		var cleanupCalls atomic.Int32
		var issued atomic.Bool
		held := make(chan struct{})
		f.s.sshCommand = func(ctx context.Context, binary string, args []string, input []byte) (utm.CommandResult, error) {
			q := vmTransportRequest(input)
			if q.Request.Identity.JobID == jobID && q.Operation == "cleanup" {
				cleanupCalls.Add(1)
			}
			// Hold the one original cleanup exchange past its admitted
			// deadline; the real endpoint later refuses it unchanged.
			if q.Request.Identity.JobID == jobID && q.Operation == "cleanup" && issued.CompareAndSwap(false, true) {
				<-release
				defer close(held)
			}
			return original(ctx, binary, args, input)
		}
		r, done, prepareDeadline := manualNativeDeadlineLaunch(f, jobID, marker, func(job *managed.JobSpec) {
			job.Deadlines.CleanupSeconds = 2
		})
		humanDeadline := manualNativeAwaitHumanStage(f, r, prepareDeadline)
		preparation := manualNativeCapturePreparation(f, jobID, prepareDeadline, true)
		executionErr := manualNativeSettleWait(f, done, humanDeadline)
		gomega.Expect(executionErr).To(gomega.MatchError(runner.ErrUnproven))
		releaseHold()
		gomega.Eventually(held, manualObserverSlack).Should(gomega.BeClosed())
		f.s.workers.Wait()
		gomega.Expect(cleanupCalls.Load()).To(gomega.Equal(int32(1)))
		phases, err := f.s.ledger.LocalPhases(jobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["collect"].Outcome).To(gomega.Equal("failed"))
		gomega.Expect(phases["collect"].EvidenceGaps).To(gomega.HaveLen(1))
		gomega.Expect(phases["collect"].EvidenceGaps[0].Reason).To(gomega.Equal(manualObservationGapReason))
		gomega.Expect(phases["cleanup"].Outcome).To(gomega.Equal("unproven"))
		gomega.Expect(phases["cleanup"].BudgetSeconds).To(gomega.Equal(int64(2)))
		f.s.failed(jobID, executionErr)
		snap, err := f.s.ledger.RecoverySnapshot(jobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(snap.Receipt.JobState).To(gomega.Equal("reconciling"))
		status, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Occupancy).To(gomega.Equal(1))
		gomega.Expect(status.Availability).To(gomega.Equal("quarantined"))
		_, err = f.s.ledger.Result(jobID)
		gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
		_, err = f.reserve(f.admit(jobID + "-successor"))
		gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
		session, err := f.s.ledger.ManualSession(jobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(session.Deadline).To(gomega.Equal(humanDeadline))
		gomega.Expect(session.AbortReason).To(gomega.Equal("manual_deadline"))
		preparation.expectPreserved(f, jobID, true)
		manualNativeExpectOneAbandonment(f, jobID)
		gomega.Expect(session.Preparation.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("pass")))
		// The expired original cleanup refused without deleting guest bytes.
		data, err := os.ReadFile(filepath.Join(f.guest, jobID, "workspace/results/output.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("sealed"))
		_, err = os.Stat(filepath.Join(f.guest, jobID))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		contents, err := os.ReadFile(marker)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(contents)).To(gomega.Equal("setupsmoke"))
		gomega.Expect(f.control.effects).To(gomega.BeEmpty())
	})

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
