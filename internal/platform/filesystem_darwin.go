//go:build darwin

package platform

import "golang.org/x/sys/unix"

func LocalFilesystem(path string) (bool, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return false, err
	}
	return stat.Flags&unix.MNT_LOCAL != 0, nil
}
