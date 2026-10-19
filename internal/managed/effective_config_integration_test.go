//go:build integration

package managed

import (
	"errors"
	"path/filepath"

	"github.com/irootkernel/haepari/internal/config"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task023EffectiveConfig", ginkgo.Label("integration"), func() {
	ginkgo.It("commits digest revision and audit once and keeps a rejected change from touching jobs", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "state.sqlite")
		db, ledger := openLedger(t, path, 1)
		defer func() { _ = db.Close() }()
		gomega.Expect(ledger.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		job := validJob()
		admitted, err := ledger.Admit(encode(t, job), digest, "request-a", "job-a")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(admitted.JobState).To(gomega.Equal("queued"))
		_, err = ledger.Reserve(ReserveRequest{JobID: "job-a", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-a"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		doc := config.Defaults()
		doc.Revision = 99
		accepted, err := ledger.CommitEffectiveConfig(doc, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(accepted.Revision).To(gomega.Equal(int64(1)))
		_, documentDigest, valuesDigest, err := accepted.Digests()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var storedRevision int64
		var storedDocument, storedValues string
		var audits int
		gomega.Expect(db.QueryRow(`SELECT revision,document_digest,values_digest FROM managed_effective_config WHERE singleton=1`).Scan(&storedRevision, &storedDocument, &storedValues)).To(gomega.Succeed())
		gomega.Expect(storedRevision).To(gomega.Equal(int64(1)))
		gomega.Expect(storedDocument).To(gomega.Equal(documentDigest))
		gomega.Expect(storedValues).To(gomega.Equal(valuesDigest))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_config_audits`).Scan(&audits)).To(gomega.Succeed())
		gomega.Expect(audits).To(gomega.Equal(1))

		doc.Revision = 5
		replayed, err := ledger.CommitEffectiveConfig(doc, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replayed.Revision).To(gomega.Equal(int64(1)))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_config_audits`).Scan(&audits)).To(gomega.Succeed())
		gomega.Expect(audits).To(gomega.Equal(1))

		doc.GlobalLimit = 2
		changed, err := ledger.CommitEffectiveConfig(doc, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(changed.Revision).To(gomega.Equal(int64(2)))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_config_audits`).Scan(&audits)).To(gomega.Succeed())
		gomega.Expect(audits).To(gomega.Equal(2))

		explicit := changed
		explicit.GlobalLimit = 1
		_, err = ledger.CommitEffectiveConfig(explicit, true)
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("changed the accepted digest"))
		lower := explicit
		lower.Revision = 1
		lower.GlobalLimit = 2
		_, err = ledger.CommitEffectiveConfig(lower, true)
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("below accepted revision"))
		skipped := explicit
		skipped.Revision = 4
		_, err = ledger.CommitEffectiveConfig(skipped, true)
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("must be the next revision"))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_config_audits`).Scan(&audits)).To(gomega.Succeed())
		gomega.Expect(audits).To(gomega.Equal(2))
		gomega.Expect(db.QueryRow(`SELECT revision FROM managed_effective_config WHERE singleton=1`).Scan(&storedRevision)).To(gomega.Succeed())
		gomega.Expect(storedRevision).To(gomega.Equal(int64(2)))

		var jobState string
		var jobRevision, pinned int64
		gomega.Expect(db.QueryRow(`SELECT state,revision,pinned_config_revision FROM managed_jobs WHERE id='job-a'`).Scan(&jobState, &jobRevision, &pinned)).To(gomega.Succeed())
		gomega.Expect(ledger.MarkInterrupted()).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT state,revision,pinned_config_revision FROM managed_jobs WHERE id='job-a'`).Scan(&jobState, &jobRevision, &pinned)).To(gomega.Succeed())
		var availability string
		gomega.Expect(db.QueryRow(`SELECT availability FROM managed_resources WHERE id='local-a'`).Scan(&availability)).To(gomega.Succeed())
		_, err = ledger.CommitEffectiveConfig(explicit, true)
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
		var afterState, afterAvailability string
		var afterRevision, afterPinned int64
		gomega.Expect(db.QueryRow(`SELECT state,revision,pinned_config_revision FROM managed_jobs WHERE id='job-a'`).Scan(&afterState, &afterRevision, &afterPinned)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT availability FROM managed_resources WHERE id='local-a'`).Scan(&afterAvailability)).To(gomega.Succeed())
		gomega.Expect(afterState).To(gomega.Equal(jobState))
		gomega.Expect(afterRevision).To(gomega.Equal(jobRevision))
		gomega.Expect(afterPinned).To(gomega.Equal(pinned))
		gomega.Expect(afterAvailability).To(gomega.Equal(availability))
		var jobs int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_jobs`).Scan(&jobs)).To(gomega.Succeed())
		gomega.Expect(jobs).To(gomega.Equal(1))

		next := changed
		next.Revision = 3
		next.GlobalLimit = 1
		accepted, err = ledger.CommitEffectiveConfig(next, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(accepted.Revision).To(gomega.Equal(int64(3)))
		replayed, err = ledger.CommitEffectiveConfig(next, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replayed.Revision).To(gomega.Equal(int64(3)))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_config_audits`).Scan(&audits)).To(gomega.Succeed())
		gomega.Expect(audits).To(gomega.Equal(3))

		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, ledger = openLedger(t, path, 1)
		gomega.Expect(db.QueryRow(`SELECT revision FROM managed_effective_config WHERE singleton=1`).Scan(&storedRevision)).To(gomega.Succeed())
		gomega.Expect(storedRevision).To(gomega.Equal(int64(3)))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_config_audits`).Scan(&audits)).To(gomega.Succeed())
		gomega.Expect(audits).To(gomega.Equal(3))
		gomega.Expect(db.QueryRow(`SELECT state,revision,pinned_config_revision FROM managed_jobs WHERE id='job-a'`).Scan(&afterState, &afterRevision, &afterPinned)).To(gomega.Succeed())
		gomega.Expect(afterState).To(gomega.Equal(jobState))
		gomega.Expect(afterRevision).To(gomega.Equal(jobRevision))
		gomega.Expect(afterPinned).To(gomega.Equal(pinned))
	})

	ginkgo.It("does not evict a reserved job when the global limit drops", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "state.sqlite")
		db, ledger := openLedger(t, path, 2)
		defer func() { _ = db.Close() }()
		gomega.Expect(ledger.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		job := validJob()
		_, err := ledger.Admit(encode(t, job), digest, "request-a", "job-a")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = ledger.Admit(encode(t, job), digest, "request-b", "job-b")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = ledger.Reserve(ReserveRequest{JobID: "job-a", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-a"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		wide := config.Defaults()
		wide.GlobalLimit = 2
		_, err = ledger.CommitEffectiveConfig(wide, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		narrow := wide
		narrow.Revision = 2
		narrow.GlobalLimit = 1
		_, err = ledger.CommitEffectiveConfig(narrow, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		var stateA, stateB string
		var occupancy int
		gomega.Expect(db.QueryRow(`SELECT state FROM managed_jobs WHERE id='job-a'`).Scan(&stateA)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT state FROM managed_jobs WHERE id='job-b'`).Scan(&stateB)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT occupancy FROM managed_resources WHERE id='local-a'`).Scan(&occupancy)).To(gomega.Succeed())
		gomega.Expect(stateA).To(gomega.Equal("preparing"))
		gomega.Expect(stateB).To(gomega.Equal("queued"))
		gomega.Expect(occupancy).To(gomega.Equal(1))

		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, ledger = openLedger(t, path, 1)
		_, err = ledger.Reserve(ReserveRequest{JobID: "job-b", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-b"})
		gomega.Expect(errors.Is(err, ErrBusy)).To(gomega.BeTrue())
		gomega.Expect(db.QueryRow(`SELECT state FROM managed_jobs WHERE id='job-a'`).Scan(&stateA)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT state FROM managed_jobs WHERE id='job-b'`).Scan(&stateB)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT occupancy FROM managed_resources WHERE id='local-a'`).Scan(&occupancy)).To(gomega.Succeed())
		gomega.Expect(stateA).To(gomega.Equal("preparing"))
		gomega.Expect(stateB).To(gomega.Equal("queued"))
		gomega.Expect(occupancy).To(gomega.Equal(1))
	})

	ginkgo.It("migrates a schema 10 store without changing the existing job", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "state.sqlite")
		db, ledger := openLedger(t, path, 1)
		defer func() { _ = db.Close() }()
		gomega.Expect(ledger.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		_, err := ledger.Admit(encode(t, validJob()), digest, "request-a", "job-a")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var beforeState string
		var beforeRevision, beforePinned int64
		gomega.Expect(db.QueryRow(`SELECT state,revision,pinned_config_revision FROM managed_jobs WHERE id='job-a'`).Scan(&beforeState, &beforeRevision, &beforePinned)).To(gomega.Succeed())
		_, err = db.Exec(`DROP TABLE managed_effective_config; DROP TABLE managed_config_audits; DELETE FROM schema_migrations WHERE version=11; PRAGMA user_version=10`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(db.Close()).To(gomega.Succeed())

		db, ledger = openLedger(t, path, 1)
		var version, jobs, rows int
		gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&version)).To(gomega.Succeed())
		gomega.Expect(version).To(gomega.Equal(13))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_jobs`).Scan(&jobs)).To(gomega.Succeed())
		gomega.Expect(jobs).To(gomega.Equal(1))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_effective_config`).Scan(&rows)).To(gomega.Succeed())
		gomega.Expect(rows).To(gomega.Equal(0))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_config_audits`).Scan(&rows)).To(gomega.Succeed())
		gomega.Expect(rows).To(gomega.Equal(0))
		var state string
		var revision, pinned int64
		gomega.Expect(db.QueryRow(`SELECT state,revision,pinned_config_revision FROM managed_jobs WHERE id='job-a'`).Scan(&state, &revision, &pinned)).To(gomega.Succeed())
		gomega.Expect(state).To(gomega.Equal(beforeState))
		gomega.Expect(revision).To(gomega.Equal(beforeRevision))
		gomega.Expect(pinned).To(gomega.Equal(beforePinned))

		accepted, err := ledger.CommitEffectiveConfig(config.Defaults(), true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(accepted.Revision).To(gomega.Equal(int64(1)))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_effective_config`).Scan(&rows)).To(gomega.Succeed())
		gomega.Expect(rows).To(gomega.Equal(1))
		gomega.Expect(db.QueryRow(`SELECT state,revision FROM managed_jobs WHERE id='job-a'`).Scan(&state, &revision)).To(gomega.Succeed())
		gomega.Expect(state).To(gomega.Equal(beforeState))
		gomega.Expect(revision).To(gomega.Equal(beforeRevision))
	})

	ginkgo.It("accepts an operator revision on an empty store and rejects a skipped successor", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "state.sqlite")
		db, ledger := openLedger(t, path, 1)
		defer func() { _ = db.Close() }()
		doc := config.Defaults()
		doc.Revision = 3
		accepted, err := ledger.CommitEffectiveConfig(doc, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(accepted.Revision).To(gomega.Equal(int64(3)))
		_, err = ledger.CommitEffectiveConfig(doc, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var audits int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_config_audits`).Scan(&audits)).To(gomega.Succeed())
		gomega.Expect(audits).To(gomega.Equal(1))
		doc.Revision = 5
		_, err = ledger.CommitEffectiveConfig(doc, true)
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_config_audits`).Scan(&audits)).To(gomega.Succeed())
		gomega.Expect(audits).To(gomega.Equal(1))
		var jobs int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_jobs`).Scan(&jobs)).To(gomega.Succeed())
		gomega.Expect(jobs).To(gomega.Equal(0))
	})
})
