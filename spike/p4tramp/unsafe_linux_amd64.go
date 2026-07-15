//go:build linux && amd64

package p4tramp

import (
	"unsafe"
)

// memAddr returns the address of the []byte's underlying array (uintptr).
//
// **unsafe scope**: used only to convert an mmap'd segment address into a uintptr
// for callJIT. The Go GC does not move mmap segments (they are not Go heap objects;
// the []byte returned by unix.Mmap is exposed to Go via syscall, but its backing is
// an anonymous mmap that the Go GC is unaware of), so the uintptr is stable.
//
// Beyond this spike, the main library code (internal/gibbous/jit) uses the same
// pattern via codePagePool, relying on this same property.
func memAddr(mem []byte) uintptr {
	return uintptr(unsafe.Pointer(&mem[0]))
}
