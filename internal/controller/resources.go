package controller

import (
	"bytes"
	"io"
	"net/http"

	"github.com/irootkernel/haepari/internal/managed"
)

func (s *runtimeServer) resourceAvailability(w http.ResponseWriter, r *http.Request) {
	op := "resource." + r.PathValue("action")
	if r.PathValue("action") == "update" {
		status, err := s.ledger.ResourceStatus(r.PathValue("id"))
		if err != nil {
			reject(w, op, err)
			return
		}
		if status.Kind == "utm-macos" {
			s.vmResourceUpdate(w, r)
			return
		}
	}
	if r.PathValue("action") == "recover" {
		status, err := s.ledger.ResourceStatus(r.PathValue("id"))
		if err != nil {
			reject(w, op, err)
			return
		}
		if status.Kind != "lock" {
			s.resourceRecovery(w, r)
			return
		}
	}
	if r.PathValue("action") == "update" || r.PathValue("action") == "remove" || r.PathValue("action") == "recover" {
		s.lockResourceAction(w, r)
		return
	}
	// Keep the existing malformed-body/path/action order for managed calls.
	// Retain bounded bytes to enforce the advisory shape once its kind is known.
	defer r.Body.Close()
	const limit = 1<<20 + 4096
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || len(raw) > limit {
		reject(w, op, managed.ErrInvalid)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var q managed.AvailabilityRequest
	if err := decodeBody(r, &q); err != nil {
		reject(w, op, err)
		return
	}
	if q.ResourceID != r.PathValue("id") {
		reject(w, op, managed.ErrInvalid)
		return
	}
	resume := r.PathValue("action") == "resume"
	if !resume && r.PathValue("action") != "pause" {
		reject(w, op, managed.ErrInvalid)
		return
	}
	status, err := s.ledger.ResourceStatus(q.ResourceID)
	if err != nil {
		reject(w, op, err)
		return
	}
	if status.Kind == "lock" {
		if err := decodeLockJSON(raw, &q); err != nil {
			reject(w, op, err)
			return
		}
		updated, err := s.ledger.SetAvailability(q, resume)
		if err != nil {
			reject(w, op, err)
			return
		}
		sendLock(w, op, updated)
		return
	}
	if prior, found, err := s.ledger.ReplayAvailability(q, resume); found || err != nil {
		if err != nil {
			reject(w, op, err)
		} else {
			send(w, 200, op, prior, nil)
		}
		return
	}
	if resume {
		if s.beforeAvailabilityObservation != nil {
			s.beforeAvailabilityObservation()
		}
		status, err := s.ledger.ResourceStatus(q.ResourceID)
		if err != nil {
			reject(w, op, err)
			return
		}
		if status.Kind == "utm-macos" {
			q.ReadinessDigest, q.ObservationReference, err = s.observeVMReadiness(q)
		} else if status.Kind == "local" {
			q.ReadinessDigest, q.ObservationReference, err = s.observeLocalReadiness(q.ResourceID)
		} else {
			err = managed.ErrUnsupported
		}
		if err != nil {
			// Another identical request may have committed while the observation
			// failed. Recover that receipt before reporting a new-operation failure.
			if prior, found, replayErr := s.ledger.ReplayAvailability(q, resume); found || replayErr != nil {
				if replayErr != nil {
					reject(w, op, replayErr)
				} else {
					send(w, 200, op, prior, nil)
				}
				return
			}
			reject(w, op, err)
			return
		}

	}
	status, err = s.ledger.SetAvailability(q, resume)
	if err != nil {
		reject(w, op, err)
		return
	}
	s.notifyScheduler()
	send(w, 200, op, status, nil)
}

func (s *runtimeServer) resourceInspect(w http.ResponseWriter, r *http.Request) {
	status, err := s.ledger.ResourceStatus(r.PathValue("id"))
	if err != nil {
		reject(w, "resource.inspect", err)
		return
	}
	send(w, 200, "resource.inspect", status, nil)
}
