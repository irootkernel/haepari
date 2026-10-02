//go:build integration

package managed

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func admitScheduling(t ginkgo.GinkgoTInterface, l *Ledger, resource, id string) Receipt {
	t.Helper()
	j := validJob()
	j.ResourceID = resource
	r, err := l.Admit(encode(t, j), digest, "key-"+id, id)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return r
}
func reserveScheduling(l *Ledger, r Receipt, guard *VMReservationGuard) (Receipt, error) {
	return l.Reserve(ReserveRequest{JobID: r.JobID, ExpectedJobRevision: r.JobRevision, ExpectedConfigRevision: r.ResourceConfigRevision, ExpectedAvailabilityRevision: r.ResourceAvailabilityRevision, MutationKey: "reserve-" + r.JobID, VMGuard: guard})
}
func settleScheduling(t ginkgo.GinkgoTInterface, l *Ledger, r Receipt, cleanup string) Receipt {
	t.Helper()
	var err error
	r, err = l.AdvanceLocal(r.JobID, r.JobRevision, "preparing", "ready", "ready-"+r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	r, err = l.StartLocal(r.JobID, r.JobRevision, "run-"+r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	id, err := l.LocalIdentity(r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	r, err = l.RecordLocalOutcome(LocalOutcomeRequest{Identity: id, ExpectedRevision: r.JobRevision, Outcome: LocalOutcome{Started: true, WorkloadOutcome: "pass"}, MutationKey: "observed-" + r.JobID})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	r, err = l.CompleteLocalRun(LocalTerminationRequest{Identity: id, ExpectedRevision: r.JobRevision, MutationKey: "stopped-" + r.JobID})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	for _, phase := range [][2]string{{"collecting", "cleaning"}, {"cleaning", "settling"}} {
		r, err = l.AdvanceLocal(r.JobID, r.JobRevision, phase[0], phase[1], phase[1]+"-"+r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
	availability := "available"
	verdict := Pass
	if cleanup == "failed" {
		availability = "quarantined"
		verdict = Incomplete
	}
	result := Result{Schema: "haepari.result/v1", JobID: r.JobID, ResourceID: r.ResourceID, ResourceRevision: r.ResourceConfigRevision, LeaseGeneration: r.LeaseGeneration, JobState: "finished", ResourceAvailability: availability, WorkloadOutcome: "pass", EvidenceCompleteness: "complete", CleanupOutcome: CleanupOutcome(cleanup), FinalVerdict: verdict, Artifacts: []ResultArtifact{{Path: "results/build.json", SizeBytes: 1, SHA256: digest}}, Observations: &ResultObservations{Source: "isolated-scheduling-observation"}}
	r, err = l.SettleLocal(r.JobID, r.JobRevision, result)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return r
}
func vmScheduling(t ginkgo.GinkgoTInterface, l *Ledger, id string, n int, power string, stop bool) ResourceSpec {
	t.Helper()
	r := validResource()
	r.ID = id
	r.Kind = "utm-macos"
	r.Capacity = 1
	r.Cleanup = nil
	r.Adapter.UTMCtl = "/Applications/UTM.app/Contents/MacOS/utmctl"
	r.VM = &VM{UTMVMUUID: fmt.Sprintf("%08d-1111-4111-8111-111111111111", n), MACAddress: fmt.Sprintf("aa:bb:cc:dd:ee:%02x", n), AddressDiscovery: "dhcp-lease-by-mac", GuestJobRoot: "/var/haepari/jobs", AllowAutomaticStop: stop}
	r.Guest = &Guest{ConnectionReference: "ssh-isolated", KnownHostsReference: "key-isolated"}
	gomega.Expect(l.RegisterResource(encode(t, r))).To(gomega.Succeed())
	_, err := l.ObserveVM(VMObservation{ResourceID: id, ConfigRevision: 1, VMUUID: r.VM.UTMVMUUID, Power: power, EvidenceDigest: digest})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return r
}
func vmGuard(t ginkgo.GinkgoTInterface, db *sql.DB, limit int) *VMReservationGuard {
	t.Helper()
	g := &VMReservationGuard{HostLimit: limit, ObservationRevisions: map[string]int64{}}
	rows, err := db.Query(`SELECT resource_id,observation_revision FROM managed_vm_observations`)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer rows.Close()
	for rows.Next() {
		var id string
		var rev int64
		gomega.Expect(rows.Scan(&id, &rev)).To(gomega.Succeed())
		g.ObservationRevisions[id] = rev
	}
	gomega.Expect(rows.Err()).To(gomega.Succeed())
	return g
}

var _ = ginkgo.Describe("Task007Scheduling", ginkgo.Label("integration"), func() {
	ginkgo.It("counts unregistered running guests at the host boundary and rejects negative counts", func() {
		t := ginkgo.GinkgoT()
		for _, limit := range []int{1, 2} {
			db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 3)
			vmScheduling(t, l, "target", 1, "stopped", false)
			job := admitScheduling(t, l, "target", "operator-limit")
			guard := vmGuard(t, db, limit)
			guard.UnregisteredRunning = -1
			_, err := reserveScheduling(l, job, guard)
			gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
			guard.UnregisteredRunning = limit
			_, err = reserveScheduling(l, job, guard)
			gomega.Expect(errors.Is(err, ErrBusy)).To(gomega.BeTrue())
			current, err := l.Snapshot(job.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current.LeaseGeneration).To(gomega.BeZero())
			guard.UnregisteredRunning = limit - 1
			_, err = reserveScheduling(l, job, guard)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(db.Close()).To(gomega.Succeed())
		}
	})
	ginkgo.It("migrates a historical idle clock as unknown until a trustworthy final slot return", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "state.sqlite")
		db, l := openLedger(t, path, 2)
		r := validResource()
		gomega.Expect(l.RegisterResource(encode(t, r))).To(gomega.Succeed())
		// Reconstruct the actual schema4 shape, without a registration timestamp.
		for _, name := range schedulingTables {
			_, err := db.Exec("DROP TABLE " + name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		_, err := db.Exec(`DROP TABLE managed_lock_requests; DROP TABLE managed_lock_slots; DROP TABLE managed_lock_clock; DROP TABLE managed_lock_discontinuities; DROP TABLE managed_runner_receipts; DELETE FROM schema_migrations WHERE version=8; DELETE FROM schema_migrations WHERE version=7; DROP TABLE managed_installation; DROP TABLE managed_cancellations; DROP TABLE managed_cancel_edges; DROP TABLE managed_local_outcomes; ALTER TABLE managed_resources DROP COLUMN idle_since_ns; DELETE FROM schema_migrations WHERE version>=5; PRAGMA user_version=4`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, l = openLedger(t, path, 2)
		defer db.Close()
		status, err := l.ResourceStatus(r.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.IdleSince).To(gomega.BeNil())
		q := AvailabilityRequest{MutationKey: "legacy-pause", ResourceID: r.ID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: "maintenance"}
		_, err = l.SetAvailability(q, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q.MutationKey = "legacy-resume"
		q.ExpectedAvailabilityRevision = 2
		q.Reason = ""
		q.ReadinessDigest = digest
		q.ObservationReference = "local-inspect"
		_, err = l.SetAvailability(q, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		status, err = l.ResourceStatus(r.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.IdleSince).To(gomega.BeNil())
		j := admitScheduling(t, l, r.ID, "historical-job")
		j, err = reserveScheduling(l, j, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		settleScheduling(t, l, j, "verified")
		status, err = l.ResourceStatus(r.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.IdleSince).NotTo(gomega.BeNil())
	})
	ginkgo.It("uses resource ID to break equal idle clocks and blocks victim leases and stale observation guards", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 3)
		defer db.Close()
		vmScheduling(t, l, "victim-z", 1, "started", true)
		vmScheduling(t, l, "victim-a", 2, "started", true)
		vmScheduling(t, l, "target", 3, "stopped", false)
		_, err := db.Exec(`UPDATE managed_resources SET idle_since_ns=123 WHERE id LIKE 'victim-%'`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		target := admitScheduling(t, l, "target", "tie-target")
		guard := vmGuard(t, db, 2)
		stale := *guard
		stale.ObservationRevisions = map[string]int64{"victim-z": 1, "victim-a": 2, "target": 1}
		_, err = reserveScheduling(l, target, &stale)
		gomega.Expect(errors.Is(err, ErrStale)).To(gomega.BeTrue())
		_, err = reserveScheduling(l, target, guard)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		p, err := l.PowerIntent(1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.Identity.VictimResourceID).To(gomega.Equal("victim-a"))
		victimJob := admitScheduling(t, l, "victim-a", "victim-job")
		_, err = reserveScheduling(l, victimJob, guard)
		gomega.Expect(errors.Is(err, ErrBusy)).To(gomega.BeTrue())
	})
	ginkgo.It("settles a confirmed no-start host limit failure without quarantine and pauses VM dispatch atomically", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 3)
		defer db.Close()
		vmScheduling(t, l, "target", 1, "stopped", false)
		vmScheduling(t, l, "other-vm", 2, "stopped", false)
		local := validResource()
		gomega.Expect(l.RegisterResource(encode(t, local))).To(gomega.Succeed())
		j := admitScheduling(t, l, "target", "failed-start")
		j, err := reserveScheduling(l, j, vmGuard(t, db, 1))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		p, err := l.PowerIntent(1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(l.ClaimPowerEffect(p.Identity, "start")).To(gomega.Succeed())
		gomega.Expect(l.ConfirmPower(PowerConfirmation{Identity: p.Identity, ExpectedStage: "start_issued", TargetPower: "stopped", NoEffectProven: true, EvidenceDigest: digest})).To(gomega.Succeed())
		proof := noEffectProof()
		observed := recordObservation(t, l, j.JobID, j.JobRevision, j.ResourceID, j.Slot, j.LeaseGeneration, proof)
		classified, err := l.ClassifyNoEffect(ClassifyNoEffectRequest{JobID: j.JobID, ExpectedJobRevision: observed.JobRevision, ResourceID: j.ResourceID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Slot: j.Slot, LeaseGeneration: j.LeaseGeneration, FailureKind: "prepare_failure", HostPauseReason: "host_vm_limit", Proof: proof, MutationKey: "host-failure"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(classified.ResourceAvailability).To(gomega.Equal("paused"))
		gomega.Expect(classified.GlobalOccupancy).To(gomega.Equal(1))
		finished, err := l.SettleNoEffect(SettleNoEffectRequest{JobID: j.JobID, ExpectedJobRevision: classified.JobRevision, ResourceID: j.ResourceID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 2, Slot: j.Slot, LeaseGeneration: j.LeaseGeneration, MutationKey: "host-settle"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(finished.Verdict).To(gomega.Equal(Incomplete))
		gomega.Expect(finished.GlobalOccupancy).To(gomega.BeZero())
		other, err := l.ResourceStatus("other-vm")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(other.Availability).To(gomega.Equal("paused"))
		gomega.Expect(other.Reason).To(gomega.Equal("host_vm_limit"))
		localStatus, err := l.ResourceStatus(local.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(localStatus.Availability).To(gomega.Equal("available"))
	})
	ginkgo.It("selects resource FIFO heads without unavailable-resource head blocking", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 3)
		defer db.Close()
		a := validResource()
		a.Capacity = 1
		gomega.Expect(l.RegisterResource(encode(t, a))).To(gomega.Succeed())
		b := a
		b.ID = "local-b"
		b.Capacity = 2
		gomega.Expect(l.RegisterResource(encode(t, b))).To(gomega.Succeed())
		first := admitScheduling(t, l, a.ID, "first")
		second := admitScheduling(t, l, a.ID, "second")
		other := admitScheduling(t, l, b.ID, "other")
		last := admitScheduling(t, l, b.ID, "last")
		_, err := l.SetAvailability(AvailabilityRequest{MutationKey: fmt.Sprintf("availability-%d", time.Now().UnixNano()), ResourceID: a.ID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: "maintenance"}, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		next, err := l.NextQueued()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(next.Receipt.JobID).To(gomega.Equal(other.JobID))
		_, err = reserveScheduling(l, last, nil)
		gomega.Expect(errors.Is(err, ErrBusy)).To(gomega.BeTrue())
		reserved, err := reserveScheduling(l, other, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(reserved.Slot).To(gomega.Equal(0))
		next, err = l.NextQueued()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(next.Receipt.JobID).To(gomega.Equal(last.JobID))
		r, err := reserveScheduling(l, last, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(r.Slot).To(gomega.Equal(1))
		gomega.Expect(r.GlobalOccupancy).To(gomega.Equal(2))
		_, err = reserveScheduling(l, second, nil)
		gomega.Expect(errors.Is(err, ErrBusy)).To(gomega.BeTrue())
		first, err = l.Snapshot(first.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(first.JobState).To(gomega.Equal("queued"))
	})
	ginkgo.It("preserves idle UTC across maintenance and advances it only on final slot return while preserving quarantine", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "state.sqlite")
		db, l := openLedger(t, path, 2)
		r := validResource()
		gomega.Expect(l.RegisterResource(encode(t, r))).To(gomega.Succeed())
		status, err := l.ResourceStatus(r.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		initial := *status.IdleSince
		queued := admitScheduling(t, l, r.ID, "cancelled")
		_, err = l.CancelQueued(queued.JobID, 1, "cancel-idle")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		paused, err := l.SetAvailability(AvailabilityRequest{MutationKey: fmt.Sprintf("availability-%d", time.Now().UnixNano()), ResourceID: r.ID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: "maintenance"}, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		r.Revision = 2
		gomega.Expect(l.UpdateResource(encode(t, r), 1, paused.AvailabilityRevision)).To(gomega.Succeed())
		_, err = l.SetAvailability(AvailabilityRequest{MutationKey: fmt.Sprintf("availability-%d", time.Now().UnixNano()), ResourceID: r.ID, ExpectedConfigRevision: 2, ExpectedAvailabilityRevision: 2, ReadinessDigest: digest, ObservationReference: "actual-local-readiness"}, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, l = openLedger(t, path, 2)
		defer db.Close()
		status, err = l.ResourceStatus(r.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(*status.IdleSince).To(gomega.Equal(initial))
		one := admitScheduling(t, l, r.ID, "one")
		two := admitScheduling(t, l, r.ID, "two")
		one, err = reserveScheduling(l, one, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		two, err = reserveScheduling(l, two, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		settleScheduling(t, l, one, "failed")
		status, err = l.ResourceStatus(r.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Occupancy).To(gomega.Equal(1))
		gomega.Expect(*status.IdleSince).To(gomega.Equal(initial))
		settled := settleScheduling(t, l, two, "verified")
		gomega.Expect(settled.ResourceAvailability).To(gomega.Equal("quarantined"))
		status, err = l.ResourceStatus(r.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.IdleSince.After(initial)).To(gomega.BeTrue())
		gomega.Expect(status.Occupancy).To(gomega.BeZero())
	})
	ginkgo.It("fences availability and keeps all effectful phases occupied through restart uncertainty", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "state.sqlite")
		db, l := openLedger(t, path, 1)
		r := validResource()
		gomega.Expect(l.RegisterResource(encode(t, r))).To(gomega.Succeed())
		j := admitScheduling(t, l, r.ID, "occupied")
		j, err := reserveScheduling(l, j, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = l.SetAvailability(AvailabilityRequest{MutationKey: fmt.Sprintf("availability-%d", time.Now().UnixNano()), ResourceID: r.ID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, ExpectedLeaseGeneration: 0, Reason: "stale"}, false)
		gomega.Expect(errors.Is(err, ErrStale)).To(gomega.BeTrue())
		_, err = l.AdvanceLocal(j.JobID, 1, "preparing", "ready", "stale-phase")
		gomega.Expect(errors.Is(err, ErrStale)).To(gomega.BeTrue())
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, l = openLedger(t, path, 1)
		defer db.Close()
		gomega.Expect(l.MarkInterrupted()).To(gomega.Succeed())
		j, err = l.Snapshot(j.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(j.JobState).To(gomega.Equal("reconciling"))
		gomega.Expect(j.GlobalOccupancy).To(gomega.Equal(1))
		gomega.Expect(j.ResourceAvailability).To(gomega.Equal("quarantined"))
		_, err = l.SetAvailability(AvailabilityRequest{MutationKey: "reject-quarantine-pause", ResourceID: r.ID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: j.ResourceAvailabilityRevision, ExpectedLeaseGeneration: j.LeaseGeneration, Reason: "maintenance"}, false)
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		_, err = l.SetAvailability(AvailabilityRequest{MutationKey: "reject-quarantine-resume", ResourceID: r.ID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: j.ResourceAvailabilityRevision, ExpectedLeaseGeneration: j.LeaseGeneration, ReadinessDigest: digest, ObservationReference: "local-readiness"}, true)
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())

	})
	ginkgo.It("rejects nested admission even with spare slots and retains foreign entries and nonce fencing", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 4)
		defer db.Close()
		r := validResource()
		gomega.Expect(l.RegisterResource(encode(t, r))).To(gomega.Succeed())
		other := r
		other.ID = "local-b"
		gomega.Expect(l.RegisterResource(encode(t, other))).To(gomega.Succeed())
		for _, id := range []string{"parent-one", "parent-two"} {
			j := admitScheduling(t, l, r.ID, id)
			j, err := reserveScheduling(l, j, nil)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			chain, err := l.RunnerParentChain("installation-a", id, j.LeaseGeneration)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			nested, err := l.Nested("installation-a", chain)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(nested).To(gomega.BeTrue())
			foreign, err := l.Nested("installation-b", chain)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(foreign).To(gomega.BeFalse())
			child := validJob()
			child.ResourceID = other.ID
			_, err = l.AdmitWithParent(encode(t, child), digest, "", nil, "nested-"+id, "child-"+id, "installation-a", chain)
			gomega.Expect(errors.Is(err, ErrNestedAcquisition)).To(gomega.BeTrue())
			chain[0].Nonce = digest
			nested, err = l.Nested("installation-a", chain)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(nested).To(gomega.BeFalse())
		}
		foreign := ParentChain{{"foreign-installation", "foreign-job", 12, digest}}
		j := validJob()
		j.ResourceID = other.ID
		admitted, err := l.AdmitWithParent(encode(t, j), digest, "", nil, "cross-install", "cross-job", "installation-a", foreign)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		reserved, err := reserveScheduling(l, admitted, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		chain, err := l.RunnerParentChain("installation-a", reserved.JobID, reserved.LeaseGeneration)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(chain).To(gomega.HaveLen(2))
		gomega.Expect(chain[0]).To(gomega.Equal(foreign[0]))
	})
	ginkgo.It("serializes concurrent direct host starts at limits one and two", func() {
		t := ginkgo.GinkgoT()
		for _, limit := range []int{1, 2} {
			path := filepath.Join(t.TempDir(), "state.sqlite")
			db, l := openLedger(t, path, 4)
			vmScheduling(t, l, "target-a", 1, "stopped", false)
			vmScheduling(t, l, "target-b", 2, "stopped", false)
			vmScheduling(t, l, "target-c", 3, "stopped", false)
			jobs := []Receipt{admitScheduling(t, l, "target-a", "start-a"), admitScheduling(t, l, "target-b", "start-b"), admitScheduling(t, l, "target-c", "start-c")}
			guard := vmGuard(t, db, limit)
			db2, l2 := openLedger(t, path, 4)
			db3, l3 := openLedger(t, path, 4)
			ledgers := []*Ledger{l, l2, l3}
			start := make(chan struct{})
			results := make(chan error, 3)
			var wg sync.WaitGroup
			for i := range jobs {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					_, err := reserveScheduling(ledgers[i], jobs[i], guard)
					results <- err
				}(i)
			}
			close(start)
			wg.Wait()
			close(results)
			successes := 0
			for err := range results {
				if err == nil {
					successes++
				}
			}
			gomega.Expect(successes).To(gomega.BeNumerically("<=", limit))
			gomega.Expect(successes).To(gomega.BeNumerically(">=", 1))
			// Retry transactions that lost a SQLite write-upgrade race. The unchanged
			// guard must still account for every already durable pending start.
			for i, j := range jobs {
				current, err := l.Snapshot(j.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				if current.JobState == "queued" {
					_, _ = reserveScheduling(ledgers[i], current, guard)
				}
			}
			var claims, occupancy int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_power_transitions WHERE state='pending'`).Scan(&claims)).To(gomega.Succeed())
			gomega.Expect(db.QueryRow(`SELECT SUM(occupancy) FROM managed_resources`).Scan(&occupancy)).To(gomega.Succeed())
			gomega.Expect(claims).To(gomega.Equal(limit))
			gomega.Expect(occupancy).To(gomega.Equal(limit))
			db3.Close()
			db2.Close()
			db.Close()
		}
	})
	ginkgo.It("transfers the oldest idle victim place only after observed stop and retains uncertainty across restart", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "state.sqlite")
		db, l := openLedger(t, path, 4)
		victim := vmScheduling(t, l, "victim", 1, "started", true)
		target := vmScheduling(t, l, "target", 2, "stopped", false)
		vmScheduling(t, l, "other", 3, "stopped", false)
		j := admitScheduling(t, l, target.ID, "target-job")
		guard := vmGuard(t, db, 1)
		reserved, err := reserveScheduling(l, j, guard)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(reserved.GlobalOccupancy).To(gomega.Equal(1))
		p, err := l.PowerIntent(1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.Identity.VictimResourceID).To(gomega.Equal(victim.ID))
		gomega.Expect(p.Stage).To(gomega.Equal("stop_ready"))
		gomega.Expect(errors.Is(l.ClaimPowerEffect(p.Identity, "start"), ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(l.ClaimPowerEffect(p.Identity, "stop")).To(gomega.Succeed())
		gomega.Expect(errors.Is(l.ClaimPowerEffect(p.Identity, "stop"), ErrBlocked)).To(gomega.BeTrue())
		_, err = l.SetAvailability(AvailabilityRequest{MutationKey: fmt.Sprintf("availability-%d", time.Now().UnixNano()), ResourceID: victim.ID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: "maintenance"}, false)
		gomega.Expect(errors.Is(err, ErrBusy)).To(gomega.BeTrue())
		victim.Revision = 2
		gomega.Expect(errors.Is(l.UpdateResource(encode(t, victim), 1, 1), ErrBusy)).To(gomega.BeTrue())
		gomega.Expect(l.ConfirmPower(PowerConfirmation{Identity: p.Identity, ExpectedStage: "stop_issued", VictimPower: "uncertain", TargetPower: "stopped", EvidenceDigest: digest})).To(gomega.Succeed())
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, l = openLedger(t, path, 4)
		defer db.Close()
		gomega.Expect(errors.Is(l.ClaimPowerEffect(p.Identity, "stop"), ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(errors.Is(l.ClaimPowerEffect(p.Identity, "start"), ErrBlocked)).To(gomega.BeTrue())
		other := admitScheduling(t, l, "other", "other-job")
		_, err = reserveScheduling(l, other, vmGuard(t, db, 1))
		gomega.Expect(errors.Is(err, ErrBusy)).To(gomega.BeTrue())
		stale := p.Identity
		stale.TargetLeaseGeneration++
		gomega.Expect(errors.Is(l.ConfirmPower(PowerConfirmation{Identity: stale, ExpectedStage: "stop_uncertain", VictimPower: "stopped", TargetPower: "stopped", EvidenceDigest: digest}), ErrStale)).To(gomega.BeTrue())
		gomega.Expect(l.ConfirmPower(PowerConfirmation{Identity: p.Identity, ExpectedStage: "stop_uncertain", VictimPower: "stopped", TargetPower: "stopped", EvidenceDigest: digest})).To(gomega.Succeed())
		_, err = reserveScheduling(l, other, vmGuard(t, db, 1))
		gomega.Expect(errors.Is(err, ErrBusy)).To(gomega.BeTrue())
		gomega.Expect(l.ClaimPowerEffect(p.Identity, "start")).To(gomega.Succeed())
		gomega.Expect(l.ConfirmPower(PowerConfirmation{Identity: p.Identity, ExpectedStage: "start_issued", VictimPower: "stopped", TargetPower: "uncertain", EvidenceDigest: digest})).To(gomega.Succeed())
		gomega.Expect(errors.Is(l.ClaimPowerEffect(p.Identity, "start"), ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(l.ConfirmPower(PowerConfirmation{Identity: p.Identity, ExpectedStage: "start_uncertain", VictimPower: "stopped", TargetPower: "started", EvidenceDigest: digest})).To(gomega.Succeed())
		p, err = l.PowerIntent(1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.State).To(gomega.Equal("reconciled"))
	})
	ginkgo.It("keeps a blocked VM job queued with original deadline and filters protected or unknown-idle victims", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 3)
		defer db.Close()
		vmScheduling(t, l, "victim", 1, "started", true)
		target := vmScheduling(t, l, "target", 2, "stopped", false)
		gomega.Expect(db.Exec(`UPDATE managed_resources SET idle_since_ns=NULL WHERE id='victim'`)).Error().NotTo(gomega.HaveOccurred())
		j := admitScheduling(t, l, target.ID, "blocked")
		var deadline int64
		gomega.Expect(db.QueryRow(`SELECT queue_deadline_ns FROM managed_jobs WHERE id=?`, j.JobID).Scan(&deadline)).To(gomega.Succeed())
		_, err := reserveScheduling(l, j, vmGuard(t, db, 1))
		var blocked *HostCapacityBlocked
		gomega.Expect(errors.As(err, &blocked)).To(gomega.BeTrue())
		gomega.Expect(l.RecordQueueBlocker(j.JobID, 1, blocked.Blocker)).To(gomega.Succeed())
		gomega.Expect(l.RecordQueueBlocker(j.JobID, 1, blocked.Blocker)).To(gomega.Succeed())
		var blockedEvents int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='queue_blocked'`, j.JobID).Scan(&blockedEvents)).To(gomega.Succeed())
		gomega.Expect(blockedEvents).To(gomega.Equal(1))
		current, err := l.Snapshot(j.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.JobState).To(gomega.Equal("queued"))
		gomega.Expect(current.GlobalOccupancy).To(gomega.BeZero())
		var after int64
		gomega.Expect(db.QueryRow(`SELECT queue_deadline_ns FROM managed_jobs WHERE id=?`, j.JobID).Scan(&after)).To(gomega.Succeed())
		gomega.Expect(after).To(gomega.Equal(deadline))
		blocker, err := l.QueueBlocker(j.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var details map[string][]string
		gomega.Expect(json.Unmarshal(blocker.Details, &details)).To(gomega.Succeed())
		gomega.Expect(details["unknown_idle"]).To(gomega.Equal([]string{"victim"}))
		_, err = l.SetAvailability(AvailabilityRequest{MutationKey: fmt.Sprintf("availability-%d", time.Now().UnixNano()), ResourceID: "victim", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: "maintenance"}, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(db.Exec(`UPDATE managed_resources SET idle_since_ns=? WHERE id='victim'`, time.Now().UnixNano())).Error().NotTo(gomega.HaveOccurred())
		_, err = reserveScheduling(l, j, vmGuard(t, db, 1))
		gomega.Expect(errors.As(err, &blocked)).To(gomega.BeTrue())
		gomega.Expect(json.Unmarshal(blocked.Blocker.Details, &details)).To(gomega.Succeed())
		gomega.Expect(details["pause"]).To(gomega.Equal([]string{"victim"}))
		_, err = l.SetAvailability(AvailabilityRequest{MutationKey: "resume-blocked-victim", ResourceID: "victim", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 2, ReadinessDigest: digest, ObservationReference: "isolated-victim-readiness"}, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		preexisting, err := l.QueueBlocker(j.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(preexisting).NotTo(gomega.BeNil())
		gomega.Expect(json.Unmarshal(preexisting.Details, &details)).To(gomega.Succeed())
		gomega.Expect(details["pause"]).To(gomega.Equal([]string{"victim"}))
		reserved, err := reserveScheduling(l, j, vmGuard(t, db, 1))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(reserved.JobState).To(gomega.Equal("preparing"))
		gomega.Expect(reserved.GlobalOccupancy).To(gomega.Equal(1))
		remaining, err := l.QueueBlocker(j.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(remaining).To(gomega.BeNil())
		gomega.Expect(db.QueryRow(`SELECT queue_deadline_ns FROM managed_jobs WHERE id=?`, j.JobID).Scan(&after)).To(gomega.Succeed())
		gomega.Expect(after).To(gomega.Equal(deadline))
	})
	ginkgo.It("rolls back the whole reservation when a power intent insert fails", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
		defer db.Close()
		vmScheduling(t, l, "target", 1, "stopped", false)
		j := admitScheduling(t, l, "target", "rollback")
		guard := vmGuard(t, db, 1)
		gomega.Expect(db.Exec(`CREATE TRIGGER block_power BEFORE INSERT ON managed_power_transitions BEGIN SELECT RAISE(ABORT,'simulated intent write failure'); END`)).Error().NotTo(gomega.HaveOccurred())
		_, err := reserveScheduling(l, j, guard)
		gomega.Expect(err).To(gomega.HaveOccurred())
		current, err := l.Snapshot(j.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.GlobalOccupancy).To(gomega.BeZero())
		gomega.Expect(current.JobState).To(gomega.Equal("queued"))
		gomega.Expect(current.LeaseGeneration).To(gomega.BeZero())
		var count int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_mutations`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.BeZero())
		gomega.Expect(db.Exec(`DROP TRIGGER block_power`)).Error().NotTo(gomega.HaveOccurred())
		_, err = reserveScheduling(l, j, guard)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})
})

var _ = ginkgo.Describe("Task007ReviewCorrections", ginkgo.Label("integration"), func() {
	ginkgo.It("preserves capacity blocking and storage diagnostics when the blocker insert fails", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
		defer db.Close()
		vmScheduling(t, l, "protected-victim", 1, "started", false)
		vmScheduling(t, l, "target", 2, "stopped", false)
		job := admitScheduling(t, l, "target", "blocker-write-failure")
		guard := vmGuard(t, db, 1)
		gomega.Expect(db.Exec(`CREATE TRIGGER block_queue_blocker BEFORE INSERT ON managed_queue_blockers BEGIN SELECT RAISE(ABORT,'simulated queue blocker storage failure'); END`)).Error().NotTo(gomega.HaveOccurred())
		_, err := reserveScheduling(l, job, guard)
		var blocked *HostCapacityBlocked
		gomega.Expect(errors.As(err, &blocked)).To(gomega.BeTrue())
		gomega.Expect(errors.Is(err, ErrBusy)).To(gomega.BeTrue())
		gomega.Expect(blocked.Blocker.Reason).To(gomega.Equal("host_capacity_no_eligible_victim"))
		var details map[string][]string
		gomega.Expect(json.Unmarshal(blocked.Blocker.Details, &details)).To(gomega.Succeed())
		gomega.Expect(details["policy"]).To(gomega.Equal([]string{"protected-victim"}))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring(blocked.Blocker.Reason))
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("simulated queue blocker storage failure"))
		current, err := l.Snapshot(job.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.JobState).To(gomega.Equal("queued"))
		gomega.Expect(current.JobRevision).To(gomega.Equal(job.JobRevision))
		gomega.Expect(current.Slot).To(gomega.Equal(-1))
		gomega.Expect(current.LeaseGeneration).To(gomega.BeZero())
		gomega.Expect(current.ResourceOccupancy).To(gomega.BeZero())
		gomega.Expect(current.GlobalOccupancy).To(gomega.BeZero())
		status, err := l.ResourceStatus("target")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.LeaseGeneration).To(gomega.BeZero())
		var intents, mutations, blockers int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_power_transitions`).Scan(&intents)).To(gomega.Succeed())
		gomega.Expect(intents).To(gomega.BeZero())
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_mutations WHERE mutation_key=?`, "reserve-"+job.JobID).Scan(&mutations)).To(gomega.Succeed())
		gomega.Expect(mutations).To(gomega.BeZero())
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_queue_blockers WHERE job_id=?`, job.JobID).Scan(&blockers)).To(gomega.Succeed())
		gomega.Expect(blockers).To(gomega.BeZero())
	})
	ginkgo.DescribeTable("refuses independent VM observation refresh without changing either protected observation", func(uncertain bool) {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
		defer db.Close()
		victim := vmScheduling(t, l, "victim", 1, "started", true)
		target := vmScheduling(t, l, "target", 2, "stopped", false)
		job := admitScheduling(t, l, target.ID, "protected-observations")
		_, err := reserveScheduling(l, job, vmGuard(t, db, 1))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		intent, err := l.PowerIntent(1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(intent.Identity.VictimResourceID).To(gomega.Equal(victim.ID))
		if uncertain {
			gomega.Expect(l.ClaimPowerEffect(intent.Identity, "stop")).To(gomega.Succeed())
			gomega.Expect(l.ConfirmPower(PowerConfirmation{Identity: intent.Identity, ExpectedStage: "stop_issued", VictimPower: "uncertain", TargetPower: "stopped", EvidenceDigest: digest})).To(gomega.Succeed())
			intent, err = l.PowerIntent(1)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(intent.State).To(gomega.Equal("uncertain"))
		} else {
			gomega.Expect(intent.State).To(gomega.Equal("pending"))
		}
		readObservation := func(id string) VMObservation {
			var observation VMObservation
			gomega.Expect(db.QueryRow(`SELECT resource_id,config_revision,vm_uuid,power,observation_revision,evidence_digest FROM managed_vm_observations WHERE resource_id=?`, id).Scan(&observation.ResourceID, &observation.ConfigRevision, &observation.VMUUID, &observation.Power, &observation.ExpectedObservationRevision, &observation.EvidenceDigest)).To(gomega.Succeed())
			return observation
		}
		observations := []VMObservation{readObservation(victim.ID), readObservation(target.ID)}
		for _, before := range observations {
			refresh := before
			refresh.Power = "started"
			if before.Power == "started" {
				refresh.Power = "stopped"
			}
			refresh.EvidenceDigest = strings.Repeat("b", 64)
			_, err = l.ObserveVM(refresh)
			gomega.Expect(errors.Is(err, ErrBusy)).To(gomega.BeTrue(), before.ResourceID)
			for _, original := range observations {
				gomega.Expect(readObservation(original.ResourceID)).To(gomega.Equal(original))
			}
		}
		after, err := l.PowerIntent(1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(intent))
		// Reconciled resources accept refreshes at their current observation fences.
		stage := "stop_uncertain"
		if !uncertain {
			gomega.Expect(l.ClaimPowerEffect(intent.Identity, "stop")).To(gomega.Succeed())
			stage = "stop_issued"
		}
		gomega.Expect(l.ConfirmPower(PowerConfirmation{Identity: intent.Identity, ExpectedStage: stage, VictimPower: "started", TargetPower: "stopped", NoEffectProven: true, EvidenceDigest: digest})).To(gomega.Succeed())
		for _, resource := range []ResourceSpec{victim, target} {
			refresh := readObservation(resource.ID)
			revision, err := l.ObserveVM(refresh)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(revision).To(gomega.Equal(refresh.ExpectedObservationRevision + 1))
		}
	}, ginkgo.Entry("pending victim and target", false), ginkgo.Entry("uncertain victim and target", true))
	ginkgo.It("replays availability with current resumed and leased state plus the original mutation identity", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "state.sqlite")
		db, l := openLedger(t, path, 1)
		r := validResource()
		gomega.Expect(l.RegisterResource(encode(t, r))).To(gomega.Succeed())
		pause := AvailabilityRequest{ResourceID: r.ID, MutationKey: "original-pause", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: "maintenance"}
		paused, err := l.SetAvailability(pause, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(paused.Mutation).To(gomega.Equal(&AvailabilityIdentity{MutationKey: pause.MutationKey, AppliedAvailabilityRevision: 2}))
		resume := AvailabilityRequest{ResourceID: r.ID, MutationKey: "original-resume", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 2, ReadinessDigest: digest, ObservationReference: "isolated-readiness"}
		resumed, err := l.SetAvailability(resume, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(resumed.Mutation).To(gomega.Equal(&AvailabilityIdentity{MutationKey: resume.MutationKey, AppliedAvailabilityRevision: 3}))
		job := admitScheduling(t, l, r.ID, "intervening-lease")
		job, err = reserveScheduling(l, job, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(job.LeaseGeneration).To(gomega.Equal(int64(1)))
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, l = openLedger(t, path, 1)
		defer db.Close()
		current, err := l.ResourceStatus(r.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.Availability).To(gomega.Equal("available"))
		gomega.Expect(current.AvailabilityRevision).To(gomega.Equal(int64(3)))
		gomega.Expect(current.Occupancy).To(gomega.Equal(1))
		var eventsBefore, eventsAfter int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&eventsBefore)).To(gomega.Succeed())
		for _, original := range []struct {
			request AvailabilityRequest
			resume  bool
			status  ResourceStatus
		}{{pause, false, paused}, {resume, true, resumed}} {
			replay, err := l.SetAvailability(original.request, original.resume)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			expected := current
			expected.Mutation = original.status.Mutation
			gomega.Expect(replay).To(gomega.Equal(expected))
		}
		after, err := l.ResourceStatus(r.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(current))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&eventsAfter)).To(gomega.Succeed())
		gomega.Expect(eventsAfter).To(gomega.Equal(eventsBefore))
		var receipts int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_availability_receipts`).Scan(&receipts)).To(gomega.Succeed())
		gomega.Expect(receipts).To(gomega.Equal(2))
	})
	ginkgo.It("rejects conflicting availability keys and removes client readiness claims from pause receipts", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 2)
		defer db.Close()
		r := validResource()
		gomega.Expect(l.RegisterResource(encode(t, r))).To(gomega.Succeed())
		q := AvailabilityRequest{ResourceID: r.ID, MutationKey: "same-availability", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: "maintenance", ReadinessDigest: "client-junk", ObservationReference: "client-junk"}
		_, err := l.SetAvailability(q, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var raw []byte
		gomega.Expect(db.QueryRow(`SELECT request_json FROM managed_availability_receipts WHERE mutation_key=?`, q.MutationKey).Scan(&raw)).To(gomega.Succeed())
		gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring("client-junk"))
		q.ReadinessDigest, q.ObservationReference = "", ""
		_, err = l.SetAvailability(q, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, change := range []func(*AvailabilityRequest){func(q *AvailabilityRequest) { q.Reason = "other" }, func(q *AvailabilityRequest) { q.ResourceID = "other" }, func(q *AvailabilityRequest) { q.ExpectedConfigRevision++ }, func(q *AvailabilityRequest) { q.ExpectedLeaseGeneration++ }} {
			other := q
			change(&other)
			_, err = l.SetAvailability(other, false)
			gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
		}
		q.ReadinessDigest, q.ObservationReference = digest, "controller-observation"
		_, err = l.SetAvailability(q, true)
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
	})
	ginkgo.It("keeps oversized host blockers actionable and removes them on queued termination", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 2)
		defer db.Close()
		for i := 0; i < 40; i++ {
			vmScheduling(t, l, fmt.Sprintf("vm-%03d-%s", i, strings.Repeat("x", 110)), i+1, "started", false)
		}
		vmScheduling(t, l, "target", 41, "stopped", false)
		j := admitScheduling(t, l, "target", "oversized")
		_, err := reserveScheduling(l, j, vmGuard(t, db, 2))
		gomega.Expect(errors.Is(err, ErrBusy)).To(gomega.BeTrue())
		blocker, err := l.QueueBlocker(j.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(len(blocker.Details)).To(gomega.BeNumerically("<=", 4096))
		gomega.Expect(string(blocker.Details)).To(gomega.ContainSubstring("36 additional resources"))
		_, err = l.CancelQueued(j.JobID, 1, "cancel-oversized")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		remaining, err := l.QueueBlocker(j.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(remaining).To(gomega.BeNil())
	})
	ginkgo.It("publishes sibling quarantine changes once while retaining uncertain occupancy", func() {
		t := ginkgo.GinkgoT()
		for _, cause := range []string{"restart", "uncertain", "cleanup"} {
			db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 2)
			r := validResource()
			gomega.Expect(l.RegisterResource(encode(t, r))).To(gomega.Succeed())
			held := admitScheduling(t, l, r.ID, "held")
			held, err := reserveScheduling(l, held, nil)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			queued := admitScheduling(t, l, r.ID, "sibling")
			switch cause {
			case "restart":
				gomega.Expect(l.MarkInterrupted()).To(gomega.Succeed())
				gomega.Expect(l.MarkInterrupted()).To(gomega.Succeed())
			case "uncertain":
				gomega.Expect(l.ReconcileLocalFailure(held.JobID)).To(gomega.Succeed())
				gomega.Expect(l.ReconcileLocalFailure(held.JobID)).To(gomega.Succeed())
			case "cleanup":
				settleScheduling(t, l, held, "failed")
			}
			var count int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='resource_availability'`, queued.JobID).Scan(&count)).To(gomega.Succeed())
			gomega.Expect(count).To(gomega.Equal(1))
			status, err := l.ResourceStatus(r.ID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(status.Availability).To(gomega.Equal("quarantined"))
			if cause != "cleanup" {
				gomega.Expect(status.Occupancy).To(gomega.Equal(1))
			}
			gomega.Expect(db.Close()).To(gomega.Succeed())
		}
	})
	ginkgo.It("refuses incomplete schema5 rather than silently recreating missing state", func() {
		t := ginkgo.GinkgoT()
		for _, statement := range []string{"DROP TABLE managed_parent_nonces", "ALTER TABLE managed_resources DROP COLUMN idle_since_ns"} {
			db, _ := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
			_, err := db.Exec(statement)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = NewLedger(db, 1)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(db.Close()).To(gomega.Succeed())
		}
	})
	ginkgo.It("rejects closed-schema and size violations before consuming nested admission keys", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 2)
		defer db.Close()
		r := validResource()
		gomega.Expect(l.RegisterResource(encode(t, r))).To(gomega.Succeed())
		for _, raw := range []string{strings.Repeat("x", 8193), `[{"installation_id":"i","job_id":"j","lease_generation":1,"nonce":"` + digest + `","extra":true}]`, `[{"installation_id":"i","installation_id":"j"}]`} {
			_, err := DecodeParentChain(raw)
			gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		}
		entry := ParentEntry{"foreign", "parent", 1, digest}
		chain := make(ParentChain, 32)
		for i := range chain {
			chain[i] = entry
		}
		gomega.Expect(chain.Validate()).To(gomega.Succeed())
		gomega.Expect(append(chain, entry).Validate()).To(gomega.Equal(ErrInvalid))
		_, err := l.AdmitWithParent(encode(t, validJob()), digest, "", nil, "headroom-key", "child", "installation", chain)
		gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		_, err = l.Request("headroom-key")
		gomega.Expect(errors.Is(err, ErrNotFound)).To(gomega.BeTrue())
		parent := admitScheduling(t, l, r.ID, "settled-parent")
		parent, err = reserveScheduling(l, parent, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		inherited, err := l.RunnerParentChain("installation", parent.JobID, parent.LeaseGeneration)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		settleScheduling(t, l, parent, "verified")
		_, err = l.AdmitWithParent(encode(t, validJob()), digest, "", nil, "after-parent", "child", "installation", inherited)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})
})

var _ = ginkgo.Describe("Task007ManualAccounting", ginkgo.Label("integration"), func() {
	ginkgo.DescribeTable("rejects an unready manual hold without releasing its reservation", func(pausedReady bool) {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
		defer db.Close()
		r := validResource()
		gomega.Expect(l.RegisterResource(encode(t, r))).To(gomega.Succeed())
		manual := validJob()
		manual.Mode = "manual"
		manual.Manual = &Manual{ScenarioDocument: "scenarios/check.md", RequiredScenarios: []string{"one"}}
		manual.Deadlines.RunSeconds = 0
		manual.Deadlines.ManualSeconds = 60
		job, err := l.Admit(encode(t, manual), digest, "unready-manual-key", "unready-manual")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		job, err = reserveScheduling(l, job, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		wantErr := ErrConflict
		if pausedReady {
			job, err = l.AdvanceLocal(job.JobID, job.JobRevision, "preparing", "ready", "unready-manual-ready")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = l.SetAvailability(AvailabilityRequest{ResourceID: r.ID, MutationKey: "pause-manual-ready", ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, ExpectedLeaseGeneration: job.LeaseGeneration, Reason: "maintenance"}, false)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			wantErr = ErrBlocked
		}
		before, err := l.Snapshot(job.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q := ManualHoldRequest{JobID: job.JobID, ExpectedJobRevision: before.JobRevision, ExpectedConfigRevision: before.ResourceConfigRevision, ExpectedAvailabilityRevision: before.ResourceAvailabilityRevision, Slot: before.Slot, LeaseGeneration: before.LeaseGeneration, EnvironmentDigest: manual.Environment.ProfileDigest, ReadinessDigest: digest, ObservationReference: "isolated-manual-ready", MutationKey: "reject-unready-hold"}
		_, err = l.HoldManualCapacity(q)
		gomega.Expect(errors.Is(err, wantErr)).To(gomega.BeTrue())
		after, err := l.Snapshot(job.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
		gomega.Expect(after.ResourceOccupancy).To(gomega.Equal(1))
		gomega.Expect(after.GlobalOccupancy).To(gomega.Equal(1))
		var holds, mutations int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='awaiting_manual'`, job.JobID).Scan(&holds)).To(gomega.Succeed())
		gomega.Expect(holds).To(gomega.BeZero())
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_mutations WHERE mutation_key=?`, q.MutationKey).Scan(&mutations)).To(gomega.Succeed())
		gomega.Expect(mutations).To(gomega.BeZero())
		competitor := admitScheduling(t, l, r.ID, "unready-competitor")
		_, err = reserveScheduling(l, competitor, nil)
		gomega.Expect(errors.Is(err, ErrBusy)).To(gomega.BeTrue())
	}, ginkgo.Entry("preparing", false), ginkgo.Entry("ready on a paused resource", true))
	ginkgo.It("retains a fenced manual hold across deadlines and restart without granting another lease", func() {
		t := ginkgo.GinkgoT()
		path := filepath.Join(t.TempDir(), "state.sqlite")
		db, l := openLedger(t, path, 1)
		r := validResource()
		r.Capacity = 2
		gomega.Expect(l.RegisterResource(encode(t, r))).To(gomega.Succeed())
		manual := validJob()
		manual.Mode = "manual"
		manual.Manual = &Manual{ScenarioDocument: "scenarios/check.md", RequiredScenarios: []string{"one"}}
		manual.Deadlines.RunSeconds = 0
		manual.Deadlines.ManualSeconds = 1
		parent, err := l.Admit(encode(t, manual), digest, "manual-key", "manual-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		parent, err = reserveScheduling(l, parent, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		parent, err = l.AdvanceLocal(parent.JobID, parent.JobRevision, "preparing", "ready", "manual-ready")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q := ManualHoldRequest{JobID: parent.JobID, ExpectedJobRevision: parent.JobRevision, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Slot: parent.Slot, LeaseGeneration: parent.LeaseGeneration, EnvironmentDigest: manual.Environment.ProfileDigest, ReadinessDigest: digest, ObservationReference: "isolated-manual-adapter-ready", MutationKey: "hold-manual"}
		stale := q
		stale.LeaseGeneration++
		_, err = l.HoldManualCapacity(stale)
		gomega.Expect(errors.Is(err, ErrStale)).To(gomega.BeTrue())
		held, err := l.HoldManualCapacity(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(held.JobState).To(gomega.Equal("awaiting_manual"))
		gomega.Expect(held.CapacityDelta).To(gomega.BeZero())
		gomega.Expect(held.GlobalOccupancy).To(gomega.Equal(1))
		replay, err := l.HoldManualCapacity(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.JobRevision).To(gomega.Equal(held.JobRevision))
		chain, err := l.RunnerParentChain("installation", held.JobID, held.LeaseGeneration)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		nested, err := l.Nested("installation", chain)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(nested).To(gomega.BeTrue())
		// Let the recorded manual deadline elapse; it never grants another lease.
		time.Sleep(1100 * time.Millisecond)
		// No automatic managed takeover exists. An elapsed admitted queue deadline
		// cannot cancel an awaiting human or free its slot.
		_, err = db.Exec(`UPDATE managed_jobs SET queue_deadline_ns=1 WHERE id=?`, held.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = l.ExpireQueued(held.JobID, held.JobRevision, "expired-manual")
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
		competitor := admitScheduling(t, l, r.ID, "manual-competitor")
		_, err = reserveScheduling(l, competitor, nil)
		gomega.Expect(errors.Is(err, ErrBusy)).To(gomega.BeTrue())
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, l = openLedger(t, path, 1)
		defer db.Close()
		current, err := l.Snapshot(held.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.JobState).To(gomega.Equal("awaiting_manual"))
		gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
		gomega.Expect(l.MarkInterrupted()).To(gomega.Succeed())
		current, err = l.Snapshot(held.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.JobState).To(gomega.Equal("reconciling"))
		gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
		competitor, err = l.Snapshot(competitor.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = reserveScheduling(l, competitor, nil)
		gomega.Expect(errors.Is(err, ErrBusy)).To(gomega.BeTrue())
	})
})
