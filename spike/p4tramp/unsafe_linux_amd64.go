//go:build linux && amd64

package p4tramp

import (
	"unsafe"
)

// memAddr 返回 []byte 底层数组首地址(uintptr)。
//
// **unsafe 范围**:仅用于把 mmap 出来的段地址转为 uintptr 给 callJIT。Go GC
// 不会移动 mmap 段(它不是 Go 堆对象,unix.Mmap 返回的 []byte 经 syscall 暴露
// 给 Go,但底层是 anonymous mmap,Go GC 不感知);因此 uintptr 稳定。
//
// 本 spike 之外的主库代码(internal/gibbous/jit)同款形态会用 codePagePool,
// 同源依赖此性质。
func memAddr(mem []byte) uintptr {
	return uintptr(unsafe.Pointer(&mem[0]))
}
