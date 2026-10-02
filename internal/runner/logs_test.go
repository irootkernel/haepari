package runner

import (
	"bytes"
	"io"
	"syscall"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

type exhaustedLog struct{ bytes.Buffer }

func (w *exhaustedLog) Write(p []byte) (int, error) {
	n, _ := w.Buffer.Write(p[:min(2, len(p))])
	return n, syscall.ENOSPC
}

var _ = ginkgo.Describe("Task012LogCollection", ginkgo.Label("unit"), func() {
	ginkgo.It("retains the actual partial prefix and disk exhaustion gap", func() {
		dst := &exhaustedLog{}
		collector := newBoundedLog(dst, 64)
		n, err := io.Copy(collector, bytes.NewBufferString("required evidence"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(n).To(gomega.Equal(int64(len("required evidence"))))
		manifest := collector.manifest()
		gomega.Expect(manifest.Artifact.SizeBytes).To(gomega.Equal(int64(2)))
		gomega.Expect(manifest.Gap).To(gomega.Equal("log_write_failed"))
		gomega.Expect(manifest.DroppedBytes).To(gomega.Equal(int64(len("required evidence") - 2)))
		gomega.Expect(dst.String()).To(gomega.Equal("re"))
	})
})
