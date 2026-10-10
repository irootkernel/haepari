package utm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func leaseFixture(expiry int64) string {
	return fmt.Sprintf("{\nname=macOS\nip_address=192.168.64.3\nhw_address=1,%s\nlease=0x%x\n}\n", vmMAC, expiry)
}

var _ = ginkgo.Describe("IndividualDHCPLease", ginkgo.Label("unit"), func() {
	ginkgo.It("Task037 accepts Apple's padded or unpadded Ethernet lease octets without weakening identity or expiry", func() {
		now := time.Unix(2000000000, 0)
		mac := "00:05:02:3e:04:30"
		valid := strings.ReplaceAll(leaseFixture(now.Add(time.Hour).Unix()), vmMAC, "0:5:2:3e:4:30")
		for _, spelling := range []string{"0:5:2:3e:4:30", "00:05:02:3E:04:30"} {
			lease, err := ReadLease(strings.NewReader(strings.ReplaceAll(valid, "0:5:2:3e:4:30", spelling)), mac, now)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(lease.MAC).To(gomega.Equal(mac))
		}
		for _, spelling := range []string{"0:5:2:3e:4", "0:5:2:3e:4:030", "0:5:2:3e:4:+3", "0:5:2:3e:4:gg", "0:5:2:3e:4: 30", "0:5:2:3e:4:30:1"} {
			_, err := ReadLease(strings.NewReader(strings.ReplaceAll(valid, "0:5:2:3e:4:30", spelling)), mac, now)
			gomega.Expect(err).To(gomega.HaveOccurred())
		}
		for _, text := range []string{valid + strings.ReplaceAll(valid, "0:5:2:3e:4:30", mac), strings.ReplaceAll(valid, "hw_address=1,", "hw_address=0,"), strings.ReplaceAll(valid, fmt.Sprintf("0x%x", now.Add(time.Hour).Unix()), fmt.Sprintf("0x%x", now.Unix()))} {
			_, err := ReadLease(strings.NewReader(text), mac, now)
			gomega.Expect(err).To(gomega.HaveOccurred())
		}
		_, err := normalizeMAC("0:5:2:3e:4:30")
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
	ginkgo.It("preserves host denial before and after guest identity proof", func() {
		for _, deniedAt := range []int{1, 2} {
			statuses, proofs := 0, 0
			c := Control{Executable: "/utmctl", Run: func(_ context.Context, _ string, args ...string) (CommandResult, error) {
				if args[0] == "list" {
					return CommandResult{Stdout: "UUID Status Name\n" + vmID + " started VM\n"}, nil
				}
				statuses++
				if statuses == deniedAt {
					return CommandResult{Stderr: "Error from event: (OSStatus error -1743.)"}, nil
				}
				return CommandResult{Stdout: "started"}, nil
			}}
			now := time.Now()
			verify := func(_ context.Context, lease Lease, uuid, nonce string) (GuestIdentity, error) {
				proofs++
				return GuestIdentity{VMUUID: uuid, MAC: vmMAC, Address: lease.Address, HostKey: "pin", BootID: vmID, Nonce: nonce, OS: "darwin", Arch: "arm64"}, nil
			}
			_, _, err := c.ResolveGuest(context.Background(), strings.NewReader(leaseFixture(now.Add(time.Hour).Unix())), vmID, vmMAC, "pin", "fresh", func() time.Time { return now }, verify)
			var failure *CommandFailure
			gomega.Expect(errors.As(err, &failure)).To(gomega.BeTrue())
			gomega.Expect(failure.HostPermissionDenied).To(gomega.BeTrue())
			gomega.Expect(proofs).To(gomega.Equal(deniedAt - 1))
		}
	})
	ginkgo.It("requires exactly one known unexpired per-MAC lease", func() {
		now := time.Unix(2000000000, 0)
		valid := leaseFixture(now.Add(time.Hour).Unix())
		lease, err := ReadLease(strings.NewReader(valid), strings.ToUpper(vmMAC), now)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(lease.Address.String()).To(gomega.Equal("192.168.64.3"))
		for _, input := range []string{"", leaseFixture(now.Unix()), leaseFixture(now.Add(-time.Second).Unix()), valid + valid,
			valid + leaseFixture(now.Add(-time.Hour).Unix()), strings.ReplaceAll(valid, "lease=", "unknown="),
			strings.ReplaceAll(valid, "0x", "broken"), strings.ReplaceAll(valid, "192.168.64.3", "127.0.0.1"),
			strings.TrimSuffix(valid, "}\n"), strings.ReplaceAll(valid, "name=macOS", "name=macOS\nname=duplicate")} {
			_, err := ReadLease(strings.NewReader(input), vmMAC, now)
			gomega.Expect(err).To(gomega.HaveOccurred())
		}
	})

	ginkgo.It("uses a pre-start or renewed lease only with current identity and rechecked expiry", func() {
		now := time.Unix(2000000000, 0)
		c := Control{Executable: "/utmctl", Run: func(_ context.Context, _ string, args ...string) (CommandResult, error) {
			if args[0] == "list" {
				return CommandResult{Stdout: "UUID Status Name\n" + vmID + " started VM\n"}, nil
			}
			return CommandResult{Stdout: "started"}, nil
		}}
		verify := func(_ context.Context, lease Lease, uuid, nonce string) (GuestIdentity, error) {
			return GuestIdentity{VMUUID: uuid, MAC: vmMAC, Address: lease.Address, HostKey: "pin", BootID: vmID, Nonce: nonce, OS: "darwin", Arch: "arm64"}, nil
		}
		for _, expiry := range []time.Duration{time.Minute, time.Hour} {
			_, _, err := c.ResolveGuest(context.Background(), strings.NewReader(leaseFixture(now.Add(expiry).Unix())), vmID, vmMAC, "pin", "fresh-nonce", func() time.Time { return now }, verify)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		for _, change := range []func(*GuestIdentity){func(i *GuestIdentity) { i.HostKey = "wrong" }, func(i *GuestIdentity) { i.Nonce = "old" }, func(i *GuestIdentity) { i.MAC = "00:00:00:00:00:00" }, func(i *GuestIdentity) { i.VMUUID = "22222222-2222-4222-8222-222222222222" }, func(i *GuestIdentity) { i.BootID = "" }, func(i *GuestIdentity) { i.OS = "linux" }, func(i *GuestIdentity) { i.Arch = "amd64" }} {
			wrong := func(ctx context.Context, lease Lease, uuid, nonce string) (GuestIdentity, error) {
				i, err := verify(ctx, lease, uuid, nonce)
				change(&i)
				return i, err
			}
			_, _, err := c.ResolveGuest(context.Background(), strings.NewReader(leaseFixture(now.Add(time.Minute).Unix())), vmID, vmMAC, "pin", "fresh-nonce", func() time.Time { return now }, wrong)
			gomega.Expect(err).To(gomega.HaveOccurred())
		}
		expireDuringProof := func(ctx context.Context, lease Lease, uuid, nonce string) (GuestIdentity, error) {
			i, err := verify(ctx, lease, uuid, nonce)
			now = now.Add(time.Hour)
			return i, err
		}
		_, _, err := c.ResolveGuest(context.Background(), strings.NewReader(leaseFixture(now.Add(time.Minute).Unix())), vmID, vmMAC, "pin", "fresh-nonce", func() time.Time { return now }, expireDuringProof)
		gomega.Expect(err).To(gomega.HaveOccurred())
	})
})
