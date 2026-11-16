//go:build integration

package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/relocation"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func relocationClient(s *runtimeServer) (*client.Client, func()) {
	listener, err := net.Listen("unix", s.root.SocketPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	server := httptest.NewUnstartedServer(s.routes())
	server.Listener = listener
	server.Start()
	c, err := client.Connect(context.Background(), s.root)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return c, func() { c.Close(); server.Close() }
}
func expectRelocatedHistory(db *sql.DB, s *runtimeServer, id, key string, job managed.JobSpec) {
	t := ginkgo.GinkgoT()
	ctx := context.Background()
	parent := filepath.Dir(s.root.Path)
	target := filepath.Join(parent, "relocated")
	backup := filepath.Join(parent, "backup")
	binary := filepath.Join(parent, "mover")
	// Bind legacy absent inventory through its existing supported owner before
	// the relocation baseline; the mover itself never fills/mutates SQL rows.
	original, err := s.store.Read(func() string {
		r, e := s.ledger.Request(key)
		gomega.Expect(e).NotTo(gomega.HaveOccurred())
		return r.ObjectID
	}())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	inventory, _ := json.Marshal(original.Manifest)
	r, err := s.ledger.Request(key)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	if len(r.Inventory) == 0 {
		gomega.Expect(s.ledger.BindAdmissionInventory(key, original.ObjectID, inventory)).To(gomega.Succeed())
	}
	// A retained writer may have stored valid noncanonical whitespace. Seed that
	// historical format before the baseline, with the writer's raw-byte digest;
	// relocation must preserve it rather than remarshal JSON to validate it.
	var receiptKind string
	var receiptRaw []byte
	var historicalRaw []byte
	err = db.QueryRow("SELECT kind,payload_json FROM managed_runner_receipts WHERE job_id=? ORDER BY kind LIMIT 1", id).Scan(&receiptKind, &receiptRaw)
	if err != sql.ErrNoRows {
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var formatted bytes.Buffer
		gomega.Expect(json.Indent(&formatted, receiptRaw, "", "  ")).To(gomega.Succeed())
		historicalRaw = append(formatted.Bytes(), '\n')
		sum := sha256.Sum256(historicalRaw)
		_, err = db.Exec("UPDATE managed_runner_receipts SET payload_json=?,digest=? WHERE job_id=? AND kind=?", historicalRaw, "sha256:"+hex.EncodeToString(sum[:]), id, receiptKind)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
	beforeClient, closeBefore := relocationClient(s)
	inspection, _, err := beforeClient.Inspect(ctx, id)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	logs, _, err := beforeClient.Logs(ctx, id, client.LogsOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	events, _, err := beforeClient.Events(ctx, id, client.EventsOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	beforeExport := filepath.Join(parent, "export-before")
	_, _, err = beforeClient.Export(ctx, id, beforeExport)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	closeBefore()
	immutable := map[string][]byte{}
	identities := map[string]os.FileInfo{}
	jobs := filepath.Join(s.root.Path, "jobs")
	gomega.Expect(filepath.Walk(jobs, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			relative, e := filepath.Rel(s.root.Path, p)
			if e != nil {
				return e
			}
			raw, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			immutable[relative] = raw
			identities[relative] = info
		}
		return nil
	})).To(gomega.Succeed())
	gomega.Expect(db.Close()).To(gomega.Succeed())
	lock, err := s.root.AcquireController()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(lock.Close()).To(gomega.Succeed())
	gomega.Expect(os.WriteFile(binary, []byte("isolated-mover-fixture"), 0700)).To(gomega.Succeed())
	a := relocation.ServicePlan{GlobalLimit: 1, HostMACOSGuestLimit: 1, Schema: "haepari.release-service-plan/v1", Instance: "history", Label: "io.rootkernel.haepari.history", Domain: "gui/" + strconv.Itoa(os.Geteuid()), OwnerUID: os.Geteuid(), Binary: binary, BinarySHA256: relocation.Digest([]byte("isolated-mover-fixture")), Root: s.root.Path, PlistPath: filepath.Join(parent, "absent.plist"), Plist: "fixture source"}
	b := a
	b.Schema = "haepari.release-service-plan/v2"
	b.Root = target
	b.Plist = "fixture target"
	owner := map[string]any{"schema": "haepari.release-service-owner/v1", "instance": a.Instance, "owner_uid": a.OwnerUID, "root": a.Root}
	raw, _ := json.Marshal(owner)
	gomega.Expect(os.WriteFile(filepath.Join(a.Root, ".development-service.json"), raw, 0600)).To(gomega.Succeed())
	env := relocation.Environment{MoverBinary: binary, ServicesStopped: func(relocation.ServicePlan) error { return nil }, ValidateService: func(p relocation.ServicePlan) error {
		raw, err := os.ReadFile(p.Binary)
		if err != nil {
			return err
		}
		if relocation.Digest(raw) != p.BinarySHA256 {
			return managed.ErrConflict
		}
		return nil
	}}
	p, err := relocation.NewPlan(a, b, backup, env)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	receipt, err := relocation.Operate(p, "apply", env)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(receipt.InstallationID).To(gomega.Equal(s.installationID()))
	for relative, raw := range immutable {
		p := filepath.Join(target, relative)
		gomega.Expect(os.ReadFile(p)).To(gomega.Equal(raw))
		now, err := os.Stat(p)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.SameFile(identities[relative], now)).To(gomega.BeTrue())
	}
	checkHistory := func(rootPath, suffix string) {
		moved, err := state.Resolve(rootPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ownership, err := moved.AcquireController()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(relocation.Activate(rootPath, ownership)).To(gomega.Succeed())
		newDB, err := sqlite.Open(moved.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer newDB.Close()
		if len(historicalRaw) > 0 {
			var retained []byte
			gomega.Expect(newDB.QueryRow("SELECT payload_json FROM managed_runner_receipts WHERE job_id=? AND kind=?", id, receiptKind).Scan(&retained)).To(gomega.Succeed())
			gomega.Expect(retained).To(gomega.Equal(historicalRaw))
		}
		ledger, err := managed.NewLedger(newDB, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		store, err := snapshot.OpenStore(rootPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		current, err := newRuntime(moved, ledger, store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.installationID()).To(gomega.Equal(s.installationID()))
		afterClient, closeAfter := relocationClient(current)
		defer closeAfter()
		defer ownership.Close()
		after, _, err := afterClient.Inspect(ctx, id)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(inspection))
		newLogs, _, err := afterClient.Logs(ctx, id, client.LogsOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(newLogs).To(gomega.Equal(logs))
		newEvents, _, err := afterClient.Events(ctx, id, client.EventsOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(newEvents).To(gomega.Equal(events))
		afterExport := filepath.Join(parent, "export-"+suffix)
		_, _, err = afterClient.Export(ctx, id, afterExport)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(filepath.Walk(beforeExport, func(name string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() && info.Name() != "manifest.json" {
				relative, e := filepath.Rel(beforeExport, name)
				if e != nil {
					return e
				}
				old, e := os.ReadFile(name)
				if e != nil {
					return e
				}
				gomega.Expect(os.ReadFile(filepath.Join(afterExport, relative))).To(gomega.Equal(old))
			}
			return nil
		})).To(gomega.Succeed())
		replay, _, err := afterClient.Submit(ctx, mustJSONScheduling(job), key)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay.JobID).To(gomega.Equal(id))
		gomega.Expect(replay.JobState).To(gomega.Equal("finished"))
		var count int
		gomega.Expect(newDB.QueryRow("SELECT COUNT(*) FROM managed_jobs").Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.Equal(1))
		for relative, raw := range immutable {
			file := filepath.Join(rootPath, relative)
			gomega.Expect(os.ReadFile(file)).To(gomega.Equal(raw))
			info, err := os.Stat(file)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(os.SameFile(identities[relative], info)).To(gomega.BeTrue())
		}
	}

	// Reverse the first move while still stopped, before activation/consumer use.
	// After restored-root observations, a NEW exact-predecessor plan preserves the
	// original forward public observations without pretending they permit rollback.
	reversed, err := relocation.Operate(p, "rollback", env)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(reversed.Phase).To(gomega.Equal("rolled_back"))
	gomega.Expect(reversed.InstallationID).To(gomega.Equal(receipt.InstallationID))
	for relative, raw := range immutable {
		file := filepath.Join(a.Root, relative)
		gomega.Expect(os.ReadFile(file)).To(gomega.Equal(raw))
		info, err := os.Stat(file)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.SameFile(identities[relative], info)).To(gomega.BeTrue())
	}
	checkHistory(a.Root, "restored")
	next, err := relocation.NewPlan(a, b, filepath.Join(parent, "forward-backup"), env)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(next.Predecessor).NotTo(gomega.BeNil())
	_, err = relocation.Operate(next, "apply", env)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	checkHistory(target, "forward")

	_ = t
}

var _ = ginkgo.Describe("Task043RetainedHistory", ginkgo.Label("integration"), func() {
	ginkgo.It("keeps local PASS, FAIL and historical missing-output INCOMPLETE readable/exportable/replayable without another run", func() {
		for _, kind := range []string{"pass", "fail", "gap"} {
			ginkgo.By(kind)
			script := "mkdir -p ../results; printf retained > ../results/output.txt; printf retained-log"
			if kind == "fail" {
				script += "; exit 7"
			}
			if kind == "gap" {
				script = "printf retained-log"
			}
			db, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), script, func(j *managed.JobSpec) {
				if kind == "gap" {
					j.Schema = "haepari.job/v2"
				}
			})
			gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
			result, err := s.ledger.Result(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			expected := map[string]managed.Verdict{"pass": managed.Pass, "fail": managed.Fail, "gap": managed.Incomplete}
			gomega.Expect(result.FinalVerdict).To(gomega.Equal(expected[kind]))
			expectRelocatedHistory(db, s, r.JobID, "cancel-key", job)
		}
	})
	ginkgo.It("refuses a legacy missing-output history whose cleanup evidence remains currently protected", func() {
		db, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "printf legacy-gap", func(*managed.JobSpec) {})
		defer db.Close()
		gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		result, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		tx, err := db.Begin()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer tx.Rollback()
		_, err = managed.RelocationObservation(tx, s.root.Path)
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
	ginkgo.It("preserves an explicit synthetic manual observation as history without turning relocation into new human evidence", func() {
		script := "mkdir -p ../results; printf manual-retained > ../results/output.txt"
		db, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), script, func(j *managed.JobSpec) {
			j.Schema = "haepari.job/v2"
			j.Mode = "manual"
			j.Manual = &managed.Manual{ScenarioDocument: "source/input.txt", RequiredScenarios: []string{"check"}}
			j.Setup = &managed.Setup{Argv: []string{"/bin/sh", "-c", script}}
			j.Deadlines.RunSeconds = 0
			j.Deadlines.ManualSeconds = 30
		})
		done := make(chan error, 1)
		go func() {
			defer ginkgo.GinkgoRecover()
			done <- s.executeLocal(r, job, object, time.Now().Add(5*time.Second))
		}()
		gomega.Eventually(func() string { r, _ := s.ledger.Snapshot(r.JobID); return r.JobState }, 10*time.Second, 20*time.Millisecond).Should(gomega.Equal("awaiting_manual"))
		r, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		record := managed.ManualResult{Schema: "haepari.manual-result/v1", JobID: r.JobID, InstallationID: s.installationID(), ProfileID: job.Environment.ProfileID, ProfileDigest: job.Environment.ProfileDigest, ExpectedRevision: r.JobRevision, Observer: "synthetic-relocation-fixture", Scenarios: []managed.ManualScenario{{ID: "check", Status: "pass", EvidenceReferences: []string{"fixture:record-preservation-only"}}}}
		w := vmMutationHTTP(s, "/v1/jobs/"+r.JobID+"/manual/complete", map[string]any{"result": record, "expected_revision": r.JobRevision})
		gomega.Expect(w.Code).To(gomega.Equal(200), w.Body.String())
		gomega.Eventually(done, 10*time.Second).Should(gomega.Receive(gomega.Succeed()))
		expectRelocatedHistory(db, s, r.JobID, "cancel-key", job)
	})
	ginkgo.It("moves retained fake-VM history while leaving guest paths/registrations unchanged", func() {
		t := ginkgo.GinkgoT()
		original := nativeVMFixture(t, "mkdir -p ../results; printf vm-retained > ../results/output.txt; printf guest-log")
		root, err := state.Resolve(filepath.Join(t.TempDir(), "fresh"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(root.Prepare()).To(gomega.Succeed())
		gomega.Expect(os.Mkdir(filepath.Join(root.Path, "jobs"), 0700)).To(gomega.Succeed())
		db, err := sqlite.Open(root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		ledger, err := managed.NewLedger(db, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ledger.UseLegacyFixtureBounds()
		store, err := snapshot.OpenStore(root.Path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		s, err := newRuntime(root, ledger, store)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		s.utmCommand = original.s.utmCommand
		s.sshCommand = original.s.sshCommand
		s.vmLeases = original.s.vmLeases
		s.vmExecutable = original.s.vmExecutable
		gomega.Expect(ledger.RegisterResource(mustJSONScheduling(original.spec))).To(gomega.Succeed())
		sealed, err := store.Seal(context.Background(), original.job.Input.Root, original.job.Input.SelectedFiles, snapshot.Provenance{EvidenceClass: "development"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f := &vmJobFixture{s, original.spec, original.job, sealed.ObjectID, original.guest, original.control}
		result := f.run("relocation-vm")
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		guestBefore, err := os.Stat(f.guest)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		expectRelocatedHistory(db, s, "relocation-vm", "key-relocation-vm", f.job)
		guestAfter, err := os.Stat(f.guest)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.SameFile(guestBefore, guestAfter)).To(gomega.BeTrue())
	})

})

type relocationRootEntry struct {
	info os.FileInfo
	raw  []byte
}

func captureRelocationRoot(root string) map[string]relocationRootEntry {
	entries := map[string]relocationRootEntry{}
	gomega.Expect(filepath.Walk(root, func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		var raw []byte
		if info.Mode().IsRegular() {
			raw, err = os.ReadFile(name)
			if err != nil {
				return err
			}
		}
		entries[relative] = relocationRootEntry{info, raw}
		return nil
	})).To(gomega.Succeed())
	return entries
}
func expectSameRelocationRoot(before, after map[string]relocationRootEntry) {
	gomega.Expect(after).To(gomega.HaveLen(len(before)))
	for name, entry := range before {
		now, ok := after[name]
		gomega.Expect(ok).To(gomega.BeTrue(), name)
		gomega.Expect(now.info.Mode()).To(gomega.Equal(entry.info.Mode()))
		gomega.Expect(os.SameFile(entry.info, now.info)).To(gomega.BeTrue(), name)
		gomega.Expect(now.raw).To(gomega.Equal(entry.raw), name)
	}
}

var _ = ginkgo.Describe("Task043PurgedInventory", ginkgo.Label("integration"), func() {
	for _, kind := range []string{"valid", "malformed", "digest"} {
		ginkgo.It("validates retained "+kind+" inventory after genuine completed purge", func() {
			db, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "mkdir -p ../results; printf retained > ../results/output.txt")
			defer db.Close()
			gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
			finished, err := s.ledger.Snapshot(r.JobID)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			purged, err := s.purgeJob(r.JobID, finished.JobRevision)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(purged.Complete).To(gomega.BeTrue())
			_, err = s.store.Read(object)
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			var inventory []byte
			var retention string
			gomega.Expect(db.QueryRow("SELECT inventory_json,retention_state FROM managed_admissions WHERE job_id=?", r.JobID).Scan(&inventory, &retention)).To(gomega.Succeed())
			gomega.Expect(retention).To(gomega.Equal("unavailable"))
			gomega.Expect(len(inventory)).To(gomega.BeNumerically(">", 0))
			// Corruption is a fixture input before the baseline, never a mover repair.
			if kind != "valid" {
				raw := []byte("{")
				if kind == "digest" {
					raw = append(append([]byte{}, inventory...), '\n')
				}
				_, err = db.Exec("UPDATE managed_admissions SET inventory_json=? WHERE job_id=?", raw, r.JobID)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			gomega.Expect(db.Close()).To(gomega.Succeed())
			parent := filepath.Dir(s.root.Path)
			binary := filepath.Join(parent, "purged-mover")
			gomega.Expect(os.WriteFile(binary, []byte("purged fixture"), 0700)).To(gomega.Succeed())
			a := relocation.ServicePlan{Schema: "haepari.release-service-plan/v1", Instance: "purged", Label: "io.rootkernel.haepari.purged", Domain: "gui/" + strconv.Itoa(os.Geteuid()), OwnerUID: os.Geteuid(), Binary: binary, BinarySHA256: relocation.Digest([]byte("purged fixture")), Root: s.root.Path, PlistPath: filepath.Join(parent, "purged.plist"), Plist: "source", GlobalLimit: 1, HostMACOSGuestLimit: 1}
			b := a
			b.Schema = "haepari.release-service-plan/v2"
			b.Root = filepath.Join(parent, "purged-target")
			b.Plist = "target"
			owner := map[string]any{"schema": "haepari.release-service-owner/v1", "instance": a.Instance, "owner_uid": a.OwnerUID, "root": a.Root}
			gomega.Expect(os.WriteFile(filepath.Join(a.Root, relocation.OwnerName), mustJSONScheduling(owner), 0600)).To(gomega.Succeed())
			lock, err := s.root.AcquireController()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(lock.Close()).To(gomega.Succeed())
			env := relocation.Environment{MoverBinary: binary, ValidateService: func(relocation.ServicePlan) error { return nil }, ServicesStopped: func(relocation.ServicePlan) error { return nil }}
			backup := filepath.Join(parent, "purged-backup")
			before := captureRelocationRoot(a.Root)
			plan, err := relocation.NewPlan(a, b, backup, env)
			if kind != "valid" {
				gomega.Expect(err).To(gomega.HaveOccurred())
				expectSameRelocationRoot(before, captureRelocationRoot(a.Root))
				_, err = os.Lstat(filepath.Join(a.Root, relocation.JournalName))
				gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
				_, err = os.Lstat(backup)
				gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
				return
			}
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = relocation.Operate(plan, "apply", env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = relocation.Operate(plan, "rollback", env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			movedDB, err := sqlite.Open(s.root.DBPath)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer movedDB.Close()
			ledger, err := managed.NewLedger(movedDB, 1)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			store, err := snapshot.OpenStore(a.Root)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			current, err := newRuntime(s.root, ledger, store)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			c, closeClient := relocationClient(current)
			defer closeClient()
			replay, _, err := c.Submit(context.Background(), mustJSONScheduling(job), "cancel-key")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(replay.JobID).To(gomega.Equal(r.JobID))
			gomega.Expect(replay.ResultStatus).To(gomega.Equal("unavailable"))
			_, err = ledger.Result(r.JobID)
			gomega.Expect(err).To(gomega.MatchError(managed.ErrResultUnavailable))
			var retained []byte
			gomega.Expect(movedDB.QueryRow("SELECT inventory_json FROM managed_admissions WHERE job_id=?", r.JobID).Scan(&retained)).To(gomega.Succeed())
			gomega.Expect(retained).To(gomega.Equal(inventory))
			var count int
			gomega.Expect(movedDB.QueryRow("SELECT COUNT(*) FROM managed_runs").Scan(&count)).To(gomega.Succeed())
			gomega.Expect(count).To(gomega.Equal(1))
			_, err = store.Read(object)
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		})
	}
})
