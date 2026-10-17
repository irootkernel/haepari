package managed

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

// DiagnosticRetentionRow is one jobs[] element. Unknown policy omits the three
// policy fields. permanent_replay_count counts the nine private-event binding
// kinds on the retention allowlist. It is not every permanent database identity
// and not every payload. A computed 0 is present. A missing payload above the
// public floor omits it.
type DiagnosticRetentionRow struct {
	JobID                string `json:"job_id"`
	ResourceID           string `json:"resource_id"`
	State                string `json:"state"`
	Mode                 string `json:"mode"`
	PolicyClass          string `json:"policy_class"`
	PolicyRevision       *int64 `json:"policy_revision,omitempty"`
	EventRetentionDays   *int64 `json:"event_retention_days,omitempty"`
	ResultRetentionDays  *int64 `json:"result_retention_days,omitempty"`
	RetentionClass       string `json:"retention_class"`
	EventWindow          string `json:"event_window,omitempty"`
	ResultWindow         string `json:"result_window,omitempty"`
	PermanentReplayCount *int64 `json:"permanent_replay_count,omitempty"`
}

func retentionItemsTx(tx *sql.Tx, q DiagnosticQuery, cursor pageCursor, observed time.Time) ([]diagnosticItem, error) {
	rows, err := tx.Query(`SELECT a.sequence,a.job_id,a.retention_state,j.resource_id,j.state,j.admitted_at_ns,j.spec_json,j.result_json,r.availability,r.occupancy,r.kind
		FROM managed_admissions a
		JOIN managed_jobs j ON j.id=a.job_id
		JOIN managed_resources r ON r.id=j.resource_id
		WHERE a.sequence>? AND a.sequence<=? AND (?='' OR j.resource_id=?)
		ORDER BY a.sequence`, cursor.Last, cursor.Upper, q.Resource, q.Resource)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []diagnosticItem{}
	for rows.Next() {
		var sequence, admittedNS int64
		var occupancy int
		var jobID, retentionState, resourceID, state, availability, kind string
		var specJSON, resultJSON []byte
		if err = rows.Scan(&sequence, &jobID, &retentionState, &resourceID, &state, &admittedNS, &specJSON, &resultJSON, &availability, &occupancy, &kind); err != nil {
			return nil, err
		}
		if q.Resource != "" && resourceID != q.Resource {
			continue
		}
		row, err := diagnosticRetentionRowTx(tx, jobID, resourceID, state, retentionState, availability, kind, occupancy, admittedNS, specJSON, resultJSON, observed)
		if err != nil {
			return nil, err
		}
		items = append(items, diagnosticItem{id: jobID, sequence: sequence, job: row})
	}
	return items, rows.Err()
}

func diagnosticRetentionRowTx(tx *sql.Tx, jobID, resourceID, state, retentionState, availability, kind string, occupancy int, admittedNS int64, specJSON, resultJSON []byte, observed time.Time) (DiagnosticRetentionRow, error) {
	row := DiagnosticRetentionRow{JobID: jobID, ResourceID: resourceID, State: state, Mode: "unknown", PolicyClass: "unknown"}
	if spec, err := DecodeJob(specJSON); err == nil && (spec.Mode == "auto" || spec.Mode == "manual") {
		row.Mode = spec.Mode
	}
	if err := applyDiagnosticPolicyTx(tx, jobID, &row); err != nil {
		return DiagnosticRetentionRow{}, err
	}
	missing, err := diagnosticPayloadMissingTx(tx, jobID)
	if err != nil {
		return DiagnosticRetentionRow{}, err
	}
	if missing {
		row.RetentionClass = "unknown"
		return row, nil
	}
	count, err := permanentReplayCountTx(tx, jobID)
	if err != nil {
		return DiagnosticRetentionRow{}, err
	}
	row.PermanentReplayCount = diagnosticInt(count)
	switch {
	case state != "finished":
		row.RetentionClass = "active"
	case occupancy > 0:
		row.RetentionClass = "occupied"
	default:
		hold, err := diagnosticProtectedTx(tx, jobID, resourceID, availability, kind, resultJSON)
		if err != nil {
			return DiagnosticRetentionRow{}, err
		}
		switch hold {
		case "unknown":
			row.RetentionClass = "unknown"
		case "protected":
			row.RetentionClass = "protected"
		case "":
			if retentionState == "unavailable" {
				row.RetentionClass = "unavailable"
			} else {
				row.RetentionClass = "clear"
			}
		default:
			return DiagnosticRetentionRow{}, fmt.Errorf("%w: retention hold %q", ErrInvalid, hold)
		}
	}
	if row.PolicyClass != "accepted" || row.RetentionClass != "clear" {
		return row, nil
	}
	admitted := time.Unix(0, admittedNS).UTC()
	if row.EventRetentionDays != nil {
		if window, ok := diagnosticEventWindow(admitted, *row.EventRetentionDays, observed); ok {
			row.EventWindow = window
		}
	}
	if row.ResultRetentionDays != nil {
		ended, err := diagnosticVerifyEndedTx(tx, jobID, resultJSON)
		if err != nil {
			return DiagnosticRetentionRow{}, err
		}
		if window, ok := diagnosticResultWindow(admitted, ended, observed, *row.ResultRetentionDays); ok {
			row.ResultWindow = window
		}
	}
	return row, nil
}

func applyDiagnosticPolicyTx(tx *sql.Tx, jobID string, row *DiagnosticRetentionRow) error {
	policy, err := validatedJobBoundsTx(tx, jobID)
	if err != nil {
		if errors.Is(err, ErrBlocked) {
			row.PolicyClass = "unknown"
			return nil
		}
		return err
	}
	switch policy.Source {
	case "accepted_configuration":
		row.PolicyClass = "accepted"
		if policy.ConfigRevision.Valid {
			row.PolicyRevision = diagnosticInt(policy.ConfigRevision.Int64)
		}
		row.EventRetentionDays = diagnosticInt(policy.EventRetentionDays)
		row.ResultRetentionDays = diagnosticInt(policy.ResultRetentionDays)
	case "legacy_defaults":
		row.PolicyClass = "advisory_default"
		row.EventRetentionDays = diagnosticInt(policy.EventRetentionDays)
		row.ResultRetentionDays = diagnosticInt(policy.ResultRetentionDays)
	default:
		row.PolicyClass = "unknown"
	}
	return nil
}

func diagnosticPayloadMissingTx(tx *sql.Tx, jobID string) (bool, error) {
	var floor int64
	err := tx.QueryRow(`SELECT purged_through FROM managed_event_progress WHERE job_id=?`, jobID).Scan(&floor)
	if errors.Is(err, sql.ErrNoRows) {
		floor = 0
	} else if err != nil {
		return false, err
	}
	var missing int
	err = tx.QueryRow(`SELECT COUNT(*) FROM managed_event_owners o LEFT JOIN managed_events e ON e.cursor=o.cursor WHERE o.job_id=? AND o.cursor>? AND e.cursor IS NULL`, jobID, floor).Scan(&missing)
	return missing > 0, err
}

func permanentReplayCountTx(tx *sql.Tx, jobID string) (int64, error) {
	var count int64
	// Same nine kinds as the purge allowlist in retention.go. Other permanent
	// rows and ordinary payloads, including admitted, are not this count.
	err := tx.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind IN ('recovery_intent','phase_reconciled','vm_prepared','vm_retained','vm_transport_binding','vm_transport_admission','vm_retire_intent','power_cessation','local_workspace')`, jobID).Scan(&count)
	return count, err
}

// diagnosticProtectedTx reports "protected", "unknown", or "" for no hold.
// Every parsed result is checked with evidenceResolvedTx. False and unknown
// both hold, including a zero-list failed cleanup. A later complete exact
// receipt releases a protected-evidence hold and leaves the stored result
// unchanged. A normal recovery_intent is resolved by the durable settlement
// receipt, cursor order, and the same job, resource, config revision, and
// lease under the current runtime identity. A separate post-settlement failed
// cleanup_deadline intent is released only when that same intent is the exact
// guest-completed-cleanup-observed authority. phase_reconciled is not that proof.
func diagnosticProtectedTx(tx *sql.Tx, jobID, resourceID, availability, kind string, resultJSON []byte) (string, error) {
	if availability == "quarantined" || availability == "recovering" {
		return "protected", nil
	}
	uncertain, err := diagnosticPowerUncertainTx(tx, kind, resourceID)
	if err != nil {
		return "", err
	}
	if uncertain {
		return "protected", nil
	}
	if len(resultJSON) == 0 {
		return "unknown", nil
	}
	var result Result
	if decodeClosed(resultJSON, &result) != nil {
		return "unknown", nil
	}
	resolved, err := evidenceResolvedTx(tx, result)
	if errors.Is(err, ErrBlocked) {
		return "unknown", nil
	}
	if err != nil {
		return "", err
	}
	hold := ""
	if !resolved {
		hold = "protected"
	}
	receiptHold, err := diagnosticEvidenceReceiptHoldTx(tx, resourceID)
	if err != nil {
		return "", err
	}
	hold = mergeDiagnosticHold(hold, receiptHold)
	if hold == "unknown" {
		return "unknown", nil
	}
	intentHold, err := diagnosticRecoveryIntentHoldTx(tx, jobID, result)
	if err != nil {
		return "", err
	}
	hold = mergeDiagnosticHold(hold, intentHold)
	if hold == "unknown" {
		return "unknown", nil
	}
	var complete int
	err = tx.QueryRow(`SELECT complete FROM managed_purges WHERE job_id=?`, jobID).Scan(&complete)
	if errors.Is(err, sql.ErrNoRows) {
		return hold, nil
	}
	if err != nil {
		return "", err
	}
	if complete == 0 {
		return "protected", nil
	}
	return hold, nil
}

func mergeDiagnosticHold(current, next string) string {
	if current == "unknown" || next == "unknown" {
		return "unknown"
	}
	if current == "protected" || next == "protected" {
		return "protected"
	}
	return ""
}

func diagnosticEvidenceReceiptHoldTx(tx *sql.Tx, resourceID string) (string, error) {
	rows, err := tx.Query(`SELECT request_json,receipt_json FROM managed_evidence_recovery WHERE resource_id=?`, resourceID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	hold := ""
	for rows.Next() {
		var request, receipt []byte
		if err = rows.Scan(&request, &receipt); err != nil {
			return "", err
		}
		if len(receipt) == 0 {
			hold = mergeDiagnosticHold(hold, "protected")
			continue
		}
		var intent EvidenceRecoveryIntent
		var proof EvidenceRecoveryReceipt
		if decodeClosed(request, &intent) != nil || decodeClosed(receipt, &proof) != nil || !validEvidenceIntent(intent) || !validEvidenceReceipt(intent, proof) {
			return "unknown", nil
		}
		if !proof.Complete {
			hold = mergeDiagnosticHold(hold, "protected")
		}
	}
	return hold, rows.Err()
}

func diagnosticRecoveryIntentHoldTx(tx *sql.Tx, jobID string, result Result) (string, error) {
	rows, err := tx.Query(`SELECT cursor,payload_json FROM managed_events WHERE job_id=? AND kind='recovery_intent' ORDER BY cursor`, jobID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	hold := ""
	for rows.Next() {
		var cursor int64
		var raw []byte
		if err = rows.Scan(&cursor, &raw); err != nil {
			return "", err
		}
		var intent JobRecoveryRequest
		if decodeClosed(raw, &intent) != nil || intent.Identity.JobID != jobID || !ValidRequestKey(intent.MutationKey) || !intent.Evidence.valid() {
			return "unknown", nil
		}
		resolved, err := diagnosticIntentResolvedTx(tx, cursor, intent, result)
		if err != nil {
			return "", err
		}
		// A finished job or a later phase_reconciled does not clear this intent.
		if !resolved {
			hold = "protected"
		}
	}
	return hold, rows.Err()
}

// diagnosticIntentResolvedTx binds one historical recovery_intent. The normal
// proof is a verified local_settle receipt whose owner cursor is after the
// intent. That receipt survives a purge that retires the finished payload.
// The guest completed-cleanup authority is a separate exact match and does
// not consult the settlement cursor or the current runtime identity.
func diagnosticIntentResolvedTx(tx *sql.Tx, cursor int64, intent JobRecoveryRequest, result Result) (bool, error) {
	guest, err := diagnosticGuestCleanupResolvedTx(tx, intent, result)
	if err != nil || guest {
		return guest, err
	}
	if result.CleanupOutcome != "verified" || intent.Identity.JobID != result.JobID || intent.Identity.ResourceID != result.ResourceID || intent.Identity.ConfigRevision != result.ResourceRevision || intent.Identity.LeaseGeneration != result.LeaseGeneration {
		return false, nil
	}
	if err := matchLocalIdentityTx(tx, intent.Identity); err != nil {
		if errors.Is(err, ErrStale) || errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	var raw []byte
	err = tx.QueryRow(`SELECT receipt_json FROM managed_mutations WHERE mutation_key=?`, "settle-"+result.JobID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var receipt Receipt
	if decodeClosed(raw, &receipt) != nil || receipt.JobState != "finished" || receipt.JobID != result.JobID || receipt.ResourceID != result.ResourceID || receipt.ResourceConfigRevision != result.ResourceRevision || receipt.LeaseGeneration != result.LeaseGeneration || receipt.EventCursor <= cursor {
		return false, nil
	}
	var owner string
	err = tx.QueryRow(`SELECT job_id FROM managed_event_owners WHERE cursor=?`, receipt.EventCursor).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return owner == result.JobID, nil
}

// diagnosticGuestCleanupResolvedTx mirrors the exact guest-completed-cleanup
// authority. It releases only this intent. Another intent on the same job
// stays unresolved.
func diagnosticGuestCleanupResolvedTx(tx *sql.Tx, intent JobRecoveryRequest, result Result) (bool, error) {
	if len(result.ProtectedEvidence) != 0 || result.CleanupOutcome != "failed" || result.Reason != "cleanup_deadline" {
		return false, nil
	}
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
	matched := complete && intent.Identity.JobID == result.JobID && intent.Identity.ResourceID == result.ResourceID && intent.Identity.ConfigRevision == result.ResourceRevision && intent.Identity.LeaseGeneration == result.LeaseGeneration && intent.Evidence.valid() && intent.Evidence.Source == "machine-observed" && intent.Evidence.Mechanism == "guest-completed-cleanup-observed" && intent.Evidence.Reference == "job:"+result.JobID
	return matched, nil
}

func diagnosticVerifyEndedTx(tx *sql.Tx, jobID string, resultJSON []byte) (time.Time, error) {
	rows, err := tx.Query(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='phase_observed' ORDER BY cursor`, jobID)
	if err != nil {
		return time.Time{}, err
	}
	defer rows.Close()
	var ended time.Time
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return time.Time{}, err
		}
		var phase LocalPhase
		if json.Unmarshal(raw, &phase) != nil || phase.Phase != "verify" || !phase.Completed || phase.ObservedAt.IsZero() {
			continue
		}
		ended = phase.ObservedAt.UTC()
	}
	if err = rows.Err(); err != nil {
		return time.Time{}, err
	}
	if !ended.IsZero() {
		return ended, nil
	}
	if len(resultJSON) == 0 {
		return time.Time{}, nil
	}
	var result Result
	if json.Unmarshal(resultJSON, &result) != nil || result.Timings == nil {
		return time.Time{}, nil
	}
	for _, phase := range result.Timings.Phases {
		if phase.Phase != "verify" || phase.EndedAt == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, phase.EndedAt)
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, nil
}

func diagnosticEventWindow(admitted time.Time, days int64, observed time.Time) (string, bool) {
	bound, ok := diagnosticDayBound(admitted, days)
	if !ok {
		return "", false
	}
	if observed.Unix() <= bound {
		return "within", true
	}
	return "warning", true
}

func diagnosticResultWindow(admitted, ended, observed time.Time, days int64) (string, bool) {
	if ended.IsZero() || admitted.After(ended) || ended.After(observed) {
		return "", false
	}
	bound, ok := diagnosticDayBound(ended, days)
	if !ok {
		return "", false
	}
	if observed.Unix() <= bound {
		return "within", true
	}
	return "past", true
}

func diagnosticDayBound(start time.Time, days int64) (int64, bool) {
	if days <= 0 || days > math.MaxInt64/86400 || start.IsZero() {
		return 0, false
	}
	span := days * 86400
	sec := start.Unix()
	if sec > math.MaxInt64-span {
		return 0, false
	}
	return sec + span, true
}
