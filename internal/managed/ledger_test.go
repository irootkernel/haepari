package managed

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/irootkernel/haepari/internal/store/sqlite"
)

func openLedger(t *testing.T, path string, limit int) (*sql.DB, *Ledger) {
	t.Helper()
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	l, err := NewLedger(db, limit)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, l
}

func TestAdvanceLocalRejectsInvalidPhasePairs(t *testing.T) {
	var ledger Ledger
	for _, pair := range [][2]string{{"preparing", "collecting"}, {"ready", "cleaning"}, {"running", "settling"}} {
		if _, err := ledger.AdvanceLocal("job", 1, pair[0], pair[1], "invalid-transition"); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s -> %s: %v", pair[0], pair[1], err)
		}
	}
}

func noEffectProof() NoEffectProof {
	return NoEffectProof{ObservationReference: "observed-prerequisite-no-effect", NoOwnedJobEffect: true, NoUnprovenProcess: true, NoProtectedPath: true, PowerReconciled: true}
}

func recordObservation(t *testing.T, l *Ledger, jobID string, revision int64, resourceID string, slot int, lease int64, proof NoEffectProof) Receipt {
	t.Helper()
	receipt, err := l.RecordNoEffectObservation(NoEffectObservationRequest{JobID: jobID, ExpectedJobRevision: revision, ResourceID: resourceID,
		ExpectedConfigRevision: 1, Slot: slot, LeaseGeneration: lease, Source: "machine", Mechanism: "adapter-inspect", EvidenceDigest: digest,
		Proof: proof, MutationKey: "observe-" + jobID})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestReservedNoEffectReplayAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, l := openLedger(t, path, 2)
	resource := validResource()
	resource.Capacity = 1
	if err := l.RegisterResource(encode(t, resource)); err != nil {
		t.Fatal(err)
	}
	job := validJob()
	admitted, err := l.Admit(encode(t, job), digest, "request-1", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if admitted.GlobalOccupancy != 0 || admitted.JobState != "queued" {
		t.Fatalf("admitted: %+v", admitted)
	}
	reserve := ReserveRequest{JobID: "job-1", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-1"}
	reserved, err := l.Reserve(reserve)
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	if classified.JobState != "settling" || classified.GlobalOccupancy != 1 {
		t.Fatalf("classification: %+v", classified)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, l = openLedger(t, path, 2)
	defer db.Close()
	settle := SettleNoEffectRequest{JobID: "job-1", ExpectedJobRevision: 4, ResourceID: "local-a", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Slot: 0, LeaseGeneration: 1, MutationKey: "settle-1"}
	finished, err := l.SettleNoEffect(settle)
	if err != nil {
		t.Fatal(err)
	}
	if finished.GlobalOccupancy != 0 || finished.ResourceOccupancy != 0 || finished.CapacityDelta != -1 || finished.Verdict != Cancelled || finished.ResourceAvailability != "available" {
		t.Fatalf("settled: %+v", finished)
	}
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
}

func TestInterruptedLocalJobQuarantinesWithoutReleasingSlot(t *testing.T) {
	db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
	defer db.Close()
	resource := validResource()
	resource.Capacity = 1
	if err := l.RegisterResource(encode(t, resource)); err != nil {
		t.Fatal(err)
	}
	job := validJob()
	if _, err := l.Admit(encode(t, job), digest, "restart-key", "restart-job"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve(ReserveRequest{JobID: "restart-job", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "restart-reserve"}); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	seen, err := l.Snapshot("restart-job")
	if err != nil || seen.JobState != "reconciling" || seen.ResourceAvailability != "quarantined" || seen.ResourceOccupancy != 1 || seen.GlobalOccupancy != 1 {
		t.Fatalf("uncertain work released or advertised available: %+v %v", seen, err)
	}
	if err := l.MarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	again, err := l.Snapshot("restart-job")
	if err != nil || again.JobRevision != seen.JobRevision || again.ResourceAvailabilityRevision != seen.ResourceAvailabilityRevision {
		t.Fatalf("restart reconciliation changed settled uncertainty: %+v %v", again, err)
	}
}

func TestUnprovenCollectorKeepsLeaseAndQuarantines(t *testing.T) {
	db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
	defer db.Close()
	resource := validResource()
	resource.Capacity = 1
	if err := l.RegisterResource(encode(t, resource)); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Admit(encode(t, validJob()), digest, "collector-key", "collector-job"); err != nil {
		t.Fatal(err)
	}
	receipt, err := l.Reserve(ReserveRequest{JobID: "collector-job", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "collector-reserve"})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = l.AdvanceLocal("collector-job", receipt.JobRevision, "preparing", "ready", "collector-ready")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = l.StartLocal("collector-job", receipt.JobRevision, "collector-run")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.AdvanceLocal("collector-job", receipt.JobRevision, "running", "collecting", "collector-exit"); err != nil {
		t.Fatal(err)
	}
	if err := l.ReconcileLocalFailure("collector-job"); err != nil {
		t.Fatal(err)
	}
	seen, err := l.Snapshot("collector-job")
	if err != nil || seen.JobState != "reconciling" || seen.ResourceAvailability != "quarantined" || seen.GlobalOccupancy != 1 || seen.ResourceOccupancy != 1 {
		t.Fatalf("unproven collector released or finished: %+v %v", seen, err)
	}
	if _, err := l.Result("collector-job"); !errors.Is(err, ErrBusy) {
		t.Fatalf("unproven collector had a terminal result: %v", err)
	}
}

func TestQueuedCancelAndCapacityFence(t *testing.T) {
	db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
	defer db.Close()
	r := validResource()
	r.Capacity = 2
	if err := l.RegisterResource(encode(t, r)); err != nil {
		t.Fatal(err)
	}
	job := validJob()
	if _, err := l.Admit(encode(t, job), digest, "request-1", "job-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Admit(encode(t, job), digest, "request-2", "job-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Admit(encode(t, job), digest, "request-3", "job-3"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve(ReserveRequest{JobID: "job-1", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve(ReserveRequest{JobID: "job-2", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-2"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("global limit: %v", err)
	}
	cancelled, err := l.CancelQueued("job-2", 1, "cancel-2")
	if err != nil {
		t.Fatal(err)
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
}

func TestVMFailurePauseOrderingAndStaleRegistration(t *testing.T) {
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
		t.Fatal(err)
	}
	other := validResource()
	if err := l.RegisterResource(encode(t, other)); err != nil {
		t.Fatal(err)
	}
	job := validJob()
	job.ResourceID = "vm-a"
	if _, err := l.Admit(encode(t, job), digest, "request-vm", "job-vm"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO managed_power_transitions(operation_generation,victim_resource_id,target_resource_id,identity_json,state) VALUES(2,'vm-a','other-vm','{}','pending')`); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve(ReserveRequest{JobID: "job-vm", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-power-pending"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("pending power reservation: %v", err)
	}
	if _, err := db.Exec(`UPDATE managed_power_transitions SET state='reconciled' WHERE operation_generation=2`); err != nil {
		t.Fatal(err)
	}
	reserved, err := l.Reserve(ReserveRequest{JobID: "job-vm", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-vm"})
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	if _, err := l.ClassifyNoEffect(q); !errors.Is(err, ErrBlocked) {
		t.Fatalf("uncertain power classification: %v", err)
	}
	if _, err := db.Exec(`UPDATE managed_power_transitions SET state='reconciled' WHERE operation_generation=1`); err != nil {
		t.Fatal(err)
	}
	recordObservation(t, l, "job-vm", 2, "vm-a", 0, 1, q.Proof)
	q.ExpectedJobRevision = 3
	classified, err := l.ClassifyNoEffect(q)
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
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
		t.Fatal(err)
	}
	q.MutationKey = "stale-vm-failure"
	if _, err := l.ClassifyNoEffect(q); !errors.Is(err, ErrStale) {
		t.Fatalf("stale vm failure: %v", err)
	}
}

func TestQuarantinePreservedAndCursorScoped(t *testing.T) {
	db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 2)
	defer db.Close()
	r := validResource()
	if err := l.RegisterResource(encode(t, r)); err != nil {
		t.Fatal(err)
	}
	job := validJob()
	if _, err := l.Admit(encode(t, job), digest, "key-one", "job-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Admit(encode(t, job), digest, "key-two", "job-two"); err != nil {
		t.Fatal(err)
	}
	reserved, err := l.Reserve(ReserveRequest{JobID: "job-one", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-one"})
	if err != nil {
		t.Fatal(err)
	}
	// An independent recovery can quarantine the resource while this Job still
	// owns the slot. Classification and settlement preserve that state.
	if _, err := db.Exec(`UPDATE managed_resources SET availability='quarantined',availability_reason='other-slot',availability_revision=availability_revision+1 WHERE id='local-a'`); err != nil {
		t.Fatal(err)
	}
	recordObservation(t, l, "job-one", 2, "local-a", reserved.Slot, reserved.LeaseGeneration, noEffectProof())
	if _, err := l.ClassifyNoEffect(ClassifyNoEffectRequest{JobID: "job-one", ExpectedJobRevision: 3, ResourceID: "local-a", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 2, Slot: reserved.Slot, LeaseGeneration: reserved.LeaseGeneration, FailureKind: "cancel", Proof: noEffectProof(), MutationKey: "classify-one"}); err != nil {
		t.Fatal(err)
	}
	stale := SettleNoEffectRequest{JobID: "job-one", ExpectedJobRevision: 4, ResourceID: "local-a", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Slot: reserved.Slot, LeaseGeneration: reserved.LeaseGeneration, MutationKey: "settle-stale"}
	if _, err := l.SettleNoEffect(stale); !errors.Is(err, ErrStale) {
		t.Fatalf("stale availability: %v", err)
	}
	stale.ExpectedAvailabilityRevision = 2
	stale.MutationKey = "settle-current"
	finished, err := l.SettleNoEffect(stale)
	if err != nil {
		t.Fatal(err)
	}
	if finished.ResourceAvailability != "quarantined" || finished.CapacityDelta != -1 {
		t.Fatalf("quarantine changed: %+v", finished)
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
		t.Fatal(err)
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
		t.Fatal(err)
	}
	if err := l.PurgeEventsThrough("job-two", otherCursor); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	resync, err = l.EventsAfter("job-one", events[0].Cursor, 20)
	if err != nil || resync.Resync == nil || resync.CurrentCursor < events[len(events)-1].Cursor {
		t.Fatalf("fully purged cursor resync: %+v %v", resync, err)
	}
	resync, err = l.EventsAfter("job-one", 0, 20)
	if err != nil || resync.Resync == nil {
		t.Fatalf("purged initial page resync: %+v %v", resync, err)
	}
}

func TestInitialSchemaRejectsNewerAndDuplicateVM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, l := openLedger(t, path, 1)
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 3 {
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
		t.Fatal(err)
	}
	r.ID = "vm-b"
	r.VM.MACAddress = "AA:BB:CC:DD:EE:FF"
	if err := l.RegisterResource(encode(t, r)); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate VM identity: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version=4`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewLedger(db, 1); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("newer schema: %v", err)
	}
}

func TestProvisionalV1PowerTableMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, identity TEXT NOT NULL)`,
		`INSERT INTO schema_migrations(version,identity) VALUES(1,'managed-initial-v1')`,
		`CREATE TABLE managed_power_transitions(operation_generation INTEGER PRIMARY KEY, victim_resource_id TEXT NOT NULL, target_resource_id TEXT NOT NULL, identity_json BLOB NOT NULL, state TEXT NOT NULL)`,
		`PRAGMA user_version=1`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, l := openLedger(t, path, 1)
	defer db.Close()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 3 {
		t.Fatalf("migrated version %d: %v", version, err)
	}
	var value int64
	if err := db.QueryRow(`SELECT reconciled_at_ns FROM managed_power_transitions LIMIT 1`).Scan(&value); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("migrated column: %v", err)
	}
	if _, err := l.EventsAfter("missing-job", 0, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing job: %v", err)
	}
}

func TestQueueDeadlineAndHead(t *testing.T) {
	db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 2)
	defer db.Close()
	r := validResource()
	if err := l.RegisterResource(encode(t, r)); err != nil {
		t.Fatal(err)
	}
	job := validJob()
	if _, err := l.Admit(encode(t, job), digest, "key-first", "job-first"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Admit(encode(t, job), digest, "key-second", "job-second"); err != nil {
		t.Fatal(err)
	}
	second := ReserveRequest{JobID: "job-second", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-second"}
	if _, err := l.Reserve(second); !errors.Is(err, ErrBusy) {
		t.Fatalf("queue head: %v", err)
	}
	if _, err := l.ExpireQueued("job-first", 1, "expire-first"); !errors.Is(err, ErrBusy) {
		t.Fatalf("early deadline: %v", err)
	}
	if _, err := db.Exec(`UPDATE managed_jobs SET queue_deadline_ns=0 WHERE id='job-first'`); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve(ReserveRequest{JobID: "job-first", ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-expired"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("expired reservation: %v", err)
	}
	expired, err := l.ExpireQueued("job-first", 1, "expire-first")
	if err != nil {
		t.Fatal(err)
	}
	if expired.Verdict != Incomplete || expired.GlobalOccupancy != 0 || expired.CapacityDelta != 0 {
		t.Fatalf("expired: %+v", expired)
	}
	if _, err := l.Reserve(second); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedResultKeepsPinnedConfiguration(t *testing.T) {
	db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
	defer db.Close()
	r := validResource()
	if err := l.RegisterResource(encode(t, r)); err != nil {
		t.Fatal(err)
	}
	job := validJob()
	if _, err := l.Admit(encode(t, job), digest, "key-one", "job-one"); err != nil {
		t.Fatal(err)
	}
	r.Revision = 2
	if err := l.UpdateResource(encode(t, r), 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := l.CancelQueued("job-one", 1, "cancel-one"); err != nil {
		t.Fatal(err)
	}
	var resultJSON []byte
	if err := db.QueryRow(`SELECT result_json FROM managed_jobs WHERE id='job-one'`).Scan(&resultJSON); err != nil {
		t.Fatal(err)
	}
	result, err := DecodeResult(resultJSON)
	if err != nil {
		t.Fatal(err)
	}
	if result.ResourceRevision != 1 {
		t.Fatalf("queued result revision = %d", result.ResourceRevision)
	}
}
