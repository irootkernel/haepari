//go:build integration

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func artifactClient(s *runtimeServer) *client.Client {
	t := ginkgo.GinkgoT()
	listener, err := net.Listen("unix", s.root.SocketPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	server := httptest.NewUnstartedServer(s.routes())
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	c, err := client.Connect(context.Background(), s.root)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	t.Cleanup(c.Close)
	return c
}

var _ = ginkgo.Describe("Task012ArtifactDelivery", ginkgo.Label("integration"), func() {
	ginkgo.It("publishes immutable owned receipts and refuses aliases and conflicting replay", func() {
		root, err := filepath.EvalSymlinks(ginkgo.GinkgoT().TempDir())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		first := map[string]string{"owner": "original"}
		gomega.Expect(writeReceipt(root, "identity.json", first)).To(gomega.Succeed())
		gomega.Expect(writeReceipt(root, "identity.json", first)).To(gomega.Succeed())
		gomega.Expect(writeReceipt(root, "identity.json", map[string]string{"owner": "foreign"})).To(gomega.MatchError(managed.ErrConflict))
		raw, err := os.ReadFile(filepath.Join(root, "identity.json"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(raw)).To(gomega.Equal(`{"owner":"original"}`))
		gomega.Expect(os.Symlink(root, root+"-alias")).To(gomega.Succeed())
		defer os.Remove(root + "-alias")
		gomega.Expect(writeReceipt(root+"-alias", "other.json", first)).To(gomega.HaveOccurred())
		_, err = os.Stat(filepath.Join(root, "other.json"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})
	ginkgo.It("streams exact offsets, exports verified bytes and refuses an existing destination", func() {
		_, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "mkdir -p ../results; /usr/bin/head -c 150123 /dev/zero > ../results/output.txt; printf observed")
		gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		c := artifactClient(s)
		m, _, err := c.Artifacts(context.Background(), r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(m.Files).To(gomega.HaveLen(2))
		page, _, err := c.ArtifactBytes(context.Background(), r.JobID, m.Files[0], 65536)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(page.Offset).To(gomega.Equal(int64(65536)))
		gomega.Expect(page.NextOffset).To(gomega.Equal(int64(131072)))
		dest := filepath.Join(ginkgo.GinkgoT().TempDir(), "export")
		_, _, err = c.Export(context.Background(), r.JobID, dest)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		data, err := os.ReadFile(filepath.Join(dest, "artifacts/results/output.txt"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(data).To(gomega.HaveLen(150123))
		data, err = os.ReadFile(filepath.Join(dest, "output.log"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(data)).To(gomega.Equal("observed"))
		_, _, err = c.Export(context.Background(), r.JobID, dest)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(os.Stat(filepath.Join(dest, "manifest.json"))).Error().NotTo(gomega.HaveOccurred())
		corrupt := bytes.Repeat([]byte{1}, 150123)
		artifact := filepath.Join(s.root.Path, "jobs", r.JobID, "artifacts/results/output.txt")
		gomega.Expect(os.WriteFile(artifact, corrupt, 0600)).To(gomega.Succeed())
		_, _, err = c.ArtifactBytes(context.Background(), r.JobID, m.Files[0], 0)
		gomega.Expect(err).To(gomega.HaveOccurred())
		_, _, err = c.Artifacts(context.Background(), r.JobID)
		gomega.Expect(err).To(gomega.HaveOccurred())
		failed := filepath.Join(ginkgo.GinkgoT().TempDir(), "corrupt-export")
		_, _, err = c.Export(context.Background(), r.JobID, failed)
		gomega.Expect(err).To(gomega.HaveOccurred())
		_, err = os.Stat(filepath.Join(failed, "manifest.json"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		gomega.Expect(os.ReadFile(artifact)).To(gomega.Equal(corrupt))
	})
	ginkgo.It("expires a purge waiting for the evidence fence without recording deletion authority", func() {
		_, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "mkdir -p ../results; printf done > ../results/output.txt")
		gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		finished, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		s.evidenceMu.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := s.purgeJobContext(ctx, r.JobID, finished.JobRevision); done <- err }()
		// Release the fence even if a regression makes the deadline ineffective.
		defer s.evidenceMu.Unlock()
		gomega.Eventually(done, time.Second).Should(gomega.Receive(gomega.MatchError(context.DeadlineExceeded)))
		_, found, _, err := s.ledger.PurgeIntent(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(found).To(gomega.BeFalse())
		gomega.Expect(os.Stat(filepath.Join(s.root.Path, "jobs", r.JobID, "output.log"))).Error().NotTo(gomega.HaveOccurred())
	})
	ginkgo.It("purges actual bulk bytes, preserves permanent admission/cancel replay and refuses a stale revision", func() {
		db, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "mkdir -p ../results; printf done > ../results/output.txt")
		gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		finished, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		cancelled, err := s.requestCancel(managed.CancelRequest{JobID: r.JobID, ExpectedRevision: finished.JobRevision, MutationKey: "terminal-cancel"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = s.purgeJob(r.JobID, finished.JobRevision-1)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrStale))
		p, err := s.purgeJob(r.JobID, finished.JobRevision)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(p.Complete).To(gomega.BeTrue())
		_, err = os.Stat(filepath.Join(s.root.Path, "jobs", r.JobID, "output.log"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		_, err = s.store.Read(object)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		_, err = s.ledger.Result(r.JobID)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrResultUnavailable))
		c := artifactClient(s)
		raw, _ := json.Marshal(job)
		prior, _, err := c.Submit(context.Background(), raw, "cancel-key")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(prior.JobID).To(gomega.Equal(r.JobID))
		gomega.Expect(prior.ResultStatus).To(gomega.Equal("unavailable"))
		replayed, err := s.requestCancel(managed.CancelRequest{JobID: r.JobID, ExpectedRevision: finished.JobRevision, MutationKey: "terminal-cancel"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(replayed.Mutation).To(gomega.Equal(cancelled.Mutation))
		var count int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_runs`).Scan(&count)).To(gomega.Succeed())
		gomega.Expect(count).To(gomega.Equal(1))
		_, err = s.purgeJob(r.JobID, finished.JobRevision)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// An idle recovery plan remains usable without exposing the purged
		// original result through another public observer.
		gomega.Expect(db.Exec(`UPDATE managed_resources SET availability='quarantined',availability_reason='fixture',availability_revision=availability_revision+1 WHERE id=?`, r.ResourceID)).Error().NotTo(gomega.HaveOccurred())
		plan, err := s.planResourceRecovery(r.ResourceID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(plan.PlanToken).NotTo(gomega.BeEmpty())
		gomega.Expect(plan.Snapshot.Jobs).To(gomega.HaveLen(1))
		gomega.Expect(plan.Snapshot.Jobs[0].Result).To(gomega.BeNil())
		gomega.Expect(plan.Snapshot.Jobs[0].ResultStatus).To(gomega.Equal("unavailable"))
		gomega.Expect(plan.Snapshot.Jobs[0].EvidenceResolved).To(gomega.BeTrue())
		encoded, err := json.Marshal(plan)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(encoded)).NotTo(gomega.ContainSubstring(`"final_verdict"`))
		// Unsupported backend inspection also preserves the unavailable projection.
		s.backendEvidence = func(string, string) (evidenceBackend, error) { return nil, managed.ErrBlocked }
		inspection, err := s.planResourceRecovery(r.ResourceID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(inspection.Snapshot.Jobs[0].Result).To(gomega.BeNil())
	})
	ginkgo.It("retains a shared sealed object and refuses unknown artifact leaves before deletion", func() {
		_, s, r, job, object := cancellationRuntime(ginkgo.GinkgoT(), "mkdir -p ../results; printf done > ../results/output.txt")
		gomega.Expect(s.executeLocal(r, job, object, time.Now().Add(5*time.Second))).To(gomega.Succeed())
		sealed, err := s.store.Read(object)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		inventory, _ := json.Marshal(sealed.Manifest)
		spec, _ := json.Marshal(job)
		_, err = s.ledger.AdmitSealedInventory(spec, job.Input.InventoryDigest, object, inventory, "shared-key", "shared-job")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		finished, err := s.ledger.Snapshot(r.JobID)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		foreign := filepath.Join(s.root.Path, "jobs", r.JobID, "artifacts/foreign")
		gomega.Expect(os.WriteFile(foreign, []byte("foreign"), 0600)).To(gomega.Succeed())
		_, err = s.purgeJob(r.JobID, finished.JobRevision)
		gomega.Expect(err).To(gomega.MatchError(managed.ErrBlocked))
		gomega.Expect(os.Stat(foreign)).Error().NotTo(gomega.HaveOccurred())
		gomega.Expect(os.Remove(foreign)).To(gomega.Succeed())
		_, err = s.purgeJob(r.JobID, finished.JobRevision)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = s.store.Read(object)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})
	ginkgo.It("records incomplete collection at count and byte bounds while preserving source bytes", func() {
		root := ginkgo.GinkgoT().TempDir()
		source := filepath.Join(root, "workspace")
		gomega.Expect(os.Mkdir(source, 0700)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(source, "a"), []byte("12345"), 0600)).To(gomega.Succeed())
		a, complete, _ := collectOutputsBounded(source, filepath.Join(root, "artifacts"), []string{"a"}, time.Now().Add(time.Second), 4, 1)
		gomega.Expect(a).To(gomega.BeEmpty())
		gomega.Expect(complete).To(gomega.BeFalse())
		gomega.Expect(os.Stat(filepath.Join(source, "a"))).Error().NotTo(gomega.HaveOccurred())
		_, complete, _ = collectOutputsBounded(source, filepath.Join(root, "count"), []string{"a", "b"}, time.Now().Add(time.Second), 100, 1)
		gomega.Expect(complete).To(gomega.BeFalse())
		_, complete, _ = collectOutputs(source, filepath.Join(root, "missing"), []string{"absent"}, time.Now().Add(time.Second))
		gomega.Expect(complete).To(gomega.BeFalse())
		partial := filepath.Join(root, "disk-full")
		a, complete, _ = collectOutputsWithCopy(source, partial, []string{"a"}, time.Now().Add(time.Second), 100, 1, func(w io.Writer, r io.Reader, d time.Time) (int64, error) {
			n, err := copyUntil(w, io.LimitReader(r, 2), d)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			return n, syscall.ENOSPC
		})
		gomega.Expect(a).To(gomega.BeEmpty())
		gomega.Expect(complete).To(gomega.BeFalse())
		gomega.Expect(os.IsNotExist(func() error { _, err := os.Stat(filepath.Join(partial, "a")); return err }())).To(gomega.BeTrue())
		bytes, err := os.ReadFile(filepath.Join(source, "a"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(string(bytes)).To(gomega.Equal("12345"))
	})
})
