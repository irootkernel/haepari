package main

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("SelectedEvidenceRecoveryCLI", ginkgo.Label("unit"), func() {
	ginkgo.It("forwards a selected resource recollect plan through the Unix client", func() {
		var got managed.RecoveryRequest
		_, cleanup := diagnosticCLI(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/hello" {
				diagnosticHello(w)
				return
			}
			gomega.Expect(r.URL.Path).To(gomega.Equal("/v1/resources/resource-id/recover"))
			gomega.Expect(json.NewDecoder(r.Body).Decode(&got)).To(gomega.Succeed())
			_ = json.NewEncoder(w).Encode(map[string]any{"schema": "haepari.envelope/v1", "operation": "resource.recover", "ok": true, "data": map[string]any{"plan_token": "fixture-token"}})
		})
		defer cleanup()
		var out, diagnostic bytes.Buffer
		gomega.Expect(run([]string{"resource", "recover", "resource-id", "--plan", "--action", "recollect", "--job-id", "target-job", "--json"}, &out, &diagnostic)).To(gomega.Equal(0), diagnostic.String())
		gomega.Expect(got).To(gomega.Equal(managed.RecoveryRequest{Plan: true, Action: "recollect", JobID: "target-job"}))
	})
	ginkgo.It("rejects selections outside resource recollect planning before connecting", func() {
		for _, args := range [][]string{
			{"resource", "recover", "r", "--plan", "--action", "dispose", "--job-id", "target", "--json"},
			{"resource", "recover", "r", "--plan", "--job-id", "target", "--json"},
			{"resource", "recover", "r", "--plan", "--action", "attest-guest-stopped", "--job-id", "target", "--json"},
			{"resource", "recover", "r", "--plan-token", "token", "--request-key", "key", "--job-id", "target", "--json"},
			{"resource", "recover", "r", "--plan", "--action", "recollect", "--job-id", "../bad", "--json"},
			{"job", "recover", "j", "--plan", "--job-id", "target", "--json"},
		} {
			var out, diagnostic bytes.Buffer
			gomega.Expect(run(args, &out, &diagnostic)).To(gomega.Equal(2))
			gomega.Expect(out.Len()).To(gomega.BeZero())
			gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring("invalid recovery"))
		}
	})
})
