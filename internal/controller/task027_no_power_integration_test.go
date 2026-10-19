//go:build integration

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task027DrainedVMPreflight", ginkgo.Label("integration"), func() {
	for _, mode := range []string{"auto", "manual"} {
		for _, stage := range []string{managed.PowerStartReady, managed.PowerStopReady, "returned-stop", "confirmed-stop", "refused-start"} {
			for _, restart := range []bool{false, true} {
				stage, restart := stage, restart
				ginkgo.It(fmt.Sprintf("%s %s restart=%t prevents every guest call and settles the original lease", mode, stage, restart), func() {
					s, c, db := availabilityFixture(ginkgo.GinkgoT())
					target := vmControllerSpec(ginkgo.GinkgoT(), "drain-target", 2, false)
					victim := vmControllerSpec(ginkgo.GinkgoT(), "drain-victim", 1, true)
					f := &fakeUTMControl{states: map[string]string{target.VM.UTMVMUUID: "started", victim.VM.UTMVMUUID: "started"}}
					s.utmCommand = f.command
					for _, spec := range []managed.ResourceSpec{target, victim} {
						_, err := c.Register(context.Background(), mustJSONScheduling(spec))
						gomega.Expect(err).NotTo(gomega.HaveOccurred())
					}
					guard, err := s.observeVMs(context.Background(), 1)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					r, err := reserveControllerVM(s, target.ID, "drain-job", guard, func(job *managed.JobSpec) {
						if mode == "manual" {
							job.Mode = "manual"
							job.Manual = &managed.Manual{ScenarioDocument: "source/check.md", RequiredScenarios: []string{"check"}}
							job.Deadlines.RunSeconds = 0
							job.Deadlines.ManualSeconds = 60
						}
					})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					initial := stage
					if stage == "refused-start" {
						initial = managed.PowerStartReady
					}
					var oldVictim *managed.ResourceSpec
					if stage != managed.PowerStartReady && stage != "refused-start" {
						initial = managed.PowerStopReady
						oldVictim = &victim
					}
					id := seedControllerHistoricalPower(db, r, target, oldVictim, initial)
					if stage == "returned-stop" || stage == "confirmed-stop" {
						gomega.Expect(s.ledger.ClaimHostPowerEffect(id, "stop")).To(gomega.Succeed())
						gomega.Expect(s.ledger.RecordPowerCommandReturn(id, "stop", true, strings.Repeat("a", 64))).To(gomega.Succeed())
						proof := managed.PowerConfirmation{Identity: id, ExpectedStage: managed.PowerStopIssued, CommandReturned: true, CommandReturnRequired: true, TargetPower: "stopped", VictimPower: "stopped", EvidenceDigest: strings.Repeat("a", 64)}
						if stage == "confirmed-stop" {
							gomega.Expect(s.ledger.ConfirmPower(proof)).To(gomega.Succeed())
						} else {
							// Exact old layout after confirmation and before the unissued start.
							_, err = db.Exec(`UPDATE managed_power_progress SET stage=?,observation_json=? WHERE operation_generation=?`, managed.PowerStartReady, mustJSONScheduling(proof), id.OperationGeneration)
							gomega.Expect(err).NotTo(gomega.HaveOccurred())
						}
					}
					if stage == "refused-start" {
						gomega.Expect(s.ledger.ClaimHostPowerEffect(id, "start")).To(gomega.Succeed())
						gomega.Expect(s.ledger.RecordPowerRefusal(id, strings.Repeat("a", 64))).To(gomega.Succeed())
						f.states[target.VM.UTMVMUUID] = "stopped"
						if restart {
							gomega.Expect(s.ledger.ConfirmPower(managed.PowerConfirmation{Identity: id, ExpectedStage: managed.PowerStartIssued, TargetPower: "stopped", NoEffectProven: true, EvidenceDigest: strings.Repeat("b", 64)})).To(gomega.Succeed())
						}
					}
					calls := 0
					s.sshCommand = func(context.Context, string, []string, []byte) (utm.CommandResult, error) {
						calls++
						return utm.CommandResult{}, errors.New("unexpected guest operation")
					}
					original, err := s.ledger.Request("key-drain-job")
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					if restart {
						// Drain before the Job is selected again: AdapterIntents is now empty.
						gomega.Expect(s.ledger.DrainHistoricalPower()).To(gomega.Succeed())
						gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
						snap, err := s.ledger.RecoverySnapshot(r.JobID)
						gomega.Expect(err).NotTo(gomega.HaveOccurred())
						gomega.Expect(snap.AdapterIntents).To(gomega.BeEmpty())
						plan, err := s.planVMJobRecovery(jobRecoveryPlan{Snapshot: snap, Action: "reconcile"})
						gomega.Expect(err).NotTo(gomega.HaveOccurred())
						gomega.Expect(plan.Unknowns).NotTo(gomega.BeEmpty())
						gomega.Expect(s.recoverInterrupted(r.JobID, true, nil, nil, "", "")).To(gomega.Succeed())
					} else {
						gomega.Expect(s.executeVM(r, original.Spec, "unused", time.Now().Add(time.Minute))).To(gomega.Succeed())
					}
					finished, err := s.ledger.Snapshot(r.JobID)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Expect(finished.JobState).To(gomega.Equal("finished"))
					gomega.Expect(finished.Verdict).To(gomega.Equal(managed.Incomplete))
					gomega.Expect(finished.ResourceOccupancy).To(gomega.BeZero())
					result, err := s.ledger.Result(r.JobID)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					var runs int
					gomega.Expect(db.QueryRow(`SELECT count(*) FROM managed_runs WHERE job_id=?`, r.JobID).Scan(&runs)).To(gomega.Succeed())
					gomega.Expect(runs).To(gomega.BeZero())
					gomega.Expect(result.NativeExitCode).To(gomega.BeNil())
					gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("not_needed")))
					gomega.Expect(s.ledger.DrainHistoricalPower()).To(gomega.Succeed())
					after, err := s.ledger.Snapshot(r.JobID)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Expect(after).To(gomega.Equal(finished))
					saved, err := s.ledger.Request("key-drain-job")
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Expect(saved.Spec).To(gomega.Equal(original.Spec))
					gomega.Expect(calls).To(gomega.BeZero())
					gomega.Expect(f.effects).To(gomega.BeEmpty())
				})
			}
		}
	}
})
