//go:build integration

package sqlite

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"os"
	"path/filepath"
)

var _ = ginkgo.Describe("RejectLinkedDatabase", ginkgo.Label("integration"), func() {
	ginkgo.It("RejectLinkedDatabase", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		outside := filepath.Join(parent, "outside")
		if err := os.WriteFile(outside, []byte("leave intact"), 0600); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		linked := filepath.Join(parent, "state.sqlite")
		if err := os.Symlink(outside, linked); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if db, err := Open(linked); err == nil {
			db.Close()
			t.Fatal("linked database accepted")
		}
		data, err := os.ReadFile(outside)
		if err != nil || string(data) != "leave intact" {
			t.Fatalf("outside file changed: %q, %v", data, err)
		}

	})
})

var _ = ginkgo.Describe("RejectPublicDatabase", ginkgo.Label("integration"), func() {
	ginkgo.It("RejectPublicDatabase", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		path := filepath.Join(parent, "state.sqlite")
		if err := os.WriteFile(path, []byte("leave intact"), 0644); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if err := os.Chmod(path, 0644); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if db, err := Open(path); err == nil {
			db.Close()
			t.Fatal("public database accepted")
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "leave intact" {
			t.Fatalf("public database changed: %q, %v", data, err)
		}

	})
})

var _ = ginkgo.Describe("PrivateWALFiles", ginkgo.Label("integration"), func() {
	ginkgo.It("PrivateWALFiles", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-")
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		t.Cleanup(func() { _ = os.RemoveAll(parent) })
		path := filepath.Join(parent, "state.sqlite")
		db, err := Open(path)
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		defer db.Close()
		if _, err := db.Exec("CREATE TABLE probe (value INTEGER)"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if _, err := db.Exec("INSERT INTO probe (value) VALUES (1)"); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
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
		// Closing the idle connection forces database/sql to use a new driver
		// connection, where connection-local pragmas must still be in force.
		db.SetMaxIdleConns(0)
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
				t.Fatalf("replacement SQLite %s: %d, %v", setting.name, got, err)
			}
		}

	})
})
