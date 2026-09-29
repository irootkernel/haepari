package state

import (
	"errors"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"os"
	"path/filepath"
	"strings"
)

var _ = ginkgo.Describe("ResolveAndPrepare", ginkgo.Label("unit"), func() {
	ginkgo.It("ResolveAndPrepare", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		root, err := Resolve(filepath.Join(parent, "state"))
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := root.Prepare(); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		info, err := os.Stat(root.Path)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("private root: %v, %v", info, err)
		}

	})
})

var _ = ginkgo.Describe("PrepareDoesNotCreateMissingAncestors", ginkgo.Label("unit"), func() {
	ginkgo.It("PrepareDoesNotCreateMissingAncestors", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		missing := filepath.Join(parent, "missing")
		root, err := Resolve(filepath.Join(missing, "state"))
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := root.Prepare(); !errors.Is(err, ErrInvalidRoot) {
			t.Fatalf("missing parent classified incorrectly: %v", err)
		}
		if _, err := os.Lstat(missing); !os.IsNotExist(err) {
			t.Fatalf("missing ancestor was created: %v", err)
		}

	})
})

var _ = ginkgo.Describe("RejectInvalidRoot", ginkgo.Label("unit"), func() {
	ginkgo.It("RejectInvalidRoot", func() {
		t := ginkgo.GinkgoT()
		if _, err := Resolve("relative"); err == nil {
			t.Fatal("relative root accepted")
		}
		if _, err := Resolve("/" + strings.Repeat("x", 100)); err == nil {
			t.Fatal("long socket path accepted")
		}
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		if err := os.Mkdir(filepath.Join(parent, "public"), 0777); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := os.Chmod(filepath.Join(parent, "public"), 0777); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := Resolve(filepath.Join(parent, "public")); err == nil {
			t.Fatal("public root accepted")
		}
		if _, err := Resolve(filepath.Join(parent, "public", "state")); err == nil {
			t.Fatal("shared ancestor accepted")
		}
		if err := os.Symlink(parent, filepath.Join(parent, "link")); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := Resolve(filepath.Join(parent, "link")); err == nil {
			t.Fatal("symlink root accepted")
		}
		if _, err := Resolve(filepath.Join(parent, "link", "child")); err == nil {
			t.Fatal("symlink ancestor accepted")
		}

	})
})

var _ = ginkgo.Describe("DefaultRoot", ginkgo.Label("unit"), func() {
	ginkgo.It("DefaultRoot", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		t.Setenv("HOME", parent)
		root, err := Resolve("")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		canonicalParent, err := filepath.EvalSymlinks(parent)
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if root.Path != filepath.Join(canonicalParent, ".haepari") {
			t.Fatalf("default root: %q", root.Path)
		}

	})
})

var _ = ginkgo.Describe("SocketPathBoundary", ginkgo.Label("unit"), func() {
	ginkgo.It("SocketPathBoundary", func() {
		t := ginkgo.GinkgoT()
		canonicalTmp, err := filepath.EvalSymlinks("/tmp")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		segmentLen := 103 - len(canonicalTmp) - 1 - len("/control.sock")
		if _, err := Resolve("/tmp/" + strings.Repeat("x", segmentLen)); err != nil {
			t.Fatalf("103-byte socket path rejected: %v", err)
		}
		if _, err := Resolve("/tmp/" + strings.Repeat("x", segmentLen+1)); err == nil {
			t.Fatal("104-byte socket path accepted")
		}
		if _, err := Resolve("/tmp/" + strings.Repeat("é", segmentLen/2+1)); err == nil {
			t.Fatal("multibyte overlong socket path accepted")
		}

	})
})

var _ = ginkgo.Describe("RejectNULAndFileAncestor", ginkgo.Label("unit"), func() {
	ginkgo.It("RejectNULAndFileAncestor", func() {
		t := ginkgo.GinkgoT()
		if _, err := Resolve("/tmp/x\x00y"); err == nil {
			t.Fatal("NUL path accepted")
		}
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		file := filepath.Join(parent, "file")
		if err := os.WriteFile(file, nil, 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := Resolve(filepath.Join(file, "state")); err == nil {
			t.Fatal("file ancestor accepted")
		}

	})
})
