//go:build integration

package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

type task024Daemon struct {
	cmd      *exec.Cmd
	log      *os.File
	done     chan error
	identity platform.ProcessIdentity
	parent   int
	observed bool
	reaped   bool
	code     int
}

func task024DaemonEnv(home string) []string {
	out := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "HAEPARI_HOME=") || strings.HasPrefix(entry, "HAEPARI_TEST_DAEMON=") {
			continue
		}
		out = append(out, entry)
	}
	return append(out, "HAEPARI_TEST_DAEMON=1", "HAEPARI_HOME="+home)
}

func task024StartDaemon(home, logPath string) *task024Daemon {
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	cmd := exec.Command(os.Args[0])
	cmd.Env = task024DaemonEnv(home)
	cmd.Stdout = log
	cmd.Stderr = log
	gomega.Expect(cmd.Start()).To(gomega.Succeed())
	daemon := &task024Daemon{cmd: cmd, log: log, done: make(chan error, 1)}
	go func() { daemon.done <- cmd.Wait() }()
	return daemon
}

func (d *task024Daemon) output() string {
	if d.log != nil {
		_ = d.log.Sync()
	}
	data, err := os.ReadFile(d.log.Name())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return string(data)
}

func (d *task024Daemon) waitExit(timeout time.Duration) int {
	if d.reaped {
		return d.code
	}
	select {
	case err := <-d.done:
		d.reaped = true
		if err == nil {
			d.code = 0
			return 0
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			d.code = exitErr.ExitCode()
			return d.code
		}
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	case <-time.After(timeout):
		ginkgo.Fail(fmt.Sprintf("daemon did not exit\n%s", d.output()))
	}
	return d.code
}

func (d *task024Daemon) capture() {
	observed, err := platform.ObserveProcess(d.cmd.Process.Pid)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(observed.Zombie).To(gomega.BeFalse())
	d.identity = observed.Identity
	d.parent = observed.ParentPID
	d.observed = true
}

func (d *task024Daemon) stopOwned() {
	if d.reaped || d.cmd == nil || d.cmd.Process == nil {
		return
	}
	observed, err := platform.ObserveProcess(d.cmd.Process.Pid)
	if errors.Is(err, unix.ESRCH) {
		d.waitExit(2 * time.Second)
		return
	}
	if err != nil || !d.observed || observed.Identity != d.identity || observed.ParentPID != d.parent {
		ginkgo.Fail(fmt.Sprintf("refuse to signal uncertain daemon pid %d: %v", d.cmd.Process.Pid, err))
		return
	}
	gomega.Expect(d.cmd.Process.Signal(syscall.SIGHUP)).To(gomega.Succeed())
	d.waitExit(10 * time.Second)
}

func (d *task024Daemon) waitSocket(path string) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(path); err == nil {
			d.capture()
			return
		}
		select {
		case err := <-d.done:
			d.reaped = true
			ginkgo.Fail(fmt.Sprintf("daemon exited before socket: %v\n%s", err, d.output()))
		case <-time.After(10 * time.Millisecond):
		}
	}
	ginkgo.Fail("daemon socket did not appear\n" + d.output())
}

type task024Canary struct {
	identity                platform.ProcessIdentity
	parent                  int
	installation            string
	rootDev, rootIno        uint64
	dbDev, dbIno            uint64
	lockDev, lockIno        uint64
	socketDev, socketIno    uint64
	version                 int
	migrations, schema, job string
	occupancy               int
	jobLease                int64
	lease                   int64
}

func task024FileID(path string) (uint64, uint64, os.FileMode) {
	info, err := os.Lstat(path)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	stat, ok := info.Sys().(*syscall.Stat_t)
	gomega.Expect(ok).To(gomega.BeTrue())
	gomega.Expect(info.Mode() & os.ModeSymlink).To(gomega.BeZero())
	return uint64(stat.Dev), uint64(stat.Ino), info.Mode()
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

func task024Text(db *sql.DB, query string, args ...any) string {
	var value string
	gomega.Expect(db.QueryRow(query, args...).Scan(&value)).To(gomega.Succeed())
	return value
}

func task024ObserveCanary(root state.Root, daemon *task024Daemon) task024Canary {
	db := task024ReadOnly(root.DBPath)
	defer db.Close()
	view := task024Canary{identity: daemon.identity, parent: daemon.parent}
	gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&view.version)).To(gomega.Succeed())
	view.migrations = task024Text(db, `SELECT COALESCE(group_concat(version || ':' || identity, ','), '') FROM (SELECT version, identity FROM schema_migrations ORDER BY version)`)
	view.schema = task024Text(db, `SELECT COALESCE(group_concat(sql, char(10)), '') FROM (SELECT sql FROM sqlite_schema WHERE sql IS NOT NULL ORDER BY type, name)`)
	gomega.Expect(db.QueryRow(`SELECT id FROM managed_installation WHERE singleton=1`).Scan(&view.installation)).To(gomega.Succeed())
	gomega.Expect(view.installation).NotTo(gomega.BeEmpty())
	var jobLease int64
	err := db.QueryRow(`SELECT id || '|' || state, lease_generation FROM managed_jobs`).Scan(&view.job, &jobLease)
	if err != nil {
		return view
	}
	if err = db.QueryRow(`SELECT occupancy, lease_generation FROM managed_resources WHERE id='local-canary'`).Scan(&view.occupancy, &view.lease); err != nil {
		return view
	}
	view.jobLease = jobLease
	view.rootDev, view.rootIno, _ = task024FileID(root.Path)
	view.dbDev, view.dbIno, _ = task024FileID(root.DBPath)
	view.lockDev, view.lockIno, _ = task024FileID(filepath.Join(root.Path, "controller.lock"))
	view.socketDev, view.socketIno, _ = task024FileID(root.SocketPath)
	return view
}

func task024ExpectCanary(root state.Root, daemon *task024Daemon, saved task024Canary) {
	current, err := platform.ObserveProcess(daemon.cmd.Process.Pid)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(current.Identity).To(gomega.Equal(saved.identity))
	gomega.Expect(current.ParentPID).To(gomega.Equal(saved.parent))
	gomega.Expect(current.Zombie).To(gomega.BeFalse())
	gomega.Expect(task024ObserveCanary(root, daemon)).To(gomega.Equal(saved))
	out, diagnostic, code := cliOut("resource", "inspect", "local-canary", "--json")
	gomega.Expect(code).To(gomega.BeZero(), diagnostic.String())
	var body struct {
		Data managed.ResourceStatus `json:"data"`
	}
	gomega.Expect(json.Unmarshal(out.Bytes(), &body)).To(gomega.Succeed())
	gomega.Expect(body.Data.Occupancy).To(gomega.BeNumerically(">", 0))
	gomega.Expect(body.Data.LeaseGeneration).To(gomega.Equal(saved.lease))
}

func task024CanaryReady(view task024Canary) bool {
	return view.occupancy > 0 && view.lease > 0 && view.jobLease == view.lease && strings.HasSuffix(view.job, "|running")
}

func task024Quote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "''") + "'"
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

func task024RestoreOwned(root state.Root, backup string, rootDev, rootIno, dbDev, dbIno, lockDev, lockIno, backupDev, backupIno uint64) {
	gomega.Expect(root.DBPath).To(gomega.Equal(filepath.Join(root.Path, "state.sqlite")))
	gomega.Expect(backup).NotTo(gomega.Equal(root.DBPath))
	gotRoot, gotRootIno, rootMode := task024FileID(root.Path)
	gotDB, gotDBIno, dbMode := task024FileID(root.DBPath)
	gotLock, gotLockIno, lockMode := task024FileID(filepath.Join(root.Path, "controller.lock"))
	gotBackup, gotBackupIno, backupMode := task024FileID(backup)
	gomega.Expect(rootMode.IsDir()).To(gomega.BeTrue())
	gomega.Expect(dbMode.IsRegular()).To(gomega.BeTrue())
	gomega.Expect(lockMode.IsRegular()).To(gomega.BeTrue())
	gomega.Expect(backupMode.IsRegular()).To(gomega.BeTrue())
	gomega.Expect([]uint64{gotRoot, gotRootIno, gotDB, gotDBIno, gotLock, gotLockIno, gotBackup, gotBackupIno}).To(gomega.Equal([]uint64{rootDev, rootIno, dbDev, dbIno, lockDev, lockIno, backupDev, backupIno}))
	for _, suffix := range []string{"-wal", "-shm"} {
		side := root.DBPath + suffix
		info, err := os.Lstat(side)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || filepath.Base(side) != filepath.Base(root.DBPath)+suffix {
			gomega.Expect(fmt.Errorf("uncertain sidecar %s", side)).NotTo(gomega.HaveOccurred())
		}
		gomega.Expect(os.Remove(side)).To(gomega.Succeed())
	}
	temp := root.DBPath + ".restore"
	in, err := os.Open(backup)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	out, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = io.Copy(out, in)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(out.Sync()).To(gomega.Succeed())
	gomega.Expect(out.Close()).To(gomega.Succeed())
	gomega.Expect(in.Close()).To(gomega.Succeed())
	gomega.Expect(os.Rename(temp, root.DBPath)).To(gomega.Succeed())
}

type task024Seed struct {
	root           state.Root
	installationID string
	receipt        string
	result         string
	history        string
	lease          int64
	version        int
	migrations     string
	backup         string
	backupHash     string
	rootDev        uint64
	rootIno        uint64
	dbDev          uint64
	dbIno          uint64
	lockDev        uint64
	lockIno        uint64
	backupDev      uint64
	backupIno      uint64
	jobID          string
}

func task024SeedCandidate(parent, name string, version int, trigger bool) task024Seed {
	root, err := state.Resolve(filepath.Join(parent, name))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(root.Prepare()).To(gomega.Succeed())
	lock, err := root.AcquireController()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer lock.Close()
	db, err := sqlite.Open(root.DBPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	closed := false
	defer func() {
		if !closed {
			_ = db.Close()
		}
	}()
	ledger, err := managed.NewLedger(db, 1)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ledger.UseLegacyFixtureBounds()
	sum := strings.Repeat("a", 64)
	resource := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "local-candidate", Kind: "local", ExecutionMode: "managed", Capacity: 1, Revision: 1, Adapter: managed.Adapter{QualificationReference: "task024"}, Cleanup: &managed.CleanupPolicy{PolicyReference: "task024"}}
	body, err := json.Marshal(resource)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(ledger.RegisterResource(body)).To(gomega.Succeed())
	inputRoot := filepath.Join(parent, name+"-input")
	gomega.Expect(os.Mkdir(inputRoot, 0700)).To(gomega.Succeed())
	job := managed.JobSpec{
		Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: "local-candidate", Mode: "auto", Suite: "task024",
		Input:       managed.Input{Root: inputRoot, InventoryDigest: sum},
		Environment: managed.Environment{ProfileID: "local", ProfileDigest: sum},
		Workload:    managed.Workload{Argv: []string{"/bin/sh", "-c", "true"}, CWD: "source", Environment: map[string]string{}, RequiredOutputs: []string{"results/out.txt"}},
		Deadlines:   managed.Deadlines{QueueSeconds: 10, PrepareSeconds: 10, RunSeconds: 10, CollectSeconds: 10, CleanupSeconds: 10},
		Effects:     managed.Effects{Mode: "offline", AuthorizationReference: "task024"},
		Provenance:  managed.Provenance{CandidateDigest: sum, TestsDigest: sum, TargetProfileDigest: sum, EvidenceClass: "development"},
	}
	spec, err := json.Marshal(job)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	admitted, err := ledger.Admit(spec, sum, "task024-candidate-admit", "task024-candidate")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	cancelled, err := ledger.CancelQueued(admitted.JobID, admitted.JobRevision, "task024-candidate-cancel")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(cancelled.JobID).To(gomega.Equal(admitted.JobID))
	gomega.Expect(cancelled.JobState).To(gomega.Equal("finished"))
	gomega.Expect(cancelled.Verdict).To(gomega.Equal(managed.Cancelled))
	gomega.Expect(cancelled.LeaseGeneration).To(gomega.BeZero())
	installation, err := ledger.BindInstallation("task024-candidate")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(installation).To(gomega.Equal("task024-candidate"))
	if version == 11 {
		_, err = db.Exec(`DROP TABLE managed_job_bounds`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`DELETE FROM schema_migrations WHERE version>=12`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`PRAGMA user_version=11`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
	if version == 14 {
		_, err = db.Exec(`PRAGMA user_version=14`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
	seed := task024Seed{root: root, installationID: installation, version: version, jobID: admitted.JobID}
	seed.receipt = task024Text(db, `SELECT hex(receipt_json) FROM managed_mutations WHERE mutation_key='task024-candidate-cancel'`)
	seed.result = task024Text(db, `SELECT hex(result_json) FROM managed_jobs WHERE id=?`, admitted.JobID)
	seed.history = task024Text(db, `SELECT COALESCE(group_concat(cursor || ':' || kind || ':' || hex(payload_json), ','), '') FROM (SELECT cursor, kind, payload_json FROM managed_events WHERE job_id=? ORDER BY cursor)`, admitted.JobID)
	seed.migrations = task024Text(db, `SELECT COALESCE(group_concat(version || ':' || identity, ','), '') FROM (SELECT version, identity FROM schema_migrations ORDER BY version)`)
	var occupancy int
	gomega.Expect(db.QueryRow(`SELECT lease_generation, occupancy FROM managed_resources WHERE id='local-candidate'`).Scan(&seed.lease, &occupancy)).To(gomega.Succeed())
	gomega.Expect(seed.lease).To(gomega.BeZero())
	gomega.Expect(occupancy).To(gomega.BeZero())
	var storedRaw []byte
	var stored managed.Result
	gomega.Expect(db.QueryRow(`SELECT result_json FROM managed_jobs WHERE id=?`, admitted.JobID).Scan(&storedRaw)).To(gomega.Succeed())
	gomega.Expect(json.Unmarshal(storedRaw, &stored)).To(gomega.Succeed())
	gomega.Expect(stored.JobID).To(gomega.Equal(admitted.JobID))
	gomega.Expect(stored.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("cancelled")))
	gomega.Expect(stored.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("not_needed")))
	gomega.Expect(stored.FinalVerdict).To(gomega.Equal(managed.Cancelled))
	gomega.Expect(stored.LeaseGeneration).To(gomega.BeZero())
	gomega.Expect(stored.Reason).To(gomega.Equal("cancelled"))
	seed.rootDev, seed.rootIno, _ = task024FileID(root.Path)
	if trigger {
		seed.backup = filepath.Join(parent, name+"-backup.sqlite")
		_, err = db.Exec(`PRAGMA wal_autocheckpoint=0`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`UPDATE managed_resources SET availability_reason=availability_reason WHERE id='local-candidate'`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		wal, walErr := os.Lstat(root.DBPath + "-wal")
		gomega.Expect(walErr).NotTo(gomega.HaveOccurred())
		gomega.Expect(wal.Mode().IsRegular() && wal.Size() > 0).To(gomega.BeTrue())
		_, err = db.Exec(`VACUUM INTO ` + task024Quote(seed.backup))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.Chmod(seed.backup, 0600)).To(gomega.Succeed())
		file, openErr := os.Open(seed.backup)
		gomega.Expect(openErr).NotTo(gomega.HaveOccurred())
		gomega.Expect(file.Sync()).To(gomega.Succeed())
		gomega.Expect(file.Close()).To(gomega.Succeed())
		_, absent := os.Lstat(seed.backup + "-wal")
		gomega.Expect(os.IsNotExist(absent)).To(gomega.BeTrue())
		snap := task024ReadOnly(seed.backup)
		gomega.Expect(task024Text(snap, `SELECT hex(receipt_json) FROM managed_mutations WHERE mutation_key='task024-candidate-cancel'`)).To(gomega.Equal(seed.receipt))
		gomega.Expect(task024Text(snap, `SELECT hex(result_json) FROM managed_jobs WHERE id=?`, seed.jobID)).To(gomega.Equal(seed.result))
		gomega.Expect(task024Text(snap, `SELECT COALESCE(group_concat(cursor || ':' || kind || ':' || hex(payload_json), ','), '') FROM (SELECT cursor, kind, payload_json FROM managed_events WHERE job_id=? ORDER BY cursor)`, seed.jobID)).To(gomega.Equal(seed.history))
		var snapVersion int
		gomega.Expect(snap.QueryRow(`PRAGMA user_version`).Scan(&snapVersion)).To(gomega.Succeed())
		gomega.Expect(snapVersion).To(gomega.Equal(version))
		gomega.Expect(snap.Close()).To(gomega.Succeed())
		seed.backupHash = task024SHA256(seed.backup)
		seed.backupDev, seed.backupIno, _ = task024FileID(seed.backup)
		_, err = db.Exec(`CREATE TRIGGER task024_boundary BEFORE INSERT ON schema_migrations WHEN NEW.version=12 BEGIN SELECT RAISE(ABORT, 'task024 boundary 12'); END`)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
	var got int
	gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&got)).To(gomega.Succeed())
	gomega.Expect(got).To(gomega.Equal(version))
	gomega.Expect(db.Close()).To(gomega.Succeed())
	closed = true
	seed.dbDev, seed.dbIno, _ = task024FileID(root.DBPath)
	seed.lockDev, seed.lockIno, _ = task024FileID(lock.Name())
	return seed
}

func task024CandidateReceipt(path, jobID string) (version int, installation, receipt, result, migrations, history string, lease int64) {
	db := task024ReadOnly(path)
	defer db.Close()
	gomega.Expect(db.QueryRow(`PRAGMA user_version`).Scan(&version)).To(gomega.Succeed())
	gomega.Expect(db.QueryRow(`SELECT id FROM managed_installation WHERE singleton=1`).Scan(&installation)).To(gomega.Succeed())
	receipt = task024Text(db, `SELECT hex(receipt_json) FROM managed_mutations WHERE mutation_key='task024-candidate-cancel'`)
	result = task024Text(db, `SELECT hex(result_json) FROM managed_jobs WHERE id=?`, jobID)
	migrations = task024Text(db, `SELECT COALESCE(group_concat(version || ':' || identity, ','), '') FROM (SELECT version, identity FROM schema_migrations ORDER BY version)`)
	history = task024Text(db, `SELECT COALESCE(group_concat(cursor || ':' || kind || ':' || hex(payload_json), ','), '') FROM (SELECT cursor, kind, payload_json FROM managed_events WHERE job_id=? ORDER BY cursor)`, jobID)
	gomega.Expect(db.QueryRow(`SELECT lease_generation FROM managed_resources WHERE id='local-candidate'`).Scan(&lease)).To(gomega.Succeed())
	return version, installation, receipt, result, migrations, history, lease
}

func task024ExpectCandidate(path string, seed task024Seed, version int, migrated bool) {
	gotVersion, installation, receipt, result, migrations, history, lease := task024CandidateReceipt(path, seed.jobID)
	gomega.Expect(gotVersion).To(gomega.Equal(version))
	gomega.Expect(installation).To(gomega.Equal(seed.installationID))
	gomega.Expect(receipt).To(gomega.Equal(seed.receipt))
	gomega.Expect(result).To(gomega.Equal(seed.result))
	gomega.Expect(history).To(gomega.Equal(seed.history))
	gomega.Expect(lease).To(gomega.Equal(seed.lease))
	gomega.Expect(lease).To(gomega.BeZero())
	if migrated {
		gomega.Expect(migrations).To(gomega.ContainSubstring("12:job-bounds-v12"))
		gomega.Expect(migrations).To(gomega.ContainSubstring("13:evidence-recovery-budget-v13"))
		return
	}
	gomega.Expect(migrations).To(gomega.Equal(seed.migrations))
}

func task024Bound(path string, dev, ino uint64) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || uint64(stat.Dev) != dev || uint64(stat.Ino) != ino {
		return fmt.Errorf("owned directory binding changed: %s", path)
	}
	return nil
}

func task024Retain(parent string, problems []string) {
	ginkgo.Fail("owned cleanup uncertain; retained " + parent + "\n" + strings.Join(problems, "\n"))
}

// task024CandidateEvidence checks the stored queued cancellation. It does not
// invent a CLI exit. A runner receipt or a changed cancel result keeps the fixture.
func task024CandidateEvidence(seed task024Seed) (err error) {
	if seed.jobID == "" {
		return errors.New("candidate job id was not captured")
	}
	if err := task024Bound(seed.root.Path, seed.rootDev, seed.rootIno); err != nil {
		return err
	}
	jobDir := filepath.Join(seed.root.Path, "jobs", seed.jobID)
	for _, name := range []string{"intent.json", "start.json"} {
		info, err := os.Lstat(filepath.Join(jobDir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular receipt", name)
		}
		return fmt.Errorf("queued cancellation %s has runner receipt %s", seed.jobID, name)
	}
	dsn := (&url.URL{Scheme: "file", Path: seed.root.DBPath, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	defer func() {
		if closeErr := db.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if _, execErr := db.Exec(`PRAGMA busy_timeout=5000`); execErr != nil {
		return execErr
	}
	var state string
	var result []byte
	var occupancy int
	var lease int64
	if err = db.QueryRow(`SELECT j.state, j.result_json, r.occupancy, r.lease_generation FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, seed.jobID).Scan(&state, &result, &occupancy, &lease); err != nil {
		return err
	}
	if state != "finished" || len(result) == 0 || occupancy != 0 || lease != seed.lease {
		return fmt.Errorf("queued cancellation %s state %s occupancy %d lease %d", seed.jobID, state, occupancy, lease)
	}
	if task024Text(db, `SELECT hex(result_json) FROM managed_jobs WHERE id=?`, seed.jobID) != seed.result {
		return fmt.Errorf("queued cancellation %s result changed", seed.jobID)
	}
	var settled managed.Result
	if err = json.Unmarshal(result, &settled); err != nil {
		return err
	}
	if settled.JobID != seed.jobID || settled.WorkloadOutcome != "cancelled" || settled.CleanupOutcome != "not_needed" || settled.FinalVerdict != managed.Cancelled || settled.LeaseGeneration != 0 || settled.Reason != "cancelled" {
		return fmt.Errorf("queued cancellation %s result is %s/%s/%s lease %d reason %s", seed.jobID, settled.WorkloadOutcome, settled.CleanupOutcome, settled.FinalVerdict, settled.LeaseGeneration, settled.Reason)
	}
	storedReceipt := task024Text(db, `SELECT hex(receipt_json) FROM managed_mutations WHERE mutation_key='task024-candidate-cancel'`)
	if storedReceipt != seed.receipt {
		return fmt.Errorf("queued cancellation %s receipt changed", seed.jobID)
	}
	storedHistory := task024Text(db, `SELECT COALESCE(group_concat(cursor || ':' || kind || ':' || hex(payload_json), ','), '') FROM (SELECT cursor, kind, payload_json FROM managed_events WHERE job_id=? ORDER BY cursor)`, seed.jobID)
	if storedHistory != seed.history {
		return fmt.Errorf("queued cancellation %s history changed", seed.jobID)
	}
	if err = ownedProcessesStopped(seed.root.Path, map[string]bool{seed.jobID: true}); err != nil {
		return err
	}
	return socketGone(seed.root.SocketPath)
}

var _ = ginkgo.Describe("Task024M1 isolation", ginkgo.Label("integration"), func() {
	ginkgo.It("keeps a leased canary stable across an isolated candidate", func() {
		t := ginkgo.GinkgoT()
		if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
			t.Skip("bootstrap daemon requires Darwin arm64")
		}
		parent, err := os.MkdirTemp("/tmp", "hp-t024c-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		parentDev, parentIno, parentMode := task024FileID(parent)
		gomega.Expect(parentMode.IsDir()).To(gomega.BeTrue())
		var daemons []*task024Daemon
		var openDB *sql.DB
		var canary state.Root
		var canaryDev, canaryIno uint64
		var canaryJobID string
		var candidates []task024Seed
		t.Cleanup(func() {
			problems := make([]string, 0)
			note := func(format string, args ...any) {
				problems = append(problems, fmt.Sprintf(format, args...))
			}
			qualified := false
			if err := task024Bound(parent, parentDev, parentIno); err != nil {
				note("parent binding: %v", err)
			} else if canary.Path == "" {
				note("canary root was not captured")
			} else if err := task024Bound(canary.Path, canaryDev, canaryIno); err != nil {
				note("canary root binding: %v", err)
			} else if canaryJobID == "" {
				note("canary job id was not captured")
			} else {
				previous, present := os.LookupEnv("HAEPARI_HOME")
				if err := os.Setenv("HAEPARI_HOME", canary.Path); err != nil {
					note("canary home: %v", err)
				} else {
					func() {
						defer func() {
							if present {
								_ = os.Setenv("HAEPARI_HOME", previous)
							} else {
								_ = os.Unsetenv("HAEPARI_HOME")
							}
						}()
						if err := (&m2aHarness{}).cancelAndAwait(canaryJobID); err != nil {
							note("canary cancel: %v", err)
						} else if result, code, detail, err := waitCLIResult(canaryJobID, "45s"); err != nil {
							note("canary wait: %v", err)
						} else if !cleanupQualified(code, result) {
							note("canary lacks verified cleanup: exit %d cleanup %s lease %d workload %s (%s)", code, result.CleanupOutcome, result.LeaseGeneration, result.WorkloadOutcome, detail)
						} else if err := requireZeroOccupancy(); err != nil {
							note("occupancy: %v", err)
						} else {
							qualified = true
						}
					}()
				}
			}
			for _, daemon := range daemons {
				if daemon != nil {
					daemon.stopOwned()
				}
			}
			for _, daemon := range daemons {
				if daemon != nil && !daemon.reaped {
					pid := 0
					if daemon.cmd != nil && daemon.cmd.Process != nil {
						pid = daemon.cmd.Process.Pid
					}
					note("unreaped owned daemon pid %d", pid)
				}
			}
			if qualified {
				if err := ownedProcessesStopped(canary.Path, map[string]bool{canaryJobID: true}); err != nil {
					note("canary processes: %v", err)
					qualified = false
				}
				if err := socketGone(canary.SocketPath); err != nil {
					note("canary socket: %v", err)
					qualified = false
				}
			}
			for _, candidate := range candidates {
				if err := task024CandidateEvidence(candidate); err != nil {
					note("candidate %s: %v", candidate.jobID, err)
					qualified = false
				}
			}
			if openDB != nil {
				if closeErr := openDB.Close(); closeErr != nil {
					note("database close failed: %v", closeErr)
					qualified = false
				}
				openDB = nil
			}
			if err := task024Bound(parent, parentDev, parentIno); err != nil {
				note("parent recheck: %v", err)
				qualified = false
			}
			if canary.Path != "" {
				if err := task024Bound(canary.Path, canaryDev, canaryIno); err != nil {
					note("canary root recheck: %v", err)
					qualified = false
				}
			}
			for _, candidate := range candidates {
				if err := task024Bound(candidate.root.Path, candidate.rootDev, candidate.rootIno); err != nil {
					note("candidate root recheck %s: %v", candidate.jobID, err)
					qualified = false
				}
			}
			if ginkgo.CurrentSpecReport().Failed() {
				note("spec failed; retaining fixture")
				qualified = false
			}
			if !qualified || len(problems) > 0 {
				task024Retain(parent, problems)
				return
			}
			if removeErr := os.RemoveAll(parent); removeErr != nil {
				ginkgo.Fail("remove task024 fixture: " + removeErr.Error())
			}
		})
		canary, err = state.Resolve(filepath.Join(parent, "canary"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(len(canary.SocketPath)).To(gomega.BeNumerically("<", 104))
		gomega.Expect(canary.Prepare()).To(gomega.Succeed())
		canaryDev, canaryIno, _ = task024FileID(canary.Path)
		source := filepath.Join(parent, "input")
		gomega.Expect(os.MkdirAll(filepath.Join(source, "source"), 0700)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(source, "source", "input.txt"), []byte("input\n"), 0600)).To(gomega.Succeed())
		store, err := snapshot.OpenStore(canary.Path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		sealed, err := store.Seal(context.Background(), source, []string{"source/input.txt"}, snapshot.Provenance{EvidenceClass: "development"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		t.Setenv("HAEPARI_HOME", canary.Path)
		daemon := task024StartDaemon(canary.Path, filepath.Join(parent, "canary.log"))
		daemons = append(daemons, daemon)
		daemon.waitSocket(canary.SocketPath)
		sum := strings.Repeat("a", 64)
		resource := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "local-canary", Kind: "local", ExecutionMode: "managed", Capacity: 1, Revision: 1, Adapter: managed.Adapter{QualificationReference: "task024"}, Cleanup: &managed.CleanupPolicy{PolicyReference: "task024"}}
		resourcePath := filepath.Join(parent, "resource.json")
		raw, err := json.Marshal(resource)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.WriteFile(resourcePath, raw, 0600)).To(gomega.Succeed())
		_, diagnostic, code := cliOut("resource", "register", "--file", resourcePath, "--json")
		gomega.Expect(code).To(gomega.BeZero(), diagnostic.String())
		job := managed.JobSpec{
			Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: "local-canary", Mode: "auto", Suite: "task024",
			Input:       managed.Input{Root: source, InventoryDigest: strings.TrimPrefix(sealed.Manifest.TreeSHA256, "sha256:"), SelectedFiles: []string{"source/input.txt"}},
			Environment: managed.Environment{ProfileID: "local", ProfileDigest: sum},
			Workload:    managed.Workload{Argv: []string{"/bin/sh", "-c", "mkdir -p ../results; printf ok > ../results/out.txt; while [ ! -f done ]; do sleep 0.2; done"}, CWD: "source", Environment: map[string]string{}, RequiredOutputs: []string{"results/out.txt"}},
			Effects:     managed.Effects{Mode: "offline", AuthorizationReference: "task024"},
			Provenance:  managed.Provenance{CandidateDigest: sum, TestsDigest: sum, TargetProfileDigest: sum, EvidenceClass: "development"},
		}
		spec, err := json.Marshal(job)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		spec = withDeadlines(spec, `{"manual_seconds":0}`)
		jobPath := filepath.Join(parent, "job.json")
		gomega.Expect(os.WriteFile(jobPath, spec, 0600)).To(gomega.Succeed())
		out, diagnostic, code := cliOut("submit", "--spec", jobPath, "--request-key", "task024-canary", "--json")
		gomega.Expect(code).To(gomega.BeZero(), "%s %s", out.String(), diagnostic.String())
		var submitted struct {
			Data managed.Receipt `json:"data"`
		}
		gomega.Expect(json.Unmarshal(out.Bytes(), &submitted)).To(gomega.Succeed())
		gomega.Expect(submitted.Data.JobID).NotTo(gomega.BeEmpty())
		canaryJobID = submitted.Data.JobID
		deadline := time.Now().Add(30 * time.Second)
		var saved task024Canary
		for {
			saved = task024ObserveCanary(canary, daemon)
			if task024CanaryReady(saved) {
				break
			}
			if time.Now().After(deadline) {
				ginkgo.Fail(fmt.Sprintf("canary did not hold a running lease: job %s occupancy %d lease %d", saved.job, saved.occupancy, saved.lease))
			}
			time.Sleep(50 * time.Millisecond)
		}
		gomega.Expect(saved.occupancy).To(gomega.BeNumerically(">", 0))
		gomega.Expect(saved.lease).To(gomega.BeNumerically(">", 0))
		gomega.Expect(saved.jobLease).To(gomega.Equal(saved.lease))
		gomega.Expect(saved.job).To(gomega.HavePrefix(canaryJobID + "|"))

		migrated := task024SeedCandidate(parent, "migrate", 11, false)
		candidates = append(candidates, migrated)
		migrator := task024StartDaemon(migrated.root.Path, filepath.Join(parent, "migrate.log"))
		daemons = append(daemons, migrator)
		migrator.waitSocket(migrated.root.SocketPath)
		task024ExpectCandidate(migrated.root.DBPath, migrated, 13, true)
		task024ExpectCanary(canary, daemon, saved)
		migrator.stopOwned()

		fault := task024SeedCandidate(parent, "fault", 11, true)
		candidates = append(candidates, fault)
		faultDaemon := task024StartDaemon(fault.root.Path, filepath.Join(parent, "fault.log"))
		daemons = append(daemons, faultDaemon)
		gomega.Expect(faultDaemon.waitExit(20 * time.Second)).To(gomega.Equal(6))
		gomega.Expect(faultDaemon.output()).To(gomega.ContainSubstring("task024 boundary 12"))
		task024ExpectCandidate(fault.root.DBPath, fault, 11, false)
		lock, err := fault.root.AcquireController()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		task024RestoreOwned(fault.root, fault.backup, fault.rootDev, fault.rootIno, fault.dbDev, fault.dbIno, fault.lockDev, fault.lockIno, fault.backupDev, fault.backupIno)
		gomega.Expect(task024SHA256(fault.backup)).To(gomega.Equal(fault.backupHash))
		gomega.Expect(lock.Close()).To(gomega.Succeed())
		restart := task024StartDaemon(fault.root.Path, filepath.Join(parent, "fault-restart.log"))
		daemons = append(daemons, restart)
		restart.waitSocket(fault.root.SocketPath)
		task024ExpectCandidate(fault.root.DBPath, fault, 13, true)
		task024ExpectCanary(canary, daemon, saved)
		restart.stopOwned()

		owned := task024StartDaemon(canary.Path, filepath.Join(parent, "owned.log"))
		daemons = append(daemons, owned)
		gomega.Expect(owned.waitExit(10 * time.Second)).To(gomega.Equal(2))
		gomega.Expect(owned.output()).To(gomega.ContainSubstring("owned by another controller"))
		task024ExpectCanary(canary, daemon, saved)

		newer := task024SeedCandidate(parent, "toonew", 14, false)
		candidates = append(candidates, newer)
		newerDaemon := task024StartDaemon(newer.root.Path, filepath.Join(parent, "toonew.log"))
		daemons = append(daemons, newerDaemon)
		gomega.Expect(newerDaemon.waitExit(20 * time.Second)).To(gomega.Equal(6))
		gomega.Expect(newerDaemon.output()).To(gomega.ContainSubstring("UNSUPPORTED"))
		task024ExpectCandidate(newer.root.DBPath, newer, 14, false)
		task024ExpectCanary(canary, daemon, saved)
	})
})
