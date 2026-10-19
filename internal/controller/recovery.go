package controller

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/runner"
)

type jobRecoveryRequest = managed.RecoveryRequest

type recoveryToken struct {
	Installation      string    `json:"installation_id"`
	JobID             string    `json:"job_id"`
	Action            string    `json:"action"`
	SnapshotDigest    string    `json:"snapshot_digest"`
	ObservationDigest string    `json:"observation_digest"`
	ExpiresAt         time.Time `json:"expires_at"`
	BudgetDigest      string    `json:"budget_digest,omitempty"`
}

type jobRecoveryPlan struct {
	Schema       string                      `json:"schema"`
	Snapshot     managed.RecoverySnapshot    `json:"snapshot"`
	Action       string                      `json:"action"`
	Observation  any                         `json:"observation"`
	ObservedAt   time.Time                   `json:"observed_at"`
	Unknowns     []string                    `json:"unknowns"`
	Effects      []string                    `json:"proposed_effects"`
	EvidenceLoss []managed.ProtectedEvidence `json:"evidence_loss"`
	PlanToken    string                      `json:"plan_token,omitempty"`
	ExpiresAt    time.Time                   `json:"expires_at"`
}

func (s *runtimeServer) signRecoveryToken(t recoveryToken) string {
	raw, _ := json.Marshal(t)
	mac := hmac.New(sha256.New, s.recoveryKey[:])
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *runtimeServer) readRecoveryToken(raw string) (recoveryToken, error) {
	var t recoveryToken
	if len(raw) > 4096 {
		return t, managed.ErrInvalid
	}
	payload, signature, ok := strings.Cut(raw, ".")
	if !ok {
		return t, managed.ErrInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return t, managed.ErrInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return t, managed.ErrInvalid
	}
	mac := hmac.New(sha256.New, s.recoveryKey[:])
	_, _ = mac.Write(data)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return t, managed.ErrStale
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if managed.ValidateUniqueJSONKeys(data) != nil || d.Decode(&t) != nil || t.Installation != s.installationID() {
		return t, managed.ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return t, managed.ErrInvalid
	}
	for name := range fields {
		if name != "budget_digest" && strings.EqualFold(name, "budget_digest") {
			return t, managed.ErrInvalid
		}
	}
	if _, present := fields["budget_digest"]; present {
		digest, prefixed := strings.CutPrefix(t.BudgetDigest, "sha256:")
		if !prefixed || len(digest) != 64 || strings.ToLower(digest) != digest {
			return t, managed.ErrInvalid
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return t, managed.ErrInvalid
		}
	}
	return t, nil
}

func (s *runtimeServer) planJobRecovery(jobID, action string) (jobRecoveryPlan, error) {
	if action == "" {
		action = "reconcile"
	}
	if action != "reconcile" && action != "attest-stopped" && action != "attest-no-start" {
		return jobRecoveryPlan{}, managed.ErrInvalid
	}
	snap, err := s.ledger.RecoverySnapshot(jobID)
	if err != nil {
		return jobRecoveryPlan{}, err
	}
	p := jobRecoveryPlan{Schema: "haepari.recovery-plan/v1", Snapshot: snap, Action: action, ObservedAt: time.Now().UTC(), Unknowns: []string{}, Effects: []string{}, EvidenceLoss: []managed.ProtectedEvidence{}}
	if snap.Receipt.JobState != "reconciling" {
		return p, managed.ErrBlocked
	}
	if snap.Resource.Kind == "utm-macos" {
		return s.planVMJobRecovery(p)
	}
	if snap.Resource.Kind != "local" {
		if action != "reconcile" {
			return p, managed.ErrUnsupported
		}
		for _, phase := range snap.Phases {
			p.EvidenceLoss = append(p.EvidenceLoss, phase.ProtectedEvidence...)
		}
		p.Observation = map[string]any{"ownership_kind": snap.Resource.Kind, "adapter_intents": snap.AdapterIntents}
		p.Unknowns = append(p.Unknowns, "generation-bound backend observation and evidence-preserving cleanup are required")
		return p, nil
	}
	workerAlive := s.jobWorkerAlive(jobID)
	if workerAlive {
		p.Unknowns = append(p.Unknowns, "a resumed filesystem worker is still active")
	}
	for _, name := range []string{"prepare", "collect", "cleanup"} {
		phase, ok := snap.Phases[name]
		if !ok {
			continue
		}
		p.EvidenceLoss = append(p.EvidenceLoss, phase.ProtectedEvidence...)
		if !controllerOwnerStopped(phase) {
			p.Unknowns = append(p.Unknowns, name+" worker termination is unproven")
		}
	}
	clockValid := true
	for _, phase := range snap.Phases {
		if p.ObservedAt.Before(phase.ObservedAt) {
			clockValid = false
			p.Unknowns = append(p.Unknowns, "phase observation is ahead of the current clock")
			break
		}
	}
	owned, rootErr := s.inspectRecoveryRoot(snap.Identity)
	observation := map[string]any{"owned_root": owned, "owned_root_valid": rootErr == nil, "clock_valid": clockValid}
	observation["resumed_worker_alive"] = workerAlive
	attestationBlocked := recoveryOwnerAlive(snap, runner.Observation{}, false)
	stopped := map[string]bool{}
	for _, name := range []string{"prepare", "collect", "cleanup"} {
		if p, ok := snap.Phases[name]; ok {
			stopped[name] = controllerOwnerStopped(p)
		}
	}
	observation["phase_workers_stopped"] = stopped
	if rootErr != nil {
		p.Unknowns = append(p.Unknowns, "owned input receipt is unproven")
	}
	if len(snap.AdapterIntents) > 0 {
		p.Unknowns = append(p.Unknowns, "adapter intent requires its generation-bound backend observer")
	}
	if snap.Identity.RunID != "" {
		request, view, err := s.inspectRecoveryRunner(snap)
		attestationBlocked = recoveryOwnerAlive(snap, view, err == nil)
		observation["request"], observation["receipts"], observation["receipts_valid"] = request, view, err == nil
		live := false
		if err == nil && view.Intent != nil {
			observed, e := platform.ObserveProcess(view.Intent.Supervisor.PID)
			live = e == nil && !observed.Zombie && observed.Identity == view.Intent.Supervisor
			if live {
				observation["supervisor"] = observed.Identity
			}
		}
		if !snap.Stopped && !(err == nil && view.Exit != nil && view.Exit.TerminationProven) && !live {
			p.Unknowns = append(p.Unknowns, "workload termination or surviving supervisor is unproven")
		}
		if action == "attest-no-start" && (view.Start != nil || snap.Outcome != nil && snap.Outcome.Started) {
			return p, managed.ErrConflict
		}
	} else if !snap.Stopped {
		if phase, ok := snap.Phases["prepare"]; !ok || !controllerOwnerStopped(phase) {
			p.Unknowns = append(p.Unknowns, "preparation stop is unproven")
		}
	}
	observation["attestation_owner_alive"] = attestationBlocked
	attestationRelevant := action != "reconcile" || snap.Attestation != nil
	if attestationRelevant && attestationBlocked {
		p.Unknowns = append(p.Unknowns, "a live execution or filesystem owner contradicts termination attestation")
	}
	p.Observation = observation
	if clockValid && !workerAlive && !(attestationRelevant && attestationBlocked) && (len(p.Unknowns) == 0 || action != "reconcile" && rootErr == nil && len(snap.AdapterIntents) == 0) {
		p.Effects = []string{"commit revision-bound recovery intent", "observe existing execution without a new start", "consume durable phases; bounded owned cleanup or retain protected evidence", "settle only with termination proof"}
		if action != "reconcile" {
			p.Effects[1] = "record scoped operator termination evidence; keep workload outcome inconclusive"
		}
		p.ExpiresAt = time.Now().UTC().Add(2 * time.Minute)
		p.PlanToken = s.signRecoveryToken(recoveryToken{s.installationID(), jobID, action, managed.RecoveryDigest(snap), managed.RecoveryDigest(observation), p.ExpiresAt, ""})
	}
	return p, nil
}

func (s *runtimeServer) jobRecovery(w http.ResponseWriter, r *http.Request) {
	op := "job.recover"
	var q jobRecoveryRequest
	if err := decodeBody(r, &q); err != nil {
		reject(w, op, err)
		return
	}
	id := r.PathValue("id")
	if q.Plan {
		if q.PlanToken != "" || q.MutationKey != "" || q.Evidence != nil {
			reject(w, op, managed.ErrInvalid)
			return
		}
		plan, err := s.planJobRecovery(id, q.Action)
		if err != nil {
			reject(w, op, err)
			return
		}
		send(w, 200, op, plan, nil)
		return
	}
	if q.PlanToken == "" || q.Action != "" || !managed.ValidRequestKey(q.MutationKey) || len(q.MutationKey) > 100 {
		reject(w, op, managed.ErrInvalid)
		return
	}
	clientDigest := managed.RecoveryDigest(struct {
		JobID   string             `json:"job_id"`
		Request jobRecoveryRequest `json:"request"`
	}{id, q})
	key := "recovery-" + q.MutationKey
	if receipt, found, err := s.ledger.ReplayJobRecovery(key, clientDigest); found || err != nil {
		if err != nil {
			reject(w, op, err)
		} else {
			send(w, 200, op, receipt, nil)
		}
		return
	}
	token, err := s.readRecoveryToken(q.PlanToken)
	if err != nil {
		reject(w, op, err)
		return
	}
	if token.JobID != id || !time.Now().Before(token.ExpiresAt) {
		reject(w, op, managed.ErrStale)
		return
	}
	if token.Action == "reconcile" && q.Evidence != nil || token.Action != "reconcile" && (q.Evidence == nil || q.Evidence.Source != "operator-attested") {
		reject(w, op, managed.ErrInvalid)
		return
	}
	if q.Evidence != nil && managed.ValidateRecoveryEvidence(*q.Evidence) != nil {
		reject(w, op, managed.ErrInvalid)
		return
	}
	current, err := s.planJobRecovery(id, token.Action)
	if err != nil {
		reject(w, op, err)
		return
	}
	if managed.RecoveryDigest(current.Snapshot) != token.SnapshotDigest || managed.RecoveryDigest(current.Observation) != token.ObservationDigest {
		reject(w, op, managed.ErrStale)
		return
	}
	current.ExpiresAt = token.ExpiresAt
	if err = s.applyPlannedJobRecovery(current, q.Evidence, key, clientDigest); err != nil {
		reject(w, op, err)
		return
	}
	receipt, found, err := s.ledger.ReplayJobRecovery(key, clientDigest)
	if err != nil || !found {
		reject(w, op, managed.ErrBlocked)
		return
	}
	send(w, 200, op, receipt, nil)
}
