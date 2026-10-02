package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Task011CLIValidation", ginkgo.Label("unit"), func() {
	ginkgo.It("rejects conflicting recovery modes and invalid kind-specific flags before connecting", func() {
		for _, kind := range []string{"job", "resource"} {
			for _, flags := range [][]string{
				{"--plan", "--plan-token", "token", "--json"},
				{"--plan", "--request-key", "saved-key", "--json"},
				{"--plan", "--evidence-file", "never-opened.json", "--json"},
				{"--plan-token", "token", "--json"},
				{"--plan-token", "token", "--request-key", "saved-key", "--action", "reconcile", "--json"},
				{"--plan", "--action", "invented-action", "--json"},
			} {
				var out, diagnostic bytes.Buffer
				args := append([]string{kind, "recover", "job-id"}, flags...)
				gomega.Expect(run(args, &out, &diagnostic)).To(gomega.Equal(2), "%v", args)
				gomega.Expect(out.Len()).To(gomega.BeZero())
				gomega.Expect(diagnostic.String()).NotTo(gomega.BeEmpty())
				gomega.Expect(diagnostic.String()).NotTo(gomega.ContainSubstring("connect"))
			}
		}
		for _, flags := range [][]string{
			{"--plan", "--action", "attest-stopped", "--json"},
			{"--plan-token", "token", "--request-key", "saved-key", "--evidence-file", "never-opened.json", "--json"},
		} {
			var out, diagnostic bytes.Buffer
			gomega.Expect(run(append([]string{"resource", "recover", "resource-id"}, flags...), &out, &diagnostic)).To(gomega.Equal(2))
			gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring("invalid recovery action, evidence flags or request key length"))
			gomega.Expect(out.Len()).To(gomega.BeZero())
		}
		for _, tc := range []struct {
			kind string
			size int
		}{{"job", 101}, {"resource", 91}} {
			var out, diagnostic bytes.Buffer
			gomega.Expect(run([]string{tc.kind, "recover", "id", "--plan-token", "token", "--request-key", strings.Repeat("k", tc.size), "--json"}, &out, &diagnostic)).To(gomega.Equal(2))
			gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring("request key length"))
			gomega.Expect(out.Len()).To(gomega.BeZero())
		}
	})
	ginkgo.It("rejects malformed or machine-labeled operator evidence before connecting", func() {
		parent := ginkgo.GinkgoT().TempDir()
		valid := `{"source":"operator-attested","observer":"fixture","observed_at":"2026-10-02T00:00:00Z","mechanism":"fixture-stop","digest":"sha256:` + strings.Repeat("a", 64) + `","reference":"fixture:stop"}`
		for _, raw := range []string{
			strings.Replace(valid, "operator-attested", "machine-observed", 1),
			strings.Replace(valid, "2026-10-02T00:00:00Z", "2026-10-02T09:00:00+09:00", 1),
			strings.Replace(valid, "2026-10-02T00:00:00Z", "2026-10-01T20:00:00-04:00", 1),
			strings.Replace(valid, "2026-10-02T00:00:00Z", "0001-01-01T00:00:00Z", 1),
			strings.Replace(valid, `"observer":"fixture"`, `"observer":"fixture","observer":"other"`, 1),
			strings.Replace(valid, `"reference":"fixture:stop"`, `"reference":"fixture:stop","unknown":true`, 1),
			strings.Replace(valid, `"mechanism":"fixture-stop"`, `"mechanism":""`, 1),
			valid + " {}",
		} {
			file := filepath.Join(parent, "evidence.json")
			gomega.Expect(os.WriteFile(file, []byte(raw), 0600)).To(gomega.Succeed())
			var out, diagnostic bytes.Buffer
			gomega.Expect(run([]string{"job", "recover", "job-id", "--plan-token", "token", "--request-key", "saved-key", "--evidence-file", file, "--json"}, &out, &diagnostic)).To(gomega.Equal(2))
			gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring("evidence"))
			gomega.Expect(diagnostic.String()).NotTo(gomega.ContainSubstring("connect"))
			gomega.Expect(out.Len()).To(gomega.BeZero())
		}
	})
	ginkgo.It("rejects service action flag conflicts before reading a plan or operating launchd", func() {
		for _, args := range [][]string{
			{"service"},
			{"service", "install", "--json"},
			{"service", "install", "--plan-file", "never-opened.json", "--instance", "foreign", "--json"},
			{"service", "uninstall", "--plan-file", "never-opened.json", "--root", "/private/tmp/foreign", "--json"},
			{"service", "plan", "--plan-file", "never-opened.json", "--json"},
			{"service", "install", "--plan-file", "never-opened.json"},
		} {
			var out, diagnostic bytes.Buffer
			gomega.Expect(run(args, &out, &diagnostic)).To(gomega.Equal(2), "%v", args)
			gomega.Expect(out.Len()).To(gomega.BeZero())
			gomega.Expect(diagnostic.String()).NotTo(gomega.ContainSubstring("launchctl"))
			gomega.Expect(diagnostic.String()).NotTo(gomega.ContainSubstring("never-opened"))
		}
	})
})
