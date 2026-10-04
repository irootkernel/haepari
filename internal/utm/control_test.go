package utm

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

const vmID = "11111111-1111-4111-8111-111111111111"
const vmMAC = "aa:bb:cc:dd:ee:ff"

var _ = ginkgo.Describe("UTMCommandBoundary", ginkgo.Label("unit"), func() {
	ginkgo.It("uses UUID argv and excludes names and forbidden operations", func() {
		var calls [][]string
		c := Control{Executable: "/Applications/UTM.app/Contents/MacOS/utmctl", Run: func(ctx context.Context, executable string, args ...string) (CommandResult, error) {
			gomega.Expect(executable).To(gomega.Equal("/Applications/UTM.app/Contents/MacOS/utmctl"))
			_, bounded := ctx.Deadline()
			gomega.Expect(bounded).To(gomega.BeTrue())
			calls = append(calls, append([]string{}, args...))
			if args[0] == "list" {
				return CommandResult{Stdout: "UUID Status Name\n" + vmID + " started same display name\n22222222-2222-4222-8222-222222222222 stopped same display name\n"}, nil
			}
			return CommandResult{Stdout: "started\n"}, nil
		}}
		vm, err := c.Inspect(context.Background(), vmID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(vm.UUID).To(gomega.Equal(vmID))
		_, err = c.Inspect(context.Background(), "33333333-3333-4333-8333-333333333333")
		gomega.Expect(errors.Is(err, ErrUnavailable)).To(gomega.BeTrue())
		for _, op := range []string{"start", "stop"} {
			gomega.Expect(c.Power(context.Background(), vmID, op)).To(gomega.Succeed())
		}
		before := len(calls)
		for _, op := range []string{"delete", "clone", "ip-address", "stop --request"} {
			gomega.Expect(c.Power(context.Background(), vmID, op)).NotTo(gomega.Succeed())
		}
		gomega.Expect(c.Power(context.Background(), "name; command", "start")).NotTo(gomega.Succeed())
		gomega.Expect(len(calls)).To(gomega.Equal(before))
		gomega.Expect(calls[2:]).To(gomega.Equal([][]string{{"start", vmID}, {"stop", vmID}}))
	})

	ginkgo.It("rejects exit-zero automation errors, nonzero, loss and excessive output without retry", func() {
		for _, result := range []CommandResult{{Stdout: "started", Stderr: "Error from event: permission denied"}, {Stdout: "started", ExitCode: 1}, {Stdout: strings.Repeat("a", commandLimit+1)}} {
			calls := 0
			c := Control{Executable: "/utmctl", Run: func(context.Context, string, ...string) (CommandResult, error) { calls++; return result, nil }}
			_, err := c.Status(context.Background(), vmID)
			gomega.Expect(errors.Is(err, ErrUnavailable)).To(gomega.BeTrue())
			gomega.Expect(calls).To(gomega.Equal(1))
		}
		c := Control{Executable: "/utmctl", Run: func(context.Context, string, ...string) (CommandResult, error) {
			return CommandResult{}, context.DeadlineExceeded
		}}
		gomega.Expect(c.Power(context.Background(), vmID, "start")).NotTo(gomega.Succeed())
	})

	ginkgo.It("rejects malformed membership and unqualified power states", func() {
		for _, output := range []string{"", "UUID Status Name\nname started VM", "UUID Status Name\n" + vmID + " started VM\n" + strings.ToUpper(vmID) + " stopped VM"} {
			c := Control{Executable: "/utmctl", Run: func(context.Context, string, ...string) (CommandResult, error) {
				return CommandResult{Stdout: output}, nil
			}}
			_, err := c.List(context.Background())
			gomega.Expect(err).To(gomega.HaveOccurred())
		}
		for _, output := range []string{"paused", "starting", "started\nstopped", "unknown"} {
			c := Control{Executable: "/utmctl", Run: func(context.Context, string, ...string) (CommandResult, error) {
				return CommandResult{Stdout: output}, nil
			}}
			_, err := c.Status(context.Background(), vmID)
			gomega.Expect(err).To(gomega.HaveOccurred())
		}
	})

	ginkgo.It("bounds retained output while draining a noisy command", func() {
		var b boundedOutput
		data := []byte(strings.Repeat("x", commandLimit+9))
		n, err := b.Write(data)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(n).To(gomega.Equal(len(data)))
		gomega.Expect(len(b.bytes)).To(gomega.Equal(commandLimit))
		gomega.Expect(b.overflow).To(gomega.BeTrue())
	})

	ginkgo.It("passes literal argv through the native command runner", func() {
		r, err := runCommand(context.Background(), "/bin/echo", "$(touch not-executed); literal", "a b")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(r.Stdout).To(gomega.Equal("$(touch not-executed); literal a b\n"))
	})

	ginkgo.It("drains and reaps native oversized output while refusing the result", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r, err := runCommand(ctx, "/usr/bin/head", "-c", strconv.Itoa(commandLimit+1), "/dev/zero")
		gomega.Expect(errors.Is(err, ErrUnavailable)).To(gomega.BeTrue())
		gomega.Expect(ctx.Err()).NotTo(gomega.HaveOccurred())
		gomega.Expect(r.ExitCode).To(gomega.BeZero())
		gomega.Expect(len(r.Stdout)).To(gomega.BeNumerically("<=", commandLimit))
	})
})
