//go:build integration

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/utm"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("PartialGuestLogExport", ginkgo.Label("integration"), func() {
	ginkgo.It("exports immutable gaps and verified outputs before log recollection, while refusing ambiguous or lost evidence", func() {
		f := nativeVMFixture(ginkgo.GinkgoT(), "mkdir -p ../results; printf retained > ../results/output.txt; printf native-log")
		native := f.s.sshCommand
		f.s.sshCommand = func(ctx context.Context, exe string, args []string, input []byte) (utm.CommandResult, error) {
			q := vmTransportRequest(input)
			if q.Operation == "file" && q.Area == "log" {
				return utm.CommandResult{}, errors.New("fixture log transport unavailable")
			}
			return native(ctx, exe, args, input)
		}
		original := f.run("partial-export")
		gomega.Expect(original.FinalVerdict).To(gomega.Equal(managed.Incomplete))
		gomega.Expect(original.EvidenceGaps).To(gomega.ContainElement(managed.EvidenceGap{Path: "output.log", Reason: "guest_log_uncollected"}))
		gomega.Expect(original.Artifacts).NotTo(gomega.BeEmpty())
		jobRoot := filepath.Join(f.s.root.Path, "jobs", original.JobID)
		logPath := filepath.Join(jobRoot, "output.log")
		bindingPath := filepath.Join(jobRoot, "output-identity.json")
		for _, path := range []string{logPath, bindingPath} {
			_, err := os.Lstat(path)
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		}
		c, err := client.Connect(context.Background(), f.s.root)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer c.Close()
		destination := filepath.Join(ginkgo.GinkgoT().TempDir(), "partial")
		manifest, _, err := c.Export(context.Background(), original.JobID, destination)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(manifest.Result).To(gomega.Equal(original))
		gomega.Expect(manifest.Files).To(gomega.HaveLen(len(original.Artifacts)))
		gomega.Expect(manifest.Supplementary).To(gomega.BeEmpty())
		raw, err := os.ReadFile(filepath.Join(destination, "result.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var exported managed.Result
		gomega.Expect(json.Unmarshal(raw, &exported)).To(gomega.Succeed())
		gomega.Expect(exported).To(gomega.Equal(original))
		gomega.Expect(os.ReadFile(filepath.Join(destination, "artifacts/results/output.txt"))).To(gomega.Equal([]byte("retained")))
		gomega.Expect(os.Stat(filepath.Join(destination, "manifest.json"))).Error().NotTo(gomega.HaveOccurred())
		_, err = os.Lstat(filepath.Join(destination, "output.log"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())

		refuse := func() {
			ginkgo.GinkgoHelper()
			dest := filepath.Join(ginkgo.GinkgoT().TempDir(), "refused")
			_, _, err := c.Export(context.Background(), original.JobID, dest)
			gomega.Expect(err).To(gomega.HaveOccurred())
			_, err = os.Stat(filepath.Join(dest, "manifest.json"))
			gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
			gomega.Expect(f.s.ledger.Result(original.JobID)).To(gomega.Equal(original))
		}
		// A present but unbound log is not the recorded absence.
		gomega.Expect(os.WriteFile(logPath, []byte("native-log"), 0600)).To(gomega.Succeed())
		refuse()
		observation, err := f.s.ledger.LogObservation(original.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		log, err := os.Open(logPath)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(f.s.bindLocalLog(observation.Snapshot, observation.RunID, log)).To(gomega.Succeed())
		log.Close()
		// A collected log must still verify against the immutable guest hash.
		gomega.Expect(os.WriteFile(logPath, []byte("changed-log"), 0600)).To(gomega.Succeed())
		refuse()
		gomega.Expect(os.WriteFile(logPath, []byte("native-log"), 0600)).To(gomega.Succeed())
		gomega.Expect(os.Chmod(logPath, 0000)).To(gomega.Succeed())
		refuse()
		gomega.Expect(os.Chmod(logPath, 0600)).To(gomega.Succeed())
		gomega.Expect(os.Rename(logPath, logPath+"-original")).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(logPath, []byte("native-log"), 0600)).To(gomega.Succeed())
		refuse() // Same bytes, different native file incarnation.
		gomega.Expect(os.Remove(logPath)).To(gomega.Succeed())
		refuse() // Previously collected: durable binding still exists.
		gomega.Expect(os.Chmod(bindingPath, 0000)).To(gomega.Succeed())
		refuse()
		gomega.Expect(os.Chmod(bindingPath, 0600)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(bindingPath, []byte("malformed"), 0600)).To(gomega.Succeed())
		refuse()
		gomega.Expect(os.Remove(bindingPath)).To(gomega.Succeed())
		gomega.Expect(os.Symlink("absent", bindingPath)).To(gomega.Succeed())
		refuse() // Dangling links do not prove absence.
		gomega.Expect(os.Remove(bindingPath)).To(gomega.Succeed())
		artifact := filepath.Join(jobRoot, "artifacts/results/output.txt")
		gomega.Expect(os.WriteFile(artifact, []byte("modified"), 0600)).To(gomega.Succeed())
		refuse()
		gomega.Expect(os.Remove(artifact)).To(gomega.Succeed())
		refuse()
	})

	ginkgo.It("refuses unexplained missing logs even when both host leaves are absent", func() {
		_, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "mkdir -p ../results; printf retained > ../results/output.txt; printf log")
		gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		original, err := s.ledger.Result(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(original.EvidenceGaps).To(gomega.BeEmpty())
		for _, name := range []string{"output.log", "output-identity.json"} {
			gomega.Expect(os.Remove(filepath.Join(s.root.Path, "jobs", r.JobID, name))).To(gomega.Succeed())
		}
		c := artifactClient(s)
		_, _, err = c.Export(context.Background(), r.JobID, filepath.Join(ginkgo.GinkgoT().TempDir(), "unexplained"))
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(s.ledger.Result(r.JobID)).To(gomega.Equal(original))
	})
})
