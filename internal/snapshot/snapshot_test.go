package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func roots(t *testing.T) (*Store, string) {
	t.Helper()
	base := t.TempDir()
	state := filepath.Join(base, "state")
	source := filepath.Join(base, "source")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(state)
	if err != nil {
		t.Fatal(err)
	}
	return store, source
}

func write(t *testing.T, root, name, content string, mode os.FileMode) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, mode); err != nil {
		t.Fatal(err)
	}
}

func TestSealSelectedBytesIdentityAndReuse(t *testing.T) {
	store, source := roots(t)
	write(t, source, "scripts/run.sh", "#!/bin/sh\necho one\n", 0755)
	write(t, source, "tests/case.txt", "expected", 0644)
	write(t, source, "unselected.txt", "private", 0600)
	base := strings.Repeat("a", 40)
	first, err := store.Seal(context.Background(), source, []string{"tests/case.txt", "scripts/run.sh"}, Provenance{EvidenceClass: "development", BaseCommit: base})
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.Entries[0].Path != "scripts/run.sh" || first.Manifest.Entries[1].Path != "tests/case.txt" ||
		first.Manifest.Provenance.BaseCommit != base || first.Manifest.Algorithm != treeAlgorithm {
		t.Fatalf("wrong identity: %+v", first.Manifest)
	}
	if info, err := os.Stat(filepath.Join(first.Root, "scripts/run.sh")); err != nil || info.Mode().Perm() != 0500 {
		t.Fatalf("executable mode: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(first.Root, "unselected.txt")); !os.IsNotExist(err) {
		t.Fatalf("unselected file copied: %v", err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	read, err := store.Read(first.ObjectID)
	if err != nil || read.Manifest.TreeSHA256 != first.Manifest.TreeSHA256 {
		t.Fatalf("sealed object could not be reused: %+v %v", read, err)
	}
	if got, err := os.ReadFile(filepath.Join(read.Root, "tests/case.txt")); err != nil || string(got) != "expected" {
		t.Fatalf("sealed bytes changed: %q %v", got, err)
	}
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, source, "scripts/run.sh", "#!/bin/sh\necho one\n", 0755)
	write(t, source, "tests/case.txt", "expected", 0644)
	second, err := store.Seal(context.Background(), source, []string{"scripts/run.sh", "tests/case.txt"}, Provenance{EvidenceClass: "development", BaseCommit: strings.Repeat("b", 40)})
	if err != nil {
		t.Fatal(err)
	}
	if second.Manifest.TreeSHA256 != first.Manifest.TreeSHA256 || second.ObjectID == first.ObjectID {
		t.Fatalf("base commit must not replace tree identity: first=%+v second=%+v", first, second)
	}
	if err := os.Chmod(filepath.Join(source, "scripts/run.sh"), 0644); err != nil {
		t.Fatal(err)
	}
	third, err := store.Seal(context.Background(), source, []string{"scripts/run.sh", "tests/case.txt"}, Provenance{EvidenceClass: "development", BaseCommit: base})
	if err != nil || third.Manifest.TreeSHA256 == first.Manifest.TreeSHA256 {
		t.Fatalf("executable bit did not change tree identity: %+v %v", third, err)
	}
}

func TestRejectOutOfScopeAndUnsupportedInputs(t *testing.T) {
	store, source := roots(t)
	write(t, source, "safe.txt", "safe", 0644)
	write(t, source, "Case.txt", "upper", 0644)
	write(t, source, "case.txt", "lower", 0644)
	write(t, source, "dir/child.txt", "child", 0644)
	write(t, source, ".ssh/id_rsa", "not for bundles", 0600)
	if err := os.Symlink("safe.txt", filepath.Join(source, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("dir", filepath.Join(source, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(source, "safe.txt"), filepath.Join(source, "hard.txt")); err != nil {
		t.Fatal(err)
	}
	for _, selected := range [][]string{
		{"../outside"}, {"/etc/passwd"}, {"a/../../outside"}, {"a//b"}, {"a\\b"},
		{"Case.txt", "case.txt"}, {"dir/child.txt", "DIR/other.txt"},
		{"link.txt"}, {"linked-dir/child.txt"}, {"hard.txt"}, {".ssh/id_rsa"}, {"missing.txt"},
	} {
		if _, err := store.Seal(context.Background(), source, selected, Provenance{EvidenceClass: "development"}); err == nil {
			t.Errorf("accepted unsafe selection %v", selected)
		}
		objects, err := os.ReadDir(store.root)
		if err != nil {
			t.Fatalf("read objects: %v", err)
		}
		for _, object := range objects {
			if object.Name() != ".seal.lock" {
				t.Fatalf("%v left object or staging: %s", selected, object.Name())
			}
		}
	}
	if got, err := os.ReadFile(filepath.Join(source, "safe.txt")); err != nil || string(got) != "safe" {
		t.Fatalf("source was changed: %q %v", got, err)
	}
}

func TestCredentialRootAndOrphanStaging(t *testing.T) {
	store, source := roots(t)
	secretRoot := filepath.Join(source, ".ssh")
	write(t, secretRoot, "id_ecdsa", "private key bytes", 0600)
	write(t, secretRoot, "notes.txt", "ordinary name beneath credential root", 0600)
	for _, selected := range []string{"id_ecdsa", "notes.txt"} {
		if _, err := store.Seal(context.Background(), secretRoot, []string{selected}, Provenance{EvidenceClass: "development"}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("credential root accepted %s: %v", selected, err)
		}
	}
	orphan := filepath.Join(store.root, ".staging-interrupted")
	write(t, orphan, "files/payload", "stranded", 0600)
	if _, err := OpenStore(filepath.Dir(store.root)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan staging remains: %v", err)
	}
	journal, err := os.ReadFile(filepath.Join(store.root, "staging-cleanup.jsonl"))
	if err != nil || strings.Count(string(journal), `"name":".staging-interrupted"`) != 2 ||
		!strings.Contains(string(journal), `"phase":"intent"`) || !strings.Contains(string(journal), `"phase":"complete"`) || strings.Contains(string(journal), "stranded") {
		t.Fatalf("missing bounded cleanup record: %q %v", journal, err)
	}
	// Simulate termination after removing a staged directory but before
	// recording completion. The durable intent must be finished on restart.
	if err := store.recordCleanup(".staging-interrupted-again", "intent", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(filepath.Dir(store.root)); err != nil {
		t.Fatal(err)
	}
	journal, err = os.ReadFile(filepath.Join(store.root, "staging-cleanup.jsonl"))
	if err != nil || strings.Count(string(journal), `"name":".staging-interrupted-again"`) != 2 {
		t.Fatalf("pending intent was lost: %q %v", journal, err)
	}
}

func TestCorruptionIsNotReused(t *testing.T) {
	store, source := roots(t)
	write(t, source, "input.txt", "sealed", 0644)
	snap, err := store.Seal(context.Background(), source, []string{"input.txt"}, Provenance{EvidenceClass: "development"})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(snap.Root, "input.txt")
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(snap.ObjectID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt object verified: %v", err)
	}
	if _, err := store.Seal(context.Background(), source, []string{"input.txt"}, Provenance{EvidenceClass: "development"}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt duplicate was reused: %v", err)
	}
}

func TestUnexpectedObjectFileIsRejected(t *testing.T) {
	store, source := roots(t)
	write(t, source, "input.txt", "sealed", 0644)
	snap, err := store.Seal(context.Background(), source, []string{"input.txt"}, Provenance{EvidenceClass: "development"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap.Root, "extra.txt"), []byte("unlisted"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(snap.ObjectID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("object with unlisted file verified: %v", err)
	}
}

func TestProvenanceMustBeExplicit(t *testing.T) {
	store, source := roots(t)
	write(t, source, "input.txt", "bytes", 0644)
	for _, p := range []Provenance{
		{EvidenceClass: "candidate-integration"},
		{EvidenceClass: "release-distribution", BaseCommit: strings.Repeat("a", 40)},
		{EvidenceClass: "development", ReleaseReference: "v1"},
		{EvidenceClass: "development", BaseCommit: "HEAD"},
	} {
		if _, err := store.Seal(context.Background(), source, []string{"input.txt"}, p); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted unsupported provenance %+v: %v", p, err)
		}
	}
}

func TestConcurrentEditCannotSeal(t *testing.T) {
	store, source := roots(t)
	name := filepath.Join(source, "large.bin")
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(32 << 20); err != nil {
		t.Fatal(err)
	}
	file.Close()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		writer, err := os.OpenFile(name, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer writer.Close()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = writer.WriteAt([]byte("changed"), 0)
				time.Sleep(time.Millisecond)
			}
		}
	}()
	_, err = store.Seal(context.Background(), source, []string{"large.bin"}, Provenance{EvidenceClass: "development"})
	close(stop)
	<-done
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("concurrently edited source sealed: %v", err)
	}
}

func TestEarlierSelectionChangedDuringLaterCopy(t *testing.T) {
	store, source := roots(t)
	write(t, source, "a.txt", "before", 0644)
	large := filepath.Join(source, "z.bin")
	file, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	file.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10000; i++ {
			matches, _ := filepath.Glob(filepath.Join(store.root, ".staging-*", "files", "z.bin"))
			if len(matches) != 0 {
				_ = os.WriteFile(filepath.Join(source, "a.txt"), []byte("after"), 0644)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	_, err = store.Seal(context.Background(), source, []string{"a.txt", "z.bin"}, Provenance{EvidenceClass: "development"})
	<-done
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("earlier source changed during later copy: %v", err)
	}
}

func TestRemovedSourceCannotSeal(t *testing.T) {
	store, source := roots(t)
	name := filepath.Join(source, "large.bin")
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	file.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10000; i++ {
			matches, _ := filepath.Glob(filepath.Join(store.root, ".staging-*", "files", "large.bin"))
			if len(matches) != 0 {
				_ = os.Remove(name)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	_, err = store.Seal(context.Background(), source, []string{"large.bin"}, Provenance{EvidenceClass: "development"})
	<-done
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("removed source sealed: %v", err)
	}
}
