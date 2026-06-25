//go:build !wangshu_p4

package jit

// P4HostState 默认 build 占位接口。
type P4HostState interface {
	DoReturn(base int32, pc int32, a int32, b int32) int32
	SetReg(idx int32, val uint64)
}
