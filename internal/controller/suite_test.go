//go:build integration

package controller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	_ "unsafe"

	"github.com/irootkernel/haepari/internal/runner"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

//go:linkname runnerSpawnBoundary github.com/irootkernel/haepari/internal/runner.spawnBoundary
var runnerSpawnBoundary func(string)

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
		// The same after_spawn fault as the runner suite: the owned supervisor
		// exits at that boundary. It is not a production hook.
		if point, path := runnerFaultPoint(os.Args[2:]); point != "" {
			runnerSpawnBoundary = func(name string) {
				if name == point {
					os.Exit(91)
				}
			}
			os.Exit(runner.Command([]string{"start", "--request", path}, os.Stdout, os.Stderr))
		}
		os.Exit(runner.Command(os.Args[2:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func runnerFaultPoint(args []string) (string, string) {
	if len(args) != 3 || args[0] != "start" || args[1] != "--request" {
		return "", ""
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(args[2]), "after-spawn-fault"))
	if err != nil {
		return "", ""
	}
	point := strings.TrimSpace(string(raw))
	if point != "after_spawn" {
		return "", ""
	}
	return point, args[2]
}

func TestController(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Controller Suite")
}
