// trampoline_amd64.s — issue #89 spike: switch SP onto a self-managed
// spill stack before entering the mmap segment, restore it after.
//
// Plan 9 asm (Go flavor). Mirrors the design in
// docs/design/p4-method-jit/05-system-pipeline.md §3.4 and the amd64
// trampoline in internal/gibbous/jit/amd64/trampoline_spec_amd64.s, but
// adds the SP switch that the PJ10 path currently skips.
//
// The window between the SP switch and the restore is NOSPLIT: no Go call,
// no stack growth, no back-edge check, so the Go runtime never tries to
// read g / grow / preempt while SP points at the spill buffer. This is the
// load-bearing invariant — if the runtime walked the stack while SP is off
// the goroutine stack, unwinding would fault.

#include "textflag.h"

// func callJITOnSpillStack(codeAddr uintptr, spillBase uintptr) uint64
//
// Args:
//   codeAddr  +0(FP)  uintptr  ← mmap segment start (PROT_RX)
//   spillBase +8(FP)  uintptr  ← self-managed stack base (high address end,
//                                 grows down); if 0, run on the goroutine
//                                 stack (baseline, no switch)
// Return:
//   ret      +16(FP)  uint64   ← rax after the segment runs
//
// R14 is the Go G register (Go 1.17+ ABIInternal). We do not call any Go
// function, so we never touch R14 — the segment runs pure machine code and
// returns via `ret`.
//
// We save the goroutine SP in R15 across the switch. R15 is caller-saved
// under Go's internal ABI for a leaf like this (we make no Go call), and
// we restore SP from it before returning, so the caller's frame is intact.
// The Go prologue/epilogue for the declared $8 frame manages the return
// address on the goroutine stack; the SEGMENT's own CALL/RET happens on
// whichever stack SP currently points at (the spill stack when switched).
TEXT ·callJITOnSpillStack(SB),NOSPLIT,$8-24
	MOVQ codeAddr+0(FP), AX
	MOVQ spillBase+8(FP), R13
	MOVQ SP, R15                // save goroutine SP in R15

	TESTQ R13, R13
	JZ    call                  // spillBase == 0 -> baseline, no switch

	MOVQ  R13, SP               // switch SP onto the spill stack

call:
	CALL AX                     // enter the segment; it ret's back here

	MOVQ R15, SP                // restore goroutine SP unconditionally
	MOVQ AX, ret+16(FP)
	RET
