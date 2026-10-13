package main

import (
	"bytes"
	"encoding/json"
	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"os"
	"path/filepath"
	"strings"
)

var _ = ginkgo.Describe("Task017DoctorCLI", ginkgo.Label("unit"), func() {
	ginkgo.It("validates usage and exposes missing-controller failure without creating state", func() {
		t := ginkgo.GinkgoT()
		parent, err := os.MkdirTemp("/tmp", "hp-doctor-")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer os.RemoveAll(parent)
		root := filepath.Join(parent, "state")
		t.Setenv("HAEPARI_HOME", root)
		for _, args := range [][]string{
			{"doctor"}, {"doctor", "--resource", "local-a"},
			{"doctor", "--resource", "../unsafe", "--json"},
			{"doctor", "--resource", "local-a", "--json", "extra"},
		} {
			var out, diag bytes.Buffer
			gomega.Expect(run(args, &out, &diag)).To(gomega.Equal(2))
			gomega.Expect(diag.String()).To(gomega.ContainSubstring("usage: haepari doctor"))
		}
		var out, diag bytes.Buffer
		gomega.Expect(run([]string{"doctor", "--resource", "local-a", "--json"}, &out, &diag)).To(gomega.Equal(2))
		gomega.Expect(diag.String()).To(gomega.BeEmpty())
		var response client.Envelope
		gomega.Expect(json.Unmarshal(out.Bytes(), &response)).To(gomega.Succeed())
		gomega.Expect(response.Schema).To(gomega.Equal("haepari.envelope/v1"))
		gomega.Expect(response.Operation).To(gomega.Equal("doctor"))
		gomega.Expect(response.OK).To(gomega.BeFalse())
		gomega.Expect(response.Error.Code).To(gomega.Equal("PREREQUISITE_UNAVAILABLE"))
		gomega.Expect(response.Error.Message).To(gomega.ContainSubstring("HAEPARI_HOME"))
		_, err = os.Stat(root)
		gomega.Expect(os.IsNotExist(err)).To(gomega.BeTrue())
	})

	ginkgo.It("advertises the implemented doctor command and diagnostic schema", func() {
		var out, diag bytes.Buffer
		gomega.Expect(run([]string{"help", "--json"}, &out, &diag)).To(gomega.Equal(0))
		var response client.Envelope
		gomega.Expect(json.Unmarshal(out.Bytes(), &response)).To(gomega.Succeed())
		var help struct {
			Commands []commandHelp `json:"commands"`
			Schemas  []string      `json:"schemas"`
		}
		gomega.Expect(json.Unmarshal(response.Data, &help)).To(gomega.Succeed())
		gomega.Expect(help.Commands).To(gomega.ContainElement(commandHelp{"doctor", "doctor --resource <id> --json"}))
		gomega.Expect(help.Commands).To(gomega.ContainElement(commandHelp{"resource remove", "resource remove <lock-id> --expected-revision <n> --json (idle advisory locks only; local and VM removal unsupported)"}))
		gomega.Expect(help.Schemas).To(gomega.ContainElement("haepari.doctor/v1"))
		gomega.Expect(help.Commands).To(gomega.ContainElement(commandHelp{"manual complete", "manual complete <job-id> --input <json> --expected-revision <n> --json"}))
		gomega.Expect(help.Schemas).To(gomega.ContainElement("haepari.manual-result/v1"))
	})
})

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
				Capabilities      []string `json:"capabilities"`
			} `json:"data"`
		}
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		if result.Schema != "haepari.envelope/v1" || result.Operation != "capabilities" || !result.OK || len(result.Data.CompiledBackends) != 1 || result.Data.CompiledBackends[0] != "local" || len(result.Data.QualifiedBackends) != 0 || len(result.Data.Protocols) != 1 || result.Data.Protocols[0] != "managed-execution/v1" {
			t.Fatalf("unexpected capabilities: %+v", result)
		}
		gomega.Expect(result.Data.Capabilities).To(gomega.ConsistOf("managed-execution/v1", "durable-observation/v1", "advisory-lock/v1", "managed-recovery/v1", "artifact-delivery/v1", "evidence-retention/v1"))

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
			{"resource", "pause"}, {"resource", "resume", "id", "--json"}, {"resource", "pause", "id", "--expected-revision", "1"}, {"resource", "pause", "id", "--expected-revision", "1", "--json", "--request-key", "bad/key"}, {"resource", "inspect"}, {"resource", "pause", "id", "--expected-revision", "1", "--json", "--expected-config-revision", "1"},
			{"resource", "pause", "id", "--expected-revision", "1", "--json", "--expected-lease-generation", "0"},
			{"resource", "resume", "id", "--expected-revision", "1", "--json", "--reason", "ignored"},
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
			{"CONFLICT", 2}, {"UNSUPPORTED", 2}, {"RESULT_UNAVAILABLE", 2}, {"WAIT_TIMEOUT", 5},
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
		// Follow responses carry their typed issue without a page envelope.
		for _, tc := range []struct {
			code string
			want int
		}{
			{"NOT_FOUND", 2}, {"RESULT_UNAVAILABLE", 2}, {"NESTED_WAIT_REJECTED", 2},
			{"WAIT_TIMEOUT", 5}, {"STORAGE_UNAVAILABLE", 6}, {"TRANSPORT_UNCERTAIN", 6},
		} {
			var out, diagnostic bytes.Buffer
			issue := &client.Issue{Code: tc.code, Message: "bounded explanation", Retry: "none"}
			gomega.Expect(clientFailure(&out, &diagnostic, client.Envelope{}, issue)).To(gomega.Equal(tc.want))
			gomega.Expect(out.Len()).To(gomega.BeZero())
			gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring(tc.code))
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

var _ = ginkgo.Describe("Task007MalformedParentDiagnostic", ginkgo.Label("unit"), func() {
	ginkgo.It("classifies malformed ambient context before a transport attempt", func() {
		t := ginkgo.GinkgoT()
		t.Setenv(managed.ParentContextEnvironment, "invalid")
		var out, diagnostic bytes.Buffer
		c, code := connect(&diagnostic)
		gomega.Expect(c).To(gomega.BeNil())
		gomega.Expect(code).To(gomega.Equal(2))
		gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring("check HAEPARI_PARENT_CONTEXT"))
		gomega.Expect(diagnostic.String()).NotTo(gomega.ContainSubstring("check the daemon"))
		diagnostic.Reset()
		gomega.Expect(clientFailure(&out, &diagnostic, client.Envelope{}, managed.ErrInvalid)).To(gomega.Equal(2))
	})
})

var _ = ginkgo.Describe("Task007AvailabilityDiagnostics", ginkgo.Label("unit"), func() {
	ginkgo.It("distinguishes explicitly supplied invalid epoch pins from omitted defaults before connecting", func() {
		for _, action := range []string{"pause", "resume"} {
			for _, tc := range []struct {
				flags []string
				want  string
			}{
				{[]string{"--expected-config-revision", "0"}, "--expected-config-revision must be at least 1"},
				{[]string{"--expected-config-revision", "-1", "--expected-lease-generation", "0"}, "--expected-config-revision must be at least 1"},
				{[]string{"--expected-lease-generation", "-1"}, "--expected-lease-generation must be at least 0"},
				{[]string{"--expected-config-revision", "1", "--expected-lease-generation", "-2"}, "--expected-lease-generation must be at least 0"},
				{[]string{"--expected-config-revision", "0", "--expected-lease-generation", "-1"}, "--expected-config-revision must be at least 1"},
				{[]string{"--expected-config-revision", "0", "--expected-lease-generation", "0"}, "--expected-config-revision must be at least 1"},
			} {
				var out, diagnostic bytes.Buffer
				args := append([]string{"resource", action, "id", "--expected-revision", "1", "--json"}, tc.flags...)
				gomega.Expect(run(args, &out, &diagnostic)).To(gomega.Equal(2))
				gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring(tc.want))
				gomega.Expect(out.Len()).To(gomega.BeZero())
			}
		}
	})

	ginkgo.It("rejects empty or oversized pause reasons before connecting and explains the byte bound in help", func() {
		for _, reason := range []string{"", strings.Repeat("a", 257), strings.Repeat("é", 129)} {
			var out, diagnostic bytes.Buffer
			gomega.Expect(run([]string{"resource", "pause", "id", "--expected-revision", "1", "--json", "--reason", reason}, &out, &diagnostic)).To(gomega.Equal(2))
			gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring("--reason must contain 1 to 256 bytes"))
			gomega.Expect(out.Len()).To(gomega.BeZero())
		}
		var out, diagnostic bytes.Buffer
		gomega.Expect(run([]string{"resource", "pause", "id", "--help"}, &out, &diagnostic)).To(gomega.Equal(2))
		gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring("1 to 256 bytes (pause only)"))
	})

	ginkgo.It("names both required epoch pins and rejects resume reasons before connecting", func() {
		for _, tc := range []struct {
			flags []string
			want  string
		}{
			{[]string{"--expected-config-revision", "1"}, "--expected-config-revision and --expected-lease-generation"},
			{[]string{"--expected-lease-generation", "0"}, "--expected-config-revision and --expected-lease-generation"},
			{[]string{"--reason", "ignored"}, "--reason applies only to resource pause"},
		} {
			var out, diagnostic bytes.Buffer
			args := append([]string{"resource", "resume", "id", "--expected-revision", "1", "--json"}, tc.flags...)
			gomega.Expect(run(args, &out, &diagnostic)).To(gomega.Equal(2))
			gomega.Expect(diagnostic.String()).To(gomega.ContainSubstring(tc.want))
			gomega.Expect(out.Len()).To(gomega.BeZero())
		}
	})
})
