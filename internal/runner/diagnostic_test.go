package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/irootkernel/haepari/internal/platform"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

var _ = ginkgo.Describe("NativeDiagnostic", ginkgo.Label("unit"), func() {
	ginkgo.It("binds a private immutable native failure without retaining arbitrary error text", func() {
		root := privateFixtureDirectory()
		defer os.RemoveAll(root)
		q := requestFixture(root)
		d, err := openDirectory(root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer d.close()
		failure := &platform.ProcessError{Operation: "getsid", PID: 123, Err: fmt.Errorf("secret-sentinel: %w", unix.EIO)}
		gomega.Expect(recordNativeDiagnostic(d, q, "process_census", failure)).To(gomega.Succeed())
		var v nativeDiagnostic
		gomega.Expect(d.read("native-diagnostic.json", &v)).To(gomega.Succeed())
		gomega.Expect(v.Binding).To(gomega.Equal(q.Binding()))
		gomega.Expect(v.BootID).To(gomega.Equal(q.BootID))
		gomega.Expect(v.Phase).To(gomega.Equal("run"))
		gomega.Expect(v.NativeOperation).To(gomega.Equal("getsid"))
		gomega.Expect(v.PID).To(gomega.Equal(123))
		gomega.Expect(v.Errno).To(gomega.Equal(int(unix.EIO)))
		gomega.Expect(v.ErrorCode).To(gomega.Equal("native_errno"))
		gomega.Expect(v.ObservedAt.IsZero()).To(gomega.BeFalse())
		before, err := os.ReadFile(filepath.Join(root, "native-diagnostic.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(before)).NotTo(gomega.ContainSubstring("secret-sentinel"))
		gomega.Expect(recordNativeDiagnostic(d, q, "signal_term", errors.New("other-secret"))).To(gomega.MatchError(ErrConflict))
		gomega.Expect(os.ReadFile(filepath.Join(root, "native-diagnostic.json"))).To(gomega.Equal(before))
	})
})
