package utm

import (
	"context"
	"errors"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task037SSHMeasurements", ginkgo.Label("unit"), func() {
	ginkgo.It("records an interrupted exchange once and refuses issuance when its start cannot be saved", func() {
		s, spec, q := sshFixture()
		calls := 0
		var records []SSHExchange
		s.Measure = func(r SSHExchange) error { records = append(records, r); return nil }
		s.Run = func(context.Context, string, []string, []byte) (CommandResult, error) {
			calls++
			return CommandResult{Stdout: "partial", Stderr: "denied", StdoutBytes: 100, StderrBytes: 200, ExitCode: 255}, errors.New("transport interrupted")
		}
		_, err := s.Call(context.Background(), spec, freshSSHLeases(), q)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(calls).To(gomega.Equal(1))
		gomega.Expect(records).To(gomega.HaveLen(2))
		gomega.Expect(records[0].Complete).To(gomega.BeFalse())
		gomega.Expect(records[0].ExitCode).To(gomega.Equal(-1))
		gomega.Expect(records[1].ID).To(gomega.Equal(records[0].ID))
		gomega.Expect(records[1].Complete).To(gomega.BeTrue())
		gomega.Expect(records[1].TransportFailed).To(gomega.BeTrue())
		gomega.Expect(records[1].StdoutBytes).To(gomega.Equal(int64(100)))
		gomega.Expect(records[1].StderrBytes).To(gomega.Equal(int64(200)))
		s.Measure = func(SSHExchange) error { return errors.New("storage unavailable") }
		_, err = s.Call(context.Background(), spec, freshSSHLeases(), q)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(calls).To(gomega.Equal(1))
	})
	ginkgo.It("leaves an incomplete measurement after a failed completion write without reissuing the call", func() {
		s, spec, q := sshFixture()
		calls, starts := 0, 0
		s.Measure = func(r SSHExchange) error {
			if r.Complete {
				return errors.New("completion unavailable")
			}
			starts++
			return nil
		}
		s.Run = func(_ context.Context, _ string, _ []string, input []byte) (CommandResult, error) {
			calls++
			return fakeSSHReply(decodeSSHInput(input)), nil
		}
		_, err := s.Call(context.Background(), spec, freshSSHLeases(), q)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(calls).To(gomega.Equal(1))
		gomega.Expect(starts).To(gomega.Equal(1))
		output := boundedOutput{}
		data := strings.Repeat("x", commandLimit+100)
		n, err := output.Write([]byte(data))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(n).To(gomega.Equal(len(data)))
		gomega.Expect(output.total).To(gomega.Equal(int64(len(data))))
		gomega.Expect(len(output.bytes)).To(gomega.Equal(commandLimit))
	})
})
