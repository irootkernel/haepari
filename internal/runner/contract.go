// Package runner supervises one exact run. It neither admits Jobs nor owns
// managed capacity. Receipts, rather than the supervisor's exit code, are its
// interface to the controller.
package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
)

var (
	ErrInvalid   = errors.New("invalid contained runner request or receipt")
	ErrConflict  = errors.New("runner request conflicts with its durable claim")
	ErrAmbiguous = errors.New("spawn intent exists without a completed exit receipt; do not start again")
	ErrUnproven  = errors.New("owned workload termination is unproven")
)

type Request struct {
	Schema             string                `json:"schema"`
	Identity           managed.LocalIdentity `json:"identity"`
	BootID             string                `json:"boot_id"`
	WorkDir            string                `json:"work_dir"`
	Argv               []string              `json:"argv"`
	Environment        []string              `json:"environment"`
	Deadline           time.Time             `json:"deadline"`
	RunSeconds         int64                 `json:"run_seconds"`
	TerminationSeconds int64                 `json:"termination_seconds"`
	LogDevice          uint64                `json:"log_device"`
	LogInode           uint64                `json:"log_inode"`
}

type Binding struct {
	Identity      managed.LocalIdentity `json:"identity"`
	RequestDigest string                `json:"request_digest"`
}

type Intent struct {
	Schema string `json:"schema"`
	Binding
	Supervisor platform.ProcessIdentity `json:"supervisor"`
	RecordedAt time.Time                `json:"recorded_at"`
}
type Start struct {
	Schema string `json:"schema"`
	Binding
	Process   platform.ProcessIdentity `json:"process"`
	StartedAt time.Time                `json:"started_at"`
}
type Outcome struct {
	Schema string `json:"schema"`
	Binding
	Outcome    managed.LocalOutcome `json:"outcome"`
	ObservedAt time.Time            `json:"observed_at"`
}
type Exit struct {
	Schema string `json:"schema"`
	Binding
	Outcome           managed.LocalOutcome `json:"outcome"`
	TerminationProven bool                 `json:"termination_proven"`
	Reason            string               `json:"reason,omitempty"`
	FinishedAt        time.Time            `json:"finished_at"`
}
type Cancellation struct {
	Schema string `json:"schema"`
	Binding
	RequestedAt time.Time `json:"requested_at"`
}
type Observation struct {
	State   string   `json:"state"`
	Intent  *Intent  `json:"intent,omitempty"`
	Start   *Start   `json:"start,omitempty"`
	Outcome *Outcome `json:"outcome,omitempty"`
	Exit    *Exit    `json:"exit,omitempty"`
}

func (q Request) Validate(root string) error {
	id := q.Identity
	if q.Schema != "haepari.runner-request/v1" || !managed.ValidRequestKey(id.JobID) || !managed.ValidRequestKey(id.ResourceID) || !managed.ValidRequestKey(id.RunID) || id.ConfigRevision <= 0 || id.LeaseGeneration <= 0 || id.Slot < 0 || id.Nonce == "" || q.BootID == "" || q.Deadline.IsZero() || q.RunSeconds <= 0 || q.RunSeconds > (1<<63-1)/int64(time.Second) || q.TerminationSeconds <= 0 || q.TerminationSeconds > (1<<63-1)/int64(time.Second) || q.LogInode == 0 {
		return ErrInvalid
	}
	workspace := filepath.Join(root, "workspace")
	if !filepath.IsAbs(q.WorkDir) || (q.WorkDir != workspace && !strings.HasPrefix(q.WorkDir, workspace+string(filepath.Separator))) || filepath.Clean(q.WorkDir) != q.WorkDir || len(q.Argv) == 0 || len(q.Argv) > 4096 {
		return ErrInvalid
	}
	for _, arg := range q.Argv {
		if strings.ContainsRune(arg, 0) {
			return ErrInvalid
		}
	}
	if q.Argv[0] == "" {
		return ErrInvalid
	}
	seen := make(map[string]bool)
	for _, variable := range q.Environment {
		key, _, ok := strings.Cut(variable, "=")
		if !ok || key == "" || seen[key] || strings.ContainsRune(variable, 0) {
			return ErrInvalid
		}
		seen[key] = true
	}
	return nil
}

func (q Request) Binding() Binding {
	data, _ := json.Marshal(q)
	sum := sha256.Sum256(data)
	return Binding{q.Identity, "sha256:" + hex.EncodeToString(sum[:])}
}

func validOutcome(out managed.LocalOutcome) bool {
	switch out.WorkloadOutcome {
	case "pass", "fail", "cancelled", "timed_out", "inconclusive":
	default:
		return false
	}
	if out.NativeExitCode != nil && (*out.NativeExitCode < 0 || *out.NativeExitCode > 255 || out.NativeSignal != "") {
		return false
	}
	if out.WorkloadOutcome == "pass" && (!out.Started || out.NativeExitCode == nil || *out.NativeExitCode != 0) {
		return false
	}
	if out.WorkloadOutcome == "fail" && (!out.Started || (out.NativeSignal == "" && (out.NativeExitCode == nil || *out.NativeExitCode == 0))) {
		return false
	}
	return out.Started || (out.NativeExitCode == nil && out.NativeSignal == "" && (out.WorkloadOutcome == "inconclusive" || out.WorkloadOutcome == "cancelled"))
}
