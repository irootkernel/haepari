package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
)

func (s *runtimeServer) manualComplete(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Result           json.RawMessage `json:"result"`
		ExpectedRevision int64           `json:"expected_revision"`
	}
	if err := decodeBody(r, &input); err != nil {
		reject(w, "manual.complete", err)
		return
	}
	id := r.PathValue("id")
	s.cancelMu.Lock()
	c := s.localRuns[id]
	s.cancelMu.Unlock()
	if c != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
	}
	receipt, err := s.ledger.CompleteManual(id, s.installationID(), input.Result, input.ExpectedRevision)
	if err != nil {
		reject(w, "manual.complete", err)
		return
	}
	if c != nil {
		// Replay's immutable receipt is not a new live revision.
		if current, err := s.ledger.Snapshot(id); err == nil {
			c.receipt = current
		}
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
	s.notifyScheduler()
	send(w, http.StatusOK, "manual.complete", receipt, nil)
}

func preparationArgv(job managed.JobSpec, executable string) ([]string, error) {
	steps := runner.PreparationSteps{Workload: job.Workload.Argv}
	if job.Setup != nil {
		steps.Setup = job.Setup.Argv
	}
	return runner.PreparationArgv(executable, steps)
}

// Called before local effects and before phase identity publication.
func (s *runtimeServer) claimManualLocal(c *localRunControl, deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cancel, err := s.ledger.Cancellation(c.identity.JobID)
	if err != nil {
		return err
	}
	if cancel.Requested && c.identity.RunID == "" {
		// The existing no-effect cancellation path remains authoritative.
		c.manual = false
		return nil
	}
	runID, err := randomID("prepare-")
	if err != nil {
		return err
	}
	id := c.identity
	id.RunID = runID
	r, err := s.ledger.ClaimManualLocal(id, c.receipt.JobRevision, deadline, s.installationID())
	if err == nil {
		c.identity = id
		c.receipt = r
	}
	return err
}

func (s *runtimeServer) executeManualLocal(c *localRunControl, job managed.JobSpec, root, workspace string, deadline time.Time) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	argv, err := preparationArgv(job, executable)
	if err != nil {
		return err
	}
	preparation := job
	preparation.Workload.Argv = argv
	preparation.Deadlines.RunSeconds = job.Deadlines.PrepareSeconds
	_, log, err := observeWithinRunBudgetWithWorker(deadline, time.Duration(job.Deadlines.CleanupSeconds)*time.Second, s.startWorker, s.detached, func() (bool, *os.File, error) {
		return s.observeSupervisor(c, preparation, root, workspace, c.identity.RunID, deadline)
	})
	if log != nil {
		defer log.Close()
	}
	if err != nil {
		return err
	}
	view, err := runner.Inspect(c.runnerRequest)
	if err != nil || view.Exit == nil || !view.Exit.TerminationProven {
		return errors.Join(runner.ErrUnproven, err)
	}
	return s.finishManualPreparation(c, job, workspace, view.Exit.Outcome, nil, nil)
}

func (s *runtimeServer) finishManualPreparation(c *localRunControl, job managed.JobSpec, workspace string, out managed.LocalOutcome, guest *runner.GuestRequest, steps []managed.ResultStep) error {
	phase := c.phases["prepare"]
	outcome := "complete"
	if out.WorkloadOutcome != "pass" {
		outcome = "failed"
	}
	if phase.Completed {
		// Recovery preserves the committed observation, not its replay time.
		outcome = phase.Outcome
	} else if !time.Now().Before(phase.Deadline) {
		outcome = "late"
	}
	if !phase.Completed {
		if err := s.endPhase(c, "prepare", managed.LocalPhase{Outcome: outcome, AdapterSteps: steps}); err != nil {
			return err
		}
	}
	if err := s.ledger.RecordManualPreparation(c.identity, out, workspace); err != nil {
		return err
	}
	c.mu.Lock()
	cancel, err := s.ledger.Cancellation(c.identity.JobID)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	m, err := s.ledger.ManualSession(c.identity.JobID)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	if out.WorkloadOutcome != "pass" || cancel.Requested || m.AbortReason != "" || outcome == "late" {
		c.mu.Unlock()
		reason := "manual_prepare_failed"
		if outcome == "late" || m.AbortReason == "prepare_deadline" {
			reason = "prepare_deadline"
		}
		if cancel.Requested {
			reason = "manual_cancelled"
		}
		return s.settleManualAbort(c, job, workspace, guest, reason, false)
	}
	if c.receipt.JobState == "preparing" {
		key := "prepared-" + c.identity.JobID
		if c.recoveryEvidence != nil {
			key = "prepared-recovery-" + managed.RecoveryDigest(struct {
				Receipt  managed.Receipt
				Evidence managed.RecoveryEvidence
			}{c.receipt, *c.recoveryEvidence})
		}
		c.receipt, err = s.ledger.AdvanceLocal(c.identity.JobID, c.receipt.JobRevision, "preparing", "ready", key)
	}
	if err == nil {
		r := c.receipt
		c.receipt, err = s.ledger.HoldManualCapacity(managed.ManualHoldRequest{JobID: r.JobID, ExpectedJobRevision: r.JobRevision, ExpectedConfigRevision: r.ResourceConfigRevision, ExpectedAvailabilityRevision: r.ResourceAvailabilityRevision, Slot: r.Slot, LeaseGeneration: r.LeaseGeneration, EnvironmentDigest: job.Environment.ProfileDigest, ReadinessDigest: vmEvidence(out), ObservationReference: "manual-preparation:" + c.identity.RunID, MutationKey: "manual-hold-" + r.JobID})
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	c.phase("awaiting_manual")
	return s.awaitManual(c, job, workspace, guest)
}

func (s *runtimeServer) manualPreparationFailure(c *localRunControl, job managed.JobSpec, root, workspace, reason string) error {
	if !c.ownsJobRoot && c.effects {
		return managed.ErrBlocked
	}
	if !c.effects {
		if _, err := os.Lstat(root); !os.IsNotExist(err) {
			return managed.ErrBlocked
		}
	}
	out := managed.LocalOutcome{WorkloadOutcome: "inconclusive"}
	if err := s.ledger.RecordManualPreparation(c.identity, out, workspace); err != nil {
		return err
	}
	return s.settleManualAbort(c, job, workspace, nil, reason, !c.effects)
}

func (s *runtimeServer) settleManualAbort(c *localRunControl, job managed.JobSpec, workspace string, guest *runner.GuestRequest, reason string, noEffect bool) error {
	c.mu.Lock()
	r, err := s.ledger.AbortManual(c.identity, c.receipt.JobRevision, reason, noEffect)
	if err == nil {
		c.receipt = r
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	out, err := s.ledger.LocalOutcome(c.identity.JobID)
	if err != nil {
		return err
	}
	m, err := s.ledger.ManualSession(c.identity.JobID)
	if err != nil {
		return err
	}
	reason = m.AbortReason
	if guest != nil {
		return s.settleVMPhases(c, job, *guest, out)
	}
	return s.settleLocalPhases(c, job, filepath.Join(s.root.Path, "jobs", c.identity.JobID), workspace, out, noEffect, reason, nil)
}

func (s *runtimeServer) awaitManual(c *localRunControl, job managed.JobSpec, workspace string, guest *runner.GuestRequest) error {
	for {
		changed := s.ledger.Changes()
		m, err := s.ledger.ManualSession(c.identity.JobID)
		if err != nil {
			return err
		}
		c.mu.Lock()
		r, err := s.ledger.Snapshot(c.identity.JobID)
		if err == nil {
			c.receipt = r
		}
		c.mu.Unlock()
		if err != nil {
			return err
		}
		if m.Outcome != nil {
			if guest != nil {
				return s.settleVMPhases(c, job, *guest, *m.Outcome)
			}
			return s.settleLocalPhases(c, job, filepath.Join(s.root.Path, "jobs", c.identity.JobID), workspace, *m.Outcome, false, m.AbortReason, nil)
		}
		cancel, err := s.ledger.Cancellation(c.identity.JobID)
		if err != nil {
			return err
		}
		if cancel.Requested {
			return s.settleManualAbort(c, job, workspace, guest, "manual_cancelled", false)
		}
		if !time.Now().Before(m.Deadline) || m.AbortReason != "" {
			c.mu.Lock()
			c.receipt, err = s.ledger.ManualDeadline(c.identity, c.receipt.JobRevision, "manual_deadline")
			c.mu.Unlock()
			if err != nil {
				return err
			}
			return s.settleManualAbort(c, job, workspace, guest, "manual_deadline", false)
		}
		timer := time.NewTimer(time.Until(m.Deadline))
		select {
		case <-s.detached:
			timer.Stop()
			return errControllerDetached
		case <-changed:
			timer.Stop()
		case <-c.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// Missing declared human observations affect evidence, not physical cleanup
// safety. Call only after the filesystem collection/cleanup decision is known.
func (s *runtimeServer) manualEvidence(job managed.JobSpec, jobID string, evidence managed.EvidenceCompleteness, gaps []managed.EvidenceGap) (managed.EvidenceCompleteness, []managed.EvidenceGap, error) {
	if job.Mode != "manual" {
		return evidence, gaps, nil
	}
	m, err := s.ledger.ManualSession(jobID)
	if errors.Is(err, managed.ErrNotFound) {
		return evidence, gaps, nil
	}
	if err != nil {
		return evidence, gaps, err
	}
	// VM retirement can prove cancellation before its preparation run starts
	// through the common no-start receipt, before any human stage exists.
	if m.Outcome == nil && m.Deadline.IsZero() {
		outcome, err := s.ledger.LocalOutcome(jobID)
		if err == nil && !outcome.Started && outcome.WorkloadOutcome == "cancelled" {
			return evidence, gaps, nil
		}
		if err != nil && !errors.Is(err, managed.ErrNotFound) {
			return evidence, gaps, err
		}
	}
	if m.Unobserved || m.Completion == nil && (m.Outcome == nil || m.Outcome.WorkloadOutcome != "cancelled") {
		evidence = "partial"
		gaps = append(gaps, managed.EvidenceGap{Path: job.Manual.ScenarioDocument, Reason: manualObservationGapReason})
	}
	return evidence, gaps, nil
}

const manualObservationGapReason = "manual_observation_unobserved"

// Physical cleanup ignores only this manual-owned observational gap. Unknown
// physical evidence, protected paths and unproven phase outcomes remain intact.
func physicalCollectionPhase(job managed.JobSpec, phase managed.LocalPhase) managed.LocalPhase {
	if job.Mode != "manual" || job.Manual == nil || phase.EvidenceCompleteness != "partial" || phase.Outcome != "failed" {
		return phase
	}
	physical := phase
	physical.EvidenceGaps = nil
	removed := false
	for _, gap := range phase.EvidenceGaps {
		if gap.Reason == manualObservationGapReason && gap.Path == job.Manual.ScenarioDocument {
			removed = true
		} else {
			physical.EvidenceGaps = append(physical.EvidenceGaps, gap)
		}
	}
	if removed && len(physical.EvidenceGaps) == 0 {
		physical.EvidenceCompleteness = "complete"
		physical.Outcome = "complete"
	}
	return physical
}

func (s *runtimeServer) manualCollectionPhase(job managed.JobSpec, jobID string, phase managed.LocalPhase) (managed.LocalPhase, error) {
	var err error
	phase.EvidenceCompleteness, phase.EvidenceGaps, err = s.manualEvidence(job, jobID, phase.EvidenceCompleteness, phase.EvidenceGaps)
	if err != nil {
		return phase, err
	}
	if phase.EvidenceCompleteness != "complete" && phase.Outcome == "complete" {
		phase.Outcome = "failed"
	}
	return phase, nil
}
