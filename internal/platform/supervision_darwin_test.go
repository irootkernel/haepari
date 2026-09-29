//go:build darwin && macos_supervision

package platform

import (
	"errors"
	"fmt"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const probeEnv = "HAEPARI_TASK004_PROBE"

func TestMain(m *testing.M) {
	mode := os.Getenv(probeEnv)
	if mode == "" {
		os.Exit(m.Run())
	}
	root := os.Getenv("HAEPARI_TASK004_ROOT")
	switch mode {
	case "controller":
		os.Exit(probeController(root))
	case "runner":
		os.Exit(probeRunner(root))
	case "group-parent":
		os.Exit(probeGroupParent(root))
	default:
		os.Exit(2)
	}
}

func probeGroupParent(root string) int {
	child := exec.Command("/bin/sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return 1
	}
	if err := os.WriteFile(filepath.Join(root, "escaped.pid"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		return 1
	}
	time.Sleep(30 * time.Second)
	return 0
}

func probeController(root string) int {
	if err := os.WriteFile(filepath.Join(root, "controller.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		return 1
	}
	if _, err := os.Stat(filepath.Join(root, "runner.pid")); err == nil {
		if err := os.WriteFile(filepath.Join(root, "restarted"), []byte(strconv.Itoa(os.Getpid())), 0600); err == nil {
			return 0
		}
		return 1
	}
	bin, err := os.Executable()
	if err != nil {
		return 1
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), probeEnv+"=runner", "HAEPARI_TASK004_ROOT="+root)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 1
	}
	if err := os.WriteFile(filepath.Join(root, "spawned.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return 1
	}
	// The runner writes its identity only after it has entered its own session.
	return 0
}

func probeRunner(root string) int {
	pid := os.Getpid()
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid != pid {
		return 1
	}
	if err := os.WriteFile(filepath.Join(root, "runner.pid"), []byte(strconv.Itoa(pid)), 0600); err != nil {
		return 1
	}
	for deadline := time.Now().Add(45 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(filepath.Join(root, "release")); err == nil {
			return 0
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 1
}

var _ = ginkgo.Describe("AquaLaunchAgentLeavesIndependentRunnerAlive", ginkgo.Label("macos-supervision"), func() {
	ginkgo.It("AquaLaunchAgentLeavesIndependentRunnerAlive", func() {
		t := ginkgo.GinkgoT()
		root, err := os.MkdirTemp("/tmp", "hp-task004-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := os.Chmod(root, 0700); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		label := fmt.Sprintf("io.rootkernel.haepari.task004.%d", os.Getpid())
		target := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
		bin, err := os.Executable()
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		plist := filepath.Join(root, "probe.plist")
		data := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string></array>
<key>EnvironmentVariables</key><dict>
<key>%s</key><string>controller</string>
<key>HAEPARI_TASK004_ROOT</key><string>%s</string>
</dict>
<key>RunAtLoad</key><true/>
<key>LimitLoadToSessionType</key><string>Aqua</string>
<key>AbandonProcessGroup</key><true/>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>`, label, xmlText(bin), probeEnv, xmlText(root), xmlText(filepath.Join(root, "stdout.log")), xmlText(filepath.Join(root, "stderr.log")))
		if err := os.WriteFile(plist, []byte(data), 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		bootstrapped := false
		var runnerPID int
		t.Cleanup(func() {
			preserve := false
			if err := os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
				t.Errorf("release runner: %v", err)
				preserve = true
			}
			if bootstrapped {
				if out, err := exec.Command("/bin/launchctl", "bootout", target).CombinedOutput(); err != nil {
					t.Errorf("bootout Aqua probe: %v: %s", err, out)
					preserve = true
				}
			}
			if runnerPID == 0 {
				runnerPID, _ = readProbePID(root, "runner.pid", "spawned.pid")
			}
			if runnerPID > 0 {
				if err := waitProbeGroupGone(runnerPID, 2*time.Second); err != nil {
					t.Errorf("runner %d did not exit after release: %v", runnerPID, err)
					if pgid, lookupErr := syscall.Getpgid(runnerPID); lookupErr == nil && pgid == runnerPID {
						_ = syscall.Kill(-runnerPID, syscall.SIGTERM)
						if err := waitProbeGroupGone(runnerPID, 2*time.Second); err != nil {
							t.Errorf("runner %d remains after SIGTERM: %v", runnerPID, err)
						}
					}
					preserve = true
				}
			} else if bootstrapped {
				t.Errorf("runner identity unavailable; retained probe directory %s for recovery (runner exits within 45 seconds)", root)
				preserve = true
			}
			if !preserve {
				_ = os.RemoveAll(root)
			}
		})
		if out, err := exec.Command("/bin/launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plist).CombinedOutput(); err != nil {
			t.Fatalf("bootstrap Aqua probe: %v: %s", err, out)
		}
		bootstrapped = true
		if err := waitProbeFile(filepath.Join(root, "runner.pid")); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		pidText, err := os.ReadFile(filepath.Join(root, "runner.pid"))
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		runnerPID, err = strconv.Atoi(string(pidText))
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		assertProbeRunner(t, runnerPID)
		controllerPID, err := readProbePID(root, "controller.pid")
		if err != nil {
			t.Fatalf("initial controller identity: %v", err)
		}
		// The runner can become ready before the controller has exited, especially
		// with the race detector's exit delay. Kickstart does not restart a live service.
		if err := waitProbeGroupGone(controllerPID, 5*time.Second); err != nil {
			t.Fatalf("initial controller did not exit before restart: %v", err)
		}
		assertProbeRunner(t, runnerPID)
		if out, err := exec.Command("/bin/launchctl", "kickstart", target).CombinedOutput(); err != nil {
			t.Fatalf("restart Aqua probe: %v: %s", err, out)
		}
		if err := waitProbeFile(filepath.Join(root, "restarted")); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		restartedPID, err := readProbePID(root, "restarted")
		if err != nil || restartedPID == controllerPID {
			t.Fatalf("controller restart identity: initial=%d restarted=%d err=%v", controllerPID, restartedPID, err)
		}
		if err := waitProbeGroupGone(restartedPID, 5*time.Second); err != nil {
			t.Fatalf("restarted controller did not exit: %v", err)
		}
		assertProbeRunner(t, runnerPID)
		currentPID, err := os.ReadFile(filepath.Join(root, "runner.pid"))
		if err != nil || string(currentPID) != string(pidText) {
			t.Fatalf("runner identity changed on controller restart: %q, %v", currentPID, err)
		}
		t.Logf("Aqua LaunchAgent %s exited and restarted; independent runner pid/pgid %d survived", label, runnerPID)

	})
})

var _ = ginkgo.Describe("DarwinUnixSocketPathLimit", ginkgo.Label("macos-supervision"), func() {
	ginkgo.It("DarwinUnixSocketPathLimit", func() {
		t := ginkgo.GinkgoT()
		root, err := os.MkdirTemp("/tmp", "hp-sock-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(root) })
		path := func(length int) string {
			return filepath.Join(root, strings.Repeat("s", length-len(root)-1))
		}
		short := path(103)
		listener, err := net.Listen("unix", short)
		if err != nil {
			t.Fatalf("103-byte Unix socket path: %v", err)
		}
		if err := listener.Close(); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := net.Listen("unix", path(104)); err == nil {
			t.Fatal("104-byte Unix socket path was accepted")
		}
		t.Log("Darwin accepted a 103-byte Unix socket path and rejected a 104-byte path")

	})
})

var _ = ginkgo.Describe("DarwinFullFsyncAvailable", ginkgo.Label("macos-supervision"), func() {
	ginkgo.It("DarwinFullFsyncAvailable", func() {
		t := ginkgo.GinkgoT()
		file, err := os.CreateTemp("/tmp", "hp-fullfsync-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.Remove(file.Name()) })
		defer file.Close()
		if _, err := file.WriteString("task004"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := unix.FcntlInt(file.Fd(), unix.F_FULLFSYNC, 0); err != nil {
			t.Fatalf("F_FULLFSYNC on local probe file: %v", err)
		}
		t.Log("F_FULLFSYNC succeeded on the local /tmp filesystem")

	})
})

var _ = ginkgo.Describe("EscapedDescendantIsOutsideOriginalProcessGroup", ginkgo.Label("macos-supervision"), func() {
	ginkgo.It("EscapedDescendantIsOutsideOriginalProcessGroup", func() {
		t := ginkgo.GinkgoT()
		root, err := os.MkdirTemp("/tmp", "hp-group-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		preserve := false
		t.Cleanup(func() {
			if !preserve {
				_ = os.RemoveAll(root)
			}
		})
		bin, err := os.Executable()
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		parent := exec.Command(bin)
		parent.Env = append(os.Environ(), probeEnv+"=group-parent", "HAEPARI_TASK004_ROOT="+root)
		parent.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := parent.Start(); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		var childPID int
		defer func() {
			if childPID == 0 {
				childPID, _ = readProbePID(root, "escaped.pid")
			}
			if childPID > 0 {
				if pgid, err := syscall.Getpgid(childPID); err == nil && pgid == childPID {
					if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
						t.Errorf("terminate escaped child group %d: %v", childPID, err)
						preserve = true
					}
				} else if err != nil && !errors.Is(err, syscall.ESRCH) || err == nil && pgid != childPID {
					t.Errorf("escaped child %d has uncertain process group: pgid=%d err=%v", childPID, pgid, err)
					preserve = true
				}
				if err := waitProbeGroupGone(childPID, 2*time.Second); err != nil {
					t.Errorf("escaped child group %d remains after cleanup: %v", childPID, err)
					preserve = true
				}
			} else {
				t.Errorf("escaped child identity unavailable; retained probe directory %s for recovery (child exits within 30 seconds)", root)
				preserve = true
			}
			if pgid, err := syscall.Getpgid(parent.Process.Pid); err == nil && pgid == parent.Process.Pid {
				_ = syscall.Kill(-pgid, syscall.SIGTERM)
			}
			_ = parent.Wait()
		}()
		if err := waitProbeFile(filepath.Join(root, "escaped.pid")); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		data, err := os.ReadFile(filepath.Join(root, "escaped.pid"))
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		childPID, err = strconv.Atoi(string(data))
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if pgid, err := syscall.Getpgid(childPID); err != nil || pgid != childPID {
			t.Fatalf("escaped child pgid=%d err=%v", pgid, err)
		}
		if err := syscall.Kill(-parent.Process.Pid, syscall.SIGTERM); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if pgid, err := syscall.Getpgid(childPID); err != nil || pgid != childPID {
			t.Fatalf("escaped child did not survive original group termination: pgid=%d err=%v", pgid, err)
		}
		t.Logf("terminating group %d left escaped descendant group %d alive", parent.Process.Pid, childPID)

	})
})

func readProbePID(root string, names ...string) (int, error) {
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(string(data))
		if err == nil && pid > 0 {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("no valid probe PID in %s", root)
}

func waitProbeGroupGone(pid int, timeout time.Duration) error {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		pgid, err := syscall.Getpgid(pid)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		if pgid != pid {
			return fmt.Errorf("pid %d moved to pgid %d", pid, pgid)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("process group %d still exists after %s", pid, timeout)
}

func assertProbeRunner(t ginkgo.GinkgoTInterface, pid int) {
	t.Helper()
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid != pid {
		t.Fatalf("runner pid %d no longer owns its process group: pgid=%d err=%v", pid, pgid, err)
	}
}

func waitProbeFile(path string) error {
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", filepath.Base(path))
}

func xmlText(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(value)
}
