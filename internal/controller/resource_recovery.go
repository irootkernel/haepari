package controller

import (
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"golang.org/x/sys/unix"
)

type resourceRecoveryPlan struct {
	Schema       string                           `json:"schema"`
	Snapshot     managed.ResourceRecoverySnapshot `json:"snapshot"`
	Observation  any                              `json:"observation"`
	ObservedAt   time.Time                        `json:"observed_at"`
	Unknowns     []string                         `json:"unknowns"`
	Effects      []string                         `json:"proposed_effects"`
	EvidenceLoss []managed.ProtectedEvidence      `json:"evidence_loss"`
	PlanToken    string                           `json:"plan_token,omitempty"`
	ExpiresAt    time.Time                        `json:"expires_at"`
}

func (s *runtimeServer) resumeResourceRecovery(id string) error {
	snap, err := s.ledger.ResourceRecoverySnapshot(id)
	if err != nil {
		return err
	}
	if snap.Resource.Kind != "local" || snap.Pending == nil || snap.PendingStatus == nil {
		return managed.ErrBlocked
	}
	facts, healthy := s.observeResourceRecovery(snap)
	evidence := recoveryObservation("local-resource-recovery-verification", facts, "resource:"+id)
	_, err = s.ledger.CompleteResourceRecovery(*snap.PendingStatus, snap.Pending.MutationKey+"-complete", snap.Pending.ClientDigest, evidence, healthy)
	return err
}

func (s *runtimeServer) observeResourceRecovery(snap managed.ResourceRecoverySnapshot) (any, bool) {
	fd, err := unix.Open(filepath.Join(s.root.Path, "jobs"), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return map[string]any{"jobs_root_valid": false}, false
	}
	stat, err := privateLogStat(fd, true)
	unix.Close(fd)
	if err != nil {
		return map[string]any{"jobs_root_valid": false}, false
	}
	facts := map[string]any{"jobs_root_device": stat.Dev, "jobs_root_inode": stat.Ino, "jobs_root_mode": stat.Mode, "jobs_root_uid": stat.Uid}
	absence := map[string]bool{}
	healthy := true
	for _, j := range snap.Jobs {
		jobFD, err := openPrivateJob(s.root.Path, j.JobID)
		if errors.Is(err, unix.ENOENT) || errors.Is(err, errLogMissing) {
			absence[j.JobID] = true
			continue
		}
		if err != nil {
			absence[j.JobID] = false
			healthy = false
			continue
		}
		var workspace unix.Stat_t
		err = unix.Fstatat(jobFD, "workspace", &workspace, unix.AT_SYMLINK_NOFOLLOW)
		unix.Close(jobFD)
		absent := errors.Is(err, unix.ENOENT)
		absence[j.JobID] = absent
		if !absent {
			healthy = false
		}
	}
	facts["owned_workspaces_absent"] = absence
	return facts, healthy
}

func (s *runtimeServer) planResourceRecovery(id string) (resourceRecoveryPlan, error) {
	snap, err := s.ledger.ResourceRecoverySnapshot(id)
	if err != nil {
		return resourceRecoveryPlan{}, err
	}
	p := resourceRecoveryPlan{Schema: "haepari.recovery-plan/v1", Snapshot: snap, ObservedAt: time.Now().UTC(), Unknowns: []string{}, Effects: []string{}, EvidenceLoss: []managed.ProtectedEvidence{}}
	if snap.Resource.Kind != "local" {
		for _, j := range snap.Jobs {
			p.EvidenceLoss = append(p.EvidenceLoss, j.Result.ProtectedEvidence...)
		}
		p.Observation = map[string]any{"ownership_kind": snap.Resource.Kind, "adapter_intents": snap.AdapterIntents}
		p.Unknowns = append(p.Unknowns, "generation-bound backend observation and evidence-preserving cleanup are required")
		return p, nil
	}
	if snap.Resource.Availability != "quarantined" && snap.Resource.Availability != "recovering" {
		return p, managed.ErrBlocked
	}
	for _, j := range snap.Jobs {
		p.EvidenceLoss = append(p.EvidenceLoss, j.Result.ProtectedEvidence...)
		if len(j.Result.ProtectedEvidence) > 0 || j.Result.CleanupOutcome == "failed" || j.Result.CleanupOutcome == "unknown" {
			p.Unknowns = append(p.Unknowns, "job:"+j.JobID+" requires evidence-preserving collection or disposal before reuse")
		}
	}
	if len(snap.AdapterIntents) > 0 {
		p.Unknowns = append(p.Unknowns, "unresolved adapter intents remain protected")
	}
	observation, healthy := s.observeResourceRecovery(snap)
	p.Observation = observation
	if !healthy {
		p.Unknowns = append(p.Unknowns, "owned workspace absence or local readiness is unproven")
	}
	if len(p.Unknowns) == 0 {
		p.Effects = []string{"commit resource recovery intent", "verify local readiness and owned workspace absence", "return availability without changing any Job result"}
		p.ExpiresAt = time.Now().UTC().Add(2 * time.Minute)
		p.PlanToken = s.signRecoveryToken(recoveryToken{s.installationID(), "resource:" + id, "reconcile", managed.RecoveryDigest(snap), managed.RecoveryDigest(observation), p.ExpiresAt})
	}
	return p, nil
}

func (s *runtimeServer) resourceRecovery(w http.ResponseWriter, r *http.Request) {
	op := "resource.recover"
	var q managed.RecoveryRequest
	if err := decodeBody(r, &q); err != nil {
		reject(w, op, err)
		return
	}
	id := r.PathValue("id")
	if q.Action != "" || q.Evidence != nil {
		reject(w, op, managed.ErrInvalid)
		return
	}
	if q.Plan {
		if q.PlanToken != "" || q.MutationKey != "" {
			reject(w, op, managed.ErrInvalid)
			return
		}
		plan, err := s.planResourceRecovery(id)
		if err != nil {
			reject(w, op, err)
			return
		}
		send(w, 200, op, plan, nil)
		return
	}
	if q.PlanToken == "" || !managed.ValidRequestKey(q.MutationKey) || len(q.MutationKey) > 90 {
		reject(w, op, managed.ErrInvalid)
		return
	}
	key := "resource-recovery-" + q.MutationKey
	digest := managed.RecoveryDigest(struct {
		ResourceID string                  `json:"resource_id"`
		Request    managed.RecoveryRequest `json:"request"`
	}{id, q})
	if prior, found, err := s.ledger.ReplayResourceRecovery(key, digest); found || err != nil {
		if err != nil {
			reject(w, op, err)
		} else {
			send(w, 200, op, prior, nil)
		}
		return
	}
	token, err := s.readRecoveryToken(q.PlanToken)
	if err != nil {
		reject(w, op, err)
		return
	}
	if token.JobID != "resource:"+id || token.Action != "reconcile" || !time.Now().Before(token.ExpiresAt) {
		reject(w, op, managed.ErrStale)
		return
	}
	plan, err := s.planResourceRecovery(id)
	if err != nil {
		reject(w, op, err)
		return
	}
	if managed.RecoveryDigest(plan.Snapshot) != token.SnapshotDigest || managed.RecoveryDigest(plan.Observation) != token.ObservationDigest {
		reject(w, op, managed.ErrStale)
		return
	}
	evidence := recoveryObservation("local-resource-readiness", plan.Observation, "resource:"+id)
	intent := managed.ResourceRecoveryIntent{Schema: "haepari.resource-recovery-intent/v1", ResourceID: id, SnapshotDigest: token.SnapshotDigest, ClientDigest: digest, MutationKey: key, Evidence: evidence}
	started, err := s.ledger.BeginResourceRecovery(intent)
	if err != nil {
		reject(w, op, err)
		return
	}
	facts, healthy := s.observeResourceRecovery(plan.Snapshot)
	evidence = recoveryObservation("local-resource-recovery-verification", facts, "resource:"+id)
	_, err = s.ledger.CompleteResourceRecovery(started, key+"-complete", digest, evidence, healthy)
	if err != nil {
		reject(w, op, err)
		return
	}
	s.notifyScheduler()
	completed, _, err := s.ledger.ReplayResourceRecovery(key, digest)
	if err != nil {
		reject(w, op, err)
		return
	}
	send(w, 200, op, completed, nil)
}
