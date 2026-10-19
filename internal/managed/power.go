package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// VMReservationGuard is supplied by the qualified adapter, never the client.
// Its revisions bind a complete registered-VM observation set. Operator VMs
// consume places but are never candidates for automatic stops.
type VMReservationGuard struct {
	HostLimit            int
	UnregisteredRunning  int
	ObservationRevisions map[string]int64
}
type HostCapacityBlocked struct{ Blocker QueueBlocker }

func (e *HostCapacityBlocked) Error() string { return e.Blocker.Reason }
func (e *HostCapacityBlocked) Unwrap() error { return ErrBusy }

type VMObservation struct {
	ResourceID                  string `json:"resource_id"`
	ConfigRevision              int64  `json:"config_revision"`
	VMUUID                      string `json:"vm_uuid"`
	Power                       string `json:"power"`
	ExpectedObservationRevision int64  `json:"expected_observation_revision"`
	EvidenceDigest              string `json:"evidence_digest"`
}

func (l *Ledger) ObserveVM(q VMObservation) (int64, error) {
	if !oneOf(q.Power, "started", "stopped", "uncertain") || !validDigest(q.EvidenceDigest) || q.ExpectedObservationRevision < 0 {
		return 0, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var rev int64
	var uuid, kind string
	if err := tx.QueryRow(`SELECT config_revision,vm_uuid,kind FROM managed_resources WHERE id=?`, q.ResourceID).Scan(&rev, &uuid, &kind); err != nil {
		return 0, mapNoRows(err)
	}
	if kind != "utm-macos" || rev != q.ConfigRevision || uuid != strings.ToLower(q.VMUUID) {
		return 0, ErrStale
	}
	if pending, err := pendingPowerTx(tx, q.ResourceID); err != nil {
		return 0, err
	} else if pending {
		return 0, ErrBusy
	}
	var previous int64
	err = tx.QueryRow(`SELECT observation_revision FROM managed_vm_observations WHERE resource_id=?`, q.ResourceID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if previous != q.ExpectedObservationRevision {
		return 0, ErrStale
	}
	next := previous + 1
	if _, err := tx.Exec(`INSERT INTO managed_vm_observations VALUES(?,?,?,?,?,?) ON CONFLICT(resource_id) DO UPDATE SET config_revision=excluded.config_revision,vm_uuid=excluded.vm_uuid,power=excluded.power,observation_revision=excluded.observation_revision,evidence_digest=excluded.evidence_digest`, q.ResourceID, rev, uuid, q.Power, next, q.EvidenceDigest); err != nil {
		return 0, err
	}
	return next, l.commit(tx)
}

type vmSchedulingRow struct {
	id, uuid, availability, power string
	config, observationRevision   int64
	occupancy                     int
	idle                          sql.NullInt64
	spec                          ResourceSpec
	pending                       bool
}

func reserveVMHostPlaceTx(tx *sql.Tx, jobID, resourceID string, lease int64, guard *VMReservationGuard) error {
	var kind string
	if err := tx.QueryRow(`SELECT kind FROM managed_resources WHERE id=?`, resourceID).Scan(&kind); err != nil {
		return err
	}
	if kind != "utm-macos" {
		if guard != nil {
			return ErrInvalid
		}
		return nil
	}
	if guard == nil || guard.HostLimit < 1 || guard.HostLimit > 2 || guard.UnregisteredRunning < 0 {
		return ErrBlocked
	}
	rows, err := tx.Query(`SELECT r.id,r.config_revision,r.vm_uuid,r.availability,r.occupancy,r.idle_since_ns,r.spec_json,o.power,o.config_revision,o.vm_uuid,o.observation_revision FROM managed_resources r LEFT JOIN managed_vm_observations o ON o.resource_id=r.id WHERE r.kind='utm-macos' ORDER BY r.id`)
	if err != nil {
		return err
	}
	var vms []vmSchedulingRow
	for rows.Next() {
		var v vmSchedulingRow
		var raw []byte
		var power, uuid sql.NullString
		var config, observation sql.NullInt64
		if err := rows.Scan(&v.id, &v.config, &v.uuid, &v.availability, &v.occupancy, &v.idle, &raw, &power, &config, &uuid, &observation); err != nil {
			rows.Close()
			return err
		}
		if !power.Valid || !config.Valid || config.Int64 != v.config || uuid.String != v.uuid || guard.ObservationRevisions[v.id] != observation.Int64 {
			rows.Close()
			return ErrStale
		}
		if err := json.Unmarshal(raw, &v.spec); err != nil {
			rows.Close()
			return err
		}
		v.power = power.String
		v.observationRevision = observation.Int64
		vms = append(vms, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(guard.ObservationRevisions) != len(vms) {
		return ErrStale
	}
	var target *vmSchedulingRow
	for i := range vms {
		v := &vms[i]
		v.pending, err = pendingPowerTx(tx, v.id)
		if err != nil {
			return err
		}
		if v.id == resourceID {
			target = v
		}
	}
	if target == nil {
		return ErrStale
	}
	if target.pending {
		return ErrBlocked
	}
	if target.power == "started" {
		return nil
	}
	raw, err := json.Marshal(map[string]string{
		"schema": "haepari.vm-not-started/v1", "resource_id": resourceID, "observation": target.power,
	})
	if err != nil {
		return err
	}
	return &HostCapacityBlocked{QueueBlocker{"vm_not_started", raw}}
}

type PowerIntent struct {
	Identity              PowerTransitionIdentity `json:"identity"`
	State                 string                  `json:"state"`
	Stage                 string                  `json:"stage"`
	noEffectReturned      bool
	commandReturned       bool
	commandReturnRequired bool
	hostControlled        bool
}

// The claim origin belongs to the transition, independently of replaceable
// power observations. It is private ledger metadata, not a wire identity field.
type powerTransitionRecord struct {
	PowerTransitionIdentity
	HostControlled bool `json:"host_controlled,omitempty"`
}

func decodePowerTransition(raw []byte) (powerTransitionRecord, error) {
	var record powerTransitionRecord
	if rejectDuplicateKeys(raw) != nil {
		return record, ErrBlocked
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return record, ErrBlocked
	}
	if value, ok := fields["host_controlled"]; ok {
		if string(value) != "true" && string(value) != "false" {
			return record, ErrBlocked
		}
		if json.Unmarshal(value, &record.HostControlled) != nil {
			return record, ErrBlocked
		}
		delete(fields, "host_controlled")
	}
	identity, err := json.Marshal(fields)
	if err != nil || decodeClosed(identity, &record.PowerTransitionIdentity) != nil || !record.PowerTransitionIdentity.Valid() {
		return record, ErrBlocked
	}
	return record, nil
}

// CommandReturned reports a successful command return recorded for this exact
// effect. It is not power-state or no-effect evidence.
func (p PowerIntent) CommandReturned() bool      { return p.commandReturned }
func (p PowerIntent) RefusedWithoutEffect() bool { return p.noEffectReturned }

// Power stages are internal durable vocabulary. The ledger owns which ready
// stage permits a single effect; adapters only observe issued/uncertain stages.
const (
	PowerStopReady      = "stop_ready"
	PowerStopIssued     = "stop_issued"
	PowerStopUncertain  = "stop_uncertain"
	PowerStartReady     = "start_ready"
	PowerStartIssued    = "start_issued"
	PowerStartUncertain = "start_uncertain"
	PowerDone           = "done"
	PowerFailedNoEffect = "failed_no_effect"
	PowerAbandoned      = "abandoned"
	PowerStopRetained   = "stop_retained"
)

func (p PowerIntent) ReadyEffect() (string, bool) {
	if p.State != "pending" {
		return "", false
	}
	switch p.Stage {
	case PowerStopReady:
		return "stop", true
	case PowerStartReady:
		return "start", true
	default:
		return "", false
	}
}

// IssuedPowerStage names the durable stage after a successful effect claim.
// It supplies vocabulary only; ClaimPowerEffect owns permission to issue it.
func IssuedPowerStage(effect string) (string, bool) {
	switch effect {
	case "stop":
		return PowerStopIssued, true
	case "start":
		return PowerStartIssued, true
	default:
		return "", false
	}
}

func powerIntentTx(q rowQuerier, generation int64) (PowerIntent, error) {
	var p PowerIntent
	var raw, observation []byte
	var hasObservation bool
	err := q.QueryRow(`SELECT p.identity_json,p.state,g.stage,g.observation_json,g.observation_json IS NOT NULL FROM managed_power_transitions p JOIN managed_power_progress g USING(operation_generation) WHERE p.operation_generation=?`, generation).Scan(&raw, &p.State, &p.Stage, &observation, &hasObservation)
	if err != nil {
		return p, mapNoRows(err)
	}
	transition, err := decodePowerTransition(raw)
	if err != nil {
		return p, err
	}
	p.Identity = transition.PowerTransitionIdentity
	p.hostControlled = transition.HostControlled
	// SQL NULL is the qualified-adapter claim's absent observation. A present
	// but unreadable or unrelated proof must not erase the host-return guard.
	if !hasObservation {
		if p.hostControlled && oneOf(p.Stage, PowerStopIssued, PowerStopUncertain, PowerStartIssued, PowerStartUncertain) {
			return p, ErrBlocked
		}
		return p, nil
	}
	var proof PowerConfirmation
	if decodeClosed(observation, &proof) != nil || proof.Identity != p.Identity {
		return p, ErrBlocked
	}
	if proof.CommandReturnRequired != p.hostControlled {
		return p, ErrBlocked
	}
	if samePowerEffect(proof.ExpectedStage, p.Stage) {
		p.commandReturnRequired = proof.CommandReturnRequired
		p.commandReturned = proof.CommandReturned
		p.noEffectReturned = proof.NoEffectProven && proof.CommandReturned
	} else if !((p.Stage == PowerStartReady && oneOf(proof.ExpectedStage, PowerStopIssued, PowerStopUncertain)) ||
		(p.Stage == PowerStopRetained && oneOf(proof.ExpectedStage, PowerStopIssued, PowerStopUncertain)) ||
		(p.Stage == PowerDone && oneOf(proof.ExpectedStage, PowerStartIssued, PowerStartUncertain)) ||
		(p.Stage == PowerAbandoned && proof.ExpectedStage == "no_command" && !proof.CommandReturned) ||
		(oneOf(p.Stage, PowerFailedNoEffect, PowerAbandoned) && oneOf(proof.ExpectedStage, PowerStopIssued, PowerStopUncertain, PowerStartIssued, PowerStartUncertain))) {
		return p, ErrBlocked
	}
	return p, nil
}

func samePowerEffect(a, b string) bool {
	return (oneOf(a, PowerStopIssued, PowerStopUncertain) && oneOf(b, PowerStopIssued, PowerStopUncertain)) ||
		(oneOf(a, PowerStartIssued, PowerStartUncertain) && oneOf(b, PowerStartIssued, PowerStartUncertain))
}
func (l *Ledger) PowerIntent(generation int64) (PowerIntent, error) {
	return powerIntentTx(l.db, generation)
}
func powerFenceTx(tx *sql.Tx, identity PowerTransitionIdentity) (PowerIntent, error) {
	p, err := powerIntentTx(tx, identity.OperationGeneration)
	if err != nil {
		return p, err
	}
	if p.Identity != identity {
		return p, ErrStale
	}
	for _, v := range []struct {
		id, uuid string
		revision int64
	}{{identity.TargetResourceID, identity.TargetVMUUID, identity.TargetConfigRevision}, {identity.VictimResourceID, identity.VictimVMUUID, identity.VictimConfigRevision}} {
		if v.id == "" {
			continue
		}
		var rev int64
		var uuid string
		if err := tx.QueryRow(`SELECT config_revision,vm_uuid FROM managed_resources WHERE id=?`, v.id).Scan(&rev, &uuid); err != nil {
			return p, mapNoRows(err)
		}
		if rev != v.revision || uuid != v.uuid {
			return p, ErrStale
		}
	}
	var lease int64
	var resource, state string
	if err := tx.QueryRow(`SELECT lease_generation,resource_id,state FROM managed_jobs WHERE id=?`, identity.TargetJobID).Scan(&lease, &resource, &state); err != nil {
		return p, mapNoRows(err)
	}
	if lease != identity.TargetLeaseGeneration || resource != identity.TargetResourceID || oneOf(state, "queued", "finished") {
		return p, ErrStale
	}
	return p, nil
}

// ClaimPowerEffect is deliberately not receipt-idempotent: exactly one caller
// gets permission to issue a command. A lost reply requires observation, not a
// second claim or command. No transaction remains open during the command.
func (l *Ledger) ClaimPowerEffect(identity PowerTransitionIdentity, effect string) error {
	return l.claimPowerEffect(identity, effect, false)
}

// ClaimHostPowerEffect binds the UTM controller's claim to a required durable
// command return. Observers cannot bypass that requirement, including on reopen.
// ClaimPowerEffect remains the trusted qualified-adapter reconciliation API.
func (l *Ledger) ClaimHostPowerEffect(identity PowerTransitionIdentity, effect string) error {
	return l.claimPowerEffect(identity, effect, true)
}

func (l *Ledger) claimPowerEffect(identity PowerTransitionIdentity, effect string, requireReturn bool) error {
	stage, valid := IssuedPowerStage(effect)
	if !identity.Valid() || !valid {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := powerFenceTx(tx, identity)
	if err != nil {
		return err
	}
	var jobState, availability string
	if err := tx.QueryRow(`SELECT j.state,r.availability FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, identity.TargetJobID).Scan(&jobState, &availability); err != nil {
		return err
	}
	if jobState != "preparing" || availability != "available" {
		return ErrBlocked
	}
	if err := requireJobBoundsTx(tx, identity.TargetJobID); err != nil {
		return err
	}
	readyEffect, ready := p.ReadyEffect()
	if !ready || readyEffect != effect {
		return ErrBlocked
	}
	if p.hostControlled && !requireReturn {
		return ErrBlocked
	}
	var observation []byte
	if requireReturn {
		observation, _ = json.Marshal(PowerConfirmation{Identity: identity, ExpectedStage: stage, CommandReturnRequired: true})
		transition, _ := json.Marshal(powerTransitionRecord{PowerTransitionIdentity: identity, HostControlled: true})
		if _, err := tx.Exec(`UPDATE managed_power_transitions SET identity_json=? WHERE operation_generation=?`, transition, identity.OperationGeneration); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE managed_power_progress SET stage=?,observation_json=? WHERE operation_generation=?`, stage, observation, identity.OperationGeneration); err != nil {
		return err
	}
	if _, err := addEvent(tx, identity.TargetJobID, "power_effect_intent", map[string]any{"identity": identity, "effect": effect}); err != nil {
		return err
	}
	return l.commit(tx)
}

type PowerConfirmation struct {
	Identity              PowerTransitionIdentity
	ExpectedStage         string
	VictimPower           string
	TargetPower           string
	EvidenceDigest        string
	NoEffectProven        bool
	CommandReturned       bool
	CommandReturnRequired bool
}

// RecordPowerCommandReturn is called only by the successful claimant after its
// command has returned. Errors retain uncertainty; neither an error nor a later
// desired status proves the command cannot execute late. The existing progress
// blob keeps this proof across reopen without introducing another power stage.
func (l *Ledger) RecordPowerCommandReturn(identity PowerTransitionIdentity, effect string, success bool, digest string) error {
	issued, valid := IssuedPowerStage(effect)
	if !identity.Valid() || !valid || !validDigest(digest) {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := powerFenceTx(tx, identity)
	if err != nil {
		return err
	}
	if p.Stage != issued || p.State != "pending" || !p.commandReturnRequired || p.CommandReturned() {
		return ErrStale
	}
	state, stage := p.State, p.Stage
	if !success {
		state, stage = "uncertain", PowerStartUncertain
		if effect == "stop" {
			stage = PowerStopUncertain
		}
	}
	if err := requireJobBoundsTx(tx, identity.TargetJobID); err != nil {
		return err
	}
	proof := PowerConfirmation{Identity: identity, ExpectedStage: stage, EvidenceDigest: digest, CommandReturned: success, CommandReturnRequired: true}
	raw, _ := json.Marshal(proof)
	if _, err := tx.Exec(`UPDATE managed_power_progress SET stage=?,observation_json=? WHERE operation_generation=?`, stage, raw, identity.OperationGeneration); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE managed_power_transitions SET state=? WHERE operation_generation=?`, state, identity.OperationGeneration); err != nil {
		return err
	}
	if _, err := addEvent(tx, identity.TargetJobID, "power_command_returned", map[string]any{"identity": identity, "effect": effect, "success": success, "evidence_digest": digest}); err != nil {
		return err
	}
	return l.commit(tx)
}

// ConfirmPower consumes a fresh, identity-bound adapter observation. Uncertain
// effects retain both protections. A confirmed stop stays stopped. PowerDone
// requires the original command return plus the existing start fences.
func (l *Ledger) ConfirmPower(q PowerConfirmation) error {
	if !q.Identity.Valid() || !validDigest(q.EvidenceDigest) || !oneOf(q.TargetPower, "started", "stopped", "uncertain") || (q.Identity.VictimResourceID != "" && !oneOf(q.VictimPower, "started", "stopped", "uncertain")) {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := powerFenceTx(tx, q.Identity)
	if err != nil {
		return err
	}
	if p.Stage != q.ExpectedStage || p.State == "reconciled" {
		return ErrStale
	}
	// A fresh power observation does not prove that the original command returned.
	// PowerDone and stop_retained both require that durable return.
	if !p.CommandReturned() || q.NoEffectProven != p.RefusedWithoutEffect() {
		return ErrBlocked
	}
	q.CommandReturnRequired = p.commandReturnRequired
	q.CommandReturned = p.CommandReturned()
	state, stage := "uncertain", p.Stage
	if oneOf(p.Stage, PowerStopIssued, PowerStopUncertain) {
		if q.TargetPower != "stopped" {
			return ErrBlocked
		}
		stage = PowerStopUncertain
		if q.VictimPower == "stopped" {
			state, stage = "reconciled", PowerStopRetained
		}
	} else if oneOf(p.Stage, PowerStartIssued, PowerStartUncertain) {
		stage = PowerStartUncertain
		if q.TargetPower == "started" && (q.Identity.VictimResourceID == "" || q.VictimPower == "stopped") {
			state, stage = "reconciled", PowerDone
		}
	} else {
		return ErrBlocked
	}
	if q.NoEffectProven && q.TargetPower == "stopped" && ((oneOf(p.Stage, PowerStartIssued, PowerStartUncertain) && (q.Identity.VictimResourceID == "" || q.VictimPower == "stopped")) || (oneOf(p.Stage, PowerStopIssued, PowerStopUncertain) && q.VictimPower == "started")) {
		state, stage = "reconciled", PowerFailedNoEffect
	}
	if err := requireJobBoundsTx(tx, q.Identity.TargetJobID); err != nil {
		return err
	}
	raw, _ := json.Marshal(q)
	if _, err := tx.Exec(`UPDATE managed_power_progress SET stage=?,observation_json=? WHERE operation_generation=?`, stage, raw, q.Identity.OperationGeneration); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE managed_power_transitions SET state=?,reconciled_at_ns=? WHERE operation_generation=?`, state, time.Now().UTC().UnixNano(), q.Identity.OperationGeneration); err != nil {
		return err
	}
	// Keep unknown stop counted conservatively and prohibit independent refresh.
	for _, v := range []struct{ id, power string }{{q.Identity.VictimResourceID, q.VictimPower}, {q.Identity.TargetResourceID, q.TargetPower}} {
		if v.id == "" {
			continue
		}
		if state == "uncertain" && v.power == "stopped" && v.id == q.Identity.VictimResourceID {
			continue
		}
		if _, err := tx.Exec(`UPDATE managed_vm_observations SET power=?,observation_revision=observation_revision+1,evidence_digest=? WHERE resource_id=?`, v.power, q.EvidenceDigest, v.id); err != nil {
			return err
		}
	}
	if _, err := addEvent(tx, q.Identity.TargetJobID, "power_observed", map[string]any{"identity": q.Identity, "state": state, "stage": stage, "evidence_digest": q.EvidenceDigest}); err != nil {
		return err
	}
	return l.commit(tx)
}

// RecordPowerRefusal records a returned framework rejection of this exact start,
// separately from successful effects and ambiguous failures. A fresh stopped
// target observation is still required before either protection can clear.
func (l *Ledger) RecordPowerRefusal(id PowerTransitionIdentity, digest string) error {
	if !id.Valid() || !validDigest(digest) {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := powerFenceTx(tx, id)
	if err != nil {
		return err
	}
	if p.State != "pending" || p.Stage != PowerStartIssued || !p.commandReturnRequired || p.CommandReturned() {
		return ErrStale
	}
	if err := requireJobBoundsTx(tx, id.TargetJobID); err != nil {
		return err
	}
	proof := PowerConfirmation{Identity: id, ExpectedStage: p.Stage, EvidenceDigest: digest, CommandReturned: true, CommandReturnRequired: true, NoEffectProven: true}
	raw, _ := json.Marshal(proof)
	if _, err = tx.Exec(`UPDATE managed_power_progress SET observation_json=? WHERE operation_generation=?`, raw, id.OperationGeneration); err != nil {
		return err
	}
	if _, err = addEvent(tx, id.TargetJobID, "power_start_refused", map[string]any{"identity": id, "evidence_digest": digest}); err != nil {
		return err
	}
	return l.commit(tx)
}

// DrainHistoricalPower reconciles only an established unissued ready row, or a
// start_ready row whose stored proof is the original returned stop. A present
// malformed, mismatched, unreturned, or return-required contradiction stays
// pending and byte-identical. Issued and uncertain rows are not selected.
func (l *Ledger) DrainHistoricalPower() error {
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT p.operation_generation,p.identity_json,g.stage,g.observation_json,g.observation_json IS NOT NULL FROM managed_power_transitions p JOIN managed_power_progress g USING(operation_generation) WHERE p.state='pending' AND g.stage IN (?,?) ORDER BY p.operation_generation`, PowerStartReady, PowerStopReady)
	if err != nil {
		return err
	}
	type readyRow struct {
		generation  int64
		identity    PowerTransitionIdentity
		stage       string
		observation []byte
		has         bool
	}
	var ready []readyRow
	for rows.Next() {
		var item readyRow
		var raw []byte
		if err = rows.Scan(&item.generation, &raw, &item.stage, &item.observation, &item.has); err != nil {
			rows.Close()
			return err
		}
		record, err := decodePowerTransition(raw)
		if err != nil {
			rows.Close()
			return err
		}
		item.identity = record.PowerTransitionIdentity
		ready = append(ready, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range ready {
		var jobState string
		if err = tx.QueryRow(`SELECT state FROM managed_jobs WHERE id=?`, item.identity.TargetJobID).Scan(&jobState); err != nil {
			return mapNoRows(err)
		}
		if jobState == "finished" {
			continue
		}
		if err = requireJobBoundsTx(tx, item.identity.TargetJobID); err != nil {
			return err
		}
		intent, fenceErr := powerFenceTx(tx, item.identity)
		if fenceErr != nil || intent.State != "pending" || !oneOf(intent.Stage, PowerStartReady, PowerStopReady) {
			continue
		}
		var observation []byte
		var has bool
		if err = tx.QueryRow(`SELECT observation_json, observation_json IS NOT NULL FROM managed_power_progress WHERE operation_generation=?`, item.generation).Scan(&observation, &has); err != nil {
			return err
		}
		stage := ""
		next := observation
		if has {
			var proof PowerConfirmation
			if decodeClosed(observation, &proof) != nil || intent.Stage != PowerStartReady || !validRetainedStopProof(intent, proof) {
				continue
			}
			stage = PowerStopRetained
		} else if intent.hostControlled || intent.commandReturnRequired {
			continue
		} else {
			stage = PowerAbandoned
			next, _ = json.Marshal(PowerConfirmation{Identity: item.identity, ExpectedStage: "no_command"})
		}
		if _, err = tx.Exec(`UPDATE managed_power_progress SET stage=?,observation_json=? WHERE operation_generation=?`, stage, next, item.generation); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE managed_power_transitions SET state='reconciled',reconciled_at_ns=? WHERE operation_generation=?`, time.Now().UTC().UnixNano(), item.generation); err != nil {
			return err
		}
		if _, err = addEvent(tx, item.identity.TargetJobID, "power_observed", map[string]any{"identity": item.identity, "state": "reconciled", "stage": stage, "no_command": stage == PowerAbandoned}); err != nil {
			return err
		}
	}
	return l.commit(tx)
}

// DrainedVMPreflight binds the no-guest decision to the complete current lease
// and original durable generation. Raw stored evidence is included in its digest.
type DrainedVMPreflight struct {
	Identity      LocalIdentity
	PowerIdentity PowerTransitionIdentity
	Stage         string
	Transition    json.RawMessage
	Observation   json.RawMessage
	Refusal       json.RawMessage
	ReconciledAt  int64
}

func (b DrainedVMPreflight) EvidenceDigest() string {
	return strings.TrimPrefix(RecoveryDigest(b), "sha256:")
}

const DrainedVMPreflightMechanism = "historical-power-drained-before-guest/v1"

func (l *Ledger) DrainedVMPreflight(id LocalIdentity) (DrainedVMPreflight, error) {
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DrainedVMPreflight{}, err
	}
	defer tx.Rollback()
	return drainedVMPreflightTx(tx, id)
}

func drainedVMPreflightTx(tx *sql.Tx, id LocalIdentity) (DrainedVMPreflight, error) {
	var boundary DrainedVMPreflight
	if id.RunID != "" {
		return boundary, ErrBlocked
	}
	if err := matchLocalIdentityTx(tx, id); err != nil {
		return boundary, err
	}
	rows, err := tx.Query(`SELECT p.operation_generation,p.identity_json FROM managed_power_transitions p WHERE p.target_resource_id=? ORDER BY p.operation_generation`, id.ResourceID)
	if err != nil {
		return boundary, err
	}
	var matches []PowerTransitionIdentity
	for rows.Next() {
		var generation int64
		var raw []byte
		if err = rows.Scan(&generation, &raw); err != nil {
			rows.Close()
			return boundary, err
		}
		record, e := decodePowerTransition(raw)
		if e != nil {
			rows.Close()
			return boundary, e
		}
		identity := record.PowerTransitionIdentity
		if identity.TargetJobID == id.JobID && identity.TargetLeaseGeneration == id.LeaseGeneration && identity.TargetConfigRevision == id.ConfigRevision && identity.TargetResourceID == id.ResourceID {
			if identity.OperationGeneration != generation {
				rows.Close()
				return boundary, ErrBlocked
			}
			matches = append(matches, identity)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return boundary, err
	}
	if len(matches) == 0 {
		return boundary, ErrNotFound
	}
	if len(matches) != 1 {
		return boundary, ErrBlocked
	}
	intent, err := powerFenceTx(tx, matches[0])
	if err != nil {
		return boundary, err
	}
	if intent.State == "reconciled" && intent.Stage == PowerDone {
		return boundary, ErrNotFound
	}
	var claims int
	if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM managed_runs WHERE job_id=?)+(SELECT COUNT(*) FROM managed_runner_receipts WHERE job_id=?)+(SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind IN ('vm_plan','vm_prepared','vm_retained','vm_retire_intent'))`, id.JobID, id.JobID, id.JobID).Scan(&claims); err != nil {
		return boundary, err
	}
	if claims != 0 {
		return boundary, ErrBlocked
	}
	if intent.State != "reconciled" || !oneOf(intent.Stage, PowerAbandoned, PowerStopRetained, PowerFailedNoEffect) {
		return boundary, ErrBlocked
	}
	boundary.Identity, boundary.PowerIdentity, boundary.Stage = id, intent.Identity, intent.Stage
	if err = tx.QueryRow(`SELECT p.identity_json,g.observation_json,p.reconciled_at_ns FROM managed_power_transitions p JOIN managed_power_progress g USING(operation_generation) WHERE p.operation_generation=?`, intent.Identity.OperationGeneration).Scan(&boundary.Transition, &boundary.Observation, &boundary.ReconciledAt); err != nil {
		return boundary, err
	}
	var proof PowerConfirmation
	if decodeClosed(boundary.Observation, &proof) != nil || proof.Identity != intent.Identity || boundary.ReconciledAt <= 0 || boundary.ReconciledAt > time.Now().UnixNano() {
		return boundary, ErrBlocked
	}
	switch intent.Stage {
	case PowerAbandoned:
		if intent.hostControlled || proof != (PowerConfirmation{Identity: intent.Identity, ExpectedStage: "no_command"}) {
			return boundary, ErrBlocked
		}
	case PowerStopRetained:
		if !validRetainedStopProof(intent, proof) {
			return boundary, ErrBlocked
		}
	case PowerFailedNoEffect:
		if !validRefusedStartProof(intent, proof) {
			return boundary, ErrBlocked
		}
		if err = tx.QueryRow(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='power_start_refused' AND json_extract(payload_json,'$.identity.operation_generation')=? ORDER BY cursor DESC LIMIT 1`, id.JobID, intent.Identity.OperationGeneration).Scan(&boundary.Refusal); err != nil {
			return boundary, ErrBlocked
		}
		var refusal struct {
			Identity       PowerTransitionIdentity `json:"identity"`
			EvidenceDigest string                  `json:"evidence_digest"`
		}
		if decodeClosed(boundary.Refusal, &refusal) != nil || refusal.Identity != intent.Identity || !validDigest(refusal.EvidenceDigest) {
			return boundary, ErrBlocked
		}
	}
	return boundary, nil
}

func validRetainedStopProof(intent PowerIntent, proof PowerConfirmation) bool {
	return proof.Identity == intent.Identity && intent.Identity.VictimResourceID != "" && intent.hostControlled && proof.CommandReturned && proof.CommandReturnRequired && !proof.NoEffectProven && validDigest(proof.EvidenceDigest) && oneOf(proof.ExpectedStage, PowerStopIssued, PowerStopUncertain) && proof.TargetPower == "stopped" && proof.VictimPower == "stopped"
}

func validRefusedStartProof(intent PowerIntent, proof PowerConfirmation) bool {
	victim := proof.VictimPower == "stopped"
	if intent.Identity.VictimResourceID == "" {
		victim = proof.VictimPower == ""
	}
	return proof.Identity == intent.Identity && intent.hostControlled && proof.CommandReturned && proof.CommandReturnRequired && proof.NoEffectProven && validDigest(proof.EvidenceDigest) && oneOf(proof.ExpectedStage, PowerStartIssued, PowerStartUncertain) && proof.TargetPower == "stopped" && victim
}

// DrainedVMSettlementRequest is immutable across controller restart. Current
// revisions and cancellation are checked inside the same commit as settlement.
type DrainedVMSettlementRequest struct {
	Identity       LocalIdentity
	BoundaryDigest string
	MutationKey    string
}

func (l *Ledger) SettleDrainedVMPreflight(q DrainedVMSettlementRequest) (Receipt, error) {
	if q.Identity.RunID != "" || q.Identity.JobID == "" || q.Identity.LeaseGeneration <= 0 || q.Identity.Nonce == "" || !validDigest(q.BoundaryDigest) || q.MutationKey != "vm-no-effect-"+q.Identity.JobID {
		return Receipt{}, ErrInvalid
	}
	return l.mutate("settle_drained_vm_preflight", q.MutationKey, q, func(tx *sql.Tx) (Receipt, error) {
		if err := requireJobBoundsTx(tx, q.Identity.JobID); err != nil {
			return Receipt{}, err
		}
		boundary, err := drainedVMPreflightTx(tx, q.Identity)
		if err != nil {
			return Receipt{}, err
		}
		if boundary.EvidenceDigest() != q.BoundaryDigest {
			return Receipt{}, ErrStale
		}
		r, err := l.snapshotTx(tx, q.Identity.JobID)
		if err != nil {
			return Receipt{}, err
		}
		status, err := cancellationStatusTx(tx, q.Identity.JobID)
		if err != nil {
			return Receipt{}, err
		}
		failure := "prepare_failure"
		if status.Requested {
			failure = "cancel"
		}
		proof := NoEffectProof{ObservationReference: "vm-preflight:" + q.Identity.JobID, NoOwnedJobEffect: true, NoUnprovenProcess: true, NoProtectedPath: true, PowerReconciled: true}
		r, err = l.recordNoEffectObservationTx(tx, NoEffectObservationRequest{JobID: r.JobID, ExpectedJobRevision: r.JobRevision, ResourceID: r.ResourceID, ExpectedConfigRevision: r.ResourceConfigRevision, Slot: r.Slot, LeaseGeneration: r.LeaseGeneration, Source: "machine", Mechanism: DrainedVMPreflightMechanism, EvidenceDigest: q.BoundaryDigest, Proof: proof})
		if err != nil {
			return Receipt{}, err
		}
		hostReason := ""
		if boundary.Stage == PowerFailedNoEffect {
			hostReason = "host_vm_limit"
		}
		r, err = l.classifyNoEffectTx(tx, ClassifyNoEffectRequest{JobID: r.JobID, ExpectedJobRevision: r.JobRevision, ResourceID: r.ResourceID, ExpectedConfigRevision: r.ResourceConfigRevision, ExpectedAvailabilityRevision: r.ResourceAvailabilityRevision, Slot: r.Slot, LeaseGeneration: r.LeaseGeneration, FailureKind: failure, HostPauseReason: hostReason, Proof: proof})
		if err != nil {
			return Receipt{}, err
		}
		return l.settleNoEffectTx(tx, SettleNoEffectRequest{JobID: r.JobID, ExpectedJobRevision: r.JobRevision, ResourceID: r.ResourceID, ExpectedConfigRevision: r.ResourceConfigRevision, ExpectedAvailabilityRevision: r.ResourceAvailabilityRevision, Slot: r.Slot, LeaseGeneration: r.LeaseGeneration})
	})
}

// GuestPowerDisposition never turns malformed or unresolved historical claims
// into permission for guest activity. Only absence of a matching claim is clear.
func (l *Ledger) GuestPowerDisposition(id LocalIdentity) (string, error) {
	if id.JobID == "" || id.LeaseGeneration <= 0 {
		return "protected", nil
	}
	if id.RunID != "" {
		return "clear", nil
	}
	_, err := l.DrainedVMPreflight(id)
	if err == nil {
		return "no_guest", nil
	}
	if errors.Is(err, ErrNotFound) {
		return "clear", nil
	}
	if errors.Is(err, ErrBlocked) || errors.Is(err, ErrStale) {
		return "protected", nil
	}
	return "", err
}

func drainedVMObservationTx(tx *sql.Tx, jobID string) bool {
	raw, err := localIdentityTx(tx, jobID)
	if err != nil {
		return false
	}
	var id LocalIdentity
	if json.Unmarshal(raw, &id) != nil {
		return false
	}
	boundary, err := drainedVMPreflightTx(tx, id)
	if err != nil {
		return false
	}
	var count int
	err = tx.QueryRow(`SELECT count(*) FROM managed_no_effect_observations WHERE job_id=? AND mechanism=? AND evidence_digest=?`, jobID, DrainedVMPreflightMechanism, boundary.EvidenceDigest()).Scan(&count)
	return err == nil && count == 1
}
