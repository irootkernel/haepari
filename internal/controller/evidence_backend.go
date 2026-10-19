package controller

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/utm"
	"golang.org/x/sys/unix"
)

// Recovery choreography owns durable stages, deadlines and worker lifetime.
// A backend owns source identity, collection, cleanup and readiness facts.
// Local and authenticated VM backends share this recovery choreography.
type evidenceBackend interface {
	Observe(managed.LocalIdentity, string, time.Time) (managed.RetainedPath, error)
	Collect(managed.EvidenceRecoveryJob, string) collectionAttempt
	Cleanup(managed.EvidenceRecoveryJob) error
	Health(managed.ResourceRecoverySnapshot) (any, bool)
}

func (s *runtimeServer) evidenceBackendFor(kind, id string) (evidenceBackend, error) {
	if kind == "local" {
		return localEvidenceBackend{s}, nil
	}
	if s.backendEvidence != nil {
		backend, err := s.backendEvidence(kind, id)
		if err == nil && backend == nil {
			return nil, managed.ErrUnsupported
		}
		return backend, err
	}
	if kind == "utm-macos" {
		return vmEvidenceBackend{s, id}, nil
	}
	return nil, managed.ErrUnsupported
}

type localEvidenceBackend struct{ s *runtimeServer }

// Supplementary recovery keeps the original v2 preparation identity. Legacy
// and never-started outcomes retain their original cleanup policy.
func (b localEvidenceBackend) workspaceBinding(id managed.LocalIdentity) (*managed.RetainedPath, error) {
	snapshot, err := b.s.ledger.BulkSnapshot(id.JobID)
	if errors.Is(err, managed.ErrBusy) {
		// Legacy callers may observe their owned root before terminal settlement.
		// Preserve that v1 path without bypassing the v2 workspace fence.
		active, readErr := b.s.ledger.RecoverySnapshot(id.JobID)
		if readErr == nil && active.Identity == id && !managed.AbsenceAware(active.Spec) {
			return nil, nil
		}
	}
	if err != nil {
		return nil, err
	}
	if snapshot.Identity == nil || *snapshot.Identity != id {
		return nil, managed.ErrBlocked
	}
	if !managed.AbsenceAware(snapshot.Spec) {
		return nil, nil
	}
	outcome, err := b.s.ledger.LocalOutcome(id.JobID)
	if err != nil {
		return nil, err
	}
	if !outcome.Started {
		return nil, nil
	}
	root, err := b.s.verifyLocalWorkspace(id, filepath.Join(b.s.root.Path, "jobs", id.JobID, "workspace"))
	if err != nil {
		return nil, err
	}
	return &root, nil
}

func (b localEvidenceBackend) Observe(id managed.LocalIdentity, _ string, _ time.Time) (managed.RetainedPath, error) {
	owned, err := b.s.inspectRecoveryRoot(id)
	if err != nil || !owned {
		return managed.RetainedPath{}, managed.ErrBlocked
	}
	if _, err := b.workspaceBinding(id); err != nil {
		return managed.RetainedPath{}, err
	}
	fd, err := openPrivateJob(b.s.root.Path, id.JobID)
	if err != nil {
		return managed.RetainedPath{}, err
	}
	defer unix.Close(fd)
	return retainedPath(fd, "", true)
}

func (b localEvidenceBackend) Collect(j managed.EvidenceRecoveryJob, destination string) collectionAttempt {
	root, err := b.Observe(j.Identity, "recollect", time.Time{})
	if err != nil || root != j.Root {
		return collectionAttempt{}
	}
	_, _, artifactBytes, artifactCount, _, err := b.s.jobCollectionBounds(j.JobID)
	if err != nil {
		return collectionAttempt{}
	}
	// Required files use the pin before any copy. This path does not collect output.log.
	collected := collectOutputDetails(filepath.Join(b.s.root.Path, "jobs", j.JobID, "workspace"), destination, j.Outputs, j.CollectDeadline, artifactBytes, int(artifactCount), copyUntil)
	root, err = b.Observe(j.Identity, "recollect", time.Time{})
	if err != nil || root != j.Root {
		return collectionAttempt{artifacts: collected.artifacts, expired: collected.expired, rootConflict: true}
	}
	return collectionAttempt{artifacts: collected.artifacts, complete: collected.complete, expired: collected.expired}
}

func (b localEvidenceBackend) Cleanup(j managed.EvidenceRecoveryJob) error {
	root, err := b.Observe(j.Identity, "dispose", time.Time{})
	if err != nil || root != j.Root {
		return managed.ErrBlocked
	}
	fd, err := openPrivateJob(b.s.root.Path, j.JobID)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	opened, err := retainedPath(fd, "", true)
	if err != nil || opened != j.Root {
		return managed.ErrStale
	}
	binding, err := b.workspaceBinding(j.Identity)
	if err != nil {
		return err
	}
	err = removeEntryUntilBound(fd, "workspace", j.CleanupDeadline, binding)
	if errors.Is(err, unix.ENOENT) && binding == nil {
		return nil
	}
	return err
}

func (b localEvidenceBackend) Health(snap managed.ResourceRecoverySnapshot) (any, bool) {
	return b.s.observeLocalResourceRecovery(snap)
}

type vmEvidenceBackend struct {
	s          *runtimeServer
	resourceID string
}

var errVMNativeTermination = fmt.Errorf("%w: original guest native termination proof is required for evidence collection and cleanup; a stopped filesystem worker or controller attestation does not supply it", managed.ErrBlocked)

func (b vmEvidenceBackend) Observe(id managed.LocalIdentity, action string, until time.Time) (managed.RetainedPath, error) {
	if until.IsZero() || !time.Now().Before(until) {
		return managed.RetainedPath{}, managed.ErrBlocked
	}
	if q, found, err := b.s.vmAttestation(id.JobID); err != nil {
		return managed.RetainedPath{}, err
	} else if found {
		return b.observeAttested(q, id, action, until)
	}
	q, err := b.s.vmBinding(id.JobID)
	if err != nil || q.Request.Identity != id || id.ResourceID != b.resourceID {
		return managed.RetainedPath{}, managed.ErrBlocked
	}
	q.Operation = "state"
	q.Deadline = until
	reply, err := b.s.vmCall(q)
	if err != nil || reply.Root == nil || reply.Control == nil || !reply.Control.Stopped {
		return managed.RetainedPath{}, managed.ErrBlocked
	}
	if q.Root != nil && *q.Root != *reply.Root {
		return managed.RetainedPath{}, managed.ErrBlocked
	}
	root := *reply.Root
	if action == "dispose" && reply.Control.Phase == "cleanup" {
		// A completed deletion can lose its host reply. The original control
		// receipt and native health check can prove that effect without opening
		// or adopting a replacement Job directory.
		q.Operation = "health"
		q.Root = &root
		q.Deadline = until
		health, err := b.s.vmCall(q)
		if err != nil {
			// Do not overwrite the cleanup control phase with a root request
			// when health is unknown: an absent source may already be deleted.
			return managed.RetainedPath{}, err
		}
		if health.Reusable {
			return root, nil
		}
	}
	// Partial preparation can be disposed only behind the guest retirement
	// barrier. It has no native request to inspect and cannot supply outputs.
	if action == "dispose" && reply.Control.Retired && reply.Control.NativeRequest == nil {
		return root, nil
	}
	q.Operation = "root"
	q.Root = &root
	q.Deadline = until
	reply, err = b.s.vmCall(q)
	if err != nil {
		return managed.RetainedPath{}, err
	}
	if reply.Root == nil || *reply.Root != root || reply.Control == nil || !reply.Control.Stopped {
		return managed.RetainedPath{}, managed.ErrBlocked
	}
	view := reply.Observation
	// Match the guest file/cleanup and health prerequisites. Current owner
	// absence cannot upgrade an immutable false native termination receipt.
	if view == nil || !(view.Exit != nil && view.Exit.TerminationProven || action == "dispose" && reply.Control.Retired && view.State == "prepared" && view.Intent == nil) {
		return managed.RetainedPath{}, errVMNativeTermination
	}
	return root, nil
}
func (b vmEvidenceBackend) Collect(j managed.EvidenceRecoveryJob, destination string) collectionAttempt {
	if q, found, err := b.s.vmAttestation(j.JobID); err != nil {
		return collectionAttempt{}
	} else if found {
		return b.collectAttested(q, j, destination)
	}
	q, err := b.s.vmBinding(j.JobID)
	if err != nil || q.Request.Identity != j.Identity {
		return collectionAttempt{}
	}
	q.Root = &j.Root
	_, hasRequest, artifactBytes, artifactCount, _, err := b.s.jobCollectionBounds(j.JobID)
	if err != nil {
		return collectionAttempt{}
	}
	collected := b.s.collectVMWithStop(q, j.Outputs, destination, j.CollectDeadline, artifactBytes, artifactCount, func(stopped runner.GuestRequest) bool {
		return b.s.vmWorkerStoppedUntil(stopped, j.CollectDeadline)
	})
	if collected.complete && !hasRequest {
		collected.complete = false
	}
	if collected.complete {
		if err := b.s.collectVMLog(q, j.CollectDeadline); err != nil {
			collected.complete = false
			if errors.Is(err, utm.ErrGuestUncertain) && !b.s.vmWorkerStoppedUntil(q, j.CollectDeadline) {
				collected.unproven = true
			}
		}
	}
	return collected
}
func (b vmEvidenceBackend) Cleanup(j managed.EvidenceRecoveryJob) error {
	if q, found, err := b.s.vmAttestation(j.JobID); err != nil {
		return err
	} else if found {
		return b.cleanupAttested(q, j)
	}
	q, err := b.s.vmBinding(j.JobID)
	if err != nil || q.Request.Identity != j.Identity {
		return managed.ErrBlocked
	}
	q.Root = &j.Root
	err = b.s.cleanupVM(q, j.CleanupDeadline)
	if errors.Is(err, utm.ErrGuestUncertain) && !b.s.vmWorkerStoppedUntil(q, j.CleanupDeadline) {
		return runner.ErrUnproven
	}
	return err
}
func (b vmEvidenceBackend) Health(snap managed.ResourceRecoverySnapshot) (any, bool) {
	facts := map[string]any{}
	healthy := len(snap.AdapterIntents) == 0
	for _, j := range snap.Jobs {
		if j.Result != nil && j.Result.CleanupOutcome == "not_needed" && len(j.Result.ProtectedEvidence) == 0 {
			continue
		}
		until := time.Now().Add(recoveryObservationBudget)
		q, err := b.s.vmBinding(j.JobID)
		if err != nil {
			facts[j.JobID] = "guest ownership unavailable"
			healthy = false
			continue
		}
		if a, found, e := b.s.vmAttestation(j.JobID); e != nil {
			healthy = false
			facts[j.JobID] = false
			continue
		} else if found {
			a.Operation = "health"
			a.Deadline = until
			reply, e := b.s.vmRecoveryCall(a)
			ok := e == nil && reply.Reusable
			facts[j.JobID] = ok
			healthy = healthy && ok
			continue
		}
		q.Operation = "health"
		q.Deadline = until
		reply, err := b.s.vmCall(q)
		ok := err == nil && reply.Reusable
		if !ok {
			_, recovered, e := b.s.observeVMRebootRecoveryUntil(q, until)
			ok = e == nil && recovered.CompletedCleanup != nil && recovered.Reusable && j.EvidenceResolved
		}
		facts[j.JobID] = ok
		healthy = healthy && ok
	}
	return facts, healthy
}
