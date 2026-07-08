// trampoline_relay_amd64.s — issue #89 spike gate G3: SP handoff for
// nested trampoline re-entry.
//
// The real path re-enters CallJITSpec while already inside a segment (the
// exit-reason resume and the HelperCall-driven callee Run each make a fresh
// CallJITSpec). If every entry reset SP to spillBase, the inner descent
// would overwrite the outer frame's live spill region. The fix: only reset
// SP to spillBase when SP is NOT already inside the spill buffer; when it
// is (a nested entry), keep descending from the current SP.

#include "textflag.h"

// func callJITRelay(codeAddr, spillBase, spillLo uintptr) uint64
//
// Args:
//   codeAddr  +0(FP)   ← mmap segment start
//   spillBase +8(FP)   ← spill stack high-address end (SP entry point)
//   spillLo   +16(FP)  ← spill stack low-address end (growth limit)
// Return:
//   ret      +24(FP)   ← rax after the segment runs
//
// If SP is already within [spillLo, spillBase] we are a nested entry:
// leave SP where it is (continue the outer descent). Otherwise switch SP to
// spillBase (first entry from the goroutine stack).
TEXT ·callJITRelay(SB),NOSPLIT,$8-32
	MOVQ codeAddr+0(FP), AX
	MOVQ spillBase+8(FP), R13
	MOVQ spillLo+16(FP), R12
	MOVQ SP, R15                // save current SP

	// nested? spillLo <= SP <= spillBase
	CMPQ SP, R12
	JB   switch                 // SP < lo  -> not in buffer -> switch
	CMPQ SP, R13
	JA   switch                 // SP > base -> not in buffer -> switch
	JMP  call                   // already inside -> keep SP (nested)

switch:
	MOVQ R13, SP                // first entry: switch to spill base

call:
	CALL AX

	MOVQ R15, SP                // restore whatever SP we came in with
	MOVQ AX, ret+24(FP)
	RET
