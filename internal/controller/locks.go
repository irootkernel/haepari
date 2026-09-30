package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
)

func lockBorrower(r *http.Request) (*managed.LockGrantReference, error) {
	raw := r.Header.Get(managed.LockGrantHeader)
	if len(r.Header.Values(managed.LockGrantHeader)) > 1 {
		return nil, managed.ErrInvalid
	}
	if raw == "" {
		return nil, nil
	}
	ref, err := managed.DecodeLockGrant(raw)
	if err != nil {
		return nil, err
	}
	return &ref, nil
}

func sendLock(w http.ResponseWriter, op string, data any) {
	raw, err := json.Marshal(response{Schema: "haepari.envelope/v1", Operation: op, OK: true, Data: data})
	if err != nil || len(raw)+1 > managed.MaxEnvelopeBytes {
		reject(w, op, errors.New("advisory response exceeds envelope bound"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(append(raw, '\n'))
}

func (s *runtimeServer) lockAcquire(w http.ResponseWriter, r *http.Request) {
	const op = "lock.acquire"
	var q managed.LockAcquireRequest
	if err := decodeLockBody(r, &q); err != nil {
		reject(w, op, err)
		return
	}
	chain, err := managed.DecodeParentChain(r.Header.Get(managed.ParentContextHeader))
	if err != nil || len(r.Header.Values(managed.ParentContextHeader)) > 1 {
		if err == nil {
			err = managed.ErrInvalid
		}
		reject(w, op, err)
		return
	}
	nested, err := s.ledger.Nested(s.installationID(), chain)
	if err != nil {
		reject(w, op, err)
		return
	}
	if nested {
		reject(w, op, managed.ErrNestedAcquisition)
		return
	}
	borrowed, err := lockBorrower(r)
	if err != nil {
		reject(w, op, err)
		return
	}
	if borrowed != nil {
		if _, err = s.ledger.CheckLockGrant(*borrowed, q.LockID); err == nil {
			err = managed.ErrConflict
		}
		reject(w, op, err)
		return
	}
	reply, err := s.ledger.AcquireLock(q, s.installationID(), chain)
	if err != nil {
		reject(w, op, err)
		return
	}
	sendLock(w, op, reply)
}

func (s *runtimeServer) locksList(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		reject(w, "locks.list", managed.ErrInvalid)
		return
	}
	limit := 50
	for k, v := range q {
		if len(v) != 1 || (k != "lock" && k != "cursor" && k != "limit") {
			reject(w, "locks.list", managed.ErrInvalid)
			return
		}
	}
	if q.Get("lock") != "" && !managed.ValidRequestKey(q.Get("lock")) {
		reject(w, "locks.list", managed.ErrInvalid)
		return
	}
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
	}
	if err != nil || limit < 1 || limit > 100 {
		reject(w, "locks.list", managed.ErrInvalid)
		return
	}
	data, err := s.ledger.LocksPage(q.Get("lock"), q.Get("cursor"), limit)
	if err != nil {
		reject(w, "locks.list", err)
		return
	}
	sendLock(w, "locks.list", data)
}

func (s *runtimeServer) lockWait(w http.ResponseWriter, r *http.Request) {
	const op = "lock.wait"
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		reject(w, op, managed.ErrInvalid)
		return
	}
	for k, v := range q {
		if k != "timeout" || len(v) != 1 {
			reject(w, op, managed.ErrInvalid)
			return
		}
	}
	timeout := 5 * time.Minute
	if q.Has("timeout") {
		timeout, err = time.ParseDuration(q.Get("timeout"))
	}
	if err != nil || timeout <= 0 || timeout > 24*time.Hour || !managed.ValidRequestKey(r.PathValue("id")) {
		reject(w, op, managed.ErrInvalid)
		return
	}
	borrowed, err := lockBorrower(r)
	if err != nil {
		reject(w, op, err)
		return
	}
	if borrowed != nil {
		request, err := s.ledger.InspectLockRequest(r.PathValue("id"))
		if err == nil {
			_, err = s.ledger.CheckLockGrant(*borrowed, request.Current.LockID)
		}
		if err == nil {
			err = managed.ErrConflict
		}
		reject(w, op, err)
		return
	}
	chain, err := managed.DecodeParentChain(r.Header.Get(managed.ParentContextHeader))
	if err != nil || len(r.Header.Values(managed.ParentContextHeader)) > 1 {
		reject(w, op, managed.ErrInvalid)
		return
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		changed := s.ledger.Changes()
		request, err := s.ledger.InspectLockRequest(r.PathValue("id"))
		if err != nil {
			reject(w, op, err)
			return
		}
		if request.Current.State == "queued" {
			nested, err := s.ledger.Nested(s.installationID(), chain)
			if err != nil {
				reject(w, op, err)
				return
			}
			if nested {
				reject(w, op, managed.ErrNestedWait)
				return
			}
		}
		reply, err := s.ledger.ClaimLock(r.PathValue("id"))
		if err != nil {
			reject(w, op, err)
			return
		}
		if reply.Current.State != "queued" {
			sendLock(w, op, reply)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			send(w, http.StatusRequestTimeout, op, nil, &responseError{Code: "WAIT_TIMEOUT", Message: "The caller wait ended; the lock request remains durable.", Retry: "retry_same_key"})
			return
		case <-changed:
		}
	}
}

type lockActionInput struct {
	Force  bool   `json:"force"`
	Reason string `json:"reason,omitempty"`
}

func (s *runtimeServer) lockGrantAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	op := "lock." + action
	var input lockActionInput
	if err := decodeLockBody(r, &input); err != nil {
		reject(w, op, err)
		return
	}
	borrowed, err := lockBorrower(r)
	if err != nil {
		reject(w, op, err)
		return
	}
	var reply managed.LockReply
	switch action {
	case "renew":
		if input.Force || input.Reason != "" {
			reject(w, op, managed.ErrInvalid)
			return
		}
		reply, err = s.ledger.RenewLock(r.PathValue("id"), borrowed)
	case "release":
		reply, err = s.ledger.ReleaseLock(r.PathValue("id"), input.Force, input.Reason, borrowed)
	default:
		err = managed.ErrInvalid
	}
	if err != nil {
		reject(w, op, err)
		return
	}
	sendLock(w, op, reply)
}

func (s *runtimeServer) lockCancel(w http.ResponseWriter, r *http.Request) {
	const op = "lock.cancel"
	var input lockActionInput
	if err := decodeLockBody(r, &input); err != nil {
		reject(w, op, err)
		return
	}
	borrowed, err := lockBorrower(r)
	if err != nil {
		reject(w, op, err)
		return
	}
	reply, err := s.ledger.CancelLock(r.PathValue("id"), input.Force, input.Reason, borrowed)
	if err != nil {
		reject(w, op, err)
		return
	}
	sendLock(w, op, reply)
}
