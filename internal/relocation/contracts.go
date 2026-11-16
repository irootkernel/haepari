// Package relocation moves one stopped installation without rewriting history.
// It has no service-start, workload, provider, consumer or disposal authority.
package relocation

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"strings"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"github.com/irootkernel/haepari/internal/store/sqlite"
)

const (
	PlanSchema    = "haepari.service-relocation-plan/v1"
	JournalSchema = "haepari.service-relocation-journal/v1"
	ReceiptSchema = "haepari.service-relocation-receipt/v1"
	JournalName   = ".service-relocation.json"
	HistoryName   = ".service-relocation-history"
	OwnerName     = ".development-service.json"
)

// ServicePlan retains the original service JSON field order and omission rules.
type ServicePlan struct {
	GlobalLimit         int    `json:"global_limit,omitempty"`
	HostMACOSGuestLimit int    `json:"host_macos_guest_limit,omitempty"`
	Schema              string `json:"schema"`
	Instance            string `json:"instance"`
	Label               string `json:"label"`
	Domain              string `json:"domain"`
	OwnerUID            int    `json:"owner_uid"`
	Binary              string `json:"binary"`
	BinarySHA256        string `json:"binary_sha256"`
	Root                string `json:"root"`
	PlistPath           string `json:"plist_path"`
	Plist               string `json:"plist"`
}
type Directory struct {
	Identity platform.FileIdentity `json:"identity"`
	OwnerUID uint32                `json:"owner_uid"`
	Mode     uint32                `json:"mode"`
}
type File struct {
	Path     string                `json:"path"`
	Kind     string                `json:"kind"`
	Identity platform.FileIdentity `json:"identity"`
	OwnerUID uint32                `json:"owner_uid"`
	Mode     uint32                `json:"mode"`
	Size     *int64                `json:"size,omitempty"`
	SHA256   string                `json:"sha256,omitempty"`
}
type Predecessor struct {
	RelocationID    string                `json:"relocation_id"`
	PlanSHA256      string                `json:"plan_sha256"`
	Sequence        int64                 `json:"sequence"`
	JournalSHA256   string                `json:"journal_sha256"`
	JournalIdentity platform.FileIdentity `json:"journal_identity"`
	CurrentRoot     string                `json:"current_root"`
}
type Plan struct {
	Schema               string       `json:"schema"`
	RelocationID         string       `json:"relocation_id"`
	InstallationID       string       `json:"installation_id"`
	SourcePlan           ServicePlan  `json:"source_plan"`
	TargetPlan           ServicePlan  `json:"target_plan"`
	SourceRootIdentity   Directory    `json:"source_root_identity"`
	SourceParentIdentity Directory    `json:"source_parent_identity"`
	TargetParentIdentity Directory    `json:"target_parent_identity"`
	BackupParentIdentity Directory    `json:"backup_parent_identity"`
	BackupDir            string       `json:"backup_dir"`
	Files                []File       `json:"files"`
	LedgerSHA256         string       `json:"ledger_sha256"`
	Predecessor          *Predecessor `json:"predecessor,omitempty"`
	PlanSHA256           string       `json:"plan_sha256,omitempty"`
}
type Receipt struct {
	Schema               string `json:"schema"`
	RelocationID         string `json:"relocation_id"`
	PlanSHA256           string `json:"plan_sha256"`
	InstallationID       string `json:"installation_id"`
	Phase                string `json:"phase"`
	Sequence             int64  `json:"sequence"`
	SourceRoot           string `json:"source_root"`
	TargetRoot           string `json:"target_root"`
	CurrentRoot          string `json:"current_root"`
	BackupState          string `json:"backup_state"`
	BackupManifestSHA256 string `json:"backup_manifest_sha256,omitempty"`
}
type Archive struct {
	Path              string                `json:"path"`
	Identity          platform.FileIdentity `json:"identity"`
	SHA256            string                `json:"sha256"`
	DirectoryIdentity platform.FileIdentity `json:"directory_identity"`
	OwnerUID          uint32                `json:"owner_uid"`
	Mode              uint32                `json:"mode"`
}
type Journal struct {
	Schema               string     `json:"schema"`
	Plan                 Plan       `json:"plan"`
	PlanSHA256           string     `json:"plan_sha256"`
	Sequence             int64      `json:"sequence"`
	Phase                string     `json:"phase"`
	Activated            bool       `json:"activated"`
	BackupState          string     `json:"backup_state"`
	OriginalOwner        []byte     `json:"original_owner"`
	TargetOwner          []byte     `json:"target_owner"`
	PredecessorArchive   *Archive   `json:"predecessor_archive,omitempty"`
	BackupIdentity       *Directory `json:"backup_identity,omitempty"`
	BackupManifestSHA256 string     `json:"backup_manifest_sha256,omitempty"`
	CompletionReceipt    *Receipt   `json:"completion_receipt,omitempty"`
	RollbackReceipt      *Receipt   `json:"rollback_receipt,omitempty"`
}
type Environment struct {
	ValidateService func(ServicePlan) error
	ServicesStopped func(ServicePlan) error
	MoverBinary     string
	// Boundary is nil in production. Subprocess fixtures interrupt actual effects.
	Boundary func(string)
	// ObservationOpened is nil in production; fault fixtures retain creator-owned
	// snapshot handles so deliberate process exits need not leak disposable copies.
	DirectorySync     func(string) error // nil in production; fault injection before real descriptor Sync.
	ObservationOpened func(*sqlite.Observation)
}

func (e Environment) boundary(s string) {
	if e.Boundary != nil {
		e.Boundary(s)
	}
}

var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func Digest(raw []byte) string { s := sha256.Sum256(raw); return "sha256:" + hex.EncodeToString(s[:]) }
func canonical(v any) []byte   { raw, _ := json.Marshal(v); return raw }
func planDigest(p Plan) string { p.PlanSHA256 = ""; return Digest(canonical(p)) }
func newID() (string, error) {
	var b [16]byte
	_, e := rand.Read(b[:])
	return hex.EncodeToString(b[:]), e
}
func Decode(raw []byte, v any) error {
	if requiredJSON(raw, reflect.TypeOf(v).Elem()) != nil || managed.ValidateUniqueJSONKeys(raw) != nil {
		return managed.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return managed.ErrInvalid
	}
	return nil
}
func DecodePlan(raw []byte) (Plan, error) {
	var p Plan
	if Decode(raw, &p) != nil || p.Schema != PlanSchema || !idPattern.MatchString(p.RelocationID) || !digestPattern.MatchString(p.PlanSHA256) || planDigest(p) != p.PlanSHA256 || !digestPattern.MatchString(p.LedgerSHA256) || p.InstallationID == "" || p.Files == nil {
		return Plan{}, managed.ErrInvalid
	}
	if validatePairShape(p) != nil || validateFilePlan(p) != nil {
		return Plan{}, managed.ErrInvalid
	}
	return p, nil
}
func terminal(j Journal) bool { return j.Phase == "complete" || j.Phase == "rolled_back" }
func receipt(j Journal, root, backup string) Receipt {
	return Receipt{ReceiptSchema, j.Plan.RelocationID, j.PlanSHA256, j.Plan.InstallationID, j.Phase, j.Sequence, j.Plan.SourcePlan.Root, j.Plan.TargetPlan.Root, root, backup, j.BackupManifestSHA256}
}
func fail(stage string, err error) error {
	var known *Error
	if errors.As(err, &known) {
		return err
	}
	// Fixed stages are the only public detail; raw backend/path content stays out.
	kind := managed.ErrConflict
	switch {
	case errors.Is(err, managed.ErrInvalid), errors.Is(err, managed.ErrUnsupported):
		kind = managed.ErrInvalid
	case errors.Is(err, managed.ErrBlocked), errors.Is(err, managed.ErrBusy):
		kind = managed.ErrBlocked
	case !errors.Is(err, managed.ErrConflict):
		return &Error{Stage: stage, Cause: err}
	}
	return &Error{Stage: stage, Cause: kind}
}

type Error struct {
	Stage string
	Cause error
}

func (e *Error) Error() string { return "relocation refused at " + e.Stage }
func (e *Error) Unwrap() error { return e.Cause }

// Required fields are checked independently of Go zero values. Null is never
// an alternate spelling of absent optional data or false/zero required data.
func requiredJSON(raw []byte, t reflect.Type) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return managed.ErrInvalid
	}
	if t.Kind() == reflect.Pointer {
		return requiredJSON(raw, t.Elem())
	}
	switch t.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return managed.ErrInvalid
		}
		known := map[string]bool{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "" {
				name = f.Name
			}
			if name != "-" {
				known[name] = true
			}
		}
		for name := range fields {
			if !known[name] {
				return managed.ErrInvalid
			}
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")
			name := tag[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			value, ok := fields[name]
			optional := false
			for _, s := range tag[1:] {
				optional = optional || s == "omitempty" || s == "omitzero"
			}
			if !ok {
				if !optional {
					return managed.ErrInvalid
				}
				continue
			}
			if err := requiredJSON(value, f.Type); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return managed.ErrInvalid
		}
		for _, value := range values {
			if err := requiredJSON(value, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateJournal(j Journal) error {
	if _, err := DecodePlan(canonical(j.Plan)); err != nil {
		return managed.ErrConflict
	}
	if j.PlanSHA256 != j.Plan.PlanSHA256 || j.Sequence < 1 || j.BackupIdentity == nil && (j.BackupState != "not_created" || j.BackupManifestSHA256 != "") || j.BackupIdentity != nil && j.BackupState == "not_created" {
		return managed.ErrConflict
	}
	if (j.Plan.Predecessor == nil) != (j.PredecessorArchive == nil) {
		return managed.ErrConflict
	}
	if j.PredecessorArchive != nil {
		a := j.PredecessorArchive
		if a.Path != HistoryName+"/"+archiveName(*j.Plan.Predecessor) || !a.Identity.Valid() || !a.DirectoryIdentity.Valid() || !digestPattern.MatchString(a.SHA256) || a.SHA256 != j.Plan.Predecessor.JournalSHA256 || a.OwnerUID != uint32(j.Plan.SourcePlan.OwnerUID) || a.Mode&0077 != 0 {
			return managed.ErrConflict
		}
	}
	if j.BackupIdentity != nil {
		b := j.BackupIdentity
		if !b.Identity.Valid() || b.Identity.VolumeUUID != j.Plan.SourceRootIdentity.Identity.VolumeUUID || b.OwnerUID != uint32(j.Plan.SourcePlan.OwnerUID) || b.Mode&0077 != 0 {
			return managed.ErrConflict
		}
	}
	if (j.Phase == "backup_verified" || j.Phase == "rename_pending" || j.Phase == "moved" || j.Phase == "owner_pending" || j.Phase == "complete") && j.BackupState != "verified" {
		return managed.ErrConflict
	}
	if j.Phase == "complete" && j.CompletionReceipt == nil || j.Phase == "rolled_back" && j.RollbackReceipt == nil {
		return managed.ErrConflict
	}
	if j.CompletionReceipt != nil && j.Phase != "complete" && j.Phase != "rollback_pending" && j.Phase != "rolled_back" {
		return managed.ErrConflict
	}
	var originalHash string
	for _, f := range j.Plan.Files {
		if f.Path == OwnerName {
			originalHash = f.SHA256
		}
	}
	if originalHash == "" || Digest(j.OriginalOwner) != originalHash {
		return managed.ErrConflict
	}
	var owner struct {
		Schema   string `json:"schema"`
		Instance string `json:"instance"`
		OwnerUID int    `json:"owner_uid"`
		Root     string `json:"root"`
	}
	if Decode(j.OriginalOwner, &owner) != nil || owner.Schema != "haepari.release-service-owner/v1" || owner.Instance != j.Plan.SourcePlan.Instance || owner.OwnerUID != j.Plan.SourcePlan.OwnerUID || owner.Root != j.Plan.SourcePlan.Root {
		return managed.ErrConflict
	}
	owner.Root = j.Plan.TargetPlan.Root
	if !bytes.Equal(canonical(owner), j.TargetOwner) {
		return managed.ErrConflict
	}
	checkReceipt := func(r *Receipt, phase, root string) error {
		if r == nil {
			return nil
		}
		if r.Schema != ReceiptSchema || r.RelocationID != j.Plan.RelocationID || r.PlanSHA256 != j.PlanSHA256 || r.InstallationID != j.Plan.InstallationID || r.Phase != phase || r.CurrentRoot != root || r.SourceRoot != j.Plan.SourcePlan.Root || r.TargetRoot != j.Plan.TargetPlan.Root || r.Sequence < 1 || r.Sequence > j.Sequence || r.BackupManifestSHA256 != j.BackupManifestSHA256 {
			return managed.ErrConflict
		}
		if r.BackupState != "not_created" && r.BackupState != "unproven" && r.BackupState != "bound" && r.BackupState != "verified" {
			return managed.ErrConflict
		}
		return nil
	}
	if err := checkReceipt(j.CompletionReceipt, "complete", j.Plan.TargetPlan.Root); err != nil {
		return err
	}
	if err := checkReceipt(j.RollbackReceipt, "rolled_back", j.Plan.SourcePlan.Root); err != nil {
		return err
	}
	if j.CompletionReceipt != nil && (j.BackupState != "verified" || j.CompletionReceipt.BackupState != "verified") {
		return managed.ErrConflict
	}
	if j.RollbackReceipt != nil && j.Phase != "rolled_back" {
		return managed.ErrConflict
	}
	if j.Activated && j.Phase != "complete" && j.Phase != "rollback_pending" && j.Phase != "rolled_back" {
		return managed.ErrConflict
	}
	return nil
}
