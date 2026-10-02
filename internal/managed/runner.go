package managed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

func checkRunnerSchemaTx(tx *sql.Tx) error {
	if _, err := tx.Exec(`SELECT job_id,run_id,kind,digest,payload_json FROM managed_runner_receipts LIMIT 0`); err != nil {
		return fmt.Errorf("managed schema8 managed_runner_receipts: %w", err)
	}
	return nil
}
func initRunnerSchemaTx(tx *sql.Tx, version int) error {
	if version >= 8 {
		return nil
	}
	if _, err := tx.Exec(`CREATE TABLE managed_runner_receipts (job_id TEXT NOT NULL REFERENCES managed_jobs(id),run_id TEXT NOT NULL,kind TEXT NOT NULL,digest TEXT NOT NULL,payload_json BLOB NOT NULL,PRIMARY KEY(job_id,kind))`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version,identity) VALUES(8,'independent-runner-receipts-v8')`); err != nil {
		return err
	}
	_, err := tx.Exec(`PRAGMA user_version=8`)
	return err
}

// RecordRunnerReceipt imports a validated receipt under the current exact
// Job/resource/configuration/slot/lease/run/nonce. No transaction spans a
// native effect. Lost legacy evidence is never backfilled during migration.
func (l *Ledger) RecordRunnerReceipt(identity LocalIdentity, kind string, payload []byte) error {
	if !identifier.MatchString(identity.JobID) || !identifier.MatchString(identity.RunID) || identity.Nonce == "" || len(payload) == 0 || len(payload) > 2<<20 {
		return ErrInvalid
	}
	if kind != "request" && kind != "intent" && kind != "start" && kind != "outcome" && kind != "exit" {
		return ErrInvalid
	}
	var header struct {
		Identity      LocalIdentity `json:"identity"`
		Schema        string        `json:"schema"`
		RequestDigest string        `json:"request_digest"`
	}
	if ValidateUniqueJSONKeys(payload) != nil || json.Unmarshal(payload, &header) != nil || header.Identity != identity || header.Schema != "haepari.runner-"+kind+"/v1" {
		return ErrInvalid
	}
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := localIdentityTx(tx, identity.JobID)
	if err != nil {
		return err
	}
	expected, _ := json.Marshal(identity)
	if !bytes.Equal(current, expected) {
		return ErrStale
	}
	var priorDigest string
	err = tx.QueryRow(`SELECT digest FROM managed_runner_receipts WHERE job_id=? AND kind=?`, identity.JobID, kind).Scan(&priorDigest)
	if err == nil {
		if priorDigest != digest {
			return ErrConflict
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	if kind != "request" {
		var requestDigest string
		if err := tx.QueryRow(`SELECT digest FROM managed_runner_receipts WHERE job_id=? AND run_id=? AND kind='request'`, identity.JobID, identity.RunID).Scan(&requestDigest); err != nil {
			if err == sql.ErrNoRows {
				return ErrBlocked
			}
			return err
		}
		if header.RequestDigest != requestDigest {
			return ErrConflict
		}
		predecessor := map[string]string{"intent": "request", "start": "intent", "outcome": "intent", "exit": "outcome"}[kind]
		var present int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM managed_runner_receipts WHERE job_id=? AND run_id=? AND kind=?`, identity.JobID, identity.RunID, predecessor).Scan(&present); err != nil {
			return err
		}
		if present != 1 {
			return ErrBlocked
		}
	}
	if _, err := tx.Exec(`INSERT INTO managed_runner_receipts(job_id,run_id,kind,digest,payload_json) VALUES(?,?,?,?,?)`, identity.JobID, identity.RunID, kind, digest, payload); err != nil {
		return err
	}
	if _, err := addEvent(tx, identity.JobID, "runner_receipt", map[string]any{"run_id": identity.RunID, "kind": kind, "digest": digest}); err != nil {
		return err
	}
	return l.commit(tx)
}
