//go:build darwin

package runner

import "golang.org/x/sys/unix"

func publishExclusive(fd int, temporary, name string) error {
	return unix.RenameatxNp(fd, temporary, fd, name, unix.RENAME_EXCL)
}
