package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
)

var ErrLockGrantContext = errors.New("invalid inherited advisory grant")

func (c *Client) requireAdvisoryLock() error {
	if !slices.Contains(c.Hello.Capabilities, "advisory-lock/v1") {
		return &Issue{Code: "UNSUPPORTED", Message: "The selected controller did not advertise advisory-lock/v1.", Retry: "none"}
	}
	return nil
}

func (c *Client) AcquireLock(ctx context.Context, q managed.LockAcquireRequest) (managed.LockReply, Envelope, error) {
	var reply managed.LockReply
	if err := c.requireAdvisoryLock(); err != nil {
		return reply, Envelope{}, err
	}
	env, err := c.call(ctx, "POST", "/v1/locks/acquire", q, &reply)
	return reply, env, err
}
func (c *Client) WaitLock(ctx context.Context, id string, timeout time.Duration) (managed.LockReply, Envelope, error) {
	var reply managed.LockReply
	if err := c.requireAdvisoryLock(); err != nil {
		return reply, Envelope{}, err
	}
	if timeout <= 0 || timeout > 24*time.Hour {
		return reply, Envelope{}, managed.ErrInvalid
	}
	env, err := c.call(ctx, "GET", "/v1/locks/requests/"+url.PathEscape(id)+"/wait?timeout="+url.QueryEscape(timeout.String()), nil, &reply)
	return reply, env, err
}
func (c *Client) RenewLock(ctx context.Context, id string) (managed.LockReply, Envelope, error) {
	return c.lockAction(ctx, "grants", id, "renew", false, "")
}
func (c *Client) ReleaseLock(ctx context.Context, id string, force bool, reason string) (managed.LockReply, Envelope, error) {
	return c.lockAction(ctx, "grants", id, "release", force, reason)
}
func (c *Client) CancelLock(ctx context.Context, id string, force bool, reason string) (managed.LockReply, Envelope, error) {
	return c.lockAction(ctx, "requests", id, "cancel", force, reason)
}
func (c *Client) lockAction(ctx context.Context, kind, id, action string, force bool, reason string) (managed.LockReply, Envelope, error) {
	var reply managed.LockReply
	if err := c.requireAdvisoryLock(); err != nil {
		return reply, Envelope{}, err
	}
	q := struct {
		Force  bool   `json:"force"`
		Reason string `json:"reason,omitempty"`
	}{force, reason}
	env, err := c.call(ctx, "POST", "/v1/locks/"+kind+"/"+url.PathEscape(id)+"/"+action, q, &reply)
	return reply, env, err
}
func (c *Client) LocksList(ctx context.Context, lock string) (managed.LockList, Envelope, error) {
	return c.LocksPage(ctx, lock, "", 50)
}
func (c *Client) LocksPage(ctx context.Context, lock, cursor string, limit int) (managed.LockList, Envelope, error) {
	var data managed.LockList
	if err := c.requireAdvisoryLock(); err != nil {
		return data, Envelope{}, err
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if lock != "" {
		q.Set("lock", lock)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	env, err := c.call(ctx, "GET", "/v1/locks?"+q.Encode(), nil, &data)
	return data, env, err
}

// CheckLockGrant uses a single read-only inspection of the selected installation.
// Its Now and request deadlines come from the same view; the environment is only
// a reference, never proof or permission to reacquire.
func (c *Client) CheckLockGrant(ctx context.Context, ref managed.LockGrantReference, intendedLock string) (managed.LockRequest, error) {
	var empty managed.LockRequest
	if ref.InstallationID != c.Hello.InstallationID || ref.LockID != intendedLock {
		return empty, managed.ErrConflict
	}
	cursor := ""
	for {
		data, _, err := c.LocksPage(ctx, intendedLock, cursor, 100)
		if err != nil {
			return empty, err
		}
		if data.InstallationID != ref.InstallationID || data.Now.IsZero() {
			return empty, managed.ErrConflict
		}
		for _, request := range data.Requests {
			if request.GrantID != ref.GrantID {
				continue
			}
			if request.LockID != intendedLock || request.InstallationID != ref.InstallationID {
				return empty, managed.ErrConflict
			}
			if request.Generation != ref.Generation || request.State != "granted" || request.LivenessDeadline == nil || request.MaxHoldDeadline == nil || !data.Now.Before(*request.LivenessDeadline) || !data.Now.Before(*request.MaxHoldDeadline) {
				return empty, managed.ErrLockExpired
			}
			return request, nil
		}
		if !data.More {
			return empty, managed.ErrNotFound
		}
		if data.NextCursor == "" || data.NextCursor == cursor {
			return empty, managed.ErrInvalid
		}
		cursor = data.NextCursor
	}

}

func (c *Client) LockResourceAction(ctx context.Context, action, id string, spec json.RawMessage, expected int64, plan bool) (Envelope, error) {
	if err := c.requireAdvisoryLock(); err != nil {
		return Envelope{}, err
	}
	q := struct {
		Spec             json.RawMessage `json:"spec,omitempty"`
		ExpectedRevision int64           `json:"expected_revision,omitempty"`
		Plan             bool            `json:"plan,omitempty"`
	}{spec, expected, plan}
	return c.call(ctx, "POST", "/v1/resources/"+url.PathEscape(id)+"/"+action, q, nil)
}
