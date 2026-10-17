package client

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/irootkernel/haepari/internal/managed"
)

// AcceptedConfiguration reads the closed config view. It does not open the
// operator's mutable file or substitute a built-in document.
func (c *Client) AcceptedConfiguration(ctx context.Context) (managed.OperatorConfigView, error) {
	var view managed.OperatorConfigView
	_, err := c.call(ctx, "GET", "/v1/operator/projection?view=config", nil, &view)
	if err != nil || view.Schema != "haepari.operator-projection/v1" || view.View != "config" || len(view.Config) == 0 {
		if err == nil {
			err = errors.New("response is not the config view")
		}
		return managed.OperatorConfigView{}, fmt.Errorf("accepted configuration projection is unavailable; omitted phase budgets cannot be filled: %w", err)
	}
	return view, nil
}

// OperatorDiagnostic reads one queue or retention page. Worker proof stays on
// the controller and is not a query parameter. Only limit 0 omits the key so
// the server applies its default. Any other limit, including a negative value,
// is forwarded for the server's strict check.
func (c *Client) OperatorDiagnostic(ctx context.Context, section, resource, cursor string, limit int) (managed.OperatorDiagnostic, Envelope, error) {
	values := url.Values{}
	values.Set("view", "diagnostic")
	values.Set("section", section)
	if resource != "" {
		values.Set("resource", resource)
	}
	if cursor != "" {
		values.Set("cursor", cursor)
	}
	if limit != 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	var body managed.OperatorDiagnostic
	envelope, err := c.call(ctx, "GET", "/v1/operator/projection?"+values.Encode(), nil, &body)
	if err != nil || body.Schema != "haepari.operator-diagnostic/v1" || body.View != "diagnostic" || body.Section != section {
		if err == nil {
			err = errors.New("response is not the diagnostic view")
		}
		return managed.OperatorDiagnostic{}, envelope, fmt.Errorf("operator diagnostic projection is unavailable: %w", err)
	}
	return body, envelope, nil
}
