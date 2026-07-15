// trampoline_full_amd64.s — P4 PJ2 complete trampoline asm draft (switch SP +
// save callee-saved + load jitContext).
//
// Follows docs/design/p4-method-jit/05-system-pipeline.md §2.4 trampoline
// enter/exit three-exit protocol + §3.3 jitContext + §3.4 self-managed stack +
// 06-backends.md §4.1 amd64 register conventions.
//
// **PJ2 status: draft (NOT WIRED)** — this file implements the complete
// trampoline asm, but within the current PJ2 scope GibbousCode.Run does **not**
// call callJITFull (wiring left to PJ3+). SupportsAllOpcodes is still all-false
// ⇒ no Proto takes the P4 path, so this asm is dead code and does not affect
// main-library behavior.
//
// The purpose of landing this asm: let the Go runtime compatibility issues on
// PJ3+ startup (keeping r14=G register on SP switch / Go ABI0 callee-saved
// protocol / morestack protocol) pass compile-time checks already in the PJ2
// stage, rather than discovering the asm cannot be written at PJ3+ startup.
//
// **Key technical constraints** (from the P4 design + measured Go runtime
// behavior):
//   - r14 = Go G register (Go 1.17+ ABI0) — this trampoline does not trigger
//     morestack/preemption inside the NOSPLIT section (CALL AX into the mmap
//     section calls no Go function), so R14 can be borrowed as scratch inside
//     the section (typically loading the PJ4 IC arena base); **the trampoline
//     must PUSH/POP R14** (from the PR #26 external-review R14 ABI-violation fix,
//     2026-06-28); the POP at the end restores Go G, so subsequent Go runtime
//     morestack/preemption/synchronization sees the correct G.
//   - callee-saved (amd64 SysV ABI): rbx, rbp, r12, r13, r14, r15 — the
//     trampoline pushes/pops them all. r15 = jitContext / r14 can be borrowed
//     inside the spec section / the other callee-saved are pushed at entry and
//     popped at exit;
//   - NOSPLIT|NOFRAME: the trampoline allocates no Go stack frame and is not
//     instrumented by the morestack check — this is the premise of "the JIT
//     section + trampoline path does not go through the Go runtime scheduler".

#include "textflag.h"

// func callJITFull(codeAddr uintptr, jitCtx uintptr) uint64
//
// Parameters:
//   codeAddr +0(FP)   uintptr  ← mmap section start (PROT_RX)
//   jitCtx   +8(FP)   uintptr  ← *JITContext (Go heap, loaded into r15)
// Returns:
//   ret      +16(FP)  uint64   ← RAX value after executing the mmap section
//
// Implementation points:
//   1. Save callee-saved (rbx/rbp/r12/r13; r14=G untouched; r15 about to be overwritten, save first);
//   2. Load r15 = jitContext;
//   3. CALL codeAddr to jump into the mmap section — inside, r15+offset can be read to fetch jitContext fields;
//   4. After the section returns, r15 may have been clobbered by the mmap section (if it called a helper); restore callee-saved;
//   5. Write RAX back to ret.
//
// **PJ2 simplified form**: no SP switch — keep running the mmap section on the
// Go stack (the section is instantaneous, ~ns, same risk surface as PJ1). The
// full SP switch is left to PJ3+ (enabled when arithmetic + helper calls
// introduce sub-CALLs).

TEXT ·callJITFull(SB),NOSPLIT|NOFRAME,$0-24
	// Save callee-saved registers (rbx/rbp/r12/r13/r14/r15)
	// **r14 = Go G**: this trampoline does not trigger morestack/preemption inside
	// the NOSPLIT section, so the spec section can borrow R14 as scratch (arena
	// base load); the POP R14 at the end restores Go G (same R14 ABI-violation fix
	// as trampoline_spec_amd64.s).
	PUSHQ BX
	PUSHQ BP
	PUSHQ R12
	PUSHQ R13
	PUSHQ R14
	PUSHQ R15

	// Load r15 = jitContext (06 §4.1 amd64 register conventions)
	MOVQ jitCtx+8(FP), R15

	// Fetch codeAddr into AX
	MOVQ codeAddr+0(FP), AX

	// CALL the mmap section (the section ends with RET, carrying a RAX return value)
	CALL AX

	// Section returned: RAX is already the return value. Restore callee-saved (pop in reverse)
	POPQ R15
	POPQ R14
	POPQ R13
	POPQ R12
	POPQ BP
	POPQ BX

	// Write back the return value
	MOVQ AX, ret+16(FP)
	RET
