//go:build integration

package sqlite

import (
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task043WALObservation", ginkgo.Label("integration"), func() {
	ginkgo.It("reads committed uncheckpointed WAL and backs up the same logical transaction without touching source sidecars", func() {
		dir := ginkgo.GinkgoT().TempDir()
		path := filepath.Join(dir, "state.sqlite")
		db, err := Open(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer db.Close()
		_, err = db.Exec("PRAGMA wal_autocheckpoint=0; CREATE TABLE retained(v); INSERT INTO retained VALUES('committed only in WAL'),(x'00ff'),(3.141592653589793),(NULL),(9223372036854775807)")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		before := map[string][]byte{}
		infos := map[string]os.FileInfo{}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			before[suffix], err = os.ReadFile(path + suffix)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			infos[suffix], err = os.Stat(path + suffix)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		gomega.Expect(len(before["-wal"])).To(gomega.BeNumerically(">", 32))
		observation, err := Observe(before[""], before["-wal"])
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer observation.Close()
		var count int
		gomega.Expect(observation.Tx.QueryRow("SELECT COUNT(*) FROM retained").Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.Equal(5))
		digest, err := LogicalDigest(observation.Tx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		backup := filepath.Join(dir, "backup.sqlite")
		gomega.Expect(observation.Backup(backup)).To(gomega.Succeed())
		copied, err := os.ReadFile(backup)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		verified, err := Observe(copied, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer verified.Close()
		got, err := LogicalDigest(verified.Tx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(got).To(gomega.Equal(digest))
		for suffix, raw := range before {
			got, err := os.ReadFile(path + suffix)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(got).To(gomega.Equal(raw))
			after, err := os.Stat(path + suffix)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(os.SameFile(infos[suffix], after)).To(gomega.BeTrue())
			gomega.Expect(after.ModTime()).To(gomega.Equal(infos[suffix].ModTime()))
		}
	})
	ginkgo.It("reads a database without sidecars and never creates them beside its source", func() {
		path := filepath.Join(ginkgo.GinkgoT().TempDir(), "state.sqlite")
		db, err := Open(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec("CREATE TABLE retained(v); INSERT INTO retained VALUES('stable')")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(db.Close()).To(gomega.Succeed())
		raw, err := os.ReadFile(path)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		observation, err := Observe(raw, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(observation.Close()).To(gomega.Succeed())
		for _, suffix := range []string{"-wal", "-shm"} {
			_, err = os.Lstat(path + suffix)
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		}
	})
})
