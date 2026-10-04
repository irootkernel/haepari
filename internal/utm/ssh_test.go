package utm

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func sshFixture() (SSH, managed.ResourceSpec, runner.GuestRequest) {
	root := ginkgo.GinkgoT().TempDir()
	write := func(name string, data []byte) string {
		filename := filepath.Join(root, name)
		gomega.Expect(os.WriteFile(filename, data, 0600)).To(gomega.Succeed())
		sum := sha256.Sum256(data)
		return "sha256:" + hex.EncodeToString(sum[:]) + ":" + filename
	}
	key := base64.StdEncoding.EncodeToString(append([]byte{0, 0, 0, 11, 's', 's', 'h', '-', 'e', 'd', '2', '5', '5', '1', '9', 0, 0, 0, 32}, make([]byte, 32)...))
	pin := write("known_hosts", []byte(HostAlias(vmID)+" ssh-ed25519 "+key+"\n"))
	write("identity", []byte("synthetic key never sent to a guest"))
	mapping, _ := json.Marshal(SSHMapping{Schema: "haepari.ssh-mapping/v1", User: "fixture", Port: 22, IdentityFile: filepath.Join(root, "identity")})
	spec := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "vm-fixture", Kind: "utm-macos", ExecutionMode: "managed", Capacity: 1, Revision: 1, Adapter: managed.Adapter{QualificationReference: "fake-task014", UTMCtl: "/fake/utmctl"}, VM: &managed.VM{UTMVMUUID: vmID, MACAddress: vmMAC, AddressDiscovery: "dhcp-lease-by-mac", GuestJobRoot: "/private/guest/jobs"}, Guest: &managed.Guest{ConnectionReference: write("mapping.json", mapping), KnownHostsReference: pin}}
	binary := []byte("pinned fixture runner")
	sum := sha256.Sum256(binary)
	s := SSH{Executable: "/usr/bin/ssh", Runner: binary, RunnerDigest: "sha256:" + hex.EncodeToString(sum[:]), Control: Control{Executable: "/fake/utmctl", Run: func(_ context.Context, _ string, args ...string) (CommandResult, error) {
		if args[0] == "list" {
			return CommandResult{Stdout: "UUID Status Name\n" + vmID + " started VM\n"}, nil
		}
		return CommandResult{Stdout: "started", ExitCode: 0}, nil
	}}}
	request := runner.Request{Schema: "haepari.runner-request/v1", Identity: managed.LocalIdentity{JobID: "job-fixture", ResourceID: spec.ID, ConfigRevision: 1, LeaseGeneration: 1, RunID: "run-fixture", Nonce: "job-nonce"}, BootID: vmID, WorkDir: "/private/guest/jobs/job-fixture/workspace", Argv: []string{"/bin/printf", "literal ; $HOME ' \n"}, Environment: []string{"PATH=/usr/bin:/bin"}, Deadline: time.Now().Add(time.Minute).UTC(), RunSeconds: 30, TerminationSeconds: 2, LogDevice: 1, LogInode: 1}
	q := runner.GuestRequest{Schema: "haepari.guest-request/v1", Operation: "inspect", VMUUID: vmID, MAC: vmMAC, BootID: vmID, JobRoot: spec.VM.GuestJobRoot, RunnerDigest: s.RunnerDigest, Request: request, InputDigest: "sha256:" + strings.Repeat("1", 64), ProfileDigest: "sha256:" + strings.Repeat("2", 64), ObjectID: "sha256:" + strings.Repeat("3", 64), Deadline: time.Now().Add(time.Minute).UTC()}
	return s, spec, q
}

func decodeSSHInput(input []byte) runner.GuestRequest {
	archive := tar.NewReader(bytes.NewReader(input))
	var data []byte
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		content, err := io.ReadAll(archive)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		if header.Name == "request.json" {
			data = content
		}
	}
	var q runner.GuestRequest
	gomega.Expect(json.Unmarshal(data, &q)).To(gomega.Succeed())
	return q
}
func fakeSSHReply(q runner.GuestRequest) CommandResult {
	r := runner.GuestReply{Schema: "haepari.guest-reply/v1", ProtocolDigest: runner.GuestProtocolDigest(q), VMUUID: q.VMUUID, MAC: q.MAC, Challenge: q.Challenge, BootID: vmID, OS: "darwin", Arch: "arm64"}
	if q.Operation != "identity" {
		r.Request = &q.Request
		r.Observation = &runner.Observation{State: "prepared"}
	}
	return encodedSSHReply(r)
}
func encodedSSHReply(reply runner.GuestReply) CommandResult {
	data, _ := json.Marshal(reply)
	encoded, _ := json.Marshal(managed.Envelope{Schema: "haepari.envelope/v1", Operation: "runner.guest", OK: true, Data: data})
	return CommandResult{Stdout: string(encoded), ExitCode: 0}
}
func freshSSHLeases() *strings.Reader {
	return strings.NewReader(leaseFixture(time.Now().Add(time.Minute).Unix()))
}

var _ = ginkgo.Describe("Task014SSHBoundary", ginkgo.Label("unit"), func() {
	ginkgo.It("uses fixed remote argv and encoded requests with one pinned identity and no ambient authentication", func() {
		s, spec, q := sshFixture()
		calls := 0
		s.Run = func(_ context.Context, executable string, args []string, input []byte) (CommandResult, error) {
			calls++
			gomega.Expect(executable).To(gomega.Equal("/usr/bin/ssh"))
			gomega.Expect(args[len(args)-1]).To(gomega.Equal(guestBootstrap))
			gomega.Expect(args).To(gomega.ContainElements("/dev/null", "StrictHostKeyChecking=yes", "ForwardAgent=no", "IdentityAgent=none", "IdentitiesOnly=yes", "ClearAllForwardings=yes", "BatchMode=yes", "HostKeyAlgorithms=ssh-ed25519"))
			request := decodeSSHInput(input)
			gomega.Expect(request.Request.Argv).To(gomega.Equal(q.Request.Argv))
			gomega.Expect(request.Challenge).NotTo(gomega.BeEmpty())
			gomega.Expect(string(input)).NotTo(gomega.ContainSubstring("synthetic key never sent"))
			return fakeSSHReply(request), nil
		}
		r, err := s.Call(context.Background(), spec, freshSSHLeases(), q)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(r.Request).To(gomega.Equal(&q.Request))
		gomega.Expect(calls).To(gomega.Equal(2))
	})
	ginkgo.It("refuses wrong host proof, VM, MAC, boot, nonce and platform before issuing a job operation", func() {
		for _, field := range []string{"transport", "vm", "mac", "boot", "nonce", "os", "arch"} {
			s, spec, q := sshFixture()
			operations := 0
			s.Run = func(_ context.Context, _ string, _ []string, input []byte) (CommandResult, error) {
				request := decodeSSHInput(input)
				if request.Operation != "identity" {
					operations++
				}
				if field == "transport" {
					return CommandResult{ExitCode: 255}, errors.New("host key rejected")
				}
				reply := runner.GuestReply{Schema: "haepari.guest-reply/v1", ProtocolDigest: runner.GuestProtocolDigest(request), VMUUID: request.VMUUID, MAC: request.MAC, Challenge: request.Challenge, BootID: vmID, OS: "darwin", Arch: "arm64"}
				switch field {
				case "vm":
					reply.VMUUID = "22222222-2222-4222-8222-222222222222"
				case "mac":
					reply.MAC = "00:00:00:00:00:00"
				case "boot":
					reply.BootID = "22222222-2222-4222-8222-222222222222"
				case "nonce":
					reply.Challenge = "old"
				case "os":
					reply.OS = "linux"
				case "arch":
					reply.Arch = "amd64"
				}
				return encodedSSHReply(reply), nil
			}
			_, err := s.Call(context.Background(), spec, freshSSHLeases(), q)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(operations).To(gomega.BeZero())
		}
	})
	ginkgo.It("reports uncertainty on a lost effect response without retrying it", func() {
		s, spec, q := sshFixture()
		q.Operation = "start"
		effects := 0
		s.Run = func(_ context.Context, _ string, _ []string, input []byte) (CommandResult, error) {
			request := decodeSSHInput(input)
			if request.Operation == "identity" {
				return fakeSSHReply(request), nil
			}
			effects++
			return CommandResult{ExitCode: 255}, context.DeadlineExceeded
		}
		_, err := s.Call(context.Background(), spec, freshSSHLeases(), q)
		gomega.Expect(errors.Is(err, ErrGuestUncertain)).To(gomega.BeTrue())
		gomega.Expect(effects).To(gomega.Equal(1))
	})
	ginkgo.It("rejects expired leases, changed pins and unsafe authentication references without SSH", func() {
		for _, bad := range []string{"lease", "pin", "mapping", "identity"} {
			s, spec, q := sshFixture()
			calls := 0
			s.Run = func(context.Context, string, []string, []byte) (CommandResult, error) {
				calls++
				return CommandResult{}, nil
			}
			leases := freshSSHLeases()
			switch bad {
			case "lease":
				leases = strings.NewReader(leaseFixture(time.Now().Add(-time.Second).Unix()))
			case "pin":
				spec.Guest.KnownHostsReference = "sha256:" + strings.Repeat("0", 64) + ":/missing"
			case "mapping":
				spec.Guest.ConnectionReference = "unbound"
			case "identity":
				parts := strings.SplitN(spec.Guest.ConnectionReference, ":", 3)
				gomega.Expect(os.Chmod(filepath.Join(filepath.Dir(parts[2]), "identity"), 0644)).To(gomega.Succeed())
			}
			_, err := s.Call(context.Background(), spec, leases, q)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(calls).To(gomega.BeZero())
		}
	})
})
