package controller

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
)

type nativeCompletion struct {
	at      time.Time
	outcome *managed.LocalOutcome
}

// The gate serializes spawn and observed native completion against public
// cancellation. It is never held throughout preparation, collection or cleanup.
type localRunControl struct {
	mu               sync.Mutex
	wake             chan struct{}
	receipt          managed.Receipt
	identity         managed.LocalIdentity
	completionMu     sync.Mutex
	completed        chan struct{}
	completion       *nativeCompletion
	observed         bool
	deadline         time.Time
	vm               *runner.GuestRequest
	runnerRequest    string
	runnerExited     chan struct{}
	effects          bool
	ownsJobRoot      bool
	phases           map[string]managed.LocalPhase
	beforePhase      func(string) // deterministic integration barrier; nil in production
	beforeInputCopy  func() error // stopped-worker fault injection after owned preparation; nil in production
	recoveryEvidence *managed.RecoveryEvidence
}

func (c *localRunControl) phase(name string) {
	if c.beforePhase != nil {
		c.beforePhase(name)
	}
}

func (s *runtimeServer) registerLocal(r managed.Receipt) (*localRunControl, error) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	if s.localRuns == nil {
		s.localRuns = make(map[string]*localRunControl)
	}
	if s.localRuns[r.JobID] != nil {
		return nil, managed.ErrConflict
	}
	c := &localRunControl{wake: make(chan struct{}, 1), completed: make(chan struct{}), receipt: r, identity: managed.LocalIdentity{JobID: r.JobID, ResourceID: r.ResourceID, ConfigRevision: r.ResourceConfigRevision, Slot: r.Slot, LeaseGeneration: r.LeaseGeneration}}
	s.localRuns[r.JobID] = c
	// Registration precedes durable recheck: a commit before registration is
	// found here; a commit afterwards either wakes this handle or is rechecked.
	current, err := s.ledger.Snapshot(r.JobID)
	if err != nil {
		delete(s.localRuns, r.JobID)
		return nil, err
	}
	if current.ResourceID != r.ResourceID || current.ResourceConfigRevision != r.ResourceConfigRevision || current.LeaseGeneration != r.LeaseGeneration || current.Slot != r.Slot {
		delete(s.localRuns, r.JobID)
		return nil, managed.ErrStale
	}
	c.receipt = current
	id, err := s.ledger.LocalIdentity(r.JobID)
	if err != nil {
		delete(s.localRuns, r.JobID)
		return nil, err
	}
	if id.ResourceID != r.ResourceID || id.ConfigRevision != r.ResourceConfigRevision || id.LeaseGeneration != r.LeaseGeneration || id.Slot != r.Slot {
		delete(s.localRuns, r.JobID)
		return nil, managed.ErrStale
	}
	c.identity = id
	return c, nil
}

func (s *runtimeServer) unregisterLocal(c *localRunControl) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	if s.localRuns[c.identity.JobID] == c {
		delete(s.localRuns, c.identity.JobID)
	}
}

func (s *runtimeServer) cancel(w http.ResponseWriter, r *http.Request) {
	var q managed.CancelRequest
	if err := decodeBody(r, &q); err != nil {
		reject(w, "cancel", err)
		return
	}
	if q.JobID == "" {
		q.JobID = r.PathValue("id")
	}
	if q.JobID != r.PathValue("id") {
		reject(w, "cancel", managed.ErrInvalid)
		return
	}
	receipt, err := s.requestCancel(q)
	if err != nil {
		reject(w, "cancel", err)
		return
	}
	send(w, http.StatusOK, "cancel", receipt, nil)
}

func (s *runtimeServer) requestCancel(q managed.CancelRequest) (managed.CancelReceipt, error) {
	s.cancelMu.Lock()
	c := s.localRuns[q.JobID]
	s.cancelMu.Unlock()
	if c != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		// A readable guest outcome gets the same cancellation precedence as a
		// local native outcome. Transport loss cannot prevent recording the
		// cancellation; the original run still needs termination proof.
		if c.vm != nil && c.vm.Root != nil && !c.observed {
			probe := *c.vm
			probe.Operation = "inspect"
			probe.Deadline = time.Now().Add(5 * time.Second)
			if view, err := s.vmCall(probe); err == nil && view.Observation != nil {
				if err = s.importVMView(c, *view.Observation); err != nil {
					return managed.CancelReceipt{}, err
				}
			}
		}
		// Import a native result already published by Wait before deciding the
		// cancellation edge. Main-process exit is not a process-tree proof.
		if err := s.observeLocalCompletion(c); err != nil {
			return managed.CancelReceipt{}, err
		}
	}
	receipt, err := s.ledger.Cancel(q)
	if err != nil {
		return receipt, err
	}
	if c != nil {
		c.receipt = receipt.Receipt
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
	s.notifyScheduler()
	return receipt, nil
}

// Completion publication is first-writer-wins under completionMu. Readers never
// consume a result channel; closing the notification wakes every observer.
func (c *localRunControl) publishCompletion(end nativeCompletion) {
	c.completionMu.Lock()
	defer c.completionMu.Unlock()
	if c.completion == nil {
		c.completion = &end
		close(c.completed)
	}
}
func (c *localRunControl) nativeCompletion() *nativeCompletion {
	c.completionMu.Lock()
	defer c.completionMu.Unlock()
	return c.completion
}

// Called with c.mu held. Native receipt import owns outcome publication.
func (s *runtimeServer) observeLocalCompletion(c *localRunControl) error {
	if c.observed {
		return nil
	}
	if c.runnerRequest != "" {
		if _, err := s.importRunner(c); err != nil {
			return err
		}
	}
	end := c.nativeCompletion()
	if end == nil {
		return nil
	}
	if c.recoveryEvidence != nil {
		if _, err := s.ledger.LocalOutcome(c.identity.JobID); err == nil {
			// A prior controller may have frozen the outcome before its exit.
			// Never replay its mutation key with a newly observed revision.
			c.observed = true
			return nil
		} else if !errors.Is(err, managed.ErrNotFound) {
			return err
		}
	}
	if end.outcome == nil {
		return managed.ErrBlocked
	}
	out := *end.outcome
	key := "outcome-" + c.identity.JobID
	if c.recoveryEvidence != nil {
		key = "recovered-outcome-" + c.identity.JobID
	}
	r, err := s.ledger.RecordLocalOutcome(managed.LocalOutcomeRequest{Identity: c.identity, ExpectedRevision: c.receipt.JobRevision, Outcome: out, MutationKey: key})
	if err != nil {
		return err
	}
	c.receipt = r
	c.observed = true
	return nil
}

// Observe only the durable runner request under a bounded termination budget.
func terminateLocal(c *localRunControl, budget time.Duration) error {
	if budget <= 0 {
		return errors.New("missing termination budget")
	}
	deadline := time.Now().Add(budget)
	if c.runnerRequest != "" {
		if err := runner.Cancel(c.runnerRequest); err != nil {
			return err
		}
		c.phase("termination_wait")
		for time.Now().Before(deadline) {
			view, err := runner.Inspect(c.runnerRequest)
			if err != nil {
				return err
			}
			if view.Exit != nil {
				if view.Exit.TerminationProven {
					return nil
				}
				return runner.ErrUnproven
			}
			select {
			case <-c.runnerExited:
				// Publication may race the preceding read; reread after native
				// supervisor exit before declaring the receipt missing.
				view, err := runner.Inspect(c.runnerRequest)
				if err == nil && view.Exit != nil && view.Exit.TerminationProven {
					return nil
				}
				return runner.ErrUnproven
			case <-time.After(25 * time.Millisecond):
			}
		}
		return runner.ErrUnproven
	}
	return errors.New("missing durable runner request")
}

// Called only after the preparation worker stopped, or before it was allowed
// any filesystem effect. Never called for an ambiguous preparation timeout.
func (s *runtimeServer) settleCancelledPreparation(c *localRunControl, job managed.JobSpec, jobRoot, workspace string) error {
	return s.finishPreparation(c, job, jobRoot, workspace, "cancel_before_start")
}
