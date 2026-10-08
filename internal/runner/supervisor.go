package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"golang.org/x/sys/unix"
)

type nativeEnd struct {
	err error
	at  time.Time
}

var spawnBoundary func(string)                      // nil in production; native crash tests only
var processCensus = platform.ProcessCensus          // native tests can inject observation failure
var observeStartedProcess = platform.ObserveProcess // native tests can inject initial observation failure
func boundary(name string) {
	if spawnBoundary != nil {
		spawnBoundary(name)
	}
}

// Start holds the exclusive run lock until the final receipt. A lost supervisor
// leaves its intent behind; no subsequent start may repeat its effects.
func StartRun(path string) (Observation, error) {
	d, q, err := readRequest(path)
	if err != nil {
		return Observation{}, err
	}
	defer d.close()
	boot, err := platform.BootIdentity()
	if err != nil {
		return Observation{}, err
	}
	if boot != q.BootID {
		return Observation{}, ErrConflict
	}
	lock, err := d.open("run.lock", unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return Observation{}, err
	}
	defer lock.Close()
	if err := lock.Sync(); err != nil {
		return Observation{}, err
	}
	if err := d.file.Sync(); err != nil {
		return Observation{}, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return inspect(d, q)
		}
		return Observation{}, err
	}
	view, err := inspect(d, q)
	if err != nil {
		return view, err
	}
	if view.Exit != nil {
		return view, nil
	}
	if view.Intent != nil {
		return view, ErrAmbiguous
	}
	supervisor, err := platform.ObserveProcess(os.Getpid())
	if err != nil {
		return view, err
	}
	if supervisor.Identity.BootID != q.BootID || supervisor.Identity.PGID != os.Getpid() || supervisor.Identity.SID != os.Getpid() {
		return view, ErrInvalid
	}
	intent := Intent{Schema: managed.RunnerSchema("intent"), Binding: q.Binding(), Supervisor: supervisor.Identity, RecordedAt: time.Now().UTC()}
	if err := d.write("intent.json", intent); err != nil {
		return view, err
	}
	boundary("before_spawn")
	err = supervise(d, q, path)
	view, readErr := inspect(d, q)
	return view, errors.Join(err, readErr)
}

func supervise(d *directory, q Request, path string) (result error) {
	var uncertainty error
	var diagnosticErr error
	defer func() { result = errors.Join(result, diagnosticErr) }()
	recordUncertainty := func(operation string, err error) {
		if err != nil && uncertainty == nil {
			// Preserve the triggering observation rather than replacing it
			// with a later stop failure. Never turn later absence into proof.
			uncertainty = err
			diagnosticErr = recordNativeDiagnostic(d, q, operation, err)
		}
	}
	log, err := d.open("output.log", unix.O_WRONLY|unix.O_APPEND)
	if err != nil {
		return err
	}
	defer log.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(log.Fd()), &st); err != nil {
		return err
	}
	if uint64(st.Dev) != q.LogDevice || uint64(st.Ino) != q.LogInode {
		return ErrConflict
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	gate, release, err := os.Pipe()
	if err != nil {
		return err
	}
	defer release.Close()
	status, statusWriter, err := os.Pipe()
	if err != nil {
		gate.Close()
		return err
	}
	defer status.Close()
	defer statusWriter.Close()
	logRead, logWrite, err := os.Pipe()
	if err != nil {
		gate.Close()
		return err
	}
	defer logRead.Close()
	defer logWrite.Close()
	// This trampoline cannot execute project argv until its identity and start
	// receipt are durable. EOF after supervisor death exits without a workload.
	cmd := exec.Command(executable, "runner", "_exec", "--request", path)
	cmd.Env = q.Environment
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = logWrite
	cmd.Stderr = logWrite
	cmd.ExtraFiles = []*os.File{gate, statusWriter}
	if err := cmd.Start(); err != nil {
		gate.Close()
		out := managed.LocalOutcome{Started: false, WorkloadOutcome: "inconclusive"}
		return finish(d, q, out, true, "workload_trampoline_start_failed", time.Now())
	}
	gate.Close()
	statusWriter.Close()
	logWrite.Close()
	collector := newBoundedLog(log, q.LogLimitBytes)
	collected := make(chan error, 1)
	go func() {
		_, err := io.CopyBuffer(collector, logRead, make([]byte, 64<<10))
		logRead.Close()
		collected <- err
	}()
	var logComplete bool
	var logFailure error
	boundary("after_spawn")
	ended := make(chan nativeEnd, 1)
	go func() { err := cmd.Wait(); ended <- nativeEnd{err, time.Now()} }()
	process, err := observeStartedProcess(cmd.Process.Pid)
	if err != nil {
		recordUncertainty("start_observation", err)
		return err
	}
	if process.Identity.PGID != cmd.Process.Pid || process.Identity.SID != cmd.Process.Pid || process.Identity.BootID != q.BootID {
		err := &platform.ProcessError{Operation: "process_incarnation", PID: cmd.Process.Pid, Err: ErrInvalid}
		recordUncertainty("start_observation", err)
		return err
	}
	if err := d.write("start.json", Start{Schema: managed.RunnerSchema("start"), Binding: q.Binding(), Process: process.Identity, StartedAt: time.Now().UTC()}); err != nil {
		return err
	}
	boundary("after_start")
	cancelledBeforeExec, err := cancellation(d, q)
	if err != nil {
		recordUncertainty("cancellation_observation", err)
		return err
	}
	if !cancelledBeforeExec && time.Now().Before(q.Deadline) {
		if _, err := release.Write([]byte{'G'}); err != nil {
			return err
		}
	}
	release.Close()
	deadline := time.Now().Add(min(time.Until(q.Deadline), time.Duration(q.RunSeconds)*time.Second))
	tracker := newTracker(process.Identity)
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	var end *nativeEnd
	var outcome *managed.LocalOutcome
	var stopping time.Time
	cancelledBeforeOutcome := false
	killed := false
	for {
		if !logComplete {
			select {
			case logFailure = <-collected:
				logComplete = true
			default:
			}
		}
		// Import native completion before classifying cancellation. Exit is not
		// termination proof; surviving group members must still be stopped.
		if end == nil {
			select {
			case observed := <-ended:
				end = &observed
			default:
			}
		}
		if end != nil && outcome == nil {
			out := nativeOutcome(cmd, *end, deadline)
			// FD 4 is close-on-exec: a failure byte comes only from the
			// trampoline, never from a workload that happens to exit 126.
			ack, err := io.ReadAll(io.LimitReader(status, 2))
			if err != nil || len(ack) > 1 {
				return ErrUnproven
			}
			if len(ack) == 1 {
				if ack[0] != 'F' {
					return ErrUnproven
				}
				out = managed.LocalOutcome{Started: false, WorkloadOutcome: "inconclusive"}
				if cancelledBeforeExec {
					out.WorkloadOutcome = "cancelled"
				}
				// A refused gate or failed exec is a no-start result, even
				// after the deadline. Only an entered workload can time out.
			}
			if out.Started && cancelledBeforeOutcome && !stopping.IsZero() && end.at.After(stopping) && out.WorkloadOutcome != "timed_out" {
				out.WorkloadOutcome = "cancelled"
			}
			outcome = &out
			if err := d.write("outcome.json", Outcome{Schema: managed.RunnerSchema("outcome"), Binding: q.Binding(), Outcome: out, ObservedAt: end.at.UTC()}); err != nil {
				return err
			}
			boundary("after_outcome")
		}
		alive, scanErr := tracker.scan()
		recordUncertainty("process_census", scanErr)
		cancelled, cancelErr := cancellation(d, q)
		recordUncertainty("cancellation_observation", cancelErr)
		// Uncertainty can require a stop without a cancellation request.
		// Preserve the native outcome unless durable cancellation preceded it.
		if cancelled && outcome == nil {
			cancelledBeforeOutcome = true
		}
		if stopping.IsZero() && (end != nil || cancelled || !time.Now().Before(deadline) || uncertainty != nil) {
			stopping = time.Now()
			recordUncertainty("signal_term", tracker.signal(unix.SIGTERM))
		}
		if end != nil && len(alive) == 0 && scanErr == nil {
			// Probe only; never signal an old PGID after reaping the leader.
			groupErr := unix.Kill(-process.Identity.PGID, 0)
			if errors.Is(groupErr, unix.ESRCH) && logComplete {
				if logFailure != nil && collector.gap == "" {
					collector.gap = "log_read_failed"
				}
				if err := log.Sync(); err != nil {
					return err
				}
				if uncertainty != nil {
					return finish(d, q, *outcome, false, "native_observation_uncertain", end.at, collector.manifest())
				}
				return finish(d, q, *outcome, true, "", end.at, collector.manifest())
			}
			if groupErr != nil && !errors.Is(groupErr, unix.EPERM) && !errors.Is(groupErr, unix.ESRCH) {
				recordUncertainty("group_probe", groupErr)
			}
		}
		if !stopping.IsZero() {
			budget := time.Duration(q.TerminationSeconds) * time.Second
			if !killed && time.Since(stopping) >= min(2*time.Second, budget/2) {
				killed = true
				recordUncertainty("signal_kill", tracker.signal(unix.SIGKILL))
			}
			if time.Since(stopping) >= budget {
				if !logComplete {
					return ErrUnproven
				}
				if outcome == nil {
					return ErrUnproven
				}
				return finish(d, q, *outcome, false, "termination_budget_expired", end.at, collector.manifest())
			}
		}
		<-tick.C
	}
}

func cancellation(d *directory, q Request) (bool, error) {
	var cancel Cancellation
	if err := d.read("cancel.json", &cancel); errors.Is(err, unix.ENOENT) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if cancel.Schema != managed.RunnerSchema("cancel") || cancel.Binding != q.Binding() || cancel.RequestedAt.IsZero() {
		return false, ErrConflict
	}
	return true, nil
}

func finish(d *directory, q Request, out managed.LocalOutcome, proven bool, reason string, at time.Time, logs ...LogManifest) error {
	var prior Outcome
	if err := d.read("outcome.json", &prior); errors.Is(err, unix.ENOENT) {
		if err := d.write("outcome.json", Outcome{Schema: managed.RunnerSchema("outcome"), Binding: q.Binding(), Outcome: out, ObservedAt: at.UTC()}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	exit := Exit{Schema: managed.RunnerSchema("exit"), Binding: q.Binding(), Outcome: out, TerminationProven: proven, Reason: reason, FinishedAt: time.Now().UTC()}
	if len(logs) > 0 {
		exit.Log = &logs[0]
	}
	return d.write("exit.json", exit)
}

func nativeOutcome(cmd *exec.Cmd, end nativeEnd, deadline time.Time) managed.LocalOutcome {
	out := managed.LocalOutcome{Started: true, WorkloadOutcome: "fail"}
	if end.at.After(deadline) {
		out.WorkloadOutcome = "timed_out"
	} else if end.err == nil {
		out.WorkloadOutcome = "pass"
	}
	if cmd.ProcessState != nil {
		code := cmd.ProcessState.ExitCode()
		if code >= 0 && code <= 255 {
			out.NativeExitCode = &code
		}
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			out.NativeExitCode = nil
			out.NativeSignal = status.Signal().String()
		}
	}
	return out
}

type processTracker struct {
	leader platform.ProcessIdentity
	known  map[int]platform.ProcessIdentity
}

func newTracker(id platform.ProcessIdentity) *processTracker {
	return &processTracker{id, map[int]platform.ProcessIdentity{id.PID: id}}
}
func sameIncarnation(a, b platform.ProcessIdentity) bool {
	return a.BootID == b.BootID && a.PID == b.PID && a.StartedSeconds == b.StartedSeconds && a.StartedMicros == b.StartedMicros && a.UID == b.UID
}

func (t *processTracker) scan() ([]platform.ProcessIdentity, error) {
	processes, err := processCensus()
	if err != nil {
		return nil, err
	}
	// Iterate to close ancestry chains independent of process-table order.
	for changed := true; changed; {
		changed = false
		for _, p := range processes {
			id := p.Identity
			prior, known := t.known[id.PID]
			if known && !sameIncarnation(prior, id) {
				return nil, ErrUnproven
			}
			if known {
				t.known[id.PID] = id
				continue
			}
			parent, parentKnown := t.known[p.ParentPID]
			if id.SID == t.leader.SID || (parentKnown && (id.StartedSeconds > parent.StartedSeconds || (id.StartedSeconds == parent.StartedSeconds && id.StartedMicros >= parent.StartedMicros))) {
				t.known[id.PID] = id
				changed = true
			}
		}
	}
	alive := []platform.ProcessIdentity{}
	for _, p := range processes {
		if prior, ok := t.known[p.Identity.PID]; ok && sameIncarnation(prior, p.Identity) {
			alive = append(alive, p.Identity)
		}
	}
	return alive, nil
}
func (t *processTracker) signal(signal unix.Signal) error {
	alive, err := t.scan()
	if err != nil {
		return err
	}
	for _, id := range alive {
		if err := platform.SignalProcess(id, signal); err != nil && !errors.Is(err, unix.ESRCH) && !errors.Is(err, unix.ENOENT) {
			return err
		}
	}
	return nil
}

// execWorkload runs only behind the supervisor's inherited gate. The explicit
// environment is never merged with the controller or user's ambient values.
func execWorkload(path string) (result error) {
	var statusStat unix.Stat_t
	if err := unix.Fstat(4, &statusStat); err != nil || uint32(statusStat.Mode)&unix.S_IFMT != unix.S_IFIFO {
		return ErrInvalid
	}
	unix.CloseOnExec(4)
	defer func() {
		if result != nil {
			_, _ = unix.Write(4, []byte{'F'})
		}
		_ = unix.Close(4)
	}()
	d, q, err := readRequest(path)
	if err != nil {
		return err
	}
	defer d.close()
	gate := os.NewFile(3, "runner-exec-gate")
	if gate == nil {
		return ErrInvalid
	}
	defer gate.Close()
	var st unix.Stat_t
	if err := unix.Fstat(3, &st); err != nil || uint32(st.Mode)&unix.S_IFMT != unix.S_IFIFO {
		return ErrInvalid
	}
	var allow [1]byte
	if n, err := gate.Read(allow[:]); err != nil || n != 1 || allow[0] != 'G' {
		return ErrUnproven
	}
	view, err := inspect(d, q)
	if err != nil || view.Start == nil {
		return ErrInvalid
	}
	current, err := platform.ObserveProcess(os.Getpid())
	if err != nil || current.Identity != view.Start.Process {
		return ErrConflict
	}
	boot, err := platform.BootIdentity()
	if err != nil || boot != q.BootID {
		return ErrConflict
	}
	working, err := openDirectory(q.WorkDir)
	if err != nil {
		return err
	}
	defer working.close()
	if err := unix.Fchdir(int(working.file.Fd())); err != nil {
		return err
	}
	command, err := resolveCommand(q.Argv[0], q.WorkDir, q.Environment)
	if err != nil {
		return err
	}
	gate.Close()
	d.close()
	return unix.Exec(command, q.Argv, q.Environment)
}

func resolveCommand(name, workDir string, environment []string) (string, error) {
	if filepath.IsAbs(name) {
		return name, nil
	}
	if strings.ContainsRune(name, filepath.Separator) {
		return filepath.Join(workDir, name), nil
	}
	path := managed.DefaultWorkloadPATH
	for _, variable := range environment {
		if key, value, ok := strings.Cut(variable, "="); ok && key == "PATH" {
			path = value
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(workDir, dir)
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("workload executable %q unavailable in its PATH", name)
}
