package utm

import (
	"time"

	"github.com/irootkernel/haepari/internal/managed"
)

// SSHExchange measures application bytes handed to the actual SSHRunner seam.
// A start without its matching completion is unavailable measurement, never a
// zero-byte successful response. Packet overhead requires TASK-038 capture.
type SSHExchange struct {
	Schema          string                `json:"schema"`
	ID              string                `json:"id"`
	Identity        managed.LocalIdentity `json:"identity"`
	VMUUID          string                `json:"vm_uuid"`
	Operation       string                `json:"operation"`
	RequestDigest   string                `json:"request_digest"`
	RunnerDigest    string                `json:"runner_digest"`
	Deadline        time.Time             `json:"deadline"`
	StartedAt       time.Time             `json:"started_at"`
	Complete        bool                  `json:"complete"`
	ArchiveBytes    int                   `json:"archive_bytes"`
	RunnerBytes     int                   `json:"runner_bytes"`
	StdoutBytes     int64                 `json:"stdout_bytes"`
	StderrBytes     int64                 `json:"stderr_bytes"`
	DurationMillis  int64                 `json:"duration_ms"`
	ExitCode        int                   `json:"exit_code"`
	TransportFailed bool                  `json:"transport_failed"`
}
