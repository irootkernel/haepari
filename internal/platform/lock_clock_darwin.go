//go:build darwin

package platform

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// AdvisoryLockClock samples the kernel's absolute last-wake marker alongside
// UTC. A changed marker lets the ledger extend liveness before applying due
// deadlines. Wall-clock or timer delays alone are not evidence of host sleep.
func AdvisoryLockClock() (time.Time, string, error) {
	for range 3 {
		before, err := unix.SysctlRaw("kern.wake_abs_time")
		if err != nil {
			return time.Time{}, "", fmt.Errorf("read advisory wake marker: %w", err)
		}
		now := time.Now().UTC()
		after, err := unix.SysctlRaw("kern.wake_abs_time")
		if err != nil || len(before) != 8 || len(after) != 8 {
			return time.Time{}, "", fmt.Errorf("invalid advisory wake marker: %v", err)
		}
		if bytes.Equal(before, after) {
			return now, hex.EncodeToString(after), nil
		}
	}
	return time.Time{}, "", fmt.Errorf("advisory wake marker changed during clock observation")
}
