package controller

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/runner"
)

// Manual recovery observes the sole original preparation. It never starts a
// second setup/smoke sequence or converts its native success into human PASS.
func (s *runtimeServer) recoverManual(snapshot managed.RecoverySnapshot, m managed.ManualSession, automatic bool, key, clientDigest string) error {
	if snapshot.Receipt.JobState != "reconciling" || m.Legacy || m.Identity != snapshot.Identity || s.jobWorkerAlive(m.Identity.JobID) {
		return managed.ErrBlocked
	}
	for _, phase := range snapshot.Phases {
		if time.Now().Before(phase.ObservedAt) || !controllerOwnerStopped(phase) {
			return managed.ErrBlocked
		}
	}
	if len(snapshot.AdapterIntents) > 0 {
		return managed.ErrBlocked
	}
	var request runner.Request
	var view runner.Observation
	var guest *runner.GuestRequest
	var guestSupervisorAlive *bool
	workspace := filepath.Join(s.root.Path, "jobs", m.Identity.JobID, "workspace")
	if snapshot.Resource.Kind == "utm-macos" {
		q, facts, err := s.observeVMRecovery(snapshot)
		if err != nil || facts.View == nil || facts.PartialPreparation || !facts.WorkerStopped {
			return errors.Join(managed.ErrBlocked, err)
		}
		request = q.Request
		view = *facts.View
		guest = &q
		guestSupervisorAlive = facts.SupervisorAlive
		workspace = filepath.Join(q.JobRoot, m.Identity.JobID, "workspace")
	} else if snapshot.Resource.Kind == "local" {
		owned, err := s.inspectRecoveryRoot(m.Identity)
		if err != nil || !owned {
			return errors.Join(managed.ErrBlocked, err)
		}
		request, view, err = s.inspectRecoveryRunner(snapshot)
		if err != nil {
			return err
		}
		if managed.AbsenceAware(snapshot.Spec) {
			if _, err := s.verifyLocalWorkspace(m.Identity, workspace); err != nil {
				return err
			}
		}
	} else {
		return managed.ErrUnsupported
	}
	if request.Identity != m.Identity || !request.Deadline.Equal(m.PrepareDeadline) {
		return managed.ErrStale
	}
	if view.Exit == nil || !view.Exit.TerminationProven {
		if view.Intent == nil {
			return managed.ErrBlocked
		}
		if guest != nil {
			if guestSupervisorAlive == nil || !*guestSupervisorAlive {
				return managed.ErrBlocked
			}
		} else {
			owner, err := platform.ObserveProcess(view.Intent.Supervisor.PID)
			if err != nil || owner.Zombie || owner.Identity != view.Intent.Supervisor {
				return managed.ErrBlocked
			}
		}
	}
	for _, item := range []struct {
		kind    string
		value   any
		present bool
	}{
		{"intent", view.Intent, view.Intent != nil}, {"start", view.Start, view.Start != nil}, {"outcome", view.Outcome, view.Outcome != nil}, {"exit", view.Exit, view.Exit != nil},
	} {
		if item.present {
			raw, _ := json.Marshal(item.value)
			if err := s.ledger.RecordRunnerReceipt(m.Identity, item.kind, raw); err != nil {
				return err
			}
		}
	}
	fresh, err := s.ledger.RecoverySnapshot(m.Identity.JobID)
	if err != nil {
		return err
	}
	if fresh.Receipt.JobRevision != snapshot.Receipt.JobRevision || fresh.Identity != snapshot.Identity || fresh.Resource.ConfigRevision != snapshot.Resource.ConfigRevision || fresh.Resource.AvailabilityRevision != snapshot.Resource.AvailabilityRevision {
		return managed.ErrStale
	}
	c, err := s.registerLocal(fresh.Receipt)
	if err != nil {
		return err
	}
	next := "preparing"
	if !m.Deadline.IsZero() {
		next = "awaiting_manual"
	}
	evidence := recoveryObservation("original-manual-preparation", map[string]any{"session": m, "request": request, "receipts": view}, "job:"+m.Identity.JobID)
	q := managed.JobRecoveryRequest{Identity: m.Identity, SnapshotDigest: managed.RecoveryDigest(fresh), MutationKey: "manual-restart-" + fresh.EventsDigest[7:39], Automatic: automatic, Action: "reattach", NextState: next, Evidence: evidence}
	if key != "" {
		q.MutationKey, q.ClientDigest = key, clientDigest
	}
	r, err := s.ledger.BeginJobRecovery(q)
	if err != nil {
		s.unregisterLocal(c)
		return err
	}
	c.receipt = r
	c.phases = fresh.Phases
	c.manualSteps = append([]managed.ResultStep(nil), m.PreparationSteps...)
	c.effects = true
	c.ownsJobRoot = true
	c.deadline = request.Deadline
	c.recoveryEvidence = &evidence
	if guest != nil {
		c.vm = guest
	} else {
		c.runnerRequest = filepath.Join(s.root.Path, "jobs", m.Identity.JobID, "request.json")
	}
	if !s.startWorker(func() {
		defer s.unregisterLocal(c)
		var err error
		if next == "awaiting_manual" {
			err = s.awaitManual(c, fresh.Spec, workspace, guest)
		} else if view.Exit != nil && view.Exit.TerminationProven {
			// The already verified original stop needs no new watch/cancel
			// effects, even when recovery occurs after the prepare budget.
			err = s.finishManualPreparation(c, fresh.Spec, workspace, view.Exit.Outcome, guest, c.manualSteps)
		} else if guest != nil {
			err = s.watchVM(c, fresh.Spec, *guest, m.PrepareDeadline)
		} else {
			log, _, _, e := openPrivateLog(s.root.Path, m.Identity.JobID, m.Identity.RunID)
			if e != nil {
				err = e
			} else {
				_, _, err = s.watchSupervisor(c, fresh.Spec, log, m.PrepareDeadline)
				log.Close()
				if err == nil {
					v, e := runner.Inspect(c.runnerRequest)
					if e != nil || v.Exit == nil || !v.Exit.TerminationProven {
						err = errors.Join(runner.ErrUnproven, e)
					} else {
						err = s.finishManualPreparation(c, fresh.Spec, workspace, v.Exit.Outcome, nil, nil)
					}
				}
			}
		}
		if err != nil && !errors.Is(err, errControllerDetached) {
			s.failed(m.Identity.JobID, err)
		}
		s.notifyScheduler()
	}) {
		s.unregisterLocal(c)
		return errControllerDetached
	}
	return nil
}
