//go:build integration

package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// seedControllerHistoricalPower represents an existing pre-upgrade ledger row;
// current production reservations never create a power transition.
func seedControllerHistoricalPower(db *sql.DB, r managed.Receipt, target managed.ResourceSpec, victim *managed.ResourceSpec, stage string) managed.PowerTransitionIdentity {
	var generation int64
	gomega.Expect(db.QueryRow(`SELECT COALESCE(MAX(operation_generation),0)+1 FROM managed_power_transitions`).Scan(&generation)).To(gomega.Succeed())
	id := managed.PowerTransitionIdentity{OperationGeneration: generation, TargetJobID: r.JobID, TargetLeaseGeneration: r.LeaseGeneration, TargetResourceID: target.ID, TargetConfigRevision: target.Revision, TargetVMUUID: target.VM.UTMVMUUID}
	if victim != nil {
		id.VictimResourceID = victim.ID
		id.VictimConfigRevision = victim.Revision
		id.VictimVMUUID = victim.VM.UTMVMUUID
	}
	raw, err := json.Marshal(id)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = db.Exec(`INSERT INTO managed_power_transitions(operation_generation,victim_resource_id,target_resource_id,identity_json,state) VALUES(?,?,?,?,'pending')`, generation, id.VictimResourceID, target.ID, raw)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = db.Exec(`INSERT INTO managed_power_progress(operation_generation,stage) VALUES(?,?)`, generation, stage)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return id
}

func orphanPowerFixture(effect string) (*runtimeServer, *client.Client, *fakeUTMControl, managed.PowerTransitionIdentity) {
	t := ginkgo.GinkgoT()
	s, c, db := availabilityFixture(t)
	target := vmControllerSpec(t, "power-target", 2, false)
	f := &fakeUTMControl{states: map[string]string{target.VM.UTMVMUUID: "started"}}
	s.utmCommand = f.command
	_, err := c.Register(context.Background(), mustJSONScheduling(target))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var historicalVictim *managed.ResourceSpec
	if effect == "stop" {
		victim := vmControllerSpec(t, "power-victim", 1, true)
		historicalVictim = &victim
		f.states[victim.VM.UTMVMUUID] = "started"
		_, err = c.Register(context.Background(), mustJSONScheduling(victim))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
	guard, err := s.observeVMs(context.Background(), 1)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	reserved, err := reserveControllerVM(s, target.ID, "orphan-power", guard)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	stage := managed.PowerStartReady
	if effect == "stop" {
		stage = managed.PowerStopReady
	}
	seedControllerHistoricalPower(db, reserved, target, historicalVictim, stage)
	f.states[target.VM.UTMVMUUID] = "stopped"
	snap, err := s.ledger.PowerRecoverySnapshot(target.ID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(s.ledger.ClaimHostPowerEffect(snap.Intent.Identity, effect)).To(gomega.Succeed())
	gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
	return s, c, f, snap.Intent.Identity
}

func publicPowerPlan(c *client.Client, id string) powerRecoveryPlan {
	env, err := c.RecoverResource(context.Background(), id, managed.RecoveryRequest{Plan: true, Action: "attest-power-stopped"})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var p powerRecoveryPlan
	gomega.Expect(json.Unmarshal(env.Data, &p)).To(gomega.Succeed())
	return p
}

func powerApplyRequest(p powerRecoveryPlan) managed.RecoveryRequest {
	e := p.RequiredEvidence
	e.Observer = "qualified-operator"
	e.ObservedAt = time.Now().UTC()
	e.Digest = managed.RecoveryDigest("bounded external cessation evidence")
	return managed.RecoveryRequest{PlanToken: p.PlanToken, MutationKey: "saved-power-recovery", Evidence: &e}
}

var _ = ginkgo.Describe("Task036PowerRecovery", ginkgo.Label("integration"), func() {
	for _, corrupt := range []bool{false, true} {
		corrupt := corrupt
		name := "explains a retired transition's absence without changing durable state"
		if corrupt {
			name = "refuses unsupported corrupted multiple transitions without selecting or changing either claim"
		}
		ginkgo.It(name, func() {
			s, c, f, id := orphanPowerFixture("start")
			db, err := sqlite.Open(s.root.DBPath)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer db.Close()
			count := 0
			if corrupt {
				// Unsupported corruption fixture: production reservation fences
				// prohibit two unresolved claims referencing the same resource.
				// Selection must refuse before decoding or choosing either row.
				_, err = db.Exec(`INSERT INTO managed_power_transitions(operation_generation,victim_resource_id,target_resource_id,identity_json,state) SELECT operation_generation+1,victim_resource_id,target_resource_id,identity_json,state FROM managed_power_transitions WHERE operation_generation=?`, id.OperationGeneration)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				_, err = db.Exec(`INSERT INTO managed_power_progress(operation_generation,stage,observation_json) SELECT operation_generation+1,stage,observation_json FROM managed_power_progress WHERE operation_generation=?`, id.OperationGeneration)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				count = 2
			} else {
				p := publicPowerPlan(c, id.TargetResourceID)
				_, err = c.RecoverResource(context.Background(), id.TargetResourceID, powerApplyRequest(p))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			before, err := s.ledger.RecoverySnapshot(id.TargetJobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			intent, err := s.ledger.PowerIntent(id.OperationGeneration)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			// Include the corrupted row and replay records in the unchanged-state
			// check rather than inspecting only the original claim.
			readRows := func() string {
				var rows string
				e := db.QueryRow(`SELECT group_concat(row,'|') FROM (SELECT printf('%d:%s:%s:%s:%s',operation_generation,victim_resource_id,target_resource_id,hex(identity_json),state) AS row FROM managed_power_transitions ORDER BY operation_generation)`).Scan(&rows)
				gomega.Expect(e).NotTo(gomega.HaveOccurred())
				return rows
			}
			rows := readRows()
			var mutations int
			gomega.Expect(db.QueryRow(`SELECT count(*) FROM managed_mutations`).Scan(&mutations)).To(gomega.Succeed())
			_, err = s.ledger.PowerRecoverySnapshot(id.TargetResourceID)
			gomega.Expect(errors.Is(err, managed.ErrBlocked)).To(gomega.BeTrue())
			var refusal *managed.PowerRecoverySelectionRefusal
			gomega.Expect(errors.As(err, &refusal)).To(gomega.BeTrue())
			gomega.Expect(refusal.Count).To(gomega.Equal(count))
			env, err := c.RecoverResource(context.Background(), id.TargetResourceID, managed.RecoveryRequest{Plan: true, Action: "attest-power-stopped"})
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(env.Error).NotTo(gomega.BeNil())
			gomega.Expect(env.Error.Code).To(gomega.Equal("RECOVERY_BLOCKED"))
			gomega.Expect(env.Error.Message).To(gomega.Equal(refusal.Error()))
			gomega.Expect(env.Data).To(gomega.BeEmpty())
			after, err := s.ledger.RecoverySnapshot(id.TargetJobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(after).To(gomega.Equal(before))
			afterIntent, err := s.ledger.PowerIntent(id.OperationGeneration)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(afterIntent).To(gomega.Equal(intent))
			gomega.Expect(readRows()).To(gomega.Equal(rows))
			var afterMutations int
			gomega.Expect(db.QueryRow(`SELECT count(*) FROM managed_mutations`).Scan(&afterMutations)).To(gomega.Succeed())
			gomega.Expect(afterMutations).To(gomega.Equal(mutations))
			gomega.Expect(f.effects).To(gomega.BeEmpty())
		})
	}
	ginkgo.It("cannot use power cessation to settle a Job with an existing guest claim", func() {
		s, c, f, id := orphanPowerFixture("start")
		p := publicPowerPlan(c, id.TargetResourceID)
		_, err := c.RecoverResource(context.Background(), id.TargetResourceID, powerApplyRequest(p))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		original, err := s.ledger.LocalIdentity(id.TargetJobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		db, err := sqlite.Open(s.root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		// Controlled lost-receipt fixture: a retained claim is authoritative even
		// though the guest cannot currently supply a usable native observation.
		_, err = db.Exec(`INSERT INTO managed_runs(job_id,run_id,intent_json) VALUES(?,?,?)`, id.TargetJobID, "claimed-guest-run", []byte(`{}`))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = s.ledger.VMPreflightCessation(original)
		gomega.Expect(err).To(gomega.HaveOccurred())
		env, err := c.RecoverJob(context.Background(), id.TargetJobID, managed.RecoveryRequest{Plan: true})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var plan jobRecoveryPlan
		gomega.Expect(json.Unmarshal(env.Data, &plan)).To(gomega.Succeed())
		gomega.Expect(plan.PlanToken).To(gomega.BeEmpty())
		gomega.Expect(plan.Unknowns).NotTo(gomega.BeEmpty())
		job, err := s.ledger.Snapshot(id.TargetJobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(job.JobState).To(gomega.Equal("reconciling"))
		gomega.Expect(job.ResourceOccupancy).To(gomega.Equal(1))
		gomega.Expect(f.effects).To(gomega.BeEmpty())
	})
	ginkgo.It("settles the original pre-guest Job only through an explicit plan and returns reusable capacity after resource recovery", func() {
		s, c, f, id := orphanPowerFixture("start")
		p := publicPowerPlan(c, id.TargetResourceID)
		powerQ := powerApplyRequest(p)
		_, err := c.RecoverResource(context.Background(), id.TargetResourceID, powerQ)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		snap, err := s.ledger.RecoverySnapshot(id.TargetJobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(s.recoverVMPreflight(snap, true, nil, nil, "", "")).To(gomega.MatchError(managed.ErrBlocked))
		env, err := c.RecoverJob(context.Background(), id.TargetJobID, managed.RecoveryRequest{Plan: true})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var jobPlan jobRecoveryPlan
		gomega.Expect(json.Unmarshal(env.Data, &jobPlan)).To(gomega.Succeed())
		gomega.Expect(jobPlan.Schema).To(gomega.Equal("haepari.recovery-plan/v2"))
		gomega.Expect(jobPlan.Unknowns).To(gomega.BeEmpty())
		q := managed.RecoveryRequest{PlanToken: jobPlan.PlanToken, MutationKey: "saved-preflight-settlement"}
		_, err = c.RecoverJob(context.Background(), id.TargetJobID, q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		job, err := s.ledger.Snapshot(id.TargetJobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(job.JobState).To(gomega.Equal("finished"))
		gomega.Expect(job.Verdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(job.ResourceOccupancy).To(gomega.BeZero())
		gomega.Expect(job.ResourceAvailability).To(gomega.Equal("quarantined"))
		result, err := s.ledger.Result(id.TargetJobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("not_needed")))
		gomega.Expect(result.Observations.OperatorAttested).To(gomega.BeTrue())
		env, err = c.RecoverResource(context.Background(), id.TargetResourceID, managed.RecoveryRequest{Plan: true})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var resourcePlan resourceRecoveryPlan
		gomega.Expect(json.Unmarshal(env.Data, &resourcePlan)).To(gomega.Succeed())
		gomega.Expect(resourcePlan.Unknowns).To(gomega.BeEmpty())
		_, err = c.RecoverResource(context.Background(), id.TargetResourceID, managed.RecoveryRequest{PlanToken: resourcePlan.PlanToken, MutationKey: "saved-resource-readiness"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f.states[id.TargetVMUUID] = "started"
		guard, err := s.observeVMs(context.Background(), 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		successor, err := s.ledger.Admit(mustJSONScheduling(snap.Spec), strings.Repeat("a", 64), "power-successor-key", "power-successor")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		resource, err := s.ledger.ResourceStatus(id.TargetResourceID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		successor, err = s.ledger.Reserve(managed.ReserveRequest{JobID: successor.JobID, ExpectedJobRevision: successor.JobRevision, ExpectedConfigRevision: resource.ConfigRevision, ExpectedAvailabilityRevision: resource.AvailabilityRevision, MutationKey: "power-successor-reserve", VMGuard: guard})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(successor.JobState).To(gomega.Equal("preparing"))
		gomega.Expect(successor.LeaseGeneration).To(gomega.BeNumerically(">", id.TargetLeaseGeneration))
		events, err := s.ledger.EventsAfter(id.TargetJobID, 0, 1000)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(s.ledger.PurgeEventsThrough(id.TargetJobID, events.CurrentCursor)).To(gomega.Succeed())
		_, err = c.RecoverResource(context.Background(), id.TargetResourceID, powerQ)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = c.RecoverJob(context.Background(), id.TargetJobID, q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		unchanged, err := s.ledger.Result(id.TargetJobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(unchanged).To(gomega.Equal(result))
		gomega.Expect(f.effects).To(gomega.BeEmpty())
	})
	ginkgo.It("retires an orphan start without fabricating return or releasing Job occupancy and replays after restart", func() {
		s, c, f, id := orphanPowerFixture("start")
		p := publicPowerPlan(c, id.TargetResourceID)
		gomega.Expect(p.Schema).To(gomega.Equal("haepari.power-recovery-plan/v2"))
		gomega.Expect(p.Unknowns).To(gomega.BeEmpty())
		gomega.Expect(p.PlanToken).NotTo(gomega.BeEmpty())
		q := powerApplyRequest(p)
		q.Evidence = nil
		_, err := c.RecoverResource(context.Background(), id.TargetResourceID, q)
		gomega.Expect(err).To(gomega.HaveOccurred())
		original, err := s.ledger.PowerIntent(id.OperationGeneration)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(original.State).To(gomega.Equal("pending"))
		q = powerApplyRequest(p)
		env, err := c.RecoverResource(context.Background(), id.TargetResourceID, q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		current, err := s.ledger.PowerIntent(id.OperationGeneration)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.Stage).To(gomega.Equal(managed.PowerAbandoned))
		gomega.Expect(current.CommandReturned()).To(gomega.BeFalse())
		gomega.Expect(current.RefusedWithoutEffect()).To(gomega.BeFalse())
		gomega.Expect(errors.Is(s.ledger.ClaimHostPowerEffect(id, "start"), managed.ErrBlocked)).To(gomega.BeTrue())
		job, err := s.ledger.Snapshot(id.TargetJobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(job.JobState).To(gomega.Equal("reconciling"))
		gomega.Expect(job.ResourceOccupancy).To(gomega.Equal(1))
		gomega.Expect(job.ResourceAvailability).To(gomega.Equal("quarantined"))
		gomega.Expect(f.effects).To(gomega.BeEmpty())
		db, err := sqlite.Open(s.root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		s.ledger, err = managed.NewLedger(db, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		s.recoveryKey[0] ^= 1
		replay, err := c.RecoverResource(context.Background(), id.TargetResourceID, q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.Data).To(gomega.Equal(env.Data))
		q.Evidence.Digest = managed.RecoveryDigest("different proof")
		_, err = c.RecoverResource(context.Background(), id.TargetResourceID, q)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(f.effects).To(gomega.BeEmpty())
	})
	ginkgo.It("retires an orphan victim stop without starting the target", func() {
		s, c, f, id := orphanPowerFixture("stop")
		f.states[id.VictimVMUUID] = "stopped"
		p := publicPowerPlan(c, id.VictimResourceID)
		gomega.Expect(p.Unknowns).To(gomega.BeEmpty())
		_, err := c.RecoverResource(context.Background(), id.VictimResourceID, powerApplyRequest(p))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		current, err := s.ledger.PowerIntent(id.OperationGeneration)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.Stage).To(gomega.Equal(managed.PowerAbandoned))
		gomega.Expect(f.effects).To(gomega.BeEmpty())
		guard, err := s.observeVMs(context.Background(), 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(guard.ObservationRevisions).To(gomega.HaveLen(2))
	})
	ginkgo.It("records an observed completed start using distinct cessation proof", func() {
		s, c, f, id := orphanPowerFixture("start")
		f.states[id.TargetVMUUID] = "started"
		p := publicPowerPlan(c, id.TargetResourceID)
		_, err := c.RecoverResource(context.Background(), id.TargetResourceID, powerApplyRequest(p))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		current, err := s.ledger.PowerIntent(id.OperationGeneration)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.Stage).To(gomega.Equal(managed.PowerDone))
		gomega.Expect(current.CommandReturned()).To(gomega.BeFalse())
		gomega.Expect(f.effects).To(gomega.BeEmpty())
	})
	ginkgo.It("rejects changed power observations and wrongly scoped or stale operator evidence", func() {
		s, c, f, id := orphanPowerFixture("start")
		p := publicPowerPlan(c, id.TargetResourceID)
		q := powerApplyRequest(p)
		for _, modify := range []func(*managed.RecoveryEvidence){
			func(e *managed.RecoveryEvidence) { e.Source = "machine-observed" },
			func(e *managed.RecoveryEvidence) { e.Mechanism = "VM is stopped" },
			func(e *managed.RecoveryEvidence) { e.Reference = "power:999" },
			func(e *managed.RecoveryEvidence) { e.ObservedAt = time.Now().UTC().Add(-time.Hour) },
			func(e *managed.RecoveryEvidence) { e.ObservedAt = time.Now().UTC().Add(time.Hour) },
		} {
			bad := q
			copied := *q.Evidence
			bad.Evidence = &copied
			modify(bad.Evidence)
			_, err := c.RecoverResource(context.Background(), id.TargetResourceID, bad)
			gomega.Expect(err).To(gomega.HaveOccurred())
		}
		f.states[id.TargetVMUUID] = "started"
		_, err := c.RecoverResource(context.Background(), id.TargetResourceID, q)
		gomega.Expect(err).To(gomega.HaveOccurred())
		current, err := s.ledger.PowerIntent(id.OperationGeneration)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.Stage).To(gomega.Equal(managed.PowerStartIssued))
		gomega.Expect(current.CommandReturned()).To(gomega.BeFalse())
		gomega.Expect(f.effects).To(gomega.BeEmpty())
	})
	ginkgo.It("withholds a token for live workers and unknown fresh states", func() {
		s, c, f, id := orphanPowerFixture("start")
		s.workerMu.Lock()
		if s.jobWorkers == nil {
			s.jobWorkers = map[string]int{}
		}
		s.jobWorkers[id.TargetJobID] = 1
		s.workerMu.Unlock()
		p := publicPowerPlan(c, id.TargetResourceID)
		gomega.Expect(p.PlanToken).To(gomega.BeEmpty())
		s.workerMu.Lock()
		delete(s.jobWorkers, id.TargetJobID)
		s.workerMu.Unlock()
		f.failStatus = map[string]bool{id.TargetVMUUID: true}
		p = publicPowerPlan(c, id.TargetResourceID)
		gomega.Expect(p.PlanToken).To(gomega.BeEmpty())
		gomega.Expect(strings.Join(p.Unknowns, " ")).To(gomega.ContainSubstring("unproven"))
		gomega.Expect(f.effects).To(gomega.BeEmpty())
	})
})
