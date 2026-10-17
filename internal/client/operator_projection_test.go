package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

type projectionTransport struct {
	status int
	body   []byte
	path   string
}

func (p *projectionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	p.path = r.URL.RequestURI()
	status := p.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(p.body)), Header: make(http.Header)}, nil
}

var _ = ginkgo.Describe("AcceptedConfiguration", ginkgo.Label("unit"), func() {
	ginkgo.It("reads the config view and names the route", func() {
		view, err := json.Marshal(map[string]any{
			"schema":          "haepari.operator-projection/v1",
			"view":            "config",
			"installation_id": "local-test",
			"observed_at":     "2026-10-16T00:00:00Z",
			"config":          json.RawMessage(`{"schema":"haepari.config/v1"}`),
			"document_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"values_digest":   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		body, err := json.Marshal(Envelope{Schema: "haepari.envelope/v1", Operation: "operator.projection", OK: true, Data: view})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		transport := &projectionTransport{body: body}
		c := &Client{http: &http.Client{Transport: transport}}
		got, err := c.AcceptedConfiguration(context.Background())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(transport.path).To(gomega.Equal("/v1/operator/projection?view=config"))
		gomega.Expect(got.Schema).To(gomega.Equal("haepari.operator-projection/v1"))
		gomega.Expect(got.View).To(gomega.Equal("config"))
		gomega.Expect(got.InstallationID).To(gomega.Equal("local-test"))
		gomega.Expect(string(got.Config)).To(gomega.Equal(`{"schema":"haepari.config/v1"}`))
	})

	ginkgo.It("refuses a non-envelope and a view that is not config", func() {
		transport := &projectionTransport{status: http.StatusNotFound, body: []byte(`{"nope":true}`)}
		c := &Client{http: &http.Client{Transport: transport}}
		_, err := c.AcceptedConfiguration(context.Background())
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("accepted configuration projection is unavailable; omitted phase budgets cannot be filled")))

		wrong, err := json.Marshal(map[string]any{"schema": "haepari.operator-projection/v1", "view": "diagnostic", "config": json.RawMessage(`{}`)})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		body, err := json.Marshal(Envelope{Schema: "haepari.envelope/v1", Operation: "operator.projection", OK: true, Data: wrong})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		c = &Client{http: &http.Client{Transport: &projectionTransport{body: body}}}
		_, err = c.AcceptedConfiguration(context.Background())
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("accepted configuration projection is unavailable; omitted phase budgets cannot be filled")))
	})
})

var _ = ginkgo.Describe("OperatorDiagnostic", ginkgo.Label("unit"), func() {
	ginkgo.It("requests one section and refuses a body that is not that diagnostic", func() {
		view, err := json.Marshal(map[string]any{
			"schema": "haepari.operator-diagnostic/v1", "view": "diagnostic", "section": "queue",
			"installation_id": "local-test", "observed_at": "2026-10-17T00:00:00Z",
			"any_unknown_occupancy": false,
			"current_config":        map[string]any{"status": "present", "revision": 1},
			"page":                  map[string]any{"limit": 10, "more": false},
			"resources":             []any{},
		})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		body, err := json.Marshal(Envelope{Schema: "haepari.envelope/v1", Operation: "operator.projection", OK: true, Data: view})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		transport := &projectionTransport{body: body}
		c := &Client{http: &http.Client{Transport: transport}}
		got, _, err := c.OperatorDiagnostic(context.Background(), "queue", "local-a", "cursor-1", 2)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(transport.path).To(gomega.ContainSubstring("/v1/operator/projection?"))
		gomega.Expect(transport.path).To(gomega.ContainSubstring("view=diagnostic"))
		gomega.Expect(transport.path).To(gomega.ContainSubstring("section=queue"))
		gomega.Expect(transport.path).To(gomega.ContainSubstring("resource=local-a"))
		gomega.Expect(transport.path).To(gomega.ContainSubstring("cursor=cursor-1"))
		gomega.Expect(transport.path).To(gomega.ContainSubstring("limit=2"))
		gomega.Expect(transport.path).NotTo(gomega.ContainSubstring("worker"))
		gomega.Expect(got.Schema).To(gomega.Equal("haepari.operator-diagnostic/v1"))
		gomega.Expect(got.Section).To(gomega.Equal("queue"))

		wrong, err := json.Marshal(map[string]any{"schema": "haepari.operator-projection/v1", "view": "config", "section": "queue", "config": json.RawMessage(`{}`)})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		body, err = json.Marshal(Envelope{Schema: "haepari.envelope/v1", Operation: "operator.projection", OK: true, Data: wrong})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		c = &Client{http: &http.Client{Transport: &projectionTransport{body: body}}}
		_, _, err = c.OperatorDiagnostic(context.Background(), "queue", "", "", 0)
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("operator diagnostic projection is unavailable")))

		refused := &projectionTransport{status: http.StatusBadRequest, body: []byte(`{"schema":"haepari.envelope/v1","operation":"operator.projection","ok":false,"error":{"code":"INVALID_REQUEST","message":"diagnostic limit","retry":"none"}}`)}
		c = &Client{http: &http.Client{Transport: refused}}
		_, _, err = c.OperatorDiagnostic(context.Background(), "queue", "", "", -1)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(refused.path).To(gomega.ContainSubstring("limit=-1"))
		omitted := &projectionTransport{body: body}
		c = &Client{http: &http.Client{Transport: omitted}}
		_, _, err = c.OperatorDiagnostic(context.Background(), "queue", "", "", 0)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(omitted.path).NotTo(gomega.ContainSubstring("limit="))
	})
})
