package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

type corruptArtifactTransport struct {
	manifest managed.ArtifactManifest
	bytes    []byte
}

func (t corruptArtifactTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var data any = t.manifest
	if r.URL.Query().Get("path") != "" {
		data = managed.ArtifactPage{Schema: "haepari.artifact-page/v1", JobID: t.manifest.JobID, File: t.manifest.Files[0].ResultArtifact, Data: t.bytes, NextOffset: int64(len(t.bytes))}
	}
	raw, err := json.Marshal(Envelope{Schema: "haepari.envelope/v1", Operation: "artifacts", OK: true, Data: mustJSON(ginkgo.GinkgoT(), data)})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw))}, nil
}

var _ = ginkgo.Describe("Task012ExportChecksum", ginkgo.Label("unit"), func() {
	ginkgo.It("refuses matching page metadata with same-size corrupt bytes and retains only partial export", func() {
		digest := sha256.Sum256([]byte("good"))
		manifest := managed.ArtifactManifest{Schema: "haepari.artifact-manifest/v1", JobID: "job-1", Result: managed.Result{Schema: "haepari.result/v1", JobID: "job-1"}, Files: []managed.DeliveryFile{{ResultArtifact: managed.ResultArtifact{Path: "artifacts/result", SizeBytes: 4, SHA256: hex.EncodeToString(digest[:])}, PageToken: "exact-file"}}}
		c := &Client{http: &http.Client{Transport: corruptArtifactTransport{manifest: manifest, bytes: []byte("evil")}}}
		destination := filepath.Join(ginkgo.GinkgoT().TempDir(), "export")
		_, _, err := c.Export(context.Background(), manifest.JobID, destination)
		gomega.Expect(err).To(gomega.MatchError("export checksum mismatch for artifacts/result"))
		gomega.Expect(os.ReadFile(filepath.Join(destination, "artifacts/result"))).To(gomega.Equal([]byte("evil")))
		_, err = os.Stat(filepath.Join(destination, "manifest.json"))
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
		gomega.Expect(os.Stat(filepath.Join(destination, "result.json"))).Error().NotTo(gomega.HaveOccurred())
	})
})
