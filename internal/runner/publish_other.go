//go:build !darwin

package runner

import "errors"

func publishExclusive(int, string, string) error {
	return errors.New("native runner receipt publication requires Darwin")
}
