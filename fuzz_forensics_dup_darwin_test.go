//go:build darwin

package wangshu_test

import "syscall"

func dupFD(oldfd, newfd int) error {
	return syscall.Dup2(oldfd, newfd)
}
