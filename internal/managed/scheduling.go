package managed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var schedulingTables = []string{"managed_parent_context", "managed_parent_nonces", "managed_queue_blockers", "managed_vm_observations", "managed_power_progress", "managed_availability_receipts"}

func checkSchedulingSchemaTx(tx *sql.Tx) error {
	for _, name := range schedulingTables {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?`, name).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("managed schema5 lost required table %s", name)
		}
	}
	if _, err := tx.Exec(`SELECT idle_since_ns FROM managed_resources LIMIT 0`); err != nil {
		return err
	}
	return nil
}

func initSchedulingTx(tx *sql.Tx, version int) error {
	if version > 0 && version < 5 {
		rows, err := tx.Query(`PRAGMA table_info(managed_resources)`)
		if err != nil {
			return err
		}
		exists := false
		for rows.Next() {
			var cid, notNull, pk int
			var name, typ string
			var value sql.NullString
			if err := rows.Scan(&cid, &name, &typ, &notNull, &value, &pk); err != nil {
				rows.Close()
				return err
			}
			if name == "idle_since_ns" {
				exists = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if !exists {
			if _, err := tx.Exec(`ALTER TABLE managed_resources ADD COLUMN idle_since_ns INTEGER`); err != nil {
				return err
			}
		}
	}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS managed_availability_receipts(sequence INTEGER PRIMARY KEY AUTOINCREMENT,mutation_key TEXT NOT NULL UNIQUE,resource_id TEXT NOT NULL,request_json BLOB NOT NULL,status_json BLOB NOT NULL,recorded_at_ns INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS managed_parent_context(job_id TEXT PRIMARY KEY, chain_json BLOB NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS managed_parent_nonces(job_id TEXT PRIMARY KEY, lease_generation INTEGER NOT NULL, nonce TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS managed_queue_blockers(job_id TEXT PRIMARY KEY, reason TEXT NOT NULL, details_json BLOB NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS managed_vm_observations(resource_id TEXT PRIMARY KEY, config_revision INTEGER NOT NULL, vm_uuid TEXT NOT NULL, power TEXT NOT NULL, observation_revision INTEGER NOT NULL, evidence_digest TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS managed_power_progress(operation_generation INTEGER PRIMARY KEY, stage TEXT NOT NULL, observation_json BLOB)`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	if version < 5 {
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version,identity) VALUES(5,'managed-scheduling-v5')`); err != nil {
			return err
		}
		if _, err := tx.Exec(`PRAGMA user_version=5`); err != nil {
			return err
		}
	}
	return nil
}

type AvailabilityIdentity struct {
	MutationKey                 string `json:"mutation_key"`
	AppliedAvailabilityRevision int64  `json:"applied_availability_revision"`
}

type ResourceStatus struct {
	AdvisoryGrants       int                   `json:"advisory_grants,omitempty"`
	AdvisoryQueued       int                   `json:"advisory_queued,omitempty"`
	Mutation             *AvailabilityIdentity `json:"mutation,omitempty"`
	ID                   string                `json:"resource_id"`
	Kind                 string                `json:"kind"`
	ConfigurationDigest  string                `json:"configuration_digest"`
	ConfigRevision       int64                 `json:"config_revision"`
	AvailabilityRevision int64                 `json:"availability_revision"`
	Availability         string                `json:"availability"`
	Reason               string                `json:"reason"`
	Capacity             int                   `json:"capacity"`
	Occupancy            int                   `json:"occupancy"`
	LeaseGeneration      int64                 `json:"lease_generation"`
	IdleSince            *time.Time            `json:"idle_since,omitempty"`
}

func resourceStatusTx(q rowQuerier, id string) (ResourceStatus, error) {
	var r ResourceStatus
	var idle sql.NullInt64
	var spec []byte
	err := q.QueryRow(`SELECT id,kind,config_revision,availability_revision,availability,availability_reason,capacity,occupancy,lease_generation,idle_since_ns,spec_json FROM managed_resources WHERE id=?`, id).Scan(&r.ID, &r.Kind, &r.ConfigRevision, &r.AvailabilityRevision, &r.Availability, &r.Reason, &r.Capacity, &r.Occupancy, &r.LeaseGeneration, &idle, &spec)
	if err == nil {
		digest := sha256.Sum256(spec)
		r.ConfigurationDigest = hex.EncodeToString(digest[:])
	}
	if err == nil && r.Kind == "lock" {
		err = q.QueryRow(`SELECT COUNT(CASE WHEN state='granted' THEN 1 END),COUNT(CASE WHEN state='queued' THEN 1 END) FROM managed_lock_requests WHERE lock_id=?`, id).Scan(&r.AdvisoryGrants, &r.AdvisoryQueued)
	}
	if idle.Valid {
		value := time.Unix(0, idle.Int64).UTC()
		r.IdleSince = &value
	}
	return r, mapNoRows(err)
}

func (l *Ledger) ResourceStatus(id string) (ResourceStatus, error) {
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ResourceStatus{}, err
	}
	defer tx.Rollback()
	return resourceStatusTx(tx, id)
}

type AvailabilityRequest struct {
	MutationKey                  string `json:"mutation_key"`
	ResourceID                   string `json:"resource_id"`
	ExpectedConfigRevision       int64  `json:"expected_config_revision"`
	ExpectedAvailabilityRevision int64  `json:"expected_availability_revision"`
	ExpectedLeaseGeneration      int64  `json:"expected_lease_generation"`
	Reason                       string `json:"reason,omitempty"`
	ReadinessDigest              string `json:"readiness_digest,omitempty"`
	ObservationReference         string `json:"observation_reference,omitempty"`
}

// AvailabilityRefusal describes the state observed by the refusing transaction.
type AvailabilityRefusal struct {
	Current, Required string
}

func (e *AvailabilityRefusal) Error() string {
	return fmt.Sprintf("This availability transition requires %s; the resource is %s. Pause/resume cannot clear quarantine.", e.Required, e.Current)
}
func (e *AvailabilityRefusal) Unwrap() error { return ErrBlocked }

// Availability changes are fenced to the current registration and lease epoch.
// Resume accepts a fresh adapter readiness observation only for a paused
// resource. Quarantine requires the independent reconciliation workflow.
func (l *Ledger) SetAvailability(q AvailabilityRequest, resume bool) (ResourceStatus, error) {
	if !resume {
		q.ReadinessDigest, q.ObservationReference = "", ""
	}
	if !validAvailabilityIntent(q, resume) {
		return ResourceStatus{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return ResourceStatus{}, err
	}
	defer tx.Rollback()
	requestJSON, _ := json.Marshal(struct {
		Resume  bool
		Request AvailabilityRequest
	}{resume, q})
	if replay, found, err := availabilityReplayTx(tx, q, resume); found || err != nil {
		if err == nil && replay.Kind == "lock" {
			_, changed, dueErr := l.clockAndDueTx(tx, false)
			if dueErr != nil {
				return ResourceStatus{}, dueErr
			}
			current, e := resourceStatusTx(tx, q.ResourceID)
			if e != nil {
				return current, e
			}
			current.Mutation = replay.Mutation
			return current, l.commitLocks(tx, changed)
		}
		return replay, err
	}
	r, err := resourceStatusTx(tx, q.ResourceID)
	if err != nil {
		return r, err
	}
	if r.Kind == "lock" {
		return l.setLockAvailabilityTx(tx, q, resume, requestJSON, r)
	}
	if resume && (!validDigest(q.ReadinessDigest) || q.ObservationReference == "" || len(q.ObservationReference) > 512) {
		return ResourceStatus{}, ErrInvalid
	}
	if r.ConfigRevision != q.ExpectedConfigRevision || r.AvailabilityRevision != q.ExpectedAvailabilityRevision || r.LeaseGeneration != q.ExpectedLeaseGeneration {
		return r, ErrStale
	}
	if pending, err := pendingPowerTx(tx, r.ID); err != nil {
		return r, err
	} else if pending {
		return r, ErrBusy
	}
	availability, reason := "paused", q.Reason
	if resume {
		if r.Availability != "paused" {
			return r, &AvailabilityRefusal{Current: r.Availability, Required: "paused"}
		}
		availability, reason = "available", ""
	} else if r.Availability != "available" {
		return r, &AvailabilityRefusal{Current: r.Availability, Required: "available"}
	}
	if _, err := tx.Exec(`UPDATE managed_resources SET availability=?,availability_reason=?,availability_revision=availability_revision+1 WHERE id=?`, availability, reason, r.ID); err != nil {
		return r, err
	}
	if err := queuedAvailabilityEventsTx(tx, r.ID, map[string]any{"availability": availability, "reason": reason, "readiness_digest": q.ReadinessDigest, "observation_reference": q.ObservationReference}); err != nil {
		return r, err
	}
	r, err = resourceStatusTx(tx, r.ID)
	if err != nil {
		return r, err
	}
	r.Mutation = &AvailabilityIdentity{q.MutationKey, r.AvailabilityRevision}
	statusJSON, _ := json.Marshal(r)
	if _, err := tx.Exec(`INSERT INTO managed_availability_receipts(mutation_key,resource_id,request_json,status_json,recorded_at_ns) VALUES(?,?,?,?,?)`, q.MutationKey, r.ID, requestJSON, statusJSON, time.Now().UTC().UnixNano()); err != nil {
		return r, err
	}
	return r, l.commit(tx)
}

type QueueBlocker struct {
	Reason  string          `json:"reason"`
	Details json.RawMessage `json:"details"`
}

// Blockers do not consume a lease, change admission order, or extend a deadline.
func (l *Ledger) RecordQueueBlocker(jobID string, revision int64, blocker QueueBlocker) error {
	if blocker.Reason != "host_capacity_no_eligible_victim" || len(blocker.Details) > 4096 || !json.Valid(blocker.Details) {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	var current int64
	if err := tx.QueryRow(`SELECT state,revision FROM managed_jobs WHERE id=?`, jobID).Scan(&state, &current); err != nil {
		return mapNoRows(err)
	}
	if state != "queued" || revision != current {
		return ErrStale
	}
	var priorReason string
	var priorDetails []byte
	err = tx.QueryRow(`SELECT reason,details_json FROM managed_queue_blockers WHERE job_id=?`, jobID).Scan(&priorReason, &priorDetails)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if priorReason == blocker.Reason && string(priorDetails) == string(blocker.Details) {
		return nil
	}
	if err := requireJobBoundsTx(tx, jobID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO managed_queue_blockers VALUES(?,?,?) ON CONFLICT(job_id) DO UPDATE SET reason=excluded.reason,details_json=excluded.details_json`, jobID, blocker.Reason, blocker.Details); err != nil {
		return err
	}
	if _, err := addEvent(tx, jobID, "queue_blocked", blocker); err != nil {
		return err
	}
	return l.commit(tx)
}

func (l *Ledger) QueueBlocker(jobID string) (*QueueBlocker, error) {
	var b QueueBlocker
	err := l.db.QueryRow(`SELECT reason,details_json FROM managed_queue_blockers WHERE job_id=?`, jobID).Scan(&b.Reason, &b.Details)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &b, err
}

// The final occupied slot return is the only release that advances the UTC idle clock.
func releaseSlotTx(tx *sql.Tx, jobID, resourceID string) error {
	if err := requireJobBoundsTx(tx, jobID); err != nil {
		return err
	}
	updated, err := tx.Exec(`UPDATE managed_resources SET idle_since_ns=CASE WHEN occupancy=1 THEN ? ELSE idle_since_ns END,occupancy=occupancy-1 WHERE id=? AND occupancy>0`, time.Now().UTC().UnixNano(), resourceID)
	if err != nil {
		return err
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrBlocked
	}
	return nil
}

func queuedAvailabilityEventsTx(tx *sql.Tx, resourceID string, facts map[string]any) error {
	rows, err := tx.Query(`SELECT id FROM managed_jobs WHERE resource_id=? AND state='queued' ORDER BY id`, resourceID)
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
		if _, err := addEvent(tx, id, "resource_availability", facts); err != nil {
			return err
		}
	}
	return nil
}

func quarantineResourceTx(tx *sql.Tx, resourceID, reason string) error {
	updated, err := tx.Exec(`UPDATE managed_resources SET availability='quarantined',availability_reason=?,availability_revision=availability_revision+1 WHERE id=? AND availability!='quarantined'`, reason, resourceID)
	if err != nil {
		return err
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	return queuedAvailabilityEventsTx(tx, resourceID, map[string]any{"availability": "quarantined", "reason": reason})
}

// ManualHoldRequest binds the prepared manual adapter observation to an occupied
// lease. TASK-019 owns runtime session discovery, completion and abandonment;
// this accounting transition does not create a human verdict or return capacity.
type ManualHoldRequest struct {
	JobID                        string
	ExpectedJobRevision          int64
	ExpectedConfigRevision       int64
	ExpectedAvailabilityRevision int64
	Slot                         int
	LeaseGeneration              int64
	EnvironmentDigest            string
	ReadinessDigest              string
	ObservationReference         string
	MutationKey                  string
}

func (l *Ledger) HoldManualCapacity(q ManualHoldRequest) (Receipt, error) {
	if !validDigest(q.EnvironmentDigest) || !validDigest(q.ReadinessDigest) || q.ObservationReference == "" || len(q.ObservationReference) > 512 {
		return Receipt{}, ErrInvalid
	}
	return l.mutate("manual_hold", q.MutationKey, q, func(tx *sql.Tx) (Receipt, error) {
		receipt, err := l.snapshotTx(tx, q.JobID)
		if err != nil {
			return receipt, err
		}
		if receipt.JobRevision != q.ExpectedJobRevision || receipt.ResourceConfigRevision != q.ExpectedConfigRevision || receipt.ResourceAvailabilityRevision != q.ExpectedAvailabilityRevision || receipt.LeaseGeneration != q.LeaseGeneration || receipt.Slot != q.Slot {
			return receipt, ErrStale
		}
		if receipt.JobState != "ready" {
			return receipt, ErrConflict
		}
		if receipt.ResourceAvailability != "available" || receipt.LeaseGeneration <= 0 {
			return receipt, ErrBlocked
		}
		var raw []byte
		if err := tx.QueryRow(`SELECT spec_json FROM managed_jobs WHERE id=?`, q.JobID).Scan(&raw); err != nil {
			return receipt, err
		}
		job, err := DecodeJob(raw)
		if err != nil {
			return receipt, err
		}
		if job.Mode != "manual" || job.Manual == nil || job.Environment.ProfileDigest != q.EnvironmentDigest {
			return receipt, ErrInvalid
		}
		if _, err := tx.Exec(`UPDATE managed_jobs SET state='awaiting_manual',revision=revision+1 WHERE id=?`, q.JobID); err != nil {
			return receipt, err
		}
		deadline := time.Now().UTC().Add(time.Duration(job.Deadlines.ManualSeconds) * time.Second)
		session, sessionErr := manualSessionTx(tx, q.JobID)
		if sessionErr == nil {
			if session.Legacy || !session.Stopped || session.Preparation == nil || session.Preparation.WorkloadOutcome != "pass" || session.AbortReason != "" {
				return receipt, ErrBlocked
			}
			if !session.Deadline.IsZero() {
				return receipt, ErrConflict
			}
			session.Deadline = deadline
			if err := saveManualSessionTx(tx, session); err != nil {
				return receipt, err
			}
		} else if !errors.Is(sessionErr, ErrNotFound) {
			return receipt, sessionErr
		}
		cursor, err := addEvent(tx, q.JobID, "awaiting_manual", map[string]any{"lease_generation": q.LeaseGeneration, "environment_digest": q.EnvironmentDigest, "readiness_digest": q.ReadinessDigest, "observation_reference": q.ObservationReference, "scenario_document": job.Manual.ScenarioDocument, "required_scenarios": job.Manual.RequiredScenarios, "manual_deadline": deadline.Format(time.RFC3339Nano)})
		if err != nil {
			return receipt, err
		}
		receipt, err = l.snapshotTx(tx, q.JobID)
		receipt.EventCursor = cursor
		return receipt, err
	})
}

// ReplayAvailability observes an already committed mutation without performing
// fresh readiness work or changing availability. A miss authorizes no effect.
func (l *Ledger) ReplayAvailability(q AvailabilityRequest, resume bool) (ResourceStatus, bool, error) {
	if !resume {
		q.ReadinessDigest, q.ObservationReference = "", ""
	}
	if !validAvailabilityIntent(q, resume) {
		return ResourceStatus{}, false, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ResourceStatus{}, false, err
	}
	defer tx.Rollback()
	return availabilityReplayTx(tx, q, resume)
}

func availabilityReplayTx(tx *sql.Tx, q AvailabilityRequest, resume bool) (ResourceStatus, bool, error) {
	var requestJSON, statusJSON []byte
	err := tx.QueryRow(`SELECT request_json,status_json FROM managed_availability_receipts WHERE mutation_key=?`, q.MutationKey).Scan(&requestJSON, &statusJSON)
	if err == sql.ErrNoRows {
		return ResourceStatus{}, false, nil
	}
	if err != nil {
		return ResourceStatus{}, false, err
	}
	var prior struct {
		Resume  bool
		Request AvailabilityRequest
	}
	if err := json.Unmarshal(requestJSON, &prior); err != nil {
		return ResourceStatus{}, true, err
	}
	currentIntent, priorIntent := q, prior.Request
	if resume {
		currentIntent.ReadinessDigest = ""
		currentIntent.ObservationReference = ""
		priorIntent.ReadinessDigest = ""
		priorIntent.ObservationReference = ""
	}
	if prior.Resume != resume || currentIntent != priorIntent {
		return ResourceStatus{}, true, ErrConflict
	}
	var original ResourceStatus
	if err := json.Unmarshal(statusJSON, &original); err != nil {
		return ResourceStatus{}, true, err
	}
	current, err := resourceStatusTx(tx, q.ResourceID)
	current.Mutation = original.Mutation
	return current, true, err
}

func validAvailabilityIntent(q AvailabilityRequest, resume bool) bool {
	return !(q.ExpectedConfigRevision <= 0 || q.ExpectedAvailabilityRevision <= 0 || q.ExpectedLeaseGeneration < 0 || !ValidRequestKey(q.MutationKey) || !identifier.MatchString(q.ResourceID) || (!resume && (q.Reason == "" || len(q.Reason) > 256)))
}
