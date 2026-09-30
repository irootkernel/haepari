//go:build !darwin

package platform

import (
	"fmt"
	"time"
)

func AdvisoryLockClock() (time.Time, string, error) {
	return time.Time{}, "", fmt.Errorf("advisory wake observation requires macOS")
}
