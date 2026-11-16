package relocation

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/irootkernel/haepari/internal/config"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task043RelocationRefusals", ginkgo.Label("unit"), func() {
	ginkgo.It("refuses loaded services, existing targets, busy original locks and inventory/binary replacements before moving", func() {
		for _, kind := range []string{"loaded", "controller", "store", "target", "link", "hardlink", "inventory", "identity", "binary", "parent"} {
			ginkgo.By(kind)
			f := newFixture()
			p, err := NewPlan(f.a, f.b, f.backup, f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			root, err := openDirectory(f.source)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			var cleanup func()
			switch kind {
			case "loaded":
				f.env.ServicesStopped = func(ServicePlan) error { return managed.ErrBlocked }
			case "controller":
				lock, err := lockExisting(root, "controller.lock")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				cleanup = lock.close
			case "store":
				objects, err := openAt(root.fd(), "objects", true)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				lock, err := lockExisting(objects, ".seal.lock")
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				cleanup = func() { lock.close(); objects.close() }
			case "target":
				gomega.Expect(os.Mkdir(f.target, 0700)).To(gomega.Succeed())
			case "link":
				gomega.Expect(os.Symlink(f.binary, filepath.Join(f.source, "unexpected-link"))).To(gomega.Succeed())
			case "hardlink":
				gomega.Expect(os.Link(f.binary, filepath.Join(f.source, "unexpected-hardlink"))).To(gomega.Succeed())
			case "inventory":
				gomega.Expect(os.WriteFile(filepath.Join(f.source, "retained.txt"), []byte("changed"), 0600)).To(gomega.Succeed())
			case "identity":
				old := filepath.Join(f.source, "retained.txt")
				raw, err := os.ReadFile(old)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(os.Rename(old, filepath.Join(f.parent, "original-retained"))).To(gomega.Succeed())
				gomega.Expect(os.WriteFile(old, raw, 0600)).To(gomega.Succeed())
			case "binary":
				gomega.Expect(os.WriteFile(f.binary, []byte("changed"), 0700)).To(gomega.Succeed())
			case "parent":
				p.TargetParentIdentity.Identity.VolumeUUID = "11111111111111111111111111111111"
				p.PlanSHA256 = planDigest(p)
			}
			_, err = Operate(p, "apply", f.env)
			gomega.Expect(err).To(gomega.HaveOccurred())
			if cleanup != nil {
				cleanup()
			}
			root.close()
			gomega.Expect(os.Stat(f.source)).Error().NotTo(gomega.HaveOccurred())
			_, err = os.Stat(f.backup)
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		}
	})
	ginkgo.It("refuses a replaced bound backup without adopting or deleting foreign bytes", func() {
		f := newFixture()
		p, err := NewPlan(f.a, f.b, f.backup, f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		interrupt(p, "apply", "", "backup_bound", f.parent)
		gomega.Expect(os.Rename(f.backup, filepath.Join(f.parent, "original-backup"))).To(gomega.Succeed())
		gomega.Expect(os.Mkdir(f.backup, 0700)).To(gomega.Succeed())
		sentinel := filepath.Join(f.backup, "foreign")
		gomega.Expect(os.WriteFile(sentinel, []byte("preserve"), 0600)).To(gomega.Succeed())
		for _, action := range []string{"apply", "rollback", "status"} {
			_, err = Operate(p, action, f.env)
			gomega.Expect(errors.Is(err, managed.ErrConflict)).To(gomega.BeTrue(), action)
		}
		gomega.Expect(os.ReadFile(sentinel)).To(gomega.Equal([]byte("preserve")))
	})
	ginkgo.It("durably activates once, permits subsequent ordinary work/startup and refuses rollback after changed history", func() {
		f := newFixture()
		p, err := NewPlan(f.a, f.b, f.backup, f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = Operate(p, "apply", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		root, err := state.Resolve(f.target)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		lock, err := root.AcquireController()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(Activate(root.Path, lock)).To(gomega.Succeed())
		journal := filepath.Join(root.Path, JournalName)
		activated, err := os.ReadFile(journal)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(Activate(root.Path, lock)).To(gomega.Succeed())
		gomega.Expect(os.ReadFile(journal)).To(gomega.Equal(activated))
		db, err := sqlite.Open(root.DBPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ledger, err := managed.NewLedger(db, 1)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		doc := config.Defaults()
		doc.Revision = 2
		doc.DefaultDeadlines.RunSeconds++
		_, err = ledger.CommitEffectiveConfig(doc, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(db.Close()).To(gomega.Succeed())
		gomega.Expect(Activate(root.Path, lock)).To(gomega.Succeed())
		gomega.Expect(os.ReadFile(journal)).To(gomega.Equal(activated))
		gomega.Expect(lock.Close()).To(gomega.Succeed())
		_, err = Operate(p, "rollback", f.env)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(os.Stat(f.target)).Error().NotTo(gomega.HaveOccurred())
	})
})

var _ = ginkgo.Describe("Task043DescriptorFences", ginkgo.Label("unit"), func() {
	ginkgo.It("keeps original controller and store locks held at the new path during both rename directions", func() {
		f := newFixture()
		p, err := NewPlan(f.a, f.b, f.backup, f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		observed := 0
		f.env.Boundary = func(point string) {
			if point != "rename_effect" {
				return
			}
			current := f.target
			if _, err := os.Stat(current); os.IsNotExist(err) {
				current = f.source
			}
			root, err := openDirectory(current)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer root.close()
			_, err = lockExisting(root, "controller.lock")
			gomega.Expect(errors.Is(err, managed.ErrBlocked)).To(gomega.BeTrue())
			objects, err := openAt(root.fd(), "objects", true)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer objects.close()
			_, err = lockExisting(objects, ".seal.lock")
			gomega.Expect(errors.Is(err, managed.ErrBlocked)).To(gomega.BeTrue())
			observed++
		}
		_, err = Operate(p, "apply", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = Operate(p, "rollback", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(observed).To(gomega.Equal(2))
	})
	ginkgo.It("refuses a replaced staged file before publication and preserves foreign siblings during a subsequent attempt", func() {
		f := newFixture()
		p, err := NewPlan(f.a, f.b, f.backup, f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var foreign string
		f.env.Boundary = func(point string) {
			if point != "publication_synced" || foreign != "" {
				return
			}
			entries, err := os.ReadDir(f.parent)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".haepari-publish-") {
					foreign = filepath.Join(f.parent, entry.Name(), "record")
					gomega.Expect(os.Rename(foreign, filepath.Join(f.parent, "original-staged-record"))).To(gomega.Succeed())
					gomega.Expect(os.WriteFile(foreign, []byte("foreign"), 0600)).To(gomega.Succeed())
					break
				}
			}
		}
		_, err = Operate(p, "apply", f.env)
		gomega.Expect(errors.Is(err, managed.ErrConflict)).To(gomega.BeTrue())
		gomega.Expect(foreign).NotTo(gomega.BeEmpty())
		gomega.Expect(os.ReadFile(foreign)).To(gomega.Equal([]byte("foreign")))
		_, err = os.Stat(filepath.Join(f.source, JournalName))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		f.env.Boundary = nil
		_, err = Operate(p, "apply", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.ReadFile(foreign)).To(gomega.Equal([]byte("foreign")))
	})
	ginkgo.It("refuses a live listener and preserves a proven stale socket's original identity through offline moves", func() {
		f := newFixture()
		name := filepath.Join(f.source, "control.sock")
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		listener.SetUnlinkOnClose(false)
		defer listener.Close()
		gomega.Expect(os.Chmod(name, 0600)).To(gomega.Succeed())
		_, err = NewPlan(f.a, f.b, f.backup, f.env)
		gomega.Expect(errors.Is(err, managed.ErrBlocked)).To(gomega.BeTrue())
		gomega.Expect(listener.Close()).To(gomega.Succeed())
		before, err := os.Stat(name)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		p, err := NewPlan(f.a, f.b, f.backup, f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = Operate(p, "apply", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		after, err := os.Stat(filepath.Join(f.target, "control.sock"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.SameFile(before, after)).To(gomega.BeTrue())
		_, err = Operate(p, "rollback", f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})
})

var _ = ginkgo.Describe("Task043PublicationDestination", ginkgo.Label("unit"), func() {
	ginkgo.It("refuses a root pathname replacement after staging sync before publishing into an orphaned original descriptor", func() {
		f := newFixture()
		p, err := NewPlan(f.a, f.b, f.backup, f.env)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		replaced := false
		f.env.Boundary = func(point string) {
			if point != "publication_synced" || replaced {
				return
			}
			replaced = true
			gomega.Expect(os.Rename(f.source, filepath.Join(f.parent, "original-root"))).To(gomega.Succeed())
			gomega.Expect(os.Mkdir(f.source, 0700)).To(gomega.Succeed())
			gomega.Expect(os.WriteFile(filepath.Join(f.source, "foreign"), []byte("preserve"), 0600)).To(gomega.Succeed())
		}
		_, err = Operate(p, "apply", f.env)
		gomega.Expect(errors.Is(err, managed.ErrConflict)).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(filepath.Join(f.source, "foreign"))).To(gomega.Equal([]byte("preserve")))
		_, err = os.Lstat(filepath.Join(f.parent, "original-root", JournalName))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		_, err = os.Lstat(f.backup)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
})

var _ = ginkgo.Describe("Task043FinalCompletion", ginkgo.Label("unit"), func() {
	for _, kind := range []string{"retained-file", "addition"} {
		ginkgo.It("keeps post-owner "+kind+" drift nonterminal until final verification succeeds", func() {
			f := newFixture()
			p, err := NewPlan(f.a, f.b, f.backup, f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			changed := filepath.Join(f.target, "retained.txt")
			original := []byte("never rewritten")
			if kind == "addition" {
				changed = filepath.Join(f.target, "unexpected-after-owner")
			}
			injected := false
			f.env.Boundary = func(point string) {
				if point != "owner_updated" || injected {
					return
				}
				injected = true
				gomega.Expect(os.WriteFile(changed, []byte("post-owner drift"), 0600)).To(gomega.Succeed())
			}
			_, err = Operate(p, "apply", f.env)
			gomega.Expect(errors.Is(err, managed.ErrConflict)).To(gomega.BeTrue())
			gomega.Expect(injected).To(gomega.BeTrue())
			held, err := openDirectory(f.target)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			j, _, _, err := readJournal(held)
			held.close()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(j.Phase).To(gomega.Equal("owner_pending"))
			gomega.Expect(j.CompletionReceipt).To(gomega.BeNil())
			status, err := Operate(p, "status", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(status.Phase).To(gomega.Equal("owner_pending"))
			root, err := state.Resolve(f.target)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			lock, err := root.AcquireController()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			before, err := os.ReadFile(root.DBPath)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(errors.Is(Activate(root.Path, lock), managed.ErrBlocked)).To(gomega.BeTrue())
			gomega.Expect(os.ReadFile(root.DBPath)).To(gomega.Equal(before))
			gomega.Expect(lock.Close()).To(gomega.Succeed())
			// Undo only the fixture-owned fault. Same-plan recovery remains supported;
			// the mover itself never repairs or hides retained-content drift.
			if kind == "addition" {
				gomega.Expect(os.Remove(changed)).To(gomega.Succeed())
			} else {
				gomega.Expect(os.WriteFile(changed, original, 0600)).To(gomega.Succeed())
			}
			result, err := Operate(p, "apply", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(result.Phase).To(gomega.Equal("complete"))
			_, err = Operate(p, "rollback", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
	}
})

var _ = ginkgo.Describe("Task043DirectStartupLock", ginkgo.Label("unit"), func() {
	for _, activated := range []bool{false, true} {
		for _, kind := range []string{"missing", "replaced"} {
			ginkgo.It(fmt.Sprintf("refuses %s original lock with activated=%v while its inode remains held", kind, activated), func() {
				f := newFixture()
				p, err := NewPlan(f.a, f.b, f.backup, f.env)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				_, err = Operate(p, "apply", f.env)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				root, err := state.Resolve(f.target)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				original, err := root.AcquireController()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				defer original.Close()
				if activated {
					gomega.Expect(Activate(root.Path, original)).To(gomega.Succeed())
				}
				lockPath := filepath.Join(root.Path, "controller.lock")
				gomega.Expect(os.Rename(lockPath, filepath.Join(f.parent, "held-original-lock"))).To(gomega.Succeed())
				supplied := original
				if kind == "replaced" {
					supplied, err = os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					defer supplied.Close()
				}
				journal, err := os.ReadFile(filepath.Join(root.Path, JournalName))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				database, err := os.ReadFile(root.DBPath)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(Activate(root.Path, supplied)).To(gomega.HaveOccurred())
				gomega.Expect(os.ReadFile(filepath.Join(root.Path, JournalName))).To(gomega.Equal(journal))
				gomega.Expect(os.ReadFile(root.DBPath)).To(gomega.Equal(database))
				if kind == "missing" {
					_, err = os.Lstat(lockPath)
					gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
				}
			})
		}
	}
})

var _ = ginkgo.Describe("Task043DurablePair", ginkgo.Label("unit"), func() {
	for _, active := range []bool{false, true} {
		for _, kind := range []string{"schema", "limits"} {
			for _, consumer := range []string{"acquire", "activate"} {
				label := kind + "/" + consumer
				if active {
					label += "/activated"
				}
				ginkgo.It("refuses internally rehashed unsupported journal "+label, func() {
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
					if active {
						gomega.Expect(Activate(r.Path, lock)).To(gomega.Succeed())
					}
					root, err := openDirectory(r.Path)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					j, _, _, err := readJournal(root)
					root.close()
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					if kind == "schema" {
						j.Plan.TargetPlan.Schema = "haepari.release-service-plan/v999"
					} else {
						j.Plan.TargetPlan.GlobalLimit++
					}
					j.Plan.PlanSHA256 = planDigest(j.Plan)
					j.PlanSHA256 = j.Plan.PlanSHA256
					j.CompletionReceipt.PlanSHA256 = j.PlanSHA256
					raw := canonical(j)
					name := filepath.Join(r.Path, JournalName)
					gomega.Expect(os.WriteFile(name, raw, 0600)).To(gomega.Succeed())
					database, err := os.ReadFile(r.DBPath)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					if consumer == "acquire" {
						gomega.Expect(lock.Close()).To(gomega.Succeed())
						fd, e := AcquireController(r)
						if fd != nil {
							fd.Close()
						}
						gomega.Expect(e).To(gomega.HaveOccurred())
					} else {
						gomega.Expect(Activate(r.Path, lock)).To(gomega.HaveOccurred())
					}
					gomega.Expect(os.ReadFile(name)).To(gomega.Equal(raw))
					gomega.Expect(os.ReadFile(r.DBPath)).To(gomega.Equal(database))
				})
			}
		}
	}
})

func initialHistoryFixture() (fixture, Journal) {
	f := newFixture()
	p, err := NewPlan(f.a, f.b, f.backup, f.env)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = Operate(p, "apply", f.env)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = Operate(p, "rollback", f.env)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	root, err := openDirectory(f.source)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	j, _, _, err := readJournal(root)
	root.close()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(os.Rename(filepath.Join(f.source, JournalName), filepath.Join(f.parent, "terminal-fixture.json"))).To(gomega.Succeed())
	f.backup = filepath.Join(f.parent, "new-backup")
	return f, j
}
func putInitialHistory(f fixture, j Journal, kind string) {
	if kind == "missing" {
		return
	}
	gomega.Expect(os.Mkdir(filepath.Join(f.source, HistoryName), 0700)).To(gomega.Succeed())
	if kind == "empty" {
		return
	}
	raw := canonical(j)
	name := j.Plan.RelocationID + "-" + strings.TrimPrefix(Digest(raw), "sha256:") + ".json"
	switch kind {
	case "unknown":
		name = "foreign.json"
		raw = []byte("{}")
	case "malformed":
		raw = []byte("{")
	case "wrong-installation":
		j.Plan.InstallationID = "local-other-installation"
		j.Plan.PlanSHA256 = planDigest(j.Plan)
		j.PlanSHA256 = j.Plan.PlanSHA256
		for _, r := range []*Receipt{j.CompletionReceipt, j.RollbackReceipt} {
			if r != nil {
				r.InstallationID = j.Plan.InstallationID
				r.PlanSHA256 = j.PlanSHA256
			}
		}
		raw = canonical(j)
		name = j.Plan.RelocationID + "-" + strings.TrimPrefix(Digest(raw), "sha256:") + ".json"
	}
	gomega.Expect(os.WriteFile(filepath.Join(f.source, HistoryName, name), raw, 0600)).To(gomega.Succeed())
}

var _ = ginkgo.Describe("Task043InitialHistory", ginkgo.Label("unit"), func() {
	for _, kind := range []string{"unknown", "malformed", "wrong-installation"} {
		ginkgo.It("refuses "+kind+" history before a usable first plan without changing startup eligibility", func() {
			f, j := initialHistoryFixture()
			putInitialHistory(f, j, kind)
			root, err := openDirectory(f.source)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer root.close()
			before, err := inventory(root, f.source)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = NewPlan(f.a, f.b, f.backup, f.env)
			gomega.Expect(err).To(gomega.HaveOccurred())
			after, err := inventory(root, f.source)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(equalFiles(before, after)).To(gomega.BeTrue())
			r, err := state.Resolve(f.source)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			lock, err := r.AcquireController()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(Activate(r.Path, lock)).To(gomega.Succeed())
			gomega.Expect(lock.Close()).To(gomega.Succeed())
			final, err := inventory(root, f.source)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(equalFiles(before, final)).To(gomega.BeTrue())
			_, err = os.Lstat(filepath.Join(f.source, JournalName))
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			_, err = os.Lstat(f.backup)
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		})
		ginkgo.It("independently refuses inventoried "+kind+" history before publishing a first journal", func() {
			f, j := initialHistoryFixture()
			p, err := NewPlan(f.a, f.b, f.backup, f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			putInitialHistory(f, j, kind)
			// Simulate a saved plan with internally consistent file inventory/digest.
			// An untrusted plan cannot waive the native history semantic preflight.
			root, err := openDirectory(f.source)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			defer root.close()
			p.Files, err = inventory(root, f.source)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			p.PlanSHA256 = planDigest(p)
			before := p.Files
			_, err = Operate(p, "apply", f.env)
			gomega.Expect(err).To(gomega.HaveOccurred())
			_, err = os.Lstat(filepath.Join(f.source, JournalName))
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			_, err = os.Lstat(f.backup)
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			after, err := inventory(root, f.source)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(equalFiles(before, after)).To(gomega.BeTrue())
			r, err := state.Resolve(f.source)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			lock, err := r.AcquireController()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(Activate(r.Path, lock)).To(gomega.Succeed())
			gomega.Expect(lock.Close()).To(gomega.Succeed())
			final, err := inventory(root, f.source)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(equalFiles(before, final)).To(gomega.BeTrue())
		})
	}
	for _, kind := range []string{"missing", "empty", "recognized"} {
		ginkgo.It("preserves supported "+kind+" history without adopting a predecessor", func() {
			f, j := initialHistoryFixture()
			putInitialHistory(f, j, kind)
			p, err := NewPlan(f.a, f.b, f.backup, f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(p.Predecessor).To(gomega.BeNil())
			_, err = Operate(p, "apply", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_, err = Operate(p, "rollback", f.env)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
	}
})
