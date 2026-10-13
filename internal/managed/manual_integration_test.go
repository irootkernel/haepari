//go:build integration

package managed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"os"
	"path/filepath"
	"time"
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
		gomega.Expect(version).To(gomega.Equal(10))
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
