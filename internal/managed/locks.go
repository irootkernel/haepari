package managed

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

var ErrLockExpired = errors.New("LOCK_EXPIRED")

const LockGrantEnvironment = "HAEPARI_LOCK_GRANT"
const LockGrantHeader = "X-Haepari-Lock-Grant"

type LockAcquireRequest struct {
	LockID       string `json:"lock_id"`
	RequestKey   string `json:"request_key"`
	Holder       string `json:"holder"`
	QueueSeconds *int64 `json:"queue_seconds,omitempty"`
}
type LockRequest struct {
	InstallationID   string     `json:"installation_id"`
	LockID           string     `json:"lock_id"`
	RequestID        string     `json:"request_id"`
	RequestKey       string     `json:"request_key"`
	Holder           string     `json:"holder"`
	State            string     `json:"state"`
	Sequence         int64      `json:"sequence"`
	GrantID          string     `json:"grant_id,omitempty"`
	Generation       int64      `json:"generation,omitempty"`
	Slot             int        `json:"slot"`
	QueuedAt         time.Time  `json:"queued_at"`
	QueueDeadline    *time.Time `json:"queue_deadline,omitempty"`
	GrantedAt        *time.Time `json:"granted_at,omitempty"`
	LivenessDeadline *time.Time `json:"liveness_deadline,omitempty"`
	MaxHoldDeadline  *time.Time `json:"max_hold_deadline,omitempty"`
	LivenessSeconds  int64      `json:"liveness_seconds,omitempty"`
	MaxHoldSeconds   int64      `json:"max_hold_seconds,omitempty"`
	EndedAt          *time.Time `json:"ended_at,omitempty"`
	Reason           string     `json:"reason,omitempty"`
	Forced           bool       `json:"forced,omitempty"`
}
type LockReceipt struct {
	Operation  string    `json:"operation"`
	RequestID  string    `json:"request_id"`
	GrantID    string    `json:"grant_id,omitempty"`
	State      string    `json:"state"`
	RecordedAt time.Time `json:"recorded_at"`
	Force      bool      `json:"force,omitempty"`
	Reason     string    `json:"reason,omitempty"`
}
type LockReply struct {
	Receipt LockReceipt `json:"receipt"`
	Current LockRequest `json:"current"`
}
type LockList struct {
	InstallationID string        `json:"installation_id"`
	Now            time.Time     `json:"now"`
	Requests       []LockRequest `json:"requests"`
	More           bool          `json:"more,omitempty"`
	NextCursor     string        `json:"next_cursor,omitempty"`
}
type LockGrantReference struct {
	InstallationID string `json:"installation_id"`
	LockID         string `json:"lock_id"`
	GrantID        string `json:"grant_id"`
	Generation     int64  `json:"generation"`
}

func (r LockGrantReference) valid() bool {
	return identifier.MatchString(r.InstallationID) && identifier.MatchString(r.LockID) && identifier.MatchString(r.GrantID) && r.Generation > 0
}
func DecodeLockGrant(raw string) (LockGrantReference, error) {
	var r LockGrantReference
	if decodeClosedWithin([]byte(raw), &r, 8192) != nil || !r.valid() {
		return r, ErrInvalid
	}
	return r, nil
}
func validLockSeconds(n int64) bool { return n > 0 && n <= math.MaxInt64/int64(time.Second) }
func lockDeadline(now time.Time, seconds int64) (time.Time, error) {
	if !validLockSeconds(seconds) {
		return time.Time{}, ErrInvalid
	}
	// Deadlines are JSON time.Time values, not the clock's signed-nanosecond column.
	deadline := now.Add(time.Duration(seconds) * time.Second)
	if _, err := deadline.MarshalJSON(); err != nil {
		return time.Time{}, ErrInvalid
	}
	return deadline, nil
}
func lockID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Lock history intentionally has no resource foreign keys. Removal never frees
// a key, FIFO sequence or slot generation for reuse.
func initLockSchemaTx(tx *sql.Tx, version int) error {
	if version >= 7 {
		return nil
	}
	for _, s := range []string{
		`CREATE TABLE managed_lock_requests(sequence INTEGER PRIMARY KEY AUTOINCREMENT, request_id TEXT NOT NULL UNIQUE,lock_id TEXT NOT NULL,request_key TEXT NOT NULL,digest TEXT NOT NULL,state TEXT NOT NULL,grant_id TEXT UNIQUE,slot INTEGER NOT NULL DEFAULT -1,request_json BLOB NOT NULL,acquire_receipt BLOB,end_receipt BLOB,UNIQUE(lock_id,request_key))`,
		`CREATE UNIQUE INDEX managed_lock_active_slot ON managed_lock_requests(lock_id,slot) WHERE state='granted'`,
		`CREATE INDEX managed_lock_fifo ON managed_lock_requests(lock_id,state,sequence)`,
		`CREATE TABLE managed_lock_slots(lock_id TEXT NOT NULL,slot INTEGER NOT NULL,generation INTEGER NOT NULL,PRIMARY KEY(lock_id,slot))`,
		`CREATE TABLE managed_lock_clock(singleton INTEGER PRIMARY KEY CHECK(singleton=1),highwater_ns INTEGER NOT NULL,wake_marker TEXT NOT NULL)`,
		`INSERT INTO managed_lock_clock VALUES(1,0,'')`,
		`CREATE TABLE managed_lock_discontinuities(sequence INTEGER PRIMARY KEY AUTOINCREMENT,recorded_at_ns INTEGER NOT NULL,kind TEXT NOT NULL,wake_marker TEXT NOT NULL)`,
		`INSERT INTO schema_migrations(version,identity) VALUES(7,'advisory-lock-v7')`,
		`PRAGMA user_version=7`,
	} {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return nil
}
func checkLockSchemaTx(tx *sql.Tx) error {
	for _, table := range []string{"managed_lock_requests", "managed_lock_slots", "managed_lock_clock", "managed_lock_discontinuities"} {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("managed schema7 lost required %s", table)
		}
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM managed_lock_clock WHERE singleton=1`).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("managed schema7 lost clock row")
	}
	return nil
}

// SetLockClock is startup configuration; it must precede concurrent serving.
func (l *Ledger) SetLockClock(read func() (time.Time, string, error)) { l.lockClock = read }
func (l *Ledger) lockNowTx(tx *sql.Tx) (time.Time, string, string, error) {
	now, marker := time.Now().UTC(), ""
	if l.lockClock != nil {
		var err error
		now, marker, err = l.lockClock()
		if err != nil {
			return time.Time{}, "", "", err
		}
	}
	now = now.UTC().Round(0)
	if now.UnixNano() <= 0 || !time.Unix(0, now.UnixNano()).UTC().Equal(now) || len(marker) > 1024 {
		return time.Time{}, "", "", ErrInvalid
	}
	var high int64
	var prior string
	if err := tx.QueryRow(`SELECT highwater_ns,wake_marker FROM managed_lock_clock WHERE singleton=1`).Scan(&high, &prior); err != nil {
		return time.Time{}, "", "", err
	}
	if high > now.UnixNano() {
		now = time.Unix(0, high).UTC()
	}
	return now, marker, prior, nil
}
func loadLockTx(tx *sql.Tx, column, value string) (LockRequest, error) {
	var raw []byte
	// column is one of the internal constant selectors, never caller input.
	if err := tx.QueryRow(`SELECT request_json FROM managed_lock_requests WHERE `+column+`=?`, value).Scan(&raw); err != nil {
		return LockRequest{}, mapNoRows(err)
	}
	var r LockRequest
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	return r, nil
}
func saveLockTx(tx *sql.Tx, r LockRequest) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var grant any
	if r.GrantID != "" {
		grant = r.GrantID
	}
	_, err = tx.Exec(`UPDATE managed_lock_requests SET state=?,grant_id=?,slot=?,request_json=? WHERE request_id=?`, r.State, grant, r.Slot, raw, r.RequestID)
	return err
}
func lockReceipt(op string, r LockRequest, now time.Time, force bool, reason string) LockReceipt {
	return LockReceipt{Operation: op, RequestID: r.RequestID, GrantID: r.GrantID, State: r.State, RecordedAt: now, Force: force, Reason: reason}
}
func endLockTx(tx *sql.Tx, r LockRequest, now time.Time, state, op string, force bool, reason string) error {
	r.State, r.EndedAt, r.Reason, r.Forced = state, &now, reason, force
	if err := saveLockTx(tx, r); err != nil {
		return err
	}
	raw, err := json.Marshal(lockReceipt(op, r, now, force, reason))
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE managed_lock_requests SET end_receipt=? WHERE request_id=? AND end_receipt IS NULL`, raw, r.RequestID)
	return err
}
func lockRowsTx(tx *sql.Tx, query string, args ...any) ([]LockRequest, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LockRequest{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var r LockRequest
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func renewLockTx(tx *sql.Tx, r LockRequest, now time.Time) (LockRequest, error) {
	deadline, err := lockDeadline(now, r.LivenessSeconds)
	if err != nil {
		return r, err
	}
	if r.MaxHoldDeadline == nil {
		return r, fmt.Errorf("missing lock maximum hold")
	}
	if deadline.After(*r.MaxHoldDeadline) {
		deadline = *r.MaxHoldDeadline
	}
	r.LivenessDeadline = &deadline
	return r, saveLockTx(tx, r)
}

// clockAndDueTx freezes effective time, handles an observed discontinuity, then
// expires requests and grants FIFO successors within the same transaction.
func (l *Ledger) clockAndDueTx(tx *sql.Tx, startup bool) (time.Time, bool, error) {
	now, marker, prior, err := l.lockNowTx(tx)
	if err != nil {
		return now, false, err
	}
	changed := false
	if startup || marker != prior {
		live, err := lockRowsTx(tx, `SELECT request_json FROM managed_lock_requests WHERE state='granted' ORDER BY sequence`)
		if err != nil {
			return now, false, err
		}
		for _, r := range live {
			if r.MaxHoldDeadline == nil || r.LivenessDeadline == nil {
				return now, false, fmt.Errorf("missing lock deadline")
			}
			if !now.Before(*r.MaxHoldDeadline) {
				continue
			}
			deadline, err := lockDeadline(now, r.LivenessSeconds)
			if err != nil {
				return now, false, err
			}
			if deadline.After(*r.MaxHoldDeadline) {
				deadline = *r.MaxHoldDeadline
			}
			if deadline.After(*r.LivenessDeadline) {
				r.LivenessDeadline = &deadline
				if err := saveLockTx(tx, r); err != nil {
					return now, false, err
				}
				changed = true
			}
		}
		kind := "wake"
		if startup {
			kind = "startup"
		}
		if _, err := tx.Exec(`INSERT INTO managed_lock_discontinuities(recorded_at_ns,kind,wake_marker) VALUES(?,?,?)`, now.UnixNano(), kind, marker); err != nil {
			return now, false, err
		}
		changed = true
	}
	if _, err := tx.Exec(`UPDATE managed_lock_clock SET highwater_ns=?,wake_marker=? WHERE singleton=1 AND (highwater_ns!=? OR wake_marker!=?)`, now.UnixNano(), marker, now.UnixNano(), marker); err != nil {
		return now, false, err
	}
	due, err := processLocksTx(tx, now)
	return now, changed || due, err
}
func processLocksTx(tx *sql.Tx, now time.Time) (bool, error) {
	changed := false
	active, err := lockRowsTx(tx, `SELECT request_json FROM managed_lock_requests WHERE state IN ('queued','granted') ORDER BY sequence`)
	if err != nil {
		return false, err
	}
	for _, r := range active {
		state, reason := "", ""
		if r.State == "queued" && r.QueueDeadline != nil && !now.Before(*r.QueueDeadline) {
			state, reason = "queue_expired", "queue_deadline"
		}
		if r.State == "granted" {
			if r.MaxHoldDeadline == nil || r.LivenessDeadline == nil {
				return false, fmt.Errorf("missing lock deadline")
			}
			if !now.Before(*r.MaxHoldDeadline) {
				state, reason = "hold_expired", "max_hold_deadline"
			} else if !now.Before(*r.LivenessDeadline) {
				state, reason = "hold_expired", "liveness_deadline"
			}
		}
		if state != "" {
			if err := endLockTx(tx, r, now, state, "expire", false, reason); err != nil {
				return false, err
			}
			changed = true
		}
	}
	rows, err := tx.Query(`SELECT spec_json FROM managed_resources WHERE kind='lock' AND availability='available' ORDER BY id`)
	if err != nil {
		return false, err
	}
	specs := []ResourceSpec{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return false, err
		}
		r, err := DecodeResource(raw)
		if err != nil {
			rows.Close()
			return false, err
		}
		specs = append(specs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, spec := range specs {
		var held int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM managed_lock_requests WHERE lock_id=? AND state='granted'`, spec.ID).Scan(&held); err != nil {
			return false, err
		}
		for held < spec.Capacity {
			var id string
			err := tx.QueryRow(`SELECT request_id FROM managed_lock_requests WHERE lock_id=? AND state='queued' ORDER BY sequence LIMIT 1`, spec.ID).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
			if err != nil {
				return false, err
			}
			r, err := loadLockTx(tx, "request_id", id)
			if err != nil {
				return false, err
			}
			slot := 0
			for ; slot < spec.Capacity; slot++ {
				var count int
				if err := tx.QueryRow(`SELECT COUNT(*) FROM managed_lock_requests WHERE lock_id=? AND state='granted' AND slot=?`, spec.ID, slot).Scan(&count); err != nil {
					return false, err
				}
				if count == 0 {
					break
				}
			}
			if slot == spec.Capacity {
				break
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO managed_lock_slots(lock_id,slot,generation) VALUES(?,?,0)`, spec.ID, slot); err != nil {
				return false, err
			}
			var generation int64
			if err := tx.QueryRow(`SELECT generation FROM managed_lock_slots WHERE lock_id=? AND slot=?`, spec.ID, slot).Scan(&generation); err != nil {
				return false, err
			}
			if generation == math.MaxInt64 {
				return false, ErrBlocked
			}
			generation++
			if _, err := tx.Exec(`UPDATE managed_lock_slots SET generation=? WHERE lock_id=? AND slot=?`, generation, spec.ID, slot); err != nil {
				return false, err
			}
			grant, err := lockID()
			if err != nil {
				return false, err
			}
			max, err := lockDeadline(now, spec.MaxHoldSeconds)
			if err != nil {
				return false, err
			}
			r.State, r.GrantID, r.Slot, r.Generation, r.GrantedAt, r.MaxHoldDeadline = "granted", grant, slot, generation, &now, &max
			r.LivenessSeconds, r.MaxHoldSeconds = spec.LivenessSeconds, spec.MaxHoldSeconds
			if _, err := renewLockTx(tx, r, now); err != nil {
				return false, err
			}
			changed = true
			held++
		}
	}
	return changed, nil
}
func (l *Ledger) commitLocks(tx *sql.Tx, changed bool) error {
	if changed {
		return l.commit(tx)
	}
	return tx.Commit()
}
func (l *Ledger) LockStartup() error { return l.sweepLocks(true) }
func (l *Ledger) SweepLocks() error  { return l.sweepLocks(false) }
func (l *Ledger) sweepLocks(startup bool) error {
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, changed, err := l.clockAndDueTx(tx, startup)
	if err != nil {
		return err
	}
	return l.commitLocks(tx, changed)
}
func (l *Ledger) NearestLockDeadline() (time.Time, bool, error) {
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return time.Time{}, false, err
	}
	defer tx.Rollback()
	if _, _, _, err := l.lockNowTx(tx); err != nil {
		return time.Time{}, false, err
	}
	active, err := lockRowsTx(tx, `SELECT request_json FROM managed_lock_requests WHERE state IN ('queued','granted')`)
	if err != nil {
		return time.Time{}, false, err
	}
	var nearest time.Time
	for _, r := range active {
		deadlines := []*time.Time{r.QueueDeadline}
		if r.State == "granted" {
			deadlines = []*time.Time{r.LivenessDeadline, r.MaxHoldDeadline}
		}
		for _, d := range deadlines {
			if d != nil && (nearest.IsZero() || d.Before(nearest)) {
				nearest = *d
			}
		}
	}
	return nearest, !nearest.IsZero(), nil
}
func storedLockReceiptTx(tx *sql.Tx, r LockRequest, acquire bool) (LockReceipt, error) {
	column := "end_receipt"
	if acquire {
		column = "acquire_receipt"
	}
	var raw []byte
	if err := tx.QueryRow(`SELECT `+column+` FROM managed_lock_requests WHERE request_id=?`, r.RequestID).Scan(&raw); err != nil {
		return LockReceipt{}, err
	}
	var receipt LockReceipt
	err := json.Unmarshal(raw, &receipt)
	return receipt, err
}
func (l *Ledger) AcquireLock(q LockAcquireRequest, installationID string, chain ParentChain) (LockReply, error) {
	if !identifier.MatchString(q.LockID) || !ValidRequestKey(q.RequestKey) || strings.TrimSpace(q.Holder) == "" || len(q.Holder) > 512 || (q.QueueSeconds != nil && !validLockSeconds(*q.QueueSeconds)) || !identifier.MatchString(installationID) {
		return LockReply{}, ErrInvalid
	}
	if err := chain.Validate(); err != nil {
		return LockReply{}, err
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return LockReply{}, err
	}
	defer tx.Rollback()
	selected, err := installationTx(tx)
	if err != nil {
		return LockReply{}, err
	}
	if selected != installationID {
		return LockReply{}, ErrConflict
	}
	if nested, err := nestedTx(tx, selected, chain); err != nil {
		return LockReply{}, err
	} else if nested {
		return LockReply{}, ErrNestedAcquisition
	}
	now, changed, err := l.clockAndDueTx(tx, false)
	if err != nil {
		return LockReply{}, err
	}
	raw, _ := json.Marshal(q)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	var id, prior string
	err = tx.QueryRow(`SELECT request_id,digest FROM managed_lock_requests WHERE lock_id=? AND request_key=?`, q.LockID, q.RequestKey).Scan(&id, &prior)
	if err == nil {
		if prior != digest {
			return LockReply{}, ErrConflict
		}
		r, err := loadLockTx(tx, "request_id", id)
		if err != nil {
			return LockReply{}, err
		}
		receipt, err := storedLockReceiptTx(tx, r, true)
		if err != nil {
			return LockReply{}, err
		}
		if r.State == "granted" {
			r, err = renewLockTx(tx, r, now)
			if err != nil {
				return LockReply{}, err
			}
			changed = true
		}
		return LockReply{receipt, r}, l.commitLocks(tx, changed)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return LockReply{}, err
	}
	var kind string
	if err := tx.QueryRow(`SELECT kind FROM managed_resources WHERE id=?`, q.LockID).Scan(&kind); err != nil {
		return LockReply{}, mapNoRows(err)
	}
	if kind != "lock" {
		return LockReply{}, ErrUnsupported
	}
	id, err = lockID()
	if err != nil {
		return LockReply{}, err
	}
	r := LockRequest{InstallationID: selected, LockID: q.LockID, RequestID: id, RequestKey: q.RequestKey, Holder: q.Holder, State: "queued", Slot: -1, QueuedAt: now}
	if q.QueueSeconds != nil {
		d, err := lockDeadline(now, *q.QueueSeconds)
		if err != nil {
			return LockReply{}, err
		}
		r.QueueDeadline = &d
	}
	raw, err = json.Marshal(r)
	if err != nil {
		return LockReply{}, err
	}
	result, err := tx.Exec(`INSERT INTO managed_lock_requests(request_id,lock_id,request_key,digest,state,request_json) VALUES(?,?,?,?,'queued',?)`, id, q.LockID, q.RequestKey, digest, raw)
	if err != nil {
		return LockReply{}, err
	}
	r.Sequence, err = result.LastInsertId()
	if err != nil {
		return LockReply{}, err
	}
	if err := saveLockTx(tx, r); err != nil {
		return LockReply{}, err
	}
	if _, err := processLocksTx(tx, now); err != nil {
		return LockReply{}, err
	}
	r, err = loadLockTx(tx, "request_id", id)
	if err != nil {
		return LockReply{}, err
	}
	receipt := lockReceipt("acquire", r, now, false, "")
	raw, _ = json.Marshal(receipt)
	if _, err := tx.Exec(`UPDATE managed_lock_requests SET acquire_receipt=? WHERE request_id=?`, raw, id); err != nil {
		return LockReply{}, err
	}
	return LockReply{receipt, r}, l.commit(tx)
}
func liveLock(r LockRequest, now time.Time) bool {
	return r.State == "granted" && r.LivenessDeadline != nil && r.MaxHoldDeadline != nil && now.Before(*r.LivenessDeadline) && now.Before(*r.MaxHoldDeadline)
}
func checkLockGrantTx(tx *sql.Tx, reference LockGrantReference, intendedLock string, now time.Time) (LockRequest, error) {
	if !reference.valid() || !identifier.MatchString(intendedLock) {
		return LockRequest{}, ErrInvalid
	}
	selected, err := installationTx(tx)
	if err != nil {
		return LockRequest{}, err
	}
	if reference.InstallationID != selected || reference.LockID != intendedLock {
		return LockRequest{}, ErrConflict
	}
	r, err := loadLockTx(tx, "grant_id", reference.GrantID)
	if err != nil {
		return r, err
	}
	if r.InstallationID != selected || r.LockID != intendedLock {
		return r, ErrConflict
	}
	if r.Generation != reference.Generation || !liveLock(r, now) {
		return r, ErrLockExpired
	}
	return r, nil
}
func (l *Ledger) CheckLockGrant(reference LockGrantReference, intendedLock string) (LockRequest, error) {
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LockRequest{}, err
	}
	defer tx.Rollback()
	now, _, _, err := l.lockNowTx(tx)
	if err != nil {
		return LockRequest{}, err
	}
	return checkLockGrantTx(tx, reference, intendedLock, now)
}
func (l *Ledger) ClaimLock(requestID string) (LockReply, error) {
	return l.lockAction("claim", requestID, false, "", nil)
}
func (l *Ledger) RenewLock(grantID string, borrowed *LockGrantReference) (LockReply, error) {
	return l.lockAction("renew", grantID, false, "", borrowed)
}
func (l *Ledger) ReleaseLock(grantID string, force bool, reason string, borrowed *LockGrantReference) (LockReply, error) {
	return l.lockAction("release", grantID, force, reason, borrowed)
}
func (l *Ledger) CancelLock(requestID string, force bool, reason string, borrowed *LockGrantReference) (LockReply, error) {
	return l.lockAction("cancel", requestID, force, reason, borrowed)
}
func (l *Ledger) lockAction(operation, id string, force bool, reason string, borrowed *LockGrantReference) (LockReply, error) {
	if !identifier.MatchString(id) || len(reason) > 512 || (force && strings.TrimSpace(reason) == "") || (!force && reason != "") {
		return LockReply{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return LockReply{}, err
	}
	defer tx.Rollback()
	column := "grant_id"
	if operation == "claim" || operation == "cancel" {
		column = "request_id"
	}
	target, err := loadLockTx(tx, column, id)
	if err != nil {
		return LockReply{}, err
	}
	if borrowed != nil {
		now, _, _, err := l.lockNowTx(tx)
		if err != nil {
			return LockReply{}, err
		}
		parent, err := checkLockGrantTx(tx, *borrowed, target.LockID, now)
		if err != nil {
			return LockReply{}, err
		}
		if parent.RequestID == target.RequestID {
			return LockReply{}, ErrConflict
		}
	}
	now, changed, err := l.clockAndDueTx(tx, false)
	if err != nil {
		return LockReply{}, err
	}
	r, err := loadLockTx(tx, column, id)
	if err != nil {
		return LockReply{}, err
	}
	if r.State != "queued" && r.State != "granted" {
		receipt, err := storedLockReceiptTx(tx, r, false)
		if err != nil {
			return LockReply{}, err
		}
		if err := l.commitLocks(tx, changed); err != nil {
			return LockReply{}, err
		}
		if operation == "renew" {
			return LockReply{receipt, r}, ErrLockExpired
		}
		return LockReply{receipt, r}, nil
	}
	if operation == "claim" || operation == "renew" {
		if r.State == "granted" {
			r, err = renewLockTx(tx, r, now)
			if err != nil {
				return LockReply{}, err
			}
			changed = true
		}
		return LockReply{lockReceipt(operation, r, now, false, ""), r}, l.commitLocks(tx, changed)
	}
	state := "released"
	if r.State == "queued" {
		state = "cancelled"
	}
	if err := endLockTx(tx, r, now, state, operation, force, reason); err != nil {
		return LockReply{}, err
	}
	if _, err := processLocksTx(tx, now); err != nil {
		return LockReply{}, err
	}
	r, err = loadLockTx(tx, column, id)
	if err != nil {
		return LockReply{}, err
	}
	receipt, err := storedLockReceiptTx(tx, r, false)
	if err != nil {
		return LockReply{}, err
	}
	return LockReply{receipt, r}, l.commit(tx)
}
func (l *Ledger) LocksList(lockID string) (LockList, error) { return l.locksPage(lockID, "", 0) }
func (l *Ledger) LocksPage(lockID, cursor string, limit int) (LockList, error) {
	if limit < 1 || limit > 100 {
		return LockList{}, ErrInvalid
	}
	return l.locksPage(lockID, cursor, limit)
}
func (l *Ledger) locksPage(lockID, cursor string, limit int) (LockList, error) {
	page := LockList{Requests: []LockRequest{}}
	if lockID != "" && !identifier.MatchString(lockID) {
		return page, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	page.InstallationID, err = installationTx(tx)
	if err != nil {
		return page, err
	}
	page.Now, _, _, err = l.lockNowTx(tx)
	if err != nil {
		return page, err
	}
	c := pageCursor{Kind: "locks", Installation: page.InstallationID, Resource: lockID}
	if cursor != "" {
		c, err = decodePageCursor(cursor)
		if err != nil {
			return page, err
		}
		if c.Kind != "locks" || c.Installation != page.InstallationID || c.Resource != lockID || c.State != "" || c.LastID != "" || c.UpperID != "" || c.Last <= 0 || c.Upper < c.Last {
			return page, ErrInvalid
		}
	} else if err = tx.QueryRow(`SELECT COALESCE(MAX(sequence),0) FROM managed_lock_requests`).Scan(&c.Upper); err != nil {
		return page, err
	}
	query := `SELECT request_json FROM managed_lock_requests WHERE sequence>? AND sequence<=? AND (?='' OR lock_id=?) ORDER BY sequence`
	args := []any{c.Last, c.Upper, lockID, lockID}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit+1)
	}
	requests, err := lockRowsTx(tx, query, args...)
	if err != nil {
		return page, err
	}
	for i, r := range requests {
		if limit > 0 && i == limit {
			page.More = true
			break
		}
		page.Requests = append(page.Requests, r)
		c.Last = r.Sequence
	}
	if page.More {
		page.NextCursor = encodePageCursor(c)
	}
	return page, nil
}
