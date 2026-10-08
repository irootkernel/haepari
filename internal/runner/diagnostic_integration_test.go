//go:build integration

package runner

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

var _ = ginkgo.Describe("NativeDiagnostic", ginkgo.Label("integration"), func() {
	for _, mode := range []string{"process", "cancellation", "identity"} {
		ginkgo.It("preserves initial "+mode+" observation failure before releasing the workload gate", func() {
			root, q := nativeFixture("printf unwanted > effect.txt")
			defer os.RemoveAll(root)
			_, done := launchFixture(root, "initial-"+mode)
			view := awaitFixture(root, done, true)
			gomega.Expect(view.Intent).NotTo(gomega.BeNil())
			gomega.Expect(view.Exit).To(gomega.BeNil())
			d, err := openDirectory(root)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer d.close()
			var v nativeDiagnostic
			gomega.Expect(d.read("native-diagnostic.json", &v)).To(gomega.Succeed())
			gomega.Expect(v.Binding).To(gomega.Equal(q.Binding()))
			gomega.Expect(v.BootID).To(gomega.Equal(q.BootID))
			switch mode {
			case "process":
				gomega.Expect(view.Start).To(gomega.BeNil())
				gomega.Expect(v.Operation).To(gomega.Equal("start_observation"))
				gomega.Expect(v.NativeOperation).To(gomega.Equal("process_snapshot_before"))
				gomega.Expect(v.PID).To(gomega.BeNumerically(">", 0))
				gomega.Expect(v.Errno).To(gomega.Equal(int(unix.EIO)))
			case "identity":
				gomega.Expect(view.Start).To(gomega.BeNil())
				gomega.Expect(v.Operation).To(gomega.Equal("start_observation"))
				gomega.Expect(v.NativeOperation).To(gomega.Equal("process_incarnation"))
				gomega.Expect(v.PID).To(gomega.BeNumerically(">", 0))
				gomega.Expect(v.ErrorCode).To(gomega.Equal("invalid"))
			default:
				gomega.Expect(view.Start).NotTo(gomega.BeNil())
				gomega.Expect(v.Operation).To(gomega.Equal("cancellation_observation"))
				gomega.Expect(v.ErrorCode).To(gomega.Equal("conflict"))
			}
			before, err := os.ReadFile(filepath.Join(root, "native-diagnostic.json"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = StartRun(filepath.Join(root, "request.json"))
			gomega.Expect(errors.Is(err, ErrAmbiguous)).To(gomega.BeTrue())
			gomega.Expect(os.ReadFile(filepath.Join(root, "native-diagnostic.json"))).To(gomega.Equal(before))
			_, err = os.Stat(filepath.Join(root, "workspace/effect.txt"))
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		})
	}

	ginkgo.It("reports failed diagnostic publication when unavailable observations exhaust the stop budget", func() {
		root, _ := nativeFixture("printf once > count.txt; /bin/sleep 6", func(q *Request) { q.TerminationSeconds = 1 })
		defer os.RemoveAll(root)
		defer func() {
			view, err := Inspect(filepath.Join(root, "request.json"))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if view.Start != nil {
				tracker := newTracker(view.Start.Process)
				gomega.Expect(tracker.signal(unix.SIGKILL)).To(gomega.Succeed())
				gomega.Eventually(func() bool {
					alive, err := tracker.scan()
					return err == nil && len(alive) == 0
				}, 3*time.Second, 25*time.Millisecond).Should(gomega.BeTrue())
			}
		}()
		prior := []byte("preserve conflicting diagnostic")
		gomega.Expect(os.WriteFile(filepath.Join(root, "native-diagnostic.json"), prior, 0600)).To(gomega.Succeed())
		before := time.Now()
		cmd, done := launchFixture(root, "census-unavailable")
		view := awaitFixture(root, done, true)
		gomega.Expect(time.Since(before)).To(gomega.BeNumerically("<", 5*time.Second))
		gomega.Expect(cmd.ProcessState.Success()).To(gomega.BeFalse())
		gomega.Expect(view.Exit).To(gomega.BeNil())
		gomega.Expect(view.Start).NotTo(gomega.BeNil())
		log, err := os.ReadFile(filepath.Join(root, "diagnostics.log"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(log)).To(gomega.ContainSubstring(ErrUnproven.Error()))
		gomega.Expect(string(log)).To(gomega.ContainSubstring(ErrConflict.Error()))
		gomega.Expect(os.ReadFile(filepath.Join(root, "native-diagnostic.json"))).To(gomega.Equal(prior))
		gomega.Expect(os.ReadFile(filepath.Join(root, "workspace/count.txt"))).To(gomega.Equal([]byte("once")))
	})

	ginkgo.It("preserves the triggering native census error across stop and same-run replay", func() {
		root, q := nativeFixture("printf once > count.txt; /bin/sleep .2; exit 7")
		defer os.RemoveAll(root)
		_, done := launchFixture(root, "census-native-error")
		view := awaitFixture(root, done, false)
		gomega.Expect(view.Exit.TerminationProven).To(gomega.BeFalse())
		gomega.Expect(view.Exit.Reason).To(gomega.Equal("native_observation_uncertain"))
		d, err := openDirectory(root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer d.close()
		var v nativeDiagnostic
		gomega.Expect(d.read("native-diagnostic.json", &v)).To(gomega.Succeed())
		gomega.Expect(v.Binding).To(gomega.Equal(q.Binding()))
		gomega.Expect(v.Operation).To(gomega.Equal("process_census"))
		gomega.Expect(v.NativeOperation).To(gomega.Equal("process_snapshot_before"))
		gomega.Expect(v.PID).To(gomega.Equal(view.Start.Process.PID))
		gomega.Expect(v.Errno).To(gomega.Equal(int(unix.EIO)))
		before, err := os.ReadFile(filepath.Join(root, "native-diagnostic.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		replay, err := StartRun(filepath.Join(root, "request.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replay).To(gomega.Equal(view))
		gomega.Expect(os.ReadFile(filepath.Join(root, "native-diagnostic.json"))).To(gomega.Equal(before))
		gomega.Expect(os.ReadFile(filepath.Join(root, "workspace/count.txt"))).To(gomega.Equal([]byte("once")))
	})
})
