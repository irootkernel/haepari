//go:build darwin

package platform

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

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
		return ProcessObservation{}, &ProcessError{"boot_identity", pid, err}
	}
	before, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.EIO) && errors.Is(unix.Kill(pid, 0), unix.ESRCH) {
			return ProcessObservation{}, unix.ESRCH
		}
		return ProcessObservation{}, &ProcessError{"process_snapshot_before", pid, err}
	}
	pgid, err := unix.Getpgid(pid)
	if err != nil {
		return ProcessObservation{}, &ProcessError{"getpgid", pid, err}
	}
	sid, err := unix.Getsid(pid)
	if err != nil {
		return ProcessObservation{}, &ProcessError{"getsid", pid, err}
	}
	after, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.EIO) && errors.Is(unix.Kill(pid, 0), unix.ESRCH) {
			return ProcessObservation{}, unix.ESRCH
		}
		return ProcessObservation{}, &ProcessError{"process_snapshot_after", pid, err}
	}
	if before.Proc.P_pid != int32(pid) || after.Proc.P_pid != int32(pid) || before.Proc.P_starttime != after.Proc.P_starttime || int(after.Eproc.Pgid) != pgid {
		return ProcessObservation{}, &ProcessError{"process_incarnation", pid, errors.New("process incarnation changed during observation")}
	}
	return ProcessObservation{Identity: ProcessIdentity{boot, pid, after.Proc.P_starttime.Sec, int64(after.Proc.P_starttime.Usec), pgid, sid, after.Eproc.Ucred.Uid}, ParentPID: int(after.Eproc.Ppid), Zombie: after.Proc.P_stat == 5}, nil
}

// Census is best-effort native descendant discovery, not a sandbox. Once a
// descendant is observed, callers retain its incarnation across reparenting.
// Any incomplete census is uncertainty, not evidence of termination.
func ProcessCensus() ([]ProcessObservation, error) {
	list, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Geteuid())
	if err != nil {
		return nil, &ProcessError{"process_list", 0, err}
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
		return &ProcessError{"signal_identity", expected.PID, errors.New("process identity no longer matches")}
	}
	if err := unix.Kill(expected.PID, signal); err != nil {
		return &ProcessError{"signal", expected.PID, err}
	}
	return nil
}
