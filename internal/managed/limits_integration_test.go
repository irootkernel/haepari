//go:build integration

package managed

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"path/filepath"
)

var _ = ginkgo.Describe("Task037MixedExecutionLimits", ginkgo.Label("integration"), func() {
	ginkgo.It("caps durable SSH details and publishes one explicit measurement gap", func() {
		t := ginkgo.GinkgoT()
		db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
		defer db.Close()
		vmScheduling(t, l, "vm-a", 1, "started", false)
		r := admitScheduling(t, l, "vm-a", "measurement-bound")
		r, err := reserveScheduling(l, r, vmGuard(t, db, 1))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		id, err := l.LocalIdentity(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		tx, err := db.Begin()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for i := 0; i < 4096; i++ {
			_, err = addEvent(tx, r.JobID, "vm_ssh_exchange_start", map[string]any{"fixture": i})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		gomega.Expect(tx.Commit()).To(gomega.Succeed())
		raw := []byte(`{"schema":"haepari.ssh-exchange/v1"}`)
		gomega.Expect(l.RecordVMSSHMeasurement(id, raw, true)).To(gomega.Succeed())
		gomega.Expect(l.RecordVMSSHMeasurement(id, raw, false)).To(gomega.Succeed())
		gomega.Expect(l.RecordVMSSHMeasurement(id, raw, false)).To(gomega.Succeed())
		gomega.Expect(l.RecordVMSSHMeasurement(id, raw, true)).To(gomega.Succeed())
		for kind, expected := range map[string]int{"vm_ssh_exchange_start": 4096, "vm_ssh_exchange_complete": 1, "vm_ssh_measurement_gap": 1} {
			var count int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind=?`, r.JobID, kind).Scan(&count)).To(gomega.Succeed())
			gomega.Expect(count).To(gomega.Equal(expected))
		}
	})
	for _, limit := range []int{1, 2, 3} {
		ginkgo.It("preserves independent mixed reservations and one slot per VM at the selected limit", func() {
			t := ginkgo.GinkgoT()
			db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), limit)
			defer db.Close()
			vmScheduling(t, l, "vm-a", 1, "started", false)
			vmScheduling(t, l, "vm-b", 2, "started", false)
			resource := validResource()
			resource.ID = "local"
			resource.Capacity = 2
			gomega.Expect(l.RegisterResource(encode(t, resource))).To(gomega.Succeed())
			first := admitScheduling(t, l, "vm-a", "first")
			first, err := reserveScheduling(l, first, vmGuard(t, db, 2))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			second := admitScheduling(t, l, "vm-b", "second")
			local := admitScheduling(t, l, "local", "third")
			duplicate := admitScheduling(t, l, "vm-a", "same-vm")
			_, err = reserveScheduling(l, duplicate, vmGuard(t, db, 2))
			gomega.Expect(err).To(gomega.MatchError(ErrBusy))
			if limit >= 2 {
				second, err = reserveScheduling(l, second, vmGuard(t, db, 2))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			} else {
				_, err = reserveScheduling(l, second, vmGuard(t, db, 2))
				gomega.Expect(err).To(gomega.MatchError(ErrBusy))
			}
			if limit == 3 {
				local, err = reserveScheduling(l, local, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(local.GlobalOccupancy).To(gomega.Equal(3))
			} else {
				_, err = reserveScheduling(l, local, nil)
				gomega.Expect(err).To(gomega.MatchError(ErrBusy))
			}
			// Explicit no-effect classification returns one exact reservation;
			// the other VM retains its original lease, never sharing VM capacity.
			proof := noEffectProof()
			proof.ObservationReference = "mixed-first"
			first = recordObservation(t, l, first.JobID, first.JobRevision, first.ResourceID, first.Slot, first.LeaseGeneration, proof)
			first, err = l.ClassifyNoEffect(ClassifyNoEffectRequest{JobID: first.JobID, ExpectedJobRevision: first.JobRevision, ResourceID: first.ResourceID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Slot: first.Slot, LeaseGeneration: first.LeaseGeneration, FailureKind: "prepare_failure", Proof: proof, MutationKey: "classify-first"})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = l.SettleNoEffect(SettleNoEffectRequest{JobID: first.JobID, ExpectedJobRevision: first.JobRevision, ResourceID: first.ResourceID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Slot: first.Slot, LeaseGeneration: first.LeaseGeneration, MutationKey: "settle-first"})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if limit >= 2 {
				current, e := l.Snapshot(second.JobID)
				gomega.Expect(e).NotTo(gomega.HaveOccurred())
				gomega.Expect(current.LeaseGeneration).To(gomega.Equal(second.LeaseGeneration))
				gomega.Expect(current.JobState).To(gomega.Equal("preparing"))
			}
			if limit < 3 {
				local, err = reserveScheduling(l, local, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			gomega.Expect(local.Slot).To(gomega.Equal(0))
		})
	}
})
