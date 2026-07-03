//go:build linux && amd64

package p4callinline

import (
	"unsafe"
)

// memAddr returns the base address of a []byte's backing array. Safe
// for mmap segments: they are not Go heap objects, so the uintptr is
// stable (same rationale as spike/p4tramp).
func memAddr(mem []byte) uintptr {
	return uintptr(unsafe.Pointer(&mem[0]))
}
