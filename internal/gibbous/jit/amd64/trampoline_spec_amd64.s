// trampoline_spec_amd64.s — P4 PJ2 speculation-template trampoline.
//
// **Difference from trampoline_full_amd64.s**: the full version only loads
// r15=jitContext; the spec version also loads rbx=valueStackBase (the JIT segment
// reads/writes value-stack slots directly via rbx+disp32, skipping the host helper
// round-trip).
//
// **Register convention** (per 06 §4.1):
//   - r15 = jitContext (callee-saved, globally reserved in P4)
//   - rbx = valueStackBase (dedicated to P4 PJ2+ speculation templates, callee-saved)
//   - r14 = Go G (PUSHed to save before entering the mmap segment, POPped to restore
//     after; **inside the spec segment R14 may be used as scratch to hold the arena
//     base** — same technique as PJ4 IC's six paths + PJ5 SELF spec template, per the
//     R14 ABI-violation fix from PR #26's external review: the segment transiently
//     (~ns) overwrites R14 = arena base, and when the segment RETs normally back to
//     the trampoline it POPs R14 to recover Go G, so any later Go runtime
//     morestack/preemption/synchronization operation that reads g sees the correct g value)
//
// **PJ2 simplified form** (matching trampoline_full PJ2): does not switch SP — keeps
// running the mmap segment on the Go stack (the segment is transient, ~ns). Full SP
// switching is deferred to PJ3+ (enabled when arithmetic calls a helper).

//go:build wangshu_p4 && linux && amd64

#include "textflag.h"

// JITContext field byte offsets consumed by the SP switch (issue #89).
// These MUST match unsafe.Offsetof(JITContext{}.spillBase / .savedGoSP);
// TestSpillStackLayout in the jit package asserts they stay in sync — if a
// field is added/reordered ahead of these and the offsets drift, that test
// fails rather than corrupting SP silently.
#define JITCtxSpillBaseOff 24
#define JITCtxSavedGoSPOff 176

// func callJITSpec(codeAddr uintptr, jitCtx uintptr, vsBase uintptr) uint64
//
// Arguments:
//   codeAddr +0(FP)   uintptr  ← mmap segment start
//   jitCtx   +8(FP)   uintptr  ← *JITContext (loaded into r15)
//   vsBase   +16(FP)  uintptr  ← valueStackBase (loaded into rbx)
// Returns:
//   ret      +24(FP)  uint64   ← RAX value after the segment executes

TEXT ·callJITSpec(SB),NOSPLIT,$0-32
	// Save callee-saved registers (rbx/rbp/r12/r13/r14/r15).
	// **r14 = Go G**: this NOSPLIT trampoline segment never triggers morestack/preemption
	// (CALL AX is a direct indirect call jumping into the PROT_RX mmap segment; inside the
	// segment there is no Go function call, no stack allocation, no back-edge checkpoint =
	// nothing triggers a Go runtime read of g). So inside the segment R14 can be borrowed
	// as an arena-base scratch — when the segment RETs back to this trampoline, POP R14
	// restores Go G, and any later Go runtime preemption/morestack/synchronization read of
	// g sees the correct G. Per the R14 ABI-violation fix from PR #26's external review
	// (2026-06-28): the previous trampoline did not push/pop R14, so after the in-segment
	// PJ4 IC template did mov r14, [r15+arenaBaseOff] to overwrite R14 = arena base, the
	// segment's RET directly polluted Go G, and under production load morestack/preemption
	// reading g would SEGV; this batch's PUSH/POP R14 rescue preserves Go G correctness.
	PUSHQ BX
	PUSHQ BP
	PUSHQ R12
	PUSHQ R13
	PUSHQ R14
	PUSHQ R15

	// Load r15 = jitContext, rbx = valueStackBase
	MOVQ jitCtx+8(FP), R15
	MOVQ vsBase+16(FP), BX

	// Read codeAddr into R13 BEFORE any SP switch: `+N(FP)` is SP-relative
	// (Plan 9 virtual FP), so once SP moves onto the spill stack, codeAddr
	// +0(FP) would point at spill-buffer garbage (issue #89: this was the
	// first wiring bug — reading codeAddr after the switch faulted).
	MOVQ codeAddr+0(FP), R13

	// **issue #89 self-managed spill stack switch**: if jitCtx != 0 and
	// jitCtx.spillBase != 0, stash the goroutine SP in jitCtx.savedGoSP and
	// switch SP onto the spill stack, so deep seg2seg recursion (each level
	// does `sub sp`) descends on the 64 KiB spill buffer instead of the
	// goroutine stack's ~800 B NOSPLIT allowance (PR #86 / issue #89). This
	// whole window is NOSPLIT: no Go call, no stack growth, no back-edge
	// check between the switch and the restore, so the Go runtime never
	// walks the stack while SP is off the goroutine stack. Restored below
	// before the POPs read callee-saved from the goroutine stack. Nested
	// entries (exit-reason resume) each run sequentially after the outer
	// restore, so a single savedGoSP slot per jitCtx is safe (see
	// spike/p4spillstack DECISION.md G3). The callee-saved PUSHes above
	// already sit on the goroutine stack; only the segment's own frames
	// land on the spill stack. R15 may be 0 in low-level template unit
	// tests (jitCtx not needed) — guard against dereferencing nil.
	TESTQ R15, R15
	JZ    nospill
	MOVQ JITCtxSpillBaseOff(R15), AX  // AX = spillBase
	TESTQ AX, AX
	JZ    nospill
	MOVQ  SP, JITCtxSavedGoSPOff(R15) // stash goroutine SP
	MOVQ  AX, SP                      // switch SP onto the spill stack

nospill:
	// CALL the segment (codeAddr already in R13, read before the switch).
	CALL R13

	// Segment returned: RAX already holds the return value.
	//
	// **§9.20.9 trampoline exit-resume protocol Run-end dispatcher implementation** (commit-5a
	// corrects commit-3c): the design draft (4) assumed the trampoline asm would CALL a Go
	// dispatcher, but in practice the cross-package + Plan 9 ABI complexity is high; switched
	// to using a **Run-end Go function as the dispatcher**: Run checks raxSpec==ExitInlineHelper
	// → calls dispatchInlineHelper → a second callJITSpec jumps to the resume entry (all done
	// on the Go side; the trampoline asm passes RAX through without interpreting it).
	//
	// So after the segment returns, the trampoline asm goes straight to the normal stack
	// unwinding, no longer CMP RAX — the Run side reads RAX and routes. The original commit-3c
	// CMP + INT 3 segment is removed (keeping INT 3 would really trigger SIGTRAP after the
	// commit-5 archSupportsFrameInline=true + emit ExitInlineHelper segment).

	// **issue #89 restore**: switch SP back to the goroutine stack before
	// the POPs read callee-saved from it. If spillBase was 0 (no switch)
	// or jitCtx was nil, savedGoSP is stale but SP was never moved, so
	// skip the restore. MUST NOT clobber RAX here: RAX carries the
	// segment's return value / exit-reason status that the Go-side Run
	// reads. Use R13 for the test (R15 is still jitCtx — P4-reserved
	// callee-saved, unchanged by the segment; the POPs below restore R13
	// for our caller).
	TESTQ R15, R15
	JZ    norestore
	MOVQ JITCtxSpillBaseOff(R15), R13
	TESTQ R13, R13
	JZ    norestore
	MOVQ  JITCtxSavedGoSPOff(R15), SP // restore goroutine SP

norestore:
	// Segment returned: RAX already holds the return value. Restore callee-saved (pop in reverse order).
	// **R14 Go G restore rescue**: POP R14 writes the G value that the Go runtime loaded at
	// entry back into R14, undoing the in-segment mov r14, arenaBase overwrite.
	POPQ R15
	POPQ R14
	POPQ R13
	POPQ R12
	POPQ BP
	POPQ BX

	// Write back the return value
	MOVQ AX, ret+24(FP)
	RET
