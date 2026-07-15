// trampoline_amd64.s —— P4 PJ1 spike, minimal Go → mmap segment round-trip.
//
// Plan 9 asm (Go flavor). Mirrors docs/design/p4-method-jit/05-system-pipeline.md
// §2.4 (trampoline enter/exit stub) + 06-backends.md §4.1 (amd64 register convention).
//
// **PJ1 spike minimal version**: no SP switch / no jitContext setup / no callee-saved save.
// The full trampoline is implemented in internal/gibbous/jit/amd64/trampoline_amd64.s.

#include "textflag.h"

// func CallJIT(codeAddr uintptr) uint64
//
// Params:
//   codeAddr +0(FP)  uintptr  ← mmap segment start (PROT_RX)
// Returns:
//   ret      +8(FP)  uint64   ← RAX value after executing the mmap segment
//
// frame size 0: no Go stack frame allocated (`NOFRAME` flag); the Go-side caller
// owns the stack layout.
//
// Implementation:
//   MOVQ codeAddr+0(FP), AX   ; AX = mmap segment start
//   CALL AX                    ; jump into the mmap segment; the segment ends with ret carrying the RAX return value
//   MOVQ AX, ret+8(FP)         ; write RAX back to the Go frame return slot
//   RET                        ; return to the Go caller
//
// **Key points for not tripping the Go runtime**:
//   - the mmap segment does not block Go scheduling while executing (the segment is instantaneous, ~ns);
//   - it calls no Go function (the segment only runs mov + ret);
//   - callee-saved registers are untouched (mov+ret leaves r12-r15/rbp alone);
//   - Go ABI0 CALL protocol: the caller has already pushed the return address to SP before CALL, and the callee's
//     ret jumps back to that address —— the mmap segment's `c3` (ret) lands directly on the MOVQ following our CALL.
//
// NOSPLIT: prevent the Go runtime from inserting a morestack check before our CALL / after RET
// (usually unnecessary, but keeps the trampoline path behavior predictable).

TEXT ·CallJIT(SB),NOSPLIT|NOFRAME,$0-16
	MOVQ codeAddr+0(FP), AX
	CALL AX
	MOVQ AX, ret+8(FP)
	RET
