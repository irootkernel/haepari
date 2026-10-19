//go:build integration

package managed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/config"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task019ActualSchema9Migration", ginkgo.Label("integration"), func() {
	ginkgo.It("preserves actual schema9 producer identities and exact cancellation replay", func() {
		raw, err := os.ReadFile("testdata/schema9.sql")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		metadata, err := os.ReadFile("testdata/schema9.json")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var original struct {
			ProducerCommit string        `json:"producer_commit"`
			SQLSHA256      string        `json:"sql_sha256"`
			Spec           JobSpec       `json:"spec"`
			Receipt        Receipt       `json:"receipt"`
			Cancel         CancelReceipt `json:"cancel_receipt"`
		}
		gomega.Expect(json.Unmarshal(metadata, &original)).To(gomega.Succeed())
		gomega.Expect(original.ProducerCommit).To(gomega.Equal("249c82dc4b9385f84962191b846cddce952526c4"))
		sum := sha256.Sum256(raw)
		gomega.Expect(hex.EncodeToString(sum[:])).To(gomega.Equal(original.SQLSHA256))
		db, err := sqlite.Open(filepath.Join(ginkgo.GinkgoT().TempDir(), "state.sqlite"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		_, err = db.Exec("PRAGMA foreign_keys=OFF;\n" + string(raw) + "\nPRAGMA foreign_keys=ON;")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var version int
		gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&version)).To(gomega.Succeed())
		gomega.Expect(version).To(gomega.Equal(9))
		var installation string
		gomega.Expect(db.QueryRow(`SELECT id FROM managed_installation`).Scan(&installation)).To(gomega.Succeed())
		l, err := NewLedger(db, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&version)).To(gomega.Succeed())
		gomega.Expect(version).To(gomega.Equal(13))
		actual, err := l.BindInstallation("different-seed")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(actual).To(gomega.Equal(installation))
		saved, err := l.Request("schema9-original")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(saved.Receipt.Admission).To(gomega.Equal(original.Receipt.Admission))
		gomega.Expect(saved.Receipt.JobState).To(gomega.Equal("finished"))
		gomega.Expect(saved.Receipt.Verdict).To(gomega.Equal(Cancelled))
		replay, err := l.Cancel(CancelRequest{JobID: original.Receipt.JobID, ExpectedRevision: original.Receipt.JobRevision, MutationKey: "schema9-cancel"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay).To(gomega.Equal(original.Cancel))
		var count int
		gomega.Expect(db.QueryRow(`SELECT count(*) FROM managed_manual_sessions`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.BeZero())
		_, err = db.Exec(`DROP TABLE managed_manual_sessions`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = NewLedger(db, 1)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("schema10 manual sessions"))
		gomega.Expect(db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='managed_manual_sessions'`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.BeZero())
	})
})

var _ = ginkgo.Describe("Task019LegacyHoldMigration", ginkgo.Label("integration"), func() {
	for _, corrupt := range []bool{false, true} {
		corrupt := corrupt
		ginkgo.It("preserves original legacy authority without fabricated readiness or partial migration (corrupt="+fmt.Sprint(corrupt)+")", func() {
			t := ginkgo.GinkgoT()
			db, l := openLedger(t, filepath.Join(t.TempDir(), "legacy.sqlite"), 1)
			defer db.Close()
			gomega.Expect(l.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
			_, err := l.BindInstallation("legacy-installation")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			job := validJob()
			job.Mode = "manual"
			job.Manual = &Manual{ScenarioDocument: "source/check.md", RequiredScenarios: []string{"one"}}
			job.Deadlines.RunSeconds = 0
			job.Deadlines.ManualSeconds = 60
			r, err := l.Admit(encode(t, job), digest, "legacy-manual-key", "legacy-manual-job")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			r, err = reserveScheduling(l, r, nil)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			r, err = l.AdvanceLocal(r.JobID, r.JobRevision, "preparing", "ready", "legacy-manual-ready")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			hold := ManualHoldRequest{JobID: r.JobID, ExpectedJobRevision: r.JobRevision, ExpectedConfigRevision: r.ResourceConfigRevision, ExpectedAvailabilityRevision: r.ResourceAvailabilityRevision, Slot: r.Slot, LeaseGeneration: r.LeaseGeneration, EnvironmentDigest: job.Environment.ProfileDigest, ReadinessDigest: digest, ObservationReference: "supplementary-legacy-hold", MutationKey: "legacy-manual-hold"}
			held, err := l.HoldManualCapacity(hold)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var raw []byte
			gomega.Expect(db.QueryRow(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='awaiting_manual'`, r.JobID).Scan(&raw)).To(gomega.Succeed())
			var event struct {
				Deadline time.Time `json:"manual_deadline"`
			}
			gomega.Expect(json.Unmarshal(raw, &event)).To(gomega.Succeed())
			// Supplementary reconstructed v9 in-flight layout, distinct from actual9.sql.
			_, err = db.Exec(`DROP TABLE managed_manual_sessions; DELETE FROM schema_migrations WHERE version=10; PRAGMA user_version=9`)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if corrupt {
				_, err = db.Exec(`UPDATE managed_events SET payload_json=? WHERE job_id=? AND kind='awaiting_manual'`, []byte(`{}`), r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			migrated, err := NewLedger(db, 1)
			if corrupt {
				gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("legacy-manual-job")))
				var version, count int
				gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&version)).To(gomega.Succeed())
				gomega.Expect(version).To(gomega.Equal(9))
				gomega.Expect(db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='managed_manual_sessions'`).Scan(&count)).To(gomega.Succeed())
				gomega.Expect(count).To(gomega.BeZero())
				return
			}
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			session, err := migrated.ManualSession(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(session.Legacy).To(gomega.BeTrue())
			gomega.Expect(session.Deadline).To(gomega.Equal(event.Deadline))
			gomega.Expect(session.Preparation).To(gomega.BeNil())
			gomega.Expect(session.Stopped).To(gomega.BeFalse())
			replay, err := migrated.HoldManualCapacity(hold)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(replay).To(gomega.Equal(held))
			freshHold := hold
			freshHold.ExpectedJobRevision = held.JobRevision
			freshHold.MutationKey = "new-legacy-hold"
			_, err = migrated.HoldManualCapacity(freshHold)
			gomega.Expect(err).To(gomega.HaveOccurred())
			record := ManualResult{Schema: "haepari.manual-result/v1", JobID: r.JobID, InstallationID: "legacy-installation", ProfileID: job.Environment.ProfileID, ProfileDigest: job.Environment.ProfileDigest, ExpectedRevision: held.JobRevision, Observer: "synthetic-legacy", Scenarios: []ManualScenario{{ID: "one", Status: "pass", EvidenceReferences: []string{"fixture:check"}}}}
			_, err = migrated.CompleteManual(r.JobID, "legacy-installation", encode(t, record), held.JobRevision)
			gomega.Expect(err).To(gomega.MatchError(ErrBlocked))
			gomega.Expect(migrated.MarkInterrupted()).To(gomega.Succeed())
			snapshot, err := migrated.RecoverySnapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = migrated.BeginJobRecovery(JobRecoveryRequest{Identity: snapshot.Identity, SnapshotDigest: RecoveryDigest(snapshot), MutationKey: "legacy-manual-recovery", Automatic: true, Action: "reattach", NextState: "awaiting_manual", Evidence: RecoveryEvidence{Source: "machine-observed", Observer: "supplementary-fixture", ObservedAt: time.Now().UTC(), Mechanism: "legacy-hold-inspection", Digest: "sha256:" + digest, Reference: "job:" + r.JobID}})
			gomega.Expect(err).To(gomega.MatchError(ErrBlocked))
			current, err := migrated.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current.ResourceOccupancy).To(gomega.Equal(1))
			gomega.Expect(current.JobState).To(gomega.Equal("reconciling"))
		})
	}
})

var _ = ginkgo.Describe("Task023BindManualVMBounds", ginkgo.Label("integration"), func() {
	ginkgo.It("refuses a new VM binding when current bounds are missing or malformed and keeps exact replay", func() {
		t := ginkgo.GinkgoT()
		db, ledger := openLedger(t, filepath.Join(t.TempDir(), "manual-bind.sqlite"), 1)
		defer db.Close()
		vmScheduling(t, ledger, "manual-vm", 1, "started", false)
		job := validJob()
		job.ResourceID, job.Mode = "manual-vm", "manual"
		job.Manual = &Manual{ScenarioDocument: "source/scenarios.txt", RequiredScenarios: []string{"check"}}
		job.Input.SelectedFiles = []string{"source/scenarios.txt"}
		job.Deadlines.RunSeconds, job.Deadlines.ManualSeconds = 0, 60
		admitted, err := ledger.Admit(encode(t, job), digest, "manual-bind-key", "manual-bind-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		admitted, err = reserveScheduling(ledger, admitted, vmGuard(t, db, 1))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		id, err := ledger.LocalIdentity(admitted.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		id.RunID = "manual-bind-run"
		admitted, err = ledger.ClaimVMRun(id, admitted.JobRevision, json.RawMessage(`{}`))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		deadline := time.Unix(1_700_000_000, 0).UTC()
		bind := func(workspace string) error {
			return ledger.BindManualVM(id, deadline, "manual-bind-install", workspace)
		}
		workspace := "/var/haepari/jobs/manual-bind-job/workspace"
		for _, fault := range []string{"missing", "malformed"} {
			state, spec, evidence, occupancy, events, admissions := jobEffect(db, admitted.JobID)
			sessions := countQuery(db, `SELECT COUNT(*) FROM managed_manual_sessions`)
			if fault == "missing" {
				_, err = db.Exec(`DELETE FROM managed_job_bounds WHERE job_id=?`, admitted.JobID)
			} else {
				_, err = db.Exec(`UPDATE managed_job_bounds SET request_bytes=? WHERE job_id=?`, config.MaxRequestBytes+1, admitted.JobID)
			}
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			err = bind(workspace)
			gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue(), fault)
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("occupancy and protected evidence stay unchanged"), fault)
			afterState, afterSpec, afterEvidence, afterOccupancy, afterEvents, afterAdmissions := jobEffect(db, admitted.JobID)
			gomega.Expect(afterState).To(gomega.Equal(state), fault)
			gomega.Expect(afterSpec).To(gomega.Equal(spec), fault)
			gomega.Expect(afterEvidence).To(gomega.Equal(evidence), fault)
			gomega.Expect(afterOccupancy).To(gomega.Equal(occupancy), fault)
			gomega.Expect(afterEvents).To(gomega.Equal(events), fault)
			gomega.Expect(afterAdmissions).To(gomega.Equal(admissions), fault)
			gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_manual_sessions`)).To(gomega.Equal(sessions), fault)
			if fault == "missing" {
				_, err = db.Exec(`INSERT INTO managed_job_bounds(job_id,request_bytes,log_bytes,artifact_bytes,artifact_count,event_retention_days,result_retention_days,config_revision,config_digest,policy_source) VALUES(?,?,?,?,?,?,?,NULL,NULL,'legacy_defaults')`, admitted.JobID, config.MaxRequestBytes, config.MaxLogBytes, config.MaxArtifactBytes, config.MaxArtifactCount, 30, 90)
			} else {
				// restoreLegacyBound checks its own SQL result. The ErrBlocked from bind must not remain.
				restoreLegacyBound(db, admitted.JobID)
				err = nil
			}
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		gomega.Expect(bind(workspace)).To(gomega.Succeed())
		session, err := ledger.ManualSession(admitted.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`DELETE FROM managed_job_bounds WHERE job_id=?`, admitted.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(bind(workspace)).To(gomega.Succeed())
		replayed, err := ledger.ManualSession(admitted.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replayed).To(gomega.Equal(session))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_manual_sessions`)).To(gomega.Equal(1))
		err = bind(workspace + "-other")
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
		conflicted, err := ledger.ManualSession(admitted.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(conflicted).To(gomega.Equal(session))
	})
})
