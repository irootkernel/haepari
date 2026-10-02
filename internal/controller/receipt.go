package controller

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"golang.org/x/sys/unix"
)

// Publish an immutable receipt through its verified directory descriptor.
// Exclusive rename refuses replacement and an exact replay verifies bytes.
func writeReceipt(root, name string, value any) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Base(name) != name || name == "." || name == ".." {
		return managed.ErrInvalid
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 2<<20 {
		return managed.ErrInvalid
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	for _, part := range strings.Split(strings.TrimPrefix(root, "/"), "/") {
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return err
		}
		fd = next
	}
	defer unix.Close(fd)
	if _, err = privateLogStat(fd, true); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	temporary := ".receipt-" + hex.EncodeToString(nonce[:])
	tmpFD, err := unix.Openat(fd, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(fd, temporary, 0)
	f := os.NewFile(uintptr(tmpFD), temporary)
	if _, err = f.Write(encoded); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	err = platform.PublishExclusive(fd, temporary, name)
	if errors.Is(err, unix.EEXIST) {
		existing, openErr := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return openErr
		}
		old := os.NewFile(uintptr(existing), name)
		defer old.Close()
		if _, err = privateLogStat(existing, false); err != nil {
			return err
		}
		raw, readErr := io.ReadAll(io.LimitReader(old, int64(len(encoded))+1))
		if readErr != nil || !bytes.Equal(raw, encoded) {
			return managed.ErrConflict
		}
	} else if err != nil {
		return err
	}
	return unix.Fsync(fd)
}
