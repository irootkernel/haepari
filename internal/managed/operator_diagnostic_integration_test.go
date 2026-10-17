//go:build integration

package managed

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/config"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func diagnosticOpen(t ginkgo.GinkgoTInterface) (*sql.DB, *Ledger) {
	t.Helper()
	db, ledger := openLedger(t, filepath.Join(t.TempDir(), "state.sqlite"), 4)
	_, err := ledger.BindInstallation("diag-install")
	gomega.Expect(err).To(gomega.Succeed())
	return db, ledger
}

func diagnosticAt(section, resource, cursor string, limit int, observed time.Time, alive func(string) bool) DiagnosticQuery {
	return DiagnosticQuery{Section: section, Resource: resource, Cursor: cursor, Limit: limit, ObservedAt: observed, WorkerAlive: alive}
}

func diagnosticMust(l *Ledger, q DiagnosticQuery) OperatorDiagnostic {
	page, err := l.OperatorDiagnostic(q)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return page
}

func diagnosticInstallation(db *sql.DB) string {
	var id string
	gomega.Expect(db.QueryRow(`SELECT id FROM managed_installation WHERE singleton=1`).Scan(&id)).To(gomega.Succeed())
	return id
}

func diagnosticQueueCursor(installation, resource, lastID, upperID string) string {
	return encodePageCursor(pageCursor{Kind: diagnosticQueueKind, Installation: installation, Resource: resource, LastID: lastID, UpperID: upperID})
}

func diagnosticByResource(page OperatorDiagnostic) map[string]DiagnosticQueueRow {
	rows := map[string]DiagnosticQueueRow{}
	for _, row := range page.Resources {
		rows[row.ResourceID] = row
	}
	return rows
}

func diagnosticByJob(page OperatorDiagnostic) map[string]DiagnosticRetentionRow {
	rows := map[string]DiagnosticRetentionRow{}
	for _, row := range page.Jobs {
		rows[row.JobID] = row
	}
	return rows
}

func diagnosticRegister(t ginkgo.GinkgoTInterface, l *Ledger, id string, capacity int) {
	t.Helper()
	resource := validResource()
	resource.ID = id
	resource.Capacity = capacity
	gomega.Expect(l.RegisterResource(encode(t, resource))).To(gomega.Succeed())
}

func diagnosticAdmit(t ginkgo.GinkgoTInterface, l *Ledger, resource, id string, manual bool) Receipt {
	t.Helper()
	job := validJob()
	job.ResourceID = resource
	if manual {
		job.Mode = "manual"
		job.Deadlines.RunSeconds = 0
		job.Deadlines.ManualSeconds = 10
		job.Manual = &Manual{ScenarioDocument: "scenarios/check.md", RequiredScenarios: []string{"one"}}
	}
	receipt, err := l.Admit(encode(t, job), digest, "key-"+id, id)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return receipt
}

func diagnosticSettle(t ginkgo.GinkgoTInterface, l *Ledger, r Receipt, cleanup string, protect bool) Receipt {
	t.Helper()
	var err error
	r, err = reserveScheduling(l, r, nil)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
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
	if protect {
		result.ProtectedEvidence = []ProtectedEvidence{{Path: "workspace", Reason: "cleanup_incomplete", RecoveryReference: "job:" + r.JobID}}
	}
	r, err = l.SettleLocal(r.JobID, r.JobRevision, result)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return r
}

func diagnosticSettleFailedDeadline(t ginkgo.GinkgoTInterface, l *Ledger, r Receipt) Receipt {
	t.Helper()
	var err error
	r, err = reserveScheduling(l, r, nil)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
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
	result := Result{
		Schema: "haepari.result/v1", JobID: r.JobID, ResourceID: r.ResourceID, ResourceRevision: r.ResourceConfigRevision, LeaseGeneration: r.LeaseGeneration,
		JobState: "finished", ResourceAvailability: "quarantined", WorkloadOutcome: "pass", EvidenceCompleteness: "complete", CleanupOutcome: "failed",
		FinalVerdict: Incomplete, Reason: "cleanup_deadline", Artifacts: []ResultArtifact{{Path: "results/build.json", SizeBytes: 1, SHA256: digest}},
		Observations: &ResultObservations{Source: "isolated-scheduling-observation"},
	}
	r, err = l.SettleLocal(r.JobID, r.JobRevision, result)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return r
}

func diagnosticCancelledClear(t ginkgo.GinkgoTInterface, l *Ledger, r Receipt, verifyAt time.Time) Receipt {
	t.Helper()
	reserved, err := reserveScheduling(l, r, nil)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "reserve")
	id, err := l.LocalIdentity(reserved.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "identity")
	startedAt := verifyAt.Add(-time.Minute)
	prepare := LocalPhase{Schema: "haepari.phase-receipt/v1", Identity: id, Phase: "prepare", BudgetSeconds: 10, StartedAt: startedAt, Deadline: startedAt.Add(10 * time.Second), ObservedAt: startedAt}
	started, err := l.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: reserved.JobRevision, Record: prepare, MutationKey: "prepare-intent-" + r.JobID})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "prepare intent")
	ack, err := l.Cancel(CancelRequest{JobID: r.JobID, ExpectedRevision: started.JobRevision, MutationKey: "cancel-" + r.JobID})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "cancel")
	prepare.Completed, prepare.Outcome, prepare.ObservedAt = true, "failed", startedAt.Add(time.Second)
	ended, err := l.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: ack.Receipt.JobRevision, Record: prepare, MutationKey: "prepare-end-" + r.JobID})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "prepare end")
	stopped, err := l.RecordLocalPreparationStop(LocalPreparationStopRequest{Identity: id, ExpectedRevision: ended.JobRevision, Reason: "prepare_failed", NoOwnedEffect: true, MutationKey: "stop-" + r.JobID})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "preparation stop")
	verify := LocalPhase{Schema: "haepari.phase-receipt/v1", Identity: id, Phase: "verify", StartedAt: verifyAt, ObservedAt: verifyAt}
	recorded, err := l.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: stopped.JobRevision, Record: verify, MutationKey: "verify-intent-" + r.JobID})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "verify intent")
	verify.Completed, verify.Outcome = true, "complete"
	recorded, err = l.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: recorded.JobRevision, Record: verify, MutationKey: "verify-end-" + r.JobID})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "verify end")
	result := Result{Schema: "haepari.result/v1", JobID: r.JobID, ResourceID: r.ResourceID, ResourceRevision: reserved.ResourceConfigRevision, LeaseGeneration: reserved.LeaseGeneration, JobState: "finished", ResourceAvailability: "available", WorkloadOutcome: "cancelled", EvidenceCompleteness: "complete", CleanupOutcome: "not_needed", FinalVerdict: Cancelled, Reason: "cancel_before_start"}
	finished, err := l.SettleLocal(r.JobID, recorded.JobRevision, result)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "settle")
	return finished
}

func diagnosticRecoveryPair(result Result, protected []ProtectedEvidence, key, stage string, complete bool) (EvidenceRecoveryIntent, EvidenceRecoveryReceipt) {
	recorded := time.Unix(1_700_000_000, 0).UTC()
	intent := EvidenceRecoveryIntent{
		Schema: "haepari.evidence-recovery-intent/v1", ResourceID: result.ResourceID,
		SnapshotDigest: "sha256:" + digest, ResourceConfigRevision: 1, ResourceAvailabilityRevision: 1,
		MutationKey: key, ClientDigest: "sha256:" + digest, Action: "recollect", RecordedAt: recorded,
		Owner: platform.ProcessIdentity{BootID: "boot", PID: 1, StartedSeconds: 1},
		Jobs: []EvidenceRecoveryJob{{
			JobID: result.JobID, ResultDigest: RecoveryDigest(result),
			Identity:        LocalIdentity{JobID: result.JobID, ResourceID: result.ResourceID, ConfigRevision: result.ResourceRevision, LeaseGeneration: result.LeaseGeneration, Nonce: "nonce-1"},
			Root:            RetainedPath{Inode: 1, Directory: true},
			Protected:       protected,
			Outputs:         []string{"results/build.json"},
			CollectDeadline: recorded.Add(time.Hour), CleanupDeadline: recorded.Add(2 * time.Hour),
		}},
	}
	receipt := EvidenceRecoveryReceipt{
		Schema: "haepari.evidence-recovery-receipt/v1", MutationKey: key, Complete: complete,
		Observation: RecoveryEvidence{Source: "machine-observed", Observer: "fixture", ObservedAt: recorded, Mechanism: "recollect", Digest: "sha256:" + digest, Reference: "job:" + result.JobID},
		Jobs:        []EvidenceRecoveryOutcome{{JobID: result.JobID, ResultDigest: RecoveryDigest(result), Stage: stage}},
	}
	if stage == "resolved" || stage == "collected" {
		receipt.Jobs[0].ArtifactRoot = "recovery-" + key
		receipt.Jobs[0].Artifacts = []ResultArtifact{{Path: "results/build.json", SizeBytes: 1, SHA256: digest}}
	}
	return intent, receipt
}

func diagnosticInsertRecovery(db *sql.DB, intent EvidenceRecoveryIntent, receipt *EvidenceRecoveryReceipt) {
	request, err := json.Marshal(intent)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var receiptJSON any
	if receipt != nil {
		raw, err := json.Marshal(receipt)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		receiptJSON = raw
	}
	_, err = db.Exec(`INSERT INTO managed_evidence_recovery(mutation_key,resource_id,request_json,receipt_json) VALUES(?,?,?,?)`, intent.MutationKey, intent.ResourceID, request, receiptJSON)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

func diagnosticCommit(l *Ledger, doc config.Document) config.Document {
	accepted, err := l.CommitEffectiveConfig(doc, true)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return accepted
}

func diagnosticExec(db *sql.DB, query string, args ...any) {
	_, err := db.Exec(query, args...)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

func diagnosticEvent(db *sql.DB, jobID, kind, payload string) {
	result, err := db.Exec(`INSERT INTO managed_events(job_id,kind,payload_json) VALUES(?,?,?)`, jobID, kind, []byte(payload))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	cursor, err := result.LastInsertId()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = db.Exec(`INSERT INTO managed_event_owners(cursor,job_id) VALUES(?,?)`, cursor, jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

var _ = ginkgo.Describe("OperatorDiagnostic", ginkgo.Label("integration"), func() {
	ginkgo.It("bounds local, vm, and lock queues without dropping another resource total", func() {
		t := ginkgo.GinkgoT()
		var row DiagnosticQueueRow
		ok := applyQueueTotals(&row, "legacy_product_ceiling", DiagnosticCeilings{
			QueueSeconds: productPhaseCeiling, PrepareSeconds: productPhaseCeiling, RunSeconds: productPhaseCeiling,
			ManualSeconds: productPhaseCeiling, CollectSeconds: productPhaseCeiling, CleanupSeconds: productPhaseCeiling,
		}, 1, []int64{10, 10, 10, 10})
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(*row.BacklogSeconds).To(gomega.Equal(int64(40)))
		gomega.Expect(*row.ProspectiveCeilingSeconds).To(gomega.Equal(int64(4) * productPhaseCeiling))
		gomega.Expect(*row.TotalSeconds).To(gomega.Equal(int64(40) + int64(4)*productPhaseCeiling))
		gomega.Expect(row.Ceilings).NotTo(gomega.BeNil())
		added, addOK := addChecked(productPhaseCeiling, productPhaseCeiling)
		gomega.Expect(addOK).To(gomega.BeTrue())
		gomega.Expect(added).To(gomega.Equal(2 * productPhaseCeiling))
		sum, sumOK := sumChecked(math.MaxInt64, 1)
		gomega.Expect(sumOK).To(gomega.BeFalse())
		gomega.Expect(sum).To(gomega.BeZero())
		var overflow DiagnosticQueueRow
		gomega.Expect(applyQueueTotals(&overflow, "legacy_product_ceiling", DiagnosticCeilings{
			QueueSeconds: 1, PrepareSeconds: math.MaxInt64, RunSeconds: 1, ManualSeconds: 1, CollectSeconds: 1, CleanupSeconds: 1,
		}, 1, []int64{1})).To(gomega.BeFalse())
		gomega.Expect(overflow.Status).To(gomega.Equal("unknown"))
		gomega.Expect(overflow.Reason).To(gomega.Equal("sum_overflow"))
		gomega.Expect(overflow.Ceilings).To(gomega.BeNil())
		gomega.Expect(overflow.IncludedJobs).To(gomega.BeNil())
		gomega.Expect(overflow.BacklogSeconds).To(gomega.BeNil())
		gomega.Expect(overflow.ProspectiveCeilingSeconds).To(gomega.BeNil())
		gomega.Expect(overflow.TotalSeconds).To(gomega.BeNil())

		db, ledger := diagnosticOpen(t)
		defer db.Close()
		diagnosticCommit(ledger, config.Defaults())
		diagnosticRegister(t, ledger, "local-a", 2)
		diagnosticRegister(t, ledger, "local-b", 1)
		wide := v2Resource(phaseCeilings(100))
		wide.ID = "local-v2"
		gomega.Expect(ledger.RegisterResource(encode(t, wide))).To(gomega.Succeed())
		vmScheduling(t, ledger, "vm-a", 1, "started", false)
		gomega.Expect(ledger.RegisterResource([]byte(`{"schema":"haepari.resource/v1","id":"lock-a","kind":"lock"}`))).To(gomega.Succeed())
		diagnosticAdmit(t, ledger, "local-a", "queued-auto", false)
		diagnosticAdmit(t, ledger, "local-a", "queued-manual", true)
		diagnosticAdmit(t, ledger, "local-v2", "v2-auto", false)
		reserved := diagnosticAdmit(t, ledger, "local-b", "reserved-auto", false)
		reserved, err := reserveScheduling(ledger, reserved, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var state string
		var occupancy int
		gomega.Expect(db.QueryRow(`SELECT j.state,r.occupancy FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, reserved.JobID).Scan(&state, &occupancy)).To(gomega.Succeed())
		gomega.Expect(state).NotTo(gomega.Or(gomega.Equal("queued"), gomega.Equal("finished"), gomega.Equal("reconciling")))
		gomega.Expect(occupancy).To(gomega.Equal(1))

		observed := time.Unix(1_800_000_000, 0).UTC()
		page := diagnosticMust(ledger, diagnosticAt("queue", "", "", 20, observed, nil))
		gomega.Expect(page.Schema).To(gomega.Equal(diagnosticSchema))
		gomega.Expect(page.View).To(gomega.Equal("diagnostic"))
		gomega.Expect(page.Section).To(gomega.Equal("queue"))
		gomega.Expect(page.CurrentConfig.Status).To(gomega.Equal("present"))
		gomega.Expect(page.CurrentConfig.GlobalLimit).To(gomega.Equal(int64(1)))
		gomega.Expect(page.AnyUnknownOccupancy).To(gomega.BeTrue())
		raw, err := json.Marshal(page)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(raw)).To(gomega.ContainSubstring(`"resources":[`))
		gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring(`"jobs"`))
		gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring("event_retention_days"))
		rows := diagnosticByResource(page)
		local := rows["local-a"]
		gomega.Expect(local.Status).To(gomega.Equal("bounded"))
		gomega.Expect(*local.IncludedJobs).To(gomega.Equal(int64(2)))
		gomega.Expect(*local.BacklogSeconds).To(gomega.Equal(int64(80)))
		gomega.Expect(*local.ProspectiveCeilingSeconds).To(gomega.Equal(int64(4) * productPhaseCeiling))
		gomega.Expect(*local.TotalSeconds).To(gomega.Equal(*local.BacklogSeconds + *local.ProspectiveCeilingSeconds))
		gomega.Expect(local.CeilingSource).To(gomega.Equal("legacy_product_ceiling"))
		gomega.Expect(local.Ceilings.QueueSeconds).To(gomega.Equal(productPhaseCeiling))
		gomega.Expect(rows["local-b"].Status).To(gomega.Equal("unknown"))
		gomega.Expect(rows["local-b"].Reason).To(gomega.Equal("unknown_occupied_execution"))
		gomega.Expect(rows["local-b"].TotalSeconds).To(gomega.BeNil())
		v2 := rows["local-v2"]
		gomega.Expect(v2.Status).To(gomega.Equal("bounded"))
		gomega.Expect(v2.CeilingSource).To(gomega.Equal("lifecycle_budgets"))
		gomega.Expect(*v2.IncludedJobs).To(gomega.Equal(int64(1)))
		gomega.Expect(*v2.BacklogSeconds).To(gomega.Equal(int64(40)))
		gomega.Expect(*v2.ProspectiveCeilingSeconds).To(gomega.Equal(int64(400)))
		gomega.Expect(v2.Ceilings.QueueSeconds).To(gomega.Equal(int64(100)))
		gomega.Expect(*v2.TotalSeconds).To(gomega.Equal(int64(440)))
		idle := rows["vm-a"]
		gomega.Expect(idle.Kind).To(gomega.Equal("utm-macos"))
		gomega.Expect(idle.Status).To(gomega.Equal("bounded"))
		gomega.Expect(*idle.IncludedJobs).To(gomega.BeZero())
		gomega.Expect(*idle.BacklogSeconds).To(gomega.BeZero())
		gomega.Expect(rows["lock-a"].Status).To(gomega.Equal("not_applicable"))
		gomega.Expect(rows["lock-a"].Reason).To(gomega.Equal("lock_resource"))
		gomega.Expect(rows["lock-a"].Ceilings).To(gomega.BeNil())
		gomega.Expect(rows["lock-a"].IncludedJobs).To(gomega.BeNil())
		kept := *local.TotalSeconds

		alive := diagnosticMust(ledger, diagnosticAt("queue", "", "", 20, observed, func(id string) bool { return id == reserved.JobID }))
		aliveRows := diagnosticByResource(alive)
		gomega.Expect(alive.AnyUnknownOccupancy).To(gomega.BeFalse())
		gomega.Expect(aliveRows["local-b"].Status).To(gomega.Equal("bounded"))
		gomega.Expect(*aliveRows["local-b"].IncludedJobs).To(gomega.Equal(int64(1)))
		gomega.Expect(*aliveRows["local-a"].TotalSeconds).To(gomega.Equal(kept))

		diagnosticExec(db, `UPDATE managed_jobs SET state='reconciling' WHERE id=?`, reserved.JobID)
		reconciling := diagnosticByResource(diagnosticMust(ledger, diagnosticAt("queue", "", "", 20, observed, func(id string) bool { return id == reserved.JobID })))
		gomega.Expect(reconciling["local-b"].Status).To(gomega.Equal("unknown"))
		gomega.Expect(reconciling["local-b"].Reason).To(gomega.Equal("unknown_occupied_execution"))
		gomega.Expect(*reconciling["local-a"].TotalSeconds).To(gomega.Equal(kept))

		diagnosticExec(db, `UPDATE managed_vm_observations SET power='uncertain' WHERE resource_id='vm-a'`)
		uncertain := diagnosticByResource(diagnosticMust(ledger, diagnosticAt("queue", "", "", 20, observed, func(string) bool { return true })))
		gomega.Expect(uncertain["vm-a"].Status).To(gomega.Equal("unknown"))
		gomega.Expect(uncertain["vm-a"].Reason).To(gomega.Equal("unknown_occupied_execution"))
		gomega.Expect(*uncertain["local-a"].TotalSeconds).To(gomega.Equal(kept))

		diagnosticExec(db, `UPDATE managed_resources SET occupancy=1 WHERE id='lock-a'`)
		locked := diagnosticByResource(diagnosticMust(ledger, diagnosticAt("queue", "", "", 20, observed, func(string) bool { return true })))
		gomega.Expect(locked["lock-a"].Status).To(gomega.Equal("unknown"))
		gomega.Expect(locked["lock-a"].Reason).To(gomega.Equal("unknown_occupied_execution"))
		gomega.Expect(locked["lock-a"].TotalSeconds).To(gomega.BeNil())
		gomega.Expect(*locked["local-a"].TotalSeconds).To(gomega.Equal(kept))
	})

	ginkgo.It("keeps an absent or corrupt header and a closed database off the invalid request", func() {
		t := ginkgo.GinkgoT()
		db, ledger := diagnosticOpen(t)
		diagnosticRegister(t, ledger, "local-a", 1)
		observed := time.Unix(1_800_000_000, 0).UTC()
		absent := diagnosticMust(ledger, diagnosticAt("queue", "", "", 10, observed, func(string) bool { return false }))
		gomega.Expect(absent.CurrentConfig.Status).To(gomega.Equal("unknown"))
		gomega.Expect(absent.CurrentConfig.Reason).To(gomega.Equal("absent"))
		gomega.Expect(absent.CurrentConfig.Revision).To(gomega.BeZero())
		gomega.Expect(absent.Resources).To(gomega.HaveLen(1))
		diagnosticCommit(ledger, config.Defaults())
		diagnosticExec(db, `UPDATE managed_effective_config SET document_digest=? WHERE singleton=1`, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
		corrupt := diagnosticMust(ledger, diagnosticAt("queue", "", "", 10, observed, func(string) bool { return false }))
		gomega.Expect(corrupt.CurrentConfig.Status).To(gomega.Equal("unknown"))
		gomega.Expect(corrupt.CurrentConfig.Reason).To(gomega.Equal("corrupt"))
		gomega.Expect(corrupt.Resources).To(gomega.HaveLen(1))
		gomega.Expect(db.Close()).To(gomega.Succeed())
		_, err := ledger.OperatorDiagnostic(diagnosticAt("queue", "", "", 10, observed, nil))
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeFalse())
		gomega.Expect(errors.Is(err, ErrNotFound)).To(gomega.BeFalse())
	})

	ginkgo.It("keeps the historical pin, replay unit, windows, and a missing payload", func() {
		t := ginkgo.GinkgoT()
		admittedAt := time.Unix(1_700_000_000, 0).UTC()
		ended := admittedAt.Add(24 * time.Hour)
		within, ok := diagnosticEventWindow(admittedAt, 30, admittedAt.Add(10*24*time.Hour))
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(within).To(gomega.Equal("within"))
		warning, ok := diagnosticEventWindow(admittedAt, 30, admittedAt.Add(31*24*time.Hour))
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(warning).To(gomega.Equal("warning"))
		_, ok = diagnosticEventWindow(admittedAt, math.MaxInt64, admittedAt)
		gomega.Expect(ok).To(gomega.BeFalse())
		resultWithin, ok := diagnosticResultWindow(admittedAt, ended, admittedAt.Add(31*24*time.Hour), 90)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(resultWithin).To(gomega.Equal("within"))
		past, ok := diagnosticResultWindow(admittedAt, ended, ended.Add(91*24*time.Hour), 90)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(past).To(gomega.Equal("past"))
		_, ok = diagnosticResultWindow(ended, admittedAt, ended.Add(91*24*time.Hour), 90)
		gomega.Expect(ok).To(gomega.BeFalse())
		_, ok = diagnosticResultWindow(admittedAt, time.Time{}, admittedAt, 90)
		gomega.Expect(ok).To(gomega.BeFalse())
		_, ok = diagnosticResultWindow(admittedAt, ended, ended.Add(time.Hour), math.MaxInt64)
		gomega.Expect(ok).To(gomega.BeFalse())
		days := int64(math.MaxInt64 / 86400)
		_, ok = diagnosticDayBound(time.Unix(math.MaxInt64-days*86400+1, 0).UTC(), days)
		gomega.Expect(ok).To(gomega.BeFalse())
		_, ok = diagnosticDayBound(admittedAt, days)
		gomega.Expect(ok).To(gomega.BeFalse())
		_, ok = diagnosticDayBound(time.Unix(0, 0).UTC(), days)
		gomega.Expect(ok).To(gomega.BeTrue())

		db, ledger := diagnosticOpen(t)
		defer db.Close()
		doc := config.Defaults()
		accepted := diagnosticCommit(ledger, doc)
		diagnosticRegister(t, ledger, "local-a", 2)
		pin := diagnosticAdmit(t, ledger, "local-a", "pin-job", false)
		verifyAt := time.Now().UTC().Add(time.Minute)
		diagnosticCancelledClear(t, ledger, pin, verifyAt)
		var admittedNS int64
		gomega.Expect(db.QueryRow(`SELECT admitted_at_ns FROM managed_jobs WHERE id=?`, pin.JobID).Scan(&admittedNS)).To(gomega.Succeed())
		admitted := time.Unix(0, admittedNS).UTC()
		var verifyEvents int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='phase_observed'`, pin.JobID).Scan(&verifyEvents)).To(gomega.Succeed())
		gomega.Expect(verifyEvents).NotTo(gomega.BeZero())
		warningAt := admitted.Add(31 * 24 * time.Hour)
		pinned := diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 20, warningAt, nil)))[pin.JobID]
		gomega.Expect(pinned.PolicyClass).To(gomega.Equal("accepted"))
		gomega.Expect(*pinned.PolicyRevision).To(gomega.Equal(accepted.Revision))
		gomega.Expect(*pinned.EventRetentionDays).To(gomega.Equal(int64(30)))
		gomega.Expect(*pinned.ResultRetentionDays).To(gomega.Equal(int64(90)))
		gomega.Expect(pinned.RetentionClass).To(gomega.Equal("clear"))
		gomega.Expect(pinned.EventWindow).To(gomega.Equal("warning"))
		gomega.Expect(pinned.ResultWindow).To(gomega.Equal("within"))
		gomega.Expect(*pinned.PermanentReplayCount).To(gomega.BeZero())
		var admittedEvents int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='admitted'`, pin.JobID).Scan(&admittedEvents)).To(gomega.Succeed())
		gomega.Expect(admittedEvents).To(gomega.Equal(1))
		pastPage := diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 20, verifyAt.Add(91*24*time.Hour), nil)))[pin.JobID]
		gomega.Expect(pastPage.EventWindow).To(gomega.Equal("warning"))
		gomega.Expect(pastPage.ResultWindow).To(gomega.Equal("past"))

		doc.Revision = accepted.Revision + 1
		doc.Limits.EventRetentionDays = math.MaxInt64
		doc.Limits.ResultRetentionDays = math.MaxInt64
		later := diagnosticCommit(ledger, doc)
		again := diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 20, warningAt, nil)))[pin.JobID]
		gomega.Expect(*again.PolicyRevision).To(gomega.Equal(accepted.Revision))
		gomega.Expect(*again.EventRetentionDays).To(gomega.Equal(int64(30)))
		gomega.Expect(*again.ResultRetentionDays).To(gomega.Equal(int64(90)))
		overflowJob := diagnosticAdmit(t, ledger, "local-a", "overflow-job", false)
		diagnosticCancelledClear(t, ledger, overflowJob, time.Now().UTC().Add(time.Minute))
		overflow := diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 20, warningAt, nil)))[overflowJob.JobID]
		gomega.Expect(overflow.PolicyClass).To(gomega.Equal("accepted"))
		gomega.Expect(*overflow.PolicyRevision).To(gomega.Equal(later.Revision))
		gomega.Expect(*overflow.EventRetentionDays).To(gomega.Equal(int64(math.MaxInt64)))
		gomega.Expect(*overflow.ResultRetentionDays).To(gomega.Equal(int64(math.MaxInt64)))
		gomega.Expect(overflow.RetentionClass).To(gomega.Equal("clear"))
		gomega.Expect(overflow.EventWindow).To(gomega.BeEmpty())
		gomega.Expect(overflow.ResultWindow).To(gomega.BeEmpty())

		fresh := diagnosticAdmit(t, ledger, "local-a", "fresh-job", false)
		freshRow := diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 20, warningAt, nil)))[fresh.JobID]
		gomega.Expect(freshRow.RetentionClass).To(gomega.Equal("active"))
		gomega.Expect(*freshRow.PermanentReplayCount).To(gomega.BeZero())
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='admitted'`, fresh.JobID).Scan(&admittedEvents)).To(gomega.Succeed())
		gomega.Expect(admittedEvents).To(gomega.Equal(1))
		for _, kind := range []string{"recovery_intent", "phase_reconciled", "vm_prepared", "vm_retained", "vm_transport_binding", "vm_transport_admission", "vm_retire_intent", "power_cessation", "local_workspace"} {
			diagnosticEvent(db, fresh.JobID, kind, "{}")
		}
		counted := diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 20, warningAt, nil)))[fresh.JobID]
		gomega.Expect(*counted.PermanentReplayCount).To(gomega.Equal(int64(9)))
		gomega.Expect(counted.RetentionClass).To(gomega.Equal("active"))
		_, err := reserveScheduling(ledger, fresh, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		occupied := diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 20, warningAt, nil)))
		gomega.Expect(occupied[pin.JobID].RetentionClass).To(gomega.Equal("occupied"))
		gomega.Expect(occupied[fresh.JobID].RetentionClass).To(gomega.Equal("active"))
		gomega.Expect(occupied[pin.JobID].EventWindow).To(gomega.BeEmpty())
		diagnosticExec(db, `UPDATE managed_job_bounds SET log_bytes=log_bytes-1 WHERE job_id=?`, fresh.JobID)
		unknown := diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 20, warningAt, nil)))[fresh.JobID]
		gomega.Expect(unknown.PolicyClass).To(gomega.Equal("unknown"))
		gomega.Expect(unknown.PolicyRevision).To(gomega.BeNil())
		gomega.Expect(unknown.EventRetentionDays).To(gomega.BeNil())
		gomega.Expect(unknown.ResultRetentionDays).To(gomega.BeNil())
		gomega.Expect(*unknown.PermanentReplayCount).To(gomega.Equal(int64(9)))
		gomega.Expect(unknown.RetentionClass).To(gomega.Equal("active"))

		missingJob := diagnosticAdmit(t, ledger, "local-a", "missing-payload", false)
		var cursor, floor int64
		gomega.Expect(db.QueryRow(`SELECT cursor FROM managed_events WHERE job_id=? ORDER BY cursor DESC LIMIT 1`, missingJob.JobID).Scan(&cursor)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT purged_through FROM managed_event_progress WHERE job_id=?`, missingJob.JobID).Scan(&floor)).To(gomega.Succeed())
		gomega.Expect(cursor).To(gomega.BeNumerically(">", floor))
		diagnosticExec(db, `DELETE FROM managed_events WHERE cursor=?`, cursor)
		var owner, missing int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_event_owners WHERE cursor=? AND job_id=?`, cursor, missingJob.JobID).Scan(&owner)).To(gomega.Succeed())
		gomega.Expect(owner).To(gomega.Equal(1))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_event_owners o LEFT JOIN managed_events e ON e.cursor=o.cursor WHERE o.job_id=? AND o.cursor>? AND e.cursor IS NULL`, missingJob.JobID, floor).Scan(&missing)).To(gomega.Succeed())
		gomega.Expect(missing).To(gomega.BeNumerically(">", 0))
		lost := diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 20, warningAt, nil)))
		gomega.Expect(lost[missingJob.JobID].RetentionClass).To(gomega.Equal("unknown"))
		gomega.Expect(lost[missingJob.JobID].PermanentReplayCount).To(gomega.BeNil())
		gomega.Expect(lost[missingJob.JobID].EventWindow).To(gomega.BeEmpty())
		gomega.Expect(*lost[fresh.JobID].PermanentReplayCount).To(gomega.Equal(int64(9)))

		legacyDB, legacy := diagnosticOpen(t)
		defer legacyDB.Close()
		diagnosticRegister(t, legacy, "local-a", 1)
		advisoryJob := diagnosticAdmit(t, legacy, "local-a", "advisory-job", false)
		diagnosticSettle(t, legacy, advisoryJob, "verified", false)
		advisory := diagnosticByJob(diagnosticMust(legacy, diagnosticAt("retention", "", "", 10, warningAt, nil)))[advisoryJob.JobID]
		gomega.Expect(advisory.PolicyClass).To(gomega.Equal("advisory_default"))
		gomega.Expect(advisory.PolicyRevision).To(gomega.BeNil())
		gomega.Expect(*advisory.EventRetentionDays).To(gomega.Equal(int64(30)))
		gomega.Expect(*advisory.ResultRetentionDays).To(gomega.Equal(int64(90)))
		gomega.Expect(advisory.RetentionClass).To(gomega.Equal("clear"))
		gomega.Expect(advisory.EventWindow).To(gomega.BeEmpty())
		gomega.Expect(advisory.ResultWindow).To(gomega.BeEmpty())
		gomega.Expect(*advisory.PermanentReplayCount).To(gomega.BeZero())
		gomega.Expect(legacy.RetainAdmissionTombstone(advisoryJob.JobID)).To(gomega.Succeed())
		unavailable := diagnosticByJob(diagnosticMust(legacy, diagnosticAt("retention", "", "", 10, warningAt, nil)))[advisoryJob.JobID]
		gomega.Expect(unavailable.RetentionClass).To(gomega.Equal("unavailable"))
		gomega.Expect(unavailable.EventWindow).To(gomega.BeEmpty())
	})

	ginkgo.It("releases protected evidence only for an exact resolved receipt", func() {
		t := ginkgo.GinkgoT()
		db, ledger := diagnosticOpen(t)
		defer db.Close()
		diagnosticRegister(t, ledger, "local-a", 1)
		job := diagnosticAdmit(t, ledger, "local-a", "protected-job", false)
		diagnosticSettle(t, ledger, job, "failed", true)
		observed := time.Unix(1_800_000_000, 0).UTC()
		gomega.Expect(diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 10, observed, nil)))[job.JobID].RetentionClass).To(gomega.Equal("protected"))
		var before []byte
		var availability string
		gomega.Expect(db.QueryRow(`SELECT result_json FROM managed_jobs WHERE id=?`, job.JobID).Scan(&before)).To(gomega.Succeed())
		diagnosticExec(db, `UPDATE managed_resources SET availability='available' WHERE id='local-a'`)
		var after []byte
		gomega.Expect(db.QueryRow(`SELECT j.result_json,r.availability FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, job.JobID).Scan(&after, &availability)).To(gomega.Succeed())
		gomega.Expect(after).To(gomega.Equal(before))
		gomega.Expect(availability).To(gomega.Equal("available"))
		stored, err := DecodeResult(after)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(stored.ResourceAvailability).To(gomega.Equal("quarantined"))
		gomega.Expect(stored.ProtectedEvidence).NotTo(gomega.BeEmpty())
		gomega.Expect(diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 10, observed, nil)))[job.JobID].RetentionClass).To(gomega.Equal("protected"))

		partialIntent, partialReceipt := diagnosticRecoveryPair(stored, stored.ProtectedEvidence, "recover-partial", "pending", false)
		diagnosticInsertRecovery(db, partialIntent, &partialReceipt)
		gomega.Expect(diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 10, observed, nil)))[job.JobID].RetentionClass).To(gomega.Equal("protected"))
		diagnosticExec(db, `DELETE FROM managed_evidence_recovery WHERE mutation_key='recover-partial'`)

		mismatched := stored.ProtectedEvidence
		mismatched = []ProtectedEvidence{{Path: "other-workspace", Reason: "cleanup_incomplete", RecoveryReference: "job:" + job.JobID}}
		mismatchIntent, mismatchReceipt := diagnosticRecoveryPair(stored, mismatched, "recover-mismatch", "resolved", true)
		diagnosticInsertRecovery(db, mismatchIntent, &mismatchReceipt)
		gomega.Expect(diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 10, observed, nil)))[job.JobID].RetentionClass).To(gomega.Equal("protected"))
		diagnosticExec(db, `DELETE FROM managed_evidence_recovery WHERE mutation_key='recover-mismatch'`)

		exactIntent, exactReceipt := diagnosticRecoveryPair(stored, stored.ProtectedEvidence, "recover-exact", "resolved", true)
		diagnosticInsertRecovery(db, exactIntent, &exactReceipt)
		gomega.Expect(diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 10, observed, nil)))[job.JobID].RetentionClass).To(gomega.Equal("clear"))
		var unchanged []byte
		gomega.Expect(db.QueryRow(`SELECT result_json FROM managed_jobs WHERE id=?`, job.JobID).Scan(&unchanged)).To(gomega.Succeed())
		gomega.Expect(unchanged).To(gomega.Equal(before))

		nullIntent, _ := diagnosticRecoveryPair(stored, stored.ProtectedEvidence, "recover-null", "pending", false)
		diagnosticInsertRecovery(db, nullIntent, nil)
		gomega.Expect(diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 10, observed, nil)))[job.JobID].RetentionClass).To(gomega.Equal("protected"))
		request, err := json.Marshal(exactIntent)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		diagnosticExec(db, `INSERT INTO managed_evidence_recovery(mutation_key,resource_id,request_json,receipt_json) VALUES(?,?,?,?)`, "recover-corrupt", "local-a", request, []byte("{"))
		gomega.Expect(diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 10, observed, nil)))[job.JobID].RetentionClass).To(gomega.Equal("unknown"))
		gomega.Expect(db.QueryRow(`SELECT result_json FROM managed_jobs WHERE id=?`, job.JobID).Scan(&unchanged)).To(gomega.Succeed())
		gomega.Expect(unchanged).To(gomega.Equal(before))
	})

	ginkgo.It("keeps a recovery intent protected after a later phase_reconciled", func() {
		t := ginkgo.GinkgoT()
		db, ledger := diagnosticOpen(t)
		defer db.Close()
		diagnosticRegister(t, ledger, "local-a", 1)
		job := diagnosticAdmit(t, ledger, "local-a", "intent-job", false)
		diagnosticSettle(t, ledger, job, "verified", false)
		observed := time.Unix(1_800_000_000, 0).UTC()
		before := diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 10, observed, nil)))[job.JobID]
		gomega.Expect(before.RetentionClass).To(gomega.Equal("clear"))
		gomega.Expect(*before.PermanentReplayCount).To(gomega.BeZero())
		payload, err := json.Marshal(JobRecoveryRequest{
			Identity:    LocalIdentity{JobID: job.JobID, ResourceID: "local-a", ConfigRevision: 1, LeaseGeneration: 1, Nonce: "nonce-1"},
			MutationKey: "intent-1",
			Evidence:    RecoveryEvidence{Source: "machine-observed", Observer: "fixture", ObservedAt: observed, Mechanism: "inspect", Digest: "sha256:" + digest, Reference: "job:" + job.JobID},
		})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		diagnosticEvent(db, job.JobID, "recovery_intent", string(payload))
		held := diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 10, observed, nil)))[job.JobID]
		gomega.Expect(held.RetentionClass).To(gomega.Equal("protected"))
		gomega.Expect(*held.PermanentReplayCount).To(gomega.Equal(int64(1)))
		diagnosticEvent(db, job.JobID, "phase_reconciled", "{}")
		var kinds int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind IN ('recovery_intent','phase_reconciled')`, job.JobID).Scan(&kinds)).To(gomega.Succeed())
		gomega.Expect(kinds).To(gomega.Equal(2))
		after := diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 10, observed, nil)))[job.JobID]
		gomega.Expect(after.RetentionClass).To(gomega.Equal("protected"))
		gomega.Expect(*after.PermanentReplayCount).To(gomega.Equal(int64(2)))
	})

	ginkgo.It("measures the whole diagnostic body and resumes the withheld row", func() {
		t := ginkgo.GinkgoT()
		db, ledger := diagnosticOpen(t)
		defer db.Close()
		gomega.Expect(ledger.CommitEffectiveConfig(config.Defaults(), true)).Error().NotTo(gomega.HaveOccurred())
		for _, id := range []string{"resource-a", "resource-b", "resource-c"} {
			diagnosticRegister(t, ledger, id, 1)
		}
		observed := time.Unix(1_800_000_000, 0).UTC()
		one, err := ledger.readOperatorDiagnostic(diagnosticAt("queue", "", "", 1, observed, func(string) bool { return false }), 1<<20)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(one.Resources).To(gomega.HaveLen(1))
		gomega.Expect(one.Resources[0].ResourceID).To(gomega.Equal("resource-a"))
		gomega.Expect(one.Page.More).To(gomega.BeTrue())
		gomega.Expect(one.Page.NextCursor).NotTo(gomega.BeEmpty())
		gomega.Expect(one.CurrentConfig.Status).To(gomega.Equal("present"))
		oneRaw, err := json.Marshal(one)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// The limit 20 body is the same one-row page with a two-digit limit.
		cap := len(oneRaw) + len(",\"limit\":20") - len(",\"limit\":1")
		fitted, err := ledger.readOperatorDiagnostic(diagnosticAt("queue", "", "", 20, observed, func(string) bool { return false }), cap)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fittedRaw, err := json.Marshal(fitted)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(len(fittedRaw)).To(gomega.BeNumerically("<=", cap))
		gomega.Expect(fitted.Resources).To(gomega.HaveLen(1))
		gomega.Expect(fitted.Resources[0].ResourceID).To(gomega.Equal("resource-a"))
		gomega.Expect(fitted.Page.More).To(gomega.BeTrue())
		resumed, err := ledger.readOperatorDiagnostic(diagnosticAt("queue", "", fitted.Page.NextCursor, 20, observed, func(string) bool { return false }), cap)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		resumedRaw, err := json.Marshal(resumed)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(len(resumedRaw)).To(gomega.BeNumerically("<=", cap))
		gomega.Expect(resumed.Resources).NotTo(gomega.BeEmpty())
		gomega.Expect(resumed.Resources[0].ResourceID).To(gomega.Equal("resource-b"))
		_, err = ledger.readOperatorDiagnostic(diagnosticAt("queue", "", "", 20, observed, func(string) bool { return false }), cap-1)
		gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
	})

	ginkgo.It("rejects a bad diagnostic cursor and serves an upper bound below the current max", func() {
		t := ginkgo.GinkgoT()
		db, ledger := diagnosticOpen(t)
		defer db.Close()
		for _, id := range []string{"resource-a", "resource-b", "resource-c"} {
			diagnosticRegister(t, ledger, id, 1)
		}
		diagnosticAdmit(t, ledger, "resource-a", "job-a", false)
		diagnosticAdmit(t, ledger, "resource-b", "job-b", false)
		observed := time.Unix(1_800_000_000, 0).UTC()
		installation := diagnosticInstallation(db)
		gomega.Expect(installation).To(gomega.Equal("diag-install"))
		below, err := ledger.OperatorDiagnostic(diagnosticAt("queue", "", diagnosticQueueCursor(installation, "", "resource-a", "resource-b"), 20, observed, nil))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(below.Resources).To(gomega.HaveLen(1))
		gomega.Expect(below.Resources[0].ResourceID).To(gomega.Equal("resource-b"))
		empty, err := ledger.OperatorDiagnostic(diagnosticAt("queue", "", diagnosticQueueCursor(installation, "", "resource-a", "resource-a"), 20, observed, nil))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(empty.Resources).To(gomega.BeEmpty())
		filtered, err := ledger.OperatorDiagnostic(diagnosticAt("queue", "resource-a", diagnosticQueueCursor(installation, "resource-a", "resource-a", "resource-a"), 20, observed, nil))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(filtered.Resources).To(gomega.BeEmpty())
		for _, cursor := range []string{
			diagnosticQueueCursor(installation, "", "bad id", "resource-c"),
			diagnosticQueueCursor(installation, "resource-a", "resource-a", "resource-b"),
			encodePageCursor(pageCursor{Kind: "jobs", Installation: installation, LastID: "resource-a", UpperID: "resource-c"}),
			encodePageCursor(pageCursor{Kind: diagnosticQueueKind, Installation: "other", LastID: "resource-a", UpperID: "resource-c"}),
			encodePageCursor(pageCursor{Kind: diagnosticQueueKind, Installation: installation, State: "queued", LastID: "resource-a", UpperID: "resource-c"}),
			encodePageCursor(pageCursor{Kind: diagnosticQueueKind, Installation: installation, Resource: "resource-b", LastID: "resource-a", UpperID: "resource-c"}),
			encodePageCursor(pageCursor{Kind: diagnosticQueueKind, Installation: installation, Last: 1, LastID: "resource-a", UpperID: "resource-c"}),
		} {
			_, err = ledger.OperatorDiagnostic(diagnosticAt("queue", "", cursor, 10, observed, nil))
			if cursor == diagnosticQueueCursor(installation, "resource-a", "resource-a", "resource-b") {
				_, err = ledger.OperatorDiagnostic(diagnosticAt("queue", "resource-a", cursor, 10, observed, nil))
			}
			gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		}
		var first, last int64
		gomega.Expect(db.QueryRow(`SELECT MIN(sequence),MAX(sequence) FROM managed_admissions`).Scan(&first, &last)).To(gomega.Succeed())
		gomega.Expect(last).To(gomega.BeNumerically(">", first))
		bounded, err := ledger.OperatorDiagnostic(diagnosticAt("retention", "", encodePageCursor(pageCursor{Kind: diagnosticRetentionKind, Installation: installation, Last: first, Upper: first}), 20, observed, nil))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(bounded.Jobs).To(gomega.BeEmpty())
		_, err = ledger.OperatorDiagnostic(diagnosticAt("retention", "", encodePageCursor(pageCursor{Kind: diagnosticRetentionKind, Installation: installation, Last: 0, Upper: last}), 10, observed, nil))
		gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		_, err = ledger.OperatorDiagnostic(diagnosticAt("retention", "", encodePageCursor(pageCursor{Kind: diagnosticRetentionKind, Installation: installation, Last: first, Upper: last, LastID: "resource-a"}), 10, observed, nil))
		gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		_, err = ledger.OperatorDiagnostic(diagnosticAt("queue", "missing-resource", "", 10, observed, nil))
		gomega.Expect(errors.Is(err, ErrNotFound)).To(gomega.BeTrue())
		for _, query := range []DiagnosticQuery{
			diagnosticAt("queue", "bad id", "", 10, observed, nil),
			diagnosticAt("queue", "", "", 0, observed, nil),
			diagnosticAt("queue", "", "", -1, observed, nil),
			diagnosticAt("queue", "", "", 21, observed, nil),
			diagnosticAt("both", "", "", 10, observed, nil),
		} {
			_, err = ledger.OperatorDiagnostic(query)
			gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		}
	})

	ginkgo.It("holds a zero-list failed cleanup", func() {
		t := ginkgo.GinkgoT()
		db, ledger := diagnosticOpen(t)
		defer db.Close()
		diagnosticRegister(t, ledger, "local-a", 1)
		failed := diagnosticAdmit(t, ledger, "local-a", "failed-empty", false)
		diagnosticSettle(t, ledger, failed, "failed", false)
		var raw []byte
		gomega.Expect(db.QueryRow(`SELECT result_json FROM managed_jobs WHERE id=?`, failed.JobID).Scan(&raw)).To(gomega.Succeed())
		stored, err := DecodeResult(raw)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(stored.CleanupOutcome).To(gomega.Equal(CleanupOutcome("failed")))
		gomega.Expect(stored.ProtectedEvidence).To(gomega.BeEmpty())
		gomega.Expect(stored.Reason).NotTo(gomega.Equal("cleanup_deadline"))
		diagnosticExec(db, `UPDATE managed_resources SET availability='available',availability_reason='' WHERE id='local-a'`)
		observed := time.Unix(1_800_000_000, 0).UTC()
		row := diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 10, observed, nil)))[failed.JobID]
		gomega.Expect(row.RetentionClass).To(gomega.Equal("protected"))
		var unchanged []byte
		gomega.Expect(db.QueryRow(`SELECT result_json FROM managed_jobs WHERE id=?`, failed.JobID).Scan(&unchanged)).To(gomega.Succeed())
		gomega.Expect(unchanged).To(gomega.Equal(raw))
	})

	ginkgo.It("resolves one settled recovery after purge", func() {
		t := ginkgo.GinkgoT()
		db, ledger := diagnosticOpen(t)
		defer db.Close()
		doc := config.Defaults()
		accepted := diagnosticCommit(ledger, doc)
		diagnosticRegister(t, ledger, "local-a", 1)
		job := validJob()
		job.ResourceID = "local-a"
		objectID := "sha256:" + digest
		receipt, err := ledger.AdmitSealedInventory(encode(t, job), digest, objectID, []byte(`{"sealed":true}`), "key-settled-recovery", "settled-recovery")
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "admit")
		var admittedNS int64
		gomega.Expect(db.QueryRow(`SELECT admitted_at_ns FROM managed_jobs WHERE id=?`, receipt.JobID).Scan(&admittedNS)).To(gomega.Succeed())
		admitted := time.Unix(0, admittedNS).UTC()
		verifyAt := admitted.Add(time.Hour)
		observed := admitted.Add(31 * 24 * time.Hour)
		receipt, err = reserveScheduling(ledger, receipt, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "reserve")
		gomega.Expect(ledger.MarkInterrupted()).To(gomega.Succeed(), "interrupt")
		snap, err := ledger.RecoverySnapshot(receipt.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "snapshot")
		gomega.Expect(snap.Receipt.JobState).To(gomega.Equal("reconciling"))
		gomega.Expect(snap.Identity.Nonce).NotTo(gomega.BeEmpty())
		gomega.Expect(snap.Identity.LeaseGeneration).To(gomega.BeNumerically(">", 0))
		evidence := RecoveryEvidence{Source: "machine-observed", Observer: "fixture", ObservedAt: observed, Mechanism: "restart-observed", Digest: "sha256:" + digest, Reference: "job:" + receipt.JobID}
		gomega.Expect(evidence.Mechanism).NotTo(gomega.Equal("guest-completed-cleanup-observed"))
		request := JobRecoveryRequest{
			Identity: snap.Identity, SnapshotDigest: RecoveryDigest(snap), MutationKey: "settled-recovery-intent",
			Action: "no-start", NextState: "settling", NoOwnedEffect: true, Automatic: true,
			Outcome: &LocalOutcome{WorkloadOutcome: "inconclusive"}, Evidence: evidence,
		}
		applied, err := ledger.BeginJobRecovery(request)
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "begin recovery")
		gomega.Expect(applied.JobState).To(gomega.Equal("settling"))
		result := Result{
			Schema: "haepari.result/v1", JobID: applied.JobID, ResourceID: applied.ResourceID,
			ResourceRevision: applied.ResourceConfigRevision, LeaseGeneration: applied.LeaseGeneration,
			JobState: "finished", ResourceAvailability: applied.ResourceAvailability,
			WorkloadOutcome: "inconclusive", EvidenceCompleteness: "complete", CleanupOutcome: "verified",
			FinalVerdict: Incomplete, Artifacts: []ResultArtifact{{Path: "results/build.json", SizeBytes: 1, SHA256: digest}},
		}
		settled, err := ledger.SettleLocal(applied.JobID, applied.JobRevision, result)
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "settle")
		phase, err := json.Marshal(LocalPhase{
			Schema: "haepari.phase-receipt/v1", Phase: "verify",
			Identity:  LocalIdentity{JobID: settled.JobID, ResourceID: settled.ResourceID, ConfigRevision: settled.ResourceConfigRevision, LeaseGeneration: settled.LeaseGeneration, Nonce: snap.Identity.Nonce},
			StartedAt: verifyAt, ObservedAt: verifyAt, Completed: true, Outcome: "complete",
		})
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "verify evidence")
		diagnosticEvent(db, settled.JobID, "phase_observed", string(phase))
		resolved := diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 10, observed, nil)))[settled.JobID]
		gomega.Expect(resolved.RetentionClass).To(gomega.Equal("clear"))
		gomega.Expect(*resolved.PermanentReplayCount).To(gomega.BeNumerically(">", 0))
		gomega.Expect(resolved.PolicyClass).To(gomega.Equal("accepted"))
		gomega.Expect(*resolved.PolicyRevision).To(gomega.Equal(accepted.Revision))
		gomega.Expect(*resolved.EventRetentionDays).To(gomega.Equal(int64(30)))
		gomega.Expect(*resolved.ResultRetentionDays).To(gomega.Equal(int64(90)))
		gomega.Expect(resolved.EventWindow).NotTo(gomega.BeEmpty())
		gomega.Expect(resolved.ResultWindow).NotTo(gomega.BeEmpty())
		doc.Revision = accepted.Revision + 1
		doc.Limits.EventRetentionDays = 7
		doc.Limits.ResultRetentionDays = 14
		diagnosticCommit(ledger, doc)
		pinned := diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 10, observed, nil)))[settled.JobID]
		gomega.Expect(pinned.RetentionClass).To(gomega.Equal("clear"))
		gomega.Expect(*pinned.PolicyRevision).To(gomega.Equal(accepted.Revision))
		gomega.Expect(*pinned.EventRetentionDays).To(gomega.Equal(int64(30)))
		gomega.Expect(*pinned.ResultRetentionDays).To(gomega.Equal(int64(90)))
		gomega.Expect(pinned.EventWindow).NotTo(gomega.BeEmpty())
		gomega.Expect(pinned.ResultWindow).NotTo(gomega.BeEmpty())

		bulk, err := ledger.BulkSnapshot(settled.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "bulk")
		purge := PurgeIntent{
			Schema: "haepari.purge-intent/v1", JobID: settled.JobID, ExpectedRevision: bulk.Receipt.JobRevision,
			SnapshotDigest: RecoveryDigest(bulk), RecordedAt: observed,
			Object:      &RetainedPath{Path: objectID, Inode: 1, Directory: true},
			ObjectPaths: []RetainedPath{{Path: "manifest.json", Inode: 2}, {Path: "files", Inode: 3, Directory: true}},
		}
		gomega.Expect(ledger.BeginPurge(purge)).To(gomega.Succeed(), "begin purge")
		_, err = ledger.CompletePurge(purge)
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "complete purge")
		var finished, intents, resultLen int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='finished'`, settled.JobID).Scan(&finished)).To(gomega.Succeed())
		gomega.Expect(finished).To(gomega.BeZero())
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='recovery_intent'`, settled.JobID).Scan(&intents)).To(gomega.Succeed())
		gomega.Expect(intents).To(gomega.Equal(1))
		gomega.Expect(db.QueryRow(`SELECT length(result_json) FROM managed_jobs WHERE id=?`, settled.JobID).Scan(&resultLen)).To(gomega.Succeed())
		gomega.Expect(resultLen).To(gomega.BeNumerically(">", 0))
		var settlementRaw []byte
		gomega.Expect(db.QueryRow(`SELECT receipt_json FROM managed_mutations WHERE mutation_key=?`, "settle-"+settled.JobID).Scan(&settlementRaw)).To(gomega.Succeed())
		var settlement Receipt
		gomega.Expect(json.Unmarshal(settlementRaw, &settlement)).To(gomega.Succeed())
		gomega.Expect(settlement.EventCursor).To(gomega.BeNumerically(">", 0))
		var settledEvent int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE cursor=?`, settlement.EventCursor).Scan(&settledEvent)).To(gomega.Succeed())
		gomega.Expect(settledEvent).To(gomega.BeZero())
		var owner string
		gomega.Expect(db.QueryRow(`SELECT job_id FROM managed_event_owners WHERE cursor=?`, settlement.EventCursor).Scan(&owner)).To(gomega.Succeed())
		gomega.Expect(owner).To(gomega.Equal(settled.JobID))
		purged := diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 10, observed, nil)))[settled.JobID]
		gomega.Expect(purged.RetentionClass).To(gomega.Equal("unavailable"))
		gomega.Expect(*purged.PermanentReplayCount).To(gomega.BeNumerically(">", 0))
		gomega.Expect(purged.EventWindow).To(gomega.BeEmpty())
		gomega.Expect(purged.ResultWindow).To(gomega.BeEmpty())

		malformed := diagnosticAdmit(t, ledger, "local-a", "malformed-intent", false)
		diagnosticSettle(t, ledger, malformed, "verified", false)
		diagnosticEvent(db, malformed.JobID, "recovery_intent", "{")
		gomega.Expect(diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 10, observed, nil)))[malformed.JobID].RetentionClass).To(gomega.Equal("unknown"))
		foreign := diagnosticAdmit(t, ledger, "local-a", "foreign-intent", false)
		diagnosticSettle(t, ledger, foreign, "verified", false)
		foreignPayload, err := json.Marshal(JobRecoveryRequest{
			Identity:    LocalIdentity{JobID: "other-job", ResourceID: "local-a", ConfigRevision: 1, LeaseGeneration: 1, Nonce: "nonce-1"},
			MutationKey: "foreign-intent",
			Evidence:    RecoveryEvidence{Source: "machine-observed", Observer: "fixture", ObservedAt: observed, Mechanism: "inspect", Digest: "sha256:" + digest, Reference: "job:other-job"},
		})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		diagnosticEvent(db, foreign.JobID, "recovery_intent", string(foreignPayload))
		gomega.Expect(diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 10, observed, nil)))[foreign.JobID].RetentionClass).To(gomega.Equal("unknown"))
		partial := diagnosticAdmit(t, ledger, "local-a", "partial-intent", false)
		diagnosticSettle(t, ledger, partial, "verified", false)
		partialPayload, err := json.Marshal(JobRecoveryRequest{
			Identity:    LocalIdentity{JobID: partial.JobID, ResourceID: "local-a", ConfigRevision: partial.ResourceConfigRevision, LeaseGeneration: partial.LeaseGeneration + 9, Nonce: "nonce-1"},
			MutationKey: "partial-intent",
			Evidence:    RecoveryEvidence{Source: "machine-observed", Observer: "fixture", ObservedAt: observed, Mechanism: "inspect", Digest: "sha256:" + digest, Reference: "job:" + partial.JobID},
		})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		diagnosticEvent(db, partial.JobID, "recovery_intent", string(partialPayload))
		diagnosticEvent(db, partial.JobID, "phase_reconciled", "{}")
		gomega.Expect(diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 10, observed, nil)))[partial.JobID].RetentionClass).To(gomega.Equal("protected"))
	})

	ginkgo.It("releases one exact guest completed cleanup intent", func() {
		t := ginkgo.GinkgoT()
		db, ledger := diagnosticOpen(t)
		defer db.Close()
		diagnosticRegister(t, ledger, "local-guest", 1)
		diagnosticRegister(t, ledger, "local-bad", 1)
		diagnosticRegister(t, ledger, "local-partial", 1)
		diagnosticRegister(t, ledger, "local-post", 1)
		observed := time.Unix(1_800_000_000, 0).UTC()
		guestJob := diagnosticAdmit(t, ledger, "local-guest", "guest-job", false)
		settled := diagnosticSettleFailedDeadline(t, ledger, guestJob)
		var raw []byte
		gomega.Expect(db.QueryRow(`SELECT result_json FROM managed_jobs WHERE id=?`, settled.JobID).Scan(&raw)).To(gomega.Succeed())
		stored, err := DecodeResult(raw)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(stored.CleanupOutcome).To(gomega.Equal(CleanupOutcome("failed")))
		gomega.Expect(stored.Reason).To(gomega.Equal("cleanup_deadline"))
		gomega.Expect(stored.ProtectedEvidence).To(gomega.BeEmpty())
		gomega.Expect(stored.EvidenceCompleteness).To(gomega.Equal(EvidenceCompleteness("complete")))
		diagnosticExec(db, `UPDATE managed_resources SET availability='available',availability_reason='' WHERE id='local-guest'`)
		gomega.Expect(diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 20, observed, nil)))[settled.JobID].RetentionClass).To(gomega.Equal("protected"))
		exact := JobRecoveryRequest{
			Identity:    LocalIdentity{JobID: stored.JobID, ResourceID: stored.ResourceID, ConfigRevision: stored.ResourceRevision, LeaseGeneration: stored.LeaseGeneration, Nonce: "nonce-guest"},
			MutationKey: "guest-cleanup-intent",
			Evidence: RecoveryEvidence{
				Source: "machine-observed", Observer: "fixture", ObservedAt: observed,
				Mechanism: "guest-completed-cleanup-observed", Digest: "sha256:" + digest, Reference: "job:" + stored.JobID,
			},
		}
		payload, err := json.Marshal(exact)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		diagnosticEvent(db, settled.JobID, "recovery_intent", string(payload))
		released := diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 20, observed, nil)))[settled.JobID]
		gomega.Expect(released.RetentionClass).To(gomega.Equal("clear"))
		var unchanged []byte
		gomega.Expect(db.QueryRow(`SELECT result_json FROM managed_jobs WHERE id=?`, settled.JobID).Scan(&unchanged)).To(gomega.Succeed())
		gomega.Expect(unchanged).To(gomega.Equal(raw))
		unrelated := exact
		unrelated.MutationKey = "unrelated-intent"
		unrelated.Evidence.Mechanism = "inspect"
		unrelatedPayload, err := json.Marshal(unrelated)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		diagnosticEvent(db, settled.JobID, "recovery_intent", string(unrelatedPayload))
		gomega.Expect(diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 20, observed, nil)))[settled.JobID].RetentionClass).To(gomega.Equal("protected"))
		gomega.Expect(db.QueryRow(`SELECT result_json FROM managed_jobs WHERE id=?`, settled.JobID).Scan(&unchanged)).To(gomega.Succeed())
		gomega.Expect(unchanged).To(gomega.Equal(raw))

		malformed := diagnosticAdmit(t, ledger, "local-bad", "malformed-guest", false)
		diagnosticSettle(t, ledger, malformed, "verified", false)
		diagnosticEvent(db, malformed.JobID, "recovery_intent", "{")
		gomega.Expect(diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 20, observed, nil)))[malformed.JobID].RetentionClass).To(gomega.Equal("unknown"))

		partialJob := diagnosticAdmit(t, ledger, "local-partial", "partial-guest", false)
		partialSettled := diagnosticSettleFailedDeadline(t, ledger, partialJob)
		var partialRaw []byte
		gomega.Expect(db.QueryRow(`SELECT result_json FROM managed_jobs WHERE id=?`, partialSettled.JobID).Scan(&partialRaw)).To(gomega.Succeed())
		partialStored, err := DecodeResult(partialRaw)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		diagnosticExec(db, `UPDATE managed_resources SET availability='available',availability_reason='' WHERE id='local-partial'`)
		partialIntent := JobRecoveryRequest{
			Identity:    LocalIdentity{JobID: partialStored.JobID, ResourceID: partialStored.ResourceID, ConfigRevision: partialStored.ResourceRevision, LeaseGeneration: partialStored.LeaseGeneration + 9, Nonce: "nonce-partial"},
			MutationKey: "partial-guest-intent",
			Evidence: RecoveryEvidence{
				Source: "machine-observed", Observer: "fixture", ObservedAt: observed,
				Mechanism: "guest-completed-cleanup-observed", Digest: "sha256:" + digest, Reference: "job:" + partialStored.JobID,
			},
		}
		partialPayload, err := json.Marshal(partialIntent)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		diagnosticEvent(db, partialSettled.JobID, "recovery_intent", string(partialPayload))
		gomega.Expect(diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 20, observed, nil)))[partialSettled.JobID].RetentionClass).To(gomega.Equal("protected"))
		gomega.Expect(db.QueryRow(`SELECT result_json FROM managed_jobs WHERE id=?`, partialSettled.JobID).Scan(&unchanged)).To(gomega.Succeed())
		gomega.Expect(unchanged).To(gomega.Equal(partialRaw))

		post := diagnosticAdmit(t, ledger, "local-post", "post-finish-guest", false)
		postSettled := diagnosticSettle(t, ledger, post, "verified", false)
		var postRaw []byte
		gomega.Expect(db.QueryRow(`SELECT result_json FROM managed_jobs WHERE id=?`, postSettled.JobID).Scan(&postRaw)).To(gomega.Succeed())
		postStored, err := DecodeResult(postRaw)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		postIntent := JobRecoveryRequest{
			Identity:    LocalIdentity{JobID: postStored.JobID, ResourceID: postStored.ResourceID, ConfigRevision: postStored.ResourceRevision, LeaseGeneration: postStored.LeaseGeneration, Nonce: "nonce-post"},
			MutationKey: "post-finish-intent",
			Evidence: RecoveryEvidence{
				Source: "machine-observed", Observer: "fixture", ObservedAt: observed,
				Mechanism: "guest-completed-cleanup-observed", Digest: "sha256:" + digest, Reference: "job:" + postStored.JobID,
			},
		}
		postPayload, err := json.Marshal(postIntent)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		diagnosticEvent(db, postSettled.JobID, "recovery_intent", string(postPayload))
		gomega.Expect(diagnosticByJob(diagnosticMust(ledger, diagnosticAt("retention", "", "", 20, observed, nil)))[postSettled.JobID].RetentionClass).To(gomega.Equal("protected"))
		gomega.Expect(db.QueryRow(`SELECT result_json FROM managed_jobs WHERE id=?`, postSettled.JobID).Scan(&unchanged)).To(gomega.Succeed())
		gomega.Expect(unchanged).To(gomega.Equal(postRaw))
	})

	ginkgo.It("keeps a known budget fault off the occupancy flag", func() {
		t := ginkgo.GinkgoT()
		db, ledger := diagnosticOpen(t)
		defer db.Close()
		diagnosticRegister(t, ledger, "local-known", 1)
		diagnosticRegister(t, ledger, "local-budget", 1)
		diagnosticRegister(t, ledger, "local-unknown", 1)
		diagnosticAdmit(t, ledger, "local-known", "known-job", false)
		budget := diagnosticAdmit(t, ledger, "local-budget", "budget-job", false)
		reserved, err := reserveScheduling(ledger, budget, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		diagnosticExec(db, `UPDATE managed_jobs SET spec_json=? WHERE id=?`, []byte("{"), reserved.JobID)
		observed := time.Unix(1_800_000_000, 0).UTC()
		alive := func(id string) bool { return id == reserved.JobID }
		page := diagnosticMust(ledger, diagnosticAt("queue", "", "", 20, observed, alive))
		gomega.Expect(page.AnyUnknownOccupancy).To(gomega.BeFalse())
		rows := diagnosticByResource(page)
		gomega.Expect(rows["local-budget"].Status).To(gomega.Equal("unknown"))
		gomega.Expect(rows["local-budget"].Reason).To(gomega.Equal("unreadable_admitted_budget"))
		gomega.Expect(rows["local-budget"].IncludedJobs).To(gomega.BeNil())
		gomega.Expect(rows["local-budget"].BacklogSeconds).To(gomega.BeNil())
		gomega.Expect(rows["local-budget"].ProspectiveCeilingSeconds).To(gomega.BeNil())
		gomega.Expect(rows["local-budget"].TotalSeconds).To(gomega.BeNil())
		gomega.Expect(rows["local-known"].Status).To(gomega.Equal("bounded"))
		kept := *rows["local-known"].TotalSeconds

		unknownJob := diagnosticAdmit(t, ledger, "local-unknown", "unknown-job", false)
		_, err = reserveScheduling(ledger, unknownJob, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		mixed := diagnosticMust(ledger, diagnosticAt("queue", "", "", 20, observed, alive))
		gomega.Expect(mixed.AnyUnknownOccupancy).To(gomega.BeTrue())
		mixedRows := diagnosticByResource(mixed)
		gomega.Expect(mixedRows["local-unknown"].Status).To(gomega.Equal("unknown"))
		gomega.Expect(mixedRows["local-unknown"].Reason).To(gomega.Equal("unknown_occupied_execution"))
		gomega.Expect(mixedRows["local-unknown"].TotalSeconds).To(gomega.BeNil())
		gomega.Expect(mixedRows["local-budget"].Reason).To(gomega.Equal("unreadable_admitted_budget"))
		gomega.Expect(mixedRows["local-budget"].IncludedJobs).To(gomega.BeNil())
		gomega.Expect(mixedRows["local-budget"].TotalSeconds).To(gomega.BeNil())
		gomega.Expect(*mixedRows["local-known"].TotalSeconds).To(gomega.Equal(kept))
	})
})
