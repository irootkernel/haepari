package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
)

const (
	observerSchema      = "haepari.observer/v1"
	observerHeartbeat   = 15 * time.Second
	observerLogPoll     = 250 * time.Millisecond
	observerWriteWait   = 5 * time.Second
	observerLogGateWait = 5 * time.Second
)

type observerRecord struct {
	Schema   string              `json:"schema"`
	Type     string              `json:"type"`
	JobID    string              `json:"job_id"`
	Events   *managed.EventBatch `json:"events,omitempty"`
	Logs     *logPage            `json:"logs,omitempty"`
	Error    *responseError      `json:"error,omitempty"`
	Cursor   int64               `json:"cursor"`
	Offset   int64               `json:"offset"`
	Identity string              `json:"identity,omitempty"`
	Reason   string              `json:"reason,omitempty"`
}

func observerQuery(r *http.Request, events bool) (int64, int, bool, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, 0, false, managed.ErrInvalid
	}
	for key, v := range values {
		if len(v) != 1 || (key != "after" && key != "follow" && !(events && key == "limit") && !(!events && key == "identity")) {
			return 0, 0, false, managed.ErrInvalid
		}
	}
	after := int64(0)
	if values.Has("after") {
		after, err = strconv.ParseInt(values.Get("after"), 10, 64)
		if err != nil || after < 0 {
			return 0, 0, false, managed.ErrInvalid
		}
	}
	limit := 100
	if values.Has("limit") {
		limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil || limit <= 0 || limit > 1000 {
			return 0, 0, false, managed.ErrInvalid
		}
	}
	follow := false
	if values.Has("follow") {
		switch values.Get("follow") {
		case "1", "true":
			follow = true
		case "0", "false":
		default:
			return 0, 0, false, managed.ErrInvalid
		}
	}
	if len(values.Get("identity")) > 128 {
		return 0, 0, false, managed.ErrInvalid
	}
	return after, limit, follow, nil
}

func (s *runtimeServer) observerAllowed(r *http.Request, id string, follow bool) (managed.Receipt, error) {
	if !observerJobID.MatchString(id) {
		return managed.Receipt{}, managed.ErrInvalid
	}
	state, err := s.ledger.Snapshot(id)
	if err != nil {
		return state, err
	}
	if follow && state.JobState != "finished" {
		chain, err := managed.DecodeParentChain(r.Header.Get(managed.ParentContextHeader))
		if err != nil {
			return state, err
		}
		nested, err := s.ledger.Nested(s.installationID(), chain)
		if err != nil {
			return state, err
		}
		if nested {
			return state, managed.ErrNestedWait
		}
	}
	return state, nil
}

func writeObserver(w http.ResponseWriter, record observerRecord) error {
	control := http.NewResponseController(w)
	if err := control.SetWriteDeadline(time.Now().Add(observerWriteWait)); err != nil {
		return err
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return err
	}
	return control.Flush()
}

func sendObserverPage(w http.ResponseWriter, operation string, page any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(response{Schema: "haepari.envelope/v1", Operation: operation, OK: true, Data: page})
}

func observerFailure(err error) *responseError {
	code, retry := "STORAGE_UNAVAILABLE", "reconcile"
	switch {
	case errors.Is(err, managed.ErrInvalid):
		code, retry = "INVALID_REQUEST", "none"
	case errors.Is(err, managed.ErrResultUnavailable):
		code, retry = "RESULT_UNAVAILABLE", "none"
	case errors.Is(err, managed.ErrNotFound):
		code, retry = "NOT_FOUND", "none"
	}
	return &responseError{Code: code, Retry: retry, Message: "Observer could not read retained evidence."}
}

// Serialize actual local reads with the spawn/binding gate. The registry lock
// is only for lookup: cancellation never waits on a Job gate while holding it.
func (s *runtimeServer) readObservedLog(ctx context.Context, id string, reader *logReader, after int64, identity string) (logPage, error) {
	lookup := func() *localRunControl {
		s.cancelMu.Lock()
		c := s.localRuns[id]
		s.cancelMu.Unlock()
		return c
	}
	read := func() (logPage, error) {
		var page logPage
		if err := ctx.Err(); err != nil {
			return page, err
		}
		observation, err := s.ledger.LogObservation(id)
		if err != nil {
			return page, err
		}
		if observation.Snapshot.ResultStatus == "unavailable" {
			return page, managed.ErrResultUnavailable
		}
		return reader.read(after, identity, observation)
	}
	gated := func(c *localRunControl) (logPage, error) {
		wait, cancel := context.WithTimeout(ctx, observerLogGateWait)
		defer cancel()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		notified := false
		for !c.mu.TryLock() {
			if !notified && reader.beforeGateWait != nil {
				reader.beforeGateWait()
				notified = true
			}
			select {
			case <-wait.Done():
				return logPage{}, wait.Err()
			case <-tick.C:
			}
		}
		defer c.mu.Unlock()
		return read()
	}
	if c := lookup(); c != nil {
		return gated(c)
	}
	page, err := read()
	// Recheck under the new runner's spawn gate if it registered after lookup.
	// The recheck still rejects output missing from an observed started run.
	if c := lookup(); c != nil {
		return gated(c)
	}
	return page, err
}

func (s *runtimeServer) events(w http.ResponseWriter, r *http.Request) {
	after, limit, follow, err := observerQuery(r, true)
	if err != nil {
		reject(w, "events", err)
		return
	}
	id := r.PathValue("id")
	if _, err := s.observerAllowed(r, id, follow); err != nil {
		reject(w, "events", err)
		return
	}
	// Capture the committed generation before every read, including the first.
	changed := s.ledger.Changes()
	batch, err := s.ledger.EventsAfter(id, after, limit)
	if err != nil {
		reject(w, "events", err)
		return
	}
	if !follow {
		sendObserverPage(w, "events", batch)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	defer http.NewResponseController(w).SetWriteDeadline(time.Time{})
	heartbeat := time.NewTicker(observerHeartbeat)
	defer heartbeat.Stop()
	for {
		if r.Context().Err() != nil {
			return
		}
		kind := "page"
		if batch.Resync != nil {
			kind = "resync"
		}
		if err := writeObserver(w, observerRecord{Schema: observerSchema, Type: kind, JobID: id, Events: &batch, Cursor: batch.NextCursor}); err != nil {
			return
		}
		after = batch.NextCursor
		if !batch.More && batch.Snapshot.JobState == "finished" {
			_ = writeObserver(w, observerRecord{Schema: observerSchema, Type: "end", JobID: id, Cursor: after, Reason: "finished"})
			return
		}
		if !batch.More {
			waiting := true
			for waiting {
				select {
				case <-r.Context().Done():
					return
				case <-changed:
					waiting = false
				case <-heartbeat.C:
					if err := writeObserver(w, observerRecord{Schema: observerSchema, Type: "heartbeat", JobID: id, Cursor: after}); err != nil {
						return
					}
				}
			}
		}
		changed = s.ledger.Changes()
		batch, err = s.ledger.EventsAfter(id, after, limit)
		if err != nil {
			_ = writeObserver(w, observerRecord{Schema: observerSchema, Type: "error", JobID: id, Cursor: after, Error: observerFailure(err)})
			return
		}
	}
}

func (s *runtimeServer) logs(w http.ResponseWriter, r *http.Request) {
	after, _, follow, err := observerQuery(r, false)
	if err != nil {
		reject(w, "logs", err)
		return
	}
	id := r.PathValue("id")
	changed := s.ledger.Changes()
	snapshot, err := s.observerAllowed(r, id, follow)
	if err == nil && snapshot.ResultStatus == "unavailable" {
		err = managed.ErrResultUnavailable
	}
	if err != nil {
		reject(w, "logs", err)
		return
	}
	reader := logReader{root: s.root.Path, jobID: id}
	defer reader.close()
	identity := r.URL.Query().Get("identity")
	page, err := s.readObservedLog(r.Context(), id, &reader, after, identity)
	if err != nil {
		reject(w, "logs", err)
		return
	}
	if !follow {
		sendObserverPage(w, "logs", page)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	defer http.NewResponseController(w).SetWriteDeadline(time.Time{})
	heartbeat := time.NewTicker(observerHeartbeat)
	defer heartbeat.Stop()
	// output.log is written directly by the child; append is not a DB commit.
	// Only an active log observer uses this bounded server-side observation.
	poll := time.NewTicker(observerLogPoll)
	defer poll.Stop()
	first := true
	for {
		if r.Context().Err() != nil {
			return
		}
		if first || len(page.Data) > 0 || page.Identity != identity || page.Gap != nil || (!page.More && page.Snapshot.JobState == "finished") {
			kind := "page"
			if page.Gap != nil {
				kind = "resync"
			}
			if err := writeObserver(w, observerRecord{Schema: observerSchema, Type: kind, JobID: id, Logs: &page, Offset: page.NextOffset, Identity: page.Identity}); err != nil {
				return
			}
			first = false
		}
		after, identity = page.NextOffset, page.Identity
		if !page.More && page.Snapshot.JobState == "finished" {
			_ = writeObserver(w, observerRecord{Schema: observerSchema, Type: "end", JobID: id, Offset: after, Identity: identity, Reason: "finished"})
			return
		}
		if !page.More {
			waiting := true
			for waiting {
				select {
				case <-r.Context().Done():
					return
				case <-changed:
					waiting = false
				case <-poll.C:
					waiting = false
				case <-heartbeat.C:
					if err := writeObserver(w, observerRecord{Schema: observerSchema, Type: "heartbeat", JobID: id, Offset: after, Identity: identity}); err != nil {
						return
					}
				}
			}
		}
		changed = s.ledger.Changes()
		page, err = s.readObservedLog(r.Context(), id, &reader, after, identity)
		if err != nil {
			_ = writeObserver(w, observerRecord{Schema: observerSchema, Type: "error", JobID: id, Offset: after, Identity: identity, Error: observerFailure(err)})
			return
		}
	}
}
