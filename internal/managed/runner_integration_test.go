//go:build integration

package managed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task009RunnerReceiptLedger", ginkgo.Label("integration"), func() {
	ginkgo.It("migrates schema 7 without manufacturing evidence for its existing run", func() {
		db, l, admission := cancellationFixture(ginkgo.GinkgoT())
		r, id := cancellationRunning(ginkgo.GinkgoT(), l, admission)
		_, err := db.Exec(`DROP TABLE managed_runner_receipts; DELETE FROM schema_migrations WHERE version=8; PRAGMA user_version=7`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		l, err = NewLedger(db, 2)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		retained, err := l.LocalIdentity(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(retained).To(gomega.Equal(id))
		var receipts int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_runner_receipts`).Scan(&receipts)).To(gomega.Succeed())
		gomega.Expect(receipts).To(gomega.BeZero())
		current, err := l.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
	})
	ginkgo.It("fences receipt import, order and replay without changing cancellation revisions", func() {
		db, l, admission := cancellationFixture(ginkgo.GinkgoT())
		r, id := cancellationRunning(ginkgo.GinkgoT(), l, admission)
		request, err := json.Marshal(map[string]any{"schema": "haepari.runner-request/v1", "identity": id})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		sum := sha256.Sum256(request)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		payload := func(kind, binding string) []byte {
			data, err := json.Marshal(map[string]any{"schema": "haepari.runner-" + kind + "/v1", "identity": id, "request_digest": binding})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			return data
		}
		gomega.Expect(errors.Is(l.RecordRunnerReceipt(id, "start", payload("start", digest)), ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(l.RecordRunnerReceipt(id, "request", request)).To(gomega.Succeed())
		gomega.Expect(errors.Is(l.RecordRunnerReceipt(id, "outcome", payload("outcome", digest)), ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(errors.Is(l.RecordRunnerReceipt(id, "start", payload("start", digest)), ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(errors.Is(l.RecordRunnerReceipt(id, "intent", payload("intent", "wrong")), ErrConflict)).To(gomega.BeTrue())
		gomega.Expect(l.RecordRunnerReceipt(id, "intent", payload("intent", digest))).To(gomega.Succeed())
		gomega.Expect(l.RecordRunnerReceipt(id, "intent", payload("intent", digest))).To(gomega.Succeed())
		gomega.Expect(l.RecordRunnerReceipt(id, "start", payload("start", digest))).To(gomega.Succeed())
		gomega.Expect(errors.Is(l.RecordRunnerReceipt(id, "exit", payload("exit", digest)), ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(errors.Is(l.RecordRunnerReceipt(id, "cancel", payload("cancel", digest)), ErrInvalid)).To(gomega.BeTrue())
		gomega.Expect(errors.Is(l.RecordRunnerReceipt(id, "outcome", bytes.Repeat([]byte("x"), (2<<20)+1)), ErrInvalid)).To(gomega.BeTrue())
		duplicate := bytes.Replace(payload("outcome", digest), []byte(`"schema":`), []byte(`"schema":"duplicate","schema":`), 1)
		gomega.Expect(errors.Is(l.RecordRunnerReceipt(id, "outcome", duplicate), ErrInvalid)).To(gomega.BeTrue())
		gomega.Expect(l.RecordRunnerReceipt(id, "outcome", payload("outcome", digest))).To(gomega.Succeed())
		gomega.Expect(l.RecordRunnerReceipt(id, "exit", payload("exit", digest))).To(gomega.Succeed())
		stale := id
		stale.LeaseGeneration++
		data, _ := json.Marshal(map[string]any{"schema": "haepari.runner-request/v1", "identity": stale})
		gomega.Expect(errors.Is(l.RecordRunnerReceipt(stale, "request", data), ErrStale)).To(gomega.BeTrue())
		var events int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE kind='runner_receipt'`).Scan(&events)).To(gomega.Succeed())
		gomega.Expect(events).To(gomega.Equal(5))
		current, err := l.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.JobRevision).To(gomega.Equal(r.JobRevision))
		gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
	})
})
