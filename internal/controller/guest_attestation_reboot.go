package controller

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
	"golang.org/x/sys/unix"
)

func (s *runtimeServer) rebootIntent(guest runner.GuestRequest) (runner.GuestRecoveryRequest, bool, error) {
	var q runner.GuestRecoveryRequest
	path := filepath.Join(s.root.Path, "jobs", guest.Request.Identity.JobID, "guest-reboot-recovery.json")
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return q, false, nil
	}
	if err != nil {
		return q, false, err
	}
	defer f.Close()
	if _, err = privateLogStat(int(f.Fd()), false); err != nil {
		return q, false, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, managed.GuestRecoveryReceiptBytes+1))
	if err != nil {
		return q, false, err
	}
	q, err = decodeAttestationRequest(raw)
	if err != nil || q.Schema != "haepari.guest-recovery-request/v2" || !reflect.DeepEqual(guestRecoveryOwner(q.Guest), guestRecoveryOwner(guest)) {
		return q, false, managed.ErrBlocked
	}
	return q, true, nil
}

func rebootAttestation(q runner.GuestRecoveryRequest) runner.GuestAttestation {
	return runner.GuestAttestation{Schema: "haepari.guest-attestation/v2", Guest: guestRecoveryOwner(q.Guest), Basis: *q.Basis, RecoveryDigest: q.RecoveryDigest, MutationKey: q.MutationKey, Deadline: q.Deadline, Evidence: *q.Evidence}
}

func (s *runtimeServer) vmRebootAttestation(guest runner.GuestRequest) (runner.GuestRecoveryRequest, bool, error) {
	q, found, err := s.rebootIntent(guest)
	if err != nil || !found {
		return q, found, err
	}
	a := rebootAttestation(q)
	if runner.ValidateGuestAttestation(a, guest) != nil {
		return q, false, managed.ErrBlocked
	}
	path := filepath.Join(s.root.Path, "jobs", guest.Request.Identity.JobID, "guest-reboot-attestation.json")
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return q, false, nil
	}
	if err != nil {
		return q, false, err
	}
	defer f.Close()
	if _, err = privateLogStat(int(f.Fd()), false); err != nil {
		return q, false, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, managed.GuestRecoveryReceiptBytes+1))
	var saved runner.GuestAttestation
	if err != nil || json.Unmarshal(raw, &saved) != nil || !reflect.DeepEqual(saved, a) {
		return q, false, managed.ErrBlocked
	}
	q.Attestation = &a
	return q, true, nil
}

func (s *runtimeServer) observeVMRebootRecovery(guest runner.GuestRequest) (runner.GuestRequest, vmRecoveryFacts, error) {
	facts := vmRecoveryFacts{}
	spec, err := s.vmSpec(guest.Request.Identity.ResourceID)
	if err != nil {
		return guest, facts, err
	}
	transport, err := s.vmSSH(spec)
	if err != nil {
		return guest, facts, err
	}
	q := runner.GuestRecoveryRequest{Schema: "haepari.guest-recovery-request/v2", Operation: "probe", Guest: guest, RecoveryDigest: transport.RunnerDigest, Deadline: time.Now().Add(5 * time.Second)}
	if saved, found, e := s.rebootIntent(guest); e != nil {
		return guest, facts, e
	} else if found {
		q.RecoveryDigest, q.RecoveryBoot = saved.RecoveryDigest, saved.RecoveryBoot
	}
	reply, err := s.vmRecoveryCall(q)
	if err == nil && reply.CompletedCleanup != nil && runner.ValidateGuestCompletedCleanup(*reply.CompletedCleanup, guest, reply.Guest.BootID) == nil {
		no := false
		proof := reply.CompletedCleanup
		facts.Request, facts.Root, facts.View = *proof.Control.NativeRequest, proof.Control.Root, proof.Control.Observation
		facts.WorkerStopped, facts.SupervisorAlive, facts.OwnersLive, facts.Reusable, facts.CompletedCleanup = true, &no, &no, true, proof
		return guest, facts, nil
	}
	if err != nil || reply.Basis == nil || reply.Basis.RecoveryBoot == "" {
		return guest, facts, managed.ErrBlocked
	}
	reply.Guest.Challenge, reply.Guest.ProtocolDigest, reply.ProtocolDigest = "", "", ""
	no := false
	facts.Request, facts.Root, facts.View = reply.Basis.Request, &reply.Basis.Root, &reply.Basis.Observation
	facts.WorkerStopped, facts.SupervisorAlive, facts.OwnersLive, facts.Reboot = true, &no, &no, &reply
	return guest, facts, nil
}

// An explicit cross-boot application establishes termination and preserves the
// original Job directory. Recollection and health use the existing resource
// recovery plans after this Job is terminal; no native receipt is synthesized.
func (s *runtimeServer) recoverVMReboot(snapshot managed.RecoverySnapshot, guest runner.GuestRequest, reply runner.GuestRecoveryReply, plan *jobRecoveryPlan, key, clientDigest string) error {
	q, found, err := s.rebootIntent(guest)
	if err != nil {
		return err
	}
	resuming := plan == nil
	if resuming {
		if !found || !snapshot.Stopped || snapshot.Outcome == nil || q.Evidence == nil {
			return managed.ErrBlocked
		}
		committed, e := s.ledger.CommittedJobRecovery(snapshot.Identity, q.MutationKey)
		if e != nil || committed.Automatic || committed.Action != "stopped" || committed.NextState != "collecting" || committed.NoOwnedEffect || committed.Outcome == nil || !reflect.DeepEqual(committed.Evidence, *q.Evidence) || committed.Evidence.Source != "machine-observed" || committed.Evidence.Mechanism != "guest-reboot-observed" {
			return managed.ErrBlocked
		}
		// This new host-only mutation resumes the committed authority. The guest
		// intent, evidence, key and all existing phase deadlines stay unchanged.
		key, clientDigest = "reboot-settle-"+snapshot.EventsDigest[7:39], ""
	}
	if !found {
		evidence := recoveryObservation("guest-reboot-observed", *reply.Basis, "job:"+snapshot.Identity.JobID)
		q = runner.GuestRecoveryRequest{Schema: "haepari.guest-recovery-request/v2", Operation: "attest", Guest: guest, RecoveryBoot: reply.Basis.RecoveryBoot, Basis: reply.Basis, RecoveryDigest: reply.RecoveryDigest, Deadline: plan.ExpiresAt, MutationKey: key, Evidence: &evidence}
		q.Guest.Deadline = time.Time{}
		if runner.ValidateGuestAttestation(rebootAttestation(q), guest) != nil {
			return managed.ErrBlocked
		}
		spec, e := s.vmSpec(snapshot.Identity.ResourceID)
		if e != nil {
			return e
		}
		transport, e := s.vmSSH(spec)
		if e != nil || transport.RunnerDigest != q.RecoveryDigest {
			return managed.ErrBlocked
		}
		root := filepath.Join(s.root.Path, "jobs", snapshot.Identity.JobID)
		if e = publishRecoveryRunner(filepath.Join(root, "guest-recovery-runner-"+q.RecoveryDigest[7:]), transport.Runner); e != nil {
			return e
		}
		raw, _ := json.Marshal(q)
		if e = publishRecoveryRunner(filepath.Join(root, "guest-reboot-recovery.json"), raw); e != nil {
			return e
		}
	} else if !resuming && q.MutationKey != key || !reflect.DeepEqual(*q.Basis, *reply.Basis) {
		return managed.ErrConflict
	}
	a := rebootAttestation(q)
	var observed runner.GuestRecoveryReply
	if reply.Attestation != nil && reflect.DeepEqual(*reply.Attestation, a) {
		// Probe publication alone does not prove retirement. State is read-only
		// and the native capability requires the exact durable retirement barrier.
		read := q
		read.Operation, read.Attestation = "state", &a
		read.Deadline = time.Now().Add(5 * time.Second)
		observed, err = s.vmRecoveryCall(read)
	} else {
		if resuming || !time.Now().Before(q.Deadline) {
			return managed.ErrBlocked
		}
		observed, err = s.vmRecoveryCall(q)
	}
	if err != nil || observed.Attestation == nil || !reflect.DeepEqual(*observed.Attestation, a) {
		return managed.ErrBlocked
	}
	raw, _ := json.Marshal(a)
	if err = publishRecoveryRunner(filepath.Join(s.root.Path, "jobs", snapshot.Identity.JobID, "guest-reboot-attestation.json"), raw); err != nil {
		return err
	}
	for _, item := range []struct {
		kind    string
		value   any
		present bool
	}{{"intent", reply.Basis.Observation.Intent, reply.Basis.Observation.Intent != nil}, {"start", reply.Basis.Observation.Start, reply.Basis.Observation.Start != nil}, {"outcome", reply.Basis.Observation.Outcome, reply.Basis.Observation.Outcome != nil}, {"exit", reply.Basis.Observation.Exit, reply.Basis.Observation.Exit != nil}} {
		if item.present {
			raw, _ := json.Marshal(item.value)
			if err = s.ledger.RecordRunnerReceipt(snapshot.Identity, item.kind, raw); err != nil {
				return err
			}
		}
	}
	snapshot, err = s.ledger.RecoverySnapshot(snapshot.Identity.JobID)
	if err != nil {
		return err
	}
	out := managed.LocalOutcome{Started: reply.Basis.Observation.Start != nil, WorkloadOutcome: "inconclusive"}
	if snapshot.Outcome != nil {
		out = *snapshot.Outcome
	} else if reply.Basis.Observation.Outcome != nil {
		out = reply.Basis.Observation.Outcome.Outcome
	}
	request := managed.JobRecoveryRequest{Identity: snapshot.Identity, SnapshotDigest: managed.RecoveryDigest(snapshot), Action: "stopped", NextState: "collecting", Outcome: &out, MutationKey: key, ClientDigest: clientDigest, Evidence: *q.Evidence}
	if snapshot.Stopped {
		request.Action, request.Outcome = "resume", nil
		if p, exists := snapshot.Phases["collect"]; exists && p.Completed && p.Outcome != "unproven" {
			request.NextState = "cleaning"
			if p, exists := snapshot.Phases["cleanup"]; exists && p.Completed && p.Outcome != "unproven" {
				request.NextState = "settling"
			}
		}
	}
	c, err := s.registerLocal(snapshot.Receipt)
	if err != nil {
		return err
	}
	c.phases, c.vm, c.effects, c.recoveryEvidence = snapshot.Phases, &guest, true, &request.Evidence
	r, err := s.ledger.BeginJobRecovery(request)
	if err != nil {
		s.unregisterLocal(c)
		return err
	}
	c.receipt = r
	out, err = s.ledger.LocalOutcome(snapshot.Identity.JobID)
	if err != nil {
		s.unregisterLocal(c)
		return err
	}
	observeRetentionBoundary("guest-reboot-host-intent")
	if !s.startJobWorker(c.identity.JobID, func() {
		defer s.unregisterLocal(c)
		if e := s.settleRebootRetained(c, snapshot.Spec, out); e != nil && !errors.Is(e, errControllerDetached) {
			s.failed(c.identity.JobID, e)
		}
		s.notifyScheduler()
	}) {
		s.unregisterLocal(c)
		return errControllerDetached
	}
	return nil
}

func (s *runtimeServer) settleRebootRetained(c *localRunControl, job managed.JobSpec, out managed.LocalOutcome) error {
	protected := []managed.ProtectedEvidence{{Path: "workspace", Reason: "guest_reboot_evidence_retained", RecoveryReference: "job:" + c.identity.JobID}}
	for _, name := range []string{"prepare", "run"} {
		if p, exists := c.phases[name]; exists && (!p.Completed || p.Outcome == "unproven") {
			if err := s.endPhase(c, name, managed.LocalPhase{Outcome: "failed"}); err != nil {
				return err
			}
		}
	}
	for _, phase := range []struct {
		name, from, to string
		seconds        int64
	}{{"collect", "collecting", "cleaning", job.Deadlines.CollectSeconds}, {"cleanup", "cleaning", "settling", job.Deadlines.CleanupSeconds}} {
		if _, exists := c.phases[phase.name]; !exists {
			if err := s.beginPhase(c, phase.name, phase.seconds, time.Now().Add(time.Duration(phase.seconds)*time.Second)); err != nil {
				return err
			}
		}
		p := c.phases[phase.name]
		if !p.Completed || p.Outcome == "unproven" {
			completion := managed.LocalPhase{Outcome: "failed", ProtectedEvidence: protected}
			if phase.name == "collect" {
				completion.EvidenceCompleteness = "partial"
				completion.EvidenceGaps = []managed.EvidenceGap{{Path: "workspace", Reason: "guest_reboot_evidence_retained"}}
				var err error
				completion, err = s.manualCollectionPhase(job, c.identity.JobID, completion)
				if err != nil {
					return err
				}
			}
			if err := s.endPhase(c, phase.name, completion); err != nil {
				return err
			}
		}
		if c.receipt.JobState == phase.from {
			if err := s.advancePhase(c, phase.from, phase.to, "reboot-retained-"+phase.name); err != nil {
				return err
			}
		}
		observeRetentionBoundary("guest-reboot-host-" + phase.name + "-recorded")
	}
	collect, cleanup := c.phases["collect"], c.phases["cleanup"]
	return s.verifyLocalResult(c, managed.Result{WorkloadOutcome: out.WorkloadOutcome, NativeExitCode: out.NativeExitCode, NativeSignal: out.NativeSignal, EvidenceCompleteness: collect.EvidenceCompleteness, CleanupOutcome: "failed", ResourceAvailability: "quarantined", Reason: "guest_reboot_evidence_retained", Artifacts: collect.Artifacts, Logs: collect.Logs, ProtectedEvidence: cleanup.ProtectedEvidence, EvidenceGaps: collect.EvidenceGaps}, false)
}

func (s *runtimeServer) settleVMCompletedCleanup(c *localRunControl, job managed.JobSpec, q runner.GuestRequest, out managed.LocalOutcome) error {
	collect, exists := c.phases["collect"]
	cleanup, cleanupExists := c.phases["cleanup"]
	if !exists || !collect.Completed || !collectionAllowsCleanup(job, collect) || !cleanupExists || !controllerOwnerStopped(cleanup) {
		return managed.ErrBlocked
	}
	// This bounded read verifies existing host bytes. It grants no new effect
	// budget and cannot turn a late original cleanup into a timely completion.
	if err := s.verifyVMStored(c.identity.JobID, collect, time.Now().Add(5*time.Second)); err != nil {
		return err
	}
	if c.receipt.JobState == "collecting" {
		if err := s.advancePhase(c, "collecting", "cleaning", "collected"); err != nil {
			return err
		}
	}
	if !cleanup.Completed || cleanup.Outcome == "unproven" {
		if err := s.endPhase(c, "cleanup", managed.LocalPhase{Outcome: "complete"}); err != nil {
			return err
		}
		cleanup = c.phases["cleanup"]
	}
	if c.receipt.JobState == "cleaning" {
		if err := s.advancePhase(c, "cleaning", "settling", "cleaned"); err != nil {
			return err
		}
	}
	result := managed.Result{WorkloadOutcome: out.WorkloadOutcome, NativeExitCode: out.NativeExitCode, NativeSignal: out.NativeSignal, EvidenceCompleteness: collect.EvidenceCompleteness, CleanupOutcome: "verified", Artifacts: collect.Artifacts, Logs: collect.Logs, EvidenceGaps: collect.EvidenceGaps, ProtectedEvidence: cleanup.ProtectedEvidence}
	if cleanup.Outcome != "complete" {
		result.CleanupOutcome, result.Reason = "failed", "cleanup_deadline"
	}
	return s.verifyLocalResult(c, result, false)
}
