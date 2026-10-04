package managed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Ledger is the durable managed-state owner. Callers must use the same SQLite
// database as the controller; no other in-memory lifecycle state is kept.
type Ledger struct {
	db                  *sql.DB
	globalLimit         int
	transactionBoundary func(string) // nil in production; crash probes surround the real commit
	changesMu           sync.Mutex
	changes             chan struct{}
	lockClock           func() (time.Time, string, error)
}

// Changes returns the current commit generation. Subscribe before reading the
// durable predicate; notifications carry no lifecycle or progress authority.
func (l *Ledger) Changes() <-chan struct{} {
	l.changesMu.Lock()
	defer l.changesMu.Unlock()
	if l.changes == nil {
		l.changes = make(chan struct{})
	}
	return l.changes
}

func (l *Ledger) commit(tx *sql.Tx) error {
	if l.transactionBoundary != nil {
		l.transactionBoundary("before_commit")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	l.changesMu.Lock()
	if l.changes != nil {
		close(l.changes)
	}
	l.changes = make(chan struct{})
	l.changesMu.Unlock()
	if l.transactionBoundary != nil {
		l.transactionBoundary("after_commit")
	}
	return nil
}

type Receipt struct {
	Admission                    AdmissionIdentity `json:"admission,omitempty"`
	ResultStatus                 string            `json:"result_status,omitempty"`
	JobID                        string            `json:"job_id"`
	JobRevision                  int64             `json:"job_revision"`
	JobState                     string            `json:"job_state"`
	ResourceID                   string            `json:"resource_id"`
	ResourceConfigRevision       int64             `json:"resource_config_revision"`
	ResourceAvailabilityRevision int64             `json:"resource_availability_revision"`
	ResourceAvailability         string            `json:"resource_availability"`
	Slot                         int               `json:"slot"`
	LeaseGeneration              int64             `json:"lease_generation"`
	ResourceOccupancy            int               `json:"resource_occupancy"`
	GlobalOccupancy              int               `json:"global_occupancy"`
	CapacityDelta                int               `json:"capacity_delta"`
	Verdict                      Verdict           `json:"verdict,omitempty"`
	EventCursor                  int64             `json:"event_cursor"`
}

type Event struct {
	Cursor  int64           `json:"cursor"`
	JobID   string          `json:"job_id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type EventBatch struct {
	Events        []Event  `json:"events"`
	Resync        *Receipt `json:"resync,omitempty"`
	CurrentCursor int64    `json:"current_cursor"`
	NextCursor    int64    `json:"next_cursor"`
	Snapshot      Receipt  `json:"snapshot"`
	More          bool     `json:"more"`
}

// EventsAfter returns global monotonic cursors scoped to one Job. A nonzero
// cursor from another Job is rejected instead of silently starting elsewhere.
func (l *Ledger) EventsAfter(jobID string, after int64, limit int) (EventBatch, error) {
	if !identifier.MatchString(jobID) || after < 0 || limit <= 0 || limit > 1000 {
		return EventBatch{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EventBatch{}, err
	}
	defer tx.Rollback()
	snapshot, err := l.snapshotTx(tx, jobID)
	if err != nil {
		return EventBatch{}, err
	}
	var current int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(cursor),0) FROM managed_event_owners WHERE job_id=?`, jobID).Scan(&current); err != nil {
		return EventBatch{}, err
	}
	if after > 0 {
		var owner string
		err := tx.QueryRow(`SELECT job_id FROM managed_event_owners WHERE cursor=?`, after).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			return EventBatch{}, ErrInvalid
		}
		if err != nil {
			return EventBatch{}, err
		}
		if owner != jobID {
			return EventBatch{}, ErrInvalid
		}
	}
	if snapshot.ResultStatus == "unavailable" {
		return EventBatch{}, ErrResultUnavailable
	}
	var purgedThrough int64
	if err := tx.QueryRow(`SELECT purged_through FROM managed_event_progress WHERE job_id=?`, jobID).Scan(&purgedThrough); err != nil {
		return EventBatch{}, mapNoRows(err)
	}
	// Only explicit retention permits resynchronization. A lost owned payload
	// above the floor is storage uncertainty, never an empty successful page.
	var missing int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM managed_event_owners o LEFT JOIN managed_events e ON e.cursor=o.cursor WHERE o.job_id=? AND o.cursor>? AND e.cursor IS NULL`, jobID, purgedThrough).Scan(&missing); err != nil {
		return EventBatch{}, err
	}
	if missing != 0 {
		return EventBatch{}, fmt.Errorf("managed event payload missing")
	}
	if purgedThrough > 0 && after <= purgedThrough {
		return EventBatch{Resync: &snapshot, Snapshot: snapshot, CurrentCursor: current, NextCursor: current}, nil
	}
	rows, err := tx.Query(`SELECT cursor,job_id,kind,payload_json FROM managed_events WHERE job_id=? AND cursor>? ORDER BY cursor LIMIT ?`, jobID, after, limit)
	if err != nil {
		return EventBatch{}, err
	}
	defer rows.Close()
	batch := EventBatch{Snapshot: snapshot, CurrentCursor: current, NextCursor: after, Events: []Event{}}
	bytes := 0
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.Cursor, &event.JobID, &event.Kind, &event.Payload); err != nil {
			return EventBatch{}, err
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			return EventBatch{}, err
		}
		if len(encoded) > 2<<20 {
			return EventBatch{}, fmt.Errorf("managed event payload exceeds page bound")
		}
		if bytes+len(encoded) > 2<<20 {
			break
		}
		bytes += len(encoded)
		batch.Events = append(batch.Events, event)
		batch.NextCursor = event.Cursor
	}
	batch.More = batch.NextCursor < current
	return batch, rows.Err()
}

// PurgeEventsThrough removes only a finished Job's event prefix. Ownership
// tombstones and the durable floor remain so replay cursors keep their scope.
func (l *Ledger) PurgeEventsThrough(jobID string, through int64) error {
	if !identifier.MatchString(jobID) || through <= 0 {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, owner string
	if err := tx.QueryRow(`SELECT state FROM managed_jobs WHERE id=?`, jobID).Scan(&state); err != nil {
		return mapNoRows(err)
	}
	if state != "finished" {
		return ErrBusy
	}
	if err := tx.QueryRow(`SELECT job_id FROM managed_event_owners WHERE cursor=?`, through).Scan(&owner); err != nil {
		return mapNoRows(err)
	}
	if owner != jobID {
		return ErrInvalid
	}
	var floor int64
	if err := tx.QueryRow(`SELECT purged_through FROM managed_event_progress WHERE job_id=?`, jobID).Scan(&floor); err != nil {
		return mapNoRows(err)
	}
	if through <= floor {
		return nil
	}
	// Recovery intents and compensations bind permanent replay/provenance;
	// they remain private below the public resynchronization floor.
	if _, err := tx.Exec(`DELETE FROM managed_events WHERE job_id=? AND cursor<=? AND kind NOT IN ('recovery_intent','phase_reconciled')`, jobID, through); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE managed_event_progress SET purged_through=? WHERE job_id=?`, through, jobID); err != nil {
		return err
	}
	if _, err := addEvent(tx, jobID, "resync_checkpoint", map[string]any{"purged_through": through}); err != nil {
		return err
	}
	return l.commit(tx)
}

// NoEffectProof is supplied from a reconciled adapter observation. The
// observation reference must name durable evidence; a boolean alone is not a
// cleanup or termination proof.
type NoEffectProof struct {
	ObservationReference string `json:"observation_reference"`
	NoOwnedJobEffect     bool   `json:"no_owned_job_effect"`
	NoUnprovenProcess    bool   `json:"no_unproven_process"`
	NoProtectedPath      bool   `json:"no_protected_path"`
	PowerReconciled      bool   `json:"power_reconciled"`
}

func (p NoEffectProof) valid() bool {
	return p.ObservationReference != "" && len(p.ObservationReference) <= 512 && p.NoOwnedJobEffect &&
		p.NoUnprovenProcess && p.NoProtectedPath && p.PowerReconciled
}

// Existing stores must retain their original authority before creation or
// migration statements run. An absent receipt table is not an empty history.
func checkBaseSchemaTx(tx *sql.Tx, version int) error {
	for _, table := range []struct {
		name, columns string
		since         int
	}{
		{"schema_migrations", "version,identity", 1},
		{"managed_resources", "id,kind,config_revision,availability_revision,availability,availability_reason,capacity,occupancy,lease_generation,vm_uuid,vm_mac,spec_json", 1},
		{"managed_jobs", "id,request_key,submitted_digest,resource_id,revision,state,pinned_config_revision,admitted_at_ns,queue_deadline_ns,slot,lease_generation,pause_availability_revision,no_effect_proof_json,failure_kind,result_json,spec_json", 1},
		{"managed_events", "cursor,job_id,kind,payload_json", 1},
		{"managed_event_owners", "cursor,job_id", 1},
		{"managed_event_progress", "job_id,purged_through", 1},
		{"managed_mutations", "mutation_key,digest,receipt_json", 1},
		{"managed_runs", "job_id,run_id,intent_json", 3},
		{"managed_power_transitions", "operation_generation,victim_resource_id,target_resource_id,identity_json,state", 1},
		{"managed_no_effect_observations", "reference,job_id,resource_id,config_revision,slot,lease_generation,source,mechanism,evidence_digest,recorded_at_ns,proof_json", 2},
		{"managed_job_inputs", "job_id,sealed_digest,object_id", 3},
		{"managed_admissions", "sequence,request_key,job_id,submitted_digest,spec_json,sealed_digest,object_id,inventory_json,identity_json,retention_state,tombstone_receipt", 4},
	} {
		if version < table.since {
			continue
		}
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?`, table.name).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("managed schema%d lost required table %s", version, table.name)
		}
		if _, err := tx.Exec("SELECT " + table.columns + " FROM " + table.name + " LIMIT 0"); err != nil {
			return fmt.Errorf("managed schema%d required columns in %s: %w", version, table.name, err)
		}
	}
	if version >= 2 {
		if _, err := tx.Exec(`SELECT reconciled_at_ns FROM managed_power_transitions LIMIT 0`); err != nil {
			return fmt.Errorf("managed schema%d required power reconciliation column: %w", version, err)
		}
	}
	return nil
}

func NewLedger(db *sql.DB, globalLimit int) (*Ledger, error) {
	if db == nil || globalLimit <= 0 {
		return nil, ErrInvalid
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, identity TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS managed_resources (
		 id TEXT PRIMARY KEY, kind TEXT NOT NULL, config_revision INTEGER NOT NULL,
		 availability_revision INTEGER NOT NULL DEFAULT 1, availability TEXT NOT NULL DEFAULT 'available',
		 availability_reason TEXT NOT NULL DEFAULT '', capacity INTEGER NOT NULL, occupancy INTEGER NOT NULL DEFAULT 0,
		 idle_since_ns INTEGER, lease_generation INTEGER NOT NULL DEFAULT 0, vm_uuid TEXT NOT NULL DEFAULT '', vm_mac TEXT NOT NULL DEFAULT '', spec_json BLOB NOT NULL)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS managed_vm_uuid ON managed_resources(vm_uuid) WHERE vm_uuid != ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS managed_vm_mac ON managed_resources(vm_mac) WHERE vm_mac != ''`,
		`CREATE TABLE IF NOT EXISTS managed_admissions (
		 sequence INTEGER PRIMARY KEY AUTOINCREMENT, request_key TEXT NOT NULL UNIQUE, job_id TEXT NOT NULL UNIQUE,
		 submitted_digest TEXT NOT NULL, spec_json BLOB NOT NULL, sealed_digest TEXT NOT NULL, object_id TEXT NOT NULL,
		 inventory_json BLOB, identity_json BLOB NOT NULL, retention_state TEXT NOT NULL DEFAULT 'available', tombstone_receipt BLOB)`,
		`CREATE TABLE IF NOT EXISTS managed_jobs (
		 id TEXT PRIMARY KEY, request_key TEXT NOT NULL UNIQUE, submitted_digest TEXT NOT NULL,
		 resource_id TEXT NOT NULL REFERENCES managed_resources(id), revision INTEGER NOT NULL DEFAULT 1,
		 state TEXT NOT NULL DEFAULT 'queued', pinned_config_revision INTEGER NOT NULL,
		 admitted_at_ns INTEGER NOT NULL, queue_deadline_ns INTEGER NOT NULL,
		 slot INTEGER, lease_generation INTEGER, pause_availability_revision INTEGER,
		 no_effect_proof_json BLOB, failure_kind TEXT NOT NULL DEFAULT '', result_json BLOB,
		 spec_json BLOB NOT NULL)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS managed_active_slots ON managed_jobs(resource_id,slot) WHERE slot IS NOT NULL AND state != 'finished'`,
		`CREATE TABLE IF NOT EXISTS managed_events (
		 cursor INTEGER PRIMARY KEY AUTOINCREMENT, job_id TEXT NOT NULL, kind TEXT NOT NULL, payload_json BLOB NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS managed_events_job_cursor ON managed_events(job_id,cursor)`,
		`CREATE TABLE IF NOT EXISTS managed_event_owners (
		 cursor INTEGER PRIMARY KEY, job_id TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS managed_event_owners_job_cursor ON managed_event_owners(job_id,cursor)`,
		`CREATE TABLE IF NOT EXISTS managed_event_progress (
		 job_id TEXT PRIMARY KEY, purged_through INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS managed_mutations (
		 mutation_key TEXT PRIMARY KEY, digest TEXT NOT NULL, receipt_json BLOB NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS managed_job_inputs (
		 job_id TEXT PRIMARY KEY REFERENCES managed_jobs(id), sealed_digest TEXT NOT NULL, object_id TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS managed_runs (
		 job_id TEXT PRIMARY KEY REFERENCES managed_jobs(id), run_id TEXT NOT NULL UNIQUE,
		 intent_json BLOB NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS managed_power_transitions (
		 operation_generation INTEGER PRIMARY KEY, victim_resource_id TEXT NOT NULL,
		 target_resource_id TEXT NOT NULL, identity_json BLOB NOT NULL, state TEXT NOT NULL,
		 reconciled_at_ns INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS managed_no_effect_observations (
		 reference TEXT PRIMARY KEY, job_id TEXT NOT NULL, resource_id TEXT NOT NULL,
		 config_revision INTEGER NOT NULL, slot INTEGER NOT NULL, lease_generation INTEGER NOT NULL,
		 source TEXT NOT NULL, mechanism TEXT NOT NULL, evidence_digest TEXT NOT NULL,
		 recorded_at_ns INTEGER NOT NULL, proof_json BLOB NOT NULL)`,
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return nil, err
	}
	if version > 9 {
		return nil, fmt.Errorf("%w: managed store version %d", ErrUnsupported, version)
	}
	if err := checkBaseSchemaTx(tx, version); err != nil {
		return nil, err
	}
	if version >= 5 {
		if err := checkSchedulingSchemaTx(tx); err != nil {
			return nil, err
		}
	}
	if version >= 6 {
		if err := checkClientSchemaTx(tx); err != nil {
			return nil, err
		}
	}
	if version >= 7 {
		if err := checkLockSchemaTx(tx); err != nil {
			return nil, err
		}
	}
	if version >= 8 {
		if err := checkRunnerSchemaTx(tx); err != nil {
			return nil, err
		}
	}
	if version >= 9 {
		if err := checkRetentionSchemaTx(tx); err != nil {
			return nil, err
		}
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO managed_event_owners(cursor,job_id) SELECT cursor,job_id FROM managed_events`); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO managed_event_progress(job_id) SELECT id FROM managed_jobs`); err != nil {
		return nil, err
	}
	if version == 1 {
		hasColumn, err := powerTransitionHasColumnTx(tx, "reconciled_at_ns")
		if err != nil {
			return nil, err
		}
		if !hasColumn {
			if _, err := tx.Exec(`ALTER TABLE managed_power_transitions ADD COLUMN reconciled_at_ns INTEGER NOT NULL DEFAULT 0`); err != nil {
				return nil, err
			}
		}
	}
	if version < 2 {
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version,identity) VALUES(2,'managed-observation-v2')`); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`PRAGMA user_version=2`); err != nil {
			return nil, err
		}
	}
	if version < 3 {
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version,identity) VALUES(3,'sealed-job-input-v3')`); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`PRAGMA user_version=3`); err != nil {
			return nil, err
		}
	}
	if version < 4 {
		if err := migrateAdmissions(tx); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version,identity) VALUES(4,'durable-admissions-v4')`); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`PRAGMA user_version=4`); err != nil {
			return nil, err
		}
	}
	if err := initSchedulingTx(tx, version); err != nil {
		return nil, err
	}
	if err := initClientSchemaTx(tx, version); err != nil {
		return nil, err
	}
	if err := initLockSchemaTx(tx, version); err != nil {
		return nil, err
	}
	if err := initRunnerSchemaTx(tx, version); err != nil {
		return nil, err
	}
	if err := initRetentionSchemaTx(tx, version); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Ledger{db: db, globalLimit: globalLimit}, nil
}

func powerTransitionHasColumnTx(tx *sql.Tx, column string) (bool, error) {
	rows, err := tx.Query(`PRAGMA table_info(managed_power_transitions)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (l *Ledger) RegisterResource(data []byte) error {
	r, err := DecodeResource(data)
	if err != nil {
		return err
	}
	if r.Kind == "lock" {
		return l.registerLockResource(r)
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
	_, err = tx.Exec(`INSERT INTO managed_resources(id,kind,config_revision,capacity,vm_uuid,vm_mac,spec_json,idle_since_ns) VALUES(?,?,?,?,?,?,?,?)`,
		r.ID, r.Kind, r.Revision, r.Capacity, vmUUID(r), vmMAC(r), canonical, time.Now().UTC().UnixNano())
	if err != nil {
		return uniquenessError(err, "resource registration")
	}
	return l.commit(tx)
}

func vmUUID(r ResourceSpec) string {
	if r.VM != nil {
		return strings.ToLower(r.VM.UTMVMUUID)
	}
	return ""
}

func vmMAC(r ResourceSpec) string {
	if r.VM != nil {
		return strings.ToLower(r.VM.MACAddress)
	}
	return ""
}

// UpdateResource replaces operator configuration only while idle. Its fences
// prevent a stale VM observation from changing a newer registration.
func (l *Ledger) UpdateResource(data []byte, expectedConfigRevision, expectedAvailabilityRevision int64) error {
	r, err := DecodeResource(data)
	if err != nil {
		return err
	}
	if r.Revision != expectedConfigRevision+1 {
		return ErrStale
	}
	if r.Kind == "lock" {
		return l.updateLockResource(r, expectedConfigRevision, expectedAvailabilityRevision)
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
	var configRev, availabilityRev int64
	var occupancy int
	var currentKind string
	err = tx.QueryRow(`SELECT config_revision,availability_revision,occupancy,kind FROM managed_resources WHERE id=?`, r.ID).Scan(&configRev, &availabilityRev, &occupancy, &currentKind)
	if err != nil {
		return mapNoRows(err)
	}
	if configRev != expectedConfigRevision || availabilityRev != expectedAvailabilityRevision {
		return ErrStale
	}
	if currentKind == "lock" {
		return ErrConflict
	}
	if occupancy != 0 {
		return ErrBusy
	}
	var activePower int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM managed_power_transitions WHERE state IN ('pending','uncertain') AND (victim_resource_id=? OR target_resource_id=?)`, r.ID, r.ID).Scan(&activePower); err != nil {
		return err
	}
	if activePower != 0 {
		return ErrBusy
	}
	_, err = tx.Exec(`UPDATE managed_resources SET kind=?,config_revision=?,capacity=?,vm_uuid=?,vm_mac=?,spec_json=? WHERE id=?`,
		r.Kind, r.Revision, r.Capacity, vmUUID(r), vmMAC(r), canonical, r.ID)
	if err != nil {
		return uniquenessError(err, "resource update")
	}
	return l.commit(tx)
}

func (l *Ledger) Admit(data []byte, sealedDigest, requestKey, jobID string) (Receipt, error) {
	return l.AdmitSealed(data, sealedDigest, "", requestKey, jobID)
}

// AdmitSealed binds a verified snapshot object to the immutable admission in
// the same transaction. Empty object IDs are retained for legacy ledger-only
// callers; the controller always supplies an object ID.
func (l *Ledger) AdmitSealed(data []byte, sealedDigest, objectID, requestKey, jobID string) (Receipt, error) {
	return l.AdmitSealedInventory(data, sealedDigest, objectID, nil, requestKey, jobID)
}

// AdmitSealedInventory retains the small sealed manifest alongside the key so
// surviving source files can be compared after an explicit bulk purge. The
// controller supplies a verified manifest; ledger-only callers omit it.
func (l *Ledger) AdmitSealedInventory(data []byte, sealedDigest, objectID string, inventory []byte, requestKey, jobID string) (Receipt, error) {
	return l.AdmitWithParent(data, sealedDigest, objectID, inventory, requestKey, jobID, "", nil)
}

func (l *Ledger) AdmitWithParent(data []byte, sealedDigest, objectID string, inventory []byte, requestKey, jobID, installationID string, chain ParentChain) (Receipt, error) {
	if err := admissionParentCapacity(installationID, jobID, chain); err != nil {
		return Receipt{}, err
	}
	if err := chain.Validate(); err != nil {
		return Receipt{}, err
	}
	if len(inventory) > 1<<20 || (len(inventory) > 0 && !json.Valid(inventory)) {
		return Receipt{}, ErrInvalid
	}
	job, err := DecodeJob(data)
	if err != nil {
		return Receipt{}, err
	}
	if !identifier.MatchString(requestKey) || !identifier.MatchString(jobID) {
		return Receipt{}, ErrInvalid
	}
	if job.Input.InventoryDigest != sealedDigest || (objectID != "" && (!strings.HasPrefix(objectID, "sha256:") || !validDigest(strings.TrimPrefix(objectID, "sha256:")))) {
		return Receipt{}, ErrInvalid
	}
	digest, err := CanonicalDigest(job, sealedDigest)
	if err != nil {
		return Receipt{}, err
	}
	canonical, err := json.Marshal(job)
	if err != nil {
		return Receipt{}, err
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.Rollback()
	if nested, err := nestedTx(tx, installationID, chain); err != nil {
		return Receipt{}, err
	} else if nested {
		return Receipt{}, ErrNestedAcquisition
	}
	var priorID, priorDigest string
	err = tx.QueryRow(`SELECT job_id,submitted_digest FROM managed_admissions WHERE request_key=?`, requestKey).Scan(&priorID, &priorDigest)
	if err == nil {
		if priorDigest != digest {
			return Receipt{}, ErrConflict
		}
		return l.admissionReceiptTx(tx, priorID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, err
	}
	if err := checkAdmissionLossTx(tx, requestKey); err != nil {
		return Receipt{}, err
	}
	var configRevision int64
	var availability, kind string
	err = tx.QueryRow(`SELECT config_revision,availability,kind FROM managed_resources WHERE id=?`, job.ResourceID).Scan(&configRevision, &availability, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, ErrNotFound
	}
	if err != nil {
		return Receipt{}, err
	}
	if kind == "lock" {
		return Receipt{}, ErrUnsupported
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(`INSERT INTO managed_jobs(id,request_key,submitted_digest,resource_id,pinned_config_revision,admitted_at_ns,queue_deadline_ns,spec_json) VALUES(?,?,?,?,?,?,?,?)`,
		jobID, requestKey, digest, job.ResourceID, configRevision, now.UnixNano(), now.Add(time.Duration(job.Deadlines.QueueSeconds)*time.Second).UnixNano(), canonical); err != nil {
		return Receipt{}, admissionError(err)
	}

	parentJSON, _ := json.Marshal(chain)
	if _, err := tx.Exec(`INSERT INTO managed_parent_context(job_id,chain_json) VALUES(?,?)`, jobID, parentJSON); err != nil {
		return Receipt{}, err
	}

	inserted, err := tx.Exec(`INSERT INTO managed_admissions(request_key,job_id,submitted_digest,spec_json,sealed_digest,object_id,inventory_json,identity_json) VALUES(?,?,?,?,?,?,?,?)`, requestKey, jobID, digest, canonical, sealedDigest, objectID, inventory, []byte(`{}`))
	if err != nil {
		return Receipt{}, admissionError(err)
	}
	sequence, err := inserted.LastInsertId()
	if err != nil {
		return Receipt{}, err
	}
	identityJSON, err := json.Marshal(admissionIdentity(job, sequence, requestKey, digest, sealedDigest, objectID, configRevision, now.UnixNano()))
	if err != nil {
		return Receipt{}, err
	}
	if _, err := tx.Exec(`UPDATE managed_admissions SET identity_json=? WHERE job_id=?`, identityJSON, jobID); err != nil {
		return Receipt{}, err
	}
	if _, err := tx.Exec(`INSERT INTO managed_event_progress(job_id) VALUES(?)`, jobID); err != nil {
		return Receipt{}, err
	}
	if _, err := tx.Exec(`INSERT INTO managed_job_inputs(job_id,sealed_digest,object_id) VALUES(?,?,?)`, jobID, sealedDigest, objectID); err != nil {
		return Receipt{}, err
	}
	if _, err := addEvent(tx, jobID, "admitted", map[string]any{"resource_id": job.ResourceID, "submitted_digest": digest}); err != nil {
		return Receipt{}, err
	}
	receipt, err := l.snapshotTx(tx, jobID)
	if err != nil {
		return Receipt{}, err
	}
	if err := l.commit(tx); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

type ReserveRequest struct {
	JobID                        string
	ExpectedJobRevision          int64
	ExpectedConfigRevision       int64
	ExpectedAvailabilityRevision int64
	MutationKey                  string
	VMGuard                      *VMReservationGuard
}

// Reserve owns the commit guards described at NextQueued; selection is advisory.
func (l *Ledger) Reserve(q ReserveRequest) (Receipt, error) {
	receipt, err := l.mutate("reserve", q.MutationKey, q, func(tx *sql.Tx) (Receipt, error) {
		var resourceID, state string
		var revision, pinned int64
		err := tx.QueryRow(`SELECT resource_id,state,revision,pinned_config_revision FROM managed_jobs WHERE id=?`, q.JobID).Scan(&resourceID, &state, &revision, &pinned)
		if err != nil {
			return Receipt{}, mapNoRows(err)
		}
		if revision != q.ExpectedJobRevision || pinned != q.ExpectedConfigRevision {
			return Receipt{}, ErrStale
		}
		if state != "queued" {
			return Receipt{}, ErrConflict
		}
		var deadline int64
		if err := tx.QueryRow(`SELECT queue_deadline_ns FROM managed_jobs WHERE id=?`, q.JobID).Scan(&deadline); err != nil {
			return Receipt{}, err
		}
		if time.Now().UTC().UnixNano() >= deadline {
			return Receipt{}, ErrBusy
		}
		var queueHead string
		if err := tx.QueryRow(`SELECT j.id FROM managed_jobs j JOIN managed_admissions a ON a.job_id=j.id WHERE j.resource_id=? AND j.state='queued' ORDER BY a.sequence LIMIT 1`, resourceID).Scan(&queueHead); err != nil {
			return Receipt{}, mapNoRows(err)
		}
		if queueHead != q.JobID {
			return Receipt{}, ErrBusy
		}
		var configRev, availabilityRev, generation int64
		var availability, kind string
		var capacity, occupancy int
		err = tx.QueryRow(`SELECT config_revision,availability_revision,availability,capacity,occupancy,lease_generation,kind FROM managed_resources WHERE id=?`, resourceID).
			Scan(&configRev, &availabilityRev, &availability, &capacity, &occupancy, &generation, &kind)
		if err != nil {
			return Receipt{}, mapNoRows(err)
		}
		if kind == "lock" {
			return Receipt{}, ErrUnsupported
		}
		if configRev != q.ExpectedConfigRevision || availabilityRev != q.ExpectedAvailabilityRevision {
			return Receipt{}, ErrStale
		}
		if availability != "available" || occupancy >= capacity {
			return Receipt{}, ErrBusy
		}
		if pending, err := pendingPowerTx(tx, resourceID); err != nil {
			return Receipt{}, err
		} else if pending {
			return Receipt{}, ErrBusy
		}
		var global int
		if err := tx.QueryRow(`SELECT COALESCE(SUM(occupancy),0) FROM managed_resources`).Scan(&global); err != nil {
			return Receipt{}, err
		}
		if global >= l.globalLimit {
			return Receipt{}, ErrBusy
		}
		slot := 0
		for ; slot < capacity; slot++ {
			var count int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM managed_jobs WHERE resource_id=? AND slot=? AND state!='finished'`, resourceID, slot).Scan(&count); err != nil {
				return Receipt{}, err
			}
			if count == 0 {
				break
			}
		}
		if slot == capacity {
			return Receipt{}, ErrBusy
		}
		generation++
		if _, err := tx.Exec(`UPDATE managed_resources SET occupancy=occupancy+1,lease_generation=? WHERE id=?`, generation, resourceID); err != nil {
			return Receipt{}, err
		}
		if _, err := tx.Exec(`UPDATE managed_jobs SET state='preparing',revision=revision+1,slot=?,lease_generation=? WHERE id=?`, slot, generation, q.JobID); err != nil {
			return Receipt{}, err
		}
		if err := reserveVMHostPlaceTx(tx, q.JobID, resourceID, generation, q.VMGuard); err != nil {
			return Receipt{}, err
		}
		if err := issueParentNonceTx(tx, q.JobID, generation); err != nil {
			return Receipt{}, err
		}
		if _, err := tx.Exec(`DELETE FROM managed_queue_blockers WHERE job_id=?`, q.JobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, q.JobID, "reserved", map[string]any{"slot": slot, "lease_generation": generation, "reserved_at": time.Now().UTC().Format(time.RFC3339Nano)})
		if err != nil {
			return Receipt{}, err
		}
		receipt, err := l.snapshotTx(tx, q.JobID)
		receipt.CapacityDelta = 1
		receipt.EventCursor = cursor
		return receipt, err
	})
	var blocked *HostCapacityBlocked
	if errors.As(err, &blocked) {
		if recordErr := l.RecordQueueBlocker(q.JobID, q.ExpectedJobRevision, blocked.Blocker); recordErr != nil && !errors.Is(recordErr, ErrStale) {
			return Receipt{}, errors.Join(err, recordErr)
		}
	}
	return receipt, err
}

type NoEffectObservationRequest struct {
	JobID                  string
	ExpectedJobRevision    int64
	ResourceID             string
	ExpectedConfigRevision int64
	Slot                   int
	LeaseGeneration        int64
	Source                 string
	Mechanism              string
	EvidenceDigest         string
	Proof                  NoEffectProof
	MutationKey            string
}

// RecordNoEffectObservation binds a machine observation to the live lease.
// The adapter supplies the evidence digest; TASK-033/TASK-016 own the actual
// host and VM observations that produce it.
func (l *Ledger) RecordNoEffectObservation(q NoEffectObservationRequest) (Receipt, error) {
	if !q.Proof.valid() || q.Source != "machine" || q.Mechanism == "" || len(q.Mechanism) > 256 || !validDigest(q.EvidenceDigest) {
		return Receipt{}, ErrInvalid
	}
	return l.mutate("record_no_effect_observation", q.MutationKey, q, func(tx *sql.Tx) (Receipt, error) {
		var resourceID, state string
		var revision, pinned, lease int64
		var slot sql.NullInt64
		err := tx.QueryRow(`SELECT resource_id,state,revision,pinned_config_revision,slot,lease_generation FROM managed_jobs WHERE id=?`, q.JobID).Scan(&resourceID, &state, &revision, &pinned, &slot, &lease)
		if err != nil {
			return Receipt{}, mapNoRows(err)
		}
		if resourceID != q.ResourceID || revision != q.ExpectedJobRevision || pinned != q.ExpectedConfigRevision || !slot.Valid || int(slot.Int64) != q.Slot || lease != q.LeaseGeneration {
			return Receipt{}, ErrStale
		}
		if state != "preparing" && state != "ready" {
			return Receipt{}, ErrConflict
		}
		if pending, err := pendingPowerTx(tx, resourceID); err != nil {
			return Receipt{}, err
		} else if pending {
			return Receipt{}, ErrBlocked
		}
		var latestReconcile int64
		if err := tx.QueryRow(`SELECT COALESCE(MAX(reconciled_at_ns),0) FROM managed_power_transitions WHERE victim_resource_id=? OR target_resource_id=?`, resourceID, resourceID).Scan(&latestReconcile); err != nil {
			return Receipt{}, err
		}
		now := time.Now().UTC().UnixNano()
		if latestReconcile > now {
			return Receipt{}, ErrBlocked
		}
		proofJSON, _ := json.Marshal(q.Proof)
		_, err = tx.Exec(`INSERT INTO managed_no_effect_observations(reference,job_id,resource_id,config_revision,slot,lease_generation,source,mechanism,evidence_digest,recorded_at_ns,proof_json) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			q.Proof.ObservationReference, q.JobID, q.ResourceID, q.ExpectedConfigRevision, q.Slot, q.LeaseGeneration, q.Source, q.Mechanism, q.EvidenceDigest, now, proofJSON)
		if err != nil {
			return Receipt{}, fmt.Errorf("%w: observation reference: %v", ErrConflict, err)
		}
		if _, err := tx.Exec(`UPDATE managed_jobs SET revision=revision+1 WHERE id=?`, q.JobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, q.JobID, "no_effect_observed", map[string]any{"reference": q.Proof.ObservationReference, "evidence_digest": q.EvidenceDigest})
		if err != nil {
			return Receipt{}, err
		}
		receipt, err := l.snapshotTx(tx, q.JobID)
		receipt.EventCursor = cursor
		return receipt, err
	})
}

type ClassifyNoEffectRequest struct {
	JobID                        string
	ExpectedJobRevision          int64
	ResourceID                   string
	ExpectedConfigRevision       int64
	ExpectedAvailabilityRevision int64
	Slot                         int
	LeaseGeneration              int64
	FailureKind                  string // cancel or prepare_failure
	VMUnavailable                bool
	HostPauseReason              string
	Proof                        NoEffectProof
	MutationKey                  string
}

func (l *Ledger) ClassifyNoEffect(q ClassifyNoEffectRequest) (Receipt, error) {
	if !q.Proof.valid() || !oneOf(q.FailureKind, "cancel", "prepare_failure", "prepare_deadline") {
		return Receipt{}, ErrInvalid
	}
	return l.mutate("classify_no_effect", q.MutationKey, q, func(tx *sql.Tx) (Receipt, error) {
		var resourceID, state string
		var revision, pinned, lease int64
		var slot sql.NullInt64
		err := tx.QueryRow(`SELECT resource_id,state,revision,pinned_config_revision,slot,lease_generation FROM managed_jobs WHERE id=?`, q.JobID).
			Scan(&resourceID, &state, &revision, &pinned, &slot, &lease)
		if err != nil {
			return Receipt{}, mapNoRows(err)
		}
		if resourceID != q.ResourceID || pinned != q.ExpectedConfigRevision || revision != q.ExpectedJobRevision || !slot.Valid || int(slot.Int64) != q.Slot || lease != q.LeaseGeneration {
			return Receipt{}, ErrStale
		}
		if state != "preparing" && state != "ready" {
			return Receipt{}, ErrConflict
		}
		var kind, availability string
		var configRev, availabilityRev int64
		err = tx.QueryRow(`SELECT kind,config_revision,availability_revision,availability FROM managed_resources WHERE id=?`, resourceID).Scan(&kind, &configRev, &availabilityRev, &availability)
		if err != nil {
			return Receipt{}, mapNoRows(err)
		}
		if configRev != q.ExpectedConfigRevision || availabilityRev != q.ExpectedAvailabilityRevision {
			return Receipt{}, ErrStale
		}
		if pending, err := pendingPowerTx(tx, resourceID); err != nil {
			return Receipt{}, err
		} else if pending {
			return Receipt{}, ErrBlocked
		}
		if err := observedNoEffectTx(tx, q.Proof, q.JobID, resourceID, q.ExpectedConfigRevision, q.Slot, q.LeaseGeneration); err != nil {
			return Receipt{}, err
		}
		if q.VMUnavailable && (kind != "utm-macos" || q.FailureKind != "prepare_failure") {
			return Receipt{}, ErrInvalid
		}
		if q.VMUnavailable && availability == "available" {
			_, err = tx.Exec(`UPDATE managed_resources SET availability='paused',availability_reason='vm_unavailable',availability_revision=availability_revision+1 WHERE id=?`, resourceID)
			if err != nil {
				return Receipt{}, err
			}
			availabilityRev++
		}
		if q.HostPauseReason != "" {
			if q.VMUnavailable || kind != "utm-macos" || q.FailureKind != "prepare_failure" || !oneOf(q.HostPauseReason, "host_vm_limit", "host_session_unavailable", "host_permission_unavailable") {
				return Receipt{}, ErrInvalid
			}
			if _, err := tx.Exec(`UPDATE managed_resources SET availability='paused',availability_reason=?,availability_revision=availability_revision+1 WHERE kind='utm-macos' AND availability='available'`, q.HostPauseReason); err != nil {
				return Receipt{}, err
			}
			if err := tx.QueryRow(`SELECT availability_revision FROM managed_resources WHERE id=?`, resourceID).Scan(&availabilityRev); err != nil {
				return Receipt{}, err
			}
		}
		proofJSON, _ := json.Marshal(q.Proof)
		_, err = tx.Exec(`UPDATE managed_jobs SET state='settling',revision=revision+1,no_effect_proof_json=?,failure_kind=?,pause_availability_revision=? WHERE id=?`, proofJSON, q.FailureKind, availabilityRev, q.JobID)
		if err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, q.JobID, "no_effect_classified", map[string]any{"failure_kind": q.FailureKind, "vm_unavailable": q.VMUnavailable, "host_pause_reason": q.HostPauseReason, "availability_revision": availabilityRev})
		if err != nil {
			return Receipt{}, err
		}
		receipt, err := l.snapshotTx(tx, q.JobID)
		receipt.EventCursor = cursor
		return receipt, err
	})
}

type SettleNoEffectRequest struct {
	JobID                        string
	ExpectedJobRevision          int64
	ResourceID                   string
	ExpectedConfigRevision       int64
	ExpectedAvailabilityRevision int64
	Slot                         int
	LeaseGeneration              int64
	MutationKey                  string
}

func (l *Ledger) SettleNoEffect(q SettleNoEffectRequest) (Receipt, error) {
	return l.mutate("settle_no_effect", q.MutationKey, q, func(tx *sql.Tx) (Receipt, error) {
		var resourceID, state, failure string
		var revision, pinned, lease, pauseRev int64
		var slot sql.NullInt64
		var proofJSON []byte
		err := tx.QueryRow(`SELECT resource_id,state,revision,pinned_config_revision,slot,lease_generation,pause_availability_revision,no_effect_proof_json,failure_kind FROM managed_jobs WHERE id=?`, q.JobID).
			Scan(&resourceID, &state, &revision, &pinned, &slot, &lease, &pauseRev, &proofJSON, &failure)
		if err != nil {
			return Receipt{}, mapNoRows(err)
		}
		if resourceID != q.ResourceID || revision != q.ExpectedJobRevision || pinned != q.ExpectedConfigRevision || !slot.Valid || int(slot.Int64) != q.Slot || lease != q.LeaseGeneration || pauseRev != q.ExpectedAvailabilityRevision {
			return Receipt{}, ErrStale
		}
		if state != "settling" {
			return Receipt{}, ErrConflict
		}
		var proof NoEffectProof
		if json.Unmarshal(proofJSON, &proof) != nil || !proof.valid() {
			return Receipt{}, ErrBlocked
		}
		var configRev, availabilityRev int64
		var availability string
		var occupancy int
		err = tx.QueryRow(`SELECT config_revision,availability_revision,availability,occupancy FROM managed_resources WHERE id=?`, resourceID).Scan(&configRev, &availabilityRev, &availability, &occupancy)
		if err != nil {
			return Receipt{}, mapNoRows(err)
		}
		if configRev != q.ExpectedConfigRevision || availabilityRev != q.ExpectedAvailabilityRevision {
			return Receipt{}, ErrStale
		}
		if pending, err := pendingPowerTx(tx, resourceID); err != nil {
			return Receipt{}, err
		} else if pending {
			return Receipt{}, ErrBlocked
		}
		if err := observedNoEffectTx(tx, proof, q.JobID, resourceID, q.ExpectedConfigRevision, q.Slot, q.LeaseGeneration); err != nil {
			return Receipt{}, err
		}
		if occupancy <= 0 {
			return Receipt{}, ErrBlocked
		}
		outcome := WorkloadOutcome("inconclusive")
		verdict := Incomplete
		if failure == "cancel" {
			outcome = "cancelled"
			verdict = Cancelled
		}
		facts := ResultFacts{WorkloadOutcome: outcome, EvidenceCompleteness: "complete", CleanupOutcome: "not_needed", ManualEligible: true, CancelledBeforeOutcome: failure == "cancel", TerminationProven: true, NoOwnedEffect: true}
		derived, err := DeriveVerdict(facts)
		if err != nil || derived != verdict {
			return Receipt{}, ErrBlocked
		}
		timings, err := resultTimingsTx(tx, q.JobID)
		if err != nil {
			return Receipt{}, err
		}
		resultJSON, _ := json.Marshal(Result{Schema: "haepari.result/v1", JobID: q.JobID, ResourceID: resourceID,
			ResourceRevision: configRev, LeaseGeneration: lease, JobState: "finished", ResourceAvailability: availability,
			WorkloadOutcome: outcome, EvidenceCompleteness: "complete", CleanupOutcome: "not_needed", FinalVerdict: verdict,
			Observations: &ResultObservations{Source: q.JobID + ":" + proof.ObservationReference}, Timings: timings})
		if err := releaseSlotTx(tx, resourceID); err != nil {
			return Receipt{}, err
		}
		if _, err := tx.Exec(`UPDATE managed_jobs SET state='finished',revision=revision+1,result_json=? WHERE id=?`, resultJSON, q.JobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, q.JobID, "finished", map[string]any{"verdict": verdict, "cleanup_outcome": "not_needed"})
		if err != nil {
			return Receipt{}, err
		}
		receipt, err := l.snapshotTx(tx, q.JobID)
		receipt.Verdict = verdict
		receipt.CapacityDelta = -1
		receipt.EventCursor = cursor
		return receipt, err
	})
}

func (l *Ledger) CancelQueued(jobID string, expectedRevision int64, mutationKey string) (Receipt, error) {
	return l.finishQueued(jobID, expectedRevision, mutationKey, "cancel_queued", "cancelled", "cancelled")
}

func (l *Ledger) ExpireQueued(jobID string, expectedRevision int64, mutationKey string) (Receipt, error) {
	return l.finishQueued(jobID, expectedRevision, mutationKey, "queue_deadline", "inconclusive", "queue_expired")
}

func (l *Ledger) FailQueuedConfiguration(jobID string, expectedRevision int64, mutationKey string) (Receipt, error) {
	return l.finishQueued(jobID, expectedRevision, mutationKey, "configuration_changed", "inconclusive", "resource_configuration_changed")
}

func (l *Ledger) FailQueuedInput(jobID string, expectedRevision int64, mutationKey string) (Receipt, error) {
	return l.finishQueued(jobID, expectedRevision, mutationKey, "input_unavailable", "inconclusive", "sealed_input_unavailable")
}

func (l *Ledger) finishQueued(jobID string, expectedRevision int64, mutationKey, kind string, outcome WorkloadOutcome, reason string) (Receipt, error) {
	q := struct {
		JobID            string
		ExpectedRevision int64
	}{jobID, expectedRevision}
	return l.mutate(kind, mutationKey, q, func(tx *sql.Tx) (Receipt, error) {
		var state string
		var revision int64
		err := tx.QueryRow(`SELECT state,revision FROM managed_jobs WHERE id=?`, jobID).Scan(&state, &revision)
		if err != nil {
			return Receipt{}, mapNoRows(err)
		}
		if revision != expectedRevision {
			return Receipt{}, ErrStale
		}
		if state != "queued" {
			return Receipt{}, ErrConflict
		}
		if kind == "queue_deadline" {
			var deadline int64
			if err := tx.QueryRow(`SELECT queue_deadline_ns FROM managed_jobs WHERE id=?`, jobID).Scan(&deadline); err != nil {
				return Receipt{}, err
			}
			if time.Now().UTC().UnixNano() < deadline {
				return Receipt{}, ErrBusy
			}
		}
		facts := ResultFacts{WorkloadOutcome: outcome, EvidenceCompleteness: "complete", CleanupOutcome: "not_needed", ManualEligible: true, CancelledBeforeOutcome: kind == "cancel_queued", TerminationProven: true, NoOwnedEffect: true}
		verdict, err := DeriveVerdict(facts)
		if err != nil {
			return Receipt{}, err
		}
		var resourceID, availability string
		var configRev int64
		if err := tx.QueryRow(`SELECT j.resource_id,r.availability,j.pinned_config_revision FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, jobID).Scan(&resourceID, &availability, &configRev); err != nil {
			return Receipt{}, err
		}
		timings, err := resultTimingsTx(tx, jobID)
		if err != nil {
			return Receipt{}, err
		}
		resultJSON, _ := json.Marshal(Result{Schema: "haepari.result/v1", JobID: jobID, ResourceID: resourceID,
			ResourceRevision: configRev, JobState: "finished", ResourceAvailability: availability, WorkloadOutcome: facts.WorkloadOutcome,
			EvidenceCompleteness: facts.EvidenceCompleteness, CleanupOutcome: facts.CleanupOutcome, FinalVerdict: verdict, Reason: reason, Timings: timings})
		if _, err := tx.Exec(`UPDATE managed_jobs SET state='finished',revision=revision+1,result_json=? WHERE id=?`, resultJSON, jobID); err != nil {
			return Receipt{}, err
		}
		if _, err := tx.Exec(`DELETE FROM managed_queue_blockers WHERE job_id=?`, jobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, jobID, "finished", map[string]any{"verdict": verdict, "cleanup_outcome": "not_needed", "reason": reason})
		if err != nil {
			return Receipt{}, err
		}
		receipt, err := l.snapshotTx(tx, jobID)
		receipt.Verdict = verdict
		receipt.EventCursor = cursor
		return receipt, err
	})
}

func (l *Ledger) Snapshot(jobID string) (Receipt, error) {
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Receipt{}, err
	}
	defer tx.Rollback()
	return l.snapshotTx(tx, jobID)
}

type rowQuerier interface{ QueryRow(string, ...any) *sql.Row }

func (l *Ledger) snapshotTx(q rowQuerier, jobID string) (Receipt, error) {
	return l.admissionReceiptTx(q, jobID)
}

func (l *Ledger) currentSnapshotTx(q rowQuerier, jobID string) (Receipt, error) {
	var r Receipt
	var slot sql.NullInt64
	var lease sql.NullInt64
	var resultJSON []byte
	err := q.QueryRow(`SELECT j.id,j.revision,j.state,j.resource_id,r.config_revision,r.availability_revision,r.availability,j.slot,j.lease_generation,r.occupancy,j.result_json FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, jobID).
		Scan(&r.JobID, &r.JobRevision, &r.JobState, &r.ResourceID, &r.ResourceConfigRevision, &r.ResourceAvailabilityRevision, &r.ResourceAvailability, &slot, &lease, &r.ResourceOccupancy, &resultJSON)
	if err != nil {
		return r, mapNoRows(err)
	}
	if slot.Valid {
		r.Slot = int(slot.Int64)
	} else {
		r.Slot = -1
	}
	if lease.Valid {
		r.LeaseGeneration = lease.Int64
	}
	if len(resultJSON) > 0 {
		var result Result
		if err := json.Unmarshal(resultJSON, &result); err != nil {
			return r, err
		}
		r.Verdict = result.FinalVerdict
	}
	if err := q.QueryRow(`SELECT COALESCE(SUM(occupancy),0) FROM managed_resources`).Scan(&r.GlobalOccupancy); err != nil {
		return r, err
	}
	return r, nil
}

func (l *Ledger) mutate(kind, key string, request any, do func(*sql.Tx) (Receipt, error)) (Receipt, error) {
	if key == "" || len(key) > 128 {
		return Receipt{}, ErrInvalid
	}
	encoded, err := json.Marshal(struct {
		Kind    string `json:"kind"`
		Request any    `json:"request"`
	}{kind, request})
	if err != nil {
		return Receipt{}, err
	}
	digestBytes := sha256.Sum256(encoded)
	digest := hex.EncodeToString(digestBytes[:])
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.Rollback()
	var priorDigest string
	var priorJSON []byte
	err = tx.QueryRow(`SELECT digest,receipt_json FROM managed_mutations WHERE mutation_key=?`, key).Scan(&priorDigest, &priorJSON)
	if err == nil {
		if priorDigest != digest {
			return Receipt{}, ErrConflict
		}
		var prior Receipt
		if err := json.Unmarshal(priorJSON, &prior); err != nil {
			return Receipt{}, err
		}
		return prior, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, err
	}
	receipt, err := do(tx)
	if err != nil {
		return Receipt{}, err
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return Receipt{}, err
	}
	if _, err := tx.Exec(`INSERT INTO managed_mutations(mutation_key,digest,receipt_json) VALUES(?,?,?)`, key, digest, receiptJSON); err != nil {
		return Receipt{}, err
	}
	if err := l.commit(tx); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func addEvent(tx *sql.Tx, jobID, kind string, payload any) (int64, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	result, err := tx.Exec(`INSERT INTO managed_events(job_id,kind,payload_json) VALUES(?,?,?)`, jobID, kind, encoded)
	if err != nil {
		return 0, err
	}
	cursor, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO managed_event_owners(cursor,job_id) VALUES(?,?)`, cursor, jobID); err != nil {
		return 0, err
	}
	return cursor, nil
}
func mapNoRows(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func pendingPowerTx(tx *sql.Tx, resourceID string) (bool, error) {
	var count int
	err := tx.QueryRow(`SELECT COUNT(*) FROM managed_power_transitions WHERE state IN ('pending','uncertain') AND (victim_resource_id=? OR target_resource_id=?)`, resourceID, resourceID).Scan(&count)
	return count != 0, err
}

func observedNoEffectTx(tx *sql.Tx, proof NoEffectProof, jobID, resourceID string, configRevision int64, slot int, lease int64) error {
	var recordedJob, recordedResource, source, mechanism, evidenceDigest string
	var recordedConfig, recordedLease, recordedAt int64
	var recordedSlot int
	var recordedProofJSON []byte
	err := tx.QueryRow(`SELECT job_id,resource_id,config_revision,slot,lease_generation,source,mechanism,evidence_digest,recorded_at_ns,proof_json FROM managed_no_effect_observations WHERE reference=?`, proof.ObservationReference).
		Scan(&recordedJob, &recordedResource, &recordedConfig, &recordedSlot, &recordedLease, &source, &mechanism, &evidenceDigest, &recordedAt, &recordedProofJSON)
	if err != nil {
		return ErrBlocked
	}
	var recordedProof NoEffectProof
	if json.Unmarshal(recordedProofJSON, &recordedProof) != nil || recordedProof != proof || recordedJob != jobID || recordedResource != resourceID || recordedConfig != configRevision || recordedSlot != slot || recordedLease != lease || source != "machine" || mechanism == "" || !validDigest(evidenceDigest) {
		return ErrBlocked
	}
	var latestReconcile int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(reconciled_at_ns),0) FROM managed_power_transitions WHERE victim_resource_id=? OR target_resource_id=?`, resourceID, resourceID).Scan(&latestReconcile); err != nil {
		return err
	}
	if latestReconcile > recordedAt {
		return ErrBlocked
	}
	return nil
}
