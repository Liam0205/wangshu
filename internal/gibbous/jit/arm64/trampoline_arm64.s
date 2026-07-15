// trampoline_arm64.s —— P4 PJ8 arm64 full trampoline asm.
//
// Per docs/design/p4-method-jit/05-system-pipeline.md §2.4 + 06-backends.md §4.2.
//
// **Key technical constraints**:
//   - X28 = Go G register (Go 1.17+ ABI0 arm64) —— must stay untouched;
//   - X27 holds jitContext (callee-saved);
//   - Plan 9 arm64 framesize determines how the Go runtime stack walker
//     unwinds —— with framesize > 0, Go auto prologue/epilogue manages
//     LR/FP + SP; NOFRAME + a manual SUB SP makes the runtime stack walker
//     miscompute the caller LR position → unwind fails on sigpanic with
//     "unexpected return pc" (per the failure lesson of the prior batch's
//     1c74df9).
//
// So this file uses framesize=$80 (letting the Go runtime auto-manage
// prologue/epilogue + LR/FP save); we only manually store X19-X27 into the
// frame.
//
// **Go arm64 auto-prologue actual frame layout** (verified on real M1
// hardware in F3-#3b, LR slot position per the PR#27 SIGSEGV debugging):
// for `TEXT name,NOSPLIT,$N-arglen` on arm64, the prologue the Go compiler
// generates is equivalent to:
//
//   STR.W X30, [SP, #-(N+16)]!   // SP -= N+16, LR stored at [SP+0]
//   STUR  X29, [SP, #-8]         // FP stored at [SP-8] (outside user space, below SP)
//   SUB   $8, SP, X29            // X29 = SP - 8
//   <user code sees SP here as the start of user space, size N bytes>
//
// So the **first 8 bytes [SP+0..8) of user space `[SP+0 .. SP+N)` are
// written by Go as LR**, and the epilogue `LDR.P X30, [SP], #(N+16)` reads
// LR from that same slot + pops the stack. **User code must not write to
// [SP+0..8), or LR is clobbered and the section reads a wrong value after
// RET → SIGSEGV/SIGBUS**.
//
// **frame layout** (framesize=$80):
//   [SP+ 0 .. SP+ 8) — LR written by Go auto-prologue (user must steer clear)
//   [SP+ 8 .. SP+72) — user manually stores X19-X26 (8 registers, STP×4)
//   [SP+72 .. SP+80) — user manually stores X27 (MOVD, exactly fills 80 bytes, no padding)

//go:build wangshu_p4 && arm64 && (linux || (darwin && cgo))

#include "textflag.h"

// JITContext field byte offsets consumed by the SP switch (issue #89).
// These MUST match unsafe.Offsetof(JITContext{}.spillBase / .savedGoSP);
// TestSpillStackLayout in the jit package asserts they stay in sync.
#define JITCtxSpillBaseOff 24
#define JITCtxSavedGoSPOff 176

// func callJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64
//
// Args:
//   codeAddr   +0(FP)   uintptr
//   jitCtxAddr +8(FP)   uintptr
// Returns:
//   ret        +16(FP)  uint64
//
// Implementation:
//   - Go arm64 auto-prologue auto STRs LR into [SP+0] + the FP-pop protocol
//     manages SP 96;
//   - on function entry SP points at the start of user space (80 bytes);
//   - we manually store X19-X27 starting at user offset 8, **steering clear
//     of the [SP+0..8) LR slot**;
//   - BL (R8) jumps into the mmap section, X30 is overwritten by BL;
//   - the section RET returns to the instruction after BL;
//   - manually LDP-restore X19-X27 (from user offset 8);
//   - Go auto-epilogue auto-restores LR (from [SP+0]) + FP + ADD SP + RET.

TEXT ·callJITFull(SB),NOSPLIT,$80-24
	// Go auto-prologue has already STR.W X30 [SP,#-96]! → LR is at [SP+0],
	// occupying [SP+0..8). User space starts at [SP+0], but [SP+0..8) is the
	// LR slot, so we manually store X19-X27 from [SP+8) (matching the 80-byte
	// frame: 8 LR + 9 registers × 8B = 80, no padding).
	STP	(R19, R20), 8(RSP)
	STP	(R21, R22), 24(RSP)
	STP	(R23, R24), 40(RSP)
	STP	(R25, R26), 56(RSP)
	MOVD	R27, 72(RSP)

	// Load R27 = jitContext (06 §4.2)
	MOVD	jitCtxAddr+8(FP), R27

	// Fetch codeAddr into R8 (caller-saved)
	MOVD	codeAddr+0(FP), R8

	// Indirect call into the mmap section —— Plan 9 arm64 uses BL (Reg) for BLR
	BL	(R8)

	// Section returns: R0 is already the return value. Manually restore
	// X19-X27 (LR/FP restored by the epilogue)
	LDP	8(RSP), (R19, R20)
	LDP	24(RSP), (R21, R22)
	LDP	40(RSP), (R23, R24)
	LDP	56(RSP), (R25, R26)
	MOVD	72(RSP), R27

	// Write back the return value (R0 is still the X0 produced by the mmap section)
	MOVD	R0, ret+16(FP)
	RET

// func callJITSpec(codeAddr uintptr, jitCtxAddr uintptr, vsBaseAddr uintptr) uint64
//
// Args:
//   codeAddr   +0(FP)   uintptr
//   jitCtxAddr +8(FP)   uintptr
//   vsBaseAddr +16(FP)  uintptr
// Returns:
//   ret        +24(FP)  uint64
//
// **vs callJITFull**: additionally loads X26 = vsBaseAddr (per 06 §4.2 arm64
// trampoline protocol, matches amd64 callJITSpec loading rbx=vsBase). Spec
// template sections (PJ2 speculative + PJ3 FORLOOP body/RegLimit) need value
// stack addressing [x26+disp]; once this trampoline sets it up, the section
// can use it directly.
//
// Same as callJITFull: framesize=$80 + auto-prologue managing LR/FP,
// callee-saved X19-X27 manually stored in the frame ([SP+8..SP+80), steering
// clear of the [SP+0..8) LR slot); X28=G untouched; X26/X27 loaded by this
// function after the STPs.
TEXT ·callJITSpec(SB),NOSPLIT,$80-32
	// frame layout matches callJITFull: [SP+0..8) Go LR slot / [SP+8..72)
	// X19-X26 / [SP+72..80) X27.
	STP	(R19, R20), 8(RSP)
	STP	(R21, R22), 24(RSP)
	STP	(R23, R24), 40(RSP)
	STP	(R25, R26), 56(RSP)
	MOVD	R27, 72(RSP)

	// Load R26 = vsBaseAddr + R27 = jitCtxAddr (per 06 §4.2)
	// **Order**: the STPs have already saved the original R26/R27 values, so
	// they can now be safely overwritten.
	MOVD	jitCtxAddr+8(FP), R27
	MOVD	vsBaseAddr+16(FP), R26

	// Fetch codeAddr into R8 (caller-saved). **Must be read before switching
	// SP**: `+N(FP)` is SP-relative addressing (Plan 9 virtual FP); after
	// switching SP onto the self-managed stack, codeAddr+0(FP) would point at
	// self-managed-stack garbage (issue #89, the same FP lesson as amd64).
	MOVD	codeAddr+0(FP), R8

	// **issue #89 self-managed spill stack switch (arm64)**: if R27 (jitCtx)
	// != 0 and jitCtx.spillBase != 0, stash the goroutine SP in
	// jitCtx.savedGoSP and switch SP onto the spill stack, so deep seg2seg
	// recursion descends on the 64 KiB spill buffer instead of the
	// goroutine stack's NOSPLIT allowance (PR #86 / issue #89). The window
	// between the switch and the restore is NOSPLIT with no Go call / stack
	// growth / back-edge check, so the runtime never walks the stack while
	// SP is off the goroutine stack — the load-bearing invariant the file
	// header's LR-slot warning is about. The manual X19-X27 STPs above are
	// on the goroutine frame; only the segment's own frames land on the
	// spill stack. Restored below before the manual LDPs and the Go
	// auto-epilogue (which read from the goroutine frame). R9/R10 are
	// caller-saved scratch, not in the saved set.
	CBZ	R27, nospill              // jitCtx == 0 -> no switch (unit tests)
	MOVD	JITCtxSpillBaseOff(R27), R9
	CBZ	R9, nospill               // spillBase == 0 -> no switch
	MOVD	RSP, R10                  // R10 = goroutine SP
	MOVD	R10, JITCtxSavedGoSPOff(R27)
	MOVD	R9, RSP                   // switch SP onto the spill stack

nospill:
	// Indirect call into the mmap section
	BL	(R8)

	// **issue #89 restore (arm64)**: switch SP back to the goroutine stack
	// before the manual LDPs and auto-epilogue read from it. R0 holds the
	// segment return value / exit-reason status — do NOT clobber it; use
	// R9/R10 scratch. Skip if no switch happened (jitCtx nil or spillBase 0).
	CBZ	R27, norestore
	MOVD	JITCtxSpillBaseOff(R27), R9
	CBZ	R9, norestore
	MOVD	JITCtxSavedGoSPOff(R27), R10
	MOVD	R10, RSP                  // restore goroutine SP

norestore:

	// Section returns: R0 is the return value.
	//
	// **§9.20.9 trampoline exit-resume protocol Run-end dispatcher
	// implementation** (commit-5a fixing commit-3c): switched to a Go
	// function on the Run side as the dispatcher; after the trampoline asm
	// section returns it goes straight through the normal stack pop. Run
	// checks r0==ExitInlineHelper → calls dispatchInlineHelper → a second
	// callJITSpec jumps to the resume entry (all on the Go side, same
	// technique as amd64). The original commit-3c CMP + BRK section is
	// removed (to avoid actually triggering SIGTRAP once commit-5 really
	// emits ExitInlineHelper).
	//
	// Manually restore X19-X27 (LR/FP restored by the epilogue)
	LDP	8(RSP), (R19, R20)
	LDP	24(RSP), (R21, R22)
	LDP	40(RSP), (R23, R24)
	LDP	56(RSP), (R25, R26)
	MOVD	72(RSP), R27

	// Write back the return value
	MOVD	R0, ret+24(FP)
	RET
