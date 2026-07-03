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

	// qNanBoxBaseArm64 is the lower bound of the non-number NaN-box
	// space (mirror of emit_amd64.go's qNanBoxBaseU64, which lives in
	// an amd64-only file). Any raw uint64 >= this constant is a tagged
	// non-number.
	qNanBoxBaseArm64 uint64 = 0xFFF8_0000_0000_0000
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

// -----------------------------------------------------------------------
// arm64 exit-reason protocol (issue #37, PJ10 lowering)
// -----------------------------------------------------------------------
//
// This is the PJ10 exit-reason emit (mirror of amd64 emitExitReason in
// emit_ops_amd64.go), NOT the PJ4/5 frame-inline helper request
// (jitarm64.EmitFrameInlineExitHelperRequestArm64). The two protocols
// differ: PJ4/5 packs only a bare helperCode and additionally writes
// jitCtx.exitReasonCode; PJ10 packs (helperCode, a, b, c, pc) into
// exitArg0, writes resumeOff, and signals ExitInlineHelper solely via
// the X0 return value. Do not mix them.
//
// X16 (IP0) is the scratch register: it's reserved for intra-procedure
// use by the arm64 ABI, never holds a live value across our emits, and
// doesn't collide with X26 (vsBase) / X27 (jitCtx) / X28 (Go G).

// emitExitReasonArm64 packs (helperCode, a, b, c, pc) into
// jitCtx.exitArg0, writes a placeholder resumeOff (patched by
// emitResumePreludeIfPendingArm64 once the next op's offset is known),
// sets X0 = ExitInlineHelper, and RETs. Field layout matches the
// arch-shared dispatchHelper in translator_native_dispatch.go:
//
//	bits  0..15 : helper code (jit.HelperXxx)
//	bits 16..23 : op arg A (0-255)
//	bits 24..32 : op arg B (0-511)
//	bits 33..41 : op arg C (0-511)
//	bits 42..63 : op pc (0..4M)
func emitExitReasonArm64(cb *codeBuf, helperCode uint64, pc int32, a, b, c int32) {
	packed := helperCode |
		(uint64(uint32(a)&0xFF) << 16) |
		(uint64(uint32(b)&0x1FF) << 24) |
		(uint64(uint32(c)&0x1FF) << 33) |
		(uint64(uint32(pc)&0x3FFFFF) << 42)
	// mov X16, packed (16B: movz + 3×movk)
	cb.emit(jitarm64.EmitMovXdImm64(nil, 16, packed))
	// str X16, [X27 + exitArg0Off]
	cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 16, regX27,
		uint16(jit.JITContextExitArg0Offset)))
	// movz W16, #lo16; movk W16, #hi16 LSL16 — placeholder resumeOff
	// imm32 split across two fixed-length instructions; patched by
	// emitResumePreludeIfPendingArm64. The fixup records the movz
	// instruction offset (arm64 interpretation of the shared
	// pendingResumeOffFixups slice — amd64 records a raw imm32 offset).
	movzOff := int(cb.pos())
	cb.emit(jitarm64.EmitMovzWdImm16(nil, 16, 0))
	cb.emit(jitarm64.EmitMovkWdImm16Lsl16(nil, 16, 0))
	cb.markResumeOffFixup(movzOff)
	// str W16, [X27 + resumeOffOff] (32-bit store)
	cb.emit(jitarm64.EmitStrWtToXnDisp(nil, 16, regX27,
		uint16(jit.JITContextResumeOffOffset)))
	// movz W0, #ExitInlineHelper — segment exit status (upper 32 bits
	// of X0 are zeroed by the 32-bit movz, so Run's uint32(rawStatus)
	// comparison sees exactly ExitInlineHelper).
	cb.emit(jitarm64.EmitMovzWdImm16(nil, 0, uint16(jit.ExitInlineHelper)))
	// ret
	cb.emit(jitarm64.EmitRet(nil))
}

// emitResumePreludeIfPendingArm64 binds the resume entry for all
// pending exit-reason emits: emits `ldr X26, [X27+vsBaseOff]` (the
// dispatcher may have refreshed vsBase via arena grow before reentry)
// and patches each pending movz/movk pair with the resume offset.
// Safe no-op when nothing pends. Called at the start of every
// emitLinearOpArm64 / emitTerminatorArm64 — mirror of the amd64
// emitResumePreludeIfPending in translator_native.go.
func emitResumePreludeIfPendingArm64(cb *codeBuf) {
	if len(cb.pendingResumeOffFixups) == 0 {
		return
	}
	resumeOff := uint32(cb.pos())
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, regX26, regX27,
		uint16(jit.JITContextValueStackBaseOffset)))
	for _, po := range cb.pendingResumeOffFixups {
		patchArm64MovzMovkImm32(cb.bytes, po, resumeOff)
	}
	cb.pendingResumeOffFixups = cb.pendingResumeOffFixups[:0]
}

// patchArm64MovzMovkImm32 patches a movz (at off) + movk-LSL16 (at
// off+4) pair's imm16 fields with the low/high halves of v. The imm16
// field occupies bits 5..20 of each instruction word.
func patchArm64MovzMovkImm32(bytes []byte, off int, v uint32) {
	patch := func(o int, imm16 uint32) {
		insn := uint32(bytes[o]) | uint32(bytes[o+1])<<8 |
			uint32(bytes[o+2])<<16 | uint32(bytes[o+3])<<24
		insn &= 0xFFE0001F
		insn |= (imm16 & 0xFFFF) << 5
		bytes[o] = byte(insn)
		bytes[o+1] = byte(insn >> 8)
		bytes[o+2] = byte(insn >> 16)
		bytes[o+3] = byte(insn >> 24)
	}
	patch(off, v&0xFFFF)
	patch(off+4, v>>16)
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

// emitGETUPVALArm64 emits GETUPVAL A B via the exit-reason protocol
// (issue #37): pack (HelperGetUpval, a, b) into exitArg0, RET; Run's
// dispatcher does host.SetReg(a, host.GetUpval(base, b)) and reenters.
// Never raises. Mirror of amd64 emitGETUPVAL.
func emitGETUPVALArm64(cb *codeBuf, a, b uint8) {
	emitExitReasonArm64(cb, jit.HelperGetUpval, 0, int32(a), int32(b), 0)
}

// emitSETUPVALArm64 emits SETUPVAL A B via the exit-reason protocol:
// Run's dispatcher does host.SetUpvalFromReg(base, a, b) and reenters.
// Never raises. Mirror of amd64 emitSETUPVAL.
func emitSETUPVALArm64(cb *codeBuf, a, b uint8) {
	emitExitReasonArm64(cb, jit.HelperSetUpval, 0, int32(a), int32(b), 0)
}

// emitARITHArm64 emits arm64 arithmetic op via shimArith. Same
// signature-mapping as amd64: shimArith(ctx, base, pc, op, b, c, a).
func emitARITHArm64(cb *codeBuf, op bytecode.OpCode, pc int32, a, b, c uint8) {
	emitCallShimArm64(cb, shimArithAddr(), []int32{0, pc, int32(op), int32(b), int32(c), int32(a)})
}

// emitStatusCheckAndBubbleArm64 previously wrapped a shim-status
// check for the arm64 emit; the design keeps arm64 arith fully
// inline (see file header for translator_native_arm64.go), so no
// shim call needs a status check yet. Removed to keep lint quiet;
// re-add if a future op-family does need to bubble a shim status.

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

// emitGETTABLEArm64 emits GETTABLE A B C: inline ArrayHit fast path
// when the IC snapshot allows (mirror of amd64
// emitInlineGetTableArrayHit), else the exit-reason path (NodeHit
// sites ride host.GetTable — byte-equal to the interpreter's IC path).
func emitGETTABLEArm64(cb *codeBuf, pc int32, a, b uint8, c int) {
	if emitInlineGetTableArrayHitArm64(cb, pc, a, b, c) {
		return
	}
	emitExitReasonArm64(cb, jit.HelperGetTable, pc, int32(a), int32(b), int32(c))
}

// emitSETTABLEArm64 emits SETTABLE A B C: inline ArrayHit overwrite
// fast path, else exit-reason (host.SetTable).
func emitSETTABLEArm64(cb *codeBuf, pc int32, a uint8, b, c int) {
	if emitInlineSetTableArrayHitArm64(cb, pc, a, b, c) {
		return
	}
	emitExitReasonArm64(cb, jit.HelperSetTable, pc, int32(a), int32(b), int32(c))
}

// emitTablePreludeArm64 emits the shared GETTABLE/SETTABLE ArrayHit
// prelude: IsTable guard on R(tblReg), GCRef extraction, arena base
// load, key load + IsNumber guard + f64→int round-trip check, bounds
// check against live asize, and slot address computation.
//
// Register state on fall-through (all guards passed):
//
//	X1 = arena base
//	X3 = absolute slot address (arrayBase + (idx-1)*8)
//	X4 = slot value
//	X5 = value.Nil bits (reusable for Nil compares)
//
// Guard misses branch to the miss block; their patch offsets are
// appended to *guardFixups.
//
// vs amd64: FCMPE leaves Z=0 for unordered operands, so a single B.NE
// covers both "fractional" and "NaN" (amd64 needs jne + jp). The slot
// address survives in X3, so SETTABLE needs no idx recompute.
//
// NOTE: no TableRef / gen identity guards — same reasoning as the
// amd64 emit: a non-Nil array slot read/overwrite is correct for ANY
// table value (no __index/__newindex, bounds from live asize); the IC
// snapshot only gates WHICH pc sites get the inline emit.
func emitTablePreludeArm64(cb *codeBuf, tblReg uint8, keyRK int, guardFixups *[]int32) {
	// --- Guard 1: R(tblReg) is a Table (tag == 0xFFFC) ---
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 0, regX26, uint16(tblReg)*8))
	cb.emit(jitarm64.EmitLsrXdImm6(nil, 4, 0, 48))
	cb.emit(jitarm64.EmitMovzWdImm16(nil, 5, uint16(value.TagTable)))
	cb.emit(jitarm64.EmitCmpXnXm(nil, 4, 5))
	*guardFixups = append(*guardFixups, cb.pos())
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondNE, 0))

	// --- GCRef extract: X0 &= payloadMask; X3 = arenaBase + X0 ---
	cb.emit(jitarm64.EmitMovXdImm64(nil, 5, 0x0000_FFFF_FFFF_FFFF))
	cb.emit(jitarm64.EmitAndXdXnXm(nil, 0, 0, 5))
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 1, regX27,
		uint16(jit.JITContextArenaBaseOffset)))
	cb.emit(jitarm64.EmitAddXdXnXm(nil, 3, 1, 0))

	// --- Load key from RK into X0 ---
	if keyRK < 256 {
		cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 0, regX26, uint16(keyRK)*8))
	} else {
		cb.emit(jitarm64.EmitMovXdImm64(nil, 0, cb.proto.Consts[keyRK-256]))
	}

	// --- Guard: IsNumber(key) — key < qNanBoxBase ---
	cb.emit(jitarm64.EmitMovXdImm64(nil, 5, qNanBoxBaseArm64))
	cb.emit(jitarm64.EmitCmpXnXm(nil, 0, 5))
	*guardFixups = append(*guardFixups, cb.pos())
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondHS, 0))

	// --- f64 → int round trip: X2 = trunc(key); verify integral ---
	cb.emit(jitarm64.EmitFmovDdFromXn(nil, 0, 0))
	cb.emit(jitarm64.EmitFcvtzsXdDn(nil, 2, 0))
	cb.emit(jitarm64.EmitScvtfDdXn(nil, 1, 2))
	cb.emit(jitarm64.EmitFcmpeDnDm(nil, 0, 1))
	*guardFixups = append(*guardFixups, cb.pos())
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondNE, 0))

	// --- Bounds: 1 <= idx <= asize (asize = word1 low32 at +8) ---
	cb.emit(jitarm64.EmitCmpXnImm12(nil, 2, 1))
	*guardFixups = append(*guardFixups, cb.pos())
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondLT, 0))
	cb.emit(jitarm64.EmitLdrWtFromXnDisp(nil, 4, 3, 8))
	cb.emit(jitarm64.EmitCmpXnXm(nil, 2, 4))
	*guardFixups = append(*guardFixups, cb.pos())
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondGT, 0))

	// --- Slot addr: X3 = arenaBase + arrayRef + (idx-1)*8 ---
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 4, 3, 16)) // arrayRef (word2)
	cb.emit(jitarm64.EmitAddXdXnXm(nil, 4, 4, 1))
	cb.emit(jitarm64.EmitSubXdImm12(nil, 2, 2, 1))
	cb.emit(jitarm64.EmitLslXdImm6(nil, 2, 2, 3))
	cb.emit(jitarm64.EmitAddXdXnXm(nil, 3, 4, 2))

	// --- Load slot value + set up X5 = Nil for the caller's guards ---
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 4, 3, 0))
	cb.emit(jitarm64.EmitMovXdImm64(nil, 5, uint64(value.Nil)))
}

// emitInlineGetTableArrayHitArm64 emits the GETTABLE ArrayHit inline
// fast path (guards + live-table array slot load; miss → exit-reason).
// Returns true when the inline path was emitted.
func emitInlineGetTableArrayHitArm64(cb *codeBuf, pc int32, a, b uint8, c int) bool {
	if cb.proto == nil || int(pc) < 0 || int(pc) >= len(cb.proto.IC) {
		return false
	}
	if cb.proto.IC[pc].Kind != bytecode.ICKindArrayHit {
		return false
	}
	// Pre-emit K sanity: bail before any bytes are emitted (a mid-emit
	// bail would leave guard fixups dangling).
	if c >= 256 {
		if kidx := c - 256; kidx < 0 || kidx >= len(cb.proto.Consts) {
			return false
		}
	}

	var guardFixups []int32
	emitTablePreludeArm64(cb, b, c, &guardFixups)

	// Guard: slot != Nil (Nil routes to the helper for the __index
	// chain). X4 = slot value, X5 = Nil from the prelude.
	cb.emit(jitarm64.EmitCmpXnXm(nil, 4, 5))
	guardFixups = append(guardFixups, cb.pos())
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondEQ, 0))

	// Store R(A) = slot; b done.
	cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 4, regX26, uint16(a)*8))
	bDoneOff := cb.pos()
	cb.emit([]byte{0x00, 0x00, 0x00, 0x14})

	// miss:
	missOff := cb.pos()
	for _, po := range guardFixups {
		patchBCondArm64(cb, po, missOff)
	}
	emitExitReasonArm64(cb, jit.HelperGetTable, pc, int32(a), int32(b), int32(c))

	// done:
	patchArm64B26(cb, bDoneOff, cb.pos())
	return true
}

// emitInlineSetTableArrayHitArm64 emits the SETTABLE ArrayHit inline
// overwrite fast path (`R(A)[RK(B)] := RK(C)`): existing non-Nil array
// slot overwritten with a non-Nil value is a raw store for ANY table
// (no __newindex, no rehash, bounds from live asize).
func emitInlineSetTableArrayHitArm64(cb *codeBuf, pc int32, a uint8, b, c int) bool {
	if cb.proto == nil || int(pc) < 0 || int(pc) >= len(cb.proto.IC) {
		return false
	}
	if cb.proto.IC[pc].Kind != bytecode.ICKindArrayHit {
		return false
	}
	for _, rk := range [2]int{b, c} {
		if rk >= 256 {
			if kidx := rk - 256; kidx < 0 || kidx >= len(cb.proto.Consts) {
				return false
			}
		}
	}

	var guardFixups []int32
	emitTablePreludeArm64(cb, a, b, &guardFixups)

	// Guard: existing slot != Nil (a Nil slot means insert semantics —
	// __newindex consultation — so route to the helper).
	cb.emit(jitarm64.EmitCmpXnXm(nil, 4, 5))
	guardFixups = append(guardFixups, cb.pos())
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondEQ, 0))

	// Load the new value RK(C) into X4.
	if c < 256 {
		cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 4, regX26, uint16(c)*8))
	} else {
		cb.emit(jitarm64.EmitMovXdImm64(nil, 4, cb.proto.Consts[c-256]))
	}

	// Guard: new value != Nil (writing Nil is delete semantics).
	cb.emit(jitarm64.EmitCmpXnXm(nil, 4, 5)) // X5 still Nil
	guardFixups = append(guardFixups, cb.pos())
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondEQ, 0))

	// Store [X3] = X4 (slot address survived the value load); b done.
	cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 4, 3, 0))
	bDoneOff := cb.pos()
	cb.emit([]byte{0x00, 0x00, 0x00, 0x14})

	// miss:
	missOff := cb.pos()
	for _, po := range guardFixups {
		patchBCondArm64(cb, po, missOff)
	}
	emitExitReasonArm64(cb, jit.HelperSetTable, pc, int32(a), int32(b), int32(c))

	// done:
	patchArm64B26(cb, bDoneOff, cb.pos())
	return true
}

// emitGETGLOBALArm64 emits GETGLOBAL A Bx: inline NodeHit fast path
// when the IC snapshot allows (mirror of amd64 emitGETGLOBAL /
// emitInlineGetGlobalNodeHit), else the exit-reason path. bx is up to
// 18 bits, split across the b (low 9) / c (high 9) arg slots — the
// dispatcher reassembles bx = b | c<<9.
func emitGETGLOBALArm64(cb *codeBuf, pc int32, a uint8, bx uint16) {
	if emitInlineGetGlobalNodeHitArm64(cb, pc, a, bx) {
		return
	}
	emitExitReasonArm64(cb, jit.HelperGetGlobal, pc, int32(a), int32(bx)&0x1FF, (int32(bx)>>9)&0x1FF)
}

// emitInlineGetGlobalNodeHitArm64 emits the GETGLOBAL NodeHit inline
// fast path. The globals table byte offset (taddr) and the IC snapshot
// (node index + gen) are compile-time constants:
//
//	[Guard: gen (word5 high32 at taddr+40) == snap.Shape]
//	[Load nodeRef = word3 at taddr+24, val = nodeRef + Index*24 + 8]
//	[Guard: val != Nil]
//	[Store val -> R(A)]
//	[b done]
//	miss: <exit-reason HelperGetGlobal>
//	done:
//
// Register use: X1 = arenaBase, X2/X3 = addr scratch, X4 = loaded
// value, X5 = compare imm. taddr can exceed the ldr imm12 range, so
// addresses are formed via mov-imm64 + add instead of scaled
// displacement.
func emitInlineGetGlobalNodeHitArm64(cb *codeBuf, pc int32, a uint8, bx uint16) bool {
	if cb.proto == nil || cb.proto.GlobalsTaddr == 0 || int(pc) >= len(cb.proto.IC) {
		return false
	}
	snap := cb.proto.IC[pc]
	if snap.Kind != bytecode.ICKindNodeHit {
		return false
	}
	taddr := uint64(cb.proto.GlobalsTaddr)
	var guardFixups []int32

	// X1 = arena base
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 1, regX27,
		uint16(jit.JITContextArenaBaseOffset)))

	// Guard: gen == snap.Shape.
	// X2 = taddr+40; X3 = X1+X2; X4 = [X3]; X4 >>= 32; cmp X4, snap.Shape
	cb.emit(jitarm64.EmitMovXdImm64(nil, 2, taddr+40))
	cb.emit(jitarm64.EmitAddXdXnXm(nil, 3, 1, 2))
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 4, 3, 0))
	cb.emit(jitarm64.EmitLsrXdImm6(nil, 4, 4, 32))
	cb.emit(jitarm64.EmitMovXdImm64(nil, 5, uint64(snap.Shape)))
	cb.emit(jitarm64.EmitCmpXnXm(nil, 4, 5))
	guardFixups = append(guardFixups, cb.pos())
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondNE, 0))

	// X4 = nodeRef (word3 at taddr+24), absolute = X4 + X1.
	cb.emit(jitarm64.EmitMovXdImm64(nil, 2, taddr+24))
	cb.emit(jitarm64.EmitAddXdXnXm(nil, 3, 1, 2))
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 4, 3, 0))
	cb.emit(jitarm64.EmitAddXdXnXm(nil, 4, 4, 1))
	// X4 = node val = [X4 + Index*24 + 8]
	cb.emit(jitarm64.EmitMovXdImm64(nil, 2, uint64(snap.Index)*24+8))
	cb.emit(jitarm64.EmitAddXdXnXm(nil, 3, 4, 2))
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 4, 3, 0))

	// Guard: val != Nil.
	cb.emit(jitarm64.EmitMovXdImm64(nil, 5, uint64(value.Nil)))
	cb.emit(jitarm64.EmitCmpXnXm(nil, 4, 5))
	guardFixups = append(guardFixups, cb.pos())
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondEQ, 0))

	// Store R(A) = X4; b done.
	cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 4, regX26, uint16(a)*8))
	bDoneOff := cb.pos()
	cb.emit([]byte{0x00, 0x00, 0x00, 0x14}) // b done (patched below)

	// miss:
	missOff := cb.pos()
	for _, po := range guardFixups {
		patchBCondArm64(cb, po, missOff)
	}
	emitExitReasonArm64(cb, jit.HelperGetGlobal, pc, int32(a), int32(bx)&0x1FF, (int32(bx)>>9)&0x1FF)

	// done:
	doneOff := cb.pos()
	patchArm64B26(cb, bDoneOff, doneOff)
	return true
}

// emitSETGLOBALArm64 emits SETGLOBAL A Bx: inline NodeHit existing-key
// overwrite fast path (mirror of amd64 emitInlineSetGlobalNodeHit),
// else exit-reason. Same bx split as GETGLOBAL.
func emitSETGLOBALArm64(cb *codeBuf, pc int32, a uint8, bx uint16) {
	if emitInlineSetGlobalNodeHitArm64(cb, pc, a, bx) {
		return
	}
	emitExitReasonArm64(cb, jit.HelperSetGlobal, pc, int32(a), int32(bx)&0x1FF, (int32(bx)>>9)&0x1FF)
}

// emitInlineSetGlobalNodeHitArm64: Gtable[K(Bx)] := R(A) when gen
// matches and the slot's existing value is non-Nil (existing-key
// overwrite never rehashes / never consults __newindex) and the new
// value is non-Nil (writing Nil is delete semantics → slow path).
func emitInlineSetGlobalNodeHitArm64(cb *codeBuf, pc int32, a uint8, bx uint16) bool {
	if cb.proto == nil || cb.proto.GlobalsTaddr == 0 || int(pc) >= len(cb.proto.IC) {
		return false
	}
	snap := cb.proto.IC[pc]
	if snap.Kind != bytecode.ICKindNodeHit {
		return false
	}
	taddr := uint64(cb.proto.GlobalsTaddr)
	var guardFixups []int32

	// X1 = arena base
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 1, regX27,
		uint16(jit.JITContextArenaBaseOffset)))

	// Guard: gen == snap.Shape.
	cb.emit(jitarm64.EmitMovXdImm64(nil, 2, taddr+40))
	cb.emit(jitarm64.EmitAddXdXnXm(nil, 3, 1, 2))
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 4, 3, 0))
	cb.emit(jitarm64.EmitLsrXdImm6(nil, 4, 4, 32))
	cb.emit(jitarm64.EmitMovXdImm64(nil, 5, uint64(snap.Shape)))
	cb.emit(jitarm64.EmitCmpXnXm(nil, 4, 5))
	guardFixups = append(guardFixups, cb.pos())
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondNE, 0))

	// X3 = absolute node val slot addr = arenaBase + nodeRef + Index*24 + 8.
	cb.emit(jitarm64.EmitMovXdImm64(nil, 2, taddr+24))
	cb.emit(jitarm64.EmitAddXdXnXm(nil, 3, 1, 2))
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 4, 3, 0))
	cb.emit(jitarm64.EmitAddXdXnXm(nil, 4, 4, 1))
	cb.emit(jitarm64.EmitMovXdImm64(nil, 2, uint64(snap.Index)*24+8))
	cb.emit(jitarm64.EmitAddXdXnXm(nil, 3, 4, 2))

	// Guard: existing slot val != Nil (key exists; delete goes slow).
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 4, 3, 0))
	cb.emit(jitarm64.EmitMovXdImm64(nil, 5, uint64(value.Nil)))
	cb.emit(jitarm64.EmitCmpXnXm(nil, 4, 5))
	guardFixups = append(guardFixups, cb.pos())
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondEQ, 0))

	// X4 = R(A); Guard: new value != Nil (writing Nil deletes → slow).
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 4, regX26, uint16(a)*8))
	cb.emit(jitarm64.EmitCmpXnXm(nil, 4, 5)) // X5 still NilBits
	guardFixups = append(guardFixups, cb.pos())
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondEQ, 0))

	// Store [X3] = X4; b done.
	cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 4, 3, 0))
	bDoneOff := cb.pos()
	cb.emit([]byte{0x00, 0x00, 0x00, 0x14})

	// miss:
	missOff := cb.pos()
	for _, po := range guardFixups {
		patchBCondArm64(cb, po, missOff)
	}
	emitExitReasonArm64(cb, jit.HelperSetGlobal, pc, int32(a), int32(bx)&0x1FF, (int32(bx)>>9)&0x1FF)

	// done:
	doneOff := cb.pos()
	patchArm64B26(cb, bDoneOff, doneOff)
	return true
}

// patchArm64B26 patches an already-emitted unconditional B at bufOff to
// branch to targetOff (imm26 word-scaled, PC = bufOff).
func patchArm64B26(cb *codeBuf, bufOff, targetOff int32) {
	wordDisp := (targetOff - bufOff) / 4
	insn := uint32(0x14000000) | (uint32(wordDisp) & 0x03FFFFFF)
	cb.bytes[bufOff] = byte(insn)
	cb.bytes[bufOff+1] = byte(insn >> 8)
	cb.bytes[bufOff+2] = byte(insn >> 16)
	cb.bytes[bufOff+3] = byte(insn >> 24)
}

// emitNEWTABLEArm64 emits NEWTABLE A B C via exit-reason (allocation
// must happen host-side). AnalyzeNative rejects B/C >= 256 so the
// packed 9-bit arg slots stay faithful.
func emitNEWTABLEArm64(cb *codeBuf, pc int32, a, b, c uint8) {
	emitExitReasonArm64(cb, jit.HelperNewTable, pc, int32(a), int32(b), int32(c))
}

// emitSETLISTArm64 emits arm64 SETLIST via shimSetList.
func emitSETLISTArm64(cb *codeBuf, pc int32, a, b, c uint8) {
	emitCallShimArm64(cb, shimSetListAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
}

// emitCALLArm64 emits CALL A B C via the exit-reason protocol (issue
// #37 step 2, mirror of amd64 emitCALL): Run's dispatcher invokes
// host.CallBaseline (synchronous callee completion) and reenters.
// AnalyzeNative rejects B=0 / C=0 forms (they depend on a live `top`
// the native segment doesn't maintain per-op) and gates acceptance on
// CALL density.
func emitCALLArm64(cb *codeBuf, pc int32, a, b, c uint8) {
	emitExitReasonArm64(cb, jit.HelperCall, pc, int32(a), int32(b), int32(c))
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
