//go:build integration

package managed

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func cancellationFixture(t ginkgo.GinkgoTInterface) (*sql.DB, *Ledger, Receipt) {
	db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 2)
	t.Cleanup(func() { _ = db.Close() })
	gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
	r := admitScheduling(t, l, "local-a", "cancel-job")
	return db, l, r
}
func cancellationRunning(t ginkgo.GinkgoTInterface, l *Ledger, r Receipt) (Receipt, LocalIdentity) {
	var err error
	r, err = reserveScheduling(l, r, nil)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	r, err = l.AdvanceLocal(r.JobID, r.JobRevision, "preparing", "ready", "ready-test")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	r, err = l.StartLocal(r.JobID, r.JobRevision, "run-test")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	id, err := l.LocalIdentity(r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return r, id
}
func cancellationProof(t ginkgo.GinkgoTInterface, l *Ledger, r Receipt, id LocalIdentity, out LocalOutcome) Receipt {
	var err error
	r, err = l.RecordLocalOutcome(LocalOutcomeRequest{Identity: id, ExpectedRevision: r.JobRevision, Outcome: out, MutationKey: "observed-test"})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	r, err = l.CompleteLocalRun(LocalTerminationRequest{Identity: id, ExpectedRevision: r.JobRevision, MutationKey: "stopped-test"})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return r
}
func cancellationResult(r Receipt, out WorkloadOutcome, code *int, signal string) Result {
	verdict := Pass
	if out == "fail" {
		verdict = Fail
	}
	if out == "cancelled" {
		verdict = Cancelled
	}
	return Result{Schema: "haepari.result/v1", JobID: r.JobID, ResourceID: r.ResourceID, ResourceRevision: r.ResourceConfigRevision, LeaseGeneration: r.LeaseGeneration, JobState: "finished", ResourceAvailability: r.ResourceAvailability, WorkloadOutcome: out, EvidenceCompleteness: "complete", CleanupOutcome: "verified", FinalVerdict: verdict, NativeExitCode: code, NativeSignal: signal, Artifacts: []ResultArtifact{{Path: "results/build.json", SizeBytes: 1, SHA256: digest}}}
}

var _ = ginkgo.Describe("Task008CancellationLedger", ginkgo.Label("integration"), func() {
	ginkgo.It("retains exact cancellation admission and intent after fixture-only bulk Job retention", func() {
		db, l, r := cancellationFixture(ginkgo.GinkgoT())
		q := CancelRequest{r.JobID, r.JobRevision, "retained-cancel"}
		original, err := l.Cancel(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(l.RetainAdmissionTombstone(r.JobID)).To(gomega.Succeed())
		// TASK-012 owns production GC. Keep permanent admission, mutation and
		// cancellation intent; this fixture removes only the bulk Job children.
		for _, table := range []string{"managed_job_inputs", "managed_runs", "managed_local_outcomes", "managed_cancel_edges", "managed_events", "managed_event_owners", "managed_event_progress", "managed_jobs"} {
			_, err = db.Exec("DELETE FROM " + table)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		replay, err := l.Cancel(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.Mutation).To(gomega.Equal(original.Mutation))
		gomega.Expect(replay.Cancellation).To(gomega.Equal(original.Cancellation))
		gomega.Expect(replay.Receipt.JobState).To(gomega.Equal("finished"))
		gomega.Expect(replay.Receipt.ResultStatus).To(gomega.Equal("unavailable"))
		_, err = l.Cancel(CancelRequest{r.JobID, r.JobRevision, "retained-late-cancel"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})
	ginkgo.It("keeps the queued admission's pinned configuration in its cancelled result after an idle resource update", func() {
		t := ginkgo.GinkgoT()
		_, l, r := cancellationFixture(t)
		resource := validResource()
		resource.Revision = 2
		gomega.Expect(l.UpdateResource(encode(t, resource), 1, 1)).To(gomega.Succeed())
		ack, err := l.Cancel(CancelRequest{r.JobID, r.JobRevision, "updated-resource-cancel"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ack.Receipt.ResourceConfigRevision).To(gomega.Equal(int64(2)))
		result, err := l.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.ResourceRevision).To(gomega.Equal(int64(1)))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(Cancelled))
		gomega.Expect(ack.Receipt.GlobalOccupancy).To(gomega.BeZero())
	})
	ginkgo.It("fences outcome observation to resource, configuration, slot, lease, run and nonce without consuming a refused key", func() {
		t := ginkgo.GinkgoT()
		db, l, r := cancellationFixture(t)
		r, id := cancellationRunning(t, l, r)
		before, err := l.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, change := range []func(*LocalIdentity){func(x *LocalIdentity) { x.ResourceID = "other" }, func(x *LocalIdentity) { x.ConfigRevision++ }, func(x *LocalIdentity) { x.Slot++ }, func(x *LocalIdentity) { x.LeaseGeneration++ }, func(x *LocalIdentity) { x.RunID = "other-run" }, func(x *LocalIdentity) { x.Nonce = "other-nonce" }} {
			wrong := id
			change(&wrong)
			_, err := l.RecordLocalOutcome(LocalOutcomeRequest{Identity: wrong, ExpectedRevision: r.JobRevision, Outcome: LocalOutcome{Started: true, WorkloadOutcome: "fail"}, MutationKey: "owned-observation"})
			gomega.Expect(errors.Is(err, ErrStale)).To(gomega.BeTrue())
		}
		current, err := l.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current).To(gomega.Equal(before))
		var count int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_mutations WHERE mutation_key='owned-observation'`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.BeZero())
		_, err = l.RecordLocalOutcome(LocalOutcomeRequest{Identity: id, ExpectedRevision: r.JobRevision, Outcome: LocalOutcome{Started: true, WorkloadOutcome: "fail"}, MutationKey: "owned-observation"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})
	ginkgo.It("records reconciliation cancellation as deferred without inferring execution, outcome or capacity release", func() {
		t := ginkgo.GinkgoT()
		_, l, r := cancellationFixture(t)
		r, _ = cancellationRunning(t, l, r)
		gomega.Expect(l.ReconcileLocalFailure(r.JobID)).To(gomega.Succeed())
		r, err := l.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ack, err := l.Cancel(CancelRequest{r.JobID, r.JobRevision, "reconcile-cancel"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ack.Receipt.JobState).To(gomega.Equal("reconciling"))
		gomega.Expect(ack.Receipt.ResourceAvailability).To(gomega.Equal("quarantined"))
		gomega.Expect(ack.Receipt.GlobalOccupancy).To(gomega.Equal(1))
		gomega.Expect(ack.Cancellation.Deferred).To(gomega.BeTrue())
		gomega.Expect(ack.Cancellation.BeforeOutcome).To(gomega.BeFalse())
		gomega.Expect(ack.Cancellation.TerminationProven).To(gomega.BeFalse())
		_, err = l.LocalOutcome(r.JobID)
		gomega.Expect(errors.Is(err, ErrNotFound)).To(gomega.BeTrue())
	})
	ginkgo.It("migrates an actual schema5 reservation without inventing cancel or termination evidence", func() {
		t := ginkgo.GinkgoT()
		db, l, r := cancellationFixture(t)
		r, err := reserveScheduling(l, r, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, name := range append(append([]string{}, cancellationTables...), "managed_installation") {
			_, err := db.Exec("DROP TABLE " + name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		_, err = db.Exec(`DROP TABLE managed_lock_requests; DROP TABLE managed_lock_slots; DROP TABLE managed_lock_clock; DROP TABLE managed_lock_discontinuities; DROP TABLE managed_runner_receipts; DELETE FROM schema_migrations WHERE version=8; DELETE FROM schema_migrations WHERE version=7; DELETE FROM schema_migrations WHERE version=6; PRAGMA user_version=5`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		l, err = NewLedger(db, 2)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		current, err := l.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.JobRevision).To(gomega.Equal(r.JobRevision))
		gomega.Expect(current.JobState).To(gomega.Equal("preparing"))
		gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
		status, err := l.Cancellation(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Requested).To(gomega.BeFalse())
		gomega.Expect(status.TerminationProven).To(gomega.BeFalse())
		_, err = l.LocalOutcome(r.JobID)
		gomega.Expect(errors.Is(err, ErrNotFound)).To(gomega.BeTrue())
	})
	ginkgo.It("blocks settlement of an observed native outcome until a separate tree/no-start proof exists", func() {
		t := ginkgo.GinkgoT()
		_, l, r := cancellationFixture(t)
		r, id := cancellationRunning(t, l, r)
		code := 7
		r, err := l.RecordLocalOutcome(LocalOutcomeRequest{Identity: id, ExpectedRevision: r.JobRevision, Outcome: LocalOutcome{Started: true, WorkloadOutcome: "fail", NativeExitCode: &code}, MutationKey: "native-only"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, p := range [][2]string{{"running", "collecting"}, {"collecting", "cleaning"}, {"cleaning", "settling"}} {
			r, err = l.AdvanceLocal(r.JobID, r.JobRevision, p[0], p[1], p[1]+"-test")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		_, err = l.SettleLocal(r.JobID, r.JobRevision, cancellationResult(r, "fail", &code, ""))
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		current, err := l.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
	})
	for _, cancelFirst := range []bool{true, false} {
		first := cancelFirst
		ginkgo.It("serializes queued cancel/reservation ordering with a real commit barrier: cancel first="+map[bool]string{true: "true", false: "false"}[first], func() {
			t := ginkgo.GinkgoT()
			db, l, r := cancellationFixture(t)
			reached, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			l.transactionBoundary = func(stage string) {
				if stage == "before_commit" {
					once.Do(func() { close(reached); <-release })
				}
			}
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			cancel := func() error { _, err := l.Cancel(CancelRequest{r.JobID, r.JobRevision, "queued-cancel"}); return err }
			reserve := func() error { _, err := reserveScheduling(l, r, nil); return err }
			a, b := cancel, reserve
			if !first {
				a, b = reserve, cancel
			}
			done := make(chan error, 1)
			go func() { done <- a() }()
			<-reached
			second := make(chan error, 1)
			started := make(chan struct{})
			go func() { close(started); second <- b() }()
			<-started
			close(release)
			gomega.Expect(<-done).NotTo(gomega.HaveOccurred())
			gomega.Expect(errors.Is(<-second, ErrStale)).To(gomega.BeTrue())
			current, err := l.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if first {
				gomega.Expect(current.JobState).To(gomega.Equal("finished"))
				gomega.Expect(current.LeaseGeneration).To(gomega.BeZero())
				gomega.Expect(current.GlobalOccupancy).To(gomega.BeZero())
				result, err := l.Result(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(result.FinalVerdict).To(gomega.Equal(Cancelled))
			} else {
				gomega.Expect(current.JobState).To(gomega.Equal("preparing"))
				gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
			}
			var count int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_runs`).Scan(&count)).To(gomega.Succeed())
			gomega.Expect(count).To(gomega.BeZero())
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_power_transitions`).Scan(&count)).To(gomega.Succeed())
			gomega.Expect(count).To(gomega.BeZero())
		})
	}
	for _, phase := range []string{"collecting", "cleaning", "settling"} {
		phase := phase
		ginkgo.It("keeps known failure and rebases only sticky cancellation revisions during "+phase, func() {
			t := ginkgo.GinkgoT()
			db, l, r := cancellationFixture(t)
			r, id := cancellationRunning(t, l, r)
			code := 7
			r = cancellationProof(t, l, r, id, LocalOutcome{Started: true, WorkloadOutcome: "fail", NativeExitCode: &code})
			var err error
			if phase != "collecting" {
				r, err = l.AdvanceLocal(r.JobID, r.JobRevision, "collecting", "cleaning", "collection-test")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			if phase == "settling" {
				r, err = l.AdvanceLocal(r.JobID, r.JobRevision, "cleaning", "settling", "cleanup-test")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			base := r
			q := CancelRequest{r.JobID, r.JobRevision, "late-cancel"}
			ack, err := l.Cancel(q)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(ack.Cancellation.BeforeOutcome).To(gomega.BeFalse())
			gomega.Expect(ack.Cancellation.TerminationProven).To(gomega.BeTrue())
			second, err := l.Cancel(CancelRequest{r.JobID, ack.Receipt.JobRevision, "late-cancel-2"})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			replay, err := l.Cancel(q)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(replay.Mutation).To(gomega.Equal(ack.Mutation))
			gomega.Expect(replay.Receipt.JobRevision).To(gomega.Equal(second.Receipt.JobRevision))
			switch phase {
			case "collecting":
				r, err = l.AdvanceLocal(base.JobID, base.JobRevision, "collecting", "cleaning", "collection-test")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				r, err = l.AdvanceLocal(r.JobID, r.JobRevision, "cleaning", "settling", "cleanup-test")
			case "cleaning":
				r, err = l.AdvanceLocal(base.JobID, base.JobRevision, "cleaning", "settling", "cleanup-test")
			case "settling":
				r = base
			}
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = l.SetAvailability(AvailabilityRequest{MutationKey: "pause-test", ResourceID: r.ResourceID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, ExpectedLeaseGeneration: r.LeaseGeneration, Reason: "operator"}, false)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			result := cancellationResult(r, "fail", &code, "")
			finished, err := l.SettleLocal(r.JobID, r.JobRevision, result)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(finished.ResourceAvailability).To(gomega.Equal("paused"))
			gomega.Expect(finished.GlobalOccupancy).To(gomega.BeZero())
			before, err := l.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(before.WorkloadOutcome).To(gomega.Equal(WorkloadOutcome("fail")))
			gomega.Expect(before.FinalVerdict).To(gomega.Equal(Fail))
			terminal, err := l.Cancel(CancelRequest{r.JobID, 1, "terminal-cancel"})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(terminal.Receipt.JobRevision).To(gomega.Equal(finished.JobRevision))
			after, err := l.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(after).To(gomega.Equal(before))
			var count int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_cancellations`).Scan(&count)).To(gomega.Succeed())
			gomega.Expect(count).To(gomega.Equal(1))
		})
	}
	ginkgo.It("rejects changed-key payloads, stale public revisions and unrelated internal outcome revisions without mutation", func() {
		t := ginkgo.GinkgoT()
		db, l, r := cancellationFixture(t)
		r, id := cancellationRunning(t, l, r)
		old := r
		r, err := l.RecordLocalOutcome(LocalOutcomeRequest{Identity: id, ExpectedRevision: r.JobRevision, Outcome: LocalOutcome{Started: true, WorkloadOutcome: "pass"}, MutationKey: "native-test"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = l.AdvanceLocal(r.JobID, old.JobRevision, "running", "collecting", "stale-phase")
		gomega.Expect(errors.Is(err, ErrStale)).To(gomega.BeTrue())
		_, err = l.Cancel(CancelRequest{r.JobID, old.JobRevision, "stale-public"})
		gomega.Expect(errors.Is(err, ErrStale)).To(gomega.BeTrue())
		q := CancelRequest{r.JobID, r.JobRevision, "cancel-test"}
		ack, err := l.Cancel(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ack.Cancellation.BeforeOutcome).To(gomega.BeFalse())
		q.ExpectedRevision++
		_, err = l.Cancel(q)
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
		var count int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_mutations WHERE mutation_key IN ('stale-phase','stale-public')`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.BeZero())
		current, err := l.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.JobRevision).To(gomega.Equal(ack.Receipt.JobRevision))
	})
	ginkgo.It("cancel-before-native freezes cancelled and refuses no-effect proof or missing active outputs", func() {
		t := ginkgo.GinkgoT()
		_, l, r := cancellationFixture(t)
		r, id := cancellationRunning(t, l, r)
		ack, err := l.Cancel(CancelRequest{r.JobID, r.JobRevision, "early-cancel"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ack.Receipt.JobState).To(gomega.Equal("cancelling"))
		gomega.Expect(ack.Receipt.GlobalOccupancy).To(gomega.Equal(1))
		gomega.Expect(ack.Cancellation.TerminationProven).To(gomega.BeFalse())
		r, err = l.RecordLocalOutcome(LocalOutcomeRequest{Identity: id, ExpectedRevision: ack.Receipt.JobRevision, Outcome: LocalOutcome{Started: true, WorkloadOutcome: "fail", NativeSignal: "terminated"}, MutationKey: "native-test"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		out, err := l.LocalOutcome(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(out.WorkloadOutcome).To(gomega.Equal(WorkloadOutcome("cancelled")))
		_, err = l.CompleteLocalRun(LocalTerminationRequest{Identity: id, ExpectedRevision: r.JobRevision, NoOwnedEffect: true, MutationKey: "bad-no-effect"})
		gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		r, err = l.CompleteLocalRun(LocalTerminationRequest{Identity: id, ExpectedRevision: r.JobRevision, MutationKey: "termination-test"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, p := range [][2]string{{"collecting", "cleaning"}, {"cleaning", "settling"}} {
			r, err = l.AdvanceLocal(r.JobID, r.JobRevision, p[0], p[1], p[1]+"-test")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		result := cancellationResult(r, "cancelled", nil, "terminated")
		result.Artifacts = nil
		_, err = l.SettleLocal(r.JobID, r.JobRevision, result)
		gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		result.Reason = "cancel_before_start"
		_, err = l.SettleLocal(r.JobID, r.JobRevision, result)
		gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		current, err := l.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
	})
	ginkgo.It("survives reopen with original ACK and current intent, and refuses a lost schema6 cancellation table", func() {
		t := ginkgo.GinkgoT()
		db, l, r := cancellationFixture(t)
		var path string
		var seq int
		var name string
		gomega.Expect(db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path)).To(gomega.Succeed())
		q := CancelRequest{r.JobID, r.JobRevision, "queued-replay"}
		ack, err := l.Cancel(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, l = openLedger(t, path, 2)
		defer db.Close()
		replay, err := l.Cancel(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay).To(gomega.Equal(ack))
		_, err = db.Exec(`DROP TABLE managed_cancel_edges`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = NewLedger(db, 2)
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
	ginkgo.It("does not treat a forged no-start reason without a durable outcome as permission to omit outputs", func() {
		t := ginkgo.GinkgoT()
		_, l, r := cancellationFixture(t)
		r, _ = cancellationRunning(t, l, r)
		var err error
		for _, p := range [][2]string{{"running", "collecting"}, {"collecting", "cleaning"}, {"cleaning", "settling"}} {
			r, err = l.AdvanceLocal(r.JobID, r.JobRevision, p[0], p[1], p[1]+"-test")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		result := cancellationResult(r, "cancelled", nil, "")
		result.Reason = "cancel_before_start"
		result.Artifacts = nil
		data, _ := json.Marshal(result)
		_, err = DecodeResultForJob(data, validJob())
		gomega.Expect(err).NotTo(gomega.HaveOccurred()) // envelope describes a possible no-start; settlement requires proof
		_, err = l.SettleLocal(r.JobID, r.JobRevision, result)
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
	})
})
