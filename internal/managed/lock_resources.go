package managed

import (
	"context"
	"database/sql"
	"encoding/json"
)

func (l *Ledger) registerLockResource(r ResourceSpec) error {
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now, _, err := l.clockAndDueTx(tx, false)
	if err != nil {
		return err
	}
	for _, seconds := range []int64{r.LivenessSeconds, r.MaxHoldSeconds} {
		if _, err := lockDeadline(now, seconds); err != nil {
			return err
		}
	}
	canonical, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO managed_resources(id,kind,config_revision,capacity,spec_json) VALUES(?,'lock',?,?,?)`, r.ID, r.Revision, r.Capacity, canonical); err != nil {
		return admissionError(err)
	}
	if _, err = processLocksTx(tx, now); err != nil {
		return err
	}
	return l.commit(tx)
}
func (l *Ledger) updateLockResource(r ResourceSpec, configRevision, availabilityRevision int64) error {
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := resourceStatusTx(tx, r.ID)
	if err != nil {
		return err
	}
	if current.Kind != "lock" {
		return ErrConflict
	}
	if current.ConfigRevision != configRevision || current.AvailabilityRevision != availabilityRevision {
		return ErrStale
	}
	now, _, err := l.clockAndDueTx(tx, false)
	if err != nil {
		return err
	}
	for _, seconds := range []int64{r.LivenessSeconds, r.MaxHoldSeconds} {
		if _, err := lockDeadline(now, seconds); err != nil {
			return err
		}
	}
	canonical, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE managed_resources SET config_revision=?,capacity=?,spec_json=? WHERE id=?`, r.Revision, r.Capacity, canonical, r.ID); err != nil {
		return err
	}
	if _, err = processLocksTx(tx, now); err != nil {
		return err
	}
	return l.commit(tx)
}
func (l *Ledger) RemoveLockResource(id string, expectedConfigRevision int64) error {
	if !identifier.MatchString(id) || expectedConfigRevision <= 0 {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := resourceStatusTx(tx, id)
	if err != nil {
		return err
	}
	if r.Kind != "lock" {
		return ErrUnsupported
	}
	if r.ConfigRevision != expectedConfigRevision {
		return ErrStale
	}
	_, changed, err := l.clockAndDueTx(tx, false)
	if err != nil {
		return err
	}
	var active int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM managed_lock_requests WHERE lock_id=? AND state IN ('queued','granted')`, id).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		if err := l.commitLocks(tx, changed); err != nil {
			return err
		}
		return ErrBusy
	}
	if _, err := tx.Exec(`DELETE FROM managed_resources WHERE id=?`, id); err != nil {
		return err
	}
	return l.commit(tx)
}
func (l *Ledger) setLockAvailabilityTx(tx *sql.Tx, q AvailabilityRequest, resume bool, requestJSON []byte, r ResourceStatus) (ResourceStatus, error) {
	if r.ConfigRevision != q.ExpectedConfigRevision || r.AvailabilityRevision != q.ExpectedAvailabilityRevision || r.LeaseGeneration != q.ExpectedLeaseGeneration {
		return r, ErrStale
	}
	// Due processing precedes availability changes; resume hands off again after
	// changing the resource so no queued waiter waits for another transaction.
	now, _, err := l.clockAndDueTx(tx, false)
	if err != nil {
		return r, err
	}
	availability, reason := "paused", q.Reason
	if resume {
		if r.Availability != "paused" {
			return r, &AvailabilityRefusal{Current: r.Availability, Required: "paused"}
		}
		availability, reason = "available", ""
	} else if r.Availability != "available" {
		return r, &AvailabilityRefusal{Current: r.Availability, Required: "available"}
	}
	if _, err := tx.Exec(`UPDATE managed_resources SET availability=?,availability_reason=?,availability_revision=availability_revision+1 WHERE id=?`, availability, reason, r.ID); err != nil {
		return r, err
	}
	if _, err := processLocksTx(tx, now); err != nil {
		return r, err
	}
	r, err = resourceStatusTx(tx, r.ID)
	if err != nil {
		return r, err
	}
	r.Mutation = &AvailabilityIdentity{q.MutationKey, r.AvailabilityRevision}
	raw, _ := json.Marshal(r)
	if _, err := tx.Exec(`INSERT INTO managed_availability_receipts(mutation_key,resource_id,request_json,status_json,recorded_at_ns) VALUES(?,?,?,?,?)`, q.MutationKey, r.ID, requestJSON, raw, now.UnixNano()); err != nil {
		return r, err
	}
	return r, l.commit(tx)
}

// InspectLockRequest observes current state without claiming, renewing or
// applying due transitions. Now for inherited-grant checks is in LocksList.
func (l *Ledger) InspectLockRequest(id string) (LockReply, error) {
	if !identifier.MatchString(id) {
		return LockReply{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LockReply{}, err
	}
	defer tx.Rollback()
	if _, _, _, err := l.lockNowTx(tx); err != nil {
		return LockReply{}, err
	}
	r, err := loadLockTx(tx, "request_id", id)
	if err != nil {
		return LockReply{}, err
	}
	receipt, err := storedLockReceiptTx(tx, r, r.State == "queued" || r.State == "granted")
	return LockReply{receipt, r}, err
}
