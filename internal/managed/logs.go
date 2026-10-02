package managed

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
)

func (l *Ledger) LocalLogManifest(jobID string) ([]ResultArtifact, []EvidenceGap, error) {
	var raw []byte
	var digest string
	err := l.db.QueryRow(`SELECT payload_json,digest FROM managed_runner_receipts WHERE job_id=? AND kind='exit'`, jobID).Scan(&raw, &digest)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(raw)
	if "sha256:"+hex.EncodeToString(sum[:]) != digest {
		return nil, nil, ErrBlocked
	}
	var record map[string]json.RawMessage
	if err = json.Unmarshal(raw, &record); err != nil {
		return nil, nil, err
	}
	if len(record["log"]) == 0 {
		return nil, nil, nil
	}
	var m struct {
		Artifact     ResultArtifact `json:"artifact"`
		LimitBytes   int64          `json:"limit_bytes"`
		DroppedBytes int64          `json:"dropped_bytes"`
		Gap          string         `json:"gap,omitempty"`
	}
	if err = decodeClosed(record["log"], &m); err != nil {
		return nil, nil, err
	}
	if m.Artifact.Path != "output.log" || m.Artifact.SizeBytes < 0 || !validDigest(m.Artifact.SHA256) || m.LimitBytes <= 0 || m.Artifact.SizeBytes > m.LimitBytes || m.DroppedBytes < 0 || !oneOf(m.Gap, "", "log_limit", "log_write_failed", "log_read_failed") || m.Gap == "" && m.DroppedBytes != 0 {
		return nil, nil, ErrBlocked
	}
	gaps := []EvidenceGap{}
	if m.Gap != "" {
		gaps = append(gaps, EvidenceGap{Path: "output.log", Reason: m.Gap, Offset: m.Artifact.SizeBytes, DroppedBytes: m.DroppedBytes})
	}
	return []ResultArtifact{m.Artifact}, gaps, nil
}
