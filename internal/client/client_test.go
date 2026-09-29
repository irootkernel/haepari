package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
)

type responseTransport struct{ body []byte }

func (r responseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(r.body)), Header: make(http.Header)}, nil
}

func TestWaitReadsLargestValidOutputInventory(t *testing.T) {
	artifacts := make([]managed.ResultArtifact, 1000)
	for i := range artifacts {
		artifacts[i] = managed.ResultArtifact{
			Path:      strings.Repeat("segment/", 117) + strings.Repeat("x", 8) + string(rune('a'+i%26)) + strings.Repeat("y", i/26),
			SizeBytes: 1,
			SHA256:    strings.Repeat("a", 64),
		}
	}
	body, err := json.Marshal(Envelope{Schema: "haepari.envelope/v1", Operation: "wait", OK: true, Data: mustJSON(t, managed.Result{Schema: "haepari.result/v1", JobID: "job-1", Artifacts: artifacts})})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 1<<20 {
		t.Fatalf("response did not cross old limit: %d", len(body))
	}
	c := &Client{http: &http.Client{Transport: responseTransport{body: body}}}
	result, _, err := c.Wait(context.Background(), "job-1", time.Second)
	if err != nil || len(result.Artifacts) != 1000 {
		t.Fatalf("large truthful result lost: %d artifacts, %v", len(result.Artifacts), err)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
