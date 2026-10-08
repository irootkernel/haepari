//go:build integration

package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

func guestCopyPublicationFixture(root, stage, savedDeadline string) int {
	deadline, err := time.Parse(time.RFC3339Nano, savedDeadline)
	if err != nil {
		return 90
	}
	data := []byte("retained")
	digest := sha256.Sum256(data)
	s := runtimeServer{root: state.Root{Path: root}}
	retentionBoundary = func(point string) {
		if point == stage {
			os.Exit(91)
		}
	}
	if stage == "fail-sync" {
		s.vmSyncParents = func(string, string) error { return unix.EIO }
	}
	q := runner.GuestRequest{}
	q.Request.Identity.JobID = "saved-copy"
	// Scripted source metadata isolates actual host publication and replay;
	// it does not qualify a guest or transport.
	call := func(runner.GuestRequest) (runner.GuestReply, error) {
		return runner.GuestReply{File: &runner.GuestFile{Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}, Output: data, NextOffset: int64(len(data))}, nil
	}
	target := filepath.Join(root, "jobs", "saved-copy", "recovery-fixture", "results", "output.txt")
	_, err = s.collectVMFileUsing(q, "results/output.txt", target, deadline, 1<<30, call)
	if stage == "fail-sync" && errors.Is(err, unix.EIO) {
		return 77
	}
	if err != nil {
		return 90
	}
	return 0
}

func recoveryPublicationFixture(path, stage string) int {
	exe, err := os.Executable()
	if err != nil {
		return 90
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return 90
	}
	retentionBoundary = func(point string) {
		if point == stage {
			os.Exit(91)
		}
	}
	if publishRecoveryRunner(path, data) != nil {
		return 90
	}
	return 0
}

func recoveryPublicationChild(path, stage string) {
	exe, err := os.Executable()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	child := exec.Command(exe, "guest-recovery-publication-fixture", path, stage)
	child.Env = []string{"PATH=/usr/bin:/bin", "GORACE=atexit_sleep_ms=0"}
	err = child.Run()
	gomega.Expect(err).To(gomega.HaveOccurred())
	gomega.Expect(child.ProcessState.ExitCode()).To(gomega.Equal(91))
}

var _ = ginkgo.Describe("GuestRecoveryPublication", ginkgo.Label("integration"), func() {
	ginkgo.It("completes the exact existing nested artifact sync chain after native publication death", func() {
		exe, err := os.Executable()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		root := ginkgo.GinkgoT().TempDir()
		saved := time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
		run := func(stage string, code int) {
			child := exec.Command(exe, "guest-copy-publication-fixture", root, stage, saved)
			child.Env = []string{"PATH=/usr/bin:/bin", "GORACE=atexit_sleep_ms=0"}
			err := child.Run()
			if code == 0 {
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			} else {
				gomega.Expect(err).To(gomega.HaveOccurred())
			}
			gomega.Expect(child.ProcessState.ExitCode()).To(gomega.Equal(code))
		}
		run("guest-copy-published", 91)
		target := filepath.Join(root, "jobs", "saved-copy", "recovery-fixture", "results", "output.txt")
		before, err := os.Stat(target)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		run("guest-copy-before-directory-sync", 91)
		run("fail-sync", 77)
		run("", 0)
		after, err := os.Stat(target)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.SameFile(before, after)).To(gomega.BeTrue())
		gomega.Expect(after.ModTime()).To(gomega.Equal(before.ModTime()))
		gomega.Expect(os.ReadFile(target)).To(gomega.Equal([]byte("retained")))
	})
	ginkgo.It("replays complete executable publication after native interruption before and after the final name", func() {
		exe, err := os.Executable()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		data, err := os.ReadFile(exe)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, stage := range []string{"guest-recovery-runner-created", "guest-recovery-runner-synced", "guest-recovery-runner-published"} {
			path := filepath.Join(ginkgo.GinkgoT().TempDir(), "saved-runner")
			recoveryPublicationChild(path, stage)
			if stage != "guest-recovery-runner-published" {
				_, err = os.Stat(path)
				gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			} else {
				gomega.Expect(os.ReadFile(path)).To(gomega.Equal(data))
			}
			gomega.Expect(publishRecoveryRunner(path, data)).To(gomega.Succeed())
			gomega.Expect(os.ReadFile(path)).To(gomega.Equal(data))
			gomega.Expect(publishRecoveryRunner(path, []byte("foreign"))).To(gomega.MatchError(managed.ErrConflict))
			gomega.Expect(os.ReadFile(path)).To(gomega.Equal(data))
		}
	})
})
