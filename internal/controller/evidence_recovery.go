package controller

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/runner"
)

func retainedPath(fd int, name string, directory bool) (managed.RetainedPath, error) {
	st, err := privateLogStat(fd, directory)
	if err != nil {
		return managed.RetainedPath{}, err
	}
	return managed.RetainedPath{Path: name, Device: uint64(st.Dev), Inode: st.Ino, Directory: directory}, nil
}

func (s *runtimeServer) evidenceRecoveryBasis(id string) (managed.ResourceRecoverySnapshot, []managed.EvidenceRecoveryJob, error) {
	snap, err := s.ledger.ResourceRecoverySnapshot(id)
	if err != nil {
		return snap, nil, err
	}
	if snap.Resource.Availability != "quarantined" || len(snap.AdapterIntents) > 0 {
		return snap, nil, managed.ErrBlocked
	}
	backend, err := s.evidenceBackendFor(snap.Resource.Kind, id)
	if err != nil {
		return snap, nil, err
	}
	jobs := []managed.EvidenceRecoveryJob{}
	for _, j := range snap.Jobs {
		if j.EvidenceResolved {
			continue
		}
		b, err := s.ledger.BulkSnapshot(j.JobID)
		if err != nil {
			return snap, nil, err
		}
		if b.Identity == nil || s.jobWorkerAlive(j.JobID) {
			return snap, nil, managed.ErrBlocked
		}
		for _, p := range b.Result.ProtectedEvidence {
			if p.Path != "workspace" || p.RecoveryReference != "job:"+j.JobID {
				return snap, nil, managed.ErrBlocked
			}
		}
		root, err := backend.Observe(*b.Identity)
		if err != nil {
			return snap, nil, err
		}
		jobs = append(jobs, managed.EvidenceRecoveryJob{JobID: j.JobID, ResultDigest: managed.RecoveryDigest(b.Result), Identity: *b.Identity, Root: root, Protected: b.Result.ProtectedEvidence, Outputs: b.Spec.Workload.RequiredOutputs})
	}
	if len(jobs) == 0 {
		return snap, nil, managed.ErrBlocked
	}
	return snap, jobs, nil
}

func (s *runtimeServer) planEvidenceRecovery(id, action string) (resourceRecoveryPlan, error) {
	p := resourceRecoveryPlan{Schema: "haepari.recovery-plan/v1", Action: action, ObservedAt: time.Now().UTC(), Unknowns: []string{}, Effects: []string{}, EvidenceLoss: []managed.ProtectedEvidence{}}
	snap, jobs, err := s.evidenceRecoveryBasis(id)
	p.Snapshot = snap
	if err != nil {
		return p, err
	}
	p.Observation = jobs
	if action == "dispose" {
		for _, j := range jobs {
			p.EvidenceLoss = append(p.EvidenceLoss, j.Protected...)
		}
		p.Effects = []string{"record explicit authorization of the listed evidence loss", "delete the listed owned workspaces under original recovery deadlines", "retain immutable Job verdicts and verify resource health separately"}
	} else {
		p.Effects = []string{"record bounded evidence recovery intent", "collect required outputs into a separate immutable supplementary inventory", "record collection before workspace cleanup", "retain immutable Job verdicts and verify resource health separately"}
	}
	if snap.Resource.Kind == "utm-macos" {
		for i, effect := range p.Effects {
			p.Effects[i] = strings.ReplaceAll(effect, "workspaces", "guest Job directories (including native logs and receipts)")
			p.Effects[i] = strings.ReplaceAll(p.Effects[i], "workspace cleanup", "guest Job directory cleanup")
		}
	}
	p.ExpiresAt = time.Now().UTC().Add(2 * time.Minute)
	p.PlanToken = s.signRecoveryToken(recoveryToken{s.installationID(), "resource:" + id, action, managed.RecoveryDigest(snap), managed.RecoveryDigest(jobs), p.ExpiresAt})
	return p, nil
}

func (s *runtimeServer) applyEvidenceRecovery(w http.ResponseWriter, r *http.Request, id string, q managed.RecoveryRequest, token recoveryToken) {
	op := "resource.recover"
	key := "evidence-recovery-" + q.MutationKey
	digest := managed.RecoveryDigest(struct {
		ResourceID string
		Request    managed.RecoveryRequest
	}{id, q})
	prior, receipt, found, err := s.ledger.EvidenceRecovery(key, digest)
	if err != nil {
		reject(w, op, err)
		return
	}
	if found {
		if receipt != nil && receipt.Complete {
			send(w, 200, op, receipt, nil)
			return
		}
		if err = s.resumeEvidenceRecovery(prior); err != nil {
			reject(w, op, err)
			return
		}
		_, receipt, _, err = s.ledger.EvidenceRecovery(key, digest)
		if err != nil {
			reject(w, op, err)
			return
		}
		send(w, 200, op, receipt, nil)
		return
	}
	if token.Installation != s.installationID() || token.JobID != "resource:"+id || !time.Now().Before(token.ExpiresAt) {
		reject(w, op, managed.ErrStale)
		return
	}
	s.evidenceMu.Lock()
	defer s.evidenceMu.Unlock()
	snap, jobs, err := s.evidenceRecoveryBasis(id)
	if err != nil {
		reject(w, op, err)
		return
	}
	if managed.RecoveryDigest(snap) != token.SnapshotDigest || managed.RecoveryDigest(jobs) != token.ObservationDigest {
		reject(w, op, managed.ErrStale)
		return
	}
	owner, err := platform.ObserveProcess(os.Getpid())
	if err != nil {
		reject(w, op, err)
		return
	}
	now := time.Now().UTC()
	for i, j := range jobs {
		b, err := s.ledger.BulkSnapshot(j.JobID)
		if err != nil {
			reject(w, op, err)
			return
		}
		jobs[i].CollectDeadline = now.Add(time.Duration(b.Spec.Deadlines.CollectSeconds) * time.Second)
		jobs[i].CleanupDeadline = jobs[i].CollectDeadline.Add(time.Duration(b.Spec.Deadlines.CleanupSeconds) * time.Second)
	}
	intent := managed.EvidenceRecoveryIntent{Schema: "haepari.evidence-recovery-intent/v1", ResourceID: id, ResourceConfigRevision: snap.Resource.ConfigRevision, ResourceAvailabilityRevision: snap.Resource.AvailabilityRevision, SnapshotDigest: token.SnapshotDigest, MutationKey: key, ClientDigest: digest, Action: token.Action, Owner: owner.Identity, Jobs: jobs, RecordedAt: now}
	if token.Action == "dispose" {
		intent.AuthorityReference = "request:" + key
	}
	if err = s.ledger.BeginEvidenceRecovery(intent); err != nil {
		reject(w, op, err)
		return
	}
	if intent.Action == "dispose" {
		observeRetentionBoundary("evidence-disposal-intent")
	}
	if err = s.executeEvidenceRecovery(intent); err != nil {
		reject(w, op, err)
		return
	}
	_, receipt, _, err = s.ledger.EvidenceRecovery(key, digest)
	if err != nil {
		reject(w, op, err)
		return
	}
	send(w, 200, op, receipt, nil)
}

func (s *runtimeServer) resumeEvidenceRecovery(q managed.EvidenceRecoveryIntent) error {
	// A live old controller could still finish an unobserved filesystem effect.
	if !controllerOwnerStopped(managed.LocalPhase{ControllerOwner: &q.Owner}) {
		return managed.ErrBlocked
	}
	deadline := time.Time{}
	for _, j := range q.Jobs {
		if j.CleanupDeadline.After(deadline) {
			deadline = j.CleanupDeadline
		}
	}
	for !s.evidenceMu.TryLock() {
		if !time.Now().Before(deadline) {
			return managed.ErrBlocked
		}
		time.Sleep(min(25*time.Millisecond, time.Until(deadline)))
	}
	defer s.evidenceMu.Unlock()
	if s.jobWorkerAlive("evidence:" + q.MutationKey) {
		return managed.ErrBlocked
	}
	return s.executeEvidenceRecovery(q)
}

func (s *runtimeServer) executeEvidenceRecovery(q managed.EvidenceRecoveryIntent) error {
	snap, err := s.ledger.ResourceRecoverySnapshot(q.ResourceID)
	if err != nil {
		return err
	}
	if snap.EvidenceIntent == nil || managed.RecoveryDigest(*snap.EvidenceIntent) != managed.RecoveryDigest(q) {
		return managed.ErrStale
	}
	backend, err := s.evidenceBackendFor(snap.Resource.Kind, q.ResourceID)
	if err != nil {
		return err
	}
	_, r, _, err := s.ledger.EvidenceRecovery(q.MutationKey, q.ClientDigest)
	if err != nil {
		return err
	}
	if r == nil {
		r = &managed.EvidenceRecoveryReceipt{Schema: "haepari.evidence-recovery-receipt/v1", MutationKey: q.MutationKey, Jobs: []managed.EvidenceRecoveryOutcome{}, Observation: recoveryObservation("evidence-recovery-intent", q, "resource:"+q.ResourceID)}
		for _, j := range q.Jobs {
			r.Jobs = append(r.Jobs, managed.EvidenceRecoveryOutcome{JobID: j.JobID, ResultDigest: j.ResultDigest, Stage: "pending", Artifacts: []managed.ResultArtifact{}})
		}
	}
	if r.ActiveOwner != nil && !controllerOwnerStopped(managed.LocalPhase{ControllerOwner: r.ActiveOwner}) {
		return managed.ErrBlocked
	}
	markEffect := func() error {
		owner, err := platform.ObserveProcess(os.Getpid())
		if err != nil {
			return err
		}
		r.ActiveOwner = &owner.Identity
		r.Observation = recoveryObservation("evidence-recovery-worker-intent", *r, "resource:"+q.ResourceID)
		return s.ledger.RecordEvidenceRecovery(q, *r)
	}
	start := func(work func()) bool { return s.startJobWorker("evidence:"+q.MutationKey, work) }
	for i, j := range q.Jobs {
		o := &r.Jobs[i]
		if o.Stage == "resolved" || o.Stage == "failed" {
			continue
		}
		if !time.Now().Before(j.CleanupDeadline) {
			// A stopped, expired intent can settle without opening evidence or
			// granting another filesystem worker any time allowance.
			r.ActiveOwner = nil
			o.Stage = "failed"
			o.Gap = "original evidence recovery deadline expired"
			r.Observation = recoveryObservation("evidence-recovery-expired", *r, "resource:"+q.ResourceID)
			if err = s.ledger.RecordEvidenceRecovery(q, *r); err != nil {
				return err
			}
			continue
		}
		root, err := backend.Observe(j.Identity)
		if err != nil {
			return err
		}
		if err != nil || root != j.Root {
			return managed.ErrBlocked
		}
		jobRoot := filepath.Join(s.root.Path, "jobs", j.JobID)
		if o.Stage == "pending" && q.Action == "recollect" {
			if err = markEffect(); err != nil {
				return err
			}
			destination := "recovery-" + q.MutationKey
			collected, status := boundedPhaseWithWorker(j.CollectDeadline, s.detached, start, func() collectionAttempt {
				observeRetentionBoundary("collect-worker")
				return backend.Collect(j, filepath.Join(jobRoot, destination))
			})
			if status == phaseUnproven || collected.unproven {
				return managed.ErrBlocked
			}
			r.ActiveOwner = nil
			o.Artifacts = collected.artifacts
			o.ArtifactRoot = destination
			if status != phaseInTime || !collected.complete {
				o.Stage = "failed"
				o.Gap = "required evidence remains uncollected"
			} else {
				o.Stage = "collected"
			}
			r.Observation = recoveryObservation("supplementary-collection", *r, "resource:"+q.ResourceID)
			if err = s.ledger.RecordEvidenceRecovery(q, *r); err != nil {
				return err
			}
			observeRetentionBoundary("collection-recorded")
			if o.Stage == "failed" {
				continue
			}
		}
		if o.Stage == "collected" {
			// Revalidation is a prerequisite of cleanup and consumes that same
			// saved deadline. An unobserved reader keeps its durable owner.
			if err = markEffect(); err != nil {
				return err
			}
			verified, status := boundedPhaseWithWorker(j.CleanupDeadline, s.detached, start, func() error {
				observeRetentionBoundary("verify-worker")
				for _, a := range o.Artifacts {
					if !time.Now().Before(j.CleanupDeadline) {
						return context.DeadlineExceeded
					}
					f, err := openOwnedEvidence(s.root.Path, j.JobID, o.ArtifactRoot+"/"+a.Path)
					if err != nil {
						return err
					}
					_, err = checkedArtifactUntil(f, &a, a.Path, j.CleanupDeadline)
					f.Close()
					if err != nil {
						return err
					}
				}
				return nil
			})
			if status == phaseUnproven {
				return managed.ErrBlocked
			}
			r.ActiveOwner = nil
			if status != phaseInTime || verified != nil {
				o.Stage = "failed"
				o.Gap = "supplementary evidence verification failed or expired"
				r.Observation = recoveryObservation("supplementary-verification-failed", *r, "resource:"+q.ResourceID)
				if err = s.ledger.RecordEvidenceRecovery(q, *r); err != nil {
					return err
				}
				continue
			}
		}
		if err = markEffect(); err != nil {
			return err
		}
		cleanup, status := boundedPhaseWithWorker(j.CleanupDeadline, s.detached, start, func() error {
			return backend.Cleanup(j)
		})
		if status == phaseUnproven || errors.Is(cleanup, runner.ErrUnproven) {
			return managed.ErrBlocked
		}
		r.ActiveOwner = nil
		if status == phaseInTime && cleanup == nil {
			o.Stage = "resolved"
		} else {
			o.Stage = "failed"
			o.Gap = "owned cleanup remains unverified"
		}
		r.Observation = recoveryObservation("evidence-recovery-cleanup", *r, "resource:"+q.ResourceID)
		if err = s.ledger.RecordEvidenceRecovery(q, *r); err != nil {
			return err
		}
	}
	r.Complete = true
	r.Observation = recoveryObservation("evidence-recovery-complete", *r, "resource:"+q.ResourceID)
	return s.ledger.RecordEvidenceRecovery(q, *r)
}
