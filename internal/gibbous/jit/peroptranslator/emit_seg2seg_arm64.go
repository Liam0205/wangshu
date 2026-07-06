//go:build wangshu_p4 && arm64

// emit_seg2seg_arm64.go — arm64 port of the issue #50 Spike 5
// segment-to-segment CALL dispatch, inline GETUPVAL, dual-semantics
// RETURN, and the seg2seg deopt guard. This mirrors the amd64 emit in
// emit_ops_amd64.go (emitCallInlineFastPath / emitGETUPVALInline /
// emitReturnDualSemantics / emitSegCallDeoptGuard) instruction-for-
// instruction, translated to arm64 encodings.
//
// **Validation status**: the logic mirrors the amd64 implementation
// which is fully validated (difftest / -race / benchmarks). The arm64
// machine code itself is validated on real arm64 hardware only through
// the CI three-platform matrix (linux/arm64 + darwin/arm64) — this repo
// is developed on amd64 with no qemu-aarch64, so the arm64 seg2seg path
// cannot be exercised locally. See issue #61.
//
// Register convention (per emit_arm64.go): X26 = vsBase, X27 = jitCtx,
// X28 = G (permanent), X30 = LR, SP = X31. Scratch: X9..X17.

package peroptranslator

import (
	"unsafe"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	jitarm64 "github.com/Liam0205/wangshu/internal/gibbous/jit/arm64"
)

const regX30 uint8 = 30 // link register (return address after blr)

// icSlotAddrArm64 returns the stable Go-heap address of the CallIC slot
// for the given call site, baked into the guard as an imm64. cb.proto.
// CallICs is allocated once before emit and never re-sliced, so the
// element address is stable for the mmap page's lifetime.
func icSlotAddrArm64(cb *codeBuf, callSiteIdx int) uint64 {
	return uint64(uintptr(unsafe.Pointer(&cb.proto.CallICs[callSiteIdx])))
}

// arm64PayloadMask is the NaN-box GCRef payload mask (mirror of amd64's
// 0x0000_FFFF_FFFF_FFFF): low 48 bits are the arena byte offset.
const arm64PayloadMask uint64 = 0x0000_FFFF_FFFF_FFFF

// -----------------------------------------------------------------------
// Raw SP-relative + branch-patch helpers.
//
// The shared arm64 emitters (EmitLdrXtFromXnDisp / EmitStrXtToXnDisp /
// EmitAddXdImm12 / EmitSubXdImm12) clamp Rn/Rd > 30 to 0, so they cannot
// address SP (x31). The seg2seg caller must save X30 (LR, clobbered by
// blr) + X26 (caller vsBase) + the caller closure ref across the nested
// blr, which needs the machine stack. Encode the SP forms directly.
// -----------------------------------------------------------------------

func arm64Word(w uint32) []byte {
	return []byte{byte(w), byte(w >> 8), byte(w >> 16), byte(w >> 24)}
}

// a64StrXSp emits `str Xt, [sp, #off]` (off is an 8-byte-scaled byte offset).
func a64StrXSp(cb *codeBuf, rt uint8, off uint16) {
	imm12 := uint32(off/8) & 0xFFF
	cb.emit(arm64Word(0xF9000000 | imm12<<10 | 31<<5 | uint32(rt)&0x1F))
}

// a64LdrXSp emits `ldr Xt, [sp, #off]`.
func a64LdrXSp(cb *codeBuf, rt uint8, off uint16) {
	imm12 := uint32(off/8) & 0xFFF
	cb.emit(arm64Word(0xF9400000 | imm12<<10 | 31<<5 | uint32(rt)&0x1F))
}

// a64SubSp emits `sub sp, sp, #imm` (imm is a plain byte count, <=0xFFF).
func a64SubSp(cb *codeBuf, imm uint16) {
	cb.emit(arm64Word(0xD1000000 | (uint32(imm)&0xFFF)<<10 | 31<<5 | 31))
}

// a64AddSp emits `add sp, sp, #imm`.
func a64AddSp(cb *codeBuf, imm uint16) {
	cb.emit(arm64Word(0x91000000 | (uint32(imm)&0xFFF)<<10 | 31<<5 | 31))
}

// a64PatchRel19 patches the imm19 field (bits 5..23) of a B.cond / CBZ /
// CBNZ instruction at insnOff so it branches to targetOff. Delegates to
// the CI-validated patchBCondArm64 (the imm19 field is identical across
// B.cond / CBZ / CBNZ).
func a64PatchRel19(cb *codeBuf, insnOff, targetOff int) {
	patchBCondArm64(cb, int32(insnOff), int32(targetOff))
}

// a64PatchRel26 patches the imm26 field (bits 0..25) of a B instruction
// at insnOff so it branches to targetOff. Delegates to the CI-validated
// patchArm64B26.
func a64PatchRel26(cb *codeBuf, insnOff, targetOff int) {
	patchArm64B26(cb, int32(insnOff), int32(targetOff))
}

// -----------------------------------------------------------------------
// emitSegCallDeoptGuardArm64 — mirror of amd64 emitSegCallDeoptGuard.
//
//	ldr w16, [x27, #segCallDepthOff]
//	cbz w16, skip          ; depth == 0 -> no-op (fall through)
//	mov w16, #1
//	str w16, [x27, #segCallDeoptOff]
//	ret                    ; depth > 0 -> deopt: set flag + ret
//	skip:
//
// -----------------------------------------------------------------------
func emitSegCallDeoptGuardArm64(cb *codeBuf) {
	if !segToSegEnabled {
		return
	}
	cb.emit(jitarm64.EmitLdrWtFromXnDisp(nil, 16, regX27,
		uint16(jit.JITContextSegCallDepthOffset)))
	cbzOff := len(cb.bytes)
	cb.emit(jitarm64.EmitCbzX(nil, 16, 0)) // -> skip
	cb.emit(jitarm64.EmitMovzWdImm16(nil, 16, 1))
	cb.emit(jitarm64.EmitStrWtToXnDisp(nil, 16, regX27,
		uint16(jit.JITContextSegCallDeoptOffset)))
	cb.emit(jitarm64.EmitRet(nil))
	a64PatchRel19(cb, cbzOff, len(cb.bytes))
}

// -----------------------------------------------------------------------
// emitReturnDualSemanticsArm64 — mirror of amd64 emitReturnDualSemantics.
// When segCallDepth > 0 the RETURN tears the frame down in-segment and
// rets back into the caller segment; at depth 0 it exits (single-return:
// mov x0,#0; ret -> Go DoReturn / multi-return: HelperReturn exit-reason).
// -----------------------------------------------------------------------
func emitReturnDualSemanticsArm64(cb *codeBuf, a, b uint8, pc int32, multiReturn bool) {
	if !multiReturn && cb.proto != nil {
		cb.proto.RetA = int32(a)
		cb.proto.RetB = int32(b)
		cb.proto.RetPC = pc
	}
	// ldr w16, [x27, #segCallDepthOff]; cbz w16, go_exit
	cb.emit(jitarm64.EmitLdrWtFromXnDisp(nil, 16, regX27,
		uint16(jit.JITContextSegCallDepthOffset)))
	goExitOff := len(cb.bytes)
	cb.emit(jitarm64.EmitCbzX(nil, 16, 0)) // depth == 0 -> go_exit

	// In-segment teardown: funcIdx byte addr = x26 - 8.
	cb.emit(jitarm64.EmitSubXdImm12(nil, 17, regX26, 8)) // x17 = x26 - 8
	nret := int32(b) - 1
	if b != 0 {
		for k := int32(0); k < nret; k++ {
			cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 9, regX26, uint16((int32(a)+k)*8)))
			cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 9, 17, uint16(k*8)))
		}
	}
	cb.emit(jitarm64.EmitRet(nil)) // ret into caller segment

	// go_exit:
	a64PatchRel19(cb, goExitOff, len(cb.bytes))
	if multiReturn {
		emitExitReasonArm64(cb, jit.HelperReturn, pc, int32(a), int32(b), 0)
		cb.pendingResumeOffFixups = cb.pendingResumeOffFixups[:0]
		return
	}
	cb.emit(jitarm64.EmitMovXdImm64(nil, 0, 0)) // mov x0, #0
	cb.emit(jitarm64.EmitRet(nil))
}

// -----------------------------------------------------------------------
// emitGETUPVALInlineArm64 — mirror of amd64 emitGETUPVALInline. Resolves
// the running frame's closure via jitCtx.currentClosureRef, reads
// upvalRef, and branches closed vs open (single-thread inlineUpvalSafe);
// foreign owner falls back to the HelperGetUpval exit-reason / deopt.
// Clobbers x9..x14.
// -----------------------------------------------------------------------
func emitGETUPVALInlineArm64(cb *codeBuf, a, b uint8) {
	// x9 = arenaBase
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 9, regX27,
		uint16(jit.JITContextArenaBaseOffset)))
	// x10 = currentClosureRef; x10 = closureAddr = x10 + x9
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 10, regX27,
		uint16(jit.JITContextCurrentClosureRefOffset)))
	cb.emit(jitarm64.EmitAddXdXnXm(nil, 10, 10, 9))
	// x10 = upvalRef = [closureAddr + (2+B)*8]; x10 = upvalAddr = x10 + x9
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 10, 10, uint16((2+int32(b))*8)))
	cb.emit(jitarm64.EmitAddXdXnXm(nil, 10, 10, 9))
	// closed-bit test: w11 = header low32; w11 &= 0x1000; cbnz -> closed
	cb.emit(jitarm64.EmitLdrWtFromXnDisp(nil, 11, 10, 0))
	cb.emit(jitarm64.EmitMovzWdImm16(nil, 12, 0x1000))
	cb.emit(jitarm64.EmitAndXdXnXm(nil, 11, 11, 12))
	cbnzClosedOff := len(cb.bytes)
	cb.emit(jitarm64.EmitCbnzW(nil, 11, 0)) // closed -> closed_label
	// open path: check inlineUpvalSafe
	cb.emit(jitarm64.EmitLdrWtFromXnDisp(nil, 11, regX27,
		uint16(jit.JITContextInlineUpvalSafeOffset)))
	cbzFallbackOff := len(cb.bytes)
	cb.emit(jitarm64.EmitCbzX(nil, 11, 0)) // !safe -> fallback
	// w11 = stackIdx = [upvalAddr + 8]; x10 = threadStackBase0
	cb.emit(jitarm64.EmitLdrWtFromXnDisp(nil, 11, 10, 8))
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 10, regX27,
		uint16(jit.JITContextThreadStackBase0Offset)))
	// x11 = stackIdx*8; x10 = x10 + x11; x10 = [x10] (owner.slot value)
	cb.emit(jitarm64.EmitLslXdImm6(nil, 11, 11, 3))
	cb.emit(jitarm64.EmitAddXdXnXm(nil, 10, 10, 11))
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 10, 10, 0))
	bStoreOff := len(cb.bytes)
	cb.emit(jitarm64.EmitB(nil, 0)) // b store
	// closed_label: x10 = [upvalAddr + 16] (upval word2)
	a64PatchRel19(cb, cbnzClosedOff, len(cb.bytes))
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 10, 10, 16))
	// store: R(A) = x10
	a64PatchRel26(cb, bStoreOff, len(cb.bytes))
	cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 10, regX26, uint16(int32(a)*8)))
	bDoneOff := len(cb.bytes)
	cb.emit(jitarm64.EmitB(nil, 0)) // b done
	// fallback: deopt if nested, else exit-reason
	a64PatchRel19(cb, cbzFallbackOff, len(cb.bytes))
	emitSegCallDeoptGuardArm64(cb)
	emitGETUPVALArm64(cb, a, b)
	// done:
	a64PatchRel26(cb, bDoneOff, len(cb.bytes))
}

// -----------------------------------------------------------------------
// emitCallInlineFastPathArm64 — mirror of amd64 emitCallInlineFastPath.
// Guards R(A) tag + IC protoID + flags/arity, then (when eligible)
// dispatches segment-to-segment; otherwise exit-reasons to
// HelperExecutePlainCall (fast body) or HelperCall (guard-fail slow
// path). Returns true if it consumed the CALL.
// -----------------------------------------------------------------------
func emitCallInlineFastPathArm64(cb *codeBuf, pc int32, a, b, c uint8, callSiteIdx int) bool {
	if cb.proto == nil || callSiteIdx < 0 || callSiteIdx >= len(cb.proto.CallICs) {
		return false
	}
	icSlotAddr := icSlotAddrArm64(cb, callSiteIdx)

	var slowBranches []int // offsets of guard-fail branches -> slow path

	// ---- guard ----
	// x10 = R(A) NaN-box
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 10, regX26, uint16(int32(a)*8)))
	// w11 = tag = x10 >> 48; cmp against TagFunction (0xFFFD)
	cb.emit(jitarm64.EmitLsrXdImm6(nil, 11, 10, 48))
	cb.emit(jitarm64.EmitMovzWdImm16(nil, 13, 0xFFFD))
	cb.emit(jitarm64.EmitCmpXnXm(nil, 11, 13))
	slowBranches = append(slowBranches, len(cb.bytes))
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondNE, 0)) // b.ne slow
	// payload: x10 &= payloadMask; x15 = arenaBase; x10 = closureAddr
	cb.emit(jitarm64.EmitMovXdImm64(nil, 15, arm64PayloadMask))
	cb.emit(jitarm64.EmitAndXdXnXm(nil, 10, 10, 15))
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 15, regX27,
		uint16(jit.JITContextArenaBaseOffset)))
	cb.emit(jitarm64.EmitAddXdXnXm(nil, 10, 10, 15))
	// w11 = protoID = [closureAddr + 8]; w11 += 1 (unbias vs IC.CalleeProtoID+1)
	cb.emit(jitarm64.EmitLdrWtFromXnDisp(nil, 11, 10, 8))
	cb.emit(jitarm64.EmitAddXdImm12(nil, 11, 11, 1))
	// x12 = icSlotAddr; w13 = IC.CalleeProtoID; cmp
	cb.emit(jitarm64.EmitMovXdImm64(nil, 12, icSlotAddr))
	cb.emit(jitarm64.EmitLdrWtFromXnDisp(nil, 13, 12, 0))
	cb.emit(jitarm64.EmitCmpXnXm(nil, 11, 13))
	slowBranches = append(slowBranches, len(cb.bytes))
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondNE, 0)) // b.ne slow
	// flags gate: w13 = flags byte [ic+6]; w13 &= 0x87; cbnz slow
	cb.emit(jitarm64.EmitLdrbWtFromXnDisp(nil, 13, 12, uint16(callICFlagsByteOffset)))
	cb.emit(jitarm64.EmitMovzWdImm16(nil, 14, uint16(CallICFlagIsVararg|CallICFlagNeedsArg|CallICFlagIsHost|CallICFlagStuck)))
	cb.emit(jitarm64.EmitAndXdXnXm(nil, 13, 13, 14))
	slowBranches = append(slowBranches, len(cb.bytes))
	cb.emit(jitarm64.EmitCbnzW(nil, 13, 0)) // cbnz slow
	// arity gate: w14 = NumParams byte [ic+4]; cmp #nargs; b.ne slow
	cb.emit(jitarm64.EmitLdrbWtFromXnDisp(nil, 14, 12, 4))
	cb.emit(jitarm64.EmitCmpXnImm12(nil, 14, uint16(int32(b)-1)))
	slowBranches = append(slowBranches, len(cb.bytes))
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondNE, 0)) // b.ne slow

	// ---- segment-to-segment dispatch ----
	jmpDoneOff := -1
	if segToSegEnabled {
		// NeverExits flag test: w13 = [ic+6]; w13 &= 0x08; cbz skip_seg
		cb.emit(jitarm64.EmitLdrbWtFromXnDisp(nil, 13, 12, uint16(callICFlagsByteOffset)))
		cb.emit(jitarm64.EmitMovzWdImm16(nil, 14, uint16(CallICFlagNeverExits)))
		cb.emit(jitarm64.EmitAndXdXnXm(nil, 13, 13, 14))
		cbzNeverOff := len(cb.bytes)
		cb.emit(jitarm64.EmitCbzX(nil, 13, 0)) // -> skip_seg
		// x13 = segAddr = [ic+16]; cbz skip_seg
		cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 13, 12, uint16(callICSegAddrByteOffset)))
		cbzSegOff := len(cb.bytes)
		cb.emit(jitarm64.EmitCbzX(nil, 13, 0)) // -> skip_seg
		// cap: w14 = segCallDepth; cmp #cap; b.hs skip_seg
		cb.emit(jitarm64.EmitLdrWtFromXnDisp(nil, 14, regX27,
			uint16(jit.JITContextSegCallDepthOffset)))
		cb.emit(jitarm64.EmitMovzWdImm16(nil, 15, uint16(segToSegDepthCap)))
		cb.emit(jitarm64.EmitCmpXnXm(nil, 14, 15))
		bhsCapOff := len(cb.bytes)
		cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondHS, 0)) // depth >= cap -> skip_seg
		// save caller closure into x15; set jitCtx.currentClosureRef = callee closure
		cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 15, regX27,
			uint16(jit.JITContextCurrentClosureRefOffset)))
		cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 9, regX26, uint16(int32(a)*8)))
		cb.emit(jitarm64.EmitMovXdImm64(nil, 11, arm64PayloadMask))
		cb.emit(jitarm64.EmitAndXdXnXm(nil, 9, 9, 11))
		cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 9, regX27,
			uint16(jit.JITContextCurrentClosureRefOffset)))
		// inc segCallDepth (w14 still holds it)
		cb.emit(jitarm64.EmitAddXdImm12(nil, 14, 14, 1))
		cb.emit(jitarm64.EmitStrWtToXnDisp(nil, 14, regX27,
			uint16(jit.JITContextSegCallDepthOffset)))
		// save X30 (LR), X26 (caller vsBase), X15 (caller closure) on the stack
		a64SubSp(cb, 32)
		a64StrXSp(cb, regX30, 0)
		a64StrXSp(cb, regX26, 8)
		a64StrXSp(cb, 15, 16)
		// callee vsBase = X26 + (A+1)*8; publish to jitCtx.vsBase
		cb.emit(jitarm64.EmitAddXdImm12(nil, regX26, regX26, uint16((int32(a)+1)*8)))
		cb.emit(jitarm64.EmitStrXtToXnDisp(nil, regX26, regX27,
			uint16(jit.JITContextValueStackBaseOffset)))
		// blr segAddr
		cb.emit(jitarm64.EmitBlrXn(nil, 13))
		// restore
		a64LdrXSp(cb, regX30, 0)
		a64LdrXSp(cb, regX26, 8)
		a64LdrXSp(cb, 15, 16)
		a64AddSp(cb, 32)
		cb.emit(jitarm64.EmitStrXtToXnDisp(nil, regX26, regX27,
			uint16(jit.JITContextValueStackBaseOffset)))
		cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 15, regX27,
			uint16(jit.JITContextCurrentClosureRefOffset)))
		// dec segCallDepth
		cb.emit(jitarm64.EmitLdrWtFromXnDisp(nil, 14, regX27,
			uint16(jit.JITContextSegCallDepthOffset)))
		cb.emit(jitarm64.EmitSubXdImm12(nil, 14, 14, 1))
		cb.emit(jitarm64.EmitStrWtToXnDisp(nil, 14, regX27,
			uint16(jit.JITContextSegCallDepthOffset)))
		// deopt check: w14 = segCallDeopt; cbz no_deopt
		cb.emit(jitarm64.EmitLdrWtFromXnDisp(nil, 14, regX27,
			uint16(jit.JITContextSegCallDeoptOffset)))
		cbzNoDeoptOff := len(cb.bytes)
		cb.emit(jitarm64.EmitCbzX(nil, 14, 0)) // -> no_deopt
		// depth > 0 -> propagate (ret); depth == 0 -> clear + redo
		cb.emit(jitarm64.EmitLdrWtFromXnDisp(nil, 14, regX27,
			uint16(jit.JITContextSegCallDepthOffset)))
		cbnzPropOff := len(cb.bytes)
		cb.emit(jitarm64.EmitCbnzW(nil, 14, 0)) // depth > 0 -> propagate
		// clear deopt flag; b skip_seg (redo via exit-reason)
		cb.emit(jitarm64.EmitMovzWdImm16(nil, 14, 0))
		cb.emit(jitarm64.EmitStrWtToXnDisp(nil, 14, regX27,
			uint16(jit.JITContextSegCallDeoptOffset)))
		bRedoOff := len(cb.bytes)
		cb.emit(jitarm64.EmitB(nil, 0)) // -> skip_seg
		// propagate: ret
		a64PatchRel19(cb, cbnzPropOff, len(cb.bytes))
		cb.emit(jitarm64.EmitRet(nil))
		// no_deopt: bump SegToSegHitCount, then b done
		a64PatchRel19(cb, cbzNoDeoptOff, len(cb.bytes))
		cb.emit(jitarm64.EmitMovXdImm64(nil, 9, SegToSegHitCountAddr()))
		cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 10, 9, 0))
		cb.emit(jitarm64.EmitAddXdImm12(nil, 10, 10, 1))
		cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 10, 9, 0))
		jmpDoneOff = len(cb.bytes)
		cb.emit(jitarm64.EmitB(nil, 0)) // b done
		// skip_seg: patch the eligibility skips + the deopt-redo jump here
		skipSegPos := len(cb.bytes)
		a64PatchRel19(cb, cbzNeverOff, skipSegPos)
		a64PatchRel19(cb, cbzSegOff, skipSegPos)
		a64PatchRel19(cb, bhsCapOff, skipSegPos)
		a64PatchRel26(cb, bRedoOff, skipSegPos)
	}

	// ---- fast body: guard passed, seg2seg skipped -> HelperExecutePlainCall ----
	emitSegCallDeoptGuardArm64(cb)
	nargs := int32(b) - 1
	nresults := int32(c) - 1
	emitExitReasonArm64(cb, jit.HelperExecutePlainCall, pc, int32(a), nargs, nresults)

	// ---- slow path: guard-fail branches land here -> HelperCall ----
	slowPos := len(cb.bytes)
	for _, off := range slowBranches {
		a64PatchRel19(cb, off, slowPos)
	}
	emitSegCallDeoptGuardArm64(cb)
	emitExitReasonArm64(cb, jit.HelperCall, pc, int32(a), int32(b), int32(c))

	// done: seg2seg completion continues to the next op.
	if jmpDoneOff >= 0 {
		a64PatchRel26(cb, jmpDoneOff, len(cb.bytes))
	}
	return true
}
