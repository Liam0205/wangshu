//go:build darwin

package fuzzforensics

import "syscall"

func dupFD(oldfd, newfd int) error {
	return syscall.Dup2(oldfd, newfd)
}
