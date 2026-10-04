package controller

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/utm"
)

const protocol = "managed-execution/v1"

type runtimeServer struct {
	root         state.Root
	ledger       *managed.Ledger
	store        *snapshot.Store
	installation string
	cancelMu     sync.Mutex
	localRuns    map[string]*localRunControl
	wake         chan struct{}
	detached     chan struct{}
	workerMu     sync.Mutex
	jobWorkers   map[string]int
	closing      bool
	workers      sync.WaitGroup
	recoveryKey  [32]byte
	// Serialize finished evidence reads/deletion and sealing through admission.
	evidenceMu      sync.RWMutex
	backendEvidence func(kind, id string) (evidenceBackend, error)
	utmCommand      utm.CommandRunner // nil in production; the argv boundary is injectable for fake UTM
	// Test barrier after a replay miss, before observing readiness. Nil in production.
	beforeAvailabilityObservation func()
	beforeWaitRead                func() // nil in production; subscribe/read race barrier
}

func newRuntime(root state.Root, ledger *managed.Ledger, store *snapshot.Store) (*runtimeServer, error) {
	sum := sha256.Sum256([]byte(root.Path))
	id, err := ledger.BindInstallation("local-" + hex.EncodeToString(sum[:8]))
	if err != nil {
		return nil, err
	}
	s := &runtimeServer{root: root, ledger: ledger, store: store, installation: id, wake: make(chan struct{}, 1), detached: make(chan struct{})}
	if _, err := rand.Read(s.recoveryKey[:]); err != nil {
		return nil, err
	}
	return s, nil
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
	case errors.Is(err, managed.ErrNestedAcquisition):
		code = "NESTED_ACQUISITION_REJECTED"
	case errors.Is(err, managed.ErrNestedWait):
		code = "NESTED_WAIT_REJECTED"
	case errors.Is(err, managed.ErrConflict), errors.Is(err, snapshot.ErrChanged):
		code = "CONFLICT"
	case errors.Is(err, managed.ErrNotFound):
		code = "NOT_FOUND"
		status = http.StatusNotFound
	case errors.Is(err, managed.ErrLockExpired):
		code = "LOCK_EXPIRED"
	case errors.Is(err, managed.ErrBusy):
		code = "RESOURCE_BUSY"
		status = http.StatusConflict
	case errors.Is(err, managed.ErrResultUnavailable):
		code = "RESULT_UNAVAILABLE"
	case errors.Is(err, managed.ErrBlocked):
		code = "RECOVERY_BLOCKED"
		status = http.StatusConflict
	case errors.Is(err, managed.ErrUnsupported):
		code = "UNSUPPORTED"
	case errors.Is(err, managed.ErrStale):
		code = "STALE_REVISION"
		status = http.StatusConflict
	case errors.Is(err, managed.ErrInvalid), errors.Is(err, snapshot.ErrInvalid):
		code = "INVALID_REQUEST"
	case errors.Is(err, snapshot.ErrSourceUnavailable), errors.Is(err, utm.ErrUnavailable):
		code = "PREREQUISITE_UNAVAILABLE"
	default:
		code = "STORAGE_UNAVAILABLE"
		status = http.StatusServiceUnavailable
		retry = "reconcile"
	}
	message := map[string]string{
		"NESTED_ACQUISITION_REJECTED": "A managed parent still holds capacity; settle it before acquiring more work.",
		"NESTED_WAIT_REJECTED":        "A managed parent still holds capacity; blocking on nonterminal work is prohibited.",
		"RESULT_UNAVAILABLE":          "The admission is known, but its retained result is unavailable; no new job was launched.",
		"INVALID_REQUEST":             "The request is malformed or unsafe.",
		"PREREQUISITE_UNAVAILABLE":    "A required input or backend prerequisite is unavailable; correct it before retrying.",
		"CONFLICT":                    "The request conflicts with existing or changed input.",
		"NOT_FOUND":                   "The requested job or resource was not found.",
		"LOCK_EXPIRED":                "The advisory grant has ended or its generation is stale.",
		"RESOURCE_BUSY":               "The resource is occupied or already registered.",
		"UNSUPPORTED":                 "This mode or resource is not supported.",
		"STALE_REVISION":              "The expected revision is no longer current.",
		"RECOVERY_BLOCKED":            "The current availability or readiness blocks this transition; inspect the resource. Pause/resume cannot clear quarantine.",
		"STORAGE_UNAVAILABLE":         "Durable state is unavailable; reconcile before retrying.",
	}[code]
	if code == "RECOVERY_BLOCKED" {
		var availability *managed.AvailabilityRefusal
		var readiness *localReadinessRefusal
		switch {
		case errors.As(err, &availability):
			message = availability.Error()
		case errors.As(err, &readiness):
			message = readiness.Error()
		}
	}
	send(w, status, op, nil, &responseError{Code: code, Message: message, Retry: retry})
}
func (s *runtimeServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/hello", func(w http.ResponseWriter, r *http.Request) {
		send(w, 200, "hello", map[string]any{"protocol": protocol, "protocol_version": 1, "capabilities": managed.Capabilities(), "installation_id": s.installationID()}, nil)
	})
	mux.HandleFunc("POST /v1/resources", s.register)
	mux.HandleFunc("GET /v1/resources", s.resourcesList)
	mux.HandleFunc("GET /v1/jobs", s.jobsList)
	mux.HandleFunc("GET /v1/jobs/{id}/events", s.events)
	mux.HandleFunc("GET /v1/jobs/{id}/logs", s.logs)
	mux.HandleFunc("GET /v1/jobs/{id}/artifacts", s.artifacts)
	mux.HandleFunc("POST /v1/jobs/{id}/purge", s.purge)
	mux.HandleFunc("POST /v1/jobs/{id}/cancel", s.cancel)
	mux.HandleFunc("POST /v1/jobs/{id}/recover", s.jobRecovery)
	mux.HandleFunc("POST /v1/resources/{id}/{action}", s.resourceAvailability)
	mux.HandleFunc("GET /v1/resources/{id}", s.resourceInspect)
	mux.HandleFunc("POST /v1/jobs", s.submit)
	mux.HandleFunc("GET /v1/jobs/{id}", s.inspect)
	mux.HandleFunc("GET /v1/jobs/{id}/wait", s.wait)
	mux.HandleFunc("POST /v1/locks/acquire", s.lockAcquire)
	mux.HandleFunc("GET /v1/locks", s.locksList)
	mux.HandleFunc("GET /v1/locks/requests/{id}/wait", s.lockWait)
	mux.HandleFunc("POST /v1/locks/requests/{id}/cancel", s.lockCancel)
	mux.HandleFunc("POST /v1/locks/grants/{id}/{action}", s.lockGrantAction)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		minimum, minErr := strconv.Atoi(r.Header.Get("X-Haepari-Protocol-Min"))
		maximum, maxErr := strconv.Atoi(r.Header.Get("X-Haepari-Protocol-Max"))
		if len(r.Header.Values("X-Haepari-Protocol-Min")) != 1 || len(r.Header.Values("X-Haepari-Protocol-Max")) != 1 || minErr != nil || maxErr != nil || minimum < 1 || maximum < minimum || minimum > 1 || maximum < 1 || (r.URL.Path != "/v1/hello" && (len(r.Header.Values("X-Haepari-Protocol")) != 1 || r.Header.Get("X-Haepari-Protocol") != protocol)) {
			send(w, http.StatusBadRequest, "handshake", nil, &responseError{Code: "PROTOCOL_UNSUPPORTED", Message: "protocol version mismatch", Retry: "none"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func decodeBody(r *http.Request, target any) error {
	defer r.Body.Close()
	// The CLI accepts a 1 MiB spec. Leave room for the JSON envelope and
	// bounded request key, and reject any bytes beyond the complete limit.
	const maxBody = 1<<20 + 4096
	encoded, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(encoded) > maxBody {
		return managed.ErrInvalid
	}
	if err := managed.ValidateUniqueJSONKeys(encoded); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
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
	// Preserve managed decoding while retaining the bytes for exact advisory fields.
	defer r.Body.Close()
	const limit = 1<<20 + 4096
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || len(raw) > limit {
		reject(w, "resource.register", managed.ErrInvalid)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
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
	if spec.Kind == "lock" {
		if err := decodeLockJSON(raw, &input); err != nil {
			reject(w, "resource.register", err)
			return
		}
	}
	if spec.Kind == "utm-macos" {
		if err := s.verifyVMRegistration(r.Context(), spec); err != nil {
			reject(w, "resource.register", err)
			return
		}
	}
	if err := s.ledger.RegisterResource(input.Spec); err != nil {
		reject(w, "resource.register", err)
		return
	}
	s.notifyScheduler()
	send(w, 200, "resource.register", map[string]any{"resource_id": spec.ID, "revision": spec.Revision}, nil)
}
func (s *runtimeServer) submit(w http.ResponseWriter, r *http.Request) {
	s.evidenceMu.Lock()
	defer s.evidenceMu.Unlock()
	chain, err := managed.DecodeParentChain(r.Header.Get(managed.ParentContextHeader))
	if err != nil {
		reject(w, "submit", err)
		return
	}
	if nested, err := s.ledger.Nested(s.installationID(), chain); err != nil {
		reject(w, "submit", err)
		return
	} else if nested {
		reject(w, "submit", managed.ErrNestedAcquisition)
		return
	}
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
	resource, err := s.ledger.ResourceStatus(job.ResourceID)
	if err != nil || resource.Kind == "lock" {
		if err == nil {
			err = managed.ErrUnsupported
		}
		reject(w, "submit", err)
		return
	}
	if !managed.ValidRequestKey(input.RequestKey) {
		reject(w, "submit", managed.ErrInvalid)
		return
	}
	prior, err := s.ledger.Request(input.RequestKey)
	if err == nil {
		submittedDigest, err := managed.CanonicalDigest(job, job.Input.InventoryDigest)
		if err != nil {
			reject(w, "submit", err)
			return
		}
		if submittedDigest != prior.Receipt.Admission.SubmittedDigest {
			reject(w, "submit", managed.ErrConflict)
			return
		}

		var original snapshot.Snapshot
		if prior.Receipt.ResultStatus == "unavailable" {
			original.Manifest, err = snapshot.DecodeInventory(prior.Inventory, prior.ObjectID)
			if err != nil {
				reject(w, "submit", err)
				return
			}
			original.ObjectID = prior.ObjectID
		} else {
			original, err = s.store.Read(prior.ObjectID)
			if err != nil {
				reject(w, "submit", err)
				return
			}
			if len(prior.Inventory) == 0 {
				inventory, err := json.Marshal(original.Manifest)
				if err != nil {
					reject(w, "submit", err)
					return
				}
				if err := s.ledger.BindAdmissionInventory(input.RequestKey, original.ObjectID, inventory); err != nil {
					reject(w, "submit", err)
					return
				}
			}

		}
		// Compare surviving selected files without re-sealing or recreating a
		// purged payload. Missing source retains the admitted identity.
		if err := snapshot.CompareAvailable(r.Context(), job.Input.Root, original); err != nil {
			if errors.Is(err, snapshot.ErrChanged) || errors.Is(err, snapshot.ErrInvalid) {
				reject(w, "submit", managed.ErrConflict)
			} else {
				reject(w, "submit", err)
			}
			return
		}

		send(w, 200, "submit", prior.Receipt, nil)
		return
	}
	if !errors.Is(err, managed.ErrNotFound) {
		reject(w, "submit", err)
		return
	}
	// Registration and host control precede TASK-014's authenticated guest
	// execution. Never dispatch a registered VM through the local runner.
	if resource.Kind != "local" || job.Mode != "auto" || job.Provenance.EvidenceClass != "development" || len(job.Input.SelectedFiles) == 0 {
		reject(w, "submit", managed.ErrUnsupported)
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
	inventory, err := json.Marshal(sealed.Manifest)
	if err != nil {
		reject(w, "submit", err)
		return
	}
	receipt, err := s.ledger.AdmitWithParent(input.Spec, job.Input.InventoryDigest, sealed.ObjectID, inventory, input.RequestKey, jobID, s.installationID(), chain)
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
	if receipt.JobState == "queued" {
		blocker, err := s.ledger.QueueBlocker(id)
		if err != nil {
			reject(w, "inspect", err)
			return
		}
		if blocker != nil {
			data["queue_blocker"] = blocker
		}
	}
	if receipt.JobState == "finished" && receipt.ResultStatus != "unavailable" {
		result, err := s.ledger.Result(id)
		if err != nil {
			reject(w, "inspect", err)
			return
		}
		data["result"] = result
		supplementary, err := s.ledger.SupplementaryEvidence(id)
		if err != nil {
			reject(w, "inspect", err)
			return
		}
		if len(supplementary) > 0 {
			data["supplementary"] = supplementary
		}
	}
	send(w, 200, "inspect", data, nil)
}
func (s *runtimeServer) wait(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	timeout := 5 * time.Minute
	if v := r.URL.Query().Get("timeout"); v != "" {
		var err error
		timeout, err = time.ParseDuration(v)
		if err != nil || timeout <= 0 || timeout > 24*time.Hour {
			reject(w, "wait", managed.ErrInvalid)
			return
		}
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		changed := s.ledger.Changes()
		if s.beforeWaitRead != nil {
			s.beforeWaitRead()
		}
		receipt, err := s.ledger.Snapshot(id)
		if err != nil {
			reject(w, "wait", err)
			return
		}
		if receipt.ResultStatus == "unavailable" {
			reject(w, "wait", managed.ErrResultUnavailable)
			return
		}
		if receipt.JobState == "finished" {
			result, err := s.ledger.Result(id)
			if err != nil {
				reject(w, "wait", err)
				return
			}
			send(w, 200, "wait", result, nil)
			return
		}
		chain, err := managed.DecodeParentChain(r.Header.Get(managed.ParentContextHeader))
		if err != nil {
			reject(w, "wait", err)
			return
		}
		nested, err := s.ledger.Nested(s.installationID(), chain)
		if err != nil {
			reject(w, "wait", err)
			return
		}
		if nested {
			reject(w, "wait", managed.ErrNestedWait)
			return
		}
		select {
		case <-changed:
		case <-deadline.C:
			send(w, http.StatusRequestTimeout, "wait", nil, &responseError{Code: "WAIT_TIMEOUT", Message: "wait deadline elapsed", Retry: "none"})
			return
		case <-r.Context().Done():
			return
		}
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
		}
		for {
			// See managed.NextQueued for selector/Reserve/controller guard ownership.
			queued, err := s.ledger.NextQueued()
			if err != nil {
				break
			}
			if time.Now().UTC().UnixNano() >= queued.DeadlineNS {
				if _, err := s.ledger.ExpireQueued(queued.Receipt.JobID, queued.Receipt.JobRevision, "expired-"+queued.Receipt.JobID); err != nil {
					break
				}
				continue
			}
			if queued.PinnedConfigRevision != queued.Receipt.ResourceConfigRevision {
				if _, err := s.ledger.FailQueuedConfiguration(queued.Receipt.JobID, queued.Receipt.JobRevision, "config-"+queued.Receipt.JobID); err != nil {
					break
				}
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
				continue
			}
			r, err := s.ledger.Reserve(managed.ReserveRequest{JobID: queued.Receipt.JobID, ExpectedJobRevision: queued.Receipt.JobRevision, ExpectedConfigRevision: queued.Receipt.ResourceConfigRevision, ExpectedAvailabilityRevision: queued.Receipt.ResourceAvailabilityRevision, MutationKey: "reserve-" + queued.Receipt.JobID})
			if err != nil {
				if errors.Is(err, managed.ErrBusy) && time.Now().UTC().UnixNano() >= queued.DeadlineNS {
					if _, e := s.ledger.ExpireQueued(queued.Receipt.JobID, queued.Receipt.JobRevision, "expired-"+queued.Receipt.JobID); e == nil {
						continue
					}
				}
				break
			}
			s.startWorker(func() {
				s.execute(ctx, r, queued.Spec, queued.ObjectID, time.Now().Add(time.Duration(queued.Spec.Deadlines.PrepareSeconds)*time.Second))
			})
		}
	}
}

func (s *runtimeServer) failed(id string, _ error) {
	select {
	case <-s.detached:
		return
	default:
	}
	// Completed preparation failures use the common bounded phase path.
	// Unexpected errors cannot prove an owned worker stopped or cleanup safe.
	_ = s.ledger.ReconcileLocalFailure(id)
}

func (s *runtimeServer) installationID() string {
	return s.installation
}
