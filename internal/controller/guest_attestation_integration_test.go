//go:build integration

package controller

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

func attestedVMFixture() *vmJobFixture {
	f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf retained > ../results/output.txt; printf once > once.txt")
	f.job.Deadlines.CollectSeconds = 30
	f.job.Deadlines.CleanupSeconds = 30
	// Run the actual fixed bootstrap and transferred executable. Worker birth
	// receipts therefore belong to real short-lived native guest processes.
	f.s.sshCommand = func(ctx context.Context, _ string, args []string, input []byte) (utm.CommandResult, error) {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", args[len(args)-1])
		cmd.Stdin = bytes.NewReader(input)
		cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "GORACE=atexit_sleep_ms=0"}
		cmd.WaitDelay = time.Second
		var out, diag bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &diag
		err := cmd.Run()
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		return utm.CommandResult{Stdout: out.String(), Stderr: diag.String(), ExitCode: code}, err
	}
	return f
}
func guestEvidence(plan resourceRecoveryPlan) managed.RecoveryEvidence {
	raw, _ := json.Marshal(plan.Observation)
	var replies []runner.GuestRecoveryReply
	gomega.Expect(json.Unmarshal(raw, &replies)).To(gomega.Succeed())
	gomega.Expect(replies).To(gomega.HaveLen(1))
	return managed.RecoveryEvidence{Source: "operator-attested", Observer: "owned-native-fixture", ObservedAt: time.Now().UTC(), Mechanism: "guest-workload-and-descendants-stopped", Digest: managed.RecoveryDigest(*replies[0].Basis), Reference: "job:" + replies[0].Basis.Request.Identity.JobID}
}
func applyGuestPlan(f *vmJobFixture, plan resourceRecoveryPlan, key string, evidence managed.RecoveryEvidence) managed.RecoveryRequest {
	q := managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: key, Evidence: &evidence}
	response := vmMutationHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", q)
	gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
	return q
}

var _ = ginkgo.Describe("GuestAttestedRecovery", ginkgo.Label("integration"), func() {
	ginkgo.It("retains source and quarantine when exact existing host artifact parent sync fails", func() {
		f := attestedVMFixture()
		f.s.vmWrite = func(*os.File, []byte) (int, error) { return 0, unix.ENOSPC }
		original := f.run("guest-existing-sync-failure")
		exit := falseVMExit(f, original.JobID)
		f.s.vmWrite = nil
		plan, err := f.s.planGuestAttestation(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		applyGuestPlan(f, plan, "guest-sync-attest", guestEvidence(plan))
		plan, err = f.s.planEvidenceRecovery(f.spec.ID, "recollect")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
		key := "guest-sync-collect"
		target := filepath.Join(f.s.root.Path, "jobs", original.JobID, "recovery-evidence-recovery-"+key, "results/output.txt")
		gomega.Expect(os.MkdirAll(filepath.Dir(target), 0700)).To(gomega.Succeed())
		// Synthetic exact-existing interrupted publication; the separate native
		// child test exercises the real death immediately after publication.
		gomega.Expect(os.WriteFile(target, []byte("retained"), 0600)).To(gomega.Succeed())
		calls := 0
		f.s.vmSyncParents = func(name, stop string) error {
			calls++
			gomega.Expect(name).To(gomega.Equal(target))
			gomega.Expect(stop).To(gomega.Equal(filepath.Join(f.s.root.Path, "jobs", original.JobID)))
			return unix.EIO
		}
		response := vmMutationHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: key})
		gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
		gomega.Expect(calls).To(gomega.Equal(1))
		_, receipt, found, err := f.s.ledger.EvidenceRecovery("evidence-recovery-"+key, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(found).To(gomega.BeTrue())
		gomega.Expect(receipt.Jobs[0].Stage).To(gomega.Equal("failed"))
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, original.JobID, "exit.json"))).To(gomega.Equal(exit))
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, original.JobID, "workspace/source/once.txt"))).To(gomega.Equal([]byte("once")))
		health, err := f.s.planResourceRecovery(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(health.PlanToken).To(gomega.BeEmpty())
		gomega.Expect(health.Snapshot.Resource.Availability).To(gomega.Equal("quarantined"))
		current, err := f.s.ledger.Result(original.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current).To(gomega.Equal(original))
	})
	for _, mode := range []string{"changed-bytes", "replacement-inode"} {
		mode := mode
		ginkgo.It("retains guest evidence and the original result after log "+mode, func() {
			f := attestedVMFixture()
			f.s.vmWrite = func(*os.File, []byte) (int, error) { return 0, unix.ENOSPC }
			original := f.run("guest-log-" + mode)
			exit := falseVMExit(f, original.JobID)
			f.s.vmWrite = nil
			plan, err := f.s.planGuestAttestation(f.spec.ID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			applyGuestPlan(f, plan, "guest-log-attest", guestEvidence(plan))
			path := filepath.Join(f.guest, original.JobID, "output.log")
			if mode == "replacement-inode" {
				data, err := os.ReadFile(path)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(os.Rename(path, path+"-fixture-original")).To(gomega.Succeed())
				gomega.Expect(os.WriteFile(path, data, 0600)).To(gomega.Succeed())
			} else {
				gomega.Expect(os.WriteFile(path, []byte("changed-native-log"), 0600)).To(gomega.Succeed())
			}
			plan, err = f.s.planEvidenceRecovery(f.spec.ID, "recollect")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
			response := vmMutationHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "guest-log-collect"})
			gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
			_, receipt, found, err := f.s.ledger.EvidenceRecovery("evidence-recovery-guest-log-collect", "")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(found).To(gomega.BeTrue())
			gomega.Expect(receipt.Jobs[0].Stage).To(gomega.Equal("failed"))
			gomega.Expect(os.ReadFile(filepath.Join(f.guest, original.JobID, "exit.json"))).To(gomega.Equal(exit))
			gomega.Expect(os.ReadFile(filepath.Join(f.guest, original.JobID, "workspace/source/once.txt"))).To(gomega.Equal([]byte("once")))
			health, err := f.s.planResourceRecovery(f.spec.ID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(health.PlanToken).To(gomega.BeEmpty())
			gomega.Expect(health.Snapshot.Resource.Availability).To(gomega.Equal("quarantined"))
			current, err := f.s.ledger.Result(original.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current).To(gomega.Equal(original))
		})
	}
	ginkgo.It("preserves false native proof and original result while recovering evidence and health through the explicit new protocol", func() {
		f := attestedVMFixture()
		f.s.vmWrite = func(*os.File, []byte) (int, error) { return 0, unix.ENOSPC }
		original := f.run("guest-attested-native")
		exit := falseVMExit(f, original.JobID)
		f.s.vmWrite = nil
		p, err := f.s.planGuestAttestation(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.Unknowns).To(gomega.BeEmpty())
		gomega.Expect(p.PlanToken).NotTo(gomega.BeEmpty())
		evidence := guestEvidence(p)
		basis := p.Observation.([]runner.GuestRecoveryReply)[0]
		savedRunner := filepath.Join(f.s.root.Path, "jobs", original.JobID, "guest-recovery-runner-"+basis.RecoveryDigest[7:])
		recoveryPublicationChild(savedRunner, "guest-recovery-runner-created")
		// The killed publication has no durable final-path authority. Finished
		// purge must preserve its unknown temporary bytes, not guess ownership.
		temporaries := map[string][]byte{}
		entries, err := os.ReadDir(filepath.Dir(savedRunner))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".guest-recovery-runner-") {
				path := filepath.Join(filepath.Dir(savedRunner), entry.Name())
				data, e := os.ReadFile(path)
				gomega.Expect(e).NotTo(gomega.HaveOccurred())
				temporaries[path] = data
			}
		}
		gomega.Expect(temporaries).To(gomega.HaveLen(1))
		_, err = os.Stat(savedRunner)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		_, err = os.Stat(filepath.Join(f.guest, ".haepari-attestation-"+original.JobID+".json"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		_, _, foundIntent, err := f.s.ledger.EvidenceRecovery("evidence-recovery-guest-native-attest", "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(foundIntent).To(gomega.BeFalse())
		bad := evidence
		bad.Mechanism = "PID absence only"
		response := vmMutationHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: p.PlanToken, MutationKey: "guest-invalid-evidence", Evidence: &bad})
		gomega.Expect(response.Code).NotTo(gomega.Equal(200))
		q := applyGuestPlan(f, p, "guest-native-attest", evidence)
		response = vmMutationHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", q)
		gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
		_, receipt, found, err := f.s.ledger.EvidenceRecovery("evidence-recovery-guest-native-attest", "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(found).To(gomega.BeTrue())
		gomega.Expect(receipt.Complete).To(gomega.BeTrue())
		gomega.Expect(receipt.Jobs[0].Stage).To(gomega.Equal("attested"))
		public, err := f.s.ledger.PublicSupplementaryEvidence(original.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(public).To(gomega.BeEmpty())
		request := httptest.NewRequest(http.MethodGet, "/v1/jobs/"+original.JobID, nil)
		request.SetPathValue("id", original.JobID)
		view := httptest.NewRecorder()
		f.s.inspect(view, request)
		gomega.Expect(view.Code).To(gomega.Equal(200), view.Body.String())
		var inspection managed.Envelope
		gomega.Expect(json.Unmarshal(view.Body.Bytes(), &inspection)).To(gomega.Succeed())
		var data map[string]json.RawMessage
		gomega.Expect(json.Unmarshal(inspection.Data, &data)).To(gomega.Succeed())
		gomega.Expect(data).NotTo(gomega.HaveKey("supplementary"))
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, original.JobID, "exit.json"))).To(gomega.Equal(exit))
		plan, err := f.s.planEvidenceRecovery(f.spec.ID, "dispose")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.PlanToken).To(gomega.BeEmpty())
		health, err := f.s.planResourceRecovery(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(health.PlanToken).To(gomega.BeEmpty())
		binding, err := f.s.vmBinding(original.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		binding.Operation = "start"
		binding.Deadline = time.Now().Add(5 * time.Second)
		_, err = f.s.vmCall(binding)
		gomega.Expect(err).To(gomega.HaveOccurred())
		plan, err = f.s.planEvidenceRecovery(f.spec.ID, "recollect")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
		response = vmMutationHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "guest-native-collect"})
		gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
		_, collected, _, err := f.s.ledger.EvidenceRecovery("evidence-recovery-guest-native-collect", "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(collected.Complete).To(gomega.BeTrue())
		gomega.Expect(collected.Jobs[0].Stage).To(gomega.Equal("resolved"), "%+v", collected.Jobs[0])
		gomega.Expect(collected.Jobs[0].GuestArtifacts).To(gomega.HaveLen(7))
		local := filepath.Join(f.s.root.Path, "jobs", original.JobID, collected.Jobs[0].ArtifactRoot)
		gomega.Expect(os.ReadFile(filepath.Join(local, "results/output.txt"))).To(gomega.Equal([]byte("retained")))
		gomega.Expect(os.ReadFile(filepath.Join(local+"-native", "exit.json"))).To(gomega.Equal(exit))
		_, err = os.Stat(filepath.Join(f.guest, original.JobID))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		current, err := f.s.ledger.Result(original.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current).To(gomega.Equal(original))
		manifest, err := f.s.artifactManifest(original.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(manifest.Files).To(gomega.HaveLen(8))
		encoded, err := json.Marshal(manifest)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var wire struct {
			Supplementary []json.RawMessage `json:"supplementary"`
		}
		gomega.Expect(json.Unmarshal(encoded, &wire)).To(gomega.Succeed())
		gomega.Expect(wire.Supplementary).To(gomega.HaveLen(1))
		for _, record := range wire.Supplementary {
			var legacy struct {
				JobID        string                   `json:"job_id"`
				ResultDigest string                   `json:"result_digest"`
				Stage        string                   `json:"stage"`
				ArtifactRoot string                   `json:"artifact_root,omitempty"`
				Artifacts    []managed.ResultArtifact `json:"artifacts"`
				Gap          string                   `json:"gap,omitempty"`
			}
			decoder := json.NewDecoder(bytes.NewReader(record))
			decoder.DisallowUnknownFields()
			gomega.Expect(decoder.Decode(&legacy)).To(gomega.Succeed())
			gomega.Expect(legacy.Stage).To(gomega.Equal("resolved"))
		}
		var nativeExit bool
		for _, file := range manifest.Files {
			if file.Path == collected.Jobs[0].ArtifactRoot+"-native/exit.json" {
				nativeExit = true
				gomega.Expect(file.PageToken).NotTo(gomega.BeEmpty())
			}
		}
		gomega.Expect(nativeExit).To(gomega.BeTrue())
		health, err = f.s.planResourceRecovery(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(health.PlanToken).NotTo(gomega.BeEmpty())
		response = vmMutationHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: health.PlanToken, MutationKey: "guest-native-health"})
		gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
		snap, err := f.s.ledger.Snapshot(original.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = f.s.purgeJob(original.JobID, snap.JobRevision)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for path, data := range temporaries {
			gomega.Expect(os.ReadFile(path)).To(gomega.Equal(data))
		}
		_, err = os.Stat(local + "-native")
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		_, err = os.Stat(filepath.Join(f.guest, ".haepari-attestation-"+original.JobID+".json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})
	ginkgo.It("reconciles the same attestation key after a lost reply without another workload or proof rewrite", func() {
		f := attestedVMFixture()
		f.s.vmWrite = func(*os.File, []byte) (int, error) { return 0, unix.ENOSPC }
		original := f.run("guest-attested-reply-loss")
		exit := falseVMExit(f, original.JobID)
		p, err := f.s.planGuestAttestation(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		evidence := guestEvidence(p)
		native := f.s.sshCommand
		lost := false
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			r, e := native(ctx, exe, args, input)
			if recoveryTransportOperation(input) == "attest" && !lost && e == nil {
				lost = true
				return utm.CommandResult{}, errors.New("fixture lost attestation reply")
			}
			return r, e
		}
		q := managed.RecoveryRequest{PlanToken: p.PlanToken, MutationKey: "guest-attestation-reply-loss", Evidence: &evidence}
		response := vmMutationHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", q)
		// The guest effect has completed but its reply is deliberately lost.
		// Reuse the durable host intent, original key and deadline.
		gomega.Expect(response.Code).NotTo(gomega.Equal(200))
		gomega.Expect(lost).To(gomega.BeTrue())
		f.s.sshCommand = native
		response = vmMutationHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", q)
		gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, original.JobID, "exit.json"))).To(gomega.Equal(exit))
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, original.JobID, "workspace/source/once.txt"))).To(gomega.Equal([]byte("once")))
	})
	ginkgo.It("refuses stale evidence basis and preserves quarantine when required collection is incomplete", func() {
		f := attestedVMFixture()
		f.s.vmWrite = func(*os.File, []byte) (int, error) { return 0, unix.ENOSPC }
		original := f.run("guest-attested-stale")
		falseVMExit(f, original.JobID)
		f.s.vmWrite = nil
		p, err := f.s.planGuestAttestation(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		evidence := guestEvidence(p)
		control := filepath.Join(f.guest, ".haepari-control-"+original.JobID+".json")
		lock, err := os.OpenFile(control, os.O_RDWR, 0)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)).To(gomega.Succeed())
		unknown, err := f.s.planGuestAttestation(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(unknown.PlanToken).To(gomega.BeEmpty())
		gomega.Expect(unknown.Unknowns).NotTo(gomega.BeEmpty())
		lock.Close()
		exitPath := filepath.Join(f.guest, original.JobID, "exit.json")
		prior, err := os.ReadFile(exitPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.WriteFile(exitPath, append(append([]byte{}, prior...), byte('\n')), 0600)).To(gomega.Succeed())
		stale := vmMutationHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: p.PlanToken, MutationKey: "guest-stale-basis", Evidence: &evidence})
		gomega.Expect(stale.Code).NotTo(gomega.Equal(200))
		_, _, found, err := f.s.ledger.EvidenceRecovery("evidence-recovery-guest-stale-basis", "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(found).To(gomega.BeFalse())
		gomega.Expect(os.WriteFile(exitPath, prior, 0600)).To(gomega.Succeed())

		applyGuestPlan(f, p, "guest-partial-attest", evidence)
		plan, err := f.s.planEvidenceRecovery(f.spec.ID, "recollect")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
		gomega.Expect(os.Remove(filepath.Join(f.guest, original.JobID, "workspace/results/output.txt"))).To(gomega.Succeed())
		response := vmMutationHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "guest-partial-collect"})
		gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
		_, receipt, _, err := f.s.ledger.EvidenceRecovery("evidence-recovery-guest-partial-collect", "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(receipt.Jobs[0].Stage).To(gomega.Equal("failed"))
		_, err = os.Stat(filepath.Join(f.guest, original.JobID))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		health, err := f.s.planResourceRecovery(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(health.PlanToken).To(gomega.BeEmpty())
		current, err := f.s.ledger.Result(original.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current).To(gomega.Equal(original))
	})
	ginkgo.It("observes owned absence after a lost cleanup reply without repeating cleanup", func() {
		f := attestedVMFixture()
		f.s.vmWrite = func(*os.File, []byte) (int, error) { return 0, unix.ENOSPC }
		original := f.run("guest-attested-cleanup-loss")
		falseVMExit(f, original.JobID)
		f.s.vmWrite = nil
		p, err := f.s.planGuestAttestation(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		applyGuestPlan(f, p, "guest-cleanup-attest", guestEvidence(p))
		plan, err := f.s.planEvidenceRecovery(f.spec.ID, "recollect")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		native := f.s.sshCommand
		cleanups := 0
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			r, e := native(ctx, exe, args, input)
			if recoveryTransportOperation(input) == "cleanup" {
				cleanups++
				if e == nil {
					return utm.CommandResult{}, errors.New("fixture lost completed cleanup reply")
				}
			}
			return r, e
		}
		response := vmMutationHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "guest-lost-cleanup"})
		gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
		_, receipt, _, err := f.s.ledger.EvidenceRecovery("evidence-recovery-guest-lost-cleanup", "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(receipt.Jobs[0].Stage).To(gomega.Equal("resolved"))
		gomega.Expect(cleanups).To(gomega.Equal(1))
	})

})

func recoveryTransportOperation(input []byte) string {
	r := tar.NewReader(bytes.NewReader(input))
	for {
		h, e := r.Next()
		if e != nil {
			return ""
		}
		if h.Name == "request.json" {
			var q runner.GuestRecoveryRequest
			if json.NewDecoder(r).Decode(&q) != nil {
				return ""
			}
			return q.Operation
		}
	}
}
