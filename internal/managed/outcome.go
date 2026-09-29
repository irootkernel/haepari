package managed

import (
	"encoding/json"
	"fmt"
)

type WorkloadOutcome string
type EvidenceCompleteness string
type CleanupOutcome string
type Verdict string

const (
	Pass       Verdict = "PASS"
	Fail       Verdict = "FAIL"
	Cancelled  Verdict = "CANCELLED"
	Incomplete Verdict = "INCOMPLETE"
)

type ResultFacts struct {
	WorkloadOutcome        WorkloadOutcome      `json:"workload_outcome"`
	EvidenceCompleteness   EvidenceCompleteness `json:"evidence_completeness"`
	CleanupOutcome         CleanupOutcome       `json:"cleanup_outcome"`
	ManualEligible         bool                 `json:"manual_eligible"`
	CancelledBeforeOutcome bool                 `json:"cancelled_before_outcome"`
	TerminationProven      bool                 `json:"termination_proven"`
	NoOwnedEffect          bool                 `json:"no_owned_effect"`
}

func DeriveVerdict(f ResultFacts) (Verdict, error) {
	if !oneOf(string(f.WorkloadOutcome), "pass", "fail", "cancelled", "timed_out", "inconclusive") ||
		!oneOf(string(f.EvidenceCompleteness), "complete", "partial", "missing", "corrupt", "unobserved") ||
		!oneOf(string(f.CleanupOutcome), "verified", "failed", "unknown", "not_needed") {
		return "", ErrInvalid
	}
	if !f.TerminationProven {
		return "", ErrBlocked
	}
	if f.CleanupOutcome == "not_needed" && !f.NoOwnedEffect {
		return "", ErrInvalid
	}
	if f.NoOwnedEffect && f.CleanupOutcome == "verified" {
		return "", ErrInvalid
	}
	if f.EvidenceCompleteness != "complete" || f.CleanupOutcome == "failed" || f.CleanupOutcome == "unknown" || !f.ManualEligible {
		return Incomplete, nil
	}
	if f.CancelledBeforeOutcome && f.WorkloadOutcome == "cancelled" {
		return Cancelled, nil
	}
	if f.WorkloadOutcome == "pass" && f.CleanupOutcome == "verified" {
		return Pass, nil
	}
	if f.WorkloadOutcome == "fail" && f.CleanupOutcome == "verified" {
		return Fail, nil
	}
	return Incomplete, nil
}

type ManualResult struct {
	Schema           string           `json:"schema"`
	JobID            string           `json:"job_id"`
	InstallationID   string           `json:"installation_id"`
	ProfileID        string           `json:"profile_id"`
	ProfileDigest    string           `json:"profile_digest"`
	ExpectedRevision int64            `json:"expected_revision"`
	Observer         string           `json:"observer"`
	Scenarios        []ManualScenario `json:"scenarios"`
}
type ManualScenario struct {
	ID                 string   `json:"id"`
	Status             string   `json:"status"`
	EvidenceReferences []string `json:"evidence_references"`
}

type Result struct {
	Schema               string               `json:"schema"`
	JobID                string               `json:"job_id"`
	ResourceID           string               `json:"resource_id"`
	ResourceRevision     int64                `json:"resource_revision"`
	LeaseGeneration      int64                `json:"lease_generation"`
	JobState             string               `json:"job_state"`
	ResourceAvailability string               `json:"resource_availability"`
	WorkloadOutcome      WorkloadOutcome      `json:"workload_outcome"`
	EvidenceCompleteness EvidenceCompleteness `json:"evidence_completeness"`
	CleanupOutcome       CleanupOutcome       `json:"cleanup_outcome"`
	FinalVerdict         Verdict              `json:"final_verdict"`
	Reason               string               `json:"reason,omitempty"`
	NativeExitCode       *int                 `json:"native_exit_code,omitempty"`
	NativeSignal         string               `json:"native_signal,omitempty"`
	Timings              *ResultTimings       `json:"timings,omitempty"`
	Artifacts            []ResultArtifact     `json:"artifacts,omitempty"`
	Observations         *ResultObservations  `json:"observations,omitempty"`
	ProtectedEvidence    []ProtectedEvidence  `json:"protected_evidence,omitempty"`
}
type ResultTimings struct {
	AdmittedAt   string        `json:"admitted_at"`
	ReservedAt   string        `json:"reserved_at,omitempty"`
	Phases       []ResultPhase `json:"phases"`
	AdapterSteps []ResultStep  `json:"adapter_steps"`
}
type ResultPhase struct {
	Phase     string `json:"phase"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
}
type ResultStep struct {
	Step           string `json:"step"`
	DurationMillis int64  `json:"duration_millis"`
}
type ResultArtifact struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}
type ResultObservations struct {
	Source           string `json:"source"`
	OperatorAttested bool   `json:"operator_attested"`
}
type ProtectedEvidence struct {
	Path              string `json:"path"`
	Reason            string `json:"reason"`
	RecoveryReference string `json:"recovery_reference"`
}

func DecodeResult(data []byte) (Result, error) {
	var result Result
	if err := decodeClosed(data, &result); err != nil {
		return result, err
	}
	if result.Schema != "haepari.result/v1" {
		return result, ErrUnsupported
	}
	if !identifier.MatchString(result.JobID) || !identifier.MatchString(result.ResourceID) || result.ResourceRevision <= 0 ||
		result.JobState != "finished" || !oneOf(result.ResourceAvailability, "available", "paused", "recovering", "quarantined") ||
		!oneOf(string(result.FinalVerdict), "PASS", "FAIL", "CANCELLED", "INCOMPLETE") ||
		!oneOf(string(result.WorkloadOutcome), "pass", "fail", "cancelled", "timed_out", "inconclusive") ||
		!oneOf(string(result.EvidenceCompleteness), "complete", "partial", "missing", "corrupt", "unobserved") ||
		!oneOf(string(result.CleanupOutcome), "verified", "failed", "unknown", "not_needed") {
		return result, ErrInvalid
	}
	if result.NativeExitCode != nil && (*result.NativeExitCode < 0 || *result.NativeExitCode > 255) {
		return result, ErrInvalid
	}
	if result.NativeExitCode != nil && result.NativeSignal != "" {
		return result, ErrInvalid
	}
	for _, e := range result.ProtectedEvidence {
		if !relativePath(e.Path) || e.Reason == "" || e.RecoveryReference == "" {
			return result, ErrInvalid
		}
	}
	for _, a := range result.Artifacts {
		if !relativePath(a.Path) || a.SizeBytes < 0 || !validDigest(a.SHA256) {
			return result, ErrInvalid
		}
	}
	if len(result.Artifacts) > 0 {
		paths := make([]string, 0, len(result.Artifacts))
		for _, a := range result.Artifacts {
			paths = append(paths, a.Path)
		}
		if !uniquePaths(paths) {
			return result, ErrInvalid
		}
	}
	if len(result.ProtectedEvidence) > 0 && (result.ResourceAvailability != "quarantined" || result.FinalVerdict != Incomplete || result.CleanupOutcome != "failed") {
		return result, ErrInvalid
	}
	if result.LeaseGeneration < 0 {
		return result, ErrInvalid
	}
	if result.LeaseGeneration == 0 && result.CleanupOutcome != "not_needed" {
		return result, ErrInvalid
	}
	if result.FinalVerdict == Pass && (result.WorkloadOutcome != "pass" || result.EvidenceCompleteness != "complete" || result.CleanupOutcome != "verified") {
		return result, ErrInvalid
	}
	if result.FinalVerdict == Fail && (result.WorkloadOutcome != "fail" || result.EvidenceCompleteness != "complete" || result.CleanupOutcome != "verified") {
		return result, ErrInvalid
	}
	if (result.FinalVerdict == Pass || result.FinalVerdict == Fail) && len(result.Artifacts) == 0 {
		return result, ErrInvalid
	}
	if result.FinalVerdict == Cancelled && (result.WorkloadOutcome != "cancelled" || result.EvidenceCompleteness != "complete" || !oneOf(string(result.CleanupOutcome), "verified", "not_needed")) {
		return result, ErrInvalid
	}
	if result.CleanupOutcome == "not_needed" && oneOf(string(result.WorkloadOutcome), "pass", "fail") {
		return result, ErrInvalid
	}
	derived, err := DeriveVerdict(ResultFacts{WorkloadOutcome: result.WorkloadOutcome, EvidenceCompleteness: result.EvidenceCompleteness,
		CleanupOutcome: result.CleanupOutcome, ManualEligible: true, CancelledBeforeOutcome: result.WorkloadOutcome == "cancelled",
		TerminationProven: true, NoOwnedEffect: result.CleanupOutcome == "not_needed"})
	if err != nil || derived != result.FinalVerdict {
		return result, ErrInvalid
	}
	return result, nil
}

// DecodeResultForJob checks required outputs against the exact admitted Job.
// A complete verdict cannot be inferred from the result envelope alone.
func DecodeResultForJob(data []byte, job JobSpec) (Result, error) {
	result, err := DecodeResult(data)
	if err != nil {
		return result, err
	}
	if result.EvidenceCompleteness == "complete" && result.CleanupOutcome != "not_needed" {
		present := map[string]bool{}
		for _, artifact := range result.Artifacts {
			present[artifact.Path] = true
		}
		for _, required := range job.Workload.RequiredOutputs {
			if !present[required] {
				return result, ErrInvalid
			}
		}
	}
	return result, nil
}

func DecodeManualResult(data []byte, required []string) (ManualResult, error) {
	var result ManualResult
	if err := decodeClosed(data, &result); err != nil {
		return result, err
	}
	if result.Schema != "haepari.manual-result/v1" {
		return result, ErrUnsupported
	}
	if !identifier.MatchString(result.JobID) || !identifier.MatchString(result.InstallationID) ||
		!identifier.MatchString(result.ProfileID) || !validDigest(result.ProfileDigest) || result.ExpectedRevision <= 0 ||
		result.Observer == "" || len(result.Observer) > 256 || len(result.Scenarios) != len(required) {
		return result, ErrInvalid
	}
	seen := map[string]bool{}
	for _, scenario := range result.Scenarios {
		if !oneOf(scenario.Status, "pass", "fail", "unobserved") || seen[scenario.ID] || !identifier.MatchString(scenario.ID) {
			return result, ErrInvalid
		}
		seen[scenario.ID] = true
		if scenario.Status != "unobserved" && len(scenario.EvidenceReferences) == 0 {
			return result, ErrInvalid
		}
		for _, reference := range scenario.EvidenceReferences {
			if reference == "" || len(reference) > 512 {
				return result, ErrInvalid
			}
		}
	}
	for _, id := range required {
		if !seen[id] {
			return result, ErrInvalid
		}
	}
	return result, nil
}

type ErrorCode string

const (
	InvalidRequest            ErrorCode = "INVALID_REQUEST"
	Conflict                  ErrorCode = "CONFLICT"
	Unsupported               ErrorCode = "UNSUPPORTED"
	PrerequisiteUnavailable   ErrorCode = "PREREQUISITE_UNAVAILABLE"
	NestedAcquisitionRejected ErrorCode = "NESTED_ACQUISITION_REJECTED"
	NestedWaitRejected        ErrorCode = "NESTED_WAIT_REJECTED"
	NotFound                  ErrorCode = "NOT_FOUND"
	ResourceBusy              ErrorCode = "RESOURCE_BUSY"
	LockExpired               ErrorCode = "LOCK_EXPIRED"
	ProtocolUnsupported       ErrorCode = "PROTOCOL_UNSUPPORTED"
	StaleRevision             ErrorCode = "STALE_REVISION"
	RecoveryBlocked           ErrorCode = "RECOVERY_BLOCKED"
	WaitTimeout               ErrorCode = "WAIT_TIMEOUT"
	TransportUncertain        ErrorCode = "TRANSPORT_UNCERTAIN"
	StorageUnavailable        ErrorCode = "STORAGE_UNAVAILABLE"
)

func ExitCode(code ErrorCode) (int, error) {
	switch code {
	case InvalidRequest, Conflict, Unsupported, PrerequisiteUnavailable, NestedAcquisitionRejected,
		NestedWaitRejected, NotFound, ResourceBusy, LockExpired, ProtocolUnsupported, StaleRevision, RecoveryBlocked:
		return 2, nil
	case WaitTimeout:
		return 5, nil
	case TransportUncertain, StorageUnavailable:
		return 6, nil
	default:
		return 0, fmt.Errorf("%w: unknown error code", ErrInvalid)
	}
}

func VerdictExitCode(verdict Verdict) (int, error) {
	switch verdict {
	case Pass:
		return 0, nil
	case Fail:
		return 1, nil
	case Incomplete:
		return 3, nil
	case Cancelled:
		return 4, nil
	default:
		return 0, ErrInvalid
	}
}

type Envelope struct {
	Schema    string          `json:"schema"`
	Operation string          `json:"operation"`
	OK        bool            `json:"ok"`
	Data      json.RawMessage `json:"data,omitempty"`
	Error     *MachineError   `json:"error,omitempty"`
}
type MachineError struct {
	Code       ErrorCode `json:"code"`
	Message    string    `json:"message"`
	Retry      string    `json:"retry"`
	JobID      string    `json:"job_id,omitempty"`
	ResourceID string    `json:"resource_id,omitempty"`
}

func DecodeEnvelope(data []byte) (Envelope, error) {
	var result Envelope
	if err := decodeClosed(data, &result); err != nil {
		return result, err
	}
	if result.Schema != "haepari.envelope/v1" {
		return result, ErrUnsupported
	}
	if result.Operation == "" || len(result.Operation) > 128 || (result.OK && (result.Error != nil || len(result.Data) == 0)) ||
		(!result.OK && (result.Error == nil || len(result.Data) != 0)) {
		return result, ErrInvalid
	}
	if result.Error != nil {
		if _, err := ExitCode(result.Error.Code); err != nil || result.Error.Message == "" || len(result.Error.Message) > 512 ||
			!oneOf(result.Error.Retry, "none", "retry_same_key", "reconcile") {
			return result, ErrInvalid
		}
	}
	return result, nil
}
