// trampoline_amd64.s —— P4 PJ1 amd64 trampoline entry/exit stub.
//
// **PJ1 simplified version**: no switch to a self-managed stack, no jitContext
// setup, no callee-saved preservation. It just CALLs the mmap segment, expecting
// the segment to end with RET (returning RAX); the Go side reads RAX.
//
// Same shape as spike/p4tramp/trampoline_amd64.s; this file is the main-library
// version. The full trampoline (SP switch / jitContext setup / callee-saved
// preservation) is filled in incrementally across PJ2-PJ5 (the trampoline shape
// is extended alongside each new class of opcode template).
//
// Reference: docs/design/p4-method-jit/05-system-pipeline.md §2.4 trampoline
// three exits + 06-backends.md §4.1 amd64 register conventions.
//
// The current simplified shape corresponds to the PJ1 straight-line templates
// (MOVE/LOADK/LOADBOOL/LOADNIL/JMP/RETURN): these opcodes call no helper, so the
// template needs no jitContext / self-managed stack; it only needs to jump into
// the segment start → execute the template bytes in order → RET back. In the
// simplified shape RAX holds the template's "most recently written value" (LOADK
// bakes in the imm, LOADBOOL bakes in 0/1, etc.) —— exactly the same shape as
// spike S1.

#include "textflag.h"

// func callJIT(codeAddr uintptr) uint64
//
// PJ1 simplified shape:
//   arg    codeAddr +0(FP)  uintptr  ← mmap segment start (PROT_RX)
//   return ret      +8(FP)  uint64   ← RAX value when the mmap segment RETs
//
// frame size 0 (NOFRAME): does not allocate a Go stack frame.
// NOSPLIT: disables the morestack check (segment is momentary, no deep recursion).
//
// Implementation:
//   MOVQ codeAddr+0(FP), AX
//   CALL AX
//   MOVQ AX, ret+8(FP)
//   RET
//
// **Key points for not stepping on the Go runtime** (following spike DECISION.md):
//   - the segment does not block Go scheduling while executing (~ns scale);
//   - it calls no Go function (the segment only runs straight-line templates);
//   - the segment RET lands directly on callJIT's next MOVQ;
//   - Go ABI0 CALL protocol: the caller has already pushed the return address
//     before CALL, and the callee's c3 ret jumps back to the pushed address.

TEXT ·callJIT(SB),NOSPLIT|NOFRAME,$0-16
	MOVQ codeAddr+0(FP), AX
	CALL AX
	MOVQ AX, ret+8(FP)
	RET
