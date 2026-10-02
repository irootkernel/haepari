package managed

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"
	"time"

	"github.com/irootkernel/haepari/internal/platform"
)

// LocalPhase is an exact lease-bound intent or observation. Active event
// payloads cannot be purged; terminal timings retain their useful projection.
type LocalPhase struct {
	Schema               string                    `json:"schema"`
	Identity             LocalIdentity             `json:"identity"`
	Phase                string                    `json:"phase"`
	BudgetSeconds        int64                     `json:"budget_seconds"`
	StartedAt            time.Time                 `json:"started_at"`
	Deadline             time.Time                 `json:"deadline"`
	ObservedAt           time.Time                 `json:"observed_at"`
	Completed            bool                      `json:"completed"`
	Outcome              string                    `json:"outcome,omitempty"`
	ProtectedEvidence    []ProtectedEvidence       `json:"protected_evidence,omitempty"`
	Artifacts            []ResultArtifact          `json:"artifacts,omitempty"`
	Logs                 []ResultArtifact          `json:"logs,omitempty"`
	EvidenceGaps         []EvidenceGap             `json:"evidence_gaps,omitempty"`
	EvidenceCompleteness EvidenceCompleteness      `json:"evidence_completeness,omitempty"`
	ControllerOwner      *platform.ProcessIdentity `json:"controller_owner,omitempty"`
}

type LocalPhaseRequest struct {
	ExpectedRevision int64
	Record           LocalPhase
	MutationKey      string
}

func (l *Ledger) RecordLocalPhase(q LocalPhaseRequest) (Receipt, error) {
	p := q.Record
	if q.ExpectedRevision <= 0 || !validLocalPhase(p) {
		return Receipt{}, ErrInvalid
	}
	return l.mutate("local_phase_observation", q.MutationKey, q, func(tx *sql.Tx) (Receipt, error) {
		if err := matchLocalIdentityTx(tx, p.Identity); err != nil {
			return Receipt{}, err
		}
		var raw []byte
		if err := tx.QueryRow(`SELECT spec_json FROM managed_jobs WHERE id=?`, p.Identity.JobID).Scan(&raw); err != nil {
			return Receipt{}, err
		}
		job, err := DecodeJob(raw)
		if err != nil {
			return Receipt{}, err
		}
		budgets := map[string]int64{"prepare": job.Deadlines.PrepareSeconds, "run": job.Deadlines.RunSeconds, "collect": job.Deadlines.CollectSeconds, "cleanup": job.Deadlines.CleanupSeconds, "verify": 0}
		if p.BudgetSeconds != budgets[p.Phase] {
			return Receipt{}, ErrInvalid
		}
		current, err := l.snapshotTx(tx, p.Identity.JobID)
		if err != nil {
			return Receipt{}, err
		}
		allowed := map[string][]string{
			"prepare": {"preparing", "ready", "collecting"}, "run": {"running", "cancelling", "collecting"},
			"collect": {"collecting"}, "cleanup": {"cleaning"}, "verify": {"settling"},
		}
		if !oneOf(current.JobState, allowed[p.Phase]...) {
			return Receipt{}, ErrConflict
		}
		if _, err := localRevisionTx(tx, p.Identity.JobID, q.ExpectedRevision, current.JobState); err != nil {
			return Receipt{}, err
		}
		phases, err := localPhasesTx(tx, p.Identity.JobID)
		if err != nil {
			return Receipt{}, err
		}
		prior, exists := phases[p.Phase]
		if !p.Completed && exists || p.Completed && (!exists || prior.Completed) {
			return Receipt{}, ErrConflict
		}
		if p.Completed && !samePhaseIntent(prior, p) {
			return Receipt{}, ErrConflict
		}
		if _, err := tx.Exec(`UPDATE managed_jobs SET revision=revision+1 WHERE id=?`, p.Identity.JobID); err != nil {
			return Receipt{}, err
		}
		kind := "phase_intent"
		if p.Completed {
			kind = "phase_observed"
		}
		cursor, err := addEvent(tx, p.Identity.JobID, kind, p)
		if err != nil {
			return Receipt{}, err
		}
		r, err := l.snapshotTx(tx, p.Identity.JobID)
		r.EventCursor = cursor
		return r, err
	})
}

func validLocalPhase(p LocalPhase) bool {
	if (p.Phase != "collect" || !p.Completed) && (len(p.Logs) > 0 || len(p.EvidenceGaps) > 0) {
		return false
	}
	if len(p.EvidenceGaps) > 0 && p.EvidenceCompleteness == "complete" {
		return false
	}
	if len(p.Logs) > 1 {
		return false
	}
	for _, a := range p.Logs {
		if a.Path != "output.log" || a.SizeBytes < 0 || !validDigest(a.SHA256) {
			return false
		}
	}
	for _, g := range p.EvidenceGaps {
		if !ValidArtifactPath(g.Path) || g.Reason == "" || g.Offset < 0 || g.DroppedBytes < 0 {
			return false
		}
	}
	if owner := p.ControllerOwner; owner != nil && (owner.BootID == "" || owner.PID <= 0 || owner.PGID <= 0 || owner.SID <= 0 || owner.StartedSeconds <= 0 || owner.StartedMicros < 0 || owner.StartedMicros >= 1000000) {
		return false
	}
	if p.Schema != "haepari.phase-receipt/v1" ||
		!oneOf(p.Phase, "prepare", "run", "collect", "cleanup", "verify") || p.StartedAt.IsZero() || p.ObservedAt.IsZero() ||
		(p.Phase != "verify" && (p.BudgetSeconds <= 0 || p.Deadline.IsZero())) ||
		(p.Phase == "verify" && (p.BudgetSeconds != 0 || !p.Deadline.IsZero())) ||
		(p.Completed && !oneOf(p.Outcome, "complete", "failed", "late", "unproven", "not_needed")) ||
		(!p.Completed && (p.Outcome != "" || len(p.ProtectedEvidence) != 0 || len(p.Artifacts) != 0 || p.EvidenceCompleteness != "")) ||
		(p.Phase != "collect" && (len(p.Artifacts) != 0 || p.EvidenceCompleteness != "")) ||
		(p.Phase == "collect" && p.Completed && !oneOf(string(p.EvidenceCompleteness), "complete", "partial")) {
		return false
	}
	for _, e := range p.ProtectedEvidence {
		if !relativePath(e.Path) || e.Reason == "" || e.RecoveryReference == "" {
			return false
		}
	}
	paths := []string{}
	for _, a := range p.Artifacts {
		if !relativePath(a.Path) || a.SizeBytes < 0 || !validDigest(a.SHA256) {
			return false
		}
		paths = append(paths, a.Path)
	}
	if len(paths) > 0 && !uniquePaths(paths) {
		return false
	}
	return !p.ObservedAt.Before(p.StartedAt) && (p.Deadline.IsZero() || !p.Deadline.After(p.StartedAt.Add(time.Duration(p.BudgetSeconds)*time.Second)))
}

func samePhaseIntent(a, b LocalPhase) bool {
	return a.Identity == b.Identity && a.BudgetSeconds == b.BudgetSeconds && a.StartedAt.Equal(b.StartedAt) && a.Deadline.Equal(b.Deadline) && reflect.DeepEqual(a.ControllerOwner, b.ControllerOwner)
}

func localPhasesTx(tx *sql.Tx, jobID string) (map[string]LocalPhase, error) {
	var floor, missing int64
	if err := tx.QueryRow(`SELECT purged_through FROM managed_event_progress WHERE job_id=?`, jobID).Scan(&floor); err != nil {
		return nil, err
	}
	if floor > 0 {
		return nil, ErrResultUnavailable
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM managed_event_owners o LEFT JOIN managed_events e ON e.cursor=o.cursor AND e.job_id=o.job_id WHERE o.job_id=? AND e.cursor IS NULL`, jobID).Scan(&missing); err != nil {
		return nil, err
	}
	if missing != 0 {
		return nil, ErrBlocked
	}
	rows, err := tx.Query(`SELECT kind,payload_json FROM managed_events WHERE job_id=? AND kind IN ('phase_intent','phase_observed') ORDER BY cursor`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	phases := map[string]LocalPhase{}
	for rows.Next() {
		var raw []byte
		var kind string
		if err := rows.Scan(&kind, &raw); err != nil {
			return nil, err
		}
		var p LocalPhase
		if err := decodeClosed(raw, &p); err != nil {
			return nil, err
		}
		if !validLocalPhase(p) || p.Identity.JobID != jobID || p.Completed != (kind == "phase_observed") {
			return nil, ErrBlocked
		}
		prior, exists := phases[p.Phase]
		if !p.Completed && exists || p.Completed && (!exists || prior.Completed || !samePhaseIntent(prior, p)) {
			return nil, ErrBlocked
		}
		phases[p.Phase] = p
	}
	return phases, rows.Err()
}

// LocalPhases supplies restart consumers with recorded facts, never permission
// to repeat an interrupted effect. A missing completion remains unproven.
func (l *Ledger) LocalPhases(jobID string) (map[string]LocalPhase, error) {
	tx, err := l.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := l.snapshotTx(tx, jobID); err != nil {
		return nil, err
	}
	return localPhasesTx(tx, jobID)
}

func resultTimingsTx(tx *sql.Tx, jobID string) (*ResultTimings, error) {
	var admitted int64
	if err := tx.QueryRow(`SELECT admitted_at_ns FROM managed_jobs WHERE id=?`, jobID).Scan(&admitted); err != nil {
		return nil, err
	}
	timings := &ResultTimings{AdmittedAt: time.Unix(0, admitted).UTC().Format(time.RFC3339Nano), Phases: []ResultPhase{}, AdapterSteps: []ResultStep{}}
	var reserved []byte
	err := tx.QueryRow(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='reserved' ORDER BY cursor LIMIT 1`, jobID).Scan(&reserved)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if len(reserved) > 0 {
		var value struct {
			ReservedAt string `json:"reserved_at"`
		}
		if err := json.Unmarshal(reserved, &value); err != nil {
			return nil, err
		}
		timings.ReservedAt = value.ReservedAt
	}
	phases, err := effectiveLocalPhasesTx(tx, jobID)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"prepare", "run", "collect", "cleanup", "verify"} {
		p, exists := phases[name]
		if !exists {
			continue
		}
		if !p.Completed || p.Outcome == "unproven" {
			return nil, ErrBlocked
		}
		timings.Phases = append(timings.Phases, ResultPhase{Phase: name, StartedAt: p.StartedAt.UTC().Format(time.RFC3339Nano), EndedAt: p.ObservedAt.UTC().Format(time.RFC3339Nano)})
	}
	return timings, nil
}

func validatePhaseResultTx(tx *sql.Tx, jobID string, result Result) error {
	phases, err := effectiveLocalPhasesTx(tx, jobID)
	if err != nil {
		return err
	}
	// Historical ledger-only Jobs have no phase receipts. Their separate
	// termination proof remains mandatory; observed new phases cannot be ignored.
	if len(phases) == 0 {
		return nil
	}
	if result.CleanupOutcome == "not_needed" {
		if !phases["verify"].Completed || phases["verify"].Outcome != "complete" || len(result.Artifacts) != 0 || len(result.ProtectedEvidence) != 0 || result.EvidenceCompleteness != "complete" {
			return ErrBlocked
		}
		return nil // validateLocalOutcomeTx separately requires NoOwnedEffect.
	}
	for _, name := range []string{"collect", "cleanup", "verify"} {
		if !phases[name].Completed || phases[name].Outcome == "unproven" {
			return ErrBlocked
		}
	}
	collect, cleanup := phases["collect"], phases["cleanup"]
	if result.EvidenceCompleteness != collect.EvidenceCompleteness || !slices.Equal(result.Artifacts, collect.Artifacts) ||
		!slices.Equal(result.Logs, collect.Logs) || !slices.Equal(result.EvidenceGaps, collect.EvidenceGaps) ||
		!slices.Equal(result.ProtectedEvidence, cleanup.ProtectedEvidence) {
		return ErrInvalid
	}
	expected := map[string]CleanupOutcome{"complete": "verified", "not_needed": "not_needed", "failed": "failed", "late": "failed"}[cleanup.Outcome]
	if result.CleanupOutcome != expected {
		return ErrInvalid
	}
	return nil
}
