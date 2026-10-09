package controller

import (
	"slices"

	"github.com/irootkernel/haepari/internal/managed"
)

func outputGapReason(job managed.JobSpec, name string, absent []string) string {
	if managed.AbsenceAware(job) && slices.Contains(absent, name) {
		return "required_output_absent"
	}
	return "required_output_uncollected"
}
func absenceAllowsCleanup(job managed.JobSpec, gaps []managed.EvidenceGap, a collectionAttempt, status phaseStatus) bool {
	if status != phaseInTime || a.expired || a.unproven {
		return false
	}
	return collectionAllowsCleanup(job, managed.LocalPhase{Outcome: "failed", EvidenceCompleteness: "partial", EvidenceGaps: gaps})
}
func collectionAllowsCleanup(job managed.JobSpec, p managed.LocalPhase) bool {
	if len(p.ProtectedEvidence) != 0 {
		return false
	}
	if p.EvidenceCompleteness == "complete" {
		return p.Outcome == "complete"
	}
	if !managed.AbsenceAware(job) || p.EvidenceCompleteness != "partial" || p.Outcome != "failed" || len(p.EvidenceGaps) == 0 {
		return false
	}
	for _, g := range p.EvidenceGaps {
		if g.Reason != "required_output_absent" || !slices.Contains(job.Workload.RequiredOutputs, g.Path) {
			return false
		}
	}
	return true
}
