package managed

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
)

// ManualSession is private reconciliation authority, never a public wire shape.
// Preparation and human observations are deliberately separate durable facts.
type ManualSession struct {
	Identity         LocalIdentity `json:"identity"`
	InstallationID   string        `json:"installation_id"`
	PrepareDeadline  time.Time     `json:"prepare_deadline"`
	Deadline         time.Time     `json:"deadline"`
	Workspace        string        `json:"workspace"`
	Backend          string        `json:"backend"`
	Preparation      *LocalOutcome `json:"preparation,omitempty"`
	PreparationSteps []ResultStep  `json:"preparation_steps,omitempty"`
	Stopped          bool          `json:"stopped"`
	Legacy           bool          `json:"legacy"`
	AbortReason      string        `json:"abort_reason,omitempty"`
	Completion       *ManualResult `json:"completion,omitempty"`
	Receipt          *Receipt      `json:"receipt,omitempty"`
	Outcome          *LocalOutcome `json:"outcome,omitempty"`
	Unobserved       bool          `json:"unobserved"`
}

func checkManualSchemaTx(tx *sql.Tx) error {
	if _, err := tx.Exec(`SELECT job_id,session_json FROM managed_manual_sessions LIMIT 0`); err != nil {
		return fmt.Errorf("managed schema10 manual sessions: %w", err)
	}
	return nil
}

func initManualSchemaTx(tx *sql.Tx, version int) error {
	if version >= 10 {
		return nil
	}
	if _, err := tx.Exec(`CREATE TABLE managed_manual_sessions(job_id TEXT PRIMARY KEY REFERENCES managed_admissions(job_id),session_json BLOB NOT NULL)`); err != nil {
		return err
	}
	// Legacy holds retain only the actually recorded deadline and ownership.
	// A legacy readiness label cannot manufacture a native preparation proof.
	rows, err := tx.Query(`SELECT job_id,cursor,payload_json FROM managed_events WHERE kind='awaiting_manual' ORDER BY cursor`)
	if err != nil {
		return err
	}
	type legacy struct {
		id       string
		deadline time.Time
	}
	var holds []legacy
	for rows.Next() {
		var id string
		var cursor int64
		var raw []byte
		if err := rows.Scan(&id, &cursor, &raw); err != nil {
			rows.Close()
			return err
		}
		var event struct {
			Deadline time.Time `json:"manual_deadline"`
		}
		if json.Unmarshal(raw, &event) != nil || event.Deadline.IsZero() {
			rows.Close()
			return fmt.Errorf("schema10 legacy manual deadline for Job %s event %d: %w", id, cursor, ErrBlocked)
		}
		holds = append(holds, legacy{id, event.Deadline})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, h := range holds {
		raw, err := localIdentityTx(tx, h.id)
		if err != nil {
			return fmt.Errorf("schema10 legacy manual identity for Job %s: %w", h.id, err)
		}
		var id LocalIdentity
		if json.Unmarshal(raw, &id) != nil {
			return fmt.Errorf("schema10 legacy manual identity for Job %s: %w", h.id, ErrBlocked)
		}
		m := ManualSession{Identity: id, Deadline: h.deadline, Legacy: true}
		data, _ := json.Marshal(m)
		if _, err := tx.Exec(`INSERT INTO managed_manual_sessions(job_id,session_json) VALUES(?,?)`, h.id, data); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version,identity) VALUES(10,'durable-manual-sessions-v10')`); err != nil {
		return err
	}
	_, err = tx.Exec(`PRAGMA user_version=10`)
	return err
}

func manualSessionTx(tx *sql.Tx, jobID string) (ManualSession, error) {
	var m ManualSession
	var raw []byte
	if err := tx.QueryRow(`SELECT session_json FROM managed_manual_sessions WHERE job_id=?`, jobID).Scan(&raw); err != nil {
		return m, mapNoRows(err)
	}
	if err := decodeClosedWithin(raw, &m, 2<<20); err != nil {
		return m, ErrBlocked
	}
	if m.Identity.JobID != jobID || m.Identity.LeaseGeneration <= 0 || m.Identity.Nonce == "" || (!m.Legacy && (m.InstallationID == "" || m.PrepareDeadline.IsZero())) {
		return m, ErrBlocked
	}
	return m, nil
}

func saveManualSessionTx(tx *sql.Tx, m ManualSession) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	result, err := tx.Exec(`UPDATE managed_manual_sessions SET session_json=? WHERE job_id=?`, raw, m.Identity.JobID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrBlocked
	}
	return nil
}

func (l *Ledger) ManualSession(jobID string) (ManualSession, error) {
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ManualSession{}, err
	}
	defer tx.Rollback()
	return manualSessionTx(tx, jobID)
}

func manualJobTx(tx *sql.Tx, jobID string) (JobSpec, error) {
	var raw []byte
	if err := tx.QueryRow(`SELECT spec_json FROM managed_admissions WHERE job_id=?`, jobID).Scan(&raw); err != nil {
		return JobSpec{}, mapNoRows(err)
	}
	job, err := DecodeJob(raw)
	if err != nil {
		return job, err
	}
	if job.Mode != "manual" || job.Manual == nil {
		return job, ErrInvalid
	}
	return job, nil
}

func insertManualSessionTx(tx *sql.Tx, m ManualSession) error {
	if _, err := manualJobTx(tx, m.Identity.JobID); err != nil {
		return err
	}
	if err := matchLocalIdentityTx(tx, m.Identity); err != nil {
		return err
	}
	if m.Identity.RunID == "" || !identifier.MatchString(m.InstallationID) || m.PrepareDeadline.IsZero() {
		return ErrInvalid
	}
	raw, _ := json.Marshal(m)
	_, err := tx.Exec(`INSERT INTO managed_manual_sessions(job_id,session_json) VALUES(?,?)`, m.Identity.JobID, raw)
	return err
}

// ClaimManualLocal creates one preparation identity before the first effect.
func (l *Ledger) ClaimManualLocal(id LocalIdentity, revision int64, deadline time.Time, installation string) (Receipt, error) {
	q := struct {
		Identity     LocalIdentity
		Revision     int64
		Deadline     time.Time
		Installation string
	}{id, revision, deadline, installation}
	return l.mutate("manual_claim", "manual-claim-"+id.JobID, q, func(tx *sql.Tx) (Receipt, error) {
		before := id
		before.RunID = ""
		if !identifier.MatchString(id.RunID) {
			return Receipt{}, ErrInvalid
		}
		if err := matchLocalIdentityTx(tx, before); err != nil {
			return Receipt{}, err
		}
		if _, err := localRevisionTx(tx, id.JobID, revision, "preparing"); err != nil {
			return Receipt{}, err
		}
		if _, err := manualJobTx(tx, id.JobID); err != nil {
			return Receipt{}, err
		}
		var kind string
		if err := tx.QueryRow(`SELECT kind FROM managed_resources WHERE id=?`, id.ResourceID).Scan(&kind); err != nil {
			return Receipt{}, err
		}
		if kind != "local" {
			return Receipt{}, ErrInvalid
		}
		intent, _ := json.Marshal(q)
		if _, err := tx.Exec(`INSERT INTO managed_runs(job_id,run_id,intent_json) VALUES(?,?,?)`, id.JobID, id.RunID, intent); err != nil {
			return Receipt{}, err
		}
		if err := insertManualSessionTx(tx, ManualSession{Identity: id, InstallationID: installation, PrepareDeadline: deadline, Backend: kind}); err != nil {
			return Receipt{}, err
		}
		if _, err := tx.Exec(`UPDATE managed_jobs SET revision=revision+1 WHERE id=?`, id.JobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, id.JobID, "manual_prepare_intent", q)
		if err != nil {
			return Receipt{}, err
		}
		r, err := l.snapshotTx(tx, id.JobID)
		r.EventCursor = cursor
		return r, err
	})
}

func (l *Ledger) BindManualVM(id LocalIdentity, deadline time.Time, installation, workspace string) error {
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m := ManualSession{Identity: id, PrepareDeadline: deadline, InstallationID: installation, Workspace: workspace, Backend: "utm-macos"}
	prior, err := manualSessionTx(tx, id.JobID)
	if err == nil {
		if !reflect.DeepEqual(prior, m) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	if err := insertManualSessionTx(tx, m); err != nil {
		return err
	}
	return l.commit(tx)
}

// RecordManualPreparationSteps preserves adapter observations before the sole
// guest workload starts. It never changes a phase intent or renews a deadline.
func (l *Ledger) RecordManualPreparationSteps(id LocalIdentity, steps []ResultStep) error {
	if len(steps) == 0 || len(steps) > 3 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, step := range steps {
		if !oneOf(step.Step, "vm_start", "ssh_ready", "transfer") || step.DurationMillis < 0 || seen[step.Step] {
			return ErrInvalid
		}
		seen[step.Step] = true
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := matchLocalIdentityTx(tx, id); err != nil {
		return err
	}
	m, err := manualSessionTx(tx, id.JobID)
	if err != nil {
		return err
	}
	if m.Identity != id || m.Legacy || m.Backend != "utm-macos" {
		return ErrStale
	}
	if m.PreparationSteps != nil {
		if !reflect.DeepEqual(m.PreparationSteps, steps) {
			return ErrConflict
		}
		return nil
	}
	if m.Stopped || m.Outcome != nil {
		return ErrBlocked
	}
	m.PreparationSteps = append([]ResultStep{}, steps...)
	if err := saveManualSessionTx(tx, m); err != nil {
		return err
	}
	return l.commit(tx)
}

// RecordManualPreparation imports cessation, not a human conclusion. The
// controller verifies full closed native receipts before their ledger import.
func (l *Ledger) RecordManualPreparation(id LocalIdentity, out LocalOutcome, workspace string) error {
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := matchLocalIdentityTx(tx, id); err != nil {
		return err
	}
	m, err := manualSessionTx(tx, id.JobID)
	if err != nil {
		return err
	}
	if m.Identity != id {
		return ErrStale
	}
	if m.Preparation != nil {
		if !reflect.DeepEqual(*m.Preparation, out) || m.Workspace != workspace || !m.Stopped {
			return ErrConflict
		}
		return nil
	}
	var raw []byte
	err = tx.QueryRow(`SELECT payload_json FROM managed_runner_receipts WHERE job_id=? AND kind='exit'`, id.JobID).Scan(&raw)
	if err == nil {
		var proof struct {
			TerminationProven bool         `json:"termination_proven"`
			Outcome           LocalOutcome `json:"outcome"`
		}
		if json.Unmarshal(raw, &proof) != nil || !proof.TerminationProven || !reflect.DeepEqual(proof.Outcome, out) {
			return ErrBlocked
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM managed_runner_receipts WHERE job_id=?`, id.JobID).Scan(&count); err != nil {
			return err
		}
		phases, err := effectiveLocalPhasesTx(tx, id.JobID)
		if err != nil {
			return err
		}
		p := phases["prepare"]
		if count != 0 || out.Started || !p.Completed || p.Outcome == "unproven" {
			return ErrBlocked
		}
	} else {
		return err
	}
	m.Preparation = &out
	m.Stopped = true
	m.Workspace = workspace
	if err := saveManualSessionTx(tx, m); err != nil {
		return err
	}
	if _, err := addEvent(tx, id.JobID, "manual_preparation_stopped", map[string]any{"outcome": out, "workspace": workspace, "backend": m.Backend}); err != nil {
		return err
	}
	return l.commit(tx)
}

func manualDecisionTx(tx *sql.Tx, m *ManualSession, revision int64, out LocalOutcome, noEffect bool, reason string) error {
	if !m.Stopped || m.Preparation == nil {
		return ErrBlocked
	}
	if err := matchLocalIdentityTx(tx, m.Identity); err != nil {
		return err
	}
	identity, _ := json.Marshal(m.Identity)
	data, _ := json.Marshal(out)
	proof, _ := json.Marshal(LocalTerminationRequest{Identity: m.Identity, ExpectedRevision: revision, NoOwnedEffect: noEffect, MutationKey: "manual-decision-" + m.Identity.JobID})
	if _, err := tx.Exec(`INSERT INTO managed_local_outcomes(job_id,identity_json,outcome_json,proof_json) VALUES(?,?,?,?)`, m.Identity.JobID, identity, data, proof); err != nil {
		return err
	}
	m.Outcome = &out
	m.AbortReason = reason
	if _, err := tx.Exec(`UPDATE managed_jobs SET state=?,revision=revision+1 WHERE id=?`, func() string {
		if noEffect {
			return "settling"
		}
		return "collecting"
	}(), m.Identity.JobID); err != nil {
		return err
	}
	return nil
}

// CompleteManual permanently binds the canonical human record and its original
// receipt. Exact replay is checked before current state, deadline or revision.
func (l *Ledger) CompleteManual(jobID, installation string, raw []byte, expected int64) (Receipt, error) {
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.Rollback()
	job, err := manualJobTx(tx, jobID)
	if err != nil {
		return Receipt{}, err
	}
	result, err := DecodeManualResult(raw, job.Manual.RequiredScenarios)
	if err != nil {
		return Receipt{}, err
	}
	for _, binding := range []struct {
		field   string
		matches bool
	}{
		{"job_id", result.JobID == jobID}, {"installation_id", result.InstallationID == installation}, {"expected_revision", result.ExpectedRevision == expected}, {"profile_id", result.ProfileID == job.Environment.ProfileID}, {"profile_digest", result.ProfileDigest == job.Environment.ProfileDigest},
	} {
		if !binding.matches {
			return Receipt{}, manualResultInvalid(binding.field, "must match the original admitted Job and completion request")
		}
	}
	m, err := manualSessionTx(tx, jobID)
	if errors.Is(err, ErrNotFound) {
		return Receipt{}, &ManualCompletionRefusal{Reason: "not_prepared"}
	}
	if err != nil {
		return Receipt{}, err
	}
	if m.Completion != nil {
		a, _ := json.Marshal(m.Completion)
		b, _ := json.Marshal(result)
		if !bytes.Equal(a, b) || m.Receipt == nil {
			return Receipt{}, ErrConflict
		}
		return *m.Receipt, nil
	}
	if m.Legacy {
		return Receipt{}, &ManualCompletionRefusal{Reason: "legacy"}
	}
	if m.InstallationID != installation {
		return Receipt{}, manualResultInvalid("installation_id", "must match the original manual session")
	}
	if m.AbortReason != "" {
		return Receipt{}, &ManualCompletionRefusal{Reason: "retired"}
	}
	if !m.Stopped || m.Preparation == nil || m.Preparation.WorkloadOutcome != "pass" || m.Deadline.IsZero() {
		return Receipt{}, &ManualCompletionRefusal{Reason: "not_prepared"}
	}
	if !time.Now().Before(m.Deadline) {
		return Receipt{}, &ManualCompletionRefusal{Reason: "deadline"}
	}
	r, err := l.snapshotTx(tx, jobID)
	if err != nil {
		return Receipt{}, err
	}
	if r.JobRevision != expected {
		return Receipt{}, ErrStale
	}
	if r.JobState != "awaiting_manual" {
		return Receipt{}, &ManualCompletionRefusal{Reason: "state"}
	}
	cancel, err := cancellationStatusTx(tx, jobID)
	if err != nil {
		return Receipt{}, err
	}
	if cancel.Requested {
		return Receipt{}, &ManualCompletionRefusal{Reason: "retired"}
	}
	out := LocalOutcome{Started: true, WorkloadOutcome: "pass"}
	for _, s := range result.Scenarios {
		if s.Status == "fail" {
			out.WorkloadOutcome = "fail"
		}
		if s.Status == "unobserved" {
			m.Unobserved = true
		}
	}
	if m.Unobserved && out.WorkloadOutcome == "pass" {
		out.WorkloadOutcome = "inconclusive"
	}
	m.Completion = &result
	if err := manualDecisionTx(tx, &m, r.JobRevision, out, false, ""); err != nil {
		return Receipt{}, err
	}
	cursor, err := addEvent(tx, jobID, "manual_completed", result)
	if err != nil {
		return Receipt{}, err
	}
	r, err = l.snapshotTx(tx, jobID)
	if err != nil {
		return Receipt{}, err
	}
	r.EventCursor = cursor
	m.Receipt = &r
	if err := saveManualSessionTx(tx, m); err != nil {
		return Receipt{}, err
	}
	if err := l.commit(tx); err != nil {
		return Receipt{}, err
	}
	return r, nil
}

// ManualDeadline retires completion authority before any native cancellation.
func (l *Ledger) ManualDeadline(id LocalIdentity, revision int64, reason string) (Receipt, error) {
	if !oneOf(reason, "manual_deadline", "prepare_deadline") {
		return Receipt{}, ErrInvalid
	}
	// A committed abandonment intent survives interruption before the abort
	// commit. Recovery has a new live revision, but must not journal the same
	// permanent mutation key with a different request digest.
	m, err := l.ManualSession(id.JobID)
	if err != nil {
		return Receipt{}, err
	}
	if m.Identity != id {
		return Receipt{}, ErrStale
	}
	if m.Outcome != nil || m.AbortReason == reason {
		return l.Snapshot(id.JobID)
	}
	if m.AbortReason != "" {
		return Receipt{}, ErrConflict
	}
	q := struct {
		Identity LocalIdentity
		Revision int64
		Reason   string
	}{id, revision, reason}
	return l.mutate("manual_deadline", "manual-deadline-"+id.JobID, q, func(tx *sql.Tx) (Receipt, error) {
		m, err := manualSessionTx(tx, id.JobID)
		if err != nil {
			return Receipt{}, err
		}
		if err := matchLocalIdentityTx(tx, id); err != nil {
			return Receipt{}, err
		}
		r, err := l.snapshotTx(tx, id.JobID)
		if err != nil {
			return Receipt{}, err
		}
		if m.Outcome != nil {
			return r, nil
		}
		if r.JobRevision != revision {
			return Receipt{}, ErrStale
		}
		if !oneOf(r.JobState, "preparing", "ready", "awaiting_manual", "cancelling", "reconciling") {
			return Receipt{}, ErrConflict
		}
		m.AbortReason = reason
		if err := saveManualSessionTx(tx, m); err != nil {
			return Receipt{}, err
		}
		if _, err := tx.Exec(`UPDATE managed_jobs SET state='cancelling',revision=revision+1 WHERE id=?`, id.JobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, id.JobID, "manual_abandonment_intent", map[string]any{"reason": reason})
		if err != nil {
			return Receipt{}, err
		}
		r, err = l.snapshotTx(tx, id.JobID)
		r.EventCursor = cursor
		return r, err
	})
}

func (l *Ledger) AbortManual(id LocalIdentity, revision int64, reason string, noEffect bool) (Receipt, error) {
	q := struct {
		Identity LocalIdentity
		Revision int64
		Reason   string
		NoEffect bool
	}{id, revision, reason, noEffect}
	return l.mutate("manual_abort", "manual-abort-"+id.JobID, q, func(tx *sql.Tx) (Receipt, error) {
		m, err := manualSessionTx(tx, id.JobID)
		if err != nil {
			return Receipt{}, err
		}
		r, err := l.snapshotTx(tx, id.JobID)
		if err != nil {
			return Receipt{}, err
		}
		if m.Outcome != nil {
			return r, nil
		}
		if r.JobRevision != revision {
			return Receipt{}, ErrStale
		}
		if !oneOf(r.JobState, "preparing", "ready", "awaiting_manual", "cancelling", "collecting", "reconciling") {
			return Receipt{}, ErrConflict
		}
		if !m.Stopped || m.Preparation == nil {
			return Receipt{}, ErrBlocked
		}
		out := *m.Preparation
		cancel, err := cancellationStatusTx(tx, id.JobID)
		if err != nil {
			return Receipt{}, err
		}
		if cancel.Requested && cancel.BeforeOutcome {
			out.WorkloadOutcome = "cancelled"
		} else if reason == "manual_deadline" || reason == "prepare_deadline" {
			out.WorkloadOutcome = "timed_out"
		} else if out.WorkloadOutcome == "pass" {
			out.WorkloadOutcome = "inconclusive"
		}
		if out.WorkloadOutcome == "cancelled" || out.WorkloadOutcome == "timed_out" {
			out.NativeExitCode = nil
			out.NativeSignal = ""
		}
		m.Unobserved = out.WorkloadOutcome != "cancelled"
		if err := manualDecisionTx(tx, &m, r.JobRevision, out, noEffect, reason); err != nil {
			return Receipt{}, err
		}
		if err := saveManualSessionTx(tx, m); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, id.JobID, "manual_aborted", map[string]any{"reason": reason})
		if err != nil {
			return Receipt{}, err
		}
		r, err = l.snapshotTx(tx, id.JobID)
		r.EventCursor = cursor
		return r, err
	})
}

func validateManualResultTx(tx *sql.Tx, job JobSpec, result Result) error {
	if job.Mode != "manual" || (result.FinalVerdict != Pass && result.FinalVerdict != Fail) {
		return nil
	}
	m, err := manualSessionTx(tx, result.JobID)
	if err != nil {
		return ErrBlocked
	}
	if m.Completion == nil || m.Unobserved || m.Outcome == nil || m.Outcome.WorkloadOutcome != result.WorkloadOutcome {
		return ErrBlocked
	}
	return nil
}
