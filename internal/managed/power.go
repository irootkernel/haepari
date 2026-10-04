package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	count := guard.UnregisteredRunning
	var target *vmSchedulingRow
	var candidates []vmSchedulingRow
	details := map[string][]string{"policy": {}, "pause": {}, "occupation": {}, "transition": {}, "unknown_idle": {}, "uncertain": {}}
	for i := range vms {
		v := &vms[i]
		v.pending, err = pendingPowerTx(tx, v.id)
		if err != nil {
			return err
		}
		if v.id == resourceID {
			target = v
		}
		if v.power != "stopped" {
			count++
		}
		// A transferred claim initially consumes the victim's still occupied place.
		// After observed stop, and for a direct start, it consumes its own place.
		if v.pending && v.power == "stopped" {
			var stage string
			err := tx.QueryRow(`SELECT g.stage FROM managed_power_transitions p JOIN managed_power_progress g USING(operation_generation) WHERE p.target_resource_id=? AND p.state IN ('pending','uncertain')`, v.id).Scan(&stage)
			if err == nil && stage != PowerStopReady && stage != PowerStopIssued && stage != PowerStopUncertain {
				count++
			} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if v.id == resourceID || v.power == "stopped" {
			continue
		}
		switch {
		case v.pending:
			details["transition"] = append(details["transition"], v.id)
		case v.power != "started":
			details["uncertain"] = append(details["uncertain"], v.id)
		case v.occupancy != 0:
			details["occupation"] = append(details["occupation"], v.id)
		case v.availability != "available":
			details["pause"] = append(details["pause"], v.id)
		case v.spec.VM == nil || !v.spec.VM.AllowAutomaticStop:
			details["policy"] = append(details["policy"], v.id)
		case !v.idle.Valid:
			details["unknown_idle"] = append(details["unknown_idle"], v.id)
		default:
			candidates = append(candidates, *v)
		}
	}
	if target == nil {
		return ErrStale
	}
	if target.power == "started" {
		return nil
	}
	if target.power != "stopped" {
		return ErrBlocked
	}
	var victim *vmSchedulingRow
	if count >= guard.HostLimit {
		// A single stop cannot repair an already over-limit host.
		if count == guard.HostLimit {
			for i := range candidates {
				c := &candidates[i]
				if victim == nil || c.idle.Int64 < victim.idle.Int64 || (c.idle.Int64 == victim.idle.Int64 && c.id < victim.id) {
					victim = c
				}
			}
		}
		if victim == nil {
			// Bound diagnostics even for an already over-limit host. Preserve category
			// samples and report omitted counts; never turn capacity blocking into invalid input.
			for category, ids := range details {
				if len(ids) > 4 {
					details[category] = append(ids[:4:4], fmt.Sprintf("%d additional resources", len(ids)-4))
				}
			}
			raw, _ := json.Marshal(details)
			return &HostCapacityBlocked{QueueBlocker{"host_capacity_no_eligible_victim", raw}}
		}
	}
	var generation int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(operation_generation),0)+1 FROM managed_power_transitions`).Scan(&generation); err != nil {
		return err
	}
	identity := PowerTransitionIdentity{OperationGeneration: generation, TargetJobID: jobID, TargetLeaseGeneration: lease, TargetResourceID: resourceID, TargetConfigRevision: target.config, TargetVMUUID: target.uuid}
	stage := PowerStartReady
	if victim != nil {
		identity.VictimResourceID = victim.id
		identity.VictimConfigRevision = victim.config
		identity.VictimVMUUID = victim.uuid
		stage = PowerStopReady
	}
	raw, _ := json.Marshal(identity)
	if _, err := tx.Exec(`INSERT INTO managed_power_transitions(operation_generation,victim_resource_id,target_resource_id,identity_json,state) VALUES(?,?,?,?,'pending')`, generation, identity.VictimResourceID, resourceID, raw); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO managed_power_progress(operation_generation,stage) VALUES(?,?)`, generation, stage); err != nil {
		return err
	}
	_, err = addEvent(tx, jobID, "power_reserved", identity)
	return err
}

type PowerIntent struct {
	Identity              PowerTransitionIdentity `json:"identity"`
	State                 string                  `json:"state"`
	Stage                 string                  `json:"stage"`
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

// CommandReturned reports a successful command return recorded for this exact
// effect. It is not power-state or no-effect evidence.
func (p PowerIntent) CommandReturned() bool { return p.commandReturned }

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
	var transition powerTransitionRecord
	if err := json.Unmarshal(raw, &transition); err != nil {
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
	if json.Unmarshal(observation, &proof) != nil || proof.Identity != p.Identity {
		return p, ErrBlocked
	}
	if proof.CommandReturnRequired != p.hostControlled {
		return p, ErrBlocked
	}
	if samePowerEffect(proof.ExpectedStage, p.Stage) {
		p.commandReturnRequired = proof.CommandReturnRequired
		p.commandReturned = proof.CommandReturned
	} else if !((p.Stage == PowerStartReady && oneOf(proof.ExpectedStage, PowerStopIssued, PowerStopUncertain)) ||
		(p.Stage == PowerDone && oneOf(proof.ExpectedStage, PowerStartIssued, PowerStartUncertain)) ||
		(p.Stage == PowerFailedNoEffect && oneOf(proof.ExpectedStage, PowerStopIssued, PowerStopUncertain, PowerStartIssued, PowerStartUncertain))) {
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
// effects retain both protections. Only a confirmed stop enables target start.
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
	// The host controller requires durable cessation of the claimed command in
	// addition to fresh power observations. Other qualified adapters retain their
	// existing responsibility to supply reconciled evidence to this trusted API.
	if p.commandReturnRequired && !p.CommandReturned() {
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
			state, stage = "pending", PowerStartReady
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
