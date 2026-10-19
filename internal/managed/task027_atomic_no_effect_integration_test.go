//go:build integration

package managed

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func atomicDrainedFixture(t ginkgo.GinkgoTInterface) (*sql.DB, *Ledger, DrainedVMSettlementRequest, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, l := openLedger(t, path, 1)
	target := vmScheduling(t, l, "target", 2, "started", false)
	job := admitScheduling(t, l, target.ID, "crash-job")
	reserved, err := reserveScheduling(l, job, vmGuard(t, db, 1))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	seedHistoricalPower(t, db, reserved, target, nil, PowerStartReady)
	gomega.Expect(l.DrainHistoricalPower()).To(gomega.Succeed())
	id, err := l.LocalIdentity(job.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	boundary, err := l.DrainedVMPreflight(id)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return db, l, DrainedVMSettlementRequest{Identity: id, BoundaryDigest: boundary.EvidenceDigest(), MutationKey: "vm-no-effect-" + id.JobID}, path
}

func atomicNoEffectCounts(db *sql.DB) []int {
	counts := make([]int, 4)
	for i, query := range []string{
		`SELECT count(*) FROM managed_no_effect_observations`,
		`SELECT count(*) FROM managed_mutations WHERE mutation_key LIKE 'vm-no-effect-%'`,
		`SELECT count(*) FROM managed_events WHERE kind='no_effect_classified'`,
		`SELECT count(*) FROM managed_events WHERE kind='finished'`,
	} {
		gomega.Expect(db.QueryRow(query).Scan(&counts[i])).To(gomega.Succeed())
	}
	return counts
}

var _ = ginkgo.Describe("Task027AtomicNoEffect", ginkgo.Label("integration"), func() {
	for _, after := range []string{"observation", "classification"} {
		after := after
		ginkgo.It("rolls back every no-effect step when failing after "+after, func() {
			db, l, q, _ := atomicDrainedFixture(ginkgo.GinkgoT())
			defer db.Close()
			before, err := l.Snapshot(q.Identity.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var deadline int64
			gomega.Expect(db.QueryRow(`SELECT queue_deadline_ns FROM managed_jobs WHERE id=?`, q.Identity.JobID).Scan(&deadline)).To(gomega.Succeed())
			var trigger string
			if after == "observation" {
				trigger = `CREATE TRIGGER fail_atomic_classification BEFORE UPDATE OF state ON managed_jobs WHEN NEW.state='settling' BEGIN SELECT RAISE(ABORT,'after observation'); END`
			} else {
				trigger = `CREATE TRIGGER fail_atomic_classification BEFORE UPDATE OF state ON managed_jobs WHEN NEW.state='finished' BEGIN SELECT RAISE(ABORT,'after classification'); END`
			}
			_, err = db.Exec(trigger)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = l.SettleDrainedVMPreflight(q)
			gomega.Expect(err).To(gomega.HaveOccurred())
			current, err := l.Snapshot(q.Identity.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current).To(gomega.Equal(before))
			gomega.Expect(atomicNoEffectCounts(db)).To(gomega.Equal([]int{0, 0, 0, 0}))
			_, err = db.Exec(`DROP TRIGGER fail_atomic_classification`)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			reopened, err := NewLedger(db, 1)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			reopened.UseLegacyFixtureBounds()
			gomega.Expect(reopened.MarkInterrupted()).To(gomega.Succeed())
			finished, err := reopened.SettleDrainedVMPreflight(q)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(finished.JobState).To(gomega.Equal("finished"))
			gomega.Expect(finished.Verdict).To(gomega.Equal(Incomplete))
			gomega.Expect(finished.ResourceOccupancy).To(gomega.BeZero())
			replay, err := reopened.SettleDrainedVMPreflight(q)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(replay).To(gomega.Equal(finished))
			gomega.Expect(atomicNoEffectCounts(db)).To(gomega.Equal([]int{1, 1, 1, 1}))
			var saved int64
			gomega.Expect(db.QueryRow(`SELECT queue_deadline_ns FROM managed_jobs WHERE id=?`, q.Identity.JobID).Scan(&saved)).To(gomega.Succeed())
			gomega.Expect(saved).To(gomega.Equal(deadline))
		})
	}
	for _, boundary := range []string{"before_commit", "after_commit"} {
		boundary := boundary
		ginkgo.It("survives a real child-process death "+boundary+" with the same request and one capacity return", func() {
			t := ginkgo.GinkgoT()
			db, l, q, path := atomicDrainedFixture(t)
			request, _ := json.Marshal(q)
			var deadline int64
			gomega.Expect(db.QueryRow(`SELECT queue_deadline_ns FROM managed_jobs WHERE id=?`, q.Identity.JobID).Scan(&deadline)).To(gomega.Succeed())
			gomega.Expect(db.Close()).To(gomega.Succeed())
			exe, err := os.Executable()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			child := exec.Command(exe)
			child.Env = append(os.Environ(), "HAEPARI_STORAGE_PROBE="+path, "HAEPARI_STORAGE_BOUNDARY="+boundary, "HAEPARI_STORAGE_OPERATION=vm-drained-settle")
			output, err := os.Create(path + ".output")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer output.Close()
			child.Stdout = output
			child.Stderr = output
			gomega.Expect(child.Start()).To(gomega.Succeed())
			waited := false
			t.Cleanup(func() {
				if !waited {
					_ = child.Process.Kill()
					_ = child.Wait()
				}
			})
			reached := false
			until := time.Now().Add(10 * time.Second)
			for time.Now().Before(until) {
				if _, err := os.Stat(path + ".barrier"); err == nil {
					reached = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !reached {
				raw, _ := os.ReadFile(path + ".output")
				t.Fatalf("atomic commit barrier missing: %s", raw)
			}
			gomega.Expect(child.Process.Kill()).To(gomega.Succeed())
			gomega.Expect(child.Wait()).To(gomega.HaveOccurred())
			waited = true
			db, l = openLedger(t, path, 1)
			defer db.Close()
			if boundary == "before_commit" {
				gomega.Expect(atomicNoEffectCounts(db)).To(gomega.Equal([]int{0, 0, 0, 0}))
			} else {
				gomega.Expect(atomicNoEffectCounts(db)).To(gomega.Equal([]int{1, 1, 1, 1}))
			}
			gomega.Expect(l.MarkInterrupted()).To(gomega.Succeed())
			var savedRequest DrainedVMSettlementRequest
			gomega.Expect(json.Unmarshal(request, &savedRequest)).To(gomega.Succeed())
			gomega.Expect(savedRequest).To(gomega.Equal(q))
			first, err := l.SettleDrainedVMPreflight(savedRequest)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			second, err := l.SettleDrainedVMPreflight(savedRequest)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(second).To(gomega.Equal(first))
			gomega.Expect(first.Verdict).To(gomega.Equal(Incomplete))
			gomega.Expect(first.ResourceOccupancy).To(gomega.BeZero())
			gomega.Expect(first.CapacityDelta).To(gomega.Equal(-1))
			gomega.Expect(atomicNoEffectCounts(db)).To(gomega.Equal([]int{1, 1, 1, 1}))
			var saved int64
			gomega.Expect(db.QueryRow(`SELECT queue_deadline_ns FROM managed_jobs WHERE id=?`, q.Identity.JobID).Scan(&saved)).To(gomega.Succeed())
			gomega.Expect(saved).To(gomega.Equal(deadline))
		})
	}
	ginkgo.It("honors cancellation committed before settlement and never changes the original receipt afterward", func() {
		db, l, q, _ := atomicDrainedFixture(ginkgo.GinkgoT())
		defer db.Close()
		snapshot, err := l.Snapshot(q.Identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = l.Cancel(CancelRequest{JobID: q.Identity.JobID, ExpectedRevision: snapshot.JobRevision, MutationKey: "cancel-before-no-effect"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		first, err := l.SettleDrainedVMPreflight(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(first.Verdict).To(gomega.Equal(Cancelled))
		replay, err := l.SettleDrainedVMPreflight(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay).To(gomega.Equal(first))
		gomega.Expect(atomicNoEffectCounts(db)).To(gomega.Equal([]int{1, 1, 1, 1}))
	})
	ginkgo.It("keeps a settled no-effect result immutable when cancellation arrives later", func() {
		db, l, q, _ := atomicDrainedFixture(ginkgo.GinkgoT())
		defer db.Close()
		first, err := l.SettleDrainedVMPreflight(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(first.Verdict).To(gomega.Equal(Incomplete))
		_, err = l.Cancel(CancelRequest{JobID: q.Identity.JobID, ExpectedRevision: first.JobRevision, MutationKey: "cancel-after-no-effect"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		result, err := l.Result(q.Identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(Incomplete))
		replay, err := l.SettleDrainedVMPreflight(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay).To(gomega.Equal(first))
		gomega.Expect(atomicNoEffectCounts(db)).To(gomega.Equal([]int{1, 1, 1, 1}))
	})
	ginkgo.It("keeps a genuine claimed run on its original recovery route and blocks stale no-guest authority", func() {
		db, l, q, _ := atomicDrainedFixture(ginkgo.GinkgoT())
		defer db.Close()
		r, err := l.Snapshot(q.Identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		run := q.Identity
		run.RunID = "original-run"
		_, err = l.ClaimVMRun(run, r.JobRevision, json.RawMessage(`{}`))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		current, err := l.LocalIdentity(q.Identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current).To(gomega.Equal(run))
		disposition, err := l.GuestPowerDisposition(current)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(disposition).To(gomega.Equal("clear"))
		_, err = l.SettleDrainedVMPreflight(q)
		gomega.Expect(errors.Is(err, ErrStale)).To(gomega.BeTrue())
		gomega.Expect(atomicNoEffectCounts(db)).To(gomega.Equal([]int{0, 0, 0, 0}))
		r, err = l.Snapshot(q.Identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(r.ResourceOccupancy).To(gomega.Equal(1))
	})

	ginkgo.It("refuses a changed boundary, lease, or existing conflicting key without consuming another key", func() {
		db, l, q, _ := atomicDrainedFixture(ginkgo.GinkgoT())
		defer db.Close()
		changed := q
		changed.BoundaryDigest = strings.Repeat("b", 64)
		_, err := l.SettleDrainedVMPreflight(changed)
		gomega.Expect(errors.Is(err, ErrStale)).To(gomega.BeTrue())
		gomega.Expect(atomicNoEffectCounts(db)).To(gomega.Equal([]int{0, 0, 0, 0}))
		changed = q
		changed.Identity.LeaseGeneration++
		_, err = l.SettleDrainedVMPreflight(changed)
		gomega.Expect(errors.Is(err, ErrStale)).To(gomega.BeTrue())
		r, err := l.Snapshot(q.Identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		proof := noEffectProof()
		_, err = l.RecordNoEffectObservation(NoEffectObservationRequest{JobID: r.JobID, ExpectedJobRevision: r.JobRevision, ResourceID: r.ResourceID, ExpectedConfigRevision: r.ResourceConfigRevision, Slot: r.Slot, LeaseGeneration: r.LeaseGeneration, Source: "machine", Mechanism: "adapter-inspect", EvidenceDigest: digest, Proof: proof, MutationKey: q.MutationKey})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		before, err := l.Snapshot(q.Identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		counts := atomicNoEffectCounts(db)
		_, err = l.SettleDrainedVMPreflight(q)
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
		after, err := l.Snapshot(q.Identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
		gomega.Expect(atomicNoEffectCounts(db)).To(gomega.Equal(counts))
	})
})
