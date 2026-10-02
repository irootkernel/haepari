//go:build !darwin

package platform

import (
	"errors"
	"golang.org/x/sys/unix"
)

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

func BootIdentity() (string, error) { return "", errors.New("native runner requires Darwin") }
func ObserveProcess(int) (ProcessObservation, error) {
	return ProcessObservation{}, errors.New("native runner requires Darwin")
}
func ProcessCensus() ([]ProcessObservation, error) {
	return nil, errors.New("native runner requires Darwin")
}
func SignalProcess(ProcessIdentity, unix.Signal) error {
	return errors.New("native runner requires Darwin")
}
