//go:build integration

package managed

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func recoveryEvidenceForTest() RecoveryEvidence {
	return RecoveryEvidence{Source: "operator-attested", Observer: "fixture-operator", ObservedAt: time.Now().UTC(), Mechanism: "scoped-no-start-observation", Digest: RecoveryDigest("fixture"), Reference: "fixture:no-start"}
}

var _ = ginkgo.Describe("Task011RecoveryLedger", ginkgo.Label("integration"), func() {
	ginkgo.It("retains issued adapter generations and uncertain victims across reopening without another command", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "recovery.sqlite")
		db, l := openLedger(t, path, 1)
		vmScheduling(t, l, "victim", 1, "started", true)
		vmScheduling(t, l, "target", 2, "stopped", false)
		_, err := l.BindInstallation("adapter-recovery-fixture")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		j := validJob()
		j.ResourceID = "target"
		j.Deadlines.QueueSeconds = 600
		r, err := l.Admit(encode(t, j), digest, "adapter-key", "adapter-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = l.Reserve(ReserveRequest{JobID: r.JobID, ExpectedJobRevision: r.JobRevision, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "adapter-reserve", VMGuard: &VMReservationGuard{HostLimit: 1, ObservationRevisions: map[string]int64{"victim": 1, "target": 1}}})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		p, err := l.PowerIntent(1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(l.ClaimPowerEffect(p.Identity, "stop")).To(gomega.Succeed())
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, l = openLedger(t, path, 1)
		defer db.Close()
		gomega.Expect(l.MarkInterrupted()).To(gomega.Succeed())
		s, err := l.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(s.Resource.Kind).To(gomega.Equal("utm-macos"))
		gomega.Expect(s.Receipt.ResourceOccupancy).To(gomega.Equal(1))
		gomega.Expect(s.AdapterIntents).To(gomega.HaveLen(1))
		gomega.Expect(s.AdapterIntents[0].Identity).To(gomega.Equal(p.Identity))
		gomega.Expect(s.AdapterIntents[0].Stage).To(gomega.Equal("stop_issued"))
		victim, err := l.ResourceRecoverySnapshot("victim")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(victim.AdapterIntents).To(gomega.Equal(s.AdapterIntents))
		gomega.Expect(l.ClaimPowerEffect(p.Identity, "stop")).To(gomega.HaveOccurred())
		q := PowerConfirmation{Identity: p.Identity, ExpectedStage: "stop_issued", VictimPower: "uncertain", TargetPower: "stopped", EvidenceDigest: digest}
		stale := q
		stale.Identity.OperationGeneration++
		gomega.Expect(l.ConfirmPower(stale)).To(gomega.HaveOccurred())
		gomega.Expect(l.ConfirmPower(q)).To(gomega.Succeed())
		after, err := l.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after.AdapterIntents[0].State).To(gomega.Equal("uncertain"))
		gomega.Expect(after.AdapterIntents[0].Stage).To(gomega.Equal("stop_uncertain"))
		gomega.Expect(RecoveryDigest(after)).NotTo(gomega.Equal(RecoveryDigest(s)))
		victimAfter, err := l.ResourceRecoverySnapshot("victim")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(RecoveryDigest(victimAfter)).NotTo(gomega.Equal(RecoveryDigest(victim)))
		gomega.Expect(l.ClaimPowerEffect(p.Identity, "start")).To(gomega.HaveOccurred())
	})
	ginkgo.It("keeps planning read-only, fences stale observations, preserves provenance and replays one intent", func() {
		t := ginkgo.GinkgoT()
		db, l, r := cancellationFixture(t)
		r, err := reserveScheduling(l, r, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = l.BindInstallation("recovery-fixture")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(l.MarkInterrupted()).To(gomega.Succeed())
		s, err := l.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var before, after int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&before)).To(gomega.Succeed())
		for range 3 {
			same, err := l.RecoverySnapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(RecoveryDigest(same)).To(gomega.Equal(RecoveryDigest(s)))
		}
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&after)).To(gomega.Succeed())
		gomega.Expect(after).To(gomega.Equal(before))
		out := LocalOutcome{WorkloadOutcome: "inconclusive"}
		q := JobRecoveryRequest{Identity: s.Identity, SnapshotDigest: RecoveryDigest(s), MutationKey: "explicit-recovery", Action: "no-start", NextState: "settling", NoOwnedEffect: true, Outcome: &out, Evidence: recoveryEvidenceForTest()}
		for _, stamp := range []string{"2026-10-02T00:00:00Z", "2026-10-02T00:00:00+00:00"} {
			evidence := q.Evidence
			evidence.ObservedAt, err = time.Parse(time.RFC3339, stamp)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			original := RecoveryDigest(evidence)
			gomega.Expect(ValidateRecoveryEvidence(evidence)).To(gomega.Succeed())
			gomega.Expect(RecoveryDigest(evidence)).To(gomega.Equal(original))
		}
		for _, stamp := range []string{"2026-10-02T09:00:00+09:00", "2026-10-01T20:00:00-04:00", "0001-01-01T00:00:00Z"} {
			bad := q
			bad.Evidence.ObservedAt, err = time.Parse(time.RFC3339, stamp)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = l.BeginJobRecovery(bad)
			gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
			unchanged, err := l.RecoverySnapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(unchanged).To(gomega.Equal(s))
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&after)).To(gomega.Succeed())
			gomega.Expect(after).To(gomega.Equal(before))
		}
		ack, err := l.Cancel(CancelRequest{JobID: r.JobID, ExpectedRevision: s.Receipt.JobRevision, MutationKey: "change-recovery-basis"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = l.BeginJobRecovery(q)
		gomega.Expect(errors.Is(err, ErrStale)).To(gomega.BeTrue())
		unchanged, err := l.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(unchanged).To(gomega.Equal(ack.Receipt))
		s, err = l.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q.SnapshotDigest = RecoveryDigest(s)
		pass := LocalOutcome{Started: true, WorkloadOutcome: "pass"}
		bad := q
		bad.Outcome = &pass
		_, err = l.BeginJobRecovery(bad)
		gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		applied, err := l.BeginJobRecovery(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(applied.JobState).To(gomega.Equal("settling"))
		gomega.Expect(applied.ResourceOccupancy).To(gomega.Equal(1))
		replay, err := l.BeginJobRecovery(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay).To(gomega.Equal(applied))
		frozen, err := l.LocalOutcome(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(frozen.WorkloadOutcome).To(gomega.Equal(WorkloadOutcome("inconclusive")))
		intent, err := l.Cancellation(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(intent.Requested).To(gomega.BeTrue())
		var count int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='recovery_intent'`, r.JobID).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.Equal(1))
	})
	ginkgo.It("appends a resolution without overwriting an unproven observation or extending its deadline", func() {
		db, l, r := cancellationFixture(ginkgo.GinkgoT())
		_ = db
		r, err := reserveScheduling(l, r, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		id, err := l.LocalIdentity(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		now := time.Now().UTC().Add(-20 * time.Second)
		p := LocalPhase{Schema: "haepari.phase-receipt/v1", Identity: id, Phase: "prepare", BudgetSeconds: 10, StartedAt: now, Deadline: now.Add(10 * time.Second), ObservedAt: now}
		r, err = l.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: r.JobRevision, Record: p, MutationKey: "interrupted-prepare-intent"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		p.Completed, p.Outcome, p.ObservedAt = true, "unproven", time.Now().UTC()
		_, err = l.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: r.JobRevision, Record: p, MutationKey: "interrupted-prepare-observed"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(l.MarkInterrupted()).To(gomega.Succeed())
		r, err = l.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		resolved := p
		resolved.Outcome = "complete"
		resolved.ObservedAt = time.Now().UTC()
		q := PhaseResolutionRequest{ExpectedRevision: r.JobRevision, OriginalDigest: RecoveryDigest(p), Record: resolved, Evidence: recoveryEvidenceForTest(), MutationKey: "resolve-prepare"}
		_, err = l.ResolveLocalPhase(q)
		gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		q.Record.Outcome = "late"
		q.Record.Deadline = q.Record.Deadline.Add(time.Second)
		_, err = l.ResolveLocalPhase(q)
		gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		q.Record.Deadline = p.Deadline
		applied, err := l.ResolveLocalPhase(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		replay, err := l.ResolveLocalPhase(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay).To(gomega.Equal(applied))
		original, err := l.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(original["prepare"]).To(gomega.Equal(p))
		s, err := l.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(s.Phases["prepare"]).To(gomega.Equal(q.Record))
		gomega.Expect(s.Receipt.ResourceOccupancy).To(gomega.Equal(1))
	})
})
