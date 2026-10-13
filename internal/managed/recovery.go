package managed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

// RecoveryEvidence identifies the observation, rather than treating a caller's
// assertion as a machine proof. Operator evidence cannot establish PASS.
type RecoveryEvidence struct {
	Source     string    `json:"source"`
	Observer   string    `json:"observer"`
	ObservedAt time.Time `json:"observed_at"`
	Mechanism  string    `json:"mechanism"`
	Digest     string    `json:"digest"`
	Reference  string    `json:"reference"`
}

type RecoveryRequest struct {
	Plan        bool              `json:"plan"`
	Action      string            `json:"action,omitempty"`
	PlanToken   string            `json:"plan_token,omitempty"`
	MutationKey string            `json:"mutation_key,omitempty"`
	Evidence    *RecoveryEvidence `json:"evidence,omitempty"`
}

func (e RecoveryEvidence) valid() bool {
	_, offset := e.ObservedAt.Zone()
	return oneOf(e.Source, "machine-observed", "operator-attested") && e.Observer != "" && len(e.Observer) <= 256 &&
		!e.ObservedAt.IsZero() && offset == 0 && e.Mechanism != "" && len(e.Mechanism) <= 256 && validRecoveryDigest(e.Digest) && e.Reference != "" && len(e.Reference) <= 1024
}

func ValidateRecoveryEvidence(e RecoveryEvidence) error {
	if !e.valid() {
		return ErrInvalid
	}
	return nil
}

func validRecoveryDigest(s string) bool {
	return strings.HasPrefix(s, "sha256:") && validDigest(strings.TrimPrefix(s, "sha256:"))
}

// RecoverySnapshot is read from one transaction. Its digest covers all active
// event bytes, receipt bytes, ownership and resource fences. It contains no
// freshly generated timestamp, so reading a plan never changes its basis.
type RecoverySnapshot struct {
	Installation      string                     `json:"installation_id"`
	Identity          LocalIdentity              `json:"identity"`
	Receipt           Receipt                    `json:"receipt"`
	Resource          ResourceStatus             `json:"resource"`
	Spec              JobSpec                    `json:"spec"`
	Phases            map[string]LocalPhase      `json:"phases"`
	Runner            map[string]json.RawMessage `json:"runner_receipts"`
	Outcome           *LocalOutcome              `json:"outcome,omitempty"`
	Stopped           bool                       `json:"termination_proven"`
	NoOwnedEffect     bool                       `json:"no_owned_effect"`
	EventsDigest      string                     `json:"events_digest"`
	AdapterIntents    []PowerIntent              `json:"adapter_intents"`
	InterruptedFrom   string                     `json:"interrupted_from,omitempty"`
	PriorResource     *ResourceStatus            `json:"prior_resource,omitempty"`
	Attestation       *RecoveryEvidence          `json:"attestation,omitempty"`
	DeadlineRequested bool                       `json:"deadline_requested"`
}

func RecoveryDigest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (l *Ledger) RecoverySnapshot(jobID string) (RecoverySnapshot, error) {
	if !identifier.MatchString(jobID) {
		return RecoverySnapshot{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RecoverySnapshot{}, err
	}
	defer tx.Rollback()
	return l.recoverySnapshotTx(tx, jobID)
}

func (l *Ledger) recoverySnapshotTx(tx *sql.Tx, jobID string) (RecoverySnapshot, error) {
	s := RecoverySnapshot{Runner: map[string]json.RawMessage{}, AdapterIntents: []PowerIntent{}}
	var err error
	s.Installation, err = installationTx(tx)
	if err != nil {
		return s, err
	}
	s.Receipt, err = l.snapshotTx(tx, jobID)
	if err != nil {
		return s, err
	}
	if s.Receipt.ResultStatus == "unavailable" {
		return s, ErrResultUnavailable
	}
	s.Resource, err = resourceStatusTx(tx, s.Receipt.ResourceID)
	if err != nil {
		return s, err
	}
	var raw []byte
	if err = tx.QueryRow(`SELECT spec_json FROM managed_jobs WHERE id=?`, jobID).Scan(&raw); err != nil {
		return s, err
	}
	s.Spec, err = DecodeJob(raw)
	if err != nil {
		return s, err
	}
	if s.Receipt.JobState != "queued" && s.Receipt.JobState != "finished" {
		raw, err = localIdentityTx(tx, jobID)
		if err != nil {
			return s, err
		}
		if err = decodeClosed(raw, &s.Identity); err != nil {
			return s, err
		}
	}
	s.Phases, err = effectiveLocalPhasesTx(tx, jobID)
	if err != nil {
		return s, err
	}
	rows, err := tx.Query(`SELECT kind,payload_json,digest FROM managed_runner_receipts WHERE job_id=? ORDER BY kind`, jobID)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var kind, digest string
		if err = rows.Scan(&kind, &raw, &digest); err != nil {
			rows.Close()
			return s, err
		}
		sum := sha256.Sum256(raw)
		if "sha256:"+hex.EncodeToString(sum[:]) != digest {
			rows.Close()
			return s, ErrBlocked
		}
		s.Runner[kind] = append(json.RawMessage(nil), raw...)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return s, err
	}
	var proof []byte
	err = tx.QueryRow(`SELECT outcome_json,proof_json FROM managed_local_outcomes WHERE job_id=?`, jobID).Scan(&raw, &proof)
	if err == nil {
		s.Outcome = new(LocalOutcome)
		if err = decodeClosed(raw, s.Outcome); err != nil {
			return s, err
		}
		if len(proof) > 0 {
			var p LocalTerminationRequest
			if err = decodeClosed(proof, &p); err != nil || p.Identity != s.Identity {
				return s, ErrBlocked
			}
			s.Stopped, s.NoOwnedEffect = true, p.NoOwnedEffect
		}
	} else if err != sql.ErrNoRows {
		return s, err
	}
	rows, err = tx.Query(`SELECT cursor,kind,payload_json FROM managed_events WHERE job_id=? ORDER BY cursor`, jobID)
	if err != nil {
		return s, err
	}
	h := sha256.New()
	encoder := json.NewEncoder(h)
	for rows.Next() {
		var event Event
		event.JobID = jobID
		if err = rows.Scan(&event.Cursor, &event.Kind, &event.Payload); err != nil {
			rows.Close()
			return s, err
		}
		if err = encoder.Encode(event); err != nil {
			rows.Close()
			return s, err
		}
		if event.Kind == "interrupted" {
			var value struct {
				From     string          `json:"from"`
				Resource *ResourceStatus `json:"resource"`
			}
			if err = json.Unmarshal(event.Payload, &value); err != nil {
				rows.Close()
				return s, err
			}
			s.InterruptedFrom, s.PriorResource = value.From, value.Resource
		}
		if event.Kind == "execution_deadline" {
			s.DeadlineRequested = true
		}
		if event.Kind == "recovery_intent" || event.Kind == "phase_reconciled" {
			var value struct {
				Evidence RecoveryEvidence `json:"evidence"`
			}
			if err = json.Unmarshal(event.Payload, &value); err != nil {
				rows.Close()
				return s, err
			}
			if value.Evidence.Source == "operator-attested" {
				e := value.Evidence
				s.Attestation = &e
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return s, err
	}
	s.EventsDigest = "sha256:" + hex.EncodeToString(h.Sum(nil))
	s.AdapterIntents, err = pendingRecoveryIntentsTx(tx, s.Resource.ID)
	return s, err
}

// Keep an issued adapter command and its progress together. Reopening an
// intent never revokes its external effect or permits issuing it again.
func pendingRecoveryIntentsTx(tx *sql.Tx, resourceID string) ([]PowerIntent, error) {
	rows, err := tx.Query(`SELECT p.identity_json,p.state,g.stage FROM managed_power_transitions p LEFT JOIN managed_power_progress g USING(operation_generation) WHERE p.state IN ('pending','uncertain') AND (p.victim_resource_id=? OR p.target_resource_id=?) ORDER BY p.operation_generation`, resourceID, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	intents := []PowerIntent{}
	for rows.Next() {
		var raw []byte
		var p PowerIntent
		if err := rows.Scan(&raw, &p.State, &p.Stage); err != nil {
			return nil, err
		}
		record, err := decodePowerTransition(raw)
		if err != nil || !oneOf(p.Stage, "stop_ready", "stop_issued", "stop_uncertain", "start_ready", "start_issued", "start_uncertain") {
			return nil, ErrBlocked
		}
		p.Identity = record.PowerTransitionIdentity
		intents = append(intents, p)
	}
	return intents, rows.Err()
}

type JobRecoveryRequest struct {
	Identity       LocalIdentity    `json:"identity"`
	SnapshotDigest string           `json:"snapshot_digest"`
	MutationKey    string           `json:"mutation_key"`
	Action         string           `json:"action"`
	NextState      string           `json:"next_state"`
	Evidence       RecoveryEvidence `json:"evidence"`
	Outcome        *LocalOutcome    `json:"outcome,omitempty"`
	NoOwnedEffect  bool             `json:"no_owned_effect,omitempty"`
	Automatic      bool             `json:"automatic,omitempty"`
	ClientDigest   string           `json:"client_digest,omitempty"`
}

// CommittedJobRecovery reads an existing authority; it does not admit a new
// recovery or renew an effect deadline. The caller still proves current owners.
func (l *Ledger) CommittedJobRecovery(id LocalIdentity, key string) (JobRecoveryRequest, error) {
	var q JobRecoveryRequest
	if !ValidRequestKey(key) {
		return q, ErrInvalid
	}
	var raw []byte
	err := l.db.QueryRow(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='recovery_intent' AND json_extract(payload_json,'$.mutation_key')=? ORDER BY cursor DESC LIMIT 1`, id.JobID, key).Scan(&raw)
	if err != nil {
		return q, mapNoRows(err)
	}
	if decodeClosed(raw, &q) != nil || q.Identity != id || q.MutationKey != key || !q.Evidence.valid() {
		return q, ErrBlocked
	}
	return q, nil
}

// ReplayJobRecovery compares the permanent public request identity before
// current token expiry or lease checks. It returns an observation receipt, not
// another execution permit. A different token/evidence under that key conflicts.
func (l *Ledger) ReplayJobRecovery(key, clientDigest string) (Receipt, bool, error) {
	if !ValidRequestKey(key) || !validRecoveryDigest(clientDigest) {
		return Receipt{}, false, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Receipt{}, false, err
	}
	defer tx.Rollback()
	var raw []byte
	err = tx.QueryRow(`SELECT receipt_json FROM managed_mutations WHERE mutation_key=?`, key).Scan(&raw)
	if err == sql.ErrNoRows {
		return Receipt{}, false, nil
	}
	if err != nil {
		return Receipt{}, false, err
	}
	var receipt Receipt
	if err = decodeClosed(raw, &receipt); err != nil {
		return receipt, true, err
	}
	var intent []byte
	err = tx.QueryRow(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='recovery_intent' AND json_extract(payload_json,'$.mutation_key')=? ORDER BY cursor DESC LIMIT 1`, receipt.JobID, key).Scan(&intent)
	if err != nil {
		return Receipt{}, true, ErrBlocked
	}
	var q JobRecoveryRequest
	if err = decodeClosed(intent, &q); err != nil {
		return Receipt{}, true, err
	}
	if q.ClientDigest != clientDigest {
		return Receipt{}, true, ErrConflict
	}
	return receipt, true, nil
}

// BeginJobRecovery commits the exact intent before any resumed filesystem or
// cancellation effect. Imported facts alone do not authorize starting work.
func (l *Ledger) BeginJobRecovery(q JobRecoveryRequest) (Receipt, error) {
	if !validRecoveryDigest(q.SnapshotDigest) || !q.Evidence.valid() || q.Automatic && q.Evidence.Source != "machine-observed" || !oneOf(q.Action, "reattach", "resume", "stopped", "no-start") ||
		!oneOf(q.NextState, "running", "cancelling", "collecting", "cleaning", "settling", "preparing", "awaiting_manual") {
		return Receipt{}, ErrInvalid
	}
	return l.mutate("job_recovery", q.MutationKey, q, func(tx *sql.Tx) (Receipt, error) {
		s, err := l.recoverySnapshotTx(tx, q.Identity.JobID)
		if err != nil {
			return Receipt{}, err
		}
		if s.Identity != q.Identity || RecoveryDigest(s) != q.SnapshotDigest {
			return Receipt{}, ErrStale
		}
		if s.Receipt.JobState != "reconciling" || !oneOf(s.Resource.Kind, "local", "utm-macos") {
			return Receipt{}, ErrBlocked
		}
		if len(s.AdapterIntents) != 0 {
			return Receipt{}, ErrBlocked
		}
		manualResume := oneOf(q.NextState, "preparing", "awaiting_manual")
		if manualResume {
			m, err := manualSessionTx(tx, q.Identity.JobID)
			if err != nil || s.Spec.Mode != "manual" || m.Legacy || m.Identity != q.Identity || m.Outcome != nil || q.Action != "reattach" {
				return Receipt{}, ErrBlocked
			}
			if q.NextState == "awaiting_manual" && (!m.Stopped || m.Preparation == nil || m.Preparation.WorkloadOutcome != "pass" || m.Deadline.IsZero()) {
				return Receipt{}, ErrBlocked
			}
		}
		if q.Action == "reattach" {
			if q.Evidence.Source != "machine-observed" || (!oneOf(q.NextState, "running", "cancelling") && !manualResume) || q.Identity.RunID == "" || q.Outcome != nil || q.NoOwnedEffect {
				return Receipt{}, ErrInvalid
			}
		} else if q.Action == "resume" {
			if !s.Stopped || oneOf(q.NextState, "running", "cancelling") || q.Outcome != nil || q.NoOwnedEffect != s.NoOwnedEffect {
				return Receipt{}, ErrBlocked
			}
		} else {
			if q.Outcome == nil || q.NextState != "collecting" && !(q.NextState == "settling" && q.NoOwnedEffect) {
				return Receipt{}, ErrInvalid
			}
			out := *q.Outcome
			if q.Action == "no-start" && out.Started || q.NoOwnedEffect && out.Started ||
				q.Evidence.Source == "operator-attested" && s.Outcome == nil && (out.WorkloadOutcome != "inconclusive" || out.NativeExitCode != nil || out.NativeSignal != "") {
				return Receipt{}, ErrInvalid
			}
			if !oneOf(string(out.WorkloadOutcome), "pass", "fail", "timed_out", "inconclusive", "cancelled") {
				return Receipt{}, ErrInvalid
			}
			if s.Outcome != nil {
				// Preserve an already frozen outcome and any earlier proof.
				if !reflect.DeepEqual(*s.Outcome, out) {
					return Receipt{}, ErrConflict
				}
			} else {
				status, err := cancellationStatusTx(tx, q.Identity.JobID)
				if err != nil {
					return Receipt{}, err
				}
				if status.Requested && status.BeforeOutcome {
					out.WorkloadOutcome = "cancelled"
				}
				id, _ := json.Marshal(q.Identity)
				data, _ := json.Marshal(out)
				if _, err = tx.Exec(`INSERT INTO managed_local_outcomes(job_id,identity_json,outcome_json) VALUES(?,?,?)`, q.Identity.JobID, id, data); err != nil {
					return Receipt{}, err
				}
			}
			if !s.Stopped {
				proof, _ := json.Marshal(LocalTerminationRequest{Identity: q.Identity, ExpectedRevision: s.Receipt.JobRevision, NoOwnedEffect: q.NoOwnedEffect, MutationKey: q.MutationKey})
				if _, err = tx.Exec(`UPDATE managed_local_outcomes SET proof_json=? WHERE job_id=? AND proof_json IS NULL`, proof, q.Identity.JobID); err != nil {
					return Receipt{}, err
				}
			}
		}
		if _, err := tx.Exec(`UPDATE managed_jobs SET state=?,revision=revision+1 WHERE id=?`, q.NextState, q.Identity.JobID); err != nil {
			return Receipt{}, err
		}
		// Only startup's own quarantine may be lifted automatically, and only
		// after every other occupying Job has left reconciliation. Operator
		// pause and older quarantine retain their original policy and reason.
		if q.Automatic && s.Resource.Reason == "controller_restart_uncertain" && s.PriorResource != nil && oneOf(s.PriorResource.Availability, "available", "paused") {
			var unknown int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM managed_jobs WHERE resource_id=? AND state='reconciling' AND slot IS NOT NULL`, s.Resource.ID).Scan(&unknown); err != nil {
				return Receipt{}, err
			}
			if unknown == 0 {
				if _, err := tx.Exec(`UPDATE managed_resources SET availability=?,availability_reason=?,availability_revision=availability_revision+1 WHERE id=?`, s.PriorResource.Availability, s.PriorResource.Reason, s.Resource.ID); err != nil {
					return Receipt{}, err
				}
				if err := queuedAvailabilityEventsTx(tx, s.Resource.ID, map[string]any{"availability": s.PriorResource.Availability, "reason": s.PriorResource.Reason, "cause": "restart_reconciled"}); err != nil {
					return Receipt{}, err
				}
			}
		}
		cursor, err := addEvent(tx, q.Identity.JobID, "recovery_intent", q)
		if err != nil {
			return Receipt{}, err
		}
		r, err := l.snapshotTx(tx, q.Identity.JobID)
		r.EventCursor = cursor
		return r, err
	})
}

func (l *Ledger) RecordRecoveryClockDiscontinuity(id LocalIdentity, digest string, observed time.Time) error {
	if !validRecoveryDigest(digest) || observed.IsZero() {
		return ErrInvalid
	}
	q := struct {
		Identity       LocalIdentity `json:"identity"`
		SnapshotDigest string        `json:"snapshot_digest"`
		ObservedAt     time.Time     `json:"observed_at"`
	}{id, digest, observed}
	_, err := l.mutate("recovery_clock", "clock-"+id.JobID+"-"+digest[7:23], q, func(tx *sql.Tx) (Receipt, error) {
		s, err := l.recoverySnapshotTx(tx, id.JobID)
		if err != nil {
			return Receipt{}, err
		}
		if s.Identity != id || RecoveryDigest(s) != digest {
			return Receipt{}, ErrStale
		}
		if _, err = tx.Exec(`UPDATE managed_jobs SET revision=revision+1 WHERE id=?`, id.JobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, id.JobID, "clock_discontinuity", q)
		if err != nil {
			return Receipt{}, err
		}
		r, err := l.snapshotTx(tx, id.JobID)
		r.EventCursor = cursor
		return r, err
	})
	return err
}

type PhaseResolutionRequest struct {
	ExpectedRevision int64            `json:"expected_revision"`
	OriginalDigest   string           `json:"original_digest"`
	Record           LocalPhase       `json:"record"`
	Evidence         RecoveryEvidence `json:"evidence"`
	MutationKey      string           `json:"mutation_key"`
}

// ResolveLocalPhase appends a compensating receipt. Original observations,
// including an unproven observation, are never overwritten or re-budgeted.
func (l *Ledger) ResolveLocalPhase(q PhaseResolutionRequest) (Receipt, error) {
	if !q.Evidence.valid() || !validRecoveryDigest(q.OriginalDigest) || !validLocalPhase(q.Record) || !q.Record.Completed || q.Record.Outcome == "unproven" {
		return Receipt{}, ErrInvalid
	}
	return l.mutate("phase_recovery", q.MutationKey, q, func(tx *sql.Tx) (Receipt, error) {
		if err := matchLocalIdentityTx(tx, q.Record.Identity); err != nil {
			return Receipt{}, err
		}
		if err := validatePhaseAdapterStepsTx(tx, q.Record); err != nil {
			return Receipt{}, err
		}
		r, err := l.snapshotTx(tx, q.Record.Identity.JobID)
		if err != nil {
			return Receipt{}, err
		}
		if r.JobRevision != q.ExpectedRevision {
			return Receipt{}, ErrStale
		}
		if !oneOf(r.JobState, "reconciling", "running", "cancelling", "collecting", "cleaning", "settling") {
			m, err := manualSessionTx(tx, r.JobID)
			if r.JobState != "preparing" || q.Record.Phase != "prepare" || err != nil || m.Legacy || m.Identity != q.Record.Identity || m.Outcome != nil {
				return Receipt{}, ErrConflict
			}
		}
		phases, err := effectiveLocalPhasesTx(tx, r.JobID)
		if err != nil {
			return Receipt{}, err
		}
		prior, exists := phases[q.Record.Phase]
		if !exists || prior.Completed && prior.Outcome != "unproven" || RecoveryDigest(prior) != q.OriginalDigest || !samePhaseIntent(prior, q.Record) || q.Record.ObservedAt.Before(prior.ObservedAt) {
			return Receipt{}, ErrConflict
		}
		if q.Record.Phase != "verify" && q.Record.ObservedAt.After(prior.Deadline) && q.Record.Outcome == "complete" {
			return Receipt{}, ErrInvalid
		}
		if _, err = tx.Exec(`UPDATE managed_jobs SET revision=revision+1 WHERE id=?`, r.JobID); err != nil {
			return Receipt{}, err
		}
		cursor, err := addEvent(tx, r.JobID, "phase_reconciled", q)
		if err != nil {
			return Receipt{}, err
		}
		r, err = l.snapshotTx(tx, r.JobID)
		r.EventCursor = cursor
		return r, err
	})
}

func effectiveLocalPhasesTx(tx *sql.Tx, jobID string) (map[string]LocalPhase, error) {
	phases, err := localPhasesTx(tx, jobID)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='phase_reconciled' ORDER BY cursor`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var q PhaseResolutionRequest
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = decodeClosed(raw, &q); err != nil {
			return nil, err
		}
		prior, exists := phases[q.Record.Phase]
		if !exists || RecoveryDigest(prior) != q.OriginalDigest || !samePhaseIntent(prior, q.Record) || !q.Evidence.valid() || !validLocalPhase(q.Record) || !q.Record.Completed || q.Record.Outcome == "unproven" || prior.Completed && prior.Outcome != "unproven" || q.Record.ObservedAt.Before(prior.ObservedAt) || q.Record.Phase != "verify" && q.Record.ObservedAt.After(prior.Deadline) && q.Record.Outcome == "complete" {
			return nil, ErrBlocked
		}
		phases[q.Record.Phase] = q.Record
	}
	return phases, rows.Err()
}

func (l *Ledger) InterruptedJobs() ([]Receipt, error) {
	rows, err := l.db.Query(`SELECT id FROM managed_jobs WHERE state='reconciling' AND slot IS NOT NULL ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var jobs []Receipt
	for _, id := range ids {
		r, err := l.Snapshot(id)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, r)
	}
	return jobs, nil
}

func validateRecoveryProvenanceTx(tx *sql.Tx, jobID string, result Result) error {
	var attested bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM managed_events WHERE job_id=? AND kind IN ('recovery_intent','phase_reconciled') AND json_extract(payload_json,'$.evidence.source')='operator-attested')`, jobID).Scan(&attested); err != nil {
		return err
	}
	if attested && (result.Observations == nil || !result.Observations.OperatorAttested || result.Observations.Source != "operator-attested") {
		return ErrInvalid
	}
	return nil
}
