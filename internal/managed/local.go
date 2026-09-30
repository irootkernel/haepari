package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type QueuedJob struct {
	Receipt    Receipt
	Spec       JobSpec
	ObjectID   string
	DeadlineNS int64
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
	var job QueuedJob
	var spec []byte
	err := l.db.QueryRow(`SELECT j.id,j.spec_json,i.object_id FROM managed_jobs j JOIN managed_job_inputs i ON i.job_id=j.id WHERE j.request_key=?`, key).
		Scan(&job.Receipt.JobID, &spec, &job.ObjectID)
	if err != nil {
		return job, mapNoRows(err)
	}
	if err := json.Unmarshal(spec, &job.Spec); err != nil {
		return job, err
	}
	job.Receipt, err = l.Snapshot(job.Receipt.JobID)
	return job, err
}

// NextQueued is used only by the single controller scheduler. Reserve enforces
// the queue head and capacity again in its transaction.
func (l *Ledger) NextQueued() (QueuedJob, error) {
	var job QueuedJob
	var spec []byte
	err := l.db.QueryRow(`SELECT j.id,j.spec_json,i.object_id,j.queue_deadline_ns FROM managed_jobs j JOIN managed_job_inputs i ON i.job_id=j.id WHERE j.state='queued' ORDER BY j.rowid LIMIT 1`).
		Scan(&job.Receipt.JobID, &spec, &job.ObjectID, &job.DeadlineNS)
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

// RegisterOnlyLocal is the one-resource admission gate for TASK-033. The
// count and insertion share a transaction so concurrent clients cannot both
// establish a second resource.
func (l *Ledger) RegisterOnlyLocal(data []byte) error {
	r, err := DecodeResource(data)
	if err != nil {
		return err
	}
	if r.Kind != "local" || r.Capacity != 1 {
		return ErrUnsupported
	}
	canonical, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM managed_resources`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrBusy
	}
	if _, err := tx.Exec(`INSERT INTO managed_resources(id,kind,config_revision,capacity,vm_uuid,vm_mac,spec_json) VALUES(?,?,?,?,?,?,?)`, r.ID, r.Kind, r.Revision, r.Capacity, "", "", canonical); err != nil {
		return fmt.Errorf("%w: resource registration: %v", ErrConflict, err)
	}
	return l.commit(tx)
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
		if current != revision {
			return Receipt{}, ErrStale
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
		if current != revision {
			return Receipt{}, ErrStale
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
	if _, err := tx.Exec(`UPDATE managed_resources SET availability='quarantined',availability_reason='local_effect_uncertain',availability_revision=availability_revision+1 WHERE id=(SELECT resource_id FROM managed_jobs WHERE id=?) AND availability!='quarantined'`, jobID); err != nil {
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
		if current != revision {
			return Receipt{}, ErrStale
		}
		if state != "settling" {
			return Receipt{}, ErrConflict
		}
		var spec JobSpec
		if err := json.Unmarshal(specJSON, &spec); err != nil {
			return Receipt{}, err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return Receipt{}, err
		}
		if _, err := DecodeResultForJob(encoded, spec); err != nil {
			return Receipt{}, err
		}
		if result.JobID != jobID || result.ResourceID != resourceID || result.ResourceRevision != pinned || result.LeaseGeneration != lease {
			return Receipt{}, ErrInvalid
		}
		if result.CleanupOutcome == "failed" {
			if _, err := tx.Exec(`UPDATE managed_resources SET availability='quarantined',availability_reason='local_cleanup_failed',availability_revision=availability_revision+1 WHERE id=?`, resourceID); err != nil {
				return Receipt{}, err
			}
		} else if result.ResourceAvailability != "available" {
			return Receipt{}, ErrInvalid
		}
		updated, err := tx.Exec(`UPDATE managed_resources SET occupancy=occupancy-1 WHERE id=? AND occupancy>0`, resourceID)
		if err != nil {
			return Receipt{}, err
		}
		if count, err := updated.RowsAffected(); err != nil || count != 1 {
			return Receipt{}, ErrBlocked
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
	var raw []byte
	err := l.db.QueryRow(`SELECT result_json FROM managed_jobs WHERE id=?`, jobID).Scan(&raw)
	if err != nil {
		return Result{}, mapNoRows(err)
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
		if _, err := tx.Exec(`UPDATE managed_resources SET availability='quarantined',availability_reason='controller_restart_uncertain',availability_revision=availability_revision+1 WHERE id=(SELECT resource_id FROM managed_jobs WHERE id=?) AND availability!='quarantined'`, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE managed_resources SET availability='quarantined',availability_reason='controller_restart_uncertain',availability_revision=availability_revision+1 WHERE availability!='quarantined' AND id IN (SELECT resource_id FROM managed_jobs WHERE state='reconciling')`); err != nil {
		return err
	}
	return l.commit(tx)
}
