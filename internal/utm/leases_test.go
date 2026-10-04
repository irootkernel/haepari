package utm

import (
	"context"
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
