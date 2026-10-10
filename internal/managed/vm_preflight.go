package managed

import (
	"database/sql"
)

// Only a cancelled VM with no run claim or native/effect receipts can use the
// existing no-effect classifier from collecting. All lease/configuration,
// machine-observation and pending-power fences still apply in its transaction.
func cancelledVMPreflightTx(tx *sql.Tx, jobID string) bool {
	status, err := cancellationStatusTx(tx, jobID)
	if err != nil || !status.Requested || !status.BeforeOutcome || status.Deferred {
		return false
	}
	raw, err := localIdentityTx(tx, jobID)
	var id LocalIdentity
	if err != nil || decodeClosed(raw, &id) != nil || id.RunID != "" {
		return false
	}
	var kind string
	if tx.QueryRow(`SELECT kind FROM managed_resources WHERE id=?`, id.ResourceID).Scan(&kind) != nil || kind != "utm-macos" {
		return false
	}
	var count int
	if tx.QueryRow(`SELECT (SELECT COUNT(*) FROM managed_runs WHERE job_id=?)+(SELECT COUNT(*) FROM managed_runner_receipts WHERE job_id=?)+(SELECT COUNT(*) FROM managed_local_outcomes WHERE job_id=?)`, jobID, jobID, jobID).Scan(&count) != nil || count != 0 {
		return false
	}
	if tx.QueryRow(`SELECT COUNT(*) FROM managed_cancel_edges WHERE job_id=? AND to_phase='collecting' AND from_phase IN ('preparing','ready') AND identity_json=?`, jobID, raw).Scan(&count) != nil || count != 1 {
		return false
	}
	return true
}
