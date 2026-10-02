//go:build integration

package controller

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

var _ = ginkgo.Describe("Task009IndependentControllerRunner", ginkgo.Label("integration"), func() {
	ginkgo.It("imports a proven no-start deadline receipt and permits collection", func() {
		t := ginkgo.GinkgoT()
		_, s, r, job, _ := cancellationRuntime(t, "true")
		var err error
		r, err = s.ledger.AdvanceLocal(r.JobID, r.JobRevision, "preparing", "ready", "ready-expired-gate")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		r, err = s.ledger.StartLocal(r.JobID, r.JobRevision, "run-expired-gate")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		c, err := s.registerLocal(r)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer s.unregisterLocal(c)
		root := filepath.Join(s.root.Path, "jobs", r.JobID)
		work := filepath.Join(root, "workspace")
		gomega.Expect(os.MkdirAll(work, 0700)).To(gomega.Succeed())
		log, err := s.createRunnerFile(r.JobID, "output.log")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var st unix.Stat_t
		gomega.Expect(unix.Fstat(int(log.Fd()), &st)).To(gomega.Succeed())
		gomega.Expect(log.Close()).To(gomega.Succeed())
		boot, err := platform.BootIdentity()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q := runner.Request{Schema: "haepari.runner-request/v1", Identity: c.identity, BootID: boot, WorkDir: work, Argv: []string{"/bin/sh", "-c", "printf unwanted > effect.txt"}, Environment: []string{"PATH=/usr/bin:/bin"}, Deadline: time.Now().Add(-time.Second).UTC(), RunSeconds: 1, TerminationSeconds: 2, LogDevice: uint64(st.Dev), LogInode: uint64(st.Ino)}
		data, err := json.Marshal(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(s.ledger.RecordRunnerReceipt(c.identity, "request", data)).To(gomega.Succeed())
		gomega.Expect(runner.Prepare(root, q)).To(gomega.Succeed())
		c.runnerRequest = filepath.Join(root, "request.json")
		executable, err := os.Executable()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		cmd := exec.Command(executable, "runner", "start", "--request", c.runnerRequest)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		cmd.Env = []string{"PATH=/usr/bin:/bin", "GORACE=atexit_sleep_ms=0"}
		gomega.Expect(cmd.Run()).To(gomega.Succeed())
		view, err := s.importRunner(c)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(view.Exit.TerminationProven).To(gomega.BeTrue())
		gomega.Expect(s.observeLocalCompletion(c)).To(gomega.Succeed())
		out, err := s.ledger.LocalOutcome(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(out).To(gomega.Equal(managed.LocalOutcome{WorkloadOutcome: "inconclusive"}))
		current, err := s.ledger.CompleteLocalRun(managed.LocalTerminationRequest{Identity: c.identity, ExpectedRevision: c.receipt.JobRevision, MutationKey: "termination-expired-gate"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.JobState).To(gomega.Equal("collecting"))
		gomega.Expect(current.ResourceAvailability).To(gomega.Equal("available"))
		c.receipt = current
		gomega.Expect(s.settleLocalPhases(c, job, root, work, out, false, "process_start_failed", nil)).To(gomega.Succeed())
		result, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("complete")))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("available"))
		gomega.Expect(result.ProtectedEvidence).To(gomega.BeEmpty())
		_, err = os.Stat(filepath.Join(work, "effect.txt"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
	ginkgo.It("retains occupancy when a present exit receipt cannot prove termination", func() {
		t := ginkgo.GinkgoT()
		_, s, r, job, object := cancellationRuntime(t, "printf once > once.txt; exec /bin/sleep 20")
		_, release, done := cancellationBarrier(t, s, r, job, object, "running")
		root := filepath.Join(s.root.Path, "jobs", r.JobID)
		path := filepath.Join(root, "request.json")
		gomega.Eventually(func() string {
			data, _ := os.ReadFile(filepath.Join(root, "workspace/source/once.txt"))
			return string(data)
		}, 4*time.Second, 25*time.Millisecond).Should(gomega.Equal("once"))
		data, err := os.ReadFile(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var q runner.Request
		gomega.Expect(json.Unmarshal(data, &q)).To(gomega.Succeed())
		binding := q.Binding()
		binding.Identity.Nonce = "conflicting-cancellation"
		data, err = json.Marshal(runner.Cancellation{Schema: "haepari.runner-cancel/v1", Binding: binding, RequestedAt: time.Now().UTC()})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// Native supervision observes conflicting evidence and stops its owned
		// workload, while retaining an explicit unproven termination receipt.
		gomega.Expect(os.WriteFile(filepath.Join(root, "cancel.json"), data, 0600)).To(gomega.Succeed())
		release()
		select {
		case err := <-done:
			gomega.Expect(errors.Is(err, runner.ErrUnproven)).To(gomega.BeTrue())
			s.failed(r.JobID, err)
		case <-time.After(4 * time.Second):
			ginkgo.Fail("controller did not bound unproven exit observation")
		}
		view, err := runner.Inspect(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(view.Exit).NotTo(gomega.BeNil())
		gomega.Expect(view.Exit.TerminationProven).To(gomega.BeFalse())
		gomega.Expect(view.Exit.Reason).To(gomega.Equal("native_observation_uncertain"))
		current, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.JobState).To(gomega.Equal("reconciling"))
		gomega.Expect(current.ResourceAvailability).To(gomega.Equal("quarantined"))
		gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
		replay, err := runner.StartRun(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay).To(gomega.Equal(view))
		data, err = os.ReadFile(filepath.Join(root, "workspace/source/once.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("once"))
	})
	ginkgo.It("resolves a bare workload through the declared PATH in the production runner", func() {
		t := ginkgo.GinkgoT()
		_, s, r, job, object := cancellationRuntime(t, "", func(j *managed.JobSpec) {
			j.Workload.Argv = []string{"haepari-declared-path-fixture", "-c", "/bin/mkdir -p ../results; printf path-passed > ../results/output.txt"}
			j.Workload.Environment = map[string]string{"PATH": "./declared"}
		})
		_, release, done := cancellationBarrier(t, s, r, job, object, "ready")
		bin := filepath.Join(s.root.Path, "jobs", r.JobID, "workspace", "source", "declared")
		gomega.Expect(os.Mkdir(bin, 0700)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(bin, "haepari-declared-path-fixture"), []byte("#!/bin/sh\nexec /bin/sh \"$@\"\n"), 0700)).To(gomega.Succeed())
		release()
		awaitCancellation(t, done)
		result, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
	})
	ginkgo.It("retains occupied reconciliation after supervisor death without repeating the workload", func() {
		t := ginkgo.GinkgoT()
		_, s, r, job, object := cancellationRuntime(t, "printf once > once.txt; exec /bin/sleep 20")
		_, release, done := cancellationBarrier(t, s, r, job, object, "running")
		root := filepath.Join(s.root.Path, "jobs", r.JobID)
		path := filepath.Join(root, "request.json")
		var view runner.Observation
		gomega.Eventually(func() bool {
			var err error
			view, err = runner.Inspect(path)
			data, readErr := os.ReadFile(filepath.Join(root, "workspace/source/once.txt"))
			return err == nil && view.Start != nil && readErr == nil && string(data) == "once"
		}, 4*time.Second, 25*time.Millisecond).Should(gomega.BeTrue())
		// Test cleanup owns only this exact observed workload incarnation.
		defer func() {
			_ = platform.SignalProcess(view.Start.Process, unix.SIGKILL)
			gomega.Eventually(func() bool {
				observed, err := platform.ObserveProcess(view.Start.Process.PID)
				return errors.Is(err, unix.ESRCH) || (err == nil && observed.Zombie)
			}, 3*time.Second, 25*time.Millisecond).Should(gomega.BeTrue())
		}()
		gomega.Expect(platform.SignalProcess(view.Intent.Supervisor, unix.SIGKILL)).To(gomega.Succeed())
		release()
		select {
		case err := <-done:
			gomega.Expect(errors.Is(err, runner.ErrUnproven)).To(gomega.BeTrue())
			s.failed(r.JobID, err)
		case <-time.After(4 * time.Second):
			ginkgo.Fail("controller did not bound missing-supervisor observation")
		}
		current, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.JobState).To(gomega.Equal("reconciling"))
		gomega.Expect(current.ResourceAvailability).To(gomega.Equal("quarantined"))
		gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
		_, err = runner.StartRun(path)
		gomega.Expect(errors.Is(err, runner.ErrAmbiguous)).To(gomega.BeTrue())
		data, err := os.ReadFile(filepath.Join(root, "workspace/source/once.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("once"))
	})
})
