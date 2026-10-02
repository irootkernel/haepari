//go:build integration

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

func observerTestLog(root, id string, data []byte) string {
	path := filepath.Join(root, "jobs", id)
	gomega.Expect(os.MkdirAll(path, 0700)).To(gomega.Succeed())
	path = filepath.Join(path, "output.log")
	gomega.Expect(os.WriteFile(path, data, 0600)).To(gomega.Succeed())
	return path
}

func observerTestIdentity(path, runID string) {
	file, err := os.Open(path)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	st, err := privateLogStat(int(file.Fd()), false)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(file.Close()).To(gomega.Succeed())
	// These reader fixtures deliberately replace or corrupt bindings. They do
	// not exercise the immutable production publisher.
	raw, err := json.Marshal(outputIdentity{Schema: "haepari.output-identity/v1", JobID: filepath.Base(filepath.Dir(path)), RunID: runID, Dev: uint64(st.Dev), Inode: st.Ino})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(os.WriteFile(filepath.Join(filepath.Dir(path), "output-identity.json"), raw, 0600)).To(gomega.Succeed())
}

func observerContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func observerClaimRun(s *runtimeServer) managed.Receipt {
	r, err := s.ledger.Reserve(managed.ReserveRequest{JobID: "queued-job", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "observer-reserve"})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	r, err = s.ledger.AdvanceLocal(r.JobID, r.JobRevision, "preparing", "ready", "observer-ready")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	r, err = s.ledger.StartLocal(r.JobID, r.JobRevision, "observer-run")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return r
}

func observerBoundLog(s *runtimeServer, r managed.Receipt, data []byte) string {
	path := observerTestLog(s.root.Path, r.JobID, data)
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(s.bindLocalLog(r, "observer-run", file)).To(gomega.Succeed())
	gomega.Expect(file.Close()).To(gomega.Succeed())
	return path
}

func observerPublishBoundLog(s *runtimeServer, data []byte) {
	r, err := s.ledger.Reserve(managed.ReserveRequest{JobID: "queued-job", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "publication-reserve"})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	control, err := s.registerLocal(r)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ginkgo.GinkgoT().Cleanup(func() { s.unregisterLocal(control) })
	control.mu.Lock()
	defer control.mu.Unlock()
	r, err = s.ledger.AdvanceLocal(r.JobID, r.JobRevision, "preparing", "ready", "publication-ready")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	r, err = s.ledger.StartLocal(r.JobID, r.JobRevision, "observer-run")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	observerBoundLog(s, r, data)
	control.receipt = r
	control.identity, err = s.ledger.LocalIdentity(r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

// These are ledger/file observations, without running a workload or provider.
func observerFinishRun(s *runtimeServer, r managed.Receipt) {
	id, err := s.ledger.LocalIdentity(r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	code := 0
	r, err = s.ledger.RecordLocalOutcome(managed.LocalOutcomeRequest{Identity: id, ExpectedRevision: r.JobRevision, Outcome: managed.LocalOutcome{Started: true, WorkloadOutcome: "pass", NativeExitCode: &code}, MutationKey: "observer-outcome"})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	r, err = s.ledger.CompleteLocalRun(managed.LocalTerminationRequest{Identity: id, ExpectedRevision: r.JobRevision, MutationKey: "observer-stopped"})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	r, err = s.ledger.AdvanceLocal(r.JobID, r.JobRevision, "collecting", "cleaning", "observer-collected")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	r, err = s.ledger.AdvanceLocal(r.JobID, r.JobRevision, "cleaning", "settling", "observer-cleaned")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = s.ledger.SettleLocal(r.JobID, r.JobRevision, managed.Result{Schema: "haepari.result/v1", JobID: r.JobID, ResourceID: r.ResourceID, ResourceRevision: r.ResourceConfigRevision, LeaseGeneration: r.LeaseGeneration, JobState: "finished", ResourceAvailability: "available", WorkloadOutcome: "pass", EvidenceCompleteness: "complete", CleanupOutcome: "verified", FinalVerdict: managed.Pass, NativeExitCode: &code, Artifacts: []managed.ResultArtifact{{Path: "results/build.json", SHA256: strings.Repeat("a", 64)}}})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

var _ = ginkgo.Describe("Task008Observers", ginkgo.Label("integration"), func() {
	ginkgo.It("drains bounded terminal event pages and explicitly resyncs purged history", func() {
		s, c, _ := availabilityFixture(ginkgo.GinkgoT())
		ctx, cancel := observerContext()
		defer cancel()
		finished, err := s.ledger.CancelQueued("queued-job", 1, "observer-cancel")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		first, envelope, err := c.Events(ctx, "queued-job", client.EventsOptions{Limit: 1})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(envelope.Operation).To(gomega.Equal("events"))
		gomega.Expect(first.More).To(gomega.BeTrue())
		gomega.Expect(first.Snapshot.JobState).To(gomega.Equal("finished"))
		var types []string
		gomega.Expect(c.FollowEvents(ctx, "queued-job", client.EventsOptions{Limit: 1}, func(r client.Record) error {
			types = append(types, r.Type)
			return nil
		})).To(gomega.Succeed())
		gomega.Expect(types).To(gomega.Equal([]string{"page", "page", "end"}))
		gomega.Expect(s.ledger.PurgeEventsThrough("queued-job", finished.EventCursor)).To(gomega.Succeed())
		checkpoint, err := s.ledger.EventsAfter("queued-job", 0, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(checkpoint.Resync).NotTo(gomega.BeNil())
		types = nil
		gomega.Expect(c.FollowEvents(ctx, "queued-job", client.EventsOptions{}, func(r client.Record) error {
			types = append(types, r.Type)
			if r.Events != nil {
				gomega.Expect(r.Events.NextCursor).To(gomega.Equal(checkpoint.CurrentCursor))
			}
			return nil
		})).To(gomega.Succeed())
		gomega.Expect(types).To(gomega.Equal([]string{"resync", "end"}))
	})

	ginkgo.It("does not lose a commit between the initial page and blocking registration", func() {
		s, c, _ := availabilityFixture(ginkgo.GinkgoT())
		ctx, cancel := observerContext()
		defer cancel()
		var types []string
		gomega.Expect(c.FollowEvents(ctx, "queued-job", client.EventsOptions{}, func(r client.Record) error {
			types = append(types, r.Type)
			if len(types) == 1 {
				_, err := s.ledger.CancelQueued("queued-job", 1, "observer-race-cancel")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			return nil
		})).To(gomega.Succeed())
		gomega.Expect(types).To(gomega.Equal([]string{"page", "page", "end"}))
	})

	ginkgo.It("keeps a full valid event page bounded when payload contains HTML bytes", func() {
		s, c, db := availabilityFixture(ginkgo.GinkgoT())
		payload := []byte(`{"text":"` + strings.Repeat("<>", ((2<<20)-1024)/12) + `"}`)
		_, err := db.Exec(`UPDATE managed_events SET payload_json=? WHERE job_id='queued-job'`, payload)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = s.ledger.CancelQueued("queued-job", 1, "large-observer-cancel")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ctx, cancel := observerContext()
		defer cancel()
		page, _, err := c.Events(ctx, "queued-job", client.EventsOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(page.Events[0].Payload).To(gomega.Equal(json.RawMessage(payload)))
		gomega.Expect(c.FollowEvents(ctx, "queued-job", client.EventsOptions{}, func(client.Record) error { return nil })).To(gomega.Succeed())
	})

	ginkgo.It("returns storage uncertainty for missing owned events and refuses unknown cursors", func() {
		_, c, db := availabilityFixture(ginkgo.GinkgoT())
		ctx, cancel := observerContext()
		defer cancel()
		_, _, err := c.Events(ctx, "queued-job", client.EventsOptions{After: 999999})
		var issue *client.Issue
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("INVALID_REQUEST"))
		gomega.Expect(db.Exec(`DELETE FROM managed_events WHERE job_id='queued-job'`)).Error().NotTo(gomega.HaveOccurred())
		_, _, err = c.Events(ctx, "queued-job", client.EventsOptions{})
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("STORAGE_UNAVAILABLE"))
	})
	ginkgo.It("delivers scoped storage uncertainty after an actual event stream is established without changing Job ownership", func() {
		s, c, db := availabilityFixture(ginkgo.GinkgoT())
		before, err := s.ledger.Snapshot("queued-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ctx, cancel := observerContext()
		defer cancel()
		var records []client.Record
		var checkpoint int64
		err = c.FollowEvents(ctx, "queued-job", client.EventsOptions{Limit: 1}, func(record client.Record) error {
			records = append(records, record)
			if len(records) == 1 {
				gomega.Expect(record.Type).To(gomega.Equal("page"))
				gomega.Expect(record.Events.More).To(gomega.BeFalse())
				checkpoint = record.Cursor
				_, err := db.Exec(`DELETE FROM managed_events WHERE job_id=? AND cursor=?`, "queued-job", checkpoint)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				// An unrelated real commit wakes the established follower. The
				// subscribe-before-read generation also covers this callback race.
				_, err = s.ledger.BindInstallation(s.installationID())
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			return nil
		})
		var issue *client.Issue
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("STORAGE_UNAVAILABLE"))
		gomega.Expect(issue.Retry).To(gomega.Equal("reconcile"))
		gomega.Expect(records).To(gomega.HaveLen(2))
		failure := records[1]
		gomega.Expect(failure.Schema).To(gomega.Equal(client.ObserverSchema))
		gomega.Expect(failure.Type).To(gomega.Equal("error"))
		gomega.Expect(failure.JobID).To(gomega.Equal("queued-job"))
		gomega.Expect(failure.Cursor).To(gomega.Equal(checkpoint))
		gomega.Expect(failure.Error).To(gomega.Equal(issue))
		after, err := s.ledger.Snapshot("queued-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
	})
	ginkgo.It("refuses unexplained above-floor payload loss for every purge reconnect cursor", func() {
		s, c, db := availabilityFixture(ginkgo.GinkgoT())
		ctx, cancel := observerContext()
		defer cancel()
		finished, err := s.ledger.CancelQueued("queued-job", 1, "purge-loss-cancel")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		first, _, err := c.Events(ctx, "queued-job", client.EventsOptions{Limit: 1})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		floor := first.NextCursor
		gomega.Expect(s.ledger.PurgeEventsThrough("queued-job", floor)).To(gomega.Succeed())
		_, err = db.Exec(`DELETE FROM managed_events WHERE job_id=? AND cursor>?`, "queued-job", floor)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, after := range []int64{0, floor, finished.EventCursor} {
			_, _, err := c.Events(ctx, "queued-job", client.EventsOptions{After: after})
			var issue *client.Issue
			gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
			gomega.Expect(issue.Code).To(gomega.Equal("STORAGE_UNAVAILABLE"))
			consumed := 0
			err = c.FollowEvents(ctx, "queued-job", client.EventsOptions{After: after}, func(client.Record) error { consumed++; return nil })
			gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
			gomega.Expect(issue.Code).To(gomega.Equal("STORAGE_UNAVAILABLE"))
			gomega.Expect(consumed).To(gomega.BeZero())
		}
	})

	ginkgo.It("disconnects an observer without changing job state or capacity", func() {
		s, c, _ := availabilityFixture(ginkgo.GinkgoT())
		ctx, cancel := observerContext()
		defer cancel()
		before, err := s.ledger.Snapshot("queued-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		stop := errors.New("observer closed")
		gomega.Expect(c.FollowEvents(ctx, "queued-job", client.EventsOptions{}, func(client.Record) error { return stop })).To(gomega.MatchError(stop))
		after, err := s.ledger.Snapshot("queued-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
	})

	ginkgo.It("returns exact base64 log pages and drains terminal bytes before end", func() {
		s, c, _ := availabilityFixture(ginkgo.GinkgoT())
		data := bytes.Repeat([]byte{0xff, 0, '\n', 0xfe}, logPageBytes/4+4)
		r := observerClaimRun(s)
		observerBoundLog(s, r, data)
		ctx, cancel := observerContext()
		defer cancel()
		observerFinishRun(s, r)
		page, envelope, err := c.Logs(ctx, "queued-job", client.LogsOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(envelope.Operation).To(gomega.Equal("logs"))
		gomega.Expect(page.Data).To(gomega.Equal(data[:logPageBytes]))
		gomega.Expect(page.NextOffset).To(gomega.Equal(int64(logPageBytes)))
		gomega.Expect(page.More).To(gomega.BeTrue())
		var got []byte
		var types []string
		gomega.Expect(c.FollowLogs(ctx, "queued-job", client.LogsOptions{}, func(r client.Record) error {
			types = append(types, r.Type)
			if r.Logs != nil {
				got = append(got, r.Logs.Data...)
			}
			return nil
		})).To(gomega.Succeed())
		gomega.Expect(got).To(gomega.Equal(data))
		gomega.Expect(types).To(gomega.Equal([]string{"page", "page", "end"}))
	})

	ginkgo.It("observes a direct file append without a ledger commit and does not cancel on disconnect", func() {
		s, c, _ := availabilityFixture(ginkgo.GinkgoT())
		r := observerClaimRun(s)
		path := observerBoundLog(s, r, nil)
		ctx, cancel := observerContext()
		defer cancel()
		before, err := s.ledger.Snapshot("queued-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		stop := errors.New("log consumed")
		var got []byte
		gomega.Expect(c.FollowLogs(ctx, "queued-job", client.LogsOptions{}, func(r client.Record) error {
			if r.Logs != nil && r.Logs.NextOffset == 0 {
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				_, err = file.Write([]byte{0xff, 0, 0xfe})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(file.Close()).To(gomega.Succeed())
			} else if r.Logs != nil {
				got = append(got, r.Logs.Data...)
				return stop
			}
			return nil
		})).To(gomega.MatchError(stop))
		gomega.Expect(got).To(gomega.Equal([]byte{0xff, 0, 0xfe}))
		after, err := s.ledger.Snapshot("queued-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
	})

	ginkgo.It("emits a terminal snapshot when an empty active log ends", func() {
		s, c, _ := availabilityFixture(ginkgo.GinkgoT())
		r := observerClaimRun(s)
		observerBoundLog(s, r, nil)
		ctx, cancel := observerContext()
		defer cancel()
		var types []string
		gomega.Expect(c.FollowLogs(ctx, "queued-job", client.LogsOptions{}, func(record client.Record) error {
			types = append(types, record.Type)
			if len(types) == 1 {
				observerFinishRun(s, r)
			}
			return nil
		})).To(gomega.Succeed())
		gomega.Expect(types).To(gomega.Equal([]string{"page", "page", "end"}))
	})

	ginkgo.It("publishes the identity when an initially absent output file appears empty", func() {
		s, c, _ := availabilityFixture(ginkgo.GinkgoT())
		ctx, cancel := observerContext()
		defer cancel()
		stop := errors.New("file identity observed")
		pages := 0
		gomega.Expect(c.FollowLogs(ctx, "queued-job", client.LogsOptions{}, func(r client.Record) error {
			if r.Logs != nil {
				pages++
				if pages == 1 {
					gomega.Expect(r.Logs.Identity).To(gomega.BeEmpty())
					observerPublishBoundLog(s, nil)
				} else {
					gomega.Expect(r.Logs.Identity).NotTo(gomega.BeEmpty())
					return stop
				}
			}
			return nil
		})).To(gomega.MatchError(stop))
		gomega.Expect(pages).To(gomega.Equal(2))
	})

	ginkgo.It("ends only observation on a caller context cancellation", func() {
		s, c, _ := availabilityFixture(ginkgo.GinkgoT())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		before, err := s.ledger.Snapshot("queued-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = c.FollowEvents(ctx, "queued-job", client.EventsOptions{}, func(client.Record) error { cancel(); return nil })
		gomega.Expect(errors.Is(err, context.Canceled)).To(gomega.BeTrue())
		after, err := s.ledger.Snapshot("queued-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
	})

	ginkgo.It("allows nested pages but refuses nested nonterminal event and log follow", func() {
		t := ginkgo.GinkgoT()
		s, c, _ := availabilityFixture(t)
		reserved, err := s.ledger.Reserve(managed.ReserveRequest{JobID: "queued-job", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "observer-parent"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		chain, err := s.ledger.RunnerParentChain(s.installationID(), "queued-job", reserved.LeaseGeneration)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		raw, err := json.Marshal(chain)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		t.Setenv(managed.ParentContextEnvironment, string(raw))
		ctx, cancel := observerContext()
		defer cancel()
		_, _, err = c.Events(ctx, "queued-job", client.EventsOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, follow := range []func() error{
			func() error {
				return c.FollowEvents(ctx, "queued-job", client.EventsOptions{}, func(client.Record) error { return nil })
			},
			func() error {
				return c.FollowLogs(ctx, "queued-job", client.LogsOptions{}, func(client.Record) error { return nil })
			},
		} {
			var issue *client.Issue
			gomega.Expect(errors.As(follow(), &issue)).To(gomega.BeTrue())
			gomega.Expect(issue.Code).To(gomega.Equal("NESTED_WAIT_REJECTED"))
		}
	})

	ginkgo.It("rejects missing started terminal and reconciling payloads without a successful empty end", func() {
		for _, terminal := range []bool{false, true} {
			s, c, _ := availabilityFixture(ginkgo.GinkgoT())
			r := observerClaimRun(s)
			path := observerBoundLog(s, r, []byte("native output"))
			if terminal {
				observerFinishRun(s, r)
			} else {
				id, err := s.ledger.LocalIdentity(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				code := 1
				_, err = s.ledger.RecordLocalOutcome(managed.LocalOutcomeRequest{Identity: id, ExpectedRevision: r.JobRevision, Outcome: managed.LocalOutcome{Started: true, WorkloadOutcome: "fail", NativeExitCode: &code}, MutationKey: "missing-outcome"})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(s.ledger.ReconcileLocalFailure(r.JobID)).To(gomega.Succeed())
			}
			gomega.Expect(os.Remove(path)).To(gomega.Succeed())
			ctx, cancel := observerContext()
			defer cancel()
			before, err := s.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, _, err = c.Logs(ctx, r.JobID, client.LogsOptions{})
			var issue *client.Issue
			gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
			gomega.Expect(issue.Code).To(gomega.Equal("STORAGE_UNAVAILABLE"))
			records := 0
			err = c.FollowLogs(ctx, r.JobID, client.LogsOptions{}, func(client.Record) error { records++; return nil })
			gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
			gomega.Expect(issue.Code).To(gomega.Equal("STORAGE_UNAVAILABLE"))
			gomega.Expect(records).To(gomega.BeZero())
			after, err := s.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(after).To(gomega.Equal(before))
		}
	})

	ginkgo.It("allows legitimate never-started terminal logs but not a missing ambiguous run claim", func() {
		s, c, _ := availabilityFixture(ginkgo.GinkgoT())
		ctx, cancel := observerContext()
		defer cancel()
		page, _, err := c.Logs(ctx, "queued-job", client.LogsOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(page.Data).To(gomega.BeEmpty())
		_, err = s.ledger.CancelQueued("queued-job", 1, "no-start-cancel")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(c.FollowLogs(ctx, "queued-job", client.LogsOptions{}, func(client.Record) error { return nil })).To(gomega.Succeed())
		s, c, _ = availabilityFixture(ginkgo.GinkgoT())
		observerClaimRun(s)
		_, _, err = c.Logs(ctx, "queued-job", client.LogsOptions{})
		var issue *client.Issue
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("STORAGE_UNAVAILABLE"))
	})

	ginkgo.It("refuses a fresh observer's unexplained private replacement", func() {
		s, c, _ := availabilityFixture(ginkgo.GinkgoT())
		r := observerClaimRun(s)
		path := observerBoundLog(s, r, []byte("original"))
		gomega.Expect(os.Rename(path, path+".original")).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(path, []byte("unbound replacement"), 0600)).To(gomega.Succeed())
		ctx, cancel := observerContext()
		defer cancel()
		_, _, err := c.Logs(ctx, r.JobID, client.LogsOptions{})
		var issue *client.Issue
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("STORAGE_UNAVAILABLE"))
	})

	ginkgo.It("reports a previously verified file removal as a gap without returning unbound bytes", func() {
		s, c, _ := availabilityFixture(ginkgo.GinkgoT())
		r := observerClaimRun(s)
		path := observerBoundLog(s, r, []byte("original"))
		ctx, cancel := observerContext()
		defer cancel()
		stop := errors.New("observed removal")
		records := 0
		gomega.Expect(c.FollowLogs(ctx, r.JobID, client.LogsOptions{}, func(record client.Record) error {
			records++
			if records == 1 {
				gomega.Expect(os.Remove(path)).To(gomega.Succeed())
				return nil
			}
			gomega.Expect(record.Type).To(gomega.Equal("resync"))
			gomega.Expect(record.Logs.Gap.Reason).To(gomega.Equal("missing"))
			gomega.Expect(record.Logs.Data).To(gomega.BeEmpty())
			return stop
		})).To(gomega.MatchError(stop))
	})
})

var _ = ginkgo.Describe("Task008LogContainment", ginkgo.Label("integration"), func() {
	ginkgo.It("serializes a reader at the committed StartLocal claim until the original fd is durably bound", func() {
		s, _, _ := availabilityFixture(ginkgo.GinkgoT())
		r, err := s.ledger.Reserve(managed.ReserveRequest{JobID: "queued-job", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "gate-reserve"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		r, err = s.ledger.AdvanceLocal(r.JobID, r.JobRevision, "preparing", "ready", "gate-ready")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		control, err := s.registerLocal(r)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer s.unregisterLocal(control)
		control.mu.Lock()
		locked := true
		defer func() {
			if locked {
				control.mu.Unlock()
			}
		}()
		r, err = s.ledger.StartLocal(r.JobID, r.JobRevision, "observer-run")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		waiting := make(chan struct{})
		reader := logReader{root: s.root.Path, jobID: r.JobID, beforeGateWait: func() { close(waiting) }}
		defer reader.close()
		ctx, cancel := observerContext()
		defer cancel()
		type observation struct {
			page logPage
			err  error
		}
		done := make(chan observation, 1)
		go func() { p, err := s.readObservedLog(ctx, r.JobID, &reader, 0, ""); done <- observation{p, err} }()
		select {
		case <-waiting:
		case <-ctx.Done():
			ginkgo.Fail("reader did not reach the publication gate")
		}
		gomega.Expect(s.cancelMu.TryLock()).To(gomega.BeTrue())
		s.cancelMu.Unlock()
		select {
		case got := <-done:
			ginkgo.Fail("reader crossed the locked publication gate: " + fmt.Sprint(got.err))
		default:
		}
		observerBoundLog(s, r, []byte("original fd bytes"))
		control.mu.Unlock()
		locked = false
		select {
		case got := <-done:
			gomega.Expect(got.err).NotTo(gomega.HaveOccurred())
			gomega.Expect(got.page.Data).To(gomega.Equal([]byte("original fd bytes")))
		case <-ctx.Done():
			ginkgo.Fail("reader did not resume after durable binding")
		}
	})

	ginkgo.It("cancels only a blocked observer while leaving the spawn gate and lease intact", func() {
		s, _, _ := availabilityFixture(ginkgo.GinkgoT())
		r, err := s.ledger.Reserve(managed.ReserveRequest{JobID: "queued-job", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "gate-cancel-reserve"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		control, err := s.registerLocal(r)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer s.unregisterLocal(control)
		control.mu.Lock()
		defer control.mu.Unlock()
		before, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		waiting := make(chan struct{})
		reader := logReader{root: s.root.Path, jobID: r.JobID, beforeGateWait: func() { close(waiting) }}
		defer reader.close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := s.readObservedLog(ctx, r.JobID, &reader, 0, ""); done <- err }()
		select {
		case <-waiting:
		case <-time.After(5 * time.Second):
			ginkgo.Fail("blocked observer missing")
		}
		cancel()
		select {
		case err := <-done:
			gomega.Expect(errors.Is(err, context.Canceled)).To(gomega.BeTrue())
		case <-time.After(5 * time.Second):
			ginkgo.Fail("observer disconnect did not release its wait")
		}
		current, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current).To(gomega.Equal(before))
	})

	ginkgo.DescribeTable("refuses unsafe or corrupt identity receipts", func(kind string) {
		root := ginkgo.GinkgoT().TempDir()
		path := observerTestLog(root, "job-1", []byte("owned bytes"))
		observerTestIdentity(path, "observer-run")
		receiptPath := filepath.Join(filepath.Dir(path), "output-identity.json")
		switch kind {
		case "symlink":
			gomega.Expect(os.Rename(receiptPath, receiptPath+".real")).To(gomega.Succeed())
			gomega.Expect(os.Symlink(receiptPath+".real", receiptPath)).To(gomega.Succeed())
		case "hard link":
			gomega.Expect(os.Link(receiptPath, receiptPath+".link")).To(gomega.Succeed())
		case "fifo":
			gomega.Expect(os.Remove(receiptPath)).To(gomega.Succeed())
			gomega.Expect(unix.Mkfifo(receiptPath, 0600)).To(gomega.Succeed())
		case "public mode":
			gomega.Expect(os.Chmod(receiptPath, 0644)).To(gomega.Succeed())
		case "oversized":
			gomega.Expect(os.WriteFile(receiptPath, bytes.Repeat([]byte("x"), 4097), 0600)).To(gomega.Succeed())
		case "duplicate key":
			gomega.Expect(os.WriteFile(receiptPath, []byte(`{"schema":"haepari.output-identity/v1","schema":"haepari.output-identity/v1"}`), 0600)).To(gomega.Succeed())
		case "trailing data":
			file, err := os.OpenFile(receiptPath, os.O_APPEND|os.O_WRONLY, 0)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = file.WriteString(" {}")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(file.Close()).To(gomega.Succeed())
		case "wrong run":
			observerTestIdentity(path, "another-run")
		case "missing binding":
			gomega.Expect(os.Remove(receiptPath)).To(gomega.Succeed())
		}
		reader := logReader{root: root, jobID: "job-1"}
		defer reader.close()
		_, err := reader.read(0, "", managed.LogObservation{Snapshot: managed.Receipt{JobID: "job-1", JobState: "running"}, RunID: "observer-run"})
		gomega.Expect(err).To(gomega.HaveOccurred())
	},
		ginkgo.Entry("symlink", "symlink"),
		ginkgo.Entry("hard link", "hard link"),
		ginkgo.Entry("fifo", "fifo"),
		ginkgo.Entry("public mode", "public mode"),
		ginkgo.Entry("oversized", "oversized"),
		ginkgo.Entry("duplicate key", "duplicate key"),
		ginkgo.Entry("trailing data", "trailing data"),
		ginkgo.Entry("wrong run", "wrong run"),
		ginkgo.Entry("missing binding", "missing binding"),
	)

	ginkgo.It("reports replacement and truncation before resetting the byte checkpoint", func() {
		root := ginkgo.GinkgoT().TempDir()
		path := observerTestLog(root, "job-1", []byte("old bytes"))
		observerTestIdentity(path, "observer-run")
		reader := logReader{root: root, jobID: "job-1"}
		defer reader.close()
		snapshot := managed.LogObservation{Snapshot: managed.Receipt{JobID: "job-1", JobState: "running"}, RunID: "observer-run"}
		old, err := reader.read(0, "", snapshot)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.Rename(path, path+".old")).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(path, []byte("new"), 0600)).To(gomega.Succeed())
		_, err = reader.read(old.NextOffset, old.Identity, snapshot)
		gomega.Expect(err).To(gomega.HaveOccurred())
		// A replacement gap permits bytes only after a valid same-run binding.
		observerTestIdentity(path, "observer-run")
		replaced, err := reader.read(old.NextOffset, old.Identity, snapshot)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replaced.Gap.Reason).To(gomega.Equal("replaced"))
		gomega.Expect(replaced.Data).To(gomega.BeEmpty())
		gomega.Expect(replaced.NextOffset).To(gomega.BeZero())
		page, err := reader.read(0, replaced.Identity, snapshot)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(page.Data).To(gomega.Equal([]byte("new")))
		gomega.Expect(os.Truncate(path, 1)).To(gomega.Succeed())
		truncated, err := reader.read(page.NextOffset, page.Identity, snapshot)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(truncated.Gap.Reason).To(gomega.Equal("truncated"))
		gomega.Expect(os.Remove(path)).To(gomega.Succeed())
		missing, err := reader.read(0, truncated.Identity, snapshot)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(missing.Gap.Reason).To(gomega.Equal("missing"))
	})

	ginkgo.DescribeTable("rejects unsafe log paths without blocking", func(kind string) {
		root := ginkgo.GinkgoT().TempDir()
		path := observerTestLog(root, "job-1", []byte("secret"))
		switch kind {
		case "file symlink":
			gomega.Expect(os.Rename(path, path+".real")).To(gomega.Succeed())
			gomega.Expect(os.Symlink(path+".real", path)).To(gomega.Succeed())
		case "job symlink", "jobs symlink":
			dir := filepath.Dir(path)
			if kind == "jobs symlink" {
				dir = filepath.Dir(dir)
			}
			gomega.Expect(os.Rename(dir, dir+".real")).To(gomega.Succeed())
			gomega.Expect(os.Symlink(dir+".real", dir)).To(gomega.Succeed())
		case "hard link":
			gomega.Expect(os.Link(path, path+".link")).To(gomega.Succeed())
		case "fifo":
			gomega.Expect(os.Remove(path)).To(gomega.Succeed())
			gomega.Expect(unix.Mkfifo(path, 0600)).To(gomega.Succeed())
		case "directory":
			gomega.Expect(os.Remove(path)).To(gomega.Succeed())
			gomega.Expect(os.Mkdir(path, 0700)).To(gomega.Succeed())
		case "public file":
			gomega.Expect(os.Chmod(path, 0644)).To(gomega.Succeed())
		case "public job directory":
			gomega.Expect(os.Chmod(filepath.Dir(path), 0755)).To(gomega.Succeed())
		case "missing jobs root":
			gomega.Expect(os.RemoveAll(filepath.Join(root, "jobs"))).To(gomega.Succeed())
		}
		reader := logReader{root: root, jobID: "job-1"}
		defer reader.close()
		_, err := reader.read(0, "", managed.LogObservation{Snapshot: managed.Receipt{JobID: "job-1"}, RunID: "observer-run"})
		gomega.Expect(err).To(gomega.HaveOccurred())
	},
		ginkgo.Entry("file symlink", "file symlink"),
		ginkgo.Entry("job symlink", "job symlink"),
		ginkgo.Entry("jobs symlink", "jobs symlink"),
		ginkgo.Entry("hard link", "hard link"),
		ginkgo.Entry("fifo", "fifo"),
		ginkgo.Entry("directory", "directory"),
		ginkgo.Entry("public file", "public file"),
		ginkgo.Entry("public job directory", "public job directory"),
		ginkgo.Entry("missing jobs root", "missing jobs root"),
	)

	ginkgo.DescribeTable("rejects malformed observer query values", func(query string) {
		_, _, _, err := observerQuery(httptest.NewRequest(http.MethodGet, "/?"+query, nil), true)
		gomega.Expect(errors.Is(err, managed.ErrInvalid)).To(gomega.BeTrue())
	},
		ginkgo.Entry("negative cursor", "after=-1"),
		ginkgo.Entry("overflow", "after=9223372036854775808"),
		ginkgo.Entry("repeated cursor", "after=0&after=1"),
		ginkgo.Entry("malformed escape", "after=%zz"),
		ginkgo.Entry("invalid follow", "follow=maybe"),
		ginkgo.Entry("excess limit", "limit=1001"),
		ginkgo.Entry("unknown field", "poll=true"),
	)
})

type observerDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadline time.Time
	refuse   bool
}

func (w *observerDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	if w.refuse {
		return errors.New("deadline unavailable")
	}
	return nil
}

var _ = ginkgo.Describe("Task008ObserverWriteDeadline", ginkgo.Label("integration"), func() {
	ginkgo.It("sets a bounded write deadline before each record and stops if it cannot", func() {
		writer := &observerDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		before := time.Now()
		gomega.Expect(writeObserver(writer, observerRecord{Schema: observerSchema, Type: "heartbeat", JobID: "job-1"})).To(gomega.Succeed())
		gomega.Expect(writer.deadline.After(before)).To(gomega.BeTrue())
		gomega.Expect(writer.deadline.Before(time.Now().Add(observerWriteWait))).To(gomega.BeTrue())
		gomega.Expect(writer.Flushed).To(gomega.BeTrue())
		writer = &observerDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), refuse: true}
		gomega.Expect(writeObserver(writer, observerRecord{})).To(gomega.HaveOccurred())
		gomega.Expect(writer.Body.Len()).To(gomega.BeZero())
	})
})
