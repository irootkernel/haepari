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
func (s *runtimeServer) resumeLocalSettlement(c *localRunControl, job managed.JobSpec, workspace string, out managed.LocalOutcome, reason string, logLimit int64, hasRequest bool, artifactBytes, artifactCount int64) error {
	collect := c.phases["collect"]
	if !collect.Completed || collect.Outcome == "unproven" {
		// The native manifest has no pinned token. Keep a gap the unproven receipt already recorded.
		priorGaps := append([]managed.EvidenceGap(nil), collect.EvidenceGaps...)
		unproven := collect.Outcome == "unproven"
		collect.Completed, collect.Outcome = true, "failed"
		collect.EvidenceCompleteness = "partial"
		var err error
		collect.Logs, collect.EvidenceGaps, err = s.ledger.LocalLogManifest(c.identity.JobID)
		if err != nil {
			return err
		}
		if len(collect.Logs) > 0 && !hasRequest {
			return managed.ErrBlocked
		}
		collect.Logs, collect.EvidenceGaps, _ = rejectOversizedPinnedLog(collect.Logs, collect.EvidenceGaps, logLimit, hasRequest)
		if unproven {
			for _, gap := range priorGaps {
				if gap.Reason != "pinned_log_exceeded" {
					continue
				}
				kept := false
				for _, existing := range collect.EvidenceGaps {
					if existing == gap {
						kept = true
						break
					}
				}
				if !kept {
					collect.EvidenceGaps = append(collect.EvidenceGaps, gap)
				}
			}
		}
		collect.ProtectedEvidence = []managed.ProtectedEvidence{{Path: "workspace", Reason: "collection_interrupted", RecoveryReference: "job:" + c.identity.JobID}}
		if err := s.endPhase(c, "collect", collect); err != nil {
			return err
		}
		collect = c.phases["collect"]
	} else {
		if len(collect.Logs) > 0 && !hasRequest {
			return managed.ErrBlocked
		}
		for _, item := range collect.Logs {
			if item.SizeBytes > logLimit {
				return managed.ErrInvalid
			}
		}
		if int64(len(collect.Artifacts)) > artifactCount {
			return managed.ErrInvalid
		}
		var used int64
		for _, item := range collect.Artifacts {
			if item.SizeBytes > artifactBytes-used {
				return managed.ErrInvalid
			}
			used += item.SizeBytes
		}
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
		if !collectionAllowsCleanup(job, collect) {
			observed.Outcome, observed.ProtectedEvidence = "failed", collect.ProtectedEvidence
		} else {
			err, status := boundedPhaseWithWorker(deadline, s.detached, func(work func()) bool {
				return s.startJobWorker(c.identity.JobID, work)
			}, func() error {
				err := s.cleanupLocalWorkspace(c.identity, job, out.Started, workspace, deadline)
				// A missing v2 prepared pathname does not prove deletion of
				// its bound directory; it may have moved with retained evidence.
				if os.IsNotExist(err) && (!managed.AbsenceAware(job) || !out.Started) {
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
				if _, err := os.Lstat(workspace); managed.AbsenceAware(job) && out.Started || !os.IsNotExist(err) {
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
	// A manual-only observation gap leaves physical collection complete.
	// Fresh settlement uses that physical fact and does not call it retained.
	if physicalCollectionPhase(job, collect).EvidenceCompleteness != "complete" {
		// Positive absence applies only after cleanup completes. Incomplete
		// cleanup keeps evidence_retained, or cleanup_deadline when already set.
		if reason == "" && cleanup.Outcome == "complete" && requiredOutputAbsence(job, collect) {
			result.Reason = "required_output_absent"
		} else if result.Reason != "cleanup_deadline" {
			result.Reason = "evidence_retained"
		}
	}
	if result.Reason == "" && out.WorkloadOutcome == "timed_out" {
		result.Reason = "run_deadline"
	}
	if c.receipt.JobState != "settling" {
		return errors.Join(managed.ErrBlocked, context.Canceled)
	}
	return s.verifyLocalResult(c, result, false)
}
