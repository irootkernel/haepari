package managed

import (
	"context"
)

// Diagnostic codes are a closed, nonsecret contract shared by the ledger,
// native runner and transport. They describe observations, never authority.
const (
	DiagnosticDeadlineExceeded     = "deadline_exceeded"
	DiagnosticIdentityConflict     = "identity_conflict"
	DiagnosticTerminationUnproven  = "termination_unproven"
	DiagnosticReceiptAmbiguous     = "receipt_ambiguous"
	DiagnosticInvalidReceipt       = "invalid_receipt"
	DiagnosticPermissionDenied     = "permission_denied"
	DiagnosticPathAbsent           = "path_absent"
	DiagnosticObservationFailed    = "observation_failed"
	DiagnosticTransportUnavailable = "transport_unavailable"
	DiagnosticStaleRevision        = "stale_revision"
	DiagnosticGuestEffectUncertain = "guest_effect_uncertain"
)

// ValidDiagnosticCause checks the shared closed diagnostic vocabulary.
func ValidDiagnosticCause(cause string) bool {
	return oneOf(cause, DiagnosticDeadlineExceeded, DiagnosticIdentityConflict, DiagnosticTerminationUnproven, DiagnosticReceiptAmbiguous, DiagnosticInvalidReceipt, DiagnosticPermissionDenied, DiagnosticPathAbsent, DiagnosticObservationFailed, DiagnosticTransportUnavailable, DiagnosticStaleRevision, DiagnosticGuestEffectUncertain)
}

// RecordDiagnosticCause accepts only nonsecret classification codes. A bounded
// event inventory survives restart without allowing transient retries to grow it.
func (l *Ledger) RecordDiagnosticCause(jobID, cause string) error {
	return l.recordDiagnosticCause(jobID, cause, false)
}

// RecordRecoveryDiagnosticCause supplements failed v2 recovery observations of
// retained Jobs. It never changes a terminal result or adds purged history.
func (l *Ledger) RecordRecoveryDiagnosticCause(jobID, cause string) error {
	return l.recordDiagnosticCause(jobID, cause, true)
}

func (l *Ledger) recordDiagnosticCause(jobID, cause string, recovery bool) error {
	if !ValidDiagnosticCause(cause) {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='diagnostic'`, jobID).Scan(&count); err != nil {
		return err
	}
	if count >= 32 {
		return nil
	}
	var exists bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM managed_events WHERE job_id=? AND kind='diagnostic' AND json_extract(payload_json,'$.cause')=?)`, jobID, cause).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	var state string
	if err = tx.QueryRow(`SELECT state FROM managed_jobs WHERE id=?`, jobID).Scan(&state); err != nil {
		return mapNoRows(err)
	}
	if state == "finished" {
		if !recovery {
			return nil
		}
		var retention string
		if err = tx.QueryRow(`SELECT retention_state FROM managed_admissions WHERE job_id=?`, jobID).Scan(&retention); err != nil {
			return mapNoRows(err)
		}
		if retention != "available" {
			return nil
		}
	}
	// The event supplements observation only; it cannot release capacity,
	// establish proof or mutate an immutable terminal result.
	payload := struct {
		Schema string `json:"schema"`
		Cause  string `json:"cause"`
	}{"haepari.diagnostic/v1", cause}
	if _, err = addEvent(tx, jobID, "diagnostic", payload); err != nil {
		return err
	}
	return l.commit(tx)
}
