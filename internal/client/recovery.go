package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/irootkernel/haepari/internal/managed"
)

func (c *Client) RecoverJob(ctx context.Context, id string, q managed.RecoveryRequest) (Envelope, error) {
	return c.call(ctx, http.MethodPost, "/v1/jobs/"+url.PathEscape(id)+"/recover", q, nil)
}

func (c *Client) RecoverResource(ctx context.Context, id string, q managed.RecoveryRequest) (Envelope, error) {
	return c.call(ctx, http.MethodPost, "/v1/resources/"+url.PathEscape(id)+"/recover", q, nil)
}
