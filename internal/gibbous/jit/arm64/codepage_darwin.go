//go:build wangshu_p4 && darwin && arm64 && cgo

// Package arm64 manages darwin/arm64 backend code pages (MAP_JIT + W^X flip + icache flush).
//
// **This file has no direct cgo import** (per the F1 fix: the parent arm64
// package contains Plan 9 .s files, and enabling cgo trips the Go toolchain's
// "same-package cgo + Go asm are mutually exclusive" rule); the unavoidable cgo
// calls pthread_jit_write_protect_np + sys_icache_invalidate are isolated into
// the subpackage internal/gibbous/jit/arm64/jitcgo, and the parent package
// forwards to them via ordinary Go function calls.
//
// Per docs/design/p4-method-jit/05-system-pipeline.md §2.1 exec mmap protocol +
// §2.3 arm64 icache flush protocol + 06-backends.md §4.2 arm64 register
// conventions + tmp/wangshu-p4-todo.md §3 darwin/arm64 W^X real implementation.
//
// **macOS-specific constraints** (linux path see codepage_linux.go):
//  1. mmap must carry the MAP_JIT (0x800) flag — the macOS Hardened Runtime
//     forbids PROT_EXEC by default, and only MAP_JIT segments are exempt (needs
//     the `com.apple.security.cs.allow-jit` entitlement; the GH Actions
//     macos-latest runner's `go test` subprocess allows it by default);
//  2. the W^X flip cannot use mprotect — macOS arm64 enforces W^X, switched via
//     pthread_jit_write_protect_np (0=writable, 1=executable); **thread-local
//     state**, so runtime.LockOSThread + defer UnlockOSThread pins the thread to
//     stop goroutine scheduling from polluting other goroutines' writable state;
//  3. icache flush uses sys_icache_invalidate (libkern/OSCacheControl.h) rather
//     than the linux side's hand-written DC CVAU/IC IVAU (macOS does not expose
//     the EL0 cache-maintenance instructions).
//
// **cgo isolation discipline** (per the user-decided plan I): the 4th build tag
// position `cgo` is strictly enforced — the main library's default build
// (CGO_ENABLED=0 cross-build or amd64 main path) uses the codepage_other.go
// stub, and only when macos-latest CI enables cgo does it link the jitcgo
// subpackage's real implementation. **The main library's zero-cgo-import promise
// stays unchanged**: the parent arm64 package has no import "C"; cgo lives only
// inside the jitcgo subpackage.
package arm64

import (
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"

	jitcgo "github.com/Liam0205/wangshu/internal/gibbous/jit/arm64/jitcgo"
)

// CodePage is a MAP_JIT mmap-allocated executable segment (post W^X flip),
// with the same refcount lifecycle as codepage_linux.go. See
// amd64/codepage_linux.go for the Enter/Exit/Dispose protocol rationale.
type CodePage struct {
	mem      []byte
	length   int
	refcount atomic.Int32
	disposed atomic.Bool
}

// MmapCode allocates a MAP_JIT segment, writes code, flips W^X + flushes the
// arm64 icache.
//
// Flow (mirrors the codepage_linux.go MmapCode structure + macOS-specific steps):
//  1. runtime.LockOSThread: pins the current goroutine to this OS thread, since
//     pthread_jit_write_protect_np affects thread-local state; UnlockOSThread is
//     deferred to release after the RX flip + icache flush;
//  2. unix.Mmap MAP_ANON|MAP_PRIVATE|MAP_JIT, PROT_READ|PROT_WRITE|PROT_EXEC —
//     macOS MAP_JIT segments must carry PROT_EXEC from the start (without the
//     mmap flag it cannot be flipped later);
//  3. jitcgo.JITWriteProtectEnter(): enter the writable state (cgo forward);
//  4. copy code into the segment;
//  5. jitcgo.JITWriteProtectExit(): flip to RX (cgo forward);
//  6. jitcgo.ICacheInvalidate(addr, length): flush the i-cache (cgo forward);
//  7. runtime.UnlockOSThread (deferred).
//
// The error path must unmap + UnlockOSThread to avoid leaking resources / a
// pinned thread.
func MmapCode(code []byte) (*CodePage, error) {
	if len(code) == 0 {
		return nil, errors.New("internal/gibbous/jit/arm64: empty code")
	}
	pageSize := unix.Getpagesize()
	length := ((len(code) + pageSize - 1) / pageSize) * pageSize

	// Step 1: pin the thread — pthread_jit_write_protect_np is thread-local
	// state, and goroutine cross-thread scheduling would pollute other
	// goroutines' writable state.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Step 2: mmap MAP_JIT + PROT_RWX.
	mem, err := unix.Mmap(
		-1, 0, length,
		unix.PROT_READ|unix.PROT_WRITE|unix.PROT_EXEC,
		unix.MAP_ANON|unix.MAP_PRIVATE|unix.MAP_JIT,
	)
	if err != nil {
		return nil, fmt.Errorf("internal/gibbous/jit/arm64: mmap MAP_JIT failed: %w", err)
	}

	// Step 3: enter the writable state (jitcgo forward, thread-local).
	jitcgo.JITWriteProtectEnter()

	// Step 4: copy code into the segment.
	copy(mem, code)

	// Step 5: flip to RX (jitcgo forward).
	jitcgo.JITWriteProtectExit()

	// Step 6: flush the i-cache (jitcgo forward; required on macOS arm64).
	jitcgo.ICacheInvalidate(unsafe.Pointer(&mem[0]), uintptr(length))

	cp := &CodePage{mem: mem, length: length}
	cp.refcount.Store(1)
	return cp, nil
}

// Addr returns the segment start uintptr.
func (c *CodePage) Addr() uintptr {
	if c == nil || len(c.mem) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&c.mem[0]))
}

// Enter acquires one reference. Returns false if the segment has been
// disposed or its refcount already reached zero. Same CAS-guarded bump
// pattern as amd64/codepage_linux.go.
func (c *CodePage) Enter() bool {
	if c == nil {
		return false
	}
	for {
		r := c.refcount.Load()
		if r == 0 {
			return false
		}
		if c.disposed.Load() {
			return false
		}
		if c.refcount.CompareAndSwap(r, r+1) {
			if c.disposed.Load() {
				c.Exit()
				return false
			}
			return true
		}
	}
}

// Exit releases one reference; if refcount hits zero, real munmap fires here.
func (c *CodePage) Exit() {
	if c == nil {
		return
	}
	if c.refcount.Add(-1) == 0 {
		c.doMunmap()
	}
}

// Dispose flips the disposed flag and drops the constructor's initial ref.
// Real munmap fires synchronously if no Run holds the segment. Idempotent.
func (c *CodePage) Dispose() error {
	if c == nil {
		return nil
	}
	if !c.disposed.CompareAndSwap(false, true) {
		return nil
	}
	if c.refcount.Add(-1) == 0 {
		return c.doMunmapCapturing()
	}
	return nil
}

// Munmap is a compatibility alias for Dispose. Deprecated: prefer Dispose.
func (c *CodePage) Munmap() error {
	return c.Dispose()
}

func (c *CodePage) doMunmap() {
	mem := c.mem
	c.mem = nil
	c.length = 0
	if mem != nil {
		_ = unix.Munmap(mem)
	}
}

func (c *CodePage) doMunmapCapturing() error {
	mem := c.mem
	c.mem = nil
	c.length = 0
	if mem == nil {
		return nil
	}
	return unix.Munmap(mem)
}

// Length returns the actual allocated byte count (page-aligned).
func (c *CodePage) Length() int {
	if c == nil {
		return 0
	}
	return c.length
}
