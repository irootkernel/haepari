package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var ErrControllerOwned = errors.New("state root is owned by another controller")

// AcquireController holds the kernel lock for the controller's whole lifetime.
// The lock file stays in place: unlinking it would allow two different inodes
// to be locked by different processes at the same path.
func (r Root) AcquireController() (*os.File, error) {
	path := filepath.Join(r.Path, "controller.lock")
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EISDIR) || errors.Is(err, syscall.ENOTDIR) {
			return nil, fmt.Errorf("%w: %s is not a private owner file: %v", ErrInvalidRoot, path, err)
		}
		return nil, fmt.Errorf("open controller lock %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	fail := func(err error) (*os.File, error) { _ = file.Close(); return nil, err }
	info, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return fail(fmt.Errorf("%w: %s is not a private owner file", ErrInvalidRoot, path))
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return fail(fmt.Errorf("%w: %s; use the running controller or choose another HAEPARI_HOME", ErrControllerOwned, path))
		}
		return fail(fmt.Errorf("lock controller state: %w", err))
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return fail(fmt.Errorf("%w: %s changed during acquisition", ErrInvalidRoot, path))
	}
	return file, nil
}
