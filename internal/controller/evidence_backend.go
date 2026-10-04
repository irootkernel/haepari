package controller

import (
	"errors"
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
	Observe(managed.LocalIdentity) (managed.RetainedPath, error)
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

func (b localEvidenceBackend) Observe(id managed.LocalIdentity) (managed.RetainedPath, error) {
	owned, err := b.s.inspectRecoveryRoot(id)
	if err != nil || !owned {
		return managed.RetainedPath{}, managed.ErrBlocked
	}
	fd, err := openPrivateJob(b.s.root.Path, id.JobID)
	if err != nil {
		return managed.RetainedPath{}, err
	}
	defer unix.Close(fd)
	return retainedPath(fd, "", true)
}

func (b localEvidenceBackend) Collect(j managed.EvidenceRecoveryJob, destination string) collectionAttempt {
	root, err := b.Observe(j.Identity)
	if err != nil || root != j.Root {
		return collectionAttempt{}
	}
	a, complete, expired := collectOutputs(filepath.Join(b.s.root.Path, "jobs", j.JobID, "workspace"), destination, j.Outputs, j.CollectDeadline)
	return collectionAttempt{artifacts: a, complete: complete, expired: expired}
}

func (b localEvidenceBackend) Cleanup(j managed.EvidenceRecoveryJob) error {
	root, err := b.Observe(j.Identity)
	if err != nil || root != j.Root {
		return managed.ErrBlocked
	}
	fd, err := openPrivateJob(b.s.root.Path, j.JobID)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	err = removeEntryUntil(fd, "workspace", j.CleanupDeadline)
	if errors.Is(err, unix.ENOENT) {
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

func (b vmEvidenceBackend) Observe(id managed.LocalIdentity) (managed.RetainedPath, error) {
	q, err := b.s.vmBinding(id.JobID)
	if err != nil || q.Request.Identity != id || id.ResourceID != b.resourceID {
		return managed.RetainedPath{}, managed.ErrBlocked
	}
	q.Operation = "state"
	q.Deadline = time.Now().Add(5 * time.Second)
	reply, err := b.s.vmCall(q)
	if err != nil || reply.Root == nil || reply.Control == nil || !reply.Control.Stopped {
		return managed.RetainedPath{}, managed.ErrBlocked
	}
	if q.Root != nil && *q.Root != *reply.Root {
		return managed.RetainedPath{}, managed.ErrBlocked
	}
	return *reply.Root, nil
}
func (b vmEvidenceBackend) Collect(j managed.EvidenceRecoveryJob, destination string) collectionAttempt {
	q, err := b.s.vmBinding(j.JobID)
	if err != nil || q.Request.Identity != j.Identity {
		return collectionAttempt{}
	}
	q.Root = &j.Root
	collected := b.s.collectVM(q, j.Outputs, destination, j.CollectDeadline)
	if collected.complete {
		if err := b.s.collectVMLog(q, j.CollectDeadline); err != nil {
			collected.complete = false
			if errors.Is(err, utm.ErrGuestUncertain) && !b.s.vmWorkerStopped(q) {
				collected.unproven = true
			}
		}
	}
	return collected
}
func (b vmEvidenceBackend) Cleanup(j managed.EvidenceRecoveryJob) error {
	q, err := b.s.vmBinding(j.JobID)
	if err != nil || q.Request.Identity != j.Identity {
		return managed.ErrBlocked
	}
	q.Root = &j.Root
	err = b.s.cleanupVM(q, j.CleanupDeadline)
	if errors.Is(err, utm.ErrGuestUncertain) && !b.s.vmWorkerStopped(q) {
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
		q, err := b.s.vmBinding(j.JobID)
		if err != nil {
			facts[j.JobID] = "guest ownership unavailable"
			healthy = false
			continue
		}
		q.Operation = "health"
		q.Deadline = time.Now().Add(5 * time.Second)
		reply, err := b.s.vmCall(q)
		ok := err == nil && reply.Reusable
		facts[j.JobID] = ok
		healthy = healthy && ok
	}
	return facts, healthy
}
