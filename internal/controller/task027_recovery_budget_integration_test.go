//go:build integration

package controller

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func task027ResourceBudgets(resource *managed.ResourceSpec) {
	resource.Schema = "haepari.resource/v2"
	resource.LifecycleBudgets = &managed.LifecycleBudgets{
		QueueSeconds: 180, PrepareSeconds: 180, RunSeconds: 60,
		ManualSeconds: 3600, CollectSeconds: 90, CleanupSeconds: 90,
	}
}

func task027SavedRecovery(s *runtimeServer, resource string, request managed.RecoveryRequest) (managed.EvidenceRecoveryIntent, *managed.EvidenceRecoveryReceipt) {
	digest := managed.RecoveryDigest(struct {
		ResourceID string
		Request    managed.RecoveryRequest
	}{resource, request})
	intent, receipt, found, err := s.ledger.EvidenceRecovery("evidence-recovery-"+request.MutationKey, digest)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(found).To(gomega.BeTrue())
	return intent, receipt
}

var _ = ginkgo.Describe("Task027RecoveryBudgets", ginkgo.Label("integration"), func() {
	ginkgo.It("rejects a present noncanonical budget digest without rejecting legacy absence or current tokens", func() {
		s, r, _ := protectedLocalFixture()
		plan, err := s.planEvidenceRecovery(r.ResourceID, "dispose")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		legacy, err := s.readRecoveryToken(plan.PlanToken)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(legacy.BudgetDigest).To(gomega.BeEmpty())
		payload, _, _ := strings.Cut(plan.PlanToken, ".")
		raw, err := base64.RawURLEncoding.DecodeString(payload)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var fields map[string]json.RawMessage
		gomega.Expect(json.Unmarshal(raw, &fields)).To(gomega.Succeed())
		gomega.Expect(fields).NotTo(gomega.HaveKey("budget_digest"))
		signFields := func() string {
			data, err := json.Marshal(fields)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			mac := hmac.New(sha256.New, s.recoveryKey[:])
			_, err = mac.Write(data)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		}
		for _, value := range []string{`null`, `""`, `"sha256:abcd"`, `"sha256:` + strings.Repeat("A", 64) + `"`, `"sha256:` + strings.Repeat("g", 64) + `"`, `"` + strings.Repeat("a", 64) + `"`} {
			fields["budget_digest"] = json.RawMessage(value)
			_, err = s.readRecoveryToken(signFields())
			gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid), value)
		}
		canonical := json.RawMessage(`"` + managed.RecoveryDigest("current-budget") + `"`)
		for _, alias := range []string{"BUDGET_DIGEST", "Budget_Digest", "budget_digeſt"} {
			for _, value := range []json.RawMessage{json.RawMessage(`null`), json.RawMessage(`""`), canonical} {
				delete(fields, "budget_digest")
				fields[alias] = value
				_, err = s.readRecoveryToken(signFields())
				gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid), alias)
				fields["budget_digest"] = canonical
				_, err = s.readRecoveryToken(signFields())
				gomega.Expect(err).To(gomega.MatchError(managed.ErrInvalid), "canonical plus "+alias)
				delete(fields, alias)
			}
		}
		legacy.BudgetDigest = managed.RecoveryDigest("current-budget")
		current, err := s.readRecoveryToken(s.signRecoveryToken(legacy))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current).To(gomega.Equal(legacy))
	})
	for _, action := range []string{"recollect", "dispose"} {
		ginkgo.It("uses resource ceilings for a new v3 "+action+" and replays its saved receipt after controller replacement", func() {
			s, r, original := protectedLocalResourceFixture(task027ResourceBudgets)
			before, err := s.ledger.BulkSnapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(before.Spec.Deadlines.CollectSeconds).To(gomega.Equal(int64(3)))
			gomega.Expect(before.Spec.Deadlines.CleanupSeconds).To(gomega.Equal(int64(2)))
			plan, err := s.planEvidenceRecovery(r.ResourceID, action)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(plan.Schema).To(gomega.Equal("haepari.recovery-plan/v3"))
			gomega.Expect(plan.Budget).NotTo(gomega.BeNil())
			gomega.Expect(plan.Budget.CollectSeconds).To(gomega.Equal(int64(90)))
			gomega.Expect(plan.Budget.CleanupSeconds).To(gomega.Equal(int64(90)))
			gomega.Expect(plan.Budget.SourceConfigRevision).To(gomega.Equal(int64(1)))
			token, err := s.readRecoveryToken(plan.PlanToken)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(token.BudgetDigest).To(gomega.Equal(managed.RecoveryDigest(*plan.Budget)))

			request := managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "new-budget-" + action}
			w := recoveryHTTP(s, "/v1/resources/"+r.ResourceID+"/recover", request)
			gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
			intent, receipt := task027SavedRecovery(s, r.ResourceID, request)
			gomega.Expect(intent.Schema).To(gomega.Equal("haepari.evidence-recovery-intent/v3"))
			gomega.Expect(intent.Budget).To(gomega.Equal(plan.Budget))
			gomega.Expect(receipt.Schema).To(gomega.Equal("haepari.evidence-recovery-receipt/v3"))
			gomega.Expect(receipt.Complete).To(gomega.BeTrue())
			gomega.Expect(receipt.Jobs[0].Stage).To(gomega.Equal("resolved"))
			gomega.Expect(intent.Jobs[0].CollectDeadline).To(gomega.Equal(intent.RecordedAt.Add(90 * time.Second)))
			gomega.Expect(intent.Jobs[0].CleanupDeadline).To(gomega.Equal(intent.RecordedAt.Add(180 * time.Second)))
			gomega.Expect(os.Stat(filepath.Join(s.root.Path, "jobs", r.JobID, "workspace"))).Error().To(gomega.MatchError(os.ErrNotExist))

			fresh, err := newRuntime(s.root, s.ledger, s.store)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
			w = recoveryHTTP(fresh, "/v1/resources/"+r.ResourceID+"/recover", request)
			gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
			replayed, replayedReceipt := task027SavedRecovery(fresh, r.ResourceID, request)
			gomega.Expect(replayed).To(gomega.Equal(intent))
			gomega.Expect(replayedReceipt).To(gomega.Equal(receipt))
			after, err := fresh.ledger.BulkSnapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(after.Spec).To(gomega.Equal(before.Spec))
			gomega.Expect(after.Result).To(gomega.Equal(original))
		})
	}

	for _, version := range []string{"v1", "v2"} {
		ginkgo.It("rejects a token from the wrong budget branch before any "+version+" recovery effect", func() {
			var configure func(*managed.ResourceSpec)
			if version == "v2" {
				configure = task027ResourceBudgets
			}
			s, r, original := protectedLocalResourceFixture(configure)
			plan, err := s.planEvidenceRecovery(r.ResourceID, "dispose")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			token, err := s.readRecoveryToken(plan.PlanToken)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if version == "v1" {
				gomega.Expect(plan.Budget).To(gomega.BeNil())
				gomega.Expect(token.BudgetDigest).To(gomega.BeEmpty())
				token.BudgetDigest = managed.RecoveryDigest("foreign-resource-budget")
			} else {
				gomega.Expect(token.BudgetDigest).NotTo(gomega.BeEmpty())
				token.BudgetDigest = ""
			}
			before, err := s.ledger.ResourceRecoverySnapshot(r.ResourceID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			request := managed.RecoveryRequest{PlanToken: s.signRecoveryToken(token), MutationKey: "wrong-budget-branch"}
			w := recoveryHTTP(s, "/v1/resources/"+r.ResourceID+"/recover", request)
			gomega.Expect(w.Code).NotTo(gomega.Equal(200), w.Body.String())
			after, err := s.ledger.ResourceRecoverySnapshot(r.ResourceID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(after).To(gomega.Equal(before))
			gomega.Expect(os.Stat(filepath.Join(s.root.Path, "jobs", r.JobID, "workspace/results/output.txt"))).Error().NotTo(gomega.HaveOccurred())
			current, err := s.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current).To(gomega.Equal(original))
		})
	}

	ginkgo.It("retains the original job-spec deadlines and field shape for a new legacy resource apply", func() {
		s, r, _ := protectedLocalFixture()
		plan, err := s.planEvidenceRecovery(r.ResourceID, "dispose")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.Schema).To(gomega.Equal("haepari.recovery-plan/v1"))
		raw, err := json.Marshal(plan)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var shape map[string]json.RawMessage
		gomega.Expect(json.Unmarshal(raw, &shape)).To(gomega.Succeed())
		gomega.Expect(shape).NotTo(gomega.HaveKey("budget"))
		request := managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "new-legacy-budget"}
		w := recoveryHTTP(s, "/v1/resources/"+r.ResourceID+"/recover", request)
		gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
		intent, receipt := task027SavedRecovery(s, r.ResourceID, request)
		gomega.Expect(intent.Schema).To(gomega.Equal("haepari.evidence-recovery-intent/v1"))
		gomega.Expect(intent.Budget).To(gomega.BeNil())
		gomega.Expect(receipt.Schema).To(gomega.Equal("haepari.evidence-recovery-receipt/v1"))
		gomega.Expect(intent.Jobs[0].CollectDeadline).To(gomega.Equal(intent.RecordedAt.Add(3 * time.Second)))
		gomega.Expect(intent.Jobs[0].CleanupDeadline).To(gomega.Equal(intent.RecordedAt.Add(5 * time.Second)))
	})
})

var _ = ginkgo.Describe("Task027ExampleCleanupBudget", ginkgo.Label("integration"), func() {
	ginkgo.It("completes authenticated native health-cleanup-health beyond five seconds under the sample's finite budget", func() {
		f := nativeVMConfiguredFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf retained > ../results/output.txt", task027ResourceBudgets)
		f.job.Deadlines.CollectSeconds, f.job.Deadlines.CleanupSeconds = 90, 90
		base := f.s.sshCommand
		var mu sync.Mutex
		var operations []string
		f.s.sshCommand = func(ctx context.Context, path string, args []string, input []byte) (utm.CommandResult, error) {
			operation := vmTransportRequest(input).Operation
			if operation == "health" || operation == "cleanup" {
				mu.Lock()
				operations = append(operations, operation)
				mu.Unlock()
			}
			if operation == "health" {
				timer := time.NewTimer(3 * time.Second)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					return utm.CommandResult{}, ctx.Err()
				}
			}
			return base(ctx, path, args, input)
		}
		result := f.run("sample-cleanup-budget")
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(result.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("complete")))
		phases, err := f.s.ledger.LocalPhases(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		cleanup := phases["cleanup"]
		gomega.Expect(cleanup.BudgetSeconds).To(gomega.Equal(int64(90)))
		gomega.Expect(cleanup.ObservedAt.Sub(cleanup.StartedAt)).To(gomega.BeNumerically(">", 5*time.Second))
		gomega.Expect(cleanup.Completed).To(gomega.BeTrue())
		mu.Lock()
		gomega.Expect(operations).To(gomega.Equal([]string{"health", "cleanup", "health"}))
		mu.Unlock()
		gomega.Expect(f.control.effects).To(gomega.BeEmpty())
	})
})
