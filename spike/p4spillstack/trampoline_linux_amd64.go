//go:build linux && amd64

package p4spillstack

import (
	"unsafe"
)

// callJITOnSpillStack enters the mmap segment at codeAddr. If spillBase is
// non-zero, it switches SP onto the spill stack (whose high-address end is
// spillBase) for the duration of the segment, then restores the goroutine
// SP. Returns the segment's rax.
//
// Implemented in trampoline_amd64.s.
//
//go:noescape
func callJITOnSpillStack(codeAddr uintptr, spillBase uintptr) uint64

// SpillStack is a Go-heap []byte used as a self-managed machine stack. The
// backing is a plain byte array, so the Go GC never scans it for pointers
// (05 §3.4: the spill stack holds only register spills / return addresses /
// alignment, never Lua values or Go heap pointers). We keep a reference to
// buf so the GC does not free it while the segment is running on it.
type SpillStack struct {
	buf []byte
}

// NewSpillStack allocates a size-byte self-managed stack.
func NewSpillStack(size int) *SpillStack {
	return &SpillStack{buf: make([]byte, size)}
}

// Base returns the high-address end of the buffer (the SP entry point for a
// down-growing stack), rounded down to 16-byte alignment as required by the
// amd64 ABI. Returns 0 for a nil/empty stack (baseline: no switch).
func (s *SpillStack) Base() uintptr {
	if s == nil || len(s.buf) == 0 {
		return 0
	}
	end := uintptr(unsafe.Pointer(&s.buf[0])) + uintptr(len(s.buf))
	return end &^ 0xf
}

// Lo returns the low-address end (the growth limit), for bounds checks.
func (s *SpillStack) Lo() uintptr {
	if s == nil || len(s.buf) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&s.buf[0]))
}

// CallOnSpillStack runs the segment at page.Addr() with SP switched onto
// this spill stack. Keeps s alive across the call.
func (s *SpillStack) CallOnSpillStack(page *CodePage) uint64 {
	r := callJITOnSpillStack(page.Addr(), s.Base())
	// Keep the backing alive until the segment has returned (SP was on it).
	keepAlive(s)
	return r
}

// CallBaseline runs the segment on the goroutine stack (no SP switch), for
// contrast with CallOnSpillStack.
func CallBaseline(page *CodePage) uint64 {
	return callJITOnSpillStack(page.Addr(), 0)
}
