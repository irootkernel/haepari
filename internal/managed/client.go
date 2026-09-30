package managed

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

func checkClientSchemaTx(tx *sql.Tx) error {
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='managed_installation'`).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("managed schema6 lost installation identity table")
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM managed_installation WHERE singleton=1`).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("managed schema6 lost installation identity row")
	}
	return checkCancellationSchemaTx(tx)
}
func initClientSchemaTx(tx *sql.Tx, version int) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS managed_installation(singleton INTEGER PRIMARY KEY CHECK(singleton=1), id TEXT NOT NULL)`); err != nil {
		return err
	}
	if version < 6 {
		if _, err := tx.Exec(`INSERT INTO managed_installation(singleton,id) VALUES(1,'')`); err != nil {
			return err
		}
	}
	if err := initCancellationTx(tx, version); err != nil {
		return err
	}
	if version < 6 {
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version,identity) VALUES(6,'durable-client-v6')`); err != nil {
			return err
		}
		_, err := tx.Exec(`PRAGMA user_version=6`)
		return err
	}
	return nil
}

// BindInstallation preserves the preexisting root-derived ID on first binding.
// Subsequent moves of this database retain that exact identity and parent scope.
func (l *Ledger) BindInstallation(seed string) (string, error) {
	if !identifier.MatchString(seed) {
		return "", ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRow(`SELECT id FROM managed_installation WHERE singleton=1`).Scan(&id)
	if err != nil {
		return "", err
	}
	if id == "" {
		if _, err = tx.Exec(`UPDATE managed_installation SET id=? WHERE singleton=1 AND id=''`, seed); err != nil {
			return "", err
		}
		id = seed
	}
	if !identifier.MatchString(id) {
		return "", fmt.Errorf("invalid stored installation identity")
	}
	if err = l.commit(tx); err != nil {
		return "", err
	}
	return id, nil
}

type JobsQuery struct {
	Resource string `json:"resource,omitempty"`
	State    string `json:"state,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	Limit    int    `json:"limit"`
}
type JobEntry struct {
	Receipt       Receipt `json:"receipt"`
	QueuePosition int64   `json:"queue_position,omitempty"`
	Result        *Result `json:"result,omitempty"`
}
type JobsPage struct {
	Jobs       []JobEntry `json:"jobs"`
	NextCursor string     `json:"next_cursor,omitempty"`
	More       bool       `json:"more"`
}
type ResourcesPage struct {
	Resources  []ResourceStatus `json:"resources"`
	NextCursor string           `json:"next_cursor,omitempty"`
	More       bool             `json:"more"`
}
type pageCursor struct {
	Kind         string `json:"kind"`
	Installation string `json:"installation"`
	Resource     string `json:"resource,omitempty"`
	State        string `json:"state,omitempty"`
	Last         int64  `json:"last,omitempty"`
	Upper        int64  `json:"upper,omitempty"`
	LastID       string `json:"last_id,omitempty"`
	UpperID      string `json:"upper_id,omitempty"`
}

func encodePageCursor(c pageCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodePageCursor(raw string) (pageCursor, error) {
	var c pageCursor
	if len(raw) > 2048 {
		return c, ErrInvalid
	}
	b, e := base64.RawURLEncoding.DecodeString(raw)
	if e != nil {
		return c, ErrInvalid
	}
	if e = decodeClosed(b, &c); e != nil {
		return c, e
	}
	return c, nil
}
func installationTx(tx *sql.Tx) (string, error) {
	var id string
	e := tx.QueryRow(`SELECT id FROM managed_installation WHERE singleton=1`).Scan(&id)
	return id, e
}

func (l *Ledger) JobsPage(q JobsQuery) (JobsPage, error) {
	page := JobsPage{Jobs: []JobEntry{}}
	if q.Limit < 1 || q.Limit > 100 || q.Resource != "" && !identifier.MatchString(q.Resource) || q.State != "" && !oneOf(q.State, "queued", "preparing", "ready", "running", "awaiting_manual", "cancelling", "collecting", "cleaning", "settling", "reconciling", "finished") {
		return page, ErrInvalid
	}
	tx, e := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return page, e
	}
	defer tx.Rollback()
	installation, e := installationTx(tx)
	if e != nil {
		return page, e
	}
	c := pageCursor{Kind: "jobs", Installation: installation, Resource: q.Resource, State: q.State}
	if q.Cursor != "" {
		c, e = decodePageCursor(q.Cursor)
		if e != nil {
			return page, e
		}
		if c.Kind != "jobs" || c.Installation != installation || c.Resource != q.Resource || c.State != q.State || c.Last < 0 || c.Upper < c.Last || c.LastID != "" || c.UpperID != "" {
			return page, ErrInvalid
		}
	} else if e = tx.QueryRow(`SELECT COALESCE(MAX(sequence),0) FROM managed_admissions`).Scan(&c.Upper); e != nil {
		return page, e
	}
	rows, e := tx.Query(`SELECT a.sequence,a.job_id FROM managed_admissions a LEFT JOIN managed_jobs j ON j.id=a.job_id WHERE a.sequence>? AND a.sequence<=? AND (?='' OR json_extract(a.spec_json,'$.resource_id')=?) AND (?='' OR COALESCE(j.state,'finished')=?) ORDER BY a.sequence LIMIT ?`, c.Last, c.Upper, q.Resource, q.Resource, q.State, q.State, q.Limit+1)
	if e != nil {
		return page, e
	}
	type entry struct {
		sequence int64
		id       string
	}
	ids := []entry{}
	for rows.Next() {
		var x entry
		if e = rows.Scan(&x.sequence, &x.id); e != nil {
			rows.Close()
			return page, e
		}
		ids = append(ids, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return page, e
	}
	size := 0
	for i, x := range ids {
		if i == q.Limit {
			page.More = true
			break
		}
		receipt, e := l.snapshotTx(tx, x.id)
		if e != nil {
			return page, e
		}
		item := JobEntry{Receipt: receipt}
		if receipt.JobState == "queued" {
			if e = tx.QueryRow(`SELECT COUNT(*)+1 FROM managed_jobs j JOIN managed_admissions a ON a.job_id=j.id WHERE j.resource_id=? AND j.state='queued' AND a.sequence<?`, receipt.ResourceID, x.sequence).Scan(&item.QueuePosition); e != nil {
				return page, e
			}
		}
		if receipt.JobState == "finished" && receipt.ResultStatus != "unavailable" {
			var raw []byte
			if e = tx.QueryRow(`SELECT result_json FROM managed_jobs WHERE id=?`, x.id).Scan(&raw); e != nil {
				return page, e
			}
			result, e := DecodeResult(raw)
			if e != nil {
				return page, e
			}
			item.Result = &result
		}
		raw, e := json.Marshal(item)
		if e != nil {
			return page, e
		}
		if len(raw) > 3<<20 {
			return page, fmt.Errorf("job exceeds bounded page")
		}
		if size+len(raw) > 3<<20 {
			page.More = true
			break
		}
		size += len(raw)
		page.Jobs = append(page.Jobs, item)
		c.Last = x.sequence
	}
	if page.More {
		page.NextCursor = encodePageCursor(c)
	}
	return page, nil
}

func (l *Ledger) ResourcesPage(cursor string, limit int) (ResourcesPage, error) {
	page := ResourcesPage{Resources: []ResourceStatus{}}
	if limit < 1 || limit > 100 {
		return page, ErrInvalid
	}
	tx, e := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return page, e
	}
	defer tx.Rollback()
	installation, e := installationTx(tx)
	if e != nil {
		return page, e
	}
	c := pageCursor{Kind: "resources", Installation: installation}
	if cursor != "" {
		c, e = decodePageCursor(cursor)
		if e != nil {
			return page, e
		}
		if c.Kind != "resources" || c.Installation != installation || c.Resource != "" || c.State != "" || c.Last != 0 || c.Upper != 0 || c.LastID == "" || c.UpperID < c.LastID {
			return page, ErrInvalid
		}
	} else if e = tx.QueryRow(`SELECT COALESCE(MAX(id),'') FROM managed_resources`).Scan(&c.UpperID); e != nil {
		return page, e
	}
	rows, e := tx.Query(`SELECT id FROM managed_resources WHERE id>? AND id<=? ORDER BY id LIMIT ?`, c.LastID, c.UpperID, limit+1)
	if e != nil {
		return page, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return page, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return page, e
	}
	for i, id := range ids {
		if i == limit {
			page.More = true
			break
		}
		r, e := resourceStatusTx(tx, id)
		if e != nil {
			return page, e
		}
		page.Resources = append(page.Resources, r)
		c.LastID = id
	}
	if page.More {
		page.NextCursor = encodePageCursor(c)
	}
	return page, nil
}
