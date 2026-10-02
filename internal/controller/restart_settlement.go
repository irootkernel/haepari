package controller

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
)

// A recorded collection is consumed, never copied again into an exclusive
// artifact directory. An interrupted collector protects its remaining source.
func (s *runtimeServer) resumeLocalSettlement(c *localRunControl, job managed.JobSpec, workspace string, out managed.LocalOutcome, reason string) error {
	collect := c.phases["collect"]
	if !collect.Completed || collect.Outcome == "unproven" {
		collect.Completed, collect.Outcome = true, "failed"
		collect.EvidenceCompleteness = "partial"
		var err error
		collect.Logs, collect.EvidenceGaps, err = s.ledger.LocalLogManifest(c.identity.JobID)
		if err != nil {
			return err
		}
		collect.ProtectedEvidence = []managed.ProtectedEvidence{{Path: "workspace", Reason: "collection_interrupted", RecoveryReference: "job:" + c.identity.JobID}}
		if err := s.endPhase(c, "collect", collect); err != nil {
			return err
		}
		collect = c.phases["collect"]
	}
	if c.receipt.JobState == "collecting" {
		if err := s.advancePhase(c, "collecting", "cleaning", "recovered-collected"); err != nil {
			return err
		}
	}
	cleanup, exists := c.phases["cleanup"]
	if !exists || !cleanup.Completed || cleanup.Outcome == "unproven" {
		deadline := cleanup.Deadline
		if !exists {
			deadline = time.Now().Add(time.Duration(job.Deadlines.CleanupSeconds) * time.Second)
			if err := s.beginPhase(c, "cleanup", job.Deadlines.CleanupSeconds, deadline); err != nil {
				return err
			}
		}
		observed := managed.LocalPhase{Outcome: "complete"}
		if collect.EvidenceCompleteness != "complete" || len(collect.ProtectedEvidence) > 0 {
			observed.Outcome, observed.ProtectedEvidence = "failed", collect.ProtectedEvidence
		} else {
			err, status := boundedPhaseWithWorker(deadline, s.detached, func(work func()) bool {
				return s.startJobWorker(c.identity.JobID, work)
			}, func() error {
				err := removeTreeUntil(workspace, deadline)
				if os.IsNotExist(err) {
					return nil
				}
				return err
			})
			if status == phaseUnproven {
				observed.Outcome = "unproven"
			} else if status == phaseLate {
				observed.Outcome = "late"
			} else if err != nil {
				observed.Outcome = "failed"
			}
			if observed.Outcome != "complete" {
				if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
					observed.ProtectedEvidence = []managed.ProtectedEvidence{{Path: "workspace", Reason: "cleanup_interrupted", RecoveryReference: "job:" + c.identity.JobID}}
				}
			}
		}
		if err := s.endPhase(c, "cleanup", observed); err != nil {
			return err
		}
		cleanup = c.phases["cleanup"]
		if cleanup.Outcome == "unproven" {
			return managed.ErrBlocked
		}
	}
	if cleanup.Outcome == "complete" {
		if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
			return managed.ErrBlocked
		}
	}
	if c.receipt.JobState == "cleaning" {
		if err := s.advancePhase(c, "cleaning", "settling", "recovered-cleaned"); err != nil {
			return err
		}
	}
	result := managed.Result{WorkloadOutcome: out.WorkloadOutcome, NativeExitCode: out.NativeExitCode, NativeSignal: out.NativeSignal, EvidenceCompleteness: collect.EvidenceCompleteness, Artifacts: collect.Artifacts, ProtectedEvidence: cleanup.ProtectedEvidence, CleanupOutcome: "verified", Reason: reason, ResourceAvailability: "available"}
	result.Logs, result.EvidenceGaps = collect.Logs, collect.EvidenceGaps
	if cleanup.Outcome != "complete" {
		result.CleanupOutcome, result.ResourceAvailability = "failed", "quarantined"
		if cleanup.Outcome == "late" {
			result.Reason = "cleanup_deadline"
		}
	}
	if collect.EvidenceCompleteness != "complete" {
		result.Reason = "evidence_retained"
	}
	if result.Reason == "" && out.WorkloadOutcome == "timed_out" {
		result.Reason = "run_deadline"
	}
	if c.receipt.JobState != "settling" {
		return errors.Join(managed.ErrBlocked, context.Canceled)
	}
	return s.verifyLocalResult(c, result, false)
}
