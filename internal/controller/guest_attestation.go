package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/utm"
	"golang.org/x/sys/unix"
)

func (s *runtimeServer) vmRecoveryCall(q runner.GuestRecoveryRequest) (runner.GuestRecoveryReply, error) {
	if len(q.RecoveryDigest) != 71 || !strings.HasPrefix(q.RecoveryDigest, "sha256:") {
		return runner.GuestRecoveryReply{}, managed.ErrBlocked
	}
	if _, err := hex.DecodeString(q.RecoveryDigest[7:]); err != nil {
		return runner.GuestRecoveryReply{}, managed.ErrBlocked
	}
	spec, err := s.vmSpec(q.Guest.Request.Identity.ResourceID)
	if err != nil {
		return runner.GuestRecoveryReply{}, err
	}
	transport, err := s.vmSSH(spec)
	if err != nil {
		return runner.GuestRecoveryReply{}, err
	}
	// Historical recovery executables know only their frozen guest protocol.
	// Only completed-cleanup health selects the new current verifier capability.
	transport.Transport = s.vmTransport && q.Operation == "health"
	if q.RecoveryDigest != transport.RunnerDigest && !(s.vmTransport && q.Operation == "health") {
		path := filepath.Join(s.root.Path, "jobs", q.Guest.Request.Identity.JobID, "guest-recovery-runner-"+q.RecoveryDigest[7:])
		f, e := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
		if e != nil {
			return runner.GuestRecoveryReply{}, e
		}
		defer f.Close()
		if _, e = privateLogStat(int(f.Fd()), false); e != nil {
			return runner.GuestRecoveryReply{}, e
		}
		transport.Runner, e = io.ReadAll(io.LimitReader(f, (64<<20)+1))
		if e != nil || len(transport.Runner) > 64<<20 {
			return runner.GuestRecoveryReply{}, managed.ErrBlocked
		}
		sum := sha256.Sum256(transport.Runner)
		transport.RunnerDigest = "sha256:" + hex.EncodeToString(sum[:])
		if transport.RunnerDigest != q.RecoveryDigest {
			return runner.GuestRecoveryReply{}, managed.ErrBlocked
		}
	}
	var leases io.ReadCloser
	if s.vmLeases != nil {
		leases, err = s.vmLeases()
	} else {
		leases, err = os.OpenFile("/var/db/dhcpd_leases", os.O_RDONLY|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return runner.GuestRecoveryReply{}, err
	}
	defer leases.Close()
	reply, err := transport.CallRecovery(context.Background(), spec, leases, q)
	if err != nil && q.Schema == "haepari.guest-recovery-request/v2" && q.Guest.Request.Identity.JobID != "" {
		// Preserve failed exchanges before callers consume their error. A
		// successful probe stays read-only and cannot invalidate a plan.
		_ = s.ledger.RecordRecoveryDiagnosticCause(q.Guest.Request.Identity.JobID, diagnosticCause(err))
		if errors.Is(err, utm.ErrGuestUncertain) {
			_ = s.ledger.RecordRecoveryDiagnosticCause(q.Guest.Request.Identity.JobID, managed.DiagnosticGuestEffectUncertain)
		}
	}
	return reply, err
}
func (s *runtimeServer) guestAttestationBasis(id string) (managed.ResourceRecoverySnapshot, []runner.GuestRecoveryReply, error) {
	snap, err := s.ledger.ResourceRecoverySnapshot(id)
	if err != nil {
		return snap, nil, err
	}
	if snap.Resource.Kind != "utm-macos" || snap.Resource.Availability != "quarantined" || snap.EvidenceIntent != nil || len(snap.AdapterIntents) > 0 {
		return snap, nil, managed.ErrBlocked
	}
	spec, err := s.vmSpec(id)
	if err != nil {
		return snap, nil, err
	}
	transport, err := s.vmSSH(spec)
	if err != nil {
		return snap, nil, err
	}
	replies := []runner.GuestRecoveryReply{}
	for _, j := range snap.Jobs {
		if j.EvidenceResolved {
			continue
		}
		b, e := s.ledger.BulkSnapshot(j.JobID)
		if e != nil || b.Identity == nil || s.jobWorkerAlive(j.JobID) {
			return snap, nil, managed.ErrBlocked
		}
		for _, p := range b.Result.ProtectedEvidence {
			if p.Path != "workspace" || p.RecoveryReference != "job:"+j.JobID {
				return snap, nil, managed.ErrBlocked
			}
		}
		guest, e := s.vmBinding(j.JobID)
		if e != nil {
			return snap, nil, e
		}
		q := runner.GuestRecoveryRequest{Schema: "haepari.guest-recovery-request/v1", Operation: "probe", Guest: guest, RecoveryDigest: transport.RunnerDigest, Deadline: time.Now().Add(5 * time.Second)}
		reply, e := s.vmRecoveryCall(q)
		if e != nil {
			q.Schema = "haepari.guest-recovery-request/v2"
			reply, e = s.vmRecoveryCall(q)
		}
		if e != nil {
			return snap, nil, e
		}
		if reply.Basis == nil {
			return snap, nil, managed.ErrBlocked
		}
		// Strip transport nonces; fresh probes of identical original evidence have
		// the same plan basis. Native liveness is checked anew, never hashed as proof.
		reply.Guest.Challenge = ""
		reply.Guest.ProtocolDigest = ""
		reply.ProtocolDigest = ""
		replies = append(replies, reply)
	}
	if len(replies) != 1 {
		return snap, nil, managed.ErrBlocked
	} // one explicit full-scope operator statement
	return snap, replies, nil
}
func (s *runtimeServer) planGuestAttestation(id string) (resourceRecoveryPlan, error) {
	p := resourceRecoveryPlan{Schema: "haepari.recovery-plan/v1", Action: "attest-guest-stopped", ObservedAt: time.Now().UTC(), Unknowns: []string{}, Effects: []string{}, EvidenceLoss: []managed.ProtectedEvidence{}}
	snap, basis, err := s.guestAttestationBasis(id)
	p.Snapshot = snap
	p.Observation = basis
	for _, reply := range basis {
		if reply.Basis != nil && reply.Basis.RecoveryBoot != "" {
			p.Schema = "haepari.recovery-plan/v2"
		}
	}
	if err != nil {
		p.Unknowns = append(p.Unknowns, "original guest evidence, stopped native owners and recovery capability must be observable")
		return p, nil
	}
	p.Effects = []string{"persist scoped operator attestation outside the original guest Job directory", "retire the original guest start path without upgrading native proof", "preserve all original receipts, evidence and terminal Job results; recollect and health require separate plans"}
	p.ExpiresAt = time.Now().UTC().Add(2 * time.Minute)
	p.PlanToken = s.signRecoveryToken(recoveryToken{s.installationID(), "resource:" + id, p.Action, managed.RecoveryDigest(snap), managed.RecoveryDigest(basis), p.ExpiresAt, ""})
	return p, nil
}
func (s *runtimeServer) applyGuestAttestation(w http.ResponseWriter, r *http.Request, id string, request managed.RecoveryRequest, token recoveryToken) {
	op := "resource.recover"
	if token.Installation != s.installationID() || token.JobID != "resource:"+id || !time.Now().Before(token.ExpiresAt) || request.Evidence == nil || managed.ValidateRecoveryEvidence(*request.Evidence) != nil {
		reject(w, op, managed.ErrStale)
		return
	}
	s.evidenceMu.Lock()
	defer s.evidenceMu.Unlock()
	snap, replies, err := s.guestAttestationBasis(id)
	if err != nil || token.SnapshotDigest != managed.RecoveryDigest(snap) || token.ObservationDigest != managed.RecoveryDigest(replies) {
		reject(w, op, managed.ErrStale)
		return
	}
	jobs := []managed.EvidenceRecoveryJob{}
	now := time.Now().UTC()
	key := "evidence-recovery-" + request.MutationKey
	for _, reply := range replies {
		jobID := reply.Basis.Request.Identity.JobID
		b, e := s.ledger.BulkSnapshot(jobID)
		if e != nil {
			reject(w, op, e)
			return
		}
		guest, e := s.vmBinding(jobID)
		if e != nil {
			reject(w, op, e)
			return
		}
		spec, e := s.vmSpec(id)
		if e != nil {
			reject(w, op, e)
			return
		}
		transport, e := s.vmSSH(spec)
		if e != nil {
			reject(w, op, e)
			return
		}
		q := runner.GuestRecoveryRequest{Schema: "haepari.guest-recovery-request/v1", Operation: "attest", Guest: guest, RecoveryDigest: transport.RunnerDigest, Deadline: token.ExpiresAt, Basis: reply.Basis, MutationKey: key, Evidence: request.Evidence}
		if reply.Basis.RecoveryBoot != "" {
			q.Schema, q.RecoveryBoot = "haepari.guest-recovery-request/v2", reply.Basis.RecoveryBoot
		}
		if request.Evidence.Source != "operator-attested" || request.Evidence.Mechanism != "guest-workload-and-descendants-stopped" || request.Evidence.Reference != "job:"+jobID || request.Evidence.Digest != managed.RecoveryDigest(*reply.Basis) || request.Evidence.ObservedAt.After(now) || request.Evidence.ObservedAt.Before(token.ExpiresAt.Add(-2*time.Minute)) {
			reject(w, op, managed.ErrInvalid)
			return
		}
		path := filepath.Join(s.root.Path, "jobs", jobID, "guest-recovery-runner-"+q.RecoveryDigest[7:])
		if err = publishRecoveryRunner(path, transport.Runner); err != nil {
			reject(w, op, err)
			return
		}
		raw, _ := json.Marshal(q)
		jobs = append(jobs, managed.EvidenceRecoveryJob{GuestAttestation: raw, JobID: jobID, ResultDigest: managed.RecoveryDigest(b.Result), Identity: *b.Identity, Root: reply.Basis.Root, Protected: b.Result.ProtectedEvidence, Outputs: b.Spec.Workload.RequiredOutputs, CollectDeadline: now.Add(time.Duration(b.Spec.Deadlines.CollectSeconds) * time.Second), CleanupDeadline: now.Add(time.Duration(b.Spec.Deadlines.CollectSeconds+b.Spec.Deadlines.CleanupSeconds) * time.Second)})
	}
	owner, err := platform.ObserveProcess(os.Getpid())
	if err != nil {
		reject(w, op, err)
		return
	}
	digest := managed.RecoveryDigest(struct {
		ResourceID string
		Request    managed.RecoveryRequest
	}{id, request})
	intent := managed.EvidenceRecoveryIntent{Schema: "haepari.evidence-recovery-intent/v1", ResourceID: id, SnapshotDigest: token.SnapshotDigest, ResourceConfigRevision: snap.Resource.ConfigRevision, ResourceAvailabilityRevision: snap.Resource.AvailabilityRevision, MutationKey: key, ClientDigest: digest, Action: token.Action, RecordedAt: now, Owner: owner.Identity, Jobs: jobs}
	if err = s.ledger.BeginEvidenceRecovery(intent); err != nil {
		reject(w, op, err)
		return
	}
	if err = s.executeGuestAttestation(intent); err != nil {
		reject(w, op, err)
		return
	}
	_, receipt, _, err := s.ledger.EvidenceRecovery(key, digest)
	if err != nil {
		reject(w, op, err)
		return
	}
	send(w, 200, op, receipt, nil)
}
func publishRecoveryRunner(path string, data []byte) error {
	if len(data) == 0 || len(data) > 64<<20 {
		return managed.ErrInvalid
	}
	parentFD, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	parent := os.NewFile(uintptr(parentFD), filepath.Dir(path))
	defer parent.Close()
	if _, err = privateLogStat(parentFD, true); err != nil {
		return err
	}
	name := filepath.Base(path)
	checkExisting := func() error {
		fd, e := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			return e
		}
		f := os.NewFile(uintptr(fd), name)
		defer f.Close()
		if _, e = privateLogStat(fd, false); e != nil {
			return e
		}
		old, e := io.ReadAll(io.LimitReader(f, (64<<20)+1))
		if e != nil || !reflect.DeepEqual(old, data) {
			return managed.ErrConflict
		}
		return errors.Join(f.Sync(), parent.Sync())
	}
	if err = checkExisting(); !errors.Is(err, unix.ENOENT) {
		return err
	}
	temporary, err := randomID(".guest-recovery-runner-")
	if err != nil {
		return err
	}
	fd, err := unix.Openat(parentFD, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0500)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(parentFD, temporary, 0)
	f := os.NewFile(uintptr(fd), temporary)
	observeRetentionBoundary("guest-recovery-runner-created")
	n, writeErr := f.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	observeRetentionBoundary("guest-recovery-runner-synced")
	if err = platform.PublishExclusive(parentFD, temporary, name); errors.Is(err, unix.EEXIST) {
		return checkExisting()
	} else if err != nil {
		return err
	}
	observeRetentionBoundary("guest-recovery-runner-published")
	return parent.Sync()
}
func (s *runtimeServer) executeGuestAttestation(intent managed.EvidenceRecoveryIntent) error {
	snap, err := s.ledger.ResourceRecoverySnapshot(intent.ResourceID)
	if err != nil || snap.EvidenceIntent == nil || managed.RecoveryDigest(*snap.EvidenceIntent) != managed.RecoveryDigest(intent) {
		return managed.ErrStale
	}
	_, receipt, _, err := s.ledger.EvidenceRecovery(intent.MutationKey, intent.ClientDigest)
	if err != nil {
		return err
	}
	if receipt == nil {
		receipt = &managed.EvidenceRecoveryReceipt{Schema: "haepari.evidence-recovery-receipt/v1", MutationKey: intent.MutationKey, Observation: recoveryObservation("guest-attestation-intent", intent, "resource:"+intent.ResourceID)}
		for _, j := range intent.Jobs {
			receipt.Jobs = append(receipt.Jobs, managed.EvidenceRecoveryOutcome{JobID: j.JobID, ResultDigest: j.ResultDigest, Stage: "pending", Artifacts: []managed.ResultArtifact{}})
		}
	}
	for i, j := range intent.Jobs {
		if receipt.Jobs[i].Stage == "attested" || receipt.Jobs[i].Stage == "failed" {
			continue
		}
		q, decodeErr := decodeAttestationRequest(j.GuestAttestation)
		if decodeErr != nil {
			return managed.ErrBlocked
		}
		if !time.Now().Before(q.Deadline) {
			receipt.Jobs[i].Stage = "failed"
			receipt.Jobs[i].Gap = "original guest attestation deadline expired"
			continue
		}
		reply, e := s.vmRecoveryCall(q)
		if e != nil {
			return e
		}
		expected := runner.GuestAttestation{Schema: "haepari.guest-attestation/v1", Guest: guestRecoveryOwner(q.Guest), Basis: *q.Basis, RecoveryDigest: q.RecoveryDigest, MutationKey: q.MutationKey, Deadline: q.Deadline, Evidence: *q.Evidence}
		if q.Schema == "haepari.guest-recovery-request/v2" {
			expected.Schema = "haepari.guest-attestation/v2"
		}
		if reply.Attestation == nil || !reflect.DeepEqual(*reply.Attestation, expected) {
			return managed.ErrBlocked
		}
		receipt.Jobs[i].Attestation, _ = json.Marshal(reply.Attestation)
		receipt.Jobs[i].Stage = "attested"
		receipt.Observation = recoveryObservation("guest-attestation-observed", *receipt, "resource:"+intent.ResourceID)
		if err = s.ledger.RecordEvidenceRecovery(intent, *receipt); err != nil {
			return err
		}
	}
	receipt.Complete = true
	receipt.Observation = recoveryObservation("guest-attestation-complete", *receipt, "resource:"+intent.ResourceID)
	return s.ledger.RecordEvidenceRecovery(intent, *receipt)
}
func guestRecoveryOwner(q runner.GuestRequest) runner.GuestRequest {
	q.Operation = "inspect"
	q.Challenge = ""
	q.Files = nil
	q.Inventory = nil
	q.Deadline = time.Time{}
	q.Root = nil
	q.Offset = 0
	q.Area = ""
	q.ExpectedFile = nil
	q.Path = ""
	return q
}
func (s *runtimeServer) vmAttestation(jobID string) (runner.GuestRecoveryRequest, bool, error) {
	var q runner.GuestRecoveryRequest
	guest, err := s.vmBinding(jobID)
	if err != nil {
		return q, false, err
	}
	if q, found, e := s.vmRebootAttestation(guest); found || e != nil {
		return q, found, e
	}
	outcomes, err := s.ledger.SupplementaryEvidence(jobID)
	if err != nil {
		return q, false, err
	}
	for _, o := range outcomes {
		if len(o.Attestation) == 0 {
			continue
		}
		var a runner.GuestAttestation
		if json.Unmarshal(o.Attestation, &a) != nil || runner.ValidateGuestAttestation(a, guest) != nil {
			return q, false, managed.ErrBlocked
		}
		intent, receipt, found, e := s.ledger.EvidenceRecovery(a.MutationKey, "")
		if e != nil || !found || receipt == nil || !receipt.Complete || intent.Action != "attest-guest-stopped" {
			return q, false, managed.ErrBlocked
		}
		matched := false
		for _, j := range intent.Jobs {
			if j.JobID == jobID && j.Identity == guest.Request.Identity && j.Root == a.Basis.Root {
				saved, decodeErr := decodeAttestationRequest(j.GuestAttestation)
				if decodeErr == nil && saved.Deadline.Equal(a.Deadline) && reflect.DeepEqual(guestRecoveryOwner(saved.Guest), a.Guest) && saved.MutationKey == a.MutationKey && saved.RecoveryDigest == a.RecoveryDigest && reflect.DeepEqual(*saved.Basis, a.Basis) && reflect.DeepEqual(*saved.Evidence, a.Evidence) {
					matched = true
				}
			}
		}
		if !matched {
			return q, false, managed.ErrBlocked
		}
		q = runner.GuestRecoveryRequest{Schema: "haepari.guest-recovery-request/v1", Guest: guest, RecoveryDigest: a.RecoveryDigest, Attestation: &a, Basis: &a.Basis}
		if a.Schema == "haepari.guest-attestation/v2" {
			q.Schema, q.RecoveryBoot = "haepari.guest-recovery-request/v2", a.Basis.RecoveryBoot
		}
		return q, true, nil
	}
	return q, false, nil
}
