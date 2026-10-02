package controller

import (
	"errors"
	"time"
)

var errControllerDetached = errors.New("controller observation detached; managed execution remains owned")

func (s *runtimeServer) startWorker(work func()) bool {
	return s.startJobWorker("", work)
}

// Resumed effects keep the original durable intent, but their current worker
// must remain protected until it actually stops. Run keeps the database and
// controller lock while these workers live, including after listener shutdown.
func (s *runtimeServer) startJobWorker(jobID string, work func()) bool {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	if s.closing {
		return false
	}
	s.workers.Add(1)
	if jobID != "" {
		if s.jobWorkers == nil {
			s.jobWorkers = make(map[string]int)
		}
		s.jobWorkers[jobID]++
	}
	go func() {
		defer s.workers.Done()
		if jobID != "" {
			defer func() {
				s.workerMu.Lock()
				defer s.workerMu.Unlock()
				s.jobWorkers[jobID]--
				if s.jobWorkers[jobID] == 0 {
					delete(s.jobWorkers, jobID)
				}
			}()
		}
		work()
	}()
	return true
}

func (s *runtimeServer) jobWorkerAlive(jobID string) bool {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	return s.jobWorkers[jobID] != 0
}

// The controller lock and database remain owned while any in-process effect
// can continue. A released listener alone never proves a collector has stopped.
func (s *runtimeServer) detachWorkers() {
	s.workerMu.Lock()
	s.closing = true
	close(s.detached)
	s.workerMu.Unlock()
}

func boundedOwnedPhase[T any](s *runtimeServer, deadline time.Time, work func() T) (T, phaseStatus) {
	return boundedPhaseWithWorker(deadline, s.detached, s.startWorker, work)
}

func boundedPhaseWithWorker[T any](deadline time.Time, detached <-chan struct{}, start func(func()) bool, work func() T) (T, phaseStatus) {
	var zero T
	if !time.Now().Before(deadline) {
		return zero, phaseLate
	}
	done := make(chan phaseCompletion[T], 1)
	if !start(func() { done <- phaseCompletion[T]{work(), time.Now()} }) {
		return zero, phaseUnproven
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case end := <-done:
		return end.value, completionStatus(end.finishedAt, deadline)
	case <-detached:
		return zero, phaseUnproven
	case <-timer.C:
		select {
		case end := <-done:
			return end.value, completionStatus(end.finishedAt, deadline)
		default:
			return zero, phaseUnproven
		}
	}
}
