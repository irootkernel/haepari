package client

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

type lockCapabilityTransport struct{ calls int }

func (t *lockCapabilityTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return nil, fmt.Errorf("unexpected lock transport call")
}

var _ = ginkgo.Describe("Task035LockCapability", ginkgo.Label("unit"), func() {
	ginkgo.It("refuses advisory operations against an older negotiated controller before any request", func() {
		transport := &lockCapabilityTransport{}
		c := &Client{http: &http.Client{Transport: transport}, Hello: Handshake{InstallationID: "old", Capabilities: []string{"managed-execution/v1"}}}
		ctx := context.Background()
		operations := []func() error{
			func() error {
				_, _, err := c.AcquireLock(ctx, managed.LockAcquireRequest{LockID: "lock", RequestKey: "key", Holder: "owner"})
				return err
			},
			func() error { _, _, err := c.WaitLock(ctx, "request", time.Second); return err },
			func() error { _, _, err := c.RenewLock(ctx, "grant"); return err },
			func() error { _, _, err := c.ReleaseLock(ctx, "grant", false, ""); return err },
			func() error { _, _, err := c.CancelLock(ctx, "request", false, ""); return err },
			func() error { _, _, err := c.LocksList(ctx, "lock"); return err },
			func() error {
				_, err := c.Register(ctx, []byte(`{"schema":"haepari.resource/v1","id":"lock","kind":"lock"}`))
				return err
			},
			func() error { _, err := c.LockResourceAction(ctx, "remove", "lock", nil, 1, false); return err },
		}
		for _, operation := range operations {
			gomega.Expect(operation()).To(gomega.MatchError("UNSUPPORTED"))
		}
		gomega.Expect(transport.calls).To(gomega.BeZero())
	})
})
