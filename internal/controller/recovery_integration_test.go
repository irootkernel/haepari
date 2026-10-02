//go:build integration

package controller

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"sync"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func recoveryHTTP(s *runtimeServer, path string, q managed.RecoveryRequest) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(q)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	r.Header.Set("X-Haepari-Protocol-Min", "1")
	r.Header.Set("X-Haepari-Protocol-Max", "1")
	r.Header.Set("X-Haepari-Protocol", protocol)
	s.routes().ServeHTTP(w, r)
	return w
}

func recoveryData(w *httptest.ResponseRecorder, target any) {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	gomega.Expect(json.Unmarshal(w.Body.Bytes(), &envelope)).To(gomega.Succeed())
	gomega.Expect(json.Unmarshal(envelope.Data, target)).To(gomega.Succeed())
}

var _ = ginkgo.Describe("Task011RecoveryProtocol", ginkgo.Label("integration"), func() {
	ginkgo.It("retains a resumed filesystem worker after its deadline and blocks another recovery until it actually stops", func() {
		_, s, r, _, _ := cancellationRuntime(ginkgo.GinkgoT(), "true")
		gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
		before, err := s.ledger.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		release := make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		ginkgo.GinkgoT().Cleanup(func() { unblock(); s.workers.Wait() })
		_, status := boundedPhaseWithWorker(time.Now().Add(20*time.Millisecond), s.detached, func(work func()) bool { return s.startJobWorker(r.JobID, work) }, func() error { <-release; return nil })
		gomega.Expect(status).To(gomega.Equal(phaseUnproven))
		gomega.Expect(s.jobWorkerAlive(r.JobID)).To(gomega.BeTrue())
		evidence := recoveryObservation("fixture-stop", before, "job:"+r.JobID)
		evidence.Source = "operator-attested"
		for _, action := range []string{"reconcile", "attest-stopped", "attest-no-start"} {
			plan, err := s.planJobRecovery(r.JobID, action)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(plan.PlanToken).To(gomega.BeEmpty())
			gomega.Expect(plan.Unknowns).To(gomega.ContainElement("a resumed filesystem worker is still active"))
		}
		gomega.Expect(s.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.MatchError(managed.ErrBlocked))
		gomega.Expect(s.tryRecoverInterrupted(r.JobID, false, &evidence)).To(gomega.MatchError(managed.ErrBlocked))
		after, err := s.ledger.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
		gomega.Expect(after.Receipt.GlobalOccupancy).To(gomega.Equal(1))
		unblock()
		s.workers.Wait()
		gomega.Expect(s.jobWorkerAlive(r.JobID)).To(gomega.BeFalse())
		plan, err := s.planJobRecovery(r.JobID, "attest-no-start")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
	})
	ginkgo.It("blocks backward-clock recovery and records the discontinuity without starting or releasing work", func() {
		db, s, r, job, _ := cancellationRuntime(ginkgo.GinkgoT(), "true")
		id, err := s.ledger.LocalIdentity(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		future := time.Now().UTC().Add(time.Hour)
		p := managed.LocalPhase{Schema: "haepari.phase-receipt/v1", Identity: id, Phase: "prepare", BudgetSeconds: job.Deadlines.PrepareSeconds, StartedAt: future, ObservedAt: future, Deadline: future.Add(time.Duration(job.Deadlines.PrepareSeconds) * time.Second)}
		_, err = s.ledger.RecordLocalPhase(managed.LocalPhaseRequest{ExpectedRevision: r.JobRevision, Record: p, MutationKey: "future-phase"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
		plan, err := s.planJobRecovery(r.JobID, "attest-no-start")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.PlanToken).To(gomega.BeEmpty())
		gomega.Expect(s.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.MatchError(managed.ErrBlocked))
		snap, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(snap.JobState).To(gomega.Equal("reconciling"))
		gomega.Expect(snap.GlobalOccupancy).To(gomega.Equal(1))
		var count int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE kind='clock_discontinuity'`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.Equal(1))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_runs`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.BeZero())
	})
	ginkgo.It("fences read-only plans and preserves explicit provenance and permanent replay across controller rotation", func() {
		db, s, r, _, _ := cancellationRuntime(ginkgo.GinkgoT(), "true")
		gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
		path := "/v1/jobs/" + r.JobID + "/recover"
		var before, after int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&before)).To(gomega.Succeed())
		var p jobRecoveryPlan
		for range 3 {
			w := recoveryHTTP(s, path, managed.RecoveryRequest{Plan: true, Action: "attest-no-start"})
			gomega.Expect(w.Code).To(gomega.Equal(200))
			recoveryData(w, &p)
			gomega.Expect(p.PlanToken).NotTo(gomega.BeEmpty())
		}
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&after)).To(gomega.Succeed())
		gomega.Expect(after).To(gomega.Equal(before))
		evidence := recoveryObservation("scoped-no-start", "fixture", "job:"+r.JobID)
		evidence.Source, evidence.Observer = "operator-attested", "fixture-operator"
		q := managed.RecoveryRequest{PlanToken: p.PlanToken, MutationKey: "public-recovery", Evidence: &evidence}
		bad := q
		missing := evidence
		missing.Reference = ""
		bad.Evidence = &missing
		gomega.Expect(recoveryHTTP(s, path, bad).Code).To(gomega.Equal(400))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events`).Scan(&after)).To(gomega.Succeed())
		gomega.Expect(after).To(gomega.Equal(before))
		_, err := s.ledger.Cancel(managed.CancelRequest{JobID: r.JobID, ExpectedRevision: p.Snapshot.Receipt.JobRevision, MutationKey: "stale-plan-cancel"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		w := recoveryHTTP(s, path, q)
		gomega.Expect(w.Body.String()).To(gomega.ContainSubstring("STALE_REVISION"))
		w = recoveryHTTP(s, path, managed.RecoveryRequest{Plan: true, Action: "attest-no-start"})
		gomega.Expect(w.Code).To(gomega.Equal(200))
		recoveryData(w, &p)
		q.PlanToken = p.PlanToken
		w = recoveryHTTP(s, path, q)
		gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
		var receipt managed.Receipt
		recoveryData(w, &receipt)
		gomega.Eventually(func() string { snap, _ := s.ledger.Snapshot(r.JobID); return snap.JobState }, 3*time.Second).Should(gomega.Equal("finished"))
		result, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.Observations.OperatorAttested).To(gomega.BeTrue())
		fresh, err := newRuntime(s.root, s.ledger, s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fresh.recoveryKey).NotTo(gomega.Equal(s.recoveryKey))
		w = recoveryHTTP(fresh, path, q)
		gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
		var replay managed.Receipt
		recoveryData(w, &replay)
		gomega.Expect(replay).To(gomega.Equal(receipt))
		q.PlanToken = "different-token"
		gomega.Expect(recoveryHTTP(fresh, path, q).Body.String()).To(gomega.ContainSubstring("CONFLICT"))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE kind='recovery_intent'`).Scan(&after)).To(gomega.Succeed())
		gomega.Expect(after).To(gomega.Equal(1))
	})
	ginkgo.It("refuses occupied resource recovery before any availability effect", func() {
		_, s, r, _, _ := cancellationRuntime(ginkgo.GinkgoT(), "true")
		gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
		before, err := s.ledger.ResourceStatus(r.ResourceID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		w := recoveryHTTP(s, "/v1/resources/"+r.ResourceID+"/recover", managed.RecoveryRequest{Plan: true})
		gomega.Expect(w.Code).To(gomega.Equal(409))
		gomega.Expect(w.Body.String()).To(gomega.ContainSubstring("RECOVERY_BLOCKED"))
		after, err := s.ledger.ResourceStatus(r.ResourceID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
	})
	ginkgo.It("retains ownership of an observer that outlives its observation deadline", func() {
		_, s, _, _, _ := cancellationRuntime(ginkgo.GinkgoT(), "true")
		release := make(chan struct{})
		defer close(release)
		_, _, err := observeWithinRunBudgetWithWorker(time.Now().Add(-supervisorObservationSlack+20*time.Millisecond), 0, s.startWorker, s.detached, func() (bool, *os.File, error) {
			<-release
			return false, nil, nil
		})
		gomega.Expect(err).To(gomega.MatchError(runner.ErrUnproven))
		s.detachWorkers()
		stopped := make(chan struct{})
		go func() { s.workers.Wait(); close(stopped) }()
		gomega.Consistently(stopped, 50*time.Millisecond, 10*time.Millisecond).ShouldNot(gomega.BeClosed())
	})
})
