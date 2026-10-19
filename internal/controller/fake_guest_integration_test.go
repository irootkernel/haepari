//go:build integration

package controller

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

const fakeGuestUUID = "11111111-1111-4111-8111-111111111111"

type fakeGuestOwner struct {
	VMUUID   string                `json:"vm_uuid"`
	Boot     string                `json:"guest_boot"`
	Identity managed.LocalIdentity `json:"identity"`
	HostRoot managed.RetainedPath  `json:"host_root"`
}

// Separate filesystem-backed guest model. It invokes no UTM or SSH. Workload
// bytes and termination are observed through the actual local native runner;
// only backend source/cleanup/health operations are modeled here.
type fakeGuestBackend struct{ guest, host localEvidenceBackend }

func attachFakeGuest(s *runtimeServer) error {
	root, err := state.Resolve(filepath.Join(filepath.Dir(s.root.Path), "guest"))
	if err != nil {
		return err
	}
	guest := &runtimeServer{root: root, ledger: s.ledger, store: s.store}
	b := fakeGuestBackend{localEvidenceBackend{guest}, localEvidenceBackend{s}}
	s.backendEvidence = func(kind, id string) (evidenceBackend, error) {
		if kind != "utm-macos" || id != "local-cancel" {
			return nil, managed.ErrUnsupported
		}
		return b, nil
	}
	return nil
}

func (b fakeGuestBackend) owner(id managed.LocalIdentity) (fakeGuestOwner, error) {
	var proof fakeGuestOwner
	f, err := openOwnedEvidence(b.guest.s.root.Path, id.JobID, "fake-guest.json")
	if err != nil {
		return proof, err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if d.Decode(&proof) != nil || proof.VMUUID != fakeGuestUUID || proof.Boot != "bounded-fake-guest-boot" || proof.Identity != id {
		return proof, managed.ErrBlocked
	}
	root, err := b.host.Observe(id, "dispose", time.Time{})
	if err != nil || root != proof.HostRoot {
		return proof, managed.ErrBlocked
	}
	return proof, nil
}
func (b fakeGuestBackend) Observe(id managed.LocalIdentity, action string, until time.Time) (managed.RetainedPath, error) {
	if _, err := b.owner(id); err != nil {
		return managed.RetainedPath{}, err
	}
	return b.guest.Observe(id, action, until)
}
func (b fakeGuestBackend) Collect(j managed.EvidenceRecoveryJob, destination string) collectionAttempt {
	root, err := b.Observe(j.Identity, "recollect", time.Time{})
	if err != nil || root != j.Root {
		return collectionAttempt{}
	}
	return b.guest.Collect(j, destination)
}
func (b fakeGuestBackend) Cleanup(j managed.EvidenceRecoveryJob) error {
	proof, err := b.owner(j.Identity)
	if err != nil {
		return err
	}
	if err = b.guest.Cleanup(j); err != nil {
		return err
	}
	j.Root = proof.HostRoot
	return b.host.Cleanup(j)
}
func (b fakeGuestBackend) Health(snap managed.ResourceRecoverySnapshot) (any, bool) {
	g, guestReady := b.guest.Health(snap)
	h, hostReady := b.host.Health(snap)
	return map[string]any{"fake_vm_uuid": fakeGuestUUID, "guest": g, "host": h}, guestReady && hostReady
}

func protectedGuestFixture() (*runtimeServer, managed.Receipt, managed.Result) {
	t := ginkgo.GinkgoT()
	_, s, r, job, object := cancellationRuntimeKind(t, "mkdir -p ../results; printf guest-required > ../results/output.txt", "utm-macos")
	guestRoot, err := state.Resolve(filepath.Join(filepath.Dir(s.root.Path), "guest"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(guestRoot.Prepare()).To(gomega.Succeed())
	c, err := s.registerLocal(r)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer s.unregisterLocal(c)
	c.beforePhase = func(phase string) {
		if phase != "collecting" {
			return
		}
		// The fake guest keeps its own required bytes before the host collector
		// encounters an actual destination fault. Both sources remain owned.
		hostJob := filepath.Join(s.root.Path, "jobs", r.JobID)
		guestJob := filepath.Join(guestRoot.Path, "jobs", r.JobID)
		gomega.Expect(os.MkdirAll(filepath.Join(guestJob, "workspace/results"), 0700)).To(gomega.Succeed())
		input, err := os.ReadFile(filepath.Join(hostJob, "input.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.WriteFile(filepath.Join(guestJob, "input.json"), input, 0600)).To(gomega.Succeed())
		bytes, err := os.ReadFile(filepath.Join(hostJob, "workspace/results/output.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.WriteFile(filepath.Join(guestJob, "workspace/results/output.txt"), bytes, 0600)).To(gomega.Succeed())
		root, err := (localEvidenceBackend{s}).Observe(c.identity, "recollect", time.Time{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		proof, _ := json.Marshal(fakeGuestOwner{fakeGuestUUID, "bounded-fake-guest-boot", c.identity, root})
		gomega.Expect(os.WriteFile(filepath.Join(guestJob, "fake-guest.json"), proof, 0600)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(hostJob, "artifacts"), []byte("host collector fault"), 0600)).To(gomega.Succeed())
	}
	gomega.Expect(s.executeLocalControlled(c, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
	original, err := s.ledger.Result(r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(original.FinalVerdict).To(gomega.Equal(managed.Incomplete))
	gomega.Expect(original.ProtectedEvidence).NotTo(gomega.BeEmpty())
	gomega.Expect(os.Remove(filepath.Join(s.root.Path, "jobs", r.JobID, "artifacts"))).To(gomega.Succeed())
	gomega.Expect(attachFakeGuest(s)).To(gomega.Succeed())
	return s, r, original
}

var _ = ginkgo.Describe("Task012FakeUTMProtectedEvidence", ginkgo.Label("integration"), func() {
	ginkgo.It("retains guest bytes after an unproven collector and expires its original recovery budget", func() {
		s, r, original := protectedGuestFixture()
		child := retentionChild(s, r.JobID, "guest-recollect", "collect-worker")
		q, receipt, _, err := s.ledger.EvidenceRecovery("evidence-recovery-restart-evidence", "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(receipt.ActiveOwner.PID).To(gomega.Equal(child.Process.Pid))
		fresh, err := newRuntime(s.root, s.ledger, s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(attachFakeGuest(fresh)).To(gomega.Succeed())
		gomega.Expect(fresh.resumeEvidenceRecovery(q)).To(gomega.MatchError(managed.ErrBlocked))
		gomega.Expect(child.Process.Kill()).To(gomega.Succeed())
		gomega.Expect(child.Wait()).To(gomega.HaveOccurred())
		time.Sleep(max(0, time.Until(q.Jobs[0].CollectDeadline)) + 10*time.Millisecond)
		gomega.Expect(fresh.resumeEvidenceRecovery(q)).To(gomega.Succeed())
		_, receipt, _, err = s.ledger.EvidenceRecovery(q.MutationKey, q.ClientDigest)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(receipt.Jobs[0].Stage).To(gomega.Equal("failed"))
		bytes, err := os.ReadFile(filepath.Join(filepath.Dir(s.root.Path), "guest/jobs", r.JobID, "workspace/results/output.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(bytes)).To(gomega.Equal("guest-required"))
		current, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current).To(gomega.Equal(original))
		plan, err := fresh.planResourceRecovery(r.ResourceID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.PlanToken).To(gomega.BeEmpty())
	})
	for _, action := range []string{"recollect", "dispose"} {
		action := action
		ginkgo.It("retains guest references through a native controller restart and explicit "+action, func() {
			s, r, original := protectedGuestFixture()
			cold, err := newRuntime(s.root, s.ledger, s.store)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			inspection, err := cold.planResourceRecovery(r.ResourceID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(inspection.Snapshot.Resource.Kind).To(gomega.Equal("utm-macos"))
			gomega.Expect(inspection.EvidenceLoss).To(gomega.Equal(original.ProtectedEvidence))
			gomega.Expect(inspection.PlanToken).To(gomega.BeEmpty())
			boundary := "collection-recorded"
			if action == "dispose" {
				boundary = "evidence-disposal-intent"
			}
			child := retentionChild(s, r.JobID, "guest-"+action, boundary)
			q, _, _, err := s.ledger.EvidenceRecovery("evidence-recovery-restart-evidence", "")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if action == "dispose" {
				gomega.Expect(q.AuthorityReference).To(gomega.Equal("request:" + q.MutationKey))
				gomega.Expect(q.Jobs[0].Protected).To(gomega.Equal(original.ProtectedEvidence))
			}
			fresh, err := newRuntime(s.root, s.ledger, s.store)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(attachFakeGuest(fresh)).To(gomega.Succeed())
			gomega.Expect(fresh.resumeEvidenceRecovery(q)).To(gomega.MatchError(managed.ErrBlocked))
			prior, err := s.ledger.Request("cancel-key")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			raw, err := json.Marshal(prior.Spec)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			queued, err := s.ledger.Admit(raw, prior.Spec.Input.InventoryDigest, "guest-successor", "guest-next")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			status, err := s.ledger.ResourceStatus(r.ResourceID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = s.ledger.Reserve(managed.ReserveRequest{JobID: queued.JobID, ExpectedJobRevision: queued.JobRevision, ExpectedConfigRevision: status.ConfigRevision, ExpectedAvailabilityRevision: status.AvailabilityRevision, MutationKey: "guest-successor-reserve"})
			gomega.Expect(err).To(gomega.MatchError(managed.ErrBusy))
			gomega.Expect(child.Process.Kill()).To(gomega.Succeed())
			gomega.Expect(child.Wait()).To(gomega.HaveOccurred())
			gomega.Expect(fresh.resumeEvidenceRecovery(q)).To(gomega.Succeed())
			current, err := fresh.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current).To(gomega.Equal(original))
			health, err := fresh.planResourceRecovery(r.ResourceID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(health.PlanToken).NotTo(gomega.BeEmpty())
			w := recoveryHTTP(fresh, "/v1/resources/"+r.ResourceID+"/recover", managed.RecoveryRequest{PlanToken: health.PlanToken, MutationKey: "guest-health"})
			gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
			status, err = fresh.ledger.ResourceStatus(r.ResourceID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(status.Availability).To(gomega.Equal("available"))
			current, err = fresh.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current).To(gomega.Equal(original))
			if action == "recollect" {
				m, err := fresh.artifactManifest(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(m.Supplementary).To(gomega.HaveLen(1))
				bytes, err := os.ReadFile(filepath.Join(fresh.root.Path, "jobs", r.JobID, m.Supplementary[0].ArtifactRoot, "results/output.txt"))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(string(bytes)).To(gomega.Equal("guest-required"))
			}
		})
	}
})
