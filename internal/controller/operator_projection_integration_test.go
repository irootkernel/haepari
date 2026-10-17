//go:build integration

package controller

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/irootkernel/haepari/internal/config"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func projectionRuntime(commit bool, adjust func(*config.Document)) (*httptest.Server, *runtimeServer, *sql.DB, string, config.Document, func()) {
	t := ginkgo.GinkgoT()
	parent := t.TempDir()
	root, err := state.Resolve(filepath.Join(parent, "state"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(root.Prepare()).To(gomega.Succeed())
	db, err := sqlite.Open(root.DBPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ledger, err := managed.NewLedger(db, 1)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	doc := config.Defaults()
	if adjust != nil {
		adjust(&doc)
	}
	if commit {
		_, err = ledger.CommitEffectiveConfig(doc, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
	store, err := snapshot.OpenStore(root.Path)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	runtime, err := newRuntime(root, ledger, store)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	server := httptest.NewServer(runtime.routes())
	return server, runtime, db, parent, doc, func() {
		server.Close()
		_ = db.Close()
	}
}

func projectionRequest(server *httptest.Server, method, path string, body []byte) (int, []byte) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequest(method, server.URL+path, reader)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	request.Header.Set("X-Haepari-Protocol", protocol)
	request.Header.Set("X-Haepari-Protocol-Min", "1")
	request.Header.Set("X-Haepari-Protocol-Max", "1")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return response.StatusCode, raw
}

func decodeProjection(raw []byte) (bool, managed.OperatorConfigView, responseError) {
	var body struct {
		OK    bool                       `json:"ok"`
		Data  managed.OperatorConfigView `json:"data"`
		Error responseError              `json:"error"`
	}
	gomega.Expect(json.Unmarshal(raw, &body)).To(gomega.Succeed())
	return body.OK, body.Data, body.Error
}

func hexObjectCount(root string) int {
	entries, err := os.ReadDir(filepath.Join(root, "objects"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	count := 0
	for _, entry := range entries {
		if entry.IsDir() && len(entry.Name()) == 64 {
			count++
		}
	}
	return count
}

func projectionJob(root, inventory string, files []string) managed.JobSpec {
	digest := strings.Repeat("a", 64)
	return managed.JobSpec{
		Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: "local-one", Mode: "auto", Suite: "build",
		Input:       managed.Input{Root: root, InventoryDigest: inventory, SelectedFiles: files},
		Environment: managed.Environment{ProfileID: "local", ProfileDigest: digest},
		Workload:    managed.Workload{Argv: []string{"/usr/bin/true"}, CWD: "source", RequiredOutputs: []string{"results/output.txt"}},
		Deadlines:   managed.Deadlines{QueueSeconds: 30, PrepareSeconds: 30, RunSeconds: 30, CollectSeconds: 30, CleanupSeconds: 30},
		Effects:     managed.Effects{Mode: "offline", AuthorizationReference: "isolated-upgrade-fixture"},
		Provenance:  managed.Provenance{CandidateDigest: digest, TestsDigest: digest, TargetProfileDigest: digest, EvidenceClass: "development"},
	}
}

func registerProjectionResource(runtime *runtimeServer) {
	resource := managed.ResourceSpec{Schema: "haepari.resource/v1", ID: "local-one", Kind: "local", ExecutionMode: "managed", Capacity: 1, Revision: 1, Adapter: managed.Adapter{QualificationReference: "isolated-integration"}, Cleanup: &managed.CleanupPolicy{PolicyReference: "owned-workspace"}}
	encoded, err := json.Marshal(resource)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(runtime.ledger.RegisterResource(encoded)).To(gomega.Succeed())
}

var _ = ginkgo.Describe("OperatorProjection", ginkgo.Label("integration"), func() {
	ginkgo.It("returns the accepted config view and refuses an unimplemented view", func() {
		server, runtime, _, parent, doc, cleanup := projectionRuntime(true, nil)
		defer cleanup()
		canonical, documentDigest, valuesDigest, err := doc.Digests()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		for _, path := range []string{"/v1/operator/projection?view=config", "/v1/operator/projection"} {
			status, raw := projectionRequest(server, http.MethodGet, path, nil)
			gomega.Expect(status).To(gomega.Equal(http.StatusOK))
			gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring(parent))
			ok, view, _ := decodeProjection(raw)
			gomega.Expect(ok).To(gomega.BeTrue())
			gomega.Expect(view.Schema).To(gomega.Equal("haepari.operator-projection/v1"))
			gomega.Expect(view.View).To(gomega.Equal("config"))
			gomega.Expect(view.InstallationID).To(gomega.Equal(runtime.installationID()))
			gomega.Expect(view.InstallationID).NotTo(gomega.BeEmpty())
			gomega.Expect(view.ObservedAt.IsZero()).To(gomega.BeFalse())
			gomega.Expect([]byte(view.Config)).To(gomega.Equal(canonical))
			gomega.Expect(view.DocumentDigest).To(gomega.Equal(documentDigest))
			gomega.Expect(view.ValuesDigest).To(gomega.Equal(valuesDigest))
		}
		edited := doc
		edited.DefaultDeadlines.QueueSeconds = 11
		rawEdited, err := json.Marshal(edited)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(os.WriteFile(filepath.Join(parent, "unaccepted.json"), rawEdited, 0600)).To(gomega.Succeed())
		_, raw := projectionRequest(server, http.MethodGet, "/v1/operator/projection?view=config", nil)
		_, view, _ := decodeProjection(raw)
		gomega.Expect([]byte(view.Config)).To(gomega.Equal(canonical))

		status, raw := projectionRequest(server, http.MethodGet, "/v1/operator/projection?view=nope", nil)
		gomega.Expect(status).NotTo(gomega.Equal(http.StatusOK))
		gomega.Expect(string(raw)).To(gomega.ContainSubstring("not implemented"))
		gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring("eligible"))
		ok, _, issue := decodeProjection(raw)
		gomega.Expect(ok).To(gomega.BeFalse())
		gomega.Expect(issue.Code).To(gomega.Equal("INVALID_REQUEST"))

		status, raw = projectionRequest(server, http.MethodGet, "/v1/operator/projection?view=diagnostic", nil)
		gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest))
		gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring("not implemented"))
		ok, _, issue = decodeProjection(raw)
		gomega.Expect(ok).To(gomega.BeFalse())
		gomega.Expect(issue.Code).To(gomega.Equal("INVALID_REQUEST"))
		gomega.Expect(issue.Retry).To(gomega.Equal("none"))
	})

	ginkgo.It("refuses a missing or corrupt accepted row without substituting defaults", func() {
		server, _, _, _, _, cleanup := projectionRuntime(false, nil)
		status, raw := projectionRequest(server, http.MethodGet, "/v1/operator/projection?view=config", nil)
		cleanup()
		gomega.Expect(status).To(gomega.Equal(http.StatusNotFound))
		ok, _, issue := decodeProjection(raw)
		gomega.Expect(ok).To(gomega.BeFalse())
		gomega.Expect(issue.Code).To(gomega.Equal("NOT_FOUND"))
		gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring("default_deadlines"))

		server, _, db, _, doc, cleanup := projectionRuntime(true, nil)
		defer cleanup()
		canonical, _, _, err := doc.Digests()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = db.Exec(`UPDATE managed_effective_config SET document_digest=? WHERE singleton=1`, strings.Repeat("b", 64))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		status, raw = projectionRequest(server, http.MethodGet, "/v1/operator/projection?view=config", nil)
		gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest))
		ok, _, issue = decodeProjection(raw)
		gomega.Expect(ok).To(gomega.BeFalse())
		gomega.Expect(issue.Code).To(gomega.Equal("INVALID_REQUEST"))
		var stored []byte
		gomega.Expect(db.QueryRow(`SELECT document_json FROM managed_effective_config WHERE singleton=1`).Scan(&stored)).To(gomega.Succeed())
		gomega.Expect(stored).To(gomega.Equal(canonical))

		corrupt := []byte(`{"schema":"haepari.config/v1"}`)
		_, err = db.Exec(`UPDATE managed_effective_config SET document_json=? WHERE singleton=1`, corrupt)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		status, raw = projectionRequest(server, http.MethodGet, "/v1/operator/projection?view=config", nil)
		gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest))
		_, _, issue = decodeProjection(raw)
		gomega.Expect(issue.Code).To(gomega.Equal("INVALID_REQUEST"))
		gomega.Expect(db.QueryRow(`SELECT document_json FROM managed_effective_config WHERE singleton=1`).Scan(&stored)).To(gomega.Succeed())
		gomega.Expect(stored).To(gomega.Equal(corrupt))
	})

	ginkgo.It("classifies only the job spec against request_bytes", func() {
		server, runtime, db, parent, _, cleanup := projectionRuntime(true, func(doc *config.Document) {
			doc.Limits.RequestBytes = 64
		})
		registerProjectionResource(runtime)
		source := filepath.Join(parent, "input")
		gomega.Expect(os.MkdirAll(filepath.Join(source, "source"), 0700)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(source, "source/input.txt"), []byte("small"), 0600)).To(gomega.Succeed())
		spec, err := json.Marshal(projectionJob(source, strings.Repeat("a", 64), []string{"source/input.txt"}))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(len(spec)).To(gomega.BeNumerically(">", 64))
		body, err := json.Marshal(map[string]any{"spec": json.RawMessage(spec), "request_key": "cap-key"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(hexObjectCount(runtime.root.Path)).To(gomega.BeZero())
		status, raw := projectionRequest(server, http.MethodPost, "/v1/jobs", body)
		gomega.Expect(status).NotTo(gomega.Equal(http.StatusOK))
		gomega.Expect(string(raw)).To(gomega.ContainSubstring("request_bytes"))
		var jobs int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_jobs`).Scan(&jobs)).To(gomega.Succeed())
		gomega.Expect(jobs).To(gomega.BeZero())
		gomega.Expect(hexObjectCount(runtime.root.Path)).To(gomega.BeZero())
		cleanup()

		server, runtime, db, parent, _, cleanup = projectionRuntime(true, nil)
		defer cleanup()
		registerProjectionResource(runtime)
		source = filepath.Join(parent, "large-input")
		gomega.Expect(os.MkdirAll(filepath.Join(source, "source"), 0700)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(source, "source/input.txt"), bytes.Repeat([]byte("x"), 1<<20+1), 0600)).To(gomega.Succeed())
		sealed, err := runtime.store.Seal(context.Background(), source, []string{"source/input.txt"}, snapshot.Provenance{EvidenceClass: "development"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		spec, err = json.Marshal(projectionJob(source, strings.TrimPrefix(sealed.Manifest.TreeSHA256, "sha256:"), []string{"source/input.txt"}))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		body, err = json.Marshal(map[string]any{"spec": json.RawMessage(spec), "request_key": "large-file-key"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		status, raw = projectionRequest(server, http.MethodPost, "/v1/jobs", body)
		gomega.Expect(status).To(gomega.Equal(http.StatusOK))
		gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring("request_bytes"))
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_jobs`).Scan(&jobs)).To(gomega.Succeed())
		gomega.Expect(jobs).To(gomega.Equal(1))
	})
})

func decodeDiagnostic(raw []byte) (bool, managed.OperatorDiagnostic, responseError) {
	var body struct {
		OK    bool                       `json:"ok"`
		Data  managed.OperatorDiagnostic `json:"data"`
		Error responseError              `json:"error"`
	}
	gomega.Expect(json.Unmarshal(raw, &body)).To(gomega.Succeed())
	return body.OK, body.Data, body.Error
}

var _ = ginkgo.Describe("OperatorDiagnostic", ginkgo.Label("integration"), func() {
	ginkgo.It("serves one strict diagnostic page and keeps a closed database at 503", func() {
		server, runtime, db, _, _, cleanup := projectionRuntime(true, nil)
		defer cleanup()
		registerProjectionResource(runtime)
		status, raw := projectionRequest(server, http.MethodGet, "/v1/operator/projection?view=diagnostic&section=queue", nil)
		gomega.Expect(status).To(gomega.Equal(http.StatusOK))
		ok, body, _ := decodeDiagnostic(raw)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(body.Schema).To(gomega.Equal("haepari.operator-diagnostic/v1"))
		gomega.Expect(body.View).To(gomega.Equal("diagnostic"))
		gomega.Expect(body.Section).To(gomega.Equal("queue"))
		gomega.Expect(body.Page.Limit).To(gomega.Equal(10))
		gomega.Expect(body.Resources).To(gomega.HaveLen(1))
		gomega.Expect(body.Resources[0].ResourceID).To(gomega.Equal("local-one"))
		gomega.Expect(body.Resources[0].IncludedJobs).NotTo(gomega.BeNil())
		gomega.Expect(*body.Resources[0].IncludedJobs).To(gomega.Equal(int64(0)))
		gomega.Expect(string(raw)).To(gomega.ContainSubstring(`"resources":[`))
		gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring(`"jobs"`))
		gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring("eligible"))

		for _, path := range []string{
			"/v1/operator/projection?view=diagnostic&section=queue&section=retention",
			"/v1/operator/projection?view=diagnostic&section=queue&extra=1",
			"/v1/operator/projection?view=diagnostic&section=queue&limit=",
			"/v1/operator/projection?view=diagnostic&section=queue&limit=01",
			"/v1/operator/projection?view=diagnostic&section=queue&limit=21",
			"/v1/operator/projection?view=diagnostic&section=queue&resource=",
			"/v1/operator/projection?view=diagnostic&section=queue&resource=%ZZ",
			"/v1/operator/projection?view=diagnostic&section=queue&resource=a;b",
			"/v1/operator/projection?view=diagnostic",
		} {
			status, raw = projectionRequest(server, http.MethodGet, path, nil)
			gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest), path)
			ok, _, issue := decodeDiagnostic(raw)
			gomega.Expect(ok).To(gomega.BeFalse())
			gomega.Expect(issue.Code).To(gomega.Equal("INVALID_REQUEST"))
			gomega.Expect(issue.Retry).To(gomega.Equal("none"))
		}
		var jobs int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_jobs`).Scan(&jobs)).To(gomega.Succeed())
		gomega.Expect(jobs).To(gomega.Equal(0))
		var resources int
		gomega.Expect(db.QueryRow(`SELECT COUNT(*) FROM managed_resources`).Scan(&resources)).To(gomega.Succeed())
		gomega.Expect(resources).To(gomega.Equal(1))

		status, raw = projectionRequest(server, http.MethodGet, "/v1/operator/projection?view=diagnostic&section=queue&resource=missing-resource", nil)
		gomega.Expect(status).To(gomega.Equal(http.StatusNotFound))
		_, _, issue := decodeDiagnostic(raw)
		gomega.Expect(issue.Code).To(gomega.Equal("NOT_FOUND"))

		gomega.Expect(db.Close()).To(gomega.Succeed())
		status, raw = projectionRequest(server, http.MethodGet, "/v1/operator/projection?view=diagnostic&section=queue", nil)
		gomega.Expect(status).To(gomega.Equal(http.StatusServiceUnavailable))
		_, _, issue = decodeDiagnostic(raw)
		gomega.Expect(issue.Code).To(gomega.Equal("STORAGE_UNAVAILABLE"))
		gomega.Expect(issue.Retry).To(gomega.Equal("reconcile"))
	})

	ginkgo.It("keeps an absent or corrupt header at HTTP 200", func() {
		server, _, _, _, _, cleanup := projectionRuntime(false, nil)
		status, raw := projectionRequest(server, http.MethodGet, "/v1/operator/projection?view=diagnostic&section=retention", nil)
		cleanup()
		gomega.Expect(status).To(gomega.Equal(http.StatusOK))
		ok, body, _ := decodeDiagnostic(raw)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(body.CurrentConfig.Status).To(gomega.Equal("unknown"))
		gomega.Expect(body.CurrentConfig.Reason).To(gomega.Equal("absent"))
		gomega.Expect(body.Jobs).NotTo(gomega.BeNil())
		gomega.Expect(body.Jobs).To(gomega.BeEmpty())
		gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring("default_deadlines"))

		server, _, db, _, _, cleanup := projectionRuntime(true, nil)
		defer cleanup()
		_, err := db.Exec(`UPDATE managed_effective_config SET document_digest=? WHERE singleton=1`, strings.Repeat("b", 64))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		status, raw = projectionRequest(server, http.MethodGet, "/v1/operator/projection?view=config", nil)
		gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest))
		status, raw = projectionRequest(server, http.MethodGet, "/v1/operator/projection?view=diagnostic&section=queue", nil)
		gomega.Expect(status).To(gomega.Equal(http.StatusOK))
		_, body, _ = decodeDiagnostic(raw)
		gomega.Expect(body.CurrentConfig.Status).To(gomega.Equal("unknown"))
		gomega.Expect(body.CurrentConfig.Reason).To(gomega.Equal("corrupt"))
	})

	ginkgo.It("rejects a semicolon diagnostic selector before the legacy view parse", func() {
		server, _, _, _, _, cleanup := projectionRuntime(true, nil)
		defer cleanup()
		for _, path := range []string{
			"/v1/operator/projection?view=diagnostic;section=queue",
			"/v1/operator/projection?section=queue;view=diagnostic",
			"/v1/operator/projection?%76iew=diagnostic;section=queue",
			"/v1/operator/projection?view=%64iagnostic;section=queue",
		} {
			status, raw := projectionRequest(server, http.MethodGet, path, nil)
			gomega.Expect(status).To(gomega.Equal(http.StatusBadRequest), path)
			ok, _, issue := decodeDiagnostic(raw)
			gomega.Expect(ok).To(gomega.BeFalse(), path)
			gomega.Expect(issue.Code).To(gomega.Equal("INVALID_REQUEST"), path)
			gomega.Expect(issue.Retry).To(gomega.Equal("none"), path)
			gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring("not implemented"), path)
		}
		status, raw := projectionRequest(server, http.MethodGet, "/v1/operator/projection?view=%64iagnostic&section=queue", nil)
		gomega.Expect(status).To(gomega.Equal(http.StatusOK))
		ok, body, _ := decodeDiagnostic(raw)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(body.View).To(gomega.Equal("diagnostic"))
		gomega.Expect(body.Schema).To(gomega.Equal("haepari.operator-diagnostic/v1"))
		status, raw = projectionRequest(server, http.MethodGet, "/v1/operator/projection?%76iew=diagnostic&section=queue", nil)
		gomega.Expect(status).To(gomega.Equal(http.StatusOK))
		ok, body, _ = decodeDiagnostic(raw)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(body.Schema).To(gomega.Equal("haepari.operator-diagnostic/v1"))
		for _, path := range []string{
			"/v1/operator/projection?preview=diagnostic",
			"/v1/operator/projection?q=view=diagnostic",
			"/v1/operator/projection",
			"/v1/operator/projection?view=config",
			"/v1/operator/projection?view=config&view=diagnostic",
			"/v1/operator/projection?view=config&view=diagnostic;section=queue",
			"/v1/operator/projection?view=&view=diagnostic;section=queue",
			"/v1/operator/projection?view&view=diagnostic;section=queue",
			"/v1/operator/projection?%76iew&view=diagnostic;section=queue",
			"/v1/operator/projection?view=config&resource=missing-resource;limit=0",
			"/v1/operator/projection?view=config;section=queue",
			"/v1/operator/projection?section=queue",
		} {
			status, raw = projectionRequest(server, http.MethodGet, path, nil)
			gomega.Expect(status).To(gomega.Equal(http.StatusOK), path)
			ok, view, _ := decodeProjection(raw)
			gomega.Expect(ok).To(gomega.BeTrue(), path)
			gomega.Expect(view.Schema).To(gomega.Equal("haepari.operator-projection/v1"), path)
			gomega.Expect(view.View).To(gomega.Equal("config"), path)
			gomega.Expect(string(raw)).NotTo(gomega.ContainSubstring("haepari.operator-diagnostic/v1"), path)
		}
	})
})
