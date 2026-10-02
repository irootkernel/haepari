package snapshot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"golang.org/x/sys/unix"
)

// Cancel a real context during native payload reads, independent of disk speed.
type interruptedReadContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *interruptedReadContext) Err() error {
	c.checks++
	if c.checks == 10 {
		c.cancel()
	}
	return c.Context.Err()
}

var _ = ginkgo.Describe("Task012SealedObjectRetention", ginkgo.Label("unit"), func() {
	ginkgo.It("interrupts complete payload verification without changing owned bytes", func() {
		store, source := roots(ginkgo.GinkgoT())
		write(ginkgo.GinkgoT(), source, "large", string(make([]byte, 2<<20)), 0600)
		sealed, err := store.Seal(context.Background(), source, []string{"large"}, Provenance{EvidenceClass: "development"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err = store.ReadContext(&interruptedReadContext{Context: ctx, cancel: cancel}, sealed.ObjectID)
		gomega.Expect(err).To(gomega.MatchError(context.Canceled))
		verified, err := store.Read(sealed.ObjectID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(verified).To(gomega.Equal(sealed))
	})
	ginkgo.It("keeps recorded parent incarnations across reopen and partial deletion", func() {
		t := ginkgo.GinkgoT()
		store, source := roots(t)
		write(t, source, "nested/a", "kept", 0600)
		write(t, source, "nested/b", "removed", 0600)
		sealed, err := store.Seal(context.Background(), source, []string{"nested/a", "nested/b"}, Provenance{EvidenceClass: "development"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		inventory, err := json.Marshal(sealed.Manifest)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		dev, ino, err := store.ObjectIdentity(sealed.ObjectID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fences, err := store.PlanPurgeOwned(context.Background(), sealed.ObjectID, dev, ino, inventory)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		object := filepath.Dir(sealed.Root)
		parent := filepath.Join(sealed.Root, "nested")
		gomega.Expect(os.Remove(filepath.Join(parent, "b"))).To(gomega.Succeed())
		gomega.Expect(os.Rename(parent, parent+"-saved")).To(gomega.Succeed())
		gomega.Expect(os.Mkdir(parent, 0700)).To(gomega.Succeed())
		// Preserve the original leaf incarnation under a foreign parent: a leaf
		// check alone would wrongly permit deletion.
		gomega.Expect(os.Rename(filepath.Join(parent+"-saved", "a"), filepath.Join(parent, "a"))).To(gomega.Succeed())
		rootFD, err := unix.Open(object, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer unix.Close(rootFD)
		byPath := map[string]PurgePath{}
		for _, p := range fences {
			byPath[p.Path] = p
		}
		gomega.Expect(removeObjectEntry(rootFD, byPath["files/nested/a"], byPath)).To(gomega.MatchError(ErrChanged))
		gomega.Expect(os.ReadFile(filepath.Join(parent, "a"))).To(gomega.Equal([]byte("kept")))
		gomega.Expect(os.Rename(filepath.Join(parent, "a"), filepath.Join(parent+"-saved", "a"))).To(gomega.Succeed())
		gomega.Expect(os.Remove(parent)).To(gomega.Succeed())
		gomega.Expect(os.Rename(parent+"-saved", parent)).To(gomega.Succeed())
		reopened, err := OpenStore(filepath.Dir(store.root))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(reopened.PurgeOwned(context.Background(), sealed.ObjectID, dev, ino, inventory, fences)).To(gomega.Succeed())
		_, err = os.Stat(object)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		gomega.Expect(reopened.PurgeOwned(context.Background(), sealed.ObjectID, dev, ino, inventory, fences)).To(gomega.Succeed())
	})
})
