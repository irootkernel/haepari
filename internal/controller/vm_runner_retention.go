package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/irootkernel/haepari/internal/managed"
	"golang.org/x/sys/unix"
)

// The immutable VM plan/attestation names payloads eligible for the same
// finished, evidence-resolved purge transaction as artifacts. Unknown runner
// leaves refuse. Unpublished temporaries stay outside the deletion inventory.
func (s *runtimeServer) planVMRunnerRetention(ctx context.Context, fd int, b managed.BulkSnapshot) (map[string]bool, error) {
	allowed := map[string]bool{}
	purgeDeadline, ok := ctx.Deadline()
	if !ok {
		return nil, managed.ErrBlocked
	}
	spec, err := s.ledger.ResourceStatus(b.Receipt.ResourceID)
	if err != nil {
		return nil, err
	}
	if spec.Kind != "utm-macos" {
		return allowed, nil
	}
	guest, err := s.vmBinding(b.Receipt.JobID)
	if err != nil {
		return nil, err
	}
	digests := map[string]string{"guest-runner": guest.RunnerDigest}
	if q, found, e := s.vmAttestation(b.Receipt.JobID); e != nil {
		return nil, e
	} else if found {
		digests["guest-recovery-runner-"+q.RecoveryDigest[7:]] = q.RecoveryDigest
	}
	duplicate, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(duplicate), "runner-inventory")
	entries, err := directory.ReadDir(16001)
	directory.Close()
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > 16000 {
		return nil, managed.ErrBlocked
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != "guest-runner" && !strings.HasPrefix(name, "guest-recovery-runner-") {
			continue
		}
		digest, ok := digests[name]
		if !ok {
			return nil, managed.ErrBlocked
		}
		runnerFD, e := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if e != nil {
			return nil, e
		}
		file := os.NewFile(uintptr(runnerFD), name)
		before, e := privateLogStat(runnerFD, false)
		if e != nil || before.Size < 1 || before.Size > 64<<20 {
			file.Close()
			return nil, managed.ErrBlocked
		}
		h := sha256.New()
		n, e := copyUntil(h, io.LimitReader(file, (64<<20)+1), purgeDeadline)
		after, statErr := privateLogStat(runnerFD, false)
		file.Close()
		var path unix.Stat_t
		if e != nil || statErr != nil || n != before.Size || before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mtim != after.Mtim || unix.Fstatat(fd, name, &path, unix.AT_SYMLINK_NOFOLLOW) != nil || path.Dev != before.Dev || path.Ino != before.Ino || "sha256:"+hex.EncodeToString(h.Sum(nil)) != digest {
			return nil, managed.ErrBlocked
		}
		allowed[name] = true
	}
	return allowed, nil
}
