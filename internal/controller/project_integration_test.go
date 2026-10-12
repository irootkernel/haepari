//go:build integration

package controller

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task017NativeGuestProject", ginkgo.Label("integration"), func() {
	ginkgo.It("runs the actual project caller and guest script through the fake/native guest", func() {
		t := ginkgo.GinkgoT()
		f := nativeVMFixture(t, "unused")
		prior, err := f.s.ledger.Snapshot("queued-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = f.s.ledger.Cancel(managed.CancelRequest{JobID: prior.JobID, ExpectedRevision: prior.JobRevision, MutationKey: "project-fixture-queue"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		f.s.doctorHostSession = func(context.Context) error { return nil }
		original := f.s.sshCommand
		f.s.sshCommand = func(ctx context.Context, binary string, args []string, input []byte) (utm.CommandResult, error) {
			if strings.Contains(args[len(args)-1], "/sbin/ifconfig -a") {
				command := exec.CommandContext(ctx, "/bin/sh", "-c", args[len(args)-1])
				command.Stdin = strings.NewReader(string(input))
				out, err := command.Output()
				return utm.CommandResult{Stdout: string(out)}, err
			}
			return original(ctx, binary, args, input)
		}
		root, err := filepath.EvalSymlinks(t.TempDir())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		project := filepath.Join(root, "project")
		gomega.Expect(os.Mkdir(project, 0700)).To(gomega.Succeed())
		for _, name := range []string{"Makefile", "host.py", "project.json", "profile.json", "source/input.txt", "tests/vm/run.sh", "scenarios/check.md"} {
			path := filepath.Join(project, name)
			gomega.Expect(os.MkdirAll(filepath.Dir(path), 0700)).To(gomega.Succeed())
			data, err := os.ReadFile(filepath.Join("../../examples/project-test", name))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			mode := os.FileMode(0600)
			if name == "tests/vm/run.sh" {
				mode = 0700
			}
			gomega.Expect(os.WriteFile(path, data, mode)).To(gomega.Succeed())
		}
		binary := filepath.Join(root, "haepari")
		build := exec.Command("go", "build", "-o", binary, "./cmd/haepari")
		build.Dir = "../.."
		out, err := build.CombinedOutput()
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s", out)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); f.s.schedule(ctx) }()
		defer func() { cancel(); <-done; f.s.workers.Wait() }()
		run := func(name string, expected int) string {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "make", "test-vm", "HAEPARI="+binary,
				"RESOURCE="+f.spec.ID, "RUN_DIR="+filepath.Join(root, name))
			command.Dir = project
			command.Env = append(os.Environ(), "HAEPARI_HOME="+f.s.root.Path)
			out, err := command.CombinedOutput()
			if expected == 0 {
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s", out)
			} else {
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(command.ProcessState.ExitCode()).To(gomega.Equal(expected), "%s", out)
			}
			var saved struct {
				Jobs []struct {
					JobID string `json:"job_id"`
				} `json:"jobs"`
			}
			data, err := os.ReadFile(filepath.Join(root, name, "run.json"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(json.Unmarshal(data, &saved)).To(gomega.Succeed())
			gomega.Expect(saved.Jobs).To(gomega.HaveLen(1))
			return saved.Jobs[0].JobID
		}
		first := run("native-success", 0)
		gomega.Expect(run("native-success", 0)).To(gomega.Equal(first))
		result, err := f.s.ledger.Result(first)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Pass))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(result.Artifacts).To(gomega.ContainElement(gomega.HaveField("Path", "results/out.txt")))
		gomega.Expect(os.WriteFile(filepath.Join(project, "source/input.txt"), []byte("wrong\n"), 0600)).To(gomega.Succeed())
		failed := run("native-failure", 2)
		result, err = f.s.ledger.Result(failed)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(result.FinalVerdict).To(gomega.Equal(managed.Fail))
		gomega.Expect(*result.NativeExitCode).To(gomega.Equal(7))
		gomega.Expect(result.CleanupOutcome).To(gomega.Equal(managed.CleanupOutcome("verified")))
		gomega.Expect(run("native-failure", 2)).To(gomega.Equal(failed))
		gomega.Expect(run("native-success", 0)).To(gomega.Equal(first))
		gomega.Expect(f.control.effects).To(gomega.BeEmpty())
		entries, err := os.ReadDir(f.guest)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(entries).To(gomega.HaveLen(2))
		for _, entry := range entries {
			// Fenced control metadata deliberately survives owned Job cleanup.
			gomega.Expect(entry.IsDir()).To(gomega.BeFalse())
			gomega.Expect(entry.Name()).To(gomega.BeElementOf(".haepari-control-"+first+".json", ".haepari-control-"+failed+".json"))
		}
	})
})
