//go:build wangshu_p4 && linux && amd64

// Package amd64 amd64 backend code-page management (W^X flip + munmap).
//
// Per docs/design/p4-method-jit/05-system-pipeline.md section 2.1 exec-mmap
// protocol:
//  1. unix.Mmap MAP_ANON|MAP_PRIVATE PROT_READ|PROT_WRITE allocation
//  2. copy code into the segment
//  3. unix.Mprotect PROT_READ|PROT_EXEC flip (W^X: never holds RWX)
//  4. linux/amd64 hardware guarantees icache coherence; no explicit flush
//
// **PJ1 spike gate green** (spike/p4tramp/DECISION.md): mmap+W^X flip
// works, single-CALL ~1.95ns. P4 self-managed codegen physical basis
// proven. Main-library form matches the spike + per-Proto segment
// release policy.
//
// # Refcounted lifecycle (concurrent Run vs Dispose safety)
//
// A CodePage's lifecycle uses refcount + deferred munmap to eliminate the
// use-after-free window in the multi-State concurrent case, which was
// previously flagged as an open gap:
//
//   - MmapCode initializes refcount = 1 (the "constructor holds one ref").
//   - Run enters via Enter (refcount +1) and releases via Exit (refcount -1).
//   - Dispose flips the disposed flag (preventing new Enter) and drops
//     the constructor's initial ref via Exit. The actual unix.Munmap runs
//     only when refcount reaches 0 -- either immediately (no active Run)
//     or when the last Run's Exit sees refcount transition to 0.
//   - Enter uses a double-check on the disposed flag to close the TOCTOU
//     window: check disposed -> refcount++ -> re-check disposed. If the
//     re-check finds disposed, Enter rolls back with Exit and returns
//     false. This guarantees that a Run started after Dispose either sees
//     the flip and refuses (returns false), or is atomically counted in
//     before Dispose's Exit and keeps the segment alive until its own Exit.
package amd64

import (
	"errors"
	"fmt"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

// CodePage is a mmap-allocated executable segment (post W^X flip).
//
// Corresponds one-to-one with P3 wazero CompiledModule: one CodePage per
// promoted Proto's native code. Dispose triggers Munmap once no Run holds
// the segment.
type CodePage struct {
	mem      []byte       // underlying mmap segment (PROT_RX after flip, read-only; Go GC unaware)
	length   int          // actual allocated bytes (>= len(code), rounded up to page size)
	refcount atomic.Int32 // >0 while alive; 0 triggers real munmap on final Exit
	disposed atomic.Bool  // true after Dispose; blocks further Enter
}

// MmapCode allocates a W+X segment, writes code, and flips W^X before
// returning. Failed allocations clean up partial mmap.
//
// Initial refcount = 1 (the constructor's own reference, released later
// by Dispose via Exit).
func MmapCode(code []byte) (*CodePage, error) {
	if len(code) == 0 {
		return nil, errors.New("internal/gibbous/jit/amd64: empty code")
	}
	pageSize := unix.Getpagesize()
	length := ((len(code) + pageSize - 1) / pageSize) * pageSize

	mem, err := unix.Mmap(
		-1, 0, length,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_ANON|unix.MAP_PRIVATE,
	)
	if err != nil {
		return nil, fmt.Errorf("internal/gibbous/jit/amd64: mmap RW failed: %w", err)
	}
	copy(mem, code)
	if err := unix.Mprotect(mem, unix.PROT_READ|unix.PROT_EXEC); err != nil {
		_ = unix.Munmap(mem)
		return nil, fmt.Errorf("internal/gibbous/jit/amd64: mprotect RX failed: %w", err)
	}
	// linux/amd64: hardware icache coherent, no explicit flush.
	// arm64 handles IC IVAU / DC CVAU in its own codepage variant.
	cp := &CodePage{mem: mem, length: length}
	cp.refcount.Store(1)
	return cp, nil
}

// Addr returns the segment start uintptr, used as a CALL target address.
//
// **unsafe scope**: only for converting the mmap segment address to a
// uintptr for the trampoline asm. Go GC does not move mmap segments (they
// are not Go heap objects; unix.Mmap returns a []byte view over anonymous
// mmap, opaque to Go GC), so the uintptr is stable.
//
// Same-source dependency: spike/p4tramp/unsafe_linux_amd64.go::memAddr.
func (c *CodePage) Addr() uintptr {
	if c == nil || len(c.mem) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&c.mem[0]))
}

// Enter acquires one reference for a caller about to execute native code
// in this segment. Returns false if the segment has been disposed (either
// disposed flag set, or refcount already at 0). The caller must not enter
// the segment when Enter returns false.
//
// The Run path pairs Enter with a deferred Exit, so refcount always
// returns to the pre-Enter value regardless of Run outcome (return,
// panic, exit-helper).
//
// **CAS-guarded bump (not a plain Add)**: Enter uses a compare-and-swap
// loop that refuses to bump when refcount is already 0. This is what
// guarantees at-most-once munmap: once Dispose (or the last Exit) drops
// refcount to 0 and triggers doMunmap, no subsequent Enter can revive the
// segment by bumping refcount to 1 and then dropping it again to 0, which
// would call doMunmap a second time. A prior plain refcount.Add(1) +
// double-check pattern had this exact race and was caught by -race in
// TestCodePage_ConcurrentRunVsDispose.
func (c *CodePage) Enter() bool {
	if c == nil {
		return false
	}
	for {
		r := c.refcount.Load()
		if r == 0 {
			// Segment already released; no revival.
			return false
		}
		if c.disposed.Load() {
			return false
		}
		if c.refcount.CompareAndSwap(r, r+1) {
			// Even after a successful CAS, disposed may have flipped in
			// between the pre-check above and here. If so, roll back via
			// Exit. Exit's decrement may or may not bring refcount to 0
			// depending on how many other Enters are in flight, but in
			// either case at-most-once munmap holds: the CAS above
			// refused to bump if refcount was already 0, and Exit's
			// Add(-1) can only match with either (a) our own +1 or (b)
			// Dispose's -1 of the constructor ref -- these together are
			// what brought refcount to 0 for the first (and only) time.
			if c.disposed.Load() {
				c.Exit()
				return false
			}
			return true
		}
	}
}

// Exit releases one reference. If refcount transitions to zero, the
// actual unix.Munmap fires here. Safe to call from any goroutine.
//
// Callers pair with Enter (Run entry/exit) or with Dispose (constructor
// ref drop). Do not call Exit without a matching prior Enter or MmapCode.
func (c *CodePage) Exit() {
	if c == nil {
		return
	}
	if c.refcount.Add(-1) == 0 {
		c.doMunmap()
	}
}

// Dispose flips the disposed flag (blocking further Enter) and drops the
// constructor's initial reference. If no Run holds the segment at this
// moment, Dispose triggers the real munmap synchronously; otherwise the
// last Run's Exit will trigger it.
//
// Idempotent: repeated Dispose is a no-op (disposed flag CAS gates the
// initial-ref drop).
//
// Returns the munmap error if it fired synchronously (this call was the
// last holder), or nil if the segment is either (a) still held by an
// active Run (munmap deferred to that Run's Exit, error unobservable
// here), or (b) already disposed (repeat call).
func (c *CodePage) Dispose() error {
	if c == nil {
		return nil
	}
	if !c.disposed.CompareAndSwap(false, true) {
		return nil
	}
	// Drop the constructor's initial reference. If refcount hits 0 here,
	// no Run held the segment; doMunmap fires synchronously and we
	// capture its error. Otherwise munmap is deferred to the last Exit
	// and its error path is logged via doMunmap; not observable here.
	if c.refcount.Add(-1) == 0 {
		return c.doMunmapCapturing()
	}
	return nil
}

// Munmap is a compatibility shim that behaves like Dispose. Existing
// call sites (tests, Compile teardown) invoked Munmap directly before
// refcount was introduced; keeping the name preserves those call sites
// while getting the refcounted semantics.
//
// Deprecated: new call sites should call Dispose directly for clarity.
func (c *CodePage) Munmap() error {
	return c.Dispose()
}

// doMunmap performs the actual unix.Munmap. Called only when refcount
// transitions to 0. Error return is dropped; use doMunmapCapturing when
// the caller needs to observe the error.
func (c *CodePage) doMunmap() {
	mem := c.mem
	c.mem = nil
	c.length = 0
	if mem != nil {
		_ = unix.Munmap(mem)
	}
}

// doMunmapCapturing is like doMunmap but returns the unix.Munmap error
// for the synchronous Dispose path.
func (c *CodePage) doMunmapCapturing() error {
	mem := c.mem
	c.mem = nil
	c.length = 0
	if mem == nil {
		return nil
	}
	return unix.Munmap(mem)
}

// Length returns the actual allocated byte count (page-aligned, may
// exceed len(code)).
//
// Callers: codePagePool release strategy; diagnostic logs.
func (c *CodePage) Length() int {
	if c == nil {
		return 0
	}
	return c.length
}
