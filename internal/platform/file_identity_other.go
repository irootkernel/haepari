//go:build !darwin

package platform

import "fmt"

func PersistentFileIdentity(fd int) (FileIdentity, error) {
	return FileIdentity{}, fmt.Errorf("persistent descriptor identity unsupported on this platform")
}
