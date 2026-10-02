package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func checkRetentionSchemaTx(tx *sql.Tx) error {
	for _, q := range []string{`SELECT job_id,request_json,complete FROM managed_purges LIMIT 0`, `SELECT mutation_key,resource_id,request_json,receipt_json FROM managed_evidence_recovery LIMIT 0`} {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("managed schema9 %s: %w", strings.Fields(q)[3], err)
		}
	}
	return nil
}

func initRetentionSchemaTx(tx *sql.Tx, version int) error {
	if version >= 9 {
		return nil
	}
	for _, q := range []string{
		`CREATE TABLE managed_purges(job_id TEXT PRIMARY KEY REFERENCES managed_admissions(job_id),request_json BLOB NOT NULL,complete INTEGER NOT NULL DEFAULT 0 CHECK(complete IN (0,1)))`,
		`CREATE TABLE managed_evidence_recovery(mutation_key TEXT PRIMARY KEY,resource_id TEXT NOT NULL REFERENCES managed_resources(id),request_json BLOB NOT NULL,receipt_json BLOB)`,
		`INSERT INTO schema_migrations(version,identity) VALUES(9,'evidence-retention-v9')`,
		`PRAGMA user_version=9`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

type BulkSnapshot struct {
	Receipt   Receipt         `json:"receipt"`
	Spec      JobSpec         `json:"spec"`
	Result    Result          `json:"result"`
	Identity  *LocalIdentity  `json:"identity,omitempty"`
	Inventory json.RawMessage `json:"inventory"`
}

func (l *Ledger) BulkSnapshot(jobID string) (BulkSnapshot, error) {
	if !ValidRequestKey(jobID) {
		return BulkSnapshot{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BulkSnapshot{}, err
	}
	defer tx.Rollback()
	return l.bulkSnapshotTx(tx, jobID)
}

func (l *Ledger) bulkSnapshotTx(tx *sql.Tx, id string) (BulkSnapshot, error) {
	var b BulkSnapshot
	var err error
	b.Receipt, err = l.snapshotTx(tx, id)
	if err != nil {
		return b, err
	}
	if b.Receipt.JobState != "finished" {
		return b, ErrBusy
	}
	var raw, result, inventory []byte
	if err = tx.QueryRow(`SELECT a.spec_json,a.inventory_json,j.result_json FROM managed_admissions a JOIN managed_jobs j ON j.id=a.job_id WHERE a.job_id=?`, id).Scan(&raw, &inventory, &result); err != nil {
		return b, err
	}
	b.Inventory = inventory
	b.Spec, err = DecodeJob(raw)
	if err != nil {
		return b, err
	}
	b.Result, err = DecodeResultForJob(result, b.Spec)
	if err != nil {
		return b, err
	}
	if b.Result.LeaseGeneration > 0 {
		raw, err = localIdentityTx(tx, id)
		if err != nil {
			return b, err
		}
		b.Identity = new(LocalIdentity)
		if err = decodeClosed(raw, b.Identity); err != nil {
			return b, err
		}
	}
	return b, nil
}

type RetainedPath struct {
	Path      string `json:"path"`
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	Directory bool   `json:"directory"`
}
type PurgeIntent struct {
	Schema           string         `json:"schema"`
	JobID            string         `json:"job_id"`
	ExpectedRevision int64          `json:"expected_revision"`
	SnapshotDigest   string         `json:"snapshot_digest"`
	Root             RetainedPath   `json:"root"`
	Paths            []RetainedPath `json:"paths"`
	Object           *RetainedPath  `json:"object,omitempty"`
	ObjectPaths      []RetainedPath `json:"object_paths"`
	RecordedAt       time.Time      `json:"recorded_at"`
}
type PurgeReceipt struct {
	Schema    string            `json:"schema"`
	JobID     string            `json:"job_id"`
	Complete  bool              `json:"complete"`
	Admission AdmissionIdentity `json:"admission"`
}

func validPurgeIntent(q PurgeIntent) bool {
	if q.Schema != "haepari.purge-intent/v1" || !ValidRequestKey(q.JobID) || q.ExpectedRevision <= 0 || !validRecoveryDigest(q.SnapshotDigest) || q.RecordedAt.IsZero() || q.Object == nil || !validRecoveryDigest(q.Object.Path) || q.Object.Inode == 0 || !q.Object.Directory || len(q.Paths) > 16000 || len(q.ObjectPaths) == 0 || len(q.ObjectPaths) > 100000 {
		return false
	}
	if len(q.Paths) > 0 && (q.Root.Path != "" || q.Root.Inode == 0 || !q.Root.Directory) {
		return false
	}
	seen := map[string]bool{}
	for _, p := range q.ObjectPaths {
		if !ValidArtifactPath(p.Path) || p.Inode == 0 || seen[p.Path] || p.Path != "manifest.json" && p.Path != "files" && !strings.HasPrefix(p.Path, "files/") || p.Path == "manifest.json" && p.Directory {
			return false
		}
		seen[p.Path] = true
	}
	seen = map[string]bool{}
	for _, p := range q.Paths {
		first := strings.Split(p.Path, "/")[0]
		if !ValidArtifactPath(p.Path) || p.Inode == 0 || seen[p.Path] || first != "artifacts" && first != "output.log" && !strings.HasPrefix(first, "recovery-") || first == "output.log" && (p.Directory || p.Path != first) {
			return false
		}
		seen[p.Path] = true
	}
	return true
}

func (l *Ledger) PurgeIntent(id string) (PurgeIntent, bool, bool, error) {
	var q PurgeIntent
	var raw []byte
	var complete bool
	err := l.db.QueryRow(`SELECT request_json,complete FROM managed_purges WHERE job_id=?`, id).Scan(&raw, &complete)
	if err == sql.ErrNoRows {
		return q, false, false, nil
	}
	if err != nil {
		return q, false, false, err
	}
	if decodeClosed(raw, &q) != nil || !validPurgeIntent(q) || q.JobID != id {
		return q, true, complete, ErrBlocked
	}
	return q, true, complete, nil
}

func (l *Ledger) BeginPurge(q PurgeIntent) error {
	if !validPurgeIntent(q) {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, err := l.bulkSnapshotTx(tx, q.JobID)
	if err != nil {
		return err
	}
	if b.Receipt.JobRevision != q.ExpectedRevision || RecoveryDigest(b) != q.SnapshotDigest {
		return ErrStale
	}
	if b.Receipt.ResultStatus == "unavailable" {
		return ErrResultUnavailable
	}
	if b.Receipt.ResourceAvailability == "quarantined" || b.Receipt.ResourceAvailability == "recovering" {
		return ErrBlocked
	}
	resolved, err := evidenceResolvedTx(tx, b.Result)
	if err != nil {
		return err
	}
	if !resolved {
		return ErrBlocked
	}
	var pending int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM managed_power_transitions WHERE state IN ('pending','uncertain') AND (victim_resource_id=? OR target_resource_id=?)`, b.Result.ResourceID, b.Result.ResourceID).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return ErrBlocked
	}
	if b.Receipt.Admission.ObjectID == "" || q.Object.Path != b.Receipt.Admission.ObjectID || len(b.Inventory) == 0 {
		return ErrBlocked
	}
	// The intent and unavailable receipt precede every bulk deletion. Small
	// immutable result and ownership records remain private reconciliation data.
	cursor, err := retentionEvent(tx, q.JobID, "purge_intent", map[string]any{"expected_revision": q.ExpectedRevision, "snapshot_digest": q.SnapshotDigest})
	if err != nil {
		return err
	}
	tombstone := Receipt{JobID: b.Receipt.JobID, JobRevision: b.Receipt.JobRevision + 1, JobState: "finished", ResourceID: b.Receipt.ResourceID, Admission: b.Receipt.Admission, ResultStatus: "unavailable", EventCursor: cursor}
	raw, err := json.Marshal(tombstone)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE managed_admissions SET retention_state='unavailable',tombstone_receipt=? WHERE job_id=?`, raw, q.JobID); err != nil {
		return err
	}
	raw, err = json.Marshal(q)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO managed_purges(job_id,request_json) VALUES(?,?)`, q.JobID, raw); err != nil {
		return err
	}
	return l.commit(tx)
}

func (l *Ledger) CompletePurge(q PurgeIntent) (PurgeReceipt, error) {
	prior, found, complete, err := l.PurgeIntent(q.JobID)
	if err != nil {
		return PurgeReceipt{}, err
	}
	if !found || RecoveryDigest(prior) != RecoveryDigest(q) {
		return PurgeReceipt{}, ErrConflict
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return PurgeReceipt{}, err
	}
	defer tx.Rollback()
	r, err := l.snapshotTx(tx, q.JobID)
	if err != nil {
		return PurgeReceipt{}, err
	}
	if !complete {
		// Preserve the cursor ownership and floor while deleting event payloads.
		// Native phase/result identities remain private recovery authority.
		if _, err = tx.Exec(`DELETE FROM managed_events WHERE job_id=? AND cursor<=?`, q.JobID, r.EventCursor); err != nil {
			return PurgeReceipt{}, err
		}
		if _, err = tx.Exec(`UPDATE managed_event_progress SET purged_through=MAX(purged_through,?) WHERE job_id=?`, r.EventCursor, q.JobID); err != nil {
			return PurgeReceipt{}, err
		}
		changed, err := tx.Exec(`UPDATE managed_purges SET complete=1 WHERE job_id=? AND complete=0`, q.JobID)
		if err != nil {
			return PurgeReceipt{}, err
		}
		n, err := changed.RowsAffected()
		if err != nil || n != 1 {
			return PurgeReceipt{}, ErrStale
		}
		r.EventCursor, err = retentionEvent(tx, q.JobID, "purge_completed", map[string]any{"expected_revision": q.ExpectedRevision})
		if err != nil {
			return PurgeReceipt{}, err
		}
		r.JobRevision++
		raw, err := json.Marshal(r)
		if err != nil {
			return PurgeReceipt{}, err
		}
		if _, err = tx.Exec(`UPDATE managed_admissions SET tombstone_receipt=? WHERE job_id=?`, raw, q.JobID); err != nil {
			return PurgeReceipt{}, err
		}
	}
	return PurgeReceipt{Schema: "haepari.purge-receipt/v1", JobID: q.JobID, Complete: true, Admission: r.Admission}, l.commit(tx)
}

// Supplementary evidence revisions describe retention, never a changed result.
func retentionEvent(tx *sql.Tx, id, kind string, payload any) (int64, error) {
	if _, err := tx.Exec(`UPDATE managed_jobs SET revision=revision+1 WHERE id=? AND state='finished'`, id); err != nil {
		return 0, err
	}
	return addEvent(tx, id, kind, payload)
}

func (l *Ledger) AvailableObjectReferences(object string) (int, error) {
	var count int
	err := l.db.QueryRow(`SELECT COUNT(*) FROM managed_admissions WHERE object_id=? AND retention_state='available'`, object).Scan(&count)
	return count, err
}
