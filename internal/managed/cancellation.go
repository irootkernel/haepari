package managed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

type CancelRequest struct {
	JobID            string `json:"job_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	MutationKey      string `json:"mutation_key"`
}

// CancellationStatus describes durable intent, independently of termination.
// BeforeOutcome is fixed by the first request; it is not a terminal verdict.
type CancellationStatus struct {
	Requested         bool   `json:"requested"`
	MutationKey       string `json:"mutation_key,omitempty"`
	Revision          int64  `json:"revision,omitempty"`
	BeforeOutcome     bool   `json:"before_outcome"`
	Deferred          bool   `json:"deferred"`
	TerminationProven bool   `json:"termination_proven"`
}

type CancelReceipt struct {
	Receipt      Receipt            `json:"receipt"`
	Mutation     Receipt            `json:"mutation"`
	Cancellation CancellationStatus `json:"cancellation"`
}

var cancellationTables = []string{"managed_cancellations", "managed_cancel_edges", "managed_local_outcomes"}

// The caller owns the schema6 stamp and the enclosing migration transaction.
func initCancellationTx(tx *sql.Tx, version int) error {
	if version >= 6 {
		return checkCancellationSchemaTx(tx)
	}
	for _, statement := range []string{
		`CREATE TABLE managed_cancellations(job_id TEXT PRIMARY KEY REFERENCES managed_admissions(job_id), mutation_key TEXT NOT NULL, revision INTEGER NOT NULL, before_outcome INTEGER NOT NULL CHECK(before_outcome IN (0,1)), deferred INTEGER NOT NULL CHECK(deferred IN (0,1)))`,
		`CREATE TABLE managed_cancel_edges(job_id TEXT NOT NULL REFERENCES managed_jobs(id), from_revision INTEGER NOT NULL, to_revision INTEGER NOT NULL CHECK(to_revision=from_revision+1), from_phase TEXT NOT NULL, to_phase TEXT NOT NULL, identity_json BLOB NOT NULL, PRIMARY KEY(job_id,to_revision))`,
		`CREATE TABLE managed_local_outcomes(job_id TEXT PRIMARY KEY REFERENCES managed_jobs(id), identity_json BLOB NOT NULL, outcome_json BLOB NOT NULL, proof_json BLOB)`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return checkCancellationSchemaTx(tx)
}

func checkCancellationSchemaTx(tx *sql.Tx) error {
	for _, query := range []string{
		`SELECT job_id,mutation_key,revision,before_outcome,deferred FROM managed_cancellations LIMIT 0`,
		`SELECT job_id,from_revision,to_revision,from_phase,to_phase,identity_json FROM managed_cancel_edges LIMIT 0`,
		`SELECT job_id,identity_json,outcome_json,proof_json FROM managed_local_outcomes LIMIT 0`,
	} {
		if _, err := tx.Exec(query); err != nil {
			return fmt.Errorf("managed schema6 cancellation: %w", err)
		}
	}
	return nil
}

func cancellationStatusTx(tx *sql.Tx, jobID string) (CancellationStatus, error) {
	var status CancellationStatus
	err := tx.QueryRow(`SELECT mutation_key,revision,before_outcome,deferred FROM managed_cancellations WHERE job_id=?`, jobID).Scan(&status.MutationKey, &status.Revision, &status.BeforeOutcome, &status.Deferred)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return status, err
	}
	status.Requested = err == nil
	var proof []byte
	err = tx.QueryRow(`SELECT proof_json FROM managed_local_outcomes WHERE job_id=?`, jobID).Scan(&proof)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return status, err
	}
	status.TerminationProven = len(proof) > 0
	if !status.TerminationProven {
		var terminal bool
		err := tx.QueryRow(`SELECT state='finished' AND result_json IS NOT NULL FROM managed_jobs WHERE id=?`, jobID).Scan(&terminal)
		if errors.Is(err, sql.ErrNoRows) {
			var data []byte
			if err := tx.QueryRow(`SELECT tombstone_receipt FROM managed_admissions WHERE job_id=? AND retention_state='unavailable'`, jobID).Scan(&data); err != nil {
				return status, mapNoRows(err)
			}
			var tombstone Receipt
			if err := json.Unmarshal(data, &tombstone); err != nil {
				return status, err
			}
			if tombstone.JobID != jobID || tombstone.JobState != "finished" || tombstone.ResultStatus != "unavailable" {
				return status, fmt.Errorf("cancellation lost retained terminal identity")
			}
			terminal = true
		} else if err != nil {
			return status, err
		}
		status.TerminationProven = terminal
	}
	return status, nil
}

func (l *Ledger) Cancellation(jobID string) (CancellationStatus, error) {
	if !identifier.MatchString(jobID) {
		return CancellationStatus{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CancellationStatus{}, err
	}
	defer tx.Rollback()
	if _, err = l.snapshotTx(tx, jobID); err != nil {
		return CancellationStatus{}, err
	}
	return cancellationStatusTx(tx, jobID)
}

// Cancel serializes the queued/reserved decision with Reserve in SQLite.
// Only the runner supplies termination or filesystem-effect proofs.
func (l *Ledger) Cancel(q CancelRequest) (CancelReceipt, error) {
	if !identifier.MatchString(q.JobID) || q.ExpectedRevision <= 0 || !ValidRequestKey(q.MutationKey) {
		return CancelReceipt{}, ErrInvalid
	}
	encoded, _ := json.Marshal(struct {
		Kind    string
		Request CancelRequest
	}{"cancel", q})
	h := sha256.Sum256(encoded)
	digest := hex.EncodeToString(h[:])
	// Public cancellation keys cannot occupy the runner's fixed mutation keys.
	// Keep the submitted key in the digest and durable intent unchanged.
	storageKey := "cancel:" + q.MutationKey
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return CancelReceipt{}, err
	}
	defer tx.Rollback()
	var priorDigest string
	var prior []byte
	var mutation Receipt
	err = tx.QueryRow(`SELECT digest,receipt_json FROM managed_mutations WHERE mutation_key=?`, storageKey).Scan(&priorDigest, &prior)
	if err == nil {
		if priorDigest != digest {
			return CancelReceipt{}, ErrConflict
		}
		if err = json.Unmarshal(prior, &mutation); err != nil {
			return CancelReceipt{}, err
		}
	} else {
		if !errors.Is(err, sql.ErrNoRows) {
			return CancelReceipt{}, err
		}
		current, err := l.snapshotTx(tx, q.JobID)
		if err != nil {
			return CancelReceipt{}, err
		}
		// Finished results are immutable, even when the caller's revision is old.
		if current.JobState != "finished" {
			if current.JobRevision != q.ExpectedRevision {
				return CancelReceipt{}, ErrStale
			}
			identity, err := localIdentityTx(tx, q.JobID)
			if err != nil {
				return CancelReceipt{}, err
			}
			var outcomeCount int
			if err = tx.QueryRow(`SELECT COUNT(*) FROM managed_local_outcomes WHERE job_id=?`, q.JobID).Scan(&outcomeCount); err != nil {
				return CancelReceipt{}, err
			}
			before := outcomeCount == 0 && oneOf(current.JobState, "queued", "preparing", "ready", "running", "awaiting_manual", "cancelling")
			deferred := current.JobState == "reconciling" && outcomeCount == 0
			next := current.JobState
			switch current.JobState {
			case "queued":
				next = "finished"
			case "preparing", "ready":
				next = "collecting" // conservative possible effects; runner proves no-start before cleanup
			case "running", "awaiting_manual":
				next = "cancelling"
			case "cancelling", "collecting", "cleaning", "settling", "reconciling":
			default:
				return CancelReceipt{}, ErrConflict
			}
			if _, err = tx.Exec(`INSERT OR IGNORE INTO managed_cancellations(job_id,mutation_key,revision,before_outcome,deferred) VALUES(?,?,?,?,?)`, q.JobID, q.MutationKey, current.JobRevision+1, before, deferred); err != nil {
				return CancelReceipt{}, err
			}
			if _, err = tx.Exec(`UPDATE managed_jobs SET state=?,revision=revision+1 WHERE id=? AND revision=?`, next, q.JobID, current.JobRevision); err != nil {
				return CancelReceipt{}, err
			}
			if _, err = tx.Exec(`INSERT INTO managed_cancel_edges VALUES(?,?,?,?,?,?)`, q.JobID, current.JobRevision, current.JobRevision+1, current.JobState, next, identity); err != nil {
				return CancelReceipt{}, err
			}
			if next == "finished" {
				var owner LocalIdentity
				if err = json.Unmarshal(identity, &owner); err != nil {
					return CancelReceipt{}, err
				}
				verdict, err := DeriveVerdict(ResultFacts{WorkloadOutcome: "cancelled", EvidenceCompleteness: "complete", CleanupOutcome: "not_needed", ManualEligible: true, CancelledBeforeOutcome: true, TerminationProven: true, NoOwnedEffect: true})
				if err != nil {
					return CancelReceipt{}, err
				}
				timings, err := resultTimingsTx(tx, q.JobID)
				if err != nil {
					return CancelReceipt{}, err
				}
				result := Result{Schema: "haepari.result/v1", JobID: q.JobID, ResourceID: current.ResourceID, ResourceRevision: owner.ConfigRevision, JobState: "finished", ResourceAvailability: current.ResourceAvailability, WorkloadOutcome: "cancelled", EvidenceCompleteness: "complete", CleanupOutcome: "not_needed", FinalVerdict: verdict, Reason: "cancelled", Timings: timings}
				data, _ := json.Marshal(result)
				if _, err = tx.Exec(`UPDATE managed_jobs SET result_json=? WHERE id=?`, data, q.JobID); err != nil {
					return CancelReceipt{}, err
				}
				if _, err = tx.Exec(`DELETE FROM managed_queue_blockers WHERE job_id=?`, q.JobID); err != nil {
					return CancelReceipt{}, err
				}
			}
			cursor, err := addEvent(tx, q.JobID, "cancel_requested", map[string]any{"from": current.JobState, "to": next, "before_outcome": before, "deferred": deferred})
			if err != nil {
				return CancelReceipt{}, err
			}
			mutation, err = l.snapshotTx(tx, q.JobID)
			if err != nil {
				return CancelReceipt{}, err
			}
			mutation.EventCursor = cursor
		} else {
			mutation = current
		}
		data, _ := json.Marshal(mutation)
		if _, err = tx.Exec(`INSERT INTO managed_mutations VALUES(?,?,?)`, storageKey, digest, data); err != nil {
			return CancelReceipt{}, err
		}
		if err = l.commit(tx); err != nil {
			return CancelReceipt{}, err
		}
		// The committed transaction is closed. Read current replay state together.
		return l.cancelReceipt(q.JobID, mutation)
	}
	current, err := l.snapshotTx(tx, q.JobID)
	if err != nil {
		return CancelReceipt{}, err
	}
	status, err := cancellationStatusTx(tx, q.JobID)
	return CancelReceipt{Receipt: current, Mutation: mutation, Cancellation: status}, err
}

func (l *Ledger) cancelReceipt(jobID string, mutation Receipt) (CancelReceipt, error) {
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CancelReceipt{}, err
	}
	defer tx.Rollback()
	current, err := l.snapshotTx(tx, jobID)
	if err != nil {
		return CancelReceipt{}, err
	}
	status, err := cancellationStatusTx(tx, jobID)
	return CancelReceipt{Receipt: current, Mutation: mutation, Cancellation: status}, err
}

type LocalIdentity struct {
	JobID           string
	ResourceID      string
	ConfigRevision  int64
	Slot            int
	LeaseGeneration int64
	RunID           string
	Nonce           string
}

func localIdentityTx(tx *sql.Tx, jobID string) ([]byte, error) {
	var id LocalIdentity
	var slot sql.NullInt64
	var lease sql.NullInt64
	err := tx.QueryRow(`SELECT j.id,j.resource_id,j.pinned_config_revision,j.slot,j.lease_generation,COALESCE(r.run_id,''),COALESCE(n.nonce,'') FROM managed_jobs j LEFT JOIN managed_runs r ON r.job_id=j.id LEFT JOIN managed_parent_nonces n ON n.job_id=j.id AND n.lease_generation=j.lease_generation WHERE j.id=?`, jobID).Scan(&id.JobID, &id.ResourceID, &id.ConfigRevision, &slot, &lease, &id.RunID, &id.Nonce)
	if err != nil {
		return nil, mapNoRows(err)
	}
	id.Slot = int(slot.Int64)
	id.LeaseGeneration = lease.Int64
	return json.Marshal(id)
}

func (l *Ledger) LocalIdentity(jobID string) (LocalIdentity, error) {
	var id LocalIdentity
	if !identifier.MatchString(jobID) {
		return id, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return id, err
	}
	defer tx.Rollback()
	data, err := localIdentityTx(tx, jobID)
	if err != nil {
		return id, err
	}
	err = json.Unmarshal(data, &id)
	return id, err
}

// Rebase only a complete chain of cancellation-only, same-phase revisions.
// A phase change, outcome record, recovery or new owner leaves a gap and fails.
func localRevisionTx(tx *sql.Tx, jobID string, revision int64, phase string) (int64, error) {
	var current int64
	var state string
	if err := tx.QueryRow(`SELECT revision,state FROM managed_jobs WHERE id=?`, jobID).Scan(&current, &state); err != nil {
		return 0, mapNoRows(err)
	}
	if current == revision {
		if state != phase {
			return 0, ErrConflict
		}
		return current, nil
	}
	if current < revision || state != phase {
		return 0, ErrStale
	}
	identity, err := localIdentityTx(tx, jobID)
	if err != nil {
		return 0, err
	}
	rows, err := tx.Query(`SELECT from_revision,to_revision,from_phase,to_phase,identity_json FROM managed_cancel_edges WHERE job_id=? AND to_revision>? AND to_revision<=? ORDER BY to_revision`, jobID, revision, current)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	next := revision
	for rows.Next() {
		var from, to int64
		var a, b string
		var owner []byte
		if err = rows.Scan(&from, &to, &a, &b, &owner); err != nil {
			return 0, err
		}
		if from != next || to != next+1 || a != phase || b != phase || string(owner) != string(identity) {
			return 0, ErrStale
		}
		next = to
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	if next != current {
		return 0, ErrStale
	}
	return current, nil
}

type LocalOutcome struct {
	Started         bool            `json:"started"`
	WorkloadOutcome WorkloadOutcome `json:"workload_outcome"`
	NativeExitCode  *int            `json:"native_exit_code,omitempty"`
	NativeSignal    string          `json:"native_signal,omitempty"`
}
type LocalOutcomeRequest struct {
	Identity         LocalIdentity
	ExpectedRevision int64
	Outcome          LocalOutcome
	MutationKey      string
}

func matchLocalIdentityTx(tx *sql.Tx, id LocalIdentity) error {
	actual, err := localIdentityTx(tx, id.JobID)
	if err != nil {
		return err
	}
	expected, _ := json.Marshal(id)
	if string(actual) != string(expected) || id.LeaseGeneration <= 0 || id.Nonce == "" {
		return ErrStale
	}
	return nil
}

// RecordLocalOutcome freezes the observed native result before collection or
// cleanup. Native child exit alone does not prove the owned tree stopped.
func (l *Ledger) RecordLocalOutcome(q LocalOutcomeRequest) (Receipt, error) {
	if q.ExpectedRevision <= 0 || (q.Outcome.Started && !identifier.MatchString(q.Identity.RunID)) ||
		(!q.Outcome.Started && (q.Outcome.NativeExitCode != nil || q.Outcome.NativeSignal != "" || !oneOf(string(q.Outcome.WorkloadOutcome), "inconclusive", "cancelled"))) {
		return Receipt{}, ErrInvalid
	}
	if !oneOf(string(q.Outcome.WorkloadOutcome), "pass", "fail", "timed_out", "inconclusive", "cancelled") || (q.Outcome.NativeExitCode != nil && (*q.Outcome.NativeExitCode < 0 || *q.Outcome.NativeExitCode > 255 || q.Outcome.NativeSignal != "")) {
		return Receipt{}, ErrInvalid
	}
	return l.mutate("local_outcome", q.MutationKey, q, func(tx *sql.Tx) (Receipt, error) {
		if err := matchLocalIdentityTx(tx, q.Identity); err != nil {
			return Receipt{}, err
		}
		current, err := l.snapshotTx(tx, q.Identity.JobID)
		if err != nil {
			return Receipt{}, err
		}
		if !oneOf(current.JobState, "running", "cancelling", "collecting") {
			return Receipt{}, ErrConflict
		}
		if _, err = localRevisionTx(tx, q.Identity.JobID, q.ExpectedRevision, current.JobState); err != nil {
			return Receipt{}, err
		}
		status, err := cancellationStatusTx(tx, q.Identity.JobID)
		if err != nil {
			return Receipt{}, err
		}
		outcome := q.Outcome
		if status.Requested && status.BeforeOutcome {
			outcome.WorkloadOutcome = "cancelled"
		}
		id, _ := json.Marshal(q.Identity)
		data, _ := json.Marshal(outcome)
		var count int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM managed_local_outcomes WHERE job_id=?`, q.Identity.JobID).Scan(&count); err != nil {
			return Receipt{}, err
		}
		if count != 0 {
			return Receipt{}, ErrConflict
		}
		if _, err = tx.Exec(`INSERT INTO managed_local_outcomes(job_id,identity_json,outcome_json) VALUES(?,?,?)`, q.Identity.JobID, id, data); err != nil {
			return Receipt{}, err
		}
		if _, err = tx.Exec(`UPDATE managed_jobs SET revision=revision+1 WHERE id=?`, q.Identity.JobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, q.Identity.JobID, "workload_observed", outcome)
		if err != nil {
			return Receipt{}, err
		}
		r, err := l.snapshotTx(tx, q.Identity.JobID)
		r.EventCursor = cursor
		return r, err
	})
}

func (l *Ledger) LocalOutcome(jobID string) (LocalOutcome, error) {
	var out LocalOutcome
	var data []byte
	err := l.db.QueryRow(`SELECT outcome_json FROM managed_local_outcomes WHERE job_id=?`, jobID).Scan(&data)
	if err != nil {
		return out, mapNoRows(err)
	}
	err = json.Unmarshal(data, &out)
	return out, err
}

type LocalTerminationRequest struct {
	Identity         LocalIdentity
	ExpectedRevision int64
	NoOwnedEffect    bool
	MutationKey      string
}

type LocalPreparationStopRequest struct {
	Identity         LocalIdentity
	ExpectedRevision int64
	Reason           string
	NoOwnedEffect    bool
	MutationKey      string
}

// RecordLocalPreparationStop records a completed local preparer, not a timed
// out observer. No run claim can exist, and the controller must have observed
// the worker stop before classifying its owned filesystem effects.
func (l *Ledger) RecordLocalPreparationStop(q LocalPreparationStopRequest) (Receipt, error) {
	if q.Identity.RunID != "" {
		return Receipt{}, ErrInvalid
	}
	return l.recordPreparationStop(q, "local")
}
func (l *Ledger) RecordVMPreparationStop(q LocalPreparationStopRequest) (Receipt, error) {
	if !identifier.MatchString(q.Identity.RunID) {
		return Receipt{}, ErrInvalid
	}
	return l.recordPreparationStop(q, "utm-macos")
}
func (l *Ledger) recordPreparationStop(q LocalPreparationStopRequest, expectedKind string) (Receipt, error) {
	if q.ExpectedRevision <= 0 || !oneOf(q.Reason, "prepare_failed", "prepare_deadline", "cancel_before_start") {
		return Receipt{}, ErrInvalid
	}
	return l.mutate("local_preparation_stop", q.MutationKey, q, func(tx *sql.Tx) (Receipt, error) {
		if err := matchLocalIdentityTx(tx, q.Identity); err != nil {
			return Receipt{}, err
		}
		current, err := l.snapshotTx(tx, q.Identity.JobID)
		if err != nil {
			return Receipt{}, err
		}
		if !oneOf(current.JobState, "preparing", "ready", "collecting") {
			return Receipt{}, ErrConflict
		}
		if _, err := localRevisionTx(tx, q.Identity.JobID, q.ExpectedRevision, current.JobState); err != nil {
			return Receipt{}, err
		}
		var kind string
		if err := tx.QueryRow(`SELECT kind FROM managed_resources WHERE id=?`, q.Identity.ResourceID).Scan(&kind); err != nil {
			return Receipt{}, err
		}
		if kind != expectedKind {
			return Receipt{}, ErrUnsupported
		}
		if kind == "local" && q.Identity.RunID != "" {
			return Receipt{}, ErrInvalid
		}
		var claims int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM managed_runs WHERE job_id=?`, q.Identity.JobID).Scan(&claims); err != nil {
			return Receipt{}, err
		}
		if kind == "local" && claims != 0 {
			return Receipt{}, ErrBlocked
		}
		if kind == "utm-macos" {
			var launched int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM managed_runner_receipts WHERE job_id=? AND kind IN ('intent','start','outcome','exit')`, q.Identity.JobID).Scan(&launched); err != nil {
				return Receipt{}, err
			}
			if launched != 0 {
				return Receipt{}, ErrBlocked
			}
		}
		status, err := cancellationStatusTx(tx, q.Identity.JobID)
		if err != nil {
			return Receipt{}, err
		}
		outcome := LocalOutcome{WorkloadOutcome: "inconclusive"}
		if status.Requested && status.BeforeOutcome {
			outcome.WorkloadOutcome = "cancelled"
		} else if q.Reason == "cancel_before_start" {
			return Receipt{}, ErrConflict
		}
		identity, _ := json.Marshal(q.Identity)
		data, _ := json.Marshal(outcome)
		proof, _ := json.Marshal(LocalTerminationRequest{Identity: q.Identity, ExpectedRevision: q.ExpectedRevision, NoOwnedEffect: q.NoOwnedEffect, MutationKey: q.MutationKey})
		if _, err := tx.Exec(`INSERT INTO managed_local_outcomes(job_id,identity_json,outcome_json,proof_json) VALUES(?,?,?,?)`, q.Identity.JobID, identity, data, proof); err != nil {
			return Receipt{}, err
		}
		next := "collecting"
		if q.NoOwnedEffect {
			next = "settling"
		}
		if _, err := tx.Exec(`UPDATE managed_jobs SET state=?,revision=revision+1 WHERE id=?`, next, q.Identity.JobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, q.Identity.JobID, "preparation_stopped", map[string]any{"reason": q.Reason, "outcome": outcome, "no_owned_effect": q.NoOwnedEffect, "identity": q.Identity})
		if err != nil {
			return Receipt{}, err
		}
		r, err := l.snapshotTx(tx, q.Identity.JobID)
		r.EventCursor = cursor
		return r, err
	})
}

func (l *Ledger) CompleteLocalRun(q LocalTerminationRequest) (Receipt, error) {
	if q.ExpectedRevision <= 0 {
		return Receipt{}, ErrInvalid
	}
	return l.mutate("local_termination", q.MutationKey, q, func(tx *sql.Tx) (Receipt, error) {
		if err := matchLocalIdentityTx(tx, q.Identity); err != nil {
			return Receipt{}, err
		}
		current, err := l.snapshotTx(tx, q.Identity.JobID)
		if err != nil {
			return Receipt{}, err
		}
		if !oneOf(current.JobState, "running", "cancelling", "collecting") {
			return Receipt{}, ErrConflict
		}
		if _, err = localRevisionTx(tx, q.Identity.JobID, q.ExpectedRevision, current.JobState); err != nil {
			return Receipt{}, err
		}
		var data, identity, priorProof []byte
		if err = tx.QueryRow(`SELECT outcome_json,identity_json,proof_json FROM managed_local_outcomes WHERE job_id=?`, q.Identity.JobID).Scan(&data, &identity, &priorProof); err != nil {
			return Receipt{}, mapNoRows(err)
		}
		expected, _ := json.Marshal(q.Identity)
		if string(identity) != string(expected) {
			return Receipt{}, ErrStale
		}
		if len(priorProof) > 0 {
			return Receipt{}, ErrConflict
		}
		var out LocalOutcome
		if err = json.Unmarshal(data, &out); err != nil {
			return Receipt{}, err
		}
		if q.NoOwnedEffect && out.Started {
			return Receipt{}, ErrInvalid
		}
		proof, _ := json.Marshal(q)
		if _, err = tx.Exec(`UPDATE managed_local_outcomes SET proof_json=? WHERE job_id=? AND proof_json IS NULL`, proof, q.Identity.JobID); err != nil {
			return Receipt{}, err
		}
		if _, err = tx.Exec(`UPDATE managed_jobs SET state='collecting',revision=revision+1 WHERE id=?`, q.Identity.JobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, q.Identity.JobID, "termination_proven", q)
		if err != nil {
			return Receipt{}, err
		}
		r, err := l.snapshotTx(tx, q.Identity.JobID)
		r.EventCursor = cursor
		return r, err
	})
}

func validateLocalOutcomeTx(tx *sql.Tx, jobID string, result Result) error {
	var data, proof, identity []byte
	err := tx.QueryRow(`SELECT outcome_json,proof_json,identity_json FROM managed_local_outcomes WHERE job_id=?`, jobID).Scan(&data, &proof, &identity)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBlocked
	}
	if err != nil {
		return err
	}
	if len(proof) == 0 {
		return ErrBlocked
	}
	var out LocalOutcome
	if err = json.Unmarshal(data, &out); err != nil {
		return err
	}
	a, _ := json.Marshal(out.NativeExitCode)
	b, _ := json.Marshal(result.NativeExitCode)
	if out.WorkloadOutcome != result.WorkloadOutcome || string(a) != string(b) || out.NativeSignal != result.NativeSignal {
		return ErrInvalid
	}
	if noStartResult(result) && out.Started {
		return ErrInvalid
	}
	if result.Reason == "cancel_before_start" && (out.Started || out.WorkloadOutcome != "cancelled" || out.NativeExitCode != nil || out.NativeSignal != "") {
		return ErrInvalid
	}
	var p LocalTerminationRequest
	if err = json.Unmarshal(proof, &p); err != nil {
		return err
	}
	expected, _ := json.Marshal(p.Identity)
	if string(identity) != string(expected) {
		return ErrStale
	}
	if err = matchLocalIdentityTx(tx, p.Identity); err != nil {
		return err
	}
	if result.CleanupOutcome == "not_needed" && !p.NoOwnedEffect {
		return ErrInvalid
	}
	return nil
}

// BeginLocalDeadline journals bounded termination without an explicit cancel
// intent. Exact replay never creates another run or repeats a termination edge.
func (l *Ledger) BeginLocalDeadline(id LocalIdentity, revision int64) (Receipt, error) {
	q := struct {
		Identity LocalIdentity
		Revision int64
	}{id, revision}
	return l.mutate("local_deadline", "deadline-"+id.JobID, q, func(tx *sql.Tx) (Receipt, error) {
		if err := matchLocalIdentityTx(tx, id); err != nil {
			return Receipt{}, err
		}
		if _, err := localRevisionTx(tx, id.JobID, revision, "running"); err != nil {
			return Receipt{}, err
		}
		if _, err := tx.Exec(`UPDATE managed_jobs SET state='cancelling',revision=revision+1 WHERE id=?`, id.JobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, id.JobID, "execution_deadline", map[string]any{"termination": "bounded"})
		if err != nil {
			return Receipt{}, err
		}
		r, err := l.snapshotTx(tx, id.JobID)
		r.EventCursor = cursor
		return r, err
	})
}
