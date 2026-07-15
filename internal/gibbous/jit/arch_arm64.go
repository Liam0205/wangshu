//go:build wangshu_p4 && arm64

// arch_arm64.go — P4 PJ8 arch routing arm64 implementation (mirrors arch_amd64.go).
//
// The arm64 side provides the same mmap+W^X+icache-flush + trampoline + emitter
// shape via the jitarm64 package.
//
// Follows docs/design/p4-method-jit/06-backends.md §4.2 arm64 register
// convention (X28=Go G untouched / X27 holds jitContext / X0 return value).
package jit

import (
	"github.com/Liam0205/wangshu/internal/bytecode"

	jitarm64 "github.com/Liam0205/wangshu/internal/gibbous/jit/arm64"
)

// arenaBaseOffArm64 checks that arenaBaseOff (the byte offset of the jitContext
// field) is within the arm64 LDR scaled-offset unsigned 12-bit + 8-byte-aligned
// range, panicking on overflow (hardening a structural precondition into a
// runtime invariant).
//
// arm64 `LDR Xt, [Xn, #pimm12]` takes an 8-byte scaled offset (the actual
// byteOff = pimm12 * 8, range [0, 32760]); `EmitLdrXtFromXnDisp` silently falls
// back to byteOff=0 on an invalid value → silently reads [x27+0] and hits the
// wrong field. This helper promotes the check from a comment to a runtime panic,
// guarding against a future JITContext field reordering that pushes arenaBase to
// ≥32760 and silently breaks it.
//
// Currently JITContextArenaBaseOffset = 0 (arenaBase is the first JITContext
// field), so this is unreachable; kept for future hardening.
func arenaBaseOffArm64(arenaBaseOff int32) uint16 {
	if arenaBaseOff < 0 || arenaBaseOff > 32760 || arenaBaseOff%8 != 0 {
		panic("internal/gibbous/jit/arm64: arenaBaseOff out of range or not 8-byte aligned")
	}
	return uint16(arenaBaseOff)
}

// archCodePage is the executable segment of the arch abstraction — in this build
// it aliases jitarm64.CodePage.
type archCodePage = jitarm64.CodePage

// archEmitLoadKReturn emits the straight-line "mov X0, value; ret" template
// (20 bytes on arm64: movz+movk×3 for 16 bytes + 4-byte ret).
//
// The constant family bakes in a NaN-box value; for the prelude/compare-fold
// family X0 is a dummy (ignored by the Run side).
func archEmitLoadKReturn(buf []byte, value uint64) []byte {
	buf = jitarm64.EmitMovX0Imm64(buf, value)
	buf = jitarm64.EmitRet(buf)
	return buf
}

// archMmapCode writes code into a W^X segment and flushes the icache.
func archMmapCode(code []byte) (*archCodePage, error) {
	return jitarm64.MmapCode(code)
}

// archCallJITFull jumps into the mmap segment (arm64 trampoline: save
// callee-saved X19-X30 + load jitContext into X27 + BLR + restore). Returns X0.
func archCallJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64 {
	return jitarm64.CallJITFull(codeAddr, jitCtxAddr)
}

// archCallJITSpec is the arm64 spec-trampoline implementation (mirrors the amd64
// callJITSpec that loads rbx=vsBase + r15=jitCtx + BLR + restore). jitarm64.CallJITSpec
// goes through trampoline_arm64.s::callJITSpec, which loads x26=valueStackBase +
// x27=jitContext then BLs into the mmap segment; the segment is expected to end
// with RET, and the return value is in X0.
//
// Enabling this implementation lets archSupportsSpec() flip to true → the PJ2
// speculative templates (reg-reg / reg-K / chain-KK) + the PJ3 FORLOOP
// body/body2/RegLimit three paths, once the Compile side builds p4Code with
// useSpec=true on arm64, have their Run side jump into the segment via this function.
//
// **Truly enabling it still requires archSupportsSpec() to flip to true** (deferred
// to the same batch as PJ8+ and physical self-hosted runner end-to-end validation;
// QEMU does not faithfully model i-cache + PROT_EXEC, so it cannot reliably do e2e).
// This batch only implements the trampoline asm + Go wrapper, reducing the future
// enablement effort.
func archCallJITSpec(codeAddr uintptr, jitCtxAddr uintptr, vsBase uintptr) uint64 {
	return jitarm64.CallJITSpec(codeAddr, jitCtxAddr, vsBase)
}

// archSseOpForArith maps a Lua arithmetic opcode to a "speculation-whitelist
// marker byte" (mirroring the amd64 SSE binop opcode). The arm64 side does not
// use these 4 bytes to emit machine code directly; instead it goes through
// arm64ArithOpSelForSseOp, which translates them into the arm64
// fadd/fsub/fmul/fdiv opSel.
//
// Unsupported ops (MOD/POW — MOD uses floor-mod which is not a single SSE
// instruction, POW uses a pow() helper) return (0, false).
//
// **Follows 03-speculation-ic.md §2 speculation whitelist**: the f64 fast-path
// speculation only holds for ADD/SUB/MUL/DIV (single IEEE 754 instructions);
// other arithmetic families take the host-helper slow path (byte-equal to the
// interpreter, no speedup but a correctness fallback).
//
// **True physical darwin/arm64 hookup** (2026-06-30): previously the stub always
// returned (0, false), so compileSpecArith never entered the
// useSpec/useSpecRegK/useSpecChain branches, and all PJ2 spec tests had 0 hits on
// physical M1. This batch mirrors the amd64 4-way dispatch (the constant bytes
// reuse the amd64 SSE op values, later translated via arm64ArithOpSelForSseOp).
func archSseOpForArith(op uint8) (byte, bool) {
	switch bytecode.OpCode(op) {
	case bytecode.ADD:
		return 0x58, true // SseOpAddsd / arm64ArithOpSelForSseOp → ArithOpAddArm64
	case bytecode.SUB:
		return 0x5C, true // SseOpSubsd / → ArithOpSubArm64
	case bytecode.MUL:
		return 0x59, true // SseOpMulsd / → ArithOpMulArm64
	case bytecode.DIV:
		return 0x5E, true // SseOpDivsd / → ArithOpDivArm64
	default:
		return 0, false
	}
}

// arm64ArithOpSelForSseOp translates an amd64 SSE opcode byte (the xx of
// F2 0F xx ModRM) into the opSel byte of the arm64 PJ2 speculative template
// (used by EmitArithSpeculativeBinopWithGuardArm64). Follows the same
// jitarm64.ArithOp*Arm64 constant definitions.
//
//	0x58 ADDSD → ArithOpAddArm64 (0x28)
//	0x5C SUBSD → ArithOpSubArm64 (0x38)
//	0x59 MULSD → ArithOpMulArm64 (0x08)
//	0x5E DIVSD → ArithOpDivArm64 (0x18)
//
// Returns (opSel, true) on a match, (0, false) if unrecognized (the caller
// should silently give up).
func arm64ArithOpSelForSseOp(sseOp byte) (uint8, bool) {
	switch sseOp {
	case 0x58: // ADDSD
		return jitarm64.ArithOpAddArm64, true
	case 0x5C: // SUBSD
		return jitarm64.ArithOpSubArm64, true
	case 0x59: // MULSD
		return jitarm64.ArithOpMulArm64, true
	case 0x5E: // DIVSD
		return jitarm64.ArithOpDivArm64, true
	default:
		return 0, false
	}
}

// archEmitArithSpecBinopWithGuard is the arm64 hookup of the PJ2 speculative
// reg-reg template (108 bytes, mirroring the amd64
// EmitArithSpeculativeBinopWithGuard 92 bytes; arm64 is 16 bytes larger due to
// RISC fixed-length encoding).
//
// **Hookup path not yet live** (this batch only exposes the byte-level template
// proxy; Compile dispatch is still blocked by archSupportsSpec()=false):
//   - true enablement requires the archCallJITSpec arm64 spec-trampoline
//     implementation + archSupportsSpec() flipping to true (deferred to the same
//     PJ8+ batch);
//   - the byte-level template and its unit tests are already in place; this batch
//     is pure wiring to reduce the future hookup effort.
//
// sseOp is auto-translated: 0x58/0x5C/0x59/0x5E → arm64 ArithOpAdd/Sub/Mul/Div;
// an unrecognized op silently returns the original buf (matching the amd64 stub's
// give-up semantics).
func archEmitArithSpecBinopWithGuard(buf []byte, sseOp byte, a, b, c uint8, deoptCode uint64) []byte {
	opSel, ok := arm64ArithOpSelForSseOp(sseOp)
	if !ok {
		return buf
	}
	return jitarm64.EmitArithSpeculativeBinopWithGuardArm64(buf, opSel, a, b, c, deoptCode)
}

// archEmitArithSpecBinopRegKWithGuard is the arm64 hookup of the PJ2 speculative
// reg-K template (92 bytes, mirroring the amd64
// EmitArithSpeculativeBinopRegKWithGuard 73 bytes; arm64 is 19 bytes larger).
// sseOp is auto-translated amd64→arm64 opSel.
//
// Same wiring path as the reg-reg WithGuard; true enablement still requires the
// archCallJITSpec spec-trampoline implementation + archSupportsSpec() flipping to
// true (deferred to PJ8+).
func archEmitArithSpecBinopRegKWithGuard(buf []byte, sseOp byte, a, b uint8, kvalue, deoptCode uint64) []byte {
	opSel, ok := arm64ArithOpSelForSseOp(sseOp)
	if !ok {
		return buf
	}
	return jitarm64.EmitArithSpeculativeBinopRegKWithGuardArm64(buf, opSel, a, b, kvalue, deoptCode)
}

// archEmitArithSpecChainKKWithGuard is the arm64 hookup of the PJ2 speculative
// chain-KK template (116 bytes, mirroring the amd64
// EmitArithSpeculativeChainKKWithGuard 92 bytes; arm64 is 24 bytes larger). Both
// sseOps are translated; if either is unrecognized it silently gives up.
func archEmitArithSpecChainKKWithGuard(buf []byte, sseOp1, sseOp2 byte, a, b uint8, k1value, k2value, deoptCode uint64) []byte {
	opSel1, ok1 := arm64ArithOpSelForSseOp(sseOp1)
	opSel2, ok2 := arm64ArithOpSelForSseOp(sseOp2)
	if !ok1 || !ok2 {
		return buf
	}
	return jitarm64.EmitArithSpeculativeChainKKWithGuardArm64(buf, opSel1, opSel2,
		a, b, k1value, k2value, deoptCode)
}

// archEmitForLoopEmptyConst is the arm64 hookup of the PJ3 FORLOOP empty-body
// template (84 bytes without safepoint / 92 bytes with; preemptFlagOff < 0 skips
// the safepoint, >= 0 enables it). Mirrors the amd64 EmitForLoopEmptyConst
// 69/83 bytes; arm64 is 15 bytes larger overall due to the 16B MOV imm64 sequence
// vs amd64's 15B movq accumulating with RISC fixed-length encoding.
//
// **loopFuel (issue #143)**: the arm64 templates now emit the loopFuel back-edge
// machinery when loopFuelOff >= 0 (loopFuelCode != 0), mirroring the amd64 side.
func archEmitForLoopEmptyConst(buf []byte, kInit, kLimit, kStep uint64,
	preemptFlagOff, loopFuelOff, loopSpillOff int32, loopFuelCode uint64) ([]byte, int) {
	var fuelOff, spillOff uint16
	var fuelCode uint64
	if loopFuelOff >= 0 {
		fuelOff = uint16(loopFuelOff)
		spillOff = uint16(loopSpillOff)
		fuelCode = loopFuelCode
	}
	return jitarm64.EmitForLoopEmptyConstArm64(buf, kInit, kLimit, kStep, preemptFlagOff,
		fuelOff, spillOff, fuelCode)
}

// archEmitForLoopRegLimit is the arm64 hookup of the PJ3 FORLOOP reg-limit
// template (120 bytes without safepoint / 128 bytes with). Mirrors the amd64
// EmitForLoopRegLimit 103/117 bytes; arm64 is 17/11 bytes larger (16B MOV imm64
// sequence vs amd64's 15B movq accumulating with RISC fixed-length encoding).
//
// guard: LDR R(limitReg) → CMP qNanBoxBase → B.HS deopt (if not a number).
func archEmitForLoopRegLimit(buf []byte, kInit, kStep uint64, limitReg uint8, deoptCode uint64,
	preemptFlagOff, loopFuelOff, loopSpillOff int32, loopFuelCode uint64) ([]byte, int) {
	var fuelOff, spillOff uint16
	var fuelCode uint64
	if loopFuelOff >= 0 {
		fuelOff = uint16(loopFuelOff)
		spillOff = uint16(loopSpillOff)
		fuelCode = loopFuelCode
	}
	return jitarm64.EmitForLoopRegLimitArm64(buf, kInit, kStep, limitReg, deoptCode, preemptFlagOff,
		fuelOff, spillOff, fuelCode)
}

// archEmitForLoopWithBody is the arm64 hookup of the PJ3 FORLOOP body template in
// the reg-K op form (144 bytes without safepoint / 152 bytes with). Mirrors the
// amd64 EmitForLoopWithRegKBody 121/135 bytes; arm64 is 23/17 bytes larger
// (MOV imm64 sequence + FMOV shuttling GP↔FP).
//
// sseOp is auto-translated: 0x58/0x5C/0x59/0x5E → arm64 FADD/FSUB/FMUL/FDIV.
func archEmitForLoopWithBody(buf []byte, kS, kInit, kLimit, kStep, kBody uint64,
	aS uint8, sseOp byte,
	preemptFlagOff, loopFuelOff, loopSpillOff int32, loopFuelCode uint64) ([]byte, int) {
	var fuelOff, spillOff uint16
	var fuelCode uint64
	if loopFuelOff >= 0 {
		fuelOff = uint16(loopFuelOff)
		spillOff = uint16(loopSpillOff)
		fuelCode = loopFuelCode
	}
	return jitarm64.EmitForLoopWithRegKBodyArm64(buf, kS, kInit, kLimit, kStep, kBody,
		aS, sseOp, preemptFlagOff, fuelOff, spillOff, fuelCode)
}

// archEmitForLoopWithBody2 is the arm64 hookup of the PJ3 FORLOOP two-stage body
// template (168 bytes without safepoint / 176 bytes with). Mirrors the amd64
// EmitForLoopWithRegKBody2 140/154 bytes; arm64 is 28/22 bytes larger (MOV imm64
// accumulating + FMOV shuttling).
//
// The two-stage body shares the d3 register (mirroring amd64's xmm3), saving one
// LDR/STR R(aS) round-trip (8 bytes / iter).
func archEmitForLoopWithBody2(buf []byte, kS, kInit, kLimit, kStep, kBody1, kBody2 uint64,
	aS uint8, sseOp1, sseOp2 byte,
	preemptFlagOff, loopFuelOff, loopSpillOff int32, loopFuelCode uint64) ([]byte, int) {
	var fuelOff, spillOff uint16
	var fuelCode uint64
	if loopFuelOff >= 0 {
		fuelOff = uint16(loopFuelOff)
		spillOff = uint16(loopSpillOff)
		fuelCode = loopFuelCode
	}
	return jitarm64.EmitForLoopWithRegKBody2Arm64(buf, kS, kInit, kLimit, kStep,
		kBody1, kBody2, aS, sseOp1, sseOp2, preemptFlagOff, fuelOff, spillOff, fuelCode)
}

// archEmitGetTableArrayHit is the arm64 PJ4 IC ArrayHit byte-level direct-slot
// template (168 bytes: strict IsTable guard + SIB replacement + gen check +
// array direct hit + nil check + write R(A) + deopt block). The arm64 side
// proxies jitarm64.EmitGetTableArrayHitArm64.
//
// **Hookup vs amd64 differences**: the arm64 side follows the trampoline_arm64.s
// protocol (x26=vsBase / x27=jitContext / x28=Go G / x14=arena base); the template
// is 36 bytes longer than amd64's 132 due to the SIB replacement (ADD+LDR
// replacing a single SIB ldr) and the MOV imm64 sequence (movz+movk×3). The
// arenaBaseOff signature is int32 on amd64 but uint16 on arm64 because arm64 LDR
// uses an unsigned 12-bit scaled offset (int32 safely converts to uint16, since
// the jitContext field offset is on the order of tens of bytes).
func archEmitGetTableArrayHit(buf []byte, aReg, bReg uint8, stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitGetTableArrayHitArm64(buf, aReg, bReg,
		stableShape, stableIndex, arenaBaseOffArm64(arenaBaseOff), deoptCode)
}

// archEmitGetTableNodeHit is the arm64 PJ4 IC NodeHit byte-level direct-slot
// template (196 bytes: IsTable guard + SIB + gen check + nodeRef +
// node[stableIndex] + key compare + NodeVal load + nil check + write R(A) +
// deopt block).
func archEmitGetTableNodeHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitGetTableNodeHitArm64(buf, aReg, bReg,
		stableShape, stableIndex, stableKey, arenaBaseOffArm64(arenaBaseOff), deoptCode)
}

// archEmitSetTableArrayHit is the arm64 PJ4 SETTABLE IC ArrayHit byte-level
// reverse-write template (144 bytes).
func archEmitSetTableArrayHit(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitSetTableArrayHitArm64(buf, aReg, cReg,
		stableShape, stableIndex, arenaBaseOffArm64(arenaBaseOff), deoptCode)
}

// archEmitSelfArrayHit is the arm64 PJ4 SELF IC ArrayHit byte-level inline
// template (172 bytes: GETTABLE ArrayHit 168 + R(A+1) copy segment 4).
func archEmitSelfArrayHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitSelfArrayHitArm64(buf, aReg, bReg,
		stableShape, stableIndex, arenaBaseOffArm64(arenaBaseOff), deoptCode)
}

// archEmitSetTableNodeHit is the arm64 PJ4 SETTABLE IC NodeHit byte-level
// reverse-write template (172 bytes).
func archEmitSetTableNodeHit(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitSetTableNodeHitArm64(buf, aReg, cReg,
		stableShape, stableIndex, stableKey, arenaBaseOffArm64(arenaBaseOff), deoptCode)
}

// archEmitSelfNodeHit is the arm64 PJ4 SELF IC NodeHit byte-level inline
// template (200 bytes: GETTABLE NodeHit 196 + R(A+1) copy segment 4).
func archEmitSelfNodeHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitSelfNodeHitArm64(buf, aReg, bReg,
		stableShape, stableIndex, stableKey, arenaBaseOffArm64(arenaBaseOff), deoptCode)
}

// archEmitSelfNodeHitNoRet is the arm64 implementation of the NoRet variant
// (following the C5 commit wrap-up PR comment 8b4ff8e placeholder-backfill lesson):
// same as archEmitSelfNodeHit but **the success path does not emit ret**; instead
// it emits a B (unconditional jump) to skip the deopt block, falling through at
// the segment tail into the BuildVoid0Arg segment emitted by the caller.
//
// **Hookup path**: in the archEmitFrameInlineExitHelperRequest + archCallJITSpec
// form, after the SELF segment succeeds it falls through into BuildVoid0Arg +
// ExitHelperRequest + PopVoid0Arg + ret; enabled once archSupportsFrameInline
// flips to true.
//
// **vs the original panic placeholder** (retired): the old panic killed the entire
// NoRet path when archSupportsFrameInline=true; this implementation replaces it so
// SELF NodeHit is byte-equal to amd64's EmitSelfNodeHitNoRet in the useFrameInline
// form (same 200 bytes, with the 4B RET swapped for a 4B B).
func archEmitSelfNodeHitNoRet(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitSelfNodeHitNoRetArm64(buf, aReg, bReg,
		stableShape, stableIndex, stableKey,
		arenaBaseOffArm64(arenaBaseOff), deoptCode)
}

// archEmitSpecArgLoadK / archEmitSpecArgLoadReg are the arm64 implementations
// (following the PJ5 spec-template byte-level inline, which skips the
// host.GetReg/SetReg round-trip). They become active once the physical runner
// enables archSupportsSpec=true, on par with the amd64 byte-level form.
func archEmitSpecArgLoadK(buf []byte, dstReg uint8, k uint64) []byte {
	return jitarm64.EmitSpecArgLoadKArm64(buf, dstReg, k)
}
func archEmitSpecArgLoadReg(buf []byte, dstReg uint8, srcReg uint8) []byte {
	return jitarm64.EmitSpecArgLoadRegArm64(buf, dstReg, srcReg)
}

// archSupportsSpec: the arm64 PJ8 engineering components are fully ready, so
// linux/arm64 flips to true to let the Compile side take the useSpec real path;
// darwin/arm64 previously returned false (the F3-#3 physical macos-latest M1
// showed SIGSEGV at PC=0x2000, i.e. trampoline_arm64.s jumped into the mmap
// segment and physical execution crashed; the root cause isolate was left as
// F3-#3b, see codepage_darwin_test.go ExecSanityProbe and trampoline_test.go
// darwin skip).
//
// Status distribution:
//   - amd64: goes through arch_amd64.go (not in this file)
//   - linux/arm64: ✅ flips to true (following C7 + tmp/wangshu-p4-todo.md §two)
//   - darwin/arm64: ✅ flips to true (after the F3-#3b trampoline_arm64.s STP/LDP
//     offset +8 fix for the LR-slot overwrite bug, physical M1 validation fully
//     passes; see the trampoline_arm64.s header comment + commit message)
//
// **Activation scope**: on the Compile side the PJ2 three speculative forms + the
// PJ3 FORLOOP body/body2/RegLimit three paths go through archCallJITSpec; the PJ4
// IC six templates keep going through archCallJITFull (already enabled); PJ5 SELF
// spec template + useFrameInline.
func archSupportsSpec() bool {
	return true
}

// archSupportsForLoop: the arm64 PJ3 FORLOOP templates are already hooked up (in
// this session PJ8 arm64 PJ3 all four forms: EmptyConst 84/92B / RegLimit
// 120/128B / WithRegKBody 144/152B / WithRegKBody2 168/176B, all byte-level unit
// tests pass); FORLOOP goes through the archCallJITFull main path, not the spec
// trampoline.
//
// **darwin/arm64 flips to true like archSupportsSpec** (after the F3-#3b
// trampoline fix).
func archSupportsForLoop() bool {
	return true
}

// archEmitHelperCall emits the generic helper-call macro (arm64 side: `mov X16,
// helperAddr imm64 + blr X16`, 20 bytes). Mirrors the amd64 archEmitHelperCall
// (12 bytes).
//
// Used by the PJ5 CALL/TAILCALL hookup (enabled once archSupportsSpec flips at
// PJ8+). arm64 is 8 bytes larger due to the MOV imm64 sequence 16 vs amd64's
// mov rax imm64 10 + BLR vs CALL reg 2. X16 is the ARMv8 IP0 scratch register
// (intra-procedure-call scratch, freely clobberable by the callee).
func archEmitHelperCall(buf []byte, helperAddr uint64) []byte {
	return jitarm64.EmitHelperCallArm64(buf, helperAddr)
}

// archEncodedHelperCallLen is the byte count of the generic helper-call macro
// (arm64 = 20, mirroring amd64 = 12). The caller uses it for inline CALL template
// length budgeting (the arm64 side is 8 bytes larger than amd64 due to RISC
// fixed-length encoding).
const archEncodedHelperCallLen = jitarm64.EncodedHelperCallArm64Len

// archSupportsFrameInline is the arm64 C7 gate: linux/arm64 + darwin/arm64 both
// return true to allow the useFrameInline real path (equivalent on both after the
// F3-#3b trampoline fix).
//
// **Dependency closure** (C5/C6 already delivered):
//   - archEmitSelfNodeHitNoRet: C5 real implementation replacing the panic placeholder
//   - archEmitFrameInlineExitHelperRequest: C6 real implementation replacing the 0-byte placeholder
//   - archEncodedFrameInlineExitHelperRequestLen: C6 from 0 → 36
func archSupportsFrameInline() bool {
	return true
}

// archEmitFrameInlineBuildVoid0ArgSkeleton is the arm64 proxy for the same-name
// jitarm64 helper (172-byte Absolute version, following §9.20 Option B Spike 1 +
// §9.20.9 commit-5l ciSegBase mirror-word semantics bug fix). The Absolute version
// appends `ldr x14, [x27+arenaBaseOff] + add x0, x0, x14` inside LoadCISlotAddr so
// x0 is an absolute address, avoiding the bug where a word offset cannot be
// dereferenced (mirroring the amd64 Absolute version; physical darwin/arm64
// macos-latest CI proved it fixes the in-segment SIGSEGV of PJ5 SelfCall
// SpecTemplate).
//
// arm64 offset uses the uint16 form (LDR Xt, [Xn, pimm] encoding restriction:
// pimm must be 0..32760 and 8-aligned). **The arenaBase offset is checked via
// arenaBaseOffArm64()** (following PR #28 review: a helper that bypasses the check
// would silently break when a future field reordering pushes arenaBase to ≥32760,
// see the arenaBaseOffArm64 comment).
func archEmitFrameInlineBuildVoid0ArgSkeleton(buf []byte,
	ciDepthAddrOff, ciSegBaseAddrOff int32, callARecv uint8,
	w0, w1, w2, w4 uint64) []byte {
	arenaBaseOff := arenaBaseOffArm64(int32(JITContextArenaBaseOffset))
	return jitarm64.EmitFrameInlineBuildVoid0ArgSkeletonAbsoluteArm64(buf,
		uint16(ciDepthAddrOff), uint16(ciSegBaseAddrOff), arenaBaseOff, callARecv,
		jitarm64.FrameInlineCISlotWordsArm64{Word0: w0, Word1: w1, Word2: w2, Word3: 0, Word4: w4})
}

// archEmitFrameInlinePopVoid0ArgSkeleton is the arm64 proxy for the same-name
// jitarm64 helper (24 bytes, = CIDepthDec 16 + movz w0 #0 4 + ret 4, following
// the F3-#3b §9.20.9 commit-5l missing-ret bug fix).
func archEmitFrameInlinePopVoid0ArgSkeleton(buf []byte, ciDepthAddrOff int32) []byte {
	return jitarm64.EmitFrameInlinePopVoid0ArgSkeletonArm64(buf, uint16(ciDepthAddrOff))
}

// archEmitFrameInlineExitHelperRequest is the arm64 implementation of the Spike 1
// exit-helper-request segment (following the C6 commit placeholder backfill, and
// the §9.20.9 (4) trampoline exit-resume protocol arm64 counterpart).
//
// Byte sequence (36 bytes, mirroring amd64's 24 bytes):
//   - movz/movk x16, helperCode imm64 (16B)
//   - str x16, [x27 + exitArg0Off] (4B, 64-bit STR)
//   - movz w16, #3 (4B, ExitInlineHelper)
//   - str w16, [x27 + exitReasonOff] (4B, 32-bit STR)
//   - movz w0, #3 (4B, set the return value; the trampoline checks X0)
//   - ret (4B)
//
// **Enabled once archSupportsFrameInline flips to true** (C7); this commit
// replaces the original 0-byte placeholder with the real implementation, with the
// Compile/Run hookup + physical runner end-to-end validation left to C7 + the
// subsequent PJ8 macos-latest CI run.
func archEmitFrameInlineExitHelperRequest(buf []byte,
	exitReasonOff, exitArg0Off int32, helperCode uint64) []byte {
	return jitarm64.EmitFrameInlineExitHelperRequestArm64(buf,
		exitReasonOff, exitArg0Off, helperCode)
}

// archEncodedFrameInlineBuildVoid0ArgSkeletonLen is the byte count of the arm64
// Spike 1 enterLuaFrame skeleton (172 bytes for the Absolute version, = the
// original 164 + 8 extra bytes for the Absolute load).
const archEncodedFrameInlineBuildVoid0ArgSkeletonLen = jitarm64.EncodedFrameInlineBuildVoid0ArgSkeletonAbsoluteArm64Len

// archEncodedFrameInlinePopVoid0ArgSkeletonLen is the byte count of the arm64
// Spike 1 popCallInfo skeleton (24 = CIDepthDec 16 + movz w0 #0 4 + ret 4,
// following the F3-#3b §9.20.9 commit-5l missing-ret bug fix).
const archEncodedFrameInlinePopVoid0ArgSkeletonLen = jitarm64.EncodedFrameInlinePopVoid0ArgSkeletonArm64Len

// archEncodedFrameInlineExitHelperRequestLen is the byte count of the arm64
// Spike 1 exit-helper-request segment (36, mirroring amd64's 24 bytes; arm64 is
// 12 bytes larger due to fixed-length encoding + no register reuse). Following the
// C6 jitarm64.EmitFrameInlineExitHelperRequestArm64 real implementation.
const archEncodedFrameInlineExitHelperRequestLen = jitarm64.EncodedFrameInlineExitHelperRequestArm64Len
