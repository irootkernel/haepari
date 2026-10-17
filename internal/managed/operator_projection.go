package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// OperatorConfigView is the closed haepari.operator-projection/v1 config view.
// Config is the stored accepted document, not a reread of the mutable file.
type OperatorConfigView struct {
	Schema         string          `json:"schema"`
	View           string          `json:"view"`
	InstallationID string          `json:"installation_id"`
	ObservedAt     time.Time       `json:"observed_at"`
	Config         json.RawMessage `json:"config"`
	DocumentDigest string          `json:"document_digest"`
	ValuesDigest   string          `json:"values_digest"`
}

// ReadAcceptedConfiguration loads and validates the accepted database row.
// A missing or corrupt row is returned as an error. Defaults are not invented.
func (l *Ledger) ReadAcceptedConfiguration() (OperatorConfigView, error) {
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return OperatorConfigView{}, err
	}
	defer tx.Rollback()
	doc, raw, missing, err := loadAcceptedConfigTx(tx)
	if err != nil {
		return OperatorConfigView{}, err
	}
	if missing {
		return OperatorConfigView{}, fmt.Errorf("%w: accepted configuration is absent", ErrNotFound)
	}
	_, documentDigest, valuesDigest, err := doc.Digests()
	if err != nil {
		return OperatorConfigView{}, err
	}
	return OperatorConfigView{
		Schema: "haepari.operator-projection/v1", View: "config",
		Config: append(json.RawMessage(nil), raw...), DocumentDigest: documentDigest, ValuesDigest: valuesDigest,
	}, nil
}
