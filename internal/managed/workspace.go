package managed

import (
	"context"
	"database/sql"
	"errors"
)

type LocalWorkspaceBinding struct {
	Schema   string        `json:"schema"`
	Identity LocalIdentity `json:"identity"`
	Root     RetainedPath  `json:"root"`
}

// The prepared directory incarnation is journaled before workload delivery.
// Workload renames cannot turn existing owned evidence into a positive absence.
func (l *Ledger) BindLocalWorkspace(id LocalIdentity, root RetainedPath) error {
	if id.RunID != "" || root.Path != "workspace" || !root.Directory || root.Inode == 0 {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = matchLocalIdentityTx(tx, id); err != nil {
		return err
	}
	var kind string
	if err = tx.QueryRow(`SELECT kind FROM managed_resources WHERE id=?`, id.ResourceID).Scan(&kind); err != nil {
		return err
	}
	if kind != "local" {
		return ErrInvalid
	}
	binding := LocalWorkspaceBinding{"haepari.local-workspace/v1", id, root}
	var old []byte
	if err = tx.QueryRow(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='local_workspace'`, id.JobID).Scan(&old); err == nil {
		var saved LocalWorkspaceBinding
		if decodeClosed(old, &saved) != nil || saved != binding {
			return ErrConflict
		}
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	if _, err = addEvent(tx, id.JobID, "local_workspace", binding); err != nil {
		return err
	}
	return l.commit(tx)
}
func (l *Ledger) LocalWorkspace(id LocalIdentity) (RetainedPath, error) {
	var raw []byte
	if err := l.db.QueryRow(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='local_workspace'`, id.JobID).Scan(&raw); err != nil {
		return RetainedPath{}, mapNoRows(err)
	}
	var b LocalWorkspaceBinding
	if decodeClosed(raw, &b) != nil {
		return RetainedPath{}, ErrBlocked
	}
	original := id
	original.RunID = ""
	if b.Schema != "haepari.local-workspace/v1" || b.Identity != original || b.Root.Path != "workspace" || !b.Root.Directory || b.Root.Inode == 0 {
		return RetainedPath{}, ErrStale
	}
	return b.Root, nil
}
