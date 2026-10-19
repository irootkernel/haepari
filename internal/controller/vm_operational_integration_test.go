//go:build integration

package controller

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task037VMOperationalBounds", ginkgo.Label("integration"), func() {
	ginkgo.It("retains an active prepared runner and unresolved finished evidence", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf kept > ../results/output.txt")
		f.s.vmTransport = true
		f.job.Deadlines.PrepareSeconds = 30
		f.job.Deadlines.CollectSeconds = 30
		f.job.Deadlines.CleanupSeconds = 30
		r, err := f.reserve(f.admit("active-runner"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		native := f.s.sshCommand
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			if vmTransportRequest(input).Operation == "start" {
				close(entered)
				<-release
			}
			return native(ctx, exe, args, input)
		}
		go func() {
			defer ginkgo.GinkgoRecover()
			done <- f.s.executeVM(r, f.job, f.object, time.Now().Add(30*time.Second))
		}()
		defer unblock()
		gomega.Eventually(entered, 10*time.Second).Should(gomega.BeClosed())
		path := filepath.Join(f.s.root.Path, "jobs", r.JobID, "guest-runner")
		before, err := os.ReadFile(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		current, err := f.s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = f.s.purgeJob(r.JobID, current.JobRevision)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrBusy))
		gomega.Expect(os.ReadFile(path)).To(gomega.Equal(before))
		unblock()
		gomega.Eventually(done, 20*time.Second).Should(gomega.Receive(gomega.Succeed()))
		unresolved := nativeVMFixture(ginkgo.GinkgoT(), "printf original")
		unresolved.s.vmTransport = true
		unresolved.job.Deadlines.PrepareSeconds = 30
		unresolved.job.Deadlines.CollectSeconds = 30
		unresolved.job.Deadlines.CleanupSeconds = 30
		u, e := unresolved.reserve(unresolved.admit("unresolved-runner"))
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(unresolved.s.executeVM(u, unresolved.job, unresolved.object, time.Now().Add(30*time.Second))).To(gomega.Succeed())
		result, e := unresolved.s.ledger.Result(u.JobID)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		path = filepath.Join(unresolved.s.root.Path, "jobs", u.JobID, "guest-runner")
		before, e = os.ReadFile(path)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		current, e = unresolved.s.ledger.Snapshot(u.JobID)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		_, e = unresolved.s.purgeJob(u.JobID, current.JobRevision)
		gomega.Expect(e).To(gomega.MatchError(managed.ErrBlocked))
		gomega.Expect(os.ReadFile(path)).To(gomega.Equal(before))
	})
	ginkgo.It("refuses unknown runner leaves and resumes interrupted purge only with its original runner inode", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf done > ../results/output.txt")
		f.s.vmTransport = true
		f.job.Deadlines.PrepareSeconds = 30
		f.job.Deadlines.CollectSeconds = 30
		f.job.Deadlines.CleanupSeconds = 30
		r, err := f.reserve(f.admit("interrupted-runner"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.s.executeVM(r, f.job, f.object, time.Now().Add(30*time.Second))).To(gomega.Succeed())
		root := filepath.Join(f.s.root.Path, "jobs", r.JobID)
		unknown := filepath.Join(root, "guest-recovery-runner-unknown")
		gomega.Expect(os.WriteFile(unknown, []byte("unknown"), 0600)).To(gomega.Succeed())
		current, err := f.s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = f.s.purgeJob(r.JobID, current.JobRevision)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrBlocked))
		gomega.Expect(os.ReadFile(unknown)).To(gomega.Equal([]byte("unknown")))
		gomega.Expect(os.Remove(unknown)).To(gomega.Succeed())
		child := retentionChild(f.s, r.JobID, "purge", "purge-leaf-removed")
		q, found, complete, err := f.s.ledger.PurgeIntent(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(found).To(gomega.BeTrue())
		gomega.Expect(complete).To(gomega.BeFalse())
		gomega.Expect(child.Process.Kill()).To(gomega.Succeed())
		gomega.Expect(child.Wait()).To(gomega.HaveOccurred())
		path := filepath.Join(root, "guest-runner")
		before, err := os.ReadFile(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.Rename(path, path+".original")).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(path, before, 0600)).To(gomega.Succeed())
		_, err = f.s.purgeJob(r.JobID, q.ExpectedRevision)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrBlocked))
		gomega.Expect(os.ReadFile(path)).To(gomega.Equal(before))
		gomega.Expect(os.Remove(path)).To(gomega.Succeed())
		gomega.Expect(os.Rename(path+".original", path)).To(gomega.Succeed())
		p, err := f.s.purgeJob(r.JobID, q.ExpectedRevision)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.Complete).To(gomega.BeTrue())
		_, err = os.Lstat(path)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
	ginkgo.It("keeps historical prepared Jobs on their original guest wire after enabling the new controller", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf old > ../results/output.txt")
		native := f.s.sshCommand
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			defer ginkgo.GinkgoRecover()
			q := vmTransportRequest(input)
			gomega.Expect(q.Schema).To(gomega.Equal("haepari.guest-request/v1"))
			if q.Operation == "start" {
				f.s.vmTransport = true
			}
			if q.Operation != "health" && q.Operation != "identity" {
				gomega.Expect(args[len(args)-1]).To(gomega.ContainSubstring("runner guest --stdio"))
			}
			return native(ctx, exe, args, input)
		}
		result := f.run("historical-wire")
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		raw, err := f.s.ledger.VMTransportAdmission(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(raw).To(gomega.BeEmpty())
	})
	ginkgo.It("waits for operator readiness, valid lease and pinned identity before staging once", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf ready > ../results/output.txt")
		r, err := f.reserve(f.admit("cold-ready"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		control := f.s.utmCommand
		starts, statuses, leases, proofs, prepares := 0, 0, 0, 0, 0
		f.s.utmCommand = func(ctx context.Context, exe string, args ...string) (utm.CommandResult, error) {
			if args[0] == "start" {
				starts++
			}
			if args[0] == "status" {
				statuses++
				if statuses < 3 {
					return utm.CommandResult{Stdout: "stopped"}, nil
				}
			}
			return control(ctx, exe, args...)
		}
		readLease := f.s.vmLeases
		f.s.vmLeases = func() (io.ReadCloser, error) {
			leases++
			if leases < 3 {
				return io.NopCloser(strings.NewReader("")), nil
			}
			return readLease()
		}
		native := f.s.sshCommand
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			q := vmTransportRequest(input)
			if q.Operation == "identity" {
				proofs++
				if proofs == 1 {
					return utm.CommandResult{}, utm.ErrUnavailable
				}
			}
			if q.Operation == "prepare" {
				prepares++
				gomega.Expect(leases).To(gomega.BeNumerically(">=", 3))
				gomega.Expect(statuses).To(gomega.BeNumerically(">=", 3))
				gomega.Expect(proofs).To(gomega.BeNumerically(">=", 2))
			}
			return native(ctx, exe, args, input)
		}
		gomega.Expect(f.s.executeVM(r, f.job, f.object, time.Now().Add(10*time.Second))).To(gomega.Succeed())
		result, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		gomega.Expect(starts).To(gomega.BeZero())
		gomega.Expect(f.control.effects).To(gomega.BeEmpty())
		gomega.Expect(prepares).To(gomega.Equal(1))
	})
	ginkgo.It("ends failed readiness at the original deadline without guest staging or a run claim", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "exit 1")
		r, err := f.reserve(f.admit("expired-ready"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		calls := 0
		f.s.vmLeases = func() (io.ReadCloser, error) { calls++; return io.NopCloser(strings.NewReader("")), nil }
		f.s.sshCommand = func(context.Context, string, []string, []byte) (utm.CommandResult, error) {
			ginkgo.Fail("no valid lease may reach SSH")
			return utm.CommandResult{}, nil
		}
		deadline := time.Now().Add(2 * time.Second)
		gomega.Expect(f.s.executeVM(r, f.job, f.object, deadline)).To(gomega.Succeed())
		gomega.Expect(time.Now().Before(deadline)).To(gomega.BeFalse())
		gomega.Expect(time.Since(deadline)).To(gomega.BeNumerically("<", time.Second))
		gomega.Expect(calls).To(gomega.BeNumerically(">", 1))
		id, err := f.s.ledger.LocalIdentity(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(id.RunID).To(gomega.BeEmpty())
		_, err = os.Lstat(filepath.Join(f.guest, r.JobID))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		result, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("not_needed")))
	})
	ginkgo.It("keeps host-wide denial pause when cancellation arrives before the no-effect classification", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "false")
		r, err := f.reserve(f.admit("cancel-host-denied"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f.s.utmCommand = func(context.Context, string, ...string) (utm.CommandResult, error) {
			current, e := f.s.ledger.Snapshot(r.JobID)
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			_, e = f.s.ledger.Cancel(managed.CancelRequest{JobID: r.JobID, ExpectedRevision: current.JobRevision, MutationKey: "cancel-before-denial"})
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			return utm.CommandResult{Stderr: "Error from event: (OSStatus error -1743.)"}, nil
		}
		f.s.sshCommand = func(context.Context, string, []string, []byte) (utm.CommandResult, error) {
			ginkgo.Fail("denied preparation must not reach SSH")
			return utm.CommandResult{}, nil
		}
		gomega.Expect(f.s.executeVM(r, f.job, f.object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		result, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Cancelled))
		status, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Reason).To(gomega.Equal("host_permission_unavailable"))
		gomega.Expect(status.Occupancy).To(gomega.BeZero())
	})
	ginkgo.It("settles a durable cancellation before any run claim without staging", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "exit 1")
		r, err := f.reserve(f.admit("cancel-ready"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f.s.vmLeases = func() (io.ReadCloser, error) {
			current, e := f.s.ledger.Snapshot(r.JobID)
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			_, e = f.s.ledger.Cancel(managed.CancelRequest{JobID: r.JobID, ExpectedRevision: current.JobRevision, MutationKey: "cancel-readiness"})
			gomega.Expect(e).NotTo(gomega.HaveOccurred())
			return io.NopCloser(strings.NewReader("")), nil
		}
		gomega.Expect(f.s.executeVM(r, f.job, f.object, time.Now().Add(2*time.Second))).To(gomega.Succeed())
		result, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Cancelled))
		current, err := f.s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.GlobalOccupancy).To(gomega.BeZero())
		_, err = os.Lstat(filepath.Join(f.guest, r.JobID))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
	ginkgo.It("uses the production capability, retains measurement pairs and purges a safe runner without losing health or keys", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf measured > ../results/output.txt")
		f.s.vmTransport = true
		f.job.Deadlines.PrepareSeconds = 30
		f.job.Deadlines.CollectSeconds = 30
		f.job.Deadlines.CleanupSeconds = 30
		r, err := f.reserve(f.admit("retained-runner"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.s.executeVM(r, f.job, f.object, time.Now().Add(30*time.Second))).To(gomega.Succeed())
		result, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		binding, err := f.s.ledger.VMTransportBinding(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(binding).NotTo(gomega.BeEmpty())
		batch, err := f.s.ledger.EventsAfter(r.JobID, 0, 1000)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		starts, completes := 0, 0
		for _, event := range batch.Events {
			if event.Kind == "vm_ssh_exchange_start" {
				starts++
			}
			if event.Kind == "vm_ssh_exchange_complete" {
				completes++
			}
		}
		gomega.Expect(starts).To(gomega.BeNumerically(">", 2))
		gomega.Expect(completes).To(gomega.Equal(starts))
		current, err := f.s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		receipt, err := f.s.purgeJob(r.JobID, current.JobRevision)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(receipt.Complete).To(gomega.BeTrue())
		_, err = os.Lstat(filepath.Join(f.s.root.Path, "jobs", r.JobID, "guest-runner"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		q, err := f.s.vmBinding(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q.Operation = "health"
		q.Deadline = time.Now().Add(10 * time.Second)
		health, err := f.s.vmCall(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(health.Reusable).To(gomega.BeTrue())
		old, err := f.s.ledger.Request("key-" + r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(old.Receipt.JobID).To(gomega.Equal(r.JobID))
		_, err = f.s.ledger.Result(r.JobID)
		gomega.Expect(errors.Is(err, managed.ErrResultUnavailable)).To(gomega.BeTrue())
		gomega.Expect(f.s.ledger.PurgeEventsThrough(r.JobID, batch.CurrentCursor)).To(gomega.Succeed())
		retained, err := f.s.ledger.VMTransportBinding(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(retained).To(gomega.Equal(binding))
	})
})
