//go:build integration

package managed

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"path/filepath"
)

func openLedger(t ginkgo.GinkgoTInterface, path string, limit int) (*sql.DB, *Ledger) {
	t.Helper()
	db, err := sqlite.Open(path)
	if err != nil {
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
	l, err := NewLedger(db, limit)
	if err != nil {
		db.Close()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
	l.UseLegacyFixtureBounds()
	return db, l
}

func noEffectProof() NoEffectProof {
	return NoEffectProof{ObservationReference: "observed-prerequisite-no-effect", NoOwnedJobEffect: true, NoUnprovenProcess: true, NoProtectedPath: true, PowerReconciled: true}
}

func recordObservation(t ginkgo.GinkgoTInterface, l *Ledger, jobID string, revision int64, resourceID string, slot int, lease int64, proof NoEffectProof) Receipt {
	t.Helper()
	receipt, err := l.RecordNoEffectObservation(NoEffectObservationRequest{JobID: jobID, ExpectedJobRevision: revision, ResourceID: resourceID,
		ExpectedConfigRevision: 1, Slot: slot, LeaseGeneration: lease, Source: "machine", Mechanism: "adapter-inspect", EvidenceDigest: digest,
		Proof: proof, MutationKey: "observe-" + jobID})
	if err != nil {
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
	return receipt
}

var _ = ginkgo.Describe("ReservedNoEffectReplayAfterRestart", ginkgo.Label("integration"), func() {
	ginkgo.It("ReservedNoEffectReplayAfterRestart", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "state.sqlite")
		db, l := openLedger(t, path, 2)
		resource := validResource()
		resource.Capacity = 1
		if err := l.RegisterResource(encode(t, resource)); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		job := validJob()
		admitted, err := l.Admit(encode(t, job), digest, "request-1", "job-1")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if admitted.GlobalOccupancy != 0 || admitted.JobState != "queued" {
			t.Fatalf("admitted: %+v", admitted)
		}
		reserve := ReserveRequest{JobID: "job-1", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-1"}
		reserved, err := l.Reserve(reserve)
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if reserved.CapacityDelta != 1 || reserved.GlobalOccupancy != 1 || reserved.Slot != 0 || reserved.LeaseGeneration != 1 {
			t.Fatalf("reserved: %+v", reserved)
		}
		if replay, err := l.Reserve(reserve); err != nil || replay != reserved {
			t.Fatalf("reserve replay: %+v %v", replay, err)
		}
		proof := noEffectProof()
		if _, err := l.ClassifyNoEffect(ClassifyNoEffectRequest{JobID: "job-1", ExpectedJobRevision: 2, ResourceID: "local-a", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Slot: 0, LeaseGeneration: 1, FailureKind: "cancel", Proof: proof, MutationKey: "unobserved-classify"}); !errors.Is(err, ErrBlocked) {
			t.Fatalf("missing durable observation: %v", err)
		}
		recordObservation(t, l, "job-1", 2, "local-a", 0, 1, proof)
		classify := ClassifyNoEffectRequest{JobID: "job-1", ExpectedJobRevision: 3, ResourceID: "local-a", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Slot: 0, LeaseGeneration: 1, FailureKind: "cancel", Proof: proof, MutationKey: "classify-1"}
		classified, err := l.ClassifyNoEffect(classify)
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if classified.JobState != "settling" || classified.GlobalOccupancy != 1 {
			t.Fatalf("classification: %+v", classified)
		}
		if err := db.Close(); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		db, l = openLedger(t, path, 2)
		defer db.Close()
		settle := SettleNoEffectRequest{JobID: "job-1", ExpectedJobRevision: 4, ResourceID: "local-a", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Slot: 0, LeaseGeneration: 1, MutationKey: "settle-1"}
		finished, err := l.SettleNoEffect(settle)
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if finished.GlobalOccupancy != 0 || finished.ResourceOccupancy != 0 || finished.CapacityDelta != -1 || finished.Verdict != Cancelled || finished.ResourceAvailability != "available" {
			t.Fatalf("settled: %+v", finished)
		}
		result, err := l.Result(finished.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Timings).NotTo(gomega.BeNil())
		gomega.Expect(result.Timings.AdmittedAt).NotTo(gomega.BeEmpty())
		gomega.Expect(result.Timings.ReservedAt).NotTo(gomega.BeEmpty())
		gomega.Expect(result.Timings.Phases).To(gomega.BeEmpty())
		gomega.Expect(result.Timings.AdapterSteps).To(gomega.Equal([]ResultStep{}))
		if replay, err := l.SettleNoEffect(settle); err != nil || replay != finished {
			t.Fatalf("settle replay: %+v %v", replay, err)
		}
		settle.MutationKey = "second-settlement"
		if _, err := l.SettleNoEffect(settle); !errors.Is(err, ErrStale) {
			t.Fatalf("second settlement: %v", err)
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id='job-1' AND kind='finished'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("terminal event count %d: %v", count, err)
		}

	})
})

var _ = ginkgo.Describe("InterruptedLocalJobQuarantinesWithoutReleasingSlot", ginkgo.Label("integration"), func() {
	ginkgo.It("InterruptedLocalJobQuarantinesWithoutReleasingSlot", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
		defer db.Close()
		resource := validResource()
		resource.Capacity = 1
		if err := l.RegisterResource(encode(t, resource)); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		job := validJob()
		if _, err := l.Admit(encode(t, job), digest, "restart-key", "restart-job"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := l.Reserve(ReserveRequest{JobID: "restart-job", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "restart-reserve"}); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := l.MarkInterrupted(); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		seen, err := l.Snapshot("restart-job")
		if err != nil || seen.JobState != "reconciling" || seen.ResourceAvailability != "quarantined" || seen.ResourceOccupancy != 1 || seen.GlobalOccupancy != 1 {
			t.Fatalf("uncertain work released or advertised available: %+v %v", seen, err)
		}
		if err := l.MarkInterrupted(); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		again, err := l.Snapshot("restart-job")
		if err != nil || again.JobRevision != seen.JobRevision || again.ResourceAvailabilityRevision != seen.ResourceAvailabilityRevision {
			t.Fatalf("restart reconciliation changed settled uncertainty: %+v %v", again, err)
		}

	})
})

var _ = ginkgo.Describe("UnprovenCollectorKeepsLeaseAndQuarantines", ginkgo.Label("integration"), func() {
	ginkgo.It("UnprovenCollectorKeepsLeaseAndQuarantines", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
		defer db.Close()
		resource := validResource()
		resource.Capacity = 1
		if err := l.RegisterResource(encode(t, resource)); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := l.Admit(encode(t, validJob()), digest, "collector-key", "collector-job"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		receipt, err := l.Reserve(ReserveRequest{JobID: "collector-job", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "collector-reserve"})
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		receipt, err = l.AdvanceLocal("collector-job", receipt.JobRevision, "preparing", "ready", "collector-ready")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		receipt, err = l.StartLocal("collector-job", receipt.JobRevision, "collector-run")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := l.AdvanceLocal("collector-job", receipt.JobRevision, "running", "collecting", "collector-exit"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := l.ReconcileLocalFailure("collector-job"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		seen, err := l.Snapshot("collector-job")
		if err != nil || seen.JobState != "reconciling" || seen.ResourceAvailability != "quarantined" || seen.GlobalOccupancy != 1 || seen.ResourceOccupancy != 1 {
			t.Fatalf("unproven collector released or finished: %+v %v", seen, err)
		}
		if _, err := l.Result("collector-job"); !errors.Is(err, ErrBusy) {
			t.Fatalf("unproven collector had a terminal result: %v", err)
		}

	})
})

var _ = ginkgo.Describe("QueuedCancelAndCapacityFence", ginkgo.Label("integration"), func() {
	ginkgo.It("QueuedCancelAndCapacityFence", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
		defer db.Close()
		r := validResource()
		r.Capacity = 2
		if err := l.RegisterResource(encode(t, r)); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		job := validJob()
		if _, err := l.Admit(encode(t, job), digest, "request-1", "job-1"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := l.Admit(encode(t, job), digest, "request-2", "job-2"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := l.Admit(encode(t, job), digest, "request-3", "job-3"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := l.Reserve(ReserveRequest{JobID: "job-1", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-1"}); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := l.Reserve(ReserveRequest{JobID: "job-2", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-2"}); !errors.Is(err, ErrBusy) {
			t.Fatalf("global limit: %v", err)
		}
		cancelled, err := l.CancelQueued("job-2", 1, "cancel-2")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if cancelled.GlobalOccupancy != 1 || cancelled.CapacityDelta != 0 || cancelled.Slot != -1 || cancelled.Verdict != Cancelled {
			t.Fatalf("queued cancel: %+v", cancelled)
		}
		if replay, err := l.CancelQueued("job-2", 1, "cancel-2"); err != nil || replay != cancelled {
			t.Fatalf("cancel replay: %+v %v", replay, err)
		}
		if _, err := l.CancelQueued("job-2", 1, "changed-cancel"); !errors.Is(err, ErrStale) {
			t.Fatalf("stale cancel: %v", err)
		}
		if _, err := l.Admit(encode(t, job), digest, "request-1", "different-id"); err != nil {
			t.Fatalf("same submitted request replay: %v", err)
		}
		job.Suite = "other"
		if _, err := l.Admit(encode(t, job), digest, "request-1", "different-id"); !errors.Is(err, ErrConflict) {
			t.Fatalf("changed digest: %v", err)
		}

	})
})

var _ = ginkgo.Describe("VMFailurePauseOrderingAndStaleRegistration", ginkgo.Label("integration"), func() {
	ginkgo.It("VMFailurePauseOrderingAndStaleRegistration", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 2)
		defer db.Close()
		r := validResource()
		r.ID = "vm-a"
		r.Kind = "utm-macos"
		r.Capacity = 1
		r.Cleanup = nil
		r.Adapter.UTMCtl = "/Applications/UTM.app/Contents/MacOS/utmctl"
		r.VM = &VM{UTMVMUUID: "11111111-1111-4111-8111-111111111111", MACAddress: "aa:bb:cc:dd:ee:ff", AddressDiscovery: "dhcp-lease-by-mac", GuestJobRoot: "/var/haepari/jobs"}
		r.Guest = &Guest{ConnectionReference: "ssh-a", KnownHostsReference: "key-a"}
		if err := l.RegisterResource(encode(t, r)); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		other := validResource()
		if err := l.RegisterResource(encode(t, other)); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		job := validJob()
		job.ResourceID = "vm-a"
		if _, err := l.Admit(encode(t, job), digest, "request-vm", "job-vm"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := db.Exec(`INSERT INTO managed_power_transitions(operation_generation,victim_resource_id,target_resource_id,identity_json,state) VALUES(2,'vm-a','other-vm','{}','pending')`); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := l.Reserve(ReserveRequest{JobID: "job-vm", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-power-pending"}); !errors.Is(err, ErrBusy) {
			t.Fatalf("pending power reservation: %v", err)
		}
		if _, err := db.Exec(`UPDATE managed_power_transitions SET state='reconciled' WHERE operation_generation=2`); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		observed, err := l.ObserveVM(VMObservation{ResourceID: "vm-a", ConfigRevision: 1, VMUUID: r.VM.UTMVMUUID, Power: "started", EvidenceDigest: digest})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		reserved, err := l.Reserve(ReserveRequest{JobID: "job-vm", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-vm", VMGuard: &VMReservationGuard{HostLimit: 1, ObservationRevisions: map[string]int64{"vm-a": observed}}})
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if reserved.GlobalOccupancy != 1 {
			t.Fatalf("reserved: %+v", reserved)
		}
		q := ClassifyNoEffectRequest{JobID: "job-vm", ExpectedJobRevision: 2, ResourceID: "vm-a", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Slot: 0, LeaseGeneration: 1, FailureKind: "prepare_failure", VMUnavailable: true, Proof: noEffectProof(), MutationKey: "classify-vm"}
		q.Proof.PowerReconciled = false
		if _, err := l.ClassifyNoEffect(q); !errors.Is(err, ErrInvalid) {
			t.Fatalf("uncertain power: %v", err)
		}
		q.Proof.PowerReconciled = true
		if _, err := db.Exec(`INSERT INTO managed_power_transitions(operation_generation,victim_resource_id,target_resource_id,identity_json,state) VALUES(1,'vm-a','other-vm','{}','uncertain')`); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := l.ClassifyNoEffect(q); !errors.Is(err, ErrBlocked) {
			t.Fatalf("uncertain power classification: %v", err)
		}
		if _, err := db.Exec(`UPDATE managed_power_transitions SET state='reconciled' WHERE operation_generation=1`); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		recordObservation(t, l, "job-vm", 2, "vm-a", 0, 1, q.Proof)
		q.ExpectedJobRevision = 3
		classified, err := l.ClassifyNoEffect(q)
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if classified.ResourceAvailability != "paused" || classified.ResourceAvailabilityRevision != 2 || classified.GlobalOccupancy != 1 {
			t.Fatalf("pause before release: %+v", classified)
		}
		var otherAvailability string
		if err := db.QueryRow(`SELECT availability FROM managed_resources WHERE id='local-a'`).Scan(&otherAvailability); err != nil || otherAvailability != "available" {
			t.Fatalf("other resource: %q %v", otherAvailability, err)
		}
		settle := SettleNoEffectRequest{JobID: "job-vm", ExpectedJobRevision: 4, ResourceID: "vm-a", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 2, Slot: 0, LeaseGeneration: 1, MutationKey: "settle-vm"}
		finished, err := l.SettleNoEffect(settle)
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if finished.Verdict != Incomplete || finished.ResourceAvailability != "paused" || finished.GlobalOccupancy != 0 {
			t.Fatalf("vm result: %+v", finished)
		}
		if _, err := l.Reserve(ReserveRequest{JobID: "job-vm", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "stale-reserve"}); !errors.Is(err, ErrStale) {
			t.Fatalf("stale job: %v", err)
		}
		// A new registration revision cannot be paused by the old failure report.
		r.Revision = 2
		if err := l.UpdateResource(encode(t, r), 1, 2); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		q.MutationKey = "stale-vm-failure"
		if _, err := l.ClassifyNoEffect(q); !errors.Is(err, ErrStale) {
			t.Fatalf("stale vm failure: %v", err)
		}

	})
})

var _ = ginkgo.Describe("QuarantinePreservedAndCursorScoped", ginkgo.Label("integration"), func() {
	ginkgo.It("QuarantinePreservedAndCursorScoped", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 2)
		defer db.Close()
		r := validResource()
		if err := l.RegisterResource(encode(t, r)); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		job := validJob()
		if _, err := l.Admit(encode(t, job), digest, "key-one", "job-one"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := l.Admit(encode(t, job), digest, "key-two", "job-two"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		reserved, err := l.Reserve(ReserveRequest{JobID: "job-one", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-one"})
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		// An independent recovery can quarantine the resource while this Job still
		// owns the slot. Classification and settlement preserve that state.
		if _, err := db.Exec(`UPDATE managed_resources SET availability='quarantined',availability_reason='other-slot',availability_revision=availability_revision+1 WHERE id='local-a'`); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		recordObservation(t, l, "job-one", 2, "local-a", reserved.Slot, reserved.LeaseGeneration, noEffectProof())
		if _, err := l.ClassifyNoEffect(ClassifyNoEffectRequest{JobID: "job-one", ExpectedJobRevision: 3, ResourceID: "local-a", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 2, Slot: reserved.Slot, LeaseGeneration: reserved.LeaseGeneration, FailureKind: "cancel", Proof: noEffectProof(), MutationKey: "classify-one"}); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		stale := SettleNoEffectRequest{JobID: "job-one", ExpectedJobRevision: 4, ResourceID: "local-a", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Slot: reserved.Slot, LeaseGeneration: reserved.LeaseGeneration, MutationKey: "settle-stale"}
		if _, err := l.SettleNoEffect(stale); !errors.Is(err, ErrStale) {
			t.Fatalf("stale availability: %v", err)
		}
		stale.ExpectedAvailabilityRevision = 2
		stale.MutationKey = "settle-current"
		finished, err := l.SettleNoEffect(stale)
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if finished.ResourceAvailability != "quarantined" || finished.CapacityDelta != -1 {
			t.Fatalf("quarantine changed: %+v", finished)
		}
		for _, tc := range []struct {
			availability string
			revision     int64
		}{
			{"quarantined", 2},
			{"paused", 3},
		} {
			if tc.availability == "paused" {
				if _, err := db.Exec(`UPDATE managed_resources SET availability='paused',availability_revision=3 WHERE id='local-a'`); err != nil {
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
			}
			_, err := l.Reserve(ReserveRequest{JobID: "job-two", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: tc.revision, MutationKey: "blocked-" + tc.availability})
			if !errors.Is(err, ErrBusy) {
				t.Fatalf("%s resource reserved successor: %v", tc.availability, err)
			}
			seen, err := l.Snapshot("job-two")
			if err != nil || seen.JobState != "queued" || seen.GlobalOccupancy != 0 || seen.LeaseGeneration != 0 {
				t.Fatalf("%s resource changed successor: %+v %v", tc.availability, seen, err)
			}
		}
		batch, err := l.EventsAfter("job-one", 0, 20)
		events := batch.Events
		if err != nil || len(events) != 5 {
			t.Fatalf("events: %+v %v", events, err)
		}
		if _, err := l.EventsAfter("job-two", events[0].Cursor, 20); !errors.Is(err, ErrInvalid) {
			t.Fatalf("cross-job cursor: %v", err)
		}
		for i := 1; i < len(events); i++ {
			if events[i].Cursor <= events[i-1].Cursor {
				t.Fatalf("nonmonotonic cursor: %+v", events)
			}
		}
		if err := l.PurgeEventsThrough("job-one", events[0].Cursor); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		resync, err := l.EventsAfter("job-one", events[0].Cursor, 20)
		if err != nil || resync.Resync == nil || resync.Resync.Verdict != Cancelled {
			t.Fatalf("purged cursor resync: %+v %v", resync, err)
		}
		otherBatch, err := l.EventsAfter("job-two", 0, 20)
		if err != nil || len(otherBatch.Events) != 1 {
			t.Fatalf("other job events: %+v %v", otherBatch, err)
		}
		otherCursor := otherBatch.Events[0].Cursor
		if _, err := l.CancelQueued("job-two", 1, "cancel-two"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := l.PurgeEventsThrough("job-two", otherCursor); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := l.EventsAfter("job-one", otherCursor, 20); !errors.Is(err, ErrInvalid) {
			t.Fatalf("deleted cross-job cursor: %v", err)
		}
		resync, err = l.EventsAfter("job-one", events[0].Cursor, 20)
		if err != nil || resync.Resync == nil {
			t.Fatalf("job resync after other activity: %+v %v", resync, err)
		}
		continued, err := l.EventsAfter("job-one", resync.NextCursor, 20)
		if err != nil || continued.Resync != nil || continued.NextCursor != resync.NextCursor {
			t.Fatalf("resume from job cursor: %+v %v", continued, err)
		}
		if err := l.PurgeEventsThrough("job-one", events[len(events)-1].Cursor); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		resync, err = l.EventsAfter("job-one", events[0].Cursor, 20)
		if err != nil || resync.Resync == nil || resync.CurrentCursor < events[len(events)-1].Cursor {
			t.Fatalf("fully purged cursor resync: %+v %v", resync, err)
		}
		resync, err = l.EventsAfter("job-one", 0, 20)
		if err != nil || resync.Resync == nil {
			t.Fatalf("purged initial page resync: %+v %v", resync, err)
		}

	})
})

var _ = ginkgo.Describe("Task023 event bound", func() {
	ginkgo.It("rejects an oversized event bound before insert and reads a small event after commit", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "event-bound.sqlite"), 1)
		defer db.Close()
		gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		admitted, err := l.Admit(encode(t, validJob()), digest, "event-bound-key", "event-bound-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var revision int64
		gomega.Expect(db.QueryRow(`SELECT revision FROM managed_jobs WHERE id=?`, admitted.JobID).Scan(&revision)).To(gomega.Succeed())
		eventsBefore := countQuery(db, `SELECT COUNT(*) FROM managed_events`)
		ownersBefore := countQuery(db, `SELECT COUNT(*) FROM managed_event_owners`)

		tx, err := db.Begin()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = tx.Exec(`UPDATE managed_jobs SET revision=revision+1 WHERE id=?`, admitted.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = addEvent(tx, admitted.JobID, "note", strings.Repeat("a", 2<<20))
		gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		gomega.Expect(countQueryTx(tx, `SELECT COUNT(*) FROM managed_events WHERE kind='note'`)).To(gomega.BeZero())
		gomega.Expect(tx.Rollback()).To(gomega.Succeed())

		var after int64
		gomega.Expect(db.QueryRow(`SELECT revision FROM managed_jobs WHERE id=?`, admitted.JobID).Scan(&after)).To(gomega.Succeed())
		gomega.Expect(after).To(gomega.Equal(revision))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_events`)).To(gomega.Equal(eventsBefore))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_event_owners`)).To(gomega.Equal(ownersBefore))

		tx, err = db.Begin()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		cursor, err := addEvent(tx, admitted.JobID, "note", map[string]string{"mark": "kept"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(tx.Commit()).To(gomega.Succeed())
		batch, err := l.EventsAfter(admitted.JobID, 0, 100)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var found bool
		for _, event := range batch.Events {
			if event.Cursor == cursor && event.Kind == "note" && string(event.Payload) == `{"mark":"kept"}` {
				found = true
			}
		}
		gomega.Expect(found).To(gomega.BeTrue())
	})
})

func countQueryTx(tx *sql.Tx, query string) int {
	var n int
	gomega.Expect(tx.QueryRow(query).Scan(&n)).To(gomega.Succeed())
	return n
}

// These supplementary layouts follow the schema-2 tables in c371408 and the
// schema-3 additions in eb9df35. They are not prior public-release fixtures.
func legacyCoreLayout(t ginkgo.GinkgoTInterface, db *sql.DB, version int) {
	t.Helper()
	for _, statement := range []string{
		`DROP TABLE managed_job_bounds; DELETE FROM schema_migrations WHERE version=12; DROP TABLE managed_effective_config; DROP TABLE managed_config_audits; DELETE FROM schema_migrations WHERE version=11; DROP TABLE managed_manual_sessions; DELETE FROM schema_migrations WHERE version=10; DROP TABLE managed_purges; DROP TABLE managed_evidence_recovery; DROP TABLE managed_runner_receipts; DROP TABLE managed_lock_requests; DROP TABLE managed_lock_slots; DROP TABLE managed_lock_clock; DROP TABLE managed_lock_discontinuities`,
		`DROP TABLE managed_installation; DROP TABLE managed_cancellations; DROP TABLE managed_cancel_edges; DROP TABLE managed_local_outcomes`,
		`DROP TABLE managed_parent_context; DROP TABLE managed_parent_nonces; DROP TABLE managed_queue_blockers; DROP TABLE managed_vm_observations; DROP TABLE managed_power_progress; DROP TABLE managed_availability_receipts`,
		`DROP TABLE managed_admissions; DROP TABLE managed_job_inputs; DROP TABLE managed_runs`,
		`ALTER TABLE managed_resources DROP COLUMN idle_since_ns`,
		"DELETE FROM schema_migrations WHERE version>" + fmt.Sprint(version),
		`INSERT OR IGNORE INTO schema_migrations(version,identity) VALUES(1,'managed-initial-v1')`,
		"PRAGMA user_version=" + fmt.Sprint(version),
	} {
		_, err := db.Exec(statement)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
	if version == 1 {
		_, err := db.Exec(`DROP TABLE managed_no_effect_observations; ALTER TABLE managed_power_transitions DROP COLUMN reconciled_at_ns`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
}

var _ = ginkgo.Describe("RequiredBaseSchemaIntegrity", ginkgo.Label("integration"), func() {
	for _, version := range []int{1, 2} {
		ginkgo.It("migrates the actual legacy layout with keys and ownership preserved from version "+fmt.Sprint(version), func() {
			t := ginkgo.GinkgoT()
			path := filepath.Join(t.TempDir(), "state.sqlite")
			db, l := openLedger(t, path, 2)
			gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
			job := validJob()
			admitted, err := l.Admit(encode(t, job), digest, "legacy-admission", "legacy-job")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			q := ReserveRequest{JobID: admitted.JobID, ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "legacy-reserve"}
			reserved, err := l.Reserve(q)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var original []byte
			gomega.Expect(db.QueryRow(`SELECT receipt_json FROM managed_mutations WHERE mutation_key='legacy-reserve'`).Scan(&original)).To(gomega.Succeed())
			legacyCoreLayout(t, db, version)
			gomega.Expect(db.Close()).To(gomega.Succeed())
			db, err = sqlite.Open(path)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer db.Close()
			l, err = NewLedger(db, 2)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var current int
			gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&current)).To(gomega.Succeed())
			gomega.Expect(current).To(gomega.Equal(13))
			var retained []byte
			gomega.Expect(db.QueryRow(`SELECT receipt_json FROM managed_mutations WHERE mutation_key='legacy-reserve'`).Scan(&retained)).To(gomega.Succeed())
			gomega.Expect(retained).To(gomega.Equal(original))
			replay, err := l.Reserve(q)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(replay).To(gomega.Equal(reserved))
			q.ExpectedJobRevision++
			_, err = l.Reserve(q)
			gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
			// Versions 1/2 did not retain input objects/references. Preserve the
			// existing storage-uncertainty boundary, never fabricate sealed input
			// or turn a known admission key into a new executable Job.
			_, err = l.Admit(encode(t, job), digest, "legacy-admission", "unused-new-job")
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeFalse())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("input reference"))
			job.Suite = "changed-legacy-request"
			_, err = l.Admit(encode(t, job), digest, "legacy-admission", "unused-new-job")
			gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
			var state, key string
			var slot, occupancy int
			var lease, revision int64
			gomega.Expect(db.QueryRow(`SELECT j.state,j.request_key,j.slot,j.lease_generation,j.pinned_config_revision,r.occupancy FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, admitted.JobID).Scan(&state, &key, &slot, &lease, &revision, &occupancy)).To(gomega.Succeed())
			gomega.Expect(state).To(gomega.Equal("preparing"))
			gomega.Expect(key).To(gomega.Equal("legacy-admission"))
			gomega.Expect(slot).To(gomega.Equal(reserved.Slot))
			gomega.Expect(lease).To(gomega.Equal(reserved.LeaseGeneration))
			gomega.Expect(revision).To(gomega.Equal(reserved.ResourceConfigRevision))
			gomega.Expect(occupancy).To(gomega.Equal(1))
			gomega.Expect(l.MarkInterrupted()).To(gomega.Succeed())
			gomega.Expect(db.QueryRow(`SELECT state FROM managed_jobs WHERE id=?`, admitted.JobID).Scan(&state)).To(gomega.Succeed())
			gomega.Expect(state).To(gomega.Equal("reconciling"))
			gomega.Expect(db.QueryRow(`SELECT occupancy FROM managed_resources WHERE id=?`, job.ResourceID).Scan(&occupancy)).To(gomega.Succeed())
			gomega.Expect(occupancy).To(gomega.Equal(1))
			var count int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_jobs`).Scan(&count)).To(gomega.Succeed())
			gomega.Expect(count).To(gomega.Equal(1))
		})
	}
	for _, version := range []int{2, 3, 4, 5, 6, 7} {
		ginkgo.It("refuses a lost reconciliation column in version "+fmt.Sprint(version), func() {
			t := ginkgo.GinkgoT()
			path := filepath.Join(t.TempDir(), "state.sqlite")
			db, _ := openLedger(t, path, 2)
			if version == 2 {
				legacyCoreLayout(t, db, version)
			}
			_, err := db.Exec("ALTER TABLE managed_power_transitions DROP COLUMN reconciled_at_ns; PRAGMA user_version=" + fmt.Sprint(version))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(db.Close()).To(gomega.Succeed())
			db, err = sqlite.Open(path)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer db.Close()
			_, err = NewLedger(db, 2)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("reconciled_at_ns"))
			var current, count int
			gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&current)).To(gomega.Succeed())
			gomega.Expect(current).To(gomega.Equal(version))
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('managed_power_transitions') WHERE name='reconciled_at_ns'`).Scan(&count)).To(gomega.Succeed())
			gomega.Expect(count).To(gomega.BeZero())
		})
	}
	for _, table := range []string{"managed_runs", "managed_job_inputs"} {
		ginkgo.It("refuses a lost schema-3 "+table+" table", func() {
			db, _ := openLedger(ginkgo.GinkgoT(), filepath.Join(ginkgo.GinkgoT().TempDir(), "state.sqlite"), 2)
			defer db.Close()
			_, err := db.Exec("DROP TABLE " + table + "; PRAGMA user_version=3")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = NewLedger(db, 2)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring(table))
		})
	}
	for _, table := range []string{"schema_migrations", "managed_resources", "managed_jobs", "managed_events", "managed_event_owners", "managed_event_progress", "managed_mutations", "managed_runs", "managed_power_transitions", "managed_no_effect_observations", "managed_job_inputs", "managed_admissions", "managed_runner_receipts", "managed_purges", "managed_evidence_recovery", "managed_effective_config", "managed_config_audits", "managed_job_bounds"} {
		for _, loss := range []string{"table", "column"} {
			ginkgo.It("refuses a lost "+table+" "+loss+" without changing the surviving schema", func() {
				t := ginkgo.GinkgoT()
				path := filepath.Join(t.TempDir(), "state.sqlite")
				db, l := openLedger(t, path, 2)
				gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
				job := admitScheduling(t, l, "local-a", "retained-schema-job")
				_, err := l.Cancel(CancelRequest{job.JobID, job.JobRevision, "retained-schema-key"})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(db.Close()).To(gomega.Succeed())
				db, err = sqlite.Open(path)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				defer db.Close()
				_, err = db.Exec(`PRAGMA foreign_keys=OFF`)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				if loss == "table" {
					_, err = db.Exec("DROP TABLE " + table)
				} else {
					var column string
					gomega.Expect(db.QueryRow("SELECT name FROM pragma_table_info(?) ORDER BY cid LIMIT 1", table).Scan(&column)).To(gomega.Succeed())
					_, err = db.Exec("ALTER TABLE " + table + " RENAME COLUMN " + column + " TO lost_column")
				}
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				const schemaQuery = `SELECT COALESCE(group_concat(sql, ';'),'') FROM (SELECT sql FROM sqlite_schema ORDER BY type,name)`
				var before, after string
				gomega.Expect(db.QueryRow(schemaQuery).Scan(&before)).To(gomega.Succeed())
				_, err = NewLedger(db, 2)
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(err.Error()).To(gomega.ContainSubstring(table))
				gomega.Expect(db.QueryRow(schemaQuery).Scan(&after)).To(gomega.Succeed())
				gomega.Expect(after).To(gomega.Equal(before))
				var version int
				gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&version)).To(gomega.Succeed())
				gomega.Expect(version).To(gomega.Equal(13))
			})
		}
	}
	for _, version := range []int{1, 2, 3, 4, 5, 6} {
		ginkgo.It("rejects a lost receipt table before migrating version "+fmt.Sprint(version), func() {
			t := ginkgo.GinkgoT()
			db, l, job := cancellationFixture(t)
			_, err := l.Cancel(CancelRequest{job.JobID, job.JobRevision, "migration-key"})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = db.Exec("DROP TABLE managed_mutations; PRAGMA user_version=" + fmt.Sprint(version))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = NewLedger(db, 2)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("managed_mutations"))
			var current, count int
			gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&current)).To(gomega.Succeed())
			gomega.Expect(current).To(gomega.Equal(version))
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name='managed_mutations'`).Scan(&count)).To(gomega.Succeed())
			gomega.Expect(count).To(gomega.BeZero())
		})
	}
	ginkgo.It("refuses reopening after permanent cancellation receipts are lost", func() {
		t := ginkgo.GinkgoT()
		db, l, first := cancellationFixture(t)
		second := admitScheduling(t, l, "local-a", "second-cancel-job")
		_, err := l.Cancel(CancelRequest{first.JobID, first.JobRevision, "permanent-key"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = l.Cancel(CancelRequest{second.JobID, second.JobRevision, "permanent-key"})
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
		var path, name string
		var sequence int
		gomega.Expect(db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &path)).To(gomega.Succeed())
		_, err = db.Exec(`DROP TABLE managed_mutations`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, err = sqlite.Open(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		opened, err := NewLedger(db, 2)
		if err == nil {
			cancelled, replayErr := opened.Cancel(CancelRequest{second.JobID, second.JobRevision, "permanent-key"})
			t.Logf("lost receipt key reused on second Job: state=%s error=%v", cancelled.Receipt.JobState, replayErr)
		}
		gomega.Expect(err).To(gomega.HaveOccurred())
		var count int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name='managed_mutations'`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.BeZero())
		var state string
		gomega.Expect(db.QueryRow(`SELECT state FROM managed_jobs WHERE id=?`, second.JobID).Scan(&state)).To(gomega.Succeed())
		gomega.Expect(state).To(gomega.Equal("queued"))
	})
})

var _ = ginkgo.Describe("InitialSchemaRejectsNewerAndDuplicateVM", ginkgo.Label("integration"), func() {
	ginkgo.It("InitialSchemaRejectsNewerAndDuplicateVM", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "state.sqlite")
		db, l := openLedger(t, path, 1)
		var version int
		if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 13 {
			t.Fatalf("schema version %d: %v", version, err)
		}
		r := validResource()
		r.ID = "vm-a"
		r.Kind = "utm-macos"
		r.Capacity = 1
		r.Cleanup = nil
		r.Adapter.UTMCtl = "/Applications/UTM.app/Contents/MacOS/utmctl"
		r.VM = &VM{UTMVMUUID: "11111111-1111-4111-8111-111111111111", MACAddress: "aa:bb:cc:dd:ee:ff", AddressDiscovery: "dhcp-lease-by-mac", GuestJobRoot: "/var/haepari/jobs"}
		r.Guest = &Guest{ConnectionReference: "ssh-a", KnownHostsReference: "key-a"}
		if err := l.RegisterResource(encode(t, r)); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		r.ID = "vm-b"
		r.VM.MACAddress = "AA:BB:CC:DD:EE:FF"
		if err := l.RegisterResource(encode(t, r)); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate VM identity: %v", err)
		}
		if _, err := db.Exec(`PRAGMA user_version=14`); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := db.Close(); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		db, err := sqlite.Open(path)
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		defer db.Close()
		if _, err := NewLedger(db, 1); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("newer schema: %v", err)
		}

	})
})

var _ = ginkgo.Describe("ProvisionalV1PowerTableMigration", ginkgo.Label("integration"), func() {
	ginkgo.It("ProvisionalV1PowerTableMigration", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "state.sqlite")
		db, _ := openLedger(t, path, 1)
		legacyCoreLayout(t, db, 1)
		if err := db.Close(); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		db, l := openLedger(t, path, 1)
		defer db.Close()
		var version int
		if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 13 {
			t.Fatalf("migrated version %d: %v", version, err)
		}
		var value int64
		if err := db.QueryRow(`SELECT reconciled_at_ns FROM managed_power_transitions LIMIT 1`).Scan(&value); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("migrated column: %v", err)
		}
		if _, err := l.EventsAfter("missing-job", 0, 1); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing job: %v", err)
		}

	})
})

var _ = ginkgo.Describe("QueueDeadlineAndHead", ginkgo.Label("integration"), func() {
	ginkgo.It("QueueDeadlineAndHead", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 2)
		defer db.Close()
		r := validResource()
		if err := l.RegisterResource(encode(t, r)); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		job := validJob()
		if _, err := l.Admit(encode(t, job), digest, "key-first", "job-first"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := l.Admit(encode(t, job), digest, "key-second", "job-second"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		second := ReserveRequest{JobID: "job-second", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-second"}
		if _, err := l.Reserve(second); !errors.Is(err, ErrBusy) {
			t.Fatalf("queue head: %v", err)
		}
		if _, err := l.ExpireQueued("job-first", 1, "expire-first"); !errors.Is(err, ErrBusy) {
			t.Fatalf("early deadline: %v", err)
		}
		if _, err := db.Exec(`UPDATE managed_jobs SET queue_deadline_ns=0 WHERE id='job-first'`); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := l.Reserve(ReserveRequest{JobID: "job-first", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-expired"}); !errors.Is(err, ErrBusy) {
			t.Fatalf("expired reservation: %v", err)
		}
		expired, err := l.ExpireQueued("job-first", 1, "expire-first")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if expired.Verdict != Incomplete || expired.GlobalOccupancy != 0 || expired.CapacityDelta != 0 {
			t.Fatalf("expired: %+v", expired)
		}
		if _, err := l.Reserve(second); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}

	})
})

var _ = ginkgo.Describe("QueuedResultKeepsPinnedConfiguration", ginkgo.Label("integration"), func() {
	ginkgo.It("QueuedResultKeepsPinnedConfiguration", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
		defer db.Close()
		r := validResource()
		if err := l.RegisterResource(encode(t, r)); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		job := validJob()
		if _, err := l.Admit(encode(t, job), digest, "key-one", "job-one"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		r.Revision = 2
		if err := l.UpdateResource(encode(t, r), 1, 1); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := l.CancelQueued("job-one", 1, "cancel-one"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		var resultJSON []byte
		if err := db.QueryRow(`SELECT result_json FROM managed_jobs WHERE id='job-one'`).Scan(&resultJSON); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		result, err := DecodeResult(resultJSON)
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if result.ResourceRevision != 1 {
			t.Fatalf("queued result revision = %d", result.ResourceRevision)
		}

	})
})
