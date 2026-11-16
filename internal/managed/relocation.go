package managed

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/irootkernel/haepari/internal/platform"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// RelocationObservation uses only the caller's consistent read transaction. It
// deliberately does not construct NewLedger, migrate, advance clocks or settle
// anything. An immutable negative verdict/gap is distinct from a current hold.
func RelocationObservation(tx *sql.Tx, oldRoot string) (string, error) {
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return "", err
	}
	if version < 6 || version > 13 {
		return "", ErrUnsupported
	}
	var unsupported int
	if err := tx.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE type IN ('trigger','view') OR upper(sql) LIKE '%CREATE VIRTUAL TABLE%'").Scan(&unsupported); err != nil {
		return "", err
	}
	if unsupported != 0 {
		return "", ErrUnsupported
	}
	checks := []struct {
		since int
		check func(*sql.Tx) error
	}{{1, func(t *sql.Tx) error { return checkBaseSchemaTx(t, version) }}, {5, checkSchedulingSchemaTx}, {6, checkCancellationSchemaTx}, {6, checkClientSchemaTx}, {7, checkLockSchemaTx}, {8, checkRunnerSchemaTx}, {9, checkRetentionSchemaTx}, {10, checkManualSchemaTx}, {11, checkEffectiveConfigSchemaTx}, {12, checkJobBoundsSchemaTx}, {13, checkEvidenceBudgetSchemaTx}}
	for _, c := range checks {
		if version >= c.since {
			if err := c.check(tx); err != nil {
				return "", err
			}
		}
	}
	identities := []string{"managed-initial-v1", "managed-observation-v2", "sealed-job-input-v3", "durable-admissions-v4", "managed-scheduling-v5", "durable-client-v6", "advisory-lock-v7", "independent-runner-receipts-v8", "evidence-retention-v9", "durable-manual-sessions-v10", "effective-config-v11", "job-bounds-v12", "evidence-recovery-budget-v13"}
	rows, err := tx.Query("SELECT version,identity FROM schema_migrations ORDER BY version")
	if err != nil {
		return "", err
	}
	seen := map[int]bool{}
	for rows.Next() {
		var v int
		var id string
		if err = rows.Scan(&v, &id); err != nil {
			rows.Close()
			return "", err
		}
		if v < 1 || v > version || id != identities[v-1] || seen[v] {
			rows.Close()
			return "", ErrUnsupported
		}
		seen[v] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	for v := 2; v <= version; v++ {
		if !seen[v] {
			return "", ErrUnsupported
		}
	}
	installation, err := installationTx(tx)
	if err != nil || !identifier.MatchString(installation) {
		return "", ErrInvalid
	}
	for _, q := range []string{"SELECT COUNT(*) FROM managed_jobs WHERE state!='finished'", "SELECT COUNT(*) FROM managed_resources WHERE occupancy!=0 OR availability NOT IN ('available','paused')", "SELECT COUNT(*) FROM managed_power_transitions WHERE state!='reconciled'"} {
		var n int
		if err = tx.QueryRow(q).Scan(&n); err != nil {
			return "", err
		}
		if n != 0 {
			return "", ErrBlocked
		}
	}
	if version >= 7 {
		var n int
		if err = tx.QueryRow("SELECT COUNT(*) FROM managed_lock_requests WHERE state NOT IN ('released','queue_expired','hold_expired','cancelled')").Scan(&n); err != nil {
			return "", err
		}
		if n != 0 {
			return "", ErrBlocked
		}
	}
	if version >= 9 {
		for _, q := range []string{"SELECT COUNT(*) FROM managed_purges WHERE complete!=1", "SELECT COUNT(*) FROM managed_evidence_recovery WHERE receipt_json IS NULL"} {
			var n int
			if err = tx.QueryRow(q).Scan(&n); err != nil {
				return "", err
			}
			if n != 0 {
				return "", ErrBlocked
			}
		}
	}
	rows, err = tx.Query("SELECT id,spec_json FROM managed_resources ORDER BY id")
	if err != nil {
		return "", err
	}
	type resource struct {
		id  string
		raw []byte
	}
	var resources []resource
	for rows.Next() {
		var r resource
		if err = rows.Scan(&r.id, &r.raw); err != nil {
			rows.Close()
			return "", err
		}
		resources = append(resources, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	for _, r := range resources {
		spec, e := DecodeResource(r.raw)
		if e != nil || spec.ID != r.id {
			return "", ErrInvalid
		}
		if err := relocationReferences(spec, oldRoot); err != nil {
			return "", err
		}
		if version >= 9 {
			hold, e := diagnosticEvidenceReceiptHoldTx(tx, r.id)
			if e != nil {
				return "", e
			}
			if hold != "" {
				return "", ErrBlocked
			}
		}
		status, e := resourceStatusTx(tx, r.id)
		if e != nil {
			return "", e
		}
		if status.Mutation != nil || status.Occupancy != 0 || status.AdvisoryGrants != 0 || status.AdvisoryQueued != 0 {
			return "", ErrBlocked
		}
	}
	rows, err = tx.Query("SELECT j.id,j.resource_id,j.spec_json,j.result_json,r.availability,r.kind FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id ORDER BY j.id")
	if err != nil {
		return "", err
	}
	type job struct {
		id, resource, availability, kind string
		spec, result                     []byte
	}
	var jobs []job
	for rows.Next() {
		var j job
		if err = rows.Scan(&j.id, &j.resource, &j.spec, &j.result, &j.availability, &j.kind); err != nil {
			rows.Close()
			return "", err
		}
		jobs = append(jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	for _, j := range jobs {
		spec, e := DecodeJob(j.spec)
		if e != nil {
			return "", e
		}
		result, e := DecodeResultForJob(j.result, spec)
		if e != nil {
			return "", e
		}
		if result.JobID != j.id || result.ResourceID != j.resource {
			return "", ErrInvalid
		}
		if version >= 9 {
			hold, e := diagnosticProtectedTx(tx, j.id, j.resource, j.availability, j.kind, j.result)
			if e != nil {
				return "", e
			}
			if hold != "" {
				return "", ErrBlocked
			}
		} else {
			if len(result.ProtectedEvidence) != 0 || !oneOf(string(result.CleanupOutcome), "verified", "not_needed") {
				return "", ErrBlocked
			}
		}
	}
	// Native runner payloads remain immutable execution history. Validate every
	// stored digest/identity through the existing contract; never retarget paths.
	if version >= 8 {
		rows, err = tx.Query("SELECT job_id,run_id,kind,digest,payload_json FROM managed_runner_receipts ORDER BY job_id,kind")
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var jobID, runID, kind, digest string
			var raw []byte
			if err = rows.Scan(&jobID, &runID, &kind, &digest, &raw); err != nil {
				rows.Close()
				return "", err
			}
			var header struct {
				Schema      string                `json:"schema"`
				Identity    LocalIdentity         `json:"identity"`
				LogIdentity platform.FileIdentity `json:"log_identity"`
			}
			if ValidateUniqueJSONKeys(raw) != nil || json.Unmarshal(raw, &header) != nil || header.Identity.JobID != jobID || header.Identity.RunID != runID || !oneOf(kind, "request", "intent", "start", "outcome", "exit") || !validRecoveryDigest(digest) || (header.Schema != RunnerSchema(kind) && !(kind == "request" && header.Schema == PersistentRunnerRequestSchema && header.LogIdentity.Valid())) || kind == "request" && header.Schema == RunnerSchema(kind) && header.LogIdentity != (platform.FileIdentity{}) {
				rows.Close()
				return "", ErrInvalid
			}
			sum := sha256.Sum256(raw)
			if digest != "sha256:"+hex.EncodeToString(sum[:]) {
				rows.Close()
				return "", fmt.Errorf("%w: retained runner digest", ErrInvalid)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	// Unrecognized tables are durable state whose lifecycle this binary cannot
	// classify. SQLite's own sequence table is retained in the logical digest.
	allowed := map[string]bool{}
	for _, name := range strings.Fields("schema_migrations sqlite_sequence managed_resources managed_admissions managed_jobs managed_events managed_event_owners managed_event_progress managed_mutations managed_job_inputs managed_runs managed_power_transitions managed_no_effect_observations managed_availability_receipts managed_parent_context managed_parent_nonces managed_queue_blockers managed_vm_observations managed_power_progress managed_cancellations managed_cancel_edges managed_local_outcomes managed_installation managed_lock_requests managed_lock_slots managed_lock_clock managed_lock_discontinuities managed_runner_receipts managed_purges managed_evidence_recovery managed_manual_sessions managed_effective_config managed_config_audits managed_job_bounds") {
		allowed[name] = true
	}
	rows, err = tx.Query("SELECT name FROM sqlite_schema WHERE type='table'")
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return "", err
		}
		if !allowed[name] {
			rows.Close()
			return "", ErrUnsupported
		}
	}
	err = rows.Err()
	rows.Close()
	return installation, err
}

func relocationReferences(spec ResourceSpec, root string) error {
	inside := func(p string) bool { return p == root || strings.HasPrefix(p, root+string(filepath.Separator)) }
	if spec.Kind != "utm-macos" {
		return nil
	}
	if inside(spec.Adapter.UTMCtl) {
		return ErrBlocked
	}
	if resolved, err := filepath.EvalSymlinks(spec.Adapter.UTMCtl); err == nil && inside(resolved) {
		return ErrBlocked
	}
	for i, ref := range []string{spec.Guest.ConnectionReference, spec.Guest.KnownHostsReference} {
		parts := strings.SplitN(ref, ":", 3)
		if len(parts) != 3 || parts[0] != "sha256" || !validRecoveryDigest(parts[0]+":"+parts[1]) || !filepath.IsAbs(parts[2]) || filepath.Clean(parts[2]) != parts[2] || inside(parts[2]) {
			return ErrBlocked
		}
		if resolved, err := filepath.EvalSymlinks(parts[2]); err != nil || inside(resolved) {
			return ErrBlocked
		}
		fd, err := unix.Open(parts[2], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return ErrBlocked
		}
		f := os.NewFile(uintptr(fd), parts[2])
		var st unix.Stat_t
		err = unix.Fstat(fd, &st)
		if err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Mode&0022 != 0 {
			f.Close()
			return ErrBlocked
		}
		raw, err := io.ReadAll(io.LimitReader(f, 16385))
		f.Close()
		sum := sha256.Sum256(raw)
		if err != nil || len(raw) > 16384 || hex.EncodeToString(sum[:]) != parts[1] {
			return ErrBlocked
		}
		if i == 0 {
			var mapping struct {
				Schema       string `json:"schema"`
				User         string `json:"user"`
				Port         int    `json:"port"`
				IdentityFile string `json:"identity_file"`
			}
			if decodeClosed(raw, &mapping) != nil || mapping.Schema != "haepari.ssh-mapping/v1" || !filepath.IsAbs(mapping.IdentityFile) || filepath.Clean(mapping.IdentityFile) != mapping.IdentityFile || inside(mapping.IdentityFile) {
				return ErrBlocked
			}
			if resolved, err := filepath.EvalSymlinks(mapping.IdentityFile); err == nil && inside(resolved) {
				return ErrBlocked
			}
		}
	}
	return nil
}
