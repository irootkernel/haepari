//go:build integration

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task008ClientProtocol", ginkgo.Label("integration"), func() {
	ginkgo.It("refuses an unimplemented selected version or missing capability before exposing a mutating client", func() {
		parent, err := os.MkdirTemp("/tmp", "hp-range-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer os.RemoveAll(parent)
		root, err := state.Resolve(filepath.Join(parent, "state"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(root.Prepare()).To(gomega.Succeed())
		for _, hello := range []client.Handshake{
			{Protocol: protocol, Version: 3, InstallationID: "fixture", Capabilities: []string{protocol}},
			{Protocol: protocol, Version: 1, InstallationID: "fixture", Capabilities: []string{"unrelated/v1"}},
		} {
			listener, err := net.Listen("unix", root.SocketPath)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				send(w, 200, "hello", hello, nil)
			})}
			done := make(chan error, 1)
			go func() { done <- server.Serve(listener) }()
			c, err := client.ConnectRange(context.Background(), root, 1, 3)
			gomega.Expect(server.Close()).To(gomega.Succeed())
			<-done
			gomega.Expect(c).To(gomega.BeNil())
			var issue *client.Issue
			gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
			gomega.Expect(issue.Code).To(gomega.Equal("PROTOCOL_UNSUPPORTED"))
		}
	})
	ginkgo.It("negotiates a range and immutable installation ID, refusing every unsupported mutation before effects", func() {
		t := ginkgo.GinkgoT()
		s, c, db := availabilityFixture(t)
		ranged, e := client.ConnectRange(context.Background(), s.root, 1, 3)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		defer ranged.Close()
		gomega.Expect(ranged.Hello.Version).To(gomega.Equal(1))
		gomega.Expect(ranged.Hello.InstallationID).To(gomega.Equal(c.Hello.InstallationID))
		gomega.Expect(ranged.Hello.Capabilities).To(gomega.ConsistOf("managed-execution/v1", "durable-observation/v1", "advisory-lock/v1", "managed-recovery/v1", "artifact-delivery/v1", "evidence-retention/v1"))
		_, e = client.ConnectRange(context.Background(), s.root, 2, 3)
		var issue *client.Issue
		gomega.Expect(errors.As(e, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("PROTOCOL_UNSUPPORTED"))
		before, e := s.ledger.Snapshot("queued-job")
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		var events, mutations, resources int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&events)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_mutations`).Scan(&mutations)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_resources`).Scan(&resources)).To(gomega.Succeed())
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", s.root.SocketPath)
		}}
		defer transport.CloseIdleConnections()
		hc := &http.Client{Transport: transport}
		for _, headers := range [][2]string{{"", ""}, {"2", "3"}, {"0", "1"}, {"3", "2"}, {"bad", "1"}} {
			for _, path := range []string{"/v1/jobs", "/v1/resources", "/v1/jobs/queued-job/cancel", "/v1/resources/local-a/pause"} {
				req, e := http.NewRequest("POST", "http://haepari"+path, bytes.NewReader([]byte(`{}`)))
				gomega.Expect(e).NotTo(gomega.HaveOccurred())
				req.Header.Set("X-Haepari-Protocol", protocol)
				req.Header.Set("X-Haepari-Protocol-Min", headers[0])
				req.Header.Set("X-Haepari-Protocol-Max", headers[1])
				response, e := hc.Do(req)
				gomega.Expect(e).NotTo(gomega.HaveOccurred())
				raw, e := io.ReadAll(response.Body)
				response.Body.Close()
				gomega.Expect(e).NotTo(gomega.HaveOccurred())
				var envelope client.Envelope
				gomega.Expect(json.Unmarshal(raw, &envelope)).To(gomega.Succeed())
				gomega.Expect(envelope.Error.Code).To(gomega.Equal("PROTOCOL_UNSUPPORTED"))
			}
		}
		after, e := s.ledger.Snapshot("queued-job")
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
		var current int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&current)).To(gomega.Succeed())
		gomega.Expect(current).To(gomega.Equal(events))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_mutations`).Scan(&current)).To(gomega.Succeed())
		gomega.Expect(current).To(gomega.Equal(mutations))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_resources`).Scan(&current)).To(gomega.Succeed())
		gomega.Expect(current).To(gomega.Equal(resources))
	})
	ginkgo.It("closes subscribe/read races using the durable predicate without an HTTP terminal signal", func() {
		t := ginkgo.GinkgoT()
		s, c, _ := availabilityFixture(t)
		entered := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		s.beforeWaitRead = func() { once.Do(func() { close(entered); <-release }) }
		done := make(chan error, 1)
		go func() { _, _, e := c.Wait(context.Background(), "queued-job", time.Second); done <- e }()
		select {
		case <-entered:
		case <-time.After(time.Second):
			ginkgo.Fail("wait did not subscribe")
		}
		_, e := s.ledger.CancelQueued("queued-job", 1, "direct-cancel")
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		close(release)
		select {
		case e := <-done:
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
		case <-time.After(time.Second):
			ginkgo.Fail("lost committed terminal wake")
		}
	})
	ginkgo.It("does not let unrelated commits restart a caller wait deadline or disconnect cancel a Job", func() {
		t := ginkgo.GinkgoT()
		s, c, _ := availabilityFixture(t)
		ctx, stop := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, _, e := c.Wait(ctx, "queued-job", time.Second); done <- e }()
		stop()
		gomega.Eventually(done, time.Second).Should(gomega.Receive())
		before, e := s.ledger.Snapshot("queued-job")
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(before.JobState).To(gomega.Equal("queued"))
		started := time.Now()
		commitsDone := make(chan struct{})
		go func() {
			defer close(commitsDone)
			for i := 0; i < 30; i++ {
				_, _ = s.ledger.BindInstallation("same-local")
				time.Sleep(5 * time.Millisecond)
			}
		}()
		_, _, e = c.Wait(context.Background(), "queued-job", 70*time.Millisecond)
		var issue *client.Issue
		gomega.Expect(errors.As(e, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("WAIT_TIMEOUT"))
		gomega.Expect(time.Since(started)).To(gomega.BeNumerically("<", 500*time.Millisecond))
		after, e := s.ledger.Snapshot("queued-job")
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
		<-commitsDone
	})
	ginkgo.It("returns native filtered pages and terminal observations independently of the Job verdict", func() {
		t := ginkgo.GinkgoT()
		s, c, _ := availabilityFixture(t)
		page, _, e := c.Jobs(context.Background(), managed.JobsQuery{Resource: "local-a", State: "queued", Limit: 1})
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(page.Jobs).To(gomega.HaveLen(1))
		gomega.Expect(page.Jobs[0].QueuePosition).To(gomega.Equal(int64(1)))
		resources, _, e := c.Resources(context.Background(), "", 1)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(resources.Resources).To(gomega.HaveLen(1))
		for _, path := range []string{"/v1/jobs?limit=1&limit=2", "/v1/jobs?limit=", "/v1/jobs?unknown=true", "/v1/resources?state=queued", "/v1/resources?cursor=x&cursor=y", "/v1/jobs?limit=1;state=queued"} {
			request, err := http.NewRequest("GET", "http://haepari"+path, nil)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			request.Header.Set("X-Haepari-Protocol", protocol)
			request.Header.Set("X-Haepari-Protocol-Min", "1")
			request.Header.Set("X-Haepari-Protocol-Max", "1")
			transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", s.root.SocketPath)
			}}
			response, err := (&http.Client{Transport: transport}).Do(request)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var rejected client.Envelope
			gomega.Expect(json.NewDecoder(response.Body).Decode(&rejected)).To(gomega.Succeed())
			response.Body.Close()
			transport.CloseIdleConnections()
			gomega.Expect(rejected.Error).NotTo(gomega.BeNil())
			gomega.Expect(rejected.Error.Code).To(gomega.Equal("INVALID_REQUEST"))
		}
		_, e = s.ledger.CancelQueued("queued-job", 1, "cancel")
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		inspection, envelope, e := c.Inspect(context.Background(), "queued-job")
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(envelope.OK).To(gomega.BeTrue())
		gomega.Expect(inspection.Result.FinalVerdict).To(gomega.Equal(managed.Cancelled))
	})
})
