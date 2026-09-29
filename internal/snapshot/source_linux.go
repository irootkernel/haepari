//go:build linux

package snapshot

import "golang.org/x/sys/unix"

func statTimes(s *unix.Stat_t) (timestamp, timestamp) {
	return timestamp{s.Mtim.Sec, s.Mtim.Nsec}, timestamp{s.Ctim.Sec, s.Ctim.Nsec}
}
