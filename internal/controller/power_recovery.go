package controller

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
)

type powerRecoveryObservation struct {
	TargetPower string `json:"target_power"`
	VictimPower string `json:"victim_power,omitempty"`
}

type powerRecoveryPlan struct {
	Schema           string                        `json:"schema"`
	Action           string                        `json:"action"`
	Snapshot         managed.PowerRecoverySnapshot `json:"snapshot"`
	Observation      powerRecoveryObservation      `json:"observation"`
	ObservedAt       time.Time                     `json:"observed_at"`
	Unknowns         []string                      `json:"unknowns"`
	Effects          []string                      `json:"proposed_effects"`
	EvidenceLoss     []managed.ProtectedEvidence   `json:"evidence_loss"`
	RequiredEvidence managed.RecoveryEvidence      `json:"required_evidence"`
	PlanToken        string                        `json:"plan_token,omitempty"`
	ExpiresAt        time.Time                     `json:"expires_at"`
}

func (s *runtimeServer) planPowerRecovery(id string) (powerRecoveryPlan, error) {
	p := powerRecoveryPlan{Schema: "haepari.power-recovery-plan/v2", Action: "attest-power-stopped", ObservedAt: time.Now().UTC(), Unknowns: []string{}, Effects: []string{}, EvidenceLoss: []managed.ProtectedEvidence{}}
	var err error
	p.Snapshot, err = s.ledger.PowerRecoverySnapshot(id)
	if err != nil {
		return p, err
	}
	identity := p.Snapshot.Intent.Identity
	p.RequiredEvidence = managed.RecoveryEvidence{Source: "operator-attested", Mechanism: "power-command-and-effects-ceased/v1", Reference: fmt.Sprintf("power:%d", identity.OperationGeneration)}
	if p.Snapshot.Job.JobState != "reconciling" || s.jobWorkerAlive(identity.TargetJobID) {
		p.Unknowns = append(p.Unknowns, "original controller worker has not stopped")
	}
	stage := p.Snapshot.Intent.Stage
	if stage != managed.PowerStopIssued && stage != managed.PowerStopUncertain && stage != managed.PowerStartIssued && stage != managed.PowerStartUncertain {
		p.Unknowns = append(p.Unknowns, "an issued or uncertain exact power claim is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observe := func(v managed.VMResourceSnapshot, uuid string, revision int64) string {
		if v.Spec.Revision != revision || v.Spec.VM == nil || v.Spec.VM.UTMVMUUID != uuid {
			p.Unknowns = append(p.Unknowns, "registered VM identity or config revision changed")
			return "uncertain"
		}
		state, e := s.vmControl(v.Spec).Status(ctx, uuid)
		if e != nil || state != "started" && state != "stopped" {
			p.Unknowns = append(p.Unknowns, "fresh stable VM power state is unproven")
			return "uncertain"
		}
		return state
	}
	p.Observation.TargetPower = observe(p.Snapshot.Target, identity.TargetVMUUID, identity.TargetConfigRevision)
	if p.Snapshot.Victim != nil {
		p.Observation.VictimPower = observe(*p.Snapshot.Victim, identity.VictimVMUUID, identity.VictimConfigRevision)
	}
	if p.Observation.TargetPower == "started" && (stage == managed.PowerStopIssued || stage == managed.PowerStopUncertain || p.Snapshot.Victim != nil && p.Observation.VictimPower != "stopped") {
		p.Unknowns = append(p.Unknowns, "observed states cannot safely retire this original transfer")
	}
	if len(p.Unknowns) == 0 {
		p.Effects = []string{"record scoped operator proof that the original command, descendants and framework effects cannot execute late", "retire this exact transfer without issuing another power command", "retain Job occupancy, native receipts and resource quarantine until separate Job and resource recovery"}
		p.ExpiresAt = time.Now().UTC().Add(2 * time.Minute)
		p.PlanToken = s.signRecoveryToken(recoveryToken{s.installationID(), "resource:" + id, p.Action, managed.RecoveryDigest(p.Snapshot), managed.RecoveryDigest(p.Observation), p.ExpiresAt})
	}
	return p, nil
}

func powerClientDigest(id string, q managed.RecoveryRequest) string {
	return managed.RecoveryDigest(struct {
		ResourceID string                  `json:"resource_id"`
		Request    managed.RecoveryRequest `json:"request"`
	}{id, q})
}

func (s *runtimeServer) applyPowerRecovery(w http.ResponseWriter, r *http.Request, id string, q managed.RecoveryRequest, token recoveryToken) {
	op := "resource.recover"
	if token.JobID != "resource:"+id || token.Action != "attest-power-stopped" || !time.Now().Before(token.ExpiresAt) || q.Evidence == nil {
		reject(w, op, managed.ErrStale)
		return
	}
	p, err := s.planPowerRecovery(id)
	if err != nil {
		reject(w, op, err)
		return
	}
	if len(p.Unknowns) != 0 || managed.RecoveryDigest(p.Snapshot) != token.SnapshotDigest || managed.RecoveryDigest(p.Observation) != token.ObservationDigest {
		reject(w, op, managed.ErrStale)
		return
	}
	e := *q.Evidence
	now := time.Now().UTC()
	if managed.ValidateRecoveryEvidence(e) != nil || e.Source != "operator-attested" || e.Mechanism != p.RequiredEvidence.Mechanism || e.Reference != p.RequiredEvidence.Reference || e.ObservedAt.After(now) || e.ObservedAt.Before(token.ExpiresAt.Add(-2*time.Minute)) {
		reject(w, op, managed.ErrInvalid)
		return
	}
	cessation := managed.PowerCessation{Schema: "haepari.power-cessation/v2", Identity: p.Snapshot.Intent.Identity, ExpectedStage: p.Snapshot.Intent.Stage, SnapshotDigest: token.SnapshotDigest, ClientDigest: powerClientDigest(id, q), MutationKey: "power-recovery-" + q.MutationKey, TargetPower: p.Observation.TargetPower, VictimPower: p.Observation.VictimPower, Evidence: e}
	receipt, err := s.ledger.ResolvePowerCessation(cessation)
	if err != nil {
		reject(w, op, err)
		return
	}
	s.notifyScheduler()
	send(w, 200, op, receipt, nil)
}

type vmPreflightRecoveryObservation struct {
	Cessation        managed.PowerCessation `json:"power_cessation"`
	GuestClaimAbsent bool                   `json:"guest_claim_absent"`
	OwnedRootAbsent  bool                   `json:"owned_root_absent"`
	WorkerStopped    bool                   `json:"worker_stopped"`
}

func (s *runtimeServer) planVMPreflightRecovery(p jobRecoveryPlan) (jobRecoveryPlan, error) {
	p.Schema = "haepari.recovery-plan/v2"
	proof, err := s.ledger.VMPreflightCessation(p.Snapshot.Identity)
	if err != nil {
		return p, err
	}
	owned, rootErr := s.inspectRecoveryRoot(p.Snapshot.Identity)
	stopped := !s.jobWorkerAlive(p.Snapshot.Identity.JobID)
	for _, phase := range p.Snapshot.Phases {
		if !controllerOwnerStopped(phase) || time.Now().Before(phase.ObservedAt) {
			stopped = false
		}
	}
	observation := vmPreflightRecoveryObservation{Cessation: proof, GuestClaimAbsent: true, OwnedRootAbsent: rootErr == nil && !owned, WorkerStopped: stopped}
	p.Observation = observation
	if p.Action != "reconcile" {
		p.Unknowns = append(p.Unknowns, "pre-guest power recovery requires an explicit reconcile plan")
	}
	if p.Snapshot.Receipt.JobState != "reconciling" || len(p.Snapshot.AdapterIntents) != 0 || !stopped {
		p.Unknowns = append(p.Unknowns, "original prepare worker or transfer remains unresolved")
	}
	if !observation.OwnedRootAbsent {
		p.Unknowns = append(p.Unknowns, "owned host Job root absence is unproven")
	}
	if p.Snapshot.Outcome != nil && (!p.Snapshot.NoOwnedEffect || p.Snapshot.Outcome.Started) {
		p.Unknowns = append(p.Unknowns, "an existing workload outcome contradicts the pre-guest boundary")
	}
	if len(p.Unknowns) == 0 {
		p.Effects = []string{"record exact pre-guest boundary and existing operator power cessation provenance", "settle the original Job INCOMPLETE with no workload execution or guest cleanup", "return Job capacity while retaining resource quarantine for independent readiness verification"}
		p.ExpiresAt = time.Now().UTC().Add(2 * time.Minute)
		p.PlanToken = s.signRecoveryToken(recoveryToken{s.installationID(), p.Snapshot.Identity.JobID, p.Action, managed.RecoveryDigest(p.Snapshot), managed.RecoveryDigest(observation), p.ExpiresAt})
	}
	return p, nil
}

func (s *runtimeServer) recoverVMPreflight(snapshot managed.RecoverySnapshot, automatic bool, attestation *managed.RecoveryEvidence, plan *jobRecoveryPlan, key, clientDigest string) error {
	if automatic || attestation != nil || plan == nil || plan.Action != "reconcile" {
		return managed.ErrBlocked
	}
	fresh, err := s.planVMPreflightRecovery(jobRecoveryPlan{Snapshot: snapshot, Action: "reconcile", Unknowns: []string{}, Effects: []string{}, EvidenceLoss: []managed.ProtectedEvidence{}})
	if err != nil {
		return err
	}
	if len(fresh.Unknowns) != 0 || managed.RecoveryDigest(fresh.Snapshot) != managed.RecoveryDigest(plan.Snapshot) || managed.RecoveryDigest(fresh.Observation) != managed.RecoveryDigest(plan.Observation) {
		return managed.ErrStale
	}
	proof, err := s.ledger.VMPreflightCessation(snapshot.Identity)
	if err != nil {
		return err
	}
	q := managed.JobRecoveryRequest{Identity: snapshot.Identity, SnapshotDigest: managed.RecoveryDigest(snapshot), ClientDigest: clientDigest, MutationKey: key, Action: "no-start", NextState: "settling", NoOwnedEffect: true, Evidence: proof.Evidence}
	if snapshot.Stopped {
		q.Action = "resume"
	} else {
		q.Outcome = &managed.LocalOutcome{WorkloadOutcome: "inconclusive"}
	}
	c, err := s.registerLocal(snapshot.Receipt)
	if err != nil {
		return err
	}
	defer s.unregisterLocal(c)
	c.recoveryEvidence = &q.Evidence
	c.phases = snapshot.Phases
	c.receipt, err = s.ledger.BeginJobRecovery(q)
	if err != nil {
		return err
	}
	out, err := s.ledger.LocalOutcome(snapshot.Identity.JobID)
	if err != nil {
		return err
	}
	return s.verifyLocalResult(c, managed.Result{WorkloadOutcome: out.WorkloadOutcome, EvidenceCompleteness: "complete", CleanupOutcome: "not_needed", Reason: "prepare_failed", ResourceAvailability: "quarantined"}, true)
}
