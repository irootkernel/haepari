//go:build integration

package managed

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"path/filepath"
)

var _ = ginkgo.Describe("Task008DurablePages", ginkgo.Label("integration"), func() {
	ginkgo.It("binds installation once and preserves it after reopen and relocation seed", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "db")
		db, l := openLedger(t, path, 1)
		id, e := l.BindInstallation("local-original")
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(id).To(gomega.Equal("local-original"))
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, l = openLedger(t, path, 1)
		defer db.Close()
		id, e = l.BindInstallation("local-other-root")
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(id).To(gomega.Equal("local-original"))
	})
	ginkgo.It("pages permanent admission sequence with a fixed upper bound, filter scope and queue positions", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "db"), 1)
		defer db.Close()
		_, e := l.BindInstallation("local-test")
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		for i := 1; i <= 4; i++ {
			_, e := l.Admit(encode(t, validJob()), digest, fmt.Sprintf("key-%d", i), fmt.Sprintf("job-%d", i))
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
		}
		q := JobsQuery{Limit: 2}
		a, e := l.JobsPage(q)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(a.More).To(gomega.BeTrue())
		gomega.Expect(a.Jobs[0].Receipt.JobID).To(gomega.Equal("job-1"))
		gomega.Expect(a.Jobs[1].QueuePosition).To(gomega.Equal(int64(2)))
		_, e = l.Admit(encode(t, validJob()), digest, "new-key", "job-5")
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		q.Cursor = a.NextCursor
		b, e := l.JobsPage(q)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(b.More).To(gomega.BeFalse())
		gomega.Expect(b.Jobs).To(gomega.HaveLen(2))
		gomega.Expect(b.Jobs[1].Receipt.JobID).To(gomega.Equal("job-4"))
		q.Resource = "local-a"
		_, e = l.JobsPage(q)
		gomega.Expect(errors.Is(e, ErrInvalid)).To(gomega.BeTrue())
		q.Resource = ""
		q.State = "queued"
		_, e = l.JobsPage(q)
		gomega.Expect(errors.Is(e, ErrInvalid)).To(gomega.BeTrue())
	})
	ginkgo.It("pages resource IDs and refuses a foreign page kind or installation", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "db"), 1)
		defer db.Close()
		_, e := l.BindInstallation("local-test")
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		for _, id := range []string{"local-c", "local-a", "local-b"} {
			r := validResource()
			r.ID = id
			gomega.Expect(l.RegisterResource(encode(t, r))).To(gomega.Succeed())
		}
		a, e := l.ResourcesPage("", 1)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(a.Resources[0].ID).To(gomega.Equal("local-a"))
		b, e := l.ResourcesPage(a.NextCursor, 2)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(b.Resources).To(gomega.HaveLen(2))
		gomega.Expect(b.More).To(gomega.BeFalse())
		_, e = l.JobsPage(JobsQuery{Cursor: a.NextCursor, Limit: 1})
		gomega.Expect(errors.Is(e, ErrInvalid)).To(gomega.BeTrue())
		_, e = l.ResourcesPage(encodePageCursor(pageCursor{Kind: "resources", Installation: "foreign", LastID: "local-a", UpperID: "local-c"}), 1)
		gomega.Expect(errors.Is(e, ErrInvalid)).To(gomega.BeTrue())
	})
	ginkgo.It("pages unavailable retained admissions between terminal results before and after fixture payload removal", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "db"), 1)
		defer db.Close()
		_, err := l.BindInstallation("local-retained-pages")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		for i := 1; i <= 4; i++ {
			id := fmt.Sprintf("page-job-%d", i)
			_, err = l.Admit(encode(t, validJob()), digest, fmt.Sprintf("page-key-%d", i), id)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if i < 4 {
				_, err = l.CancelQueued(id, 1, fmt.Sprintf("page-cancel-%d", i))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
		}
		gomega.Expect(l.RetainAdmissionTombstone("page-job-2")).To(gomega.Succeed())
		check := func() {
			for _, state := range []string{"", "finished"} {
				q := JobsQuery{Resource: validResource().ID, State: state, Limit: 1}
				count := 4
				if state == "finished" {
					count = 3
				}
				for i := 1; i <= count; i++ {
					page, err := l.JobsPage(q)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Expect(page.Jobs).To(gomega.HaveLen(1))
					entry := page.Jobs[0]
					gomega.Expect(entry.Receipt.JobID).To(gomega.Equal(fmt.Sprintf("page-job-%d", i)))
					if i == 2 {
						gomega.Expect(entry.Receipt.JobState).To(gomega.Equal("finished"))
						gomega.Expect(entry.Receipt.ResultStatus).To(gomega.Equal("unavailable"))
						gomega.Expect(entry.Result).To(gomega.BeNil())
						var wire map[string]any
						gomega.Expect(json.Unmarshal(encode(t, entry), &wire)).To(gomega.Succeed())
						gomega.Expect(wire).NotTo(gomega.HaveKey("result"))
					} else if i < 4 {
						gomega.Expect(entry.Result).NotTo(gomega.BeNil())
					} else {
						gomega.Expect(entry.Receipt.JobState).To(gomega.Equal("queued"))
					}
					gomega.Expect(page.More).To(gomega.Equal(i < count))
					if page.More {
						gomega.Expect(page.NextCursor).NotTo(gomega.BeEmpty())
					}
					q.Cursor = page.NextCursor
				}
			}
		}
		check()
		// Only this temporary fixture's retained Job payload is removed.
		for _, table := range []string{"managed_job_inputs", "managed_runs", "managed_events", "managed_event_owners", "managed_event_progress", "managed_job_bounds", "managed_jobs"} {
			column := "job_id"
			if table == "managed_jobs" {
				column = "id"
			}
			_, err = db.Exec("DELETE FROM "+table+" WHERE "+column+"=?", "page-job-2")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		check()
	})
	ginkgo.It("broadcasts committed changes across subscribe/read and never broadcasts a rollback", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "db"), 1)
		defer db.Close()
		changed := l.Changes()
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		select {
		case <-changed:
		default:
			ginkgo.Fail("commit notification lost")
		}
		changed = l.Changes()
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).NotTo(gomega.Succeed())
		select {
		case <-changed:
			ginkgo.Fail("rollback notified")
		default:
		}
		_, e := l.Admit(encode(t, validJob()), digest, "key", "job-1")
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		select {
		case <-changed:
		default:
			ginkgo.Fail("admission commit notification lost")
		}
	})
	ginkgo.It("returns explicit purged resync but fails unexplained event payload loss", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "db"), 1)
		defer db.Close()
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		r, e := l.Admit(encode(t, validJob()), digest, "key", "job-1")
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		a, e := l.EventsAfter(r.JobID, 0, 1)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		cursor := a.Events[0].Cursor
		_, e = l.CancelQueued(r.JobID, 1, "cancel")
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(l.PurgeEventsThrough(r.JobID, cursor)).To(gomega.Succeed())
		a, e = l.EventsAfter(r.JobID, cursor, 1)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(a.Resync).NotTo(gomega.BeNil())
		gomega.Expect(a.Snapshot.JobState).To(gomega.Equal("finished"))
		gomega.Expect(a.NextCursor).To(gomega.Equal(a.CurrentCursor))
		_, e = db.Exec(`DELETE FROM managed_events WHERE cursor=(SELECT MAX(cursor) FROM managed_event_owners WHERE job_id='job-1')`)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		for _, after := range []int64{0, cursor, a.NextCursor} {
			_, e = l.EventsAfter(r.JobID, after, 1)
			gomega.Expect(e).To(gomega.HaveOccurred())
			gomega.Expect(errors.Is(e, ErrInvalid)).To(gomega.BeFalse())
		}
	})
	ginkgo.It("migrates schema5 without changing admissions and fails closed on missing schema6 identity", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "db"), 1)
		defer db.Close()
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		r, e := l.Admit(encode(t, validJob()), digest, "key", "job-1")
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		_, e = db.Exec(`DROP TABLE managed_lock_requests; DROP TABLE managed_lock_slots; DROP TABLE managed_lock_clock; DROP TABLE managed_lock_discontinuities; DROP TABLE managed_manual_sessions; DELETE FROM schema_migrations WHERE version=10; DROP TABLE managed_purges; DROP TABLE managed_evidence_recovery; DELETE FROM schema_migrations WHERE version=9; DROP TABLE managed_runner_receipts; DELETE FROM schema_migrations WHERE version=8; DELETE FROM schema_migrations WHERE version=7; DROP TABLE managed_installation; DROP TABLE managed_cancellations; DROP TABLE managed_cancel_edges; DROP TABLE managed_local_outcomes; DELETE FROM schema_migrations WHERE version=6; PRAGMA user_version=5`)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		l, e = NewLedger(db, 1)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		after, e := l.Snapshot(r.JobID)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(r))
		_, e = l.BindInstallation("local-migrated")
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		_, e = db.Exec(`DELETE FROM managed_installation WHERE singleton=1`)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		_, e = NewLedger(db, 1)
		gomega.Expect(e).To(gomega.HaveOccurred())
		_, e = db.Exec(`DROP TABLE managed_installation`)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		_, e = NewLedger(db, 1)
		gomega.Expect(e).To(gomega.HaveOccurred())
	})
})
