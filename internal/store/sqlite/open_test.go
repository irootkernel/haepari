package sqlite

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRejectLinkedDatabase(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	outside := filepath.Join(parent, "outside")
	if err := os.WriteFile(outside, []byte("leave intact"), 0600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(parent, "state.sqlite")
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	if db, err := Open(linked); err == nil {
		db.Close()
		t.Fatal("linked database accepted")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "leave intact" {
		t.Fatalf("outside file changed: %q, %v", data, err)
	}
}

func TestRejectPublicDatabase(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	path := filepath.Join(parent, "state.sqlite")
	if err := os.WriteFile(path, []byte("leave intact"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if db, err := Open(path); err == nil {
		db.Close()
		t.Fatal("public database accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "leave intact" {
		t.Fatalf("public database changed: %q, %v", data, err)
	}
}

func TestPrivateWALFiles(t *testing.T) {
	parent, err := os.MkdirTemp("/tmp", "hp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	path := filepath.Join(parent, "state.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE probe (value INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO probe (value) VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("SQLite journal mode: %q, %v", mode, err)
	}
	for _, setting := range []struct {
		name string
		want int
	}{
		{"foreign_keys", 1},
		{"busy_timeout", 5000},
		{"synchronous", 2},
		{"fullfsync", 1},
		{"checkpoint_fullfsync", 1},
	} {
		var got int
		if err := db.QueryRow("PRAGMA " + setting.name).Scan(&got); err != nil || got != setting.want {
			t.Fatalf("SQLite %s: %d, %v", setting.name, got, err)
		}
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("SQLite file permissions for %s: %v, %v", name, info, err)
		}
	}
}
