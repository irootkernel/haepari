//go:build integration

package controller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/config"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

var _ = ginkgo.Describe("Task037ControllerStartupLimits", ginkgo.Label("integration"), func() {
	ginkgo.It("refuses invalid options before creating a state root", func() {
		root, err := state.Resolve(filepath.Join(ginkgo.GinkgoT().TempDir(), "absent"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, options := range []Options{{GlobalLimit: 0, HostMACOSGuestLimit: 1}, {GlobalLimit: -1, HostMACOSGuestLimit: 1}, {GlobalLimit: 2, HostMACOSGuestLimit: 3}} {
			gomega.Expect(RunWithOptions(context.Background(), root, options)).To(gomega.MatchError(managed.ErrInvalid))
			_, err = os.Lstat(root.Path)
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		}
	})
	for _, limit := range []int{1, 2, 3} {
		ginkgo.It(fmt.Sprintf("admits %d independent native workers through the real Unix controller and reuses a freed slot", limit), func() {
			t := ginkgo.GinkgoT()
			parent, err := filepath.EvalSymlinks(t.TempDir())
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			root, err := state.Resolve(filepath.Join(parent, "state"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			source := filepath.Join(parent, "input")
			signal := filepath.Join(parent, "signal")
			gomega.Expect(os.Mkdir(source, 0700)).To(gomega.Succeed())
			gomega.Expect(os.Mkdir(signal, 0700)).To(gomega.Succeed())
			gomega.Expect(os.Mkdir(filepath.Join(source, "source"), 0700)).To(gomega.Succeed())
			gomega.Expect(os.Mkdir(filepath.Join(parent, "sealing"), 0700)).To(gomega.Succeed())
			gomega.Expect(os.WriteFile(filepath.Join(source, "source", "input.txt"), []byte("sealed"), 0600)).To(gomega.Succeed())
			store, err := snapshot.OpenStore(filepath.Join(parent, "sealing"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			sealed, err := store.Seal(context.Background(), source, []string{"source/input.txt"}, snapshot.Provenance{EvidenceClass: "development"})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				if limit == 1 {
					done <- Run(ctx, root)
				} else {
					done <- RunWithOptions(ctx, root, Options{GlobalLimit: limit, HostMACOSGuestLimit: 1})
				}
			}()
			t.Cleanup(func() { cancel(); gomega.Eventually(done, 5*time.Second).Should(gomega.Receive(gomega.Succeed())) })
			var c *client.Client
			gomega.Eventually(func() error { var e error; c, e = client.Connect(context.Background(), root); return e }, 5*time.Second, 25*time.Millisecond).Should(gomega.Succeed())
			defer c.Close()
			ids := map[string]string{}
			digest := strings.Repeat("a", 64)
			for i := 0; i < 3; i++ {
				name := fmt.Sprintf("local-%d", i)
				resource := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: name, Kind: "local", ExecutionMode: "managed", Capacity: 1, Revision: 1, Adapter: managed.Adapter{QualificationReference: "isolated-native-limits"}, Cleanup: &managed.CleanupPolicy{PolicyReference: "owned-workspace"}}
				_, err = c.Register(context.Background(), mustJSONScheduling(resource))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				job := managed.JobSpec{Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: name, Mode: "auto", Suite: "limits", Input: managed.Input{Root: source, InventoryDigest: strings.TrimPrefix(sealed.Manifest.TreeSHA256, "sha256:"), SelectedFiles: []string{"source/input.txt"}}, Environment: managed.Environment{ProfileID: "development", ProfileDigest: digest}, Workload: managed.Workload{Argv: []string{"/bin/sh", "-c", "printf started > \"$SIGNAL/$NAME.started\"; while [ ! -f \"$SIGNAL/$NAME.release\" ]; do sleep .02; done; mkdir -p ../results; printf complete > ../results/output.txt"}, CWD: "source", Environment: map[string]string{"SIGNAL": signal, "NAME": name}, RequiredOutputs: []string{"results/output.txt"}}, Deadlines: managed.Deadlines{QueueSeconds: 60, PrepareSeconds: 30, RunSeconds: 30, CollectSeconds: 30, CleanupSeconds: 30}, Effects: managed.Effects{Mode: "offline", AuthorizationReference: "isolated-native-workers"}, Provenance: managed.Provenance{CandidateDigest: digest, TestsDigest: digest, TargetProfileDigest: digest, EvidenceClass: "development"}}
				r, _, e := c.Submit(context.Background(), mustJSONScheduling(job), "limit-"+name)
				gomega.Expect(e).NotTo(gomega.HaveOccurred())
				ids[name] = r.JobID
			}
			started := func() []string {
				entries, _ := os.ReadDir(signal)
				names := []string{}
				for _, entry := range entries {
					if strings.HasSuffix(entry.Name(), ".started") {
						names = append(names, strings.TrimSuffix(entry.Name(), ".started"))
					}
				}
				return names
			}
			gomega.Eventually(func() int { return len(started()) }, 10*time.Second, 25*time.Millisecond).Should(gomega.Equal(limit))
			gomega.Consistently(func() int { return len(started()) }, 200*time.Millisecond, 25*time.Millisecond).Should(gomega.Equal(limit))
			first := started()[0]
			gomega.Expect(os.WriteFile(filepath.Join(signal, first+".release"), nil, 0600)).To(gomega.Succeed())
			if limit < 3 {
				gomega.Eventually(func() int { return len(started()) }, 10*time.Second, 25*time.Millisecond).Should(gomega.Equal(limit + 1))
			}
			for name := range ids {
				gomega.Expect(os.WriteFile(filepath.Join(signal, name+".release"), nil, 0600)).To(gomega.Succeed())
			}
			for _, id := range ids {
				var result managed.Result
				gomega.Eventually(func() error {
					var e error
					result, _, e = c.Wait(context.Background(), id, 100*time.Millisecond)
					return e
				}, 10*time.Second, 25*time.Millisecond).Should(gomega.Succeed())
				gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
			}
		})
	}
})

func pinnedDocument(logBytes, artifactBytes, artifactCount int64) config.Document {
	doc := config.Defaults()
	doc.Limits.LogBytesPerStream = logBytes
	doc.Limits.ArtifactBytes = artifactBytes
	doc.Limits.ArtifactCount = artifactCount
	return doc
}

func pinnedLocalRuntime(t ginkgo.GinkgoTInterface, script, jobID string, doc config.Document, options ...func(*managed.JobSpec)) (*sql.DB, *runtimeServer, managed.Receipt, managed.JobSpec, string) {
	t.Helper()
	root, err := state.Resolve(filepath.Join(t.TempDir(), "state"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(root.Prepare()).To(gomega.Succeed())
	gomega.Expect(os.Mkdir(filepath.Join(root.Path, "jobs"), 0700)).To(gomega.Succeed())
	db, err := sqlite.Open(root.DBPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	t.Cleanup(func() { _ = db.Close() })
	ledger, err := managed.NewLedger(db, 1)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = ledger.CommitEffectiveConfig(doc, true)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	store, err := snapshot.OpenStore(root.Path)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	server, err := newRuntime(root, ledger, store)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	resource := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "local-pin", Kind: "local", ExecutionMode: "managed", Capacity: 1, Revision: 1, Adapter: managed.Adapter{QualificationReference: "isolated-development"}, Cleanup: &managed.CleanupPolicy{PolicyReference: "owned-workspace"}}
	gomega.Expect(ledger.RegisterResource(mustJSONScheduling(resource))).To(gomega.Succeed())
	source := filepath.Join(t.TempDir(), "input")
	gomega.Expect(os.MkdirAll(filepath.Join(source, "source"), 0700)).To(gomega.Succeed())
	gomega.Expect(os.WriteFile(filepath.Join(source, "source/input.txt"), []byte("sealed"), 0600)).To(gomega.Succeed())
	sealed, err := store.Seal(context.Background(), source, []string{"source/input.txt"}, snapshot.Provenance{EvidenceClass: "development"})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	digest := strings.Repeat("a", 64)
	job := managed.JobSpec{Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: resource.ID, Mode: "auto", Suite: "pin", Input: managed.Input{Root: source, InventoryDigest: strings.TrimPrefix(sealed.Manifest.TreeSHA256, "sha256:"), SelectedFiles: []string{"source/input.txt"}}, Environment: managed.Environment{ProfileID: "local", ProfileDigest: digest}, Workload: managed.Workload{Argv: []string{"/bin/sh", "-c", script}, CWD: "source", RequiredOutputs: []string{"results/output.txt"}}, Deadlines: managed.Deadlines{QueueSeconds: 60, PrepareSeconds: 5, RunSeconds: 10, CollectSeconds: 3, CleanupSeconds: 2}, Effects: managed.Effects{Mode: "offline", AuthorizationReference: "isolated-pin"}, Provenance: managed.Provenance{CandidateDigest: digest, TestsDigest: digest, TargetProfileDigest: digest, EvidenceClass: "development"}}
	for _, option := range options {
		option(&job)
	}
	admitted, err := ledger.AdmitSealed(mustJSONScheduling(job), job.Input.InventoryDigest, sealed.ObjectID, "pin-key-"+jobID, jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	reserved, err := ledger.Reserve(managed.ReserveRequest{JobID: admitted.JobID, ExpectedJobRevision: admitted.JobRevision, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "reserve-" + jobID})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return db, server, reserved, job, sealed.ObjectID
}

func occupancyOf(db *sql.DB, jobID string) int {
	var occupancy int
	gomega.Expect(db.QueryRow(`SELECT r.occupancy FROM managed_jobs j JOIN managed_resources r ON r.id=j.resource_id WHERE j.id=?`, jobID).Scan(&occupancy)).To(gomega.Succeed())
	return occupancy
}

func collectedPinReceipt(q managed.EvidenceRecoveryIntent, artifactSize, guestLog int64) managed.EvidenceRecoveryReceipt {
	job := q.Jobs[0]
	outcome := managed.EvidenceRecoveryOutcome{JobID: job.JobID, ResultDigest: job.ResultDigest, Stage: "collected", ArtifactRoot: "recovery-" + q.MutationKey, Artifacts: []managed.ResultArtifact{{Path: job.Outputs[0], SizeBytes: artifactSize, SHA256: strings.Repeat("ab", 32)}}}
	if guestLog >= 0 {
		outcome.GuestArtifacts = []managed.ResultArtifact{{Path: "output.log", SizeBytes: guestLog, SHA256: strings.Repeat("cd", 32)}}
	}
	return managed.EvidenceRecoveryReceipt{Schema: "haepari.evidence-recovery-receipt/v1", MutationKey: q.MutationKey, Jobs: []managed.EvidenceRecoveryOutcome{outcome}, Observation: recoveryObservation("pinned-output-receipt", q.MutationKey, "resource:"+q.ResourceID)}
}

func requestReceipts(db *sql.DB, jobID string) int {
	var count int
	gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_runner_receipts WHERE job_id=? AND kind='request'`, jobID).Scan(&count)).To(gomega.Succeed())
	return count
}

var _ = ginkgo.Describe("Task023 runtime bounds", func() {
	ginkgo.It("publishes a small combined log limit and keeps the admitted phase budget", func() {
		t := ginkgo.GinkgoT()
		db, s, r, job, object := pinnedLocalRuntime(t, "mkdir -p ../results; printf passed > ../results/output.txt; printf 0123456789abcdef", "local-log-pin", pinnedDocument(8, config.MaxArtifactBytes, config.MaxArtifactCount), func(j *managed.JobSpec) {
			j.Deadlines.CollectSeconds = 1000
		})
		gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		result, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.NativeExitCode).NotTo(gomega.BeNil())
		gomega.Expect(*result.NativeExitCode).To(gomega.Equal(0))
		gomega.Expect(result.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("partial")))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.ProtectedEvidence).To(gomega.ContainElement(managed.ProtectedEvidence{Path: "workspace", Reason: "required_output_uncollected", RecoveryReference: "job:" + r.JobID}))
		gomega.Expect(result.Logs).To(gomega.HaveLen(1))
		gomega.Expect(result.Logs[0].SizeBytes).To(gomega.Equal(int64(8)))
		gomega.Expect(result.EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "output.log", Reason: "log_limit", Offset: 8, DroppedBytes: 8}))
		for _, gap := range result.EvidenceGaps {
			gomega.Expect(gap.Reason).NotTo(gomega.Equal("pinned_log_exceeded"))
		}
		gomega.Expect(os.ReadFile(filepath.Join(s.root.Path, "jobs", r.JobID, "output.log"))).To(gomega.Equal([]byte("01234567")))
		payload, err := s.ledger.RunnerReceiptPayload(r.JobID, "request")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(payload)).To(gomega.ContainSubstring(`"log_limit_bytes":8`))
		phases, err := s.ledger.LocalPhases(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["collect"].BudgetSeconds).To(gomega.Equal(int64(1000)))
		gomega.Expect(phases["prepare"].BudgetSeconds).To(gomega.Equal(job.Deadlines.PrepareSeconds))
		gomega.Expect(phases["run"].BudgetSeconds).To(gomega.Equal(job.Deadlines.RunSeconds))
		before, err := os.ReadFile(filepath.Join(s.root.Path, "jobs", r.JobID, "output.log"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = s.collectVMLog(runner.GuestRequest{Request: runner.Request{Identity: managed.LocalIdentity{JobID: r.JobID}}}, time.Now().Add(time.Second))
		gomega.Expect(errors.Is(err, managed.ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(filepath.Join(s.root.Path, "jobs", r.JobID, "output.log"))).To(gomega.Equal(before))
		gomega.Expect(requestReceipts(db, r.JobID)).To(gomega.Equal(1))
	})

	ginkgo.It("accepts an exact pinned output byte fit and refuses one extra byte", func() {
		t := ginkgo.GinkgoT()
		_, s, r, job, object := pinnedLocalRuntime(t, "mkdir -p ../results; printf passed > ../results/output.txt", "exact-output", pinnedDocument(config.MaxLogBytes, 6, config.MaxArtifactCount))
		gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		result, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		gomega.Expect(result.Artifacts).To(gomega.ContainElement(gomega.HaveField("Path", "results/output.txt")))
		gomega.Expect(result.Artifacts[0].SizeBytes).To(gomega.Equal(int64(6)))
		gomega.Expect(os.ReadFile(filepath.Join(s.root.Path, "jobs", r.JobID, "artifacts/results/output.txt"))).To(gomega.Equal([]byte("passed")))

		_, s, r, job, object = pinnedLocalRuntime(t, "mkdir -p ../results; printf passed > ../results/output.txt", "over-output", pinnedDocument(config.MaxLogBytes, 5, config.MaxArtifactCount))
		gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		result, err = s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "results/output.txt", Reason: "required_output_uncollected"}))
		gomega.Expect(result.Artifacts).To(gomega.BeEmpty())
		_, err = os.Stat(filepath.Join(s.root.Path, "jobs", r.JobID, "artifacts/results/output.txt"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(filepath.Join(s.root.Path, "jobs", r.JobID, "workspace/results/output.txt"))).To(gomega.Equal([]byte("passed")))
		_, jobs, err := s.evidenceRecoveryBasis(job.ResourceID, "recollect")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(jobs).To(gomega.HaveLen(1))
		jobs[0].CollectDeadline = time.Now().Add(5 * time.Second)
		destination := filepath.Join(s.root.Path, "jobs", r.JobID, "recovery-pin")
		collected := (localEvidenceBackend{s}).Collect(jobs[0], destination)
		gomega.Expect(collected.complete).To(gomega.BeFalse())
		gomega.Expect(collected.artifacts).To(gomega.BeEmpty())
		info, err := os.Stat(destination)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(info.IsDir()).To(gomega.BeTrue())
		_, err = os.Stat(filepath.Join(destination, "results/output.txt"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(filepath.Join(s.root.Path, "jobs", r.JobID, "workspace/results/output.txt"))).To(gomega.Equal([]byte("passed")))
	})

	ginkgo.It("copies neither required file when the pinned output count is one short", func() {
		t := ginkgo.GinkgoT()
		_, s, r, job, object := pinnedLocalRuntime(t, "mkdir -p ../results; printf one > ../results/one.txt; printf two > ../results/two.txt", "count-output", pinnedDocument(config.MaxLogBytes, config.MaxArtifactBytes, 1), func(j *managed.JobSpec) {
			j.Workload.RequiredOutputs = []string{"results/one.txt", "results/two.txt"}
		})
		gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		result, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Artifacts).To(gomega.BeEmpty())
		gomega.Expect(result.EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "results/one.txt", Reason: "required_output_uncollected"}))
		gomega.Expect(result.EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "results/two.txt", Reason: "required_output_uncollected"}))
		for _, name := range []string{"artifacts/results/one.txt", "artifacts/results/two.txt"} {
			_, err = os.Stat(filepath.Join(s.root.Path, "jobs", r.JobID, name))
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		}
		gomega.Expect(os.ReadFile(filepath.Join(s.root.Path, "jobs", r.JobID, "workspace/results/one.txt"))).To(gomega.Equal([]byte("one")))
		gomega.Expect(os.ReadFile(filepath.Join(s.root.Path, "jobs", r.JobID, "workspace/results/two.txt"))).To(gomega.Equal([]byte("two")))
		_, jobs, err := s.evidenceRecoveryBasis(job.ResourceID, "recollect")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		jobs[0].CollectDeadline = time.Now().Add(5 * time.Second)
		destination := filepath.Join(s.root.Path, "jobs", r.JobID, "recovery-count")
		collected := (localEvidenceBackend{s}).Collect(jobs[0], destination)
		gomega.Expect(collected.complete).To(gomega.BeFalse())
		gomega.Expect(collected.artifacts).To(gomega.BeEmpty())
		_, err = os.Stat(destination)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})

	ginkgo.It("settles a wider saved request and refuses a pinned output above the durable recovery receipt bound", func() {
		t := ginkgo.GinkgoT()
		db, s, r, job, object := pinnedLocalRuntime(t, "mkdir -p results; printf passed > results/output.txt; printf 0123456789abcdef", "wide-request", pinnedDocument(8, 6, config.MaxArtifactCount))
		var err error
		r, err = s.ledger.AdvanceLocal(r.JobID, r.JobRevision, "preparing", "ready", "ready-wide-request")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		r, err = s.ledger.StartLocal(r.JobID, r.JobRevision, "run-wide-request")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		c, err := s.registerLocal(r)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer s.unregisterLocal(c)
		root := filepath.Join(s.root.Path, "jobs", r.JobID)
		work := filepath.Join(root, "workspace")
		gomega.Expect(os.MkdirAll(work, 0700)).To(gomega.Succeed())
		gomega.Expect(writeReceipt(root, "input.json", map[string]any{"identity": func() managed.LocalIdentity { id := c.identity; id.RunID = ""; return id }(), "snapshot_object_id": object})).To(gomega.Succeed())
		log, err := s.createRunnerFile(r.JobID, "output.log")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var st unix.Stat_t
		gomega.Expect(unix.Fstat(int(log.Fd()), &st)).To(gomega.Succeed())
		gomega.Expect(log.Close()).To(gomega.Succeed())
		boot, err := platform.BootIdentity()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q := runner.Request{Schema: managed.RunnerSchema("request"), Identity: c.identity, BootID: boot, WorkDir: work, Argv: job.Workload.Argv, Environment: []string{"PATH=/usr/bin:/bin"}, Deadline: time.Now().Add(time.Minute).UTC(), RunSeconds: job.Deadlines.RunSeconds, TerminationSeconds: job.Deadlines.CleanupSeconds, LogDevice: uint64(st.Dev), LogInode: uint64(st.Ino)}
		gomega.Expect(q.Validate(root)).To(gomega.Succeed())
		gomega.Expect(q.LogLimitBytes).To(gomega.BeZero())
		saved, err := json.Marshal(q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(saved)).NotTo(gomega.ContainSubstring("log_limit_bytes"))
		gomega.Expect(s.ledger.RecordRunnerReceipt(c.identity, "request", saved)).To(gomega.Succeed())
		next := pinnedDocument(config.MaxLogBytes, config.MaxArtifactBytes, config.MaxArtifactCount)
		next.Revision = 2
		_, err = s.ledger.CommitEffectiveConfig(next, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		bounds, err := s.ledger.ValidatedJobBounds(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(bounds.LogBytes).To(gomega.Equal(int64(8)))
		gomega.Expect(bounds.ArtifactBytes).To(gomega.Equal(int64(6)))
		stored, err := s.ledger.RunnerReceiptPayload(r.JobID, "request")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(stored).To(gomega.Equal(saved))
		gomega.Expect(runner.Prepare(root, q)).To(gomega.Succeed())
		c.runnerRequest = filepath.Join(root, "request.json")
		gomega.Expect(os.ReadFile(c.runnerRequest)).To(gomega.Equal(saved))
		executable, err := os.Executable()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		cmd := exec.Command(executable, "runner", "start", "--request", c.runnerRequest)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		cmd.Env = []string{"PATH=/usr/bin:/bin", "GORACE=atexit_sleep_ms=0"}
		gomega.Expect(cmd.Run()).To(gomega.Succeed())
		view, err := s.importRunner(c)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(view.Exit).NotTo(gomega.BeNil())
		gomega.Expect(view.Exit.TerminationProven).To(gomega.BeTrue())
		gomega.Expect(s.observeLocalCompletion(c)).To(gomega.Succeed())
		out, err := s.ledger.LocalOutcome(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(out.Started).To(gomega.BeTrue())
		gomega.Expect(out.WorkloadOutcome).To(gomega.Equal(managed.WorkloadOutcome("pass")))
		gomega.Expect(out.NativeExitCode).NotTo(gomega.BeNil())
		gomega.Expect(*out.NativeExitCode).To(gomega.Equal(0))
		current, err := s.ledger.CompleteLocalRun(managed.LocalTerminationRequest{Identity: c.identity, ExpectedRevision: c.receipt.JobRevision, MutationKey: "termination-wide-request"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		c.receipt = current
		gomega.Expect(s.settleLocalPhases(c, job, root, work, out, false, "", nil)).To(gomega.Succeed())
		stored, err = s.ledger.RunnerReceiptPayload(r.JobID, "request")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(stored).To(gomega.Equal(saved))
		gomega.Expect(os.ReadFile(c.runnerRequest)).To(gomega.Equal(saved))
		gomega.Expect(requestReceipts(db, r.JobID)).To(gomega.Equal(1))
		retained := []byte("0123456789abcdef")
		logPath := filepath.Join(root, "output.log")
		gomega.Expect(os.ReadFile(logPath)).To(gomega.Equal(retained))
		result, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.NativeExitCode).NotTo(gomega.BeNil())
		gomega.Expect(*result.NativeExitCode).To(gomega.Equal(0))
		gomega.Expect(result.Logs).To(gomega.BeEmpty())
		gomega.Expect(result.EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "output.log", Reason: "pinned_log_exceeded", Offset: int64(len(retained)), DroppedBytes: 0}))
		gomega.Expect(result.Artifacts).To(gomega.ContainElement(gomega.HaveField("SizeBytes", int64(6))))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.ProtectedEvidence).NotTo(gomega.BeEmpty())
		err = s.collectVMLog(runner.GuestRequest{Request: runner.Request{Identity: managed.LocalIdentity{JobID: r.JobID}}}, time.Now().Add(time.Second))
		gomega.Expect(errors.Is(err, managed.ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(logPath)).To(gomega.Equal(retained))
		status, err := s.ledger.ResourceStatus(job.ResourceID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(status.Availability).To(gomega.Equal("quarantined"))
		gomega.Expect(status.Occupancy).To(gomega.Equal(0))
		snap, jobs, err := s.evidenceRecoveryBasis(job.ResourceID, "recollect")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(jobs).To(gomega.HaveLen(1))
		gomega.Expect(jobs[0].Outputs).To(gomega.Equal([]string{"results/output.txt"}))
		bulk, err := s.ledger.BulkSnapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		now := time.Now().UTC()
		jobs[0].CollectDeadline = now.Add(time.Duration(bulk.Spec.Deadlines.CollectSeconds) * time.Second)
		jobs[0].CleanupDeadline = jobs[0].CollectDeadline.Add(time.Duration(bulk.Spec.Deadlines.CleanupSeconds) * time.Second)
		owner, err := platform.ObserveProcess(os.Getpid())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		intent := managed.EvidenceRecoveryIntent{Schema: "haepari.evidence-recovery-intent/v1", ResourceID: job.ResourceID, ResourceConfigRevision: snap.Resource.ConfigRevision, ResourceAvailabilityRevision: snap.Resource.AvailabilityRevision, SnapshotDigest: managed.RecoveryDigest(snap), ClientDigest: managed.RecoveryDigest("wide-request"), MutationKey: "wide-recovery", Action: "recollect", Owner: owner.Identity, Jobs: jobs, RecordedAt: now}
		gomega.Expect(s.ledger.BeginEvidenceRecovery(intent)).To(gomega.Succeed())
		var pinRequest, pinLog, pinArtifact, pinCount, pinEventDays, pinResultDays int64
		var pinRevision sql.NullInt64
		var pinDigest sql.NullString
		var pinSource string
		gomega.Expect(db.QueryRow(`SELECT request_bytes,log_bytes,artifact_bytes,artifact_count,event_retention_days,result_retention_days,config_revision,config_digest,policy_source FROM managed_job_bounds WHERE job_id=?`, r.JobID).Scan(&pinRequest, &pinLog, &pinArtifact, &pinCount, &pinEventDays, &pinResultDays, &pinRevision, &pinDigest, &pinSource)).To(gomega.Succeed())
		var availability, reason string
		var availabilityRevision, occupancy int64
		gomega.Expect(db.QueryRow(`SELECT availability,availability_reason,availability_revision,occupancy FROM managed_resources WHERE id=?`, job.ResourceID).Scan(&availability, &reason, &availabilityRevision, &occupancy)).To(gomega.Succeed())
		workspaceFile := filepath.Join(root, "workspace", "results", "output.txt")
		beforeWorkspace, err := os.ReadFile(workspaceFile)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		protected := result.ProtectedEvidence
		_, err = db.Exec(`DELETE FROM managed_job_bounds WHERE job_id=?`, r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = s.executeEvidenceRecovery(intent)
		gomega.Expect(errors.Is(err, managed.ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("job bounds are missing"))
		_, lostReceipt, lostFound, err := s.ledger.EvidenceRecovery(intent.MutationKey, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(lostFound).To(gomega.BeTrue())
		gomega.Expect(lostReceipt).To(gomega.BeNil())
		var availabilityAfter, reasonAfter string
		var revisionAfter, occupancyAfter int64
		gomega.Expect(db.QueryRow(`SELECT availability,availability_reason,availability_revision,occupancy FROM managed_resources WHERE id=?`, job.ResourceID).Scan(&availabilityAfter, &reasonAfter, &revisionAfter, &occupancyAfter)).To(gomega.Succeed())
		gomega.Expect(availabilityAfter).To(gomega.Equal(availability))
		gomega.Expect(reasonAfter).To(gomega.Equal(reason))
		gomega.Expect(revisionAfter).To(gomega.Equal(availabilityRevision))
		gomega.Expect(occupancyAfter).To(gomega.Equal(occupancy))
		gomega.Expect(os.ReadFile(workspaceFile)).To(gomega.Equal(beforeWorkspace))
		kept, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(kept.ProtectedEvidence).To(gomega.Equal(protected))
		_, err = os.Stat(filepath.Join(root, "recovery-"+intent.MutationKey))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		_, err = db.Exec(`INSERT INTO managed_job_bounds(job_id,request_bytes,log_bytes,artifact_bytes,artifact_count,event_retention_days,result_retention_days,config_revision,config_digest,policy_source) VALUES(?,?,?,?,?,?,?,?,?,?)`, r.JobID, pinRequest, pinLog, pinArtifact, pinCount, pinEventDays, pinResultDays, pinRevision, pinDigest, pinSource)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		restored, err := s.ledger.ValidatedJobBounds(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(restored.LogBytes).To(gomega.Equal(int64(8)))
		gomega.Expect(restored.ArtifactBytes).To(gomega.Equal(int64(6)))
		overArtifact := collectedPinReceipt(intent, 7, -1)
		gomega.Expect(errors.Is(s.ledger.RecordEvidenceRecovery(intent, overArtifact), managed.ErrInvalid)).To(gomega.BeTrue())
		overLog := collectedPinReceipt(intent, 6, 9)
		gomega.Expect(errors.Is(s.ledger.RecordEvidenceRecovery(intent, overLog), managed.ErrInvalid)).To(gomega.BeTrue())
		_, recorded, found, err := s.ledger.EvidenceRecovery(intent.MutationKey, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(found).To(gomega.BeTrue())
		gomega.Expect(recorded).To(gomega.BeNil())
		exact := collectedPinReceipt(intent, 6, 8)
		gomega.Expect(s.ledger.RecordEvidenceRecovery(intent, exact)).To(gomega.Succeed())
		_, recorded, found, err = s.ledger.EvidenceRecovery(intent.MutationKey, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(found).To(gomega.BeTrue())
		gomega.Expect(recorded).NotTo(gomega.BeNil())
		gomega.Expect(recorded.Jobs[0].Artifacts[0].SizeBytes).To(gomega.Equal(int64(6)))
		gomega.Expect(recorded.Jobs[0].GuestArtifacts[0].SizeBytes).To(gomega.Equal(int64(8)))
		again, err := s.ledger.ValidatedJobBounds(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(again.LogBytes).To(gomega.Equal(int64(8)))
		gomega.Expect(again.ArtifactBytes).To(gomega.Equal(int64(6)))
		stored, err = s.ledger.RunnerReceiptPayload(r.JobID, "request")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(stored).To(gomega.Equal(saved))
	})

	ginkgo.It("publishes nothing and keeps occupancy when pinned output bounds are missing or corrupt", func() {
		t := ginkgo.GinkgoT()
		for _, mode := range []string{"missing", "corrupt"} {
			db, s, r, job, object := pinnedLocalRuntime(t, "mkdir -p ../results; printf passed > ../results/output.txt", mode+"-pin", pinnedDocument(8, config.MaxArtifactBytes, config.MaxArtifactCount))
			var err error
			if mode == "missing" {
				_, err = db.Exec(`DELETE FROM managed_job_bounds WHERE job_id=?`, r.JobID)
			} else {
				_, err = db.Exec(`UPDATE managed_job_bounds SET log_bytes=log_bytes-1 WHERE job_id=?`, r.JobID)
			}
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			err = s.executeLocal(r, job, object, time.Now().Add(5*time.Second))
			gomega.Expect(errors.Is(err, managed.ErrBlocked)).To(gomega.BeTrue())
			s.failed(r.JobID, err)
			gomega.Expect(occupancyOf(db, r.JobID)).To(gomega.Equal(1))
			gomega.Expect(requestReceipts(db, r.JobID)).To(gomega.BeZero())
			var state string
			gomega.Expect(db.QueryRow(`SELECT state FROM managed_jobs WHERE id=?`, r.JobID).Scan(&state)).To(gomega.Succeed())
			gomega.Expect(state).NotTo(gomega.Equal("finished"))
			_, err = s.ledger.Result(r.JobID)
			gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
		}
	})

	ginkgo.It("omits a combined log limit field at 256MiB and fails closed without a saved request", func() {
		t := ginkgo.GinkgoT()
		db, s, r, job, object := pinnedLocalRuntime(t, "mkdir -p ../results; printf passed > ../results/output.txt", "full-log-pin", pinnedDocument(config.MaxLogBytes, config.MaxArtifactBytes, config.MaxArtifactCount))
		gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		payload, err := s.ledger.RunnerReceiptPayload(r.JobID, "request")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(payload)).NotTo(gomega.ContainSubstring("log_limit_bytes"))
		logPath := filepath.Join(s.root.Path, "jobs", r.JobID, "output.log")
		before, err := os.ReadFile(logPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`DELETE FROM managed_runner_receipts WHERE job_id=? AND kind='request'`, r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = s.collectVMLog(runner.GuestRequest{Request: runner.Request{Identity: managed.LocalIdentity{JobID: r.JobID}}}, time.Now().Add(time.Second))
		gomega.Expect(errors.Is(err, managed.ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(logPath)).To(gomega.Equal(before))
	})

	ginkgo.It("keeps a fake-native VM log limit pin, known exit, and admitted phase budget", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf passed > ../results/output.txt; printf 0123456789abcdef")
		_, err := f.s.ledger.CommitEffectiveConfig(pinnedDocument(8, config.MaxArtifactBytes, config.MaxArtifactCount), true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f.job.Deadlines.CollectSeconds = 1000
		result := f.run("vm-log-pin")
		gomega.Expect(result.NativeExitCode).NotTo(gomega.BeNil())
		gomega.Expect(*result.NativeExitCode).To(gomega.Equal(0))
		gomega.Expect(result.EvidenceCompleteness).To(gomega.Equal(managed.EvidenceCompleteness("partial")))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(result.ProtectedEvidence).NotTo(gomega.BeEmpty())
		gomega.Expect(result.Logs).To(gomega.HaveLen(1))
		gomega.Expect(result.Logs[0].SizeBytes).To(gomega.Equal(int64(8)))
		gomega.Expect(result.EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "output.log", Reason: "log_limit", Offset: 8, DroppedBytes: 8}))
		for _, gap := range result.EvidenceGaps {
			gomega.Expect(gap.Reason).NotTo(gomega.Equal("pinned_log_exceeded"))
		}
		gomega.Expect(os.ReadFile(filepath.Join(f.s.root.Path, "jobs", result.JobID, "output.log"))).To(gomega.Equal([]byte("01234567")))
		payload, err := f.s.ledger.RunnerReceiptPayload(result.JobID, "request")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(payload)).To(gomega.ContainSubstring(`"log_limit_bytes":8`))
		phases, err := f.s.ledger.LocalPhases(result.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases["collect"].BudgetSeconds).To(gomega.Equal(int64(1000)))
	})

	ginkgo.It("resumes an interrupted collect under the pinned restart log limit", func() {
		db, s, c, job, root, work, saved, out := widerPinnedCollect(ginkgo.GinkgoT(), "interrupted-restart")
		gomega.Expect(s.beginPhase(c, "collect", job.Deadlines.CollectSeconds, time.Now().Add(time.Duration(job.Deadlines.CollectSeconds)*time.Second))).To(gomega.Succeed())
		started, deadline := c.phases["collect"].StartedAt, c.phases["collect"].Deadline
		before := runnerReceiptSnapshot(db, c.identity.JobID)
		logBefore := readPinnedRestartFile(filepath.Join(root, "output.log"))
		requestBefore := readPinnedRestartFile(c.runnerRequest)
		workspaceBefore := readPinnedRestartFile(filepath.Join(work, "results", "output.txt"))
		requestClock := requestDeadline(saved)
		rawBefore, err := s.ledger.LocalPhases(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pinRestartEvidence(c)
		gomega.Expect(s.settleLocalPhases(c, job, root, work, out, false, "", nil)).To(gomega.Succeed())
		gomega.Expect(runnerReceiptSnapshot(db, c.identity.JobID)).To(gomega.Equal(before))
		gomega.Expect(readPinnedRestartFile(c.runnerRequest)).To(gomega.Equal(requestBefore))
		gomega.Expect(requestDeadline(requestBefore)).To(gomega.Equal(requestClock))
		gomega.Expect(readPinnedRestartFile(filepath.Join(root, "output.log"))).To(gomega.Equal(logBefore))
		gomega.Expect(readPinnedRestartFile(filepath.Join(work, "results", "output.txt"))).To(gomega.Equal(workspaceBefore))
		gomega.Expect(before["exit"]).NotTo(gomega.ContainSubstring("pinned_log_exceeded"))
		rawAfter, err := s.ledger.LocalPhases(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(rawAfter["collect"]).To(gomega.Equal(rawBefore["collect"]))
		gomega.Expect(rawAfter["collect"].StartedAt.Equal(started)).To(gomega.BeTrue())
		gomega.Expect(rawAfter["collect"].Deadline.Equal(deadline)).To(gomega.BeTrue())
		resolved := pinnedReconciledCollect(db, c.identity.JobID)
		gomega.Expect(resolved.OriginalDigest).To(gomega.Equal(managed.RecoveryDigest(rawBefore["collect"])))
		gomega.Expect(resolved.Record.StartedAt.Equal(rawBefore["collect"].StartedAt)).To(gomega.BeTrue())
		gomega.Expect(resolved.Record.Deadline.Equal(rawBefore["collect"].Deadline)).To(gomega.BeTrue())
		gomega.Expect(resolved.Record.Logs).To(gomega.BeEmpty())
		gomega.Expect(resolved.Record.EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "output.log", Reason: "pinned_log_exceeded", Offset: int64(len(logBefore)), DroppedBytes: 0}))
		gomega.Expect(resolved.Record.ProtectedEvidence).NotTo(gomega.BeEmpty())
		result, err := s.ledger.Result(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Logs).To(gomega.BeEmpty())
		gomega.Expect(result.EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "output.log", Reason: "pinned_log_exceeded", Offset: int64(len(logBefore)), DroppedBytes: 0}))
		gomega.Expect(result.NativeExitCode).NotTo(gomega.BeNil())
		gomega.Expect(*result.NativeExitCode).To(gomega.Equal(0))
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(occupancyOf(db, c.identity.JobID)).To(gomega.Equal(0))
		gomega.Expect(os.ReadFile(filepath.Join(work, "results", "output.txt"))).To(gomega.Equal(workspaceBefore))
	})

	ginkgo.It("keeps a pinned gap when an unproven collect resumes under the pinned restart log limit", func() {
		db, s, c, job, root, work, saved, out := widerPinnedCollect(ginkgo.GinkgoT(), "unproven-restart")
		logBefore := readPinnedRestartFile(filepath.Join(root, "output.log"))
		gap := managed.EvidenceGap{Path: "output.log", Reason: "pinned_log_exceeded", Offset: int64(len(logBefore)), DroppedBytes: 0}
		gomega.Expect(s.beginPhase(c, "collect", job.Deadlines.CollectSeconds, time.Now().Add(time.Duration(job.Deadlines.CollectSeconds)*time.Second))).To(gomega.Succeed())
		gomega.Expect(s.endPhase(c, "collect", managed.LocalPhase{Outcome: "unproven", EvidenceCompleteness: "partial", EvidenceGaps: []managed.EvidenceGap{gap}})).To(gomega.Succeed())
		started, deadline := c.phases["collect"].StartedAt, c.phases["collect"].Deadline
		before := runnerReceiptSnapshot(db, c.identity.JobID)
		requestBefore := readPinnedRestartFile(c.runnerRequest)
		workspaceBefore := readPinnedRestartFile(filepath.Join(work, "results", "output.txt"))
		requestClock := requestDeadline(saved)
		rawBefore, err := s.ledger.LocalPhases(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(rawBefore["collect"].EvidenceGaps).To(gomega.Equal([]managed.EvidenceGap{gap}))
		pinRestartEvidence(c)
		gomega.Expect(s.settleLocalPhases(c, job, root, work, out, false, "", nil)).To(gomega.Succeed())
		gomega.Expect(runnerReceiptSnapshot(db, c.identity.JobID)).To(gomega.Equal(before))
		gomega.Expect(readPinnedRestartFile(c.runnerRequest)).To(gomega.Equal(requestBefore))
		gomega.Expect(requestDeadline(requestBefore)).To(gomega.Equal(requestClock))
		gomega.Expect(readPinnedRestartFile(filepath.Join(root, "output.log"))).To(gomega.Equal(logBefore))
		gomega.Expect(readPinnedRestartFile(filepath.Join(work, "results", "output.txt"))).To(gomega.Equal(workspaceBefore))
		gomega.Expect(before["exit"]).NotTo(gomega.ContainSubstring("pinned_log_exceeded"))
		rawAfter, err := s.ledger.LocalPhases(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(rawAfter["collect"]).To(gomega.Equal(rawBefore["collect"]))
		resolved := pinnedReconciledCollect(db, c.identity.JobID)
		gomega.Expect(resolved.OriginalDigest).To(gomega.Equal(managed.RecoveryDigest(rawBefore["collect"])))
		gomega.Expect(resolved.Record.StartedAt.Equal(rawBefore["collect"].StartedAt)).To(gomega.BeTrue())
		gomega.Expect(resolved.Record.Deadline.Equal(rawBefore["collect"].Deadline)).To(gomega.BeTrue())
		gomega.Expect(resolved.Record.StartedAt.Equal(started)).To(gomega.BeTrue())
		gomega.Expect(resolved.Record.Deadline.Equal(deadline)).To(gomega.BeTrue())
		gomega.Expect(resolved.Record.Logs).To(gomega.BeEmpty())
		gomega.Expect(resolved.Record.EvidenceGaps).To(gomega.ContainElement(gap))
		gomega.Expect(resolved.Record.ProtectedEvidence).NotTo(gomega.BeEmpty())
		result, err := s.ledger.Result(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.Logs).To(gomega.BeEmpty())
		gomega.Expect(result.EvidenceGaps).To(gomega.ContainElement(gap))
		gomega.Expect(result.NativeExitCode).NotTo(gomega.BeNil())
		gomega.Expect(*result.NativeExitCode).To(gomega.Equal(0))
		gomega.Expect(occupancyOf(db, c.identity.JobID)).To(gomega.Equal(0))
	})

	ginkgo.It("refuses a pinned restart when the saved request is missing", func() {
		db, s, c, job, root, work, saved, out := widerPinnedCollect(ginkgo.GinkgoT(), "missing-restart")
		gomega.Expect(s.beginPhase(c, "collect", job.Deadlines.CollectSeconds, time.Now().Add(time.Duration(job.Deadlines.CollectSeconds)*time.Second))).To(gomega.Succeed())
		before := runnerReceiptSnapshot(db, c.identity.JobID)
		gomega.Expect(before["request"]).NotTo(gomega.BeEmpty())
		logBefore := readPinnedRestartFile(filepath.Join(root, "output.log"))
		requestBefore := readPinnedRestartFile(c.runnerRequest)
		workspaceBefore := readPinnedRestartFile(filepath.Join(work, "results", "output.txt"))
		requestClock := requestDeadline(saved)
		phasesBefore, err := s.ledger.LocalPhases(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var revision int64
		var state string
		gomega.Expect(db.QueryRow(`SELECT revision,state FROM managed_jobs WHERE id=?`, c.identity.JobID).Scan(&revision, &state)).To(gomega.Succeed())
		_, err = db.Exec(`DELETE FROM managed_runner_receipts WHERE job_id=? AND kind='request'`, c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		delete(before, "request")
		pinRestartEvidence(c)
		err = s.settleLocalPhases(c, job, root, work, out, false, "", nil)
		gomega.Expect(errors.Is(err, managed.ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(runnerReceiptSnapshot(db, c.identity.JobID)).To(gomega.Equal(before))
		gomega.Expect(readPinnedRestartFile(c.runnerRequest)).To(gomega.Equal(requestBefore))
		gomega.Expect(requestDeadline(requestBefore)).To(gomega.Equal(requestClock))
		gomega.Expect(readPinnedRestartFile(filepath.Join(root, "output.log"))).To(gomega.Equal(logBefore))
		gomega.Expect(readPinnedRestartFile(filepath.Join(work, "results", "output.txt"))).To(gomega.Equal(workspaceBefore))
		gomega.Expect(before["exit"]).NotTo(gomega.ContainSubstring("pinned_log_exceeded"))
		phases, err := s.ledger.LocalPhases(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases).To(gomega.Equal(phasesBefore))
		gomega.Expect(phases["collect"].Completed).To(gomega.BeFalse())
		_, ok := phases["cleanup"]
		gomega.Expect(ok).To(gomega.BeFalse())
		var afterRevision int64
		var afterState string
		gomega.Expect(db.QueryRow(`SELECT revision,state FROM managed_jobs WHERE id=?`, c.identity.JobID).Scan(&afterRevision, &afterState)).To(gomega.Succeed())
		gomega.Expect(afterRevision).To(gomega.Equal(revision))
		gomega.Expect(afterState).To(gomega.Equal(state))
		gomega.Expect(occupancyOf(db, c.identity.JobID)).To(gomega.Equal(1))
		_, err = s.ledger.Result(c.identity.JobID)
		gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
	})

	ginkgo.It("refuses a completed collect that exceeds the pinned restart bound", func() {
		db, s, c, job, root, work, saved, out := widerPinnedCollect(ginkgo.GinkgoT(), "completed-restart")
		logs, gaps, err := s.ledger.LocalLogManifest(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(gaps).To(gomega.BeEmpty())
		gomega.Expect(logs).To(gomega.HaveLen(1))
		logBefore := readPinnedRestartFile(filepath.Join(root, "output.log"))
		gomega.Expect(logs[0].SizeBytes).To(gomega.Equal(int64(len(logBefore))))
		gomega.Expect(logs[0].SizeBytes).To(gomega.BeNumerically(">", int64(8)))
		gomega.Expect(s.beginPhase(c, "collect", job.Deadlines.CollectSeconds, time.Now().Add(time.Duration(job.Deadlines.CollectSeconds)*time.Second))).To(gomega.Succeed())
		gomega.Expect(s.endPhase(c, "collect", managed.LocalPhase{Outcome: "complete", EvidenceCompleteness: "complete", Logs: logs})).To(gomega.Succeed())
		before := runnerReceiptSnapshot(db, c.identity.JobID)
		requestBefore := readPinnedRestartFile(c.runnerRequest)
		workspaceBefore := readPinnedRestartFile(filepath.Join(work, "results", "output.txt"))
		requestClock := requestDeadline(saved)
		phasesBefore, err := s.ledger.LocalPhases(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phasesBefore["collect"].Logs).To(gomega.Equal(logs))
		gomega.Expect(phasesBefore["collect"].EvidenceGaps).To(gomega.BeEmpty())
		var revision int64
		var state string
		gomega.Expect(db.QueryRow(`SELECT revision,state FROM managed_jobs WHERE id=?`, c.identity.JobID).Scan(&revision, &state)).To(gomega.Succeed())
		pinRestartEvidence(c)
		err = s.settleLocalPhases(c, job, root, work, out, false, "", nil)
		gomega.Expect(errors.Is(err, managed.ErrInvalid)).To(gomega.BeTrue())
		gomega.Expect(runnerReceiptSnapshot(db, c.identity.JobID)).To(gomega.Equal(before))
		gomega.Expect(readPinnedRestartFile(c.runnerRequest)).To(gomega.Equal(requestBefore))
		gomega.Expect(requestDeadline(requestBefore)).To(gomega.Equal(requestClock))
		gomega.Expect(readPinnedRestartFile(filepath.Join(root, "output.log"))).To(gomega.Equal(logBefore))
		gomega.Expect(readPinnedRestartFile(filepath.Join(work, "results", "output.txt"))).To(gomega.Equal(workspaceBefore))
		gomega.Expect(before["exit"]).NotTo(gomega.ContainSubstring("pinned_log_exceeded"))
		phases, err := s.ledger.LocalPhases(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases).To(gomega.Equal(phasesBefore))
		_, ok := phases["cleanup"]
		gomega.Expect(ok).To(gomega.BeFalse())
		var afterRevision int64
		var afterState string
		gomega.Expect(db.QueryRow(`SELECT revision,state FROM managed_jobs WHERE id=?`, c.identity.JobID).Scan(&afterRevision, &afterState)).To(gomega.Succeed())
		gomega.Expect(afterRevision).To(gomega.Equal(revision))
		gomega.Expect(afterState).To(gomega.Equal(state))
		gomega.Expect(occupancyOf(db, c.identity.JobID)).To(gomega.Equal(1))
		_, err = s.ledger.Result(c.identity.JobID)
		gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
	})

	ginkgo.It("refuses an over-log verified preterminal collect on pinned restart", func() {
		db, s, c, job, root, work, saved, out := seedVerifiedPreterminal(ginkgo.GinkgoT(), "verified-preterminal-over-log", verifiedPreterminalScript)
		logBefore := readPinnedRestartFile(filepath.Join(root, "output.log"))
		phasesBefore, err := s.ledger.LocalPhases(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phasesBefore["collect"].Logs).To(gomega.HaveLen(1))
		gomega.Expect(phasesBefore["collect"].Logs[0].SizeBytes).To(gomega.Equal(int64(len(logBefore))))
		gomega.Expect(phasesBefore["collect"].Logs[0].SizeBytes).To(gomega.Equal(int64(16)))
		gomega.Expect(phasesBefore["collect"].Logs[0].SizeBytes).To(gomega.BeNumerically(">", int64(8)))
		gomega.Expect(phasesBefore["collect"].Artifacts).To(gomega.Equal([]managed.ResultArtifact{{Path: "results/output.txt", SizeBytes: 6, SHA256: phasesBefore["collect"].Artifacts[0].SHA256}}))
		gomega.Expect(phasesBefore["collect"].Artifacts[0].SizeBytes).NotTo(gomega.BeNumerically(">", int64(6)))
		before := runnerReceiptSnapshot(db, c.identity.JobID)
		requestBefore := readPinnedRestartFile(c.runnerRequest)
		requestClock := requestDeadline(saved)
		receiptsBefore := phaseReceiptPayloads(db, c.identity.JobID)
		var revision int64
		var state, availability, availabilityReason string
		var availabilityRevision int64
		gomega.Expect(db.QueryRow(`SELECT revision,state FROM managed_jobs WHERE id=?`, c.identity.JobID).Scan(&revision, &state)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT availability,availability_reason,availability_revision FROM managed_resources WHERE id=?`, job.ResourceID).Scan(&availability, &availabilityReason, &availabilityRevision)).To(gomega.Succeed())
		_, err = os.Lstat(work)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		_, err = s.ledger.Result(c.identity.JobID)
		gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
		pinRestartEvidence(c)
		err = s.settleLocalPhases(c, job, root, work, out, false, "", nil)
		expectVerifiedPreterminalRefusal(db, s, c, err, managed.ErrInvalid)
		gomega.Expect(runnerReceiptSnapshot(db, c.identity.JobID)).To(gomega.Equal(before))
		gomega.Expect(readPinnedRestartFile(c.runnerRequest)).To(gomega.Equal(requestBefore))
		gomega.Expect(requestDeadline(requestBefore)).To(gomega.Equal(requestClock))
		gomega.Expect(readPinnedRestartFile(filepath.Join(root, "output.log"))).To(gomega.Equal(logBefore))
		_, err = os.Lstat(work)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		gomega.Expect(before["exit"]).NotTo(gomega.ContainSubstring("pinned_log_exceeded"))
		phases, err := s.ledger.LocalPhases(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases).To(gomega.Equal(phasesBefore))
		gomega.Expect(phaseReceiptPayloads(db, c.identity.JobID)).To(gomega.Equal(receiptsBefore))
		var afterRevision, afterAvailabilityRevision int64
		var afterState, afterAvailability, afterReason string
		gomega.Expect(db.QueryRow(`SELECT revision,state FROM managed_jobs WHERE id=?`, c.identity.JobID).Scan(&afterRevision, &afterState)).To(gomega.Succeed())
		gomega.Expect(afterRevision).To(gomega.Equal(revision))
		gomega.Expect(afterState).To(gomega.Equal(state))
		gomega.Expect(db.QueryRow(`SELECT availability,availability_reason,availability_revision FROM managed_resources WHERE id=?`, job.ResourceID).Scan(&afterAvailability, &afterReason, &afterAvailabilityRevision)).To(gomega.Succeed())
		gomega.Expect(afterAvailability).To(gomega.Equal(availability))
		gomega.Expect(afterReason).To(gomega.Equal(availabilityReason))
		gomega.Expect(afterAvailabilityRevision).To(gomega.Equal(availabilityRevision))
		gomega.Expect(occupancyOf(db, c.identity.JobID)).To(gomega.Equal(1))
		gomega.Expect(finishedEventCount(db, c.identity.JobID)).To(gomega.Equal(0))
		outcome, err := s.ledger.LocalOutcome(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(outcome.NativeExitCode).To(gomega.Equal(out.NativeExitCode))
	})

	ginkgo.It("refuses a missing saved request for a verified preterminal collect on pinned restart", func() {
		db, s, c, job, root, work, saved, out := seedVerifiedPreterminal(ginkgo.GinkgoT(), "verified-preterminal-missing-request", verifiedPreterminalScript)
		logBefore := readPinnedRestartFile(filepath.Join(root, "output.log"))
		phasesBefore, err := s.ledger.LocalPhases(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phasesBefore["collect"].Logs).NotTo(gomega.BeEmpty())
		gomega.Expect(phasesBefore["collect"].Artifacts).To(gomega.HaveLen(1))
		gomega.Expect(phasesBefore["collect"].Artifacts[0].Path).To(gomega.Equal("results/output.txt"))
		gomega.Expect(phasesBefore["collect"].Artifacts[0].SizeBytes).To(gomega.Equal(int64(6)))
		before := runnerReceiptSnapshot(db, c.identity.JobID)
		gomega.Expect(before["request"]).NotTo(gomega.BeEmpty())
		requestBefore := readPinnedRestartFile(c.runnerRequest)
		requestClock := requestDeadline(saved)
		receiptsBefore := phaseReceiptPayloads(db, c.identity.JobID)
		var revision int64
		var state, availability, availabilityReason string
		var availabilityRevision int64
		gomega.Expect(db.QueryRow(`SELECT revision,state FROM managed_jobs WHERE id=?`, c.identity.JobID).Scan(&revision, &state)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT availability,availability_reason,availability_revision FROM managed_resources WHERE id=?`, job.ResourceID).Scan(&availability, &availabilityReason, &availabilityRevision)).To(gomega.Succeed())
		_, err = db.Exec(`DELETE FROM managed_runner_receipts WHERE job_id=? AND kind='request'`, c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		delete(before, "request")
		_, err = os.Lstat(work)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		_, err = s.ledger.Result(c.identity.JobID)
		gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
		pinRestartEvidence(c)
		err = s.settleLocalPhases(c, job, root, work, out, false, "", nil)
		expectVerifiedPreterminalRefusal(db, s, c, err, managed.ErrBlocked)
		gomega.Expect(runnerReceiptSnapshot(db, c.identity.JobID)).To(gomega.Equal(before))
		gomega.Expect(readPinnedRestartFile(c.runnerRequest)).To(gomega.Equal(requestBefore))
		gomega.Expect(requestDeadline(requestBefore)).To(gomega.Equal(requestClock))
		gomega.Expect(readPinnedRestartFile(filepath.Join(root, "output.log"))).To(gomega.Equal(logBefore))
		_, err = os.Lstat(work)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		phases, err := s.ledger.LocalPhases(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases).To(gomega.Equal(phasesBefore))
		gomega.Expect(phaseReceiptPayloads(db, c.identity.JobID)).To(gomega.Equal(receiptsBefore))
		var afterRevision, afterAvailabilityRevision int64
		var afterState, afterAvailability, afterReason string
		gomega.Expect(db.QueryRow(`SELECT revision,state FROM managed_jobs WHERE id=?`, c.identity.JobID).Scan(&afterRevision, &afterState)).To(gomega.Succeed())
		gomega.Expect(afterRevision).To(gomega.Equal(revision))
		gomega.Expect(afterState).To(gomega.Equal(state))
		gomega.Expect(db.QueryRow(`SELECT availability,availability_reason,availability_revision FROM managed_resources WHERE id=?`, job.ResourceID).Scan(&afterAvailability, &afterReason, &afterAvailabilityRevision)).To(gomega.Succeed())
		gomega.Expect(afterAvailability).To(gomega.Equal(availability))
		gomega.Expect(afterReason).To(gomega.Equal(availabilityReason))
		gomega.Expect(afterAvailabilityRevision).To(gomega.Equal(availabilityRevision))
		gomega.Expect(occupancyOf(db, c.identity.JobID)).To(gomega.Equal(1))
		gomega.Expect(finishedEventCount(db, c.identity.JobID)).To(gomega.Equal(0))
		outcome, err := s.ledger.LocalOutcome(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(outcome.NativeExitCode).To(gomega.Equal(out.NativeExitCode))
	})

	ginkgo.It("commits one within-pin verified preterminal result on pinned restart and the finished repeat returns ErrBlocked before mutation replay", func() {
		db, s, c, job, root, work, saved, out := seedVerifiedPreterminal(ginkgo.GinkgoT(), "verified-preterminal-within-pin", withinPinScript)
		logBefore := readPinnedRestartFile(filepath.Join(root, "output.log"))
		phasesBefore, err := s.ledger.LocalPhases(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phasesBefore["collect"].Logs).To(gomega.HaveLen(1))
		gomega.Expect(phasesBefore["collect"].Logs[0].SizeBytes).To(gomega.Equal(int64(len(logBefore))))
		gomega.Expect(phasesBefore["collect"].Logs[0].SizeBytes).To(gomega.Equal(int64(8)))
		gomega.Expect(phasesBefore["collect"].Artifacts).To(gomega.HaveLen(1))
		gomega.Expect(phasesBefore["collect"].Artifacts[0].SizeBytes).To(gomega.Equal(int64(6)))
		before := runnerReceiptSnapshot(db, c.identity.JobID)
		requestBefore := readPinnedRestartFile(c.runnerRequest)
		requestClock := requestDeadline(saved)
		receiptsBefore := phaseReceiptPayloads(db, c.identity.JobID)
		_, err = os.Lstat(work)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		_, err = s.ledger.Result(c.identity.JobID)
		gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
		pinRestartEvidence(c)
		gomega.Expect(s.settleLocalPhases(c, job, root, work, out, false, "", nil)).To(gomega.Succeed())
		gomega.Expect(phaseReceiptPayloads(db, c.identity.JobID)).To(gomega.Equal(receiptsBefore))
		phases, err := s.ledger.LocalPhases(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases).To(gomega.Equal(phasesBefore))
		gomega.Expect(runnerReceiptSnapshot(db, c.identity.JobID)).To(gomega.Equal(before))
		gomega.Expect(readPinnedRestartFile(c.runnerRequest)).To(gomega.Equal(requestBefore))
		gomega.Expect(requestDeadline(requestBefore)).To(gomega.Equal(requestClock))
		gomega.Expect(readPinnedRestartFile(filepath.Join(root, "output.log"))).To(gomega.Equal(logBefore))
		_, err = os.Lstat(work)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		first, err := s.ledger.Result(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(first.NativeExitCode).NotTo(gomega.BeNil())
		gomega.Expect(*first.NativeExitCode).To(gomega.Equal(0))
		gomega.Expect(finishedEventCount(db, c.identity.JobID)).To(gomega.Equal(1))
		gomega.Expect(occupancyOf(db, c.identity.JobID)).To(gomega.Equal(0))
		finishedRepeat := s.settleLocalPhases(c, job, root, work, out, false, "", nil)
		gomega.Expect(errors.Is(finishedRepeat, managed.ErrBlocked)).To(gomega.BeTrue(), "finished repeat returns ErrBlocked before mutation replay: %v", finishedRepeat)
		second, err := s.ledger.Result(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(second).To(gomega.Equal(first))
		gomega.Expect(finishedEventCount(db, c.identity.JobID)).To(gomega.Equal(1))
		gomega.Expect(phaseReceiptPayloads(db, c.identity.JobID)).To(gomega.Equal(receiptsBefore))
		replayedPhases, err := s.ledger.LocalPhases(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replayedPhases).To(gomega.Equal(phasesBefore))
	})

	ginkgo.It("refuses an over-pin artifact for a verified preterminal collect on pinned restart", func() {
		db, s, c, job, root, work, saved, out := seedVerifiedPreterminal(ginkgo.GinkgoT(), "verified-preterminal-over-artifact", overArtifactScript)
		logBefore := readPinnedRestartFile(filepath.Join(root, "output.log"))
		phasesBefore, err := s.ledger.LocalPhases(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phasesBefore["collect"].Logs).To(gomega.HaveLen(1))
		gomega.Expect(phasesBefore["collect"].Logs[0].SizeBytes).To(gomega.Equal(int64(8)))
		gomega.Expect(phasesBefore["collect"].Logs[0].SizeBytes).NotTo(gomega.BeNumerically(">", int64(8)))
		gomega.Expect(phasesBefore["collect"].Artifacts).To(gomega.HaveLen(1))
		gomega.Expect(phasesBefore["collect"].Artifacts[0].Path).To(gomega.Equal("results/output.txt"))
		gomega.Expect(phasesBefore["collect"].Artifacts[0].SizeBytes).To(gomega.Equal(int64(7)))
		gomega.Expect(phasesBefore["collect"].Artifacts[0].SizeBytes).To(gomega.BeNumerically(">", int64(6)))
		before := runnerReceiptSnapshot(db, c.identity.JobID)
		requestBefore := readPinnedRestartFile(c.runnerRequest)
		requestClock := requestDeadline(saved)
		receiptsBefore := phaseReceiptPayloads(db, c.identity.JobID)
		var revision int64
		var state, availability, availabilityReason string
		var availabilityRevision int64
		gomega.Expect(db.QueryRow(`SELECT revision,state FROM managed_jobs WHERE id=?`, c.identity.JobID).Scan(&revision, &state)).To(gomega.Succeed())
		gomega.Expect(db.QueryRow(`SELECT availability,availability_reason,availability_revision FROM managed_resources WHERE id=?`, job.ResourceID).Scan(&availability, &availabilityReason, &availabilityRevision)).To(gomega.Succeed())
		_, err = os.Lstat(work)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		_, err = s.ledger.Result(c.identity.JobID)
		gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
		pinRestartEvidence(c)
		err = s.settleLocalPhases(c, job, root, work, out, false, "", nil)
		expectVerifiedPreterminalRefusal(db, s, c, err, managed.ErrInvalid)
		gomega.Expect(runnerReceiptSnapshot(db, c.identity.JobID)).To(gomega.Equal(before))
		gomega.Expect(readPinnedRestartFile(c.runnerRequest)).To(gomega.Equal(requestBefore))
		gomega.Expect(requestDeadline(requestBefore)).To(gomega.Equal(requestClock))
		gomega.Expect(readPinnedRestartFile(filepath.Join(root, "output.log"))).To(gomega.Equal(logBefore))
		_, err = os.Lstat(work)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		phases, err := s.ledger.LocalPhases(c.identity.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(phases).To(gomega.Equal(phasesBefore))
		gomega.Expect(phaseReceiptPayloads(db, c.identity.JobID)).To(gomega.Equal(receiptsBefore))
		var afterRevision, afterAvailabilityRevision int64
		var afterState, afterAvailability, afterReason string
		gomega.Expect(db.QueryRow(`SELECT revision,state FROM managed_jobs WHERE id=?`, c.identity.JobID).Scan(&afterRevision, &afterState)).To(gomega.Succeed())
		gomega.Expect(afterRevision).To(gomega.Equal(revision))
		gomega.Expect(afterState).To(gomega.Equal(state))
		gomega.Expect(db.QueryRow(`SELECT availability,availability_reason,availability_revision FROM managed_resources WHERE id=?`, job.ResourceID).Scan(&afterAvailability, &afterReason, &afterAvailabilityRevision)).To(gomega.Succeed())
		gomega.Expect(afterAvailability).To(gomega.Equal(availability))
		gomega.Expect(afterReason).To(gomega.Equal(availabilityReason))
		gomega.Expect(afterAvailabilityRevision).To(gomega.Equal(availabilityRevision))
		gomega.Expect(occupancyOf(db, c.identity.JobID)).To(gomega.Equal(1))
		gomega.Expect(finishedEventCount(db, c.identity.JobID)).To(gomega.Equal(0))
	})
})

const widerPinnedScript = "mkdir -p results; printf passed > results/output.txt; printf 0123456789abcdef"
const verifiedPreterminalScript = "mkdir -p ../results; printf passed > ../results/output.txt; printf 0123456789abcdef"
const withinPinScript = "mkdir -p ../results; printf passed > ../results/output.txt; printf 01234567"
const overArtifactScript = "mkdir -p ../results; printf 0123456 > ../results/output.txt; printf 01234567"

func widerPinnedCollect(t ginkgo.GinkgoTInterface, jobID string) (*sql.DB, *runtimeServer, *localRunControl, managed.JobSpec, string, string, []byte, managed.LocalOutcome) {
	t.Helper()
	return widerPinnedCollectScript(t, jobID, widerPinnedScript, "")
}

func widerPinnedCollectScript(t ginkgo.GinkgoTInterface, jobID, script, relativeCWD string) (*sql.DB, *runtimeServer, *localRunControl, managed.JobSpec, string, string, []byte, managed.LocalOutcome) {
	t.Helper()
	db, s, r, job, object := pinnedLocalRuntime(t, script, jobID, pinnedDocument(8, 6, config.MaxArtifactCount), func(spec *managed.JobSpec) {
		spec.Deadlines.CollectSeconds = 1000
	})
	var err error
	r, err = s.ledger.AdvanceLocal(r.JobID, r.JobRevision, "preparing", "ready", "ready-"+jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	r, err = s.ledger.StartLocal(r.JobID, r.JobRevision, "run-"+jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	c, err := s.registerLocal(r)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	t.Cleanup(func() { s.unregisterLocal(c) })
	root := filepath.Join(s.root.Path, "jobs", r.JobID)
	work := filepath.Join(root, "workspace")
	gomega.Expect(os.MkdirAll(work, 0700)).To(gomega.Succeed())
	workDir := work
	if relativeCWD != "" {
		workDir = filepath.Join(work, filepath.FromSlash(relativeCWD))
		gomega.Expect(os.MkdirAll(workDir, 0700)).To(gomega.Succeed())
	}
	gomega.Expect(writeReceipt(root, "input.json", map[string]any{"identity": func() managed.LocalIdentity { id := c.identity; id.RunID = ""; return id }(), "snapshot_object_id": object})).To(gomega.Succeed())
	log, err := s.createRunnerFile(r.JobID, "output.log")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var st unix.Stat_t
	gomega.Expect(unix.Fstat(int(log.Fd()), &st)).To(gomega.Succeed())
	gomega.Expect(log.Close()).To(gomega.Succeed())
	boot, err := platform.BootIdentity()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	q := runner.Request{Schema: managed.RunnerSchema("request"), Identity: c.identity, BootID: boot, WorkDir: workDir, Argv: job.Workload.Argv, Environment: []string{"PATH=/usr/bin:/bin"}, Deadline: time.Now().Add(time.Minute).UTC(), RunSeconds: job.Deadlines.RunSeconds, TerminationSeconds: job.Deadlines.CleanupSeconds, LogDevice: uint64(st.Dev), LogInode: uint64(st.Ino)}
	gomega.Expect(q.Validate(root)).To(gomega.Succeed())
	gomega.Expect(q.LogLimitBytes).To(gomega.BeZero())
	saved, err := json.Marshal(q)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(string(saved)).NotTo(gomega.ContainSubstring("log_limit_bytes"))
	gomega.Expect(s.ledger.RecordRunnerReceipt(c.identity, "request", saved)).To(gomega.Succeed())
	next := pinnedDocument(config.MaxLogBytes, config.MaxArtifactBytes, config.MaxArtifactCount)
	next.Revision = 2
	_, err = s.ledger.CommitEffectiveConfig(next, true)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	bounds, err := s.ledger.ValidatedJobBounds(r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(bounds.LogBytes).To(gomega.Equal(int64(8)))
	gomega.Expect(runner.Prepare(root, q)).To(gomega.Succeed())
	c.runnerRequest = filepath.Join(root, "request.json")
	gomega.Expect(os.ReadFile(c.runnerRequest)).To(gomega.Equal(saved))
	executable, err := os.Executable()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	cmd := exec.Command(executable, "runner", "start", "--request", c.runnerRequest)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = []string{"PATH=/usr/bin:/bin", "GORACE=atexit_sleep_ms=0"}
	gomega.Expect(cmd.Run()).To(gomega.Succeed())
	view, err := s.importRunner(c)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(view.Exit).NotTo(gomega.BeNil())
	gomega.Expect(view.Exit.TerminationProven).To(gomega.BeTrue())
	gomega.Expect(s.observeLocalCompletion(c)).To(gomega.Succeed())
	out, err := s.ledger.LocalOutcome(r.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(out.Started).To(gomega.BeTrue())
	gomega.Expect(out.NativeExitCode).NotTo(gomega.BeNil())
	gomega.Expect(*out.NativeExitCode).To(gomega.Equal(0))
	current, err := s.ledger.CompleteLocalRun(managed.LocalTerminationRequest{Identity: c.identity, ExpectedRevision: c.receipt.JobRevision, MutationKey: "termination-" + jobID})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	c.receipt = current
	return db, s, c, job, root, work, saved, out
}

func pinRestartEvidence(c *localRunControl) {
	evidence := recoveryObservation("pinned-restart", c.identity.JobID, "job:"+c.identity.JobID)
	c.recoveryEvidence = &evidence
}

func seedVerifiedPreterminal(t ginkgo.GinkgoTInterface, jobID, script string) (*sql.DB, *runtimeServer, *localRunControl, managed.JobSpec, string, string, []byte, managed.LocalOutcome) {
	t.Helper()
	db, s, c, job, root, work, saved, out := widerPinnedCollectScript(t, jobID, script, "source")
	logs, gaps, err := s.ledger.LocalLogManifest(c.identity.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(gaps).To(gomega.BeEmpty())
	gomega.Expect(logs).To(gomega.HaveLen(1))
	gomega.Expect(s.beginPhase(c, "collect", job.Deadlines.CollectSeconds, time.Now().Add(time.Duration(job.Deadlines.CollectSeconds)*time.Second))).To(gomega.Succeed())
	collected, complete, expired := collectOutputs(work, filepath.Join(root, "artifacts"), job.Workload.RequiredOutputs, c.phases["collect"].Deadline)
	gomega.Expect(expired).To(gomega.BeFalse())
	gomega.Expect(complete).To(gomega.BeTrue())
	gomega.Expect(collected).NotTo(gomega.BeEmpty())
	for _, item := range collected {
		copied := readPinnedRestartFile(filepath.Join(root, "artifacts", filepath.FromSlash(item.Path)))
		gomega.Expect(int64(len(copied))).To(gomega.Equal(item.SizeBytes))
		sum := sha256.Sum256(copied)
		gomega.Expect(hex.EncodeToString(sum[:])).To(gomega.Equal(item.SHA256))
	}
	gomega.Expect(s.endPhase(c, "collect", managed.LocalPhase{Outcome: "complete", EvidenceCompleteness: "complete", Logs: logs, Artifacts: collected})).To(gomega.Succeed())
	gomega.Expect(s.advancePhase(c, "collecting", "cleaning", "collected")).To(gomega.Succeed())
	gomega.Expect(s.beginPhase(c, "cleanup", job.Deadlines.CleanupSeconds, time.Now().Add(time.Duration(job.Deadlines.CleanupSeconds)*time.Second))).To(gomega.Succeed())
	gomega.Expect(s.cleanupLocalWorkspace(c.identity, job, out.Started, work, c.phases["cleanup"].Deadline)).To(gomega.Succeed())
	_, err = os.Lstat(work)
	gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	gomega.Expect(s.endPhase(c, "cleanup", managed.LocalPhase{Outcome: "complete"})).To(gomega.Succeed())
	gomega.Expect(s.advancePhase(c, "cleaning", "settling", "cleaned")).To(gomega.Succeed())
	gomega.Expect(s.beginPhase(c, "verify", 0, time.Time{})).To(gomega.Succeed())
	gomega.Expect(s.endPhase(c, "verify", managed.LocalPhase{Outcome: "complete"})).To(gomega.Succeed())
	phases, err := s.ledger.LocalPhases(c.identity.JobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	for _, name := range []string{"collect", "cleanup", "verify"} {
		gomega.Expect(phases[name].Completed).To(gomega.BeTrue())
		gomega.Expect(phases[name].Outcome).To(gomega.Equal("complete"))
		gomega.Expect(c.phases[name].Completed).To(gomega.BeTrue())
		gomega.Expect(c.phases[name].Outcome).To(gomega.Equal(phases[name].Outcome))
	}
	var state string
	gomega.Expect(db.QueryRow(`SELECT state FROM managed_jobs WHERE id=?`, c.identity.JobID).Scan(&state)).To(gomega.Succeed())
	gomega.Expect(state).To(gomega.Equal("settling"))
	_, err = s.ledger.Result(c.identity.JobID)
	gomega.Expect(errors.Is(err, managed.ErrBusy)).To(gomega.BeTrue())
	return db, s, c, job, root, work, saved, out
}

func expectVerifiedPreterminalRefusal(db *sql.DB, s *runtimeServer, c *localRunControl, err, target error) {
	_, resultErr := s.ledger.Result(c.identity.JobID)
	var state string
	gomega.Expect(db.QueryRow(`SELECT state FROM managed_jobs WHERE id=?`, c.identity.JobID).Scan(&state)).To(gomega.Succeed())
	gomega.Expect(errors.Is(err, target)).To(gomega.BeTrue(), "settle=%v resultErr=%v state=%s occupancy=%d", err, resultErr, state, occupancyOf(db, c.identity.JobID))
	gomega.Expect(errors.Is(resultErr, managed.ErrBusy)).To(gomega.BeTrue())
}

func phaseReceiptPayloads(db *sql.DB, jobID string) []string {
	rows, err := db.Query(`SELECT kind,payload_json FROM managed_events WHERE job_id=? AND kind IN ('phase_intent','phase_observed','phase_reconciled') ORDER BY cursor`, jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer rows.Close()
	var out []string
	for rows.Next() {
		var kind string
		var payload []byte
		gomega.Expect(rows.Scan(&kind, &payload)).To(gomega.Succeed())
		out = append(out, kind+"\n"+string(payload))
	}
	gomega.Expect(rows.Err()).NotTo(gomega.HaveOccurred())
	return out
}

func finishedEventCount(db *sql.DB, jobID string) int {
	var count int
	gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_events WHERE job_id=? AND kind='finished'`, jobID).Scan(&count)).To(gomega.Succeed())
	return count
}

func pinnedReconciledCollect(db *sql.DB, jobID string) managed.PhaseResolutionRequest {
	rows, err := db.Query(`SELECT payload_json FROM managed_events WHERE job_id=? AND kind='phase_reconciled' AND json_extract(payload_json,'$.record.phase')=? ORDER BY cursor`, jobID, "collect")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer rows.Close()
	var payloads [][]byte
	for rows.Next() {
		var payload []byte
		gomega.Expect(rows.Scan(&payload)).To(gomega.Succeed())
		payloads = append(payloads, append([]byte(nil), payload...))
	}
	gomega.Expect(rows.Err()).NotTo(gomega.HaveOccurred())
	gomega.Expect(payloads).To(gomega.HaveLen(1))
	var request managed.PhaseResolutionRequest
	gomega.Expect(json.Unmarshal(payloads[0], &request)).To(gomega.Succeed())
	gomega.Expect(request.Record.Phase).To(gomega.Equal("collect"))
	return request
}

func runnerReceiptSnapshot(db *sql.DB, jobID string) map[string]string {
	rows, err := db.Query(`SELECT kind,digest,payload_json FROM managed_runner_receipts WHERE job_id=? ORDER BY kind`, jobID)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var kind, digest string
		var payload []byte
		gomega.Expect(rows.Scan(&kind, &digest, &payload)).To(gomega.Succeed())
		out[kind] = digest + "\n" + string(payload)
	}
	gomega.Expect(rows.Err()).NotTo(gomega.HaveOccurred())
	return out
}

func readPinnedRestartFile(path string) []byte {
	data, err := os.ReadFile(path)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return data
}

func requestDeadline(payload []byte) time.Time {
	var saved struct {
		Deadline time.Time `json:"deadline"`
	}
	gomega.Expect(json.Unmarshal(payload, &saved)).To(gomega.Succeed())
	gomega.Expect(saved.Deadline.IsZero()).To(gomega.BeFalse())
	return saved.Deadline
}
