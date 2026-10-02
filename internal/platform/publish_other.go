//go:build !darwin

package platform

import "errors"

func PublishExclusive(int, string, string) error {
	return errors.New("native receipt publication requires Darwin")
}
