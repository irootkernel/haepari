package client

import (
	"context"
	"net/url"

	"github.com/irootkernel/haepari/internal/managed"
)

func (c *Client) DoctorResource(ctx context.Context, id string) (managed.DoctorReport, Envelope, error) {
	var report managed.DoctorReport
	e, err := c.call(ctx, "GET", "/v1/resources/"+url.PathEscape(id)+"/doctor", nil, &report)
	return report, e, err
}
