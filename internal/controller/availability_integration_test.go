//go:build integration

package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func availabilityFixture(t ginkgo.FullGinkgoTInterface) (*runtimeServer, *client.Client, *sql.DB) {
	t.Helper()
	parent, err := os.MkdirTemp("/tmp", "hp-availability-")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	root, err := state.Resolve(filepath.Join(parent, "state"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(root.Prepare()).To(gomega.Succeed())
	gomega.Expect(os.Mkdir(filepath.Join(root.Path, "jobs"), 0700)).To(gomega.Succeed())
	db, err := sqlite.Open(root.DBPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	t.Cleanup(func() { _ = db.Close() })
	l, err := managed.NewLedger(db, 1)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	store, err := snapshot.OpenStore(root.Path)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	runtime, err := newRuntime(root, l, store)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: root.SocketPath, Net: "unix"})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(os.Chmod(root.SocketPath, 0600)).To(gomega.Succeed())
	server := &http.Server{Handler: runtime.routes()}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	c, err := client.Connect(context.Background(), root)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	t.Cleanup(c.Close)
	r := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "local-a", Kind: "local", ExecutionMode: "managed", Capacity: 2, Revision: 1, Adapter: managed.Adapter{QualificationReference: "isolated-development"}, Cleanup: &managed.CleanupPolicy{PolicyReference: "owned-workspace"}}
	gomega.Expect(l.RegisterResource(mustJSONScheduling(r))).To(gomega.Succeed())
	digest := strings.Repeat("a", 64)
	job := managed.JobSpec{Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: r.ID, Mode: "auto", Suite: "availability", Input: managed.Input{Root: "/tmp/isolated-input", InventoryDigest: digest}, Environment: managed.Environment{ProfileID: "development", ProfileDigest: digest}, Workload: managed.Workload{Argv: []string{"/usr/bin/true"}, CWD: "source", RequiredOutputs: []string{"results/build.json"}}, Deadlines: managed.Deadlines{QueueSeconds: 600, PrepareSeconds: 10, RunSeconds: 10, CollectSeconds: 10, CleanupSeconds: 10}, Effects: managed.Effects{Mode: "offline", AuthorizationReference: "isolated-ledger-fixture"}, Provenance: managed.Provenance{CandidateDigest: digest, TestsDigest: digest, TargetProfileDigest: digest, EvidenceClass: "development"}}
	// This fixture exercises availability/event transactions, without dispatch.
	_, err = l.Admit(mustJSONScheduling(job), digest, "queued-request", "queued-job")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return runtime, c, db
}

func availabilityCounts(db *sql.DB) (int, int) {
	var receipts, events int
	gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_availability_receipts`).Scan(&receipts)).To(gomega.Succeed())
	gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE kind='resource_availability'`).Scan(&events)).To(gomega.Succeed())
	return receipts, events
}

var _ = ginkgo.Describe("Task007AvailabilityRecovery", ginkgo.Label("integration"), func() {
	ginkgo.It("names missing, non-directory and symlink jobs paths without consuming a resume intent", func() {
		runtime, c, db := availabilityFixture(ginkgo.GinkgoT())
		q := managed.AvailabilityRequest{MutationKey: "pause", ResourceID: "local-a", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: "maintenance"}
		paused, _, err := c.SetAvailability(context.Background(), q, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q.MutationKey, q.ExpectedAvailabilityRevision, q.Reason = "unready-resume", 2, ""
		jobs := filepath.Join(runtime.root.Path, "jobs")
		gomega.Expect(os.Remove(jobs)).To(gomega.Succeed())
		for _, kind := range []string{"missing", "not a directory", "symlink"} {
			switch kind {
			case "not a directory":
				gomega.Expect(os.WriteFile(jobs, []byte("not a directory"), 0600)).To(gomega.Succeed())
			case "symlink":
				gomega.Expect(os.Remove(jobs)).To(gomega.Succeed())
				gomega.Expect(os.Symlink(runtime.root.Path, jobs)).To(gomega.Succeed())
			}
			_, _, err := c.SetAvailability(context.Background(), q, true)
			var issue *client.Issue
			gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
			gomega.Expect(issue.Code).To(gomega.Equal("RECOVERY_BLOCKED"))
			gomega.Expect(issue.Message).To(gomega.ContainSubstring(kind))
			gomega.Expect(issue.Message).NotTo(gomega.ContainSubstring(runtime.root.Path))
			status, _, err := c.InspectResource(context.Background(), q.ResourceID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			paused.Mutation = nil // Inspection reports current state, not a mutation receipt.
			gomega.Expect(status).To(gomega.Equal(paused))
			receipts, events := availabilityCounts(db)
			gomega.Expect(receipts).To(gomega.Equal(1))
			gomega.Expect(events).To(gomega.Equal(1))
		}
	})

	ginkgo.It("commits simultaneous identical Unix pause and resume requests once each", func() {
		_, c, db := availabilityFixture(ginkgo.GinkgoT())
		for _, resume := range []bool{false, true} {
			q := managed.AvailabilityRequest{MutationKey: "pause", ResourceID: "local-a", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: "maintenance"}
			if resume {
				q.MutationKey, q.ExpectedAvailabilityRevision, q.Reason = "resume", 2, ""
			}
			type reply struct {
				status managed.ResourceStatus
				err    error
			}
			start := make(chan struct{})
			replies := make(chan reply, 8)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			for range 8 {
				go func() { <-start; r, _, err := c.SetAvailability(ctx, q, resume); replies <- reply{r, err} }()
			}
			close(start)
			var original *managed.AvailabilityIdentity
			for range 8 {
				r := <-replies
				gomega.Expect(r.err).NotTo(gomega.HaveOccurred())
				gomega.Expect(r.status.AvailabilityRevision).To(gomega.Equal(q.ExpectedAvailabilityRevision + 1))
				gomega.Expect(r.status.Mutation).NotTo(gomega.BeNil())
				if original == nil {
					original = r.status.Mutation
				}
				gomega.Expect(r.status.Mutation).To(gomega.Equal(original))
			}
			cancel()
			receipts, events := availabilityCounts(db)
			gomega.Expect(receipts).To(gomega.Equal(int(q.ExpectedAvailabilityRevision)))
			gomega.Expect(events).To(gomega.Equal(receipts))
		}
	})

	ginkgo.It("recovers a concurrent commit after its first replay lookup missed and readiness subsequently failed", func() {
		runtime, c, db := availabilityFixture(ginkgo.GinkgoT())
		q := managed.AvailabilityRequest{MutationKey: "pause", ResourceID: "local-a", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: "maintenance"}
		_, _, err := c.SetAvailability(context.Background(), q, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q.MutationKey, q.ExpectedAvailabilityRevision, q.Reason = "racing-resume", 2, ""
		missed, release := make(chan struct{}), make(chan struct{})
		var calls atomic.Int32
		runtime.beforeAvailabilityObservation = func() {
			if calls.Add(1) == 1 {
				close(missed)
				<-release
			}
		}
		type reply struct {
			status managed.ResourceStatus
			err    error
		}
		first := make(chan reply, 1)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Always release the handler on assertion failure, so server cleanup can finish.
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		go func() { r, _, err := c.SetAvailability(ctx, q, true); first <- reply{r, err} }()
		select {
		case <-missed:
		case <-ctx.Done():
			ginkgo.Fail("first request did not reach the replay-miss barrier")
		}
		committed, _, err := c.SetAvailability(ctx, q, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.Chmod(filepath.Join(runtime.root.Path, "jobs"), 0755)).To(gomega.Succeed())
		close(release)
		released = true
		recovered := <-first
		gomega.Expect(recovered.err).NotTo(gomega.HaveOccurred())
		gomega.Expect(recovered.status).To(gomega.Equal(committed))
		gomega.Expect(calls.Load()).To(gomega.Equal(int32(2)))
		receipts, events := availabilityCounts(db)
		gomega.Expect(receipts).To(gomega.Equal(2))
		gomega.Expect(events).To(gomega.Equal(2))
	})

	ginkgo.It("rejects mismatched resource paths and unknown actions without consuming keys or events", func() {
		runtime, c, db := availabilityFixture(ginkgo.GinkgoT())
		before, _, err := c.InspectResource(context.Background(), "local-a")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		h := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", runtime.root.SocketPath)
		}}}
		defer h.CloseIdleConnections()
		for _, path := range []string{"/v1/resources/other/pause", "/v1/resources/local-a/unknown"} {
			q := managed.AvailabilityRequest{MutationKey: "invalid-path", ResourceID: "local-a", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: "maintenance"}
			r, err := http.NewRequest("POST", "http://haepari"+path, strings.NewReader(string(mustJSONScheduling(q))))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			r.Header.Set("X-Haepari-Protocol", protocol)
			r.Header.Set("X-Haepari-Protocol-Min", "1")
			r.Header.Set("X-Haepari-Protocol-Max", "1")
			response, err := h.Do(r)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(response.StatusCode).To(gomega.Equal(http.StatusBadRequest))
			var envelope client.Envelope
			gomega.Expect(json.NewDecoder(response.Body).Decode(&envelope)).To(gomega.Succeed())
			response.Body.Close()
			gomega.Expect(envelope.Error.Code).To(gomega.Equal("INVALID_REQUEST"))
		}
		after, _, err := c.InspectResource(context.Background(), "local-a")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
		receipts, events := availabilityCounts(db)
		gomega.Expect(receipts).To(gomega.BeZero())
		gomega.Expect(events).To(gomega.BeZero())
	})

	ginkgo.It("reports actionable readiness and availability causes and accepts byte-boundary pause reasons", func() {
		runtime, c, db := availabilityFixture(ginkgo.GinkgoT())
		q := managed.AvailabilityRequest{MutationKey: "pause", ResourceID: "local-a", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: strings.Repeat("é", 128)}
		_, _, err := c.SetAvailability(context.Background(), q, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q.MutationKey, q.ExpectedAvailabilityRevision, q.Reason = "resume", 2, ""
		jobs := filepath.Join(runtime.root.Path, "jobs")
		gomega.Expect(os.Chmod(jobs, 0755)).To(gomega.Succeed())
		_, _, err = c.SetAvailability(context.Background(), q, true)
		var issue *client.Issue
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("RECOVERY_BLOCKED"))
		gomega.Expect(issue.Message).To(gomega.ContainSubstring("group or other access"))
		gomega.Expect(os.Chmod(jobs, 0700)).To(gomega.Succeed())
		_, _, err = c.SetAvailability(context.Background(), q, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q.MutationKey, q.ExpectedAvailabilityRevision = "already-available", 3
		_, _, err = c.SetAvailability(context.Background(), q, true)
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("RECOVERY_BLOCKED"))
		gomega.Expect(issue.Message).To(gomega.ContainSubstring("requires paused; the resource is available"))
		q.MutationKey, q.Reason = "one-byte-pause", "x"
		_, _, err = c.SetAvailability(context.Background(), q, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		receipts, events := availabilityCounts(db)
		gomega.Expect(receipts).To(gomega.Equal(3))
		gomega.Expect(events).To(gomega.Equal(3))
	})
})
