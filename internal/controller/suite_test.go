//go:build integration

package controller

import (
	"github.com/irootkernel/haepari/internal/runner"
	"os"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 5 && os.Args[1] == "guest-copy-publication-fixture" {
		os.Exit(guestCopyPublicationFixture(os.Args[2], os.Args[3], os.Args[4]))
	}
	if len(os.Args) == 4 && os.Args[1] == "guest-recovery-publication-fixture" {
		os.Exit(recoveryPublicationFixture(os.Args[2], os.Args[3]))
	}
	if len(os.Args) == 6 && os.Args[1] == "retention-restart-fixture" {
		os.Exit(retentionRestartFixture(os.Args[2], os.Args[3], os.Args[4], os.Args[5]))
	}
	if len(os.Args) == 5 && os.Args[1] == "restart-phase-fixture" {
		os.Exit(restartPhaseFixture(os.Args[2], os.Args[3], os.Args[4]))
	}
	if len(os.Args) > 1 && os.Args[1] == "runner" {
		os.Exit(runner.Command(os.Args[2:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func TestController(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Controller Suite")
}
