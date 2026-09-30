package managed

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	driver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// AdmissionIdentity is immutable even after configuration changes or payload
// retention. It is distinct from the current resource fences in a Receipt.
type AdmissionIdentity struct {
	Sequence                 int64  `json:"sequence"`
	RequestKey               string `json:"request_key"`
	SubmittedDigest          string `json:"submitted_digest"`
	InputDigest              string `json:"input_digest"`
	ObjectID                 string `json:"object_id,omitempty"`
	ResourceID               string `json:"resource_id"`
	ResourceConfigRevision   int64  `json:"resource_config_revision"`
	EnvironmentProfileID     string `json:"environment_profile_id"`
	EnvironmentProfileDigest string `json:"environment_profile_digest"`
	AdmittedAt               string `json:"admitted_at"`
}

var ErrResultUnavailable = errors.New("known admission has an unavailable retained result")

func ValidRequestKey(key string) bool { return identifier.MatchString(key) }

// BindAdmissionInventory fills only a migrated admission's absent inventory.
// Callers must read and verify the original object, never rebuild this from a
// mutable source. TASK-012 must bind this before purging a legacy object.
func (l *Ledger) BindAdmissionInventory(key, objectID string, inventory []byte) error {
	if !ValidRequestKey(key) || len(inventory) == 0 || len(inventory) > 1<<20 || !json.Valid(inventory) {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var original, retention string
	var prior []byte
	if err := tx.QueryRow(`SELECT object_id,retention_state,inventory_json FROM managed_admissions WHERE request_key=?`, key).Scan(&original, &retention, &prior); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if err := checkAdmissionLossTx(tx, key); err != nil {
				return err
			}
		}
		return mapNoRows(err)
	}
	if original != objectID || retention != "available" {
		return ErrConflict
	}
	if len(prior) > 0 {
		if !bytes.Equal(prior, inventory) {
			return ErrConflict
		}
		return nil
	}
	if _, err := tx.Exec(`UPDATE managed_admissions SET inventory_json=? WHERE request_key=?`, inventory, key); err != nil {
		return err
	}
	return l.commit(tx)
}

// Only uniqueness errors are conflicts. A disk, trigger or connection
// failure must remain storage uncertainty so a caller reconciles the same key.
func admissionError(err error) error {
	var failure *driver.Error
	if errors.As(err, &failure) && (failure.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || failure.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY) {
		return fmt.Errorf("%w: admission constraint: %v", ErrConflict, err)
	}
	return err
}

func admissionIdentity(job JobSpec, sequence int64, key, digest, sealedDigest, objectID string, revision, admittedNS int64) AdmissionIdentity {
	return AdmissionIdentity{Sequence: sequence, RequestKey: key, SubmittedDigest: digest,
		InputDigest: sealedDigest, ObjectID: objectID, ResourceID: job.ResourceID,
		ResourceConfigRevision: revision, EnvironmentProfileID: job.Environment.ProfileID,
		EnvironmentProfileDigest: job.Environment.ProfileDigest,
		AdmittedAt:               time.Unix(0, admittedNS).UTC().Format(time.RFC3339Nano)}
}

// Backfill immutable identities from the v3 records. In particular, use the
// Job's pinned revision, never the resource's possibly newer configuration.
func migrateAdmissions(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT j.rowid,j.id,j.request_key,j.submitted_digest,j.spec_json,j.pinned_config_revision,j.admitted_at_ns,COALESCE(i.sealed_digest,''),COALESCE(i.object_id,'') FROM managed_jobs j LEFT JOIN managed_job_inputs i ON i.job_id=j.id WHERE NOT EXISTS (SELECT 1 FROM managed_admissions a WHERE a.job_id=j.id) ORDER BY j.rowid`)
	if err != nil {
		return err
	}
	type prior struct {
		sequence, revision, admitted    int64
		id, key, digest, sealed, object string
		spec                            []byte
	}
	var jobs []prior
	for rows.Next() {
		var p prior
		if err := rows.Scan(&p.sequence, &p.id, &p.key, &p.digest, &p.spec, &p.revision, &p.admitted, &p.sealed, &p.object); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range jobs {
		var job JobSpec
		if err := json.Unmarshal(p.spec, &job); err != nil {
			return err
		}
		if p.sealed == "" {
			p.sealed = job.Input.InventoryDigest
		}
		identity, err := json.Marshal(admissionIdentity(job, p.sequence, p.key, p.digest, p.sealed, p.object, p.revision, p.admitted))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO managed_admissions(sequence,request_key,job_id,submitted_digest,spec_json,sealed_digest,object_id,identity_json) VALUES(?,?,?,?,?,?,?,?)`, p.sequence, p.key, p.id, p.digest, p.spec, p.sealed, p.object, identity); err != nil {
			return err
		}
	}
	return nil
}

func (l *Ledger) admissionReceiptTx(q rowQuerier, jobID string) (Receipt, error) {
	var identityJSON, tombstone []byte
	var retention string
	if err := q.QueryRow(`SELECT identity_json,retention_state,tombstone_receipt FROM managed_admissions WHERE job_id=?`, jobID).Scan(&identityJSON, &retention, &tombstone); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if err := checkJobAdmissionLossTx(q, jobID); err != nil {
				return Receipt{}, err
			}
		}
		return Receipt{}, mapNoRows(err)
	}
	var identity AdmissionIdentity
	if err := json.Unmarshal(identityJSON, &identity); err != nil {
		return Receipt{}, err
	}
	if retention == "unavailable" {
		var r Receipt
		if err := json.Unmarshal(tombstone, &r); err != nil {
			return Receipt{}, err
		}
		r.Admission = identity
		return r, nil
	}
	if retention != "available" {
		return Receipt{}, fmt.Errorf("invalid admission retention state for %s", jobID)
	}
	var inputDigest, objectID string
	if err := q.QueryRow(`SELECT sealed_digest,object_id FROM managed_job_inputs WHERE job_id=?`, jobID).Scan(&inputDigest, &objectID); err != nil {
		return Receipt{}, fmt.Errorf("accepted admission %s lost its input reference: %v", jobID, err)
	}
	if inputDigest != identity.InputDigest || objectID != identity.ObjectID {
		return Receipt{}, fmt.Errorf("accepted admission %s has a changed input reference", jobID)
	}
	r, err := l.currentSnapshotTx(q, jobID)
	if errors.Is(err, ErrNotFound) {
		return Receipt{}, fmt.Errorf("accepted admission %s lost its payload without a retention receipt", jobID)
	}
	r.Admission = identity
	return r, err
}

// RetainAdmissionTombstone records the identity side of a future retention
// transaction. TASK-012 owns actual bulk file/row deletion and its authority.
// Calling this never deletes payloads or resets a key. Uncertain/protected work
// must first be reconciled; it cannot be disguised as ordinary purged work.
func (l *Ledger) RetainAdmissionTombstone(jobID string) error {
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := l.admissionReceiptTx(tx, jobID)
	if err != nil {
		return err
	}
	if r.ResultStatus == "unavailable" {
		return nil
	}
	if r.JobState != "finished" || r.ResourceAvailability == "quarantined" {
		return ErrBusy
	}
	var objectID string
	var inventory []byte
	if err := tx.QueryRow(`SELECT object_id,inventory_json FROM managed_admissions WHERE job_id=?`, jobID).Scan(&objectID, &inventory); err != nil {
		return err
	}
	if objectID != "" && len(inventory) == 0 {
		return ErrBusy
	}
	tombstone := Receipt{JobID: r.JobID, JobRevision: r.JobRevision, JobState: "finished", ResourceID: r.ResourceID, Admission: r.Admission, ResultStatus: "unavailable"}
	encoded, err := json.Marshal(tombstone)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE managed_admissions SET retention_state='unavailable',tombstone_receipt=? WHERE job_id=?`, encoded, jobID); err != nil {
		return err
	}
	return l.commit(tx)
}

// A tombstone carries identity and unavailable-result status, not live lease,
// capacity or verdict claims. Ordinary receipt JSON remains unchanged.
func (r Receipt) MarshalJSON() ([]byte, error) {
	if r.ResultStatus == "unavailable" {
		return json.Marshal(struct {
			JobID        string            `json:"job_id"`
			JobRevision  int64             `json:"job_revision"`
			JobState     string            `json:"job_state"`
			ResourceID   string            `json:"resource_id"`
			Admission    AdmissionIdentity `json:"admission"`
			ResultStatus string            `json:"result_status"`
		}{r.JobID, r.JobRevision, r.JobState, r.ResourceID, r.Admission, r.ResultStatus})
	}
	type ordinary Receipt
	return json.Marshal(ordinary(r))
}

func checkAdmissionLossTx(q rowQuerier, key string) error {
	var exists int
	if err := q.QueryRow(`SELECT COUNT(*) FROM managed_jobs WHERE request_key=?`, key).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return fmt.Errorf("accepted request key lost its admission record")
	}
	return nil
}

func checkJobAdmissionLossTx(q rowQuerier, jobID string) error {
	var exists int
	if err := q.QueryRow(`SELECT COUNT(*) FROM managed_jobs WHERE id=?`, jobID).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return fmt.Errorf("accepted job %s lost its admission record", jobID)
	}
	return nil
}
