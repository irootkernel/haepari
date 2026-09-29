package main

import (
	"bytes"
	"encoding/json"
	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("VersionIdentity", ginkgo.Label("unit"), func() {
	ginkgo.It("VersionIdentity", func() {
		t := ginkgo.GinkgoT()
		var output, errors bytes.Buffer
		if code := run([]string{"version", "--json"}, &output, &errors); code != 0 {
			t.Fatalf("version exit %d: %s", code, errors.String())
		}
		var result struct {
			Schema    string `json:"schema"`
			Operation string `json:"operation"`
			OK        bool   `json:"ok"`
			Data      struct {
				Version          string `json:"version"`
				ExecutableSHA256 string `json:"executable_sha256"`
			} `json:"data"`
		}
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if result.Schema != "haepari.envelope/v1" || result.Operation != "version" || !result.OK || result.Data.Version == "" || len(result.Data.ExecutableSHA256) != 71 {
			t.Fatalf("incomplete identity: %+v", result)
		}

	})
})

var _ = ginkgo.Describe("Capabilities", ginkgo.Label("unit"), func() {
	ginkgo.It("Capabilities", func() {
		t := ginkgo.GinkgoT()
		var output, errors bytes.Buffer
		if code := run([]string{"capabilities", "--json"}, &output, &errors); code != 0 {
			t.Fatalf("capabilities exit %d: %s", code, errors.String())
		}
		var result struct {
			Schema    string `json:"schema"`
			Operation string `json:"operation"`
			OK        bool   `json:"ok"`
			Data      struct {
				CompiledBackends  []string `json:"compiled_backends"`
				QualifiedBackends []string `json:"qualified_backends"`
				Protocols         []string `json:"protocol_versions"`
			} `json:"data"`
		}
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if result.Schema != "haepari.envelope/v1" || result.Operation != "capabilities" || !result.OK || len(result.Data.CompiledBackends) != 1 || result.Data.CompiledBackends[0] != "local" || len(result.Data.QualifiedBackends) != 0 || len(result.Data.Protocols) != 1 || result.Data.Protocols[0] != "managed-execution/v1" {
			t.Fatalf("unexpected capabilities: %+v", result)
		}

	})
})

var _ = ginkgo.Describe("UsageRejectsUnsupportedArguments", ginkgo.Label("unit"), func() {
	ginkgo.It("UsageRejectsUnsupportedArguments", func() {
		t := ginkgo.GinkgoT()
		for _, args := range [][]string{
			{}, {"version"}, {"version", "--json", "extra"},
			{"capabilities"}, {"daemon", "run", "extra"},
			{"daemon", "run", "--backend"}, {"daemon", "run", "--nope"},
			{"runner"}, {"unknown"},
			{"resource", "register"}, {"resource", "register", "--file", "resource.json"},
			{"submit"}, {"submit", "--spec", "job.json", "--json"},
			{"run"}, {"run", "--spec", "job.json"},
			{"wait", "job"}, {"wait", "job", "--json", "--timeout", "0s"},
			{"wait", "job", "--json", "--timeout", "25h"},
		} {
			var output, errors bytes.Buffer
			if code := run(args, &output, &errors); code != 2 {
				t.Errorf("%q: exit %d: %s", args, code, errors.String())
			}
			if errors.Len() == 0 {
				t.Errorf("%q: rejected without a diagnostic", args)
			}
		}

	})
})

var _ = ginkgo.Describe("CLIContractErrorAndVerdictExits", ginkgo.Label("unit"), func() {
	ginkgo.It("CLIContractErrorAndVerdictExits", func() {
		t := ginkgo.GinkgoT()
		for _, tc := range []struct {
			code string
			want int
		}{
			{"CONFLICT", 2}, {"UNSUPPORTED", 2}, {"WAIT_TIMEOUT", 5},
			{"STORAGE_UNAVAILABLE", 6}, {"TRANSPORT_UNCERTAIN", 6},
		} {
			var out, diagnostic bytes.Buffer
			issue := &client.Issue{Code: tc.code, Message: "bounded explanation", Retry: "none"}
			envelope := client.Envelope{Schema: "haepari.envelope/v1", Operation: "submit", Error: issue}
			if got := clientFailure(&out, &diagnostic, envelope, issue); got != tc.want {
				t.Errorf("%s exit = %d, want %d", tc.code, got, tc.want)
			}
			if out.Len() == 0 {
				t.Errorf("%s error envelope missing", tc.code)
			}
		}
		for _, tc := range []struct {
			verdict managed.Verdict
			want    int
		}{
			{managed.Pass, 0}, {managed.Fail, 1}, {managed.Incomplete, 3}, {managed.Cancelled, 4},
		} {
			if got := verdictExit(tc.verdict); got != tc.want {
				t.Errorf("%s exit = %d, want %d", tc.verdict, got, tc.want)
			}
		}

	})
})
