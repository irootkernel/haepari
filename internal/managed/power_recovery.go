package managed

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PowerRecoverySnapshot binds an orphan claim to its admission, lease and both
// protected resources. Reading it grants no permission to issue another command.
type PowerRecoverySnapshot struct {
	Installation string              `json:"installation_id"`
	Intent       PowerIntent         `json:"intent"`
	ClaimDigest  string              `json:"claim_digest"`
	Job          Receipt             `json:"job"`
	Target       VMResourceSnapshot  `json:"target"`
	Victim       *VMResourceSnapshot `json:"victim,omitempty"`
}

type PowerCessation struct {
	Schema         string                  `json:"schema"`
	Identity       PowerTransitionIdentity `json:"identity"`
	ExpectedStage  string                  `json:"expected_stage"`
	SnapshotDigest string                  `json:"snapshot_digest"`
	ClientDigest   string                  `json:"client_digest"`
	MutationKey    string                  `json:"mutation_key"`
	TargetPower    string                  `json:"target_power"`
	VictimPower    string                  `json:"victim_power,omitempty"`
	Evidence       RecoveryEvidence        `json:"evidence"`
}

// PowerRecoverySelectionRefusal preserves the block without choosing a claim
// when the resource has no unique unresolved transition.
type PowerRecoverySelectionRefusal struct {
	Count int
}

func (e *PowerRecoverySelectionRefusal) Error() string {
	if e.Count == 0 {
		return "No unresolved power transition references this resource; inspect the original target or victim resource."
	}
	return "Multiple unresolved power transitions reference this resource; this state is outside the supported ledger invariant. Preserve state and escalate for investigation."
}

func (e *PowerRecoverySelectionRefusal) Unwrap() error { return ErrBlocked }

func (l *Ledger) PowerRecoverySnapshot(resource string) (PowerRecoverySnapshot, error) {
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PowerRecoverySnapshot{}, err
	}
	defer tx.Rollback()
	var generation int64
	rows, err := tx.Query(`SELECT operation_generation FROM managed_power_transitions WHERE state IN ('pending','uncertain') AND (target_resource_id=? OR victim_resource_id=?)`, resource, resource)
	if err != nil {
		return PowerRecoverySnapshot{}, err
	}
	count := 0
	for rows.Next() {
		if err = rows.Scan(&generation); err != nil {
			rows.Close()
			return PowerRecoverySnapshot{}, err
		}
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return PowerRecoverySnapshot{}, err
	}
	if count != 1 {
		return PowerRecoverySnapshot{}, &PowerRecoverySelectionRefusal{Count: count}
	}
	return l.powerRecoverySnapshotTx(tx, generation)
}

func (l *Ledger) powerRecoverySnapshotTx(tx *sql.Tx, generation int64) (PowerRecoverySnapshot, error) {
	var s PowerRecoverySnapshot
	var err error
	s.Installation, err = installationTx(tx)
	if err != nil {
		return s, err
	}
	s.Intent, err = powerIntentTx(tx, generation)
	if err != nil {
		return s, err
	}
	var identityRaw, observationRaw []byte
	if err = tx.QueryRow(`SELECT p.identity_json,g.observation_json FROM managed_power_transitions p JOIN managed_power_progress g USING(operation_generation) WHERE p.operation_generation=?`, generation).Scan(&identityRaw, &observationRaw); err != nil {
		return s, err
	}
	s.ClaimDigest = RecoveryDigest(struct{ Identity, Observation []byte }{identityRaw, observationRaw})
	s.Job, err = l.snapshotTx(tx, s.Intent.Identity.TargetJobID)
	if err != nil {
		return s, err
	}
	read := func(id string) (VMResourceSnapshot, error) {
		var v VMResourceSnapshot
		var raw []byte
		if e := tx.QueryRow(`SELECT spec_json FROM managed_resources WHERE id=?`, id).Scan(&raw); e != nil {
			return v, mapNoRows(e)
		}
		if e := decodeClosed(raw, &v.Spec); e != nil {
			return v, e
		}
		v.Status, err = resourceStatusTx(tx, id)
		if err != nil {
			return v, err
		}
		err = tx.QueryRow(`SELECT observation_revision,power FROM managed_vm_observations WHERE resource_id=?`, id).Scan(&v.ObservationRevision, &v.Power)
		return v, err
	}
	s.Target, err = read(s.Intent.Identity.TargetResourceID)
	if err != nil {
		return s, err
	}
	if id := s.Intent.Identity.VictimResourceID; id != "" {
		v, e := read(id)
		if e != nil {
			return s, e
		}
		s.Victim = &v
	}
	return s, nil
}

// ResolvePowerCessation retires an exact orphan transition. It does not assert a
// command return, terminate a workload, release occupancy, or permit a replay.
func (l *Ledger) ResolvePowerCessation(q PowerCessation) (Receipt, error) {
	if q.Schema != "haepari.power-cessation/v2" || !q.Identity.Valid() || !validRecoveryDigest(q.SnapshotDigest) || !validRecoveryDigest(q.ClientDigest) ||
		!q.Evidence.valid() || q.Evidence.Source != "operator-attested" || q.Evidence.Mechanism != "power-command-and-effects-ceased/v1" ||
		q.Evidence.Reference != fmt.Sprintf("power:%d", q.Identity.OperationGeneration) || q.Evidence.ObservedAt.After(time.Now().UTC()) || time.Since(q.Evidence.ObservedAt) > 2*time.Minute ||
		!oneOf(q.TargetPower, "started", "stopped") || q.Identity.VictimResourceID != "" && !oneOf(q.VictimPower, "started", "stopped") {
		return Receipt{}, ErrInvalid
	}
	return l.mutate("power_cessation", q.MutationKey, q, func(tx *sql.Tx) (Receipt, error) {
		s, err := l.powerRecoverySnapshotTx(tx, q.Identity.OperationGeneration)
		if err != nil {
			return Receipt{}, err
		}
		if s.Intent.Identity != q.Identity || s.Intent.Stage != q.ExpectedStage || RecoveryDigest(s) != q.SnapshotDigest {
			return Receipt{}, ErrStale
		}
		if _, err = powerFenceTx(tx, q.Identity); err != nil {
			return Receipt{}, err
		}
		if s.Job.JobState != "reconciling" || !oneOf(s.Intent.Stage, PowerStopIssued, PowerStopUncertain, PowerStartIssued, PowerStartUncertain) || s.Intent.State == "reconciled" {
			return Receipt{}, ErrBlocked
		}
		stage := PowerAbandoned
		if q.TargetPower == "started" {
			if !oneOf(s.Intent.Stage, PowerStartIssued, PowerStartUncertain) || q.Identity.VictimResourceID != "" && q.VictimPower != "stopped" {
				return Receipt{}, ErrBlocked
			}
			stage = PowerDone
		}
		// Retain the immutable cessation as a separate event. The original claim
		// and command-return observation are never rewritten into native proof.
		if _, err = tx.Exec(`UPDATE managed_power_progress SET stage=? WHERE operation_generation=?`, stage, q.Identity.OperationGeneration); err != nil {
			return Receipt{}, err
		}
		if _, err = tx.Exec(`UPDATE managed_power_transitions SET state='reconciled',reconciled_at_ns=? WHERE operation_generation=?`, time.Now().UTC().UnixNano(), q.Identity.OperationGeneration); err != nil {
			return Receipt{}, err
		}
		for _, v := range []struct{ id, power string }{{q.Identity.TargetResourceID, q.TargetPower}, {q.Identity.VictimResourceID, q.VictimPower}} {
			if v.id == "" {
				continue
			}
			if _, err = tx.Exec(`UPDATE managed_vm_observations SET power=?,observation_revision=observation_revision+1,evidence_digest=? WHERE resource_id=?`, v.power, RecoveryDigest(q)[7:], v.id); err != nil {
				return Receipt{}, err
			}
		}
		cursor, err := addEvent(tx, q.Identity.TargetJobID, "power_cessation", q)
		if err != nil {
			return Receipt{}, err
		}
		r, err := l.snapshotTx(tx, q.Identity.TargetJobID)
		r.EventCursor = cursor
		return r, err
	})
}

func (l *Ledger) ReplayPowerRecovery(key, clientDigest string) (Receipt, bool, error) {
	if !ValidRequestKey(key) || !validRecoveryDigest(clientDigest) {
		return Receipt{}, false, ErrInvalid
	}
	var raw []byte
	err := l.db.QueryRow(`SELECT receipt_json FROM managed_mutations WHERE mutation_key=?`, key).Scan(&raw)
	if err == sql.ErrNoRows {
		return Receipt{}, false, nil
	}
	if err != nil {
		return Receipt{}, false, err
	}
	var r Receipt
	if err = decodeClosed(raw, &r); err != nil {
		return r, true, err
	}
	err = l.db.QueryRow(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='power_cessation' AND json_extract(payload_json,'$.mutation_key')=?`, r.JobID, key).Scan(&raw)
	if err != nil {
		return Receipt{}, true, ErrBlocked
	}
	var q PowerCessation
	if err = decodeClosed(raw, &q); err != nil {
		return Receipt{}, true, err
	}
	if q.ClientDigest != clientDigest {
		return Receipt{}, true, ErrConflict
	}
	return r, true, nil
}

// VMPreflightCessation admits only the boundary before ClaimVMRun. An absent
// guest file or SSH error cannot prove this boundary once a run was claimed.
func (l *Ledger) VMPreflightCessation(id LocalIdentity) (PowerCessation, error) {
	var q PowerCessation
	if id.RunID != "" {
		return q, ErrBlocked
	}
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return q, err
	}
	defer tx.Rollback()
	if err = matchLocalIdentityTx(tx, id); err != nil {
		return q, err
	}
	var claims int
	if err = tx.QueryRow(`SELECT (SELECT COUNT(*) FROM managed_runs WHERE job_id=?)+(SELECT COUNT(*) FROM managed_runner_receipts WHERE job_id=?)+(SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind IN ('vm_plan','vm_prepared','vm_retained','vm_retire_intent'))`, id.JobID, id.JobID, id.JobID).Scan(&claims); err != nil {
		return q, err
	}
	if claims != 0 {
		return q, ErrBlocked
	}
	var raw []byte
	if err = tx.QueryRow(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='power_cessation' ORDER BY cursor DESC LIMIT 1`, id.JobID).Scan(&raw); err != nil {
		return q, mapNoRows(err)
	}
	if decodeClosed(raw, &q) != nil || q.Schema != "haepari.power-cessation/v2" || q.Identity.TargetJobID != id.JobID || q.Identity.TargetResourceID != id.ResourceID || q.Identity.TargetConfigRevision != id.ConfigRevision || q.Identity.TargetLeaseGeneration != id.LeaseGeneration || q.Evidence.Source != "operator-attested" || q.Evidence.Mechanism != "power-command-and-effects-ceased/v1" {
		return q, ErrBlocked
	}
	p, err := powerFenceTx(tx, q.Identity)
	if err != nil {
		return q, err
	}
	if p.State != "reconciled" || !oneOf(p.Stage, PowerDone, PowerAbandoned) {
		return q, ErrBlocked
	}
	return q, nil
}
