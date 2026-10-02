package platform

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
