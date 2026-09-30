package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type QueuedJob struct {
	Receipt              Receipt
	Spec                 JobSpec
	Inventory            []byte
	ObjectID             string
	DeadlineNS           int64
	PinnedConfigRevision int64
}

type ExpiredJob struct {
	JobID    string
	Revision int64
}

func (l *Ledger) NextQueueDeadline() (time.Time, bool, error) {
	var ns sql.NullInt64
	if err := l.db.QueryRow(`SELECT MIN(queue_deadline_ns) FROM managed_jobs WHERE state='queued'`).Scan(&ns); err != nil {
		return time.Time{}, false, err
	}
	if !ns.Valid {
		return time.Time{}, false, nil
	}
	return time.Unix(0, ns.Int64), true, nil
}

func (l *Ledger) FirstExpiredQueued(now time.Time) (ExpiredJob, error) {
	var job ExpiredJob
	err := l.db.QueryRow(`SELECT id,revision FROM managed_jobs WHERE state='queued' AND queue_deadline_ns<=? ORDER BY queue_deadline_ns,rowid LIMIT 1`, now.UTC().UnixNano()).Scan(&job.JobID, &job.Revision)
	return job, mapNoRows(err)
}

// Request returns the immutable admission identity for same-key recovery.
func (l *Ledger) Request(key string) (QueuedJob, error) {
	if !ValidRequestKey(key) {
		return QueuedJob{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return QueuedJob{}, err
	}
	defer tx.Rollback()
	var job QueuedJob
	var spec []byte
	err = tx.QueryRow(`SELECT job_id,spec_json,object_id,inventory_json FROM managed_admissions WHERE request_key=?`, key).Scan(&job.Receipt.JobID, &spec, &job.ObjectID, &job.Inventory)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if err := checkAdmissionLossTx(tx, key); err != nil {
				return job, err
			}
		}
		return job, mapNoRows(err)
	}
	if err := json.Unmarshal(spec, &job.Spec); err != nil {
		return job, err
	}
	job.Receipt, err = l.admissionReceiptTx(tx, job.Receipt.JobID)
	return job, err
}

// NextQueued is used only by the single controller scheduler. Advisory
// selection requires per-resource FIFO, available capacity, global capacity and
// no pending/uncertain power transition. Reserve repeats these guards inside its
// transaction, then checks pinned revisions and deadline. The controller checks
// pinned configuration, input readability and deadline before attempting Reserve.
func (l *Ledger) NextQueued() (QueuedJob, error) {
	var job QueuedJob
	var spec []byte
	err := l.db.QueryRow(`SELECT j.id,j.spec_json,i.object_id,j.queue_deadline_ns,j.pinned_config_revision FROM managed_jobs j JOIN managed_job_inputs i ON i.job_id=j.id JOIN managed_admissions a ON a.job_id=j.id JOIN managed_resources r ON r.id=j.resource_id WHERE j.state='queued' AND r.availability='available' AND r.occupancy<r.capacity AND (SELECT COALESCE(SUM(occupancy),0) FROM managed_resources)<? AND NOT EXISTS(SELECT 1 FROM managed_power_transitions p WHERE p.state IN ('pending','uncertain') AND (p.victim_resource_id=r.id OR p.target_resource_id=r.id)) AND NOT EXISTS(SELECT 1 FROM managed_jobs h JOIN managed_admissions ha ON ha.job_id=h.id WHERE h.resource_id=j.resource_id AND h.state='queued' AND ha.sequence<a.sequence) ORDER BY a.sequence LIMIT 1`, l.globalLimit).
		Scan(&job.Receipt.JobID, &spec, &job.ObjectID, &job.DeadlineNS, &job.PinnedConfigRevision)
	if err != nil {
		return job, mapNoRows(err)
	}
	if err := json.Unmarshal(spec, &job.Spec); err != nil {
		return job, err
	}
	job.Receipt, err = l.Snapshot(job.Receipt.JobID)
	return job, err
}

func (l *Ledger) Input(jobID string) (string, error) {
	var objectID string
	err := l.db.QueryRow(`SELECT object_id FROM managed_job_inputs WHERE job_id=?`, jobID).Scan(&objectID)
	return objectID, mapNoRows(err)
}

// RegisterOnlyLocal keeps the runtime local-only until TASK-016 qualifies UTM.
func (l *Ledger) RegisterOnlyLocal(data []byte) error {
	r, err := DecodeResource(data)
	if err != nil {
		return err
	}
	if r.Kind != "local" {
		return ErrUnsupported
	}
	return l.RegisterResource(data)
}

// AdvanceLocal records a phase receipt before the next external effect.
func (l *Ledger) AdvanceLocal(jobID string, revision int64, from, to, key string) (Receipt, error) {
	valid := from == "preparing" && to == "ready" || from == "running" && to == "collecting" ||
		from == "collecting" && to == "cleaning" || from == "cleaning" && to == "settling"
	if !valid {
		return Receipt{}, ErrInvalid
	}
	q := struct {
		JobID    string
		Revision int64
		From, To string
	}{jobID, revision, from, to}
	return l.mutate("local_phase", key, q, func(tx *sql.Tx) (Receipt, error) {
		var state string
		var current int64
		if err := tx.QueryRow(`SELECT state,revision FROM managed_jobs WHERE id=?`, jobID).Scan(&state, &current); err != nil {
			return Receipt{}, mapNoRows(err)
		}
		if _, err := localRevisionTx(tx, jobID, revision, from); err != nil {
			return Receipt{}, err
		}
		if state != from {
			return Receipt{}, ErrConflict
		}
		if _, err := tx.Exec(`UPDATE managed_jobs SET state=?,revision=revision+1 WHERE id=?`, to, jobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, jobID, to, map[string]any{"from": from})
		if err != nil {
			return Receipt{}, err
		}
		receipt, err := l.snapshotTx(tx, jobID)
		receipt.EventCursor = cursor
		return receipt, err
	})
}

// StartLocal commits the one run claim and spawn intent before process spawn.
func (l *Ledger) StartLocal(jobID string, revision int64, runID string) (Receipt, error) {
	if !identifier.MatchString(runID) {
		return Receipt{}, ErrInvalid
	}
	q := struct {
		JobID    string
		Revision int64
		RunID    string
	}{jobID, revision, runID}
	return l.mutate("local_start", "start-"+jobID, q, func(tx *sql.Tx) (Receipt, error) {
		var state string
		var current int64
		if err := tx.QueryRow(`SELECT state,revision FROM managed_jobs WHERE id=?`, jobID).Scan(&state, &current); err != nil {
			return Receipt{}, mapNoRows(err)
		}
		if _, err := localRevisionTx(tx, jobID, revision, "ready"); err != nil {
			return Receipt{}, err
		}
		if state != "ready" {
			return Receipt{}, ErrConflict
		}
		intent, _ := json.Marshal(q)
		if _, err := tx.Exec(`INSERT INTO managed_runs(job_id,run_id,intent_json) VALUES(?,?,?)`, jobID, runID, intent); err != nil {
			return Receipt{}, fmt.Errorf("%w: run claim: %v", ErrConflict, err)
		}
		if _, err := tx.Exec(`UPDATE managed_jobs SET state='running',revision=revision+1 WHERE id=?`, jobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, jobID, "run_intent", map[string]any{"run_id": runID})
		if err != nil {
			return Receipt{}, err
		}
		receipt, err := l.snapshotTx(tx, jobID)
		receipt.EventCursor = cursor
		return receipt, err
	})
}

// AbortLocalBeforeStart can settle a local preparation failure only while no
// run claim exists. An effect after StartLocal requires reconciliation instead.
func (l *Ledger) AbortLocalBeforeStart(jobID string, revision int64) (Receipt, error) {
	q := struct {
		JobID    string
		Revision int64
	}{jobID, revision}
	return l.mutate("local_abort", "abort-"+jobID, q, func(tx *sql.Tx) (Receipt, error) {
		var state string
		var current int64
		if err := tx.QueryRow(`SELECT state,revision FROM managed_jobs WHERE id=?`, jobID).Scan(&state, &current); err != nil {
			return Receipt{}, mapNoRows(err)
		}
		if current != revision {
			return Receipt{}, ErrStale
		}
		if state != "preparing" && state != "ready" {
			return Receipt{}, ErrConflict
		}
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM managed_runs WHERE job_id=?`, jobID).Scan(&count); err != nil {
			return Receipt{}, err
		}
		if count != 0 {
			return Receipt{}, ErrBlocked
		}
		if _, err := tx.Exec(`UPDATE managed_jobs SET state='collecting',revision=revision+1 WHERE id=?`, jobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, jobID, "prepare_failed", map[string]any{"effect": "owned_workspace_only"})
		if err != nil {
			return Receipt{}, err
		}
		receipt, err := l.snapshotTx(tx, jobID)
		receipt.EventCursor = cursor
		return receipt, err
	})
}

func (l *Ledger) ReconcileLocalFailure(jobID string) error {
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRow(`SELECT state FROM managed_jobs WHERE id=?`, jobID).Scan(&state); err != nil {
		return mapNoRows(err)
	}
	if state == "finished" || state == "queued" {
		return nil
	}
	if state != "reconciling" {
		if _, err := tx.Exec(`UPDATE managed_jobs SET state='reconciling',revision=revision+1 WHERE id=?`, jobID); err != nil {
			return err
		}
		if _, err := addEvent(tx, jobID, "reconciliation_required", map[string]any{"reason": "local_effect_uncertain"}); err != nil {
			return err
		}
	}
	var resourceID string
	if err := tx.QueryRow(`SELECT resource_id FROM managed_jobs WHERE id=?`, jobID).Scan(&resourceID); err != nil {
		return err
	}
	if err := quarantineResourceTx(tx, resourceID, "local_effect_uncertain"); err != nil {
		return err
	}
	return l.commit(tx)
}

// SettleLocal validates the result against the admitted spec and returns the
// slot exactly once in the same transaction as the immutable terminal result.
func (l *Ledger) SettleLocal(jobID string, revision int64, result Result) (Receipt, error) {
	q := struct {
		JobID    string
		Revision int64
		Result   Result
	}{jobID, revision, result}
	return l.mutate("local_settle", "settle-"+jobID, q, func(tx *sql.Tx) (Receipt, error) {
		var state, resourceID string
		var current, pinned, lease int64
		var slot int
		var specJSON []byte
		err := tx.QueryRow(`SELECT state,revision,resource_id,pinned_config_revision,slot,lease_generation,spec_json FROM managed_jobs WHERE id=?`, jobID).
			Scan(&state, &current, &resourceID, &pinned, &slot, &lease, &specJSON)
		if err != nil {
			return Receipt{}, mapNoRows(err)
		}
		if _, err := localRevisionTx(tx, jobID, revision, "settling"); err != nil {
			return Receipt{}, err
		}
		if state != "settling" {
			return Receipt{}, ErrConflict
		}
		var spec JobSpec
		if err := json.Unmarshal(specJSON, &spec); err != nil {
			return Receipt{}, err
		}
		if result.JobID != jobID || result.ResourceID != resourceID || result.ResourceRevision != pinned || result.LeaseGeneration != lease {
			return Receipt{}, ErrInvalid
		}
		var currentAvailability string
		var currentConfig int64
		if err := tx.QueryRow(`SELECT availability,config_revision FROM managed_resources WHERE id=?`, resourceID).Scan(&currentAvailability, &currentConfig); err != nil {
			return Receipt{}, mapNoRows(err)
		}
		if currentConfig != pinned {
			return Receipt{}, ErrStale
		}
		result.ResourceAvailability = currentAvailability
		if result.CleanupOutcome == "failed" {
			result.ResourceAvailability = "quarantined"
			if err := quarantineResourceTx(tx, resourceID, "local_cleanup_failed"); err != nil {
				return Receipt{}, err
			}
		}
		if err := validateLocalOutcomeTx(tx, jobID, result); err != nil {
			return Receipt{}, err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return Receipt{}, err
		}
		if _, err := DecodeResultForJob(encoded, spec); err != nil {
			return Receipt{}, err
		}
		if err := releaseSlotTx(tx, resourceID); err != nil {
			return Receipt{}, err
		}
		if _, err := tx.Exec(`UPDATE managed_jobs SET state='finished',revision=revision+1,result_json=? WHERE id=?`, encoded, jobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, jobID, "finished", map[string]any{"verdict": result.FinalVerdict, "cleanup_outcome": result.CleanupOutcome})
		if err != nil {
			return Receipt{}, err
		}
		receipt, err := l.snapshotTx(tx, jobID)
		receipt.CapacityDelta = -1
		receipt.EventCursor = cursor
		return receipt, err
	})
}

func (l *Ledger) Result(jobID string) (Result, error) {
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	var retention string
	if err := tx.QueryRow(`SELECT retention_state FROM managed_admissions WHERE job_id=?`, jobID).Scan(&retention); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if err := checkJobAdmissionLossTx(tx, jobID); err != nil {
				return Result{}, err
			}
		}
		return Result{}, mapNoRows(err)
	}
	if retention == "unavailable" {
		return Result{}, ErrResultUnavailable
	}
	if retention != "available" {
		return Result{}, fmt.Errorf("invalid admission retention state for %s", jobID)
	}
	var raw []byte
	err = tx.QueryRow(`SELECT result_json FROM managed_jobs WHERE id=?`, jobID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, fmt.Errorf("accepted admission %s lost its result payload", jobID)
	}
	if err != nil {
		return Result{}, err
	}
	if len(raw) == 0 {
		return Result{}, ErrBusy
	}
	var result Result
	err = json.Unmarshal(raw, &result)
	return result, err
}

// MarkInterrupted keeps an uncertain owned run occupied after a restart.
func (l *Ledger) MarkInterrupted() error {
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM managed_jobs WHERE state NOT IN ('queued','finished','reconciling')`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`UPDATE managed_jobs SET state='reconciling',revision=revision+1 WHERE id=?`, id); err != nil {
			return err
		}
		if _, err := addEvent(tx, id, "interrupted", map[string]any{"reason": "controller_restart"}); err != nil {
			return err
		}
	}
	rows, err = tx.Query(`SELECT DISTINCT resource_id FROM managed_jobs WHERE state='reconciling' ORDER BY resource_id`)
	if err != nil {
		return err
	}
	var resourceIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		resourceIDs = append(resourceIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range resourceIDs {
		if err := quarantineResourceTx(tx, id, "controller_restart_uncertain"); err != nil {
			return err
		}
	}
	return l.commit(tx)
}
