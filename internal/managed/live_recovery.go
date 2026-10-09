package managed

import (
	"database/sql"
)

// LiveReconciliation records which availability revision a live failure owns.
// Legacy failures and a preexisting operator restriction grant no restoration.
type LiveReconciliation struct {
	Schema             string         `json:"schema"`
	Reason             string         `json:"reason"`
	From               string         `json:"from"`
	Identity           LocalIdentity  `json:"identity"`
	PriorResource      ResourceStatus `json:"prior_resource"`
	QuarantineRevision int64          `json:"quarantine_revision"`
}

func (l *Ledger) restoreLiveAvailabilityTx(tx *sql.Tx, jobID string, result Result) (string, error) {
	if result.Schema != "haepari.result/v2" || result.CleanupOutcome != "verified" || len(result.ProtectedEvidence) != 0 || result.Observations != nil && result.Observations.OperatorAttested {
		return "", nil
	}
	var raw []byte
	err := tx.QueryRow(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='live_reconciliation' ORDER BY cursor DESC LIMIT 1`, jobID).Scan(&raw)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var proof LiveReconciliation
	if decodeClosed(raw, &proof) != nil || proof.Schema != "haepari.reconciliation-required/v2" || proof.Reason != "local_effect_uncertain" || proof.Identity.JobID != jobID || proof.Identity.ResourceID != result.ResourceID || proof.Identity.ConfigRevision != result.ResourceRevision || proof.Identity.LeaseGeneration != result.LeaseGeneration || proof.PriorResource.ID != result.ResourceID {
		return "", ErrBlocked
	}
	if proof.PriorResource.Availability != "available" || proof.PriorResource.Kind != "utm-macos" {
		return "", nil
	}
	if proof.QuarantineRevision != proof.PriorResource.AvailabilityRevision+1 {
		return "", ErrBlocked
	}
	if err = matchLocalIdentityTx(tx, proof.Identity); err != nil {
		return "", err
	}
	current, err := resourceStatusTx(tx, proof.Identity.ResourceID)
	if err != nil {
		return "", err
	}
	if current.Availability != "quarantined" || current.Reason != "local_effect_uncertain" || current.AvailabilityRevision != proof.QuarantineRevision || current.ConfigRevision != proof.PriorResource.ConfigRevision || current.ConfigurationDigest != proof.PriorResource.ConfigurationDigest || current.LeaseGeneration != proof.PriorResource.LeaseGeneration {
		return "", nil
	}
	if pending, err := pendingPowerTx(tx, current.ID); err != nil {
		return "", err
	} else if pending {
		return "", nil
	}
	var otherUnknown int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM managed_jobs WHERE resource_id=? AND id!=? AND state='reconciling' AND slot IS NOT NULL`, current.ID, jobID).Scan(&otherUnknown); err != nil {
		return "", err
	}
	if otherUnknown != 0 {
		return "", nil
	}
	phases, err := effectiveLocalPhasesTx(tx, jobID)
	if err != nil {
		return "", err
	}
	// VM cleanup's completed receipt includes pinned guest health and owned-root
	// absence; ledger-only results cannot supply that machine observation.
	for _, name := range []string{"collect", "cleanup", "verify"} {
		phase, ok := phases[name]
		if !ok || !phase.Completed || phase.Outcome == "unproven" || name != "collect" && phase.Outcome != "complete" {
			return "", nil
		}
	}
	var native int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM managed_runner_receipts WHERE job_id=? AND kind='exit'`, jobID).Scan(&native); err != nil {
		return "", err
	}
	if native != 1 {
		return "", nil
	}
	if _, err = tx.Exec(`UPDATE managed_resources SET availability='available',availability_reason=?,availability_revision=availability_revision+1 WHERE id=?`, proof.PriorResource.Reason, current.ID); err != nil {
		return "", err
	}
	if err = queuedAvailabilityEventsTx(tx, current.ID, map[string]any{"availability": "available", "reason": proof.PriorResource.Reason, "cause": "live_execution_reconciled"}); err != nil {
		return "", err
	}
	if _, err = addEvent(tx, jobID, "live_availability_restored", map[string]any{"schema": "haepari.availability-restoration/v2", "resource_id": current.ID, "quarantine_revision": proof.QuarantineRevision}); err != nil {
		return "", err
	}
	return "available", nil
}
