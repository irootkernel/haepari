//go:build integration

package utm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task014NativeSSHProtocol", ginkgo.Label("integration"), func() {
	for _, scenario := range []struct {
		owned  bool
		schema string
		large  bool
	}{{false, "haepari.guest-request/v1", false}, {true, "haepari.guest-request/v1", false}, {true, "haepari.guest-request/v2", false}, {false, "haepari.guest-request/v2", true}, {true, "haepari.guest-request/v2", true}} {
		owned := scenario.owned
		name := "runs the exact bootstrap against the product binary and reconnects after its transport subprocess exits"
		if owned {
			name = "Task037 reuses the owned runner through the exact bootstrap and refuses replaced incarnations"
		}
		name += " " + scenario.schema
		if scenario.large {
			name += " with large request metadata"
		}
		ginkgo.It(name, func() {
			s, spec, q := sshFixture()
			q.Schema = scenario.schema
			s.Transport = owned
			if owned {
				s.RecordPrepared = func(b runner.GuestTransportBinding) error { s.Binding = &b; return nil }
			}
			temporary, err := os.MkdirTemp("/tmp", "hp-ssh-native-")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			root, err := filepath.EvalSymlinks(temporary)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(os.Chmod(root, 0700)).To(gomega.Succeed())
			binaryPath := filepath.Join(root, "candidate")
			build := exec.Command("make", "build", "OUTPUT="+binaryPath)
			build.Dir = "../.."
			output, err := build.CombinedOutput()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), string(output))
			s.Runner, err = os.ReadFile(binaryPath)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			// The SSH archive contains only the executable, request and digest.
			// Its signature must remain valid without an adjacent app/Info.plist.
			relocated := filepath.Join(root, "relocated", "haepari")
			gomega.Expect(os.Mkdir(filepath.Dir(relocated), 0700)).To(gomega.Succeed())
			gomega.Expect(os.WriteFile(relocated, s.Runner, 0700)).To(gomega.Succeed())
			output, err = exec.Command("/usr/bin/codesign", "--verify", "--strict", relocated).CombinedOutput()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), string(output))
			output, err = exec.Command("/usr/bin/codesign", "--display", "--verbose=2", relocated).CombinedOutput()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), string(output))
			gomega.Expect(string(output)).To(gomega.ContainSubstring("Identifier=io.rootkernel.haepari"))
			gomega.Expect(string(output)).NotTo(gomega.ContainSubstring("Info.plist=not bound"))
			sum := sha256.Sum256(s.Runner)
			s.RunnerDigest = "sha256:" + hex.EncodeToString(sum[:])
			q.RunnerDigest = s.RunnerDigest
			interfaces, err := net.Interfaces()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			mac := ""
			for _, iface := range interfaces {
				if len(iface.HardwareAddr) == 6 {
					mac = iface.HardwareAddr.String()
					break
				}
			}
			gomega.Expect(mac).NotTo(gomega.BeEmpty())
			spec.VM.MACAddress, q.MAC = mac, mac
			boot, err := platform.BootIdentity()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			q.BootID, q.Request.BootID = boot, boot
			spec.VM.GuestJobRoot, q.JobRoot = root, root
			q.Request.WorkDir = filepath.Join(root, q.Request.Identity.JobID, "workspace")
			q.Request.LogDevice, q.Request.LogInode = 0, 0
			q.Request.Argv = []string{"/bin/sh", "-c", "printf 'literal $HOME; quote\n'; printf x >> count.txt; while :; do sleep .05; done"}
			if scenario.large {
				q.Request.Argv = append(q.Request.Argv, strings.Repeat("a", 64<<10))
				q.Request.Environment = append(q.Request.Environment, "LARGE="+strings.Repeat("e", 64<<10))
			}
			q.Request.Deadline = time.Now().Add(30 * time.Second).UTC()
			q.Request.RunSeconds = 30
			source := filepath.Join(root, "source")
			gomega.Expect(os.Mkdir(source, 0700)).To(gomega.Succeed())
			gomega.Expect(os.WriteFile(filepath.Join(source, "input.txt"), []byte("sealed"), 0600)).To(gomega.Succeed())
			store, err := snapshot.OpenStore(root)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			snap, err := store.Seal(context.Background(), source, []string{"input.txt"}, snapshot.Provenance{EvidenceClass: "development"})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			q.ObjectID = snap.ObjectID
			q.InputDigest = snap.Manifest.TreeSHA256
			q.Inventory, err = json.Marshal(snap.Manifest)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			q.Files = [][]byte{[]byte("sealed")}
			// This is a local scripted transport, not an SSH server or real VM.
			// Every call runs and reaps the same constant remote bootstrap exactly.
			s.Run = func(ctx context.Context, _ string, args []string, input []byte) (CommandResult, error) {
				remoteShell, shellArgs := "/bin/sh", []string{"-c", args[len(args)-1]}
				if owned && args[len(args)-1] == guestOwnedBootstrap {
					// A non-POSIX login shell must parse only the fixed sh envelope.
					// Full prepare retains its historical login-shell requirements.
					remoteShell, shellArgs = "/bin/csh", []string{"-f", "-c", args[len(args)-1]}
				}
				return runSSH(ctx, remoteShell, shellArgs, input)
			}
			call := func(op string) (runner.GuestReply, error) {
				q.Operation = op
				lease := strings.ReplaceAll(leaseFixture(time.Now().Add(time.Minute).Unix()), vmMAC, mac)
				return s.Call(context.Background(), spec, strings.NewReader(lease), q)
			}
			prepared, err := call("prepare")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			q.Request = *prepared.Request
			q.Root = prepared.Root
			q.Files = nil
			q.Inventory = nil
			defer func() {
				r, e := call("cancel")
				if e != nil || r.Observation == nil || r.Observation.Exit == nil || !r.Observation.Exit.TerminationProven {
					ginkgo.Fail("native protocol fixture termination unproven; retain " + root)
					return
				}
				os.RemoveAll(root)
			}()
			_, err = call("start")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			first, err := call("watch")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(first.Observation.Start).NotTo(gomega.BeNil())
			reconnected, err := call("inspect")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(reconnected.Observation.Start).To(gomega.Equal(first.Observation.Start))
			// Cancelling the transport's observer subprocess is not a semantic
			// cancellation. The detached supervisor and workload remain the same.
			q.Operation, q.Challenge = "watch", "interrupted-observer"
			lease, err := ReadLease(strings.NewReader(strings.ReplaceAll(leaseFixture(time.Now().Add(time.Minute).Unix()), vmMAC, mac)), mac, time.Now())
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			observerContext, stopObserver := context.WithTimeout(context.Background(), 150*time.Millisecond)
			connectedTransport := s.Run
			// A single owned delayed transport process supplies no guest response.
			// Its cancellation is fully reaped; it owns no guest workload process.
			s.Run = func(ctx context.Context, _ string, _ []string, _ []byte) (CommandResult, error) {
				return runSSH(ctx, "/bin/sleep", []string{"2"}, nil)
			}
			_, err = s.exchange(observerContext, spec, lease, q)
			stopObserver()
			s.Run = connectedTransport
			gomega.Expect(err).To(gomega.HaveOccurred())
			stillRunning, err := call("inspect")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(stillRunning.Observation.Start).To(gomega.Equal(first.Observation.Start))
			gomega.Expect(stillRunning.Observation.Exit).To(gomega.BeNil())
			outputReply, err := call("output")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(string(outputReply.Output)).To(gomega.ContainSubstring("literal $HOME; quote"))
			_, err = call("start")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			cancelled, err := call("cancel")
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(cancelled.Observation.Exit.TerminationProven).To(gomega.BeTrue())
			if owned || scenario.large {
				filePath := filepath.Join(root, q.Request.Identity.JobID, "workspace/large.bin")
				payload := bytes.Repeat([]byte("bounded!"), 20<<10)
				pageBytes := 64 << 10
				if q.Schema == "haepari.guest-request/v2" {
					payload = bytes.Repeat([]byte("bounded!"), (128<<10)+1)
					pageBytes = 512 << 10
				}
				gomega.Expect(os.WriteFile(filePath, payload, 0600)).To(gomega.Succeed())
				q.Path, q.Offset = "large.bin", 0
				chunk, e := call("file")
				gomega.Expect(e).NotTo(gomega.HaveOccurred())
				if scenario.large {
					gomega.Expect(len(chunk.Output)).To(gomega.BeNumerically("<", pageBytes))
				} else {
					gomega.Expect(chunk.Output).To(gomega.Equal(payload[:pageBytes]))
				}
				gomega.Expect(chunk.Output).NotTo(gomega.BeEmpty())
				q.ExpectedFile, q.Offset = chunk.File, chunk.NextOffset
				collected := append([]byte(nil), chunk.Output...)
				fileCalls := 1
				for q.Offset < int64(len(payload)) {
					chunk, e = call("file")
					gomega.Expect(e).NotTo(gomega.HaveOccurred())
					gomega.Expect(len(chunk.Output)).To(gomega.BeNumerically("<=", pageBytes))
					gomega.Expect(chunk.NextOffset).To(gomega.Equal(q.Offset + int64(len(chunk.Output))))
					gomega.Expect(chunk.Output).NotTo(gomega.BeEmpty())
					collected = append(collected, chunk.Output...)
					q.Offset = chunk.NextOffset
					fileCalls++
				}
				gomega.Expect(collected).To(gomega.Equal(payload))
				sum := sha256.Sum256(collected)
				gomega.Expect(chunk.File.SHA256).To(gomega.Equal(hex.EncodeToString(sum[:])))
				if scenario.large {
					gomega.Expect(fileCalls).To(gomega.BeNumerically(">", 3))
				} else {
					gomega.Expect(fileCalls).To(gomega.Equal(3))
				}
				gomega.Expect(os.Rename(filePath, filePath+".original")).To(gomega.Succeed())
				gomega.Expect(os.WriteFile(filePath, payload, 0600)).To(gomega.Succeed())
				q.Offset = 0
				_, e = call("file")
				gomega.Expect(e).To(gomega.HaveOccurred())
				q.ExpectedFile, q.Path, q.Offset = nil, "", 0
			}
			if owned {
				runnerPath := filepath.Join(root, q.Request.Identity.JobID, "haepari")
				// Same-inode growth exceeds the bootstrap's bounded runner copy.
				gomega.Expect(os.Truncate(runnerPath, (64<<20)+1)).To(gomega.Succeed())
				_, err = call("inspect")
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(os.Truncate(runnerPath, int64(len(s.Runner)))).To(gomega.Succeed())
				gomega.Expect(os.Rename(runnerPath, runnerPath+".original")).To(gomega.Succeed())
				gomega.Expect(os.WriteFile(runnerPath, s.Runner, 0700)).To(gomega.Succeed())
				_, err = call("inspect")
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(os.Remove(runnerPath)).To(gomega.Succeed())
				gomega.Expect(os.Rename(runnerPath+".original", runnerPath)).To(gomega.Succeed())
				jobPath := filepath.Join(root, q.Request.Identity.JobID)
				gomega.Expect(os.Rename(jobPath, jobPath+".original")).To(gomega.Succeed())
				gomega.Expect(os.Mkdir(jobPath, 0700)).To(gomega.Succeed())
				_, err = call("inspect")
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(os.Remove(jobPath)).To(gomega.Succeed())
				gomega.Expect(os.Rename(jobPath+".original", jobPath)).To(gomega.Succeed())
			}
			data, err := os.ReadFile(filepath.Join(root, q.Request.Identity.JobID, "workspace/count.txt"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(bytes.Equal(data, []byte("x"))).To(gomega.BeTrue())
		})
	}
})
