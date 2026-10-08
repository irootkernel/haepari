package platform

import "fmt"

// ProcessError preserves the native observation stage without embedding
// process arguments, environment or paths in durable runner diagnostics.
type ProcessError struct {
	Operation string
	PID       int
	Err       error
}

func (e *ProcessError) Error() string {
	return fmt.Sprintf("%s (pid %d): %v", e.Operation, e.PID, e.Err)
}
func (e *ProcessError) Unwrap() error { return e.Err }

// ProcessIdentity binds a native observation to one process incarnation.
// A PID or a kernel session pointer alone is not an ownership identity.
type ProcessIdentity struct {
	BootID         string `json:"boot_id"`
	PID            int    `json:"pid"`
	StartedSeconds int64  `json:"started_seconds"`
	StartedMicros  int64  `json:"started_micros"`
	PGID           int    `json:"pgid"`
	SID            int    `json:"sid"`
	UID            uint32 `json:"uid"`
}

type ProcessObservation struct {
	Identity  ProcessIdentity
	ParentPID int
	Zombie    bool
}
