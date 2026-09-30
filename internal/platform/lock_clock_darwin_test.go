//go:build darwin

package platform

import (
	"encoding/hex"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task035NativeLockClock", ginkgo.Label("unit"), func() {
	ginkgo.It("reads a bounded native absolute wake marker and UTC instant on the supported host", func() {
		before := time.Now().UTC()
		now, marker, err := AdvisoryLockClock()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		raw, err := hex.DecodeString(marker)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(raw).To(gomega.HaveLen(8))
		gomega.Expect(now.Before(before)).To(gomega.BeFalse())
		gomega.Expect(now.After(time.Now().UTC())).To(gomega.BeFalse())
	})
})
