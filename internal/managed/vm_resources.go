package managed

import (
	"context"
	"database/sql"
	"encoding/json"
)

// VMResourceSnapshot is a consistent read of the registration and its last
// power observation. External observations must carry these revisions back.
type VMResourceSnapshot struct {
	Spec                ResourceSpec
	Status              ResourceStatus
	ObservationRevision int64
	Power               string
}

func (l *Ledger) VMResources() ([]VMResourceSnapshot, error) {
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT r.spec_json,COALESCE(o.observation_revision,0),COALESCE(o.power,'uncertain') FROM managed_resources r LEFT JOIN managed_vm_observations o ON o.resource_id=r.id WHERE r.kind='utm-macos' ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	var result []VMResourceSnapshot
	for rows.Next() {
		var v VMResourceSnapshot
		var raw []byte
		if err := rows.Scan(&raw, &v.ObservationRevision, &v.Power); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(raw, &v.Spec); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range result {
		result[i].Status, err = resourceStatusTx(tx, result[i].Spec.ID)
		if err != nil {
			return nil, err
		}
	}
	return result, tx.Commit()
}
