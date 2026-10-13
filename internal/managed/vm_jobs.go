package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ClaimVMRun binds the sole future execution before guest preparation. Unlike
// a local preparation, the independent guest needs its immutable run identity
// while preparing. No launch is permitted by this claim.
func (l *Ledger) ClaimVMRun(id LocalIdentity, revision int64, plan json.RawMessage) (Receipt, error) {
	if !identifier.MatchString(id.RunID) || len(plan) == 0 || len(plan) > 2<<20 || ValidateUniqueJSONKeys(plan) != nil {
		return Receipt{}, ErrInvalid
	}
	q := struct {
		Identity LocalIdentity
		Revision int64
		Plan     json.RawMessage
	}{id, revision, plan}
	return l.mutate("vm_claim", "vm-claim-"+id.JobID, q, func(tx *sql.Tx) (Receipt, error) {
		before := id
		before.RunID = ""
		if err := matchLocalIdentityTx(tx, before); err != nil {
			return Receipt{}, err
		}
		if _, err := localRevisionTx(tx, id.JobID, revision, "preparing"); err != nil {
			return Receipt{}, err
		}
		var kind string
		if err := tx.QueryRow(`SELECT kind FROM managed_resources WHERE id=?`, id.ResourceID).Scan(&kind); err != nil {
			return Receipt{}, err
		}
		if kind != "utm-macos" {
			return Receipt{}, ErrUnsupported
		}
		if _, err := tx.Exec(`INSERT INTO managed_runs(job_id,run_id,intent_json) VALUES(?,?,?)`, id.JobID, id.RunID, plan); err != nil {
			return Receipt{}, ErrConflict
		}
		if _, err := tx.Exec(`UPDATE managed_jobs SET revision=revision+1 WHERE id=?`, id.JobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, id.JobID, "vm_plan", plan)
		if err != nil {
			return Receipt{}, err
		}
		r, err := l.snapshotTx(tx, id.JobID)
		r.EventCursor = cursor
		return r, err
	})
}

// VMPrepared is an immutable, lease-bound guest directory and native request
// observation. A crash before publication cannot authorize a second prepare.
func (l *Ledger) RecordVMPrepared(id LocalIdentity, payload json.RawMessage) error {
	if len(payload) == 0 || len(payload) > 2<<20 || ValidateUniqueJSONKeys(payload) != nil {
		return ErrInvalid
	}
	var header struct {
		Request json.RawMessage `json:"request"`
	}
	if json.Unmarshal(payload, &header) != nil {
		return ErrInvalid
	}
	kind := "vm_prepared"
	if len(header.Request) == 0 || string(header.Request) == "null" {
		kind = "vm_retained"
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = matchLocalIdentityTx(tx, id); err != nil {
		return err
	}
	var prior []byte
	err = tx.QueryRow(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind=? ORDER BY cursor LIMIT 1`, id.JobID, kind).Scan(&prior)
	if err == nil {
		if string(prior) != string(payload) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = addEvent(tx, id.JobID, kind, payload); err != nil {
		return err
	}
	return l.commit(tx)
}
func (l *Ledger) VMJobBinding(jobID string) (json.RawMessage, json.RawMessage, error) {
	var plan, prepared []byte
	if err := l.db.QueryRow(`SELECT intent_json FROM managed_runs WHERE job_id=?`, jobID).Scan(&plan); err != nil {
		return nil, nil, mapNoRows(err)
	}
	err := l.db.QueryRow(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind IN ('vm_prepared','vm_retained') ORDER BY CASE kind WHEN 'vm_prepared' THEN 0 ELSE 1 END,cursor LIMIT 1`, jobID).Scan(&prepared)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	return plan, prepared, nil
}

// The transport observation is additive private data, separate from frozen
// guest preparation and native runner receipts.
func (l *Ledger) VMTransportBinding(jobID string) (json.RawMessage, error) {
	var raw []byte
	err := l.db.QueryRow(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='vm_transport_binding' ORDER BY cursor LIMIT 1`, jobID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}
func (l *Ledger) RecordVMTransportBinding(id LocalIdentity, raw json.RawMessage) error {
	return l.recordVMTransportFact(id, raw, "vm_transport_binding")
}
func (l *Ledger) VMTransportAdmission(jobID string) (json.RawMessage, error) {
	var raw []byte
	err := l.db.QueryRow(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='vm_transport_admission' ORDER BY cursor LIMIT 1`, jobID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}
func (l *Ledger) RecordVMTransportAdmission(id LocalIdentity, raw json.RawMessage) error {
	return l.recordVMTransportFact(id, raw, "vm_transport_admission")
}
func (l *Ledger) recordVMTransportFact(id LocalIdentity, raw json.RawMessage, kind string) error {
	if len(raw) == 0 || len(raw) > 2<<20 || ValidateUniqueJSONKeys(raw) != nil {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = matchLocalIdentityTx(tx, id); err != nil {
		return err
	}
	var prior []byte
	err = tx.QueryRow(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind=? ORDER BY cursor LIMIT 1`, id.JobID, kind).Scan(&prior)
	if err == nil {
		if string(prior) != string(raw) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = addEvent(tx, id.JobID, kind, raw); err != nil {
		return err
	}
	return l.commit(tx)
}

// Keep at most 4096 measured exchange pairs per Job. Overflow records a durable
// gap once; consumers must reject incomplete coverage instead of extrapolating.
func (l *Ledger) RecordVMSSHMeasurement(id LocalIdentity, raw json.RawMessage, complete bool) error {
	if len(raw) == 0 || len(raw) > 4096 || ValidateUniqueJSONKeys(raw) != nil {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = matchLocalIdentityTx(tx, id); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='vm_ssh_exchange_start'`, id.JobID).Scan(&count); err != nil {
		return err
	}
	if count >= 4096 && !complete || count > 4096 {
		var gap bool
		if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM managed_events WHERE job_id=? AND kind='vm_ssh_measurement_gap')`, id.JobID).Scan(&gap); err != nil {
			return err
		}
		if !gap {
			if _, err = addEvent(tx, id.JobID, "vm_ssh_measurement_gap", map[string]any{"schema": "haepari.ssh-measurement-gap/v1", "reason": "exchange_detail_limit"}); err != nil {
				return err
			}
		}
		return l.commit(tx)
	}
	// Once overflow begins, no more details are admitted, including completion
	// of an unrecorded start. A pending last admitted pair can still complete.
	var gap bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM managed_events WHERE job_id=? AND kind='vm_ssh_measurement_gap')`, id.JobID).Scan(&gap); err != nil {
		return err
	}
	if gap {
		return nil
	}
	kind := "vm_ssh_exchange_start"
	if complete {
		kind = "vm_ssh_exchange_complete"
	}
	if _, err = addEvent(tx, id.JobID, kind, raw); err != nil {
		return err
	}
	return l.commit(tx)
}

func (l *Ledger) StartVM(id LocalIdentity, revision int64) (Receipt, error) {
	q := struct {
		Identity LocalIdentity
		Revision int64
	}{id, revision}
	return l.mutate("vm_start", "start-"+id.JobID, q, func(tx *sql.Tx) (Receipt, error) {
		if err := matchLocalIdentityTx(tx, id); err != nil {
			return Receipt{}, err
		}
		if _, err := localRevisionTx(tx, id.JobID, revision, "ready"); err != nil {
			return Receipt{}, err
		}
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='vm_prepared'`, id.JobID).Scan(&count); err != nil {
			return Receipt{}, err
		}
		if count != 1 {
			return Receipt{}, ErrBlocked
		}
		if _, err := tx.Exec(`UPDATE managed_jobs SET state='running',revision=revision+1 WHERE id=?`, id.JobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, id.JobID, "run_intent", map[string]any{"run_id": id.RunID})
		if err != nil {
			return Receipt{}, err
		}
		r, err := l.snapshotTx(tx, id.JobID)
		r.EventCursor = cursor
		return r, err
	})
}

// VMRetirementDeadline commits a bounded no-start barrier before the guest
// effect. Replay consumes its original allowance and never creates more time.
func (l *Ledger) VMRetirementDeadline(id LocalIdentity, snapshotDigest string, deadline time.Time) (time.Time, error) {
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	if err = matchLocalIdentityTx(tx, id); err != nil {
		return time.Time{}, err
	}
	var raw []byte
	err = tx.QueryRow(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='vm_retire_intent' ORDER BY cursor LIMIT 1`, id.JobID).Scan(&raw)
	var prior struct {
		Identity LocalIdentity `json:"identity"`
		Deadline time.Time     `json:"deadline"`
	}
	if err == nil {
		if decodeClosed(raw, &prior) != nil || prior.Identity != id || prior.Deadline.IsZero() {
			return time.Time{}, ErrBlocked
		}
		return prior.Deadline, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, err
	}
	snap, err := l.recoverySnapshotTx(tx, id.JobID)
	if err != nil {
		return time.Time{}, err
	}
	if RecoveryDigest(snap) != snapshotDigest {
		return time.Time{}, ErrStale
	}
	manualCancelledBeforeStart := false
	if snap.Spec.Mode == "manual" && snap.Receipt.JobState == "collecting" && snap.Outcome == nil {
		cancel, err := cancellationStatusTx(tx, id.JobID)
		if err != nil {
			return time.Time{}, err
		}
		manualCancelledBeforeStart = cancel.Requested && cancel.BeforeOutcome &&
			len(snap.Runner["intent"]) == 0 && len(snap.Runner["start"]) == 0 && len(snap.Runner["outcome"]) == 0 && len(snap.Runner["exit"]) == 0
	}
	if (!oneOf(snap.Receipt.JobState, "preparing", "ready", "reconciling") && !manualCancelledBeforeStart) || deadline.IsZero() {
		return time.Time{}, ErrBlocked
	}
	prior.Identity = id
	prior.Deadline = deadline.UTC()
	if _, err = addEvent(tx, id.JobID, "vm_retire_intent", prior); err != nil {
		return time.Time{}, err
	}
	return prior.Deadline, l.commit(tx)
}
