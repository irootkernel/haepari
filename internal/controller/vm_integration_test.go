//go:build integration

package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

type fakeUTMControl struct {
	mu         sync.Mutex
	states     map[string]string
	effects    []string
	failEffect bool
	failStatus map[string]bool
}

func (f *fakeUTMControl) command(_ context.Context, _ string, args ...string) (utm.CommandResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch args[0] {
	case "list":
		output := "UUID Status Name\n"
		for uuid, state := range f.states {
			output += uuid + " " + state + " duplicate name\n"
		}
		return utm.CommandResult{Stdout: output}, nil
	case "status":
		if f.failStatus[args[1]] {
			return utm.CommandResult{}, fmt.Errorf("status unavailable")
		}
		return utm.CommandResult{Stdout: f.states[args[1]]}, nil
	case "start", "stop":
		f.effects = append(f.effects, strings.Join(args, " "))
		if f.failEffect {
			return utm.CommandResult{Stderr: "Error from event: unknown effect"}, nil
		}
		state := "started"
		if args[0] == "stop" {
			state = "stopped"
		}
		f.states[args[1]] = state
		return utm.CommandResult{}, nil
	}
	return utm.CommandResult{}, fmt.Errorf("unexpected operation")
}

func vmControllerSpec(t ginkgo.FullGinkgoTInterface, id string, n int, allowStop bool) managed.ResourceSpec {
	t.Helper()
	uuid := fmt.Sprintf("%08d-1111-4111-8111-111111111111", n)
	key := base64.StdEncoding.EncodeToString(append([]byte{0, 0, 0, 11, 's', 's', 'h', '-', 'e', 'd', '2', '5', '5', '1', '9', 0, 0, 0, 32}, make([]byte, 32)...))
	data := []byte(utm.HostAlias(uuid) + " ssh-ed25519 " + key + "\n")
	filename := filepath.Join(t.TempDir(), "known_hosts")
	gomega.Expect(os.WriteFile(filename, data, 0600)).To(gomega.Succeed())
	sum := sha256.Sum256(data)
	return managed.ResourceSpec{Schema: "haepari.resource/v1", ID: id, Kind: "utm-macos", ExecutionMode: "managed", Capacity: 1, Revision: 1,
		Adapter: managed.Adapter{UTMCtl: "/Applications/UTM.app/Contents/MacOS/utmctl", QualificationReference: "fake-task013"},
		VM:      &managed.VM{UTMVMUUID: uuid, MACAddress: fmt.Sprintf("aa:bb:cc:dd:ee:%02x", n), GuestJobRoot: "/var/haepari/jobs", AddressDiscovery: "dhcp-lease-by-mac", AllowAutomaticStop: allowStop},
		Guest:   &managed.Guest{ConnectionReference: "task014-mapping", KnownHostsReference: "sha256:" + hex.EncodeToString(sum[:]) + ":" + filename}}
}

func reserveControllerVM(s *runtimeServer, id, jobID string, guard *managed.VMReservationGuard) (managed.Receipt, error) {
	digest := strings.Repeat("a", 64)
	j := managed.JobSpec{Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: id, Mode: "auto", Suite: "utm-control",
		Input: managed.Input{Root: "/tmp/isolated-vm-input", InventoryDigest: digest}, Environment: managed.Environment{ProfileID: "development", ProfileDigest: digest},
		Workload:  managed.Workload{Argv: []string{"/usr/bin/true"}, CWD: "source", RequiredOutputs: []string{"results/output"}},
		Deadlines: managed.Deadlines{QueueSeconds: 600, PrepareSeconds: 10, RunSeconds: 10, CollectSeconds: 10, CleanupSeconds: 10},
		Effects:   managed.Effects{Mode: "offline", AuthorizationReference: "fake-task013"}, Provenance: managed.Provenance{CandidateDigest: digest, TestsDigest: digest, TargetProfileDigest: digest, EvidenceClass: "development"}}
	r, err := s.ledger.Admit(mustJSONScheduling(j), digest, "key-"+jobID, jobID)
	if err != nil {
		return r, err
	}
	return s.ledger.Reserve(managed.ReserveRequest{JobID: jobID, ExpectedJobRevision: r.JobRevision, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-" + jobID, VMGuard: guard})
}

var _ = ginkgo.Describe("Task013UTMController", ginkgo.Label("integration"), func() {
	ginkgo.It("validates VM update flags before connecting and updates through the real CLI", func() {
		t := ginkgo.GinkgoT()
		s, c, _ := availabilityFixture(t)
		a := vmControllerSpec(t, "vm-cli", 1, false)
		f := &fakeUTMControl{states: map[string]string{a.VM.UTMVMUUID: "started"}}
		s.utmCommand = f.command
		_, err := c.Register(context.Background(), mustJSONScheduling(a))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, _, err = c.SetAvailability(context.Background(), managed.AvailabilityRequest{ResourceID: a.ID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "cli-maintenance", Reason: "maintenance"}, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		before, _, err := c.InspectResource(context.Background(), a.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		a.Revision = 2
		parent := t.TempDir()
		file := filepath.Join(parent, "vm.json")
		gomega.Expect(os.WriteFile(file, mustJSONScheduling(a), 0600)).To(gomega.Succeed())
		local := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "local-a", Kind: "local", ExecutionMode: "managed", Capacity: 2, Revision: 2, Adapter: managed.Adapter{QualificationReference: "isolated-development"}, Cleanup: &managed.CleanupPolicy{PolicyReference: "owned-workspace"}}
		localFile := filepath.Join(parent, "local.json")
		gomega.Expect(os.WriteFile(localFile, mustJSONScheduling(local), 0600)).To(gomega.Succeed())
		executable := filepath.Join(parent, "haepari")
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		build := exec.CommandContext(ctx, "go", "build", "-o", executable, "./cmd/haepari")
		build.Dir = "../.."
		buildOutput, err := build.CombinedOutput()
		cancel()
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s", buildOutput)
		runCLI := func(home string, args ...string) (int, string, string) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, args...)
			cmd.Env = append(os.Environ(), "HAEPARI_HOME="+home)
			var out, diagnostic bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &diagnostic
			err := cmd.Run()
			gomega.Expect(ctx.Err()).NotTo(gomega.HaveOccurred())
			code := 0
			if err != nil {
				var exited *exec.ExitError
				gomega.Expect(errors.As(err, &exited)).To(gomega.BeTrue())
				code = exited.ExitCode()
			}
			return code, out.String(), diagnostic.String()
		}
		for _, tc := range []struct {
			file  string
			flags []string
		}{
			{file, nil},
			{file, []string{"--expected-availability-revision", "0"}},
			{file, []string{"--expected-availability-revision", "-1"}},
			{localFile, []string{"--expected-availability-revision", "0"}},
			{localFile, []string{"--expected-availability-revision", "1"}},
		} {
			args := append([]string{"resource", "update", "--file", tc.file, "--expected-revision", "1", "--json"}, tc.flags...)
			code, out, diagnostic := runCLI(filepath.Join(parent, "missing", "state"), args...)
			gomega.Expect(code).To(gomega.Equal(2))
			gomega.Expect(out).To(gomega.BeEmpty())
			gomega.Expect(diagnostic).To(gomega.ContainSubstring("--expected-availability-revision"))
			if tc.file == localFile {
				gomega.Expect(diagnostic).To(gomega.ContainSubstring("resource file's kind to be utm-macos"))
			}
			gomega.Expect(diagnostic).NotTo(gomega.ContainSubstring("connect"))
		}
		code, out, diagnostic := runCLI(s.root.Path, "resource", "update", "--file", file, "--expected-revision", "1", "--expected-availability-revision", "2", "--json")
		gomega.Expect(code).To(gomega.BeZero(), "%s", diagnostic)
		gomega.Expect(diagnostic).To(gomega.BeEmpty())
		gomega.Expect(out).To(gomega.ContainSubstring(`"operation":"resource.update"`))
		updated, _, err := c.InspectResource(context.Background(), a.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(updated.ConfigRevision).To(gomega.Equal(int64(2)))
		gomega.Expect(updated.Availability).To(gomega.Equal("paused"))
		gomega.Expect(updated.AvailabilityRevision).To(gomega.Equal(before.AvailabilityRevision))
		gomega.Expect(updated.IdleSince).To(gomega.Equal(before.IdleSince))
		gomega.Expect(f.effects).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses unverified successor UUIDs and changed or missing pins without altering registration", func() {
		for _, scenario := range []string{"unknown-uuid", "changed-pin", "missing-pin"} {
			s, c, _ := availabilityFixture(ginkgo.GinkgoT())
			a := vmControllerSpec(ginkgo.GinkgoT(), "vm-successor", 1, false)
			f := &fakeUTMControl{states: map[string]string{a.VM.UTMVMUUID: "started"}}
			s.utmCommand = f.command
			_, err := c.Register(context.Background(), mustJSONScheduling(a))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			before, _, err := c.InspectResource(context.Background(), a.ID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			candidate := a
			if scenario == "unknown-uuid" {
				candidate = vmControllerSpec(ginkgo.GinkgoT(), a.ID, 2, false)
			} else {
				pin := strings.SplitN(candidate.Guest.KnownHostsReference, ":", 3)[2]
				if scenario == "changed-pin" {
					gomega.Expect(os.WriteFile(pin, []byte("# changed bytes\n"), 0600)).To(gomega.Succeed())
				} else {
					gomega.Expect(os.Remove(pin)).To(gomega.Succeed())
				}
			}
			candidate.Revision = 2
			_, _, err = c.UpdateVMResource(context.Background(), a.ID, mustJSONScheduling(candidate), 1, 1)
			var issue *client.Issue
			gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue(), "%s", scenario)
			gomega.Expect(issue.Code).To(gomega.Equal("PREREQUISITE_UNAVAILABLE"))
			after, _, err := c.InspectResource(context.Background(), a.ID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(after).To(gomega.Equal(before))
			stored, err := s.ledger.VMResources()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(stored[0].Spec).To(gomega.Equal(a))
			gomega.Expect(f.effects).To(gomega.BeEmpty())
		}
	})

	ginkgo.It("registers by verified UUID and pin, refusing unknown and duplicate UUID/MAC without effects", func() {
		s, c, _ := availabilityFixture(ginkgo.GinkgoT())
		a := vmControllerSpec(ginkgo.GinkgoT(), "vm-a", 1, false)
		b := vmControllerSpec(ginkgo.GinkgoT(), "vm-b", 2, false)
		f := &fakeUTMControl{states: map[string]string{a.VM.UTMVMUUID: "started", b.VM.UTMVMUUID: "stopped"}}
		s.utmCommand = f.command
		_, err := c.Register(context.Background(), mustJSONScheduling(a))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, scenario := range []string{"uuid", "mac", "unknown", "missing-pin"} {
			candidate := vmControllerSpec(ginkgo.GinkgoT(), "duplicate-"+scenario, 2, false)
			code := "CONFLICT"
			switch scenario {
			case "uuid":
				candidate.VM.UTMVMUUID = a.VM.UTMVMUUID
				candidate.Guest.KnownHostsReference = a.Guest.KnownHostsReference
			case "mac":
				candidate.VM.MACAddress = a.VM.MACAddress
			case "unknown":
				candidate = vmControllerSpec(ginkgo.GinkgoT(), "unknown", 3, false)
				code = "PREREQUISITE_UNAVAILABLE"
			case "missing-pin":
				candidate.Guest.KnownHostsReference = "/missing"
				code = "PREREQUISITE_UNAVAILABLE"
			}
			_, err := c.Register(context.Background(), mustJSONScheduling(candidate))
			var issue *client.Issue
			gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
			gomega.Expect(issue.Code).To(gomega.Equal(code))
		}
		gomega.Expect(f.effects).To(gomega.BeEmpty())
		resources, err := s.ledger.VMResources()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(resources).To(gomega.HaveLen(1))
		gomega.Expect(resources[0].Spec.VM.AllowAutomaticStop).To(gomega.BeFalse())
		prior, err := s.ledger.Request("queued-request")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		job := prior.Spec
		job.ResourceID = a.ID
		job.Input.SelectedFiles = []string{"source/input.txt"}
		_, _, err = c.Submit(context.Background(), mustJSONScheduling(job), "vm-unsupported")
		var issue *client.Issue
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("UNSUPPORTED"))
		_, err = s.ledger.Request("vm-unsupported")
		gomega.Expect(errors.Is(err, managed.ErrNotFound)).To(gomega.BeTrue())
	})

	ginkgo.It("fences idle registration updates and retains maintenance availability", func() {
		s, c, _ := availabilityFixture(ginkgo.GinkgoT())
		a := vmControllerSpec(ginkgo.GinkgoT(), "vm-a", 1, false)
		f := &fakeUTMControl{states: map[string]string{a.VM.UTMVMUUID: "started"}}
		s.utmCommand = f.command
		_, err := c.Register(context.Background(), mustJSONScheduling(a))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, _, err = c.SetAvailability(context.Background(), managed.AvailabilityRequest{ResourceID: a.ID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "vm-maintenance", Reason: "maintenance"}, false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		a.Revision = 2
		_, _, err = c.UpdateVMResource(context.Background(), a.ID, mustJSONScheduling(a), 1, 1)
		var issue *client.Issue
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("STALE_REVISION"))
		updated, _, err := c.UpdateVMResource(context.Background(), a.ID, mustJSONScheduling(a), 1, 2)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(updated.Availability).To(gomega.Equal("paused"))
		gomega.Expect(updated.ConfigRevision).To(gomega.Equal(int64(2)))
		_, err = s.ledger.ObserveVM(managed.VMObservation{ResourceID: a.ID, ConfigRevision: 1, VMUUID: a.VM.UTMVMUUID, Power: "started", EvidenceDigest: strings.Repeat("a", 64)})
		gomega.Expect(errors.Is(err, managed.ErrStale)).To(gomega.BeTrue())
		gomega.Expect(f.effects).To(gomega.BeEmpty())
	})

	ginkgo.It("refuses unchanged and skipped VM successor revisions at both update fences", func() {
		s, c, _ := availabilityFixture(ginkgo.GinkgoT())
		a := vmControllerSpec(ginkgo.GinkgoT(), "vm-successor-fence", 1, false)
		f := &fakeUTMControl{states: map[string]string{a.VM.UTMVMUUID: "started"}}
		s.utmCommand = f.command
		_, err := c.Register(context.Background(), mustJSONScheduling(a))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		before, _, err := c.InspectResource(context.Background(), a.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, revision := range []int64{1, 3} {
			candidate := a
			candidate.Revision = revision
			_, _, err = c.UpdateVMResource(context.Background(), a.ID, mustJSONScheduling(candidate), 1, 1)
			var issue *client.Issue
			gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
			gomega.Expect(issue.Code).To(gomega.Equal("STALE_REVISION"))
			gomega.Expect(errors.Is(s.ledger.UpdateResource(mustJSONScheduling(candidate), 1, 1), managed.ErrStale)).To(gomega.BeTrue())
			after, _, err := c.InspectResource(context.Background(), a.ID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(after).To(gomega.Equal(before))
			stored, err := s.ledger.VMResources()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(stored[0].Spec).To(gomega.Equal(a))
		}
		gomega.Expect(f.effects).To(gomega.BeEmpty())
	})

	ginkgo.It("keeps storage failures distinct from duplicate registration and occupied updates", func() {
		s, c, db := availabilityFixture(ginkgo.GinkgoT())
		a := vmControllerSpec(ginkgo.GinkgoT(), "vm-a", 1, false)
		f := &fakeUTMControl{states: map[string]string{a.VM.UTMVMUUID: "started"}}
		s.utmCommand = f.command
		gomega.Expect(db.Exec(`CREATE TRIGGER registration_failure BEFORE INSERT ON managed_resources BEGIN SELECT RAISE(ABORT,'storage fixture'); END`)).Error().NotTo(gomega.HaveOccurred())
		_, err := c.Register(context.Background(), mustJSONScheduling(a))
		var issue *client.Issue
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("STORAGE_UNAVAILABLE"))
		_, err = db.Exec(`DROP TRIGGER registration_failure`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = c.Register(context.Background(), mustJSONScheduling(a))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		guard, err := s.observeVMs(context.Background(), 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = reserveControllerVM(s, a.ID, "occupied", guard)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		a.Revision = 2
		_, _, err = c.UpdateVMResource(context.Background(), a.ID, mustJSONScheduling(a), 1, 1)
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("RESOURCE_BUSY"))
		gomega.Expect(f.effects).To(gomega.BeEmpty())
	})

	ginkgo.It("claims a transfer once and retains command-return proof through uncertain observations", func() {
		s, c, db := availabilityFixture(ginkgo.GinkgoT())
		victim := vmControllerSpec(ginkgo.GinkgoT(), "victim", 1, true)
		target := vmControllerSpec(ginkgo.GinkgoT(), "target", 2, false)
		f := &fakeUTMControl{states: map[string]string{victim.VM.UTMVMUUID: "started", target.VM.UTMVMUUID: "stopped"}}
		s.utmCommand = f.command
		for _, spec := range []managed.ResourceSpec{victim, target} {
			_, err := c.Register(context.Background(), mustJSONScheduling(spec))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		guard, err := s.observeVMs(context.Background(), 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = reserveControllerVM(s, target.ID, "transfer", guard)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var generation int64
		gomega.Expect(db.QueryRow(`SELECT operation_generation FROM managed_power_transitions WHERE target_resource_id='target'`).Scan(&generation)).To(gomega.Succeed())
		p, err := s.ledger.PowerIntent(generation)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		identity := p.Identity
		stale := identity
		stale.TargetConfigRevision++
		_, err = s.advanceVMPower(context.Background(), stale)
		gomega.Expect(errors.Is(err, managed.ErrStale)).To(gomega.BeTrue())
		f.failStatus = map[string]bool{victim.VM.UTMVMUUID: true}
		p, err = s.advanceVMPower(context.Background(), identity)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.Stage).To(gomega.Equal("stop_uncertain"))
		p, err = s.advanceVMPower(context.Background(), identity)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.State).To(gomega.Equal("uncertain"))
		_, _, err = c.UpdateVMResource(context.Background(), victim.ID, mustJSONScheduling(func() managed.ResourceSpec { v := victim; v.Revision++; return v }()), 1, 1)
		var issue *client.Issue
		gomega.Expect(errors.As(err, &issue)).To(gomega.BeTrue())
		gomega.Expect(issue.Code).To(gomega.Equal("RESOURCE_BUSY"))
		f.mu.Lock()
		f.failStatus = nil
		f.mu.Unlock()
		gomega.Expect(db.Close()).To(gomega.Succeed())
		reopened, err := sqlite.Open(s.root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer reopened.Close()
		s.ledger, err = managed.NewLedger(reopened, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		p, err = s.ledger.PowerIntent(generation)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.CommandReturned()).To(gomega.BeTrue())
		p, err = s.advanceVMPower(context.Background(), identity)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.Stage).To(gomega.Equal("start_ready"))
		gomega.Expect(p.CommandReturned()).To(gomega.BeFalse())
		gomega.Expect(errors.Is(s.ledger.ClaimPowerEffect(identity, "start"), managed.ErrBlocked)).To(gomega.BeTrue())
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() { _, _ = s.advanceVMPower(context.Background(), identity) })
		}
		wg.Wait()
		p, err = s.advanceVMPower(context.Background(), identity)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.State).To(gomega.Equal("reconciled"))
		gomega.Expect(f.effects).To(gomega.Equal([]string{"stop " + victim.VM.UTMVMUUID, "start " + target.VM.UTMVMUUID}))
	})

	ginkgo.It("retains uncertain command protection across reopen despite a later stopped status", func() {
		s, c, db := availabilityFixture(ginkgo.GinkgoT())
		victim := vmControllerSpec(ginkgo.GinkgoT(), "victim", 1, true)
		target := vmControllerSpec(ginkgo.GinkgoT(), "target", 2, false)
		f := &fakeUTMControl{states: map[string]string{victim.VM.UTMVMUUID: "started", target.VM.UTMVMUUID: "stopped"}, failEffect: true}
		s.utmCommand = f.command
		for _, spec := range []managed.ResourceSpec{victim, target} {
			_, err := c.Register(context.Background(), mustJSONScheduling(spec))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		guard, err := s.observeVMs(context.Background(), 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = reserveControllerVM(s, target.ID, "uncertain-transfer", guard)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var generation int64
		gomega.Expect(db.QueryRow(`SELECT operation_generation FROM managed_power_transitions WHERE target_resource_id='target'`).Scan(&generation)).To(gomega.Succeed())
		p, err := s.ledger.PowerIntent(generation)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		p, err = s.advanceVMPower(context.Background(), p.Identity)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.Stage).To(gomega.Equal(managed.PowerStopUncertain))
		f.states[victim.VM.UTMVMUUID] = "stopped"
		f.failEffect = false
		gomega.Expect(db.Close()).To(gomega.Succeed())
		reopened, err := sqlite.Open(s.root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer reopened.Close()
		s.ledger, err = managed.NewLedger(reopened, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for range 2 {
			p, err = s.advanceVMPower(context.Background(), p.Identity)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(p.Stage).To(gomega.Equal(managed.PowerStopUncertain))
			gomega.Expect(p.CommandReturned()).To(gomega.BeFalse())
		}
		gomega.Expect(f.effects).To(gomega.Equal([]string{"stop " + victim.VM.UTMVMUUID}))
		victim.Revision++
		gomega.Expect(errors.Is(s.ledger.UpdateResource(mustJSONScheduling(victim), 1, 1), managed.ErrBusy)).To(gomega.BeTrue())
	})

	ginkgo.DescribeTable("retains protection while a claimed command is delayed before its effect", func(effect string, corruptGuard bool) {
		s, c, db := availabilityFixture(ginkgo.GinkgoT())
		victim := vmControllerSpec(ginkgo.GinkgoT(), "victim", 1, true)
		target := vmControllerSpec(ginkgo.GinkgoT(), "target", 2, false)
		f := &fakeUTMControl{states: map[string]string{victim.VM.UTMVMUUID: "started", target.VM.UTMVMUUID: "stopped"}}
		if effect == "start" {
			f.states[victim.VM.UTMVMUUID] = "stopped"
		}
		entered, release := make(chan struct{}), make(chan struct{})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.utmCommand = func(ctx context.Context, executable string, args ...string) (utm.CommandResult, error) {
			if args[0] == effect {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return utm.CommandResult{}, ctx.Err()
				}
			}
			return f.command(ctx, executable, args...)
		}
		for _, spec := range []managed.ResourceSpec{victim, target} {
			_, err := c.Register(ctx, mustJSONScheduling(spec))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		guard, err := s.observeVMs(ctx, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = reserveControllerVM(s, target.ID, "delayed-transfer", guard)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var generation int64
		gomega.Expect(db.QueryRow(`SELECT operation_generation FROM managed_power_transitions WHERE target_resource_id='target'`).Scan(&generation)).To(gomega.Succeed())
		p, err := s.ledger.PowerIntent(generation)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		done := make(chan error, 1)
		go func() { _, err := s.advanceVMPower(ctx, p.Identity); done <- err }()
		defer func() {
			close(release)
			select {
			case <-done:
			case <-ctx.Done():
			}
		}()
		gomega.Eventually(entered).Should(gomega.BeClosed())
		if corruptGuard {
			proof := managed.PowerConfirmation{Identity: p.Identity, ExpectedStage: managed.PowerStartIssued}
			if effect == "stop" {
				proof.ExpectedStage = managed.PowerStopIssued
			}
			_, err = db.Exec(`UPDATE managed_power_progress SET observation_json=? WHERE operation_generation=?`, mustJSONScheduling(proof), generation)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(db.Close()).To(gomega.Succeed())
			reopened, err := sqlite.Open(s.root.DBPath)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			ginkgo.DeferCleanup(reopened.Close)
			s.ledger, err = managed.NewLedger(reopened, 1)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		// An operator effect is positive power evidence, but cannot establish that
		// the earlier claimed command has finished or cannot execute later.
		f.mu.Lock()
		if effect == "stop" {
			f.states[victim.VM.UTMVMUUID] = "stopped"
		} else {
			f.states[target.VM.UTMVMUUID] = "started"
		}
		f.mu.Unlock()
		for range 2 {
			_, err = s.advanceVMPower(ctx, p.Identity)
			if corruptGuard {
				gomega.Expect(errors.Is(err, managed.ErrBlocked)).To(gomega.BeTrue())
			} else {
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
		}
		current, err := s.ledger.PowerIntent(generation)
		if corruptGuard {
			gomega.Expect(errors.Is(err, managed.ErrBlocked)).To(gomega.BeTrue())
		} else {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		issued, _ := managed.IssuedPowerStage(effect)
		gomega.Expect(current.Stage).To(gomega.Equal(issued))
		q := managed.PowerConfirmation{Identity: p.Identity, ExpectedStage: issued, VictimPower: "stopped", TargetPower: "stopped", EvidenceDigest: strings.Repeat("a", 64)}
		if effect == "start" {
			q.TargetPower = "started"
		}
		gomega.Expect(errors.Is(s.ledger.ConfirmPower(q), managed.ErrBlocked)).To(gomega.BeTrue())
		f.mu.Lock()
		effects := append([]string(nil), f.effects...)
		f.mu.Unlock()
		gomega.Expect(effects).To(gomega.BeEmpty())
		protected := victim
		if effect == "start" {
			protected = target
		}
		protected.Revision++
		err = s.ledger.UpdateResource(mustJSONScheduling(protected), 1, 1)
		gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
	}, ginkgo.Entry("stop", "stop", false), ginkgo.Entry("start", "start", false), ginkgo.Entry("stop with lost guard after reopen", "stop", true), ginkgo.Entry("start with lost guard after reopen", "start", true))

	ginkgo.It("keeps a host claim protected after reopen when command return was never recorded", func() {
		s, c, db := availabilityFixture(ginkgo.GinkgoT())
		a := vmControllerSpec(ginkgo.GinkgoT(), "target", 1, false)
		f := &fakeUTMControl{states: map[string]string{a.VM.UTMVMUUID: "stopped"}}
		s.utmCommand = f.command
		_, err := c.Register(context.Background(), mustJSONScheduling(a))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		guard, err := s.observeVMs(context.Background(), 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = reserveControllerVM(s, a.ID, "lost-return", guard)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var generation int64
		gomega.Expect(db.QueryRow(`SELECT operation_generation FROM managed_power_transitions WHERE target_resource_id='target'`).Scan(&generation)).To(gomega.Succeed())
		p, err := s.ledger.PowerIntent(generation)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(s.ledger.ClaimHostPowerEffect(p.Identity, "start")).To(gomega.Succeed())
		f.states[a.VM.UTMVMUUID] = "started"
		gomega.Expect(db.Close()).To(gomega.Succeed())
		reopened, err := sqlite.Open(s.root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer reopened.Close()
		s.ledger, err = managed.NewLedger(reopened, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q := managed.PowerConfirmation{Identity: p.Identity, ExpectedStage: managed.PowerStartIssued, TargetPower: "started", EvidenceDigest: strings.Repeat("a", 64)}
		gomega.Expect(errors.Is(s.ledger.ConfirmPower(q), managed.ErrBlocked)).To(gomega.BeTrue())
		p, err = s.advanceVMPower(context.Background(), p.Identity)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.Stage).To(gomega.Equal(managed.PowerStartIssued))
		gomega.Expect(p.CommandReturned()).To(gomega.BeFalse())
		gomega.Expect(f.effects).To(gomega.BeEmpty())
	})

	ginkgo.DescribeTable("refuses corrupt or unrelated durable host command proofs", func(corruption string) {
		s, c, db := availabilityFixture(ginkgo.GinkgoT())
		a := vmControllerSpec(ginkgo.GinkgoT(), "target", 1, false)
		f := &fakeUTMControl{states: map[string]string{a.VM.UTMVMUUID: "stopped"}}
		s.utmCommand = f.command
		_, err := c.Register(context.Background(), mustJSONScheduling(a))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		guard, err := s.observeVMs(context.Background(), 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = reserveControllerVM(s, a.ID, "corrupt-return", guard)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var generation int64
		gomega.Expect(db.QueryRow(`SELECT operation_generation FROM managed_power_transitions WHERE target_resource_id='target'`).Scan(&generation)).To(gomega.Succeed())
		p, err := s.ledger.PowerIntent(generation)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(s.ledger.ClaimHostPowerEffect(p.Identity, "start")).To(gomega.Succeed())
		proof := managed.PowerConfirmation{Identity: p.Identity, ExpectedStage: managed.PowerStartIssued, CommandReturnRequired: true}
		raw := []byte("{")
		switch corruption {
		case "empty":
			raw = []byte{}
		case "null":
			raw = []byte("null")
		case "identity":
			proof.Identity.TargetConfigRevision++
			raw = mustJSONScheduling(proof)
		case "effect":
			proof.ExpectedStage = managed.PowerStopIssued
			raw = mustJSONScheduling(proof)
		case "missing guard":
			raw = bytes.Replace(mustJSONScheduling(proof), []byte(`,"CommandReturnRequired":true`), nil, 1)
		case "false guard":
			proof.CommandReturnRequired = false
			raw = mustJSONScheduling(proof)
		case "SQL NULL":
			raw = nil
		}
		_, err = db.Exec(`UPDATE managed_power_progress SET observation_json=? WHERE operation_generation=?`, raw, generation)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(db.Close()).To(gomega.Succeed())
		reopened, err := sqlite.Open(s.root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer reopened.Close()
		db = reopened
		s.ledger, err = managed.NewLedger(db, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f.states[a.VM.UTMVMUUID] = "started"
		_, err = s.ledger.PowerIntent(generation)
		gomega.Expect(errors.Is(err, managed.ErrBlocked)).To(gomega.BeTrue())
		q := managed.PowerConfirmation{Identity: p.Identity, ExpectedStage: managed.PowerStartIssued, TargetPower: "started", EvidenceDigest: strings.Repeat("a", 64)}
		gomega.Expect(errors.Is(s.ledger.ConfirmPower(q), managed.ErrBlocked)).To(gomega.BeTrue())
		_, err = s.advanceVMPower(context.Background(), p.Identity)
		gomega.Expect(errors.Is(err, managed.ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(f.effects).To(gomega.BeEmpty())
		a.Revision++
		gomega.Expect(errors.Is(s.ledger.UpdateResource(mustJSONScheduling(a), 1, 1), managed.ErrBusy)).To(gomega.BeTrue())
		var stage, state string
		gomega.Expect(db.QueryRow(`SELECT stage,state FROM managed_power_progress JOIN managed_power_transitions USING(operation_generation) WHERE operation_generation=?`, generation).Scan(&stage, &state)).To(gomega.Succeed())
		gomega.Expect(stage).To(gomega.Equal(managed.PowerStartIssued))
		gomega.Expect(state).To(gomega.Equal("pending"))
	}, ginkgo.Entry("truncated JSON", "truncated"), ginkgo.Entry("empty non-NULL blob", "empty"), ginkgo.Entry("JSON null", "null"), ginkgo.Entry("different identity", "identity"), ginkgo.Entry("different effect", "effect"), ginkgo.Entry("missing host guard", "missing guard"), ginkgo.Entry("false host guard", "false guard"), ginkgo.Entry("lost observation SQL NULL", "SQL NULL"))

	ginkgo.It("counts failed status observations conservatively and refuses a failed host list", func() {
		s, c, _ := availabilityFixture(ginkgo.GinkgoT())
		victim := vmControllerSpec(ginkgo.GinkgoT(), "victim", 1, true)
		target := vmControllerSpec(ginkgo.GinkgoT(), "target", 2, false)
		f := &fakeUTMControl{states: map[string]string{victim.VM.UTMVMUUID: "started", target.VM.UTMVMUUID: "stopped"}, failStatus: map[string]bool{victim.VM.UTMVMUUID: true}}
		s.utmCommand = f.command
		for _, spec := range []managed.ResourceSpec{victim, target} {
			_, err := c.Register(context.Background(), mustJSONScheduling(spec))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		guard, err := s.observeVMs(context.Background(), 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		stored, err := s.ledger.VMResources()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, v := range stored {
			if v.Spec.ID == victim.ID {
				gomega.Expect(v.Power).To(gomega.Equal("uncertain"))
			}
		}
		_, err = reserveControllerVM(s, target.ID, "failed-status", guard)
		gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
		s.utmCommand = func(ctx context.Context, executable string, args ...string) (utm.CommandResult, error) {
			if args[0] == "list" {
				return utm.CommandResult{}, fmt.Errorf("list unavailable")
			}
			return f.command(ctx, executable, args...)
		}
		_, err = s.observeVMs(context.Background(), 1)
		gomega.Expect(errors.Is(err, utm.ErrUnavailable)).To(gomega.BeTrue())
		gomega.Expect(f.effects).To(gomega.BeEmpty())
	})

	ginkgo.It("protects paused, unapproved and unregistered running VMs", func() {
		for _, mode := range []string{"paused", "policy", "operator"} {
			s, c, _ := availabilityFixture(ginkgo.GinkgoT())
			victim := vmControllerSpec(ginkgo.GinkgoT(), "victim", 1, mode != "policy")
			target := vmControllerSpec(ginkgo.GinkgoT(), "target", 2, false)
			f := &fakeUTMControl{states: map[string]string{victim.VM.UTMVMUUID: "started", target.VM.UTMVMUUID: "stopped"}}
			s.utmCommand = f.command
			for _, spec := range []managed.ResourceSpec{victim, target} {
				if mode == "operator" && spec.ID == victim.ID {
					continue
				}
				_, err := c.Register(context.Background(), mustJSONScheduling(spec))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			if mode == "paused" {
				_, _, err := c.SetAvailability(context.Background(), managed.AvailabilityRequest{ResourceID: victim.ID, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "pause-victim", Reason: "maintenance"}, false)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			guard, err := s.observeVMs(context.Background(), 1)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = reserveControllerVM(s, target.ID, "blocked", guard)
			var blocked *managed.HostCapacityBlocked
			gomega.Expect(errors.As(err, &blocked)).To(gomega.BeTrue())
			gomega.Expect(f.effects).To(gomega.BeEmpty())
		}
	})
})
