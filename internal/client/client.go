package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/state"
)

const protocol = "managed-execution/v1"
const maxResponseBytes = 4 << 20

type Client struct{ http *http.Client }
type Envelope struct {
	Schema    string          `json:"schema"`
	Operation string          `json:"operation"`
	OK        bool            `json:"ok"`
	Data      json.RawMessage `json:"data,omitempty"`
	Error     *Issue          `json:"error,omitempty"`
}
type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Retry   string `json:"retry"`
}

func (i *Issue) Error() string { return i.Code }

func Connect(ctx context.Context, root state.Root) (*Client, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", root.SocketPath)
	}}
	c := &Client{http: &http.Client{Transport: transport}}
	var hello struct {
		Protocol       string `json:"protocol"`
		InstallationID string `json:"installation_id"`
	}
	if _, err := c.call(ctx, "GET", "/v1/hello", nil, &hello); err != nil {
		return nil, err
	}
	if hello.Protocol != protocol || hello.InstallationID == "" {
		return nil, errors.New("protocol handshake failed")
	}
	return c, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) call(ctx context.Context, method, path string, input any, target any) (Envelope, error) {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return Envelope{}, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://haepari"+path, body)
	if err != nil {
		return Envelope{}, err
	}
	req.Header.Set("X-Haepari-Protocol", protocol)
	if raw := os.Getenv(managed.ParentContextEnvironment); raw != "" {
		if _, err := managed.DecodeParentChain(raw); err != nil {
			return Envelope{}, fmt.Errorf("%s: %w", managed.ParentContextEnvironment, managed.ErrInvalid)
		}
		req.Header.Set(managed.ParentContextHeader, raw)
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return Envelope{}, err
	}
	defer response.Body.Close()
	var envelope Envelope
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes+1))
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, err
	}
	var tail any
	if err := decoder.Decode(&tail); err != io.EOF {
		return Envelope{}, errors.New("oversized or trailing controller response")
	}
	if envelope.Schema != "haepari.envelope/v1" {
		return Envelope{}, errors.New("invalid controller envelope")
	}
	if !envelope.OK {
		if envelope.Error == nil {
			return Envelope{}, errors.New("controller error without code")
		}
		return envelope, envelope.Error
	}
	if target != nil {
		if err := json.Unmarshal(envelope.Data, target); err != nil {
			return Envelope{}, err
		}
	}
	return envelope, nil
}
func (c *Client) Register(ctx context.Context, spec []byte) (Envelope, error) {
	return c.call(ctx, "POST", "/v1/resources", map[string]any{"spec": json.RawMessage(spec)}, nil)
}
func (c *Client) Submit(ctx context.Context, spec []byte, key string) (managed.Receipt, Envelope, error) {
	var receipt managed.Receipt
	envelope, err := c.call(ctx, "POST", "/v1/jobs", map[string]any{"spec": json.RawMessage(spec), "request_key": key}, &receipt)
	return receipt, envelope, err
}

type Inspection struct {
	Receipt      managed.Receipt       `json:"receipt"`
	Result       *managed.Result       `json:"result,omitempty"`
	QueueBlocker *managed.QueueBlocker `json:"queue_blocker,omitempty"`
}

func (c *Client) Inspect(ctx context.Context, id string) (Inspection, Envelope, error) {
	var data Inspection
	envelope, err := c.call(ctx, "GET", "/v1/jobs/"+url.PathEscape(id), nil, &data)
	return data, envelope, err
}
func (c *Client) Wait(ctx context.Context, id string, timeout time.Duration) (managed.Result, Envelope, error) {
	var result managed.Result
	if timeout <= 0 {
		return result, Envelope{}, fmt.Errorf("invalid wait timeout")
	}
	envelope, err := c.call(ctx, "GET", "/v1/jobs/"+url.PathEscape(id)+"/wait?timeout="+url.QueryEscape(timeout.String()), nil, &result)
	return result, envelope, err
}

func (c *Client) SetAvailability(ctx context.Context, q managed.AvailabilityRequest, resume bool) (managed.ResourceStatus, Envelope, error) {
	action := "pause"
	if resume {
		action = "resume"
	}
	var result managed.ResourceStatus
	envelope, err := c.call(ctx, "POST", "/v1/resources/"+url.PathEscape(q.ResourceID)+"/"+action, q, &result)
	return result, envelope, err
}

func (c *Client) InspectResource(ctx context.Context, id string) (managed.ResourceStatus, Envelope, error) {
	var result managed.ResourceStatus
	envelope, err := c.call(ctx, "GET", "/v1/resources/"+url.PathEscape(id), nil, &result)
	return result, envelope, err
}
