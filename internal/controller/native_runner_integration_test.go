//go:build integration

package controller

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var nativeGuestExecutable, nativeGuestExecutableRoot string

var _ = ginkgo.BeforeSuite(func() {
	// Build before reserving any Job. Transferring the complete controller
	// test suite repeatedly spends native observation budgets on Ginkgo
	// initialization and unrelated test code, not on the runner under test.
	root, err := os.MkdirTemp("/tmp", "hp-controller-runner-")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	nativeGuestExecutableRoot, err = filepath.EvalSymlinks(root)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	nativeGuestExecutable = filepath.Join(nativeGuestExecutableRoot, "haepari")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-race", "-ldflags=-s -w", "-o", nativeGuestExecutable, "./testdata/native-runner")
	output, err := cmd.CombinedOutput()
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s", output)
	metadata, err := exec.Command("go", "version", "-m", nativeGuestExecutable).CombinedOutput()
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s", metadata)
	gomega.Expect(string(metadata)).To(gomega.ContainSubstring("-race=true"))
	gomega.Expect(string(metadata)).NotTo(gomega.ContainSubstring("github.com/onsi/ginkgo"))
})

var _ = ginkgo.ReportAfterSuite("native guest executable cleanup", func(report ginkgo.Report) {
	if nativeGuestExecutableRoot == "" {
		return
	}
	if !report.SuiteSucceeded {
		ginkgo.GinkgoWriter.Printf("native guest executable retained: %s\n", nativeGuestExecutableRoot)
		return
	}
	gomega.Expect(os.RemoveAll(nativeGuestExecutableRoot)).To(gomega.Succeed())
})
