//go:build !darwin

package platform

import "fmt"

func LocalFilesystem(path string) (bool, error) {
	return false, fmt.Errorf("local filesystem check is unavailable on this platform")
}
