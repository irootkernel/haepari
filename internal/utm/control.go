// Package utm owns the host-side UTM command and address observation boundary.
// Job leases and durable permission to issue power effects belong to the ledger.
package utm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("UTM prerequisite unavailable")

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var frameworkLimitPattern = regexp.MustCompile(`(?:\(VZErrorDomain error 6\.\)|Error Domain=VZErrorDomain Code=6(?: |$))`)

// UTM 4.7.5 reports errAEEventNotPermitted for denied Automation, including
// an unavailable login context. Preserve that host-wide cause separately from
// a VM lookup/status failure, without treating a power error as no effect.
var hostPermissionPattern = regexp.MustCompile(`(?:\(OSStatus error -1743\.\)|Error Domain=NSOSStatusErrorDomain Code=-1743(?: |$))`)

const commandLimit = 1 << 20

type CommandResult struct {
	StdoutBytes int64
	StderrBytes int64
	Stdout      string
	Stderr      string
	ExitCode    int
}

type CommandRunner func(context.Context, string, ...string) (CommandResult, error)

type Control struct {
	Executable string
	Run        CommandRunner
}

type VM struct {
	UUID        string
	State       string
	commandUUID string // Exact spelling from UTM; UUID remains canonical for identity comparisons.
}

// CommandFailure does not assert whether a power command took effect. Its caller
// must reconcile the durable intent using a new observation, never retry it.
type CommandFailure struct {
	Operation            string
	ExitCode             int
	FrameworkLimit       bool
	HostPermissionDenied bool
}

func (e *CommandFailure) Error() string {
	return fmt.Sprintf("%s: UTM command failed (exit %d)", e.Operation, e.ExitCode)
}
func (e *CommandFailure) Unwrap() error { return ErrUnavailable }

func (c Control) call(ctx context.Context, args ...string) (CommandResult, error) {
	if !filepath.IsAbs(c.Executable) || filepath.Clean(c.Executable) != c.Executable {
		return CommandResult{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	run := c.Run
	if run == nil {
		run = runCommand
	}
	r, err := run(ctx, c.Executable, args...)
	if err != nil || r.ExitCode != 0 || strings.Contains(r.Stderr, "Error from event") ||
		len(r.Stdout) > commandLimit || len(r.Stderr) > commandLimit {
		var exitFailure *exec.ExitError
		returned := err == nil || errors.As(err, &exitFailure) && exitFailure.ExitCode() >= 0
		refused := returned && args[0] == "start" && ctx.Err() == nil && r.ExitCode >= 0 && len(r.Stderr) <= commandLimit &&
			frameworkLimitPattern.MatchString(r.Stderr)
		permissionDenied := returned && ctx.Err() == nil && len(r.Stderr) <= commandLimit &&
			strings.Contains(r.Stderr, "Error from event") && hostPermissionPattern.MatchString(r.Stderr)
		return r, &CommandFailure{Operation: args[0], ExitCode: r.ExitCode, FrameworkLimit: refused, HostPermissionDenied: permissionDenied}
	}
	return r, nil
}

func (c Control) List(ctx context.Context) ([]VM, error) {
	r, err := c.call(ctx, "list")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	if len(lines) == 0 || strings.Join(strings.Fields(lines[0]), " ") != "UUID Status Name" {
		return nil, ErrUnavailable
	}
	vms := make([]VM, 0, len(lines)-1)
	seen := make(map[string]bool)
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || !uuidPattern.MatchString(fields[0]) {
			return nil, ErrUnavailable
		}
		id := strings.ToLower(fields[0])
		if seen[id] {
			return nil, ErrUnavailable
		}
		seen[id] = true
		vms = append(vms, VM{UUID: id, State: fields[1], commandUUID: fields[0]})
	}
	return vms, nil
}

// Inspect proves UUID membership without booting or editing the VM. Names are
// intentionally discarded; they cannot authorize resource selection.
func (c Control) Inspect(ctx context.Context, uuid string) (VM, error) {
	if !uuidPattern.MatchString(uuid) {
		return VM{}, ErrUnavailable
	}
	vms, err := c.List(ctx)
	if err != nil {
		return VM{}, err
	}
	for _, vm := range vms {
		if strings.EqualFold(vm.UUID, uuid) {
			return vm, nil
		}
	}
	return VM{}, ErrUnavailable
}

func (c Control) Status(ctx context.Context, uuid string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	vm, err := c.Inspect(ctx, uuid)
	if err != nil {
		return "", err
	}
	r, err := c.call(ctx, "status", vm.commandUUID)
	if err != nil {
		return "", err
	}
	state := strings.TrimSpace(r.Stdout)
	if state != "started" && state != "stopped" {
		return "", ErrUnavailable
	}
	return state, nil
}

// Power resolves current membership and issues at most one power command. The
// controller must claim its exact durable power intent first. Cancellation or
// command success is not effect evidence.
func (c Control) Power(ctx context.Context, uuid, operation string) error {
	if !uuidPattern.MatchString(uuid) || operation != "start" && operation != "stop" {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	vm, err := c.Inspect(ctx, uuid)
	if err != nil {
		return err
	}
	_, err = c.call(ctx, operation, vm.commandUUID)
	return err
}

type boundedOutput struct {
	bytes    []byte
	overflow bool
	total    int64
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	b.total += int64(n)
	remaining := commandLimit - len(b.bytes)
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.bytes = append(b.bytes, p...)
	return n, nil // Drain excess output without keeping it in memory.
}

func runCommand(ctx context.Context, executable string, args ...string) (CommandResult, error) {
	info, err := os.Lstat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return CommandResult{}, ErrUnavailable
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	cmd.WaitDelay = time.Second
	var stdout, stderr boundedOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	r := CommandResult{Stdout: string(stdout.bytes), Stderr: string(stderr.bytes), ExitCode: -1}
	if cmd.ProcessState != nil {
		r.ExitCode = cmd.ProcessState.ExitCode()
	}
	if stdout.overflow || stderr.overflow {
		return r, ErrUnavailable
	}
	return r, err
}
