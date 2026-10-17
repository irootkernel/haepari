package managed

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/irootkernel/haepari/internal/config"
)

func checkEffectiveConfigSchemaTx(tx *sql.Tx) error {
	if _, err := tx.Exec(`SELECT singleton,revision,document_json,document_digest,values_digest FROM managed_effective_config LIMIT 0`); err != nil {
		return fmt.Errorf("managed schema11 managed_effective_config: %w", err)
	}
	if _, err := tx.Exec(`SELECT revision,document_digest,document_json,recorded_at_ns FROM managed_config_audits LIMIT 0`); err != nil {
		return fmt.Errorf("managed schema11 managed_config_audits: %w", err)
	}
	return nil
}

func initEffectiveConfigTx(tx *sql.Tx, version int) error {
	if version >= 11 {
		return nil
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS managed_effective_config (
		singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
		revision INTEGER NOT NULL,
		document_json BLOB NOT NULL,
		document_digest TEXT NOT NULL,
		values_digest TEXT NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS managed_config_audits (
		revision INTEGER PRIMARY KEY,
		document_digest TEXT NOT NULL,
		document_json BLOB NOT NULL,
		recorded_at_ns INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO schema_migrations(version,identity) VALUES(11,'effective-config-v11')`); err != nil {
		return err
	}
	_, err := tx.Exec(`PRAGMA user_version=11`)
	return err
}

type storedEffectiveConfig struct {
	revision int64
	document string
	values   string
}

// CommitEffectiveConfig stores the accepted document and one audit row before
// the controller publishes its socket or dispatches work. An implicit document
// reuses the stored revision when its effective values match, and otherwise
// takes the next revision. An explicit revision is operator-owned: the same
// revision and digest replays with no second audit, while the same revision
// with a changed digest, a lower revision, or a skipped revision is rejected
// and leaves the stored row unchanged.
func (l *Ledger) CommitEffectiveConfig(doc config.Document, fromFile bool) (config.Document, error) {
	if err := doc.Validate(); err != nil {
		return config.Document{}, err
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return config.Document{}, err
	}
	defer tx.Rollback()
	stored, missing, err := readEffectiveConfigTx(tx)
	if err != nil {
		return config.Document{}, err
	}
	if !fromFile {
		doc, err = implicitRevision(doc, stored, missing)
		if err != nil {
			return config.Document{}, err
		}
		// The input document carries the default revision. Reuse the accepted
		// stored revision when the effective values are unchanged.
		if !missing && doc.Revision == stored.revision {
			return doc, nil
		}
	} else if !missing {
		_, documentDigest, _, digestErr := doc.Digests()
		if digestErr != nil {
			return config.Document{}, digestErr
		}
		switch {
		case doc.Revision == stored.revision && documentDigest == stored.document:
			return doc, nil
		case doc.Revision == stored.revision:
			return config.Document{}, fmt.Errorf("%w: configuration revision %d changed the accepted digest", ErrConflict, doc.Revision)
		case doc.Revision < stored.revision:
			return config.Document{}, fmt.Errorf("%w: configuration revision %d is below accepted revision %d", ErrConflict, doc.Revision, stored.revision)
		case doc.Revision != stored.revision+1:
			return config.Document{}, fmt.Errorf("%w: configuration revision %d must be the next revision after %d", ErrConflict, doc.Revision, stored.revision)
		}
	}
	if err := writeEffectiveConfigTx(tx, doc, !missing); err != nil {
		return config.Document{}, err
	}
	if err := tx.Commit(); err != nil {
		return config.Document{}, err
	}
	return doc, nil
}

func implicitRevision(doc config.Document, stored storedEffectiveConfig, missing bool) (config.Document, error) {
	_, _, values, err := doc.Digests()
	if err != nil {
		return doc, err
	}
	if missing {
		doc.Revision = 1
		return doc, doc.Validate()
	}
	if values == stored.values {
		doc.Revision = stored.revision
		return doc, doc.Validate()
	}
	doc.Revision = stored.revision + 1
	return doc, doc.Validate()
}

// loadAcceptedConfigTx returns the stored accepted document. A missing row is
// reported separately. A corrupt or non-canonical row is an error and is never
// replaced with defaults.
func loadAcceptedConfigTx(tx *sql.Tx) (config.Document, []byte, bool, error) {
	var revision int64
	var raw []byte
	var documentDigest, valuesDigest string
	err := tx.QueryRow(`SELECT revision,document_json,document_digest,values_digest FROM managed_effective_config WHERE singleton=1`).Scan(&revision, &raw, &documentDigest, &valuesDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return config.Document{}, nil, true, nil
	}
	if err != nil {
		return config.Document{}, nil, false, err
	}
	doc, _, err := config.Parse(raw)
	if err != nil {
		return config.Document{}, nil, false, fmt.Errorf("%w: accepted configuration is corrupt: %v", ErrInvalid, err)
	}
	if doc.Revision != revision {
		return config.Document{}, nil, false, fmt.Errorf("%w: accepted configuration revision does not match the stored row", ErrInvalid)
	}
	canonical, gotDocument, gotValues, err := doc.Digests()
	if err != nil || !bytes.Equal(canonical, raw) || gotDocument != documentDigest || gotValues != valuesDigest {
		return config.Document{}, nil, false, fmt.Errorf("%w: accepted configuration digest does not match the stored row", ErrInvalid)
	}
	return doc, canonical, false, nil
}

func readEffectiveConfigTx(tx *sql.Tx) (storedEffectiveConfig, bool, error) {
	var stored storedEffectiveConfig
	err := tx.QueryRow(`SELECT revision,document_digest,values_digest FROM managed_effective_config WHERE singleton=1`).Scan(&stored.revision, &stored.document, &stored.values)
	if errors.Is(err, sql.ErrNoRows) {
		return storedEffectiveConfig{}, true, nil
	}
	if err != nil {
		return storedEffectiveConfig{}, false, err
	}
	return stored, false, nil
}

func writeEffectiveConfigTx(tx *sql.Tx, doc config.Document, replace bool) error {
	raw, documentDigest, valuesDigest, err := doc.Digests()
	if err != nil {
		return err
	}
	if replace {
		result, err := tx.Exec(`UPDATE managed_effective_config SET revision=?,document_json=?,document_digest=?,values_digest=? WHERE singleton=1`, doc.Revision, raw, documentDigest, valuesDigest)
		if err != nil {
			return err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return fmt.Errorf("%w: effective configuration was not updated", ErrConflict)
		}
	} else if _, err := tx.Exec(`INSERT INTO managed_effective_config(singleton,revision,document_json,document_digest,values_digest) VALUES(1,?,?,?,?)`, doc.Revision, raw, documentDigest, valuesDigest); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO managed_config_audits(revision,document_digest,document_json,recorded_at_ns) VALUES(?,?,?,?)`, doc.Revision, documentDigest, raw, time.Now().UTC().UnixNano())
	return err
}
