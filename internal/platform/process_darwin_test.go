//go:build darwin

package platform

import (
	"errors"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
	"os"
)

var _ = ginkgo.Describe("Native process incarnation", ginkgo.Label("unit"), func() {
	ginkgo.It("rechecks the birth identity from a bulk census snapshot before adopting it", func() {
		pid := os.Getpid()
		boot, err := BootIdentity()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		before, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		before.Proc.P_starttime.Usec++
		_, err = observeProcessSnapshot(pid, boot, before)
		var native *ProcessError
		gomega.Expect(errors.As(err, &native)).To(gomega.BeTrue())
		gomega.Expect(native.Operation).To(gomega.Equal("process_incarnation"))
		current, err := ObserveProcess(pid)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		census, err := ProcessCensus()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		matches := 0
		for _, observed := range census {
			if observed.Identity.PID == pid {
				matches++
				gomega.Expect(observed).To(gomega.Equal(current))
			}
		}
		gomega.Expect(matches).To(gomega.Equal(1))
	})

	ginkgo.It("binds the current kernel identity and refuses changed boot or PID creation evidence before signaling", func() {
		current, err := ObserveProcess(os.Getpid())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(current.Identity.BootID).NotTo(gomega.BeEmpty())
		gomega.Expect(current.Identity.StartedSeconds).To(gomega.BeNumerically(">", 0))
		changed := current.Identity
		changed.BootID = "other-boot"
		gomega.Expect(SignalProcess(changed, unix.SIGKILL)).To(gomega.HaveOccurred())
		changed = current.Identity
		changed.StartedMicros++
		gomega.Expect(SignalProcess(changed, unix.SIGKILL)).To(gomega.HaveOccurred())
		gomega.Expect(SignalProcess(current.Identity, 0)).To(gomega.Succeed())
	})
})
