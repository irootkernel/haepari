//go:build integration

package controller

import (
	"encoding/json"

	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("PersistentGuestHostBinding", ginkgo.Label("integration"), func() {
	ginkgo.It("retains every descriptor identity in the host binding and refuses missing or foreign cross-boot preparation", func() {
		f, r := previousBootVMFixture()
		guest, err := f.s.vmBinding(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(guest.Preparation).NotTo(gomega.BeNil())
		gomega.Expect(guest.Root.Persistent.Valid()).To(gomega.BeTrue())
		gomega.Expect(guest.Preparation.Control.Persistent.Valid()).To(gomega.BeTrue())
		gomega.Expect(guest.Preparation.Runner.Persistent.Valid()).To(gomega.BeTrue())
		gomega.Expect(guest.Preparation.Workspace.Persistent.Valid()).To(gomega.BeTrue())
		gomega.Expect(guest.Request.LogIdentity.Valid()).To(gomega.BeTrue())
		p, err := f.s.planJobRecovery(r.JobID, "reconcile")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.PlanToken).NotTo(gomega.BeEmpty())
		_, original, err := f.s.ledger.VMJobBinding(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		db, err := sqlite.Open(f.s.root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		for _, change := range []func(*runner.GuestReply){
			func(b *runner.GuestReply) { b.Preparation = nil },
			func(b *runner.GuestReply) { b.Preparation.Control.Persistent.FileID++ },
			func(b *runner.GuestReply) { b.Preparation.Runner.Persistent.CreatedSeconds++ },
			func(b *runner.GuestReply) {
				b.Preparation.Workspace.Persistent.VolumeUUID = "00000000000000000000000000000000"
			},
			func(b *runner.GuestReply) { b.Root.Persistent.FileID++ },
			func(b *runner.GuestReply) { b.Request.LogIdentity.FileID++ },
		} {
			var b runner.GuestReply
			gomega.Expect(json.Unmarshal(original, &b)).To(gomega.Succeed())
			change(&b)
			raw, e := json.Marshal(b)
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			_, e = db.Exec(`UPDATE managed_events SET payload_json=? WHERE job_id=? AND kind='vm_prepared'`, raw, r.JobID)
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			refused, e := f.s.planJobRecovery(r.JobID, "reconcile")
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			gomega.Expect(refused.PlanToken).To(gomega.BeEmpty())
		}
		_, err = db.Exec(`UPDATE managed_events SET payload_json=? WHERE job_id=? AND kind='vm_prepared'`, original, r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		recovered, err := f.s.vmBinding(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(recovered).To(gomega.Equal(guest))
	})
})
