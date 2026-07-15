//go:build wangshu_p4 && darwin && arm64 && cgo

// Package jitcgo is the cgo-isolation subpackage for the darwin/arm64 backend — it exports only
// the two primitives that cgo makes unavoidable (pthread_jit_write_protect_np +
// sys_icache_invalidate); the rest, mmap / munmap, are called directly by the parent package via
// golang.org/x/sys/unix pure-Go syscalls.
//
// **Purpose of the standalone subpackage** (F1 fix): the parent package internal/gibbous/jit/arm64
// contains Plan 9 arm64 assembly (trampoline_arm64.s / flushcache_arm64.s), and the Go toolchain
// rule "a package with cgo enabled may not contain Plan 9 .s files" (macos-latest CI confirmed
// this by reporting `package using cgo has Go assembly file trampoline_arm64.s`).
//
// Solution: isolate the cgo implementation into this standalone package; the parent package
// forwards to it through function calls, does not import "C" itself, remains a non-cgo package,
// and stays Plan 9 asm compatible.
//
// Per docs/design/p4-method-jit/05-system-pipeline.md §2.1 exec mmap protocol + §2.3 arm64 icache
// flush protocol + tmp/wangshu-p4-todo.md §III darwin/arm64 W^X implementation.
//
// **macOS-specific constraints**:
//  1. mmap must carry the MAP_JIT (0x800) flag — the parent package passes unix.MAP_JIT to
//     unix.Mmap;
//  2. the W^X flip is done via pthread_jit_write_protect_np (0=writable, 1=executable);
//     it is **thread-local state**, so the caller (parent package) is responsible for
//     runtime.LockOSThread + defer UnlockOSThread to pin the thread and prevent goroutine
//     scheduling from polluting other goroutines' writable state;
//  3. icache flush uses sys_icache_invalidate (libkern/OSCacheControl.h) rather than the
//     hand-written DC CVAU / IC IVAU on the linux side.
//
// **cgo isolation discipline** (per the user-decided plan I): the fourth build-tag position `cgo`
// is strictly enforced — the main library's default build (CGO_ENABLED=0 cross-build or the amd64
// main path) takes the parent package's codepage_other.go stub, and only the macos-latest CI with
// cgo enabled links this subpackage's real implementation.
package jitcgo

/*
#include <pthread.h>
#include <libkern/OSCacheControl.h>
*/
import "C"

import "unsafe"

// JITWriteProtectEnter switches the thread's JIT region to the "writable, non-executable" state.
//
// macOS arm64 enforces W^X; enters the writable state via pthread_jit_write_protect_np(0).
// **The caller must invoke this inside a runtime.LockOSThread region** (pthread_jit_write_protect_np
// affects thread-local state).
//
// Use case: the parent package's codepage_darwin.go::MmapCode calls this to enter the writable
// state after unix.Mmap on the MAP_JIT region and before copy(mem, code).
func JITWriteProtectEnter() {
	C.pthread_jit_write_protect_np(0)
}

// JITWriteProtectExit switches the thread's JIT region to the "executable, non-writable" state (RX).
//
// The counterpart to JITWriteProtectEnter; flips to RX via pthread_jit_write_protect_np(1).
// **The caller must invoke this inside a runtime.LockOSThread region**.
//
// Use case: the parent package's codepage_darwin.go::MmapCode calls this to flip to RX after
// copy(mem, code), then calls ICacheInvalidate to flush the i-cache.
func JITWriteProtectExit() {
	C.pthread_jit_write_protect_np(1)
}

// ICacheInvalidate flushes the region's i-cache via libSystem sys_icache_invalidate.
//
// Required on macOS arm64 — after writing the region the i-cache must be flushed, otherwise the
// CPU's instruction fetch would pick up stale i-cache contents (per 05 §2.3.1). macOS does not
// expose the EL0 cache-maintenance instructions (DC CVAU / IC IVAU), so it must go through a
// libSystem syscall.
//
// **Parameters**: addr / length describe the region range to flush (unsafe.Pointer is the address
// of mem[0] returned by the parent package's mmap).
func ICacheInvalidate(addr unsafe.Pointer, length uintptr) {
	C.sys_icache_invalidate(addr, C.size_t(length))
}
