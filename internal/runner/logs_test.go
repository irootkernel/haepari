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

var _ = ginkgo.Describe("Task023 log limit", func() {
	ginkgo.It("keeps a readable prefix and a log limit gap for a small combined bucket", func() {
		var dst bytes.Buffer
		collector := newBoundedLog(&dst, 8)
		n, err := collector.Write([]byte("0123456789abcdef"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(n).To(gomega.Equal(16))
		gomega.Expect(dst.String()).To(gomega.Equal("01234567"))
		manifest := collector.manifest()
		gomega.Expect(manifest.Gap).To(gomega.Equal("log_limit"))
		gomega.Expect(manifest.Artifact.SizeBytes).To(gomega.Equal(int64(8)))
		gomega.Expect(manifest.DroppedBytes).To(gomega.Equal(int64(8)))
		gomega.Expect(manifest.LimitBytes).To(gomega.Equal(int64(8)))
	})

	ginkgo.It("uses the 256MiB combined log limit when the request omits one", func() {
		collector := newBoundedLog(&bytes.Buffer{}, 0)
		gomega.Expect(collector.limit).To(gomega.Equal(DefaultLogBytes))
		gomega.Expect(DefaultLogBytes).To(gomega.Equal(int64(256 << 20)))
		gomega.Expect(collector.manifest().LimitBytes).To(gomega.Equal(DefaultLogBytes))
		gomega.Expect(collector.manifest().Gap).To(gomega.BeEmpty())
	})
})
