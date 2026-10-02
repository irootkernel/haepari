//go:build !darwin

package platform

import (
	"errors"
	"golang.org/x/sys/unix"
)

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
