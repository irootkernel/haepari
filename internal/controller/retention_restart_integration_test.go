//go:build integration

package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func retentionRestartFixture(path, jobID, action, stop string) int {
	root, err := state.Resolve(path)
	if err != nil {
		return 90
	}
	db, err := sqlite.Open(root.DBPath)
	if err != nil {
		return 90
	}
	defer db.Close()
	l, err := managed.NewLedger(db, 1)
	if err != nil {
		return 90
	}
	store, err := snapshot.OpenStore(root.Path)
	if err != nil {
		return 90
	}
	s, err := newRuntime(root, l, store)
	if err != nil {
		return 90
	}
	if strings.HasPrefix(action, "guest-") {
		if attachFakeGuest(s) != nil {
			return 90
		}
		action = strings.TrimPrefix(action, "guest-")
	}
	retentionBoundary = func(stage string) {
		if stage == stop {
			if os.WriteFile(filepath.Join(root.Path, "retention-boundary"), []byte(stage), 0600) != nil {
				os.Exit(90)
			}
			for {
				time.Sleep(time.Second)
			}
		}
	}
	r, err := l.Snapshot(jobID)
	if err != nil {
		return 90
	}
	if action == "purge" {
		_, _ = s.purgeJob(jobID, r.JobRevision)
	} else {
		plan, err := s.planEvidenceRecovery(r.ResourceID, action)
		if err != nil {
			return 90
		}
		_ = recoveryHTTP(s, "/v1/resources/"+r.ResourceID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "restart-evidence"})
	}
	// Match the controller's lifetime rule even after its bounded observer
	// returns: an unproven worker keeps the process and store owned.
	s.workers.Wait()
	return 91
}

func retentionChild(s *runtimeServer, id, action, boundary string) *exec.Cmd {
	t := ginkgo.GinkgoT()
	executable, err := os.Executable()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	child := exec.Command(executable, "retention-restart-fixture", s.root.Path, id, action, boundary)
	child.Env = []string{"PATH=/usr/bin:/bin", "GORACE=atexit_sleep_ms=0"}
	gomega.Expect(child.Start()).To(gomega.Succeed())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	gomega.Eventually(func() string {
		raw, _ := os.ReadFile(filepath.Join(s.root.Path, "retention-boundary"))
		return string(raw)
	}, 5*time.Second).Should(gomega.Equal(boundary))
	return child
}

var _ = ginkgo.Describe("Task012NativeRetentionRestart", ginkgo.Label("integration"), func() {
	for _, boundary := range []string{"collection-recorded", "verify-worker"} {
		boundary := boundary
		ginkgo.It("expires saved collected evidence at "+boundary+" without renewing verification or deleting source", func() {
			s, r, original := protectedLocalFixture()
			child := retentionChild(s, r.JobID, "recollect", boundary)
			q, receipt, found, err := s.ledger.EvidenceRecovery("evidence-recovery-restart-evidence", "")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(found).To(gomega.BeTrue())
			gomega.Expect(receipt.Jobs[0].Stage).To(gomega.Equal("collected"))
			if boundary == "verify-worker" {
				gomega.Expect(receipt.ActiveOwner.PID).To(gomega.Equal(child.Process.Pid))
			}
			fresh, err := newRuntime(s.root, s.ledger, s.store)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(fresh.resumeEvidenceRecovery(q)).To(gomega.MatchError(managed.ErrBlocked))
			time.Sleep(max(0, time.Until(q.Jobs[0].CleanupDeadline)) + 10*time.Millisecond)
			gomega.Expect(fresh.resumeEvidenceRecovery(q)).To(gomega.MatchError(managed.ErrBlocked))
			gomega.Expect(child.Process.Kill()).To(gomega.Succeed())
			gomega.Expect(child.Wait()).To(gomega.HaveOccurred())
			// An expired attempt must not even open/revalidate this changed
			// supplementary file; its original protected source remains intact.
			artifact := filepath.Join(s.root.Path, "jobs", r.JobID, receipt.Jobs[0].ArtifactRoot, receipt.Jobs[0].Artifacts[0].Path)
			gomega.Expect(os.WriteFile(artifact, []byte("corrupt"), 0600)).To(gomega.Succeed())
			fresh.evidenceMu.Lock()
			gomega.Expect(fresh.resumeEvidenceRecovery(q)).To(gomega.MatchError(managed.ErrBlocked))
			fresh.evidenceMu.Unlock()
			started := time.Now()
			gomega.Expect(fresh.resumeEvidenceRecovery(q)).To(gomega.Succeed())
			gomega.Expect(time.Since(started)).To(gomega.BeNumerically("<", time.Second))
			prior, complete, _, err := s.ledger.EvidenceRecovery(q.MutationKey, q.ClientDigest)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(prior).To(gomega.Equal(q))
			gomega.Expect(complete.Complete).To(gomega.BeTrue())
			gomega.Expect(complete.ActiveOwner).To(gomega.BeNil())
			gomega.Expect(complete.Jobs[0].Stage).To(gomega.Equal("failed"))
			gomega.Expect(complete.Jobs[0].Gap).To(gomega.Equal("original evidence recovery deadline expired"))
			gomega.Expect(os.ReadFile(filepath.Join(s.root.Path, "jobs", r.JobID, "workspace/results/output.txt"))).To(gomega.Equal([]byte("recoverable")))
			gomega.Expect(os.ReadFile(artifact)).To(gomega.Equal([]byte("corrupt")))
			current, err := s.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current).To(gomega.Equal(original))
			status, err := s.ledger.ResourceStatus(r.ResourceID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(status.Availability).To(gomega.Equal("quarantined"))
			plan, err := fresh.planEvidenceRecovery(r.ResourceID, "dispose")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(plan.EvidenceLoss).To(gomega.Equal(original.ProtectedEvidence))
			gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
		})
	}
	for _, boundary := range []string{"collection-recorded", "collect-worker"} {
		boundary := boundary
		ginkgo.It("keeps a live "+boundary+" owner fenced and resumes only after its observed death", func() {
			s, r, original := protectedLocalFixture()
			child := retentionChild(s, r.JobID, "recollect", boundary)
			q, receipt, found, err := s.ledger.EvidenceRecovery("evidence-recovery-restart-evidence", "")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(found).To(gomega.BeTrue())
			gomega.Expect(q.Owner.PID).To(gomega.Equal(child.Process.Pid))
			fresh, err := newRuntime(s.root, s.ledger, s.store)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(fresh.resumeEvidenceRecovery(q)).To(gomega.MatchError(managed.ErrBlocked))
			gomega.Expect(child.Process.Kill()).To(gomega.Succeed())
			gomega.Expect(child.Wait()).To(gomega.HaveOccurred())
			if boundary == "collect-worker" {
				gomega.Expect(receipt.ActiveOwner.PID).To(gomega.Equal(child.Process.Pid))
				time.Sleep(max(0, time.Until(q.Jobs[0].CollectDeadline)) + 10*time.Millisecond)
			} else {
				gomega.Expect(receipt.Jobs[0].Stage).To(gomega.Equal("collected"))
			}
			gomega.Expect(fresh.resumeEvidenceRecovery(q)).To(gomega.Succeed())
			prior, result, _, err := s.ledger.EvidenceRecovery(q.MutationKey, q.ClientDigest)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(prior).To(gomega.Equal(q))
			gomega.Expect(result.Complete).To(gomega.BeTrue())
			want := "resolved"
			if boundary == "collect-worker" {
				want = "failed"
			}
			gomega.Expect(result.Jobs[0].Stage).To(gomega.Equal(want))
			current, err := s.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current).To(gomega.Equal(original))
			if want == "failed" {
				gomega.Expect(os.Stat(filepath.Join(s.root.Path, "jobs", r.JobID, "workspace/results/output.txt"))).Error().NotTo(gomega.HaveOccurred())
				plan, err := fresh.planResourceRecovery(r.ResourceID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(plan.PlanToken).To(gomega.BeEmpty())
			} else {
				_, err := os.Stat(filepath.Join(s.root.Path, "jobs", r.JobID, "workspace"))
				gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			}
		})
	}
	ginkgo.It("retries a partially deleted Job, refuses a replacement parent and retains permanent replay", func() {
		_, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "mkdir -p ../results; printf done > ../results/output.txt")
		gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		child := retentionChild(s, r.JobID, "purge", "purge-leaf-removed")
		q, found, complete, err := s.ledger.PurgeIntent(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(found).To(gomega.BeTrue())
		gomega.Expect(complete).To(gomega.BeFalse())
		gomega.Expect(child.Process.Kill()).To(gomega.Succeed())
		gomega.Expect(child.Wait()).To(gomega.HaveOccurred())
		parent := filepath.Join(s.root.Path, "jobs", r.JobID, "artifacts/results")
		saved := parent + "-saved"
		gomega.Expect(os.Rename(parent, saved)).To(gomega.Succeed())
		gomega.Expect(os.Mkdir(parent, 0700)).To(gomega.Succeed())
		foreign := filepath.Join(parent, "output.txt")
		gomega.Expect(os.WriteFile(foreign, []byte("foreign"), 0600)).To(gomega.Succeed())
		fresh, err := newRuntime(s.root, s.ledger, s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = fresh.purgeJob(r.JobID, q.ExpectedRevision)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrBlocked))
		bytes, err := os.ReadFile(foreign)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(bytes)).To(gomega.Equal("foreign"))
		gomega.Expect(os.Remove(foreign)).To(gomega.Succeed())
		gomega.Expect(os.Remove(parent)).To(gomega.Succeed())
		gomega.Expect(os.Rename(saved, parent)).To(gomega.Succeed())
		p, err := fresh.purgeJob(r.JobID, q.ExpectedRevision)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.Complete).To(gomega.BeTrue())
		prior, err := fresh.ledger.Request("cancel-key")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(prior.Receipt.JobID).To(gomega.Equal(r.JobID))
		gomega.Expect(prior.Receipt.ResultStatus).To(gomega.Equal("unavailable"))
	})
})
