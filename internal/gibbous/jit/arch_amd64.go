//go:build wangshu_p4 && amd64

// arch_amd64.go — P4 PJ8 arch-routing amd64 implementation (mirrors arch_arm64.go).
//
// Moves the jitamd64 dependency hardcoded in compiler.go / code.go into this
// arch adapter layer, so the jit package body no longer depends on a concrete
// GOARCH; under an arm64 build it automatically switches to jitarm64.
//
// Follows the docs/design/p4-method-jit/06-backends.md §1 "shared skeleton +
// per-arch emitter" decision: one per-arch emit function per build tag, while
// the jit main package imports a neutral interface.
package jit

import (
	"github.com/Liam0205/wangshu/internal/bytecode"
	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"
)

// archCodePage is the arch-abstracted executable segment; under this build it
// aliases jitamd64.CodePage.
type archCodePage = jitamd64.CodePage

// archEmitLoadKReturn emits the straight-line template "mov RAX, value; ret"
// (11 bytes on amd64). The constant family (LOADK/LOADBOOL/LOADNIL) bakes in
// the NaN-boxed value; for the prelude / comparison-folding family RAX is a
// dummy (ignored by the Run side), but value must still be written (the
// template byte count is fixed).
func archEmitLoadKReturn(buf []byte, value uint64) []byte {
	buf = jitamd64.EmitMovRaxImm64(buf, value)
	buf = jitamd64.EmitRet(buf)
	return buf
}

// archMmapCode writes code into a W^X segment (PROT_RW alloc → copy → flip to
// PROT_RX).
func archMmapCode(code []byte) (*archCodePage, error) {
	return jitamd64.MmapCode(code)
}

// archCallJITFull jumps into the mmap segment (full trampoline: save
// callee-saved registers + load jitContext into r15 + CALL + restore).
// Returns RAX.
func archCallJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64 {
	return jitamd64.CallJITFull(codeAddr, jitCtxAddr)
}

// archCallJITSpec jumps into a PJ2 speculative-template mmap segment (the
// callJITSpec trampoline simultaneously loads r15=jitContext + rbx=
// valueStackBase). Returns RAX (the value of the segment's last mov/movsd, or
// the deoptCode baked into the deopt block).
//
// Use case: real integration of PJ2 speculative templates (ADD/SUB/MUL/DIV
// dual-number fast path).
func archCallJITSpec(codeAddr uintptr, jitCtxAddr uintptr, vsBase uintptr) uint64 {
	return jitamd64.CallJITSpec(codeAddr, jitCtxAddr, vsBase)
}

// archSseOpForArith maps a Lua arithmetic opcode to an SSE binop opcode byte.
// Unsupported ops (MOD/POW — MOD uses floor-mod rather than a single SSE
// instruction, POW uses a pow() helper) return (0, false).
//
// **Per 03-speculation-ic.md §2 speculation whitelist**: the f64 fast-path
// speculation only holds for ADD/SUB/MUL/DIV (single IEEE 754 SSE
// instructions); other arithmetic ops take the host-helper slow path
// (byte-equal with the interpreter, no speedup but correctness fallback).
func archSseOpForArith(op uint8) (byte, bool) {
	switch bytecode.OpCode(op) {
	case bytecode.ADD:
		return jitamd64.SseOpAddsd, true
	case bytecode.SUB:
		return jitamd64.SseOpSubsd, true
	case bytecode.MUL:
		return jitamd64.SseOpMulsd, true
	case bytecode.DIV:
		return jitamd64.SseOpDivsd, true
	default:
		return 0, false
	}
}

// archEmitArithSpecBinopWithGuard splices the byte-level sequence of the PJ2
// BINOP speculative template (IsNumber×2 guard + dual-number fast path + deopt
// block), a generic version — sseOp is chosen by the caller via
// archSseOpForArith. On amd64 it delegates to
// jitamd64.EmitArithSpeculativeBinopWithGuard (92 bytes, independent of op).
func archEmitArithSpecBinopWithGuard(buf []byte, sseOp byte, a, b, c uint8, deoptCode uint64) []byte {
	return jitamd64.EmitArithSpeculativeBinopWithGuard(buf, sseOp, a, b, c, deoptCode)
}

// archEmitArithSpecBinopRegKWithGuard splices the PJ2 reg-K form speculative
// template (B is a reg + K bakes in an imm64 at compile time, single guard on
// the reg side). On amd64 it delegates to
// jitamd64.EmitArithSpeculativeBinopRegKWithGuard (73 bytes).
func archEmitArithSpecBinopRegKWithGuard(buf []byte, sseOp byte, a, b uint8, kvalue uint64, deoptCode uint64) []byte {
	return jitamd64.EmitArithSpeculativeBinopRegKWithGuard(buf, sseOp, a, b, kvalue, deoptCode)
}

// archEmitArithSpecChainKKWithGuard splices the PJ2 two-stage chained reg-K-K
// speculative template (`R(A) = R(B) op1 K1 op2 K2`). On amd64 it delegates to
// the 92-byte chain template.
func archEmitArithSpecChainKKWithGuard(buf []byte, sseOp1, sseOp2 byte, a, b uint8, k1value, k2value, deoptCode uint64) []byte {
	return jitamd64.EmitArithSpeculativeChainKKWithGuard(buf, sseOp1, sseOp2, a, b, k1value, k2value, deoptCode)
}

// archEmitForLoopEmptyConst splices the PJ3 all-constant init/limit/step
// empty-body FORLOOP template (69 bytes bare; + safepoint 83; + loopFuel
// machinery 162 — float idx accumulation + ucomisd limit + backward jcc +
// optional r15+disp byte-cmp safepoint check + issue #143 loopFuel back-edge
// accounting). On amd64 it delegates to jitamd64.EmitForLoopEmptyConst.
//
// When preemptFlagOff >= 0 the template includes the safepoint check (per V18
// -race preemption discipline); when < 0 it is omitted (unit-test / spike use
// cases). When loopFuelOff >= 0 the back-edge decrements jitCtx.loopFuel and
// on exhaustion spills xmm0/1/2 to the loopSpill slots + returns loopFuelCode
// in RAX; the returned resume offset re-enters after host.LoopPreempt.
func archEmitForLoopEmptyConst(buf []byte, kInit, kLimit, kStep uint64,
	preemptFlagOff, loopFuelOff, loopSpillOff int32, loopFuelCode uint64) ([]byte, int) {
	return jitamd64.EmitForLoopEmptyConst(buf, kInit, kLimit, kStep,
		preemptFlagOff, loopFuelOff, loopSpillOff, loopFuelCode)
}

// archEmitForLoopRegLimit splices the PJ3 reg-limit empty-body FORLOOP
// template (the hot-path form `for i=1, n do end`): IsNumber guard + float
// loop + optional safepoint + issue #143 loopFuel back-edge + deopt block. On
// amd64 it delegates to jitamd64.EmitForLoopRegLimit.
func archEmitForLoopRegLimit(buf []byte, kInit, kStep uint64, limitReg uint8, deoptCode uint64,
	preemptFlagOff, loopFuelOff, loopSpillOff int32, loopFuelCode uint64) ([]byte, int) {
	return jitamd64.EmitForLoopRegLimit(buf, kInit, kStep, limitReg, deoptCode,
		preemptFlagOff, loopFuelOff, loopSpillOff, loopFuelCode)
}

// archEmitForLoopWithBody splices the PJ3 FORLOOP template for a body
// containing a reg-K op (`local s=K_s; for i=K1,K2 do s = s op K3 end; return
// s`). 135 bytes including the safepoint check; 214 with the issue #143
// loopFuel machinery.
func archEmitForLoopWithBody(buf []byte, kS, kInit, kLimit, kStep, kBody uint64,
	aS uint8, sseOp byte,
	preemptFlagOff, loopFuelOff, loopSpillOff int32, loopFuelCode uint64) ([]byte, int) {
	return jitamd64.EmitForLoopWithRegKBody(buf, kS, kInit, kLimit, kStep, kBody, aS, sseOp,
		preemptFlagOff, loopFuelOff, loopSpillOff, loopFuelCode)
}

// archEmitForLoopWithBody2 splices the PJ3 FORLOOP two-stage body template
// (`local s; for i=K1,K2 do s = s op1 K3; s = s op2 K4 end; return s`).
// 154 bytes with the safepoint, reusing xmm3 across both stages to save one
// load/store; 233 with the issue #143 loopFuel machinery.
func archEmitForLoopWithBody2(buf []byte, kS, kInit, kLimit, kStep, kBody1, kBody2 uint64,
	aS uint8, sseOp1, sseOp2 byte,
	preemptFlagOff, loopFuelOff, loopSpillOff int32, loopFuelCode uint64) ([]byte, int) {
	return jitamd64.EmitForLoopWithRegKBody2(buf, kS, kInit, kLimit, kStep, kBody1, kBody2, aS, sseOp1, sseOp2,
		preemptFlagOff, loopFuelOff, loopSpillOff, loopFuelCode)
}

// archEmitGetTableArrayHit splices the PJ4 IC ArrayHit byte-level direct-slot
// template (132 bytes: IsTable guard + arena base load + gen check + array
// direct hit + nil check + write R(A) + deopt block). On amd64 it delegates to
// jitamd64.EmitGetTableArrayHit.
func archEmitGetTableArrayHit(buf []byte, aReg, bReg uint8, stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	return jitamd64.EmitGetTableArrayHit(buf, aReg, bReg, stableShape, stableIndex, arenaBaseOff, deoptCode)
}

// archEmitGetTableNodeHit splices the PJ4 IC NodeHit byte-level direct-slot
// template (159 bytes: strict IsTable guard + arena base + gen check + nodeRef +
// node[stableIndex] + key compare + NodeVal load + nil check + write R(A) +
// deopt block). On amd64 it delegates to jitamd64.EmitGetTableNodeHit.
//
// Key differences from ArrayHit:
//   - reads word3=nodeRef (offset 24) instead of word2=arrayRef (offset 16)
//   - node stride is 24 bytes (nodeWords=3) instead of array's 8 bytes
//   - adds a key compare (NodeKey == stableKey, guarding against key
//     degradation / __index chains)
func archEmitGetTableNodeHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitamd64.EmitGetTableNodeHit(buf, aReg, bReg,
		stableShape, stableIndex, stableKey, arenaBaseOff, deoptCode)
}

// archEmitSetTableArrayHit splices the PJ4 SETTABLE IC ArrayHit byte-level
// reverse-write template (113 bytes: strict IsTable guard + arena base +
// gen check + arrayRef + load R(C) value → rdx + reverse store
// [r14+rcx+stableIndex*8] from rdx + ret + deopt block). On amd64 it delegates
// to jitamd64.EmitSetTableArrayHit.
//
// **setter form**: retB=1 (SETTABLE has 0 return values), the Run side does not
// write R(A).
func archEmitSetTableArrayHit(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	return jitamd64.EmitSetTableArrayHit(buf, aReg, cReg,
		stableShape, stableIndex, arenaBaseOff, deoptCode)
}

// archEmitSelfArrayHit splices the PJ4 SELF IC ArrayHit byte-level inline
// template (139 bytes: GETTABLE ArrayHit 132 + a 7-byte R(A+1) copy segment).
// On amd64 it delegates to jitamd64.EmitSelfArrayHit.
//
// **SELF form**: R(A+1) := R(B); R(A) := R(B)[K]. The template first stores
// R(A+1) = R(B) at entry, then runs the same GETTABLE ArrayHit flow to fetch
// R(A).
func archEmitSelfArrayHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	return jitamd64.EmitSelfArrayHit(buf, aReg, bReg,
		stableShape, stableIndex, arenaBaseOff, deoptCode)
}

// archEmitSetTableNodeHit splices the PJ4 SETTABLE IC NodeHit byte-level
// reverse-write template (140 bytes: GetTable NodeHit 159 - getter segment 34 +
// setter segment 15). On amd64 it delegates to jitamd64.EmitSetTableNodeHit.
//
// **setter NodeHit form**: hash-segment NodeKey compare + reverse-write NodeVal.
func archEmitSetTableNodeHit(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitamd64.EmitSetTableNodeHit(buf, aReg, cReg,
		stableShape, stableIndex, stableKey, arenaBaseOff, deoptCode)
}

// archEmitSelfNodeHit splices the PJ4 SELF IC NodeHit byte-level inline
// template (166 bytes: SELF ArrayHit 139 + a 27-byte key compare). On amd64 it
// delegates to jitamd64.EmitSelfNodeHit.
//
// **SELF NodeHit form**: R(A+1) := R(B); R(A) := R(B)[K_string]
// (via a hash-segment NodeKey compare + NodeVal load). This is the typical IC
// form for real-world `obj:method()` calls (where method is a string ident).
func archEmitSelfNodeHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitamd64.EmitSelfNodeHit(buf, aReg, bReg,
		stableShape, stableIndex, stableKey, arenaBaseOff, deoptCode)
}

// archEmitSelfNodeHitNoRet is the same as archEmitSelfNodeHit but its success
// path does not ret (per §9.20.9 commit-5j, which fixed the useFrameInline path
// — it falls through to the BuildVoid0Arg segment).
func archEmitSelfNodeHitNoRet(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitamd64.EmitSelfNodeHitNoRet(buf, aReg, bReg,
		stableShape, stableIndex, stableKey, arenaBaseOff, deoptCode)
}

// archEmitSpecArgLoadK / archEmitSpecArgLoadReg are the arm-routed amd64
// implementations (per PJ5, they byte-level inline the SELF + CALL spec template
// arg loads, skipping the host.GetReg/SetReg round-trip). On arm64 these are
// stubs (until the physical runner is enabled in PJ8+); other arches, like the
// amd64 fallback, take the non-spec path.
func archEmitSpecArgLoadK(buf []byte, dstReg uint8, k uint64) []byte {
	return jitamd64.EmitSpecArgLoadK(buf, dstReg, k)
}
func archEmitSpecArgLoadReg(buf []byte, dstReg uint8, srcReg uint8) []byte {
	return jitamd64.EmitSpecArgLoadReg(buf, dstReg, srcReg)
}

// archSupportsSpec returns true when this arch supports the live PJ2
// speculative template path. amd64 ✅; arm64/others ❌ (deferred to PJ8+).
func archSupportsSpec() bool { return true }

// archSupportsForLoop returns true when this arch supports the live PJ3 FORLOOP
// template path (via the archCallJITFull main path, not the spec trampoline).
// amd64 ✅ (already enabled via archSupportsSpec; this function provides a
// decoupled switch for new arches); arm64 ✅ (this session completed the live
// byte-level templates for all four PJ8 arm64 PJ3 forms).
func archSupportsForLoop() bool { return true }

// archEmitHelperCall emits the generic helper-call macro (on amd64: `mov rax,
// helperAddr imm64 + call rax`, 12 bytes). It mirrors arm64's
// EmitHelperCallArm64 (20 bytes).
//
// Used by the live PJ5 CALL/TAILCALL path + the PJ4 deopt path when calling host
// helpers (host.DoCall / host.GetTable / host.Arith, etc.). helperAddr is the
// helper function's physical address (resolved at jit Compile time via
// reflect.ValueOf(fn).Pointer()).
//
// **Integration path**: this function currently has no caller; it is kept as
// engineering groundwork for the live PJ5 path — the next step is to call this
// macro when embedding archEmitHelperCall into the inline CALL template.
func archEmitHelperCall(buf []byte, helperAddr uint64) []byte {
	return jitamd64.EmitHelperCall(buf, helperAddr)
}

// archEncodedHelperCallLen is the byte length of the generic helper-call macro
// (amd64 = 12, arm64 = 20). Callers use it for inline CALL template length
// budgeting.
const archEncodedHelperCallLen = jitamd64.EncodedHelperCallLen

// archEmitFrameInlineBuildVoid0ArgSkeleton splices the amd64 Spike 1
// enterLuaFrame byte-level inline skeleton (120 bytes, Absolute variant,
// per section 9.20.9 commit-5l bug fix + jitamd64.EmitFrameInlineBuild
// Void0ArgSkeletonAbsolute; authoritative length constant is
// jitamd64.EncodedFrameInlineBuildVoid0ArgSkeletonLen exposed below as
// archEncodedFrameInlineBuildVoid0ArgSkeletonLen). Used by the Compile-side
// useFrameInline branch. The Absolute variant appends r14=arenaBase +
// add rax, r14 inside LoadCISlotAddr so rax becomes an absolute address,
// avoiding the "word offset cannot be dereferenced" bug.
func archEmitFrameInlineBuildVoid0ArgSkeleton(buf []byte,
	ciDepthAddrOff, ciSegBaseAddrOff int32, callARecv uint8,
	w0, w1, w2, w4 uint64) []byte {
	arenaBaseOff := int32(JITContextArenaBaseOffset)
	return jitamd64.EmitFrameInlineBuildVoid0ArgSkeletonAbsolute(buf,
		ciDepthAddrOff, ciSegBaseAddrOff, arenaBaseOff, callARecv,
		jitamd64.FrameInlineCISlotWords{Word0: w0, Word1: w1, Word2: w2, Word3: 0, Word4: w4})
}

// archEmitFrameInlinePopVoid0ArgSkeleton splices the amd64 Spike 1 popCallInfo
// byte-level inline skeleton (10 bytes, per §9.20 Option B Spike 1).
func archEmitFrameInlinePopVoid0ArgSkeleton(buf []byte, ciDepthAddrOff int32) []byte {
	return jitamd64.EmitFrameInlinePopVoid0ArgSkeleton(buf, ciDepthAddrOff)
}

// archEmitFrameInlineExitHelperRequest splices the amd64 Spike 1 trampoline
// exit-resume protocol's exit-helper-request segment (24 bytes, per
// §9.20.9 (4)).
func archEmitFrameInlineExitHelperRequest(buf []byte,
	exitReasonOff, exitArg0Off int32, helperCode uint64) []byte {
	return jitamd64.EmitFrameInlineExitHelperRequest(buf,
		exitReasonOff, exitArg0Off, helperCode)
}

// archEncodedFrameInlineBuildVoid0ArgSkeletonLen is the byte length of the amd64
// Spike 1 enterLuaFrame skeleton (120).
const archEncodedFrameInlineBuildVoid0ArgSkeletonLen = jitamd64.EncodedFrameInlineBuildVoid0ArgSkeletonLen

// archEncodedFrameInlinePopVoid0ArgSkeletonLen is the byte length of the amd64
// Spike 1 popCallInfo skeleton (10).
const archEncodedFrameInlinePopVoid0ArgSkeletonLen = jitamd64.EncodedFrameInlinePopVoid0ArgSkeletonLen

// archEncodedFrameInlineExitHelperRequestLen is the byte length of the amd64
// Spike 1 exit-helper-request segment (24, per §9.20.9 (4) optimized form).
const archEncodedFrameInlineExitHelperRequestLen = jitamd64.EncodedFrameInlineExitHelperRequestLen

// archSupportsFrameInline returns true when this arch supports the live PJ5
// Option B frame-build inlining path (per §9.20 Spike 1).
//
// **2026-06-28 amd64 flipped to true for the live path** (per §9.20.9
// commit-5h): the byte-level emit templates (BuildVoid0ArgSkeleton 120B +
// ExitHelperRequest 24B + PopVoid0ArgSkeleton 10B + LoadClosureGCRef 20B +
// WriteCIWord 14B + CIDepth++/-- 10B) are fully byte-level implemented with all
// byte-level unit tests passing + the Compile-side useFrameInline branch emit is
// wired up + the Run-side runFrameInlineDispatcher path is wired up +
// crescent.ExecuteCalleeFromInlineFrame is implemented (it looks up the closure
// GCRef → callee Proto → enterLuaFrame + executeFrom to fully rebuild the frame;
// the Spike 1 simplification gives up zero-crossing but guarantees correctness).
//
// **Enablement conditions** (per §9.20.4 + the analyzeSelfCallSpecForm guard):
//  1. the ordinary spec form passes (IC NodeHit + FBSelfMono + stableKey)
//  2. callArgCount=0 (0-arg form)
//  3. isCallVoid=true (setter form)
//  4. !isTailCall (not a TAILCALL)
//
// **arm64 is still false** (end-to-end physical runner validation is deferred to
// PJ8+, per arch_arm64.go below).
func archSupportsFrameInline() bool { return true }
