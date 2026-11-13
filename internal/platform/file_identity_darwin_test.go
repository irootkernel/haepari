//go:build darwin

package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersistentDescriptorIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "object")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	before, err := PersistentFileIdentity(int(f.Fd()))
	if err != nil || !before.Valid() {
		t.Fatalf("descriptor identity: %+v %v", before, err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := PersistentFileIdentity(int(f.Fd()))
	if err != nil || before != after {
		t.Fatalf("descriptor changed after pathname replacement: %v", err)
	}
	replacement, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	other, err := PersistentFileIdentity(int(replacement.Fd()))
	if err != nil || other == before {
		t.Fatalf("replacement adopted: %+v %v", other, err)
	}
	if _, err := PersistentFileIdentity(-1); err == nil {
		t.Fatal("invalid descriptor accepted")
	}
}
