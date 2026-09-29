// Package managed contains the provisional managed execution contract. The
// public surface remains subject to the roadmap's F1 and F2 freeze gates.
package managed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

var (
	ErrInvalid     = errors.New("INVALID_REQUEST")
	ErrUnsupported = errors.New("UNSUPPORTED")
	ErrConflict    = errors.New("CONFLICT")
	ErrStale       = errors.New("STALE_REVISION")
	ErrBusy        = errors.New("RESOURCE_BUSY")
	ErrNotFound    = errors.New("NOT_FOUND")
	ErrBlocked     = errors.New("RECOVERY_BLOCKED")
)

const maxDocumentBytes = 1 << 20

type JobSpec struct {
	Schema        string      `json:"schema"`
	ExecutionMode string      `json:"execution_mode"`
	ResourceID    string      `json:"resource_id"`
	Mode          string      `json:"mode"`
	Suite         string      `json:"suite"`
	Input         Input       `json:"input"`
	Environment   Environment `json:"environment"`
	Workload      Workload    `json:"workload"`
	Setup         *Setup      `json:"setup,omitempty"`
	Manual        *Manual     `json:"manual,omitempty"`
	Deadlines     Deadlines   `json:"deadlines"`
	Effects       Effects     `json:"effects"`
	Provenance    Provenance  `json:"provenance"`
}

type Input struct {
	Root            string   `json:"root"`
	InventoryDigest string   `json:"inventory_digest"`
	SelectedFiles   []string `json:"selected_files"`
}
type Environment struct {
	ProfileID     string `json:"profile_id"`
	ProfileDigest string `json:"profile_digest"`
}
type Workload struct {
	Argv            []string          `json:"argv"`
	CWD             string            `json:"cwd"`
	Environment     map[string]string `json:"environment"`
	RequiredOutputs []string          `json:"required_outputs"`
}
type Setup struct {
	Argv []string `json:"argv"`
}
type Manual struct {
	ScenarioDocument  string   `json:"scenario_document"`
	RequiredScenarios []string `json:"required_scenarios"`
}
type Deadlines struct {
	QueueSeconds   int64 `json:"queue_seconds"`
	PrepareSeconds int64 `json:"prepare_seconds"`
	RunSeconds     int64 `json:"run_seconds,omitempty"`
	ManualSeconds  int64 `json:"manual_seconds,omitempty"`
	CollectSeconds int64 `json:"collect_seconds"`
	CleanupSeconds int64 `json:"cleanup_seconds"`
}
type Effects struct {
	Mode                   string `json:"mode"`
	AuthorizationReference string `json:"authorization_reference"`
}
type Provenance struct {
	CandidateDigest     string `json:"candidate_digest"`
	TestsDigest         string `json:"tests_digest"`
	TargetProfileDigest string `json:"target_profile_digest"`
	EvidenceClass       string `json:"evidence_class"`
}

type ResourceSpec struct {
	Schema        string         `json:"schema"`
	ID            string         `json:"id"`
	Kind          string         `json:"kind"`
	ExecutionMode string         `json:"execution_mode"`
	Capacity      int            `json:"capacity"`
	Revision      int64          `json:"revision"`
	Adapter       Adapter        `json:"adapter"`
	Cleanup       *CleanupPolicy `json:"cleanup,omitempty"`
	VM            *VM            `json:"vm,omitempty"`
	Guest         *Guest         `json:"guest,omitempty"`
}
type Adapter struct {
	QualificationReference string `json:"qualification_reference"`
	UTMCtl                 string `json:"utmctl,omitempty"`
}
type CleanupPolicy struct {
	PolicyReference string `json:"policy_reference"`
}
type VM struct {
	UTMVMUUID          string `json:"utm_vm_uuid"`
	MACAddress         string `json:"mac_address"`
	AllowAutomaticStop bool   `json:"allow_automatic_stop"`
	AddressDiscovery   string `json:"address_discovery"`
	GuestJobRoot       string `json:"guest_job_root"`
}
type Guest struct {
	ConnectionReference string `json:"connection_reference"`
	KnownHostsReference string `json:"known_hosts_reference"`
}

// PowerTransitionIdentity is the provisional victim-to-target fence. A
// scheduler records this tuple before invoking UTM; a Job lease alone never
// authorizes stopping a different VM.
type PowerTransitionIdentity struct {
	OperationGeneration   int64  `json:"operation_generation"`
	TargetJobID           string `json:"target_job_id"`
	TargetLeaseGeneration int64  `json:"target_lease_generation"`
	TargetResourceID      string `json:"target_resource_id"`
	TargetConfigRevision  int64  `json:"target_config_revision"`
	TargetVMUUID          string `json:"target_vm_uuid"`
	VictimResourceID      string `json:"victim_resource_id"`
	VictimConfigRevision  int64  `json:"victim_config_revision"`
	VictimVMUUID          string `json:"victim_vm_uuid"`
}

func (p PowerTransitionIdentity) Valid() bool {
	return p.OperationGeneration > 0 && identifier.MatchString(p.TargetJobID) && p.TargetLeaseGeneration > 0 &&
		identifier.MatchString(p.TargetResourceID) && p.TargetConfigRevision > 0 && vmUUIDPattern.MatchString(p.TargetVMUUID) &&
		identifier.MatchString(p.VictimResourceID) && p.VictimConfigRevision > 0 && vmUUIDPattern.MatchString(p.VictimVMUUID) &&
		p.TargetResourceID != p.VictimResourceID && !strings.EqualFold(p.TargetVMUUID, p.VictimVMUUID)
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var macAddress = regexp.MustCompile(`^(?i:[0-9a-f]{2}:){5}(?i:[0-9a-f]{2})$`)
var vmUUIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func DecodeJob(data []byte) (JobSpec, error) {
	var job JobSpec
	if err := decodeClosed(data, &job); err != nil {
		return job, err
	}
	if job.Schema != "haepari.job/v1" || job.ExecutionMode != "managed" {
		return job, fmt.Errorf("%w: job schema or execution mode", ErrUnsupported)
	}
	if !identifier.MatchString(job.ResourceID) || !identifier.MatchString(job.Suite) ||
		(job.Mode != "auto" && job.Mode != "manual") || !validDigest(job.Input.InventoryDigest) ||
		!filepath.IsAbs(job.Input.Root) || filepath.Clean(job.Input.Root) != job.Input.Root ||
		!identifier.MatchString(job.Environment.ProfileID) || !validDigest(job.Environment.ProfileDigest) ||
		!validDigest(job.Provenance.CandidateDigest) || !validDigest(job.Provenance.TestsDigest) ||
		!validDigest(job.Provenance.TargetProfileDigest) || !oneOf(job.Provenance.EvidenceClass, "development", "candidate-integration", "release-distribution") ||
		!oneOf(job.Effects.Mode, "offline", "live") || job.Effects.AuthorizationReference == "" {
		return job, ErrInvalid
	}
	if len(job.Input.SelectedFiles) > 1000 || (len(job.Input.SelectedFiles) > 0 && !uniquePaths(job.Input.SelectedFiles)) ||
		len(job.Workload.Argv) == 0 || len(job.Workload.Argv) > 128 || len(job.Workload.RequiredOutputs) == 0 || len(job.Workload.RequiredOutputs) > 1000 ||
		!relativePath(job.Workload.CWD) || !uniquePaths(job.Workload.RequiredOutputs) {
		return job, ErrInvalid
	}
	for _, arg := range job.Workload.Argv {
		if arg == "" {
			return job, ErrInvalid
		}
	}
	for name := range job.Workload.Environment {
		if !envName.MatchString(name) {
			return job, ErrInvalid
		}
	}
	if !positive(job.Deadlines.QueueSeconds, job.Deadlines.PrepareSeconds, job.Deadlines.CollectSeconds, job.Deadlines.CleanupSeconds) {
		return job, ErrInvalid
	}
	if job.Mode == "auto" {
		if job.Manual != nil || !positive(job.Deadlines.RunSeconds) || job.Deadlines.ManualSeconds != 0 {
			return job, ErrInvalid
		}
	} else {
		if job.Manual == nil || !relativePath(job.Manual.ScenarioDocument) || !uniqueNames(job.Manual.RequiredScenarios) ||
			!positive(job.Deadlines.ManualSeconds) || job.Deadlines.RunSeconds != 0 {
			return job, ErrInvalid
		}
	}
	if job.Setup != nil && (len(job.Setup.Argv) == 0 || len(job.Setup.Argv) > 128) {
		return job, ErrInvalid
	}
	return job, nil
}

func DecodeResource(data []byte) (ResourceSpec, error) {
	var r ResourceSpec
	if err := decodeClosed(data, &r); err != nil {
		return r, err
	}
	if r.Schema != "haepari.resource/v1" || (r.ExecutionMode != "managed" && r.ExecutionMode != "external") || r.Kind == "external" {
		return r, fmt.Errorf("%w: resource schema or execution mode", ErrUnsupported)
	}
	if !identifier.MatchString(r.ID) || r.Capacity <= 0 || r.Revision <= 0 || r.ExecutionMode != "managed" || r.Adapter.QualificationReference == "" {
		return r, ErrInvalid
	}
	switch r.Kind {
	case "local":
		if r.VM != nil || r.Guest != nil || r.Adapter.UTMCtl != "" || r.Cleanup == nil || r.Cleanup.PolicyReference == "" {
			return r, ErrInvalid
		}
	case "utm-macos":
		if r.Capacity != 1 || r.Cleanup != nil || r.VM == nil || r.Guest == nil || !filepath.IsAbs(r.Adapter.UTMCtl) ||
			!vmUUIDPattern.MatchString(r.VM.UTMVMUUID) || !macAddress.MatchString(r.VM.MACAddress) || r.VM.AddressDiscovery != "dhcp-lease-by-mac" ||
			!path.IsAbs(r.VM.GuestJobRoot) || path.Clean(r.VM.GuestJobRoot) != r.VM.GuestJobRoot ||
			r.Guest.ConnectionReference == "" || r.Guest.KnownHostsReference == "" {
			return r, ErrInvalid
		}
	default:
		return r, fmt.Errorf("%w: resource kind", ErrUnsupported)
	}
	return r, nil
}

// CanonicalDigest binds the validated submitted shape and the already sealed
// inventory, without resolving aliases or resource configuration.
func CanonicalDigest(job JobSpec, sealedDigest string) (string, error) {
	if !validDigest(sealedDigest) {
		return "", ErrInvalid
	}
	encoded, err := json.Marshal(struct {
		Job          JobSpec `json:"job"`
		SealedDigest string  `json:"sealed_digest"`
	}{job, sealedDigest})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func decodeClosed(data []byte, target any) error {
	if len(data) == 0 || len(data) > maxDocumentBytes {
		return ErrInvalid
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	if err := rejectNoncanonicalKeys(data, reflect.TypeOf(target)); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ErrInvalid
	}
	if hasPlaceholder(data) {
		return ErrInvalid
	}
	return nil
}

func rejectNoncanonicalKeys(data []byte, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == reflect.TypeOf(json.RawMessage{}) {
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			return ErrInvalid
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			fields[name] = field.Type
		}
		for name, raw := range object {
			fieldType, ok := fields[name]
			if !ok {
				return fmt.Errorf("%w: noncanonical field %q", ErrInvalid, name)
			}
			if err := rejectNoncanonicalKeys(raw, fieldType); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return ErrInvalid
		}
		for _, item := range items {
			if err := rejectNoncanonicalKeys(item, typ.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			return ErrInvalid
		}
		for _, raw := range object {
			if err := rejectNoncanonicalKeys(raw, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanValue(decoder); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

func scanValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return errors.New("duplicate object key")
			}
			seen[key] = true
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected delimiter")
	}
	_, err = decoder.Token()
	return err
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}
func positive(values ...int64) bool {
	for _, v := range values {
		if v <= 0 || v > 366*24*60*60 {
			return false
		}
	}
	return true
}
func oneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}
func relativePath(value string) bool {
	return value != "" && value != "." && !path.IsAbs(value) && path.Clean(value) == value &&
		!strings.HasPrefix(value, "../") && value != ".." && !strings.Contains(value, "\\") && !strings.ContainsRune(value, 0)
}
func uniquePaths(values []string) bool {
	if len(values) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if !relativePath(value) || seen[strings.ToLower(value)] {
			return false
		}
		seen[strings.ToLower(value)] = true
	}
	return true
}
func uniqueNames(values []string) bool {
	if len(values) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if !identifier.MatchString(value) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
func hasPlaceholder(data []byte) bool {
	// Called only after strict parsing; inspect decoded string values rather than
	// rejecting angle brackets that legitimately occur in prose or argv.
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return true
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch typed := v.(type) {
		case string:
			return strings.Contains(typed, "<") && strings.Contains(typed, ">")
		case []any:
			for _, child := range typed {
				if walk(child) {
					return true
				}
			}
		case map[string]any:
			for _, child := range typed {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	return walk(value)
}
