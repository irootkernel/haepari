//go:build darwin

package platform

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

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

func BootIdentity() (string, error) {
	id, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return "", fmt.Errorf("observe boot-session identity: %w", err)
	}
	if strings.TrimSpace(id) == "" {
		return "", errors.New("empty boot-session identity")
	}
	return strings.TrimSpace(id), nil
}

func ObserveProcess(pid int) (ProcessObservation, error) {
	boot, err := BootIdentity()
	if pid <= 0 {
		return ProcessObservation{}, errors.New("invalid process PID")
	}
	if err != nil {
		return ProcessObservation{}, fmt.Errorf("invalid process observation: %w", err)
	}
	before, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.EIO) && errors.Is(unix.Kill(pid, 0), unix.ESRCH) {
			return ProcessObservation{}, unix.ESRCH
		}
		return ProcessObservation{}, err
	}
	pgid, err := unix.Getpgid(pid)
	if err != nil {
		return ProcessObservation{}, err
	}
	sid, err := unix.Getsid(pid)
	if err != nil {
		return ProcessObservation{}, err
	}
	after, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.EIO) && errors.Is(unix.Kill(pid, 0), unix.ESRCH) {
			return ProcessObservation{}, unix.ESRCH
		}
		return ProcessObservation{}, err
	}
	if before.Proc.P_pid != int32(pid) || after.Proc.P_pid != int32(pid) || before.Proc.P_starttime != after.Proc.P_starttime || int(after.Eproc.Pgid) != pgid {
		return ProcessObservation{}, errors.New("process incarnation changed during observation")
	}
	return ProcessObservation{Identity: ProcessIdentity{boot, pid, after.Proc.P_starttime.Sec, int64(after.Proc.P_starttime.Usec), pgid, sid, after.Eproc.Ucred.Uid}, ParentPID: int(after.Eproc.Ppid), Zombie: after.Proc.P_stat == 5}, nil
}

// Census is best-effort native descendant discovery, not a sandbox. Once a
// descendant is observed, callers retain its incarnation across reparenting.
// Any incomplete census is uncertainty, not evidence of termination.
func ProcessCensus() ([]ProcessObservation, error) {
	list, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Geteuid())
	if err != nil {
		return nil, err
	}
	result := make([]ProcessObservation, 0, len(list))
	for _, p := range list {
		if p.Proc.P_pid <= 0 || p.Proc.P_stat == 5 {
			continue
		}
		observed, err := ObserveProcess(int(p.Proc.P_pid))
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, observed)
	}
	return result, nil
}

// SignalProcess re-observes the complete incarnation before every signal.
// This is a cooperative fence; Darwin has no atomic PID-incarnation kill API.
func SignalProcess(expected ProcessIdentity, signal unix.Signal) error {
	current, err := ObserveProcess(expected.PID)
	if err != nil {
		return err
	}
	if current.Identity != expected {
		return errors.New("process identity no longer matches")
	}
	return unix.Kill(expected.PID, signal)
}
