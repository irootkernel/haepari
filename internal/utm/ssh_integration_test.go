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
	ginkgo.It("runs the exact bootstrap against the product binary and reconnects after its transport subprocess exits", func() {
		s, spec, q := sshFixture()
		temporary, err := os.MkdirTemp("/tmp", "hp-ssh-native-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		root, err := filepath.EvalSymlinks(temporary)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.Chmod(root, 0700)).To(gomega.Succeed())
		binaryPath := filepath.Join(root, "candidate")
		build := exec.Command("go", "build", "-o", binaryPath, "../../cmd/haepari")
		output, err := build.CombinedOutput()
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), string(output))
		s.Runner, err = os.ReadFile(binaryPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
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
			return runSSH(ctx, "/bin/sh", []string{"-c", args[len(args)-1]}, input)
		}
		call := func(op string) (runner.GuestReply, error) {
			q.Operation = op
			lease := strings.ReplaceAll(leaseFixture(time.Now().Add(time.Minute).Unix()), vmMAC, mac)
			return s.Call(context.Background(), spec, strings.NewReader(lease), q)
		}
		prepared, err := call("prepare")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		q.Request = *prepared.Request
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
		data, err := os.ReadFile(filepath.Join(root, q.Request.Identity.JobID, "workspace/count.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(bytes.Equal(data, []byte("x"))).To(gomega.BeTrue())
	})
})
