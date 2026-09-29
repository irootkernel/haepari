package managed

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
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

func TestProvisionalExamplesMatchParsers(t *testing.T) {
	for _, name := range []string{"job-local.template.json", "job-vm.template.json"} {
		data, err := os.ReadFile("../../docs/examples/" + name)
		if err != nil {
			t.Fatal(err)
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
			t.Fatal(err)
		}
		replacements := map[string]string{"<verified-local-supervision-receipt>": "local-receipt", "<operator-owned-cleanup-policy>": "cleanup-policy",
			"<observed-vm-uuid>": "11111111-1111-4111-8111-111111111111", "<vm-mac-address>": "aa:bb:cc:dd:ee:ff",
			"<guest-owned-job-root>": "/var/haepari/jobs", "<verified-backend-receipt>": "backend-receipt",
			"<approved-ssh-mapping>": "ssh-mapping", "<approved-pinned-host-key>": "host-key"}
		for from, to := range replacements {
			data = []byte(strings.ReplaceAll(string(data), from, to))
		}
		if _, err := DecodeResource(data); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func validResource() ResourceSpec {
	return ResourceSpec{Schema: "haepari.resource/v1", ID: "local-a", Kind: "local", ExecutionMode: "managed", Capacity: 2, Revision: 1,
		Adapter: Adapter{QualificationReference: "local-observation"}, Cleanup: &CleanupPolicy{PolicyReference: "local-policy"}}
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDecodeJobClosedAndUnsafeInputs(t *testing.T) {
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
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeJob(test.input)
			if !errors.Is(err, test.want) {
				if test.want == nil && err == nil {
					return
				}
				t.Fatalf("error = %v; want %v", err, test.want)
			}
		})
	}
}

func TestManualAndResourceValidation(t *testing.T) {
	manual := validJob()
	manual.Mode = "manual"
	manual.Deadlines.RunSeconds = 0
	manual.Deadlines.ManualSeconds = 20
	manual.Manual = &Manual{ScenarioDocument: "scenarios/check.md", RequiredScenarios: []string{"one"}}
	if _, err := DecodeJob(encode(t, manual)); err != nil {
		t.Fatal(err)
	}
	manual.Manual.RequiredScenarios = []string{"one", "one"}
	if _, err := DecodeJob(encode(t, manual)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate scenarios: %v", err)
	}
	r := validResource()
	if _, err := DecodeResource(encode(t, r)); err != nil {
		t.Fatal(err)
	}
	r.ExecutionMode = "external"
	if _, err := DecodeResource(encode(t, r)); !errors.Is(err, ErrInvalid) {
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
		t.Fatal(err)
	}
	r.VM.MACAddress = "bad"
	if _, err := DecodeResource(encode(t, r)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad MAC: %v", err)
	}
}

func TestVerdictPriorityAndErrorExits(t *testing.T) {
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
		t.Run(test.name, func(t *testing.T) {
			facts := base
			if test.change != nil {
				test.change(&facts)
			}
			got, err := DeriveVerdict(facts)
			if got != test.want || !errors.Is(err, test.wantErr) {
				t.Fatalf("got %q, %v; want %q, %v", got, err, test.want, test.wantErr)
			}
		})
	}
	for verdict, want := range map[Verdict]int{Pass: 0, Fail: 1, Incomplete: 3, Cancelled: 4} {
		got, err := VerdictExitCode(verdict)
		if err != nil || got != want {
			t.Fatalf("exit %q = %d, %v", verdict, got, err)
		}
	}
}

func TestCanonicalDigestAndManualResult(t *testing.T) {
	job := validJob()
	a, err := CanonicalDigest(job, digest)
	if err != nil {
		t.Fatal(err)
	}
	job.Environment.ProfileID = "moved-alias"
	b, err := CanonicalDigest(job, digest)
	if err != nil || a == b {
		t.Fatalf("alias digest: %q %q %v", a, b, err)
	}
	result := ManualResult{Schema: "haepari.manual-result/v1", JobID: "job-1", InstallationID: "install-1", ProfileID: "profile-1", ProfileDigest: digest, ExpectedRevision: 3, Observer: "operator", Scenarios: []ManualScenario{{ID: "one", Status: "unobserved"}}}
	if _, err := DecodeManualResult(encode(t, result), []string{"one"}); err != nil {
		t.Fatal(err)
	}
	result.Scenarios[0].Status = "pass"
	if _, err := DecodeManualResult(encode(t, result), []string{"one"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pass without evidence: %v", err)
	}
}

func TestPowerIdentityAndEnvelope(t *testing.T) {
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
		t.Fatal(err)
	}
	bad := []byte(`{"schema":"haepari.envelope/v1","operation":"inspect","ok":true,"data":{},"error":{"code":"NOT_FOUND","message":"missing","retry":"none"}}`)
	if _, err := DecodeEnvelope(bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("both branches: %v", err)
	}
	if code, err := ExitCode(TransportUncertain); err != nil || code != 6 {
		t.Fatalf("transport exit: %d %v", code, err)
	}
}

func TestResultRejectsContradictionsAndMissingOutputs(t *testing.T) {
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
		t.Fatal(err)
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
}
