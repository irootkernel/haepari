//go:build integration

package managed

import (
	"encoding/json"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("VMRecoveryObservationFence", ginkgo.Label("integration"), func() {
	ginkgo.It("retains observation audit without staling an exact ledger apply", func() {
		db, l, r := cancellationFixture(ginkgo.GinkgoT())
		r, err := reserveScheduling(l, r, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = l.BindInstallation("telemetry-fixture")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(l.MarkInterrupted()).To(gomega.Succeed())
		basis, err := l.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		appendEvent := func(kind string, payload any) {
			tx, err := db.Begin()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = addEvent(tx, r.JobID, kind, payload)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(l.commit(tx)).To(gomega.Succeed())
		}
		record := func(operation string, complete bool) map[string]any {
			raw, err := json.Marshal(VMSSHExchange{Schema: "haepari.ssh-exchange/v1", Identity: basis.Identity, Operation: operation, Complete: complete})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var value map[string]any
			gomega.Expect(json.Unmarshal(raw, &value)).To(gomega.Succeed())
			return value
		}
		for _, operation := range []string{"identity", "state", "inspect", "health", "probe"} {
			for _, complete := range []bool{false, true} {
				raw, err := json.Marshal(record(operation, complete))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(l.RecordVMSSHMeasurement(basis.Identity, raw, complete)).To(gomega.Succeed())
			}
		}
		appendEvent("vm_ssh_measurement_gap", map[string]string{"schema": "haepari.ssh-measurement-gap/v1", "reason": "exchange_detail_limit"})
		same, err := l.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(same).To(gomega.Equal(basis))
		var count int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind LIKE 'vm_ssh_%'`, r.JobID).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.Equal(11))
		fenced := func(kind string, payload any) {
			appendEvent(kind, payload)
			changed, err := l.RecoverySnapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(changed.EventsDigest).NotTo(gomega.Equal(same.EventsDigest))
			same = changed
		}
		for _, operation := range []string{"start", "prepare", "cancel", "cleanup", "retire", "future-operation"} {
			fenced("vm_ssh_exchange_start", record(operation, false))
			fenced("vm_ssh_exchange_complete", record(operation, true))
		}
		for _, mutate := range []func(map[string]any){
			func(v map[string]any) { v["schema"] = "haepari.ssh-exchange/v99" },
			func(v map[string]any) { v["Operation"] = "state"; v["operation"] = "cancel" },
			func(v map[string]any) { delete(v, "operation"); v["Operation"] = "state" },
			func(v map[string]any) {
				id := v["identity"].(map[string]any)
				id["jobid"] = id["JobID"]
				delete(id, "JobID")
			},
			func(v map[string]any) { v["unknown"] = true },
			func(v map[string]any) { delete(v, "complete") },
			func(v map[string]any) { v["complete"] = nil },
			func(v map[string]any) { v["complete"] = true },
			func(v map[string]any) {
				v["identity"].(map[string]any)["LeaseGeneration"] = basis.Identity.LeaseGeneration + 1
			},
			func(v map[string]any) { v["identity"].(map[string]any)["unknown"] = true },
			func(v map[string]any) { delete(v["identity"].(map[string]any), "Slot") },
		} {
			v := record("state", false)
			mutate(v)
			fenced("vm_ssh_exchange_start", v)
		}
		fenced("vm_ssh_exchange_start", "malformed-exchange")
		fenced("vm_ssh_exchange_complete", record("state", false))
		fenced("vm_ssh_measurement_gap", map[string]string{"schema": "haepari.ssh-measurement-gap/v1", "reason": "other"})
		fenced("vm_ssh_measurement_gap", map[string]any{"schema": "haepari.ssh-measurement-gap/v1", "reason": "exchange_detail_limit", "unknown": true})
		out := LocalOutcome{WorkloadOutcome: "inconclusive"}
		q := JobRecoveryRequest{Identity: basis.Identity, SnapshotDigest: RecoveryDigest(basis), MutationKey: "audit-recovery", Action: "no-start", NextState: "settling", NoOwnedEffect: true, Outcome: &out, Evidence: recoveryEvidenceForTest()}
		_, err = l.BeginJobRecovery(q)
		gomega.Expect(err).To(gomega.MatchError(ErrStale))
		q.SnapshotDigest = RecoveryDigest(same)
		// New observation telemetry after freezing the exact request is harmless.
		appendEvent("vm_ssh_exchange_start", record("state", false))
		first, err := l.BeginJobRecovery(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		replay, err := l.BeginJobRecovery(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay).To(gomega.Equal(first))
	})
})
