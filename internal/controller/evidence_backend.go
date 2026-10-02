package controller

import (
	"errors"
	"path/filepath"

	"github.com/irootkernel/haepari/internal/managed"
	"golang.org/x/sys/unix"
)

// Recovery choreography owns durable stages, deadlines and worker lifetime.
// A backend owns source identity, collection, cleanup and readiness facts.
// Only local is provided in production; VM transport remains TASK-013–016.
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
