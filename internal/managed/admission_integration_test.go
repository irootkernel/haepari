//go:build integration

package managed

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("DurableAdmissions", ginkgo.Label("integration"), func() {
	ginkgo.It("enforces request-key syntax boundaries without consuming rejected keys", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
		defer db.Close()
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		data := encode(t, validJob())
		for i, key := range []string{"", "invalid key", strings.Repeat("a", 129), ".key", "_key", "-key", "key\u00e9"} {
			_, err := l.Admit(data, digest, key, fmt.Sprintf("rejected-%d", i))
			gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue(), key)
			var count int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_admissions`).Scan(&count)).To(gomega.Succeed())
			gomega.Expect(count).To(gomega.BeZero())
		}
		for i, key := range []string{"a", strings.Repeat("a", 128), "a.b_c-d"} {
			receipt, err := l.Admit(data, digest, key, fmt.Sprintf("accepted-%d", i))
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), key)
			recovered, err := l.Request(key)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(recovered.Receipt.Admission).To(gomega.Equal(receipt.Admission))
		}
	})
	ginkgo.It("rejects malformed and oversized admission inventory without consuming the key", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
		defer db.Close()
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		data := encode(t, validJob())
		oversized := []byte(`{}` + strings.Repeat(" ", (1<<20)-1))
		gomega.Expect(json.Valid(oversized)).To(gomega.BeTrue())
		for _, inventory := range [][]byte{[]byte(`{`), oversized} {
			_, err := l.AdmitSealedInventory(data, digest, "sha256:"+digest, inventory, "inventory-key", "inventory-job")
			gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
			for _, table := range []string{"managed_jobs", "managed_admissions", "managed_job_inputs", "managed_events"} {
				var count int
				gomega.Expect(db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)).To(gomega.Succeed())
				gomega.Expect(count).To(gomega.BeZero())
			}
		}
		_, err := l.AdmitSealedInventory(data, digest, "sha256:"+digest, []byte(`{}`), "inventory-key", "inventory-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})
	ginkgo.It("races conflicting canonical requests without partial or duplicate admission", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
		defer db.Close()
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		first := validJob()
		second := validJob()
		second.Suite = "other-suite"
		requests := [][]byte{encode(t, first), encode(t, second)}
		start := make(chan struct{})
		type outcome struct {
			receipt Receipt
			err     error
			group   int
		}
		results := make(chan outcome, 16)
		var group sync.WaitGroup
		for i := 0; i < 16; i++ {
			group.Add(1)
			go func(i int) {
				defer group.Done()
				<-start
				r, err := l.Admit(requests[i%2], digest, "mixed-key", fmt.Sprintf("mixed-%d", i))
				results <- outcome{r, err, i % 2}
			}(i)
		}
		close(start)
		group.Wait()
		close(results)
		successes, conflicts := 0, 0
		winner := -1
		jobID := ""
		for result := range results {
			if result.err != nil {
				gomega.Expect(errors.Is(result.err, ErrConflict)).To(gomega.BeTrue())
				conflicts++
				continue
			}
			successes++
			if winner < 0 {
				winner = result.group
				jobID = result.receipt.JobID
			}
			gomega.Expect(result.group).To(gomega.Equal(winner))
			gomega.Expect(result.receipt.JobID).To(gomega.Equal(jobID))
		}
		gomega.Expect([]int{successes, conflicts}).To(gomega.Equal([]int{8, 8}))
		for _, table := range []string{"managed_jobs", "managed_admissions", "managed_job_inputs", "managed_events"} {
			var count int
			gomega.Expect(db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)).To(gomega.Succeed())
			gomega.Expect(count).To(gomega.Equal(1))
		}
	})

	ginkgo.It("reports accepted registry loss as storage uncertainty and refuses a missing schema4 table", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "state.sqlite")
		db, l := openLedger(t, path, 1)
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		data := encode(t, validJob())
		_, err := l.AdmitSealed(data, digest, "sha256:"+digest, "lost-key", "known-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`DELETE FROM managed_admissions`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, snapshotErr := l.Snapshot("known-job")
		_, requestErr := l.Request("lost-key")
		_, replayErr := l.Admit(data, digest, "lost-key", "new-job")
		_, resultErr := l.Result("known-job")
		bindErr := l.BindAdmissionInventory("lost-key", "sha256:"+digest, []byte(`{}`))
		for _, err := range []error{snapshotErr, requestErr, replayErr, resultErr, bindErr} {
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(errors.Is(err, ErrNotFound)).To(gomega.BeFalse())
			gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeFalse())
		}
		gomega.Expect(errors.Is(l.BindAdmissionInventory("never-key", "sha256:"+digest, []byte(`{}`)), ErrNotFound)).To(gomega.BeTrue())
		var count int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_jobs`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.Equal(1))
		_, err = db.Exec(`DROP TABLE managed_admissions`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, err = sqlite.Open(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		_, err = NewLedger(db, 1)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeFalse())
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name='managed_admissions'`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.BeZero())
	})

	ginkgo.It("binds a legacy verified inventory once and guards retention of uncertain work", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
		defer db.Close()
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		objectID := "sha256:" + digest
		_, err := l.AdmitSealed(encode(t, validJob()), digest, objectID, "legacy-key", "legacy-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = l.CancelQueued("legacy-job", 1, "end-legacy")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(errors.Is(l.RetainAdmissionTombstone("legacy-job"), ErrBusy)).To(gomega.BeTrue())
		inventory := []byte(`{"tree_sha256":"sha256:` + digest + `","entries":[]}`)
		gomega.Expect(errors.Is(l.BindAdmissionInventory("legacy-key", "wrong-object", inventory), ErrConflict)).To(gomega.BeTrue())
		gomega.Expect(errors.Is(l.BindAdmissionInventory("legacy-key", objectID, []byte(`invalid`)), ErrInvalid)).To(gomega.BeTrue())
		gomega.Expect(l.BindAdmissionInventory("legacy-key", objectID, inventory)).To(gomega.Succeed())
		gomega.Expect(l.BindAdmissionInventory("legacy-key", objectID, inventory)).To(gomega.Succeed())
		gomega.Expect(errors.Is(l.BindAdmissionInventory("legacy-key", objectID, []byte(`{"changed":true}`)), ErrConflict)).To(gomega.BeTrue())
		_, err = db.Exec(`UPDATE managed_resources SET availability='quarantined'`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(errors.Is(l.RetainAdmissionTombstone("legacy-job"), ErrBusy)).To(gomega.BeTrue())
		_, err = db.Exec(`UPDATE managed_resources SET availability='available'`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(l.RetainAdmissionTombstone("legacy-job")).To(gomega.Succeed())
		gomega.Expect(l.RetainAdmissionTombstone("legacy-job")).To(gomega.Succeed())
		gomega.Expect(errors.Is(l.BindAdmissionInventory("legacy-key", objectID, inventory), ErrConflict)).To(gomega.BeTrue())
	})
	ginkgo.It("races one key and preserves original resolved identities after configuration changes", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 2)
		defer db.Close()
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		data := encode(t, validJob())
		const clients = 16
		start := make(chan struct{})
		receipts := make(chan Receipt, clients)
		failures := make(chan error, clients)
		var group sync.WaitGroup
		for i := 0; i < clients; i++ {
			group.Add(1)
			go func(i int) {
				defer group.Done()
				<-start
				r, err := l.Admit(data, digest, "same-key", fmt.Sprintf("candidate-%d", i))
				receipts <- r
				failures <- err
			}(i)
		}
		close(start)
		group.Wait()
		close(receipts)
		close(failures)
		for err := range failures {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		var original Receipt
		for receipt := range receipts {
			if original.JobID == "" {
				original = receipt
			}
			gomega.Expect(receipt).To(gomega.Equal(original))
		}
		var jobs, admissions, events int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_jobs`).Scan(&jobs)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_admissions`).Scan(&admissions)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&events)).To(gomega.Succeed())
		gomega.Expect([]int{jobs, admissions, events}).To(gomega.Equal([]int{1, 1, 1}))
		updated := validResource()
		updated.Revision = 2
		updated.Adapter.QualificationReference = "new-observation"
		gomega.Expect(l.UpdateResource(encode(t, updated), 1, 1)).To(gomega.Succeed())
		again, err := l.Admit(data, digest, "same-key", "unused-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(again.Admission).To(gomega.Equal(original.Admission))
		gomega.Expect(again.ResourceConfigRevision).To(gomega.Equal(int64(2)))
		gomega.Expect(again.Admission.ResourceConfigRevision).To(gomega.Equal(int64(1)))
		gomega.Expect(again.Admission.InputDigest).To(gomega.Equal(digest))
		gomega.Expect(again.Admission.EnvironmentProfileDigest).To(gomega.Equal(digest))
		changed := validJob()
		changed.Environment.ProfileID = "another-profile"
		_, err = l.Admit(encode(t, changed), digest, "same-key", "another-job")
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
		request, err := l.Request("same-key")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(request.Receipt.Admission).To(gomega.Equal(original.Admission))
	})
	ginkgo.It("retains a terminal tombstone across synthetic bulk payload removal and restart", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "state.sqlite")
		db, l := openLedger(t, path, 1)
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		data := encode(t, validJob())
		admitted, err := l.Admit(data, digest, "retained-key", "retained-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(errors.Is(l.RetainAdmissionTombstone(admitted.JobID), ErrBusy)).To(gomega.BeTrue())
		_, err = l.CancelQueued(admitted.JobID, 1, "end-retained")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(l.RetainAdmissionTombstone(admitted.JobID)).To(gomega.Succeed())
		for _, table := range []string{"managed_job_inputs", "managed_runs", "managed_events", "managed_event_owners", "managed_event_progress", "managed_jobs"} {
			_, err := db.Exec("DELETE FROM " + table)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, l = openLedger(t, path, 1)
		defer db.Close()
		replay, err := l.Admit(data, digest, "retained-key", "should-not-exist")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.JobID).To(gomega.Equal(admitted.JobID))
		gomega.Expect(replay.Admission).To(gomega.Equal(admitted.Admission))
		gomega.Expect(replay.ResultStatus).To(gomega.Equal("unavailable"))
		gomega.Expect(replay.Verdict).To(gomega.BeEmpty())
		var output map[string]any
		gomega.Expect(json.Unmarshal(encode(t, replay), &output)).To(gomega.Succeed())
		gomega.Expect(output).To(gomega.HaveLen(6))
		for _, field := range []string{"job_id", "job_revision", "job_state", "resource_id", "admission", "result_status"} {
			gomega.Expect(output).To(gomega.HaveKey(field))
		}
		request, err := l.Request("retained-key")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(request.Receipt).To(gomega.Equal(replay))
		_, err = l.Result(admitted.JobID)
		gomega.Expect(errors.Is(err, ErrResultUnavailable)).To(gomega.BeTrue())
		changed := validJob()
		changed.Suite = "changed"
		_, err = l.Admit(encode(t, changed), digest, "retained-key", "should-not-exist")
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
		var count int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_jobs`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.BeZero())
	})
	ginkgo.It("does not disguise unexpected payload loss as an ordinary tombstone", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
		defer db.Close()
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		data := encode(t, validJob())
		_, err := l.Admit(data, digest, "damaged-key", "damaged-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`DELETE FROM managed_job_inputs`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`DELETE FROM managed_jobs`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = l.Admit(data, digest, "damaged-key", "new-job")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(errors.Is(err, ErrNotFound)).To(gomega.BeFalse())
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeFalse())
		_, err = l.Request("damaged-key")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(errors.Is(err, ErrNotFound)).To(gomega.BeFalse())
		_, err = l.Result("damaged-job")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(errors.Is(err, ErrNotFound)).To(gomega.BeFalse())
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeFalse())
		var count int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_jobs`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.BeZero())
	})
	ginkgo.It("backfills v3 admissions using the pinned original configuration", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "state.sqlite")
		db, l := openLedger(t, path, 1)
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		data := encode(t, validJob())
		admitted, err := l.Admit(data, digest, "migrated-key", "migrated-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		r := validResource()
		r.Revision = 2
		gomega.Expect(l.UpdateResource(encode(t, r), 1, 1)).To(gomega.Succeed())
		for _, statement := range []string{`DROP TABLE managed_lock_requests; DROP TABLE managed_lock_slots; DROP TABLE managed_lock_clock; DROP TABLE managed_lock_discontinuities; DROP TABLE managed_manual_sessions; DELETE FROM schema_migrations WHERE version=10; DROP TABLE managed_purges; DROP TABLE managed_evidence_recovery; DELETE FROM schema_migrations WHERE version=9; DROP TABLE managed_runner_receipts; DELETE FROM schema_migrations WHERE version=8; DELETE FROM schema_migrations WHERE version=7; DROP TABLE managed_installation; DROP TABLE managed_cancellations; DROP TABLE managed_cancel_edges; DROP TABLE managed_local_outcomes; `, `DROP TABLE managed_admissions`, `DELETE FROM schema_migrations WHERE version>=4`, `PRAGMA user_version=3`} {
			_, err := db.Exec(statement)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, l = openLedger(t, path, 1)
		defer db.Close()
		replay, err := l.Admit(data, digest, "migrated-key", "new-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.Admission).To(gomega.Equal(admitted.Admission))
		gomega.Expect(replay.ResourceConfigRevision).To(gomega.Equal(int64(2)))
		var version int
		gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&version)).To(gomega.Succeed())
		gomega.Expect(version).To(gomega.Equal(10))
	})
	ginkgo.It("leaves failed database admission keys reusable and storage errors uncertain", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
		defer db.Close()
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		_, err := db.Exec(`PRAGMA query_only=ON`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		data := encode(t, validJob())
		_, err = l.Admit(data, digest, "failed-key", "failed-job")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeFalse())
		_, err = db.Exec(`PRAGMA query_only=OFF`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`CREATE TRIGGER fail_admission AFTER INSERT ON managed_admissions BEGIN SELECT RAISE(ABORT,'injected database failure'); END`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = l.Admit(data, digest, "failed-key", "failed-job")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeFalse())
		for _, table := range []string{"managed_jobs", "managed_admissions", "managed_job_inputs", "managed_events"} {
			var count int
			gomega.Expect(db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)).To(gomega.Succeed())
			gomega.Expect(count).To(gomega.BeZero())
		}
		_, err = db.Exec(`DROP TRIGGER fail_admission`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		r, err := l.Admit(data, digest, "failed-key", "retry-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(r.JobID).To(gomega.Equal("retry-job"))
	})
})
