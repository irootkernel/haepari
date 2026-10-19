//go:build integration

package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

const observationScript = "mkdir -p ../results; printf retained > ../results/output.txt"

type guestExchange struct {
	JobID     string
	Operation string
	Deadline  time.Time
	At        time.Time
}

type observationClock struct {
	mu        sync.Mutex
	armed     bool
	delay     time.Duration
	rows      []guestExchange
	limited   bool
	remaining int
}

func (c *observationClock) limitSleeps(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.limited, c.remaining = true, n
}

func (c *observationClock) exchanges() []guestExchange {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]guestExchange(nil), c.rows...)
}

// The first list of each vmCall is the pre-identity hop. Later lists in that
// same call stay immediate, and the next call is armed again only after its
// operation exchange. Sleeping on every list would exhaust one shared clock.
func installObservationClock(f *vmJobFixture, delay time.Duration) *observationClock {
	c := &observationClock{armed: delay > 0, delay: delay}
	baseCommand := f.s.utmCommand
	baseSSH := f.s.sshCommand
	f.s.utmCommand = func(ctx context.Context, exe string, args ...string) (utm.CommandResult, error) {
		if delay > 0 && len(args) > 0 && args[0] == "list" {
			c.mu.Lock()
			sleep := c.armed
			if c.limited && c.remaining == 0 {
				sleep = false
			}
			if sleep {
				c.armed = false
				if c.limited {
					c.remaining--
				}
			}
			c.mu.Unlock()
			if sleep {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return utm.CommandResult{}, ctx.Err()
				case <-timer.C:
				}
			}
		}
		return baseCommand(ctx, exe, args...)
	}
	f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
		q := vmTransportRequest(input)
		c.mu.Lock()
		c.rows = append(c.rows, guestExchange{JobID: q.Request.Identity.JobID, Operation: q.Operation, Deadline: q.Deadline, At: time.Now()})
		if q.Operation != "identity" {
			c.armed = true
		}
		c.mu.Unlock()
		return baseSSH(ctx, exe, args, input)
	}
	return c
}

func retainedObservationJob(id string, configure func(*managed.ResourceSpec)) (*vmJobFixture, managed.Result) {
	f := nativeVMConfiguredFixture(ginkgo.GinkgoT(), observationScript, configure)
	f.s.vmWrite = func(*os.File, []byte) (int, error) { return 0, unix.ENOSPC }
	return f, f.run(id)
}

func expectNoPower(f *vmJobFixture) {
	gomega.Expect(f.control.effects).To(gomega.BeEmpty())
}

func identityDeadlines(rows []guestExchange, jobID string) []guestExchange {
	var out []guestExchange
	for _, row := range rows {
		if row.Operation == "identity" && (jobID == "" || row.JobID == jobID) {
			out = append(out, row)
		}
	}
	return out
}

func expectSharedClock(rows []guestExchange, jobID string, started time.Time) {
	seen := identityDeadlines(rows, jobID)
	gomega.Expect(seen).NotTo(gomega.BeEmpty())
	for _, row := range seen {
		gomega.Expect(row.Deadline).To(gomega.Equal(seen[0].Deadline))
		gomega.Expect(row.Deadline).To(gomega.BeTemporally("~", started.Add(recoveryObservationBudget), 3*time.Second))
	}
	gomega.Expect(seen[0].Deadline.Sub(seen[0].At)).To(gomega.BeNumerically(">", 5*time.Second))
	gomega.Expect(seen[0].Deadline.Sub(seen[0].At)).To(gomega.BeNumerically("<", recoveryObservationBudget))
}

var bootIDPattern = regexp.MustCompile(`"boot_id":"[0-9a-fA-F-]{36}"`)

var _ = ginkgo.Describe("Task027ObservationBudget", ginkgo.Label("integration"), func() {
	ginkgo.It("accepts one authenticated pre-identity delay above five seconds on a shared fifteen second clock", func() {
		f, _ := retainedObservationJob("observation-six", task027ResourceBudgets)
		clock := installObservationClock(f, 6*time.Second)
		started := time.Now()
		plan, err := f.s.planEvidenceRecovery(f.spec.ID, "dispose")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.Schema).To(gomega.Equal("haepari.recovery-plan/v3"))
		gomega.Expect(plan.Budget).NotTo(gomega.BeNil())
		gomega.Expect(plan.Budget.CollectSeconds).To(gomega.Equal(int64(90)))
		gomega.Expect(plan.Budget.CleanupSeconds).To(gomega.Equal(int64(90)))
		gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
		snap, err := f.s.ledger.ResourceRecoverySnapshot(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(snap.EvidenceIntent).To(gomega.BeNil())
		seen := identityDeadlines(clock.exchanges(), "")
		gomega.Expect(len(seen)).To(gomega.BeNumerically(">=", 2))
		expectSharedClock(clock.exchanges(), seen[0].JobID, started)
		gomega.Expect(seen[len(seen)-1].At.Sub(seen[0].At)).To(gomega.BeNumerically(">", 5*time.Second))
		expectNoPower(f)
	})

	ginkgo.It("blocks a sixteen second pre-identity delay before an evidence key exists", func() {
		f, _ := retainedObservationJob("observation-sixteen", task027ResourceBudgets)
		installObservationClock(f, 16*time.Second)
		w := recoveryHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{Plan: true, Action: "dispose"})
		gomega.Expect(w.Code).NotTo(gomega.Equal(200), w.Body.String())
		gomega.Expect(w.Body.String()).To(gomega.ContainSubstring("RECOVERY_BLOCKED"))
		snap, err := f.s.ledger.ResourceRecoverySnapshot(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(snap.EvidenceIntent).To(gomega.BeNil())
		expectNoPower(f)
	})

	ginkgo.It("keeps a quarantined single-capacity successor queued while observing the original retained job", func() {
		f, first := retainedObservationJob("unresolved-original", nil)
		beforeResource, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.spec.Capacity).To(gomega.Equal(1))
		gomega.Expect(beforeResource.Availability).To(gomega.Equal("quarantined"))
		gomega.Expect(beforeResource.Occupancy).To(gomega.BeZero())
		second := f.admit("unresolved-successor")
		before, err := f.s.ledger.Request("key-" + second.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		clock := installObservationClock(f, 0)
		_, err = f.reserve(second)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrBusy))
		gomega.Expect(clock.exchanges()).To(gomega.BeEmpty())
		after, err := f.s.ledger.Request("key-" + second.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after.DeadlineNS).To(gomega.Equal(before.DeadlineNS))
		queued, err := f.s.ledger.Snapshot(second.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(queued.JobState).To(gomega.Equal("queued"))
		gomega.Expect(queued.LeaseGeneration).To(gomega.BeZero())
		afterResource, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(afterResource.Availability).To(gomega.Equal("quarantined"))
		gomega.Expect(afterResource.Occupancy).To(gomega.BeZero())
		gomega.Expect(afterResource.LeaseGeneration).To(gomega.Equal(beforeResource.LeaseGeneration))
		clock = installObservationClock(f, 6*time.Second)
		started := time.Now()
		plan, err := f.s.planEvidenceRecovery(f.spec.ID, "dispose")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		jobs, ok := plan.Observation.([]managed.EvidenceRecoveryJob)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(jobs).To(gomega.HaveLen(1))
		gomega.Expect(jobs[0].JobID).To(gomega.Equal(first.JobID))
		expectSharedClock(clock.exchanges(), first.JobID, started)
		expectNoPower(f)
	})

	ginkgo.It("checks every historical job on its own clock and skips only empty not-needed evidence", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), observationScript)
		ids := make([]string, 6)
		for i := range ids {
			ids[i] = fmt.Sprintf("history-%d", i)
			result := f.run(ids[i])
			gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
			gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		}
		snap, err := f.s.ledger.ResourceRecoverySnapshot(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		snap.Jobs = append(snap.Jobs, managed.ResourceRecoveryJob{
			JobID: "not-needed-skipped",
			Result: &managed.Result{
				JobID:          "not-needed-skipped",
				CleanupOutcome: "not_needed",
			},
		})
		clock := installObservationClock(f, 6*time.Second)
		facts, healthy := (vmEvidenceBackend{f.s, f.spec.ID}).Health(snap)
		gomega.Expect(healthy).To(gomega.BeTrue())
		mapped, ok := facts.(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(mapped).To(gomega.HaveLen(6))
		gomega.Expect(mapped).NotTo(gomega.HaveKey("not-needed-skipped"))
		var first, last guestExchange
		for i, id := range ids {
			gomega.Expect(mapped[id]).To(gomega.Equal(true))
			seen := identityDeadlines(clock.exchanges(), id)
			gomega.Expect(seen).NotTo(gomega.BeEmpty())
			gomega.Expect(seen[0].Deadline.Sub(seen[0].At)).To(gomega.BeNumerically(">", 5*time.Second))
			gomega.Expect(seen[0].Deadline.Sub(seen[0].At)).To(gomega.BeNumerically("<", recoveryObservationBudget))
			if i == 0 {
				first = seen[0]
			}
			last = seen[0]
		}
		gomega.Expect(last.Deadline.Sub(first.Deadline)).To(gomega.BeNumerically(">", 20*time.Second))
		for _, row := range clock.exchanges() {
			gomega.Expect(row.JobID).NotTo(gomega.Equal("not-needed-skipped"))
		}
		expectNoPower(f)
	})

	ginkgo.It("opens no guest exchange from an expired health fallback and does not start another clock", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), observationScript)
		result := f.run("expired-health-fallback")
		q, err := f.s.vmBinding(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		baseSSH := f.s.sshCommand
		baseCommand := f.s.utmCommand
		calls := 0
		f.s.sshCommand = func(context.Context, string, []string, []byte) (utm.CommandResult, error) {
			calls++
			return utm.CommandResult{}, errors.New("expired fallback must not exchange")
		}
		f.s.utmCommand = func(context.Context, string, ...string) (utm.CommandResult, error) {
			calls++
			return utm.CommandResult{}, errors.New("expired fallback must not list")
		}
		_, _, err = f.s.observeVMRebootRecoveryUntil(q, time.Now().Add(-time.Second))
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(calls).To(gomega.BeZero())

		var operations []string
		var mu sync.Mutex
		f.s.utmCommand = baseCommand
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			operation := vmTransportRequest(input).Operation
			mu.Lock()
			operations = append(operations, operation)
			mu.Unlock()
			if operation == "identity" {
				<-ctx.Done()
				return utm.CommandResult{}, ctx.Err()
			}
			return baseSSH(ctx, exe, args, input)
		}
		snap, err := f.s.ledger.ResourceRecoverySnapshot(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, healthy := (vmEvidenceBackend{f.s, f.spec.ID}).Health(snap)
		gomega.Expect(healthy).To(gomega.BeFalse())
		gomega.Expect(operations).To(gomega.ContainElement("identity"))
		gomega.Expect(operations).NotTo(gomega.ContainElement("probe"))
		gomega.Expect(operations).NotTo(gomega.ContainElement("state"))
		gomega.Expect(operations).NotTo(gomega.ContainElement("health"))
		expectNoPower(f)
	})

	ginkgo.It("keeps a health reboot fallback on the same job clock", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), observationScript)
		f.run("health-same-clock")
		base := f.s.sshCommand
		var mu sync.Mutex
		var rows []guestExchange
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			q := vmTransportRequest(input)
			mu.Lock()
			rows = append(rows, guestExchange{Operation: q.Operation, Deadline: q.Deadline, At: time.Now()})
			mu.Unlock()
			if q.Operation == "health" {
				return utm.CommandResult{}, errors.New("fixture lost health")
			}
			return base(ctx, exe, args, input)
		}
		snap, err := f.s.ledger.ResourceRecoverySnapshot(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, _ = (vmEvidenceBackend{f.s, f.spec.ID}).Health(snap)
		// Same-boot CallRecovery proves identity and then refuses probe when that
		// boot still equals the original guest boot. The identity hop carries the
		// Health clock; this fixture does not reach the probe exchange.
		var identities []guestExchange
		var lastHealth time.Time
		for _, row := range rows {
			gomega.Expect(row.Operation).NotTo(gomega.Equal("probe"))
			switch row.Operation {
			case "identity":
				identities = append(identities, row)
			case "health":
				lastHealth = row.At
			}
		}
		gomega.Expect(lastHealth.IsZero()).To(gomega.BeFalse())
		gomega.Expect(identities).NotTo(gomega.BeEmpty())
		gomega.Expect(identities[0].Deadline.Sub(identities[0].At)).To(gomega.BeNumerically(">", 10*time.Second))
		gomega.Expect(identities[0].Deadline.Sub(identities[0].At)).To(gomega.BeNumerically("<", recoveryObservationBudget+time.Second))
		var fallback []guestExchange
		for _, row := range identities {
			gomega.Expect(row.Deadline).To(gomega.Equal(identities[0].Deadline))
			if row.At.After(lastHealth) {
				fallback = append(fallback, row)
			}
		}
		gomega.Expect(fallback).To(gomega.HaveLen(1))
		expectNoPower(f)
	})

	ginkgo.It("clamps effect observation to the saved legacy deadlines", func() {
		f, result := retainedObservationJob("legacy-clamp", nil)
		q, err := f.s.vmBinding(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		until := time.Now().Add(4 * time.Second)
		installObservationClock(f, 6*time.Second)
		_, err = (vmEvidenceBackend{f.s, f.spec.ID}).Observe(q.Request.Identity, "dispose", until)
		gomega.Expect(err).To(gomega.HaveOccurred())
		expectNoPower(f)

		f, result = retainedObservationJob("legacy-apply-clamp", nil)
		plan, err := f.s.planEvidenceRecovery(f.spec.ID, "recollect")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.Schema).To(gomega.Equal("haepari.recovery-plan/v1"))
		gomega.Expect(plan.Budget).To(gomega.BeNil())
		installObservationClock(f, 6*time.Second)
		request := managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "legacy-clamp-apply"}
		w := recoveryHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", request)
		gomega.Expect(w.Code).NotTo(gomega.Equal(200), w.Body.String())
		intent, _ := task027SavedRecovery(f.s, f.spec.ID, request)
		gomega.Expect(intent.Schema).To(gomega.Equal("haepari.evidence-recovery-intent/v1"))
		gomega.Expect(intent.Budget).To(gomega.BeNil())
		gomega.Expect(intent.Jobs[0].CollectDeadline).To(gomega.Equal(intent.RecordedAt.Add(5 * time.Second)))
		gomega.Expect(intent.Jobs[0].CleanupDeadline).To(gomega.Equal(intent.Jobs[0].CollectDeadline.Add(5 * time.Second)))
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, result.JobID, "workspace/results/output.txt"))).To(gomega.Equal([]byte("retained")))
		expectNoPower(f)
	})

	ginkgo.It("replays a stored legacy receipt without recomputing its deadlines", func() {
		f, result := retainedObservationJob("legacy-replay", nil)
		plan, err := f.s.planEvidenceRecovery(f.spec.ID, "dispose")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.Budget).To(gomega.BeNil())
		request := managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "legacy-replay-apply"}
		w := recoveryHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", request)
		gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
		intent, receipt := task027SavedRecovery(f.s, f.spec.ID, request)
		gomega.Expect(intent.Jobs[0].CollectDeadline).To(gomega.Equal(intent.RecordedAt.Add(5 * time.Second)))
		gomega.Expect(intent.Jobs[0].CleanupDeadline).To(gomega.Equal(intent.Jobs[0].CollectDeadline.Add(5 * time.Second)))
		gomega.Expect(receipt.Complete).To(gomega.BeTrue())
		replay := recoveryHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", request)
		gomega.Expect(replay.Code).To(gomega.Equal(200), replay.Body.String())
		again, againReceipt := task027SavedRecovery(f.s, f.spec.ID, request)
		gomega.Expect(again).To(gomega.Equal(intent))
		gomega.Expect(againReceipt).To(gomega.Equal(receipt))
		current, err := f.s.ledger.Result(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current).To(gomega.Equal(result))
		expectNoPower(f)
	})

	ginkgo.It("rejects a bad pin, lease MAC, and boot before power", func() {
		f, _ := retainedObservationJob("observation-fences", nil)
		pin := strings.SplitN(f.spec.Guest.KnownHostsReference, ":", 3)
		gomega.Expect(pin).To(gomega.HaveLen(3))
		original, err := os.ReadFile(pin[2])
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.WriteFile(pin[2], []byte("bad pin\n"), 0600)).To(gomega.Succeed())
		_, err = f.s.planEvidenceRecovery(f.spec.ID, "dispose")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(os.WriteFile(pin[2], original, 0600)).To(gomega.Succeed())
		snap, err := f.s.ledger.ResourceRecoverySnapshot(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(snap.EvidenceIntent).To(gomega.BeNil())

		savedLeases := f.s.vmLeases
		f.s.vmLeases = func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(fmt.Sprintf("{\nip_address=192.168.64.3\nhw_address=1,%s\nlease=%d\n}\n", "aa:bb:cc:dd:ee:10", time.Now().Add(time.Hour).Unix()))), nil
		}
		_, err = f.s.planEvidenceRecovery(f.spec.ID, "dispose")
		gomega.Expect(err).To(gomega.HaveOccurred())
		f.s.vmLeases = savedLeases

		base := f.s.sshCommand
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			result, callErr := base(ctx, exe, args, input)
			if vmTransportRequest(input).Operation == "identity" {
				result.Stdout = bootIDPattern.ReplaceAllString(result.Stdout, `"boot_id":"not-a-boot"`)
			}
			return result, callErr
		}
		_, err = f.s.planEvidenceRecovery(f.spec.ID, "dispose")
		gomega.Expect(err).To(gomega.HaveOccurred())
		expectNoPower(f)
	})

	ginkgo.It("does not open another ssh when resource collect is uncertain at the saved deadline", func() {
		f, result := retainedObservationJob("collect-uncertain", nil)
		q, err := f.s.vmBinding(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		backend := vmEvidenceBackend{f.s, f.spec.ID}
		root, err := backend.Observe(q.Request.Identity, "recollect", time.Now().Add(recoveryObservationBudget))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		base := f.s.sshCommand
		var mu sync.Mutex
		var operations []string
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			operation := vmTransportRequest(input).Operation
			mu.Lock()
			operations = append(operations, operation)
			mu.Unlock()
			if operation == "file" {
				<-ctx.Done()
				return utm.CommandResult{}, ctx.Err()
			}
			return base(ctx, exe, args, input)
		}
		collected := backend.Collect(managed.EvidenceRecoveryJob{
			JobID:           result.JobID,
			Identity:        q.Request.Identity,
			Root:            root,
			Outputs:         f.job.Workload.RequiredOutputs,
			CollectDeadline: time.Now().Add(2 * time.Second),
		}, filepath.Join(ginkgo.GinkgoT().TempDir(), "supplement"))
		gomega.Expect(collected.unproven).To(gomega.BeTrue())
		gomega.Expect(operations).To(gomega.ContainElement("file"))
		gomega.Expect(operations).NotTo(gomega.ContainElement("state"))
		expectNoPower(f)
	})

	ginkgo.It("clamps actual recollection observation to fifteen seconds inside a saved ninety second phase", func() {
		f, original := retainedObservationJob("effect-ninety", task027ResourceBudgets)
		f.s.vmWrite = nil
		plan, err := f.s.planEvidenceRecovery(f.spec.ID, "recollect")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		clock := installObservationClock(f, 6*time.Second)
		// Revalidation and effect observation each use two hops. Collection
		// and cleanup retain their phase budgets without this injected delay.
		clock.limitSleeps(4)
		request := managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "effect-ninety-apply"}
		w := recoveryHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", request)
		gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
		intent, receipt := task027SavedRecovery(f.s, f.spec.ID, request)
		gomega.Expect(intent.Jobs[0].CollectDeadline).To(gomega.Equal(intent.RecordedAt.Add(90 * time.Second)))
		gomega.Expect(receipt.Complete).To(gomega.BeTrue())
		gomega.Expect(receipt.Jobs[0].Stage).To(gomega.Equal("resolved"))
		var observed []guestExchange
		for _, row := range clock.exchanges() {
			if !row.At.Before(intent.RecordedAt) && (row.Operation == "state" || row.Operation == "root") {
				observed = append(observed, row)
			}
		}
		gomega.Expect(observed).To(gomega.HaveLen(2))
		gomega.Expect(observed[0].Deadline).To(gomega.Equal(observed[1].Deadline))
		gomega.Expect(observed[0].Deadline).To(gomega.BeTemporally("~", intent.RecordedAt.Add(recoveryObservationBudget), 2*time.Second))
		gomega.Expect(intent.Jobs[0].CollectDeadline.Sub(observed[0].Deadline)).To(gomega.BeNumerically(">", 60*time.Second))
		gomega.Expect(observed[1].At.Sub(observed[0].At)).To(gomega.BeNumerically(">", 5*time.Second))
		current, err := f.s.ledger.Result(original.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current).To(gomega.Equal(original))
		expectNoPower(f)
	})

	ginkgo.It("uses four remaining seconds in actual recovery and opens no exchange after saved expiry", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), observationScript)
		f.job.Deadlines.CollectSeconds, f.job.Deadlines.CleanupSeconds = 4, 2
		f.s.vmWrite = func(*os.File, []byte) (int, error) { return 0, unix.ENOSPC }
		original := f.run("effect-four")
		plan, err := f.s.planEvidenceRecovery(f.spec.ID, "recollect")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		clock := installObservationClock(f, 6*time.Second)
		base := f.s.utmCommand
		var mu sync.Mutex
		var lists []guestExchange
		f.s.utmCommand = func(ctx context.Context, exe string, args ...string) (utm.CommandResult, error) {
			if len(args) > 0 && args[0] == "list" {
				deadline, ok := ctx.Deadline()
				if ok {
					mu.Lock()
					lists = append(lists, guestExchange{At: time.Now(), Deadline: deadline})
					mu.Unlock()
				}
			}
			return base(ctx, exe, args...)
		}
		request := managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "effect-four-apply"}
		w := recoveryHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", request)
		gomega.Expect(w.Code).NotTo(gomega.Equal(200), w.Body.String())
		intent, _ := task027SavedRecovery(f.s, f.spec.ID, request)
		gomega.Expect(intent.Jobs[0].CollectDeadline).To(gomega.Equal(intent.RecordedAt.Add(4 * time.Second)))
		gomega.Expect(intent.Jobs[0].CleanupDeadline).To(gomega.Equal(intent.RecordedAt.Add(6 * time.Second)))
		var effectLists []guestExchange
		for _, row := range lists {
			if !row.At.Before(intent.RecordedAt) {
				effectLists = append(effectLists, row)
			}
		}
		gomega.Expect(effectLists).To(gomega.HaveLen(1))
		gomega.Expect(effectLists[0].Deadline).To(gomega.BeTemporally("==", intent.Jobs[0].CollectDeadline))
		for _, row := range clock.exchanges() {
			gomega.Expect(row.At.Before(intent.RecordedAt)).To(gomega.BeTrue(), "effect opened SSH past its four-second pre-identity budget")
		}
		time.Sleep(max(0, time.Until(intent.Jobs[0].CleanupDeadline)) + 10*time.Millisecond)
		beforeLists, beforeSSH := len(lists), len(clock.exchanges())
		// Exercise expiry settlement itself using the same saved intent. The
		// public owner-stop guard is not bypassed by a production caller.
		gomega.Expect(f.s.executeEvidenceRecovery(intent)).To(gomega.Succeed())
		gomega.Expect(lists).To(gomega.HaveLen(beforeLists))
		gomega.Expect(clock.exchanges()).To(gomega.HaveLen(beforeSSH))
		replayed, receipt := task027SavedRecovery(f.s, f.spec.ID, request)
		gomega.Expect(replayed).To(gomega.Equal(intent))
		gomega.Expect(receipt.Complete).To(gomega.BeTrue())
		gomega.Expect(receipt.Jobs[0].Stage).To(gomega.Equal("failed"))
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, original.JobID, "workspace/results/output.txt"))).To(gomega.Equal([]byte("retained")))
		current, err := f.s.ledger.Result(original.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current).To(gomega.Equal(original))
		expectNoPower(f)
	})
})
