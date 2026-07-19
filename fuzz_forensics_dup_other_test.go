//go:build !linux && !darwin

package wangshu_test

import "errors"

// dupFD is unsupported off linux/darwin: forensics silently disables
// itself (stderr stays on /dev/null, exactly the pre-forensics state).
func dupFD(oldfd, newfd int) error {
	return errors.New("fd redirection unsupported on this platform")
}
