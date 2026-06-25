//go:build !wangshu_p4

package jit

// P4HostState 默认 build 占位接口。
type P4HostState interface {
	DoReturn(base int32, pc int32, a int32, b int32) int32
}

// SetP4HostState 默认 build no-op(P4 完全 dead-code)。
func SetP4HostState(h P4HostState) {
	_ = h
}
