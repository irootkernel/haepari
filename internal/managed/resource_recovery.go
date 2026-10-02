package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"time"
)

type ResourceRecoveryJob struct {
	JobID            string  `json:"job_id"`
	Result           *Result `json:"result,omitempty"`
	ResultStatus     string  `json:"result_status,omitempty"`
	EvidenceResolved bool    `json:"evidence_resolved"`
}

type ResourceRecoverySnapshot struct {
	Installation    string                   `json:"installation_id"`
	Resource        ResourceStatus           `json:"resource"`
	Jobs            []ResourceRecoveryJob    `json:"jobs"`
	AdapterIntents  []PowerIntent            `json:"adapter_intents"`
	Pending         *ResourceRecoveryIntent  `json:"pending_intent,omitempty"`
	PendingStatus   *ResourceStatus          `json:"pending_status,omitempty"`
	EvidenceIntent  *EvidenceRecoveryIntent  `json:"evidence_intent,omitempty"`
	EvidenceReceipt *EvidenceRecoveryReceipt `json:"evidence_receipt,omitempty"`
}

type ResourceRecoveryIntent struct {
	Schema         string           `json:"schema"`
	ResourceID     string           `json:"resource_id"`
	SnapshotDigest string           `json:"snapshot_digest"`
	ClientDigest   string           `json:"client_digest"`
	MutationKey    string           `json:"mutation_key"`
	Evidence       RecoveryEvidence `json:"evidence"`
}

func (l *Ledger) ResourceRecoverySnapshot(id string) (ResourceRecoverySnapshot, error) {
	if !identifier.MatchString(id) {
		return ResourceRecoverySnapshot{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ResourceRecoverySnapshot{}, err
	}
	defer tx.Rollback()
	return resourceRecoverySnapshotTx(tx, id)
}

func resourceRecoverySnapshotTx(tx *sql.Tx, id string) (ResourceRecoverySnapshot, error) {
	s := ResourceRecoverySnapshot{Jobs: []ResourceRecoveryJob{}, AdapterIntents: []PowerIntent{}}
	var err error
	s.Installation, err = installationTx(tx)
	if err != nil {
		return s, err
	}
	s.Resource, err = resourceStatusTx(tx, id)
	if err != nil {
		return s, err
	}
	if s.Resource.Occupancy != 0 {
		return s, ErrBlocked
	}
	rows, err := tx.Query(`SELECT j.id,j.result_json,a.retention_state FROM managed_jobs j JOIN managed_admissions a ON a.job_id=j.id WHERE j.resource_id=? AND j.state='finished' ORDER BY j.id`, id)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var j ResourceRecoveryJob
		var raw []byte
		var retention string
		if err = rows.Scan(&j.JobID, &raw, &retention); err != nil {
			rows.Close()
			return s, err
		}
		if len(raw) == 0 {
			rows.Close()
			return s, ErrBlocked
		}
		j.Result = new(Result)
		if err = decodeClosed(raw, j.Result); err != nil {
			rows.Close()
			return s, err
		}
		if retention == "unavailable" {
			j.ResultStatus = retention
		} else if retention != "available" {
			rows.Close()
			return s, ErrBlocked
		}
		s.Jobs = append(s.Jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return s, err
	}
	for i := range s.Jobs {
		s.Jobs[i].EvidenceResolved, err = evidenceResolvedTx(tx, *s.Jobs[i].Result)
		if err != nil {
			return s, err
		}
		if s.Jobs[i].ResultStatus == "unavailable" {
			if !s.Jobs[i].EvidenceResolved {
				return s, ErrBlocked
			}
			// Purged results are private reconciliation authority. Recovery
			// still needs their ownership and resolved-evidence projection.
			s.Jobs[i].Result = nil
		}
	}
	s.AdapterIntents, err = pendingRecoveryIntentsTx(tx, id)
	if err != nil {
		return s, err
	}
	if s.Resource.Availability == "recovering" {
		var a, b []byte
		err = tx.QueryRow(`SELECT request_json,receipt_json FROM managed_evidence_recovery WHERE resource_id=? AND (receipt_json IS NULL OR json_extract(receipt_json,'$.complete')=0)`, id).Scan(&a, &b)
		if err == nil {
			q := new(EvidenceRecoveryIntent)
			if decodeClosed(a, q) != nil || !validEvidenceIntent(*q) || q.ResourceConfigRevision != s.Resource.ConfigRevision || q.ResourceAvailabilityRevision+1 != s.Resource.AvailabilityRevision {
				return s, ErrBlocked
			}
			s.EvidenceIntent = q
			if len(b) > 0 {
				s.EvidenceReceipt = new(EvidenceRecoveryReceipt)
				if decodeClosed(b, s.EvidenceReceipt) != nil || !validEvidenceReceipt(*q, *s.EvidenceReceipt) {
					return s, ErrBlocked
				}
			}
			return s, nil
		}
		if err != sql.ErrNoRows {
			return s, err
		}
		rows, err = tx.Query(`SELECT request_json,status_json FROM managed_availability_receipts WHERE resource_id=? ORDER BY recorded_at_ns DESC`, id)
		if err != nil {
			return s, err
		}
		for rows.Next() {
			var raw, status []byte
			if err = rows.Scan(&raw, &status); err != nil {
				rows.Close()
				return s, err
			}
			var q ResourceRecoveryIntent
			var r ResourceStatus
			if decodeClosed(raw, &q) == nil && q.Schema == "haepari.resource-recovery-intent/v1" && decodeClosed(status, &r) == nil && r.AvailabilityRevision == s.Resource.AvailabilityRevision {
				current := s.Resource
				current.Mutation = r.Mutation
				if !reflect.DeepEqual(current, r) || !q.Evidence.valid() || q.Evidence.Source != "machine-observed" || q.ResourceID != id || r.Mutation == nil || r.Mutation.MutationKey != q.MutationKey {
					rows.Close()
					return s, ErrBlocked
				}
				s.Pending = &q
				s.PendingStatus = &r
				break
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return s, err
		}
		if s.Pending == nil {
			return s, ErrBlocked
		}
	}
	return s, nil
}

func (l *Ledger) RecoveringResources() ([]string, error) {
	rows, err := l.db.Query(`SELECT id FROM managed_resources WHERE availability='recovering' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func resourceRecoveryReplayTx(tx *sql.Tx, key, digest string) (ResourceStatus, bool, error) {
	var raw, status []byte
	err := tx.QueryRow(`SELECT request_json,status_json FROM managed_availability_receipts WHERE mutation_key=?`, key).Scan(&raw, &status)
	if err == sql.ErrNoRows {
		return ResourceStatus{}, false, nil
	}
	if err != nil {
		return ResourceStatus{}, false, err
	}
	var q ResourceRecoveryIntent
	var r ResourceStatus
	if decodeClosed(raw, &q) != nil || q.Schema != "haepari.resource-recovery-intent/v1" || q.ClientDigest != digest {
		return r, true, ErrConflict
	}
	if err = decodeClosed(status, &r); err != nil {
		return r, true, err
	}
	current, err := resourceStatusTx(tx, q.ResourceID)
	current.Mutation = r.Mutation
	return current, true, err
}

func (l *Ledger) ReplayResourceRecovery(key, digest string) (ResourceStatus, bool, error) {
	if !ValidRequestKey(key) || !validRecoveryDigest(digest) {
		return ResourceStatus{}, false, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ResourceStatus{}, false, err
	}
	defer tx.Rollback()
	return resourceRecoveryReplayTx(tx, key, digest)
}

func (l *Ledger) BeginResourceRecovery(q ResourceRecoveryIntent) (ResourceStatus, error) {
	if q.Schema != "haepari.resource-recovery-intent/v1" || !ValidRequestKey(q.MutationKey) || !validRecoveryDigest(q.ClientDigest) || !validRecoveryDigest(q.SnapshotDigest) || !q.Evidence.valid() || q.Evidence.Source != "machine-observed" {
		return ResourceStatus{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return ResourceStatus{}, err
	}
	defer tx.Rollback()
	if prior, found, err := resourceRecoveryReplayTx(tx, q.MutationKey, q.ClientDigest); found || err != nil {
		return prior, err
	}
	s, err := resourceRecoverySnapshotTx(tx, q.ResourceID)
	if err != nil {
		return ResourceStatus{}, err
	}
	if RecoveryDigest(s) != q.SnapshotDigest {
		return ResourceStatus{}, ErrStale
	}
	if !oneOf(s.Resource.Availability, "quarantined", "recovering") || len(s.AdapterIntents) > 0 {
		return ResourceStatus{}, ErrBlocked
	}
	for _, j := range s.Jobs {
		if !j.EvidenceResolved {
			return ResourceStatus{}, ErrBlocked
		}
	}
	if _, err = tx.Exec(`UPDATE managed_resources SET availability='recovering',availability_reason='explicit_recovery',availability_revision=availability_revision+1 WHERE id=?`, q.ResourceID); err != nil {
		return ResourceStatus{}, err
	}
	if err = queuedAvailabilityEventsTx(tx, q.ResourceID, map[string]any{"availability": "recovering", "reason": "explicit_recovery", "recovery_intent": q}); err != nil {
		return ResourceStatus{}, err
	}
	r, err := resourceStatusTx(tx, q.ResourceID)
	if err != nil {
		return r, err
	}
	r.Mutation = &AvailabilityIdentity{q.MutationKey, r.AvailabilityRevision}
	raw, _ := json.Marshal(q)
	status, _ := json.Marshal(r)
	if _, err = tx.Exec(`INSERT INTO managed_availability_receipts(mutation_key,resource_id,request_json,status_json,recorded_at_ns) VALUES(?,?,?,?,?)`, q.MutationKey, q.ResourceID, raw, status, time.Now().UTC().UnixNano()); err != nil {
		return r, err
	}
	return r, l.commit(tx)
}

func (l *Ledger) CompleteResourceRecovery(expected ResourceStatus, key, digest string, evidence RecoveryEvidence, healthy bool) (ResourceStatus, error) {
	if !ValidRequestKey(key) || !validRecoveryDigest(digest) || !evidence.valid() || evidence.Source != "machine-observed" {
		return ResourceStatus{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return ResourceStatus{}, err
	}
	defer tx.Rollback()
	if prior, found, err := resourceRecoveryReplayTx(tx, key, digest); found || err != nil {
		return prior, err
	}
	s, err := resourceRecoverySnapshotTx(tx, expected.ID)
	if err != nil {
		return ResourceStatus{}, err
	}
	current := s.Resource
	current.Mutation = expected.Mutation
	if !reflect.DeepEqual(current, expected) || s.Resource.Availability != "recovering" || s.Pending == nil || len(s.AdapterIntents) > 0 {
		return ResourceStatus{}, ErrStale
	}
	for _, j := range s.Jobs {
		if !j.EvidenceResolved {
			return ResourceStatus{}, ErrBlocked
		}
	}
	availability, reason := "quarantined", "recovery_verification_failed"
	if healthy {
		availability, reason = "available", ""
	}
	if _, err = tx.Exec(`UPDATE managed_resources SET availability=?,availability_reason=?,availability_revision=availability_revision+1 WHERE id=?`, availability, reason, expected.ID); err != nil {
		return ResourceStatus{}, err
	}
	q := ResourceRecoveryIntent{Schema: "haepari.resource-recovery-intent/v1", ResourceID: expected.ID, SnapshotDigest: RecoveryDigest(s), ClientDigest: digest, MutationKey: key, Evidence: evidence}
	if err = queuedAvailabilityEventsTx(tx, expected.ID, map[string]any{"availability": availability, "reason": reason, "recovery_observation": q}); err != nil {
		return ResourceStatus{}, err
	}
	r, err := resourceStatusTx(tx, expected.ID)
	if err != nil {
		return r, err
	}
	r.Mutation = &AvailabilityIdentity{key, r.AvailabilityRevision}
	raw, _ := json.Marshal(q)
	status, _ := json.Marshal(r)
	if _, err = tx.Exec(`INSERT INTO managed_availability_receipts(mutation_key,resource_id,request_json,status_json,recorded_at_ns) VALUES(?,?,?,?,?)`, key, expected.ID, raw, status, time.Now().UTC().UnixNano()); err != nil {
		return r, err
	}
	return r, l.commit(tx)
}
