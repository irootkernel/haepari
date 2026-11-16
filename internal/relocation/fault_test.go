package relocation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

type faultRequest struct {
	Plan               Plan
	Action, Arm, Point string
}

func TestRelocationFaultChild(t *testing.T) {
	file := os.Getenv("HAEPARI_RELOCATION_CHILD")
	if file == "" {
		t.Skip("subprocess fixture only")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var q faultRequest
	if json.Unmarshal(raw, &q) != nil {
		t.Fatal("fixture")
	}
	if q.Action == "commit-wal" {
		root, err := openDirectory(q.Plan.SourcePlan.Root)
		if err != nil {
			t.Fatal(err)
		}
		release, err := acquire(root)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		db, err := sqlite.Open(filepath.Join(q.Plan.SourcePlan.Root, "state.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		ledger, err := managed.NewLedger(db, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
			t.Fatal(err)
		}
		if err = ledger.RegisterResource([]byte(`{"schema":"haepari.resource/v1","id":"wal-resource","kind":"local","execution_mode":"managed","capacity":1,"revision":1,"adapter":{"qualification_reference":"wal-fixture"},"cleanup":{"policy_reference":"owned-workspace"}}`)); err != nil {
			t.Fatal(err)
		}
		os.Exit(73)
	}
	var snapshots []*sqlite.Observation
	armed := q.Arm == ""
	env := Environment{ObservationOpened: func(o *sqlite.Observation) { snapshots = append(snapshots, o) }, MoverBinary: q.Plan.TargetPlan.Binary, ServicesStopped: func(ServicePlan) error { return nil }, ValidateService: func(p ServicePlan) error {
		raw, err := os.ReadFile(p.Binary)
		if err != nil {
			return err
		}
		if Digest(raw) != p.BinarySHA256 {
			return managed.ErrConflict
		}
		return nil
	}, Boundary: func(point string) {
		if point == q.Arm {
			armed = true
		}
		if armed && point == q.Point {
			for _, o := range snapshots {
				_ = o.Close()
			}
			os.Exit(73)
		}
	}}
	if _, err = Operate(q.Plan, q.Action, env); err != nil {
		t.Fatal(err)
	}
	t.Fatal("fault boundary not reached")
}
func interrupt(p Plan, action, arm, point string, parent string) {
	file := filepath.Join(parent, "fault-request.json")
	gomega.Expect(os.WriteFile(file, canonical(faultRequest{p, action, arm, point}), 0600)).To(gomega.Succeed())
	exe, err := os.Executable()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	cmd := exec.Command(exe, "-test.run=^TestRelocationFaultChild$")
	cmd.Env = append(os.Environ(), "HAEPARI_RELOCATION_CHILD="+file, "GORACE=atexit_sleep_ms=0")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	gomega.Expect(errors.As(err, &exit)).To(gomega.BeTrue(), string(out))
	gomega.Expect(exit.ExitCode()).To(gomega.Equal(73), string(out))
}

var _ = ginkgo.Describe("Task043RelocationFaults", ginkgo.Label("unit"), func() {
	ginkgo.It("continues the exact plan across real process interruption and partial external journal/owner publication", func() {
		cases := []struct{ arm, point string }{{"", "publication_partial"}, {"", "journal_prepared"}, {"", "backup_bound"}, {"", "backup_payload"}, {"before_backup_manifest", "publication_partial"}, {"", "backup_manifest"}, {"", "journal_backup_verified"}, {"before_journal_rename_pending", "publication_partial"}, {"", "journal_rename_pending"}, {"", "rename_effect"}, {"", "rename_durable"}, {"", "journal_moved"}, {"", "journal_owner_pending"}, {"before_owner_update", "publication_partial"}, {"", "owner_updated"}, {"", "journal_complete"}}
		for _, c := range cases {
			ginkgo.By(c.arm + "/" + c.point)
			f := newFixture()
			p, err := NewPlan(f.a, f.b, f.backup, f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			interrupt(p, "apply", c.arm, c.point, f.parent)
			r, err := Operate(p, "apply", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(r.Phase).To(gomega.Equal("complete"))
			_, err = Operate(p, "rollback", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
	})
	ginkgo.It("preserves an unbound mkdir orphan, refuses adoption and supports verified pre-move rollback and a new forward plan", func() {
		for _, c := range []struct{ arm, point string }{{"", "backup_mkdir"}, {"before_journal_prepared", "publication_partial"}} {
			f := newFixture()
			p, err := NewPlan(f.a, f.b, f.backup, f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			interrupt(p, "apply", c.arm, c.point, f.parent)
			before, err := os.Stat(f.backup)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			r, err := Operate(p, "status", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(r.BackupState).To(gomega.Equal("unproven"))
			_, err = Operate(p, "apply", f.env)
			gomega.Expect(errors.Is(err, managed.ErrConflict)).To(gomega.BeTrue())
			r, err = Operate(p, "rollback", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(r.Phase).To(gomega.Equal("rolled_back"))
			after, err := os.Stat(f.backup)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(os.SameFile(before, after)).To(gomega.BeTrue())
			entries, err := os.ReadDir(f.backup)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(entries).To(gomega.BeEmpty())
			p2, err := NewPlan(f.a, f.b, filepath.Join(f.parent, "new-backup"), f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(p2.Predecessor).NotTo(gomega.BeNil())
			_, err = Operate(p2, "apply", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = Operate(p, "apply", f.env)
			gomega.Expect(errors.Is(err, managed.ErrConflict)).To(gomega.BeTrue())
			old, err := Operate(p, "status", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(old.Phase).To(gomega.Equal("rolled_back"))
		}
	})
	ginkgo.It("preserves terminal predecessor history across interrupted archive and successor publications", func() {
		for _, point := range []string{"history_directory", "publication_partial", "predecessor_archived", "before_successor_publication", "successor_partial", "journal_prepared"} {
			ginkgo.By(point)
			f := newFixture()
			p, err := NewPlan(f.a, f.b, f.backup, f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = Operate(p, "apply", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = Operate(p, "rollback", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			next, err := NewPlan(f.a, f.b, filepath.Join(f.parent, "b2"), f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			arm := ""
			fault := point
			if point == "successor_partial" {
				arm = "before_successor_publication"
				fault = "publication_partial"
			}
			interrupt(next, "apply", arm, fault, f.parent)
			_, err = Operate(next, "apply", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = Operate(p, "rollback", f.env)
			gomega.Expect(errors.Is(err, managed.ErrConflict)).To(gomega.BeTrue())
			old, err := Operate(p, "status", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(old.Phase).To(gomega.Equal("rolled_back"))
		}
	})
	ginkgo.It("reconciles interrupted reverse move and refuses partial-journal daemon activation before DB writes", func() {
		for _, point := range []string{"journal_rollback_pending", "rollback_owner", "rename_effect", "rollback_rename", "journal_rolled_back"} {
			ginkgo.By(point)
			f := newFixture()
			p, err := NewPlan(f.a, f.b, f.backup, f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = Operate(p, "apply", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			interrupt(p, "rollback", "", point, f.parent)
			r, err := Operate(p, "rollback", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(r.Phase).To(gomega.Equal("rolled_back"))
		}
		f := newFixture()
		p, err := NewPlan(f.a, f.b, f.backup, f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		interrupt(p, "apply", "", "journal_prepared", f.parent)
		root, err := state.Resolve(f.source)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		lock, err := root.AcquireController()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		raw, err := os.ReadFile(root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(errors.Is(Activate(root.Path, lock), managed.ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(root.DBPath)).To(gomega.Equal(raw))
		gomega.Expect(lock.Close()).To(gomega.Succeed())
		_, err = Operate(p, "rollback", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})
	ginkgo.It("keeps lost-creator sibling staging untouched while unknown root-local files still conflict", func() {
		f := newFixture()
		p, err := NewPlan(f.a, f.b, f.backup, f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		interrupt(p, "apply", "", "publication_partial", f.parent)
		entries, err := os.ReadDir(f.parent)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var orphan string
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".haepari-publish-") {
				orphan = entry.Name()
			}
		}
		gomega.Expect(orphan).NotTo(gomega.BeEmpty())
		orphanPath := filepath.Join(f.parent, orphan, "record")
		raw, err := os.ReadFile(orphanPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = Operate(p, "apply", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.ReadFile(orphanPath)).To(gomega.Equal(raw))
		gomega.Expect(os.WriteFile(filepath.Join(f.target, ".unknown-temp"), []byte("foreign"), 0600)).To(gomega.Succeed())
		_, err = Operate(p, "rollback", f.env)
		gomega.Expect(errors.Is(err, managed.ErrConflict)).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(filepath.Join(f.target, ".unknown-temp"))).To(gomega.Equal([]byte("foreign")))
	})
	ginkgo.It("plans and observes committed crash-retained WAL without source writes, then preserves its exact sidecar identities through move and rollback", func() {
		f := newFixture()
		seed, err := NewPlan(f.a, f.b, f.backup, f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		interrupt(seed, "commit-wal", "", "", f.parent)
		wal, err := os.ReadFile(filepath.Join(f.source, "state.sqlite-wal"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(len(wal)).To(gomega.BeNumerically(">", 32))
		root, err := openDirectory(f.source)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		before, err := inventory(root, f.source)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		root.close()
		p, err := NewPlan(f.a, f.b, f.backup, f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = Operate(p, "status", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		root, err = openDirectory(f.source)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		after, err := inventory(root, f.source)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		root.close()
		gomega.Expect(equalFiles(before, after)).To(gomega.BeTrue())
		_, err = Operate(p, "apply", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.ReadFile(filepath.Join(f.target, "state.sqlite-wal"))).To(gomega.Equal(wal))
		_, err = Operate(p, "rollback", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.ReadFile(filepath.Join(f.source, "state.sqlite-wal"))).To(gomega.Equal(wal))
	})

})

func distinctFixture() fixture {
	f := newFixture()
	for _, name := range []string{"from", "to", "backups"} {
		gomega.Expect(os.Mkdir(filepath.Join(f.parent, name), 0700)).To(gomega.Succeed())
	}
	previous := f.source
	f.source = filepath.Join(f.parent, "from", "s")
	gomega.Expect(os.Rename(previous, f.source)).To(gomega.Succeed())
	f.a.Root = f.source
	f.target = filepath.Join(f.parent, "to", "t")
	f.b.Root = f.target
	f.backup = filepath.Join(f.parent, "backups", "b")
	gomega.Expect(os.WriteFile(filepath.Join(f.source, OwnerName), canonical(map[string]any{"schema": "haepari.release-service-owner/v1", "instance": f.a.Instance, "owner_uid": f.a.OwnerUID, "root": f.source}), 0600)).To(gomega.Succeed())
	return f
}

var _ = ginkgo.Describe("Task043RecoverySync", ginkgo.Label("unit"), func() {
	for _, reverse := range []bool{false, true} {
		name := "forward"
		if reverse {
			name = "reverse"
		}
		ginkgo.It("finishes both distinct rename parents after visible "+name+" effect before phase success", func() {
			f := distinctFixture()
			p, err := NewPlan(f.a, f.b, f.backup, f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			action := "apply"
			want := filepath.Dir(f.source)
			if reverse {
				_, err = Operate(p, "apply", f.env)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				action = "rollback"
				want = filepath.Dir(f.target)
			}
			interrupt(p, action, "", "rename_effect", f.parent)
			attempted := false
			f.env.DirectorySync = func(path string) error {
				if path == want {
					attempted = true
					return unix.EIO
				}
				return nil
			}
			_, err = Operate(p, action, f.env)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(attempted).To(gomega.BeTrue())
			root, current, err := locate(p)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			j, _, _, err := readJournal(root)
			root.close()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(terminal(j)).To(gomega.BeFalse())
			expected := "rename_pending"
			if reverse {
				expected = "rollback_pending"
			}
			gomega.Expect(j.Phase).To(gomega.Equal(expected))
			gomega.Expect(current).NotTo(gomega.BeEmpty())
			seen := map[string]bool{}
			f.env.DirectorySync = func(path string) error { seen[path] = true; return nil }
			_, err = Operate(p, action, f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(seen[filepath.Dir(f.source)]).To(gomega.BeTrue())
			gomega.Expect(seen[filepath.Dir(f.target)]).To(gomega.BeTrue())
		})
	}
	ginkgo.It("syncs an existing exact predecessor snapshot before succeeding its journal", func() {
		f := distinctFixture()
		p, err := NewPlan(f.a, f.b, f.backup, f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = Operate(p, "apply", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = Operate(p, "rollback", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		next, err := NewPlan(f.a, f.b, filepath.Join(filepath.Dir(f.backup), "b2"), f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		interrupt(next, "apply", "before_snapshot_publication", "publication_renamed", f.parent)
		attempted := false
		f.env.DirectorySync = func(path string) error {
			if path == filepath.Join(f.source, HistoryName) {
				attempted = true
				return unix.EIO
			}
			return nil
		}
		_, err = Operate(next, "apply", f.env)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(attempted).To(gomega.BeTrue())
		root, err := openDirectory(f.source)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		j, _, _, err := readJournal(root)
		root.close()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(j.PlanSHA256).To(gomega.Equal(p.PlanSHA256))
		f.env.DirectorySync = nil
		_, err = Operate(next, "apply", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})
	for _, kind := range []string{"payload", "manifest"} {
		ginkgo.It("syncs recovered "+kind+" directories before claiming verified backup", func() {
			f := distinctFixture()
			gomega.Expect(os.MkdirAll(filepath.Join(f.source, "nested", "leaf"), 0700)).To(gomega.Succeed())
			gomega.Expect(os.WriteFile(filepath.Join(f.source, "nested", "leaf", "evidence"), []byte("retained nested"), 0600)).To(gomega.Succeed())
			p, err := NewPlan(f.a, f.b, f.backup, f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			arm := "before_backup_manifest"
			want := f.backup
			if kind == "payload" {
				arm = "before_backup_payload:payload/nested/leaf/evidence"
				want = filepath.Join(f.backup, "payload", "nested", "leaf")
			}
			interrupt(p, "apply", arm, "publication_renamed", f.parent)
			attempted := false
			f.env.DirectorySync = func(path string) error {
				if path == want {
					attempted = true
					return unix.EIO
				}
				return nil
			}
			_, err = Operate(p, "apply", f.env)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(attempted).To(gomega.BeTrue())
			root, err := openDirectory(f.source)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			j, _, _, err := readJournal(root)
			root.close()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(j.Phase).To(gomega.Equal("prepared"))
			gomega.Expect(j.BackupState).To(gomega.Equal("bound"))
			f.env.DirectorySync = nil
			_, err = Operate(p, "apply", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
	}
})

var _ = ginkgo.Describe("Task043VisibleJournalSync", ginkgo.Label("unit"), func() {
	for _, phase := range []string{"prepared", "complete", "rolled_back"} {
		ginkgo.It("syncs a visible "+phase+" journal before effects or returning its receipt", func() {
			f := distinctFixture()
			p, err := NewPlan(f.a, f.b, f.backup, f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			action := "apply"
			current := f.target
			if phase == "prepared" {
				interrupt(p, "apply", "", "publication_renamed", f.parent)
				current = f.source
			} else if phase == "complete" {
				interrupt(p, "apply", "before_journal_complete", "publication_renamed", f.parent)
			} else {
				_, err = Operate(p, "apply", f.env)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				interrupt(p, "rollback", "before_journal_rolled_back", "publication_renamed", f.parent)
				action = "rollback"
				current = f.source
			}
			attempted := false
			f.env.DirectorySync = func(path string) error {
				if path == current {
					attempted = true
					return unix.EIO
				}
				return nil
			}
			_, err = Operate(p, action, f.env)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(attempted).To(gomega.BeTrue())
			if phase == "prepared" {
				_, err = os.Lstat(f.backup)
				gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			}
			f.env.DirectorySync = nil
			_, err = Operate(p, action, f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
	}
	for _, activated := range []bool{false, true} {
		ginkgo.It(fmt.Sprintf("finishes visible startup journal sync before normal open with activated=%v", activated), func() {
			f := newFixture()
			p, err := NewPlan(f.a, f.b, f.backup, f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = Operate(p, "apply", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			r, err := state.Resolve(f.target)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			lock, err := r.AcquireController()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer lock.Close()
			if activated {
				gomega.Expect(Activate(r.Path, lock)).To(gomega.Succeed())
			}
			before, err := os.ReadFile(filepath.Join(r.Path, JournalName))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			db, err := os.ReadFile(r.DBPath)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			attempted := false
			env := Environment{DirectorySync: func(path string) error {
				if path == r.Path {
					attempted = true
					return unix.EIO
				}
				return nil
			}}
			gomega.Expect(activate(r.Path, lock, env)).To(gomega.HaveOccurred())
			gomega.Expect(attempted).To(gomega.BeTrue())
			gomega.Expect(os.ReadFile(filepath.Join(r.Path, JournalName))).To(gomega.Equal(before))
			gomega.Expect(os.ReadFile(r.DBPath)).To(gomega.Equal(db))
			gomega.Expect(Activate(r.Path, lock)).To(gomega.Succeed())
		})
	}
})
