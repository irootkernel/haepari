//go:build integration

package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

func nativeFixture(script string, options ...func(*Request)) (string, Request) {
	root := privateFixtureDirectory()
	q := requestFixture(root)
	boot, err := platform.BootIdentity()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	q.BootID = boot
	gomega.Expect(os.Mkdir(q.WorkDir, 0700)).To(gomega.Succeed())
	log, err := os.OpenFile(filepath.Join(root, "output.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var st unix.Stat_t
	gomega.Expect(unix.Fstat(int(log.Fd()), &st)).To(gomega.Succeed())
	gomega.Expect(log.Close()).To(gomega.Succeed())
	q.LogDevice = uint64(st.Dev)
	q.LogInode = uint64(st.Ino)
	q.Argv = []string{"/bin/sh", "-c", script}
	for _, option := range options {
		option(&q)
	}
	gomega.Expect(Prepare(root, q)).To(gomega.Succeed())
	return root, q
}
func launchFixture(root, point string) (*exec.Cmd, <-chan error) {
	executable, err := os.Executable()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	args := []string{"runner", "start", "--request", filepath.Join(root, "request.json")}
	if point != "" {
		args = []string{"runner-crash-fixture", point, filepath.Join(root, "request.json")}
		if strings.HasPrefix(point, "census-") {
			args = []string{"runner-census-fixture", strings.TrimPrefix(point, "census-"), filepath.Join(root, "request.json")}
		}
		if point == "cancel-before-exec" {
			args = []string{"runner-cancel-fixture", filepath.Join(root, "request.json")}
		}
	}
	cmd := exec.Command(executable, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "GORACE=atexit_sleep_ms=0", "UNDECLARED=ambient-marker"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	diagnostics, err := os.OpenFile(filepath.Join(root, "diagnostics.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	cmd.Stdout = diagnostics
	cmd.Stderr = diagnostics
	gomega.Expect(cmd.Start()).To(gomega.Succeed())
	diagnostics.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return cmd, done
}
func awaitFixture(root string, done <-chan error, expectCrash bool) Observation {
	select {
	case err := <-done:
		if !expectCrash {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
	case <-time.After(9 * time.Second):
		ginkgo.Fail("bounded runner fixture did not exit; scratch root retained: " + root)
	}
	view, err := Inspect(filepath.Join(root, "request.json"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return view
}

var _ = ginkgo.Describe("Independent native runner", ginkgo.Label("integration"), func() {
	ginkgo.It("Task012 bounds the log prefix and records dropped bytes without changing native success", func() {
		root, _ := nativeFixture("printf 0123456789abcdef; printf tail >&2", func(q *Request) { q.LogLimitBytes = 8 })
		defer os.RemoveAll(root)
		_, done := launchFixture(root, "")
		view := awaitFixture(root, done, false)
		gomega.Expect(view.Exit.TerminationProven).To(gomega.BeTrue())
		gomega.Expect(view.Exit.Outcome.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("pass")))
		gomega.Expect(view.Exit.Log).NotTo(gomega.BeNil())
		gomega.Expect(view.Exit.Log.Artifact.SizeBytes).To(gomega.Equal(int64(8)))
		gomega.Expect(view.Exit.Log.DroppedBytes).To(gomega.Equal(int64(12)))
		gomega.Expect(view.Exit.Log.Gap).To(gomega.Equal("log_limit"))
		data, err := os.ReadFile(filepath.Join(root, "output.log"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("01234567"))
	})
	ginkgo.It("proves cancellation before gate release without entering or replaying workload exec", func() {
		root, _ := nativeFixture("printf unwanted > effect.txt")
		defer os.RemoveAll(root)
		_, done := launchFixture(root, "cancel-before-exec")
		view := awaitFixture(root, done, false)
		gomega.Expect(view.Start).NotTo(gomega.BeNil())
		gomega.Expect(view.Exit).NotTo(gomega.BeNil())
		gomega.Expect(view.Exit.TerminationProven).To(gomega.BeTrue())
		gomega.Expect(view.Exit.Outcome).To(gomega.Equal(managed.LocalOutcome{WorkloadOutcome: "cancelled"}))
		gomega.Expect(view.Outcome.Outcome).To(gomega.Equal(view.Exit.Outcome))
		path := filepath.Join(root, "request.json")
		replay, err := StartRun(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay).To(gomega.Equal(view))
		_, err = os.Stat(filepath.Join(root, "workspace/effect.txt"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
	ginkgo.It("proves a no-start outcome when the deadline expires before gate release", func() {
		root, _ := nativeFixture("printf unwanted > effect.txt", func(q *Request) {
			q.Deadline = time.Now().Add(-time.Second).UTC()
		})
		defer os.RemoveAll(root)
		_, done := launchFixture(root, "")
		view := awaitFixture(root, done, false)
		gomega.Expect(view.Start).NotTo(gomega.BeNil())
		gomega.Expect(view.Exit).NotTo(gomega.BeNil())
		gomega.Expect(view.Exit.TerminationProven).To(gomega.BeTrue())
		gomega.Expect(view.Exit.Outcome).To(gomega.Equal(managed.LocalOutcome{WorkloadOutcome: "inconclusive"}))
		gomega.Expect(view.Outcome.Outcome).To(gomega.Equal(view.Exit.Outcome))
		_, err := os.Stat(filepath.Join(root, "workspace/effect.txt"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
	ginkgo.It("survives caller exit and returns completed receipts without rerunning effects", func() {
		root, _ := nativeFixture("while [ ! -f ../allow ]; do /bin/sleep 0.05; done; printf once >> count.txt")
		defer os.RemoveAll(root)
		path := filepath.Join(root, "request.json")
		executable, err := os.Executable()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		caller := exec.Command(executable, "runner-caller-fixture", path)
		caller.Env = []string{"PATH=/usr/bin:/bin", "GORACE=atexit_sleep_ms=0"}
		gomega.Expect(caller.Run()).To(gomega.Succeed())
		var view Observation
		gomega.Eventually(func() bool {
			view, err = Inspect(path)
			return err == nil && view.Start != nil && view.Exit == nil
		}, 3*time.Second, 25*time.Millisecond).Should(gomega.BeTrue())
		observed, err := platform.ObserveProcess(view.Intent.Supervisor.PID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(observed.Identity).To(gomega.Equal(view.Intent.Supervisor))
		gomega.Expect(observed.ParentPID).NotTo(gomega.Equal(caller.Process.Pid))
		gomega.Expect(os.WriteFile(filepath.Join(root, "allow"), nil, 0600)).To(gomega.Succeed())
		gomega.Eventually(func() bool {
			view, err = Inspect(path)
			return err == nil && view.Exit != nil
		}, 3*time.Second, 25*time.Millisecond).Should(gomega.BeTrue())
		gomega.Expect(view.Exit.TerminationProven).To(gomega.BeTrue())
		gomega.Expect(view.Exit.Outcome.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("pass")))
		replay, err := StartRun(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay).To(gomega.Equal(view))
		data, err := os.ReadFile(filepath.Join(root, "workspace/count.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("once"))
	})
	ginkgo.It("refuses a replaced output inode before workload exec", func() {
		root, _ := nativeFixture("printf unwanted > effect.txt")
		defer os.RemoveAll(root)
		logPath := filepath.Join(root, "output.log")
		original, err := os.Open(logPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer original.Close() // keep the old inode allocated
		gomega.Expect(os.Remove(logPath)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(logPath, nil, 0600)).To(gomega.Succeed())
		_, done := launchFixture(root, "")
		select {
		case err := <-done:
			var exit *exec.ExitError
			gomega.Expect(errors.As(err, &exit)).To(gomega.BeTrue())
			gomega.Expect(exit.ExitCode()).To(gomega.Equal(2))
		case <-time.After(3 * time.Second):
			ginkgo.Fail("output mismatch refusal did not complete")
		}
		view, err := Inspect(filepath.Join(root, "request.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(view.Intent).NotTo(gomega.BeNil())
		gomega.Expect(view.Start).To(gomega.BeNil())
		_, err = os.Stat(filepath.Join(root, "workspace/effect.txt"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
	ginkgo.It("returns active and completed starts without repeating effects or receipts", func() {
		root, _ := nativeFixture("printf once >> count.txt; /bin/sleep 1")
		defer os.RemoveAll(root)
		path := filepath.Join(root, "request.json")
		_, done := launchFixture(root, "")
		gomega.Eventually(func() string {
			data, _ := os.ReadFile(filepath.Join(root, "workspace/count.txt"))
			return string(data)
		}, 3*time.Second, 25*time.Millisecond).Should(gomega.Equal("once"))
		active, err := StartRun(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(active.Start).NotTo(gomega.BeNil())
		gomega.Expect(active.Exit).To(gomega.BeNil())
		var stdout, stderr bytes.Buffer
		gomega.Expect(Command([]string{"watch", "--request", path}, &stdout, &stderr)).To(gomega.BeZero())
		completed := awaitFixture(root, done, false)
		before := make(map[string][]byte)
		for _, name := range []string{"intent.json", "start.json", "outcome.json", "exit.json", "output.log", "workspace/count.txt"} {
			before[name], err = os.ReadFile(filepath.Join(root, name))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		replay, err := StartRun(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay).To(gomega.Equal(completed))
		for name, data := range before {
			after, err := os.ReadFile(filepath.Join(root, name))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(after).To(gomega.Equal(data))
		}
	})
	for _, mode := range []string{"once", "budget"} {
		ginkgo.It("retains an unproven exit after injected census "+mode+" failure without repeating effects", func() {
			root, _ := nativeFixture("printf once >> count.txt")
			defer os.RemoveAll(root)
			_, done := launchFixture(root, "census-"+mode)
			view := awaitFixture(root, done, false)
			gomega.Expect(view.Exit).NotTo(gomega.BeNil())
			gomega.Expect(view.Exit.TerminationProven).To(gomega.BeFalse())
			reason := "native_observation_uncertain"
			if mode == "budget" {
				reason = "termination_budget_expired"
			}
			gomega.Expect(view.Exit.Reason).To(gomega.Equal(reason))
			replay, err := StartRun(filepath.Join(root, "request.json"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(replay).To(gomega.Equal(view))
			data, err := os.ReadFile(filepath.Join(root, "workspace/count.txt"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(string(data)).To(gomega.Equal("once"))
		})
	}
	ginkgo.It("refuses the exec trampoline without inherited gate descriptors", func() {
		root, _ := nativeFixture("printf unwanted > effect.txt")
		defer os.RemoveAll(root)
		executable, err := os.Executable()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		cmd := exec.Command(executable, "runner", "_exec", "--request", filepath.Join(root, "request.json"))
		gomega.Expect(cmd.Run()).NotTo(gomega.Succeed())
		_, err = os.Stat(filepath.Join(root, "workspace/effect.txt"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
	ginkgo.It("returns class-specific error envelopes and a durable cancel through the CLI", func() {
		root, q := nativeFixture("exit 0")
		defer os.RemoveAll(root)
		path := filepath.Join(root, "request.json")
		for _, test := range []struct {
			args  []string
			code  managed.ErrorCode
			retry string
		}{
			{[]string{"inspect", "--request", "relative.json"}, managed.InvalidRequest, "none"},
			{[]string{"inspect", "--request", filepath.Join(root, "missing/request.json")}, managed.InvalidRequest, "none"},
		} {
			var stdout, stderr bytes.Buffer
			gomega.Expect(Command(test.args, &stdout, &stderr)).To(gomega.Equal(2))
			envelope, err := managed.DecodeEnvelope(stdout.Bytes())
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(envelope.Error.Code).To(gomega.Equal(test.code))
			gomega.Expect(envelope.Error.Retry).To(gomega.Equal(test.retry))
			gomega.Expect(envelope.Error.Message).To(gomega.ContainSubstring("existing absolute"))
		}
		var stdout, stderr bytes.Buffer
		gomega.Expect(Command([]string{"cancel", "--request", path}, &stdout, &stderr)).To(gomega.BeZero())
		data, err := os.ReadFile(filepath.Join(root, "cancel.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var receipt Cancellation
		gomega.Expect(json.Unmarshal(data, &receipt)).To(gomega.Succeed())
		gomega.Expect(receipt.Binding).To(gomega.Equal(q.Binding()))
	})
	ginkgo.It("executes metacharacters and quotes as one literal argv value", func() {
		root, q := nativeFixture("exit 0")
		literal := "spaces 'quotes' \"$HOME\" *; $(touch unwanted)"
		q.Argv = []string{"/usr/bin/printf", "%s", literal}
		gomega.Expect(os.Remove(filepath.Join(root, "request.json"))).To(gomega.Succeed())
		gomega.Expect(Prepare(root, q)).To(gomega.Succeed())
		_, done := launchFixture(root, "")
		view := awaitFixture(root, done, false)
		gomega.Expect(view.Exit.TerminationProven).To(gomega.BeTrue())
		data, err := os.ReadFile(filepath.Join(root, "output.log"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal(literal))
		_, err = os.Stat(filepath.Join(root, "workspace/unwanted"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		gomega.Expect(os.RemoveAll(root)).To(gomega.Succeed())
	})
	ginkgo.It("records fast start/exit, preserves literal argv and environment, and streams output-heavy inherited writers", func() {
		for _, script := range []string{"printf '%s' '$HOME *'; printf '%s' \"${UNDECLARED-secret-absent}\"", "(i=0; while [ $i -lt 12000 ]; do printf 'streamed-output\\n'; i=$((i+1)); done) & wait"} {
			root, _ := nativeFixture(script)
			_, done := launchFixture(root, "")
			view := awaitFixture(root, done, false)
			gomega.Expect(view.Intent).NotTo(gomega.BeNil())
			gomega.Expect(view.Start).NotTo(gomega.BeNil())
			gomega.Expect(view.Exit).NotTo(gomega.BeNil())
			gomega.Expect(view.Exit.TerminationProven).To(gomega.BeTrue())
			gomega.Expect(string(view.Exit.Outcome.WorkloadOutcome)).To(gomega.Equal("pass"))
			for _, operation := range []string{"inspect", "watch"} {
				var stdout, stderr bytes.Buffer
				gomega.Expect(Command([]string{operation, "--request", filepath.Join(root, "request.json"), "--json"}, &stdout, &stderr)).To(gomega.BeZero())
				envelope, err := managed.DecodeEnvelope(stdout.Bytes())
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(envelope.Operation).To(gomega.Equal("runner." + operation))
				gomega.Expect(envelope.OK).To(gomega.BeTrue())
			}
			bytes, err := os.ReadFile(filepath.Join(root, "output.log"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if strings.HasPrefix(script, "printf") {
				gomega.Expect(string(bytes)).To(gomega.Equal("$HOME *secret-absent"))
			} else {
				gomega.Expect(string(bytes)).To(gomega.Equal(strings.Repeat("streamed-output\n", 12000)))
			}
			gomega.Expect(os.RemoveAll(root)).To(gomega.Succeed())
		}
	})
	ginkgo.It("bounds TERM escalation and stops background descendants holding output open", func() {
		root, q := nativeFixture("trap '' TERM; /bin/sleep 20 & wait")
		_, done := launchFixture(root, "")
		path := filepath.Join(root, "request.json")
		gomega.Eventually(func() bool { view, err := Inspect(path); return err == nil && view.Start != nil }, 3*time.Second, 25*time.Millisecond).Should(gomega.BeTrue())
		before := time.Now()
		gomega.Expect(Cancel(path)).To(gomega.Succeed())
		view := awaitFixture(root, done, false)
		gomega.Expect(view.Exit.TerminationProven).To(gomega.BeTrue())
		gomega.Expect(time.Since(before)).To(gomega.BeNumerically("<", time.Duration(q.TerminationSeconds)*time.Second+time.Second))
		gomega.Expect(errors.Is(unix.Kill(-view.Start.Process.PGID, 0), unix.ESRCH)).To(gomega.BeTrue())
		gomega.Expect(os.RemoveAll(root)).To(gomega.Succeed())
	})
	ginkgo.It("distinguishes failed exec from an actual workload exit 126", func() {
		for _, missing := range []bool{true, false} {
			root, q := nativeFixture("exit 126")
			if missing {
				gomega.Expect(os.Remove(filepath.Join(root, "request.json"))).To(gomega.Succeed())
				q.Argv = []string{"/no-such-haepari-executable"}
				gomega.Expect(Prepare(root, q)).To(gomega.Succeed())
			}
			_, done := launchFixture(root, "")
			view := awaitFixture(root, done, false)
			gomega.Expect(view.Exit.TerminationProven).To(gomega.BeTrue())
			gomega.Expect(view.Exit.Outcome.Started).To(gomega.Equal(!missing))
			if missing {
				gomega.Expect(string(view.Exit.Outcome.WorkloadOutcome)).To(gomega.Equal("inconclusive"))
				gomega.Expect(view.Exit.Outcome.NativeExitCode).To(gomega.BeNil())
			} else {
				gomega.Expect(string(view.Exit.Outcome.WorkloadOutcome)).To(gomega.Equal("fail"))
				gomega.Expect(*view.Exit.Outcome.NativeExitCode).To(gomega.Equal(126))
			}
			gomega.Expect(os.RemoveAll(root)).To(gomega.Succeed())
		}
	})
	ginkgo.It("retains a discovered descendant's birth identity across setsid and reparenting", func() {
		root, q := nativeFixture("exit 0")
		executable, err := os.Executable()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q.Argv = []string{executable, "runner-fork-fixture"}
		gomega.Expect(os.Remove(filepath.Join(root, "request.json"))).To(gomega.Succeed())
		gomega.Expect(Prepare(root, q)).To(gomega.Succeed())
		_, done := launchFixture(root, "")
		view := awaitFixture(root, done, false)
		gomega.Expect(view.Exit.TerminationProven).To(gomega.BeTrue())
		pidText, err := os.ReadFile(filepath.Join(root, "workspace/child.pid"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pid, err := strconv.Atoi(string(pidText))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		observed, err := platform.ObserveProcess(pid)
		gomega.Expect(errors.Is(err, unix.ESRCH) || (err == nil && observed.Zombie)).To(gomega.BeTrue())
		gomega.Expect(os.RemoveAll(root)).To(gomega.Succeed())
	})
	for _, point := range []string{"before_spawn", "after_spawn", "after_start", "after_outcome"} {
		ginkgo.It("retains uncertainty and refuses a repeat workload after supervisor death at "+point, func() {
			root, _ := nativeFixture("printf once >> count.txt")
			_, done := launchFixture(root, point)
			view := awaitFixture(root, done, true)
			gomega.Expect(view.Intent).NotTo(gomega.BeNil())
			gomega.Expect(view.Exit).To(gomega.BeNil())
			_, err := StartRun(filepath.Join(root, "request.json"))
			gomega.Expect(errors.Is(err, ErrAmbiguous)).To(gomega.BeTrue())
			var stdout, stderr bytes.Buffer
			gomega.Expect(Command([]string{"start", "--request", filepath.Join(root, "request.json")}, &stdout, &stderr)).To(gomega.Equal(2))
			envelope, err := managed.DecodeEnvelope(stdout.Bytes())
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(envelope.Error.Code).To(gomega.Equal(managed.RecoveryBlocked))
			gomega.Expect(envelope.Error.Retry).To(gomega.Equal("reconcile"))
			gomega.Expect(envelope.Error.Message).To(gomega.ContainSubstring("do not start again"))
			if point == "after_outcome" {
				bytes, err := os.ReadFile(filepath.Join(root, "workspace/count.txt"))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(string(bytes)).To(gomega.Equal("once"))
			} else {
				gomega.Consistently(func() bool {
					_, err := os.Stat(filepath.Join(root, "workspace/count.txt"))
					return os.IsNotExist(err)
				}, 200*time.Millisecond, 25*time.Millisecond).Should(gomega.BeTrue())
			}
			if view.Start != nil {
				gomega.Eventually(func() bool {
					_, err := platform.ObserveProcess(view.Start.Process.PID)
					return errors.Is(err, unix.ESRCH)
				}, 4*time.Second, 25*time.Millisecond).Should(gomega.BeTrue())
			}
			// after_spawn has no start identity: retain its directory rather than
			// claim an unidentified trampoline is gone. EOF prevents project exec.
			if point != "after_spawn" {
				gomega.Expect(os.RemoveAll(root)).To(gomega.Succeed())
			}
		})
	}
	ginkgo.It("rejects a prior-boot request before creating a spawn intent", func() {
		root := privateFixtureDirectory()
		defer os.RemoveAll(root)
		q := requestFixture(root)
		gomega.Expect(Prepare(root, q)).To(gomega.Succeed())
		_, err := StartRun(filepath.Join(root, "request.json"))
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
		var stdout, stderr bytes.Buffer
		gomega.Expect(Command([]string{"start", "--request", filepath.Join(root, "request.json")}, &stdout, &stderr)).To(gomega.Equal(2))
		envelope, err := managed.DecodeEnvelope(stdout.Bytes())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(envelope.Error.Code).To(gomega.Equal(managed.Conflict))
		gomega.Expect(envelope.Error.Retry).To(gomega.Equal("none"))
		_, err = os.Stat(filepath.Join(root, "intent.json"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
})

var _ = ginkgo.Describe("Task009UncertaintyOutcome", ginkgo.Label("integration"), func() {
	for _, outcome := range []struct {
		code     int
		workload managed.WorkloadOutcome
	}{{0, "pass"}, {7, "fail"}} {
		ginkgo.It(fmt.Sprintf("preserves native exit %d without inventing a cancellation request", outcome.code), func() {
			root, _ := nativeFixture(fmt.Sprintf("trap 'exit %d' TERM; printf once >> count.txt; while :; do /bin/sleep 0.02; done", outcome.code))
			defer os.RemoveAll(root)
			_, done := launchFixture(root, "census-once")
			view := awaitFixture(root, done, false)
			gomega.Expect(view.Exit).NotTo(gomega.BeNil())
			gomega.Expect(view.Exit.TerminationProven).To(gomega.BeFalse())
			gomega.Expect(view.Exit.Reason).To(gomega.Equal("native_observation_uncertain"))
			_, err := os.Stat(filepath.Join(root, "cancel.json"))
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			gomega.Expect(view.Exit.Outcome.NativeExitCode).NotTo(gomega.BeNil())
			gomega.Expect(*view.Exit.Outcome.NativeExitCode).To(gomega.Equal(outcome.code))
			gomega.Expect(view.Exit.Outcome.WorkloadOutcome).To(gomega.Equal(outcome.workload))
			replay, err := StartRun(filepath.Join(root, "request.json"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(replay).To(gomega.Equal(view))
			data, err := os.ReadFile(filepath.Join(root, "workspace/count.txt"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(string(data)).To(gomega.Equal("once"))
		})
	}
})
