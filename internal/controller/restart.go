package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/runner"
	"golang.org/x/sys/unix"
)

func recoveryObservation(mechanism string, value any, ref string) managed.RecoveryEvidence {
	return managed.RecoveryEvidence{Source: "machine-observed", Observer: "haepari-controller", ObservedAt: time.Now().UTC(), Mechanism: mechanism, Digest: managed.RecoveryDigest(value), Reference: ref}
}

func controllerOwnerStopped(p managed.LocalPhase) bool {
	if p.Completed && p.Outcome != "unproven" {
		return true
	}
	if p.ControllerOwner == nil {
		return false
	}
	boot, err := platform.BootIdentity()
	if err != nil {
		return false
	}
	if boot != p.ControllerOwner.BootID {
		return true
	}
	current, err := platform.ObserveProcess(p.ControllerOwner.PID)
	return errors.Is(err, unix.ESRCH) || err == nil && current.Identity != *p.ControllerOwner
}

// Attestation resolves missing proof; it cannot contradict an observed owner
// that can still execute. A proven terminal receipt permits a supervisor to
// remain alive briefly while publishing its completed observation.
func recoveryOwnerAlive(snapshot managed.RecoverySnapshot, view runner.Observation, receiptsValid bool) bool {
	live := func(id platform.ProcessIdentity) bool {
		observed, err := platform.ObserveProcess(id.PID)
		return err == nil && !observed.Zombie && observed.Identity == id
	}
	for _, name := range []string{"prepare", "collect", "cleanup"} {
		if p, ok := snapshot.Phases[name]; ok && (!p.Completed || p.Outcome == "unproven") && p.ControllerOwner != nil && live(*p.ControllerOwner) {
			return true
		}
	}
	if snapshot.Stopped && snapshot.Attestation == nil || receiptsValid && view.Exit != nil && view.Exit.TerminationProven {
		return false
	}
	if receiptsValid && view.Intent != nil && live(view.Intent.Supervisor) {
		return true
	}
	// A lost native file cannot erase an already imported execution owner.
	var request runner.Request
	var intent runner.Intent
	if json.Unmarshal(snapshot.Runner["request"], &request) == nil && request.Identity == snapshot.Identity &&
		json.Unmarshal(snapshot.Runner["intent"], &intent) == nil && intent.Schema == "haepari.runner-intent/v1" && intent.Binding == request.Binding() {
		return live(intent.Supervisor)
	}
	return false
}

// Input ownership was published before the run claim, so its run field is
// intentionally empty. Every other lease fence and sealed object must match.
func (s *runtimeServer) inspectRecoveryRoot(id managed.LocalIdentity) (bool, error) {
	fd, err := openPrivateJob(s.root.Path, id.JobID)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, errLogMissing) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer unix.Close(fd)
	input, err := unix.Openat(fd, "input.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	f := os.NewFile(uintptr(input), "input.json")
	defer f.Close()
	if _, err := privateLogStat(input, false); err != nil {
		return false, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 || managed.ValidateUniqueJSONKeys(raw) != nil {
		return false, managed.ErrBlocked
	}
	var receipt struct {
		Identity managed.LocalIdentity `json:"identity"`
		ObjectID string                `json:"snapshot_object_id"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&receipt) != nil {
		return false, managed.ErrBlocked
	}
	var tail any
	if d.Decode(&tail) != io.EOF {
		return false, managed.ErrBlocked
	}
	expected := id
	expected.RunID = ""
	object, err := s.ledger.Input(id.JobID)
	if err != nil || receipt.Identity != expected || receipt.ObjectID != object {
		return false, managed.ErrBlocked
	}
	return true, nil
}

func (s *runtimeServer) inspectRecoveryRunner(snapshot managed.RecoverySnapshot) (runner.Request, runner.Observation, error) {
	path := filepath.Join(s.root.Path, "jobs", snapshot.Identity.JobID, "request.json")
	q, err := runner.ReadRequest(path)
	if err != nil {
		return q, runner.Observation{}, err
	}
	if q.Identity != snapshot.Identity {
		return q, runner.Observation{}, managed.ErrStale
	}
	raw, err := json.Marshal(q)
	if err != nil {
		return q, runner.Observation{}, err
	}
	if !bytes.Equal(raw, snapshot.Runner["request"]) {
		return q, runner.Observation{}, managed.ErrConflict
	}
	view, err := runner.Inspect(path)
	return q, view, err
}

func (s *runtimeServer) reconcileStartup() error {
	jobs, err := s.ledger.InterruptedJobs()
	if err != nil {
		return err
	}
	for _, r := range jobs {
		// A malformed or uncertain native observation keeps this Job blocked;
		// unrelated resources still reach the scheduler under normal capacity.
		_ = s.tryRecoverInterrupted(r.JobID, true, nil)
	}
	resources, err := s.ledger.RecoveringResources()
	if err != nil {
		return err
	}
	for _, id := range resources {
		// Only the original committed, fence-matching health-check intent
		// resumes. No disposal or filesystem effect is inferred here.
		_ = s.resumeResourceRecovery(id)
	}
	return nil
}

func (s *runtimeServer) tryRecoverInterrupted(jobID string, automatic bool, attestation *managed.RecoveryEvidence) error {
	return s.recoverInterrupted(jobID, automatic, attestation, nil, "", "")
}

func (s *runtimeServer) applyPlannedJobRecovery(plan jobRecoveryPlan, attestation *managed.RecoveryEvidence, key, digest string) error {
	return s.recoverInterrupted(plan.Snapshot.Receipt.JobID, false, attestation, &plan, key, digest)
}

func (s *runtimeServer) recoverInterrupted(jobID string, automatic bool, attestation *managed.RecoveryEvidence, plan *jobRecoveryPlan, key, clientDigest string) error {
	snapshot, err := s.ledger.RecoverySnapshot(jobID)
	if err != nil {
		return err
	}
	if plan != nil {
		fresh, err := s.planJobRecovery(jobID, plan.Action)
		if err != nil {
			return err
		}
		if managed.RecoveryDigest(fresh.Snapshot) != managed.RecoveryDigest(plan.Snapshot) || managed.RecoveryDigest(fresh.Observation) != managed.RecoveryDigest(plan.Observation) {
			return managed.ErrStale
		}
	}
	if snapshot.Spec.Mode == "manual" {
		m, err := s.ledger.ManualSession(jobID)
		if err == nil && m.Outcome == nil && attestation == nil && snapshot.Attestation == nil {
			return s.recoverManual(snapshot, m, automatic, key, clientDigest)
		}
		if err != nil && !errors.Is(err, managed.ErrNotFound) {
			return err
		}
		if err != nil && snapshot.InterruptedFrom == "awaiting_manual" {
			return managed.ErrBlocked
		}
	}
	if snapshot.Resource.Kind == "utm-macos" {
		return s.recoverVMInterrupted(snapshot, automatic, attestation, plan, key, clientDigest)
	}
	if snapshot.Receipt.JobState != "reconciling" || snapshot.Resource.Kind != "local" || len(snapshot.AdapterIntents) != 0 {
		return managed.ErrBlocked
	}
	if s.jobWorkerAlive(jobID) {
		return managed.ErrBlocked
	}
	now := time.Now().UTC()
	for _, p := range snapshot.Phases {
		if now.Before(p.ObservedAt) {
			if err := s.ledger.RecordRecoveryClockDiscontinuity(snapshot.Identity, managed.RecoveryDigest(snapshot), now); err != nil {
				return err
			}
			return managed.ErrBlocked
		}
	}
	owned, err := s.inspectRecoveryRoot(snapshot.Identity)
	if err != nil {
		return err
	}
	for _, name := range []string{"prepare", "collect", "cleanup"} {
		if p, ok := snapshot.Phases[name]; ok && !controllerOwnerStopped(p) && attestation == nil {
			return managed.ErrBlocked
		}
	}
	q := managed.JobRecoveryRequest{Identity: snapshot.Identity, SnapshotDigest: managed.RecoveryDigest(snapshot), MutationKey: "restart-" + snapshot.EventsDigest[7:39], Automatic: automatic}
	if key != "" {
		q.MutationKey, q.ClientDigest = key, clientDigest
	}
	q.Evidence = recoveryObservation("durable-local-receipts", snapshot, "job:"+jobID)
	var request runner.Request
	var view runner.Observation
	if snapshot.Identity.RunID != "" {
		request, view, err = s.inspectRecoveryRunner(snapshot)
		if err != nil && attestation == nil {
			return err
		}
	}
	if (attestation != nil || snapshot.Attestation != nil) && recoveryOwnerAlive(snapshot, view, err == nil) {
		return managed.ErrBlocked
	}
	switch {
	case snapshot.Stopped:
		q.Action, q.NextState, q.NoOwnedEffect = "resume", "collecting", snapshot.NoOwnedEffect
		if snapshot.NoOwnedEffect {
			q.NextState = "settling"
		} else if p, ok := snapshot.Phases["collect"]; ok && p.Completed && p.Outcome != "unproven" {
			q.NextState = "cleaning"
			if p, ok := snapshot.Phases["cleanup"]; ok && p.Completed && p.Outcome != "unproven" {
				q.NextState = "settling"
			}
		}
	case view.Exit != nil && view.Exit.TerminationProven:
		q.Action, q.NextState, q.Outcome = "stopped", "collecting", &view.Exit.Outcome
		if snapshot.Outcome != nil {
			q.Outcome = snapshot.Outcome
		}
	case view.Intent != nil && attestation == nil:
		live, e := platform.ObserveProcess(view.Intent.Supervisor.PID)
		if e != nil || live.Identity != view.Intent.Supervisor || live.Zombie {
			return managed.ErrBlocked
		}
		if _, ok := snapshot.Phases["run"]; !ok {
			return managed.ErrBlocked
		}
		q.Action, q.NextState = "reattach", "running"
		if snapshot.DeadlineRequested || snapshot.InterruptedFrom == "cancelling" {
			q.NextState = "cancelling"
		}
		q.Evidence = recoveryObservation("native-supervisor-incarnation", live.Identity, "job:"+jobID)
	case snapshot.Identity.RunID == "" && attestation == nil:
		p, ok := snapshot.Phases["prepare"]
		if !ok || !controllerOwnerStopped(p) {
			return managed.ErrBlocked
		}
		out := managed.LocalOutcome{WorkloadOutcome: "inconclusive"}
		q.Action, q.NextState, q.Outcome, q.NoOwnedEffect = "no-start", "collecting", &out, !owned
		if !owned {
			q.NextState = "settling"
		}
	case attestation != nil:
		q.Evidence = *attestation
		out := managed.LocalOutcome{Started: snapshot.Identity.RunID != "", WorkloadOutcome: "inconclusive"}
		if snapshot.Outcome != nil {
			out = *snapshot.Outcome
		}
		q.Action, q.NextState, q.Outcome = "stopped", "collecting", &out
		if plan != nil && plan.Action == "attest-no-start" {
			if view.Start != nil || snapshot.Outcome != nil && snapshot.Outcome.Started {
				return managed.ErrConflict
			}
			out.Started = false
			q.Action, q.NoOwnedEffect = "no-start", !owned
			if !owned {
				q.NextState = "settling"
			}
		}
	default:
		return managed.ErrBlocked
	}
	if attestation != nil {
		q.Evidence = *attestation
		q.Automatic = false
	}
	if snapshot.Attestation != nil && attestation == nil {
		q.Evidence = *snapshot.Attestation
		q.Automatic = false
	}
	if !owned && !q.NoOwnedEffect {
		return managed.ErrBlocked
	}
	// Import only the exact closed native records before selecting a fresh
	// snapshot. Missing later receipts are never reconstructed from a PID.
	if snapshot.Identity.RunID != "" && err == nil {
		for _, item := range []struct {
			kind    string
			value   any
			present bool
		}{
			{"intent", view.Intent, view.Intent != nil}, {"start", view.Start, view.Start != nil}, {"outcome", view.Outcome, view.Outcome != nil}, {"exit", view.Exit, view.Exit != nil},
		} {
			if item.present {
				raw, e := json.Marshal(item.value)
				if e != nil {
					return e
				}
				if e = s.ledger.RecordRunnerReceipt(snapshot.Identity, item.kind, raw); e != nil {
					return e
				}
			}
		}
		before := snapshot
		snapshot, err = s.ledger.RecoverySnapshot(jobID)
		if err != nil {
			return err
		}
		if snapshot.Receipt.JobRevision != before.Receipt.JobRevision || snapshot.Resource.AvailabilityRevision != before.Resource.AvailabilityRevision || snapshot.Resource.ConfigRevision != before.Resource.ConfigRevision || snapshot.Resource.LeaseGeneration != before.Resource.LeaseGeneration {
			return managed.ErrStale
		}
		q.SnapshotDigest = managed.RecoveryDigest(snapshot)
	}
	c, err := s.registerLocal(snapshot.Receipt)
	if err != nil {
		return err
	}
	c.recoveryEvidence = &q.Evidence
	c.phases = snapshot.Phases
	c.effects, c.ownsJobRoot = owned, owned
	c.deadline = request.Deadline
	if snapshot.Identity.RunID != "" && view.Intent != nil {
		c.runnerRequest = filepath.Join(s.root.Path, "jobs", jobID, "request.json")
	}
	r, err := s.ledger.BeginJobRecovery(q)
	if err != nil {
		s.unregisterLocal(c)
		return err
	}
	c.receipt = r
	if !s.startWorker(func() {
		defer s.unregisterLocal(c)
		if err := s.resumeLocal(c, snapshot.Spec, q.Action, q.NoOwnedEffect); err != nil && !errors.Is(err, errControllerDetached) {
			s.failed(jobID, err)
		}
		s.notifyScheduler()
	}) {
		s.unregisterLocal(c)
		return errControllerDetached
	}
	return nil
}

func (s *runtimeServer) resumeLocal(c *localRunControl, job managed.JobSpec, action string, noEffect bool) error {
	root := filepath.Join(s.root.Path, "jobs", c.identity.JobID)
	work := filepath.Join(root, "workspace")
	if action == "reattach" {
		log, _, _, err := openPrivateLog(s.root.Path, c.identity.JobID, c.identity.RunID)
		if err != nil {
			return err
		}
		defer log.Close()
		_, _, err = s.watchSupervisor(c, job, log, c.deadline)
		if err != nil {
			return err
		}
		c.mu.Lock()
		r, err := s.ledger.CompleteLocalRun(managed.LocalTerminationRequest{Identity: c.identity, ExpectedRevision: c.receipt.JobRevision, MutationKey: "recovered-termination-" + c.identity.JobID})
		if err == nil {
			c.receipt = r
		}
		c.mu.Unlock()
		if err != nil {
			return err
		}
	}
	for _, name := range []string{"prepare", "run"} {
		if p, ok := c.phases[name]; ok && (!p.Completed || p.Outcome == "unproven") {
			outcome := "failed"
			if name == "run" {
				outcome = "complete"
			}
			if err := s.endPhase(c, name, managed.LocalPhase{Outcome: outcome}); err != nil {
				return err
			}
		}
	}
	out, err := s.ledger.LocalOutcome(c.identity.JobID)
	if err != nil {
		return err
	}
	reason := ""
	if !out.Started {
		if out.WorkloadOutcome == "cancelled" {
			reason = "cancel_before_start"
		} else if c.identity.RunID != "" {
			reason = "process_start_failed"
		} else if c.phases["prepare"].Outcome == "late" {
			reason = "prepare_deadline"
		} else {
			reason = "prepare_failed"
		}
	}
	return s.settleLocalPhases(c, job, root, work, out, noEffect, reason, nil)
}
