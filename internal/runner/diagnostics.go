package runner

import (
	"context"
	"errors"
	"os"

	"github.com/irootkernel/haepari/internal/managed"
	"golang.org/x/sys/unix"
)

// DiagnosticCause deliberately excludes paths, argv, environment and remote
// output. These bounded codes describe failures, never termination evidence.
func DiagnosticCause(err error) string {
	var refusal *diagnosticError
	if errors.As(err, &refusal) {
		return refusal.cause
	}
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return managed.DiagnosticDeadlineExceeded
	case errors.Is(err, ErrConflict) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR):
		return managed.DiagnosticIdentityConflict
	case errors.Is(err, ErrUnproven):
		return managed.DiagnosticTerminationUnproven
	case errors.Is(err, ErrAmbiguous):
		return managed.DiagnosticReceiptAmbiguous
	case errors.Is(err, ErrInvalid):
		return managed.DiagnosticInvalidReceipt
	case errors.Is(err, os.ErrPermission):
		return managed.DiagnosticPermissionDenied
	case errors.Is(err, os.ErrNotExist):
		return managed.DiagnosticPathAbsent
	default:
		return managed.DiagnosticObservationFailed
	}
}

type diagnosticError struct {
	cause string
	err   error
}

func (e *diagnosticError) Error() string { return e.cause }
func (e *diagnosticError) Unwrap() error { return e.err }

// DiagnosticError restores the identity of a native runner classification from
// its structured refusal. Unknown codes remain untrusted transport failures.
func DiagnosticError(cause string) error {
	if !managed.ValidDiagnosticCause(cause) {
		return nil
	}
	var err error
	switch cause {
	case managed.DiagnosticDeadlineExceeded:
		err = context.DeadlineExceeded
	case managed.DiagnosticIdentityConflict:
		err = ErrConflict
	case managed.DiagnosticTerminationUnproven:
		err = ErrUnproven
	case managed.DiagnosticReceiptAmbiguous:
		err = ErrAmbiguous
	case managed.DiagnosticInvalidReceipt:
		err = ErrInvalid
	case managed.DiagnosticPermissionDenied:
		err = os.ErrPermission
	case managed.DiagnosticPathAbsent:
		err = os.ErrNotExist
	case managed.DiagnosticStaleRevision:
		err = managed.ErrStale
	}
	return &diagnosticError{cause: cause, err: err}
}
