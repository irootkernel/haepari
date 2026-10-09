package controller

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/utm"
)

func diagnosticCause(err error) string {
	if errors.Is(err, utm.ErrGuestRefused) {
		return runner.DiagnosticCause(err)
	}
	if cause := runner.DiagnosticCause(err); cause != managed.DiagnosticObservationFailed {
		return cause
	}
	switch {
	case errors.Is(err, managed.ErrStale):
		return managed.DiagnosticStaleRevision
	case errors.Is(err, utm.ErrGuestUncertain):
		return managed.DiagnosticGuestEffectUncertain
	case errors.Is(err, utm.ErrUnavailable):
		return managed.DiagnosticTransportUnavailable
	default:
		return runner.DiagnosticCause(err)
	}
}

func (s *runtimeServer) recordDiagnosticFailure(jobID string, err error) {
	_ = s.ledger.RecordDiagnosticCause(jobID, diagnosticCause(err))
	// A joined deadline or native error must not hide uncertain guest effects.
	if errors.Is(err, utm.ErrGuestUncertain) {
		_ = s.ledger.RecordDiagnosticCause(jobID, managed.DiagnosticGuestEffectUncertain)
	}
}

func safeObservation(op string) bool {
	switch op {
	case "identity", "inspect", "watch", "state", "root", "file", "output", "health":
		return true
	}
	return false
}
func (s *runtimeServer) observeVMWithRetry(q runner.GuestRequest) (runner.GuestReply, error) {
	return retryObservation(q, s.detached, s.vmCall, func(err error) {
		if q.Request.Identity.JobID != "" {
			s.recordDiagnosticFailure(q.Request.Identity.JobID, err)
		}
	})
}
func retryObservation(q runner.GuestRequest, detached <-chan struct{}, call func(runner.GuestRequest) (runner.GuestReply, error), diagnose func(error)) (runner.GuestReply, error) {
	backoff := 100 * time.Millisecond
	var lastErr error
	// A failed observation gets a small bounded burst. Exhausting the entire
	// phase here would leave no budget for cessation proof or reconciliation.
	for attempt := 1; ; attempt++ {
		if !time.Now().Before(q.Deadline) {
			return runner.GuestReply{}, errors.Join(context.DeadlineExceeded, lastErr)
		}
		select {
		case <-detached:
			return runner.GuestReply{}, errControllerDetached
		default:
		}
		reply, err := call(q)
		if err == nil {
			return reply, nil
		}
		lastErr = err
		if diagnose != nil {
			diagnose(err)
		}
		if attempt == 3 || !safeObservation(q.Operation) || errors.Is(err, utm.ErrGuestRefused) || errors.Is(err, managed.ErrStale) || errors.Is(err, runner.ErrConflict) || errors.Is(err, managed.ErrInvalid) || errors.Is(err, runner.ErrInvalid) || errors.Is(err, os.ErrPermission) {
			return reply, err
		}
		wait := backoff
		if remaining := time.Until(q.Deadline); remaining < wait {
			wait = remaining
		}
		if wait <= 0 {
			return reply, err
		}
		timer := time.NewTimer(wait)
		select {
		case <-detached:
			timer.Stop()
			return runner.GuestReply{}, errControllerDetached
		case <-timer.C:
		}
		backoff *= 2
	}
}

// Periodic reconciliation is serialized separately from queue scheduling. It
// only revisits interrupted Jobs through the same fenced recovery path and
// never renews an admitted phase or authorizes another workload start.
func (s *runtimeServer) reconcilePeriodically(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.detached:
			return
		case <-ticker.C:
		}
		jobs, err := s.ledger.InterruptedJobs()
		if err != nil {
			continue
		}
		for _, job := range jobs {
			select {
			case <-ctx.Done():
				return
			case <-s.detached:
				return
			default:
			}
			if s.jobWorkerAlive(job.JobID) {
				continue
			}
			if err := s.tryRecoverInterrupted(job.JobID, true, nil); err != nil {
				s.recordDiagnosticFailure(job.JobID, err)
			}
		}
		s.notifyScheduler()
	}
}
