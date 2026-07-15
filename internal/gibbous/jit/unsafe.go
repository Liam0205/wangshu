//go:build wangshu_p4

package jit

import "unsafe"

// jitContextAddr converts *JITContext to uintptr, for loading into r15 as an argument to callJITFull.
//
// **unsafe scope**: JITContext is a Go heap object, and the GC does not move the Go heap; the uintptr
// stays stable for the lifetime of the JITContext (per 05-system-pipeline §1.3.4 "the JIT holds no Go
// stack pointers, and jitContext lives on the Go heap").
//
// Note: after this function returns a uintptr, the caller is responsible for keeping jitCtx from being
// reclaimed by the GC while in the JIT world (the p4Code holds a *JITContext field to keep it alive).
func jitContextAddr(ctx *JITContext) uintptr {
	if ctx == nil {
		return 0
	}
	return uintptr(unsafe.Pointer(ctx))
}
