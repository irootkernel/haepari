package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"time"

	"github.com/irootkernel/haepari/internal/platform"
)

// EvidenceAbsenceBinding is supplied only after the controller verifies the
// original native reboot attestation and exact prepared workspace. The ledger
// validates the admitted policy and preserves the proof binding; it does not
// substitute these fields for a fresh native no-follow observation.
type EvidenceAbsenceBinding struct {
	WorkspaceRoot     RetainedPath     `json:"workspace_root"`
	AttestationDigest string           `json:"attestation_digest"`
	OriginalBoot      string           `json:"original_boot"`
	RecoveryBoot      string           `json:"recovery_boot"`
	Cessation         RecoveryEvidence `json:"cessation"`
}

type EvidenceAbsentOutput struct {
	Path       string    `json:"path"`
	ObservedAt time.Time `json:"observed_at"`
}

type EvidenceRecoveryJob struct {
	AbsenceBinding     *EvidenceAbsenceBinding `json:"absence_binding,omitempty"`
	GuestLogLimitBytes int64                   `json:"guest_log_limit_bytes,omitempty"`
	GuestAttestation   json.RawMessage         `json:"guest_attestation,omitempty"`
	JobID              string                  `json:"job_id"`
	ResultDigest       string                  `json:"result_digest"`
	Identity           LocalIdentity           `json:"identity"`
	Root               RetainedPath            `json:"root"`
	Protected          []ProtectedEvidence     `json:"protected_evidence"`
	Outputs            []string                `json:"required_outputs"`
	CollectDeadline    time.Time               `json:"collect_deadline"`
	CleanupDeadline    time.Time               `json:"cleanup_deadline"`
}

type EvidenceRecoveryIntent struct {
	Schema                       string                   `json:"schema"`
	ResourceID                   string                   `json:"resource_id"`
	SnapshotDigest               string                   `json:"snapshot_digest"`
	ResourceConfigRevision       int64                    `json:"resource_config_revision"`
	ResourceAvailabilityRevision int64                    `json:"resource_availability_revision"`
	MutationKey                  string                   `json:"mutation_key"`
	ClientDigest                 string                   `json:"client_digest"`
	Action                       string                   `json:"action"`
	AuthorityReference           string                   `json:"authority_reference,omitempty"`
	RecordedAt                   time.Time                `json:"recorded_at"`
	Owner                        platform.ProcessIdentity `json:"owner"`
	Jobs                         []EvidenceRecoveryJob    `json:"jobs"`
}

type EvidenceRecoveryOutcome struct {
	AbsentOutputs  []EvidenceAbsentOutput `json:"absent_outputs,omitempty"`
	Attestation    json.RawMessage        `json:"attestation,omitempty"`
	GuestArtifacts []ResultArtifact       `json:"guest_artifacts,omitempty"`
	JobID          string                 `json:"job_id"`
	ResultDigest   string                 `json:"result_digest"`
	Stage          string                 `json:"stage"`
	ArtifactRoot   string                 `json:"artifact_root,omitempty"`
	Artifacts      []ResultArtifact       `json:"artifacts"`
	Gap            string                 `json:"gap,omitempty"`
}
type EvidenceRecoveryReceipt struct {
	Schema      string                    `json:"schema"`
	MutationKey string                    `json:"mutation_key"`
	Jobs        []EvidenceRecoveryOutcome `json:"jobs"`
	Complete    bool                      `json:"complete"`
	Observation RecoveryEvidence          `json:"observation"`
	ActiveOwner *platform.ProcessIdentity `json:"active_owner,omitempty"`
}

func evidenceResolvedTx(tx *sql.Tx, result Result) (bool, error) {
	if len(result.ProtectedEvidence) == 0 && oneOf(string(result.CleanupOutcome), "verified", "not_needed") {
		return true, nil
	}
	// A late host completion observation preserves INCOMPLETE and failed cleanup
	// timing. Exact native completed-cleanup authority may nevertheless resolve
	// the evidence hold when the original complete host inventory is preserved.
	if len(result.ProtectedEvidence) == 0 && result.CleanupOutcome == "failed" && result.Reason == "cleanup_deadline" {
		complete := result.EvidenceCompleteness == "complete"
		if !complete && result.EvidenceCompleteness == "partial" && len(result.EvidenceGaps) > 0 {
			var raw []byte
			if err := tx.QueryRow(`SELECT spec_json FROM managed_admissions WHERE job_id=?`, result.JobID).Scan(&raw); err != nil {
				return false, err
			}
			job, err := DecodeJob(raw)
			if err != nil {
				return false, err
			}
			complete = AbsenceAware(job)
			for _, gap := range result.EvidenceGaps {
				complete = complete && gap.Reason == "required_output_absent" && oneOf(gap.Path, job.Workload.RequiredOutputs...)
			}
		}
		rows, err := tx.Query(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='recovery_intent' ORDER BY cursor`, result.JobID)
		if err != nil {
			return false, err
		}
		resolved := false
		for rows.Next() {
			var raw []byte
			var q JobRecoveryRequest
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return false, err
			}
			if decodeClosed(raw, &q) != nil {
				rows.Close()
				return false, ErrBlocked
			}
			if complete && q.Identity.JobID == result.JobID && q.Identity.ResourceID == result.ResourceID && q.Identity.ConfigRevision == result.ResourceRevision && q.Identity.LeaseGeneration == result.LeaseGeneration && q.Evidence.valid() && q.Evidence.Source == "machine-observed" && q.Evidence.Mechanism == "guest-completed-cleanup-observed" && q.Evidence.Reference == "job:"+result.JobID {
				resolved = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return false, err
		}
		if resolved {
			return true, nil
		}
	}
	rows, err := tx.Query(`SELECT request_json,receipt_json FROM managed_evidence_recovery WHERE resource_id=? AND receipt_json IS NOT NULL`, result.ResourceID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	resolved := false
	for rows.Next() {
		var a, b []byte
		var q EvidenceRecoveryIntent
		var r EvidenceRecoveryReceipt
		if err = rows.Scan(&a, &b); err != nil {
			return false, err
		}
		if decodeClosed(a, &q) != nil || decodeClosed(b, &r) != nil || !validEvidenceIntent(q) || !validEvidenceReceipt(q, r) {
			return false, ErrBlocked
		}
		for _, o := range r.Jobs {
			if r.Complete && o.JobID == result.JobID && o.ResultDigest == RecoveryDigest(result) && o.Stage == "resolved" {
				for _, j := range q.Jobs {
					if j.JobID == result.JobID && reflect.DeepEqual(j.Protected, result.ProtectedEvidence) {
						resolved = true
					}
				}
			}
		}
	}
	return resolved, rows.Err()
}

func validEvidenceIntent(q EvidenceRecoveryIntent) bool {
	if !oneOf(q.Schema, "haepari.evidence-recovery-intent/v1", "haepari.evidence-recovery-intent/v2") || !ValidRequestKey(q.ResourceID) || !ValidRequestKey(q.MutationKey) || q.ResourceConfigRevision <= 0 || q.ResourceAvailabilityRevision <= 0 || !validRecoveryDigest(q.SnapshotDigest) || !validRecoveryDigest(q.ClientDigest) || !oneOf(q.Action, "recollect", "dispose", "attest-guest-stopped") || q.Owner.BootID == "" || q.Owner.PID <= 0 || q.Owner.StartedSeconds <= 0 || len(q.Jobs) == 0 || len(q.Jobs) > 1000 {
		return false
	}
	if q.RecordedAt.IsZero() || q.Action == "dispose" && q.AuthorityReference != "request:"+q.MutationKey {
		return false
	}
	seen := map[string]bool{}
	hasAbsenceBinding := false
	for _, j := range q.Jobs {
		if !ValidRequestKey(j.JobID) || seen[j.JobID] || j.Identity.JobID != j.JobID || j.Identity.LeaseGeneration <= 0 || j.Identity.Nonce == "" || !validRecoveryDigest(j.ResultDigest) || j.Root.Path != "" || j.Root.Inode == 0 || !j.Root.Directory || j.CollectDeadline.Before(q.RecordedAt) || !j.CleanupDeadline.After(j.CollectDeadline) {
			return false
		}
		if j.GuestLogLimitBytes < 0 || j.GuestLogLimitBytes > GuestRecoveryLogBytes || (q.Action == "attest-guest-stopped") != (len(j.GuestAttestation) > 0) || len(j.GuestAttestation) > 2<<20 {
			return false
		}
		if b := j.AbsenceBinding; b != nil {
			hasAbsenceBinding = true
			if q.Schema != "haepari.evidence-recovery-intent/v2" || q.Action != "recollect" || j.Identity.RunID == "" ||
				b.WorkspaceRoot.Path != "workspace" || !b.WorkspaceRoot.Directory || b.WorkspaceRoot.Inode == 0 ||
				!validRecoveryDigest(b.AttestationDigest) || b.OriginalBoot == "" || b.RecoveryBoot == "" || b.OriginalBoot == b.RecoveryBoot ||
				!b.Cessation.valid() || !(b.Cessation.Source == "machine-observed" && b.Cessation.Mechanism == "guest-reboot-observed" || b.Cessation.Source == "operator-attested" && b.Cessation.Mechanism == "guest-workload-and-descendants-stopped") ||
				b.Cessation.Reference != "job:"+j.JobID || b.Cessation.ObservedAt.After(q.RecordedAt) {
				return false
			}
		}
		seen[j.JobID] = true
	}
	return (q.Schema == "haepari.evidence-recovery-intent/v2") == hasAbsenceBinding
}

func validEvidenceReceipt(q EvidenceRecoveryIntent, r EvidenceRecoveryReceipt) bool {
	if r.ActiveOwner != nil && (r.Complete || r.ActiveOwner.PID <= 0 || r.ActiveOwner.BootID == "" || r.ActiveOwner.StartedSeconds <= 0) {
		return false
	}
	expectedSchema := "haepari.evidence-recovery-receipt/v1"
	if q.Schema == "haepari.evidence-recovery-intent/v2" {
		expectedSchema = "haepari.evidence-recovery-receipt/v2"
	}
	if r.Schema != expectedSchema || r.MutationKey != q.MutationKey || !r.Observation.valid() || r.Observation.Source != "machine-observed" || len(r.Jobs) != len(q.Jobs) {
		return false
	}
	for i, o := range r.Jobs {
		j := q.Jobs[i]
		if o.JobID != j.JobID || o.ResultDigest != j.ResultDigest || !oneOf(o.Stage, "pending", "collected", "resolved", "failed", "attested") || r.Complete && oneOf(o.Stage, "pending", "collected") || q.Action == "dispose" && len(o.Artifacts) > 0 {
			return false
		}
		if o.ArtifactRoot != "" && o.ArtifactRoot != "recovery-"+q.MutationKey {
			return false
		}
		if o.Stage == "failed" && o.Gap == "" || o.Stage != "failed" && o.Gap != "" || o.Stage == "pending" && (o.ArtifactRoot != "" || len(o.Artifacts) != 0) || q.Action == "dispose" && o.ArtifactRoot != "" {
			return false
		}
		if q.Action == "attest-guest-stopped" {
			if !oneOf(o.Stage, "pending", "attested", "failed") || (o.Stage == "attested") != (len(o.Attestation) > 0) || len(o.Artifacts) > 0 || len(o.GuestArtifacts) > 0 || o.ArtifactRoot != "" {
				return false
			}
		} else if o.Stage == "attested" || len(o.Attestation) > 0 {
			return false
		}
		paths := []string{}
		var bytes int64
		for _, a := range o.Artifacts {
			if !ValidArtifactPath(a.Path) || !oneOf(a.Path, j.Outputs...) || a.SizeBytes < 0 || a.SizeBytes > 1<<30-bytes || !validDigest(a.SHA256) {
				return false
			}
			bytes += a.SizeBytes
			paths = append(paths, a.Path)
		}
		for _, a := range o.GuestArtifacts {
			limit, valid := GuestRecoveryArtifactLimit(a.Path, j.GuestLogLimitBytes)
			if !valid || a.SizeBytes < 0 || a.SizeBytes > limit || !validDigest(a.SHA256) {
				return false
			}
		}
		if len(o.GuestArtifacts) > 0 {
			names := []string{}
			for _, a := range o.GuestArtifacts {
				names = append(names, a.Path)
			}
			if !uniquePaths(names) || q.Action != "recollect" || o.ArtifactRoot == "" {
				return false
			}
		}
		if len(paths) > 0 && !uniquePaths(paths) {
			return false
		}
		if len(o.AbsentOutputs) > 0 && (q.Schema != "haepari.evidence-recovery-intent/v2" || j.AbsenceBinding == nil || q.Action != "recollect" || o.Stage == "pending" || o.ArtifactRoot == "") {
			return false
		}
		for _, absent := range o.AbsentOutputs {
			if !ValidArtifactPath(absent.Path) || !oneOf(absent.Path, j.Outputs...) || absent.ObservedAt.IsZero() || absent.ObservedAt.Before(q.RecordedAt) || absent.ObservedAt.After(j.CollectDeadline) || absent.ObservedAt.After(r.Observation.ObservedAt) {
				return false
			}
			paths = append(paths, absent.Path)
		}
		// Verified files and positively absent paths form one disjoint inventory.
		if len(paths) > 0 && !uniquePaths(paths) {
			return false
		}
		if q.Action == "recollect" && oneOf(o.Stage, "collected", "resolved") && (o.ArtifactRoot == "" || len(paths) != len(j.Outputs)) {
			return false
		}
	}
	return true
}

func (l *Ledger) EvidenceRecovery(key, digest string) (EvidenceRecoveryIntent, *EvidenceRecoveryReceipt, bool, error) {
	var q EvidenceRecoveryIntent
	var r *EvidenceRecoveryReceipt
	var a, b []byte
	err := l.db.QueryRow(`SELECT request_json,receipt_json FROM managed_evidence_recovery WHERE mutation_key=?`, key).Scan(&a, &b)
	if err == sql.ErrNoRows {
		return q, r, false, nil
	}
	if err != nil {
		return q, r, false, err
	}
	if decodeClosed(a, &q) != nil || !validEvidenceIntent(q) {
		return q, r, true, ErrBlocked
	}
	if digest != "" && q.ClientDigest != digest {
		return q, r, true, ErrConflict
	}
	if len(b) > 0 {
		r = new(EvidenceRecoveryReceipt)
		if decodeClosed(b, r) != nil || !validEvidenceReceipt(q, *r) {
			return q, r, true, ErrBlocked
		}
	}
	return q, r, true, nil
}

func (l *Ledger) BeginEvidenceRecovery(q EvidenceRecoveryIntent) error {
	if !validEvidenceIntent(q) {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	s, err := resourceRecoverySnapshotTx(tx, q.ResourceID)
	if err != nil {
		return err
	}
	if RecoveryDigest(s) != q.SnapshotDigest || q.ResourceConfigRevision != s.Resource.ConfigRevision || q.ResourceAvailabilityRevision != s.Resource.AvailabilityRevision {
		return ErrStale
	}
	if s.Resource.Availability != "quarantined" || len(s.AdapterIntents) > 0 {
		return ErrBlocked
	}
	for _, j := range q.Jobs {
		b, err := l.bulkSnapshotTx(tx, j.JobID)
		if err != nil {
			return err
		}
		if b.Receipt.ResultStatus == "unavailable" || b.Result.ResourceID != q.ResourceID || b.Identity == nil || *b.Identity != j.Identity || RecoveryDigest(b.Result) != j.ResultDigest || !reflect.DeepEqual(b.Result.ProtectedEvidence, j.Protected) || !reflect.DeepEqual(b.Spec.Workload.RequiredOutputs, j.Outputs) || !j.CollectDeadline.Equal(q.RecordedAt.Add(time.Duration(b.Spec.Deadlines.CollectSeconds)*time.Second)) || !j.CleanupDeadline.Equal(j.CollectDeadline.Add(time.Duration(b.Spec.Deadlines.CleanupSeconds)*time.Second)) {
			return ErrStale
		}
		if j.AbsenceBinding != nil && (s.Resource.Kind != "utm-macos" || !AbsenceAware(b.Spec)) {
			return ErrInvalid
		}
		if _, err = retentionEvent(tx, j.JobID, "evidence_recovery_intent", map[string]any{"mutation_key": q.MutationKey, "action": q.Action, "result_digest": j.ResultDigest, "authority_reference": q.AuthorityReference}); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(q)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO managed_evidence_recovery(mutation_key,resource_id,request_json) VALUES(?,?,?)`, q.MutationKey, q.ResourceID, raw); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE managed_resources SET availability='recovering',availability_reason='evidence_recovery',availability_revision=availability_revision+1 WHERE id=?`, q.ResourceID); err != nil {
		return err
	}
	return l.commit(tx)
}

func (l *Ledger) RecordEvidenceRecovery(q EvidenceRecoveryIntent, r EvidenceRecoveryReceipt) error {
	if !validEvidenceIntent(q) || !validEvidenceReceipt(q, r) {
		return ErrInvalid
	}
	prior, receipt, found, err := l.EvidenceRecovery(q.MutationKey, q.ClientDigest)
	if err != nil {
		return err
	}
	if !found || RecoveryDigest(prior) != RecoveryDigest(q) {
		return ErrConflict
	}
	if receipt != nil && receipt.Complete {
		if RecoveryDigest(*receipt) == RecoveryDigest(r) {
			return nil
		}
		return ErrConflict
	}
	if receipt != nil {
		for i, o := range receipt.Jobs {
			n := r.Jobs[i]
			if oneOf(o.Stage, "resolved", "failed", "attested") && !reflect.DeepEqual(o, n) || o.Stage == "collected" && (n.Stage == "pending" || !reflect.DeepEqual(o.Artifacts, n.Artifacts) || !reflect.DeepEqual(o.AbsentOutputs, n.AbsentOutputs) || !reflect.DeepEqual(o.GuestArtifacts, n.GuestArtifacts) || o.ArtifactRoot != n.ArtifactRoot) {
				return ErrConflict
			}
		}
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err = tx.QueryRow(`SELECT availability FROM managed_resources WHERE id=? AND occupancy=0 AND config_revision=? AND availability_revision=?`, q.ResourceID, q.ResourceConfigRevision, q.ResourceAvailabilityRevision+1).Scan(&state); err != nil {
		return ErrBlocked
	}
	if state != "recovering" {
		return ErrStale
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var priorRaw any
	if receipt != nil {
		priorRaw, err = json.Marshal(receipt)
		if err != nil {
			return err
		}
	}
	changed, err := tx.Exec(`UPDATE managed_evidence_recovery SET receipt_json=? WHERE mutation_key=? AND receipt_json IS ?`, raw, q.MutationKey, priorRaw)
	if err != nil {
		return err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return ErrStale
	}
	for _, o := range r.Jobs {
		if _, err = retentionEvent(tx, o.JobID, "evidence_recovery_observed", map[string]any{"mutation_key": q.MutationKey, "stage": o.Stage, "complete": r.Complete, "receipt_digest": RecoveryDigest(r)}); err != nil {
			return err
		}
	}
	if r.Complete {
		if _, err = tx.Exec(`UPDATE managed_resources SET availability='quarantined',availability_reason='evidence_recovery_complete',availability_revision=availability_revision+1 WHERE id=?`, q.ResourceID); err != nil {
			return err
		}
	}
	return l.commit(tx)
}

func (l *Ledger) PendingEvidenceRecovery() ([]EvidenceRecoveryIntent, error) {
	rows, err := l.db.Query(`SELECT request_json,receipt_json FROM managed_evidence_recovery`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EvidenceRecoveryIntent{}
	for rows.Next() {
		var a, b []byte
		var q EvidenceRecoveryIntent
		var r EvidenceRecoveryReceipt
		if err = rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		if decodeClosed(a, &q) != nil || !validEvidenceIntent(q) {
			return nil, ErrBlocked
		}
		if len(b) > 0 {
			if decodeClosed(b, &r) != nil || !validEvidenceReceipt(q, r) {
				return nil, ErrBlocked
			}
			if r.Complete {
				continue
			}
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (l *Ledger) SupplementaryEvidence(id string) ([]EvidenceRecoveryOutcome, error) {
	return l.supplementaryEvidence(id, false)
}

func (l *Ledger) PublicSupplementaryEvidence(id string) ([]PublicRecoveryOutcome, error) {
	private, err := l.supplementaryEvidence(id, true)
	if err != nil {
		return nil, err
	}
	public := []PublicRecoveryOutcome{}
	for _, o := range private {
		public = append(public, PublicRecoveryOutcome{o.JobID, o.ResultDigest, o.Stage, o.ArtifactRoot, o.Artifacts, o.Gap})
	}
	return public, nil
}

func (l *Ledger) supplementaryEvidence(id string, public bool) ([]EvidenceRecoveryOutcome, error) {
	if !ValidRequestKey(id) {
		return nil, ErrInvalid
	}
	rows, err := l.db.Query(`SELECT request_json,receipt_json FROM managed_evidence_recovery WHERE resource_id=(SELECT resource_id FROM managed_jobs WHERE id=?) AND receipt_json IS NOT NULL ORDER BY mutation_key LIMIT 1001`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EvidenceRecoveryOutcome{}
	count := 0
	for rows.Next() {
		count++
		if count > 1000 {
			return nil, ErrBlocked
		}
		var a, b []byte
		var q EvidenceRecoveryIntent
		var r EvidenceRecoveryReceipt
		if err = rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		if decodeClosed(a, &q) != nil || decodeClosed(b, &r) != nil || !validEvidenceIntent(q) || !validEvidenceReceipt(q, r) {
			return nil, ErrBlocked
		}
		for _, o := range r.Jobs {
			if o.JobID == id && (!public || q.Action != "attest-guest-stopped") {
				out = append(out, o)
			}
		}
	}
	return out, rows.Err()
}

func rejectLegacyEvidenceField(raw []byte, field string) error {
	var value struct {
		Jobs []map[string]json.RawMessage `json:"jobs"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return ErrInvalid
	}
	for _, job := range value.Jobs {
		if _, present := job[field]; present {
			return ErrInvalid
		}
	}
	return nil
}
