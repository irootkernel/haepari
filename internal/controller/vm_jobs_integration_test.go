//go:build integration

package controller

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

type vmJobFixture struct {
	s             *runtimeServer
	spec          managed.ResourceSpec
	job           managed.JobSpec
	object, guest string
	control       *fakeUTMControl
}

func nativeVMFixture(t ginkgo.FullGinkgoTInterface, script string) *vmJobFixture {
	s, _, _ := availabilityFixture(t)
	spec := vmControllerSpec(t, "vm-native", 1, false)
	guest, err := filepath.EvalSymlinks(t.TempDir())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(os.Chmod(guest, 0700)).To(gomega.Succeed())
	spec.VM.GuestJobRoot = guest
	interfaces, err := net.Interfaces()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	for _, i := range interfaces {
		if len(i.HardwareAddr) == 6 {
			spec.VM.MACAddress = i.HardwareAddr.String()
			break
		}
	}
	gomega.Expect(spec.VM.MACAddress).NotTo(gomega.HavePrefix("aa:bb:cc"))
	mappingRoot, err := filepath.EvalSymlinks(t.TempDir())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	key := filepath.Join(mappingRoot, "key")
	gomega.Expect(os.WriteFile(key, []byte("fixture-only"), 0600)).To(gomega.Succeed())
	mapping, _ := json.Marshal(utm.SSHMapping{Schema: "haepari.ssh-mapping/v1", User: "fixture", Port: 22, IdentityFile: key})
	file := filepath.Join(mappingRoot, "mapping.json")
	gomega.Expect(os.WriteFile(file, mapping, 0600)).To(gomega.Succeed())
	sum := sha256.Sum256(mapping)
	spec.Guest.ConnectionReference = "sha256:" + hex.EncodeToString(sum[:]) + ":" + file
	gomega.Expect(s.ledger.RegisterResource(mustJSONScheduling(spec))).To(gomega.Succeed())
	f := &fakeUTMControl{states: map[string]string{spec.VM.UTMVMUUID: "started"}}
	s.utmCommand = f.command
	s.vmLeases = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(fmt.Sprintf("{\nip_address=192.168.64.3\nhw_address=1,%s\nlease=%d\n}\n", spec.VM.MACAddress, time.Now().Add(time.Hour).Unix()))), nil
	}
	// Scripted SSH, never an actual VM: decode the exact product transport and
	// invoke the native guest command against its separate private filesystem.
	s.sshCommand = func(_ context.Context, _ string, _ []string, input []byte) (utm.CommandResult, error) {
		archive := tar.NewReader(bytes.NewReader(input))
		var raw []byte
		for {
			h, e := archive.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				return utm.CommandResult{}, e
			}
			if h.Name == "request.json" {
				raw, e = io.ReadAll(archive)
				if e != nil {
					return utm.CommandResult{}, e
				}
			}
		}
		var out, diag bytes.Buffer
		code := runner.GuestCommand(bytes.NewReader(raw), &out, &diag)
		return utm.CommandResult{Stdout: out.String(), Stderr: diag.String(), ExitCode: code}, nil
	}
	source := filepath.Join(t.TempDir(), "input")
	gomega.Expect(os.MkdirAll(filepath.Join(source, "source"), 0700)).To(gomega.Succeed())
	gomega.Expect(os.WriteFile(filepath.Join(source, "source/input.txt"), []byte("sealed"), 0600)).To(gomega.Succeed())
	sealed, err := s.store.Seal(context.Background(), source, []string{"source/input.txt"}, snapshot.Provenance{EvidenceClass: "development"})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	digest := strings.Repeat("a", 64)
	job := managed.JobSpec{Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: spec.ID, Mode: "auto", Suite: "vm-native", Input: managed.Input{Root: source, InventoryDigest: strings.TrimPrefix(sealed.Manifest.TreeSHA256, "sha256:"), SelectedFiles: []string{"source/input.txt"}}, Environment: managed.Environment{ProfileID: "development", ProfileDigest: digest}, Workload: managed.Workload{Argv: []string{"/bin/sh", "-c", script}, CWD: "source", RequiredOutputs: []string{"results/output.txt"}}, Deadlines: managed.Deadlines{QueueSeconds: 60, PrepareSeconds: 10, RunSeconds: 10, CollectSeconds: 5, CleanupSeconds: 5}, Effects: managed.Effects{Mode: "offline", AuthorizationReference: "scripted-native-guest"}, Provenance: managed.Provenance{CandidateDigest: digest, TestsDigest: digest, TargetProfileDigest: digest, EvidenceClass: "development"}}
	t.Cleanup(func() { s.workers.Wait() })
	return &vmJobFixture{s, spec, job, sealed.ObjectID, guest, f}
}
func (f *vmJobFixture) admit(id string) managed.Receipt {
	raw := mustJSONScheduling(f.job)
	snap, err := f.s.store.Read(f.object)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	inventory, _ := json.Marshal(snap.Manifest)
	r, err := f.s.ledger.AdmitWithParent(raw, f.job.Input.InventoryDigest, f.object, inventory, "key-"+id, id, f.s.installationID(), nil)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return r
}
func (f *vmJobFixture) reserve(r managed.Receipt) (managed.Receipt, error) {
	guard, err := f.s.observeVMs(context.Background(), 1)
	if err != nil {
		return r, err
	}
	status, err := f.s.ledger.ResourceStatus(f.spec.ID)
	if err != nil {
		return r, err
	}
	return f.s.ledger.Reserve(managed.ReserveRequest{JobID: r.JobID, ExpectedJobRevision: r.JobRevision, ExpectedConfigRevision: status.ConfigRevision, ExpectedAvailabilityRevision: status.AvailabilityRevision, MutationKey: "reserve-" + r.JobID, VMGuard: guard})
}
func (f *vmJobFixture) run(id string) managed.Result {
	r, err := f.reserve(f.admit(id))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	err = f.s.executeVM(r, f.job, f.object, time.Now().Add(10*time.Second))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	result, err := f.s.ledger.Result(id)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return result
}

var _ = ginkgo.Describe("Task015VMJobs", ginkgo.Label("integration"), func() {
	ginkgo.It("admits and dispatches a public VM Job through the production scheduler", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf public > ../results/output.txt")
		prior, err := f.s.ledger.Snapshot("queued-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = f.s.ledger.Cancel(managed.CancelRequest{JobID: prior.JobID, ExpectedRevision: prior.JobRevision, MutationKey: "remove-fixture-queue"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		w := vmMutationHTTP(f.s, "/v1/jobs", map[string]any{"spec": f.job, "request_key": "public-vm-request"})
		gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
		admitted, err := f.s.ledger.Request("public-vm-request")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); f.s.schedule(ctx) }()
		defer func() { cancel(); <-done; f.s.workers.Wait() }()
		gomega.Eventually(func() error { _, err := f.s.ledger.Result(admitted.Receipt.JobID); return err }, 12*time.Second, 25*time.Millisecond).Should(gomega.Succeed())
		result, err := f.s.ledger.Result(admitted.Receipt.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		gomega.Expect(os.ReadFile(filepath.Join(f.s.root.Path, "jobs", admitted.Receipt.JobID, "artifacts/results/output.txt"))).To(gomega.Equal([]byte("public")))
	})

	ginkgo.It("keeps a capacity-blocked VM queued under its original deadline while dispatching local work", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf independent > ../results/output.txt")
		prior, err := f.s.ledger.Snapshot("queued-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = f.s.ledger.Cancel(managed.CancelRequest{JobID: prior.JobID, ExpectedRevision: prior.JobRevision, MutationKey: "cancel-empty-fixture"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		blocked := f.admit("vm-capacity-blocked")
		before, err := f.s.ledger.Request("key-" + blocked.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f.control.states[f.spec.VM.UTMVMUUID] = "stopped"
		f.control.states["99999999-1111-4111-8111-111111111111"] = "started"
		f.job.ResourceID = "local-a"
		local := f.admit("local-after-vm-blocker")
		f.job.ResourceID = f.spec.ID
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); f.s.schedule(ctx) }()
		defer func() { cancel(); <-done; f.s.workers.Wait() }()
		gomega.Eventually(func() error { _, err := f.s.ledger.Result(local.JobID); return err }, 12*time.Second, 25*time.Millisecond).Should(gomega.Succeed())
		result, err := f.s.ledger.Result(local.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		after, err := f.s.ledger.Request("key-" + blocked.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after.Receipt.JobState).To(gomega.Equal("queued"))
		gomega.Expect(after.DeadlineNS).To(gomega.Equal(before.DeadlineNS))
		blocker, err := f.s.ledger.QueueBlocker(blocked.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(blocker).NotTo(gomega.BeNil())
		gomega.Expect(blocker.Reason).To(gomega.Equal("host_capacity_no_eligible_victim"))
		f.control.mu.Lock()
		effects := append([]string(nil), f.control.effects...)
		f.control.mu.Unlock()
		gomega.Expect(effects).To(gomega.BeEmpty())
	})

	ginkgo.It("collects authenticated native outputs before deleting only the Job and admitting a successor", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; cat input.txt > ../results/output.txt; printf log")
		// Use an explicit path within the isolated guest fixture for persistent
		// guest state. It is outside both Job roots and remains after every cleanup.
		f.job.Workload.Argv[2] = "mkdir -p ../results; cat input.txt > ../results/output.txt; printf log; printf x >> '" + filepath.Join(f.guest, "persistent.txt") + "'"
		for _, id := range []string{"vm-first", "vm-second"} {
			result := f.run(id)
			gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
			gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
			gomega.Expect(result.Logs).To(gomega.HaveLen(1))
			gomega.Expect(result.Artifacts).To(gomega.HaveLen(1))
			_, err := os.Stat(filepath.Join(f.guest, id))
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			data, err := os.ReadFile(filepath.Join(f.s.root.Path, "jobs", id, "artifacts/results/output.txt"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(string(data)).To(gomega.Equal("sealed"))
		}
		data, err := os.ReadFile(filepath.Join(f.guest, "persistent.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("xx"))
		gomega.Expect(f.control.effects).To(gomega.BeEmpty())
	})
	for _, action := range []string{"recollect", "dispose"} {
		action := action
		ginkgo.It("retains evidence on ENOSPC and performs revision-bound "+action+" without rewriting the original verdict", func() {
			f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf recoverable > ../results/output.txt")
			f.s.vmWrite = func(*os.File, []byte) (int, error) { return 0, unix.ENOSPC }
			original := f.run("vm-disk-full")
			gomega.Expect(original.FinalVerdict).To(gomega.Equal(managed.Incomplete))
			gomega.Expect(original.ResourceAvailability).To(gomega.Equal("quarantined"))
			gomega.Expect(original.ProtectedEvidence).NotTo(gomega.BeEmpty())
			next := f.admit("vm-after-full")
			_, err := f.reserve(next)
			gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
			gomega.Expect(os.ReadFile(filepath.Join(f.guest, "vm-disk-full/workspace/results/output.txt"))).To(gomega.Equal([]byte("recoverable")))
			fresh, err := newRuntime(f.s.root, f.s.ledger, f.s.store)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			fresh.sshCommand = f.s.sshCommand
			fresh.utmCommand = f.s.utmCommand
			fresh.vmLeases = f.s.vmLeases
			plan, err := fresh.planEvidenceRecovery(f.spec.ID, action)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())

			if action == "recollect" {
				originalRoot := filepath.Join(f.guest, "vm-disk-full")
				retained := originalRoot + "-original"
				gomega.Expect(os.Rename(originalRoot, retained)).To(gomega.Succeed())
				gomega.Expect(os.Mkdir(originalRoot, 0700)).To(gomega.Succeed())
				foreign := filepath.Join(originalRoot, "foreign.txt")
				gomega.Expect(os.WriteFile(foreign, []byte("untouched"), 0600)).To(gomega.Succeed())
				rejected := recoveryHTTP(fresh, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "foreign-root-refusal"})
				gomega.Expect(rejected.Code).NotTo(gomega.Equal(200))
				gomega.Expect(os.ReadFile(foreign)).To(gomega.Equal([]byte("untouched")))
				gomega.Expect(os.Remove(foreign)).To(gomega.Succeed())
				gomega.Expect(os.Remove(originalRoot)).To(gomega.Succeed())
				gomega.Expect(os.Rename(retained, originalRoot)).To(gomega.Succeed())
			}
			w := recoveryHTTP(fresh, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "native-" + action})
			gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
			health, err := fresh.planResourceRecovery(f.spec.ID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(health.PlanToken).NotTo(gomega.BeEmpty())
			w = recoveryHTTP(fresh, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: health.PlanToken, MutationKey: "native-health"})
			gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
			current, err := fresh.ledger.Result("vm-disk-full")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current).To(gomega.Equal(original))
			f.s = fresh
			_, err = f.reserve(next)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
	}
	ginkgo.It("reconciles one lost start reply after controller restart without issuing another workload", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf x >> ../results/output.txt; sleep .5")
		native := f.s.sshCommand
		starts := 0
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			archive := tar.NewReader(bytes.NewReader(input))
			var q runner.GuestRequest
			for {
				h, e := archive.Next()
				if e != nil {
					break
				}
				if h.Name == "request.json" {
					data, _ := io.ReadAll(archive)
					_ = json.Unmarshal(data, &q)
				}
			}
			reply, err := native(ctx, exe, args, input)
			if q.Operation == "start" {
				starts++
				return reply, fmt.Errorf("lost SSH reply after exact guest start")
			}
			return reply, err
		}
		r, err := f.reserve(f.admit("vm-lost-start"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = f.s.executeVM(r, f.job, f.object, time.Now().Add(10*time.Second))
		gomega.Expect(errors.Is(err, utm.ErrGuestUncertain)).To(gomega.BeTrue())
		gomega.Expect(f.s.ledger.MarkInterrupted()).To(gomega.Succeed())
		before, err := f.s.ledger.LocalIdentity(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		snap, err := f.s.ledger.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		deadline := snap.Phases["run"].Deadline
		fresh, err := newRuntime(f.s.root, f.s.ledger, f.s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fresh.sshCommand = f.s.sshCommand
		fresh.utmCommand = f.s.utmCommand
		fresh.vmLeases = f.s.vmLeases
		observed := fresh.sshCommand
		fresh.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			if vmTransportRequest(input).Operation == "state" {
				return utm.CommandResult{}, errors.New("original run unobservable")
			}
			return observed(ctx, exe, args, input)
		}
		gomega.Expect(errors.Is(fresh.tryRecoverInterrupted(r.JobID, true, nil), managed.ErrBlocked)).To(gomega.BeTrue())
		occupied, err := fresh.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(occupied.Occupancy).To(gomega.Equal(1))
		fresh.sshCommand = observed

		gomega.Eventually(func() error { return fresh.tryRecoverInterrupted(r.JobID, true, nil) }, 4*time.Second, 50*time.Millisecond).Should(gomega.Succeed())
		fresh.workers.Wait()
		result, err := fresh.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		after, err := fresh.ledger.LocalIdentity(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(before))
		gomega.Expect(starts).To(gomega.Equal(1))
		phases, err := fresh.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["run"].Deadline).To(gomega.Equal(deadline))
		data, err := os.ReadFile(filepath.Join(f.s.root.Path, "jobs", r.JobID, "artifacts/results/output.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("x"))
	})
	ginkgo.DescribeTable("recovers a stopped partial preparation without preparing or executing again", func(unbound bool) {
		f := nativeVMFixture(ginkgo.GinkgoT(), "false")
		native := f.s.sshCommand
		preparations := 0
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			q := vmTransportRequest(input)
			reply, err := native(ctx, exe, args, input)
			if q.Operation == "prepare" {
				preparations++
				// A stopped preparer left its owned root but no complete
				// native request publication. No workload was authorized.
				marker := filepath.Join(f.guest, ".haepari-control-"+q.Request.Identity.JobID+".json")
				raw, e := os.ReadFile(marker)
				if e != nil {
					return reply, e
				}
				var control runner.GuestControl
				if e = json.Unmarshal(raw, &control); e != nil {
					return reply, e
				}
				control.NativeRequest, control.Observation = nil, nil
				if unbound {
					// Model the mkdir-before-bind interruption: the only Job
					// effect is an empty root and the worker has not completed.
					root := filepath.Join(f.guest, q.Request.Identity.JobID)
					if e = os.RemoveAll(root); e != nil {
						return reply, e
					}
					if e = os.Mkdir(root, 0700); e != nil {
						return reply, e
					}
					child := exec.Command("/bin/sleep", "30")
					if e = child.Start(); e != nil {
						return reply, e
					}
					worker, observed := platform.ObserveProcess(child.Process.Pid)
					_ = child.Process.Kill()
					_ = child.Wait()
					if observed != nil {
						return reply, observed
					}
					control.Root = nil
					control.Owner = worker.Identity
					control.Complete, control.Stopped = false, false
				}
				raw, _ = json.Marshal(control)
				if e = os.WriteFile(marker, raw, 0600); e != nil {
					return reply, e
				}
				if !unbound {
					e = os.Remove(filepath.Join(f.guest, q.Request.Identity.JobID, "request.json"))
				}
				if e != nil {
					return reply, e
				}
				return reply, errors.New("partial preparation response lost")
			}
			return reply, err
		}
		r, err := f.reserve(f.admit("vm-partial-prepare"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(errors.Is(f.s.executeVM(r, f.job, f.object, time.Now().Add(10*time.Second)), utm.ErrGuestUncertain)).To(gomega.BeTrue())
		gomega.Expect(f.s.ledger.MarkInterrupted()).To(gomega.Succeed())
		fresh, err := newRuntime(f.s.root, f.s.ledger, f.s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fresh.sshCommand, fresh.utmCommand, fresh.vmLeases = f.s.sshCommand, f.s.utmCommand, f.s.vmLeases
		if unbound {
			marker := filepath.Join(f.guest, ".haepari-control-"+r.JobID+".json")
			raw, e := os.ReadFile(marker)
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			var control runner.GuestControl
			gomega.Expect(json.Unmarshal(raw, &control)).To(gomega.Succeed())
			live, e := platform.ObserveProcess(os.Getpid())
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			original := control.Owner
			control.Owner = live.Identity
			liveRaw, e := json.Marshal(control)
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			gomega.Expect(os.WriteFile(marker, liveRaw, 0600)).To(gomega.Succeed())
			gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, true, nil)).NotTo(gomega.Succeed())
			control.Owner = original
			raw, e = json.Marshal(control)
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			gomega.Expect(os.WriteFile(marker, raw, 0600)).To(gomega.Succeed())
			sentinel := filepath.Join(f.guest, r.JobID, "foreign.txt")
			gomega.Expect(os.WriteFile(sentinel, []byte("preserve"), 0600)).To(gomega.Succeed())
			gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, true, nil)).NotTo(gomega.Succeed())
			kept, e := os.ReadFile(sentinel)
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			gomega.Expect(string(kept)).To(gomega.Equal("preserve"))
			gomega.Expect(os.Remove(sentinel)).To(gomega.Succeed())
			q, e := fresh.vmBinding(r.JobID)
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			q.Operation, q.Deadline = "state", time.Now().Add(5*time.Second)
			state, e := fresh.vmCall(q)
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			gomega.Expect(state.Root).NotTo(gomega.BeNil())
			unchanged, e := os.ReadFile(marker)
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			gomega.Expect(unchanged).To(gomega.Equal(raw))
		}
		gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.Succeed())
		fresh.workers.Wait()
		result, err := fresh.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(preparations).To(gomega.Equal(1))
		_, err = os.Stat(filepath.Join(f.guest, r.JobID))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	}, ginkgo.Entry("with its recorded root incarnation", false), ginkgo.Entry("between mkdir and root binding", true))

	ginkgo.It("retires an unobserved start before no-start settlement and rejects its delayed delivery", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf forbidden > ../results/output.txt")
		native := f.s.sshCommand
		var delayed []byte
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			q := vmTransportRequest(input)
			if q.Operation == "start" {
				delayed = append([]byte(nil), input...)
				return utm.CommandResult{}, errors.New("start delivery unknown")
			}
			return native(ctx, exe, args, input)
		}
		r, err := f.reserve(f.admit("vm-delayed-start"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.s.executeVM(r, f.job, f.object, time.Now().Add(10*time.Second))).To(gomega.WithTransform(func(err error) bool { return errors.Is(err, utm.ErrGuestUncertain) }, gomega.BeTrue()))
		gomega.Expect(delayed).NotTo(gomega.BeEmpty())
		gomega.Expect(f.s.ledger.MarkInterrupted()).To(gomega.Succeed())
		fresh, err := newRuntime(f.s.root, f.s.ledger, f.s.store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fresh.sshCommand, fresh.utmCommand, fresh.vmLeases = native, f.s.utmCommand, f.s.vmLeases
		gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.Succeed())
		fresh.workers.Wait()
		result, err := fresh.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		reply, err := native(context.Background(), "", nil, delayed)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(reply.ExitCode).NotTo(gomega.BeZero())
		_, err = os.Stat(filepath.Join(f.guest, r.JobID))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
	ginkgo.It("keeps the lease occupied while a remote collector has no termination proof", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf retained > ../results/output.txt")
		native := f.s.sshCommand
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			q := vmTransportRequest(input)
			if q.Operation == "file" || q.Operation == "state" {
				return utm.CommandResult{}, errors.New("collector termination unknown")
			}
			return native(ctx, exe, args, input)
		}
		r, err := f.reserve(f.admit("vm-collector-unknown"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = f.s.executeVM(r, f.job, f.object, time.Now().Add(10*time.Second))
		gomega.Expect(errors.Is(err, runner.ErrUnproven)).To(gomega.BeTrue())
		f.s.failed(r.JobID, err)
		status, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Occupancy).To(gomega.Equal(1))
		snap, err := f.s.ledger.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(snap.Receipt.JobState).To(gomega.Equal("reconciling"))
		gomega.Expect(snap.Phases["collect"].ProtectedEvidence).NotTo(gomega.BeEmpty())
		_, err = f.reserve(f.admit("vm-after-unknown"))
		gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, r.JobID, "workspace/results/output.txt"))).To(gomega.Equal([]byte("retained")))
		f.s.sshCommand = native
		gomega.Expect(f.s.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.Succeed())
		f.s.workers.Wait()
		result, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		phases, err := f.s.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["collect"].Deadline).To(gomega.Equal(snap.Phases["collect"].Deadline))
	})
	ginkgo.It("cancels the original guest run and proves termination before releasing its lease", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf cancelled > ../results/output.txt; sleep 8")
		r, err := f.reserve(f.admit("vm-cancel"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		done := make(chan error, 1)
		go func() {
			defer ginkgo.GinkgoRecover()
			done <- f.s.executeVM(r, f.job, f.object, time.Now().Add(10*time.Second))
		}()
		gomega.Eventually(func() bool {
			_, err := os.Stat(filepath.Join(f.guest, r.JobID, "workspace/results/output.txt"))
			return err == nil
		}, 4*time.Second, 25*time.Millisecond).Should(gomega.BeTrue())
		current, err := f.s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = f.s.requestCancel(managed.CancelRequest{JobID: r.JobID, ExpectedRevision: current.JobRevision, MutationKey: "cancel-original-vm"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var runErr error
		gomega.Eventually(done, 8*time.Second).Should(gomega.Receive(&runErr))
		gomega.Expect(runErr).NotTo(gomega.HaveOccurred())
		result, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Cancelled))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		status, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Occupancy).To(gomega.BeZero())
	})
	ginkgo.It("quarantines a stopped workload when owned cleanup fails and rejects reuse", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf safe > ../results/output.txt")
		native := f.s.sshCommand
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			if vmTransportRequest(input).Operation == "cleanup" {
				return utm.CommandResult{}, errors.New("cleanup unavailable")
			}
			return native(ctx, exe, args, input)
		}
		result := f.run("vm-cleanup-failed")
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
		gomega.Expect(result.ProtectedEvidence).NotTo(gomega.BeEmpty())
		status, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Occupancy).To(gomega.BeZero())
		_, err = f.reserve(f.admit("vm-after-cleanup-failed"))
		gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, "vm-cleanup-failed/workspace/results/output.txt"))).To(gomega.Equal([]byte("safe")))
	})

	ginkgo.It("pauses only the exact unavailable VM before capacity return and requires explicit verified resume", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf okay > ../results/output.txt")
		leases := f.s.vmLeases
		f.s.vmLeases = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("missing lease")), nil }
		result := f.run("vm-unavailable")
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("not_needed")))
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("paused"))
		next := f.admit("vm-after-pause")
		_, err := f.reserve(next)
		gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
		local, err := f.s.ledger.NextQueued()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(local.Spec.ResourceID).To(gomega.Equal("local-a"))
		gomega.Expect(f.s.ledger.MarkInterrupted()).To(gomega.Succeed())
		status, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Occupancy).To(gomega.BeZero())
		gomega.Expect(status.Reason).To(gomega.Equal("vm_unavailable"))
		f.s.vmLeases = leases
		q := managed.AvailabilityRequest{ResourceID: f.spec.ID, ExpectedConfigRevision: status.ConfigRevision, ExpectedAvailabilityRevision: status.AvailabilityRevision, ExpectedLeaseGeneration: status.LeaseGeneration, MutationKey: "explicit-vm-resume"}
		w := vmMutationHTTP(f.s, "/v1/resources/"+f.spec.ID+"/resume", q)
		gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
		w = vmMutationHTTP(f.s, "/v1/resources/"+f.spec.ID+"/resume", q)
		gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
		_, err = f.reserve(next)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})
	ginkgo.DescribeTable("pauses every available VM on a no-effect host Automation denial", func(deniedOperation string) {
		f := nativeVMFixture(ginkgo.GinkgoT(), "false")
		other := vmControllerSpec(ginkgo.GinkgoT(), "vm-other", 2, false)
		paused := vmControllerSpec(ginkgo.GinkgoT(), "vm-maintenance", 3, false)
		for _, spec := range []managed.ResourceSpec{other, paused} {
			gomega.Expect(f.s.ledger.RegisterResource(mustJSONScheduling(spec))).To(gomega.Succeed())
			f.control.states[spec.VM.UTMVMUUID] = "stopped"
		}
		_, err := f.s.ledger.SetAvailability(managed.AvailabilityRequest{ResourceID: paused.ID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, Reason: "maintenance", MutationKey: "maintenance-pause"}, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		r, err := f.reserve(f.admit("vm-host-denied"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		native := f.control.command
		f.s.utmCommand = func(ctx context.Context, exe string, args ...string) (utm.CommandResult, error) {
			if args[0] == deniedOperation {
				return utm.CommandResult{Stderr: "Error from event: (OSStatus error -1743.)"}, nil
			}
			return native(ctx, exe, args...)
		}
		sshCalls := 0
		f.s.sshCommand = func(context.Context, string, []string, []byte) (utm.CommandResult, error) {
			sshCalls++
			return utm.CommandResult{}, errors.New("guest effect forbidden")
		}
		gomega.Expect(f.s.executeVM(r, f.job, f.object, time.Now().Add(10*time.Second))).To(gomega.Succeed())
		result, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("not_needed")))
		for _, id := range []string{f.spec.ID, other.ID} {
			status, err := f.s.ledger.ResourceStatus(id)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(status.Availability).To(gomega.Equal("paused"))
			gomega.Expect(status.Reason).To(gomega.Equal("host_permission_unavailable"))
			gomega.Expect(status.Occupancy).To(gomega.BeZero())
		}
		status, err := f.s.ledger.ResourceStatus(paused.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Reason).To(gomega.Equal("maintenance"))
		local, err := f.s.ledger.ResourceStatus("local-a")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(local.Availability).To(gomega.Equal("available"))
		gomega.Expect(sshCalls).To(gomega.BeZero())
		gomega.Expect(f.control.effects).To(gomega.BeEmpty())
		_, err = os.Stat(filepath.Join(f.guest, r.JobID))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	}, ginkgo.Entry("during membership lookup", "list"), ginkgo.Entry("during VM status", "status"))
	ginkgo.It("settles a returned framework capacity rejection without quarantine or guest preparation", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "false")
		f.control.states[f.spec.VM.UTMVMUUID] = "stopped"
		native := f.control.command
		f.s.utmCommand = func(ctx context.Context, exe string, args ...string) (utm.CommandResult, error) {
			if args[0] == "start" {
				return utm.CommandResult{Stderr: "Error from event: The operation couldn’t be completed. (VZErrorDomain error 6.)", ExitCode: 0}, nil
			}
			return native(ctx, exe, args...)
		}
		result := f.run("vm-framework-limit")
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("not_needed")))
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("paused"))
		status, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Reason).To(gomega.Equal("host_vm_limit"))
		gomega.Expect(status.Occupancy).To(gomega.BeZero())
		_, err = os.Stat(filepath.Join(f.guest, "vm-framework-limit"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})

})

func vmMutationHTTP(s *runtimeServer, path string, value any) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, bytes.NewReader(mustJSONScheduling(value)))
	r.Header.Set("X-Haepari-Protocol", protocol)
	r.Header.Set("X-Haepari-Protocol-Min", "1")
	r.Header.Set("X-Haepari-Protocol-Max", "1")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

func vmTransportRequest(input []byte) runner.GuestRequest {
	archive := tar.NewReader(bytes.NewReader(input))
	for {
		h, err := archive.Next()
		if err != nil {
			return runner.GuestRequest{}
		}
		if h.Name == "request.json" {
			var q runner.GuestRequest
			_ = json.NewDecoder(archive).Decode(&q)
			return q
		}
	}
}

var _ = ginkgo.Describe("Task016VMAdapterTimings", ginkgo.Label("integration"), func() {
	ginkgo.DescribeTable("persists observed VM adapter durations in the terminal result", func(started bool) {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf timed > ../results/output.txt")
		if !started {
			f.control.states[f.spec.VM.UTMVMUUID] = "stopped"
		}
		native := f.s.sshCommand
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			q := vmTransportRequest(input)
			if q.Operation == "identity" || q.Operation == "prepare" {
				time.Sleep(20 * time.Millisecond)
			}
			return native(ctx, exe, args, input)
		}
		result := f.run("vm-timings")
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		names := []string{}
		for _, step := range result.Timings.AdapterSteps {
			names = append(names, step.Step)
			gomega.Expect(step.DurationMillis).To(gomega.BeNumerically(">=", 0))
			if step.Step == "ssh_ready" || step.Step == "transfer" {
				gomega.Expect(step.DurationMillis).To(gomega.BeNumerically(">=", 20))
			}
		}
		expected := []string{"ssh_ready", "transfer", "collect", "cleanup"}
		if !started {
			expected = append([]string{"vm_start"}, expected...)
		}
		gomega.Expect(names).To(gomega.Equal(expected))
		phases, err := f.s.ledger.LocalPhases(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, phase := range result.Timings.Phases {
			if phase.Phase == "collect" || phase.Phase == "cleanup" {
				p := phases[phase.Phase]
				gomega.Expect(result.Timings.AdapterSteps).To(gomega.ContainElement(managed.ResultStep{Step: phase.Phase, DurationMillis: p.ObservedAt.Sub(p.StartedAt).Milliseconds()}))
			}
		}
		again, err := f.s.ledger.Result(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(again).To(gomega.Equal(result))
	}, ginkgo.Entry("already running VM", true), ginkgo.Entry("required fenced VM start", false))
})
