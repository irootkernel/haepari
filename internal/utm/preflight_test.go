package utm

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task017ReadOnlySSHPreflight", ginkgo.Label("unit"), func() {
	ginkgo.DescribeTable("requires fresh pinned identity without staging or power", func(fault string) {
		s, spec, _ := sshFixture()
		sshCalls := 0
		s.Control.Run = func(_ context.Context, _ string, args ...string) (CommandResult, error) {
			gomega.Expect(args[0]).To(gomega.BeElementOf("list", "status"))
			state := "started"
			if fault == "stopped" || fault == "stopped-after-probe" && sshCalls != 0 {
				state = "stopped"
			}
			if args[0] == "list" {
				return CommandResult{Stdout: "UUID Status Name\n" + vmID + " " + state + " VM\n"}, nil
			}
			return CommandResult{Stdout: state}, nil
		}
		s.Run = func(_ context.Context, _ string, args []string, input []byte) (CommandResult, error) {
			sshCalls++
			gomega.Expect(args[len(args)-1]).To(gomega.Equal(guestPrerequisiteProbe))
			gomega.Expect(string(input)).NotTo(gomega.ContainSubstring("request.json"))
			gomega.Expect(args).To(gomega.ContainElements("StrictHostKeyChecking=yes", "IdentityAgent=none", "ForwardAgent=no"))
			for _, arg := range args {
				if strings.HasPrefix(arg, "UserKnownHostsFile=") {
					pin, err := os.ReadFile(strings.TrimPrefix(arg, "UserKnownHostsFile="))
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Expect(string(pin)).To(gomega.HavePrefix(HostAlias(vmID) + " ssh-ed25519 "))
				}
			}
			fields := strings.Split(string(input), "\n")
			gomega.Expect(fields).To(gomega.HaveLen(3))
			boot, nonce, mac, arch := vmID, fields[0], fields[1], "arm64"
			switch fault {
			case "nonce":
				nonce = "foreign"
			case "mac":
				mac = "00:01:02:03:04:05"
			case "boot":
				boot = "unknown"
			case "arch":
				arch = "x86_64"
			case "transport":
				return CommandResult{}, errors.New("lost SSH connection")
			case "oversized":
				return CommandResult{Stdout: strings.Repeat("x", 1025)}, nil
			case "oversized-stderr":
				return CommandResult{Stderr: strings.Repeat("x", commandLimit+1)}, nil
			case "malformed":
				return CommandResult{Stdout: "not an identity\n"}, nil
			}
			return CommandResult{Stdout: boot + "\nDarwin\n" + arch + "\n" + nonce + "\n" + mac + "\n"}, nil
		}
		leases := freshSSHLeases()
		if fault == "expired" {
			leases = strings.NewReader(leaseFixture(time.Now().Add(-time.Minute).Unix()))
		}
		identity, err := s.Preflight(context.Background(), spec, leases)
		if fault == "none" {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(identity.BootID).To(gomega.Equal(vmID))
			gomega.Expect(sshCalls).To(gomega.Equal(1))
		} else {
			gomega.Expect(err).To(gomega.HaveOccurred())
			var prerequisite *PrerequisiteError
			gomega.Expect(errors.As(err, &prerequisite)).To(gomega.BeTrue())
			check := "guest_identity"
			if fault == "stopped" || fault == "stopped-after-probe" {
				check = "running_vm"
			} else if fault == "expired" {
				check = "dhcp_lease"
			}
			gomega.Expect(prerequisite.Check).To(gomega.Equal(check))
			if fault == "stopped" || fault == "expired" {
				gomega.Expect(sshCalls).To(gomega.BeZero())
			}
		}
	}, ginkgo.Entry("ready", "none"), ginkgo.Entry("stopped VM", "stopped"),
		ginkgo.Entry("individual lease expired", "expired"), ginkgo.Entry("wrong challenge", "nonce"),
		ginkgo.Entry("wrong MAC", "mac"), ginkgo.Entry("invalid boot", "boot"),
		ginkgo.Entry("wrong architecture", "arch"), ginkgo.Entry("SSH lost", "transport"),
		ginkgo.Entry("VM stops during probe", "stopped-after-probe"),
		ginkgo.Entry("oversized identity response", "oversized"), ginkgo.Entry("oversized SSH stderr", "oversized-stderr"), ginkgo.Entry("malformed identity response", "malformed"))

	ginkgo.DescribeTable("refuses invalid inputs and lost pins before guest access", func(fault string) {
		s, spec, _ := sshFixture()
		s.Control.Run = func(context.Context, string, ...string) (CommandResult, error) {
			ginkgo.Fail("invalid spec or pin must not observe UTM or contact a guest")
			return CommandResult{}, nil
		}
		s.Run = func(context.Context, string, []string, []byte) (CommandResult, error) {
			ginkgo.Fail("invalid spec or pin must not contact a guest")
			return CommandResult{}, nil
		}
		switch fault {
		case "local":
			spec.Kind = "local"
		case "vm":
			spec.VM = nil
		case "guest":
			spec.Guest = nil
		case "executable":
			s.Executable = "ssh"
		case "pin":
			spec.Guest.KnownHostsReference += "-missing"
		case "mapping":
			spec.Guest.ConnectionReference += "-missing"
		}
		_, err := s.Preflight(context.Background(), spec, freshSSHLeases())
		gomega.Expect(errors.Is(err, ErrUnavailable)).To(gomega.BeTrue())
	}, ginkgo.Entry("wrong resource kind", "local"), ginkgo.Entry("missing VM identity", "vm"),
		ginkgo.Entry("missing guest identity", "guest"), ginkgo.Entry("relative SSH executable", "executable"),
		ginkgo.Entry("missing pinned key", "pin"), ginkgo.Entry("missing private mapping", "mapping"))

	ginkgo.It("honors the caller deadline without another probe", func() {
		s, spec, _ := sshFixture()
		calls := 0
		s.Run = func(ctx context.Context, _ string, _ []string, _ []byte) (CommandResult, error) {
			calls++
			<-ctx.Done()
			return CommandResult{}, ctx.Err()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err := s.Preflight(ctx, spec, freshSSHLeases())
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(calls).To(gomega.Equal(1))
	})
})
