package client

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/irootkernel/haepari/internal/managed"
)

func (c *Client) UpdateVMResource(ctx context.Context, id string, spec json.RawMessage, configRevision, availabilityRevision int64) (managed.ResourceStatus, Envelope, error) {
	var status managed.ResourceStatus
	q := struct {
		Spec                         json.RawMessage `json:"spec"`
		ExpectedRevision             int64           `json:"expected_revision"`
		ExpectedAvailabilityRevision int64           `json:"expected_availability_revision"`
	}{spec, configRevision, availabilityRevision}
	e, err := c.call(ctx, "POST", "/v1/resources/"+url.PathEscape(id)+"/update", q, &status)
	return status, e, err
}
