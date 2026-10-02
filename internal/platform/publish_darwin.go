//go:build darwin

package platform

import "golang.org/x/sys/unix"

// PublishExclusive renames within an owned directory without replacing a claim.
func PublishExclusive(fd int, temporary, name string) error {
	return unix.RenameatxNp(fd, temporary, fd, name, unix.RENAME_EXCL)
}
