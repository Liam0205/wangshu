//go:build wangshu_p4 && linux && arm64

// Package arm64 manages the arm64 backend code page (W^X flip + munmap + icache flush).
//
// Follows docs/design/p4-method-jit/05-system-pipeline.md §2.1 exec mmap protocol +
// §2.3 arm64 icache flush protocol + 06-backends.md §4.2 arm64 register conventions.
//
// **PJ8 bootstrap version** (2026-06-25): linux/arm64 basic mmap+W^X+icache flush done;
// darwin/arm64 (MAP_JIT + pthread_jit_write_protect_np) left as a PJ8+ bootstrap spike.
package arm64

import (
	"errors"
	"fmt"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

// CodePage is a mmap-allocated executable segment (post W^X flip). Refcounted
// lifecycle: see amd64/codepage_linux.go doc for the Enter/Exit/Dispose
// protocol. arm64 shares the same protocol; the only arch-specific piece is
// the explicit icache flush on mmap, since arm64 does not guarantee I/D
// cache coherence.
type CodePage struct {
	mem      []byte
	length   int
	refcount atomic.Int32
	disposed atomic.Bool
}

// MmapCode allocates a W+X segment, writes code, flips to W^X, and flushes
// the arm64 icache. Initial refcount = 1 (constructor holds one ref, released
// by Dispose).
//
// Flow (mirrors amd64 with the mandatory arm64 icache-flush step):
//  1. unix.Mmap MAP_ANON|MAP_PRIVATE PROT_RW
//  2. copy code into the segment
//  3. unix.Mprotect PROT_RX (W^X flip)
//  4. **arm64 required**: flushICacheArm64 (IC IVAU / DC CVAU + DSB ISH + ISB)
//     to establish I/D cache coherence -- without it the execution stream may
//     fetch stale bytes (section 2.3.1).
func MmapCode(code []byte) (*CodePage, error) {
	if len(code) == 0 {
		return nil, errors.New("internal/gibbous/jit/arm64: empty code")
	}
	pageSize := unix.Getpagesize()
	length := ((len(code) + pageSize - 1) / pageSize) * pageSize

	mem, err := unix.Mmap(
		-1, 0, length,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_ANON|unix.MAP_PRIVATE,
	)
	if err != nil {
		return nil, fmt.Errorf("internal/gibbous/jit/arm64: mmap RW failed: %w", err)
	}
	copy(mem, code)
	if err := unix.Mprotect(mem, unix.PROT_READ|unix.PROT_EXEC); err != nil {
		_ = unix.Munmap(mem)
		return nil, fmt.Errorf("internal/gibbous/jit/arm64: mprotect RX failed: %w", err)
	}
	flushICacheArm64(mem)
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
// disposed or its refcount already reached zero. See
// amd64/codepage_linux.go for the CAS-guarded bump rationale.
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
// Real munmap fires synchronously if no Run holds the segment; otherwise
// deferred to the last Exit. Idempotent.
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

// flushICacheArm64 real implementation (follows 05 §2.3.1): after writing the mmap
// segment, must do DC CVAU + IC IVAU + DSB + ISB, otherwise instruction fetch is wrong
// (i-cache still holds stale content). Implemented in flushcache_arm64.s.
func flushICacheArm64(mem []byte) {
	if len(mem) == 0 {
		return
	}
	start := uintptr(unsafe.Pointer(&mem[0]))
	end := start + uintptr(len(mem))
	flushICacheArm64Asm(start, end)
}

//go:noescape
func flushICacheArm64Asm(start, end uintptr)
