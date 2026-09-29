package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
)

const protocol = "managed-execution/v1"

type runtimeServer struct {
	root    state.Root
	ledger  *managed.Ledger
	store   *snapshot.Store
	wake    chan struct{}
	mu      sync.Mutex
	waiters map[string][]chan struct{}
}

func newRuntime(root state.Root, ledger *managed.Ledger, store *snapshot.Store) *runtimeServer {
	return &runtimeServer{root: root, ledger: ledger, store: store, wake: make(chan struct{}, 1), waiters: make(map[string][]chan struct{})}
}

type response struct {
	Schema    string         `json:"schema"`
	Operation string         `json:"operation"`
	OK        bool           `json:"ok"`
	Data      any            `json:"data,omitempty"`
	Error     *responseError `json:"error,omitempty"`
}
type responseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Retry   string `json:"retry"`
}

func send(w http.ResponseWriter, status int, operation string, data any, issue *responseError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response{Schema: "haepari.envelope/v1", Operation: operation, OK: issue == nil, Data: data, Error: issue})
}
func reject(w http.ResponseWriter, op string, err error) {
	code, status, retry := "INVALID_REQUEST", http.StatusBadRequest, "none"
	switch {
	case errors.Is(err, managed.ErrConflict):
		code = "CONFLICT"
	case errors.Is(err, managed.ErrNotFound):
		code = "NOT_FOUND"
		status = http.StatusNotFound
	case errors.Is(err, managed.ErrBusy):
		code = "RESOURCE_BUSY"
		status = http.StatusConflict
	case errors.Is(err, managed.ErrUnsupported):
		code = "UNSUPPORTED"
	case errors.Is(err, managed.ErrStale):
		code = "STALE_REVISION"
		status = http.StatusConflict
	case errors.Is(err, managed.ErrInvalid), errors.Is(err, snapshot.ErrInvalid), errors.Is(err, snapshot.ErrChanged):
		code = "INVALID_REQUEST"
	default:
		code = "STORAGE_UNAVAILABLE"
		status = http.StatusServiceUnavailable
		retry = "reconcile"
	}
	send(w, status, op, nil, &responseError{Code: code, Message: code, Retry: retry})
}
func (s *runtimeServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/hello", func(w http.ResponseWriter, r *http.Request) {
		sum := sha256.Sum256([]byte(s.root.Path))
		send(w, 200, "hello", map[string]any{"protocol": protocol, "installation_id": "local-" + hex.EncodeToString(sum[:8])}, nil)
	})
	mux.HandleFunc("POST /v1/resources", s.register)
	mux.HandleFunc("POST /v1/jobs", s.submit)
	mux.HandleFunc("GET /v1/jobs/{id}", s.inspect)
	mux.HandleFunc("GET /v1/jobs/{id}/wait", s.wait)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/hello" && r.Header.Get("X-Haepari-Protocol") != protocol {
			send(w, http.StatusBadRequest, "handshake", nil, &responseError{Code: "PROTOCOL_UNSUPPORTED", Message: "protocol version mismatch", Retry: "none"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func decodeBody(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return managed.ErrInvalid
	}
	var tail any
	if err := decoder.Decode(&tail); err != io.EOF {
		return managed.ErrInvalid
	}
	return nil
}
func (s *runtimeServer) register(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Spec json.RawMessage `json:"spec"`
	}
	if err := decodeBody(r, &input); err != nil {
		reject(w, "resource.register", err)
		return
	}
	spec, err := managed.DecodeResource(input.Spec)
	if err != nil {
		reject(w, "resource.register", err)
		return
	}
	if spec.Kind != "local" || spec.Capacity != 1 {
		reject(w, "resource.register", managed.ErrUnsupported)
		return
	}
	if err := s.ledger.RegisterOnlyLocal(input.Spec); err != nil {
		reject(w, "resource.register", err)
		return
	}
	s.notifyScheduler()
	send(w, 200, "resource.register", map[string]any{"resource_id": spec.ID, "revision": spec.Revision}, nil)
}
func (s *runtimeServer) submit(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Spec       json.RawMessage `json:"spec"`
		RequestKey string          `json:"request_key"`
	}
	if err := decodeBody(r, &input); err != nil {
		reject(w, "submit", err)
		return
	}
	job, err := managed.DecodeJob(input.Spec)
	if err != nil {
		reject(w, "submit", err)
		return
	}
	if job.Mode != "auto" || job.Provenance.EvidenceClass != "development" || len(job.Input.SelectedFiles) == 0 || input.RequestKey == "" {
		reject(w, "submit", managed.ErrUnsupported)
		return
	}
	prior, err := s.ledger.Request(input.RequestKey)
	if err == nil {
		if !reflect.DeepEqual(prior.Spec, job) || prior.Spec.Input.InventoryDigest != job.Input.InventoryDigest {
			reject(w, "submit", managed.ErrConflict)
			return
		}
		original, readErr := s.store.Read(prior.ObjectID)
		if readErr != nil {
			reject(w, "submit", readErr)
			return
		}
		// An available source is rechecked, so changed bytes cannot hide behind
		// a reused key. A removed source recovers the already sealed object.
		sealed, sealErr := s.store.Seal(r.Context(), job.Input.Root, job.Input.SelectedFiles, snapshot.Provenance{EvidenceClass: job.Provenance.EvidenceClass})
		if sealErr == nil && strings.TrimPrefix(sealed.Manifest.TreeSHA256, "sha256:") != job.Input.InventoryDigest {
			reject(w, "submit", managed.ErrConflict)
			return
		}
		if errors.Is(sealErr, os.ErrNotExist) {
			if compareErr := snapshot.CompareAvailable(r.Context(), job.Input.Root, original); compareErr != nil {
				if errors.Is(compareErr, snapshot.ErrChanged) {
					reject(w, "submit", managed.ErrConflict)
				} else {
					reject(w, "submit", compareErr)
				}
				return
			}
		} else if sealErr != nil {
			reject(w, "submit", sealErr)
			return
		}
		send(w, 200, "submit", prior.Receipt, nil)
		return
	}
	if !errors.Is(err, managed.ErrNotFound) {
		reject(w, "submit", err)
		return
	}
	sealed, err := s.store.Seal(r.Context(), job.Input.Root, job.Input.SelectedFiles, snapshot.Provenance{EvidenceClass: job.Provenance.EvidenceClass})
	if err != nil {
		reject(w, "submit", err)
		return
	}
	if strings.TrimPrefix(sealed.Manifest.TreeSHA256, "sha256:") != job.Input.InventoryDigest {
		reject(w, "submit", managed.ErrConflict)
		return
	}
	jobID, err := randomID("job-")
	if err != nil {
		reject(w, "submit", err)
		return
	}
	receipt, err := s.ledger.AdmitSealed(input.Spec, job.Input.InventoryDigest, sealed.ObjectID, input.RequestKey, jobID)
	if err != nil {
		reject(w, "submit", err)
		return
	}
	s.notifyScheduler()
	send(w, 200, "submit", receipt, nil)
}
func randomID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}
func (s *runtimeServer) inspect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	receipt, err := s.ledger.Snapshot(id)
	if err != nil {
		reject(w, "inspect", err)
		return
	}
	data := map[string]any{"receipt": receipt}
	if receipt.JobState == "finished" {
		result, err := s.ledger.Result(id)
		if err != nil {
			reject(w, "inspect", err)
			return
		}
		data["result"] = result
	}
	send(w, 200, "inspect", data, nil)
}
func (s *runtimeServer) wait(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	timeout := 5 * time.Minute
	if value := r.URL.Query().Get("timeout"); value != "" {
		var err error
		timeout, err = time.ParseDuration(value)
		if err != nil || timeout <= 0 || timeout > 24*time.Hour {
			reject(w, "wait", managed.ErrInvalid)
			return
		}
	}
	initial, err := s.ledger.Snapshot(id)
	if err != nil {
		reject(w, "wait", err)
		return
	}
	if initial.JobState != "finished" {
		ch := make(chan struct{})
		s.mu.Lock()
		s.waiters[id] = append(s.waiters[id], ch)
		s.mu.Unlock()
		defer s.removeWaiter(id, ch)
		// Registration and state read are separate; recheck closes the race.
		current, err := s.ledger.Snapshot(id)
		if err != nil {
			reject(w, "wait", err)
			return
		}
		if current.JobState != "finished" {
			timer := time.NewTimer(timeout)
			defer timer.Stop()
			select {
			case <-ch:
			case <-timer.C:
				send(w, http.StatusRequestTimeout, "wait", nil, &responseError{Code: "WAIT_TIMEOUT", Message: "wait deadline elapsed", Retry: "none"})
				return
			case <-r.Context().Done():
				return
			}
		}
	}
	result, err := s.ledger.Result(id)
	if err != nil {
		reject(w, "wait", err)
		return
	}
	send(w, 200, "wait", result, nil)
}
func (s *runtimeServer) removeWaiter(id string, ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.waiters[id]
	for i, c := range list {
		if c == ch {
			s.waiters[id] = append(list[:i], list[i+1:]...)
			break
		}
	}
}
func (s *runtimeServer) signalFinished(id string) {
	s.mu.Lock()
	list := s.waiters[id]
	delete(s.waiters, id)
	s.mu.Unlock()
	for _, ch := range list {
		close(ch)
	}
}
func (s *runtimeServer) notifyScheduler() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *runtimeServer) schedule(ctx context.Context) {
	s.notifyScheduler()
	for {
		deadline, hasDeadline, deadlineErr := s.ledger.NextQueueDeadline()
		var timer *time.Timer
		var timerChannel <-chan time.Time
		if deadlineErr != nil {
			timer = time.NewTimer(time.Second)
			timerChannel = timer.C
		} else if hasDeadline {
			wait := time.Until(deadline)
			if wait < 0 {
				wait = 0
			}
			timer = time.NewTimer(wait)
			timerChannel = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-s.wake:
		case <-timerChannel:
		}
		if timer != nil {
			timer.Stop()
		}
		for {
			expired, err := s.ledger.FirstExpiredQueued(time.Now().UTC())
			if err != nil {
				break
			}
			if _, err := s.ledger.ExpireQueued(expired.JobID, expired.Revision, "expired-"+expired.JobID); err != nil {
				break
			}
			s.signalFinished(expired.JobID)
		}
		for {
			queued, err := s.ledger.NextQueued()
			if err != nil {
				break
			}
			if time.Now().UTC().UnixNano() >= queued.DeadlineNS {
				if _, err := s.ledger.ExpireQueued(queued.Receipt.JobID, queued.Receipt.JobRevision, "expired-"+queued.Receipt.JobID); err != nil {
					break
				}
				s.signalFinished(queued.Receipt.JobID)
				continue
			}
			if queued.ObjectID == "" {
				err = snapshot.ErrCorrupt
			} else {
				_, err = s.store.Read(queued.ObjectID)
			}
			if err != nil {
				if _, err := s.ledger.FailQueuedInput(queued.Receipt.JobID, queued.Receipt.JobRevision, "input-"+queued.Receipt.JobID); err != nil {
					break
				}
				s.signalFinished(queued.Receipt.JobID)
				continue
			}
			r, err := s.ledger.Reserve(managed.ReserveRequest{JobID: queued.Receipt.JobID, ExpectedJobRevision: queued.Receipt.JobRevision, ExpectedConfigRevision: queued.Receipt.ResourceConfigRevision, ExpectedAvailabilityRevision: queued.Receipt.ResourceAvailabilityRevision, MutationKey: "reserve-" + queued.Receipt.JobID})
			if err != nil {
				if errors.Is(err, managed.ErrBusy) && time.Now().UTC().UnixNano() >= queued.DeadlineNS {
					if _, e := s.ledger.ExpireQueued(queued.Receipt.JobID, queued.Receipt.JobRevision, "expired-"+queued.Receipt.JobID); e == nil {
						s.signalFinished(queued.Receipt.JobID)
						continue
					}
				}
				break
			}
			go s.execute(ctx, r, queued.Spec, queued.ObjectID, time.Now().Add(time.Duration(queued.Spec.Deadlines.PrepareSeconds)*time.Second))
		}
	}
}

func (s *runtimeServer) failed(id string, cause error) {
	if errors.Is(cause, errPrepareUnproven) {
		_ = s.ledger.ReconcileLocalFailure(id)
		return
	}
	receipt, readErr := s.ledger.Snapshot(id)
	if readErr != nil {
		return
	}
	if receipt.JobState == "preparing" || receipt.JobState == "ready" {
		jobRoot := filepath.Join(s.root.Path, "jobs", id)
		reason := "prepare_failed"
		if errors.Is(cause, context.DeadlineExceeded) {
			reason = "prepare_deadline"
		}
		if os.MkdirAll(jobRoot, 0700) == nil && os.RemoveAll(filepath.Join(jobRoot, "workspace")) == nil && writeReceipt(jobRoot, "prepare-failure.json", map[string]any{"reason": reason}) == nil {
			if next, e := s.ledger.AbortLocalBeforeStart(id, receipt.JobRevision); e == nil {
				if next, e = s.ledger.AdvanceLocal(id, next.JobRevision, "collecting", "cleaning", "abort-collected-"+id); e == nil {
					if next, e = s.ledger.AdvanceLocal(id, next.JobRevision, "cleaning", "settling", "abort-cleaned-"+id); e == nil {
						result := managed.Result{Schema: "haepari.result/v1", JobID: id, ResourceID: next.ResourceID, ResourceRevision: next.ResourceConfigRevision, LeaseGeneration: next.LeaseGeneration, JobState: "finished", ResourceAvailability: "available", WorkloadOutcome: "inconclusive", EvidenceCompleteness: "unobserved", CleanupOutcome: "verified", FinalVerdict: managed.Incomplete, Reason: reason}
						if _, e = s.ledger.SettleLocal(id, next.JobRevision, result); e == nil {
							s.signalFinished(id)
							return
						}
					}
				}
			}
		}
	}
	_ = s.ledger.ReconcileLocalFailure(id)
}
