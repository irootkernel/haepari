package runner

import (
	"errors"
	"time"

	"github.com/irootkernel/haepari/internal/platform"
	"golang.org/x/sys/unix"
)

// This separate immutable artifact is diagnostic context, never an execution
// receipt or termination proof. Keeping it outside Exit preserves the frozen
// closed-shape receipt and guest protocol, including historical decoding.
type nativeDiagnostic struct {
	Schema string `json:"schema"`
	Binding
	BootID          string    `json:"boot_id"`
	Phase           string    `json:"phase"`
	Operation       string    `json:"operation"`
	NativeOperation string    `json:"native_operation,omitempty"`
	PID             int       `json:"pid,omitempty"`
	ErrorCode       string    `json:"error_code"`
	Errno           int       `json:"errno,omitempty"`
	ObservedAt      time.Time `json:"observed_at"`
}

func recordNativeDiagnostic(d *directory, q Request, operation string, err error) error {
	v := nativeDiagnostic{Schema: "haepari.runner-native-diagnostic/v1", Binding: q.Binding(), BootID: q.BootID, Phase: "run", Operation: operation, ErrorCode: "observation_failed", ObservedAt: time.Now().UTC()}
	// Only fixed categories and native errno are retained. Arbitrary error
	// messages can contain workload data or secrets and are never serialized.
	var native *platform.ProcessError
	if errors.As(err, &native) {
		switch native.Operation {
		case "boot_identity", "process_snapshot_before", "getpgid", "getsid", "process_snapshot_after", "process_incarnation", "process_list", "signal_identity", "signal":
			v.NativeOperation = native.Operation
			if native.PID > 0 {
				v.PID = native.PID
			}
		}
		if v.NativeOperation == "process_incarnation" || v.NativeOperation == "signal_identity" {
			v.ErrorCode = "identity_changed"
		}
	}
	var errno unix.Errno
	switch {
	case errors.As(err, &errno):
		v.ErrorCode, v.Errno = "native_errno", int(errno)
	case errors.Is(err, ErrConflict):
		v.ErrorCode = "conflict"
	case errors.Is(err, ErrInvalid):
		v.ErrorCode = "invalid"
	case errors.Is(err, ErrUnproven):
		v.ErrorCode = "unproven"
	}
	return d.write("native-diagnostic.json", v)
}
