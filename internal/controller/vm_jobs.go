package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/utm"
	"golang.org/x/sys/unix"
)

func (s *runtimeServer) vmSpec(id string) (managed.ResourceSpec, error) {
	resources, err := s.ledger.VMResources()
	if err != nil {
		return managed.ResourceSpec{}, err
	}
	for _, r := range resources {
		if r.Spec.ID == id {
			return r.Spec, nil
		}
	}
	return managed.ResourceSpec{}, managed.ErrNotFound
}
func (s *runtimeServer) vmSSH(spec managed.ResourceSpec) (utm.SSH, error) {
	executable, err := os.Executable()
	if err != nil {
		return utm.SSH{}, err
	}
	f, err := os.OpenFile(executable, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return utm.SSH{}, err
	}
	defer f.Close()
	binary, err := io.ReadAll(io.LimitReader(f, (64<<20)+1))
	if err != nil || len(binary) > 64<<20 {
		return utm.SSH{}, managed.ErrBlocked
	}
	sum := sha256.Sum256(binary)
	return utm.SSH{Executable: "/usr/bin/ssh", Control: s.vmControl(spec), Run: s.sshCommand, Runner: binary, RunnerDigest: "sha256:" + hex.EncodeToString(sum[:]), Transport: s.vmTransport, Measure: s.vmSSHMeasurement}, nil
}
func (s *runtimeServer) vmCall(q runner.GuestRequest) (runner.GuestReply, error) {
	spec, err := s.vmSpec(q.Request.Identity.ResourceID)
	if err != nil {
		return runner.GuestReply{}, err
	}
	if spec.Revision != q.Request.Identity.ConfigRevision {
		return runner.GuestReply{}, managed.ErrStale
	}
	transport := utm.SSH{Executable: "/usr/bin/ssh", Control: s.vmControl(spec), Run: s.sshCommand}
	if q.Request.Identity.RunID != "" {
		path := filepath.Join(s.root.Path, "jobs", q.Request.Identity.JobID, "guest-runner")
		f, e := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
		if e == nil {
			if _, e = privateLogStat(int(f.Fd()), false); e != nil {
				f.Close()
				return runner.GuestReply{}, e
			}
			data, e := io.ReadAll(io.LimitReader(f, (64<<20)+1))
			f.Close()
			if e != nil || len(data) > 64<<20 {
				return runner.GuestReply{}, managed.ErrBlocked
			}
			digest := sha256.Sum256(data)
			if "sha256:"+hex.EncodeToString(digest[:]) != q.RunnerDigest {
				return runner.GuestReply{}, managed.ErrBlocked
			}
			transport.Runner = data
			transport.RunnerDigest = q.RunnerDigest
		} else if !errors.Is(e, os.ErrNotExist) {
			return runner.GuestReply{}, e
		}
	}
	if len(transport.Runner) == 0 {
		transport, err = s.vmSSH(spec)
		if err != nil {
			return runner.GuestReply{}, err
		}
	}
	if s.vmTransport {
		admission, e := s.ledger.VMTransportAdmission(q.Request.Identity.JobID)
		if e != nil {
			return runner.GuestReply{}, e
		}
		transport.Transport = len(admission) != 0
		if transport.Transport && !validVMTransportAdmission(admission, q) {
			return runner.GuestReply{}, managed.ErrBlocked
		}
		transport.Measure = s.vmSSHMeasurement
		if q.Operation == "health" {
			transport, err = s.vmSSH(spec)
			if err != nil {
				return runner.GuestReply{}, err
			}
		} else {
			raw, e := s.ledger.VMTransportBinding(q.Request.Identity.JobID)
			if e != nil {
				return runner.GuestReply{}, e
			}
			if len(raw) != 0 && q.Root != nil {
				var b runner.GuestTransportBinding
				if json.Unmarshal(raw, &b) != nil || runner.ValidateGuestTransportBinding(b, q) != nil {
					return runner.GuestReply{}, managed.ErrBlocked
				}
				transport.Binding = &b
			}
		}
		if transport.Transport {
			transport.RecordPrepared = func(b runner.GuestTransportBinding) error {
				raw, e := json.Marshal(b)
				if e != nil {
					return e
				}
				return s.ledger.RecordVMTransportBinding(q.Request.Identity, raw)
			}
		}
	}
	if transport.RunnerDigest != q.RunnerDigest && !(s.vmTransport && q.Operation == "health") {
		return runner.GuestReply{}, managed.ErrBlocked
	}
	var leases io.ReadCloser
	if s.vmLeases != nil {
		leases, err = s.vmLeases()
	} else {
		leases, err = os.OpenFile("/var/db/dhcpd_leases", os.O_RDONLY|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return runner.GuestReply{}, err
	}
	defer leases.Close()
	return transport.Call(context.Background(), spec, leases, q)
}

type vmTransportAdmission struct {
	Schema       string                `json:"schema"`
	Identity     managed.LocalIdentity `json:"identity"`
	RunnerDigest string                `json:"runner_digest"`
}

func validVMTransportAdmission(raw []byte, q runner.GuestRequest) bool {
	var admission vmTransportAdmission
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return managed.ValidateUniqueJSONKeys(raw) == nil && d.Decode(&admission) == nil && d.Decode(new(any)) == io.EOF && admission.Schema == "haepari.vm-transport-admission/v1" && admission.Identity == q.Request.Identity && admission.RunnerDigest == q.RunnerDigest
}

func (s *runtimeServer) vmSSHMeasurement(observation utm.SSHExchange) error {
	if !s.vmTransport || observation.Identity.JobID == "" {
		return nil
	}
	raw, err := json.Marshal(observation)
	if err != nil {
		return err
	}
	return s.ledger.RecordVMSSHMeasurement(observation.Identity, raw, observation.Complete)
}
func (s *runtimeServer) vmBinding(jobID string) (runner.GuestRequest, error) {
	plan, prepared, err := s.ledger.VMJobBinding(jobID)
	if err != nil {
		return runner.GuestRequest{}, err
	}
	var q runner.GuestRequest
	if err = json.Unmarshal(plan, &q); err != nil {
		return q, err
	}
	if len(prepared) > 0 {
		var r runner.GuestReply
		if err = json.Unmarshal(prepared, &r); err != nil {
			return q, err
		}
		if r.Root == nil {
			return q, managed.ErrBlocked
		}
		if r.Request != nil {
			q.Request = *r.Request
		}
		q.Root = r.Root
	}
	id, err := s.ledger.LocalIdentity(jobID)
	if err != nil || id != q.Request.Identity {
		return q, managed.ErrStale
	}
	return q, nil
}
func (s *runtimeServer) recordVMPrepared(q *runner.GuestRequest, r runner.GuestReply) error {
	if r.Root == nil || r.Control == nil || runner.ValidateGuestControl(*r.Control, *q) != nil {
		return managed.ErrBlocked
	}
	if r.Request != nil {
		q.Request = *r.Request
	}
	q.Root = r.Root
	// Only permanent identity facts are retained; fresh challenges and transient
	// filesystem-worker owners cannot make an identical preparation a new fact.
	binding := runner.GuestReply{Request: r.Request, Root: r.Root}
	raw, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	if err = s.ledger.RecordVMPrepared(q.Request.Identity, raw); err != nil {
		return err
	}
	if r.Request == nil {
		return nil
	}
	raw, err = json.Marshal(q.Request)
	if err != nil {
		return err
	}
	return s.ledger.RecordRunnerReceipt(q.Request.Identity, "request", raw)
}
func (s *runtimeServer) importVMView(c *localRunControl, view runner.Observation) error {
	for _, item := range []struct {
		kind    string
		value   any
		present bool
	}{{"intent", view.Intent, view.Intent != nil}, {"start", view.Start, view.Start != nil}, {"outcome", view.Outcome, view.Outcome != nil}, {"exit", view.Exit, view.Exit != nil}} {
		if !item.present {
			continue
		}
		raw, err := json.Marshal(item.value)
		if err != nil {
			return err
		}
		if err = s.ledger.RecordRunnerReceipt(c.identity, item.kind, raw); err != nil {
			return err
		}
	}
	if view.Outcome != nil && !c.manual {
		out := view.Outcome.Outcome
		c.publishCompletion(nativeCompletion{at: view.Outcome.ObservedAt, outcome: &out})
	}
	return s.observeLocalCompletion(c)
}
func vmEnvironment(job managed.JobSpec, chain managed.ParentChain) []string {
	path := managed.DefaultWorkloadPATH
	if v, ok := job.Workload.Environment["PATH"]; ok {
		path = v
	}
	env := []string{"PATH=" + path}
	keys := []string{}
	for k := range job.Workload.Environment {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k != "PATH" && k != managed.ParentContextEnvironment {
			env = append(env, k+"="+job.Workload.Environment[k])
		}
	}
	raw, _ := json.Marshal(chain)
	return append(env, managed.ParentContextEnvironment+"="+string(raw))
}
func (s *runtimeServer) executeVM(r managed.Receipt, job managed.JobSpec, object string, prepareDeadline time.Time) error {
	c, err := s.registerLocal(r)
	if err != nil {
		return err
	}
	defer s.unregisterLocal(c)
	if c.receipt.JobState != "preparing" || c.identity.RunID != "" {
		return managed.ErrConflict
	}
	spec, err := s.vmSpec(r.ResourceID)
	if err != nil || spec.Revision != r.ResourceConfigRevision {
		return managed.ErrStale
	}
	snapshot, err := s.ledger.RecoverySnapshot(r.JobID)
	if err != nil {
		return err
	}
	// A power effect already has its durable claim. Advance only its next effect;
	// uncertainty never permits resubmission, even before any guest Job exists.
	steps := []managed.ResultStep{}
	powerContext, cancelPower := context.WithDeadline(context.Background(), prepareDeadline)
	defer cancelPower()
	for _, p := range snapshot.AdapterIntents {
		var startAt time.Time
		for p.State != "reconciled" && time.Now().Before(prepareDeadline) {
			if e := s.vmPreflightState(c); e != nil {
				// Cancellation may still observe an already returned command,
				// but cannot issue another power effect or guess a lost return.
				if !errors.Is(e, errVMReadinessCancelled) || !p.CommandReturned() {
					return e
				}
				if _, ready := p.ReadyEffect(); ready {
					return managed.ErrBlocked
				}
			}
			if p.Stage == managed.PowerStartReady && startAt.IsZero() {
				startAt = time.Now()
			}
			prior := managed.RecoveryDigest(p)
			p, err = s.advanceVMPower(powerContext, p.Identity)
			if err != nil {
				return err
			}
			if managed.RecoveryDigest(p) == prior {
				if !p.CommandReturned() {
					return managed.ErrBlocked
				}
				if err = s.waitVMPreparation(c, prepareDeadline, 100*time.Millisecond); err != nil {
					return err
				}
			}
		}
		if p.State != "reconciled" {
			return managed.ErrBlocked
		}
		if p.Stage == managed.PowerFailedNoEffect {
			return s.vmNoEffect(c, "prepare_failure", false, "host_vm_limit", nil)
		}
		if !startAt.IsZero() && p.Stage == managed.PowerDone {
			steps = append(steps, managed.ResultStep{Step: "vm_start", DurationMillis: time.Since(startAt).Milliseconds()})
		}
	}
	transport, err := s.vmSSH(spec)
	if err != nil {
		return s.vmNoEffect(c, "prepare_failure", true, "", err)
	}
	guestSchema := "haepari.guest-request/v1"
	if managed.AbsenceAware(job) {
		guestSchema = "haepari.guest-request/v2"
	}
	probe := runner.GuestRequest{Schema: guestSchema, Operation: "identity", VMUUID: strings.ToLower(spec.VM.UTMVMUUID), MAC: strings.ToLower(spec.VM.MACAddress), JobRoot: spec.VM.GuestJobRoot, RunnerDigest: transport.RunnerDigest, Deadline: prepareDeadline, Request: runner.Request{Identity: c.identity}}
	sshAt := time.Now()
	identity, err := s.waitVMReadiness(c, probe)
	if err != nil {
		if errors.Is(err, errVMReadinessCancelled) {
			return s.vmNoEffect(c, "cancel", false, "", err)
		}
		if errors.Is(err, errControllerDetached) || errors.Is(err, managed.ErrStale) {
			return err
		}
		var failure *utm.CommandFailure
		if errors.As(err, &failure) && failure.HostPermissionDenied {
			return s.vmNoEffect(c, "prepare_failure", false, "host_permission_unavailable", err)
		}
		return s.vmNoEffect(c, "prepare_failure", true, "", err)
	}
	steps = append(steps, managed.ResultStep{Step: "ssh_ready", DurationMillis: time.Since(sshAt).Milliseconds()})
	prepareContext, cancelPrepare := context.WithDeadline(context.Background(), prepareDeadline)
	defer cancelPrepare()
	snap, err := s.store.ReadContext(prepareContext, object)
	if err != nil {
		return s.vmNoEffect(c, "prepare_failure", false, "", err)
	}
	inventory, err := json.Marshal(snap.Manifest)
	if err != nil {
		return err
	}
	runID, err := randomID("run-")
	if err != nil {
		return err
	}
	id := c.identity
	id.RunID = runID
	chain, err := s.ledger.RunnerParentChain(s.installationID(), id.JobID, id.LeaseGeneration)
	if err != nil {
		return err
	}
	root := filepath.Join(spec.VM.GuestJobRoot, id.JobID)
	q := probe
	q.Operation = "prepare"
	q.BootID = identity.BootID
	q.ObjectID = object
	q.InputDigest = snap.Manifest.TreeSHA256
	q.ProfileDigest = "sha256:" + strings.TrimPrefix(job.Environment.ProfileDigest, "sha256:")
	q.Request = runner.Request{Schema: managed.RunnerSchema("request"), Identity: id, BootID: identity.BootID, WorkDir: filepath.Join(root, "workspace", filepath.FromSlash(job.Workload.CWD)), Argv: job.Workload.Argv, Environment: vmEnvironment(job, chain), Deadline: prepareDeadline.Add(time.Duration(job.Deadlines.RunSeconds) * time.Second).UTC(), RunSeconds: job.Deadlines.RunSeconds, TerminationSeconds: job.Deadlines.CleanupSeconds}
	if c.manual {
		argv, err := preparationArgv(job, filepath.Join(root, "haepari"))
		if err != nil {
			return err
		}
		q.Request.Argv = argv
		q.Request.Deadline = prepareDeadline.UTC()
		q.Request.RunSeconds = job.Deadlines.PrepareSeconds
	}
	q.Inventory = inventory
	var payloadBytes int64
	for _, entry := range snap.Manifest.Entries {
		payloadBytes += entry.Size
		if payloadBytes > runner.GuestWireLimit/2 || !time.Now().Before(prepareDeadline) {
			return s.vmNoEffect(c, "prepare_failure", false, "", managed.ErrBlocked)
		}
		f, _, e := openContainedOutput(snap.Root, entry.Path)
		if e != nil {
			return e
		}
		data, e := io.ReadAll(io.LimitReader(f, entry.Size+1))
		f.Close()
		if e != nil || int64(len(data)) != entry.Size {
			return managed.ErrBlocked
		}
		q.Files = append(q.Files, data)
	}
	raw, _ := json.Marshal(q)
	if len(raw) > runner.GuestWireLimit {
		return s.vmNoEffect(c, "prepare_failure", false, "", managed.ErrBlocked)
	}
	planRequest := q
	planRequest.Inventory, planRequest.Files = nil, nil
	plan, err := json.Marshal(planRequest)
	if err != nil {
		return err
	}
	c.mu.Lock()
	status, err := s.ledger.Cancellation(id.JobID)
	if err == nil && status.Requested {
		c.mu.Unlock()
		return s.vmNoEffect(c, "cancel", false, "", nil)
	}
	if err == nil {
		r, err = s.ledger.ClaimVMRun(id, c.receipt.JobRevision, plan)
	}
	if err == nil {
		c.receipt = r
		c.identity = id
		c.vm = &q
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if c.manual {
		if err := s.ledger.BindManualVM(id, prepareDeadline, s.installationID(), filepath.Join(root, "workspace")); err != nil {
			return err
		}
	}
	if s.vmTransport {
		raw, _ := json.Marshal(vmTransportAdmission{Schema: "haepari.vm-transport-admission/v1", Identity: id, RunnerDigest: q.RunnerDigest})
		if err = s.ledger.RecordVMTransportAdmission(id, raw); err != nil {
			return err
		}
	}
	if err = s.beginPhase(c, "prepare", job.Deadlines.PrepareSeconds, prepareDeadline); err != nil {
		return err
	}
	jobRoot := filepath.Join(s.root.Path, "jobs", id.JobID)
	if err = os.Mkdir(jobRoot, 0700); err != nil {
		return err
	}
	inputID := id
	inputID.RunID = ""
	if err = writeReceipt(jobRoot, "input.json", map[string]any{"identity": inputID, "snapshot_object_id": object}); err != nil {
		return err
	}
	artifact, err := os.OpenFile(filepath.Join(jobRoot, "guest-runner"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = artifact.Write(transport.Runner)
	if err == nil {
		err = artifact.Sync()
	}
	closeErr := artifact.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = syncVMParents(filepath.Join(jobRoot, "guest-runner"), jobRoot); err != nil {
		return err
	}
	c.effects = true
	transferAt := time.Now()
	prepared, err := s.vmCall(q)
	q.Files = nil
	q.Inventory = nil
	if err != nil {
		return err
	} // Reconciliation observes the original preparation; never retries it.
	steps = append(steps, managed.ResultStep{Step: "transfer", DurationMillis: time.Since(transferAt).Milliseconds()})
	if err = s.recordVMPrepared(&q, prepared); err != nil {
		return err
	}
	c.mu.Lock()
	c.vm = &q
	c.mu.Unlock()
	if !c.manual {
		if err = s.endPhase(c, "prepare", managed.LocalPhase{Outcome: "complete", AdapterSteps: steps}); err != nil {
			return err
		}
	} else {
		if err = s.ledger.RecordManualPreparationSteps(c.identity, steps); err != nil {
			return err
		}
		c.manualSteps = steps
	}
	c.mu.Lock()
	status, err = s.ledger.Cancellation(id.JobID)
	if err == nil && !status.Requested && !c.manual {
		r, err = s.ledger.AdvanceLocal(id.JobID, c.receipt.JobRevision, "preparing", "ready", "prepared-"+id.JobID)
		if err == nil {
			c.receipt = r
		}
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if status.Requested {
		return s.vmPreparationStopped(c, job, q, "cancel_before_start")
	}
	if c.manual {
		if !time.Now().Before(prepareDeadline) {
			return s.vmPreparationStopped(c, job, q, "prepare_deadline")
		}
		q.Operation = "start"
		q.Deadline = prepareDeadline
		started, err := s.vmCall(q)
		if err != nil {
			return err
		}
		if started.Observation == nil {
			return managed.ErrBlocked
		}
		c.mu.Lock()
		err = s.importVMView(c, *started.Observation)
		c.mu.Unlock()
		if err != nil {
			return err
		}
		return s.watchVM(c, job, q, prepareDeadline)
	}
	c.mu.Lock()
	r, err = s.ledger.StartVM(id, c.receipt.JobRevision)
	if err == nil {
		c.receipt = r
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(time.Duration(job.Deadlines.RunSeconds) * time.Second)
	if q.Request.Deadline.Before(deadline) {
		deadline = q.Request.Deadline
	}
	if err = s.beginPhase(c, "run", job.Deadlines.RunSeconds, deadline); err != nil {
		return err
	}
	q.Operation = "start"
	q.Deadline = deadline
	started, err := s.vmCall(q)
	if err != nil {
		return err
	}
	if started.Observation == nil {
		return managed.ErrBlocked
	}
	c.mu.Lock()
	err = s.importVMView(c, *started.Observation)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return s.watchVM(c, job, q, deadline)
}
func (s *runtimeServer) watchVM(c *localRunControl, job managed.JobSpec, q runner.GuestRequest, deadline time.Time) error {
	until := deadline.Add(time.Duration(job.Deadlines.CleanupSeconds)*time.Second + supervisorObservationSlack)
	for time.Now().Before(until) {
		select {
		case <-s.detached:
			return errControllerDetached
		default:
		}
		status, err := s.ledger.Cancellation(c.identity.JobID)
		if err != nil {
			return err
		}
		q.Operation = "watch"
		if status.Requested || !time.Now().Before(deadline) {
			q.Operation = "cancel"
		}
		if !time.Now().Before(deadline) && !status.Requested && c.receipt.JobState != "cancelling" {
			c.mu.Lock()
			var r managed.Receipt
			var e error
			if c.manual {
				r, e = s.ledger.ManualDeadline(c.identity, c.receipt.JobRevision, "prepare_deadline")
			} else {
				r, e = s.ledger.BeginLocalDeadline(c.identity, c.receipt.JobRevision)
			}
			if e == nil {
				c.receipt = r
			}
			c.mu.Unlock()
			if e != nil {
				return e
			}
		}
		q.Deadline = until
		reply, err := s.observeVMWithRetry(q)
		if err != nil {
			return err
		}
		if reply.Observation == nil {
			return managed.ErrBlocked
		}
		c.mu.Lock()
		err = s.importVMView(c, *reply.Observation)
		c.mu.Unlock()
		if err != nil {
			return err
		}
		if reply.Observation.Exit != nil && reply.Observation.Exit.TerminationProven {
			if c.manual {
				return s.finishManualPreparation(c, job, filepath.Join(q.JobRoot, c.identity.JobID, "workspace"), reply.Observation.Exit.Outcome, &q, c.manualSteps)
			}
			c.mu.Lock()
			r, e := s.ledger.CompleteLocalRun(managed.LocalTerminationRequest{Identity: c.identity, ExpectedRevision: c.receipt.JobRevision, MutationKey: "termination-" + c.identity.JobID})
			if e == nil {
				c.receipt = r
			}
			c.mu.Unlock()
			if e != nil {
				return e
			}
			if err = s.endPhase(c, "run", managed.LocalPhase{Outcome: "complete"}); err != nil {
				return err
			}
			out, err := s.ledger.LocalOutcome(c.identity.JobID)
			if err != nil {
				return err
			}
			return s.settleVMPhases(c, job, q, out)
		}
	}
	return runner.ErrUnproven
}
func (s *runtimeServer) vmPreparationStopped(c *localRunControl, job managed.JobSpec, q runner.GuestRequest, reason string) error {
	if c.manual && !c.phases["prepare"].Completed {
		outcome := "failed"
		if reason == "prepare_deadline" {
			outcome = "late"
		}
		if err := s.endPhase(c, "prepare", managed.LocalPhase{Outcome: outcome, AdapterSteps: c.manualSteps}); err != nil {
			return err
		}
	}
	if err := s.retireVM(c.identity, job, q); err != nil {
		return err
	}
	c.mu.Lock()
	r, err := s.ledger.RecordVMPreparationStop(managed.LocalPreparationStopRequest{Identity: c.identity, ExpectedRevision: c.receipt.JobRevision, Reason: reason, MutationKey: "preparation-stop-" + c.identity.JobID})
	if err == nil {
		c.receipt = r
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	out, err := s.ledger.LocalOutcome(c.identity.JobID)
	if err != nil {
		return err
	}
	return s.settleVMPhases(c, job, q, out)
}
func (s *runtimeServer) vmNoEffect(c *localRunControl, failure string, unavailable bool, hostReason string, cause error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.identity.RunID == "" && !c.effects {
		current, e := s.ledger.Snapshot(c.identity.JobID)
		if e != nil {
			return e
		}
		c.receipt = current
		status, e := s.ledger.Cancellation(c.identity.JobID)
		if e != nil {
			return e
		}
		if status.Requested {
			failure, unavailable = "cancel", false
		}
	}
	proof := managed.NoEffectProof{ObservationReference: "vm-preflight:" + c.identity.JobID, NoOwnedJobEffect: true, NoUnprovenProcess: true, NoProtectedPath: true, PowerReconciled: true}
	r, err := s.ledger.RecordNoEffectObservation(managed.NoEffectObservationRequest{JobID: c.identity.JobID, ExpectedJobRevision: c.receipt.JobRevision, ResourceID: c.identity.ResourceID, ExpectedConfigRevision: c.identity.ConfigRevision, Slot: c.identity.Slot, LeaseGeneration: c.identity.LeaseGeneration, Source: "machine", Mechanism: "authenticated-vm-preflight-before-guest-claim", EvidenceDigest: vmEvidence(struct {
		Identity managed.LocalIdentity
		Cause    string
	}{c.identity, errorString(cause)}), Proof: proof, MutationKey: "vm-no-effect-" + c.identity.JobID})
	if err != nil {
		return err
	}
	r, err = s.ledger.ClassifyNoEffect(managed.ClassifyNoEffectRequest{JobID: r.JobID, ExpectedJobRevision: r.JobRevision, ResourceID: r.ResourceID, ExpectedConfigRevision: r.ResourceConfigRevision, ExpectedAvailabilityRevision: r.ResourceAvailabilityRevision, Slot: r.Slot, LeaseGeneration: r.LeaseGeneration, FailureKind: failure, VMUnavailable: unavailable, HostPauseReason: hostReason, Proof: proof, MutationKey: "vm-no-effect-classify-" + r.JobID})
	if err != nil {
		return err
	}
	r, err = s.ledger.SettleNoEffect(managed.SettleNoEffectRequest{JobID: r.JobID, ExpectedJobRevision: r.JobRevision, ResourceID: r.ResourceID, ExpectedConfigRevision: r.ResourceConfigRevision, ExpectedAvailabilityRevision: r.ResourceAvailabilityRevision, Slot: r.Slot, LeaseGeneration: r.LeaseGeneration, MutationKey: "vm-no-effect-settle-" + r.JobID})
	if err == nil {
		c.receipt = r
	}
	return err
}
func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *runtimeServer) observeVMReadiness(fence managed.AvailabilityRequest) (string, string, error) {
	spec, err := s.vmSpec(fence.ResourceID)
	if err != nil || spec.Revision != fence.ExpectedConfigRevision {
		return "", "", managed.ErrStale
	}
	status, err := s.ledger.ResourceStatus(fence.ResourceID)
	if err != nil {
		return "", "", err
	}
	if status.ConfigRevision != fence.ExpectedConfigRevision || status.AvailabilityRevision != fence.ExpectedAvailabilityRevision || status.LeaseGeneration != fence.ExpectedLeaseGeneration {
		return "", "", managed.ErrStale
	}
	snap, err := s.ledger.ResourceRecoverySnapshot(fence.ResourceID)
	if err != nil || len(snap.AdapterIntents) > 0 {
		return "", "", managed.ErrBlocked
	}
	transport, err := s.vmSSH(spec)
	if err != nil {
		return "", "", err
	}
	q := runner.GuestRequest{Schema: "haepari.guest-request/v1", Operation: "identity", VMUUID: strings.ToLower(spec.VM.UTMVMUUID), MAC: strings.ToLower(spec.VM.MACAddress), JobRoot: spec.VM.GuestJobRoot, RunnerDigest: transport.RunnerDigest, Deadline: time.Now().Add(10 * time.Second), Request: runner.Request{Identity: managed.LocalIdentity{ResourceID: spec.ID, ConfigRevision: spec.Revision}}}
	reply, err := s.vmCall(q)
	if err != nil {
		return "", "", managed.ErrBlocked
	}
	limit := s.vmHostLimit
	if limit == 0 {
		limit = 1
	}
	guard, err := s.observeVMs(context.Background(), limit)
	if err != nil {
		return "", "", err
	}
	running := guard.UnregisteredRunning
	resources, err := s.ledger.VMResources()
	if err != nil {
		return "", "", err
	}
	for _, v := range resources {
		if v.Power != "stopped" {
			running++
		}
	}
	if running > limit {
		return "", "", managed.ErrBlocked
	}
	facts, err := json.Marshal(struct {
		Schema       string `json:"schema"`
		Resource     string `json:"resource"`
		Config       int64  `json:"config_revision"`
		Availability int64  `json:"availability_revision"`
		Lease        int64  `json:"lease_generation"`
		Boot         string `json:"boot_id"`
		VM           string `json:"vm_uuid"`
		MAC          string `json:"mac"`
		Power        string `json:"power"`
	}{"haepari.vm-readiness/v1", spec.ID, status.ConfigRevision, status.AvailabilityRevision, status.LeaseGeneration, reply.BootID, reply.VMUUID, reply.MAC, "started"})
	if err != nil || len(facts) > 512 {
		return "", "", managed.ErrBlocked
	}
	digest := sha256.Sum256(facts)
	return hex.EncodeToString(digest[:]), string(facts), nil
}

func (s *runtimeServer) retireVM(id managed.LocalIdentity, job managed.JobSpec, q runner.GuestRequest) error {
	snap, err := s.ledger.RecoverySnapshot(id.JobID)
	if err != nil {
		return err
	}
	deadline, err := s.ledger.VMRetirementDeadline(id, managed.RecoveryDigest(snap), time.Now().Add(time.Duration(job.Deadlines.CleanupSeconds)*time.Second))
	if err != nil {
		return err
	}
	if !time.Now().Before(deadline) {
		return managed.ErrBlocked
	}
	q.Operation = "retire"
	q.Deadline = deadline
	reply, err := s.vmCall(q)
	if err != nil {
		q.Operation = "state"
		reply, err = s.vmCall(q)
	}
	if err != nil || reply.Control == nil || !reply.Control.Retired || !reply.Control.Stopped {
		return managed.ErrBlocked
	}
	return nil
}
