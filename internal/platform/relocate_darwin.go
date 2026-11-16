//go:build darwin

package platform

import "golang.org/x/sys/unix"

func RenameExclusive(sourceFD int, source string, targetFD int, target string) error {
	return unix.RenameatxNp(sourceFD, source, targetFD, target, unix.RENAME_EXCL)
}
