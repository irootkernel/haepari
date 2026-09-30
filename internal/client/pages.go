package client

import (
	"context"
	"net/url"
	"strconv"

	"github.com/irootkernel/haepari/internal/managed"
)

func (c *Client) Jobs(ctx context.Context, q managed.JobsQuery) (managed.JobsPage, Envelope, error) {
	var page managed.JobsPage
	values := url.Values{"resource": {q.Resource}, "state": {q.State}, "cursor": {q.Cursor}, "limit": {strconv.Itoa(q.Limit)}}
	envelope, err := c.call(ctx, "GET", "/v1/jobs?"+values.Encode(), nil, &page)
	return page, envelope, err
}
func (c *Client) Resources(ctx context.Context, cursor string, limit int) (managed.ResourcesPage, Envelope, error) {
	var page managed.ResourcesPage
	values := url.Values{"cursor": {cursor}, "limit": {strconv.Itoa(limit)}}
	envelope, err := c.call(ctx, "GET", "/v1/resources?"+values.Encode(), nil, &page)
	return page, envelope, err
}

func (c *Client) Cancel(ctx context.Context, q managed.CancelRequest) (managed.CancelReceipt, Envelope, error) {
	var receipt managed.CancelReceipt
	envelope, err := c.call(ctx, "POST", "/v1/jobs/"+url.PathEscape(q.JobID)+"/cancel", q, &receipt)
	return receipt, envelope, err
}
