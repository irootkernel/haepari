package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveAndPrepare(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	root, err := Resolve(filepath.Join(parent, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Prepare(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root.Path)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("private root: %v, %v", info, err)
	}
}

func TestPrepareDoesNotCreateMissingAncestors(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	missing := filepath.Join(parent, "missing")
	root, err := Resolve(filepath.Join(missing, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Prepare(); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("missing parent classified incorrectly: %v", err)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing ancestor was created: %v", err)
	}
}

func TestRejectInvalidRoot(t *testing.T) {
	if _, err := Resolve("relative"); err == nil {
		t.Fatal("relative root accepted")
	}
	if _, err := Resolve("/" + strings.Repeat("x", 100)); err == nil {
		t.Fatal("long socket path accepted")
	}
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	if err := os.Mkdir(filepath.Join(parent, "public"), 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(parent, "public"), 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(filepath.Join(parent, "public")); err == nil {
		t.Fatal("public root accepted")
	}
	if _, err := Resolve(filepath.Join(parent, "public", "state")); err == nil {
		t.Fatal("shared ancestor accepted")
	}
	if err := os.Symlink(parent, filepath.Join(parent, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(filepath.Join(parent, "link")); err == nil {
		t.Fatal("symlink root accepted")
	}
	if _, err := Resolve(filepath.Join(parent, "link", "child")); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
}

func TestDefaultRoot(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	t.Setenv("HOME", parent)
	root, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatal(err)
	}
	if root.Path != filepath.Join(canonicalParent, ".haepari") {
		t.Fatalf("default root: %q", root.Path)
	}
}

func TestSocketPathBoundary(t *testing.T) {
	canonicalTmp, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Fatal(err)
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
}

func TestRejectNULAndFileAncestor(t *testing.T) {
	if _, err := Resolve("/tmp/x\x00y"); err == nil {
		t.Fatal("NUL path accepted")
	}
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(filepath.Join(file, "state")); err == nil {
		t.Fatal("file ancestor accepted")
	}
}
