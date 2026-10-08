package runner

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/irootkernel/haepari/internal/platform"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "guest-remove-deadline-fixture" {
		point, path := os.Args[2], os.Args[3]
		d, err := openDirectory(filepath.Dir(path))
		if err != nil {
			os.Exit(90)
		}
		defer d.close()
		spawnBoundary = func(name string) {
			if name == "guest_cleanup_unlink_"+point {
				fmt.Fprintln(os.Stderr, "guest-cleanup-unlink-boundary-reached")
				time.Sleep(250 * time.Millisecond)
			}
		}
		if guestRemove(int(d.file.Fd()), filepath.Base(path), time.Now().Add(100*time.Millisecond)) == ErrUnproven {
			os.Exit(77)
		}
		os.Exit(90)
	}
	if len(os.Args) > 2 && os.Args[1] == "runner" && os.Args[2] == "guest-recovery" {
		if point := os.Getenv("HAEPARI_TEST_GUEST_RECOVERY_CRASH"); point != "" {
			spawnBoundary = func(name string) {
				if strings.HasPrefix(point, "expire:") && name == strings.TrimPrefix(point, "expire:") {
					fmt.Fprintln(os.Stderr, "guest-recovery-deadline-boundary-reached")
					time.Sleep(2 * time.Second)
				} else if point == "replace-root" && name == "guest_attested_cleanup_intent" {
					root := os.Getenv("HAEPARI_TEST_GUEST_RECOVERY_ROOT")
					if os.Rename(root, root+"-retained") != nil || os.Mkdir(root, 0700) != nil || os.WriteFile(filepath.Join(root, "keep.txt"), []byte("replacement"), 0600) != nil {
						os.Exit(90)
					}
					fmt.Fprintln(os.Stderr, "guest-recovery-root-replaced")
				}
				if name == point {
					os.Exit(91)
				}
			}
		}
	}
	if len(os.Args) == 4 && os.Args[1] == "runner-initial-observation-fixture" {
		mode, path := os.Args[2], os.Args[3]
		if mode == "process" {
			observeStartedProcess = func(pid int) (platform.ProcessObservation, error) {
				return platform.ProcessObservation{}, &platform.ProcessError{Operation: "process_snapshot_before", PID: pid, Err: syscall.EIO}
			}
		} else if mode == "identity" {
			observeStartedProcess = func(pid int) (platform.ProcessObservation, error) {
				v, err := platform.ObserveProcess(pid)
				v.Identity.PGID = pid + 1
				return v, err
			}
		} else {
			spawnBoundary = func(name string) {
				if name != "after_start" {
					return
				}
				d, q, err := readRequest(path)
				if err != nil {
					os.Exit(90)
				}
				defer d.close()
				binding := q.Binding()
				binding.Identity.Nonce = "fixture-conflicting-cancellation"
				if d.write("cancel.json", Cancellation{Schema: "haepari.runner-cancel/v1", Binding: binding, RequestedAt: time.Now().UTC()}) != nil {
					os.Exit(90)
				}
			}
		}
		os.Exit(Command([]string{"start", "--request", path}, os.Stdout, os.Stderr))
	}
	if len(os.Args) == 3 && os.Args[1] == "runner-cancel-fixture" {
		path := os.Args[2]
		spawnBoundary = func(name string) {
			if name == "after_start" {
				if Cancel(path) != nil {
					os.Exit(90)
				}
			}
		}
		os.Exit(Command([]string{"start", "--request", path}, os.Stdout, os.Stderr))
	}
	if len(os.Args) == 3 && os.Args[1] == "runner-caller-fixture" {
		executable, err := os.Executable()
		if err != nil {
			os.Exit(90)
		}
		cmd := exec.Command(executable, "runner", "start", "--request", os.Args[2])
		cmd.Env = []string{"PATH=/usr/bin:/bin", "GORACE=atexit_sleep_ms=0"}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		log, err := os.OpenFile(filepath.Join(filepath.Dir(os.Args[2]), "diagnostics.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(90)
		}
		cmd.Stdout, cmd.Stderr = log, log
		if cmd.Start() != nil {
			os.Exit(90)
		}
		log.Close()
		os.Exit(0) // caller dies without waiting for or owning runner pipes
	}
	if len(os.Args) == 4 && os.Args[1] == "runner-census-fixture" {
		mode, path := os.Args[2], os.Args[3]
		injected := false
		processCensus = func() ([]platform.ProcessObservation, error) {
			if mode == "unavailable" && injected {
				return nil, syscall.EIO
			}
			processes, err := platform.ProcessCensus()
			if err != nil {
				return nil, err
			}
			view, err := Inspect(path)
			if err != nil || view.Start == nil {
				return processes, err
			}
			if mode == "budget" {
				// Deliberately retain a stale census member after native reap.
				// Native signaling still rechecks its identity and refuses it.
				return append(processes, platform.ProcessObservation{Identity: view.Start.Process}), nil
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "workspace/count.txt")); err == nil && !injected {
				injected = true
				if mode == "native-error" || mode == "unavailable" {
					return nil, &platform.ProcessError{Operation: "process_snapshot_before", PID: view.Start.Process.PID, Err: syscall.EIO}
				}
				return nil, ErrUnproven
			}
			return processes, nil
		}
		os.Exit(Command([]string{"start", "--request", path}, os.Stdout, os.Stderr))
	}
	if len(os.Args) == 2 && os.Args[1] == "runner-escaped-child" {
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(20 * time.Second)
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "runner-fork-fixture" {
		executable, err := os.Executable()
		if err != nil {
			os.Exit(90)
		}
		child := exec.Command(executable, "runner-escaped-child")
		child.Env = []string{"PATH=/usr/bin:/bin", "GORACE=atexit_sleep_ms=0"}
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if child.Start() != nil {
			os.Exit(90)
		}
		if os.WriteFile("child.pid", []byte(strconv.Itoa(child.Process.Pid)), 0600) != nil {
			os.Exit(90)
		}
		// Keep ancestry observable before this fixture exits and reparents it.
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "runner" {
		os.Exit(Command(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) == 4 && os.Args[1] == "runner-crash-fixture" {
		point := os.Args[2]
		spawnBoundary = func(name string) {
			if name == point {
				os.Exit(91)
			}
		}
		os.Exit(Command([]string{"start", "--request", os.Args[3]}, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}
func TestRunner(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Runner Suite")
}
