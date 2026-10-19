//go:build integration

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

// Seed an exited/false-proof fixture only after the actual native supervisor
// has exited. This deliberately synthetic receipt is confined to test state;
// it models the immutable native_observation_uncertain campaign outcome.
func falseVMExit(f *vmJobFixture, jobID string) []byte {
	path := filepath.Join(f.guest, jobID, "exit.json")
	var exit runner.Exit
	gomega.Eventually(func() bool {
		raw, err := os.ReadFile(path)
		return err == nil && json.Unmarshal(raw, &exit) == nil
	}, 6*time.Second, 25*time.Millisecond).Should(gomega.BeTrue())
	var intent runner.Intent
	raw, err := os.ReadFile(filepath.Join(f.guest, jobID, "intent.json"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(json.Unmarshal(raw, &intent)).To(gomega.Succeed())
	gomega.Eventually(func() bool {
		p, err := platform.ObserveProcess(intent.Supervisor.PID)
		return errors.Is(err, unix.ESRCH) || err == nil && p.Identity != intent.Supervisor
	}, 4*time.Second, 25*time.Millisecond).Should(gomega.BeTrue())
	exit.TerminationProven, exit.Reason = false, "native_observation_uncertain"
	raw, err = json.Marshal(exit)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(os.WriteFile(path, raw, 0600)).To(gomega.Succeed())
	return raw
}

var _ = ginkgo.Describe("Task015VMNativeEvidencePlan", ginkgo.Label("integration"), func() {
	ginkgo.It("preserves completed cleanup control after a lost health response so observation can retry", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf retained > ../results/output.txt")
		f.s.vmWrite = func(*os.File, []byte) (int, error) { return 0, unix.ENOSPC }
		original := f.run("vm-cleanup-health-loss")
		q, err := f.s.vmBinding(original.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// Select this fixture effect budget before issuing cleanup; never renew it.
		gomega.Expect(f.s.cleanupVM(q, time.Now().Add(30*time.Second))).To(gomega.Succeed())
		control := filepath.Join(f.guest, ".haepari-control-"+original.JobID+".json")
		before, err := os.ReadFile(control)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		native := f.s.sshCommand
		lost := false
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			r, err := native(ctx, exe, args, input)
			if vmTransportRequest(input).Operation == "health" && !lost && err == nil {
				lost = true
				return utm.CommandResult{}, errors.New("fixture lost completed health response")
			}
			return r, err
		}
		b := vmEvidenceBackend{f.s, f.spec.ID}
		_, err = b.Observe(q.Request.Identity, "dispose", time.Now().Add(recoveryObservationBudget))
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(lost).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(control)).To(gomega.Equal(before))
		root, err := b.Observe(q.Request.Identity, "dispose", time.Now().Add(recoveryObservationBudget))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(root).To(gomega.Equal(*q.Root))
		current, err := f.s.ledger.Result(original.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current).To(gomega.Equal(original))
	})

	ginkgo.It("observes an already completed deletion through native health without reopening absent files", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf retained > ../results/output.txt")
		f.s.vmWrite = func(*os.File, []byte) (int, error) { return 0, unix.ENOSPC }
		original := f.run("vm-cleanup-reply-recovery")
		q, err := f.s.vmBinding(original.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// Select this fixture effect budget before issuing cleanup; never renew it.
		gomega.Expect(f.s.cleanupVM(q, time.Now().Add(30*time.Second))).To(gomega.Succeed())
		b := vmEvidenceBackend{f.s, f.spec.ID}
		root, err := b.Observe(q.Request.Identity, "dispose", time.Now().Add(recoveryObservationBudget))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(root).To(gomega.Equal(*q.Root))
		_, err = b.Observe(q.Request.Identity, "recollect", time.Now().Add(recoveryObservationBudget))
		gomega.Expect(err).To(gomega.HaveOccurred())
		current, err := f.s.ledger.Result(original.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current).To(gomega.Equal(original))
	})

	ginkgo.It("blocks recollection and disposal after controller attestation without upgrading guest proof", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf x >> ../results/output.txt")
		native := f.s.sshCommand
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			r, err := native(ctx, exe, args, input)
			if vmTransportRequest(input).Operation == "start" && err == nil {
				return utm.CommandResult{}, errors.New("fixture lost start observer")
			}
			return r, err
		}
		r, err := f.reserve(f.admit("vm-attested-native-false"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(errors.Is(f.s.executeVM(r, f.job, f.object, time.Now().Add(10*time.Second)), utm.ErrGuestUncertain)).To(gomega.BeTrue())
		exitBytes := falseVMExit(f, r.JobID)
		f.s.sshCommand = native
		gomega.Expect(f.s.ledger.MarkInterrupted()).To(gomega.Succeed())
		plan, err := f.s.planJobRecovery(r.JobID, "attest-stopped")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.Unknowns).To(gomega.BeEmpty())
		gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
		e := managed.RecoveryEvidence{Source: "operator-attested", Observer: "test", ObservedAt: time.Now().UTC(), Mechanism: "bounded native fixture stopped", Digest: managed.RecoveryDigest(plan.Observation), Reference: "job:" + r.JobID}
		w := recoveryHTTP(f.s, "/v1/jobs/"+r.JobID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "attest-original", Evidence: &e})
		gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
		f.s.workers.Wait()
		original, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(original.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(original.Observations.OperatorAttested).To(gomega.BeTrue())
		gomega.Expect(original.ProtectedEvidence).NotTo(gomega.BeEmpty())
		for _, action := range []string{"recollect", "dispose"} {
			p, err := f.s.planEvidenceRecovery(f.spec.ID, action)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(p.PlanToken).To(gomega.BeEmpty())
			gomega.Expect(p.Unknowns).To(gomega.ContainElement(gomega.ContainSubstring("native termination proof")))
			gomega.Expect(p.Effects).To(gomega.BeEmpty())
		}
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, r.JobID, "exit.json"))).To(gomega.Equal(exitBytes))
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, r.JobID, "workspace/results/output.txt"))).To(gomega.Equal([]byte("x")))
		current, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current).To(gomega.Equal(original))
		status, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Occupancy).To(gomega.BeZero())
		gomega.Expect(status.Availability).To(gomega.Equal("quarantined"))
	})

	for _, action := range []string{"recollect", "dispose"} {
		ginkgo.It("revalidates native proof before applying an earlier "+action+" plan", func() {
			f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf retained > ../results/output.txt")
			f.s.vmWrite = func(*os.File, []byte) (int, error) { return 0, unix.ENOSPC }
			original := f.run("vm-stale-native-proof")
			p, err := f.s.planEvidenceRecovery(f.spec.ID, action)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(p.PlanToken).NotTo(gomega.BeEmpty())
			exitBytes := falseVMExit(f, original.JobID)
			w := recoveryHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: p.PlanToken, MutationKey: "stale-proof-" + action})
			gomega.Expect(w.Code).NotTo(gomega.Equal(200))
			snap, err := f.s.ledger.ResourceRecoverySnapshot(f.spec.ID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(snap.EvidenceIntent).To(gomega.BeNil())
			gomega.Expect(os.ReadFile(filepath.Join(f.guest, original.JobID, "exit.json"))).To(gomega.Equal(exitBytes))
			gomega.Expect(os.ReadFile(filepath.Join(f.guest, original.JobID, "workspace/results/output.txt"))).To(gomega.Equal([]byte("retained")))
			current, err := f.s.ledger.Result(original.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current).To(gomega.Equal(original))
		})
	}
})
