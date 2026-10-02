//go:build integration

package managed

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task010DurablePhases", ginkgo.Label("integration"), func() {
	ginkgo.It("fences phase intent, configured budget and completion without extending its deadline across reopen", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "state.sqlite")
		db, l := openLedger(t, path, 1)
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		r := admitScheduling(t, l, "local-a", "phase-job")
		r, err := reserveScheduling(l, r, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		id, err := l.LocalIdentity(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		now := time.Now().UTC()
		p := LocalPhase{Schema: "haepari.phase-receipt/v1", Identity: id, Phase: "prepare", BudgetSeconds: 10, StartedAt: now, Deadline: now.Add(10 * time.Second), ObservedAt: now}
		q := LocalPhaseRequest{ExpectedRevision: r.JobRevision, Record: p, MutationKey: "phase-start"}
		for _, change := range []func(*LocalIdentity){func(x *LocalIdentity) { x.ResourceID = "other" }, func(x *LocalIdentity) { x.ConfigRevision++ }, func(x *LocalIdentity) { x.Slot++ }, func(x *LocalIdentity) { x.LeaseGeneration++ }, func(x *LocalIdentity) { x.RunID = "other" }, func(x *LocalIdentity) { x.Nonce = "other" }} {
			wrong := q
			change(&wrong.Record.Identity)
			_, err := l.RecordLocalPhase(wrong)
			gomega.Expect(errors.Is(err, ErrStale)).To(gomega.BeTrue())
		}
		wrong := q
		wrong.Record.BudgetSeconds++
		_, err = l.RecordLocalPhase(wrong)
		gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		started, err := l.RecordLocalPhase(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		replay, err := l.RecordLocalPhase(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay).To(gomega.Equal(started))
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, l = openLedger(t, path, 1)
		defer db.Close()
		phases, err := l.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["prepare"]).To(gomega.Equal(p))
		p.Completed, p.Outcome, p.ObservedAt = true, "failed", time.Now().UTC()
		end := LocalPhaseRequest{ExpectedRevision: started.JobRevision, Record: p, MutationKey: "phase-end"}
		wrong = end
		wrong.Record.Deadline = wrong.Record.Deadline.Add(-time.Second)
		_, err = l.RecordLocalPhase(wrong)
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
		wrong = end
		wrong.Record.ObservedAt = now.Add(-time.Second)
		_, err = l.RecordLocalPhase(wrong)
		gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		// Cancellation changes preparing to collecting. That state edge needs
		// the current receipt; it cannot silently rebase an old phase revision.
		ack, err := l.Cancel(CancelRequest{JobID: r.JobID, ExpectedRevision: started.JobRevision, MutationKey: "cancel-phase"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = l.RecordLocalPhase(end)
		gomega.Expect(errors.Is(err, ErrStale)).To(gomega.BeTrue())
		end.ExpectedRevision = ack.Receipt.JobRevision
		ended, err := l.RecordLocalPhase(end)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		stopped, err := l.RecordLocalPreparationStop(LocalPreparationStopRequest{Identity: id, ExpectedRevision: ended.JobRevision, Reason: "prepare_failed", NoOwnedEffect: true, MutationKey: "phase-stop"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		outcome, err := l.LocalOutcome(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(outcome.WorkloadOutcome).To(gomega.Equal(WorkloadOutcome("cancelled")))
		start := time.Now().UTC()
		p = LocalPhase{Schema: "haepari.phase-receipt/v1", Identity: id, Phase: "verify", StartedAt: start, ObservedAt: start}
		stopped, err = l.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: stopped.JobRevision, Record: p, MutationKey: "start-verify"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		p.Completed, p.ObservedAt, p.Outcome = true, time.Now().UTC(), "complete"
		stopped, err = l.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: stopped.JobRevision, Record: p, MutationKey: "end-verify"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		result := Result{Schema: "haepari.result/v1", JobID: r.JobID, ResourceID: r.ResourceID, ResourceRevision: r.ResourceConfigRevision, LeaseGeneration: r.LeaseGeneration, JobState: "finished", ResourceAvailability: "available", WorkloadOutcome: "cancelled", EvidenceCompleteness: "complete", CleanupOutcome: "not_needed", FinalVerdict: Cancelled, Reason: "cancel_before_start"}
		terminal, err := l.SettleLocal(r.JobID, stopped.JobRevision, result)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(terminal.CapacityDelta).To(gomega.Equal(-1))
		replay, err = l.SettleLocal(r.JobID, stopped.JobRevision, result)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay).To(gomega.Equal(terminal))
		stored, err := l.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(stored.Timings.AdmittedAt).NotTo(gomega.BeEmpty())
		gomega.Expect(stored.Timings.ReservedAt).NotTo(gomega.BeEmpty())
		gomega.Expect(stored.Timings.Phases).To(gomega.HaveLen(2))
		gomega.Expect([]string{stored.Timings.Phases[0].Phase, stored.Timings.Phases[1].Phase}).To(gomega.Equal([]string{"prepare", "verify"}))
		var events int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='finished'`, r.JobID).Scan(&events)).To(gomega.Succeed())
		gomega.Expect(events).To(gomega.Equal(1))
	})

	for _, refusal := range []string{"reason", "run_identity", "cancel_without_intent", "wrong_state", "run_claim"} {
		refusal := refusal
		ginkgo.It("refuses preparation-stop classification for "+refusal+" without changing ownership", func() {
			db, l, r := cancellationFixture(ginkgo.GinkgoT())
			var id LocalIdentity
			var err error
			if refusal == "run_claim" {
				r, id = cancellationRunning(ginkgo.GinkgoT(), l, r)
				// A caller cannot erase the durable claim by presenting the
				// pre-run identity. The identity fence rejects it first.
				id.RunID = ""
			} else {
				r, err = reserveScheduling(l, r, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				id, err = l.LocalIdentity(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			if refusal == "wrong_state" {
				gomega.Expect(l.ReconcileLocalFailure(r.JobID)).To(gomega.Succeed())
				r, err = l.Snapshot(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			q := LocalPreparationStopRequest{Identity: id, ExpectedRevision: r.JobRevision, Reason: "prepare_failed", NoOwnedEffect: true, MutationKey: "refused-stop"}
			want := ErrInvalid
			switch refusal {
			case "reason":
				q.Reason = "process_start_failed"
			case "run_identity":
				q.Identity.RunID = "invented-run"
			case "cancel_without_intent":
				q.Reason, want = "cancel_before_start", ErrConflict
			case "wrong_state":
				want = ErrConflict
			case "run_claim":
				want = ErrStale
			}
			var mutations, events int
			before, err := l.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_mutations`).Scan(&mutations)).To(gomega.Succeed())
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=?`, r.JobID).Scan(&events)).To(gomega.Succeed())
			_, err = l.RecordLocalPreparationStop(q)
			gomega.Expect(errors.Is(err, want)).To(gomega.BeTrue(), "exact refusal class")
			current, err := l.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current).To(gomega.Equal(before))
			gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
			gomega.Expect(current.ResourceOccupancy).To(gomega.Equal(1))
			var afterMutations, afterEvents, outcomes int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_mutations`).Scan(&afterMutations)).To(gomega.Succeed())
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=?`, r.JobID).Scan(&afterEvents)).To(gomega.Succeed())
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_local_outcomes WHERE job_id=?`, r.JobID).Scan(&outcomes)).To(gomega.Succeed())
			gomega.Expect(afterMutations).To(gomega.Equal(mutations))
			gomega.Expect(afterEvents).To(gomega.Equal(events))
			gomega.Expect(outcomes).To(gomega.BeZero())
		})
	}

	for _, terminal := range []string{"cancel", "expire"} {
		terminal := terminal
		ginkgo.It("projects only admission timing for queued "+terminal+" without a capacity return", func() {
			db, l, r := cancellationFixture(ginkgo.GinkgoT())
			var finished Receipt
			var err error
			if terminal == "cancel" {
				ack, cancelErr := l.Cancel(CancelRequest{JobID: r.JobID, ExpectedRevision: r.JobRevision, MutationKey: "queued-timing-cancel"})
				finished, err = ack.Receipt, cancelErr
			} else {
				_, err = db.Exec(`UPDATE managed_jobs SET queue_deadline_ns=0 WHERE id=?`, r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				finished, err = l.ExpireQueued(r.JobID, r.JobRevision, "queued-timing-expire")
			}
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(finished.CapacityDelta).To(gomega.BeZero())
			gomega.Expect(finished.GlobalOccupancy).To(gomega.BeZero())
			gomega.Expect(finished.ResourceOccupancy).To(gomega.BeZero())
			result, err := l.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			admitted, err := time.Parse(time.RFC3339Nano, result.Timings.AdmittedAt)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(admitted.IsZero()).To(gomega.BeFalse())
			gomega.Expect(result.Timings.ReservedAt).To(gomega.BeEmpty())
			gomega.Expect(result.Timings.Phases).To(gomega.Equal([]ResultPhase{}))
			gomega.Expect(result.Timings.AdapterSteps).To(gomega.Equal([]ResultStep{}))
			gomega.Expect(result.CleanupOutcome).To(gomega.Equal(CleanupOutcome("not_needed")))
		})
	}

	for _, refusal := range []string{"deadline_budget", "foreign_path", "non_collect_artifact", "non_collect_evidence", "wrong_state"} {
		refusal := refusal
		ginkgo.It("refuses invalid phase "+refusal+" without writing facts or changing ownership", func() {
			db, l, r := cancellationFixture(ginkgo.GinkgoT())
			r, err := reserveScheduling(l, r, nil)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			id, err := l.LocalIdentity(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			now := time.Now().UTC()
			p := LocalPhase{Schema: "haepari.phase-receipt/v1", Identity: id, Phase: "prepare", BudgetSeconds: 10, StartedAt: now, Deadline: now.Add(10 * time.Second), ObservedAt: now}
			want := ErrInvalid
			switch refusal {
			case "deadline_budget":
				p.Deadline = p.Deadline.Add(time.Nanosecond)
			case "foreign_path":
				p.Completed, p.Outcome = true, "failed"
				p.ProtectedEvidence = []ProtectedEvidence{{Path: "../foreign", Reason: "retain", RecoveryReference: "job:" + r.JobID}}
			case "non_collect_artifact":
				p.Completed, p.Outcome = true, "complete"
				p.Artifacts = []ResultArtifact{{Path: "results/build.json", SizeBytes: 1, SHA256: digest}}
			case "non_collect_evidence":
				p.Completed, p.Outcome, p.EvidenceCompleteness = true, "complete", "complete"
			case "wrong_state":
				p.Phase, want = "collect", ErrConflict
			}
			var mutations, events int
			before, err := l.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_mutations`).Scan(&mutations)).To(gomega.Succeed())
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&events)).To(gomega.Succeed())
			_, err = l.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: r.JobRevision, Record: p, MutationKey: "refused-phase"})
			gomega.Expect(errors.Is(err, want)).To(gomega.BeTrue())
			current, err := l.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current).To(gomega.Equal(before))
			phases, err := l.LocalPhases(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(phases).To(gomega.BeEmpty())
			var afterMutations, afterEvents int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_mutations`).Scan(&afterMutations)).To(gomega.Succeed())
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&afterEvents)).To(gomega.Succeed())
			gomega.Expect(afterMutations).To(gomega.Equal(mutations))
			gomega.Expect(afterEvents).To(gomega.Equal(events))
		})
	}

	for _, mismatch := range []string{"incomplete_run", "unproven_run", "unproven_collect", "unproven_cleanup", "unproven_verify", "cleanup", "artifacts", "evidence", "protected_evidence"} {
		mismatch := mismatch
		ginkgo.It("blocks terminal return for "+mismatch+" phase facts", func() {
			_, l, r := cancellationFixture(ginkgo.GinkgoT())
			r, id := cancellationRunning(ginkgo.GinkgoT(), l, r)
			var err error
			if mismatch == "incomplete_run" || mismatch == "unproven_run" {
				now := time.Now().UTC()
				p := LocalPhase{Schema: "haepari.phase-receipt/v1", Identity: id, Phase: "run", BudgetSeconds: 10, StartedAt: now, Deadline: now.Add(10 * time.Second), ObservedAt: now}
				r, err = l.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: r.JobRevision, Record: p, MutationKey: "unfinished-run"})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				if mismatch == "unproven_run" {
					p.Completed, p.Outcome, p.ObservedAt = true, "unproven", time.Now().UTC()
					r, err = l.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: r.JobRevision, Record: p, MutationKey: "unproven-run"})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
			}
			r = cancellationProof(ginkgo.GinkgoT(), l, r, id, LocalOutcome{Started: true, WorkloadOutcome: "pass"})
			artifacts := []ResultArtifact{{Path: "results/build.json", SizeBytes: 1, SHA256: digest}}
			for _, phase := range []string{"collect", "cleanup", "verify"} {
				now := time.Now().UTC()
				p := LocalPhase{Schema: "haepari.phase-receipt/v1", Identity: id, Phase: phase, BudgetSeconds: 10, StartedAt: now, Deadline: now.Add(10 * time.Second), ObservedAt: now}
				if phase == "verify" {
					p.BudgetSeconds, p.Deadline = 0, time.Time{}
				}
				r, err = l.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: r.JobRevision, Record: p, MutationKey: "intent-" + phase})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				p.Completed, p.Outcome, p.ObservedAt = true, "complete", time.Now().UTC()
				if phase == "collect" {
					p.Artifacts, p.EvidenceCompleteness = artifacts, "complete"
				}
				if phase == "cleanup" && mismatch == "cleanup" {
					p.Outcome = "failed"
				}
				if phase == "cleanup" && mismatch == "protected_evidence" {
					p.Outcome = "failed"
				}
				if mismatch == "unproven_"+phase {
					p.Outcome = "unproven"
				}
				r, err = l.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: r.JobRevision, Record: p, MutationKey: "observed-" + phase})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				if phase != "verify" {
					from, to := "collecting", "cleaning"
					if phase == "cleanup" {
						from, to = "cleaning", "settling"
					}
					r, err = l.AdvanceLocal(r.JobID, r.JobRevision, from, to, "advance-"+phase)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
			}
			result := cancellationResult(r, "pass", nil, "")
			if mismatch == "artifacts" {
				result.Artifacts[0].SizeBytes++
			}
			if mismatch == "evidence" {
				result.EvidenceCompleteness, result.FinalVerdict = "partial", Incomplete
			}
			if mismatch == "protected_evidence" {
				result.ResourceAvailability, result.CleanupOutcome, result.FinalVerdict = "quarantined", "failed", Incomplete
				result.ProtectedEvidence = []ProtectedEvidence{{Path: "workspace", Reason: "cleanup_incomplete", RecoveryReference: "job:" + r.JobID}}
			}
			before, err := l.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = l.SettleLocal(r.JobID, r.JobRevision, result)
			if mismatch == "incomplete_run" || mismatch == "unproven_run" || mismatch == "unproven_collect" || mismatch == "unproven_cleanup" || mismatch == "unproven_verify" {
				gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
			} else {
				gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
			}
			current, err := l.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current).To(gomega.Equal(before))
			gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
			gomega.Expect(current.JobState).To(gomega.Equal("settling"))
			_, err = l.Result(r.JobID)
			gomega.Expect(errors.Is(err, ErrBusy)).To(gomega.BeTrue())
		})
	}

	ginkgo.It("refuses every preparation/no-start omission reason against an entered workload", func() {
		_, l, r := cancellationFixture(ginkgo.GinkgoT())
		r, id := cancellationRunning(ginkgo.GinkgoT(), l, r)
		r = cancellationProof(ginkgo.GinkgoT(), l, r, id, LocalOutcome{Started: true, WorkloadOutcome: "inconclusive"})
		var err error
		for _, states := range [][2]string{{"collecting", "cleaning"}, {"cleaning", "settling"}} {
			r, err = l.AdvanceLocal(r.JobID, r.JobRevision, states[0], states[1], "forged-"+states[1])
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		for _, reason := range []string{"prepare_failed", "prepare_deadline", "process_start_failed"} {
			result := cancellationResult(r, "inconclusive", nil, "")
			result.FinalVerdict, result.Reason, result.Artifacts = Incomplete, reason, nil
			_, err = l.SettleLocal(r.JobID, r.JobRevision, result)
			gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		}
		current, err := l.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
	})

	ginkgo.It("refuses lost active phase evidence rather than restarting its deadline or inventing completion", func() {
		db, l, r := cancellationFixture(ginkgo.GinkgoT())
		r, err := reserveScheduling(l, r, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		id, err := l.LocalIdentity(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		now := time.Now().UTC()
		p := LocalPhase{Schema: "haepari.phase-receipt/v1", Identity: id, Phase: "prepare", BudgetSeconds: 10, StartedAt: now, Deadline: now.Add(10 * time.Second), ObservedAt: now}
		r, err = l.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: r.JobRevision, Record: p, MutationKey: "lost-start"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`DELETE FROM managed_events WHERE cursor=?`, r.EventCursor)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = l.LocalPhases(r.JobID)
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		_, err = l.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: r.JobRevision, Record: p, MutationKey: "restarted-deadline"})
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		current, err := l.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
	})

	ginkgo.It("blocks proofless legacy settlement", func() {
		_, l, r := cancellationFixture(ginkgo.GinkgoT())
		r, id := cancellationRunning(ginkgo.GinkgoT(), l, r)
		var err error
		for _, phase := range [][2]string{{"running", "collecting"}, {"collecting", "cleaning"}, {"cleaning", "settling"}} {
			r, err = l.AdvanceLocal(r.JobID, r.JobRevision, phase[0], phase[1], "proofless-"+phase[1])
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		_, err = l.SettleLocal(r.JobID, r.JobRevision, cancellationResult(r, "pass", nil, ""))
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(l.ReconcileLocalFailure(r.JobID)).To(gomega.Succeed())
		gomega.Expect(id.RunID).NotTo(gomega.BeEmpty())
		current, err := l.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
	})
})
