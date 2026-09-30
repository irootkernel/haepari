//go:build integration

package managed

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

type task007GuardState struct {
	Job                  Receipt
	Resource             ResourceStatus
	Changes              int64
	Events               int
	Mutations            int
	AvailabilityReceipts int
}

func readTask007GuardState(t ginkgo.GinkgoTInterface, db *sql.DB, l *Ledger, jobID string) task007GuardState {
	t.Helper()
	var state task007GuardState
	var err error
	state.Job, err = l.Snapshot(jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	state.Resource, err = l.ResourceStatus(state.Job.ResourceID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	// sqlite.Open uses one connection; total_changes also detects rolled-back writes.
	gomega.Expect(db.QueryRow(`SELECT total_changes()`).Scan(&state.Changes)).To(gomega.Succeed())
	gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&state.Events)).To(gomega.Succeed())
	gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_mutations`).Scan(&state.Mutations)).To(gomega.Succeed())
	gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_availability_receipts`).Scan(&state.AvailabilityReceipts)).To(gomega.Succeed())
	return state
}

var _ = ginkgo.Describe("Task007GuardCorrections", ginkgo.Label("integration"), func() {
	ginkgo.DescribeTable("refuses runner parent export for a stale lease or ended ownership", func(scenario string) {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
		defer db.Close()
		r := validResource()
		gomega.Expect(l.RegisterResource(encode(t, r))).To(gomega.Succeed())
		job := admitScheduling(t, l, r.ID, "guard-parent")
		var nonceRows int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_parent_nonces WHERE job_id=?`, job.JobID).Scan(&nonceRows)).To(gomega.Succeed())
		gomega.Expect(nonceRows).To(gomega.BeZero())
		job, err := reserveScheduling(l, job, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		legitimate, err := l.RunnerParentChain("installation", job.JobID, job.LeaseGeneration)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(legitimate).To(gomega.HaveLen(1))
		requestedLease := job.LeaseGeneration
		switch scenario {
		case "wrong live lease":
			requestedLease++
		case "queued nonce row":
			// Test-only corruption: legitimate queued Jobs never have an issued nonce.
			// Keep the exact reserved lease and nonce so only the state guard rejects it.
			updated, err := db.Exec(`UPDATE managed_jobs SET state='queued' WHERE id=? AND state='preparing' AND revision=? AND lease_generation=? AND EXISTS(SELECT 1 FROM managed_parent_nonces n WHERE n.job_id=managed_jobs.id AND n.lease_generation=managed_jobs.lease_generation AND n.nonce=?)`, job.JobID, job.JobRevision, job.LeaseGeneration, legitimate[0].Nonce)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			count, err := updated.RowsAffected()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(count).To(gomega.Equal(int64(1)))
		case "finished issued lease":
			finished := settleScheduling(t, l, job, "verified")
			gomega.Expect(finished.JobState).To(gomega.Equal("finished"))
		}
		var nonce string
		var nonceLease int64
		gomega.Expect(db.QueryRow(`SELECT lease_generation,nonce FROM managed_parent_nonces WHERE job_id=?`, job.JobID).Scan(&nonceLease, &nonce)).To(gomega.Succeed())
		gomega.Expect(nonceLease).To(gomega.Equal(job.LeaseGeneration))
		gomega.Expect(nonce).To(gomega.Equal(legitimate[0].Nonce))
		before := readTask007GuardState(t, db, l, job.JobID)
		chain, err := l.RunnerParentChain("installation", job.JobID, requestedLease)
		gomega.Expect(errors.Is(err, ErrStale)).To(gomega.BeTrue())
		gomega.Expect(chain).To(gomega.BeNil())
		gomega.Expect(readTask007GuardState(t, db, l, job.JobID)).To(gomega.Equal(before))
	}, ginkgo.Entry("wrong live lease", "wrong live lease"), ginkgo.Entry("queued with the same issued nonce", "queued nonce row"), ginkgo.Entry("finished with its formerly live lease", "finished issued lease"))

	invalidIntents := []struct {
		name      string
		change    func(*AvailabilityRequest)
		pauseOnly bool
	}{
		{"zero config revision", func(q *AvailabilityRequest) { q.ExpectedConfigRevision = 0 }, false},
		{"negative config revision", func(q *AvailabilityRequest) { q.ExpectedConfigRevision = -1 }, false},
		{"zero availability revision", func(q *AvailabilityRequest) { q.ExpectedAvailabilityRevision = 0 }, false},
		{"negative availability revision", func(q *AvailabilityRequest) { q.ExpectedAvailabilityRevision = -1 }, false},
		{"negative lease", func(q *AvailabilityRequest) { q.ExpectedLeaseGeneration = -1 }, false},
		{"empty key", func(q *AvailabilityRequest) { q.MutationKey = "" }, false},
		{"invalid key", func(q *AvailabilityRequest) { q.MutationKey = "bad/key" }, false},
		{"oversized key", func(q *AvailabilityRequest) { q.MutationKey = strings.Repeat("k", 129) }, false},
		{"empty resource ID", func(q *AvailabilityRequest) { q.ResourceID = "" }, false},
		{"invalid resource ID", func(q *AvailabilityRequest) { q.ResourceID = "bad/resource" }, false},
		{"empty pause reason", func(q *AvailabilityRequest) { q.Reason = "" }, true},
		{"pause reason over 256 bytes", func(q *AvailabilityRequest) { q.Reason = strings.Repeat("é", 129) }, true},
	}
	for _, resume := range []bool{false, true} {
		mode := "pause"
		if resume {
			mode = "resume"
		}
		entries := []ginkgo.TableEntry{}
		for _, invalid := range invalidIntents {
			if !resume || !invalid.pauseOnly {
				entries = append(entries, ginkgo.Entry(invalid.name, invalid.change))
			}
		}
		ginkgo.DescribeTable("rejects invalid "+mode+" intents at both availability entrypoints", func(change func(*AvailabilityRequest)) {
			t := ginkgo.GinkgoT()
			db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
			defer db.Close()
			r := validResource()
			gomega.Expect(l.RegisterResource(encode(t, r))).To(gomega.Succeed())
			job := admitScheduling(t, l, r.ID, "availability-guard-job")
			seed := AvailabilityRequest{ResourceID: r.ID, MutationKey: "seed-pause", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: "maintenance"}
			_, err := l.SetAvailability(seed, false)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			q := seed
			if resume {
				q.ExpectedAvailabilityRevision = 2
				q.ReadinessDigest, q.ObservationReference = digest, "isolated-readiness"
			}
			change(&q)
			before := readTask007GuardState(t, db, l, job.JobID)
			ginkgo.By("rejecting the intent before a direct mutation or receipt replay")
			_, err = l.SetAvailability(q, resume)
			gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
			gomega.Expect(readTask007GuardState(t, db, l, job.JobID)).To(gomega.Equal(before))
			_, found, err := l.ReplayAvailability(q, resume)
			gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
			gomega.Expect(found).To(gomega.BeFalse())
			gomega.Expect(readTask007GuardState(t, db, l, job.JobID)).To(gomega.Equal(before))
		}, entries)
	}

	ginkgo.DescribeTable("does not apply pause reason limits to a valid resume intent", func(reason string) {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
		defer db.Close()
		r := validResource()
		gomega.Expect(l.RegisterResource(encode(t, r))).To(gomega.Succeed())
		_, err := l.SetAvailability(AvailabilityRequest{ResourceID: r.ID, MutationKey: "reason-pause", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: "maintenance"}, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q := AvailabilityRequest{ResourceID: r.ID, MutationKey: "reason-resume", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 2, Reason: reason, ReadinessDigest: digest, ObservationReference: "isolated-readiness"}
		_, found, err := l.ReplayAvailability(q, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(found).To(gomega.BeFalse())
		resumed, err := l.SetAvailability(q, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(resumed.Availability).To(gomega.Equal("available"))
		replay, found, err := l.ReplayAvailability(q, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(found).To(gomega.BeTrue())
		gomega.Expect(replay).To(gomega.Equal(resumed))
	}, ginkgo.Entry("empty reason", ""), ginkgo.Entry("reason over 256 bytes", strings.Repeat("é", 129)))
})
