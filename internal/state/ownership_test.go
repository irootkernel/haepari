package state

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("ControllerOwnership", ginkgo.Label("unit"), func() {
	ginkgo.It("excludes another descriptor and allows reacquisition without unlinking", func() {
		t := ginkgo.GinkgoT()
		r := Root{Path: t.TempDir()}
		first, err := r.AcquireController()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		t.Cleanup(func() { _ = first.Close() })
		original, err := first.Stat()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		second, err := r.AcquireController()
		gomega.Expect(second).To(gomega.BeNil())
		gomega.Expect(errors.Is(err, ErrControllerOwned)).To(gomega.BeTrue())
		gomega.Expect(first.Close()).To(gomega.Succeed())
		next, err := r.AcquireController()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer next.Close()
		current, err := next.Stat()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.SameFile(original, current)).To(gomega.BeTrue())
		gomega.Expect(current.Mode().Perm()).To(gomega.Equal(os.FileMode(0600)))
	})
	ginkgo.It("rejects public files, symlinks and linked lock inodes", func() {
		for _, kind := range []string{"public", "symlink", "hardlink"} {
			r := Root{Path: ginkgo.GinkgoT().TempDir()}
			path := filepath.Join(r.Path, "controller.lock")
			target := filepath.Join(r.Path, "target")
			gomega.Expect(os.WriteFile(target, []byte("preserved"), 0600)).To(gomega.Succeed())
			switch kind {
			case "public":
				gomega.Expect(os.WriteFile(path, nil, 0644)).To(gomega.Succeed())
				gomega.Expect(os.Chmod(path, 0644)).To(gomega.Succeed())
			case "symlink":
				gomega.Expect(os.Symlink(target, path)).To(gomega.Succeed())
			case "hardlink":
				gomega.Expect(os.Link(target, path)).To(gomega.Succeed())
			}
			owner, err := r.AcquireController()
			gomega.Expect(owner).To(gomega.BeNil())
			gomega.Expect(errors.Is(err, ErrInvalidRoot)).To(gomega.BeTrue())
			data, err := os.ReadFile(target)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(string(data)).To(gomega.Equal("preserved"))
		}
	})
})

func TestMain(m *testing.M) {
	if path := os.Getenv("HAEPARI_OWNERSHIP_PROBE"); path != "" {
		file, err := (Root{Path: path}).AcquireController()
		if err != nil {
			panic(err)
		}
		defer file.Close()
		if err := os.WriteFile(filepath.Join(path, "ready"), nil, 0600); err != nil {
			panic(err)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(m.Run())
}

var _ = ginkgo.Describe("ControllerOwnershipCrash", ginkgo.Label("unit"), func() {
	ginkgo.It("releases kernel ownership after process death", func() {
		t := ginkgo.GinkgoT()
		r := Root{Path: t.TempDir()}
		executable, err := os.Executable()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		child := exec.Command(executable)
		child.Env = append(os.Environ(), "HAEPARI_OWNERSHIP_PROBE="+r.Path)
		gomega.Expect(child.Start()).To(gomega.Succeed())
		waited := false
		t.Cleanup(func() {
			if !waited {
				_ = child.Process.Kill()
				_ = child.Wait()
			}
		})
		ready := false
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(r.Path, "ready")); err == nil {
				ready = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		gomega.Expect(ready).To(gomega.BeTrue())
		owner, err := r.AcquireController()
		gomega.Expect(owner).To(gomega.BeNil())
		gomega.Expect(errors.Is(err, ErrControllerOwned)).To(gomega.BeTrue())
		gomega.Expect(child.Process.Kill()).To(gomega.Succeed())
		gomega.Expect(child.Wait()).To(gomega.HaveOccurred())
		waited = true
		owner, err = r.AcquireController()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer owner.Close()
	})
})
