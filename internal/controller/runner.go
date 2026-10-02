package controller

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/runner"
	"golang.org/x/sys/unix"
)

const supervisorObservationSlack = time.Second

func (s *runtimeServer) runWithSupervisor(c *localRunControl, job managed.JobSpec, jobRoot, workspace, runID string) (bool, *os.File, error) {
	deadline := time.Now().Add(time.Duration(job.Deadlines.RunSeconds) * time.Second)
	if err := s.beginPhase(c, "run", job.Deadlines.RunSeconds, deadline); err != nil {
		return false, nil, err
	}
	terminationAllowance := time.Duration(job.Deadlines.CleanupSeconds) * time.Second
	return observeWithinRunBudgetWithWorker(deadline, terminationAllowance, s.startWorker, s.detached, func() (bool, *os.File, error) {
		return s.observeSupervisor(c, job, jobRoot, workspace, runID, deadline)
	})
}

func observeWithinRunBudgetWithWorker(deadline time.Time, terminationAllowance time.Duration, start func(func()) bool, detached <-chan struct{}, observer func() (bool, *os.File, error)) (bool, *os.File, error) {
	type completion struct {
		started bool
		log     *os.File
		err     error
	}
	done := make(chan completion, 1)
	if !start(func() {
		started, log, err := observer()
		done <- completion{started, log, err}
	}) {
		return false, nil, errControllerDetached
	}
	// The run deadline starts before log/request publication. Termination has
	// its own bounded observation allowance; a blocked setup or observer is
	// uncertainty, never a stopped workload or permission to release the lease.
	timer := time.NewTimer(time.Until(deadline) + terminationAllowance + supervisorObservationSlack)
	defer timer.Stop()
	drain := func() {
		go func() {
			result := <-done
			if result.log != nil {
				_ = result.log.Close()
			}
		}()
	}
	select {
	case result := <-done:
		return result.started, result.log, result.err
	case <-timer.C:
		drain()
		return false, nil, runner.ErrUnproven
	case <-detached:
		drain()
		return false, nil, errControllerDetached
	}
}

func (s *runtimeServer) observeSupervisor(c *localRunControl, job managed.JobSpec, jobRoot, workspace, runID string, deadline time.Time) (bool, *os.File, error) {
	c.mu.Lock()
	receipt := c.receipt
	c.mu.Unlock()
	log, err := s.createRunnerFile(receipt.JobID, "output.log")
	if err != nil {
		return false, nil, err
	}
	if err := s.bindLocalLog(receipt, runID, log); err != nil {
		return false, log, err
	}
	boot, err := platform.BootIdentity()
	if err != nil {
		return false, log, err
	}
	chain, err := s.ledger.RunnerParentChain(s.installationID(), c.identity.JobID, c.identity.LeaseGeneration)
	if err != nil {
		return false, log, err
	}
	chainJSON, err := json.Marshal(chain)
	if err != nil {
		return false, log, err
	}
	workPath := managed.DefaultWorkloadPATH
	if declared, ok := job.Workload.Environment["PATH"]; ok {
		workPath = declared
	}
	environment := []string{"PATH=" + workPath}
	keys := make([]string, 0, len(job.Workload.Environment))
	for key := range job.Workload.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key != "PATH" && key != managed.ParentContextEnvironment {
			environment = append(environment, key+"="+job.Workload.Environment[key])
		}
	}
	environment = append(environment, managed.ParentContextEnvironment+"="+string(chainJSON))
	var st unix.Stat_t
	if err := unix.Fstat(int(log.Fd()), &st); err != nil {
		return false, log, err
	}
	q := runner.Request{Schema: managed.RunnerSchema("request"), Identity: c.identity, BootID: boot, WorkDir: filepath.Join(workspace, filepath.FromSlash(job.Workload.CWD)), Argv: job.Workload.Argv, Environment: environment, Deadline: deadline.UTC(), RunSeconds: job.Deadlines.RunSeconds, TerminationSeconds: job.Deadlines.CleanupSeconds, LogDevice: uint64(st.Dev), LogInode: uint64(st.Ino)}
	if err := q.Validate(jobRoot); err != nil {
		return false, log, err
	}
	requestJSON, err := json.Marshal(q)
	if err != nil {
		return false, log, err
	}
	// Bind the exact request in SQLite before publishing it or launching the
	// supervisor. A crash after this point is a claimed run, never a new start.
	if err := s.ledger.RecordRunnerReceipt(c.identity, "request", requestJSON); err != nil {
		return false, log, err
	}
	if err := runner.Prepare(jobRoot, q); err != nil {
		return false, log, err
	}
	executable, err := os.Executable()
	if err != nil {
		return false, log, err
	}
	diagnostics, err := s.createRunnerFile(receipt.JobID, "runner.log")
	if err != nil {
		return false, log, err
	}
	defer diagnostics.Close()
	cmd := exec.Command(executable, "runner", "start", "--request", filepath.Join(jobRoot, "request.json"))
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = diagnostics
	cmd.Stderr = diagnostics
	c.mu.Lock()
	c.runnerRequest = filepath.Join(jobRoot, "request.json")
	c.deadline = deadline
	select {
	case <-s.detached:
		c.mu.Unlock()
		return false, log, errControllerDetached
	default:
	}
	if !time.Now().Before(deadline) {
		c.mu.Unlock()
		return false, log, runner.ErrUnproven
	}
	if err := cmd.Start(); err != nil {
		c.mu.Unlock()
		return false, log, err
	}
	c.runnerExited = make(chan struct{})
	go func() { _ = cmd.Wait(); close(c.runnerExited) }()
	c.mu.Unlock()
	c.phase("running")
	return s.watchSupervisor(c, job, log, deadline)
}

func (s *runtimeServer) watchSupervisor(c *localRunControl, job managed.JobSpec, log *os.File, deadline time.Time) (bool, *os.File, error) {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		select {
		case <-s.detached:
			return false, log, errControllerDetached
		default:
		}
		changed := s.ledger.Changes()
		c.mu.Lock()
		view, err := s.importRunner(c)
		if err == nil {
			err = s.observeLocalCompletion(c)
		}
		if err != nil {
			c.mu.Unlock()
			return false, log, err
		}
		if view.Exit != nil {
			c.mu.Unlock()
			if !view.Exit.TerminationProven {
				return view.Exit.Outcome.Started, log, runner.ErrUnproven
			}
			return view.Exit.Outcome.Started, log, nil
		}
		status, err := s.ledger.Cancellation(c.identity.JobID)
		if err != nil {
			c.mu.Unlock()
			return false, log, err
		}
		if status.Requested || !time.Now().Before(deadline) {
			if !status.Requested && c.receipt.JobState != "cancelling" {
				c.receipt, err = s.ledger.BeginLocalDeadline(c.identity, c.receipt.JobRevision)
				if err != nil {
					c.mu.Unlock()
					return false, log, err
				}
			}
			c.mu.Unlock()
			if err := terminateLocal(c, time.Duration(job.Deadlines.CleanupSeconds)*time.Second); err != nil {
				return false, log, err
			}
			c.mu.Lock()
			view, err = s.importRunner(c)
			if err == nil {
				err = s.observeLocalCompletion(c)
			}
			c.mu.Unlock()
			if err != nil {
				return false, log, err
			}
			if view.Exit == nil || !view.Exit.TerminationProven {
				return false, log, runner.ErrUnproven
			}
			return view.Exit.Outcome.Started, log, nil
		}
		c.mu.Unlock()
		select {
		case <-s.detached:
			return false, log, errControllerDetached
		case <-c.runnerExited:
			// The supervisor's native exit is never a workload/cleanup receipt.
			view, err := runner.Inspect(c.runnerRequest)
			if err != nil || view.Exit == nil {
				return false, log, errors.Join(runner.ErrUnproven, err)
			}
		case <-tick.C:
		case <-changed:
		case <-c.wake:
		case <-timer.C:
		}
	}
}

func (s *runtimeServer) createRunnerFile(jobID, name string) (*os.File, error) {
	jobFD, err := openPrivateJob(s.root.Path, jobID)
	if err != nil {
		return nil, err
	}
	defer unix.Close(jobFD)
	fd, err := unix.Openat(jobFD, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	if _, err := privateLogStat(fd, false); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// Called with c.mu held. The runner library verifies closed receipt shapes
// and exact request bindings before any receipt is imported into the ledger.
func (s *runtimeServer) importRunner(c *localRunControl) (runner.Observation, error) {
	view, err := runner.Inspect(c.runnerRequest)
	if err != nil {
		return view, err
	}
	for _, item := range []struct {
		kind    string
		value   any
		present bool
	}{
		{"intent", view.Intent, view.Intent != nil}, {"start", view.Start, view.Start != nil}, {"outcome", view.Outcome, view.Outcome != nil}, {"exit", view.Exit, view.Exit != nil},
	} {
		if !item.present {
			continue
		}
		data, err := json.Marshal(item.value)
		if err != nil {
			return view, err
		}
		if err := s.ledger.RecordRunnerReceipt(c.identity, item.kind, data); err != nil {
			return view, err
		}
	}
	if view.Outcome != nil {
		out := view.Outcome.Outcome
		c.publishCompletion(nativeCompletion{at: view.Outcome.ObservedAt, outcome: &out})
	}
	return view, nil
}
