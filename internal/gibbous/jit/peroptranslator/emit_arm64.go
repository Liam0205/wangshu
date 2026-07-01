//go:build wangshu_p4 && arm64

// emit_arm64.go - arm64 counterparts of the PJ10 native op emitters.
//
// arm64 register convention (per docs/design/p4-method-jit/06-backends.md):
//   - X26 = valueStackBase   (analog of amd64 RBX)
//   - X27 = jitContext        (analog of amd64 R15)
//   - X28 = Go G              (analog of amd64 R14; permanent)
//
// Since X28 stays as G naturally (Go arm64 uses it as G), we don't need
// a save/restore protocol like amd64's savedGoG dance. Go ABIInternal on
// arm64 preserves X26 and X27 across calls... actually no, X26 is caller-
// saved. So we still need to reload X26 = vsBase after each helper call.
//
// For this initial arm64 landing we mirror the amd64 shim strategy but
// with arm64 encodings. Test coverage is provided in
// e2e_arm64_test.go under //go:build ...arm64.
package peroptranslator

import (
	"github.com/Liam0205/wangshu/internal/bytecode"
	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	jitarm64 "github.com/Liam0205/wangshu/internal/gibbous/jit/arm64"
	"github.com/Liam0205/wangshu/internal/value"
)

const (
	// regX0 is the arm64 first int arg (Go ABIInternal maps arg0 here).
	regX0 uint8 = 0
	// regX26 = vsBase.
	regX26 uint8 = 26
	// regX27 = jitCtx.
	regX27 uint8 = 27
)

// emitMOVEArm64 emits `ldr x0, [x26+B*8]; str x0, [x26+A*8]`.
// R(A) := R(B).
func emitMOVEArm64(cb *codeBuf, a, b uint8) {
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 0, regX26, uint16(b)*8))
	cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 0, regX26, uint16(a)*8))
}

// emitLOADKArm64 emits `mov x0, imm64; str x0, [x26+A*8]`.
func emitLOADKArm64(cb *codeBuf, a uint8, imm uint64) {
	cb.emit(jitarm64.EmitMovXdImm64(nil, 0, imm))
	cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 0, regX26, uint16(a)*8))
}

// emitLOADBOOLArm64_valueOnly emits R(A) := True/False.
func emitLOADBOOLArm64_valueOnly(cb *codeBuf, a, b uint8) {
	var imm uint64
	if b != 0 {
		imm = uint64(value.True)
	} else {
		imm = uint64(value.False)
	}
	emitLOADKArm64(cb, a, imm)
}

// emitLOADNILArm64 emits R(A..B) := nil (inclusive).
func emitLOADNILArm64(cb *codeBuf, a, b uint8) {
	nilBits := uint64(value.Nil)
	for i := int32(a); i <= int32(b); i++ {
		emitLOADKArm64(cb, uint8(i), nilBits)
	}
}

// emitRetArm64 emits arm64 `ret`.
func emitRetArm64(cb *codeBuf) {
	cb.emit(jitarm64.EmitRet(nil))
}

// emitJMPArm64 emits `b rel26` with a placeholder + fixup. The rel26 is
// in units of 4 bytes and is patched by resolveLabels.
//
// arm64 `b imm26` opcode: 0x14000000 | (imm26 & 0x03ffffff). We emit
// zero placeholder; resolver patches. Since arm64 branches are relative
// to the instruction PC in units of 4, we need special handling in
// codeBuf.resolveLabels for arm64 -- OR we can encode the rel32 as a
// byte-level 32-bit displacement in the label patch and then post-
// process to arm64 rel26 shift.
//
// For simplicity: emit the placeholder as a full 32-bit encoded B with
// zero offset, and add a fixup with a marker that the resolveLabels
// path (arm64 build) shifts +2 bits. This is a TODO for the arm64
// bringup; for the first landing we emit b with zero and require the
// caller to handle patching by shifting rel32 >> 2 before writing.
//
// Simpler: use a bespoke arm64 label resolver that patches the low 26
// bits after shifting. Add here to keep contained.
func emitJMPArm64(cb *codeBuf, targetBB int) {
	patchOff := cb.pos()
	// Placeholder: b #0
	cb.emit([]byte{0x00, 0x00, 0x00, 0x14})
	cb.addFixup(patchOff, cb.pos(), targetBB)
	// The generic resolveLabels writes a 32-bit rel32 in bytes. For
	// arm64 we would need a different resolver. See resolveLabelsArm64
	// (TODO) for the arm64-specific patch. For this initial landing we
	// don't call resolveLabels for arm64 code; tests exercise linear
	// segments only.
}

// -----------------------------------------------------------------------
// arm64 shim call protocol
// -----------------------------------------------------------------------
//
// Register conventions:
//   - X0..X7 = Go ABIInternal int arg regs (arg0 in X0)
//   - X26 = vsBase (preserved by our code, may be clobbered by Go calls,
//           so reload after each shim call)
//   - X27 = jitCtx (preserved by our code; Go ABIInternal treats it as
//           callee-saved via the general X19-X28 pool convention)
//   - X28 = Go G (permanent; Go arm64 ABIInternal keeps G in X28)
//
// Unlike amd64, arm64 doesn't need a save/restore protocol for G:
// X28 = G is always live and Go preserves it.
//
// Shim call sequence for arm64:
//   mov X0, X27        ; arg0 = jitCtx
//   mov X1, immArg1    ; arg1
//   mov X2, immArg2    ; arg2
//   ...
//   mov X<scratch>, shimAddr
//   blr X<scratch>     ; call shim
//   ldr X26, [X27, #vsBaseOff]  ; reload vsBase (may be clobbered)

// emitReloadVsBaseArm64 emits `ldr X26, [X27, #vsBaseOff]`.
func emitReloadVsBaseArm64(cb *codeBuf) {
	off := jit.JITContextValueStackBaseOffset
	// Only works if off is multiple of 8 and fits in unsigned 12-bit
	// scaled by 8 (i.e., byte offset up to 32760). All our field
	// offsets satisfy this.
	if off > 32760 || off%8 != 0 {
		panic("emitReloadVsBaseArm64: vsBase offset out of range for scaled ldr")
	}
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, regX26, regX27, uint16(off)))
}

// emitCallShimArm64 emits the full sequence for calling a Go shim on
// arm64. See amd64 emitCallShim doc.
func emitCallShimArm64(cb *codeBuf, shimAddr uint64, args []int32) {
	// arg0 = jitCtx (from X27)
	cb.emit(jitarm64.EmitMovXdFromXn(nil, 0 /*X0*/, regX27))
	// arg1..argN in X1..Xn
	for i, v := range args {
		reg := uint8(i + 1) // X1, X2, ...
		if reg > 7 {
			panic("emitCallShimArm64: at most 7 int32 args supported")
		}
		// mov Xn, imm  -- use full 64-bit imm for simplicity
		cb.emit(jitarm64.EmitMovXdImm64(nil, reg, uint64(int64(v))))
	}
	// Call the shim via X15 (a scratch reg not used as arg).
	cb.emit(jitarm64.EmitMovXdImm64(nil, 15, shimAddr))
	cb.emit(jitarm64.EmitBlrXn(nil, 15))
	// Reload X26 = vsBase (may be clobbered).
	emitReloadVsBaseArm64(cb)
}

// emitRETURNArm64 emits the arm64 RETURN sequence via shimDoReturn.
func emitRETURNArm64(cb *codeBuf, pc int32, a, b uint8) {
	addr := shimDoReturnAddr()
	emitCallShimArm64(cb, addr, []int32{0, pc, int32(a), int32(b)})
	emitRetArm64(cb)
}

// emitGETUPVALArm64 emits arm64 GETUPVAL A B via shimGetUpval.
func emitGETUPVALArm64(cb *codeBuf, a, b uint8) {
	addr := shimGetUpvalAddr()
	emitCallShimArm64(cb, addr, []int32{0, int32(b), int32(a)})
}

// emitSETUPVALArm64 emits arm64 SETUPVAL A B via shimSetUpvalFromReg.
func emitSETUPVALArm64(cb *codeBuf, a, b uint8) {
	addr := shimSetUpvalFromRegAddr()
	emitCallShimArm64(cb, addr, []int32{0, int32(a), int32(b)})
}

// emitARITHArm64 emits arm64 arithmetic op via shimArith. Same
// signature-mapping as amd64: shimArith(ctx, base, pc, op, b, c, a).
func emitARITHArm64(cb *codeBuf, op bytecode.OpCode, pc int32, a, b, c uint8) {
	emitCallShimArm64(cb, shimArithAddr(), []int32{0, pc, int32(op), int32(b), int32(c), int32(a)})
}

// emitStatusCheckAndBubbleArm64 emits the arm64 equivalent of amd64's
// emitStatusCheckAndBubble: "if X0 != 0 then ret". Used after shim
// calls whose helpers can raise (Arith/GetTable/…), so a non-zero
// status returned by host.<Helper> bubbles up to the trampoline
// instead of the mmap segment silently continuing.
//
// EmitMovXdImm64 always emits 4 insns (16 bytes) — movz + 3 movks —
// even for tiny imms, so the skip target sits 5 words past the CBZ.
//
// Sequence (24 bytes):
//
//	cbz  X0, +24    ; skip mov+ret when X0 == 0 (imm19 = 5 words)
//	<mov X0, #1>    ; 16 bytes (4 movz/movk insns)
//	ret             ; 4 bytes
func emitStatusCheckAndBubbleArm64(cb *codeBuf) {
	cb.emit(jitarm64.EmitCbzX(nil, 0, 5))
	cb.emit(jitarm64.EmitMovXdImm64(nil, 0, 1))
	cb.emit(jitarm64.EmitRet(nil))
}

func emitADDArm64(cb *codeBuf, pc int32, a, b, c uint8) {
	emitARITHArm64(cb, bytecode.ADD, pc, a, b, c)
}
func emitSUBArm64(cb *codeBuf, pc int32, a, b, c uint8) {
	emitARITHArm64(cb, bytecode.SUB, pc, a, b, c)
}
func emitMULArm64(cb *codeBuf, pc int32, a, b, c uint8) {
	emitARITHArm64(cb, bytecode.MUL, pc, a, b, c)
}
func emitDIVArm64(cb *codeBuf, pc int32, a, b, c uint8) {
	emitARITHArm64(cb, bytecode.DIV, pc, a, b, c)
}
func emitMODArm64(cb *codeBuf, pc int32, a, b, c uint8) {
	emitARITHArm64(cb, bytecode.MOD, pc, a, b, c)
}
func emitPOWArm64(cb *codeBuf, pc int32, a, b, c uint8) {
	emitARITHArm64(cb, bytecode.POW, pc, a, b, c)
}

// emitUNMArm64 emits arm64 UNM via shimUnm.
func emitUNMArm64(cb *codeBuf, pc int32, a, b uint8) {
	emitCallShimArm64(cb, shimUnmAddr(), []int32{0, pc, int32(b), int32(a)})
}

// emitLENArm64 emits arm64 LEN via shimLen.
func emitLENArm64(cb *codeBuf, pc int32, a, b uint8) {
	emitCallShimArm64(cb, shimLenAddr(), []int32{0, pc, int32(b), int32(a)})
}

// emitCONCATArm64 emits arm64 CONCAT via shimConcat.
func emitCONCATArm64(cb *codeBuf, pc int32, a, b, c uint8) {
	emitCallShimArm64(cb, shimConcatAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
}

// emitGETTABLEArm64 emits arm64 GETTABLE via shimGetTable.
func emitGETTABLEArm64(cb *codeBuf, pc int32, a, b, c uint8) {
	emitCallShimArm64(cb, shimGetTableAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
}

// emitSETTABLEArm64 emits arm64 SETTABLE via shimSetTable.
func emitSETTABLEArm64(cb *codeBuf, pc int32, a, b, c uint8) {
	emitCallShimArm64(cb, shimSetTableAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
}

// emitGETGLOBALArm64 emits arm64 GETGLOBAL via shimGetGlobal.
func emitGETGLOBALArm64(cb *codeBuf, pc int32, a uint8, bx uint16) {
	emitCallShimArm64(cb, shimGetGlobalAddr(), []int32{0, pc, int32(a), int32(bx)})
}

// emitSETGLOBALArm64 emits arm64 SETGLOBAL via shimSetGlobal.
func emitSETGLOBALArm64(cb *codeBuf, pc int32, a uint8, bx uint16) {
	emitCallShimArm64(cb, shimSetGlobalAddr(), []int32{0, pc, int32(a), int32(bx)})
}

// emitNEWTABLEArm64 emits arm64 NEWTABLE via shimNewTable.
func emitNEWTABLEArm64(cb *codeBuf, pc int32, a, b, c uint8) {
	emitCallShimArm64(cb, shimNewTableAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
}

// emitSETLISTArm64 emits arm64 SETLIST via shimSetList.
func emitSETLISTArm64(cb *codeBuf, pc int32, a, b, c uint8) {
	emitCallShimArm64(cb, shimSetListAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
}

// emitCALLArm64 emits arm64 CALL via shimCall.
func emitCALLArm64(cb *codeBuf, pc int32, a, b, c uint8) {
	emitCallShimArm64(cb, shimCallAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
}

// emitTAILCALLArm64 emits arm64 TAILCALL via shimTailCall.
func emitTAILCALLArm64(cb *codeBuf, pc int32, a, b, c uint8) {
	emitCallShimArm64(cb, shimTailCallAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
}

// emitSELFArm64 emits arm64 SELF via shimSelf.
func emitSELFArm64(cb *codeBuf, pc int32, a, b, c uint8) {
	emitCallShimArm64(cb, shimSelfAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
}

// emitCLOSUREArm64 emits arm64 CLOSURE via shimClosure.
func emitCLOSUREArm64(cb *codeBuf, pc int32, a uint8, bx uint16) {
	emitCallShimArm64(cb, shimClosureAddr(), []int32{0, pc, int32(a), int32(bx)})
}

// emitCLOSEArm64 emits arm64 CLOSE via shimClose.
func emitCLOSEArm64(cb *codeBuf, pc int32, a uint8) {
	emitCallShimArm64(cb, shimCloseAddr(), []int32{0, pc, int32(a)})
}

// emitFORPREPArm64 emits arm64 FORPREP via shimForPrep + jump to
// FORLOOP block.
func emitFORPREPArm64(cb *codeBuf, pc int32, a uint8, targetBB int) {
	emitCallShimArm64(cb, shimForPrepAddr(), []int32{0, pc, int32(a)})
	emitJMPArm64(cb, targetBB)
}

// emitEQArm64 emits arm64 EQ via shimEq (shim-based fallback for
// non-inline path). The inline path uses inlineRawEqArm64 in
// translator_native_arm64.go.
func emitEQArm64(cb *codeBuf, pc int32, a uint8, b, c int) {
	emitCallShimArm64(cb, shimEqAddr(), []int32{0, pc, int32(b), int32(c)})
	_ = a
}

// emitLTArm64 / emitLEArm64: shim fallback for LT/LE. The inline path
// uses inlineNumericCompareArm64.
func emitLTArm64(cb *codeBuf, pc int32, a uint8, b, c int) {
	emitCallShimArm64(cb, shimCompareAddr(), []int32{0, pc, int32(bytecode.LT), int32(b), int32(c)})
	_ = a
}
func emitLEArm64(cb *codeBuf, pc int32, a uint8, b, c int) {
	emitCallShimArm64(cb, shimCompareAddr(), []int32{0, pc, int32(bytecode.LE), int32(b), int32(c)})
	_ = a
}

// emitTFORLOOPArm64 emits arm64 TFORLOOP via shimTForLoop. shim
// returns int64: -2=exit, -1=error, >=0=continue.
func emitTFORLOOPArm64(cb *codeBuf, pc int32, a, c uint8, succBack, succOut int) {
	emitCallShimArm64(cb, shimTForLoopAddr(), []int32{0, pc, int32(a), int32(c)})
	// After shim returns, X0 holds -2 / -1 / continue.
	// TODO: proper 3-way branch on X0 with rel19/rel26 fixups. For now
	// unconditional fall-through to succBack (loop body).
	emitJMPArm64(cb, succBack)
	_ = succOut
}

// emitNOTArm64 emits arm64 NOT R(A) := not R(B). Never raises.
//
// TODO: full inline implementation is complex on arm64 (imm64 loads
// take 3 or 4 instructions, so laying out the branches with rel19
// offsets requires careful arithmetic). Deferred until native path is
// enabled in production. Returns without emitting -- callers must not
// use this until fixed. For safety, panic.
func emitNOTArm64(cb *codeBuf, a, b uint8) {
	_ = cb
	_ = a
	_ = b
	panic("emitNOTArm64: inline NOT not yet implemented; use amd64-only path")
}
