//go:build integration

package managed

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task027HistoricalDrain", ginkgo.Label("integration"), func() {
	for _, corrupt := range []string{"issued", "uncertain", "missing-return", "malformed", "wrong-identity", "false-guard", "unknown-field", "duplicate-field", "target-missing", "target-started", "victim-missing", "victim-started", "no-effect", "missing-digest", "future", "valid"} {
		corrupt := corrupt
		ginkgo.It("preserves protection and exact stored bytes for "+corrupt, func() {
			t := ginkgo.GinkgoT()
			db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 2)
			defer db.Close()
			target := vmScheduling(t, l, "target", 2, "started", false)
			victim := vmScheduling(t, l, "victim", 1, "started", true)
			j := admitScheduling(t, l, target.ID, "drain-job")
			r, err := reserveScheduling(l, j, vmGuard(t, db, 1))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			id := seedHistoricalPower(t, db, r, target, &victim, PowerStopReady)
			gomega.Expect(l.ClaimHostPowerEffect(id, "stop")).To(gomega.Succeed())
			proof := PowerConfirmation{Identity: id, ExpectedStage: PowerStopIssued, CommandReturnRequired: true, CommandReturned: true, TargetPower: "stopped", VictimPower: "stopped", EvidenceDigest: digest}
			stage, state := PowerStartReady, "pending"
			switch corrupt {
			case "issued":
				stage = PowerStopIssued
				proof.CommandReturned = false
			case "uncertain":
				stage = PowerStopUncertain
				state = "uncertain"
				proof.CommandReturned = false
			case "missing-return":
				proof.CommandReturned = false
			case "wrong-identity":
				proof.Identity.TargetLeaseGeneration++
			case "false-guard":
				proof.CommandReturnRequired = false
			case "target-missing":
				proof.TargetPower = ""
			case "target-started":
				proof.TargetPower = "started"
			case "victim-missing":
				proof.VictimPower = ""
			case "victim-started":
				proof.VictimPower = "started"
			case "no-effect":
				proof.NoEffectProven = true
			case "missing-digest":
				proof.EvidenceDigest = ""
			}
			raw, _ := json.Marshal(proof)
			if corrupt == "malformed" {
				raw = []byte(`{"broken":`)
			}
			if corrupt == "unknown-field" {
				raw = append(raw[:len(raw)-1], []byte(`,"unknown_proof":true}`)...)
			}
			if corrupt == "duplicate-field" {
				raw = append(raw[:len(raw)-1], []byte(`,"CommandReturned":true}`)...)
			}
			_, err = db.Exec(`UPDATE managed_power_progress SET stage=?,observation_json=? WHERE operation_generation=?`, stage, raw, id.OperationGeneration)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = db.Exec(`UPDATE managed_power_transitions SET state=? WHERE operation_generation=?`, state, id.OperationGeneration)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if corrupt == "future" {
				_, err = db.Exec(`UPDATE managed_power_progress SET stage=? WHERE operation_generation=?`, PowerStopRetained, id.OperationGeneration)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				_, err = db.Exec(`UPDATE managed_power_transitions SET state='reconciled',reconciled_at_ns=? WHERE operation_generation=?`, time.Now().Add(time.Hour).UnixNano(), id.OperationGeneration)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			read := func() string {
				var value string
				gomega.Expect(db.QueryRow(`SELECT printf('%s:%s:%s:%s:%d',hex(identity_json),state,stage,hex(observation_json),reconciled_at_ns) FROM managed_power_transitions JOIN managed_power_progress USING(operation_generation) WHERE operation_generation=?`, id.OperationGeneration).Scan(&value)).To(gomega.Succeed())
				return value
			}
			before := read()
			gomega.Expect(l.DrainHistoricalPower()).To(gomega.Succeed())
			local, err := l.LocalIdentity(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			disposition, err := l.GuestPowerDisposition(local)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if corrupt == "valid" {
				gomega.Expect(disposition).To(gomega.Equal("no_guest"))
				boundary, err := l.DrainedVMPreflight(local)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(boundary.PowerIdentity).To(gomega.Equal(id))
				gomega.Expect(boundary.Stage).To(gomega.Equal(PowerStopRetained))
				gomega.Expect(boundary.EvidenceDigest()).To(gomega.HaveLen(64))
				stale := local
				stale.LeaseGeneration++
				_, err = l.DrainedVMPreflight(stale)
				gomega.Expect(errors.Is(err, ErrStale)).To(gomega.BeTrue())
				stable := read()
				gomega.Expect(l.DrainHistoricalPower()).To(gomega.Succeed())
				gomega.Expect(read()).To(gomega.Equal(stable))
			} else {
				gomega.Expect(disposition).To(gomega.Equal("protected"))
				gomega.Expect(read()).To(gomega.Equal(before))
			}
		})
	}
	for _, corruption := range []string{"no-effect", "return", "return-required", "target", "stage", "digest", "missing-refusal", "wrong-refusal", "guest-claim"} {
		corruption := corruption
		ginkgo.It("protects an original returned refusal with inconsistent "+corruption, func() {
			t := ginkgo.GinkgoT()
			db, l := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 1)
			defer db.Close()
			target := vmScheduling(t, l, "target", 2, "started", false)
			job := admitScheduling(t, l, target.ID, "refused-job")
			r, err := reserveScheduling(l, job, vmGuard(t, db, 1))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			id := seedHistoricalPower(t, db, r, target, nil, PowerStartReady)
			gomega.Expect(l.ClaimHostPowerEffect(id, "start")).To(gomega.Succeed())
			gomega.Expect(l.RecordPowerRefusal(id, digest)).To(gomega.Succeed())
			confirmation := PowerConfirmation{Identity: id, ExpectedStage: PowerStartIssued, TargetPower: "stopped", NoEffectProven: true, EvidenceDigest: digest}
			gomega.Expect(l.ConfirmPower(confirmation)).To(gomega.Succeed())
			var raw []byte
			gomega.Expect(db.QueryRow(`SELECT observation_json FROM managed_power_progress WHERE operation_generation=?`, id.OperationGeneration).Scan(&raw)).To(gomega.Succeed())
			var proof PowerConfirmation
			gomega.Expect(json.Unmarshal(raw, &proof)).To(gomega.Succeed())
			switch corruption {
			case "no-effect":
				proof.NoEffectProven = false
			case "return":
				proof.CommandReturned = false
			case "return-required":
				proof.CommandReturnRequired = false
			case "target":
				proof.TargetPower = "started"
			case "stage":
				proof.ExpectedStage = PowerStopIssued
			case "digest":
				proof.EvidenceDigest = ""
			case "missing-refusal":
				_, err = db.Exec(`DELETE FROM managed_events WHERE job_id=? AND kind='power_start_refused'`, r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			case "wrong-refusal":
				_, err = db.Exec(`UPDATE managed_events SET payload_json=json_set(payload_json,'$.identity.target_lease_generation',99) WHERE job_id=? AND kind='power_start_refused'`, r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			case "guest-claim":
				_, err = db.Exec(`INSERT INTO managed_events(job_id,kind,payload_json) VALUES(?,'vm_prepared','{}')`, r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			changed, _ := json.Marshal(proof)
			_, err = db.Exec(`UPDATE managed_power_progress SET observation_json=? WHERE operation_generation=?`, changed, id.OperationGeneration)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			local, err := l.LocalIdentity(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			disposition, err := l.GuestPowerDisposition(local)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(disposition).To(gomega.Equal("protected"))
			gomega.Expect(l.DrainHistoricalPower()).To(gomega.Succeed())
			var after []byte
			gomega.Expect(db.QueryRow(`SELECT observation_json FROM managed_power_progress WHERE operation_generation=?`, id.OperationGeneration).Scan(&after)).To(gomega.Succeed())
			gomega.Expect(after).To(gomega.Equal(changed))
		})
	}

})
