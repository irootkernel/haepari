package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/state"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func diagnosticCLI(handler http.HandlerFunc) (root string, cleanup func()) {
	t := ginkgo.GinkgoT()
	parent := t.TempDir()
	resolved, err := state.Resolve(filepath.Join(parent, "state"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(resolved.Prepare()).To(gomega.Succeed())
	listener, err := net.Listen("unix", resolved.SocketPath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	server := &http.Server{Handler: handler}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Setenv("HAEPARI_HOME", resolved.Path)
	return resolved.Path, func() {
		_ = server.Close()
		<-done
	}
}

func diagnosticHello(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"schema": "haepari.envelope/v1", "operation": "hello", "ok": true,
		"data": client.Handshake{Protocol: "managed-execution/v1", Version: 1, InstallationID: "fixture", Capabilities: []string{"managed-execution/v1"}},
	})
}

var _ = ginkgo.Describe("OperatorDiagnostic", ginkgo.Label("unit"), func() {
	ginkgo.It("keeps the legacy doctor flags and exits 0 for warning and past", func() {
		_, cleanup := diagnosticCLI(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/hello" {
				diagnosticHello(w)
				return
			}
			gomega.Expect(r.URL.Query().Get("view")).To(gomega.Equal("diagnostic"))
			gomega.Expect(r.URL.Query().Get("section")).To(gomega.Equal("retention"))
			gomega.Expect(r.URL.Query().Get("limit")).To(gomega.Equal(""))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schema": "haepari.envelope/v1", "operation": "operator.projection", "ok": true,
				"data": map[string]any{
					"schema": "haepari.operator-diagnostic/v1", "view": "diagnostic", "section": "retention",
					"installation_id": "fixture", "observed_at": "2026-10-17T00:00:00Z",
					"any_unknown_occupancy": false,
					"current_config":        map[string]any{"status": "unknown", "reason": "absent"},
					"page":                  map[string]any{"limit": 10, "more": false},
					"jobs": []any{map[string]any{
						"job_id": "job-1", "resource_id": "local-a", "state": "finished", "mode": "auto",
						"policy_class": "accepted", "retention_class": "clear",
						"event_window": "warning", "result_window": "past", "permanent_replay_count": 0,
					}},
				},
			})
		})
		defer cleanup()
		var output, diagnostic bytes.Buffer
		gomega.Expect(run([]string{"doctor", "--section", "queue", "--json"}, &output, &diagnostic)).To(gomega.Equal(2))
		gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring("usage: haepari doctor --resource <id> --json"))
		gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring("usage: haepari doctor --projection"))
		output.Reset()
		diagnostic.Reset()
		gomega.Expect(run([]string{"doctor", "--projection", "--section", "retention", "--json"}, &output, &diagnostic)).To(gomega.Equal(0))
		gomega.Expect(output.String()).To(gomega.ContainSubstring("warning"))
		gomega.Expect(output.String()).To(gomega.ContainSubstring("past"))
		gomega.Expect(diagnostic.String()).To(gomega.BeEmpty())

		for _, args := range [][]string{
			{"doctor", "--projection", "--section", "queue"},
			{"doctor", "--projection", "--section", "both", "--json"},
			{"doctor", "--projection", "--section", "queue", "--limit", "", "--json"},
			{"doctor", "--projection", "--section", "queue", "--limit", "01", "--json"},
			{"doctor", "--projection", "--section", "queue", "--limit", "21", "--json"},
			{"doctor", "--projection", "--section", "queue", "--limit", "-1", "--json"},
			{"doctor", "--projection", "--section", "queue", "--cursor", "", "--json"},
		} {
			output.Reset()
			diagnostic.Reset()
			gomega.Expect(run(args, &output, &diagnostic)).To(gomega.Equal(2), args)
		}
	})

	ginkgo.It("maps diagnostic envelope failures through clientFailure", func() {
		codes := map[string]int{"WAIT_TIMEOUT": 5, "STORAGE_UNAVAILABLE": 6, "NOT_FOUND": 2}
		for code, exit := range codes {
			_, cleanup := diagnosticCLI(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/hello" {
					diagnosticHello(w)
					return
				}
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"schema": "haepari.envelope/v1", "operation": "operator.projection", "ok": false,
					"error": client.Issue{Code: code, Message: "fixture " + code, Retry: "none"},
				})
			})
			var output, diagnostic bytes.Buffer
			gomega.Expect(run([]string{"doctor", "--projection", "--section", "queue", "--json"}, &output, &diagnostic)).To(gomega.Equal(exit), code)
			gomega.Expect(output.String()).To(gomega.ContainSubstring(code))
			cleanup()
		}
		parent := ginkgo.GinkgoT().TempDir()
		root, err := state.Resolve(filepath.Join(parent, "state"))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(root.Prepare()).To(gomega.Succeed())
		ginkgo.GinkgoT().Setenv("HAEPARI_HOME", root.Path)
		var output, diagnostic bytes.Buffer
		gomega.Expect(run([]string{"doctor", "--projection", "--section", "queue", "--json"}, &output, &diagnostic)).To(gomega.Equal(2))
		gomega.Expect(output.String()).To(gomega.ContainSubstring("PREREQUISITE_UNAVAILABLE"))
	})
})
