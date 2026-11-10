//go:build integration

package controller

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

// Model original host/guest receipts from a prior boot in isolated test state.
// The bootstrap still observes the real current boot; no real VM was rebooted.
func previousBootVMFixture() (*vmJobFixture, managed.Receipt) {
	return previousBootVMFixtureWithResult(false)
}

func previousBootVMFixtureWithResult(finishedRetained bool) (*vmJobFixture, managed.Receipt) {
	return previousBootVMFixtureWithOutcome(finishedRetained, false, false, "")
}

func previousBootVMFixtureWithOutcome(finishedRetained, retainOutcome, failed bool, schema string) (*vmJobFixture, managed.Receipt) {
	return previousBootVMFixtureWithOutputs(finishedRetained, retainOutcome, failed, schema, nil)
}

func previousBootVMFixtureWithOutputs(finishedRetained, retainOutcome, failed bool, schema string, outputs []string) (*vmJobFixture, managed.Receipt) {
	f := attestedVMFixture()
	if outputs != nil {
		f.job.Workload.RequiredOutputs = outputs
	}
	if schema != "" {
		f.job.Schema = schema
	}
	if failed {
		f.job.Workload.Argv[len(f.job.Workload.Argv)-1] += "; exit 7"
	}
	native := f.s.sshCommand
	var r managed.Receipt
	var err error
	if finishedRetained {
		// Resource attestation requires an already finished retained Job. Settle
		// it through the ordinary native collection failure before the boot change.
		f.s.vmWrite = func(*os.File, []byte) (int, error) { return 0, unix.ENOSPC }
		original := f.run("previous-boot-original")
		r, err = f.s.ledger.Snapshot(original.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f.s.vmWrite = nil
	} else {
		successfulStarts := 0
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			result, err := native(ctx, exe, args, input)
			q := vmTransportRequest(input)
			if q.Operation == "start" && err == nil && result.ExitCode == 0 && ctx.Err() == nil && time.Now().Before(q.Deadline) {
				envelope, decodeErr := managed.DecodeEnvelope([]byte(result.Stdout))
				if decodeErr == nil && envelope.OK && envelope.Operation == "runner.guest" {
					successfulStarts++
					if successfulStarts == 1 {
						return utm.CommandResult{}, errors.New("fixture lost original observer")
					}
				}
			}
			return result, err
		}
		r, err = f.reserve(f.admit("previous-boot-original"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		executeErr := f.s.executeVM(r, f.job, f.object, time.Now().Add(time.Duration(f.job.Deadlines.PrepareSeconds)*time.Second))
		gomega.Expect(successfulStarts).To(gomega.Equal(1), "previous-boot setup did not lose exactly one successful native start reply: %v", executeErr)
		// SSH classifies a lost command reply instead of retaining our injected
		// error identity. The counter and original receipts prove which reply.
		gomega.Expect(errors.Is(executeErr, utm.ErrGuestUncertain)).To(gomega.BeTrue(), "%v", executeErr)
		gomega.Expect(errors.Is(executeErr, utm.ErrUnavailable)).To(gomega.BeTrue(), "%v", executeErr)
	}
	// Wait within the original native run/termination bounds before seeding
	// historical bytes. The shared helper's short receipt poll is not the
	// admitted lifetime of this native process in a complete race suite.
	requestPath := filepath.Join(f.guest, r.JobID, "request.json")
	nativeRequest, e := runner.ReadRequest(requestPath)
	gomega.Expect(e).NotTo(gomega.HaveOccurred())
	expectedIdentity, e := f.s.ledger.LocalIdentity(r.JobID)
	gomega.Expect(e).NotTo(gomega.HaveOccurred())
	gomega.Expect(nativeRequest.Identity).To(gomega.Equal(expectedIdentity))
	wait := time.Until(nativeRequest.Deadline) + time.Duration(nativeRequest.TerminationSeconds)*time.Second
	if wait < time.Second {
		wait = time.Second
	}
	gomega.Eventually(func() bool {
		view, err := runner.Inspect(requestPath)
		if err != nil || view.Exit == nil || view.Intent == nil {
			return false
		}
		owner, err := platform.ObserveProcess(view.Intent.Supervisor.PID)
		return errors.Is(err, unix.ESRCH) || err == nil && owner.Identity != view.Intent.Supervisor
	}, wait, 25*time.Millisecond).Should(gomega.BeTrue())
	falseVMExit(f, r.JobID)
	f.s.sshCommand = native
	guest, err := f.s.vmBinding(r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	old := "previous-controller-fixture-boot"
	guest.BootID, guest.Request.BootID = old, old
	write := func(path string, value any) {
		raw, err := json.Marshal(value)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.WriteFile(path, raw, 0600)).To(gomega.Succeed())
	}
	read := func(path string, value any) {
		raw, err := os.ReadFile(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(json.Unmarshal(raw, value)).To(gomega.Succeed())
	}
	root := filepath.Join(f.guest, r.JobID)
	var original runner.GuestRequest
	read(filepath.Join(root, "guest.json"), &original)
	original.BootID, original.Request.BootID = old, old
	write(filepath.Join(root, "guest.json"), original)
	write(filepath.Join(root, "request.json"), guest.Request)
	var control runner.GuestControl
	controlPath := filepath.Join(f.guest, ".haepari-control-"+r.JobID+".json")
	read(controlPath, &control)
	control.Request.BootID, control.Request.Request.BootID, control.NativeRequest.BootID, control.Owner.BootID = old, old, old, old
	write(controlPath, control)
	var intent runner.Intent
	read(filepath.Join(root, "intent.json"), &intent)
	intent.Binding, intent.Supervisor.BootID = guest.Request.Binding(), old
	write(filepath.Join(root, "intent.json"), intent)
	var start runner.Start
	read(filepath.Join(root, "start.json"), &start)
	start.Binding, start.Process.BootID = guest.Request.Binding(), old
	write(filepath.Join(root, "start.json"), start)
	if retainOutcome {
		var outcome runner.Outcome
		read(filepath.Join(root, "outcome.json"), &outcome)
		outcome.Binding = guest.Request.Binding()
		write(filepath.Join(root, "outcome.json"), outcome)
		var exit runner.Exit
		read(filepath.Join(root, "exit.json"), &exit)
		exit.Binding = guest.Request.Binding()
		write(filepath.Join(root, "exit.json"), exit)
	} else {
		gomega.Expect(os.Remove(filepath.Join(root, "outcome.json"))).To(gomega.Succeed())
		gomega.Expect(os.Remove(filepath.Join(root, "exit.json"))).To(gomega.Succeed())
	}
	db, err := sqlite.Open(f.s.root.DBPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer db.Close()
	plan, prepared, err := f.s.ledger.VMJobBinding(r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var originalPlan runner.GuestRequest
	gomega.Expect(json.Unmarshal(plan, &originalPlan)).To(gomega.Succeed())
	originalPlan.BootID, originalPlan.Request.BootID = old, old
	plan, _ = json.Marshal(originalPlan)
	_, err = db.Exec(`UPDATE managed_runs SET intent_json=? WHERE job_id=?`, plan, r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var binding runner.GuestReply
	gomega.Expect(json.Unmarshal(prepared, &binding)).To(gomega.Succeed())
	binding.Request.BootID = old
	prepared, _ = json.Marshal(binding)
	_, err = db.Exec(`UPDATE managed_events SET payload_json=? WHERE job_id=? AND kind='vm_prepared'`, prepared, r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	raw, _ := json.Marshal(guest.Request)
	_, err = db.Exec(`UPDATE managed_runner_receipts SET payload_json=?,digest=? WHERE job_id=? AND kind='request'`, raw, managed.RecoveryDigest(guest.Request), r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(f.s.ledger.MarkInterrupted()).To(gomega.Succeed())
	return f, r
}

// Complete actual ordinary cleanup, detach before its host completion record,
// then model the old boot in the remaining native control and host receipts.
func completedCleanupPreviousBootFixture(schema string) (*vmJobFixture, managed.Receipt, []string) {
	f := attestedVMFixture()
	f.job.Schema = schema
	// The real race-instrumented executable is transferred during preparation.
	// Admit this fixture's budget before reservation; recovery must preserve it.
	f.job.Deadlines.PrepareSeconds = 30
	if schema == "haepari.job/v2" {
		f.job.Workload.RequiredOutputs = append(f.job.Workload.RequiredOutputs, "results/missing.txt")
	}
	native := f.s.sshCommand
	var effects []string
	f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
		q := vmTransportRequest(input)
		result, err := native(ctx, exe, args, input)
		if q.Operation == "start" || q.Operation == "cleanup" {
			effects = append(effects, q.Operation)
		}
		if q.Operation == "cleanup" && err == nil {
			f.s.detachWorkers()
			return utm.CommandResult{}, errors.New("lost completed ordinary cleanup reply")
		}
		return result, err
	}
	r, err := f.reserve(f.admit("completed-cleanup-old-boot"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	executeErr := f.s.executeVM(r, f.job, f.object, time.Now().Add(time.Duration(f.job.Deadlines.PrepareSeconds)*time.Second))
	gomega.Expect(executeErr).To(gomega.HaveOccurred())
	f.s.workers.Wait()
	f.s.sshCommand = native
	guest, err := f.s.vmBinding(r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	controlPath := filepath.Join(f.guest, ".haepari-control-"+r.JobID+".json")
	raw, err := os.ReadFile(controlPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var control runner.GuestControl
	gomega.Expect(json.Unmarshal(raw, &control)).To(gomega.Succeed())
	gomega.Expect(control.Phase).To(gomega.Equal("cleanup"), "original execution: %v", executeErr)
	gomega.Expect(control.Complete).To(gomega.BeTrue())
	gomega.Expect(control.Error).To(gomega.BeEmpty())
	gomega.Expect(control.Observation.Exit.TerminationProven).To(gomega.BeTrue())
	_, err = os.Stat(filepath.Join(f.guest, r.JobID))
	gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	old := "completed-cleanup-fixture-old-boot"
	guest.BootID, guest.Request.BootID = old, old
	control.Request.BootID, control.Request.Request.BootID, control.NativeRequest.BootID, control.Owner.BootID = old, old, old, old
	view := control.Observation
	view.Intent.Binding, view.Intent.Supervisor.BootID = guest.Request.Binding(), old
	view.Start.Binding, view.Start.Process.BootID = guest.Request.Binding(), old
	view.Outcome.Binding, view.Exit.Binding = guest.Request.Binding(), guest.Request.Binding()
	raw, _ = json.Marshal(control)
	gomega.Expect(os.WriteFile(controlPath, raw, 0600)).To(gomega.Succeed())
	db, err := sqlite.Open(f.s.root.DBPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer db.Close()
	plan, prepared, err := f.s.ledger.VMJobBinding(r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var original runner.GuestRequest
	gomega.Expect(json.Unmarshal(plan, &original)).To(gomega.Succeed())
	original.BootID, original.Request.BootID = old, old
	plan, _ = json.Marshal(original)
	_, err = db.Exec(`UPDATE managed_runs SET intent_json=? WHERE job_id=?`, plan, r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var binding runner.GuestReply
	gomega.Expect(json.Unmarshal(prepared, &binding)).To(gomega.Succeed())
	binding.Request.BootID = old
	prepared, _ = json.Marshal(binding)
	_, err = db.Exec(`UPDATE managed_events SET payload_json=? WHERE job_id=? AND kind='vm_prepared'`, prepared, r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	for _, receipt := range []struct {
		name  string
		value any
	}{{"request", guest.Request}, {"intent", view.Intent}, {"start", view.Start}, {"outcome", view.Outcome}, {"exit", view.Exit}} {
		raw, _ = json.Marshal(receipt.value)
		_, err = db.Exec(`UPDATE managed_runner_receipts SET payload_json=?,digest=? WHERE job_id=? AND kind=?`, raw, managed.RecoveryDigest(receipt.value), r.JobID, receipt.name)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
	// The runtime detachment above is real. Model the stopped prior controller
	// boot in its saved phase owners; this is not a host reboot/kill observation.
	rows, err := db.Query(`SELECT cursor,payload_json FROM managed_events WHERE job_id=? AND kind IN ('phase_intent','phase_observed')`, r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var phases []struct {
		cursor int64
		phase  managed.LocalPhase
	}
	for rows.Next() {
		var cursor int64
		var raw []byte
		gomega.Expect(rows.Scan(&cursor, &raw)).To(gomega.Succeed())
		var phase managed.LocalPhase
		gomega.Expect(json.Unmarshal(raw, &phase)).To(gomega.Succeed())
		if phase.ControllerOwner != nil {
			phase.ControllerOwner.BootID = "prior-controller-fixture-boot"
		}
		phases = append(phases, struct {
			cursor int64
			phase  managed.LocalPhase
		}{cursor, phase})
	}
	gomega.Expect(rows.Err()).NotTo(gomega.HaveOccurred())
	rows.Close()
	for _, value := range phases {
		raw, _ := json.Marshal(value.phase)
		_, err = db.Exec(`UPDATE managed_events SET payload_json=? WHERE cursor=?`, raw, value.cursor)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
	gomega.Expect(f.s.ledger.MarkInterrupted()).To(gomega.Succeed())
	return f, r, effects
}

var _ = ginkgo.Describe("GuestRebootControllerRecovery", ginkgo.Label("integration"), func() {
	ginkgo.It("consumes only exact completed ordinary cleanup after a reboot and preserves the original phase budgets", func() {
		for _, schema := range []string{"haepari.job/v1", "haepari.job/v2"} {
			f, r, effects := completedCleanupPreviousBootFixture(schema)
			gomega.Expect(effects).To(gomega.Equal([]string{"start", "cleanup"}))
			before, err := f.s.ledger.RecoverySnapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(before.Phases["collect"].Completed).To(gomega.BeTrue())
			old := f.s
			fresh, err := newRuntime(old.root, old.ledger, old.store)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			fresh.utmCommand, fresh.sshCommand, fresh.vmLeases = old.utmCommand, old.sshCommand, old.vmLeases
			fresh.vmExecutable = old.vmExecutable
			f.s = fresh
			var extraEffects []string
			fresh.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
				operation := vmTransportRequest(input).Operation
				if operation == "start" || operation == "cleanup" {
					extraEffects = append(extraEffects, operation)
				}
				return old.sshCommand(ctx, exe, args, input)
			}
			controlPath := filepath.Join(f.guest, ".haepari-control-"+r.JobID+".json")
			originalControl, err := os.ReadFile(controlPath)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			for _, mode := range []string{"missing-control", "incomplete", "error", "foreign", "false-proof", "missing-artifact", "changed-log", "replacement-root"} {
				var control runner.GuestControl
				gomega.Expect(json.Unmarshal(originalControl, &control)).To(gomega.Succeed())
				switch mode {
				case "missing-control":
					gomega.Expect(os.Remove(controlPath)).To(gomega.Succeed())
				case "incomplete":
					control.Complete = false
				case "error":
					control.Error = managed.DiagnosticPermissionDenied
				case "foreign":
					control.NativeRequest.Identity.Nonce = "foreign"
				case "false-proof":
					control.Observation.Exit.TerminationProven = false
					control.Observation.Exit.Reason = "unproven"
				case "replacement-root":
					gomega.Expect(os.Mkdir(filepath.Join(f.guest, r.JobID), 0700)).To(gomega.Succeed())
				}
				if mode != "missing-control" {
					raw, _ := json.Marshal(control)
					gomega.Expect(os.WriteFile(controlPath, raw, 0600)).To(gomega.Succeed())
				}
				var saved []byte
				var path string
				if mode == "missing-artifact" || mode == "changed-log" {
					path = filepath.Join(f.s.root.Path, "jobs", r.JobID, "artifacts/results/output.txt")
					if mode == "changed-log" {
						path = filepath.Join(f.s.root.Path, "jobs", r.JobID, "output.log")
					}
					saved, err = os.ReadFile(path)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					if mode == "missing-artifact" {
						gomega.Expect(os.Remove(path)).To(gomega.Succeed())
					} else {
						gomega.Expect(os.WriteFile(path, []byte("corrupt host log"), 0600)).To(gomega.Succeed())
					}
				}
				if path != "" {
					// Proof is valid but host inventory must refuse before an intent commit.
					gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.HaveOccurred())
					fresh.workers.Wait()
					gomega.Expect(os.WriteFile(path, saved, 0600)).To(gomega.Succeed())
				} else {
					p, err := fresh.planJobRecovery(r.JobID, "reconcile")
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Expect(p.PlanToken).To(gomega.BeEmpty(), mode)
				}
				if mode == "replacement-root" {
					gomega.Expect(os.Remove(filepath.Join(f.guest, r.JobID))).To(gomega.Succeed())
				}
				gomega.Expect(os.WriteFile(controlPath, originalControl, 0600)).To(gomega.Succeed())
			}
			if schema == "haepari.job/v2" && time.Now().Before(before.Phases["cleanup"].Deadline) {
				time.Sleep(time.Until(before.Phases["cleanup"].Deadline) + time.Millisecond)
			}
			// Native observation can transiently refuse under a busy race suite.
			// Retry the supported controller reconciliation only while the Job
			// remains reconciling and no host worker has acquired it. This path
			// consumes completed deletion proof and issues no guest effect.
			gomega.Eventually(func() error {
				if fresh.jobWorkerAlive(r.JobID) {
					return managed.ErrBlocked
				}
				state, err := fresh.ledger.Snapshot(r.JobID)
				if err != nil {
					return err
				}
				if state.JobState != "reconciling" {
					return managed.ErrBlocked
				}
				return fresh.tryRecoverInterrupted(r.JobID, true, nil)
			}, 30*time.Second, 100*time.Millisecond).Should(gomega.Succeed())
			fresh.workers.Wait()
			result, err := fresh.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("pass")))
			if schema == "haepari.job/v2" {
				gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
				gomega.Expect(result.Reason).To(gomega.Equal("cleanup_deadline"))
			}
			gomega.Expect(result.Artifacts).To(gomega.Equal(before.Phases["collect"].Artifacts))
			after, err := fresh.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(after.JobState).To(gomega.Equal("finished"))
			gomega.Expect(after.GlobalOccupancy).To(gomega.BeZero())
			phases, err := fresh.ledger.LocalPhases(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(phases["cleanup"].Deadline).To(gomega.Equal(before.Phases["cleanup"].Deadline))
			gomega.Expect(phases["cleanup"].StartedAt).To(gomega.Equal(before.Phases["cleanup"].StartedAt))
			gomega.Expect(os.ReadFile(controlPath)).To(gomega.Equal(originalControl))
			gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.MatchError(managed.ErrBlocked))
			if result.ResourceAvailability == "quarantined" {
				p, err := fresh.planResourceRecovery(f.spec.ID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(p.PlanToken).NotTo(gomega.BeEmpty())
				response := recoveryHTTP(fresh, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: p.PlanToken, MutationKey: "completed-cleanup-health"})
				gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
				status, err := fresh.ledger.ResourceStatus(f.spec.ID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(status.Availability).To(gomega.Equal("available"))
			} else {
				snap, err := fresh.ledger.ResourceRecoverySnapshot(f.spec.ID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				_, healthy := (vmEvidenceBackend{fresh, f.spec.ID}).Health(snap)
				gomega.Expect(healthy).To(gomega.BeTrue())
			}

			db, err := sqlite.Open(fresh.root.DBPath)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var count int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='finished'`, r.JobID).Scan(&count)).To(gomega.Succeed())
			db.Close()
			gomega.Expect(count).To(gomega.Equal(1))
			gomega.Expect(extraEffects).To(gomega.BeEmpty())

		}
	})

	ginkgo.It("records bound supplementary absence, rejects forged absence receipts and retains newly existing evidence", func() {
		f, r := previousBootVMFixtureWithOutputs(false, false, false, "haepari.job/v2", []string{"results/output.txt", "source/once.txt"})
		root := filepath.Join(f.guest, r.JobID)
		gomega.Expect(os.Remove(filepath.Join(root, "workspace/results/output.txt"))).To(gomega.Succeed())
		plan, err := f.s.planJobRecovery(r.JobID, "reconcile")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		response := recoveryHTTP(f.s, "/v1/jobs/"+r.JobID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "absence-reboot"})
		gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
		f.s.workers.Wait()
		original, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(original.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		q, found, err := f.s.vmAttestation(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(found).To(gomega.BeTrue())
		q.Operation, q.Guest.Path, q.Deadline = "file", "results/output.txt", time.Now().Add(5*time.Second)
		baseline, err := f.s.vmRecoveryCall(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(baseline.Guest.Absent).To(gomega.BeTrue())
		transport, err := f.s.vmSSH(f.spec)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, mode := range []string{"valid", "v1", "log", "receipt", "offset", "expected-file", "file-present", "output-present", "missing-workspace", "foreign-basis"} {
			request := q
			request.Deadline = time.Now().Add(5 * time.Second)
			switch mode {
			case "v1":
				request.Schema, request.RecoveryBoot = "haepari.guest-recovery-request/v1", ""
			case "log":
				request.Guest.Area = "log"
			case "receipt":
				request.Guest.Area = "receipt"
			case "offset":
				request.Guest.Offset = 1
			case "expected-file":
				request.Guest.ExpectedFile = &runner.GuestFile{Path: request.Guest.Path, Inode: 1, SHA256: strings.Repeat("0", 64)}
			}
			transport.Run = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
				raw := operationalRecoverySSHInput(input)
				var shape struct{ Schema, Operation string }
				gomega.Expect(json.Unmarshal(raw, &shape)).To(gomega.Succeed())
				if shape.Operation == "identity" {
					return f.s.sshCommand(ctx, exe, args, input)
				}
				actual, err := runner.DecodeGuestRecoveryRequest(raw)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				saved, _ := json.Marshal(baseline)
				var reply runner.GuestRecoveryReply
				gomega.Expect(json.Unmarshal(saved, &reply)).To(gomega.Succeed())
				reply.ProtocolDigest, reply.Guest.Challenge = managed.RecoveryDigest(actual), actual.Guest.Challenge
				if mode == "file-present" {
					reply.Guest.File = &runner.GuestFile{Path: request.Guest.Path, Inode: 1, SHA256: strings.Repeat("0", 64)}
				}
				if mode == "output-present" {
					reply.Guest.Output = []byte("uncollected")
				}
				if mode == "missing-workspace" {
					reply.Basis.WorkspaceRoot = nil
				}
				if mode == "foreign-basis" {
					reply.Basis.WorkspaceRoot.Inode++
				}
				body, _ := json.Marshal(reply)
				body, _ = json.Marshal(managed.Envelope{Schema: "haepari.envelope/v1", Operation: "runner.guest-recovery", OK: true, Data: body})
				return utm.CommandResult{Stdout: string(body)}, nil
			}
			leases, err := f.s.vmLeases()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = transport.CallRecovery(context.Background(), f.spec, leases, request)
			leases.Close()
			if mode == "valid" {
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			} else {
				gomega.Expect(err).To(gomega.HaveOccurred(), mode)
			}
		}
		// Missing one output cannot excuse failed collection of existing bytes.
		failedPlan, err := f.s.planEvidenceRecovery(f.spec.ID, "recollect")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f.s.vmWrite = func(*os.File, []byte) (int, error) { return 0, unix.ENOSPC }
		response = recoveryHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: failedPlan.PlanToken, MutationKey: "absence-uncollected"})
		// A bounded request may return while its collector still owns effects.
		// Keep injected hooks stable until those workers actually stop.
		f.s.workers.Wait()
		f.s.vmWrite = nil
		gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
		_, failedReceipt, _, err := f.s.ledger.EvidenceRecovery("evidence-recovery-absence-uncollected", "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(failedReceipt.Jobs[0].Stage).To(gomega.Equal("failed"))
		gomega.Expect(os.ReadFile(filepath.Join(root, "workspace/source/once.txt"))).To(gomega.Equal([]byte("once")))
		// Test the durable collection/deletion boundary with real guest bytes.
		p, err := f.s.planEvidenceRecovery(f.spec.ID, "recollect")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		prior := retentionBoundary
		ginkgo.DeferCleanup(func() {
			f.s.workers.Wait()
			retentionBoundary = prior
		})
		retentionBoundary = func(point string) {
			if point == "collection-recorded" {
				gomega.Expect(os.WriteFile(filepath.Join(root, "workspace/results/output.txt"), []byte("new evidence"), 0600)).To(gomega.Succeed())
			}
		}
		response = recoveryHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: p.PlanToken, MutationKey: "absence-new-file"})
		f.s.workers.Wait()
		retentionBoundary = prior
		gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
		intent, receipt, _, err := f.s.ledger.EvidenceRecovery("evidence-recovery-absence-new-file", "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(intent.Schema).To(gomega.Equal("haepari.evidence-recovery-intent/v2"))
		gomega.Expect(receipt.Schema).To(gomega.Equal("haepari.evidence-recovery-receipt/v2"))
		gomega.Expect(receipt.Jobs[0].Stage).To(gomega.Equal("failed"))
		gomega.Expect(receipt.Jobs[0].AbsentOutputs).To(gomega.HaveLen(1))
		gomega.Expect(os.ReadFile(filepath.Join(root, "workspace/results/output.txt"))).To(gomega.Equal([]byte("new evidence")))
		gomega.Expect(os.Remove(filepath.Join(root, "workspace/results/output.txt"))).To(gomega.Succeed())
		p, err = f.s.planEvidenceRecovery(f.spec.ID, "recollect")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		response = recoveryHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: p.PlanToken, MutationKey: "absence-resolved"})
		gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
		intent, receipt, _, err = f.s.ledger.EvidenceRecovery("evidence-recovery-absence-resolved", "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(receipt.Jobs[0].Stage).To(gomega.Equal("resolved"))
		gomega.Expect(receipt.Jobs[0].GuestArtifacts).To(gomega.HaveLen(5))
		for _, mode := range []string{"duplicate", "overlap", "schema", "before-window", "after-window", "receipt-mutation"} {
			raw, _ := json.Marshal(receipt)
			var changed managed.EvidenceRecoveryReceipt
			gomega.Expect(json.Unmarshal(raw, &changed)).To(gomega.Succeed())
			switch mode {
			case "duplicate":
				changed.Jobs[0].AbsentOutputs = append(changed.Jobs[0].AbsentOutputs, changed.Jobs[0].AbsentOutputs[0])
			case "overlap":
				changed.Jobs[0].Artifacts = append(changed.Jobs[0].Artifacts, managed.ResultArtifact{Path: changed.Jobs[0].AbsentOutputs[0].Path, SHA256: strings.Repeat("0", 64)})
			case "schema":
				changed.Schema = "haepari.evidence-recovery-receipt/v1"
			case "before-window":
				changed.Jobs[0].AbsentOutputs[0].ObservedAt = intent.RecordedAt.Add(-time.Second)
			case "after-window":
				changed.Jobs[0].AbsentOutputs[0].ObservedAt = intent.Jobs[0].CollectDeadline.Add(time.Second)
			case "receipt-mutation":
				changed.Jobs[0].AbsentOutputs[0].ObservedAt = changed.Jobs[0].AbsentOutputs[0].ObservedAt.Add(time.Nanosecond)
			}
			gomega.Expect(f.s.ledger.RecordEvidenceRecovery(intent, changed)).To(gomega.HaveOccurred(), mode)
		}
		// Prove closed v1 decoding through the real persisted ledger, including
		// explicit null fields that typed unmarshalling would otherwise erase.
		db, err := sqlite.Open(f.s.root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		var savedIntent, savedReceipt []byte
		gomega.Expect(db.QueryRow(`SELECT request_json,receipt_json FROM managed_evidence_recovery WHERE mutation_key=?`, intent.MutationKey).Scan(&savedIntent, &savedReceipt)).To(gomega.Succeed())
		ginkgo.DeferCleanup(func() {
			restore, err := sqlite.Open(f.s.root.DBPath)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer restore.Close()
			_, err = restore.Exec(`UPDATE managed_evidence_recovery SET request_json=?,receipt_json=? WHERE mutation_key=?`, savedIntent, savedReceipt, intent.MutationKey)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
		var legacyIntent managed.EvidenceRecoveryIntent
		var legacyReceipt managed.EvidenceRecoveryReceipt
		gomega.Expect(json.Unmarshal(savedIntent, &legacyIntent)).To(gomega.Succeed())
		gomega.Expect(json.Unmarshal(savedReceipt, &legacyReceipt)).To(gomega.Succeed())
		legacyIntent.Schema, legacyIntent.Jobs[0].AbsenceBinding = "haepari.evidence-recovery-intent/v1", nil
		legacyReceipt.Schema, legacyReceipt.Jobs[0].AbsentOutputs = "haepari.evidence-recovery-receipt/v1", nil
		legacyReceipt.Jobs[0].Stage, legacyReceipt.Jobs[0].Gap = "failed", "uncollected"
		legacyQ, _ := json.Marshal(legacyIntent)
		legacyR, _ := json.Marshal(legacyReceipt)
		for _, field := range []string{"", "absence_binding", "absent_outputs"} {
			qr, rr := legacyQ, legacyR
			if field != "" {
				var shape map[string]any
				selected := qr
				if field == "absent_outputs" {
					selected = rr
				}
				gomega.Expect(json.Unmarshal(selected, &shape)).To(gomega.Succeed())
				shape["jobs"].([]any)[0].(map[string]any)[field] = nil
				selected, _ = json.Marshal(shape)
				if field == "absence_binding" {
					qr = selected
				} else {
					rr = selected
				}
			}
			_, err = db.Exec(`UPDATE managed_evidence_recovery SET request_json=?,receipt_json=? WHERE mutation_key=?`, qr, rr, intent.MutationKey)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, _, _, err = f.s.ledger.EvidenceRecovery(intent.MutationKey, "")
			if field == "" {
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			} else {
				gomega.Expect(err).To(gomega.MatchError(managed.ErrBlocked), field)
			}
		}
		_, err = db.Exec(`UPDATE managed_evidence_recovery SET request_json=?,receipt_json=? WHERE mutation_key=?`, savedIntent, savedReceipt, intent.MutationKey)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.s.ledger.Result(r.JobID)).To(gomega.Equal(original))
		_, err = os.Stat(root)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		p, err = f.s.planResourceRecovery(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		response = recoveryHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: p.PlanToken, MutationKey: "absence-health"})
		gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
		status, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Availability).To(gomega.Equal("available"))
	})

	ginkgo.It("keeps a delayed ordinary reboot probe on its five second clock without recovery authority", func() {
		f, r := previousBootVMFixture()
		before, err := f.s.ledger.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		receipts := map[string][]byte{}
		for _, name := range []string{"guest.json", "request.json", "intent.json", "start.json"} {
			raw, err := os.ReadFile(filepath.Join(f.guest, r.JobID, name))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			receipts[name] = raw
		}
		// This prior-boot fixture deliberately has no outcome or exit proof.
		for _, name := range []string{"outcome.json", "exit.json"} {
			_, err := os.Stat(filepath.Join(f.guest, r.JobID, name))
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		}
		native := f.s.sshCommand
		probes := 0
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			q := vmTransportRequest(input)
			if q.Schema == "haepari.guest-recovery-request/v2" && q.Operation == "probe" {
				probes++
				timer := time.NewTimer(6 * time.Second)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return utm.CommandResult{}, ctx.Err()
				case <-timer.C:
				}
			}
			return native(ctx, exe, args, input)
		}
		started := time.Now()
		plan, err := f.s.planJobRecovery(r.JobID, "reconcile")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.Unknowns).NotTo(gomega.BeEmpty())
		gomega.Expect(plan.PlanToken).To(gomega.BeEmpty())
		gomega.Expect(probes).To(gomega.Equal(1))
		gomega.Expect(time.Since(started)).To(gomega.BeNumerically(">=", 5*time.Second))
		gomega.Expect(time.Since(started)).To(gomega.BeNumerically("<", 8*time.Second))
		after, err := f.s.ledger.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after.Identity).To(gomega.Equal(before.Identity))
		gomega.Expect(after.Receipt.JobState).To(gomega.Equal(before.Receipt.JobState))
		gomega.Expect(after.Receipt.GlobalOccupancy).To(gomega.Equal(before.Receipt.GlobalOccupancy))
		gomega.Expect(after.Receipt.ResourceAvailability).To(gomega.Equal(before.Receipt.ResourceAvailability))
		for name, raw := range receipts {
			gomega.Expect(os.ReadFile(filepath.Join(f.guest, r.JobID, name))).To(gomega.Equal(raw))
		}
		for _, name := range []string{"outcome.json", "exit.json"} {
			_, err := os.Stat(filepath.Join(f.guest, r.JobID, name))
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		}
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, r.JobID, "workspace/source/once.txt"))).To(gomega.Equal([]byte("once")))
		_, err = os.Stat(filepath.Join(f.s.root.Path, "jobs", r.JobID, "guest-reboot-recovery.json"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		expectNoPower(f)
	})

	for _, boundary := range []string{"guest-reboot-host-intent", "guest-reboot-host-collect-recorded", "guest-reboot-host-cleanup-recorded"} {
		boundary := boundary
		ginkgo.It("resumes only committed reboot settlement after a stopped controller at "+boundary, func() {
			f, r := previousBootVMFixture()
			original, err := f.s.ledger.RecoverySnapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			plan, err := f.s.planJobRecovery(r.JobID, "reconcile")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(plan.Unknowns).To(gomega.BeEmpty(), "reboot recovery observation is incomplete: %v", plan.Unknowns)
			gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
			old := f.s
			priorBoundary := retentionBoundary
			cut := false
			retentionBoundary = func(point string) {
				if point == boundary && !cut {
					cut = true
					old.detachWorkers()
				}
			}
			ginkgo.DeferCleanup(func() { retentionBoundary = priorBoundary })
			response := recoveryHTTP(old, "/v1/jobs/"+r.JobID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "reboot-interrupted-host"})
			old.workers.Wait()
			retentionBoundary = priorBoundary
			gomega.Expect(cut).To(gomega.BeTrue(), "crash boundary %s was not reached: %s", boundary, response.Body.String())
			gomega.Expect(old.ledger.MarkInterrupted()).To(gomega.Succeed())
			interrupted, err := old.ledger.RecoverySnapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(interrupted.Receipt.JobState).To(gomega.Equal("reconciling"))
			gomega.Expect(interrupted.Receipt.GlobalOccupancy).To(gomega.Equal(1))
			gomega.Expect(interrupted.Stopped).To(gomega.BeTrue())
			intentPath := filepath.Join(old.root.Path, "jobs", r.JobID, "guest-reboot-recovery.json")
			intentRaw, err := os.ReadFile(intentPath)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			fresh, err := newRuntime(old.root, old.ledger, old.store)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			fresh.utmCommand, fresh.sshCommand, fresh.vmLeases = old.utmCommand, old.sshCommand, old.vmLeases
			fresh.vmExecutable = old.vmExecutable
			f.s = fresh
			gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.Succeed())
			fresh.workers.Wait()
			settled, err := fresh.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(settled.JobState).To(gomega.Equal("finished"))
			gomega.Expect(settled.GlobalOccupancy).To(gomega.BeZero())
			result, err := fresh.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
			gomega.Expect(result.ProtectedEvidence).NotTo(gomega.BeEmpty())
			gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
			gomega.Expect(os.ReadFile(intentPath)).To(gomega.Equal(intentRaw))
			gomega.Expect(os.ReadFile(filepath.Join(f.guest, r.JobID, "workspace/source/once.txt"))).To(gomega.Equal([]byte("once")))
			identity, err := fresh.ledger.LocalIdentity(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(identity).To(gomega.Equal(original.Identity))
			phases, err := fresh.ledger.LocalPhases(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			for name, phase := range interrupted.Phases {
				gomega.Expect(phases[name].Deadline).To(gomega.Equal(phase.Deadline), name)
			}
			gomega.Expect(fresh.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.MatchError(managed.ErrBlocked))
			events, err := fresh.ledger.EventsAfter(r.JobID, 0, 1000)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			finished := 0
			for _, event := range events.Events {
				if event.Kind == "finished" {
					finished++
				}
			}
			gomega.Expect(finished).To(gomega.Equal(1))
		})
	}
	for _, mode := range []string{"completed", "not-issued", "publication-only"} {
		mode := mode
		ginkgo.It("consumes completed reboot authority after its fixed expiry without renewing effects "+mode, func() {
			f, r := previousBootVMFixture()
			snapshot, err := f.s.ledger.RecoverySnapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			plan, err := f.s.planJobRecovery(r.JobID, "reconcile")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			guest, facts, err := f.s.observeVMRecovery(snapshot)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(facts.Reboot).NotTo(gomega.BeNil())
			// A bounded original authority window keeps this native boundary
			// fixture timely while leaving the genuine native identity and
			// attest exchanges enough headroom inside the immutable window.
			// No saved request or deadline is changed after publication.
			plan.ExpiresAt = time.Now().UTC().Add(15 * time.Second)
			native := f.s.sshCommand
			attests := 0
			f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
				archive := tar.NewReader(bytes.NewReader(input))
				var wire runner.GuestRecoveryRequest
				for {
					header, e := archive.Next()
					if e != nil {
						break
					}
					if header.Name == "request.json" {
						raw, e := io.ReadAll(archive)
						gomega.Expect(e).NotTo(gomega.HaveOccurred())
						gomega.Expect(json.Unmarshal(raw, &wire)).To(gomega.Succeed())
						break
					}
				}
				if wire.Schema != "haepari.guest-recovery-request/v2" || wire.Operation != "attest" {
					return native(ctx, exe, args, input)
				}
				attests++
				if mode == "not-issued" {
					return utm.CommandResult{}, errors.New("fixture lost undelivered attestation")
				}
				result, e := native(ctx, exe, args, input)
				// Count a native success only with a completed transport and
				// envelope, the full original identity binding, the exact
				// attestation for this wire request and completion inside the
				// original authority window.
				gomega.Expect(e).NotTo(gomega.HaveOccurred(), result.Stderr)
				gomega.Expect(result.ExitCode).To(gomega.Equal(0), result.Stderr)
				envelope, e := managed.DecodeEnvelope([]byte(result.Stdout))
				gomega.Expect(e).NotTo(gomega.HaveOccurred())
				gomega.Expect(envelope.OK).To(gomega.BeTrue())
				gomega.Expect(envelope.Operation).To(gomega.Equal("runner.guest-recovery"))
				gomega.Expect(wire.Guest.Request.Identity).To(gomega.Equal(snapshot.Identity))
				gomega.Expect(time.Now().Before(plan.ExpiresAt)).To(gomega.BeTrue())
				reply, e := runner.DecodeGuestRecoveryReply(envelope.Data)
				gomega.Expect(e).NotTo(gomega.HaveOccurred())
				expected := rebootAttestation(wire)
				gomega.Expect(runner.ValidateGuestAttestation(expected, wire.Guest)).To(gomega.Succeed())
				gomega.Expect(reply.Schema).To(gomega.Equal("haepari.guest-recovery-reply/v2"))
				gomega.Expect(reply.RecoveryDigest).To(gomega.Equal(wire.RecoveryDigest))
				gomega.Expect(reply.ProtocolDigest).To(gomega.Equal(managed.RecoveryDigest(wire)))
				gomega.Expect(reply.Attestation).NotTo(gomega.BeNil())
				gomega.Expect(*reply.Attestation).To(gomega.Equal(expected))
				if mode == "publication-only" {
					// Model publication without durable retirement in private fixture
					// state; the resumed native state read must reject this uncertainty.
					path := filepath.Join(f.guest, ".haepari-control-"+r.JobID+".json")
					raw, e := os.ReadFile(path)
					gomega.Expect(e).NotTo(gomega.HaveOccurred())
					var control runner.GuestControl
					gomega.Expect(json.Unmarshal(raw, &control)).To(gomega.Succeed())
					control.Retired = false
					raw, e = json.Marshal(control)
					gomega.Expect(e).NotTo(gomega.HaveOccurred())
					gomega.Expect(os.WriteFile(path, raw, 0600)).To(gomega.Succeed())
				}
				if mode == "completed" {
					// The reply is discarded only after its bound native control
					// proves genuine durable retirement inside the same window:
					// full original request identity, an owner belonging to the
					// control's own guest boot, and retired true.
					path := filepath.Join(f.guest, ".haepari-control-"+r.JobID+".json")
					raw, e := os.ReadFile(path)
					gomega.Expect(e).NotTo(gomega.HaveOccurred())
					var control runner.GuestControl
					gomega.Expect(json.Unmarshal(raw, &control)).To(gomega.Succeed())
					gomega.Expect(runner.ValidateGuestControl(control, wire.Guest)).To(gomega.Succeed())
					gomega.Expect(control.NativeRequest).NotTo(gomega.BeNil())
					gomega.Expect(*control.NativeRequest).To(gomega.Equal(wire.Guest.Request))
					gomega.Expect(control.Request.Request.Identity).To(gomega.Equal(snapshot.Identity))
					gomega.Expect(control.Owner.BootID).To(gomega.Equal(control.Request.BootID))
					gomega.Expect(control.Retired).To(gomega.BeTrue())
				}
				return utm.CommandResult{}, errors.New("fixture lost completed attestation reply")
			}
			gomega.Expect(f.s.recoverVMReboot(snapshot, guest, *facts.Reboot, &plan, "recovery-expired-reboot", "fixture-client")).To(gomega.MatchError(managed.ErrBlocked))
			intentPath := filepath.Join(f.s.root.Path, "jobs", r.JobID, "guest-reboot-recovery.json")
			intentRaw, err := os.ReadFile(intentPath)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var saved runner.GuestRecoveryRequest
			gomega.Expect(json.Unmarshal(intentRaw, &saved)).To(gomega.Succeed())
			time.Sleep(max(0, time.Until(saved.Deadline)) + 10*time.Millisecond)
			freshPlan, err := f.s.planJobRecovery(r.JobID, "reconcile")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(freshPlan.PlanToken).NotTo(gomega.BeEmpty())
			response := recoveryHTTP(f.s, "/v1/jobs/"+r.JobID+"/recover", managed.RecoveryRequest{PlanToken: freshPlan.PlanToken, MutationKey: "expired-reboot"})
			f.s.workers.Wait()
			gomega.Expect(attests).To(gomega.Equal(1))
			gomega.Expect(os.ReadFile(intentPath)).To(gomega.Equal(intentRaw))
			current, err := f.s.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if mode == "completed" {
				gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
				gomega.Expect(current.JobState).To(gomega.Equal("finished"))
				gomega.Expect(current.GlobalOccupancy).To(gomega.BeZero())
			} else {
				gomega.Expect(response.Code).NotTo(gomega.Equal(200))
				gomega.Expect(current.JobState).To(gomega.Equal("reconciling"))
				gomega.Expect(current.GlobalOccupancy).To(gomega.Equal(1))
			}
			gomega.Expect(os.ReadFile(filepath.Join(f.guest, r.JobID, "workspace/source/once.txt"))).To(gomega.Equal([]byte("once")))
			gomega.Expect(reflect.DeepEqual(saved.Deadline, plan.ExpiresAt)).To(gomega.BeTrue())
		})
	}
	for _, schema := range []string{"haepari.job/v1", "haepari.job/v2"} {
		for _, failed := range []bool{false, true} {
			schema, failed := schema, failed
			ginkgo.It("preserves a present original native outcome after lost observation and reboot "+schema+map[bool]string{false: " pass", true: " fail"}[failed], func() {
				f, r := previousBootVMFixtureWithOutcome(false, true, failed, schema)
				snapshot, err := f.s.ledger.RecoverySnapshot(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(snapshot.Outcome).To(gomega.BeNil())
				plan, err := f.s.planJobRecovery(r.JobID, "reconcile")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
				root := filepath.Join(f.guest, r.JobID)
				native := map[string][]byte{}
				for _, name := range []string{"request.json", "intent.json", "start.json", "outcome.json", "exit.json"} {
					native[name], err = os.ReadFile(filepath.Join(root, name))
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				var expected runner.Outcome
				gomega.Expect(json.Unmarshal(native["outcome.json"], &expected)).To(gomega.Succeed())
				response := recoveryHTTP(f.s, "/v1/jobs/"+r.JobID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "reboot-present-native"})
				gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
				f.s.workers.Wait()
				result, err := f.s.ledger.Result(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
				gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(expected.Outcome.WorkloadOutcome))
				gomega.Expect(result.NativeExitCode).To(gomega.Equal(expected.Outcome.NativeExitCode))
				gomega.Expect(result.NativeSignal).To(gomega.Equal(expected.Outcome.NativeSignal))
				gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
				for name, raw := range native {
					gomega.Expect(os.ReadFile(filepath.Join(root, name))).To(gomega.Equal(raw), name)
				}
				gomega.Expect(os.ReadFile(filepath.Join(root, "workspace/source/once.txt"))).To(gomega.Equal([]byte("once")))
				receipt, err := f.s.ledger.Snapshot(r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(receipt.GlobalOccupancy).To(gomega.BeZero())
			})
		}
	}
	ginkgo.It("plans and applies cross-boot resource attestation through supported recovery v2 without rewriting original facts", func() {
		f, r := previousBootVMFixtureWithResult(true)
		original, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(original.ResourceAvailability).To(gomega.Equal("quarantined"))
		gomega.Expect(r.JobState).To(gomega.Equal("finished"))
		gomega.Expect(r.GlobalOccupancy).To(gomega.BeZero())
		guest, err := f.s.vmBinding(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		originalPlan, originalPrepared, err := f.s.ledger.VMJobBinding(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		root := filepath.Join(f.guest, r.JobID)
		originalFiles := map[string][]byte{}
		for _, name := range []string{"guest.json", "request.json", "intent.json", "start.json", "haepari"} {
			originalFiles[name], err = os.ReadFile(filepath.Join(root, name))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		controlPath := filepath.Join(f.guest, ".haepari-control-"+r.JobID+".json")
		readControl := func() runner.GuestControl {
			raw, err := os.ReadFile(controlPath)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var control runner.GuestControl
			gomega.Expect(json.Unmarshal(raw, &control)).To(gomega.Succeed())
			return control
		}
		originalControl := readControl()
		_, found, err := f.s.vmAttestation(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(found).To(gomega.BeFalse())

		// Observe the actual bootstrap payloads and replies; do not synthesize a
		// successful identity proof or attestation to bypass the transport fence.
		native := f.s.sshCommand
		var proofs []runner.GuestRequest
		var requests []runner.GuestRecoveryRequest
		var replies []runner.GuestRecoveryReply
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			archive := tar.NewReader(bytes.NewReader(input))
			var raw []byte
			for {
				header, err := archive.Next()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				if header.Name == "request.json" {
					raw, err = io.ReadAll(archive)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					break
				}
			}
			var shape struct{ Schema string }
			gomega.Expect(json.Unmarshal(raw, &shape)).To(gomega.Succeed())
			if shape.Schema == "haepari.guest-recovery-request/v2" {
				q, err := runner.DecodeGuestRecoveryRequest(raw)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				requests = append(requests, q)
			} else {
				var probe runner.GuestRequest
				gomega.Expect(json.Unmarshal(raw, &probe)).To(gomega.Succeed())
				gomega.Expect(probe.Operation).To(gomega.Equal("identity"))
				proofs = append(proofs, probe)
			}
			result, err := native(ctx, exe, args, input)
			if shape.Schema == "haepari.guest-recovery-request/v2" {
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				envelope, decodeErr := managed.DecodeEnvelope([]byte(result.Stdout))
				gomega.Expect(decodeErr).NotTo(gomega.HaveOccurred())
				gomega.Expect(envelope.OK).To(gomega.BeTrue())
				reply, decodeErr := runner.DecodeGuestRecoveryReply(envelope.Data)
				gomega.Expect(decodeErr).NotTo(gomega.HaveOccurred())
				replies = append(replies, reply)
			}
			return result, err
		}
		path := "/v1/resources/" + f.spec.ID + "/recover"
		response := vmMutationHTTP(f.s, path, managed.RecoveryRequest{Plan: true, Action: "attest-guest-stopped"})
		gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
		var envelope managed.Envelope
		gomega.Expect(json.Unmarshal(response.Body.Bytes(), &envelope)).To(gomega.Succeed())
		var plan resourceRecoveryPlan
		gomega.Expect(json.Unmarshal(envelope.Data, &plan)).To(gomega.Succeed())
		gomega.Expect(plan.Schema).To(gomega.Equal("haepari.recovery-plan/v2"))
		gomega.Expect(plan.Action).To(gomega.Equal("attest-guest-stopped"))
		gomega.Expect(plan.Unknowns).To(gomega.BeEmpty())
		gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
		gomega.Expect(proofs).To(gomega.HaveLen(2))
		gomega.Expect(proofs[0].Schema).To(gomega.Equal(guest.Schema))
		gomega.Expect(proofs[0].BootID).To(gomega.Equal(guest.BootID))
		gomega.Expect(proofs[1].Schema).To(gomega.Equal("haepari.guest-request/v2"))
		gomega.Expect(proofs[1].BootID).To(gomega.BeEmpty())
		gomega.Expect(proofs[1].Request).To(gomega.Equal(guest.Request))
		gomega.Expect(requests).To(gomega.HaveLen(1))
		gomega.Expect(requests[0].Operation).To(gomega.Equal("probe"))
		gomega.Expect(requests[0].Guest.Request).To(gomega.Equal(guest.Request))
		gomega.Expect(requests[0].Guest.RunnerDigest).To(gomega.Equal(guest.RunnerDigest))
		gomega.Expect(replies[0].Schema).To(gomega.Equal("haepari.guest-recovery-reply/v2"))
		basis := replies[0].Basis
		gomega.Expect(basis).NotTo(gomega.BeNil())
		gomega.Expect(basis.RecoveryBoot).NotTo(gomega.BeEmpty())
		gomega.Expect(basis.RecoveryBoot).NotTo(gomega.Equal(guest.BootID))
		gomega.Expect(requests[0].RecoveryBoot).To(gomega.Equal(basis.RecoveryBoot))
		gomega.Expect(basis.Request).To(gomega.Equal(guest.Request))
		gomega.Expect(basis.Digests["haepari"]).To(gomega.Equal(guest.RunnerDigest))
		gomega.Expect(basis.Missing).To(gomega.ConsistOf("outcome.json", "exit.json"))
		gomega.Expect(basis.Observation.Exit).To(gomega.BeNil())

		applyGuestPlan(f, plan, "resource-cross-boot-attest", guestEvidence(plan))
		intent, receipt, found, err := f.s.ledger.EvidenceRecovery("evidence-recovery-resource-cross-boot-attest", "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(found).To(gomega.BeTrue())
		gomega.Expect(receipt.Complete).To(gomega.BeTrue())
		gomega.Expect(receipt.Jobs).To(gomega.HaveLen(1))
		gomega.Expect(receipt.Jobs[0].Stage).To(gomega.Equal("attested"))
		gomega.Expect(intent.Jobs).To(gomega.HaveLen(1))
		saved, err := runner.DecodeGuestRecoveryRequest(intent.Jobs[0].GuestAttestation)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(saved.Schema).To(gomega.Equal("haepari.guest-recovery-request/v2"))
		gomega.Expect(saved.RecoveryBoot).To(gomega.Equal(basis.RecoveryBoot))
		gomega.Expect(saved.Deadline).To(gomega.Equal(plan.ExpiresAt))
		gomega.Expect(saved.Guest.Request).To(gomega.Equal(guest.Request))
		var attestation runner.GuestAttestation
		raw, err := os.ReadFile(filepath.Join(f.guest, ".haepari-attestation-"+r.JobID+".json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(json.Unmarshal(raw, &attestation)).To(gomega.Succeed())
		gomega.Expect(attestation.Schema).To(gomega.Equal("haepari.guest-attestation/v2"))
		gomega.Expect(attestation.Basis.RecoveryBoot).To(gomega.Equal(basis.RecoveryBoot))
		gomega.Expect(attestation.Basis.Request).To(gomega.Equal(guest.Request))
		gomega.Expect(attestation.Guest).To(gomega.Equal(guestRecoveryOwner(guest)))
		var retained runner.GuestAttestation
		gomega.Expect(json.Unmarshal(receipt.Jobs[0].Attestation, &retained)).To(gomega.Succeed())
		gomega.Expect(retained).To(gomega.Equal(attestation))
		q, found, err := f.s.vmAttestation(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(found).To(gomega.BeTrue())
		gomega.Expect(q.Schema).To(gomega.Equal("haepari.guest-recovery-request/v2"))
		gomega.Expect(q.RecoveryBoot).To(gomega.Equal(basis.RecoveryBoot))
		gomega.Expect(q.Attestation).To(gomega.Equal(&attestation))
		q.Operation, q.Deadline = "state", time.Now().Add(5*time.Second)
		echo, err := f.s.vmRecoveryCall(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(echo.Schema).To(gomega.Equal("haepari.guest-recovery-reply/v2"))
		gomega.Expect(echo.Attestation).To(gomega.Equal(q.Attestation))
		gomega.Expect(echo.Basis).To(gomega.Equal(q.Basis))
		gomega.Expect(requests[len(requests)-1].Attestation).To(gomega.Equal(q.Attestation))

		for name, raw := range originalFiles {
			gomega.Expect(os.ReadFile(filepath.Join(root, name))).To(gomega.Equal(raw), name)
		}
		for _, name := range []string{"outcome.json", "exit.json"} {
			_, err := os.Stat(filepath.Join(root, name))
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue(), name)
		}
		control := readControl()
		gomega.Expect(control.Retired).To(gomega.BeTrue())
		originalControl.Retired = true
		gomega.Expect(control).To(gomega.Equal(originalControl))
		currentPlan, currentPrepared, err := f.s.ledger.VMJobBinding(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(currentPlan).To(gomega.Equal(originalPlan))
		gomega.Expect(currentPrepared).To(gomega.Equal(originalPrepared))
		current, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current).To(gomega.Equal(original))
		gomega.Expect(os.ReadFile(filepath.Join(root, "workspace/source/once.txt"))).To(gomega.Equal([]byte("once")))
		status, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Occupancy).To(gomega.BeZero())
		gomega.Expect(status.Availability).To(gomega.Equal("quarantined"))
	})
	ginkgo.It("retains occupancy when the original guest root is replaced before explicit application", func() {
		f, r := previousBootVMFixture()
		plan, err := f.s.planJobRecovery(r.JobID, "reconcile")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.Schema).To(gomega.Equal("haepari.recovery-plan/v4"))
		gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
		root := filepath.Join(f.guest, r.JobID)
		retained := root + "-retained-fixture"
		gomega.Expect(os.Rename(root, retained)).To(gomega.Succeed())
		gomega.Expect(os.Mkdir(root, 0700)).To(gomega.Succeed())
		response := recoveryHTTP(f.s, "/v1/jobs/"+r.JobID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "replaced-root"})
		gomega.Expect(response.Code).NotTo(gomega.Equal(200))
		next, err := f.s.planJobRecovery(r.JobID, "reconcile")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(next.PlanToken).To(gomega.BeEmpty())
		snapshot, err := f.s.ledger.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(snapshot.Receipt.JobState).To(gomega.Equal("reconciling"))
		gomega.Expect(snapshot.Receipt.GlobalOccupancy).To(gomega.Equal(1))
		gomega.Expect(os.ReadFile(filepath.Join(retained, "workspace/source/once.txt"))).To(gomega.Equal([]byte("once")))
	})
	ginkgo.It("requires explicit apply, retains the original Job and then recollects exact evidence before reuse", func() {
		f, r := previousBootVMFixture()
		snapshot, err := f.s.ledger.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.s.tryRecoverInterrupted(r.JobID, true, nil)).To(gomega.MatchError(managed.ErrBlocked))
		unchanged, err := f.s.ledger.RecoverySnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(unchanged).To(gomega.Equal(snapshot))
		plan, err := f.s.planJobRecovery(r.JobID, "reconcile")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.Unknowns).To(gomega.BeEmpty())
		gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
		response := recoveryHTTP(f.s, "/v1/jobs/"+r.JobID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "explicit-previous-boot"})
		gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
		f.s.workers.Wait()
		original, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(original.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(original.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("inconclusive")))
		gomega.Expect(original.ResourceAvailability).To(gomega.Equal("quarantined"))
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, r.JobID, "workspace/source/once.txt"))).To(gomega.Equal([]byte("once")))
		p, err := f.s.planEvidenceRecovery(f.spec.ID, "recollect")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.Schema).To(gomega.Equal("haepari.recovery-plan/v2"))
		gomega.Expect(p.PlanToken).NotTo(gomega.BeEmpty())
		response = recoveryHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: p.PlanToken, MutationKey: "collect-previous-boot"})
		gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
		_, err = os.Stat(filepath.Join(f.guest, r.JobID))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		current, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current).To(gomega.Equal(original))
		p, err = f.s.planResourceRecovery(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.PlanToken).NotTo(gomega.BeEmpty())
		response = recoveryHTTP(f.s, "/v1/resources/"+f.spec.ID+"/recover", managed.RecoveryRequest{PlanToken: p.PlanToken, MutationKey: "health-previous-boot"})
		gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
		status, err := f.s.ledger.ResourceStatus(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Occupancy).To(gomega.BeZero())
		gomega.Expect(status.Availability).To(gomega.Equal("available"))
	})
})
