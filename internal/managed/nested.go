package managed

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
)

var ErrNestedAcquisition = errors.New("NESTED_ACQUISITION_REJECTED")
var ErrNestedWait = errors.New("NESTED_WAIT_REJECTED")

const ParentContextEnvironment = "HAEPARI_PARENT_CONTEXT"
const ParentContextHeader = "X-Haepari-Parent-Context"
const maxParentContextBytes = 8192

type ParentEntry struct {
	InstallationID  string `json:"installation_id"`
	JobID           string `json:"job_id"`
	LeaseGeneration int64  `json:"lease_generation"`
	Nonce           string `json:"nonce"`
}
type ParentChain []ParentEntry

func (c ParentChain) Validate() error {
	if len(c) > 32 {
		return ErrInvalid
	}
	for _, e := range c {
		if !identifier.MatchString(e.InstallationID) || !identifier.MatchString(e.JobID) || e.LeaseGeneration <= 0 || !validDigest(e.Nonce) {
			return ErrInvalid
		}
	}
	encoded, err := json.Marshal(c)
	if err != nil || len(encoded) > maxParentContextBytes {
		return ErrInvalid
	}
	return nil
}
func DecodeParentChain(raw string) (ParentChain, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxParentContextBytes {
		return nil, ErrInvalid
	}
	var c ParentChain
	if err := decodeClosed([]byte(raw), &c); err != nil {
		return nil, ErrInvalid
	}
	return c, c.Validate()
}

func nestedTx(q rowQuerier, installationID string, chain ParentChain) (bool, error) {
	for _, e := range chain {
		if e.InstallationID != installationID {
			continue
		}
		var nonce, state string
		var lease int64
		var slot sql.NullInt64
		err := q.QueryRow(`SELECT n.nonce,j.lease_generation,j.state,j.slot FROM managed_parent_nonces n JOIN managed_jobs j ON j.id=n.job_id WHERE n.job_id=? AND n.lease_generation=?`, e.JobID, e.LeaseGeneration).Scan(&nonce, &lease, &state, &slot)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		if lease == e.LeaseGeneration && slot.Valid && state != "finished" && subtle.ConstantTimeCompare([]byte(nonce), []byte(e.Nonce)) == 1 {
			return true, nil
		}
	}
	return false, nil
}
func (l *Ledger) Nested(installationID string, chain ParentChain) (bool, error) {
	if err := chain.Validate(); err != nil {
		return false, err
	}
	return nestedTx(l.db, installationID, chain)
}
func issueParentNonceTx(tx *sql.Tx, jobID string, lease int64) error {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO managed_parent_nonces VALUES(?,?,?)`, jobID, lease, hex.EncodeToString(nonce[:]))
	return err
}

// RunnerParentChain adds a controller-issued nonce to the admission's inherited
// chain. Foreign installation entries survive unchanged; a stale lease cannot
// receive a newly authoritative context.
func (l *Ledger) RunnerParentChain(installationID, jobID string, lease int64) (ParentChain, error) {
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var raw []byte
	var nonce, state string
	var current int64
	err = tx.QueryRow(`SELECT COALESCE(p.chain_json,'null'),n.nonce,j.lease_generation,j.state FROM managed_jobs j JOIN managed_parent_nonces n ON n.job_id=j.id LEFT JOIN managed_parent_context p ON p.job_id=j.id WHERE j.id=?`, jobID).Scan(&raw, &nonce, &current, &state)
	if err != nil {
		return nil, mapNoRows(err)
	}
	if current != lease || state == "queued" || state == "finished" {
		return nil, ErrStale
	}
	var c ParentChain
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	c = append(c, ParentEntry{installationID, jobID, lease, nonce})
	return c, c.Validate()
}

func admissionParentCapacity(installationID, jobID string, chain ParentChain) error {
	// Reserve the maximum serialized entry permitted by the identifier/nonce
	// contract and an int64 lease, so later runner export cannot overflow.
	if installationID == "" {
		installationID = strings.Repeat("a", 128)
	}
	extended := append(append(ParentChain(nil), chain...), ParentEntry{installationID, jobID, math.MaxInt64, strings.Repeat("a", 64)})
	return extended.Validate()
}
