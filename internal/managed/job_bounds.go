package managed

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/irootkernel/haepari/internal/config"
)

const productPhaseCeiling int64 = 366 * 24 * 60 * 60

type admissionPolicy struct {
	RequestBytes        int64
	LogBytes            int64
	ArtifactBytes       int64
	ArtifactCount       int64
	EventRetentionDays  int64
	ResultRetentionDays int64
	ConfigRevision      sql.NullInt64
	ConfigDigest        sql.NullString
	Source              string
}

// UseLegacyFixtureBounds selects the named historical policy for an isolated
// fixture ledger that has no accepted configuration. NewLedger does not.
func (l *Ledger) UseLegacyFixtureBounds() { l.legacyFixtureBounds = true }

// RequireAcceptedConfiguration clears an explicit fixture selection. The
// constructor already fails closed; this call is not that default.
func (l *Ledger) RequireAcceptedConfiguration() { l.legacyFixtureBounds = false }

func checkJobBoundsSchemaTx(tx *sql.Tx) error {
	if _, err := tx.Exec(`SELECT job_id,request_bytes,log_bytes,artifact_bytes,artifact_count,event_retention_days,result_retention_days,config_revision,config_digest,policy_source FROM managed_job_bounds LIMIT 0`); err != nil {
		return fmt.Errorf("managed schema12 managed_job_bounds: %w", err)
	}
	var identity string
	if err := tx.QueryRow(`SELECT identity FROM schema_migrations WHERE version=12`).Scan(&identity); err != nil || identity != "job-bounds-v12" {
		return fmt.Errorf("managed schema12 managed_job_bounds identity: %v", err)
	}
	return nil
}

func initJobBoundsTx(tx *sql.Tx, version int) error {
	if version >= 12 {
		return nil
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS managed_job_bounds (
		job_id TEXT PRIMARY KEY REFERENCES managed_jobs(id),
		request_bytes INTEGER NOT NULL,
		log_bytes INTEGER NOT NULL,
		artifact_bytes INTEGER NOT NULL,
		artifact_count INTEGER NOT NULL,
		event_retention_days INTEGER NOT NULL,
		result_retention_days INTEGER NOT NULL,
		config_revision INTEGER,
		config_digest TEXT,
		policy_source TEXT NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO managed_job_bounds(
		job_id,request_bytes,log_bytes,artifact_bytes,artifact_count,event_retention_days,result_retention_days,config_revision,config_digest,policy_source)
		SELECT id,?,?,?,?,?,?,NULL,NULL,'legacy_defaults' FROM managed_jobs
		WHERE NOT EXISTS (SELECT 1 FROM managed_job_bounds b WHERE b.job_id = managed_jobs.id)`,
		config.MaxRequestBytes, config.MaxLogBytes, config.MaxArtifactBytes, config.MaxArtifactCount, int64(30), int64(90)); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO schema_migrations(version,identity) VALUES(12,'job-bounds-v12')`); err != nil {
		return err
	}
	_, err := tx.Exec(`PRAGMA user_version=12`)
	return err
}

func (l *Ledger) ScreenAdmission(resourceID string, job JobSpec, spec []byte) error {
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = prepareAdmissionTx(tx, l.legacyFixtureBounds, resourceID, job, spec)
	return err
}

func prepareAdmissionTx(tx *sql.Tx, legacy bool, resourceID string, job JobSpec, spec []byte) (admissionPolicy, error) {
	var raw []byte
	if err := tx.QueryRow(`SELECT spec_json FROM managed_resources WHERE id=?`, resourceID).Scan(&raw); err != nil {
		return admissionPolicy{}, mapNoRows(err)
	}
	resource, err := DecodeResource(raw)
	if err != nil {
		return admissionPolicy{}, err
	}
	if err := compareAdmissionBudgets(resource, job); err != nil {
		return admissionPolicy{}, err
	}
	policy, err := admissionPolicyTx(tx, legacy)
	if err != nil {
		return admissionPolicy{}, err
	}
	if int64(len(spec)) > policy.RequestBytes {
		return admissionPolicy{}, fmt.Errorf("%w: job spec JSON is %d bytes and exceeds accepted request_bytes %d", ErrInvalid, len(spec), policy.RequestBytes)
	}
	return policy, nil
}

func admissionPolicyTx(tx *sql.Tx, legacy bool) (admissionPolicy, error) {
	doc, _, missing, err := loadAcceptedConfigTx(tx)
	if err != nil {
		return admissionPolicy{}, err
	}
	if !missing {
		_, documentDigest, _, err := doc.Digests()
		if err != nil {
			return admissionPolicy{}, err
		}
		return admissionPolicy{
			RequestBytes: doc.Limits.RequestBytes, LogBytes: doc.Limits.LogBytesPerStream,
			ArtifactBytes: doc.Limits.ArtifactBytes, ArtifactCount: doc.Limits.ArtifactCount,
			EventRetentionDays: doc.Limits.EventRetentionDays, ResultRetentionDays: doc.Limits.ResultRetentionDays,
			ConfigRevision: sql.NullInt64{Int64: doc.Revision, Valid: true},
			ConfigDigest:   sql.NullString{String: documentDigest, Valid: true},
			Source:         "accepted_configuration",
		}, nil
	}
	if !legacy {
		return admissionPolicy{}, fmt.Errorf("%w: accepted configuration is required before admission", ErrNotFound)
	}
	return admissionPolicy{
		RequestBytes: config.MaxRequestBytes, LogBytes: config.MaxLogBytes,
		ArtifactBytes: config.MaxArtifactBytes, ArtifactCount: config.MaxArtifactCount,
		EventRetentionDays: 30, ResultRetentionDays: 90,
		Source: "legacy_defaults",
	}, nil
}

func compareAdmissionBudgets(resource ResourceSpec, job JobSpec) error {
	active := map[string]int64{
		"queue":   job.Deadlines.QueueSeconds,
		"prepare": job.Deadlines.PrepareSeconds,
		"collect": job.Deadlines.CollectSeconds,
		"cleanup": job.Deadlines.CleanupSeconds,
	}
	if job.Mode == "manual" {
		active["manual"] = job.Deadlines.ManualSeconds
	} else {
		active["run"] = job.Deadlines.RunSeconds
	}
	if resource.Schema == "haepari.resource/v2" {
		if resource.LifecycleBudgets == nil {
			return fmt.Errorf("%w: resource lifecycle budgets are absent", ErrInvalid)
		}
		ceilings := map[string]int64{
			"queue": resource.LifecycleBudgets.QueueSeconds, "prepare": resource.LifecycleBudgets.PrepareSeconds,
			"run": resource.LifecycleBudgets.RunSeconds, "manual": resource.LifecycleBudgets.ManualSeconds,
			"collect": resource.LifecycleBudgets.CollectSeconds, "cleanup": resource.LifecycleBudgets.CleanupSeconds,
		}
		for name, value := range active {
			if value > ceilings[name] {
				return fmt.Errorf("%w: %s_seconds %d exceeds resource lifecycle budget %d", ErrInvalid, name, value, ceilings[name])
			}
		}
		return nil
	}
	for name, value := range active {
		if value <= 0 || value > productPhaseCeiling {
			return fmt.Errorf("%w: %s_seconds exceeds legacy_product_ceiling %d", ErrInvalid, name, productPhaseCeiling)
		}
	}
	return nil
}

func insertJobBoundsTx(tx *sql.Tx, jobID string, policy admissionPolicy) error {
	var revision any
	var digest any
	if policy.ConfigRevision.Valid {
		revision = policy.ConfigRevision.Int64
	}
	if policy.ConfigDigest.Valid {
		digest = policy.ConfigDigest.String
	}
	_, err := tx.Exec(`INSERT INTO managed_job_bounds(
		job_id,request_bytes,log_bytes,artifact_bytes,artifact_count,event_retention_days,result_retention_days,config_revision,config_digest,policy_source)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		jobID, policy.RequestBytes, policy.LogBytes, policy.ArtifactBytes, policy.ArtifactCount,
		policy.EventRetentionDays, policy.ResultRetentionDays, revision, digest, policy.Source)
	return err
}

func queueDeadlineNs(now time.Time, seconds int64) (int64, error) {
	if seconds <= 0 {
		return 0, fmt.Errorf("%w: queue deadline", ErrInvalid)
	}
	span := time.Duration(seconds) * time.Second
	if span/time.Second != time.Duration(seconds) {
		return 0, fmt.Errorf("%w: queue deadline overflows", ErrInvalid)
	}
	return now.Add(span).UnixNano(), nil
}

func requireJobBoundsTx(tx *sql.Tx, jobID string) error {
	_, err := validatedJobBoundsTx(tx, jobID)
	return err
}

// ValidatedJobBounds reads the immutable pin with the same checks as the effect guard.
// A later accepted revision does not replace it. There is no file or default fallback.
func (l *Ledger) ValidatedJobBounds(jobID string) (admissionPolicy, error) {
	tx, err := l.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return admissionPolicy{}, err
	}
	defer tx.Rollback()
	return validatedJobBoundsTx(tx, jobID)
}

func (l *Ledger) RunnerReceiptPayload(jobID, kind string) ([]byte, error) {
	var raw []byte
	err := l.db.QueryRow(`SELECT payload_json FROM managed_runner_receipts WHERE job_id=? AND kind=?`, jobID, kind).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), raw...), nil
}

func validatedJobBoundsTx(tx *sql.Tx, jobID string) (admissionPolicy, error) {
	var request, logBytes, artifact, count, eventDays, resultDays int64
	var revision sql.NullInt64
	var digest sql.NullString
	var source string
	err := tx.QueryRow(`SELECT request_bytes,log_bytes,artifact_bytes,artifact_count,event_retention_days,result_retention_days,config_revision,config_digest,policy_source FROM managed_job_bounds WHERE job_id=?`, jobID).
		Scan(&request, &logBytes, &artifact, &count, &eventDays, &resultDays, &revision, &digest, &source)
	if err == sql.ErrNoRows {
		return admissionPolicy{}, fmt.Errorf("%w: job bounds are missing; occupancy and protected evidence stay unchanged", ErrBlocked)
	}
	if err != nil {
		return admissionPolicy{}, err
	}
	policy := admissionPolicy{
		RequestBytes: request, LogBytes: logBytes, ArtifactBytes: artifact, ArtifactCount: count,
		EventRetentionDays: eventDays, ResultRetentionDays: resultDays,
		ConfigRevision: revision, ConfigDigest: digest, Source: source,
	}
	switch source {
	case "legacy_defaults":
		if request == config.MaxRequestBytes && logBytes == config.MaxLogBytes &&
			artifact == config.MaxArtifactBytes && count == config.MaxArtifactCount &&
			eventDays == 30 && resultDays == 90 && !revision.Valid && !digest.Valid {
			return policy, nil
		}
	case "accepted_configuration":
		// The pin names one historical audit. A later accepted revision does not replace it.
		if revision.Valid && revision.Int64 >= 1 && digest.Valid {
			var auditDigest string
			var auditJSON []byte
			err = tx.QueryRow(`SELECT document_digest,document_json FROM managed_config_audits WHERE revision=?`, revision.Int64).Scan(&auditDigest, &auditJSON)
			if errors.Is(err, sql.ErrNoRows) {
				return admissionPolicy{}, fmt.Errorf("%w: job bounds are malformed; occupancy and protected evidence stay unchanged", ErrBlocked)
			}
			if err != nil {
				return admissionPolicy{}, err
			}
			doc, _, parseErr := config.Parse(auditJSON)
			if parseErr != nil {
				return admissionPolicy{}, fmt.Errorf("%w: job bounds are malformed; occupancy and protected evidence stay unchanged", ErrBlocked)
			}
			canonical, documentDigest, _, digestErr := doc.Digests()
			if digestErr != nil || !bytes.Equal(canonical, auditJSON) || auditDigest != documentDigest || digest.String != documentDigest || doc.Revision != revision.Int64 {
				return admissionPolicy{}, fmt.Errorf("%w: job bounds are malformed; occupancy and protected evidence stay unchanged", ErrBlocked)
			}
			if request == doc.Limits.RequestBytes && logBytes == doc.Limits.LogBytesPerStream &&
				artifact == doc.Limits.ArtifactBytes && count == doc.Limits.ArtifactCount &&
				eventDays == doc.Limits.EventRetentionDays && resultDays == doc.Limits.ResultRetentionDays {
				return policy, nil
			}
		}
	}
	return admissionPolicy{}, fmt.Errorf("%w: job bounds are malformed; occupancy and protected evidence stay unchanged", ErrBlocked)
}

func effectJobID(request any) string {
	return effectJobIDValue(reflect.ValueOf(request), 0)
}

func effectJobIDValue(v reflect.Value, depth int) string {
	if !v.IsValid() || depth > 4 {
		return ""
	}
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return ""
	}
	if id := effectString(v, "JobID"); id != "" {
		return id
	}
	if id := effectString(v, "TargetJobID"); id != "" {
		return id
	}
	for _, name := range []string{"Identity", "Record"} {
		field := v.FieldByName(name)
		if !field.IsValid() {
			continue
		}
		if id := effectJobIDValue(field, depth+1); id != "" {
			return id
		}
	}
	return ""
}

func effectString(v reflect.Value, name string) string {
	field := v.FieldByName(name)
	if field.IsValid() && field.Kind() == reflect.String {
		return field.String()
	}
	return ""
}
