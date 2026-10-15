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
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func operationalRecoverySSHInput(input []byte) []byte {
	archive := tar.NewReader(bytes.NewReader(input))
	for {
		header, err := archive.Next()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		if header.Name == "request.json" {
			raw, err := io.ReadAll(archive)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			return raw
		}
	}
}

func operationalRecoveryRefusal(operation, message string) utm.CommandResult {
	raw, err := json.Marshal(managed.Envelope{Schema: "haepari.envelope/v1", Operation: operation, OK: false, Error: &managed.MachineError{Code: managed.StorageUnavailable, Message: message, Retry: "reconcile"}})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return utm.CommandResult{Stdout: string(raw), Stderr: "untrusted secret transport stderr", ExitCode: 6}
}

var _ = ginkgo.Describe("Task036OperationalRecovery", ginkgo.Label("integration"), func() {

	for _, mode := range []string{"unchanged", "moved", "replaced"} {
		for _, action := range []string{"recollect", "dispose"} {
			mode, action := mode, action
			ginkgo.It("fences v2 supplementary "+action+" against a "+mode+" prepared workspace", func() {
				_, server, receipt, job, object := cancellationRuntime(ginkgo.GinkgoT(), "mkdir -p ../results; printf original > ../results/output.txt", func(job *managed.JobSpec) { job.Schema = "haepari.job/v2" })
				control, err := server.registerLocal(receipt)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				defer server.unregisterLocal(control)
				root := filepath.Join(server.root.Path, "jobs", receipt.JobID)
				control.beforePhase = func(phase string) {
					if phase == "collecting" {
						gomega.Expect(os.WriteFile(filepath.Join(root, "artifacts"), []byte("destination fault"), 0600)).To(gomega.Succeed())
					}
				}
				gomega.Expect(server.executeLocalControlled(control, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
				original, err := server.ledger.Result(receipt.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(original.ProtectedEvidence).NotTo(gomega.BeEmpty())
				gomega.Expect(os.Remove(filepath.Join(root, "artifacts"))).To(gomega.Succeed())
				_, jobs, err := server.evidenceRecoveryBasis(receipt.ResourceID, action)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(jobs).To(gomega.HaveLen(1))
				plan, err := server.planEvidenceRecovery(receipt.ResourceID, action)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
				workspace, saved := filepath.Join(root, "workspace"), filepath.Join(root, "saved")
				if mode != "unchanged" {
					gomega.Expect(os.Rename(workspace, saved)).To(gomega.Succeed())
					if mode == "replaced" {
						gomega.Expect(os.MkdirAll(filepath.Join(workspace, "results"), 0700)).To(gomega.Succeed())
						gomega.Expect(os.WriteFile(filepath.Join(workspace, "results/output.txt"), []byte("foreign"), 0600)).To(gomega.Succeed())
					}
					backend := localEvidenceBackend{server}
					jobs[0].CollectDeadline, jobs[0].CleanupDeadline = time.Now().Add(5*time.Second), time.Now().Add(5*time.Second)
					gomega.Expect(backend.Collect(jobs[0], filepath.Join(root, "refused-supplementary")).complete).To(gomega.BeFalse())
					gomega.Expect(backend.Cleanup(jobs[0])).To(gomega.HaveOccurred())
					denied, e := server.planEvidenceRecovery(receipt.ResourceID, action)
					gomega.Expect(e).To(gomega.HaveOccurred())
					gomega.Expect(denied.PlanToken).To(gomega.BeEmpty())
				}
				response := recoveryHTTP(server, "/v1/resources/"+receipt.ResourceID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "bound-supplementary-" + action})
				if mode == "unchanged" {
					gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
					var envelope managed.Envelope
					gomega.Expect(json.Unmarshal(response.Body.Bytes(), &envelope)).To(gomega.Succeed())
					var completed managed.EvidenceRecoveryReceipt
					gomega.Expect(json.Unmarshal(envelope.Data, &completed)).To(gomega.Succeed())
					gomega.Expect(completed.Complete).To(gomega.BeTrue())
					gomega.Expect(completed.Jobs[0].Stage).To(gomega.Equal("resolved"))
					_, err = os.Lstat(workspace)
					gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
				} else {
					gomega.Expect(response.Code).NotTo(gomega.Equal(200))
					gomega.Expect(os.ReadFile(filepath.Join(saved, "results/output.txt"))).To(gomega.Equal([]byte("original")))
					if mode == "replaced" {
						gomega.Expect(os.ReadFile(filepath.Join(workspace, "results/output.txt"))).To(gomega.Equal([]byte("foreign")))
					}
				}
				gomega.Expect(server.ledger.Result(receipt.JobID)).To(gomega.Equal(original))
				status, err := server.ledger.ResourceStatus(receipt.ResourceID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(status.Availability).To(gomega.Equal("quarantined"))
				gomega.Expect(status.Occupancy).To(gomega.BeZero())
			})
		}
	}

	ginkgo.It("rejects forged absence without every version, area, offset and native-proof fence", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf retained > ../results/source.txt; ln -s source.txt ../results/present.txt")
		f.job.Schema = "haepari.job/v2"
		f.job.Workload.RequiredOutputs = []string{"results/missing.txt", "results/present.txt"}
		original := f.run("absence-wire-fences")
		gomega.Expect(original.ResourceAvailability).To(gomega.Equal("quarantined"))
		q, err := f.s.vmBinding(original.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q.Operation, q.Area, q.Path, q.Deadline = "file", "workspace", "results/missing.txt", time.Now().Add(5*time.Second)
		baseline, err := f.s.vmCall(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(baseline.Absent).To(gomega.BeTrue())
		gomega.Expect(baseline.Observation.Exit.TerminationProven).To(gomega.BeTrue())
		saved, err := json.Marshal(baseline)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		transport, err := f.s.vmSSH(f.spec)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, mode := range []string{"valid", "v1", "non-file", "receipt-area", "offset", "expected-file", "false-proof", "file-present", "missing-root", "output-present"} {
			request := q
			request.Deadline = time.Now().Add(5 * time.Second)
			switch mode {
			case "v1":
				request.Schema = "haepari.guest-request/v1"
			case "non-file":
				request.Operation = "output"
			case "receipt-area":
				request.Area = "receipt"
			case "offset":
				request.Offset = 1
			case "expected-file":
				request.ExpectedFile = &runner.GuestFile{Path: request.Path, Inode: 1, SHA256: strings.Repeat("0", 64)}
			}
			calls := 0
			transport.Run = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
				calls++
				actual := vmTransportRequest(input)
				if actual.Operation == "identity" {
					return f.s.sshCommand(ctx, exe, args, input)
				}
				var reply runner.GuestReply
				gomega.Expect(json.Unmarshal(saved, &reply)).To(gomega.Succeed())
				reply.Schema, reply.Challenge, reply.ProtocolDigest = runner.GuestReplySchema(actual), actual.Challenge, runner.GuestProtocolDigest(actual)
				if mode == "offset" {
					reply.NextOffset = actual.Offset
				}
				if mode == "false-proof" {
					reply.Observation.Exit.TerminationProven = false
				}
				if mode == "file-present" {
					reply.File = &runner.GuestFile{Path: actual.Path, Inode: 1, SHA256: strings.Repeat("0", 64)}
				}
				if mode == "missing-root" {
					reply.Root = nil
				}
				if mode == "output-present" {
					reply.Output, reply.NextOffset = []byte("untrusted"), 9
				}
				data, err := json.Marshal(reply)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				raw, err := json.Marshal(managed.Envelope{Schema: "haepari.envelope/v1", Operation: "runner.guest", OK: true, Data: data})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				return utm.CommandResult{Stdout: string(raw)}, nil
			}
			leases, err := f.s.vmLeases()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			reply, err := transport.Call(context.Background(), f.spec, leases, request)
			gomega.Expect(leases.Close()).To(gomega.Succeed())
			gomega.Expect(calls).To(gomega.Equal(2), mode)
			if mode == "valid" {
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(reply.Absent).To(gomega.BeTrue())
			} else {
				gomega.Expect(err).To(gomega.HaveOccurred(), mode)
				gomega.Expect(reply.Absent).To(gomega.BeFalse(), mode)
			}
		}
		gomega.Expect(f.s.ledger.Result(original.JobID)).To(gomega.Equal(original))
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, original.JobID, "workspace/results/source.txt"))).To(gomega.Equal([]byte("retained")))
	})
	ginkgo.It("preserves only classified v2 first-proof refusals without issuing the operation", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "printf never-started")
		transport, err := f.s.vmSSH(f.spec)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q := runner.GuestRequest{Schema: "haepari.guest-request/v1", Operation: "start", VMUUID: strings.ToLower(f.spec.VM.UTMVMUUID), MAC: strings.ToLower(f.spec.VM.MACAddress), JobRoot: f.guest, RunnerDigest: transport.RunnerDigest, Deadline: time.Now().Add(5 * time.Second), Request: runner.Request{Identity: managed.LocalIdentity{JobID: "proof-refusal", ResourceID: f.spec.ID, ConfigRevision: f.spec.Revision}}}
		for _, cause := range []string{managed.DiagnosticPermissionDenied, managed.DiagnosticObservationFailed} {
			for _, schema := range []string{"haepari.guest-request/v1", "haepari.guest-request/v2"} {
				q.Schema = schema
				calls := 0
				transport.Run = func(_ context.Context, _ string, _ []string, input []byte) (utm.CommandResult, error) {
					calls++
					probe := vmTransportRequest(input)
					gomega.Expect(probe.Operation).To(gomega.Equal("identity"))
					return operationalRecoveryRefusal("runner.guest", cause), nil
				}
				leases, err := f.s.vmLeases()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				reply, err := transport.Call(context.Background(), f.spec, leases, q)
				gomega.Expect(leases.Close()).To(gomega.Succeed())
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(calls).To(gomega.Equal(1))
				gomega.Expect(reply.Request).To(gomega.BeNil())
				gomega.Expect(errors.Is(err, utm.ErrGuestUncertain)).To(gomega.BeFalse())
				gomega.Expect(errors.Is(err, utm.ErrGuestRefused)).To(gomega.Equal(schema == "haepari.guest-request/v2"))
				gomega.Expect(errors.Is(err, os.ErrPermission)).To(gomega.Equal(schema == "haepari.guest-request/v2" && cause == managed.DiagnosticPermissionDenied))
				if schema == "haepari.guest-request/v2" {
					gomega.Expect(diagnosticCause(err)).To(gomega.Equal(cause))
					gomega.Expect(diagnosticCause(errors.Join(err, utm.ErrGuestUncertain))).To(gomega.Equal(cause))
				}
				_, err = os.Lstat(filepath.Join(f.guest, q.Request.Identity.JobID))
				gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			}
		}
	})

	ginkgo.It("separates recovery proof refusals from uncertain recovery replies and untrusted transport errors", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "printf never-started")
		transport, err := f.s.vmSSH(f.spec)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		guest := runner.GuestRequest{Schema: "haepari.guest-request/v1", VMUUID: strings.ToLower(f.spec.VM.UTMVMUUID), MAC: strings.ToLower(f.spec.VM.MACAddress), BootID: "earlier-fixture-boot", JobRoot: f.guest, RunnerDigest: transport.RunnerDigest, Request: runner.Request{BootID: "earlier-fixture-boot", Identity: managed.LocalIdentity{JobID: "recovery-refusal", ResourceID: f.spec.ID, ConfigRevision: f.spec.Revision}}}
		for _, mode := range []string{"proof", "recovery", "malformed", "wrong-operation", "transport-loss"} {
			q := runner.GuestRecoveryRequest{Schema: "haepari.guest-recovery-request/v2", Operation: "probe", Guest: guest, RecoveryDigest: transport.RunnerDigest, Deadline: time.Now().Add(5 * time.Second)}
			original := q
			calls := 0
			transport.Run = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
				calls++
				raw := operationalRecoverySSHInput(input)
				if calls == 1 {
					var probe runner.GuestRequest
					gomega.Expect(json.Unmarshal(raw, &probe)).To(gomega.Succeed())
					gomega.Expect(probe.Schema).To(gomega.Equal("haepari.guest-request/v2"))
					gomega.Expect(probe.Operation).To(gomega.Equal("identity"))
					if mode == "proof" {
						return operationalRecoveryRefusal("runner.guest", managed.DiagnosticPermissionDenied), nil
					}
					return f.s.sshCommand(ctx, exe, args, input)
				}
				var request runner.GuestRecoveryRequest
				gomega.Expect(json.Unmarshal(raw, &request)).To(gomega.Succeed())
				gomega.Expect(request.Guest.Request).To(gomega.Equal(guest.Request))
				gomega.Expect(request.Guest.Schema).To(gomega.Equal(guest.Schema))
				switch mode {
				case "malformed":
					return utm.CommandResult{Stdout: `{`, ExitCode: 6}, nil
				case "wrong-operation":
					return operationalRecoveryRefusal("runner.guest", managed.DiagnosticPermissionDenied), nil
				case "transport-loss":
					return utm.CommandResult{Stderr: "private secret", ExitCode: 255}, errors.New("private secret lost connection")
				default:
					return operationalRecoveryRefusal("runner.guest-recovery", managed.DiagnosticPermissionDenied), nil
				}
			}
			leases, err := f.s.vmLeases()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			reply, err := transport.CallRecovery(context.Background(), f.spec, leases, q)
			gomega.Expect(leases.Close()).To(gomega.Succeed())
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(q).To(gomega.Equal(original))
			gomega.Expect(reply.Basis).To(gomega.BeNil())
			gomega.Expect(reply.Attestation).To(gomega.BeNil())
			gomega.Expect(reply.Reusable).To(gomega.BeFalse())
			gomega.Expect(errors.Is(err, os.ErrPermission)).To(gomega.Equal(mode == "proof" || mode == "recovery"))
			gomega.Expect(errors.Is(err, utm.ErrGuestRefused)).To(gomega.Equal(mode == "proof" || mode == "recovery"))
			gomega.Expect(errors.Is(err, utm.ErrGuestUncertain)).To(gomega.Equal(mode != "proof"))
			if mode == "proof" {
				gomega.Expect(calls).To(gomega.Equal(1))
			} else {
				gomega.Expect(calls).To(gomega.Equal(2))
			}
			gomega.Expect(err.Error()).NotTo(gomega.ContainSubstring("private secret"))
			_, err = os.Lstat(filepath.Join(f.guest, guest.Request.Identity.JobID))
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		}
	})

	ginkgo.It("retains bounded recovery diagnostics when a finished retained Job's plan consumes the error", func() {
		f, r := previousBootVMFixture()
		plan, err := f.s.planJobRecovery(r.JobID, "reconcile")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
		response := recoveryHTTP(f.s, "/v1/jobs/"+r.JobID+"/recover", managed.RecoveryRequest{PlanToken: plan.PlanToken, MutationKey: "diagnostic-finish-original"})
		gomega.Expect(response.Code).To(gomega.Equal(200), response.Body.String())
		f.s.workers.Wait()
		original, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(original.ResourceAvailability).To(gomega.Equal("quarantined"))
		gomega.Expect(original.ProtectedEvidence).NotTo(gomega.BeEmpty())
		identity, err := f.s.ledger.LocalIdentity(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		receipt, err := f.s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		requestPath := filepath.Join(f.guest, r.JobID, "request.json")
		request, err := os.ReadFile(requestPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		before, err := f.s.ledger.EventsAfter(r.JobID, 0, 100)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.s.ledger.RecordDiagnosticCause(r.JobID, managed.DiagnosticPathAbsent)).To(gomega.Succeed())
		skipped, err := f.s.ledger.EventsAfter(r.JobID, 0, 100)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(skipped).To(gomega.Equal(before))
		// Success must not publish diagnostics or invalidate an accepted plan.
		success, err := f.s.planGuestAttestation(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(success.PlanToken).NotTo(gomega.BeEmpty())
		unchanged, err := f.s.ledger.EventsAfter(r.JobID, 0, 100)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(unchanged).To(gomega.Equal(before))
		native := f.s.sshCommand
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			var wire struct {
				Schema string `json:"schema"`
			}
			gomega.Expect(json.Unmarshal(operationalRecoverySSHInput(input), &wire)).To(gomega.Succeed())
			if wire.Schema == "haepari.guest-recovery-request/v2" {
				return operationalRecoveryRefusal("runner.guest-recovery", managed.DiagnosticPermissionDenied), nil
			}
			return native(ctx, exe, args, input)
		}
		var recorded managed.EventBatch
		for attempt := 0; attempt < 2; attempt++ {
			failed, err := f.s.planGuestAttestation(f.spec.ID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(failed.PlanToken).To(gomega.BeEmpty())
			gomega.Expect(failed.Unknowns).NotTo(gomega.BeEmpty())
			events, err := f.s.ledger.EventsAfter(r.JobID, 0, 100)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if attempt == 0 {
				recorded = events
			} else {
				gomega.Expect(events).To(gomega.Equal(recorded))
			}
		}
		permissions, uncertain := 0, 0
		for _, event := range recorded.Events {
			if event.Kind != "diagnostic" {
				continue
			}
			var diagnostic struct {
				Schema string `json:"schema"`
				Cause  string `json:"cause"`
			}
			gomega.Expect(json.Unmarshal(event.Payload, &diagnostic)).To(gomega.Succeed())
			gomega.Expect(diagnostic.Schema).To(gomega.Equal("haepari.diagnostic/v1"))
			if diagnostic.Cause == managed.DiagnosticPermissionDenied {
				permissions++
			}
			if diagnostic.Cause == managed.DiagnosticGuestEffectUncertain {
				uncertain++
			}
		}
		gomega.Expect(permissions).To(gomega.Equal(1))
		gomega.Expect(uncertain).To(gomega.Equal(1))
		gomega.Expect(recorded.CurrentCursor).To(gomega.BeNumerically(">", before.CurrentCursor))
		raw, err := json.Marshal(recorded)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring("secret"))
		gomega.Expect(f.s.ledger.Result(r.JobID)).To(gomega.Equal(original))
		gomega.Expect(f.s.ledger.LocalIdentity(r.JobID)).To(gomega.Equal(identity))
		gomega.Expect(f.s.ledger.Snapshot(r.JobID)).To(gomega.Equal(receipt))
		gomega.Expect(os.ReadFile(requestPath)).To(gomega.Equal(request))
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, r.JobID, "workspace/source/once.txt"))).To(gomega.Equal([]byte("once")))
		f.s.sshCommand = native
		restored, err := f.s.planGuestAttestation(f.spec.ID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(restored.PlanToken).NotTo(gomega.BeEmpty())
		final, err := f.s.ledger.EventsAfter(r.JobID, 0, 100)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(final).To(gomega.Equal(recorded))
	})

	for _, outcome := range []struct {
		script string
		native managed.WorkloadOutcome
		code   int
	}{
		{"exit 0", "pass", 0}, {"exit 7", "fail", 7},
	} {
		item := outcome
		ginkgo.It("settles positively absent v2 VM outputs without losing native "+string(item.native), func() {
			f := nativeVMFixture(ginkgo.GinkgoT(), item.script)
			f.job.Schema = "haepari.job/v2"
			result := f.run("absent-v2")
			gomega.Expect(result.Schema).To(gomega.Equal("haepari.result/v2"))
			gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(item.native))
			gomega.Expect(*result.NativeExitCode).To(gomega.Equal(item.code))
			gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
			gomega.Expect(result.EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "results/output.txt", Reason: "required_output_absent"}))
			gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
			gomega.Expect(result.ResourceAvailability).To(gomega.Equal("available"))
			gomega.Expect(result.ProtectedEvidence).To(gomega.BeEmpty())
			_, err := os.Lstat(filepath.Join(f.guest, result.JobID))
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			receipt, err := f.s.ledger.Snapshot(result.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(receipt.GlobalOccupancy).To(gomega.BeZero())
			again, err := f.s.ledger.Result(result.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(again).To(gomega.Equal(result))
			f.job.Workload.Argv = []string{"/bin/sh", "-c", "mkdir -p ../results; printf reuse > ../results/output.txt"}
			gomega.Expect(f.run("after-absence").FinalVerdict).To(gomega.Equal(managed.Pass))
		})
	}
	ginkgo.It("settles absent local outputs under the v2 policy and retains the v1 history policy", func() {
		for _, schema := range []string{"haepari.job/v1", "haepari.job/v2"} {
			_, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "exit 7", func(job *managed.JobSpec) { job.Schema = schema })
			gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
			result, err := s.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
			gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("fail")))
			gomega.Expect(*result.NativeExitCode).To(gomega.Equal(7))
			raw, err := json.Marshal(result)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(managed.DecodeResultForJob(raw, job)).To(gomega.Equal(result))
			mismatched := job
			if schema == "haepari.job/v1" {
				mismatched.Schema = "haepari.job/v2"
			} else {
				mismatched.Schema = "haepari.job/v1"
			}
			_, err = managed.DecodeResultForJob(raw, mismatched)
			gomega.Expect(err).To(gomega.HaveOccurred())
			if schema == "haepari.job/v2" {
				gomega.Expect(result.Schema).To(gomega.Equal("haepari.result/v2"))
				gomega.Expect(result.Reason).To(gomega.Equal("required_output_absent"))
				gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
				gomega.Expect(result.ResourceAvailability).To(gomega.Equal("available"))
				gomega.Expect(result.EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "results/output.txt", Reason: "required_output_absent"}))
			} else {
				gomega.Expect(result.Schema).To(gomega.Equal("haepari.result/v1"))
				gomega.Expect(result.Reason).To(gomega.Equal("evidence_retained"))
				gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("failed")))
				gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
			}
		}
	})

	for _, mode := range []string{"moved", "replaced"} {
		mode := mode
		ginkgo.It("retains the v2 workspace "+mode+" after fresh collection before cleanup", func() {
			t := ginkgo.GinkgoT()
			db, s, r, job, object := cancellationRuntime(t, "mkdir -p ../results; printf retained > ../results/output.txt; printf once > once.txt; printf native-log", func(job *managed.JobSpec) {
				job.Schema = "haepari.job/v2"
				job.Deadlines.CleanupSeconds = 10
			})
			_, release, done := cancellationBarrier(t, s, r, job, object, "cleaning")
			phases, err := s.ledger.LocalPhases(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			collected := phases["collect"]
			gomega.Expect(collected.Completed).To(gomega.BeTrue())
			gomega.Expect(collected.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("complete")))
			gomega.Expect(collected.Artifacts).To(gomega.HaveLen(1))
			gomega.Expect(collected.Logs).NotTo(gomega.BeEmpty())
			gomega.Expect(phases["cleanup"].Completed).To(gomega.BeFalse())
			root := filepath.Join(s.root.Path, "jobs", r.JobID)
			artifact := filepath.Join(root, "artifacts/results/output.txt")
			gomega.Expect(os.ReadFile(artifact)).To(gomega.Equal([]byte("retained")))
			log, err := os.ReadFile(filepath.Join(root, "output.log"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(log).To(gomega.Equal([]byte("native-log")))
			workspace, saved := filepath.Join(root, "workspace"), filepath.Join(root, "saved")
			gomega.Expect(os.Rename(workspace, saved)).To(gomega.Succeed())
			if mode == "replaced" {
				gomega.Expect(os.Mkdir(workspace, 0700)).To(gomega.Succeed())
				gomega.Expect(os.WriteFile(filepath.Join(workspace, "foreign.txt"), []byte("preserve"), 0600)).To(gomega.Succeed())
			}
			release()
			awaitCancellation(t, done)
			select {
			case <-done:
				ginkgo.Fail("fresh execution returned a second completion")
			default:
			}
			result, err := s.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("pass")))
			gomega.Expect(*result.NativeExitCode).To(gomega.BeZero())
			gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
			gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("failed")))
			gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
			gomega.Expect(result.ProtectedEvidence).To(gomega.ContainElement(managed.ProtectedEvidence{Path: "workspace", Reason: "cleanup_incomplete", RecoveryReference: "job:" + r.JobID}))
			gomega.Expect(result.Artifacts).To(gomega.Equal(collected.Artifacts))
			gomega.Expect(result.Logs).To(gomega.Equal(collected.Logs))
			after, err := s.ledger.LocalPhases(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(after["collect"]).To(gomega.Equal(collected))
			gomega.Expect(after["cleanup"].Deadline).To(gomega.Equal(phases["cleanup"].Deadline))
			gomega.Expect(os.ReadFile(artifact)).To(gomega.Equal([]byte("retained")))
			gomega.Expect(os.ReadFile(filepath.Join(root, "output.log"))).To(gomega.Equal(log))
			gomega.Expect(os.ReadFile(filepath.Join(saved, "results/output.txt"))).To(gomega.Equal([]byte("retained")))
			gomega.Expect(os.ReadFile(filepath.Join(saved, "source/once.txt"))).To(gomega.Equal([]byte("once")))
			if mode == "replaced" {
				gomega.Expect(os.ReadFile(filepath.Join(workspace, "foreign.txt"))).To(gomega.Equal([]byte("preserve")))
			}
			receipt, err := s.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(receipt.GlobalOccupancy).To(gomega.BeZero())
			gomega.Expect(receipt.JobState).To(gomega.Equal("finished"))
			var finishes int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='finished'`, r.JobID).Scan(&finishes)).To(gomega.Succeed())
			gomega.Expect(finishes).To(gomega.Equal(1))
		})
	}

	for _, mode := range []string{"unchanged", "moved", "replaced"} {
		mode := mode
		ginkgo.It("resumes absent v2 local evidence with the original cleanup deadline and "+mode+" workspace", func() {
			t := ginkgo.GinkgoT()
			db, s, r, _, _ := cancellationRuntime(t, "printf once > once.txt", func(job *managed.JobSpec) {
				job.Schema = "haepari.job/v2"
				job.Deadlines.CleanupSeconds = 15
			})
			executable, err := os.Executable()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			child := exec.Command(executable, "restart-phase-fixture", s.root.Path, r.JobID, "cleaning")
			child.Env = []string{"PATH=/usr/bin:/bin", "GORACE=atexit_sleep_ms=0"}
			gomega.Expect(child.Start()).To(gomega.Succeed())
			t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
			gomega.Eventually(func() string {
				raw, _ := os.ReadFile(filepath.Join(s.root.Path, "restart-phase-reached"))
				return string(raw)
			}, 5*time.Second, 25*time.Millisecond).Should(gomega.Equal("cleaning"))
			before, err := s.ledger.LocalPhases(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(before["collect"].EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "results/output.txt", Reason: "required_output_absent"}))
			gomega.Expect(before["collect"].ProtectedEvidence).To(gomega.BeEmpty())
			gomega.Expect(before["cleanup"].Completed).To(gomega.BeFalse())
			gomega.Expect(before["cleanup"].ControllerOwner.PID).To(gomega.Equal(child.Process.Pid))
			identity, err := s.ledger.LocalIdentity(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(child.Process.Kill()).To(gomega.Succeed())
			gomega.Expect(child.Wait()).To(gomega.HaveOccurred())
			root := filepath.Join(s.root.Path, "jobs", r.JobID)
			workspace, saved := filepath.Join(root, "workspace"), filepath.Join(root, "saved")
			if mode != "unchanged" {
				gomega.Expect(os.Rename(workspace, saved)).To(gomega.Succeed())
				if mode == "replaced" {
					gomega.Expect(os.Mkdir(workspace, 0700)).To(gomega.Succeed())
					gomega.Expect(os.WriteFile(filepath.Join(workspace, "foreign.txt"), []byte("preserve"), 0600)).To(gomega.Succeed())
				}
			}
			gomega.Expect(s.ledger.MarkInterrupted()).To(gomega.Succeed())
			fresh, err := newRuntime(s.root, s.ledger, s.store)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(fresh.reconcileStartup()).To(gomega.Succeed())
			fresh.workers.Wait()
			result, err := fresh.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(result.Schema).To(gomega.Equal("haepari.result/v2"))
			gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
			gomega.Expect(result.EvidenceGaps).To(gomega.Equal(before["collect"].EvidenceGaps))
			after, err := fresh.ledger.LocalPhases(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(after["collect"]).To(gomega.Equal(before["collect"]))
			gomega.Expect(after["cleanup"].Deadline).To(gomega.Equal(before["cleanup"].Deadline))
			gomega.Expect(fresh.ledger.LocalIdentity(r.JobID)).To(gomega.Equal(identity))
			var finishes int
			gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='finished'`, r.JobID).Scan(&finishes)).To(gomega.Succeed())
			gomega.Expect(finishes).To(gomega.Equal(1))
			if mode == "unchanged" {
				gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
				gomega.Expect(result.ProtectedEvidence).To(gomega.BeEmpty())
				_, err = os.Lstat(workspace)
				gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			} else {
				gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("failed")))
				gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
				gomega.Expect(result.ProtectedEvidence).To(gomega.ContainElement(managed.ProtectedEvidence{Path: "workspace", Reason: "cleanup_interrupted", RecoveryReference: "job:" + r.JobID}))
				gomega.Expect(os.ReadFile(filepath.Join(saved, "source/once.txt"))).To(gomega.Equal([]byte("once")))
				gomega.Expect(os.ReadFile(filepath.Join(saved, "source/input.txt"))).To(gomega.Equal([]byte("sealed")))
				if mode == "replaced" {
					gomega.Expect(os.ReadFile(filepath.Join(workspace, "foreign.txt"))).To(gomega.Equal([]byte("preserve")))
				}
			}
		})
	}

	ginkgo.It("retains mixed absent and existing uncollected v2 VM outputs", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf retained > ../results/source.txt; ln -s source.txt ../results/present.txt")
		f.job.Schema = "haepari.job/v2"
		f.job.Workload.RequiredOutputs = []string{"results/missing.txt", "results/present.txt"}
		result := f.run("mixed-v2-evidence")
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.EvidenceGaps).To(gomega.ConsistOf(managed.EvidenceGap{Path: "results/missing.txt", Reason: "required_output_absent"}, managed.EvidenceGap{Path: "results/present.txt", Reason: "required_output_uncollected"}))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("failed")))
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
		gomega.Expect(result.ProtectedEvidence).NotTo(gomega.BeEmpty())
		gomega.Expect(os.ReadFile(filepath.Join(f.guest, result.JobID, "workspace/results/source.txt"))).To(gomega.Equal([]byte("retained")))
	})

	ginkgo.It("preserves the v2 prepared local workspace binding through event and completed payload purge", func() {
		db, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "mkdir -p ../results; printf done > ../results/output.txt", func(job *managed.JobSpec) { job.Schema = "haepari.job/v2" })
		gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		identity, err := s.ledger.LocalIdentity(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		binding, err := s.ledger.LocalWorkspace(identity)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		events, err := s.ledger.EventsAfter(r.JobID, 0, 100)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(s.ledger.PurgeEventsThrough(r.JobID, events.CurrentCursor)).To(gomega.Succeed())
		gomega.Expect(s.ledger.LocalWorkspace(identity)).To(gomega.Equal(binding))
		finished, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		purged, err := s.purgeJob(r.JobID, finished.JobRevision)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(purged.Complete).To(gomega.BeTrue())
		gomega.Expect(s.ledger.LocalWorkspace(identity)).To(gomega.Equal(binding))
		var beforeDiagnostics, afterDiagnostics int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='diagnostic'`, r.JobID).Scan(&beforeDiagnostics)).To(gomega.Succeed())
		gomega.Expect(s.ledger.RecordRecoveryDiagnosticCause(r.JobID, managed.DiagnosticPermissionDenied)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='diagnostic'`, r.JobID).Scan(&afterDiagnostics)).To(gomega.Succeed())
		gomega.Expect(afterDiagnostics).To(gomega.Equal(beforeDiagnostics))
	})

	for _, replace := range []bool{false, true} {
		replacement := replace
		ginkgo.It("protects original local workspace evidence after its pathname is moved or replaced", func() {
			script := "mkdir -p ../results; printf retained > ../results/output.txt; mv ../../workspace ../../saved"
			if replacement {
				script += "; mkdir ../../workspace"
			}
			_, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), script, func(job *managed.JobSpec) { job.Schema = "haepari.job/v2" })
			gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
			result, err := s.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
			gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("failed")))
			gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
			gomega.Expect(result.ProtectedEvidence).NotTo(gomega.BeEmpty())
			gomega.Expect(result.EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "workspace", Reason: "workspace_identity_conflict"}))
			gomega.Expect(os.ReadFile(filepath.Join(s.root.Path, "jobs", r.JobID, "saved/results/output.txt"))).To(gomega.Equal([]byte("retained")))
		})
	}

	ginkgo.It("keeps existing inaccessible evidence protected instead of treating it as absence", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; ln -s /etc/hosts ../results/output.txt")
		f.job.Schema = "haepari.job/v2"
		result := f.run("existing-uncollected")
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("failed")))
		gomega.Expect(result.ResourceAvailability).To(gomega.Equal("quarantined"))
		gomega.Expect(result.ProtectedEvidence).NotTo(gomega.BeEmpty())
		gomega.Expect(result.EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "results/output.txt", Reason: "required_output_uncollected"}))
		_, err := os.Lstat(filepath.Join(f.guest, result.JobID, "workspace/results/output.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})
	ginkgo.It("retries a lost watch reply with the original run without duplicating start", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "sleep 0.2; mkdir -p ../results; printf once > ../results/output.txt")
		original := f.s.sshCommand
		watches, starts := 0, 0
		f.s.sshCommand = func(ctx context.Context, executable string, args []string, input []byte) (utm.CommandResult, error) {
			archive := tar.NewReader(bytes.NewReader(input))
			var q runner.GuestRequest
			for {
				h, err := archive.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					return utm.CommandResult{}, err
				}
				if h.Name == "request.json" {
					raw, _ := io.ReadAll(archive)
					_ = json.Unmarshal(raw, &q)
				}
			}
			if q.Operation == "start" {
				starts++
			}
			if q.Operation == "watch" {
				watches++
				if watches == 1 {
					return utm.CommandResult{}, errors.New("private secret transport detail")
				}
			}
			return original(ctx, executable, args, input)
		}
		result := f.run("retry-original")
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		gomega.Expect(starts).To(gomega.Equal(1))
		gomega.Expect(watches).To(gomega.BeNumerically(">=", 2))
		events, err := f.s.ledger.EventsAfter(result.JobID, 0, 100)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		raw, _ := json.Marshal(events)
		gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring("private secret"))
		gomega.Expect(string(raw)).To(gomega.ContainSubstring("guest_effect_uncertain"))
	})
	ginkgo.It("never reports CANCELLED with positively absent required evidence", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "printf started > started.marker; sleep 8")
		f.job.Schema = "haepari.job/v2"
		r, err := f.reserve(f.admit("cancel-absent"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		done := make(chan error, 1)
		go func() {
			defer ginkgo.GinkgoRecover()
			done <- f.s.executeVM(r, f.job, f.object, time.Now().Add(10*time.Second))
		}()
		gomega.Eventually(func() string {
			receipt, e := f.s.ledger.Snapshot(r.JobID)
			if e != nil {
				return ""
			}
			return receipt.JobState
		}, 4*time.Second, 25*time.Millisecond).Should(gomega.Equal("running"))
		// A durable run intent precedes native exec. Cancel only after the
		// workload itself has entered, so required outputs actually apply.
		gomega.Eventually(func() ([]byte, error) {
			return os.ReadFile(filepath.Join(f.guest, r.JobID, "workspace/source/started.marker"))
		}, 4*time.Second, 25*time.Millisecond).Should(gomega.Equal([]byte("started")))
		current, err := f.s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = f.s.requestCancel(managed.CancelRequest{JobID: r.JobID, ExpectedRevision: current.JobRevision, MutationKey: "cancel-absent-key"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var runErr error
		gomega.Eventually(done, 8*time.Second).Should(gomega.Receive(&runErr))
		gomega.Expect(runErr).NotTo(gomega.HaveOccurred())
		result, err := f.s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("cancelled")))
		gomega.Expect(result.Schema).To(gomega.Equal("haepari.result/v2"))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
	})

	ginkgo.It("bounds safe retries to the saved deadline and never retries an effect", func() {
		for _, op := range []string{"watch", "start", "cancel", "cleanup"} {
			calls := 0
			q := runner.GuestRequest{Operation: op, Deadline: time.Now().Add(160 * time.Millisecond)}
			saved := q.Deadline
			_, err := retryObservation(q, nil, func(actual runner.GuestRequest) (runner.GuestReply, error) {
				calls++
				gomega.Expect(actual.Deadline).To(gomega.Equal(saved))
				return runner.GuestReply{}, utm.ErrGuestUncertain
			}, nil)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(errors.Is(err, utm.ErrGuestUncertain)).To(gomega.BeTrue())
			if op == "watch" {
				gomega.Expect(calls).To(gomega.Equal(2))
			} else {
				gomega.Expect(calls).To(gomega.Equal(1))
			}
		}
	})
})
