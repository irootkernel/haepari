//go:build darwin

package platform

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
	"os"
)

var _ = ginkgo.Describe("Native process incarnation", ginkgo.Label("unit"), func() {
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
