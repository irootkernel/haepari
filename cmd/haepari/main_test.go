package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/controller"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
)

func TestVersionIdentity(t *testing.T) {
	var output, errors bytes.Buffer
	if code := run([]string{"version", "--json"}, &output, &errors); code != 0 {
		t.Fatalf("version exit %d: %s", code, errors.String())
	}
	var result struct {
		Schema    string `json:"schema"`
		Operation string `json:"operation"`
		OK        bool   `json:"ok"`
		Data      struct {
			Version          string `json:"version"`
			ExecutableSHA256 string `json:"executable_sha256"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Schema != "haepari.envelope/v1" || result.Operation != "version" || !result.OK || result.Data.Version == "" || len(result.Data.ExecutableSHA256) != 71 {
		t.Fatalf("incomplete identity: %+v", result)
	}
}

func TestCapabilities(t *testing.T) {
	var output, errors bytes.Buffer
	if code := run([]string{"capabilities", "--json"}, &output, &errors); code != 0 {
		t.Fatalf("capabilities exit %d: %s", code, errors.String())
	}
	var result struct {
		Schema    string `json:"schema"`
		Operation string `json:"operation"`
		OK        bool   `json:"ok"`
		Data      struct {
			CompiledBackends  []string `json:"compiled_backends"`
			QualifiedBackends []string `json:"qualified_backends"`
			Protocols         []string `json:"protocol_versions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Schema != "haepari.envelope/v1" || result.Operation != "capabilities" || !result.OK || len(result.Data.CompiledBackends) != 1 || result.Data.CompiledBackends[0] != "local" || len(result.Data.QualifiedBackends) != 0 || len(result.Data.Protocols) != 1 || result.Data.Protocols[0] != "managed-execution/v1" {
		t.Fatalf("unexpected capabilities: %+v", result)
	}
}

func TestRejectedStateRootExitCode(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	if err := os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAEPARI_HOME", parent)
	var output, errors bytes.Buffer
	if code := run([]string{"daemon", "run"}, &output, &errors); code != 2 {
		t.Fatalf("rejected root exit %d: %s", code, errors.String())
	}
}

func TestMissingStateParentExitCode(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
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
}

func TestStorePathRefusalExitCode(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	root := filepath.Join(parent, "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(parent, "outside"), filepath.Join(root, "state.sqlite")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAEPARI_HOME", root)
	var output, errors bytes.Buffer
	if code := run([]string{"daemon", "run"}, &output, &errors); code != 2 {
		t.Fatalf("store path refusal exit %d: %s", code, errors.String())
	}
}

func TestStorageFailureExitCode(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	root := filepath.Join(parent, "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state.sqlite"), []byte("not a SQLite database"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAEPARI_HOME", root)
	var output, errors bytes.Buffer
	if code := run([]string{"daemon", "run"}, &output, &errors); code != 6 {
		t.Fatalf("corrupt storage exit %d: %s", code, errors.String())
	}
}

func TestUsageRejectsUnsupportedArguments(t *testing.T) {
	for _, args := range [][]string{
		{}, {"version"}, {"version", "--json", "extra"},
		{"capabilities"}, {"daemon", "run", "extra"},
		{"daemon", "run", "--backend"}, {"daemon", "run", "--nope"},
		{"runner"}, {"unknown"},
		{"resource", "register"}, {"resource", "register", "--file", "resource.json"},
		{"submit"}, {"submit", "--spec", "job.json", "--json"},
		{"run"}, {"run", "--spec", "job.json"},
		{"wait", "job"}, {"wait", "job", "--json", "--timeout", "0s"},
		{"wait", "job", "--json", "--timeout", "25h"},
	} {
		var output, errors bytes.Buffer
		if code := run(args, &output, &errors); code != 2 {
			t.Errorf("%q: exit %d: %s", args, code, errors.String())
		}
		if errors.Len() == 0 {
			t.Errorf("%q: rejected without a diagnostic", args)
		}
	}
}

func TestUnavailableControllerDiagnostic(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	root, err := state.Resolve(filepath.Join(parent, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Prepare(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAEPARI_HOME", root.Path)
	var output, diagnostic bytes.Buffer
	if code := run([]string{"inspect", "job", "--json"}, &output, &diagnostic); code != 6 {
		t.Fatalf("unavailable controller exit %d: %s", code, diagnostic.String())
	}
	if !strings.Contains(diagnostic.String(), "check the daemon and HAEPARI_HOME") {
		t.Fatalf("missing recovery hint: %s", diagnostic.String())
	}
}

func TestLocalCLIAdmissionWaitAndReplay(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("local controller requires Darwin arm64")
	}
	parent, err := os.MkdirTemp("/tmp", "hp-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	root, err := state.Resolve(filepath.Join(parent, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Prepare(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAEPARI_HOME", root.Path)
	source := filepath.Join(parent, "input")
	if err := os.MkdirAll(filepath.Join(source, "source"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "source", "input.txt"), []byte("input\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := snapshot.OpenStore(root.Path)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := store.Seal(context.Background(), source, []string{"source/input.txt"}, snapshot.Provenance{EvidenceClass: "development"})
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	if code := run([]string{"resource", "register", "--file", resourcePath, "--json"}, &out, &diagnostic); code != 0 {
		t.Fatalf("register: exit %d: %s", code, diagnostic.String())
	}
	digest := strings.Repeat("a", 64)
	job := managed.JobSpec{Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: "local-one", Mode: "auto", Suite: "cli", Input: managed.Input{Root: source, InventoryDigest: strings.TrimPrefix(sealed.Manifest.TreeSHA256, "sha256:"), SelectedFiles: []string{"source/input.txt"}}, Environment: managed.Environment{ProfileID: "local", ProfileDigest: digest}, Workload: managed.Workload{Argv: []string{"/bin/sh", "-c", "mkdir -p ../results; cat input.txt > ../results/out.txt"}, CWD: "source", RequiredOutputs: []string{"results/out.txt"}}, Deadlines: managed.Deadlines{QueueSeconds: 30, PrepareSeconds: 30, RunSeconds: 30, CollectSeconds: 30, CleanupSeconds: 30}, Effects: managed.Effects{Mode: "offline", AuthorizationReference: "cli-integration"}, Provenance: managed.Provenance{CandidateDigest: digest, TestsDigest: digest, TargetProfileDigest: digest, EvidenceClass: "development"}}
	jobJSON, _ := json.Marshal(job)
	jobPath := filepath.Join(parent, "job.json")
	if err := os.WriteFile(jobPath, jobJSON, 0600); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
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
		t.Fatal(err)
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
}

func TestCLIContractErrorAndVerdictExits(t *testing.T) {
	for _, tc := range []struct {
		code string
		want int
	}{
		{"CONFLICT", 2}, {"UNSUPPORTED", 2}, {"WAIT_TIMEOUT", 5},
		{"STORAGE_UNAVAILABLE", 6}, {"TRANSPORT_UNCERTAIN", 6},
	} {
		var out, diagnostic bytes.Buffer
		issue := &client.Issue{Code: tc.code, Message: "bounded explanation", Retry: "none"}
		envelope := client.Envelope{Schema: "haepari.envelope/v1", Operation: "submit", Error: issue}
		if got := clientFailure(&out, &diagnostic, envelope, issue); got != tc.want {
			t.Errorf("%s exit = %d, want %d", tc.code, got, tc.want)
		}
		if out.Len() == 0 {
			t.Errorf("%s error envelope missing", tc.code)
		}
	}
	for _, tc := range []struct {
		verdict managed.Verdict
		want    int
	}{
		{managed.Pass, 0}, {managed.Fail, 1}, {managed.Incomplete, 3}, {managed.Cancelled, 4},
	} {
		if got := verdictExit(tc.verdict); got != tc.want {
			t.Errorf("%s exit = %d, want %d", tc.verdict, got, tc.want)
		}
	}
}

func TestExistingSocketExitCode(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	root := filepath.Join(parent, "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "control.sock")
	if err := os.WriteFile(socket, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAEPARI_HOME", root)
	var output, errors bytes.Buffer
	if code := run([]string{"daemon", "run"}, &output, &errors); code != 2 {
		t.Fatalf("existing socket exit %d: %s", code, errors.String())
	}
	if !bytes.Contains(errors.Bytes(), []byte(socket)) {
		t.Fatalf("socket path missing from diagnostic: %s", errors.String())
	}
}

func TestUnsupportedBackendDoesNotCreateState(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
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
}

func TestDaemonSIGHUPShutdown(t *testing.T) {
	if os.Getenv("HAEPARI_TEST_DAEMON") == "1" {
		os.Exit(run([]string{"daemon", "run"}, os.Stdout, os.Stderr))
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("bootstrap daemon requires Darwin arm64")
	}
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	root := filepath.Join(parent, "state")
	cmd := exec.Command(os.Args[0], "-test.run=^TestDaemonSIGHUPShutdown$")
	cmd.Env = append(os.Environ(), "HAEPARI_TEST_DAEMON=1", "HAEPARI_HOME="+root)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
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
}
