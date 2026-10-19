//go:build integration

package managed

import (
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task027RecoveryBudgetMigration", ginkgo.Label("integration"), func() {
	ginkgo.It("migrates layout 12 without rewriting legacy v1/v2 intent, receipt, owner, or deadlines", func() {
		db, l := openLedger(ginkgo.GinkgoT(), filepath.Join(ginkgo.GinkgoT().TempDir(), "state.sqlite"), 2)
		defer db.Close()
		resource := validResource()
		resource.ID = "old-vm"
		gomega.Expect(l.RegisterResource(encode(ginkgo.GinkgoT(), resource))).To(gomega.Succeed())
		type pair struct {
			intent         EvidenceRecoveryIntent
			receipt        EvidenceRecoveryReceipt
			request, reply []byte
		}
		var saved []pair
		for _, schema := range []string{"v1", "v2"} {
			result := Result{JobID: "old-job-" + schema, ResourceID: "old-vm", ResourceRevision: 1, LeaseGeneration: 7}
			intent, receipt := diagnosticRecoveryPair(result, nil, "legacy-"+schema, "failed", true)
			intent.Jobs[0].CollectDeadline = intent.RecordedAt.Add(5 * time.Second)
			intent.Jobs[0].CleanupDeadline = intent.RecordedAt.Add(10 * time.Second)
			receipt.Jobs[0].Gap = "original evidence recovery deadline expired"
			if schema == "v2" {
				intent.Schema = "haepari.evidence-recovery-intent/v2"
				receipt.Schema = "haepari.evidence-recovery-receipt/v2"
				intent.Jobs[0].Identity.RunID = "original-run"
				intent.Jobs[0].AbsenceBinding = &EvidenceAbsenceBinding{WorkspaceRoot: RetainedPath{Path: "workspace", Inode: 23, Directory: true}, AttestationDigest: "sha256:" + digest, OriginalBoot: "old-boot", RecoveryBoot: "new-boot", Cessation: RecoveryEvidence{Source: "machine-observed", Observer: "fixture", ObservedAt: intent.RecordedAt, Mechanism: "guest-reboot-observed", Digest: "sha256:" + digest, Reference: "job:" + result.JobID}}
			}
			gomega.Expect(validEvidenceIntent(intent)).To(gomega.BeTrue())
			gomega.Expect(validEvidenceReceipt(intent, receipt)).To(gomega.BeTrue())
			diagnosticInsertRecovery(db, intent, &receipt)
			request, _ := json.Marshal(intent)
			reply, _ := json.Marshal(receipt)
			saved = append(saved, pair{intent, receipt, request, reply})
		}
		_, err := db.Exec(`DELETE FROM schema_migrations WHERE version=13`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`PRAGMA user_version=12`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		l, err = NewLedger(db, 2)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var version int
		gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&version)).To(gomega.Succeed())
		gomega.Expect(version).To(gomega.Equal(13))
		for _, old := range saved {
			var a, b []byte
			gomega.Expect(db.QueryRow(`SELECT request_json,receipt_json FROM managed_evidence_recovery WHERE mutation_key=?`, old.intent.MutationKey).Scan(&a, &b)).To(gomega.Succeed())
			gomega.Expect(a).To(gomega.Equal(old.request))
			gomega.Expect(b).To(gomega.Equal(old.reply))
			intent, receipt, found, err := l.EvidenceRecovery(old.intent.MutationKey, old.intent.ClientDigest)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(found).To(gomega.BeTrue())
			gomega.Expect(intent).To(gomega.Equal(old.intent))
			gomega.Expect(receipt).To(gomega.Equal(&old.receipt))
			gomega.Expect(intent.Budget).To(gomega.BeNil())
		}
	})
	ginkgo.It("rejects a budget field, including null, in closed legacy private intents", func() {
		result := Result{JobID: "old-job", ResourceID: "old-vm", ResourceRevision: 1, LeaseGeneration: 7}
		intent, _ := diagnosticRecoveryPair(result, nil, "legacy-key", "pending", false)
		for _, schema := range []string{"haepari.evidence-recovery-intent/v1", "haepari.evidence-recovery-intent/v2"} {
			intent.Schema = schema
			raw, _ := json.Marshal(intent)
			var fields map[string]json.RawMessage
			gomega.Expect(json.Unmarshal(raw, &fields)).To(gomega.Succeed())
			for _, extra := range []string{"null", `{"schema":"haepari.recovery-budget/v1"}`} {
				fields["budget"] = json.RawMessage(extra)
				changed, err := json.Marshal(fields)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				var decoded EvidenceRecoveryIntent
				gomega.Expect(decodeClosed(changed, &decoded)).To(gomega.HaveOccurred())
			}
		}
	})

	for _, corruption := range []string{"wrong-13", "missing-13", "future-14", "wrong-marker-before-migration"} {
		corruption := corruption
		ginkgo.It("refuses "+corruption+" without partial schema changes", func() {
			db, _ := openLedger(ginkgo.GinkgoT(), filepath.Join(ginkgo.GinkgoT().TempDir(), "state.sqlite"), 1)
			defer db.Close()
			switch corruption {
			case "wrong-13", "wrong-marker-before-migration":
				_, err := db.Exec(`UPDATE schema_migrations SET identity='unrecognized-budget-layout' WHERE version=13`)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			case "missing-13":
				_, err := db.Exec(`DELETE FROM schema_migrations WHERE version=13`)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			if corruption == "future-14" {
				_, err := db.Exec(`PRAGMA user_version=14`)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			if corruption == "wrong-marker-before-migration" {
				_, err := db.Exec(`PRAGMA user_version=12`)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			read := func() string {
				var value string
				gomega.Expect(db.QueryRow(`SELECT group_concat(version||':'||identity,'|') FROM (SELECT version,identity FROM schema_migrations ORDER BY version)`).Scan(&value)).To(gomega.Succeed())
				return value
			}
			before := read()
			var version int
			gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&version)).To(gomega.Succeed())
			_, err := NewLedger(db, 1)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(read()).To(gomega.Equal(before))
			var after int
			gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&after)).To(gomega.Succeed())
			gomega.Expect(after).To(gomega.Equal(version))
		})
	}
})
