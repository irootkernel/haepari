package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
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
