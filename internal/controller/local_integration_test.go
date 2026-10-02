//go:build integration

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

var _ = ginkgo.Describe("CopyInputRevalidatesSealedBytes", ginkgo.Label("integration"), func() {
	ginkgo.It("CopyInputRevalidatesSealedBytes", func() {
		t := ginkgo.GinkgoT()
		parent := t.TempDir()
		root := filepath.Join(parent, "sealed")
		if err := os.Mkdir(root, 0700); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		original := []byte("sealed bytes")
		path := filepath.Join(root, "input.txt")
		if err := os.WriteFile(path, original, 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		digest := sha256.Sum256(original)
		snap := snapshot.Snapshot{Root: root, Manifest: snapshot.Manifest{Entries: []snapshot.Entry{{Path: "input.txt", Size: int64(len(original)), SHA256: "sha256:" + hex.EncodeToString(digest[:])}}}}
		deadline := time.Now().Add(5 * time.Second)
		if err := copyInput(snap, filepath.Join(parent, "good"), deadline); err != nil {
			t.Fatalf("valid sealed copy: %v", err)
		}
		if got, err := os.ReadFile(filepath.Join(parent, "good", "input.txt")); err != nil || string(got) != string(original) {
			t.Fatalf("copied bytes: %q %v", got, err)
		}
		if err := os.WriteFile(path, []byte("altered byte"), 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := copyInput(snap, filepath.Join(parent, "changed"), deadline); !errors.Is(err, snapshot.ErrCorrupt) {
			t.Fatalf("changed bytes accepted: %v", err)
		}
		if err := os.Remove(path); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := os.Symlink(filepath.Join(parent, "good", "input.txt"), path); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := copyInput(snap, filepath.Join(parent, "linked"), deadline); !errors.Is(err, snapshot.ErrCorrupt) {
			t.Fatalf("symlink swap accepted: %v", err)
		}
		if err := os.Remove(path); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := os.WriteFile(path, original, 0700); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := copyInput(snap, filepath.Join(parent, "mode"), deadline); !errors.Is(err, snapshot.ErrCorrupt) {
			t.Fatalf("executable mode change accepted: %v", err)
		}
		if err := copyInput(snap, filepath.Join(parent, "expired"), time.Now().Add(-time.Second)); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expired preparation accepted: %v", err)
		}

	})
})

var _ = ginkgo.Describe("ResolveWorkloadCommandUsesDeclaredPath", ginkgo.Label("integration"), func() {
	ginkgo.It("ResolveWorkloadCommandUsesDeclaredPath", func() {
		t := ginkgo.GinkgoT()
		workDir := t.TempDir()
		bin := filepath.Join(workDir, "bin")
		if err := os.Mkdir(bin, 0700); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		tool := filepath.Join(bin, "selected-tool")
		if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf declared-path"), 0700); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Setenv("PATH", "/usr/bin:/bin")
		resolved, err := resolveWorkloadCommand("selected-tool", workDir, "bin")
		if err != nil || resolved != tool {
			t.Fatalf("workload PATH resolution: %q %v", resolved, err)
		}
		output, err := (&exec.Cmd{Path: resolved, Args: []string{"selected-tool"}, Dir: workDir, Env: []string{"PATH=bin"}}).Output()
		if err != nil || string(output) != "declared-path" {
			t.Fatalf("resolved workload execution: %q %v", output, err)
		}
		if _, err := resolveWorkloadCommand("selected-tool", workDir, "/usr/bin:/bin"); err == nil {
			t.Fatal("controller-only executable path was accepted")
		}

	})
})

var _ = ginkgo.Describe("PreparationDeadlineAndUnprovenWorkerKeepCorrectOwnership", ginkgo.Label("integration"), func() {
	ginkgo.It("PreparationDeadlineAndUnprovenWorkerKeepCorrectOwnership", func() {
		t := ginkgo.GinkgoT()
		for _, tc := range []struct {
			name, wantState, wantAvailability, wantReason string
			cause                                         error
			wantOccupancy                                 int
		}{
			{"expired", "finished", "available", "prepare_deadline", context.DeadlineExceeded, 0},
			{"unproven", "reconciling", "quarantined", "", errPrepareUnproven, 1},
		} {
			ginkgo.By(tc.name)
			func(t ginkgo.GinkgoTInterface) {
				parent, err := os.MkdirTemp("/tmp", "hp-prep-")
				if err != nil {
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				defer os.RemoveAll(parent)
				root, err := state.Resolve(filepath.Join(parent, "state"))
				if err != nil {
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				if err := root.Prepare(); err != nil {
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				db, err := sqlite.Open(root.DBPath)
				if err != nil {
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				defer db.Close()
				ledger, err := managed.NewLedger(db, 1)
				if err != nil {
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				resource := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "local-one", Kind: "local", ExecutionMode: "managed", Capacity: 1, Revision: 1, Adapter: managed.Adapter{QualificationReference: "isolated-integration"}, Cleanup: &managed.CleanupPolicy{PolicyReference: "owned-workspace"}}
				resourceJSON, _ := json.Marshal(resource)
				if err := ledger.RegisterOnlyLocal(resourceJSON); err != nil {
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				digest := hex.EncodeToString(make([]byte, 32))
				job := managed.JobSpec{Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: "local-one", Mode: "auto", Suite: "preparation", Input: managed.Input{Root: filepath.Join(root.Path, "source"), InventoryDigest: digest, SelectedFiles: []string{"source/input.txt"}}, Environment: managed.Environment{ProfileID: "local", ProfileDigest: digest}, Workload: managed.Workload{Argv: []string{"/bin/true"}, CWD: "source", RequiredOutputs: []string{"output.txt"}}, Deadlines: managed.Deadlines{QueueSeconds: 30, PrepareSeconds: 1, RunSeconds: 30, CollectSeconds: 30, CleanupSeconds: 30}, Effects: managed.Effects{Mode: "offline", AuthorizationReference: "isolated-fixture"}, Provenance: managed.Provenance{CandidateDigest: digest, TestsDigest: digest, TargetProfileDigest: digest, EvidenceClass: "development"}}
				jobJSON, _ := json.Marshal(job)
				admitted, err := ledger.Admit(jobJSON, digest, "prepare-key", "prepare-job")
				if err != nil {
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				reserved, err := ledger.Reserve(managed.ReserveRequest{JobID: admitted.JobID, ExpectedJobRevision: admitted.JobRevision, ExpectedConfigRevision: 1, ExpectedAvailabilityRevision: 1, MutationKey: "prepare-reserve"})
				if err != nil {
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				s, err := newRuntime(root, ledger, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				cause := tc.cause
				var release, finished chan struct{}
				if tc.name == "expired" {
					if err := s.executeLocal(reserved, job, "", time.Now().Add(-time.Second)); err != nil {
						t.Fatalf("expired preparation settlement failed: %v", err)
					}
				} else {
					release = make(chan struct{})
					finished = make(chan struct{})
					defer func() {
						select {
						case <-release:
						default:
							close(release)
						}
					}()
					workspace := filepath.Join(root.Path, "jobs", reserved.JobID, "workspace")
					cause = prepareLocal(time.Now().Add(200*time.Millisecond), func() error {
						defer close(finished)
						if err := os.MkdirAll(workspace, 0700); err != nil {
							return err
						}
						if err := os.WriteFile(filepath.Join(workspace, "before-timeout"), []byte("owned"), 0600); err != nil {
							return err
						}
						<-release
						return os.WriteFile(filepath.Join(workspace, "after-timeout"), []byte("owned"), 0600)
					})
					if !errors.Is(cause, errPrepareUnproven) {
						t.Fatalf("blocked preparation was not unproven: %v", cause)
					}
				}
				s.failed(reserved.JobID, cause)
				got, err := ledger.Snapshot(reserved.JobID)
				if err != nil {
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				if got.JobState != tc.wantState || got.ResourceAvailability != tc.wantAvailability || got.GlobalOccupancy != tc.wantOccupancy {
					t.Fatalf("preparation ownership: %+v", got)
				}
				if tc.wantReason != "" {
					result, err := ledger.Result(reserved.JobID)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Expect(result.Reason).To(gomega.Equal(tc.wantReason))
					gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Incomplete))
					gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("not_needed")))
					_, err = os.Lstat(filepath.Join(root.Path, "jobs", reserved.JobID))
					gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
				}
				var claims int
				if err := db.QueryRow(`SELECT COUNT(*) FROM managed_runs WHERE job_id=?`, reserved.JobID).Scan(&claims); err != nil || claims != 0 {
					t.Fatalf("preparation claimed an execution: %d %v", claims, err)
				}
				if release != nil {
					workspace := filepath.Join(root.Path, "jobs", reserved.JobID, "workspace")
					if _, err := os.Stat(filepath.Join(workspace, "before-timeout")); err != nil {
						t.Fatalf("unproven workspace removed: %v", err)
					}
					close(release)
					select {
					case <-finished:
					case <-time.After(time.Second):
						t.Fatal("preparation worker did not finish")
					}
					if _, err := os.Stat(filepath.Join(workspace, "after-timeout")); err != nil {
						t.Fatalf("workspace lost while unproven worker was active: %v", err)
					}
				}
			}(t)
		}

	})
})

var _ = ginkgo.Describe("CollectorRejectsSymlinkedOutput", ginkgo.Label("integration"), func() {
	ginkgo.It("CollectorRejectsSymlinkedOutput", func() {
		t := ginkgo.GinkgoT()
		parent := t.TempDir()
		workspace := filepath.Join(parent, "workspace")
		if err := os.MkdirAll(filepath.Join(workspace, "results"), 0700); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		outside := filepath.Join(parent, "outside.txt")
		if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := os.Symlink(outside, filepath.Join(workspace, "results", "output.txt")); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		artifacts, complete, expired := collectOutputs(workspace, filepath.Join(parent, "artifacts"), []string{"results/output.txt"}, time.Now().Add(time.Second))
		if complete || expired || len(artifacts) != 0 {
			t.Fatalf("followed symlinked result: %+v", artifacts)
		}
		if _, err := os.Stat(filepath.Join(parent, "artifacts", "results", "output.txt")); !os.IsNotExist(err) {
			t.Fatalf("external file was collected: %v", err)
		}

	})
})

var _ = ginkgo.Describe("OwnedBackgroundGroupIsStopped", ginkgo.Label("integration"), func() {
	ginkgo.It("OwnedBackgroundGroupIsStopped", func() {
		t := ginkgo.GinkgoT()
		cmd := exec.Command("/bin/sh", "-c", "sleep 30 >/dev/null 2>&1 &")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		pgid := cmd.Process.Pid
		if err := cmd.Wait(); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := ensureGroupStopped(pgid, 5*time.Second); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("owned group still present: %v", err)
		}

	})
})

var _ = ginkgo.Describe("CollectorDoesNotBlockOnFIFO", ginkgo.Label("integration"), func() {
	ginkgo.It("CollectorDoesNotBlockOnFIFO", func() {
		t := ginkgo.GinkgoT()
		parent := t.TempDir()
		workspace := filepath.Join(parent, "workspace")
		if err := os.MkdirAll(filepath.Join(workspace, "results"), 0700); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := unix.Mkfifo(filepath.Join(workspace, "results", "output.txt"), 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		done := make(chan bool, 1)
		go func() {
			artifacts, complete, expired := collectOutputs(workspace, filepath.Join(parent, "artifacts"), []string{"results/output.txt"}, time.Now().Add(time.Second))
			done <- (!complete && !expired && len(artifacts) == 0)
		}()
		select {
		case correct := <-done:
			if !correct {
				t.Fatal("FIFO accepted as a regular artifact")
			}
		case <-time.After(time.Second):
			t.Fatal("FIFO opened in blocking mode")
		}

	})
})

var _ = ginkgo.Describe("CollectionAndCleanupDeadlines", ginkgo.Label("integration"), func() {
	ginkgo.It("CollectionAndCleanupDeadlines", func() {
		t := ginkgo.GinkgoT()
		parent := t.TempDir()
		workspace := filepath.Join(parent, "workspace")
		if err := os.MkdirAll(filepath.Join(workspace, "results"), 0700); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := os.WriteFile(filepath.Join(workspace, "results", "output.txt"), []byte("keep"), 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		artifacts, complete, expired := collectOutputs(workspace, filepath.Join(parent, "artifacts"), []string{"results/output.txt"}, time.Now().Add(-time.Second))
		if complete || !expired || len(artifacts) != 0 {
			t.Fatalf("expired collection accepted: %+v, %t, %t", artifacts, complete, expired)
		}
		if err := removeTreeUntil(workspace, time.Now().Add(-time.Second)); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expired cleanup: %v", err)
		}
		if _, err := os.Stat(filepath.Join(workspace, "results", "output.txt")); err != nil {
			t.Fatalf("deadline removed protected output: %v", err)
		}
		if err := removeTreeUntil(workspace, time.Now().Add(time.Second)); err != nil {
			t.Fatalf("bounded cleanup: %v", err)
		}
		if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
			t.Fatalf("cleanup left workspace: %v", err)
		}

	})
})

var _ = ginkgo.Describe("BlockedPhaseReturnsUnprovenAtDeadline", ginkgo.Label("integration"), func() {
	ginkgo.It("BlockedPhaseReturnsUnprovenAtDeadline", func() {
		t := ginkgo.GinkgoT()
		blocked := make(chan struct{})
		finished := make(chan struct{})
		start := time.Now()
		_, status := boundedPhase(start.Add(20*time.Millisecond), func() error {
			<-blocked
			close(finished)
			return nil
		})
		if status != phaseUnproven || time.Since(start) > time.Second {
			t.Fatal("blocked filesystem phase was reported stopped or exceeded watchdog budget")
		}
		select {
		case <-finished:
			t.Fatal("phase was reported unproven but already completed")
		default:
		}
		close(blocked)
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("phase worker did not finish after release")
		}

	})
})

var _ = ginkgo.Describe("LateCompletedPhaseIsNotVerified", ginkgo.Label("integration"), func() {
	ginkgo.It("LateCompletedPhaseIsNotVerified", func() {
		t := ginkgo.GinkgoT()
		deadline := time.Now()
		if got := completionStatus(deadline.Add(time.Nanosecond), deadline); got != phaseLate {
			t.Fatalf("late completion status: %v", got)
		}
		if got := completionStatus(deadline.Add(-time.Nanosecond), deadline); got != phaseInTime {
			t.Fatalf("on-time completion status: %v", got)
		}

	})
})
