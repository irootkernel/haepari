package managed

import (
	"encoding/json"
	"errors"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"os"
	"strings"
)

const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func validJob() JobSpec {
	return JobSpec{Schema: "haepari.job/v1", ExecutionMode: "managed", ResourceID: "local-a", Mode: "auto", Suite: "build",
		Input:       Input{Root: "/tmp/sealed-input", InventoryDigest: digest},
		Environment: Environment{ProfileID: "build-profile", ProfileDigest: digest},
		Workload:    Workload{Argv: []string{"make", "build"}, CWD: "source", Environment: map[string]string{}, RequiredOutputs: []string{"results/build.json"}},
		Deadlines:   Deadlines{QueueSeconds: 10, PrepareSeconds: 10, RunSeconds: 10, CollectSeconds: 10, CleanupSeconds: 10},
		Effects:     Effects{Mode: "offline", AuthorizationReference: "approved-job"},
		Provenance:  Provenance{CandidateDigest: digest, TestsDigest: digest, TargetProfileDigest: digest, EvidenceClass: "development"}}
}

var _ = ginkgo.Describe("ProvisionalExamplesMatchParsers", ginkgo.Label("unit"), func() {
	ginkgo.It("ProvisionalExamplesMatchParsers", func() {
		t := ginkgo.GinkgoT()
		for _, name := range []string{"job-local.template.json", "job-vm.template.json"} {
			data, err := os.ReadFile("../../docs/examples/" + name)
			if err != nil {
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			replacements := map[string]string{"<absolute-prepared-bundle>": "/tmp/sealed-bundle", "<sha256>": digest,
				"<local-build-profile>": "local-profile", "<prior-accepted-profile>": "vm-profile", "<approved-job-envelope>": "approved-job"}
			for from, to := range replacements {
				data = []byte(strings.ReplaceAll(string(data), from, to))
			}
			if _, err := DecodeJob(data); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		for _, name := range []string{"resource-local.template.json", "resource-macos.template.json"} {
			data, err := os.ReadFile("../../docs/examples/" + name)
			if err != nil {
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
			replacements := map[string]string{"<verified-local-supervision-receipt>": "local-receipt", "<operator-owned-cleanup-policy>": "cleanup-policy",
				"<observed-vm-uuid>": "11111111-1111-4111-8111-111111111111", "<vm-mac-address>": "aa:bb:cc:dd:ee:ff",
				"<guest-owned-job-root>": "/var/haepari/jobs", "<verified-backend-receipt>": "backend-receipt",
				"<ssh-mapping-file-sha256>": digest, "<absolute-private-mapping-file>": "/tmp/haepari-ssh-mapping", "<known-hosts-file-sha256>": digest, "<absolute-dedicated-known-hosts-file>": "/tmp/haepari-known-hosts"}
			for from, to := range replacements {
				data = []byte(strings.ReplaceAll(string(data), from, to))
			}
			if _, err := DecodeResource(data); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}

	})
})

func validResource() ResourceSpec {
	return ResourceSpec{Schema: "haepari.resource/v1", ID: "local-a", Kind: "local", ExecutionMode: "managed", Capacity: 2, Revision: 1,
		Adapter: Adapter{QualificationReference: "local-observation"}, Cleanup: &CleanupPolicy{PolicyReference: "local-policy"}}
}

func encode(t ginkgo.GinkgoTInterface, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
	return data
}

var _ = ginkgo.Describe("DecodeJobClosedAndUnsafeInputs", ginkgo.Label("unit"), func() {
	ginkgo.It("DecodeJobClosedAndUnsafeInputs", func() {
		t := ginkgo.GinkgoT()
		base := validJob()
		cases := []struct {
			name  string
			input []byte
			want  error
		}{
			{"valid", encode(t, base), nil},
			{"malformed", []byte(`{"schema":`), ErrInvalid},
			{"duplicate nested key", []byte(strings.Replace(string(encode(t, base)), `"cwd":"source"`, `"cwd":"source","cwd":"other"`, 1)), ErrInvalid},
			{"case variant duplicate", []byte(strings.Replace(string(encode(t, base)), `"mode":"auto"`, `"mode":"auto","MODE":"auto"`, 1)), ErrInvalid},
			{"case variant single", []byte(strings.Replace(string(encode(t, base)), `"mode":"auto"`, `"MODE":"auto"`, 1)), ErrInvalid},
			{"unknown field", []byte(strings.Replace(string(encode(t, base)), `"suite":"build"`, `"suite":"build","surprise":true`, 1)), ErrInvalid},
			{"unsupported execution", func() []byte { j := base; j.ExecutionMode = "external"; return encode(t, j) }(), ErrUnsupported},
			{"missing outputs", func() []byte { j := base; j.Workload.RequiredOutputs = nil; return encode(t, j) }(), ErrInvalid},
			{"traversal", func() []byte { j := base; j.Workload.CWD = "../source"; return encode(t, j) }(), ErrInvalid},
			{"duplicate outputs", func() []byte {
				j := base
				j.Workload.RequiredOutputs = []string{"Results/a", "results/A"}
				return encode(t, j)
			}(), ErrInvalid},
			{"placeholder", func() []byte { j := base; j.Effects.AuthorizationReference = "<approval>"; return encode(t, j) }(), ErrInvalid},
			{"wrong type", []byte(strings.Replace(string(encode(t, base)), `"queue_seconds":10`, `"queue_seconds":"10"`, 1)), ErrInvalid},
		}
		for _, test := range cases {
			ginkgo.By(test.name)
			func(t ginkgo.GinkgoTInterface) {
				_, err := DecodeJob(test.input)
				if !errors.Is(err, test.want) {
					if test.want == nil && err == nil {
						return
					}
					t.Fatalf("error = %v; want %v", err, test.want)
				}
			}(t)
		}

	})
})

var _ = ginkgo.Describe("ManualAndResourceValidation", ginkgo.Label("unit"), func() {
	ginkgo.It("ManualAndResourceValidation", func() {
		t := ginkgo.GinkgoT()
		manual := validJob()
		manual.Mode = "manual"
		manual.Deadlines.RunSeconds = 0
		manual.Deadlines.ManualSeconds = 20
		manual.Manual = &Manual{ScenarioDocument: "scenarios/check.md", RequiredScenarios: []string{"one"}}
		if _, err := DecodeJob(encode(t, manual)); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		manual.Manual.RequiredScenarios = []string{"one", "one"}
		if _, err := DecodeJob(encode(t, manual)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("duplicate scenarios: %v", err)
		}
		r := validResource()
		if _, err := DecodeResource(encode(t, r)); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		r.ExecutionMode = "external"
		if _, err := DecodeResource(encode(t, r)); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("external: %v", err)
		}
		r = validResource()
		r.Kind = "utm-macos"
		r.Capacity = 1
		r.Cleanup = nil
		r.Adapter.UTMCtl = "/Applications/UTM.app/Contents/MacOS/utmctl"
		r.VM = &VM{UTMVMUUID: "11111111-1111-4111-8111-111111111111", MACAddress: "aa:bb:cc:dd:ee:ff", AddressDiscovery: "dhcp-lease-by-mac", GuestJobRoot: "/var/haepari/jobs"}
		r.Guest = &Guest{ConnectionReference: "ssh-map", KnownHostsReference: "host-key"}
		if _, err := DecodeResource(encode(t, r)); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		r.ExecutionMode = "external"
		if _, err := DecodeResource(encode(t, r)); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("external VM mode: %v", err)
		}
		r.ExecutionMode = "managed"
		r.VM.MACAddress = "bad"
		if _, err := DecodeResource(encode(t, r)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad MAC: %v", err)
		}

	})
})

var _ = ginkgo.Describe("VerdictPriorityAndErrorExits", ginkgo.Label("unit"), func() {
	ginkgo.It("VerdictPriorityAndErrorExits", func() {
		t := ginkgo.GinkgoT()
		base := ResultFacts{WorkloadOutcome: "pass", EvidenceCompleteness: "complete", CleanupOutcome: "verified", ManualEligible: true, TerminationProven: true}
		cases := []struct {
			name    string
			change  func(*ResultFacts)
			want    Verdict
			wantErr error
		}{
			{"pass", nil, Pass, nil},
			{"failure", func(f *ResultFacts) { f.WorkloadOutcome = "fail" }, Fail, nil},
			{"failed with missing evidence", func(f *ResultFacts) { f.WorkloadOutcome = "fail"; f.EvidenceCompleteness = "missing" }, Incomplete, nil},
			{"failed cleanup", func(f *ResultFacts) { f.WorkloadOutcome = "fail"; f.CleanupOutcome = "failed" }, Incomplete, nil},
			{"failure with partial evidence and unknown cleanup", func(f *ResultFacts) {
				f.WorkloadOutcome = "fail"
				f.EvidenceCompleteness = "partial"
				f.CleanupOutcome = "unknown"
			}, Incomplete, nil},
			{"cancel with missing evidence", func(f *ResultFacts) {
				f.WorkloadOutcome = "cancelled"
				f.CancelledBeforeOutcome = true
				f.EvidenceCompleteness = "missing"
			}, Incomplete, nil},
			{"cancel with failed cleanup", func(f *ResultFacts) {
				f.WorkloadOutcome = "cancelled"
				f.CancelledBeforeOutcome = true
				f.CleanupOutcome = "failed"
			}, Incomplete, nil},
			{"no-effect preparation failure", func(f *ResultFacts) {
				f.WorkloadOutcome = "inconclusive"
				f.CleanupOutcome = "not_needed"
				f.NoOwnedEffect = true
			}, Incomplete, nil},
			{"manual unobserved", func(f *ResultFacts) { f.ManualEligible = false }, Incomplete, nil},
			{"cancel", func(f *ResultFacts) {
				f.WorkloadOutcome = "cancelled"
				f.CancelledBeforeOutcome = true
				f.CleanupOutcome = "not_needed"
				f.NoOwnedEffect = true
			}, Cancelled, nil},
			{"late cancel preserves failure", func(f *ResultFacts) { f.WorkloadOutcome = "fail"; f.CancelledBeforeOutcome = false }, Fail, nil},
			{"deadline", func(f *ResultFacts) { f.WorkloadOutcome = "timed_out" }, Incomplete, nil},
			{"uncertain termination", func(f *ResultFacts) { f.TerminationProven = false }, "", ErrBlocked},
			{"false not-needed", func(f *ResultFacts) { f.CleanupOutcome = "not_needed" }, "", ErrInvalid},
		}
		for _, test := range cases {
			ginkgo.By(test.name)
			func(t ginkgo.GinkgoTInterface) {
				facts := base
				if test.change != nil {
					test.change(&facts)
				}
				got, err := DeriveVerdict(facts)
				if got != test.want || !errors.Is(err, test.wantErr) {
					t.Fatalf("got %q, %v; want %q, %v", got, err, test.want, test.wantErr)
				}
			}(t)
		}
		for verdict, want := range map[Verdict]int{Pass: 0, Fail: 1, Incomplete: 3, Cancelled: 4} {
			got, err := VerdictExitCode(verdict)
			if err != nil || got != want {
				t.Fatalf("exit %q = %d, %v", verdict, got, err)
			}
		}

	})
})

var _ = ginkgo.Describe("CanonicalDigestAndManualResult", ginkgo.Label("unit"), func() {
	ginkgo.It("CanonicalDigestAndManualResult", func() {
		t := ginkgo.GinkgoT()
		job := validJob()
		a, err := CanonicalDigest(job, digest)
		if err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		job.Environment.ProfileID = "moved-alias"
		b, err := CanonicalDigest(job, digest)
		if err != nil || a == b {
			t.Fatalf("alias digest: %q %q %v", a, b, err)
		}
		result := ManualResult{Schema: "haepari.manual-result/v1", JobID: "job-1", InstallationID: "install-1", ProfileID: "profile-1", ProfileDigest: digest, ExpectedRevision: 3, Observer: "operator", Scenarios: []ManualScenario{{ID: "one", Status: "unobserved"}}}
		if _, err := DecodeManualResult(encode(t, result), []string{"one"}); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		result.Scenarios[0].Status = "pass"
		if _, err := DecodeManualResult(encode(t, result), []string{"one"}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("pass without evidence: %v", err)
		}

	})
})

var _ = ginkgo.Describe("PowerIdentityAndEnvelope", ginkgo.Label("unit"), func() {
	ginkgo.It("PowerIdentityAndEnvelope", func() {
		t := ginkgo.GinkgoT()
		p := PowerTransitionIdentity{OperationGeneration: 1, TargetJobID: "job-a", TargetLeaseGeneration: 2,
			TargetResourceID: "vm-a", TargetConfigRevision: 3, TargetVMUUID: "11111111-1111-4111-8111-111111111111",
			VictimResourceID: "vm-b", VictimConfigRevision: 4, VictimVMUUID: "22222222-2222-4222-8222-222222222222"}
		if !p.Valid() {
			t.Fatal("valid power identity rejected")
		}
		p.VictimVMUUID = p.TargetVMUUID
		if p.Valid() {
			t.Fatal("same VM selected as victim")
		}
		good := []byte(`{"schema":"haepari.envelope/v1","operation":"inspect","ok":true,"data":{"job_id":"job-a"}}`)
		if _, err := DecodeEnvelope(good); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		bad := []byte(`{"schema":"haepari.envelope/v1","operation":"inspect","ok":true,"data":{},"error":{"code":"NOT_FOUND","message":"missing","retry":"none"}}`)
		if _, err := DecodeEnvelope(bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("both branches: %v", err)
		}
		if code, err := ExitCode(TransportUncertain); err != nil || code != 6 {
			t.Fatalf("transport exit: %d %v", code, err)
		}

	})
})

var _ = ginkgo.Describe("ResultRejectsContradictionsAndMissingOutputs", ginkgo.Label("unit"), func() {
	ginkgo.It("ResultRejectsContradictionsAndMissingOutputs", func() {
		t := ginkgo.GinkgoT()
		result := Result{Schema: "haepari.result/v1", JobID: "job-a", ResourceID: "local-a", ResourceRevision: 1, LeaseGeneration: 1,
			JobState: "finished", ResourceAvailability: "available", WorkloadOutcome: "pass", EvidenceCompleteness: "complete",
			CleanupOutcome: "verified", FinalVerdict: Pass}
		if _, err := DecodeResult(encode(t, result)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("PASS without artifacts: %v", err)
		}
		result.WorkloadOutcome = "fail"
		result.FinalVerdict = Fail
		if _, err := DecodeResult(encode(t, result)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("FAIL without artifacts: %v", err)
		}
		result.WorkloadOutcome = "pass"
		result.FinalVerdict = Pass
		result.Artifacts = []ResultArtifact{{Path: "results/build.json", SizeBytes: 1, SHA256: digest}}
		if _, err := DecodeResultForJob(encode(t, result), validJob()); err != nil {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		result.Artifacts[0].Path = "results/other.json"
		if _, err := DecodeResultForJob(encode(t, result), validJob()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("missing declared output: %v", err)
		}
		result.FinalVerdict = Cancelled
		if _, err := DecodeResult(encode(t, result)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("contradictory CANCELLED: %v", err)
		}
		result.WorkloadOutcome = "alien"
		result.FinalVerdict = Incomplete
		if _, err := DecodeResult(encode(t, result)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unknown outcome: %v", err)
		}
		result.WorkloadOutcome = "fail"
		result.CleanupOutcome = "verified"
		result.EvidenceCompleteness = "complete"
		if _, err := DecodeResult(encode(t, result)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("verified failure marked incomplete: %v", err)
		}

	})
})

var _ = ginkgo.Describe("AdvanceLocalRejectsInvalidPhasePairs", ginkgo.Label("unit"), func() {
	ginkgo.It("AdvanceLocalRejectsInvalidPhasePairs", func() {
		t := ginkgo.GinkgoT()
		var ledger Ledger
		for _, pair := range [][2]string{{"preparing", "collecting"}, {"ready", "cleaning"}, {"running", "settling"}} {
			if _, err := ledger.AdvanceLocal("job", 1, pair[0], pair[1], "invalid-transition"); !errors.Is(err, ErrInvalid) {
				t.Errorf("%s -> %s: %v", pair[0], pair[1], err)
			}
		}

	})
})
