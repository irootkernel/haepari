package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
)

type vmRecoveryFacts struct {
	CompletedCleanup   *runner.GuestCompletedCleanup `json:"completed_cleanup,omitempty"`
	Reboot             *runner.GuestRecoveryReply    `json:"reboot,omitempty"`
	Request            runner.Request                `json:"request"`
	Root               *managed.RetainedPath         `json:"root"`
	View               *runner.Observation           `json:"receipts"`
	WorkerStopped      bool                          `json:"worker_stopped"`
	SupervisorAlive    *bool                         `json:"supervisor_alive"`
	OwnersLive         *bool                         `json:"owners_live"`
	PartialPreparation bool                          `json:"partial_preparation"`
	Reusable           bool                          `json:"reusable"`
}

func (s *runtimeServer) observeVMRecovery(snapshot managed.RecoverySnapshot) (runner.GuestRequest, vmRecoveryFacts, error) {
	q, err := s.vmBinding(snapshot.Identity.JobID)
	if err != nil {
		return q, vmRecoveryFacts{}, err
	}
	if q.Request.Identity != snapshot.Identity {
		return q, vmRecoveryFacts{}, managed.ErrStale
	}
	q.Operation = "state"
	q.Deadline = time.Now().Add(5 * time.Second)
	state, err := s.vmCall(q)
	if err != nil || state.Control == nil {
		guest, facts, e := s.observeVMRebootRecovery(q)
		if e == nil && facts.Reboot != nil {
			for _, name := range []string{"request", "intent", "start", "outcome", "exit"} {
				if raw := snapshot.Runner[name]; len(raw) > 0 {
					sum := sha256.Sum256(raw)
					if facts.Reboot.Basis.Digests[name+".json"] != "sha256:"+hex.EncodeToString(sum[:]) {
						return guest, facts, managed.ErrBlocked
					}
				}
			}
		}
		if e == nil && facts.CompletedCleanup != nil && !completedCleanupMatchesSnapshot(snapshot, facts) {
			return guest, facts, managed.ErrBlocked
		}
		if e == nil && facts.CompletedCleanup != nil {
			if err := s.verifyVMStored(snapshot.Identity.JobID, snapshot.Phases["collect"], time.Now().Add(5*time.Second)); err != nil {
				return guest, facts, managed.ErrBlocked
			}
		}
		return guest, facts, e
	}
	facts := vmRecoveryFacts{Request: q.Request, Root: state.Root, WorkerStopped: state.Control.Stopped}
	if !facts.WorkerStopped {
		return q, facts, managed.ErrBlocked
	}
	q.Root = state.Root
	if state.Request == nil {
		facts.PartialPreparation = state.Control.Phase == "prepare" || state.Control.Retired
		if !facts.PartialPreparation || q.Root == nil {
			return q, facts, managed.ErrBlocked
		}
		return q, facts, nil
	}
	q.Request = *state.Request
	facts.Request = q.Request
	if state.Control.Phase == "cleanup" && state.Control.Complete && state.Control.Error == "" {
		q.Operation = "health"
	} else {
		q.Operation = "inspect"
	}
	q.Deadline = time.Now().Add(5 * time.Second)
	observed, err := s.vmCall(q)
	if err != nil || observed.Observation == nil {
		return q, facts, managed.ErrBlocked
	}
	facts.View = observed.Observation
	facts.SupervisorAlive = observed.SupervisorAlive
	facts.OwnersLive = observed.OwnersLive
	facts.Reusable = observed.Reusable
	if facts.Reusable {
		no := false
		facts.SupervisorAlive = &no
		facts.OwnersLive = &no
	}
	return q, facts, nil
}
func (s *runtimeServer) planVMJobRecovery(p jobRecoveryPlan) (jobRecoveryPlan, error) {
	if p.Snapshot.Identity.RunID == "" {
		if _, err := s.ledger.VMPreflightCessation(p.Snapshot.Identity); err == nil {
			return s.planVMPreflightRecovery(p)
		}
	}
	q, facts, err := s.observeVMRecovery(p.Snapshot)
	_ = q
	p.Observation = facts
	if facts.Reboot != nil || facts.CompletedCleanup != nil {
		p.Schema = "haepari.recovery-plan/v2"
		for _, name := range []string{"prepare", "collect", "cleanup"} {
			if phase, exists := p.Snapshot.Phases[name]; exists && !controllerOwnerStopped(phase) {
				p.Unknowns = append(p.Unknowns, name+" controller filesystem worker termination is unproven")
			}
		}
	}
	if (facts.Reboot != nil || facts.CompletedCleanup != nil) && p.Action != "reconcile" {
		p.Unknowns = append(p.Unknowns, "cross-boot Job recovery requires an explicit reconcile plan")
	}
	if err != nil {
		p.Unknowns = append(p.Unknowns, "exact guest worker and native ownership observation is unavailable")
	}
	if len(p.Snapshot.AdapterIntents) > 0 {
		p.Unknowns = append(p.Unknowns, "a generation-bound power effect remains unresolved")
	}
	if s.jobWorkerAlive(p.Snapshot.Identity.JobID) {
		p.Unknowns = append(p.Unknowns, "a controller worker is still active")
	}
	for _, phase := range p.Snapshot.Phases {
		p.EvidenceLoss = append(p.EvidenceLoss, phase.ProtectedEvidence...)
		if time.Now().Before(phase.ObservedAt) {
			p.Unknowns = append(p.Unknowns, "phase observation is ahead of the current clock")
		}
	}
	if p.Action != "reconcile" {
		if facts.OwnersLive == nil || *facts.OwnersLive {
			p.Unknowns = append(p.Unknowns, "guest execution ownership contradicts or cannot admit attestation")
		}
		if p.Action == "attest-no-start" && facts.View != nil && facts.View.Start != nil {
			return p, managed.ErrConflict
		}
	} else if !p.Snapshot.Stopped && facts.Reboot == nil && !facts.PartialPreparation && !(facts.View != nil && (facts.View.Exit != nil && facts.View.Exit.TerminationProven || facts.View.State == "prepared" && facts.View.Intent == nil || facts.SupervisorAlive != nil && *facts.SupervisorAlive)) {
		p.Unknowns = append(p.Unknowns, "original workload termination or surviving supervisor is unproven")
	}
	if len(p.Unknowns) == 0 {
		p.Effects = []string{"commit exact guest and revision-bound recovery intent", "observe the original run without another prepare or start", "collect before owned Job cleanup, or retain evidence and quarantine"}
		p.ExpiresAt = time.Now().UTC().Add(2 * time.Minute)
		p.PlanToken = s.signRecoveryToken(recoveryToken{s.installationID(), p.Snapshot.Identity.JobID, p.Action, managed.RecoveryDigest(p.Snapshot), managed.RecoveryDigest(facts), p.ExpiresAt})
	}
	return p, nil
}
func (s *runtimeServer) recoverVMInterrupted(snapshot managed.RecoverySnapshot, automatic bool, attestation *managed.RecoveryEvidence, plan *jobRecoveryPlan, key, clientDigest string) error {
	if snapshot.Identity.RunID == "" {
		if _, err := s.ledger.VMPreflightCessation(snapshot.Identity); err == nil {
			return s.recoverVMPreflight(snapshot, automatic, attestation, plan, key, clientDigest)
		}
	}
	if snapshot.Receipt.JobState != "reconciling" || len(snapshot.AdapterIntents) > 0 || s.jobWorkerAlive(snapshot.Identity.JobID) {
		return managed.ErrBlocked
	}
	if plan != nil {
		fresh, err := s.planJobRecovery(snapshot.Identity.JobID, plan.Action)
		if err != nil {
			return err
		}
		if len(fresh.Unknowns) > 0 || managed.RecoveryDigest(fresh.Snapshot) != managed.RecoveryDigest(plan.Snapshot) || managed.RecoveryDigest(fresh.Observation) != managed.RecoveryDigest(plan.Observation) {
			return managed.ErrStale
		}
	}
	q, facts, err := s.observeVMRecovery(snapshot)
	if err != nil {
		return err
	}
	if facts.Reboot != nil {
		if automatic {
			return s.recoverVMReboot(snapshot, q, *facts.Reboot, nil, "", "")
		}
		if plan == nil || plan.Action != "reconcile" || attestation != nil {
			return managed.ErrBlocked
		}
		return s.recoverVMReboot(snapshot, q, *facts.Reboot, plan, key, clientDigest)
	}
	if facts.CompletedCleanup != nil && (attestation != nil || plan != nil && plan.Action != "reconcile") {
		return managed.ErrBlocked
	}
	for _, phase := range snapshot.Phases {
		if time.Now().Before(phase.ObservedAt) {
			return managed.ErrBlocked
		}
	}
	request := managed.JobRecoveryRequest{Identity: snapshot.Identity, SnapshotDigest: managed.RecoveryDigest(snapshot), MutationKey: "vm-restart-" + snapshot.EventsDigest[7:39], Automatic: automatic, Evidence: recoveryObservation("authenticated-guest-native-receipts", facts, "job:"+snapshot.Identity.JobID)}
	if facts.CompletedCleanup != nil {
		request.Evidence = recoveryObservation("guest-completed-cleanup-observed", facts, "job:"+snapshot.Identity.JobID)
	}
	if key != "" {
		request.MutationKey = key
		request.ClientDigest = clientDigest
	}
	switch {
	case snapshot.Stopped:
		request.Action, request.NextState, request.NoOwnedEffect = "resume", "collecting", snapshot.NoOwnedEffect
		if snapshot.NoOwnedEffect {
			request.NextState = "settling"
		} else if p, ok := snapshot.Phases["collect"]; ok && p.Completed && p.Outcome != "unproven" {
			request.NextState = "cleaning"
			if p, ok := snapshot.Phases["cleanup"]; ok && p.Completed && p.Outcome != "unproven" {
				request.NextState = "settling"
			}
		}
	case facts.View != nil && facts.View.Exit != nil && facts.View.Exit.TerminationProven:
		request.Action, request.NextState, request.Outcome = "stopped", "collecting", &facts.View.Exit.Outcome
		if snapshot.Outcome != nil {
			request.Outcome = snapshot.Outcome
		}
	case facts.View != nil && facts.SupervisorAlive != nil && *facts.SupervisorAlive && attestation == nil:
		if _, ok := snapshot.Phases["run"]; !ok {
			return managed.ErrBlocked
		}
		request.Action, request.NextState = "reattach", "running"
		if snapshot.DeadlineRequested || snapshot.InterruptedFrom == "cancelling" {
			request.NextState = "cancelling"
		}
	case facts.PartialPreparation || facts.View != nil && facts.View.State == "prepared" && facts.View.Intent == nil:
		out := managed.LocalOutcome{WorkloadOutcome: "inconclusive"}
		request.Action, request.NextState, request.Outcome = "no-start", "collecting", &out
	case attestation != nil:
		if facts.OwnersLive == nil || *facts.OwnersLive {
			return managed.ErrBlocked
		}
		out := managed.LocalOutcome{Started: facts.View != nil && facts.View.Start != nil, WorkloadOutcome: "inconclusive"}
		if snapshot.Outcome != nil {
			out = *snapshot.Outcome
		}
		request.Action, request.NextState, request.Outcome = "stopped", "collecting", &out
		if plan != nil && plan.Action == "attest-no-start" {
			if out.Started {
				return managed.ErrConflict
			}
			request.Action = "no-start"
		}
	default:
		return managed.ErrBlocked
	}
	if attestation != nil {
		request.Evidence = *attestation
		request.Automatic = false
	}
	if request.Action == "no-start" {
		if err = s.retireVM(snapshot.Identity, snapshot.Spec, q); err != nil {
			return err
		}
		snapshot, err = s.ledger.RecoverySnapshot(snapshot.Identity.JobID)
		if err != nil {
			return err
		}
		request.SnapshotDigest = managed.RecoveryDigest(snapshot)
	}
	c, err := s.registerLocal(snapshot.Receipt)
	if err != nil {
		return err
	}
	c.vm = &q
	c.phases = snapshot.Phases
	c.recoveryEvidence = &request.Evidence
	c.effects = true
	// Publishing an observed native request does not grant a new execution. Its
	// source is the original closed guest receipt, fenced by the current lease.
	if q.Root != nil {
		binding := runner.GuestReply{Root: q.Root}
		if !facts.PartialPreparation {
			binding.Request = &q.Request
		}
		raw, _ := json.Marshal(runner.GuestReply{Request: binding.Request, Root: q.Root})
		if err = s.ledger.RecordVMPrepared(c.identity, raw); err != nil {
			s.unregisterLocal(c)
			return err
		}
		if binding.Request != nil {
			raw, _ = json.Marshal(q.Request)
			if err = s.ledger.RecordRunnerReceipt(c.identity, "request", raw); err != nil {
				s.unregisterLocal(c)
				return err
			}
		}
	}
	if facts.View != nil {
		// Import immutable receipts without freezing a new outcome in reconciling.
		for _, item := range []struct {
			kind    string
			value   any
			present bool
		}{{"intent", facts.View.Intent, facts.View.Intent != nil}, {"start", facts.View.Start, facts.View.Start != nil}, {"outcome", facts.View.Outcome, facts.View.Outcome != nil}, {"exit", facts.View.Exit, facts.View.Exit != nil}} {
			if item.present {
				raw, _ := json.Marshal(item.value)
				if err = s.ledger.RecordRunnerReceipt(c.identity, item.kind, raw); err != nil {
					s.unregisterLocal(c)
					return err
				}
			}
		}
	}
	snapshot, err = s.ledger.RecoverySnapshot(c.identity.JobID)
	if err != nil {
		s.unregisterLocal(c)
		return err
	}
	request.SnapshotDigest = managed.RecoveryDigest(snapshot)
	r, err := s.ledger.BeginJobRecovery(request)
	if err != nil {
		s.unregisterLocal(c)
		return err
	}
	c.receipt = r
	if !s.startJobWorker(c.identity.JobID, func() {
		defer s.unregisterLocal(c)
		var err error
		if request.Action == "reattach" {
			err = s.watchVM(c, snapshot.Spec, q, snapshot.Phases["run"].Deadline)
		} else {
			for _, name := range []string{"prepare", "run"} {
				if p, ok := c.phases[name]; ok && (!p.Completed || p.Outcome == "unproven") {
					outcome := "complete"
					if name == "prepare" && facts.PartialPreparation {
						outcome = "failed"
					}
					if err = s.endPhase(c, name, managed.LocalPhase{Outcome: outcome}); err != nil {
						break
					}
				}
			}
			if err == nil {
				out, e := s.ledger.LocalOutcome(c.identity.JobID)
				if e != nil {
					err = e
				} else {
					if facts.CompletedCleanup != nil {
						err = s.settleVMCompletedCleanup(c, snapshot.Spec, q, out)
					} else {
						err = s.settleVMPhases(c, snapshot.Spec, q, out)
					}
				}
			}
		}
		if err != nil && !errors.Is(err, errControllerDetached) {
			s.failed(c.identity.JobID, err)
		}
		s.notifyScheduler()
	}) {
		s.unregisterLocal(c)
		return errControllerDetached
	}
	return nil
}

func completedCleanupMatchesSnapshot(snapshot managed.RecoverySnapshot, facts vmRecoveryFacts) bool {
	collect, collectExists := snapshot.Phases["collect"]
	cleanup, cleanupExists := snapshot.Phases["cleanup"]
	if !collectExists || !collect.Completed || !collectionAllowsCleanup(snapshot.Spec, collect) || !cleanupExists || !controllerOwnerStopped(cleanup) || facts.View == nil {
		return false
	}
	for _, value := range []struct {
		name    string
		receipt any
	}{{"intent", facts.View.Intent}, {"start", facts.View.Start}, {"outcome", facts.View.Outcome}, {"exit", facts.View.Exit}} {
		if raw := snapshot.Runner[value.name]; len(raw) > 0 {
			var compact bytes.Buffer
			current, err := json.Marshal(value.receipt)
			if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), current) {
				return false
			}
		}
	}
	for _, name := range []string{"prepare", "collect", "cleanup"} {
		if p, exists := snapshot.Phases[name]; exists && !controllerOwnerStopped(p) {
			return false
		}
	}
	return true
}
