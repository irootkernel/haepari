//go:build integration

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task019ManualPublicRecoveryReplay", ginkgo.Label("integration"), func() {
	for _, backend := range []string{"local", "utm-macos"} {
		backend := backend
		ginkgo.It("retains the public recovery receipt for "+backend+" after completion and controller replacement", func() {
			t := ginkgo.GinkgoT()
			var old *runtimeServer
			var r managed.Receipt
			var job managed.JobSpec
			var object string
			var run func() error
			marker := filepath.Join(t.TempDir(), "preparation-count")
			expectedMarker := "x"
			if backend == "local" {
				_, old, r, job, object = cancellationRuntime(t, "mkdir -p ../results; cat input.txt > ../results/output.txt; printf x >> '"+marker+"'", func(j *managed.JobSpec) {
					j.Mode = "manual"
					j.Manual = &managed.Manual{ScenarioDocument: "source/input.txt", RequiredScenarios: []string{"check"}}
					j.Deadlines.RunSeconds = 0
					j.Deadlines.ManualSeconds = 30
				})
				run = func() error { return old.executeLocal(r, job, object, time.Now().Add(5*time.Second)) }
			} else {
				f := nativeVMFixture(t, "mkdir -p ../results; cat input.txt > ../results/output.txt")
				manualGuestFixture(f, marker)
				expectedMarker = "setupsmoke"
				var err error
				r, err = f.reserve(f.admit("manual-public-recovery"))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				old, job, object = f.s, f.job, f.object
				run = func() error { return old.executeVM(r, job, object, time.Now().Add(10*time.Second)) }
			}
			done := make(chan error, 1)
			go func() { defer ginkgo.GinkgoRecover(); done <- run() }()
			gomega.Eventually(func() string { v, _ := old.ledger.Snapshot(r.JobID); return v.JobState }, 10*time.Second, 20*time.Millisecond).Should(gomega.Equal("awaiting_manual"))
			old.detachWorkers()
			gomega.Eventually(done, 5*time.Second).Should(gomega.Receive(gomega.MatchError(errControllerDetached)))
			old.workers.Wait()
			before, err := old.ledger.ManualSession(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(old.ledger.MarkInterrupted()).To(gomega.Succeed())
			fresh, err := newRuntime(old.root, old.ledger, old.store)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			fresh.sshCommand, fresh.utmCommand, fresh.vmLeases = old.sshCommand, old.utmCommand, old.vmLeases
			t.Cleanup(func() { fresh.detachWorkers(); fresh.workers.Wait() })
			path := "/v1/jobs/" + r.JobID + "/recover"
			w := recoveryHTTP(fresh, path, managed.RecoveryRequest{Plan: true})
			gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
			var plan jobRecoveryPlan
			recoveryData(w, &plan)
			gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty(), "%+v", plan.Unknowns)
			q := managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "manual-public-replay"}
			w = recoveryHTTP(fresh, path, q)
			gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
			var original managed.Receipt
			recoveryData(w, &original)
			ready, err := fresh.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(ready.JobState).To(gomega.Equal("awaiting_manual"))
			gomega.Expect(ready.ResourceOccupancy).To(gomega.Equal(1))
			after, err := fresh.ledger.ManualSession(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(after.Identity).To(gomega.Equal(before.Identity))
			gomega.Expect(after.PrepareDeadline).To(gomega.Equal(before.PrepareDeadline))
			gomega.Expect(after.Deadline).To(gomega.Equal(before.Deadline))
			manualGuestComplete(fresh, job, ready, "pass")
			fresh.workers.Wait()
			result, err := fresh.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
			gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
			db, err := sqlite.Open(fresh.root.DBPath)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer db.Close()
			counts := func() [3]int {
				var n [3]int
				for i, table := range []string{"managed_mutations", "managed_events", "managed_runs"} {
					gomega.Expect(db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n[i])).To(gomega.Succeed())
				}
				return n
			}
			beforeReplay := counts()
			var intents, runs int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='recovery_intent' AND json_extract(payload_json,'$.mutation_key')=?`, r.JobID, "recovery-"+q.MutationKey).Scan(&intents)).To(gomega.Succeed())
			gomega.Expect(intents).To(gomega.Equal(1))
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_runs WHERE job_id=?`, r.JobID).Scan(&runs)).To(gomega.Succeed())
			gomega.Expect(runs).To(gomega.Equal(1))
			rotated, err := newRuntime(fresh.root, fresh.ledger, fresh.store)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(rotated.recoveryKey).NotTo(gomega.Equal(fresh.recoveryKey))
			w = recoveryHTTP(rotated, path, q)
			gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
			var replay managed.Receipt
			recoveryData(w, &replay)
			gomega.Expect(replay).To(gomega.Equal(original))
			q.PlanToken = "changed-token"
			w = recoveryHTTP(rotated, path, q)
			gomega.Expect(w.Code).NotTo(gomega.Equal(200), w.Body.String())
			gomega.Expect(w.Body.String()).To(gomega.ContainSubstring("CONFLICT"))
			gomega.Expect(counts()).To(gomega.Equal(beforeReplay))
			gomega.Expect(os.ReadFile(marker)).To(gomega.Equal([]byte(expectedMarker)))
		})
	}
})

var _ = ginkgo.Describe("Task019ManualRemotePreparationRecovery", ginkgo.Label("integration"), func() {
	for _, proof := range []string{"alive", "stopped", "unknown"} {
		proof := proof
		ginkgo.It("uses guest-owned "+proof+" liveness without a host PID interpretation", func() {
			f := nativeVMFixture(ginkgo.GinkgoT(), "true")
			manualGuestFixture(f, filepath.Join(f.guest, "not-executed"))
			f.job.Deadlines.PrepareSeconds = 30
			native := f.s.sshCommand
			var starts, prepares atomic.Int32
			f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
				switch vmTransportRequest(input).Operation {
				case "prepare":
					prepares.Add(1)
				case "start":
					starts.Add(1)
					return utm.CommandResult{}, utm.ErrGuestUncertain
				}
				return native(ctx, exe, args, input)
			}
			r, err := f.reserve(f.admit("manual-remote-preparation"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(f.s.executeVM(r, f.job, f.object, time.Now().Add(30*time.Second))).To(gomega.HaveOccurred())
			f.s.workers.Wait()
			before, err := f.s.ledger.ManualSession(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(before.Deadline.IsZero()).To(gomega.BeTrue())
			// Model the exited original controller in isolated saved fixture state.
			// No host/VM reboot or native guest process is claimed by this fixture.
			db, err := sqlite.Open(f.s.root.DBPath)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer db.Close()
			var cursor int64
			var raw []byte
			gomega.Expect(db.QueryRow(`SELECT cursor,payload_json FROM managed_events WHERE job_id=? AND kind='phase_intent'`, r.JobID).Scan(&cursor, &raw)).To(gomega.Succeed())
			var phase managed.LocalPhase
			gomega.Expect(json.Unmarshal(raw, &phase)).To(gomega.Succeed())
			phase.ControllerOwner.BootID = "prior-controller-fixture-boot"
			raw, err = json.Marshal(phase)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = db.Exec(`UPDATE managed_events SET payload_json=? WHERE cursor=?`, raw, cursor)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(f.s.ledger.MarkInterrupted()).To(gomega.Succeed())
			fresh, err := newRuntime(f.s.root, f.s.ledger, f.s.store)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer func() { fresh.detachWorkers(); fresh.workers.Wait() }()
			fresh.utmCommand, fresh.vmLeases = f.s.utmCommand, f.s.vmLeases
			guestPID := 2147483000
			_, err = platform.ObserveProcess(guestPID)
			gomega.Expect(err).To(gomega.HaveOccurred())
			fresh.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
				q := vmTransportRequest(input)
				if q.Operation == "prepare" || q.Operation == "start" {
					return f.s.sshCommand(ctx, exe, args, input)
				}
				reply, err := native(ctx, exe, args, input)
				if err != nil || (q.Operation != "inspect" && q.Operation != "watch") {
					return reply, err
				}
				return manualGuestReplyFault(reply, func(view *runner.GuestReply) {
					view.Observation = &runner.Observation{State: "spawning", Intent: &runner.Intent{Schema: managed.RunnerSchema("intent"), Binding: q.Request.Binding(), Supervisor: platform.ProcessIdentity{BootID: q.Request.BootID, PID: guestPID, PGID: guestPID, SID: guestPID, UID: uint32(os.Geteuid()), StartedSeconds: 1}, RecordedAt: phase.StartedAt}}
					view.SupervisorAlive, view.OwnersLive, view.Reusable = nil, nil, false
					if proof != "unknown" {
						live := proof == "alive"
						view.SupervisorAlive, view.OwnersLive = &live, &live
					}
				})
			}
			original, err := fresh.ledger.RecoverySnapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			err = fresh.tryRecoverInterrupted(r.JobID, true, nil)
			if proof == "alive" {
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			} else {
				gomega.Expect(errors.Is(err, managed.ErrBlocked)).To(gomega.BeTrue(), "%v", err)
				unchanged, err := fresh.ledger.RecoverySnapshot(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(unchanged).To(gomega.Equal(original))
			}
			after, err := fresh.ledger.ManualSession(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(after.Identity).To(gomega.Equal(before.Identity))
			gomega.Expect(after.PrepareDeadline).To(gomega.Equal(before.PrepareDeadline))
			gomega.Expect(after.Deadline.IsZero()).To(gomega.BeTrue())
			current, err := fresh.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			state := "reconciling"
			if proof == "alive" {
				state = "preparing"
			}
			gomega.Expect(current.JobState).To(gomega.Equal(state))
			gomega.Expect(current.ResourceOccupancy).To(gomega.Equal(1))
			gomega.Expect(prepares.Load()).To(gomega.Equal(int32(1)))
			gomega.Expect(starts.Load()).To(gomega.Equal(int32(1)))
			gomega.Expect(f.control.effects).To(gomega.BeEmpty())
		})
	}
})
