package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/controller"
	"github.com/irootkernel/haepari/internal/identity"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
)

type envelope struct {
	Schema    string `json:"schema"`
	Operation string `json:"operation"`
	OK        bool   `json:"ok"`
	Data      any    `json:"data,omitempty"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: haepari version --json | capabilities --json | daemon run | resource register/inspect/pause/resume | resources list | jobs list | submit | run | inspect | wait | events | logs | cancel")
		return 2
	}
	switch args[0] {
	case "version":
		if len(args) != 2 || args[1] != "--json" {
			fmt.Fprintln(stderr, "usage: haepari version --json")
			return 2
		}
		info, err := identity.Read()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 6
		}
		return writeJSON(stdout, stderr, "version", info)
	case "capabilities":
		if len(args) != 2 || args[1] != "--json" {
			fmt.Fprintln(stderr, "usage: haepari capabilities --json")
			return 2
		}
		return writeJSON(stdout, stderr, "capabilities", struct {
			CompiledBackends  []string `json:"compiled_backends"`
			QualifiedBackends []string `json:"qualified_backends"`
			Protocols         []string `json:"protocol_versions"`
			Capabilities      []string `json:"capabilities"`
		}{platform.CompiledBackends(), []string{}, []string{"managed-execution/v1"}, []string{"managed-execution/v1", "durable-observation/v1"}})
	case "daemon":
		if len(args) < 2 || args[1] != "run" {
			fmt.Fprintln(stderr, "usage: haepari daemon run [--backend local]")
			return 2
		}
		flags := flag.NewFlagSet("daemon run", flag.ContinueOnError)
		flags.SetOutput(stderr)
		backend := flags.String("backend", platform.DefaultBackend, "bootstrap backend")
		if err := flags.Parse(args[2:]); err != nil {
			return 2
		}
		if flags.NArg() != 0 {
			fmt.Fprintln(stderr, "usage: haepari daemon run [--backend local]")
			return 2
		}
		if err := platform.Validate(runtime.GOOS, runtime.GOARCH, *backend); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		root, err := state.Resolve(os.Getenv("HAEPARI_HOME"))
		if err != nil {
			fmt.Fprintln(stderr, err)
			if errors.Is(err, state.ErrInvalidRoot) {
				return 2
			}
			return 6
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
		if err := controller.Run(ctx, root); err != nil {
			fmt.Fprintln(stderr, err)
			if errors.Is(err, state.ErrControllerOwned) || errors.Is(err, state.ErrInvalidRoot) || errors.Is(err, controller.ErrSocketExists) || errors.Is(err, sqlite.ErrInvalidStore) {
				return 2
			}
			return 6
		}
		return 0
	case "resource":
		if len(args) > 1 && args[1] == "inspect" {
			return resourceInspect(args[2:], stdout, stderr)
		}
		if len(args) > 1 && (args[1] == "pause" || args[1] == "resume") {
			return availability(args[1], args[2:], stdout, stderr)
		}
		if len(args) < 2 || args[1] != "register" {
			fmt.Fprintln(stderr, "usage: haepari resource register --file <json> --json | inspect <id> --json | pause/resume <id> --expected-revision <n> --json")
			return 2
		}
		return register(args[2:], stdout, stderr)
	case "submit":
		return submit(args[1:], stdout, stderr, false)
	case "run":
		return submit(args[1:], stdout, stderr, true)
	case "inspect":
		return inspect(args[1:], stdout, stderr)
	case "wait":
		return wait(args[1:], stdout, stderr)
	case "jobs", "resources":
		return list(args[0], args[1:], stdout, stderr)
	case "events", "logs":
		return observe(args[0], args[1:], stdout, stderr)
	case "cancel":
		return cancelJob(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unsupported command %q\n", args[0])
		return 2
	}
}

func connect(stderr io.Writer) (*client.Client, int) {
	root, err := state.Resolve(os.Getenv("HAEPARI_HOME"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 2
	}
	c, err := client.Connect(context.Background(), root)
	if err != nil {
		if errors.Is(err, client.ErrParentContext) {
			fmt.Fprintf(stderr, "invalid parent context: %v (check HAEPARI_PARENT_CONTEXT)\n", err)
			return nil, 2
		}
		var issue *client.Issue
		if errors.As(err, &issue) {
			fmt.Fprintf(stderr, "%s: %s\n", issue.Code, issue.Message)
			code, e := managed.ExitCode(managed.ErrorCode(issue.Code))
			if e == nil {
				return nil, code
			}
		}
		fmt.Fprintf(stderr, "cannot connect to Haepari controller: %v (check the daemon and HAEPARI_HOME)\n", err)
		return nil, 6
	}
	return c, 0
}
func emit(w io.Writer, stderr io.Writer, envelope client.Envelope) int {
	if err := json.NewEncoder(w).Encode(envelope); err != nil {
		fmt.Fprintln(stderr, err)
		return 6
	}
	return 0
}
func clientFailure(w, stderr io.Writer, envelope client.Envelope, err error) int {
	if envelope.Schema != "" {
		_ = emit(w, stderr, envelope)
		if envelope.Error != nil {
			switch envelope.Error.Code {
			case "WAIT_TIMEOUT":
				return 5
			case "STORAGE_UNAVAILABLE", "TRANSPORT_UNCERTAIN":
				return 6
			default:
				return 2
			}
		}
	}
	fmt.Fprintln(stderr, err)
	var issue *client.Issue
	if errors.As(err, &issue) {
		if code, mappingErr := managed.ExitCode(managed.ErrorCode(issue.Code)); mappingErr == nil {
			return code
		}
	}
	if errors.Is(err, managed.ErrInvalid) {
		return 2
	}
	return 6
}
func readSpec(name string) ([]byte, error) {
	if name == "" {
		return nil, fmt.Errorf("spec file required")
	}
	info, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("spec must be a regular file below 1 MiB")
	}
	return os.ReadFile(name)
}
func register(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("resource register", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("file", "", "resource file")
	jsonFlag := flags.Bool("json", false, "JSON response")
	if flags.Parse(args) != nil {
		return 2
	}
	if flags.NArg() != 0 || *path == "" || !*jsonFlag {
		fmt.Fprintln(stderr, "usage: haepari resource register --file <json> --json")
		return 2
	}
	spec, err := readSpec(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	c, code := connect(stderr)
	if code != 0 {
		return code
	}
	defer c.Close()
	envelope, err := c.Register(context.Background(), spec)
	if err != nil {
		return clientFailure(stdout, stderr, envelope, err)
	}
	return emit(stdout, stderr, envelope)
}
func submit(args []string, stdout, stderr io.Writer, blocking bool) int {
	flags := flag.NewFlagSet("submit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("spec", "", "job spec")
	key := flags.String("request-key", "", "persisted request key: 1-128 ASCII alphanumeric, dot, underscore or dash; starts alphanumeric")
	jsonFlag := flags.Bool("json", false, "JSON response")
	if flags.Parse(args) != nil {
		return 2
	}
	if flags.NArg() != 0 || *path == "" || *key == "" || (!blocking && !*jsonFlag) {
		if blocking {
			fmt.Fprintln(stderr, "usage: haepari run --spec <json> --request-key <key> [--json]")
		} else {
			fmt.Fprintln(stderr, "usage: haepari submit --spec <json> --request-key <key> --json")
		}
		return 2
	}
	spec, err := readSpec(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	c, code := connect(stderr)
	if code != 0 {
		return code
	}
	defer c.Close()
	receipt, envelope, err := c.Submit(context.Background(), spec, *key)
	if err != nil {
		return clientFailure(stdout, stderr, envelope, err)
	}
	if !blocking {
		return emit(stdout, stderr, envelope)
	}
	if *jsonFlag {
		if err := json.NewEncoder(stdout).Encode(map[string]any{"schema": "haepari.run-event/v1", "kind": "admission", "data": receipt}); err != nil {
			fmt.Fprintln(stderr, err)
			return 6
		}
	} else {
		fmt.Fprintln(stdout, receipt.JobID)
	}
	if receipt.ResultStatus == "unavailable" {
		fmt.Fprintln(stderr, "RESULT_UNAVAILABLE: known admission has no retained result; no new job was launched")
		return 2
	}
	result, final, err := c.Wait(context.Background(), receipt.JobID, 24*time.Hour)
	if err != nil {
		return clientFailure(stdout, stderr, final, err)
	}
	if *jsonFlag {
		if err := json.NewEncoder(stdout).Encode(map[string]any{"schema": "haepari.run-event/v1", "kind": "final", "data": result}); err != nil {
			fmt.Fprintln(stderr, err)
			return 6
		}
	} else {
		if code := emit(stdout, stderr, final); code != 0 {
			return code
		}
	}
	return verdictExit(result.FinalVerdict)
}
func inspect(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[1] != "--json" {
		fmt.Fprintln(stderr, "usage: haepari inspect <job-id> --json")
		return 2
	}
	c, code := connect(stderr)
	if code != 0 {
		return code
	}
	defer c.Close()
	_, envelope, err := c.Inspect(context.Background(), args[0])
	if err != nil {
		return clientFailure(stdout, stderr, envelope, err)
	}
	return emit(stdout, stderr, envelope)
}
func wait(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: haepari wait <job-id> [--timeout <duration>] --json")
		return 2
	}
	flags := flag.NewFlagSet("wait", flag.ContinueOnError)
	flags.SetOutput(stderr)
	timeout := flags.Duration("timeout", 5*time.Minute, "caller wait timeout")
	jsonFlag := flags.Bool("json", false, "JSON response")
	if flags.Parse(args[1:]) != nil {
		return 2
	}
	if flags.NArg() != 0 || !*jsonFlag || *timeout <= 0 || *timeout > 24*time.Hour {
		fmt.Fprintln(stderr, "usage: haepari wait <job-id> [--timeout <duration up to 24h>] --json")
		return 2
	}
	c, code := connect(stderr)
	if code != 0 {
		return code
	}
	defer c.Close()
	result, envelope, err := c.Wait(context.Background(), args[0], *timeout)
	if err != nil {
		return clientFailure(stdout, stderr, envelope, err)
	}
	if code := emit(stdout, stderr, envelope); code != 0 {
		return code
	}
	return verdictExit(result.FinalVerdict)
}
func verdictExit(verdict managed.Verdict) int {
	switch verdict {
	case managed.Pass:
		return 0
	case managed.Fail:
		return 1
	case managed.Incomplete:
		return 3
	case managed.Cancelled:
		return 4
	default:
		return 6
	}
}

func writeJSON(w, stderr io.Writer, operation string, data any) int {
	if err := json.NewEncoder(w).Encode(envelope{
		Schema: "haepari.envelope/v1", Operation: operation, OK: true, Data: data,
	}); err != nil {
		fmt.Fprintln(stderr, err)
		return 6
	}
	return 0
}

func availability(action string, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "resource ID required")
		return 2
	}
	id := args[0]
	flags := flag.NewFlagSet("resource "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	expected := flags.Int64("expected-revision", 0, "expected availability revision")
	config := flags.Int64("expected-config-revision", 0, "pin configuration for exact replay")
	lease := flags.Int64("expected-lease-generation", -1, "pin lease epoch for exact replay")
	key := flags.String("request-key", "", "mutation key for exact replay")
	reason := flags.String("reason", "operator_pause", "pause reason, 1 to 256 bytes (pause only)")
	jsonFlag := flags.Bool("json", false, "JSON output")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *expected <= 0 || !*jsonFlag {
		fmt.Fprintln(stderr, "resource pause/resume requires --expected-revision <availability revision> --json")
		return 2
	}
	configSupplied, leaseSupplied, reasonSupplied := false, false, false
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "expected-config-revision":
			configSupplied = true
		case "expected-lease-generation":
			leaseSupplied = true
		case "reason":
			reasonSupplied = true
		}
	})
	if configSupplied && *config < 1 {
		fmt.Fprintln(stderr, "--expected-config-revision must be at least 1")
		return 2
	}
	if leaseSupplied && *lease < 0 {
		fmt.Fprintln(stderr, "--expected-lease-generation must be at least 0")
		return 2
	}
	if configSupplied != leaseSupplied {
		fmt.Fprintln(stderr, "--expected-config-revision and --expected-lease-generation must be supplied together")
		return 2
	}
	if action == "resume" && reasonSupplied {
		fmt.Fprintln(stderr, "--reason applies only to resource pause")
		return 2
	}
	if action == "pause" && (len(*reason) == 0 || len(*reason) > 256) {
		fmt.Fprintln(stderr, "--reason must contain 1 to 256 bytes for resource pause")
		return 2
	}
	if *key == "" {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			fmt.Fprintln(stderr, err)
			return 6
		}
		*key = "availability-" + hex.EncodeToString(nonce[:])
	}
	if !managed.ValidRequestKey(*key) {
		fmt.Fprintln(stderr, "invalid request key")
		return 2
	}
	c, code := connect(stderr)
	if code != 0 {
		return code
	}
	defer c.Close()
	current, envelope, err := c.InspectResource(context.Background(), id)
	if err != nil {
		return clientFailure(stdout, stderr, envelope, err)
	}
	if !configSupplied {
		*config, *lease = current.ConfigRevision, current.LeaseGeneration
	}
	q := managed.AvailabilityRequest{MutationKey: *key, ResourceID: id, ExpectedConfigRevision: *config, ExpectedAvailabilityRevision: *expected, ExpectedLeaseGeneration: *lease}
	if action == "pause" {
		q.Reason = *reason
	}
	_, envelope, err = c.SetAvailability(context.Background(), q, action == "resume")
	if err != nil {
		return clientFailure(stdout, stderr, envelope, err)
	}
	return emit(stdout, stderr, envelope)
}

func resourceInspect(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[1] != "--json" {
		fmt.Fprintln(stderr, "usage: haepari resource inspect <id> --json")
		return 2
	}
	c, code := connect(stderr)
	if code != 0 {
		return code
	}
	defer c.Close()
	_, envelope, err := c.InspectResource(context.Background(), args[0])
	if err != nil {
		return clientFailure(stdout, stderr, envelope, err)
	}
	return emit(stdout, stderr, envelope)
}
