package controller

import (
	"context"
	"time"
)

// Native wake is sampled inside every lock transaction. A bounded marker check
// also notices wake without a connected caller; deadline timers and committed
// ledger notifications determine earlier work.
func (s *runtimeServer) scheduleLocks(ctx context.Context) {
	for ctx.Err() == nil {
		changed := s.ledger.Changes()
		err := s.ledger.SweepLocks()
		delay := time.Second
		if err == nil {
			if due, exists, e := s.ledger.NearestLockDeadline(); e == nil && exists {
				remaining := time.Until(due)
				if remaining < 0 {
					remaining = 0
				}
				if remaining < delay {
					delay = remaining
				}
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}
