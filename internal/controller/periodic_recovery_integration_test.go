//go:build integration

package controller

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func periodicRequest(input []byte) runner.GuestRequest {
	r := tar.NewReader(bytes.NewReader(input))
	for {
		h, err := r.Next()
		if err != nil {
			return runner.GuestRequest{}
		}
		if h.Name == "request.json" {
			var q runner.GuestRequest
			_ = json.NewDecoder(r).Decode(&q)
			return q
		}
	}
}

var _ = ginkgo.Describe("Task036PeriodicRecovery", ginkgo.Label("integration"), func() {
	ginkgo.It("retires an undelivered start with verified cleanup but preserves quarantine without a native exit", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf forbidden > ../results/output.txt")
		f.job.Schema = "haepari.job/v2"
		original := f.s.sshCommand
		var starts, retirements atomic.Int32
		var withheld runner.GuestRequest
		f.s.sshCommand = func(ctx context.Context, executable string, args []string, input []byte) (utm.CommandResult, error) {
			q := periodicRequest(input)
			if q.Operation == "start" {
				starts.Add(1)
				withheld = q
				// The request never reaches GuestCommand, so no supervisor or
				// workload executes. Recovery must retire this prepared identity.
				return utm.CommandResult{}, utm.ErrGuestUncertain
			}
			if q.Operation == "retire" {
				retirements.Add(1)
			}
			return original(ctx, executable, args, input)
		}
		r, err := f.reserve(f.admit("periodic-undelivered-start"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f.s.execute(context.Background(), r, f.job, f.object, time.Now().Add(10*time.Second))
		interrupted, err := f.s.ledger.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(interrupted.Receipt.JobState).To(gomega.Equal("reconciling"))
		gomega.Expect(interrupted.Resource.Occupancy).To(gomega.Equal(1))
		gomega.Expect(interrupted.Resource.Availability).To(gomega.Equal("quarantined"))
		gomega.Expect(withheld.Request.Identity).To(gomega.Equal(interrupted.Identity))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		gomega.Expect(f.s.startWorker(func() { defer close(done); f.s.reconcilePeriodically(ctx) })).To(gomega.BeTrue())
		defer func() { cancel(); <-done }()
		// Observe settlement within the full original run/collection/cleanup
		// lifetime; this waiter grants no new phase or guest-effect budget.
		gomega.Eventually(func() string {
			job, e := f.s.ledger.Snapshot(r.JobID)
			if e != nil {
				return ""
			}
			return job.JobState
		}, time.Duration(f.job.Deadlines.RunSeconds+f.job.Deadlines.CollectSeconds+f.job.Deadlines.CleanupSeconds+5)*time.Second, 25*time.Millisecond).Should(gomega.Equal("finished"))
		cancel()
		<-done
		result, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(result.ProtectedEvidence).To(gomega.BeEmpty())
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
		gomega.Expect(result.NativeExitCode).To(gomega.BeNil())
		gomega.Expect(result.NativeSignal).To(gomega.BeEmpty())
		identity, err := f.s.ledger.LocalIdentity(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(identity).To(gomega.Equal(interrupted.Identity))
		phases, err := f.s.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["run"].Deadline).To(gomega.Equal(interrupted.Phases["run"].Deadline))
		db, err := sqlite.Open(f.s.root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		var requestRaw []byte
		gomega.Expect(db.QueryRow(`SELECT payload_json FROM managed_runner_receipts WHERE job_id=? AND kind='request'`, r.JobID).Scan(&requestRaw)).To(gomega.Succeed())
		var retained runner.Request
		gomega.Expect(json.Unmarshal(requestRaw, &retained)).To(gomega.Succeed())
		gomega.Expect(retained.Identity).To(gomega.Equal(withheld.Request.Identity))
		gomega.Expect(retained.Deadline).To(gomega.Equal(withheld.Request.Deadline))
		var nativeReceipts int
		gomega.Expect(db.QueryRow(`SELECT count(*) FROM managed_runner_receipts WHERE job_id=? AND kind IN ('start','exit')`, r.JobID).Scan(&nativeReceipts)).To(gomega.Succeed())
		gomega.Expect(nativeReceipts).To(gomega.BeZero())
		outcome, err := f.s.ledger.LocalOutcome(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(outcome.Started).To(gomega.BeFalse())
		gomega.Expect(starts.Load()).To(gomega.Equal(int32(1)))
		gomega.Expect(retirements.Load()).To(gomega.Equal(int32(1)))
		_, err = os.Stat(filepath.Join(f.guest, r.JobID))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		status, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Occupancy).To(gomega.BeZero())
		gomega.Expect(status.Availability).To(gomega.Equal("quarantined"))
		gomega.Expect(status.AvailabilityRevision).To(gomega.Equal(interrupted.Resource.AvailabilityRevision))
		settled, err := f.s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.s.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.MatchError(managed.ErrBlocked))
		unchanged, err := f.s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(unchanged).To(gomega.Equal(settled))
		events, err := f.s.ledger.EventsAfter(r.JobID, 0, 1000)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		finished, provenance := 0, 0
		resolved := map[string]managed.PhaseResolutionRequest{}
		for _, event := range events.Events {
			if event.Kind == "finished" {
				finished++
			}
			if event.Kind == "live_reconciliation" {
				var proof managed.LiveReconciliation
				gomega.Expect(json.Unmarshal(event.Payload, &proof)).To(gomega.Succeed())
				gomega.Expect(proof.PriorResource.Availability).To(gomega.Equal("available"))
				gomega.Expect(proof.Identity).To(gomega.Equal(identity))
				gomega.Expect(proof.QuarantineRevision).To(gomega.Equal(status.AvailabilityRevision))
				provenance++
			}
			if event.Kind == "phase_reconciled" {
				var receipt managed.PhaseResolutionRequest
				gomega.Expect(json.Unmarshal(event.Payload, &receipt)).To(gomega.Succeed())
				resolved[receipt.Record.Phase] = receipt
			}
			gomega.Expect(event.Kind).NotTo(gomega.Equal("live_availability_restored"))
		}
		gomega.Expect(finished).To(gomega.Equal(1))
		gomega.Expect(provenance).To(gomega.Equal(1))
		// LocalPhases retains the original phase intents. Settlement and live
		// availability restoration consume the separate compensating receipts;
		// verify those observed records without rewriting the original phases.
		for _, name := range []string{"collect", "cleanup", "verify"} {
			gomega.Expect(resolved).To(gomega.HaveKey(name))
			receipt := resolved[name]
			gomega.Expect(receipt.OriginalDigest).To(gomega.Equal(managed.RecoveryDigest(phases[name])))
			gomega.Expect(receipt.Record.Identity).To(gomega.Equal(identity))
			gomega.Expect(receipt.Record.Deadline).To(gomega.Equal(phases[name].Deadline))
			gomega.Expect(receipt.Record.Completed).To(gomega.BeTrue(), name)
			gomega.Expect(receipt.Record.Outcome).NotTo(gomega.Equal("unproven"), name)
			if name != "collect" {
				gomega.Expect(receipt.Record.Outcome).To(gomega.Equal("complete"), name)
			}
		}
	})
	for _, mode := range []string{"owned-available", "operator-paused", "changed-quarantine-revision"} {
		mode := mode
		ginkgo.It("recovers the original lost-start reply with "+mode+" availability", func() {
			f := nativeVMFixture(ginkgo.GinkgoT(), "sleep 0.2; mkdir -p ../results; printf original > ../results/output.txt")
			f.job.Schema = "haepari.job/v2"
			original := f.s.sshCommand
			var starts atomic.Int32
			f.s.sshCommand = func(ctx context.Context, executable string, args []string, input []byte) (utm.CommandResult, error) {
				q := periodicRequest(input)
				reply, err := original(ctx, executable, args, input)
				if q.Operation == "start" && starts.Add(1) == 1 && err == nil {
					if mode == "operator-paused" {
						status, e := f.s.ledger.ResourceStatus(f.spec.ID)
						if e != nil {
							return reply, e
						}
						_, e = f.s.ledger.SetAvailability(managed.AvailabilityRequest{ResourceID: f.spec.ID, ExpectedConfigRevision: status.ConfigRevision, ExpectedAvailabilityRevision: status.AvailabilityRevision, ExpectedLeaseGeneration: status.LeaseGeneration, MutationKey: "operator-maintenance", Reason: "maintenance"}, false)
						if e != nil {
							return reply, e
						}
					}
					return utm.CommandResult{}, errors.New("lost native start reply")
				}
				return reply, err
			}
			r, err := f.reserve(f.admit("periodic-original"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			f.s.execute(context.Background(), r, f.job, f.object, time.Now().Add(10*time.Second))
			interrupted, err := f.s.ledger.RecoverySnapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(interrupted.Receipt.JobState).To(gomega.Equal("reconciling"))
			gomega.Expect(interrupted.Resource.Occupancy).To(gomega.Equal(1))
			gomega.Expect(interrupted.Resource.Availability).To(gomega.Equal("quarantined"))
			if mode == "changed-quarantine-revision" {
				db, e := sqlite.Open(f.s.root.DBPath)
				gomega.Expect(e).NotTo(gomega.HaveOccurred())
				// Controlled intervening operator revision: matching reason text
				// alone cannot give the failed observer ownership of this change.
				_, e = db.Exec(`UPDATE managed_resources SET availability_revision=availability_revision+1 WHERE id=?`, f.spec.ID)
				gomega.Expect(e).NotTo(gomega.HaveOccurred())
				gomega.Expect(db.Close()).To(gomega.Succeed())
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			gomega.Expect(f.s.startWorker(func() { defer close(done); f.s.reconcilePeriodically(ctx) })).To(gomega.BeTrue())
			defer func() { cancel(); <-done }()
			// Observe the complete original run/collection/cleanup lifetime. This
			// waiter changes no stored phase budget or recovery authority.
			gomega.Eventually(func() string {
				job, e := f.s.ledger.Snapshot(r.JobID)
				if e != nil {
					return ""
				}
				return job.JobState
			}, time.Duration(f.job.Deadlines.RunSeconds+f.job.Deadlines.CollectSeconds+f.job.Deadlines.CleanupSeconds+5)*time.Second, 25*time.Millisecond).Should(gomega.Equal("finished"))
			cancel()
			<-done
			result, err := f.s.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
			gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
			identity, err := f.s.ledger.LocalIdentity(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(identity).To(gomega.Equal(interrupted.Identity))
			phase, err := f.s.ledger.LocalPhases(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(phase["run"].Deadline).To(gomega.Equal(interrupted.Phases["run"].Deadline))
			gomega.Expect(starts.Load()).To(gomega.Equal(int32(1)))
			status, err := f.s.ledger.ResourceStatus(f.spec.ID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(status.Occupancy).To(gomega.BeZero())
			events, err := f.s.ledger.EventsAfter(r.JobID, 0, 1000)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			restored := 0
			for _, event := range events.Events {
				if event.Kind != "live_availability_restored" {
					continue
				}
				restored++
				var proof struct {
					Schema             string `json:"schema"`
					ResourceID         string `json:"resource_id"`
					QuarantineRevision int64  `json:"quarantine_revision"`
				}
				gomega.Expect(json.Unmarshal(event.Payload, &proof)).To(gomega.Succeed())
				gomega.Expect(proof.Schema).To(gomega.Equal("haepari.availability-restoration/v2"))
				gomega.Expect(proof.ResourceID).To(gomega.Equal(f.spec.ID))
				gomega.Expect(proof.QuarantineRevision).To(gomega.Equal(interrupted.Resource.AvailabilityRevision))
			}
			if mode == "owned-available" {
				gomega.Expect(restored).To(gomega.Equal(1))
				gomega.Expect(status.Availability).To(gomega.Equal("available"))
				gomega.Expect(result.ResourceAvailability).To(gomega.Equal("available"))
				gomega.Expect(f.run("periodic-successor").FinalVerdict).To(gomega.Equal(managed.Pass))
				gomega.Expect(starts.Load()).To(gomega.Equal(int32(2)))
			} else {
				gomega.Expect(restored).To(gomega.BeZero())
				gomega.Expect(status.Availability).To(gomega.Equal("quarantined"))
				gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
			}
		})
	}
	ginkgo.It("caps a safe retry burst at three calls and stops on native refusal or identity conflicts", func() {
		for _, failure := range []error{utm.ErrGuestUncertain, utm.ErrGuestRefused, managed.ErrStale, runner.ErrConflict, os.ErrPermission} {
			calls := 0
			q := runner.GuestRequest{Operation: "watch", Deadline: time.Now().Add(5 * time.Second)}
			_, err := retryObservation(q, nil, func(actual runner.GuestRequest) (runner.GuestReply, error) {
				calls++
				gomega.Expect(actual).To(gomega.Equal(q))
				return runner.GuestReply{}, failure
			}, nil)
			gomega.Expect(errors.Is(err, failure)).To(gomega.BeTrue())
			if failure == utm.ErrGuestUncertain {
				gomega.Expect(calls).To(gomega.Equal(3))
			} else {
				gomega.Expect(calls).To(gomega.Equal(1))
			}
		}
	})
})
