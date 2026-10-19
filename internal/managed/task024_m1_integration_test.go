//go:build integration

package managed

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var task024MigrationIdentity = map[int]string{
	2:  "managed-observation-v2",
	3:  "sealed-job-input-v3",
	4:  "durable-admissions-v4",
	5:  "managed-scheduling-v5",
	6:  "durable-client-v6",
	7:  "advisory-lock-v7",
	8:  "independent-runner-receipts-v8",
	9:  "evidence-retention-v9",
	10: "durable-manual-sessions-v10",
	11: "effective-config-v11",
	12: "job-bounds-v12",
	13: "evidence-recovery-budget-v13",
}

// task024Historical strips a current fixture down to synthetic user_version at.
// Versions 1 and 2 use legacyCoreLayout. Its schema_migrations identity 1 is
// synthetic historical fixture authority from that helper, not a production
// new-init receipt and not a public predecessor. Later versions use the same
// drop statements only for objects newer than at, so admission, run, input,
// scheduling, and idle_since_ns remain only where that version has them.
// managed_installation is absent below schema 6; this helper does not invent
// that row or copy boundID into a pre-6 store.
func task024Historical(t ginkgo.GinkgoTInterface, db *sql.DB, at int, boundID string) {
	t.Helper()
	gomega.Expect(at).To(gomega.BeNumerically(">=", 1))
	gomega.Expect(at).To(gomega.BeNumerically("<=", 11))
	gomega.Expect(boundID).NotTo(gomega.BeEmpty())
	if at <= 2 {
		legacyCoreLayout(t, db, at)
	} else {
		_, err := db.Exec(`PRAGMA foreign_keys=OFF`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var statements []string
		if at < 12 {
			statements = append(statements, `DROP TABLE managed_job_bounds`)
		}
		if at < 11 {
			statements = append(statements, `DROP TABLE managed_effective_config`, `DROP TABLE managed_config_audits`)
		}
		if at < 10 {
			statements = append(statements, `DROP TABLE managed_manual_sessions`)
		}
		if at < 9 {
			statements = append(statements, `DROP TABLE managed_purges`, `DROP TABLE managed_evidence_recovery`)
		}
		if at < 8 {
			statements = append(statements, `DROP TABLE managed_runner_receipts`)
		}
		if at < 7 {
			statements = append(statements,
				`DROP INDEX IF EXISTS managed_lock_active_slot`,
				`DROP INDEX IF EXISTS managed_lock_fifo`,
				`DROP TABLE managed_lock_discontinuities`,
				`DROP TABLE managed_lock_clock`,
				`DROP TABLE managed_lock_slots`,
				`DROP TABLE managed_lock_requests`)
		}
		if at < 6 {
			statements = append(statements,
				`DROP TABLE managed_cancellations`,
				`DROP TABLE managed_cancel_edges`,
				`DROP TABLE managed_local_outcomes`,
				`DROP TABLE managed_installation`)
		}
		if at < 5 {
			statements = append(statements,
				`DROP TABLE managed_parent_context`,
				`DROP TABLE managed_parent_nonces`,
				`DROP TABLE managed_queue_blockers`,
				`DROP TABLE managed_vm_observations`,
				`DROP TABLE managed_power_progress`,
				`DROP TABLE managed_availability_receipts`,
				`ALTER TABLE managed_resources DROP COLUMN idle_since_ns`)
		}
		if at < 4 {
			statements = append(statements, `DROP TABLE managed_admissions`)
		}
		if at < 3 {
			statements = append(statements, `DROP TABLE managed_job_inputs`, `DROP TABLE managed_runs`)
		}
		statements = append(statements,
			fmt.Sprintf(`DELETE FROM schema_migrations WHERE version>%d`, at),
			fmt.Sprintf(`PRAGMA user_version=%d`, at))
		for _, statement := range statements {
			_, err = db.Exec(statement)
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), statement)
		}
		_, err = db.Exec(`PRAGMA foreign_keys=ON`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var foreignKeys int
		gomega.Expect(db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys)).To(gomega.Succeed())
		gomega.Expect(foreignKeys).To(gomega.Equal(1))
	}
	if at <= 2 {
		var identity string
		gomega.Expect(db.QueryRow(`SELECT identity FROM schema_migrations WHERE version=1`).Scan(&identity)).To(gomega.Succeed())
		gomega.Expect(identity).To(gomega.Equal("managed-initial-v1"))
	} else {
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM schema_migrations WHERE version=1`)).To(gomega.BeZero())
	}
	if at < 3 {
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='managed_runs'`)).To(gomega.BeZero())
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='managed_job_inputs'`)).To(gomega.BeZero())
	}
	if at < 4 {
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='managed_admissions'`)).To(gomega.BeZero())
	}
	if at < 5 {
		for _, name := range []string{"managed_parent_context", "managed_parent_nonces", "managed_queue_blockers", "managed_vm_observations", "managed_power_progress", "managed_availability_receipts"} {
			gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='`+name+`'`)).To(gomega.BeZero(), name)
		}
		gomega.Expect(task024Column(db, "managed_resources", "idle_since_ns")).To(gomega.BeFalse())
	}
	if at < 6 {
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='managed_installation'`)).To(gomega.BeZero())
	} else {
		var id string
		gomega.Expect(db.QueryRow(`SELECT id FROM managed_installation WHERE singleton=1`).Scan(&id)).To(gomega.Succeed())
		gomega.Expect(id).To(gomega.Equal(boundID))
	}
}

func task024Column(db *sql.DB, table, column string) bool {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt sql.NullString
		gomega.Expect(rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk)).To(gomega.Succeed())
		if name == column {
			return true
		}
	}
	gomega.Expect(rows.Err()).NotTo(gomega.HaveOccurred())
	return false
}

type task024Snap struct {
	Version      int
	Schema       string
	Migrations   string
	Job          string
	Occupancy    int
	Lease        int64
	Mutation     string
	Admission    string
	Installation string
	Events       string
}

func task024Print(db *sql.DB, boundID string) task024Snap {
	var snap task024Snap
	gomega.Expect(boundID).NotTo(gomega.BeEmpty())
	gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&snap.Version)).To(gomega.Succeed())
	gomega.Expect(db.QueryRow(`SELECT COALESCE(group_concat(sql, char(10)), '') FROM (SELECT sql FROM sqlite_schema WHERE sql IS NOT NULL ORDER BY type, name)`).Scan(&snap.Schema)).To(gomega.Succeed())
	gomega.Expect(db.QueryRow(`SELECT COALESCE(group_concat(version || ':' || identity, ','), '') FROM (SELECT version, identity FROM schema_migrations ORDER BY version)`).Scan(&snap.Migrations)).To(gomega.Succeed())
	gomega.Expect(db.QueryRow(`SELECT id || '|' || state || '|' || CAST(revision AS TEXT) || '|' || CAST(COALESCE(lease_generation, -1) AS TEXT) FROM managed_jobs`).Scan(&snap.Job)).To(gomega.Succeed())
	gomega.Expect(db.QueryRow(`SELECT occupancy, lease_generation FROM managed_resources WHERE id='local-a'`).Scan(&snap.Occupancy, &snap.Lease)).To(gomega.Succeed())
	gomega.Expect(db.QueryRow(`SELECT hex(receipt_json) FROM managed_mutations WHERE mutation_key='task024-reserve'`).Scan(&snap.Mutation)).To(gomega.Succeed())
	gomega.Expect(db.QueryRow(`SELECT COALESCE(group_concat(cursor || ':' || kind || ':' || hex(payload_json), ','), '') FROM (SELECT cursor, kind, payload_json FROM managed_events ORDER BY cursor)`).Scan(&snap.Events)).To(gomega.Succeed())
	if snap.Version >= 4 {
		gomega.Expect(db.QueryRow(`SELECT hex(identity_json) FROM managed_admissions WHERE job_id='task024-job'`).Scan(&snap.Admission)).To(gomega.Succeed())
	}
	if snap.Version >= 6 {
		gomega.Expect(db.QueryRow(`SELECT id FROM managed_installation WHERE singleton=1`).Scan(&snap.Installation)).To(gomega.Succeed())
	} else {
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='managed_installation'`)).To(gomega.BeZero())
		snap.Installation = "absent:" + boundID
	}
	return snap
}

func task024Quote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "''") + "'"
}

func task024ReadOnly(path string) *sql.DB {
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	db.SetMaxOpenConns(1)
	gomega.Expect(db.Ping()).To(gomega.Succeed())
	_, err = db.Exec(`PRAGMA busy_timeout=5000`)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return db
}

func task024FileID(path string) (dev, ino uint64, mode os.FileMode) {
	info, err := os.Lstat(path)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	stat, ok := info.Sys().(*syscall.Stat_t)
	gomega.Expect(ok).To(gomega.BeTrue())
	gomega.Expect(info.Mode() & os.ModeSymlink).To(gomega.BeZero())
	return uint64(stat.Dev), uint64(stat.Ino), info.Mode()
}

func task024SHA256(path string) string {
	file, err := os.Open(path)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer file.Close()
	sum := sha256.New()
	_, err = io.Copy(sum, file)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return hex.EncodeToString(sum.Sum(nil))
}

// task024Vacuum snapshots the sole open connection with VACUUM INTO while the
// caller holds the controller lock. A missing VACUUM INTO fails the test.
// There is no raw-copy fallback. The snapshot is closed, mode 0600, and fsynced.
func task024Vacuum(db *sql.DB, dest string) {
	_, err := db.Exec(`VACUUM INTO ` + task024Quote(dest))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(os.Chmod(dest, 0600)).To(gomega.Succeed())
	file, err := os.Open(dest)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(file.Sync()).To(gomega.Succeed())
	gomega.Expect(file.Close()).To(gomega.Succeed())
	info, err := os.Lstat(dest)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(info.Mode().IsRegular()).To(gomega.BeTrue())
	gomega.Expect(info.Mode().Perm()).To(gomega.Equal(os.FileMode(0600)))
}

type task024Owned struct {
	rootDev, rootIno     uint64
	dbDev, dbIno         uint64
	lockDev, lockIno     uint64
	backupDev, backupIno uint64
}

func task024Restore(root state.Root, backup string, owned task024Owned) {
	gomega.Expect(root.DBPath).To(gomega.Equal(filepath.Join(root.Path, "state.sqlite")))
	gomega.Expect(backup).NotTo(gomega.Equal(root.DBPath))
	rootDev, rootIno, rootMode := task024FileID(root.Path)
	dbDev, dbIno, dbMode := task024FileID(root.DBPath)
	lockDev, lockIno, lockMode := task024FileID(filepath.Join(root.Path, "controller.lock"))
	backupDev, backupIno, backupMode := task024FileID(backup)
	gomega.Expect(rootMode.IsDir()).To(gomega.BeTrue())
	gomega.Expect(dbMode.IsRegular()).To(gomega.BeTrue())
	gomega.Expect(lockMode.IsRegular()).To(gomega.BeTrue())
	gomega.Expect(backupMode.IsRegular()).To(gomega.BeTrue())
	gomega.Expect(rootDev).To(gomega.Equal(owned.rootDev))
	gomega.Expect(rootIno).To(gomega.Equal(owned.rootIno))
	gomega.Expect(dbDev).To(gomega.Equal(owned.dbDev))
	gomega.Expect(dbIno).To(gomega.Equal(owned.dbIno))
	gomega.Expect(lockDev).To(gomega.Equal(owned.lockDev))
	gomega.Expect(lockIno).To(gomega.Equal(owned.lockIno))
	gomega.Expect(backupDev).To(gomega.Equal(owned.backupDev))
	gomega.Expect(backupIno).To(gomega.Equal(owned.backupIno))
	dbPath := root.DBPath
	for _, suffix := range []string{"-wal", "-shm"} {
		side := dbPath + suffix
		info, err := os.Lstat(side)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || filepath.Base(side) != filepath.Base(dbPath)+suffix {
			gomega.Expect(fmt.Errorf("uncertain sidecar %s", side)).NotTo(gomega.HaveOccurred())
		}
		gomega.Expect(os.Remove(side)).To(gomega.Succeed())
	}
	temp := dbPath + ".restore"
	in, err := os.Open(backup)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	out, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = io.Copy(out, in)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(out.Sync()).To(gomega.Succeed())
	gomega.Expect(out.Close()).To(gomega.Succeed())
	gomega.Expect(in.Close()).To(gomega.Succeed())
	gomega.Expect(os.Rename(temp, dbPath)).To(gomega.Succeed())
}

type task024Fixture struct {
	root             state.Root
	rootDev, rootIno uint64
	lock             *os.File
	db               *sql.DB
	reserve          ReserveRequest
	reserved         Receipt
	installationID   string
}

func task024Open(t ginkgo.GinkgoTInterface) *task024Fixture {
	t.Helper()
	parent, err := os.MkdirTemp("/tmp", "hp-t024m-")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	info, err := os.Lstat(parent)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	stat, ok := info.Sys().(*syscall.Stat_t)
	gomega.Expect(ok).To(gomega.BeTrue())
	dev, ino := uint64(stat.Dev), uint64(stat.Ino)
	fixture := &task024Fixture{}
	t.Cleanup(func() {
		if fixture.db != nil {
			if err := fixture.db.Close(); err != nil {
				ginkgo.Fail("database close failed; retaining task024 fixture " + parent + ": " + err.Error())
				return
			}
			fixture.db = nil
		}
		if fixture.lock != nil {
			_ = fixture.lock.Close()
			fixture.lock = nil
		}
		if ginkgo.CurrentSpecReport().Failed() {
			return
		}
		current, err := os.Lstat(parent)
		if err != nil {
			ginkgo.Fail("refuse to remove uncertain task024 fixture: " + err.Error())
			return
		}
		currentStat, ok := current.Sys().(*syscall.Stat_t)
		if !ok || !current.IsDir() || uint64(currentStat.Dev) != dev || uint64(currentStat.Ino) != ino {
			ginkgo.Fail("refuse to remove uncertain task024 fixture")
			return
		}
		if err := os.RemoveAll(parent); err != nil {
			ginkgo.Fail("remove task024 fixture: " + err.Error())
		}
	})
	fixture.root, err = state.Resolve(filepath.Join(parent, "state"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(fixture.root.Prepare()).To(gomega.Succeed())
	fixture.rootDev, fixture.rootIno, _ = task024FileID(fixture.root.Path)
	fixture.lock, err = fixture.root.AcquireController()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	fixture.db, err = sqlite.Open(fixture.root.DBPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ledger, err := NewLedger(fixture.db, 1)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ledger.UseLegacyFixtureBounds()
	resource := validResource()
	resource.Capacity = 1
	gomega.Expect(ledger.RegisterResource(encode(t, resource))).To(gomega.Succeed())
	admitted, err := ledger.Admit(encode(t, validJob()), digest, "task024-admit", "task024-job")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	fixture.reserve = ReserveRequest{
		JobID: admitted.JobID, ExpectedJobRevision: admitted.JobRevision,
		ExpectedConfigRevision: admitted.ResourceConfigRevision, ExpectedAvailabilityRevision: admitted.ResourceAvailabilityRevision,
		MutationKey: "task024-reserve",
	}
	fixture.reserved, err = ledger.Reserve(fixture.reserve)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(fixture.reserved.LeaseGeneration).To(gomega.BeNumerically(">", 0))
	gomega.Expect(fixture.reserved.ResourceOccupancy).To(gomega.BeNumerically(">", 0))
	fixture.installationID, err = ledger.BindInstallation("task024-install")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(fixture.installationID).To(gomega.Equal("task024-install"))
	return fixture
}

func task024QuiescedBackup(db *sql.DB, dbPath, dest string) {
	_, err := db.Exec(`PRAGMA wal_autocheckpoint=0`)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = db.Exec(`UPDATE managed_resources SET availability_reason=availability_reason WHERE id='local-a'`)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var journal string
	gomega.Expect(db.QueryRow(`PRAGMA journal_mode`).Scan(&journal)).To(gomega.Succeed())
	gomega.Expect(strings.ToLower(journal)).To(gomega.Equal("wal"))
	walInfo, err := os.Lstat(dbPath + "-wal")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(walInfo.Mode().IsRegular()).To(gomega.BeTrue())
	gomega.Expect(walInfo.Size()).To(gomega.BeNumerically(">", 0))
	task024Vacuum(db, dest)
	_, err = os.Lstat(dest + "-wal")
	gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
}

func task024ExpectMigrated(db *sql.DB, before task024Snap, boundID string, replay ReserveRequest, original Receipt) {
	gomega.Expect(before.Occupancy).To(gomega.BeNumerically(">", 0))
	gomega.Expect(before.Lease).To(gomega.BeNumerically(">", 0))
	ledger, err := NewLedger(db, 1)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ledger.UseLegacyFixtureBounds()
	var version int
	gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&version)).To(gomega.Succeed())
	gomega.Expect(version).To(gomega.Equal(13))
	for number, identity := range task024MigrationIdentity {
		var got string
		gomega.Expect(db.QueryRow(`SELECT identity FROM schema_migrations WHERE version=?`, number).Scan(&got)).To(gomega.Succeed())
		gomega.Expect(got).To(gomega.Equal(identity))
	}
	if before.Version <= 2 {
		var synthetic string
		gomega.Expect(db.QueryRow(`SELECT identity FROM schema_migrations WHERE version=1`).Scan(&synthetic)).To(gomega.Succeed())
		gomega.Expect(synthetic).To(gomega.Equal("managed-initial-v1"))
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM schema_migrations`)).To(gomega.Equal(13))
	} else {
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM schema_migrations WHERE version=1`)).To(gomega.BeZero())
		gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM schema_migrations`)).To(gomega.Equal(12))
	}
	restored := task024Print(db, boundID)
	gomega.Expect(restored.Job).To(gomega.Equal(before.Job))
	gomega.Expect(restored.Occupancy).To(gomega.Equal(before.Occupancy))
	gomega.Expect(restored.Lease).To(gomega.Equal(before.Lease))
	gomega.Expect(restored.Mutation).To(gomega.Equal(before.Mutation))
	gomega.Expect(restored.Events).To(gomega.Equal(before.Events))
	if before.Version >= 4 {
		gomega.Expect(restored.Admission).To(gomega.Equal(before.Admission))
	}
	if before.Version >= 6 {
		gomega.Expect(restored.Installation).To(gomega.Equal(boundID))
	} else {
		gomega.Expect(restored.Installation).To(gomega.BeEmpty())
		gomega.Expect(restored.Installation).NotTo(gomega.Equal(boundID))
	}
	replayed, err := ledger.Reserve(replay)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(replayed).To(gomega.Equal(original))
	gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_mutations WHERE mutation_key='task024-reserve'`)).To(gomega.Equal(1))
	again, err := NewLedger(db, 1)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	again.UseLegacyFixtureBounds()
	gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&version)).To(gomega.Succeed())
	gomega.Expect(version).To(gomega.Equal(13))
	idempotent := task024Print(db, boundID)
	gomega.Expect(idempotent.Migrations).To(gomega.Equal(restored.Migrations))
	gomega.Expect(idempotent.Job).To(gomega.Equal(restored.Job))
	gomega.Expect(idempotent.Mutation).To(gomega.Equal(restored.Mutation))
	gomega.Expect(idempotent.Events).To(gomega.Equal(restored.Events))
	gomega.Expect(idempotent.Occupancy).To(gomega.Equal(restored.Occupancy))
	gomega.Expect(idempotent.Lease).To(gomega.Equal(restored.Lease))
	gomega.Expect(idempotent.Installation).To(gomega.Equal(restored.Installation))
	if before.Version >= 4 {
		gomega.Expect(idempotent.Admission).To(gomega.Equal(restored.Admission))
	}
	second, err := again.Reserve(replay)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(second).To(gomega.Equal(original))
	gomega.Expect(countQuery(db, `SELECT COUNT(*) FROM managed_mutations WHERE mutation_key='task024-reserve'`)).To(gomega.Equal(1))
}

// Retained-event allowlist equality is reviewed for the task handoff.
// This suite asserts stored data, rollback, and replay behavior only.
var _ = ginkgo.Describe("Task024M1 storage", ginkgo.Label("integration"), func() {
	ginkgo.It("creates schema 13 under the lifetime exclusive lock", func() {
		t := ginkgo.GinkgoT()
		fixture := task024Open(t)
		var version int
		gomega.Expect(fixture.db.QueryRow(`PRAGMA user_version`).Scan(&version)).To(gomega.Succeed())
		gomega.Expect(version).To(gomega.Equal(13))
		gomega.Expect(countQuery(fixture.db, `SELECT COUNT(*) FROM schema_migrations WHERE version=1`)).To(gomega.BeZero())
		gomega.Expect(countQuery(fixture.db, `SELECT COUNT(*) FROM schema_migrations`)).To(gomega.Equal(12))
		for number, identity := range task024MigrationIdentity {
			var got string
			gomega.Expect(fixture.db.QueryRow(`SELECT identity FROM schema_migrations WHERE version=?`, number).Scan(&got)).To(gomega.Succeed())
			gomega.Expect(got).To(gomega.Equal(identity))
		}
		_, err := fixture.root.AcquireController()
		gomega.Expect(errors.Is(err, state.ErrControllerOwned)).To(gomega.BeTrue())
	})

	ginkgo.It("refuses a store newer than schema 13 before mutation", func() {
		t := ginkgo.GinkgoT()
		fixture := task024Open(t)
		before := task024Print(fixture.db, fixture.installationID)
		_, err := fixture.db.Exec(`PRAGMA user_version=14`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fixture.db.Close()).To(gomega.Succeed())
		fixture.db, err = sqlite.Open(fixture.root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = NewLedger(fixture.db, 1)
		gomega.Expect(errors.Is(err, ErrUnsupported)).To(gomega.BeTrue())
		var version int
		var schema, migrations string
		gomega.Expect(fixture.db.QueryRow(`PRAGMA user_version`).Scan(&version)).To(gomega.Succeed())
		gomega.Expect(version).To(gomega.Equal(14))
		gomega.Expect(fixture.db.QueryRow(`SELECT COALESCE(group_concat(sql, char(10)), '') FROM (SELECT sql FROM sqlite_schema WHERE sql IS NOT NULL ORDER BY type, name)`).Scan(&schema)).To(gomega.Succeed())
		gomega.Expect(schema).To(gomega.Equal(before.Schema))
		gomega.Expect(fixture.db.QueryRow(`SELECT COALESCE(group_concat(version || ':' || identity, ','), '') FROM (SELECT version, identity FROM schema_migrations ORDER BY version)`).Scan(&migrations)).To(gomega.Succeed())
		gomega.Expect(migrations).To(gomega.Equal(before.Migrations))
		gomega.Expect(countQuery(fixture.db, `SELECT COUNT(*) FROM schema_migrations WHERE version=1`)).To(gomega.BeZero())
	})

	for boundary := 2; boundary <= 12; boundary++ {
		boundary := boundary
		ginkgo.It(fmt.Sprintf("backs up, rolls back, restores, and reruns migration boundary %d", boundary), func() {
			t := ginkgo.GinkgoT()
			fixture := task024Open(t)
			at := boundary - 1
			task024Historical(t, fixture.db, at, fixture.installationID)
			backup := filepath.Join(filepath.Dir(fixture.root.Path), "backup.sqlite")
			task024QuiescedBackup(fixture.db, fixture.root.DBPath, backup)
			before := task024Print(fixture.db, fixture.installationID)
			gomega.Expect(before.Version).To(gomega.Equal(at))
			if at < 6 {
				gomega.Expect(before.Installation).To(gomega.Equal("absent:" + fixture.installationID))
			} else {
				gomega.Expect(before.Installation).To(gomega.Equal(fixture.installationID))
			}
			snap := task024ReadOnly(backup)
			gomega.Expect(task024Print(snap, fixture.installationID)).To(gomega.Equal(before))
			gomega.Expect(snap.Close()).To(gomega.Succeed())
			backupDev, backupIno, _ := task024FileID(backup)
			backupHash := task024SHA256(backup)
			dbDev, dbIno, _ := task024FileID(fixture.root.DBPath)
			lockDev, lockIno, _ := task024FileID(fixture.lock.Name())
			_, err := fixture.db.Exec(fmt.Sprintf(`CREATE TRIGGER task024_boundary BEFORE INSERT ON schema_migrations WHEN NEW.version=%d BEGIN SELECT RAISE(ABORT, 'task024 boundary %d'); END`, boundary, boundary))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			poisoned := task024Print(fixture.db, fixture.installationID)
			_, err = NewLedger(fixture.db, 1)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring(fmt.Sprintf("task024 boundary %d", boundary)))
			gomega.Expect(task024Print(fixture.db, fixture.installationID)).To(gomega.Equal(poisoned))
			gomega.Expect(fixture.db.Close()).To(gomega.Succeed())
			fixture.db = nil
			task024Restore(fixture.root, backup, task024Owned{
				rootDev: fixture.rootDev, rootIno: fixture.rootIno,
				dbDev: dbDev, dbIno: dbIno,
				lockDev: lockDev, lockIno: lockIno,
				backupDev: backupDev, backupIno: backupIno,
			})
			gomega.Expect(task024SHA256(backup)).To(gomega.Equal(backupHash))
			stillDev, stillIno, _ := task024FileID(backup)
			gomega.Expect(stillDev).To(gomega.Equal(backupDev))
			gomega.Expect(stillIno).To(gomega.Equal(backupIno))
			fixture.db, err = sqlite.Open(fixture.root.DBPath)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			task024ExpectMigrated(fixture.db, before, fixture.installationID, fixture.reserve, fixture.reserved)
		})
	}
})
