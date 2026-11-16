//go:build !darwin

package platform

import "errors"

func RenameExclusive(int, string, int, string) error {
	return errors.New("relocation requires native exclusive rename")
}
