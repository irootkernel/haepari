//go:build !darwin

package platform

import "errors"

func PersistentSocketIdentityAt(int, string) (FileIdentity, error) {
	return FileIdentity{}, errors.New("native socket identity unavailable")
}
