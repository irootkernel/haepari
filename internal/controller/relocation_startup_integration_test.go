//go:build integration

package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/irootkernel/haepari/internal/config"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/relocation"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func relocationStartupRoot(partial bool) state.Root {
	parent, err := os.MkdirTemp("/tmp", "hs-")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	parent, err = filepath.EvalSymlinks(parent)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ginkgo.DeferCleanup(func() { gomega.Expect(os.RemoveAll(parent)).To(gomega.Succeed()) })
	root, err := state.Resolve(filepath.Join(parent, "s"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(root.Prepare()).To(gomega.Succeed())
	lock, err := root.AcquireController()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(lock.Close()).To(gomega.Succeed())
	db, err := sqlite.Open(root.DBPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ledger, err := managed.NewLedger(db, 1)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = ledger.BindInstallation("local-startup-relocation-fixture")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = ledger.CommitEffectiveConfig(config.Defaults(), false)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(db.Close()).To(gomega.Succeed())
	_, err = snapshot.OpenStore(root.Path)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	binary := filepath.Join(parent, "mover")
	gomega.Expect(os.WriteFile(binary, []byte("startup fixture"), 0700)).To(gomega.Succeed())
	a := relocation.ServicePlan{Schema: "haepari.release-service-plan/v1", Instance: "startup", Label: "io.rootkernel.haepari.startup", Domain: "gui/" + strconv.Itoa(os.Geteuid()), OwnerUID: os.Geteuid(), Binary: binary, BinarySHA256: relocation.Digest([]byte("startup fixture")), Root: root.Path, PlistPath: filepath.Join(parent, "absent.plist"), Plist: "fixture source", GlobalLimit: 1, HostMACOSGuestLimit: 1}
	b := a
	b.Schema = "haepari.release-service-plan/v2"
	b.Root = filepath.Join(parent, "t")
	b.Plist = "fixture target"
	gomega.Expect(os.WriteFile(filepath.Join(root.Path, relocation.OwnerName), mustJSONScheduling(map[string]any{"schema": "haepari.release-service-owner/v1", "instance": a.Instance, "owner_uid": a.OwnerUID, "root": a.Root}), 0600)).To(gomega.Succeed())
	env := relocation.Environment{MoverBinary: binary, ServicesStopped: func(relocation.ServicePlan) error { return nil }, ValidateService: func(relocation.ServicePlan) error { return nil }}
	p, err := relocation.NewPlan(a, b, filepath.Join(parent, "b"), env)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	if partial {
		env.Boundary = func(point string) {
			if point == "before_rename" {
				gomega.Expect(os.Mkdir(b.Root, 0700)).To(gomega.Succeed())
			}
		}
	}
	_, err = relocation.Operate(p, "apply", env)
	if partial {
		gomega.Expect(err).To(gomega.HaveOccurred())
		return root
	}
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	root, err = state.Resolve(b.Root)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return root
}

var _ = ginkgo.Describe("Task043ControllerStartupLock", ginkgo.Label("integration"), func() {
	for _, activated := range []bool{false, true} {
		for _, kind := range []string{"missing", "replaced"} {
			ginkgo.It(fmt.Sprintf("does not start on %s original lock with activated=%v while old inode is held", kind, activated), func() {
				root := relocationStartupRoot(false)
				original, err := root.AcquireController()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				defer original.Close()
				if activated {
					gomega.Expect(relocation.Activate(root.Path, original)).To(gomega.Succeed())
				}
				lockPath := filepath.Join(root.Path, "controller.lock")
				gomega.Expect(os.Rename(lockPath, filepath.Join(filepath.Dir(root.Path), "held-original-lock"))).To(gomega.Succeed())
				var replacement os.FileInfo
				if kind == "replaced" {
					gomega.Expect(os.WriteFile(lockPath, []byte("replacement must remain"), 0600)).To(gomega.Succeed())
					replacement, err = os.Stat(lockPath)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				database, err := os.ReadFile(root.DBPath)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				journal, err := os.ReadFile(filepath.Join(root.Path, relocation.JournalName))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				gomega.Expect(RunWithOptions(ctx, root, Options{GlobalLimit: 1, HostMACOSGuestLimit: 1})).To(gomega.HaveOccurred())
				gomega.Expect(os.ReadFile(root.DBPath)).To(gomega.Equal(database))
				gomega.Expect(os.ReadFile(filepath.Join(root.Path, relocation.JournalName))).To(gomega.Equal(journal))
				if kind == "missing" {
					_, err = os.Lstat(lockPath)
					gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
				} else {
					after, err := os.Stat(lockPath)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Expect(os.SameFile(replacement, after)).To(gomega.BeTrue())
					gomega.Expect(os.ReadFile(lockPath)).To(gomega.Equal([]byte("replacement must remain")))
				}
			})
		}
	}
	ginkgo.It("never recreates an absent lock for a partial journal", func() {
		root := relocationStartupRoot(true)
		lockPath := filepath.Join(root.Path, "controller.lock")
		gomega.Expect(os.Remove(lockPath)).To(gomega.Succeed())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		gomega.Expect(RunWithOptions(ctx, root, Options{GlobalLimit: 1, HostMACOSGuestLimit: 1})).To(gomega.HaveOccurred())
		_, err := os.Lstat(lockPath)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
	ginkgo.It("still creates and starts an ordinary new home without a journal", func() {
		parent, err := os.MkdirTemp("/tmp", "hn-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		parent, err = filepath.EvalSymlinks(parent)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer os.RemoveAll(parent)
		root, err := state.Resolve(filepath.Join(parent, "new"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		gomega.Expect(RunWithOptions(ctx, root, Options{GlobalLimit: 1, HostMACOSGuestLimit: 1})).To(gomega.Succeed())
		gomega.Expect(os.Stat(root.DBPath)).Error().NotTo(gomega.HaveOccurred())
		_, err = os.Lstat(filepath.Join(root.Path, relocation.JournalName))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		lock, err := root.AcquireController()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(lock.Close()).To(gomega.Succeed())
	})
})
