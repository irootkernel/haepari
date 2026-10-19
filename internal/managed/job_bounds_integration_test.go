//go:build integration

package managed

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/config"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func countQuery(db *sql.DB, query string) int {
	var n int
	gomega.Expect(db.QueryRow(query).Scan(&n)).To(gomega.Succeed())
	return n
}

func v2Resource(ceil *LifecycleBudgets) ResourceSpec {
	resource := validResource()
	resource.Schema = "haepari.resource/v2"
	resource.LifecycleBudgets = ceil
	return resource
}

var _ = ginkgo.Describe("JobBoundsSchema12", ginkgo.Label("integration"), func() {
	ginkgo.It("admits v1 and compares v2 ceilings before creating a job", func() {
		t := ginkgo.GinkgoT()
		db, ledger := openLedger(t, t.TempDir()+"/state.sqlite", 2)
		defer db.Close()
		gomega.Expect(ledger.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		var stored []byte
		gomega.Expect(db.QueryRow(`SELECT spec_json FROM managed_resources WHERE id='local-a'`).Scan(&stored)).To(gomega.Succeed())
		gomega.Expect(string(stored)).To(gomega.Equal(v1ResourceGolden))
		first, err := ledger.Admit(encode(t, validJob()), digest, "v1-key", "v1-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		replay, err := ledger.Admit(encode(t, validJob()), digest, "v1-key", "other-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.JobID).To(gomega.Equal(first.JobID))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_jobs`)).To(gomega.Equal(1))

		wide := v2Resource(phaseCeilings(100))
		wide.ID = "local-b"
		gomega.Expect(ledger.RegisterResource(encode(t, wide))).To(gomega.Succeed())
		over := validJob()
		over.ResourceID = "local-b"
		over.Deadlines.RunSeconds = 101
		_, err = ledger.Admit(encode(t, over), digest, "over-key", "over-job")
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("lifecycle budget")))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_jobs WHERE resource_id='local-b'`)).To(gomega.BeZero())
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_admissions WHERE request_key='over-key'`)).To(gomega.BeZero())

		manual := over
		manual.Mode = "manual"
		manual.Deadlines.RunSeconds = 0
		manual.Deadlines.ManualSeconds = 50
		manual.Manual = &Manual{ScenarioDocument: "scenarios/check.md", RequiredScenarios: []string{"one"}}
		_, err = ledger.Admit(encode(t, manual), digest, "manual-key", "manual-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})

	ginkgo.It("keeps a pinned job when a later resource ceiling is tighter", func() {
		t := ginkgo.GinkgoT()
		db, ledger := openLedger(t, t.TempDir()+"/state.sqlite", 2)
		defer db.Close()
		resource := v2Resource(phaseCeilings(100))
		gomega.Expect(ledger.RegisterResource(encode(t, resource))).To(gomega.Succeed())
		job := validJob()
		job.Deadlines = Deadlines{QueueSeconds: 50, PrepareSeconds: 50, RunSeconds: 50, CollectSeconds: 50, CleanupSeconds: 50}
		admitted, err := ledger.Admit(encode(t, job), digest, "pinned-key", "pinned-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var spec []byte
		gomega.Expect(db.QueryRow(`SELECT spec_json FROM managed_jobs WHERE id=?`, admitted.JobID).Scan(&spec)).To(gomega.Succeed())
		resource.Revision = 2
		resource.LifecycleBudgets = phaseCeilings(10)
		gomega.Expect(ledger.UpdateResource(encode(t, resource), 1, 1)).To(gomega.Succeed())
		var state string
		var occupancy int
		var after []byte
		gomega.Expect(db.QueryRow(`SELECT j.state,j.spec_json,r.occupancy FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, admitted.JobID).Scan(&state, &after, &occupancy)).To(gomega.Succeed())
		gomega.Expect(state).To(gomega.Equal("queued"))
		gomega.Expect(after).To(gomega.Equal(spec))
		gomega.Expect(occupancy).To(gomega.BeZero())
		_, err = ledger.Reserve(ReserveRequest{JobID: admitted.JobID, ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "pinned-reserve"})
		gomega.Expect(errors.Is(err, ErrStale)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).NotTo(gomega.ContainSubstring("lifecycle budget"))
		_, err = ledger.Admit(encode(t, job), digest, "new-ceiling-key", "new-ceiling-job")
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("lifecycle budget")))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_jobs`)).To(gomega.Equal(1))
		gomega.Expect(db.QueryRow(`SELECT state FROM managed_jobs WHERE id=?`, admitted.JobID).Scan(&state)).To(gomega.Succeed())
		gomega.Expect(state).To(gomega.Equal("queued"))
	})

	ginkgo.It("conflicts a changed spec under the same key and replays an explicit one", func() {
		t := ginkgo.GinkgoT()
		db, ledger := openLedger(t, t.TempDir()+"/state.sqlite", 2)
		defer db.Close()
		gomega.Expect(ledger.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		original := validJob()
		changed := original
		changed.Deadlines.QueueSeconds = 11
		first, err := ledger.Admit(encode(t, original), digest, "same-key", "same-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		events := countQuery(db, `SELECT COUNT(*) FROM managed_events`)
		_, err = ledger.Admit(encode(t, changed), digest, "same-key", "new-job")
		gomega.Expect(errors.Is(err, ErrConflict)).To(gomega.BeTrue())
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_jobs`)).To(gomega.Equal(1))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_events`)).To(gomega.Equal(events))
		replay, err := ledger.Admit(encode(t, original), digest, "same-key", "unused")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.JobID).To(gomega.Equal(first.JobID))
		gomega.Expect(replay.Admission).To(gomega.Equal(first.Admission))
	})

	ginkgo.It("applies request_bytes to a new spec after replay", func() {
		t := ginkgo.GinkgoT()
		db, ledger := openLedger(t, t.TempDir()+"/state.sqlite", 2)
		defer db.Close()
		gomega.Expect(ledger.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		doc := config.Defaults()
		accepted, err := ledger.CommitEffectiveConfig(doc, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, documentDigest, _, err := accepted.Digests()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		spec := encode(t, validJob())
		first, err := ledger.Admit(spec, digest, "capped-key", "capped-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var source, storedDigest string
		var revision int64
		gomega.Expect(db.QueryRow(`SELECT policy_source,config_revision,config_digest FROM managed_job_bounds WHERE job_id=?`, first.JobID).Scan(&source, &revision, &storedDigest)).To(gomega.Succeed())
		gomega.Expect(source).To(gomega.Equal("accepted_configuration"))
		gomega.Expect(revision).To(gomega.Equal(accepted.Revision))
		gomega.Expect(storedDigest).To(gomega.Equal(documentDigest))
		doc.Revision = accepted.Revision + 1
		doc.Limits.RequestBytes = 32
		_, err = ledger.CommitEffectiveConfig(doc, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		replay, err := ledger.Admit(spec, digest, "capped-key", "ignored")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.JobID).To(gomega.Equal(first.JobID))
		_, err = ledger.Admit(spec, digest, "fresh-cap-key", "fresh-cap-job")
		gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("request_bytes")))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_jobs`)).To(gomega.Equal(1))
	})

	ginkgo.It("refuses a new admission from an ordinary ledger and admits only an explicit fixture", func() {
		t := ginkgo.GinkgoT()
		path := t.TempDir() + "/state.sqlite"
		db, err := sqlite.Open(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ledger, err := NewLedger(db, 2)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ledger.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		_, err = ledger.Admit(encode(t, validJob()), digest, "fixture-key", "fixture-job")
		gomega.Expect(errors.Is(err, ErrNotFound)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("accepted configuration"))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_jobs`)).To(gomega.BeZero())
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_config_audits`)).To(gomega.BeZero())
		gomega.Expect(db.Close()).To(gomega.Succeed())

		db, err = sqlite.Open(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ledger, err = NewLedger(db, 2)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ledger.UseLegacyFixtureBounds()
		admitted, err := ledger.Admit(encode(t, validJob()), digest, "fixture-key", "fixture-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(readBounds(db, admitted.JobID)).To(gomega.Equal(legacyBoundsRow()))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_config_audits`)).To(gomega.BeZero())
		replay, err := ledger.Admit(encode(t, validJob()), digest, "fixture-key", "other-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.JobID).To(gomega.Equal(admitted.JobID))
		gomega.Expect(replay.Admission).To(gomega.Equal(admitted.Admission))
		gomega.Expect(db.Close()).To(gomega.Succeed())

		db, err = sqlite.Open(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		ledger, err = NewLedger(db, 2)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		saved, err := ledger.Request("fixture-key")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(saved.Receipt.Admission).To(gomega.Equal(admitted.Admission))
		_, err = ledger.Admit(encode(t, validJob()), digest, "fresh-key", "fresh-job")
		gomega.Expect(errors.Is(err, ErrNotFound)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("accepted configuration"))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_jobs`)).To(gomega.Equal(1))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_config_audits`)).To(gomega.BeZero())
		gomega.Expect(readBounds(db, admitted.JobID)).To(gomega.Equal(legacyBoundsRow()))
	})

	ginkgo.It("rolls back the job when the bounds insert fails", func() {
		t := ginkgo.GinkgoT()
		db, ledger := openLedger(t, t.TempDir()+"/state.sqlite", 2)
		defer db.Close()
		gomega.Expect(ledger.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		_, err := db.Exec(`CREATE TRIGGER fail_bounds BEFORE INSERT ON managed_job_bounds BEGIN SELECT RAISE(ABORT, 'bounds failed'); END`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = ledger.Admit(encode(t, validJob()), digest, "rollback-key", "rollback-job")
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_jobs`)).To(gomega.BeZero())
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_admissions`)).To(gomega.BeZero())
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_job_bounds`)).To(gomega.BeZero())
	})

	ginkgo.It("backfills schema 11 jobs without rewriting them or inventing an audit", func() {
		t := ginkgo.GinkgoT()
		path := t.TempDir() + "/state.sqlite"
		db, ledger := openLedger(t, path, 2)
		gomega.Expect(ledger.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		admitted, err := ledger.Admit(encode(t, validJob()), digest, "legacy-bounds", "legacy-bounds-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var spec []byte
		var admittedAt, deadline int64
		var identity []byte
		var state, requestKey string
		gomega.Expect(db.QueryRow(`SELECT spec_json,admitted_at_ns,queue_deadline_ns,state,request_key FROM managed_jobs WHERE id=?`, admitted.JobID).Scan(&spec, &admittedAt, &deadline, &state, &requestKey)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT identity_json FROM managed_admissions WHERE job_id=?`, admitted.JobID).Scan(&identity)).To(gomega.Succeed())
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_config_audits`)).To(gomega.BeZero())
		_, err = db.Exec(`DROP TABLE managed_job_bounds; DELETE FROM schema_migrations WHERE version>=12; PRAGMA user_version=11`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, ledger = openLedger(t, path, 2)
		defer db.Close()
		var version int
		var schema11, schema12, schema13 string
		gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&version)).To(gomega.Succeed())
		gomega.Expect(version).To(gomega.Equal(13))
		gomega.Expect(db.QueryRow(`SELECT identity FROM schema_migrations WHERE version=11`).Scan(&schema11)).To(gomega.Succeed())
		gomega.Expect(schema11).To(gomega.Equal("effective-config-v11"))
		gomega.Expect(db.QueryRow(`SELECT identity FROM schema_migrations WHERE version=12`).Scan(&schema12)).To(gomega.Succeed())
		gomega.Expect(schema12).To(gomega.Equal("job-bounds-v12"))
		gomega.Expect(db.QueryRow(`SELECT identity FROM schema_migrations WHERE version=13`).Scan(&schema13)).To(gomega.Succeed())
		gomega.Expect(schema13).To(gomega.Equal("evidence-recovery-budget-v13"))
		var gotSpec []byte
		var gotAt, gotDeadline int64
		var gotState, gotKey, source string
		var request, logBytes, artifact, count, eventDays, resultDays int64
		var revision sql.NullInt64
		var configDigest sql.NullString
		gomega.Expect(db.QueryRow(`SELECT spec_json,admitted_at_ns,queue_deadline_ns,state,request_key FROM managed_jobs WHERE id=?`, admitted.JobID).Scan(&gotSpec, &gotAt, &gotDeadline, &gotState, &gotKey)).To(gomega.Succeed())
		gomega.Expect(gotSpec).To(gomega.Equal(spec))
		gomega.Expect(gotAt).To(gomega.Equal(admittedAt))
		gomega.Expect(gotDeadline).To(gomega.Equal(deadline))
		gomega.Expect(gotState).To(gomega.Equal(state))
		gomega.Expect(gotKey).To(gomega.Equal(requestKey))
		var gotIdentity []byte
		gomega.Expect(db.QueryRow(`SELECT identity_json FROM managed_admissions WHERE job_id=?`, admitted.JobID).Scan(&gotIdentity)).To(gomega.Succeed())
		gomega.Expect(gotIdentity).To(gomega.Equal(identity))
		gomega.Expect(db.QueryRow(`SELECT request_bytes,log_bytes,artifact_bytes,artifact_count,event_retention_days,result_retention_days,config_revision,config_digest,policy_source FROM managed_job_bounds WHERE job_id=?`, admitted.JobID).Scan(&request, &logBytes, &artifact, &count, &eventDays, &resultDays, &revision, &configDigest, &source)).To(gomega.Succeed())
		gomega.Expect(source).To(gomega.Equal("legacy_defaults"))
		gomega.Expect(revision.Valid).To(gomega.BeFalse())
		gomega.Expect(configDigest.Valid).To(gomega.BeFalse())
		gomega.Expect(request).To(gomega.Equal(config.MaxRequestBytes))
		gomega.Expect(logBytes).To(gomega.Equal(config.MaxLogBytes))
		gomega.Expect(artifact).To(gomega.Equal(config.MaxArtifactBytes))
		gomega.Expect(count).To(gomega.Equal(config.MaxArtifactCount))
		gomega.Expect(eventDays).To(gomega.Equal(int64(30)))
		gomega.Expect(resultDays).To(gomega.Equal(int64(90)))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_config_audits`)).To(gomega.BeZero())
		saved, err := ledger.Request("legacy-bounds")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(saved.Receipt.Admission).To(gomega.Equal(admitted.Admission))
	})

	ginkgo.It("fails closed when current bounds are missing or malformed and survives reopen", func() {
		t := ginkgo.GinkgoT()
		path := t.TempDir() + "/state.sqlite"
		db, ledger := openLedger(t, path, 2)
		gomega.Expect(ledger.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		admitted, err := ledger.Admit(encode(t, validJob()), digest, "bounds-key", "bounds-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`DELETE FROM managed_job_bounds WHERE job_id=?`, admitted.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		reserve := ReserveRequest{JobID: admitted.JobID, ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "bounds-reserve"}
		_, err = ledger.Reserve(reserve)
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("occupancy and protected evidence stay unchanged"))
		var state string
		var occupancy int
		gomega.Expect(db.QueryRow(`SELECT j.state,r.occupancy FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, admitted.JobID).Scan(&state, &occupancy)).To(gomega.Succeed())
		gomega.Expect(state).To(gomega.Equal("queued"))
		gomega.Expect(occupancy).To(gomega.BeZero())
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, ledger = openLedger(t, path, 2)
		defer db.Close()
		_, err = ledger.Reserve(reserve)
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(db.QueryRow(`SELECT occupancy FROM managed_resources WHERE id='local-a'`).Scan(&occupancy)).To(gomega.Succeed())
		gomega.Expect(occupancy).To(gomega.BeZero())
		gomega.Expect(ledger.RegisterResource(encode(t, func() ResourceSpec { r := validResource(); r.ID = "local-b"; return r }()))).To(gomega.Succeed())
		other := validJob()
		other.ResourceID = "local-b"
		second, err := ledger.Admit(encode(t, other), digest, "malformed-key", "malformed-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`UPDATE managed_job_bounds SET request_bytes=0 WHERE job_id=?`, second.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = ledger.Reserve(ReserveRequest{JobID: second.JobID, ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "malformed-reserve"})
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(db.QueryRow(`SELECT occupancy FROM managed_resources WHERE id='local-b'`).Scan(&occupancy)).To(gomega.Succeed())
		gomega.Expect(occupancy).To(gomega.BeZero())
	})

	ginkgo.It("refuses a store newer than schema 13 before mutation", func() {
		t := ginkgo.GinkgoT()
		path := t.TempDir() + "/state.sqlite"
		db, ledger := openLedger(t, path, 2)
		gomega.Expect(ledger.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		before := countQuery(db, `SELECT COUNT(*) FROM schema_migrations`)
		const schemaQuery = `SELECT COALESCE(group_concat(sql, ';'),'') FROM (SELECT sql FROM sqlite_schema ORDER BY type,name)`
		var schemaBefore string
		gomega.Expect(db.QueryRow(schemaQuery).Scan(&schemaBefore)).To(gomega.Succeed())
		_, err := db.Exec(`PRAGMA user_version=14`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(db.Close()).To(gomega.Succeed())
		db, err = sqlite.Open(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		_, err = NewLedger(db, 2)
		gomega.Expect(errors.Is(err, ErrUnsupported)).To(gomega.BeTrue())
		var version int
		var schemaAfter string
		gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&version)).To(gomega.Succeed())
		gomega.Expect(version).To(gomega.Equal(14))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM schema_migrations`)).To(gomega.Equal(before))
		gomega.Expect(db.QueryRow(schemaQuery).Scan(&schemaAfter)).To(gomega.Succeed())
		gomega.Expect(schemaAfter).To(gomega.Equal(schemaBefore))
	})

	ginkgo.It("keeps a corrupt policy from loosening a bound or releasing occupancy", func() {
		t := ginkgo.GinkgoT()
		db, ledger := openLedger(t, t.TempDir()+"/state.sqlite", 2)
		defer db.Close()
		gomega.Expect(ledger.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		admitted, err := ledger.Admit(encode(t, validJob()), digest, "corrupt-key", "corrupt-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		hexDigest := strings.Repeat("ab", 32)
		upperDigest := strings.Repeat("AB", 32)
		overRequest := strconv.FormatInt(config.MaxRequestBytes+1, 10)
		overLog := strconv.FormatInt(config.MaxLogBytes+1, 10)
		overArtifact := strconv.FormatInt(config.MaxArtifactBytes+1, 10)
		overCount := strconv.FormatInt(config.MaxArtifactCount+1, 10)
		underRequest := strconv.FormatInt(config.MaxRequestBytes-1, 10)
		cases := []string{
			`UPDATE managed_job_bounds SET request_bytes=` + overRequest + ` WHERE job_id=?`,
			`UPDATE managed_job_bounds SET log_bytes=` + overLog + ` WHERE job_id=?`,
			`UPDATE managed_job_bounds SET artifact_bytes=` + overArtifact + ` WHERE job_id=?`,
			`UPDATE managed_job_bounds SET artifact_count=` + overCount + ` WHERE job_id=?`,
			`UPDATE managed_job_bounds SET policy_source='accepted_configuration',config_revision=1,config_digest='` + hexDigest + `',request_bytes=` + overRequest + ` WHERE job_id=?`,
			`UPDATE managed_job_bounds SET policy_source='accepted_configuration',config_revision=1,config_digest='` + hexDigest + `',log_bytes=` + overLog + ` WHERE job_id=?`,
			`UPDATE managed_job_bounds SET policy_source='accepted_configuration',config_revision=1,config_digest='` + hexDigest + `',artifact_bytes=` + overArtifact + ` WHERE job_id=?`,
			`UPDATE managed_job_bounds SET policy_source='accepted_configuration',config_revision=1,config_digest='` + hexDigest + `',artifact_count=` + overCount + ` WHERE job_id=?`,
			`UPDATE managed_job_bounds SET policy_source='accepted_configuration',config_revision=1,config_digest='` + strings.Repeat("z", 64) + `' WHERE job_id=?`,
			`UPDATE managed_job_bounds SET policy_source='accepted_configuration',config_revision=1,config_digest='` + upperDigest + `' WHERE job_id=?`,
			`UPDATE managed_job_bounds SET request_bytes=` + underRequest + ` WHERE job_id=?`,
			`UPDATE managed_job_bounds SET config_digest='` + hexDigest + `' WHERE job_id=?`,
			`UPDATE managed_job_bounds SET policy_source='accepted_configuration' WHERE job_id=?`,
		}
		reserve := ReserveRequest{JobID: admitted.JobID, ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "corrupt-reserve"}
		for _, statement := range cases {
			_, err = db.Exec(statement, admitted.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			before := readBounds(db, admitted.JobID)
			_, err = ledger.Reserve(reserve)
			gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("occupancy and protected evidence stay unchanged"))
			gomega.Expect(readBounds(db, admitted.JobID)).To(gomega.Equal(before))
			var state string
			var occupancy int
			gomega.Expect(db.QueryRow(`SELECT j.state,r.occupancy FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, admitted.JobID).Scan(&state, &occupancy)).To(gomega.Succeed())
			gomega.Expect(state).To(gomega.Equal("queued"))
			gomega.Expect(occupancy).To(gomega.BeZero())
			_, err = db.Exec(`UPDATE managed_job_bounds SET request_bytes=?,log_bytes=?,artifact_bytes=?,artifact_count=?,event_retention_days=30,result_retention_days=90,config_revision=NULL,config_digest=NULL,policy_source='legacy_defaults' WHERE job_id=?`, config.MaxRequestBytes, config.MaxLogBytes, config.MaxArtifactBytes, config.MaxArtifactCount, admitted.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(readBounds(db, admitted.JobID)).To(gomega.Equal(legacyBoundsRow()))
		}
	})

	ginkgo.It("does not release an occupied job when its policy is corrupt", func() {
		t := ginkgo.GinkgoT()
		db, ledger := openLedger(t, t.TempDir()+"/state.sqlite", 2)
		defer db.Close()
		gomega.Expect(ledger.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		admitted, err := ledger.Admit(encode(t, validJob()), digest, "occupied-key", "occupied-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		reserve := ReserveRequest{JobID: admitted.JobID, ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "occupied-reserve"}
		first, err := ledger.Reserve(reserve)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`UPDATE managed_job_bounds SET request_bytes=? WHERE job_id=?`, config.MaxRequestBytes+1, admitted.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		replay, err := ledger.Reserve(reserve)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay).To(gomega.Equal(first))
		_, err = ledger.Reserve(ReserveRequest{JobID: admitted.JobID, ExpectedJobRevision: first.JobRevision, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "occupied-again"})
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		_, err = ledger.SettleLocal(admitted.JobID, first.JobRevision, Result{})
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		_, err = ledger.Cancel(CancelRequest{JobID: admitted.JobID, ExpectedRevision: first.JobRevision, MutationKey: "occupied-cancel"})
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(ledger.MarkInterrupted()).To(gomega.MatchError(gomega.ContainSubstring("job bounds are malformed")))
		var state string
		var occupancy int
		gomega.Expect(db.QueryRow(`SELECT j.state,r.occupancy FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, admitted.JobID).Scan(&state, &occupancy)).To(gomega.Succeed())
		gomega.Expect(state).To(gomega.Equal("preparing"))
		gomega.Expect(occupancy).To(gomega.Equal(1))
		gomega.Expect(readBounds(db, admitted.JobID).Request).To(gomega.Equal(config.MaxRequestBytes + 1))
	})

	ginkgo.It("refuses a current job when its policy row is missing and keeps occupancy and evidence", func() {
		t := ginkgo.GinkgoT()
		db, ledger := openLedger(t, t.TempDir()+"/state.sqlite", 2)
		defer db.Close()
		gomega.Expect(ledger.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		admitted, err := ledger.Admit(encode(t, validJob()), digest, "missing-bounds-key", "missing-bounds-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		reserved, err := ledger.Reserve(ReserveRequest{JobID: admitted.JobID, ExpectedJobRevision: 1, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "missing-bounds-reserve"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var state, spec, evidence string
		var occupancy int
		gomega.Expect(db.QueryRow(`SELECT j.state,j.spec_json,r.occupancy FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, admitted.JobID).Scan(&state, &spec, &occupancy)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT GROUP_CONCAT(item, '|') FROM (SELECT cursor || ':' || kind || ':' || payload_json AS item FROM managed_events WHERE job_id=? ORDER BY cursor)`, admitted.JobID).Scan(&evidence)).To(gomega.Succeed())
		gomega.Expect(evidence).NotTo(gomega.BeEmpty())
		gomega.Expect(occupancy).To(gomega.Equal(1))
		_, err = db.Exec(`DELETE FROM managed_job_bounds WHERE job_id=?`, admitted.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = ledger.Cancel(CancelRequest{JobID: admitted.JobID, ExpectedRevision: reserved.JobRevision, MutationKey: "missing-bounds-cancel"})
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("job bounds are missing"))
		var afterState, afterSpec, afterEvidence string
		var afterOccupancy int
		gomega.Expect(db.QueryRow(`SELECT j.state,j.spec_json,r.occupancy FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, admitted.JobID).Scan(&afterState, &afterSpec, &afterOccupancy)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT GROUP_CONCAT(item, '|') FROM (SELECT cursor || ':' || kind || ':' || payload_json AS item FROM managed_events WHERE job_id=? ORDER BY cursor)`, admitted.JobID).Scan(&afterEvidence)).To(gomega.Succeed())
		gomega.Expect(afterState).To(gomega.Equal(state))
		gomega.Expect(afterSpec).To(gomega.Equal(spec))
		gomega.Expect(afterOccupancy).To(gomega.Equal(occupancy))
		gomega.Expect(afterEvidence).To(gomega.Equal(evidence))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_jobs`)).To(gomega.Equal(1))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_mutations WHERE mutation_key='cancel:missing-bounds-cancel'`)).To(gomega.BeZero())
	})

	ginkgo.It("leaves a readable diagnostic in place when a later cause is refused", func() {
		t := ginkgo.GinkgoT()
		db, ledger := openLedger(t, t.TempDir()+"/state.sqlite", 2)
		defer db.Close()
		gomega.Expect(ledger.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		admitted, err := ledger.Admit(encode(t, validJob()), digest, "diagnostic-key", "diagnostic-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ledger.RecordDiagnosticCause(admitted.JobID, DiagnosticDeadlineExceeded)).To(gomega.Succeed())
		before := diagnosticPayload(db, admitted.JobID)
		gomega.Expect(before).To(gomega.ContainSubstring(DiagnosticDeadlineExceeded))
		_, err = db.Exec(`UPDATE managed_job_bounds SET request_bytes=? WHERE job_id=?`, config.MaxRequestBytes+1, admitted.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = ledger.RecordDiagnosticCause(admitted.JobID, DiagnosticIdentityConflict)
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(diagnosticPayload(db, admitted.JobID)).To(gomega.Equal(before))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_events WHERE kind='diagnostic'`)).To(gomega.Equal(1))
		var state string
		var occupancy int
		gomega.Expect(db.QueryRow(`SELECT j.state,r.occupancy FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, admitted.JobID).Scan(&state, &occupancy)).To(gomega.Succeed())
		gomega.Expect(state).To(gomega.Equal("queued"))
		gomega.Expect(occupancy).To(gomega.BeZero())
		gomega.Expect(readBounds(db, admitted.JobID).Request).To(gomega.Equal(config.MaxRequestBytes + 1))
	})

	ginkgo.It("keeps an imported runner receipt when a later kind is refused", func() {
		t := ginkgo.GinkgoT()
		db, ledger, admitted := cancellationFixture(t)
		current, id := cancellationRunning(t, ledger, admitted)
		request, err := json.Marshal(map[string]any{"schema": "haepari.runner-request/v1", "identity": id})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ledger.RecordRunnerReceipt(id, "request", request)).To(gomega.Succeed())
		sum := sha256.Sum256(request)
		binding := "sha256:" + hex.EncodeToString(sum[:])
		intent, err := json.Marshal(map[string]any{"schema": "haepari.runner-intent/v1", "identity": id, "request_digest": binding})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		corruptRequestBound(db, current.JobID)
		gomega.Expect(ledger.RecordRunnerReceipt(id, "request", request)).To(gomega.Succeed())
		err = ledger.RecordRunnerReceipt(id, "intent", intent)
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("job bounds are malformed"))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_runner_receipts`)).To(gomega.Equal(1))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_events WHERE kind='runner_receipt'`)).To(gomega.Equal(1))
		after, err := ledger.Snapshot(current.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after.JobState).To(gomega.Equal(current.JobState))
		gomega.Expect(after.JobRevision).To(gomega.Equal(current.JobRevision))
		gomega.Expect(after.ResourceOccupancy).To(gomega.Equal(current.ResourceOccupancy))
		gomega.Expect(after.ResourceOccupancy).To(gomega.Equal(1))
	})

	ginkgo.It("refuses power writes without changing progress or occupancy", func() {
		t := ginkgo.GinkgoT()
		db, ledger, admitted, intent := hostStopFixture(t)
		defer db.Close()
		before := powerProgress(db, intent.Identity.OperationGeneration)
		occupied, err := ledger.Snapshot(admitted.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		corruptRequestBound(db, admitted.JobID)
		err = ledger.RecordPowerCommandReturn(intent.Identity, "stop", true, digest)
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("job bounds are malformed"))
		gomega.Expect(powerProgress(db, intent.Identity.OperationGeneration)).To(gomega.Equal(before))
		restoreLegacyBound(db, admitted.JobID)
		gomega.Expect(ledger.RecordPowerCommandReturn(intent.Identity, "stop", true, digest)).To(gomega.Succeed())
		returned := powerProgress(db, intent.Identity.OperationGeneration)
		corruptRequestBound(db, admitted.JobID)
		err = ledger.ConfirmPower(PowerConfirmation{Identity: intent.Identity, ExpectedStage: returned.Stage, VictimPower: "stopped", TargetPower: "stopped", EvidenceDigest: digest})
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("job bounds are malformed"))
		gomega.Expect(powerProgress(db, intent.Identity.OperationGeneration)).To(gomega.Equal(returned))
		after, err := ledger.Snapshot(admitted.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after.JobState).To(gomega.Equal(occupied.JobState))
		gomega.Expect(after.JobRevision).To(gomega.Equal(occupied.JobRevision))
		gomega.Expect(after.ResourceOccupancy).To(gomega.Equal(occupied.ResourceOccupancy))
		gomega.Expect(after.ResourceOccupancy).To(gomega.Equal(1))

		startDB, startLedger, startJob, startIntent := hostStartFixture(t)
		defer startDB.Close()
		startBefore := powerProgress(startDB, startIntent.Identity.OperationGeneration)
		startOccupied, err := startLedger.Snapshot(startJob.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		corruptRequestBound(startDB, startJob.JobID)
		err = startLedger.RecordPowerRefusal(startIntent.Identity, digest)
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("job bounds are malformed"))
		gomega.Expect(powerProgress(startDB, startIntent.Identity.OperationGeneration)).To(gomega.Equal(startBefore))
		startAfter, err := startLedger.Snapshot(startJob.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(startAfter.JobState).To(gomega.Equal(startOccupied.JobState))
		gomega.Expect(startAfter.ResourceOccupancy).To(gomega.Equal(1))
	})

	ginkgo.It("checks an accepted pin against its historical audit and keeps a later revision from replacing it", func() {
		t := ginkgo.GinkgoT()
		db, ledger := openLedger(t, t.TempDir()+"/state.sqlite", 4)
		defer db.Close()
		resource := validResource()
		resource.Capacity = 4
		gomega.Expect(ledger.RegisterResource(encode(t, resource))).To(gomega.Succeed())
		legacySpec := validJob()
		legacyAdmitted, err := ledger.Admit(encode(t, legacySpec), digest, "historical-legacy", "historical-legacy-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(readBounds(db, legacyAdmitted.JobID)).To(gomega.Equal(legacyBoundsRow()))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_config_audits`)).To(gomega.BeZero())

		tight := config.Defaults()
		tight.Limits.RequestBytes = 4096
		tight.Limits.LogBytesPerStream = 8192
		tight.Limits.ArtifactBytes = 16384
		tight.Limits.ArtifactCount = 4
		tight.Limits.EventRetentionDays = 7
		tight.Limits.ResultRetentionDays = 14
		accepted, err := ledger.CommitEffectiveConfig(tight, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, documentDigest, _, err := accepted.Digests()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pin, err := ledger.Admit(encode(t, validJob()), digest, "historical-pin", "historical-pin-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		drift, err := ledger.Admit(encode(t, validJob()), digest, "historical-drift", "historical-drift-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		expectAcceptedPin(db, pin.JobID, accepted.Revision, documentDigest, tight.Limits)
		expectAcceptedPin(db, drift.JobID, accepted.Revision, documentDigest, tight.Limits)

		next := tight
		next.Revision = accepted.Revision + 1
		next.Limits.RequestBytes = 8192
		next.Limits.EventRetentionDays = 8
		advanced, err := ledger.CommitEffectiveConfig(next, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, advancedDigest, _, err := advanced.Digests()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		expectAcceptedPin(db, pin.JobID, accepted.Revision, documentDigest, tight.Limits)
		expectAcceptedPin(db, drift.JobID, accepted.Revision, documentDigest, tight.Limits)
		fresh, err := ledger.Admit(encode(t, validJob()), digest, "historical-fresh", "historical-fresh-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		expectAcceptedPin(db, fresh.JobID, advanced.Revision, advancedDigest, next.Limits)

		legacyReserved, err := ledger.Reserve(ReserveRequest{JobID: legacyAdmitted.JobID, ExpectedJobRevision: legacyAdmitted.JobRevision, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "historical-legacy-reserve"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = ledger.Reserve(ReserveRequest{JobID: pin.JobID, ExpectedJobRevision: pin.JobRevision, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "historical-pin-reserve"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		reservedDrift, err := ledger.Reserve(ReserveRequest{JobID: drift.JobID, ExpectedJobRevision: drift.JobRevision, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "historical-drift-reserve"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = ledger.Reserve(ReserveRequest{JobID: fresh.JobID, ExpectedJobRevision: fresh.JobRevision, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "historical-fresh-reserve"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		savedBounds := readBounds(db, drift.JobID)
		savedAudit := readAudit(db, accepted.Revision)
		refuse := func(name string) {
			state, spec, evidence, occupancy, events, admissions := jobEffect(db, drift.JobID)
			_, err := ledger.Cancel(CancelRequest{JobID: drift.JobID, ExpectedRevision: reservedDrift.JobRevision, MutationKey: "historical-" + name})
			gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue(), name)
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("job bounds are malformed"), name)
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("occupancy and protected evidence stay unchanged"), name)
			afterState, afterSpec, afterEvidence, afterOccupancy, afterEvents, afterAdmissions := jobEffect(db, drift.JobID)
			gomega.Expect(afterState).To(gomega.Equal(state), name)
			gomega.Expect(afterSpec).To(gomega.Equal(spec), name)
			gomega.Expect(afterEvidence).To(gomega.Equal(evidence), name)
			gomega.Expect(afterOccupancy).To(gomega.Equal(occupancy), name)
			gomega.Expect(afterEvents).To(gomega.Equal(events), name)
			gomega.Expect(afterAdmissions).To(gomega.Equal(admissions), name)
			gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_mutations WHERE mutation_key='cancel:historical-`+name+`'`)).To(gomega.BeZero(), name)
		}
		restore := func() {
			writeBounds(db, drift.JobID, savedBounds)
			writeAudit(db, accepted.Revision, savedAudit)
		}
		for _, change := range []struct {
			name  string
			query string
			value int64
		}{
			{"request", `UPDATE managed_job_bounds SET request_bytes=? WHERE job_id=?`, tight.Limits.RequestBytes + 1},
			{"log", `UPDATE managed_job_bounds SET log_bytes=? WHERE job_id=?`, tight.Limits.LogBytesPerStream + 1},
			{"artifact", `UPDATE managed_job_bounds SET artifact_bytes=? WHERE job_id=?`, tight.Limits.ArtifactBytes + 1},
			{"count", `UPDATE managed_job_bounds SET artifact_count=? WHERE job_id=?`, tight.Limits.ArtifactCount + 1},
			{"event-days", `UPDATE managed_job_bounds SET event_retention_days=? WHERE job_id=?`, tight.Limits.EventRetentionDays + 1},
			{"result-days", `UPDATE managed_job_bounds SET result_retention_days=? WHERE job_id=?`, tight.Limits.ResultRetentionDays + 1},
		} {
			_, err = db.Exec(change.query, change.value, drift.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(change.value).To(gomega.BeNumerically(">", int64(1)))
			gomega.Expect(change.value).To(gomega.BeNumerically("<=", acceptedLimitCap(change.name)))
			refuse(change.name)
			restore()
		}
		_, err = db.Exec(`UPDATE managed_job_bounds SET config_digest=? WHERE job_id=?`, strings.Repeat("ab", 32), drift.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		refuse("wrong-digest")
		restore()
		_, err = db.Exec(`UPDATE managed_job_bounds SET config_revision=99 WHERE job_id=?`, drift.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		refuse("unaccepted-revision")
		restore()
		_, err = db.Exec(`DELETE FROM managed_config_audits WHERE revision=?`, accepted.Revision)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		refuse("missing-audit")
		restore()
		_, err = db.Exec(`UPDATE managed_config_audits SET document_json=? WHERE revision=?`, []byte("{"), accepted.Revision)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		refuse("malformed-audit")
		restore()
		_, err = db.Exec(`UPDATE managed_config_audits SET document_json=? WHERE revision=?`, bytes.Replace(savedAudit.raw, []byte(`"schema":`), []byte(`"schema": `), 1), accepted.Revision)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		refuse("noncanonical-audit")
		restore()
		_, err = db.Exec(`UPDATE managed_config_audits SET document_digest=? WHERE revision=?`, strings.Repeat("cd", 32), accepted.Revision)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		refuse("audit-digest")
		restore()
		shifted := tight
		shifted.Revision = accepted.Revision + 8
		shiftedRaw, err := shifted.Canonical()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, shiftedDigest, _, err := shifted.Digests()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`UPDATE managed_config_audits SET document_json=?,document_digest=? WHERE revision=?`, shiftedRaw, shiftedDigest, accepted.Revision)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`UPDATE managed_job_bounds SET config_digest=? WHERE job_id=?`, shiftedDigest, drift.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		refuse("audit-revision")
		restore()

		legacyState, legacySpecJSON, legacyEvidence, legacyOccupancy, legacyEvents, legacyAdmissions := jobEffect(db, legacyAdmitted.JobID)
		_, err = db.Exec(`UPDATE managed_job_bounds SET policy_source='accepted_configuration',config_revision=99,config_digest=? WHERE job_id=?`, strings.Repeat("ef", 32), legacyAdmitted.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = ledger.Cancel(CancelRequest{JobID: legacyAdmitted.JobID, ExpectedRevision: legacyReserved.JobRevision, MutationKey: "historical-legacy-provenance"})
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("job bounds are malformed"))
		afterState, afterSpec, afterEvidence, afterOccupancy, afterEvents, afterAdmissions := jobEffect(db, legacyAdmitted.JobID)
		gomega.Expect(afterState).To(gomega.Equal(legacyState))
		gomega.Expect(afterSpec).To(gomega.Equal(legacySpecJSON))
		gomega.Expect(afterEvidence).To(gomega.Equal(legacyEvidence))
		gomega.Expect(afterOccupancy).To(gomega.Equal(legacyOccupancy))
		gomega.Expect(afterEvents).To(gomega.Equal(legacyEvents))
		gomega.Expect(afterAdmissions).To(gomega.Equal(legacyAdmissions))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_mutations WHERE mutation_key='cancel:historical-legacy-provenance'`)).To(gomega.BeZero())
	})
})

type storedBounds struct {
	Request, LogBytes, Artifact, Count, EventDays, ResultDays int64
	Revision                                                  sql.NullInt64
	Digest                                                    sql.NullString
	Source                                                    string
}

func expectAcceptedPin(db *sql.DB, jobID string, revision int64, digest string, limits config.Limits) {
	gomega.Expect(readBounds(db, jobID)).To(gomega.Equal(storedBounds{
		Request: limits.RequestBytes, LogBytes: limits.LogBytesPerStream,
		Artifact: limits.ArtifactBytes, Count: limits.ArtifactCount,
		EventDays: limits.EventRetentionDays, ResultDays: limits.ResultRetentionDays,
		Revision: sql.NullInt64{Int64: revision, Valid: true},
		Digest:   sql.NullString{String: digest, Valid: true},
		Source:   "accepted_configuration",
	}))
}

func acceptedLimitCap(name string) int64 {
	switch name {
	case "request":
		return config.MaxRequestBytes
	case "log":
		return config.MaxLogBytes
	case "artifact":
		return config.MaxArtifactBytes
	case "count":
		return config.MaxArtifactCount
	default:
		return 366 * 24
	}
}

func jobEffect(db *sql.DB, jobID string) (state, spec, evidence string, occupancy, events, admissions int) {
	gomega.Expect(db.QueryRow(`SELECT j.state,j.spec_json,r.occupancy FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, jobID).Scan(&state, &spec, &occupancy)).To(gomega.Succeed())
	gomega.Expect(db.QueryRow(`SELECT COALESCE(GROUP_CONCAT(item, '|'),'') FROM (SELECT cursor || ':' || kind || ':' || payload_json AS item FROM managed_events WHERE job_id=? ORDER BY cursor)`, jobID).Scan(&evidence)).To(gomega.Succeed())
	events = countQuery(db, `SELECT COUNT(*) FROM managed_events`)
	admissions = countQuery(db, `SELECT COUNT(*) FROM managed_admissions`)
	return
}

type configAuditRow struct {
	digest   string
	raw      []byte
	recorded int64
}

func readAudit(db *sql.DB, revision int64) configAuditRow {
	var row configAuditRow
	gomega.Expect(db.QueryRow(`SELECT document_digest,document_json,recorded_at_ns FROM managed_config_audits WHERE revision=?`, revision).Scan(&row.digest, &row.raw, &row.recorded)).To(gomega.Succeed())
	return row
}

func writeAudit(db *sql.DB, revision int64, row configAuditRow) {
	_, err := db.Exec(`INSERT INTO managed_config_audits(revision,document_digest,document_json,recorded_at_ns) VALUES(?,?,?,?) ON CONFLICT(revision) DO UPDATE SET document_digest=excluded.document_digest,document_json=excluded.document_json,recorded_at_ns=excluded.recorded_at_ns`, revision, row.digest, row.raw, row.recorded)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

func writeBounds(db *sql.DB, jobID string, row storedBounds) {
	var revision any
	var digest any
	if row.Revision.Valid {
		revision = row.Revision.Int64
	}
	if row.Digest.Valid {
		digest = row.Digest.String
	}
	_, err := db.Exec(`UPDATE managed_job_bounds SET request_bytes=?,log_bytes=?,artifact_bytes=?,artifact_count=?,event_retention_days=?,result_retention_days=?,config_revision=?,config_digest=?,policy_source=? WHERE job_id=?`,
		row.Request, row.LogBytes, row.Artifact, row.Count, row.EventDays, row.ResultDays, revision, digest, row.Source, jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

func legacyBoundsRow() storedBounds {
	return storedBounds{
		Request: config.MaxRequestBytes, LogBytes: config.MaxLogBytes,
		Artifact: config.MaxArtifactBytes, Count: config.MaxArtifactCount,
		EventDays: 30, ResultDays: 90, Source: "legacy_defaults",
	}
}

func readBounds(db *sql.DB, jobID string) storedBounds {
	var row storedBounds
	gomega.Expect(db.QueryRow(`SELECT request_bytes,log_bytes,artifact_bytes,artifact_count,event_retention_days,result_retention_days,config_revision,config_digest,policy_source FROM managed_job_bounds WHERE job_id=?`, jobID).Scan(
		&row.Request, &row.LogBytes, &row.Artifact, &row.Count, &row.EventDays, &row.ResultDays, &row.Revision, &row.Digest, &row.Source)).To(gomega.Succeed())
	return row
}

func diagnosticPayload(db *sql.DB, jobID string) string {
	var payload string
	gomega.Expect(db.QueryRow(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='diagnostic'`, jobID).Scan(&payload)).To(gomega.Succeed())
	return payload
}

func corruptRequestBound(db *sql.DB, jobID string) {
	_, err := db.Exec(`UPDATE managed_job_bounds SET request_bytes=? WHERE job_id=?`, config.MaxRequestBytes+1, jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

func restoreLegacyBound(db *sql.DB, jobID string) {
	_, err := db.Exec(`UPDATE managed_job_bounds SET request_bytes=?,log_bytes=?,artifact_bytes=?,artifact_count=?,event_retention_days=30,result_retention_days=90,config_revision=NULL,config_digest=NULL,policy_source='legacy_defaults' WHERE job_id=?`, config.MaxRequestBytes, config.MaxLogBytes, config.MaxArtifactBytes, config.MaxArtifactCount, jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

type powerProgressRow struct {
	Stage       string
	Observation []byte
}

func powerProgress(db *sql.DB, generation int64) powerProgressRow {
	var row powerProgressRow
	gomega.Expect(db.QueryRow(`SELECT stage,observation_json FROM managed_power_progress WHERE operation_generation=?`, generation).Scan(&row.Stage, &row.Observation)).To(gomega.Succeed())
	return row
}

func hostStopFixture(t ginkgo.GinkgoTInterface) (*sql.DB, *Ledger, Receipt, PowerIntent) {
	t.Helper()
	db, ledger := openLedger(t, filepath.Join(t.TempDir(), "power.sqlite"), 1)
	victimSpec := vmScheduling(t, ledger, "victim", 1, "started", true)
	targetSpec := vmScheduling(t, ledger, "target", 2, "started", false)
	_, err := ledger.BindInstallation("power-bounds-fixture")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	job := validJob()
	job.ResourceID = "target"
	job.Deadlines.QueueSeconds = 600
	admitted, err := ledger.Admit(encode(t, job), digest, "power-admission", "power-job")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	reserved, err := ledger.Reserve(ReserveRequest{JobID: admitted.JobID, ExpectedJobRevision: admitted.JobRevision, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "power-reserve", VMGuard: &VMReservationGuard{HostLimit: 1, ObservationRevisions: map[string]int64{"victim": 1, "target": 1}}})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	seedHistoricalPower(t, db, reserved, targetSpec, &victimSpec, PowerStopReady)
	intent, err := ledger.PowerIntent(1)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(ledger.ClaimHostPowerEffect(intent.Identity, "stop")).To(gomega.Succeed())
	return db, ledger, admitted, intent
}

func hostStartFixture(t ginkgo.GinkgoTInterface) (*sql.DB, *Ledger, Receipt, PowerIntent) {
	t.Helper()
	db, ledger := openLedger(t, filepath.Join(t.TempDir(), "power-start.sqlite"), 2)
	targetSpec := vmScheduling(t, ledger, "target", 1, "started", false)
	vmScheduling(t, ledger, "other-vm", 2, "stopped", false)
	_, err := ledger.BindInstallation("power-start-fixture")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	job := validJob()
	job.ResourceID = "target"
	job.Deadlines.QueueSeconds = 600
	admitted, err := ledger.Admit(encode(t, job), digest, "start-admission", "start-job")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	admitted, err = reserveScheduling(ledger, admitted, vmGuard(t, db, 1))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	seedHistoricalPower(t, db, admitted, targetSpec, nil, PowerStartReady)
	intent, err := ledger.PowerIntent(1)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(ledger.ClaimHostPowerEffect(intent.Identity, "start")).To(gomega.Succeed())
	return db, ledger, admitted, intent
}

var _ = ginkgo.Describe("Task023 job bounds reader", func() {
	ginkgo.It("keeps the historical job bounds pin when a later revision is accepted", func() {
		t := ginkgo.GinkgoT()
		db, err := sqlite.Open(filepath.Join(t.TempDir(), "state.sqlite"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		ledger, err := NewLedger(db, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		doc := config.Defaults()
		doc.Limits.LogBytesPerStream = 8
		doc.Limits.ArtifactBytes = 6
		doc.Limits.ArtifactCount = 1
		accepted, err := ledger.CommitEffectiveConfig(doc, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(ledger.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		admitted, err := ledger.Admit(encode(t, validJob()), digest, "pin-key", "pin-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		policy, err := ledger.ValidatedJobBounds(admitted.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(policy.LogBytes).To(gomega.Equal(int64(8)))
		gomega.Expect(policy.ArtifactBytes).To(gomega.Equal(int64(6)))
		gomega.Expect(policy.ArtifactCount).To(gomega.Equal(int64(1)))
		gomega.Expect(policy.Source).To(gomega.Equal("accepted_configuration"))
		gomega.Expect(policy.ConfigRevision.Int64).To(gomega.Equal(accepted.Revision))
		_, err = ledger.RunnerReceiptPayload(admitted.JobID, "request")
		gomega.Expect(errors.Is(err, ErrNotFound)).To(gomega.BeTrue())

		doc.Revision = accepted.Revision + 1
		doc.Limits.LogBytesPerStream = 64
		doc.Limits.ArtifactBytes = 32
		_, err = ledger.CommitEffectiveConfig(doc, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		again, err := ledger.ValidatedJobBounds(admitted.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(again).To(gomega.Equal(policy))

		fresh := validJob()
		fresh.Suite = "later"
		later, err := ledger.Admit(encode(t, fresh), digest, "later-key", "later-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		laterPolicy, err := ledger.ValidatedJobBounds(later.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(laterPolicy.LogBytes).To(gomega.Equal(int64(64)))
		var revision int64
		var occupancy int
		gomega.Expect(db.QueryRow(`SELECT j.revision,r.occupancy FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, later.JobID).Scan(&revision, &occupancy)).To(gomega.Succeed())
		_, err = db.Exec(`UPDATE managed_job_bounds SET log_bytes=log_bytes-1 WHERE job_id=?`, later.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = ledger.ValidatedJobBounds(later.JobID)
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("job bounds are malformed"))
		var afterRevision int64
		var afterOccupancy int
		gomega.Expect(db.QueryRow(`SELECT j.revision,r.occupancy FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, later.JobID).Scan(&afterRevision, &afterOccupancy)).To(gomega.Succeed())
		gomega.Expect(afterRevision).To(gomega.Equal(revision))
		gomega.Expect(afterOccupancy).To(gomega.Equal(occupancy))

		gomega.Expect(db.QueryRow(`SELECT revision FROM managed_jobs WHERE id=?`, admitted.JobID).Scan(&revision)).To(gomega.Succeed())
		_, err = db.Exec(`DELETE FROM managed_job_bounds WHERE job_id=?`, admitted.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = ledger.ValidatedJobBounds(admitted.JobID)
		gomega.Expect(errors.Is(err, ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("job bounds are missing"))
		gomega.Expect(db.QueryRow(`SELECT revision FROM managed_jobs WHERE id=?`, admitted.JobID).Scan(&afterRevision)).To(gomega.Succeed())
		gomega.Expect(afterRevision).To(gomega.Equal(revision))
	})
})

var _ = ginkgo.Describe("Task023 event bound", func() {
	ginkgo.It("rejects an oversized collect event bound through RecordLocalPhase", func() {
		t := ginkgo.GinkgoT()
		db, ledger := openLedger(t, filepath.Join(t.TempDir(), "event-bound-phase.sqlite"), 1)
		defer db.Close()
		gomega.Expect(ledger.RegisterResource(encode(t, validResource()))).To(gomega.Succeed())
		admitted, err := ledger.Admit(encode(t, validJob()), digest, "event-phase-key", "event-phase-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		reserved, err := reserveScheduling(ledger, admitted, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ready, err := ledger.AdvanceLocal(reserved.JobID, reserved.JobRevision, "preparing", "ready", "ready-"+reserved.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		started, err := ledger.StartLocal(ready.JobID, ready.JobRevision, "run-event-phase")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		id, err := ledger.LocalIdentity(started.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		collecting, err := ledger.AdvanceLocal(started.JobID, started.JobRevision, "running", "collecting", "collecting-"+started.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		now := time.Now().UTC()
		intent := LocalPhase{Schema: "haepari.phase-receipt/v1", Identity: id, Phase: "collect", BudgetSeconds: 10, StartedAt: now, Deadline: now.Add(10 * time.Second), ObservedAt: now}
		intentKey := "phase-intent-collect-" + collecting.JobID
		intentReceipt, err := ledger.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: collecting.JobRevision, Record: intent, MutationKey: intentKey})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		phasesBefore, err := ledger.LocalPhases(collecting.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var revision int64
		var state, availability string
		var occupancy int
		gomega.Expect(db.QueryRow(`SELECT j.revision,j.state,r.occupancy,r.availability FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, collecting.JobID).Scan(&revision, &state, &occupancy, &availability)).To(gomega.Succeed())
		eventsBefore := countQuery(db, `SELECT COUNT(*) FROM managed_events`)
		ownersBefore := countQuery(db, `SELECT COUNT(*) FROM managed_event_owners`)
		mutationsBefore := countQuery(db, `SELECT COUNT(*) FROM managed_mutations`)
		var intentJSON []byte
		gomega.Expect(db.QueryRow(`SELECT receipt_json FROM managed_mutations WHERE mutation_key=?`, intentKey).Scan(&intentJSON)).To(gomega.Succeed())
		observed := intent
		observed.Completed = true
		observed.Outcome = "failed"
		observed.EvidenceCompleteness = "partial"
		observed.EvidenceGaps = []EvidenceGap{{Path: "output.log", Reason: strings.Repeat("a", 2<<20), Offset: 0, DroppedBytes: 0}}
		observedKey := "phase-observed-collect-" + collecting.JobID
		_, err = ledger.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: intentReceipt.JobRevision, Record: observed, MutationKey: observedKey})
		gomega.Expect(errors.Is(err, ErrInvalid)).To(gomega.BeTrue())
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_mutations WHERE mutation_key='`+observedKey+`'`)).To(gomega.BeZero())
		var afterRevision int64
		var afterState, afterAvailability string
		var afterOccupancy int
		gomega.Expect(db.QueryRow(`SELECT j.revision,j.state,r.occupancy,r.availability FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, collecting.JobID).Scan(&afterRevision, &afterState, &afterOccupancy, &afterAvailability)).To(gomega.Succeed())
		gomega.Expect(afterRevision).To(gomega.Equal(revision))
		gomega.Expect(afterState).To(gomega.Equal(state))
		gomega.Expect(afterOccupancy).To(gomega.Equal(occupancy))
		gomega.Expect(afterAvailability).To(gomega.Equal(availability))
		phases, err := ledger.LocalPhases(collecting.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases).To(gomega.Equal(phasesBefore))
		gomega.Expect(phases["collect"].Completed).To(gomega.BeFalse())
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_events`)).To(gomega.Equal(eventsBefore))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_event_owners`)).To(gomega.Equal(ownersBefore))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_mutations`)).To(gomega.Equal(mutationsBefore))
		var intentAfter []byte
		gomega.Expect(db.QueryRow(`SELECT receipt_json FROM managed_mutations WHERE mutation_key=?`, intentKey).Scan(&intentAfter)).To(gomega.Succeed())
		gomega.Expect(intentAfter).To(gomega.Equal(intentJSON))
		replayed, err := ledger.RecordLocalPhase(LocalPhaseRequest{ExpectedRevision: collecting.JobRevision, Record: intent, MutationKey: intentKey})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replayed).To(gomega.Equal(intentReceipt))
		gomega.Expect(db.QueryRow(`SELECT j.revision,j.state,r.occupancy FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, collecting.JobID).Scan(&afterRevision, &afterState, &afterOccupancy)).To(gomega.Succeed())
		gomega.Expect(afterRevision).To(gomega.Equal(revision))
		gomega.Expect(afterState).To(gomega.Equal(state))
		gomega.Expect(afterOccupancy).To(gomega.Equal(occupancy))
	})
})
