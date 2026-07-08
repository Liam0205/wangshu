//go:build linux && amd64

package p4spillstack

import (
	"unsafe"
)

// callJITRelay enters the segment, switching SP to spillBase only if SP is
// not already inside [spillLo, spillBase] (nested entry keeps its SP).
// Implemented in trampoline_relay_amd64.s.
//
//go:noescape
func callJITRelay(codeAddr, spillBase, spillLo uintptr) uint64

// currentSP returns the caller's approximate stack pointer, for asserting
// which stack a call is running on. Implemented in sp_amd64.s.
//
//go:noescape
func currentSP() uintptr

// CallRelay runs the segment with the range-checked SP handoff.
func (s *SpillStack) CallRelay(page *CodePage) uint64 {
	r := callJITRelay(page.Addr(), s.Base(), s.Lo())
	keepAlive(s)
	return r
}

// InRange reports whether addr falls within this spill stack's buffer.
func (s *SpillStack) InRange(addr uintptr) bool {
	if s == nil || len(s.buf) == 0 {
		return false
	}
	lo := uintptr(unsafe.Pointer(&s.buf[0]))
	hi := lo + uintptr(len(s.buf))
	return addr >= lo && addr <= hi
}
