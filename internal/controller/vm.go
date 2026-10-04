package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/utm"
)

func (s *runtimeServer) vmControl(spec managed.ResourceSpec) utm.Control {
	return utm.Control{Executable: spec.Adapter.UTMCtl, Run: s.utmCommand}
}

func (s *runtimeServer) verifyVMRegistration(ctx context.Context, spec managed.ResourceSpec) error {
	if _, err := utm.ReadPin(spec.Guest.KnownHostsReference, spec.VM.UTMVMUUID); err != nil {
		return err
	}
	_, err := s.vmControl(spec).Inspect(ctx, spec.VM.UTMVMUUID)
	return err
}

func vmEvidence(value any) string {
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// observeVMs supplies the complete revision-bound host place inventory. Unknown
// and transient operator VMs consume places but never become stop candidates.
func (s *runtimeServer) observeVMs(ctx context.Context, hostLimit int) (*managed.VMReservationGuard, error) {
	resources, err := s.ledger.VMResources()
	if err != nil {
		return nil, err
	}
	guard := &managed.VMReservationGuard{HostLimit: hostLimit, ObservationRevisions: map[string]int64{}}
	registered := map[string]bool{}
	lists := map[string][]utm.VM{}
	for _, v := range resources {
		registered[strings.ToLower(v.Spec.VM.UTMVMUUID)] = true
		if _, ok := lists[v.Spec.Adapter.UTMCtl]; !ok {
			list, err := s.vmControl(v.Spec).List(ctx)
			if err != nil {
				return nil, err
			}
			lists[v.Spec.Adapter.UTMCtl] = list
		}
	}
	operators := map[string]bool{}
	for _, list := range lists {
		for _, vm := range list {
			if !registered[vm.UUID] && vm.State != "stopped" {
				operators[vm.UUID] = true
			}
		}
	}
	guard.UnregisteredRunning = len(operators)
	for _, v := range resources {
		power, err := s.vmControl(v.Spec).Status(ctx, v.Spec.VM.UTMVMUUID)
		if err != nil {
			power = "uncertain"
		}
		revision, err := s.ledger.ObserveVM(managed.VMObservation{ResourceID: v.Spec.ID, ConfigRevision: v.Spec.Revision,
			VMUUID: v.Spec.VM.UTMVMUUID, Power: power, ExpectedObservationRevision: v.ObservationRevision,
			EvidenceDigest: vmEvidence(struct {
				Resource managed.VMResourceSnapshot
				Power    string
			}{v, power})})
		if err != nil {
			return nil, err
		}
		guard.ObservationRevisions[v.Spec.ID] = revision
	}
	return guard, nil
}

// advanceVMPower performs at most one claimed effect, then observes both sides.
// A successful command return must be durably recorded before observations can
// advance it. An issued/uncertain effect without that proof remains protected.
func (s *runtimeServer) advanceVMPower(ctx context.Context, identity managed.PowerTransitionIdentity) (managed.PowerIntent, error) {
	p, err := s.ledger.PowerIntent(identity.OperationGeneration)
	if err != nil {
		return p, err
	}
	if p.Identity != identity {
		return p, managed.ErrStale
	}
	resources, err := s.ledger.VMResources()
	if err != nil {
		return p, err
	}
	byID := map[string]managed.ResourceSpec{}
	for _, v := range resources {
		byID[v.Spec.ID] = v.Spec
	}
	for _, fence := range []struct {
		ID, UUID string
		Revision int64
	}{
		{identity.TargetResourceID, identity.TargetVMUUID, identity.TargetConfigRevision},
		{identity.VictimResourceID, identity.VictimVMUUID, identity.VictimConfigRevision},
	} {
		if fence.ID == "" {
			continue
		}
		spec, ok := byID[fence.ID]
		if !ok || spec.Revision != fence.Revision || !strings.EqualFold(spec.VM.UTMVMUUID, fence.UUID) {
			return p, managed.ErrStale
		}
	}
	if p.State == "reconciled" {
		return p, nil
	}
	if effect, ready := p.ReadyEffect(); ready {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		resource := identity.TargetResourceID
		if effect == "stop" {
			resource = identity.VictimResourceID
		}
		if err := s.ledger.ClaimHostPowerEffect(identity, effect); err != nil {
			return p, err
		}
		commandErr := s.vmControl(byID[resource]).Power(ctx, byID[resource].VM.UTMVMUUID, effect)
		proof := struct {
			Identity managed.PowerTransitionIdentity
			Effect   string
			Success  bool
		}{identity, effect, commandErr == nil}
		if err := s.ledger.RecordPowerCommandReturn(identity, effect, proof.Success, vmEvidence(proof)); err != nil {
			return p, err
		}
		p, err = s.ledger.PowerIntent(identity.OperationGeneration)
		if err != nil {
			return p, err
		}
	}
	if !p.CommandReturned() {
		// A different caller may still be about to issue the claimed command, or
		// its return may have been lost. Never infer its cessation from status.
		return p, nil
	}
	observe := func(id string) string {
		if id == "" {
			return ""
		}
		state, err := s.vmControl(byID[id]).Status(ctx, byID[id].VM.UTMVMUUID)
		if err != nil {
			return "uncertain"
		}
		return state
	}
	q := managed.PowerConfirmation{Identity: identity, ExpectedStage: p.Stage,
		VictimPower: observe(identity.VictimResourceID), TargetPower: observe(identity.TargetResourceID)}
	q.EvidenceDigest = vmEvidence(q)
	if err := s.ledger.ConfirmPower(q); err != nil {
		return p, err
	}
	return s.ledger.PowerIntent(identity.OperationGeneration)
}
