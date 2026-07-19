//go:build linux

package wangshu_test

import "syscall"

// dupFD aliases newfd to oldfd's open file. Dup3, not Dup2: linux/arm64
// (a nightly CI arch) has no dup2 syscall in the Go syscall package.
func dupFD(oldfd, newfd int) error {
	return syscall.Dup3(oldfd, newfd, 0)
}
