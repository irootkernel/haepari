//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/irootkernel/haepari/internal/controller"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

var _ = ginkgo.Describe("RejectedStateRootExitCode", ginkgo.Label("integration"), func() {
	ginkgo.It("RejectedStateRootExitCode", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		if err := os.Chmod(parent, 0755); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Setenv("HAEPARI_HOME", parent)
		var output, errors bytes.Buffer
		if code := run([]string{"daemon", "run"}, &output, &errors); code != 2 {
			t.Fatalf("rejected root exit %d: %s", code, errors.String())
		}

	})
})

var _ = ginkgo.Describe("MissingStateParentExitCode", ginkgo.Label("integration"), func() {
	ginkgo.It("MissingStateParentExitCode", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		missing := filepath.Join(parent, "missing")
		t.Setenv("HAEPARI_HOME", filepath.Join(missing, "state"))
		var output, errors bytes.Buffer
		if code := run([]string{"daemon", "run"}, &output, &errors); code != 2 {
			t.Fatalf("missing parent exit %d: %s", code, errors.String())
		}
		if !bytes.Contains(errors.Bytes(), []byte("parent directory must exist")) {
			t.Fatalf("missing parent diagnostic: %s", errors.String())
		}
		if _, err := os.Lstat(missing); !os.IsNotExist(err) {
			t.Fatalf("missing ancestor was created: %v", err)
		}

	})
})

var _ = ginkgo.Describe("StorePathRefusalExitCode", ginkgo.Label("integration"), func() {
	ginkgo.It("StorePathRefusalExitCode", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		root := filepath.Join(parent, "state")
		if err := os.Mkdir(root, 0700); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := os.Symlink(filepath.Join(parent, "outside"), filepath.Join(root, "state.sqlite")); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Setenv("HAEPARI_HOME", root)
		var output, errors bytes.Buffer
		if code := run([]string{"daemon", "run"}, &output, &errors); code != 2 {
			t.Fatalf("store path refusal exit %d: %s", code, errors.String())
		}

	})
})

var _ = ginkgo.Describe("StorageFailureExitCode", ginkgo.Label("integration"), func() {
	ginkgo.It("StorageFailureExitCode", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		root := filepath.Join(parent, "state")
		if err := os.Mkdir(root, 0700); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := os.WriteFile(filepath.Join(root, "state.sqlite"), []byte("not a SQLite database"), 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Setenv("HAEPARI_HOME", root)
		var output, errors bytes.Buffer
		if code := run([]string{"daemon", "run"}, &output, &errors); code != 6 {
			t.Fatalf("corrupt storage exit %d: %s", code, errors.String())
		}

	})
})

var _ = ginkgo.Describe("UnavailableControllerDiagnostic", ginkgo.Label("integration"), func() {
	ginkgo.It("UnavailableControllerDiagnostic", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		root, err := state.Resolve(filepath.Join(parent, "state"))
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := root.Prepare(); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Setenv("HAEPARI_HOME", root.Path)
		var output, diagnostic bytes.Buffer
		if code := run([]string{"inspect", "job", "--json"}, &output, &diagnostic); code != 6 {
			t.Fatalf("unavailable controller exit %d: %s", code, diagnostic.String())
		}
		if !strings.Contains(diagnostic.String(), "check the daemon and HAEPARI_HOME") {
			t.Fatalf("missing recovery hint: %s", diagnostic.String())
		}

	})
})

var _ = ginkgo.Describe("LocalCLIAdmissionWaitAndReplay", ginkgo.Label("integration"), func() {
	ginkgo.It("LocalCLIAdmissionWaitAndReplay", func() {
		t := ginkgo.GinkgoT()
		if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
			t.Skip("local controller requires Darwin arm64")
		}
		parent, err := os.MkdirTemp("/tmp", "hp-cli-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		root, err := state.Resolve(filepath.Join(parent, "state"))
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := root.Prepare(); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Setenv("HAEPARI_HOME", root.Path)
		source := filepath.Join(parent, "input")
		if err := os.MkdirAll(filepath.Join(source, "source"), 0700); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := os.WriteFile(filepath.Join(source, "source", "input.txt"), []byte("input\n"), 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		store, err := snapshot.OpenStore(root.Path)
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		sealed, err := store.Seal(context.Background(), source, []string{"source/input.txt"}, snapshot.Provenance{EvidenceClass: "development"})
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- controller.Run(ctx, root) }()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("controller shutdown: %v", err)
			}
		}()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Lstat(root.SocketPath); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("controller socket did not appear")
			}
			time.Sleep(10 * time.Millisecond)
		}
		resource := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "local-one", Kind: "local", ExecutionMode: "managed", Capacity: 1, Revision: 1, Adapter: managed.Adapter{QualificationReference: "cli-integration"}, Cleanup: &managed.CleanupPolicy{PolicyReference: "owned-workspace"}}
		resourceJSON, _ := json.Marshal(resource)
		resourcePath := filepath.Join(parent, "resource.json")
		if err := os.WriteFile(resourcePath, resourceJSON, 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		var out, diagnostic bytes.Buffer
		if code := run([]string{"resource", "register", "--file", resourcePath, "--json"}, &out, &diagnostic); code != 0 {
			t.Fatalf("register: exit %d: %s", code, diagnostic.String())
		}
		// Invalid explicit pins must leave both resource state and mutation identity unchanged.
		var registered struct {
			Data managed.ResourceStatus `json:"data"`
		}
		out.Reset()
		diagnostic.Reset()
		gomega.Expect(run([]string{"resource", "inspect", "local-one", "--json"}, &out, &diagnostic)).To(gomega.BeZero(), diagnostic.String())
		gomega.Expect(json.Unmarshal(out.Bytes(), &registered)).To(gomega.Succeed())
		for _, action := range []string{"pause", "resume"} {
			for _, pins := range [][]string{
				{"--expected-config-revision", "0"},
				{"--expected-lease-generation", "-1"},
				{"--expected-config-revision", "0", "--expected-lease-generation", "-1"},
				{"--expected-config-revision", "0", "--expected-lease-generation", "0"},
				{"--expected-config-revision", "1"},
				{"--expected-lease-generation", "0"},
			} {
				out.Reset()
				diagnostic.Reset()
				args := append([]string{"resource", action, "local-one", "--expected-revision", "1", "--request-key", "cli-pause", "--json"}, pins...)
				gomega.Expect(run(args, &out, &diagnostic)).To(gomega.Equal(2), diagnostic.String())
				gomega.Expect(out.Len()).To(gomega.BeZero())
				diagnostic.Reset()
				gomega.Expect(run([]string{"resource", "inspect", "local-one", "--json"}, &out, &diagnostic)).To(gomega.BeZero(), diagnostic.String())
				var unchanged struct {
					Data managed.ResourceStatus `json:"data"`
				}
				gomega.Expect(json.Unmarshal(out.Bytes(), &unchanged)).To(gomega.Succeed())
				gomega.Expect(unchanged.Data).To(gomega.Equal(registered.Data))
			}
		}
		for _, args := range [][]string{
			{"resource", "pause", "local-one", "--expected-revision", "1", "--request-key", "cli-pause", "--expected-config-revision", "1", "--expected-lease-generation", "0", "--json"},
			{"resource", "pause", "local-one", "--expected-revision", "1", "--request-key", "cli-pause", "--json"},
			{"resource", "resume", "local-one", "--expected-revision", "2", "--request-key", "cli-resume", "--expected-config-revision", "1", "--expected-lease-generation", "0", "--json"},
			{"resource", "resume", "local-one", "--expected-revision", "2", "--request-key", "cli-resume", "--expected-config-revision", "1", "--expected-lease-generation", "0", "--json"},
		} {
			out.Reset()
			diagnostic.Reset()
			if code := run(args, &out, &diagnostic); code != 0 {
				t.Fatalf("availability %v: %d %s %s", args, code, out.String(), diagnostic.String())
			}
			var value struct {
				Data managed.ResourceStatus `json:"data"`
			}
			if err := json.Unmarshal(out.Bytes(), &value); err != nil || value.Data.Mutation == nil {
				t.Fatalf("availability receipt: %s %v", out.String(), err)
			}
		}
		digest := strings.Repeat("a", 64)
		job := managed.JobSpec{Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: "local-one", Mode: "auto", Suite: "cli", Input: managed.Input{Root: source, InventoryDigest: strings.TrimPrefix(sealed.Manifest.TreeSHA256, "sha256:"), SelectedFiles: []string{"source/input.txt"}}, Environment: managed.Environment{ProfileID: "local", ProfileDigest: digest}, Workload: managed.Workload{Argv: []string{"/bin/sh", "-c", "mkdir -p ../results; cat input.txt > ../results/out.txt"}, CWD: "source", RequiredOutputs: []string{"results/out.txt"}}, Deadlines: managed.Deadlines{QueueSeconds: 30, PrepareSeconds: 30, RunSeconds: 30, CollectSeconds: 30, CleanupSeconds: 30}, Effects: managed.Effects{Mode: "offline", AuthorizationReference: "cli-integration"}, Provenance: managed.Provenance{CandidateDigest: digest, TestsDigest: digest, TargetProfileDigest: digest, EvidenceClass: "development"}}
		jobJSON, _ := json.Marshal(job)
		jobPath := filepath.Join(parent, "job.json")
		if err := os.WriteFile(jobPath, jobJSON, 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		submit := func() managed.Receipt {
			out.Reset()
			diagnostic.Reset()
			if code := run([]string{"submit", "--spec", jobPath, "--request-key", "cli-key", "--json"}, &out, &diagnostic); code != 0 {
				t.Fatalf("submit: exit %d: %s", code, diagnostic.String())
			}
			var response struct {
				Data managed.Receipt `json:"data"`
			}
			if err := json.Unmarshal(out.Bytes(), &response); err != nil || response.Data.JobID == "" {
				t.Fatalf("submit receipt: %+v %v", response, err)
			}
			return response.Data
		}
		receipt := submit()
		out.Reset()
		diagnostic.Reset()
		if code := run([]string{"wait", receipt.JobID, "--timeout", "5s", "--json"}, &out, &diagnostic); code != 0 {
			t.Fatalf("wait: exit %d: %s", code, diagnostic.String())
		}
		var final struct {
			Data managed.Result `json:"data"`
		}
		if err := json.Unmarshal(out.Bytes(), &final); err != nil || final.Data.FinalVerdict != managed.Pass {
			t.Fatalf("wait result: %+v %v", final, err)
		}
		out.Reset()
		diagnostic.Reset()
		if code := run([]string{"resource", "inspect", "local-one", "--json"}, &out, &diagnostic); code != 0 {
			t.Fatalf("resource inspect: %d %s", code, diagnostic.String())
		}
		var resourceStatus struct {
			Data managed.ResourceStatus `json:"data"`
		}
		gomega.Expect(json.Unmarshal(out.Bytes(), &resourceStatus)).To(gomega.Succeed())
		gomega.Expect(resourceStatus.Data.LeaseGeneration).To(gomega.BeNumerically(">", 0))
		out.Reset()
		diagnostic.Reset()
		gomega.Expect(os.Chmod(filepath.Join(root.Path, "jobs"), 0755)).To(gomega.Succeed())
		if code := run([]string{"resource", "resume", "local-one", "--expected-revision", "2", "--request-key", "cli-resume", "--expected-config-revision", "1", "--expected-lease-generation", "0", "--json"}, &out, &diagnostic); code != 0 {
			t.Fatalf("pinned replay after lease return: %d %s %s", code, out.String(), diagnostic.String())
		}
		var replayed struct {
			Data managed.ResourceStatus `json:"data"`
		}
		gomega.Expect(json.Unmarshal(out.Bytes(), &replayed)).To(gomega.Succeed())
		gomega.Expect(replayed.Data.Availability).To(gomega.Equal("available"))
		gomega.Expect(replayed.Data.LeaseGeneration).To(gomega.Equal(resourceStatus.Data.LeaseGeneration))
		gomega.Expect(replayed.Data.Mutation.MutationKey).To(gomega.Equal("cli-resume"))
		gomega.Expect(os.Chmod(filepath.Join(root.Path, "jobs"), 0700)).To(gomega.Succeed())
		// No explicit key creates a bounded cryptographic mutation identity.
		for _, args := range [][]string{{"resource", "pause", "local-one", "--expected-revision", "3", "--json"}, {"resource", "resume", "local-one", "--expected-revision", "4", "--json"}} {
			out.Reset()
			diagnostic.Reset()
			if code := run(args, &out, &diagnostic); code != 0 {
				t.Fatalf("generated key: %d %s", code, diagnostic.String())
			}
			gomega.Expect(json.Unmarshal(out.Bytes(), &resourceStatus)).To(gomega.Succeed())
			gomega.Expect(resourceStatus.Data.Mutation).NotTo(gomega.BeNil())
			gomega.Expect(resourceStatus.Data.Mutation.MutationKey).To(gomega.HavePrefix("availability-"))
		}
		if replay := submit(); replay.JobID != receipt.JobID {
			t.Fatalf("same-key replay created another job: %+v", replay)
		}
		out.Reset()
		diagnostic.Reset()
		if code := run([]string{"inspect", receipt.JobID, "--json"}, &out, &diagnostic); code != 0 || !bytes.Contains(out.Bytes(), []byte(`"final_verdict":"PASS"`)) {
			t.Fatalf("inspect: exit %d: %s %s", code, out.String(), diagnostic.String())
		}
		out.Reset()
		diagnostic.Reset()
		if code := run([]string{"run", "--spec", jobPath, "--request-key", "cli-run-key", "--json"}, &out, &diagnostic); code != 0 {
			t.Fatalf("run: exit %d: %s", code, diagnostic.String())
		}
		lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
		if len(lines) != 2 || !bytes.Contains(lines[0], []byte(`"kind":"admission"`)) || !bytes.Contains(lines[1], []byte(`"kind":"final"`)) {
			t.Fatalf("run NDJSON: %s", out.String())
		}
		slow := job
		slow.Workload.Argv = []string{"/bin/sh", "-c", "sleep 0.3; mkdir -p ../results; cat input.txt > ../results/out.txt"}
		slowJSON, _ := json.Marshal(slow)
		if err := os.WriteFile(jobPath, slowJSON, 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		out.Reset()
		diagnostic.Reset()
		if code := run([]string{"submit", "--spec", jobPath, "--request-key", "cli-slow-key", "--json"}, &out, &diagnostic); code != 0 {
			t.Fatalf("slow submit: exit %d: %s", code, diagnostic.String())
		}
		var slowAdmission struct {
			Data managed.Receipt `json:"data"`
		}
		if err := json.Unmarshal(out.Bytes(), &slowAdmission); err != nil || slowAdmission.Data.JobID == "" {
			t.Fatalf("slow admission: %+v %v", slowAdmission, err)
		}
		out.Reset()
		diagnostic.Reset()
		if code := run([]string{"wait", slowAdmission.Data.JobID, "--timeout", "10ms", "--json"}, &out, &diagnostic); code != 5 || !bytes.Contains(out.Bytes(), []byte(`"code":"WAIT_TIMEOUT"`)) {
			t.Fatalf("wait timeout: exit %d: %s %s", code, out.String(), diagnostic.String())
		}
		out.Reset()
		diagnostic.Reset()
		if code := run([]string{"wait", slowAdmission.Data.JobID, "--timeout", "5s", "--json"}, &out, &diagnostic); code != 0 {
			t.Fatalf("wait reconnect: exit %d: %s", code, diagnostic.String())
		}
		deadlineJob := job
		deadlineJob.Deadlines.RunSeconds = 1
		deadlineJob.Workload.Argv = []string{"/bin/sh", "-c", "mkdir -p ../results; printf timed > ../results/out.txt; trap '' TERM; exec /bin/sleep 30"}
		deadlineJSON, _ := json.Marshal(deadlineJob)
		if err := os.WriteFile(jobPath, deadlineJSON, 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		out.Reset()
		diagnostic.Reset()
		if code := run([]string{"run", "--spec", jobPath, "--request-key", "cli-deadline-key", "--json"}, &out, &diagnostic); code != 3 {
			t.Fatalf("run deadline: exit %d: %s %s", code, out.String(), diagnostic.String())
		}
		lines = bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
		if len(lines) != 2 {
			t.Fatalf("run deadline NDJSON: %s", out.String())
		}
		var deadlineFinal struct {
			Data managed.Result `json:"data"`
		}
		if err := json.Unmarshal(lines[1], &deadlineFinal); err != nil || deadlineFinal.Data.FinalVerdict != managed.Incomplete || deadlineFinal.Data.WorkloadOutcome != "timed_out" || deadlineFinal.Data.Reason != "run_deadline" || deadlineFinal.Data.NativeSignal == "" || deadlineFinal.Data.CleanupOutcome != "verified" {
			t.Fatalf("run deadline result: %+v %v", deadlineFinal, err)
		}

	})
})

var _ = ginkgo.Describe("ExistingSocketExitCode", ginkgo.Label("integration"), func() {
	ginkgo.It("ExistingSocketExitCode", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		root := filepath.Join(parent, "state")
		if err := os.Mkdir(root, 0700); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		socket := filepath.Join(root, "control.sock")
		if err := os.WriteFile(socket, nil, 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Setenv("HAEPARI_HOME", root)
		var output, errors bytes.Buffer
		if code := run([]string{"daemon", "run"}, &output, &errors); code != 2 {
			t.Fatalf("existing socket exit %d: %s", code, errors.String())
		}
		if !bytes.Contains(errors.Bytes(), []byte(socket)) {
			t.Fatalf("socket path missing from diagnostic: %s", errors.String())
		}

	})
})

var _ = ginkgo.Describe("UnsupportedBackendDoesNotCreateState", ginkgo.Label("integration"), func() {
	ginkgo.It("UnsupportedBackendDoesNotCreateState", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		root := filepath.Join(parent, "state")
		t.Setenv("HAEPARI_HOME", root)
		var output, errors bytes.Buffer
		if code := run([]string{"daemon", "run", "--backend", "utm-macos"}, &output, &errors); code != 2 {
			t.Fatalf("unsupported backend exit %d: %s", code, errors.String())
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("unsupported backend created state: %v", err)
		}

	})
})

var _ = ginkgo.Describe("DaemonSIGHUPShutdown", ginkgo.Label("integration"), func() {
	ginkgo.It("DaemonSIGHUPShutdown", func() {
		t := ginkgo.GinkgoT()
		if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
			t.Skip("bootstrap daemon requires Darwin arm64")
		}
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		root := filepath.Join(parent, "state")
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "HAEPARI_TEST_DAEMON=1", "HAEPARI_HOME="+root)
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Start(); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		finished := false
		defer func() {
			if !finished {
				_ = cmd.Process.Kill()
				<-done
			}
		}()
		socket := filepath.Join(root, "control.sock")
		deadline := time.After(5 * time.Second)
		for {
			if _, err := os.Lstat(socket); err == nil {
				break
			}
			select {
			case err := <-done:
				finished = true
				t.Fatalf("daemon stopped before startup: %v: %s", err, output.String())
			case <-deadline:
				t.Fatal("daemon socket did not appear")
			case <-time.After(10 * time.Millisecond):
			}
		}
		if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		select {
		case err := <-done:
			finished = true
			if err != nil {
				t.Fatalf("daemon SIGHUP shutdown: %v: %s", err, output.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("daemon did not stop after SIGHUP")
		}
		if _, err := os.Lstat(socket); !os.IsNotExist(err) {
			t.Fatalf("socket remains after SIGHUP: %v", err)
		}

	})
})

var _ = ginkgo.Describe("ControllerOwnershipExitCode", ginkgo.Label("integration"), func() {
	ginkgo.It("returns a deterministic refusal without effects", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer os.RemoveAll(parent)
		root, err := state.Resolve(filepath.Join(parent, "state"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(root.Prepare()).To(gomega.Succeed())
		owner, err := root.AcquireController()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer owner.Close()
		t.Setenv("HAEPARI_HOME", root.Path)
		var output, diagnostic bytes.Buffer
		gomega.Expect(run([]string{"daemon", "run"}, &output, &diagnostic)).To(gomega.Equal(2))
		gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring("owned by another controller"))
		gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring(root.Path))
		_, err = os.Lstat(root.DBPath)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
	ginkgo.It("recovers the recorded socket after killing a real foreground daemon", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer os.RemoveAll(parent)
		root, err := state.Resolve(filepath.Join(parent, "state"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		output, err := os.Create(filepath.Join(parent, "daemon.log"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer output.Close()
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "HAEPARI_TEST_DAEMON=1", "HAEPARI_HOME="+root.Path)
		child.Stdout = output
		child.Stderr = output
		gomega.Expect(child.Start()).To(gomega.Succeed())
		waited := false
		t.Cleanup(func() {
			if !waited {
				_ = child.Process.Kill()
				_ = child.Wait()
			}
		})
		ready := false
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Lstat(root.SocketPath); err == nil {
				ready = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !ready {
			data, _ := os.ReadFile(output.Name())
			t.Fatalf("daemon startup: %s", data)
		}
		original, err := os.Lstat(root.SocketPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(child.Process.Kill()).To(gomega.Succeed())
		gomega.Expect(child.Wait()).To(gomega.HaveOccurred())
		waited = true
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- controller.Run(ctx, root) }()
		recovered := false
		deadline = time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if current, err := os.Lstat(root.SocketPath); err == nil && !os.SameFile(original, current) {
				recovered = true
				break
			}
			select {
			case err := <-done:
				t.Fatalf("restart failed: %v", err)
			case <-time.After(10 * time.Millisecond):
			}
		}
		gomega.Expect(recovered).To(gomega.BeTrue())
		cancel()
		gomega.Expect(<-done).To(gomega.Succeed())
	})
})

var _ = ginkgo.Describe("InvalidControllerLockExitCode", ginkgo.Label("integration"), func() {
	ginkgo.It("rejects an insecure lock before database creation", func() {
		parent, err := os.MkdirTemp("/tmp", "hp-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer os.RemoveAll(parent)
		root, err := state.Resolve(filepath.Join(parent, "state"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(root.Prepare()).To(gomega.Succeed())
		path := filepath.Join(root.Path, "controller.lock")
		gomega.Expect(os.WriteFile(path, nil, 0644)).To(gomega.Succeed())
		gomega.Expect(os.Chmod(path, 0644)).To(gomega.Succeed())
		t := ginkgo.GinkgoT()
		t.Setenv("HAEPARI_HOME", root.Path)
		var output, diagnostic bytes.Buffer
		gomega.Expect(run([]string{"daemon", "run"}, &output, &diagnostic)).To(gomega.Equal(2))
		gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring(path))
		_, err = os.Lstat(root.DBPath)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
})

var _ = ginkgo.Describe("UnavailableAdmissionRun", ginkgo.Label("integration"), func() {
	ginkgo.It("emits the known admission then exits 2 without starting observation or inventing a verdict", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-cli-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer os.RemoveAll(parent)
		root, err := state.Resolve(filepath.Join(parent, "state"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(root.Prepare()).To(gomega.Succeed())
		spec := filepath.Join(parent, "job.json")
		gomega.Expect(os.WriteFile(spec, []byte(`{}`), 0600)).To(gomega.Succeed())
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: root.SocketPath, Net: "unix"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var waits atomic.Int32
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/hello":
				_ = json.NewEncoder(w).Encode(map[string]any{"schema": "haepari.envelope/v1", "operation": "hello", "ok": true, "data": map[string]string{"protocol": "managed-execution/v1", "installation_id": "fixture"}})
			case "/v1/jobs":
				_ = json.NewEncoder(w).Encode(map[string]any{"schema": "haepari.envelope/v1", "operation": "submit", "ok": true, "data": managed.Receipt{JobID: "known-job", JobState: "finished", ResourceID: "local-one", ResultStatus: "unavailable"}})
			case "/v1/jobs/known-job/wait":
				waits.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"schema": "haepari.envelope/v1", "operation": "wait", "ok": false, "error": map[string]string{"code": "RESULT_UNAVAILABLE", "message": "Known admission has no retained result.", "retry": "none"}})

			default:
				waits.Add(1)
				http.Error(w, "unexpected wait", 500)
			}
		})}
		done := make(chan error, 1)
		go func() { done <- server.Serve(listener) }()
		defer func() { _ = server.Close(); <-done }()
		t.Setenv("HAEPARI_HOME", root.Path)

		for _, command := range []string{"submit", "run"} {
			for _, jsonOutput := range []bool{false, true} {
				if command == "submit" && !jsonOutput {
					continue
				}
				args := []string{command, "--spec", spec, "--request-key", "known-key"}
				if jsonOutput {
					args = append(args, "--json")
				}
				var output, diagnostic bytes.Buffer
				expectedExit := 0
				if command == "run" {
					expectedExit = 2
				}
				gomega.Expect(run(args, &output, &diagnostic)).To(gomega.Equal(expectedExit))
				gomega.Expect(output.String()).To(gomega.ContainSubstring("known-job"))
				if command == "run" {
					gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring("RESULT_UNAVAILABLE"))
				} else {
					gomega.Expect(diagnostic.String()).To(gomega.BeEmpty())
				}
				gomega.Expect(output.String()).NotTo(gomega.ContainSubstring("final_verdict"))
				if jsonOutput {
					gomega.Expect(strings.Count(output.String(), "\n")).To(gomega.Equal(1))
				}
			}
		}
		gomega.Expect(waits.Load()).To(gomega.BeZero())
		var output, diagnostic bytes.Buffer
		gomega.Expect(run([]string{"wait", "known-job", "--json"}, &output, &diagnostic)).To(gomega.Equal(2))
		gomega.Expect(waits.Load()).To(gomega.Equal(int32(1)))
		gomega.Expect(output.String()).To(gomega.ContainSubstring("RESULT_UNAVAILABLE"))
		gomega.Expect(output.String()).NotTo(gomega.ContainSubstring("final_verdict"))
		gomega.Expect(strings.Count(output.String(), "\n")).To(gomega.Equal(1))
		gomega.Expect(diagnostic.String()).To(gomega.BeEmpty())

	})
})
