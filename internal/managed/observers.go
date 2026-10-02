package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// LogObservation reads the run claim and outcome with the Job snapshot. A
// missing file is not evidence of no-start; observers use the recorded facts.
type LogObservation struct {
	Snapshot          Receipt
	RunID             string
	Outcome           *LocalOutcome
	TerminationProven bool
	Result            *Result
}

func (l *Ledger) LogObservation(jobID string) (LogObservation, error) {
	var o LogObservation
	if !identifier.MatchString(jobID) {
		return o, ErrInvalid
	}
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return o, err
	}
	defer tx.Rollback()
	o.Snapshot, err = l.snapshotTx(tx, jobID)
	if err != nil {
		return o, err
	}
	if o.Snapshot.ResultStatus == "unavailable" {
		return o, ErrResultUnavailable
	}
	var result []byte
	if err = tx.QueryRow(`SELECT COALESCE(r.run_id,''),j.result_json FROM managed_jobs j LEFT JOIN managed_runs r ON r.job_id=j.id WHERE j.id=?`, jobID).Scan(&o.RunID, &result); err != nil {
		return o, mapNoRows(err)
	}
	if len(result) > 0 {
		o.Result = &Result{}
		if err = json.Unmarshal(result, o.Result); err != nil {
			return o, err
		}
		if o.Result.JobID != jobID || o.Result.ResourceID != o.Snapshot.ResourceID || o.Result.LeaseGeneration != o.Snapshot.LeaseGeneration {
			return o, fmt.Errorf("local log result lost its job binding")
		}
	}
	var identity, outcome, proof []byte
	err = tx.QueryRow(`SELECT identity_json,outcome_json,proof_json FROM managed_local_outcomes WHERE job_id=?`, jobID).Scan(&identity, &outcome, &proof)
	if errors.Is(err, sql.ErrNoRows) {
		return o, nil
	}
	if err != nil {
		return o, err
	}
	var id LocalIdentity
	if err = json.Unmarshal(identity, &id); err != nil {
		return o, err
	}
	if id.JobID != jobID || id.RunID != o.RunID || id.LeaseGeneration != o.Snapshot.LeaseGeneration {
		return o, fmt.Errorf("local log outcome lost its run binding")
	}
	current, err := localIdentityTx(tx, jobID)
	if err != nil {
		return o, err
	}
	var actual LocalIdentity
	if err := json.Unmarshal(current, &actual); err != nil {
		return o, err
	}
	if actual != id {
		return o, fmt.Errorf("local log outcome lost its ownership fence")
	}
	o.Outcome = &LocalOutcome{}
	if err = json.Unmarshal(outcome, o.Outcome); err != nil {
		return o, err
	}
	if len(proof) > 0 {
		var termination LocalTerminationRequest
		if err = json.Unmarshal(proof, &termination); err != nil {
			return o, err
		}
		if termination.Identity != id {
			return o, fmt.Errorf("local log proof lost its run binding")
		}
		o.TerminationProven = true
	}
	return o, nil
}
