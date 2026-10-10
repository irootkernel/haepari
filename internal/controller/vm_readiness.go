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

var errVMReadinessCancelled = errors.New("VM readiness cancelled before guest claim")

func (s *runtimeServer) vmPreflightState(c *localRunControl) error {
	select {
	case <-s.detached:
		return errControllerDetached
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	id, err := s.ledger.LocalIdentity(c.identity.JobID)
	if err != nil {
		return err
	}
	if id != c.identity || id.RunID != "" || c.effects {
		return managed.ErrStale
	}
	current, err := s.ledger.Snapshot(id.JobID)
	if err != nil {
		return err
	}
	if current.ResourceConfigRevision != id.ConfigRevision || current.LeaseGeneration != id.LeaseGeneration || current.Slot != id.Slot {
		return managed.ErrStale
	}
	c.receipt = current
	status, err := s.ledger.Cancellation(id.JobID)
	if err != nil {
		return err
	}
	if status.Requested {
		return errVMReadinessCancelled
	}
	if current.JobState != "preparing" {
		return managed.ErrConflict
	}
	return nil
}

func (s *runtimeServer) waitVMPreparation(c *localRunControl, deadline time.Time, wait time.Duration) error {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.DeadlineExceeded
	}
	timer := time.NewTimer(min(wait, remaining))
	defer timer.Stop()
	select {
	case <-s.detached:
		return errControllerDetached
	case <-c.wake:
	case <-timer.C:
	}
	return nil
}

func (s *runtimeServer) waitVMReadiness(c *localRunControl, q runner.GuestRequest) (runner.GuestReply, error) {
	var last error
	backoff := 100 * time.Millisecond
	for time.Now().Before(q.Deadline) {
		if err := s.vmPreflightState(c); err != nil {
			return runner.GuestReply{}, err
		}
		r, err := s.vmCall(q)
		if err == nil {
			if err = s.vmPreflightState(c); err != nil {
				return runner.GuestReply{}, err
			}
			return r, nil
		}
		last = err
		var failure *utm.CommandFailure
		if errors.As(err, &failure) && failure.HostPermissionDenied || errors.Is(err, utm.ErrGuestRefused) || errors.Is(err, managed.ErrStale) || errors.Is(err, runner.ErrConflict) || errors.Is(err, runner.ErrInvalid) || errors.Is(err, managed.ErrInvalid) || errors.Is(err, os.ErrPermission) {
			return r, err
		}
		if err = s.waitVMPreparation(c, q.Deadline, backoff); err != nil {
			return r, errors.Join(err, last)
		}
		backoff = min(backoff*2, time.Second)
	}
	return runner.GuestReply{}, errors.Join(context.DeadlineExceeded, last)
}
