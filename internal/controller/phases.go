package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
)

func (s *runtimeServer) beginPhase(c *localRunControl, phase string, seconds int64, deadline time.Time) error {
	select {
	case <-s.detached:
		return errControllerDetached
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if prior, ok := c.phases[phase]; ok {
		if c.recoveryEvidence == nil || prior.Completed && prior.Outcome != "unproven" {
			return managed.ErrConflict
		}
		return nil // Resume consumer uses this original deadline, never a new one.
	}
	now := time.Now().UTC()
	p := managed.LocalPhase{Schema: "haepari.phase-receipt/v1", Identity: c.identity, Phase: phase, BudgetSeconds: seconds, StartedAt: now, Deadline: deadline.UTC(), ObservedAt: now}
	owner, err := platform.ObserveProcess(os.Getpid())
	if err != nil {
		return err
	}
	p.ControllerOwner = &owner.Identity
	r, err := s.ledger.RecordLocalPhase(managed.LocalPhaseRequest{ExpectedRevision: c.receipt.JobRevision, Record: p, MutationKey: "phase-intent-" + phase + "-" + c.identity.JobID})
	if err != nil {
		return err
	}
	if c.phases == nil {
		c.phases = map[string]managed.LocalPhase{}
	}
	c.phases[phase], c.receipt = p, r
	return nil
}

func (s *runtimeServer) endPhase(c *localRunControl, phase string, completion managed.LocalPhase) error {
	select {
	case <-s.detached:
		return errControllerDetached
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.phases[phase]
	p.Completed, p.ObservedAt = true, time.Now().UTC()
	p.Outcome, p.ProtectedEvidence = completion.Outcome, completion.ProtectedEvidence
	p.Artifacts, p.EvidenceCompleteness = completion.Artifacts, completion.EvidenceCompleteness
	var r managed.Receipt
	var err error
	if c.recoveryEvidence != nil {
		if p.Outcome == "complete" && phase != "verify" && p.ObservedAt.After(p.Deadline) {
			p.Outcome = "late"
		}
		r, err = s.ledger.ResolveLocalPhase(managed.PhaseResolutionRequest{ExpectedRevision: c.receipt.JobRevision, OriginalDigest: managed.RecoveryDigest(c.phases[phase]), Record: p, Evidence: *c.recoveryEvidence, MutationKey: "resolved-" + phase + "-" + c.identity.JobID})
	} else {
		r, err = s.ledger.RecordLocalPhase(managed.LocalPhaseRequest{ExpectedRevision: c.receipt.JobRevision, Record: p, MutationKey: "phase-observed-" + phase + "-" + c.identity.JobID})
	}
	if err != nil {
		return err
	}
	c.phases[phase], c.receipt = p, r
	return nil
}

func (s *runtimeServer) advancePhase(c *localRunControl, from, to, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, err := s.ledger.AdvanceLocal(c.identity.JobID, c.receipt.JobRevision, from, to, key+"-"+c.identity.JobID)
	if err == nil {
		c.receipt = r
	}
	return err
}

func (s *runtimeServer) finishPreparation(c *localRunControl, job managed.JobSpec, jobRoot, workspace, reason string) error {
	c.mu.Lock()
	noEffect := !c.effects
	if noEffect {
		// No worker was authorized, no run was claimed, and the owned Job path
		// is absent. A flag alone cannot prove an existing path safe to discard.
		if _, err := os.Lstat(jobRoot); !os.IsNotExist(err) {
			c.mu.Unlock()
			return managed.ErrBlocked
		}
	} else if !c.ownsJobRoot {
		c.mu.Unlock()
		return managed.ErrBlocked
	}
	r, err := s.ledger.RecordLocalPreparationStop(managed.LocalPreparationStopRequest{Identity: c.identity, ExpectedRevision: c.receipt.JobRevision, Reason: reason, NoOwnedEffect: noEffect, MutationKey: "preparation-stop-" + c.identity.JobID})
	if err == nil {
		c.receipt = r
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	outcome, err := s.ledger.LocalOutcome(c.identity.JobID)
	if err != nil {
		return err
	}
	if outcome.WorkloadOutcome == "cancelled" {
		reason = "cancel_before_start"
	}
	return s.settleLocalPhases(c, job, jobRoot, workspace, outcome, noEffect, reason, nil)
}

// Every stopped local path reaches this collection/cleanup/verification path.
// Unproven workers return to reconciliation; they never reach terminal commit.
func (s *runtimeServer) settleLocalPhases(c *localRunControl, job managed.JobSpec, jobRoot, workspace string, outcome managed.LocalOutcome, noEffect bool, reason string, log *os.File) error {
	if noEffect {
		return s.verifyLocalResult(c, managed.Result{WorkloadOutcome: outcome.WorkloadOutcome, EvidenceCompleteness: "complete", CleanupOutcome: "not_needed", ResourceAvailability: "available", Reason: reason}, true)
	}
	if _, exists := c.phases["collect"]; exists && c.recoveryEvidence != nil {
		return s.resumeLocalSettlement(c, job, workspace, outcome, reason)
	}
	collectDeadline := time.Now().Add(time.Duration(job.Deadlines.CollectSeconds) * time.Second)
	if err := s.beginPhase(c, "collect", job.Deadlines.CollectSeconds, collectDeadline); err != nil {
		return err
	}
	c.phase("collecting")
	// An unstarted workload has no required workload outputs. Its owned
	// preparation files still require cleanup, and native receipts remain.
	required := job.Workload.RequiredOutputs
	if !outcome.Started {
		required = nil
	}
	collected, collectionStatus := boundedOwnedPhase(s, collectDeadline, func() collectionAttempt {
		if log != nil {
			if err := log.Sync(); err != nil {
				return collectionAttempt{}
			}
		}
		artifacts, complete, expired := collectOutputs(workspace, filepath.Join(jobRoot, "artifacts"), required, collectDeadline)
		return collectionAttempt{artifacts: artifacts, complete: complete, expired: expired}
	})
	protected := []managed.ProtectedEvidence{}
	collectionOutcome := "complete"
	if collectionStatus == phaseLate {
		collected.complete, collected.expired = false, true
		collectionOutcome = "late"
	} else if collectionStatus == phaseUnproven {
		collectionOutcome = "unproven"
	} else if !collected.complete {
		collectionOutcome = "failed"
	}
	if !collected.complete {
		retentionReason := "required_output_uncollected"
		if collected.expired {
			retentionReason = "collection_deadline"
		}
		protected = append(protected, managed.ProtectedEvidence{Path: "workspace", Reason: retentionReason, RecoveryReference: "job:" + c.identity.JobID})
	}
	// Record protected paths before any cleanup intent or deletion. A lost
	// filesystem receipt cannot erase this lease-bound recovery reference.
	evidence := managed.EvidenceCompleteness("complete")
	if !collected.complete {
		evidence = "partial"
	}
	if err := s.endPhase(c, "collect", managed.LocalPhase{Outcome: collectionOutcome, ProtectedEvidence: protected, Artifacts: collected.artifacts, EvidenceCompleteness: evidence}); err != nil {
		return err
	}
	if collectionStatus == phaseUnproven {
		return errors.New("collector termination unproven after deadline")
	}
	if err := s.advancePhase(c, "collecting", "cleaning", "collected"); err != nil {
		return err
	}
	cleanupDeadline := time.Now().Add(time.Duration(job.Deadlines.CleanupSeconds) * time.Second)
	if err := s.beginPhase(c, "cleanup", job.Deadlines.CleanupSeconds, cleanupDeadline); err != nil {
		return err
	}
	c.phase("cleaning")
	cleanup := managed.CleanupOutcome("verified")
	cleanupOutcome := "complete"
	var cleanupErr error
	if !collected.complete {
		cleanupErr = errors.New("required evidence retained")
	} else {
		var status phaseStatus
		cleanupErr, status = boundedOwnedPhase(s, cleanupDeadline, func() error {
			err := removeTreeUntil(workspace, cleanupDeadline)
			if os.IsNotExist(err) {
				return nil
			}
			return err
		})
		if status == phaseUnproven {
			if err := s.endPhase(c, "cleanup", managed.LocalPhase{Outcome: "unproven", ProtectedEvidence: []managed.ProtectedEvidence{{Path: "workspace", Reason: "cleanup_unproven", RecoveryReference: "job:" + c.identity.JobID}}}); err != nil {
				return err
			}
			return errors.New("cleanup termination unproven after deadline")
		}
		if status == phaseLate {
			cleanupErr = context.DeadlineExceeded
			cleanupOutcome = "late"
		}
		if cleanupErr != nil {
			if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
				protected = append(protected, managed.ProtectedEvidence{Path: "workspace", Reason: "cleanup_incomplete", RecoveryReference: "job:" + c.identity.JobID})
			}
		}
	}
	if cleanupErr != nil {
		cleanup = "failed"
		if cleanupOutcome != "late" {
			cleanupOutcome = "failed"
		}
	}
	if err := s.endPhase(c, "cleanup", managed.LocalPhase{Outcome: cleanupOutcome, ProtectedEvidence: protected}); err != nil {
		return err
	}
	if err := s.advancePhase(c, "cleaning", "settling", "cleaned"); err != nil {
		return err
	}
	result := managed.Result{ResourceAvailability: "available", WorkloadOutcome: outcome.WorkloadOutcome, EvidenceCompleteness: evidence, CleanupOutcome: cleanup, NativeExitCode: outcome.NativeExitCode, NativeSignal: outcome.NativeSignal, Artifacts: collected.artifacts, ProtectedEvidence: protected, Reason: reason}
	if cleanup == "failed" {
		result.ResourceAvailability = "quarantined"
	}
	if reason == "" && outcome.WorkloadOutcome == "timed_out" {
		result.Reason = "run_deadline"
	} else if reason == "" && errors.Is(cleanupErr, context.DeadlineExceeded) {
		result.Reason = "cleanup_deadline"
	}
	if !collected.complete {
		result.Reason = "evidence_retained"
	}
	return s.verifyLocalResult(c, result, false)
}

// All stopped local outcomes, including no-effect paths, share verification,
// verdict derivation and the exact proof-gated atomic capacity return.
func (s *runtimeServer) verifyLocalResult(c *localRunControl, result managed.Result, noEffect bool) error {
	c.phase("settling")
	verified := c.phases["verify"].Completed && c.phases["verify"].Outcome == "complete"
	if !verified {
		if err := s.beginPhase(c, "verify", 0, time.Time{}); err != nil {
			return err
		}
	}
	eligible := c.recoveryEvidence == nil || c.recoveryEvidence.Source != "operator-attested"
	verdict, err := managed.DeriveVerdict(managed.ResultFacts{WorkloadOutcome: result.WorkloadOutcome, EvidenceCompleteness: result.EvidenceCompleteness, CleanupOutcome: result.CleanupOutcome, ManualEligible: eligible, CancelledBeforeOutcome: result.WorkloadOutcome == "cancelled", TerminationProven: true, NoOwnedEffect: noEffect})
	if err != nil {
		return err
	}
	result.Schema, result.JobID, result.ResourceID = "haepari.result/v1", c.identity.JobID, c.identity.ResourceID
	result.ResourceRevision, result.LeaseGeneration = c.identity.ConfigRevision, c.identity.LeaseGeneration
	result.JobState, result.FinalVerdict = "finished", verdict
	if c.recoveryEvidence != nil {
		result.Observations = &managed.ResultObservations{Source: c.recoveryEvidence.Source, OperatorAttested: c.recoveryEvidence.Source == "operator-attested"}
	}
	if !verified {
		if err := s.endPhase(c, "verify", managed.LocalPhase{Outcome: "complete", ProtectedEvidence: result.ProtectedEvidence}); err != nil {
			return err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r, err := s.ledger.SettleLocal(c.identity.JobID, c.receipt.JobRevision, result)
	if err == nil {
		c.receipt = r
	}
	return err
}
