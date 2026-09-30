package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"unicode/utf8"

	"github.com/irootkernel/haepari/internal/managed"
)

const (
	ObserverSchema = "haepari.observer/v1"
	MaxLogBytes    = 64 << 10
	maxRecordBytes = 4 << 20
)

type EventsOptions struct {
	After int64
	Limit int
}

type LogsOptions struct {
	After    int64
	Identity string
}

type LogGap struct {
	Reason           string `json:"reason"`
	RequestedOffset  int64  `json:"requested_offset"`
	ResumeOffset     int64  `json:"resume_offset"`
	PreviousIdentity string `json:"previous_identity,omitempty"`
}

// Data uses JSON base64, preserving arbitrary output bytes without UTF-8 repair.
type LogPage struct {
	JobID      string          `json:"job_id"`
	Identity   string          `json:"identity"`
	Offset     int64           `json:"offset"`
	NextOffset int64           `json:"next_offset"`
	Size       int64           `json:"size"`
	Data       []byte          `json:"data"`
	More       bool            `json:"more"`
	Snapshot   managed.Receipt `json:"snapshot"`
	Gap        *LogGap         `json:"gap,omitempty"`
}

type Record struct {
	Schema   string              `json:"schema"`
	Type     string              `json:"type"`
	JobID    string              `json:"job_id"`
	Events   *managed.EventBatch `json:"events,omitempty"`
	Logs     *LogPage            `json:"logs,omitempty"`
	Error    *Issue              `json:"error,omitempty"`
	Cursor   int64               `json:"cursor"`
	Offset   int64               `json:"offset"`
	Identity string              `json:"identity,omitempty"`
	Reason   string              `json:"reason,omitempty"`
}

func eventsPath(id string, q EventsOptions, follow bool) (string, error) {
	if id == "" || q.After < 0 || q.Limit < 0 || q.Limit > 1000 {
		return "", managed.ErrInvalid
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	values := url.Values{"after": {strconv.FormatInt(q.After, 10)}, "limit": {strconv.Itoa(q.Limit)}}
	if follow {
		values.Set("follow", "1")
	}
	return "/v1/jobs/" + url.PathEscape(id) + "/events?" + values.Encode(), nil
}

func logsPath(id string, q LogsOptions, follow bool) (string, error) {
	if id == "" || q.After < 0 || len(q.Identity) > 128 {
		return "", managed.ErrInvalid
	}
	values := url.Values{"after": {strconv.FormatInt(q.After, 10)}}
	if q.Identity != "" {
		values.Set("identity", q.Identity)
	}
	if follow {
		values.Set("follow", "1")
	}
	return "/v1/jobs/" + url.PathEscape(id) + "/logs?" + values.Encode(), nil
}

func (c *Client) Events(ctx context.Context, id string, q EventsOptions) (managed.EventBatch, Envelope, error) {
	var page managed.EventBatch
	path, err := eventsPath(id, q, false)
	if err != nil {
		return page, Envelope{}, err
	}
	envelope, err := c.call(ctx, http.MethodGet, path, nil, &page)
	if err == nil {
		err = validateEvents(id, q.After, page)
	}
	return page, envelope, err
}

func (c *Client) Logs(ctx context.Context, id string, q LogsOptions) (LogPage, Envelope, error) {
	var page LogPage
	path, err := logsPath(id, q, false)
	if err != nil {
		return page, Envelope{}, err
	}
	envelope, err := c.call(ctx, http.MethodGet, path, nil, &page)
	if err == nil {
		err = validateLogs(id, q.After, q.Identity, page)
	}
	return page, envelope, err
}

func validateEvents(id string, after int64, p managed.EventBatch) error {
	if p.Snapshot.JobID != id || p.CurrentCursor < after || p.NextCursor < after || p.NextCursor > p.CurrentCursor || len(p.Events) > 1000 {
		return errors.New("invalid observer event checkpoint")
	}
	if p.Resync != nil {
		if p.Resync.JobID != id || len(p.Events) != 0 || p.NextCursor != p.CurrentCursor || p.More {
			return errors.New("invalid observer event resync")
		}
		return nil
	}
	last := after
	for _, event := range p.Events {
		if event.JobID != id || event.Cursor <= last || event.Cursor > p.CurrentCursor {
			return errors.New("invalid observer event scope or order")
		}
		last = event.Cursor
	}
	if p.NextCursor != last || p.More != (p.NextCursor < p.CurrentCursor) || (p.More && last == after) {
		return errors.New("invalid observer event page progression")
	}
	return nil
}

func validateLogs(id string, after int64, identity string, p LogPage) error {
	if p.JobID != id || p.Snapshot.JobID != id || p.Offset < 0 || p.Size < 0 || p.NextOffset < p.Offset || p.NextOffset > p.Size || len(p.Data) > MaxLogBytes || p.NextOffset-p.Offset != int64(len(p.Data)) || p.More != (p.NextOffset < p.Size) || len(p.Identity) > 128 {
		return errors.New("invalid observer log byte range")
	}
	if p.Gap != nil {
		if p.Gap.RequestedOffset != after || p.Gap.PreviousIdentity != identity || p.Gap.ResumeOffset != 0 || p.Offset != p.Gap.ResumeOffset || len(p.Data) != 0 {
			return errors.New("invalid observer log gap checkpoint")
		}
		switch p.Gap.Reason {
		case "truncated", "replaced", "missing":
		default:
			return errors.New("unknown observer log gap")
		}
		return nil
	}
	if p.Offset != after || (identity != "" && p.Identity != identity) || (p.Identity == "" && (p.Size != 0 || after != 0)) || (p.More && len(p.Data) == 0) {
		return errors.New("observer log offset or identity changed without gap")
	}
	return nil
}

func (c *Client) FollowEvents(ctx context.Context, id string, q EventsOptions, consume func(Record) error) error {
	path, err := eventsPath(id, q, true)
	if err != nil {
		return err
	}
	return c.follow(ctx, id, path, "events", q.After, "", consume)
}

func (c *Client) FollowLogs(ctx context.Context, id string, q LogsOptions, consume func(Record) error) error {
	path, err := logsPath(id, q, true)
	if err != nil {
		return err
	}
	return c.follow(ctx, id, path, "logs", q.After, q.Identity, consume)
}

func observerHTTPError(response *http.Response) error {
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponseBytes {
		return errors.New("oversized observer error response")
	}
	if _, err := managed.DecodeEnvelope(data); err != nil {
		return err
	}
	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if envelope.Schema != "haepari.envelope/v1" || envelope.OK || envelope.Error == nil || envelope.Error.Code == "" {
		return errors.New("invalid observer error response")
	}
	return envelope.Error
}

func (c *Client) follow(ctx context.Context, id, path, operation string, checkpoint int64, identity string, consume func(Record) error) error {
	if consume == nil {
		return managed.ErrInvalid
	}
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/x-ndjson")
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return observerHTTPError(response)
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/x-ndjson" {
		return errors.New("invalid observer stream content type")
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), maxRecordBytes+1)
	terminal := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Bytes()
		if len(line) > maxRecordBytes || !utf8.Valid(line) {
			return errors.New("invalid or oversized observer record")
		}
		if err := managed.ValidateUniqueJSONKeys(line); err != nil {
			return fmt.Errorf("invalid observer record: %w", err)
		}
		var record Record
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return err
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return errors.New("trailing observer record data")
		}
		if err := validateRecord(id, operation, checkpoint, identity, record); err != nil {
			return err
		}
		if record.Type == "error" {
			if err := consume(record); err != nil {
				return err
			}
			return record.Error
		}
		if record.Type == "end" && !terminal {
			return errors.New("observer end without a drained terminal snapshot")
		}
		if err := consume(record); err != nil {
			return err
		}
		if record.Type == "end" {
			return nil
		}
		if record.Events != nil {
			checkpoint = record.Events.NextCursor
			terminal = !record.Events.More && record.Events.Snapshot.JobState == "finished"
		}
		if record.Logs != nil {
			checkpoint, identity = record.Logs.NextOffset, record.Logs.Identity
			terminal = !record.Logs.More && record.Logs.Snapshot.JobState == "finished"
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("observer stream ended before end record")
}

func validateRecord(id, operation string, checkpoint int64, identity string, r Record) error {
	if r.Schema != ObserverSchema || r.JobID != id {
		return errors.New("invalid observer record version or scope")
	}
	if r.Type == "error" {
		if r.Error == nil || r.Error.Code == "" || len(r.Error.Code) > 128 || r.Error.Message == "" || len(r.Error.Message) > 512 || r.Events != nil || r.Logs != nil {
			return errors.New("invalid observer error record")
		}
		if _, err := managed.ExitCode(managed.ErrorCode(r.Error.Code)); err != nil {
			return err
		}
		if r.Error.Retry != "none" && r.Error.Retry != "retry_same_key" && r.Error.Retry != "reconcile" {
			return errors.New("invalid observer retry policy")
		}
		if (operation == "events" && r.Cursor != checkpoint) || (operation == "logs" && (r.Offset != checkpoint || r.Identity != identity)) {
			return errors.New("invalid observer error checkpoint")
		}
		return nil
	}
	if r.Error != nil {
		return errors.New("observer error in non-error record")
	}
	switch r.Type {
	case "page", "resync":
		if operation == "events" {
			if r.Events == nil || r.Logs != nil || r.Cursor != r.Events.NextCursor || ((r.Type == "resync") != (r.Events.Resync != nil)) {
				return errors.New("invalid observer event record")
			}
			return validateEvents(id, checkpoint, *r.Events)
		}
		if r.Logs == nil || r.Events != nil || r.Offset != r.Logs.NextOffset || r.Identity != r.Logs.Identity || ((r.Type == "resync") != (r.Logs.Gap != nil)) {
			return errors.New("invalid observer log record")
		}
		return validateLogs(id, checkpoint, identity, *r.Logs)
	case "heartbeat", "end":
		if r.Events != nil || r.Logs != nil || (operation == "events" && r.Cursor != checkpoint) || (operation == "logs" && (r.Offset != checkpoint || r.Identity != identity)) || (r.Type == "end" && r.Reason != "finished") {
			return errors.New("invalid observer control checkpoint")
		}
		return nil
	default:
		return errors.New("unknown observer record type")
	}
}
