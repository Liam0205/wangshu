//go:build wangshu_p4 && amd64

// emit_ops_amd64.go - per-opcode native emit for the PJ10 opcodes.
// Hot shapes get inline fast paths (SSE for arithmetic, IC-inline for
// table ops); slow shapes ride the exit-reason protocol (issue #38 /
// #45): the segment packs the request into exitArg0 and RETs, and
// nativeCode.Run's Go-side dispatcher performs the host call. No op
// emits an in-segment Go call — mmap→Go shim calls crash the Go stack
// unwinder under nested + concurrent load.
package peroptranslator

import (
	"unsafe"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// Shim addr helpers moved to shims.go (arch-neutral).

// ---------------------------------------------------------------------
// Arithmetic ops (PJ10b): all go through host.Arith slow path
// ---------------------------------------------------------------------

// emitARITH emits R(A) := RK(B) <op> RK(C). ADD/SUB/MUL/DIV get an
// inline SSE fast path with an exit-reason slow path; every other
// shape (MOD/POW, non-numeric K) lowers to a plain HelperArithSlow
// exit-reason. The dispatcher re-derives the op from proto.Code[pc],
// so the packing carries only (a, b, c).
func emitARITH(cb *codeBuf, op bytecode.OpCode, pc int32, a uint8, b, c int) {
	// Fast path: inline SSE for ADD/SUB/MUL/DIV, guarded by IsNumber on
	// each reg operand. If a reg holds nil / a GC ref, exit to the
	// dispatcher so host.Arith can produce the correct __add /
	// coercion / raise semantics. Fuzz seed 4df9d8c82ce0d9f7 caught the
	// unguarded variant (P4 silently produced NaN while P1 raised
	// `attempt to perform arithmetic on nil`).
	if op == bytecode.ADD || op == bytecode.SUB || op == bytecode.MUL || op == bytecode.DIV {
		if emitInlineArithWithSlowPath(cb, op, pc, a, b, c) {
			return
		}
	}
	emitExitReason(cb, jit.HelperArithSlow, pc, int32(a), int32(b), int32(c))
}

// emitInlineArithWithSlowPath emits the guarded inline SSE fast path
// plus an exit-reason slow block for miss cases. Returns true when it
// consumed the emit (inline succeeded); false when the shape can't be
// inlined (e.g. K operand is non-numeric) and the caller should fall
// through to the plain exit-reason lowering.
//
// Layout:
//
//	[guard-B]        ; only if b is reg
//	  mov rax, [rbx+B*8]; mov rcx, qNanBoxBase; cmp rax, rcx
//	  jae slow         ; not a number → slow path
//	[guard-C]        ; only if c is reg (same shape)
//	[inline SSE]
//	  movsd xmm0, B; movsd xmm1, C; arith xmm0, xmm1
//	  movsd [rbx+A*8], xmm0
//	  jmp done
//	slow:
//	  <exit-reason HelperArithSlow>   ; dispatcher runs host.Arith
//	done:                             ; next op binds the resume prelude
//
// Reg operands go through the guard; K operands don't need one because
// AnalyzeNative already rejected non-numeric K constants for arith ops.
func emitInlineArithWithSlowPath(cb *codeBuf, op bytecode.OpCode, pc int32, a uint8, b, c int) bool {
	switch op {
	case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV:
	default:
		return false
	}
	// Verify K operands (if any) are numeric — otherwise the K-side of
	// inlineLoadOperandToXmm would return false, and we'd need to bail
	// out. Check upfront so we can commit to the fast-path layout.
	for _, rk := range [2]int{b, c} {
		if rk >= 256 {
			kidx := rk - 256
			if cb.proto == nil || kidx < 0 || kidx >= len(cb.proto.Consts) {
				return false
			}
			if !value.IsNumber(value.Value(cb.proto.Consts[kidx])) {
				return false
			}
		}
	}

	// Emit IsNumber guards for each reg operand. Guard fail jumps
	// forward to the slow block; we record patch offsets and resolve
	// them once the slow block's byte offset is known.
	var guardFixups []int
	emitRegNumberGuard := func(reg int) {
		if reg >= 256 {
			return
		}
		// mov rax, [rbx+reg*8]
		cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(reg)*8))
		// mov rcx, qNanBoxBase
		cb.emit(jitamd64.EmitMovRcxImm64(nil, qNanBoxBaseU64))
		// cmp rax, rcx
		cb.emit(jitamd64.EmitCmpRaxRcx(nil))
		// jae shim (6-byte 0F 83 rel32); rel32 patched after shim offset known
		cb.emit(jitamd64.EmitJaeRel32(nil, 0))
		guardFixups = append(guardFixups, int(cb.pos())-4)
	}
	emitRegNumberGuard(b)
	emitRegNumberGuard(c)

	// Fast-path body: inline SSE + store R(A) + jmp done.
	if !inlineLoadOperandToXmm(cb, 0, b) {
		return false
	}
	if !inlineLoadOperandToXmm(cb, 1, c) {
		return false
	}
	switch op {
	case bytecode.ADD:
		cb.emit(jitamd64.EmitAddsdXmmXmm(nil, 0, 1))
	case bytecode.SUB:
		cb.emit(jitamd64.EmitSubsdXmmXmm(nil, 0, 1))
	case bytecode.MUL:
		cb.emit(jitamd64.EmitMulsdXmmXmm(nil, 0, 1))
	case bytecode.DIV:
		cb.emit(jitamd64.EmitDivsdXmmXmm(nil, 0, 1))
	}
	// Result guard: x86 SSE ops that produce NaN emit the "real
	// indefinite" QNaN 0xFFF8_0000_0000_0000 (sign bit set), which
	// aliases the NaN-box tagged space (>= qNanBoxBase) and would be
	// misread as a non-number value. The interpreter canonicalizes NaN
	// results; route those to the slow path so host.Arith does the same.
	// movq rax, xmm0  (5B: 66 48 0F 7E C0)
	cb.emit([]byte{0x66, 0x48, 0x0F, 0x7E, 0xC0})
	cb.emit(jitamd64.EmitMovRcxImm64(nil, qNanBoxBaseU64))
	cb.emit(jitamd64.EmitCmpRaxRcx(nil))
	cb.emit(jitamd64.EmitJaeRel32(nil, 0))
	guardFixups = append(guardFixups, int(cb.pos())-4)
	cb.emit(jitamd64.EmitMovsdMemFromXmm(nil, 0, regRBX, int32(a)*8))
	// jmp done (5-byte E9 rel32); rel32 patched once done offset known.
	cb.emit([]byte{0xE9, 0, 0, 0, 0})
	fastPathJmpOff := int(cb.pos()) - 4

	// Slow block starts here — patch guard fixups. The dispatcher runs
	// host.Arith (byte-equal coercion / metamethod / raise semantics)
	// and reenters at the next op's resume prelude. When running as a
	// seg2seg callee (segCallDepth > 0) this deopts instead (issue #50
	// Spike 5) — the whole call chain unwinds and redoes via host.
	slowOff := int(cb.pos())
	for _, po := range guardFixups {
		rel := int32(slowOff) - int32(po+4)
		writeRel32(cb, po, rel)
	}
	emitSegCallDeoptGuard(cb)
	emitExitReason(cb, jit.HelperArithSlow, pc, int32(a), int32(b), int32(c))

	// Done block starts here — patch fastPathJmpOff.
	doneOff := int(cb.pos())
	rel := int32(doneOff) - int32(fastPathJmpOff+4)
	writeRel32(cb, fastPathJmpOff, rel)
	return true
}

// emitSegCallDeoptGuard emits, at the start of an op's exit-reason slow
// block, a check that deopts the segment-to-segment call chain instead
// of exiting to Go when this segment is running as a seg2seg callee
// (issue #50 Spike 5):
//
//	cmp dword [r15 + segCallDepthOff], 0
//	je normal_exit          ; depth == 0 → Go-entered, take the exit-reason
//	mov dword [r15 + segCallDeoptOff], 1
//	ret                     ; deopt: unwind to the caller fast body
//	normal_exit:
//	<existing exit-reason follows>
//
// Only emitted when segToSegEnabled. Registers untouched (uses r15 +
// immediate). Safe to prepend to any exit-reason slow block: at
// segCallDepth == 0 it's a single compare + not-taken branch.
func emitSegCallDeoptGuard(cb *codeBuf) {
	if !segToSegEnabled {
		return
	}
	// cmp dword [r15 + segCallDepthOff], 0   (41 83 BF disp32 00)
	{
		off := int32(jit.JITContextSegCallDepthOffset)
		cb.emit([]byte{0x41, 0x83, 0xBF,
			byte(uint32(off)), byte(uint32(off) >> 8),
			byte(uint32(off) >> 16), byte(uint32(off) >> 24), 0x00})
	}
	// je normal_exit   (74 rel8) — target is past the deopt block.
	// deopt block = mov dword [r15+deoptOff],1 (11B) + ret (1B) = 12B.
	cb.emit([]byte{0x74, 0x0C})
	// mov dword [r15 + segCallDeoptOff], 1   (41 C7 87 disp32 imm32) = 11B
	{
		off := int32(jit.JITContextSegCallDeoptOffset)
		cb.emit([]byte{0x41, 0xC7, 0x87,
			byte(uint32(off)), byte(uint32(off) >> 8),
			byte(uint32(off) >> 16), byte(uint32(off) >> 24),
			0x01, 0x00, 0x00, 0x00})
	}
	// ret   (C3) = 1B — total deopt block 11+1 = 12B (matches je rel8)
	cb.emit([]byte{0xC3})
	// normal_exit: (fall through to existing exit-reason)
}

// writeRel32 patches a 4-byte little-endian rel32 at offset po in cb.bytes.
func writeRel32(cb *codeBuf, po int, rel int32) {
	cb.bytes[po] = byte(rel)
	cb.bytes[po+1] = byte(rel >> 8)
	cb.bytes[po+2] = byte(rel >> 16)
	cb.bytes[po+3] = byte(rel >> 24)
}

// inlineArithSSE emits inline SSE for ADD/SUB/MUL/DIV. B and C may be
// reg refs (<256) or K refs (>=256). Numeric constants baked directly
// via mov rax, imm64.
//
// Returns true on success, false if this shape needs the shim (e.g. K
// operand refers to a non-numeric constant, or op is not one of
// ADD/SUB/MUL/DIV).
func inlineArithSSE(cb *codeBuf, op bytecode.OpCode, a uint8, b, c int) bool {
	switch op {
	case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV:
	default:
		return false
	}
	// Load operand B into xmm0.
	if !inlineLoadOperandToXmm(cb, 0, b) {
		return false
	}
	// Load operand C into xmm1.
	if !inlineLoadOperandToXmm(cb, 1, c) {
		return false
	}
	// arith xmm0, xmm1
	switch op {
	case bytecode.ADD:
		cb.emit(jitamd64.EmitAddsdXmmXmm(nil, 0, 1))
	case bytecode.SUB:
		cb.emit(jitamd64.EmitSubsdXmmXmm(nil, 0, 1))
	case bytecode.MUL:
		cb.emit(jitamd64.EmitMulsdXmmXmm(nil, 0, 1))
	case bytecode.DIV:
		cb.emit(jitamd64.EmitDivsdXmmXmm(nil, 0, 1))
	}
	// movsd [rbx + A*8], xmm0
	cb.emit(jitamd64.EmitMovsdMemFromXmm(nil, 0, regRBX, int32(a)*8))
	return true
}

// inlineLoadOperandToXmm loads RK operand rk into xmmDst. If rk < 256
// it's a reg load from [rbx+rk*8]. If rk >= 256 it's a K constant baked
// as imm64 via mov rax + movq xmm, rax. Returns false when the K const
// index is out of range or is not a numeric value (in which case the
// caller should fall back to the shim path).
func inlineLoadOperandToXmm(cb *codeBuf, xmmDst uint8, rk int) bool {
	if rk < 256 {
		cb.emit(jitamd64.EmitMovsdXmmFromMem(nil, xmmDst, regRBX, int32(rk)*8))
		return true
	}
	kidx := rk - 256
	if cb.proto == nil || kidx < 0 || kidx >= len(cb.proto.Consts) {
		return false
	}
	kbits := cb.proto.Consts[kidx]
	// Verify the K const is numeric by checking the NaN-box tag.
	if !value.IsNumber(value.Value(kbits)) {
		return false
	}
	// mov rax, imm64
	cb.emit(jitamd64.EmitMovRaxImm64(nil, kbits))
	// movq xmmDst, rax
	cb.emit(jitamd64.EmitMovqXmmFromRax(nil, xmmDst))
	return true
}

// emitADD/SUB/MUL/DIV/MOD/POW are thin wrappers on emitARITH with a
// baked op code.
func emitADD(cb *codeBuf, pc int32, a uint8, b, c int) {
	emitARITH(cb, bytecode.ADD, pc, a, b, c)
}
func emitSUB(cb *codeBuf, pc int32, a uint8, b, c int) {
	emitARITH(cb, bytecode.SUB, pc, a, b, c)
}
func emitMUL(cb *codeBuf, pc int32, a uint8, b, c int) {
	emitARITH(cb, bytecode.MUL, pc, a, b, c)
}
func emitDIV(cb *codeBuf, pc int32, a uint8, b, c int) {
	emitARITH(cb, bytecode.DIV, pc, a, b, c)
}
func emitMOD(cb *codeBuf, pc int32, a uint8, b, c int) {
	emitARITH(cb, bytecode.MOD, pc, a, b, c)
}
func emitPOW(cb *codeBuf, pc int32, a uint8, b, c int) {
	emitARITH(cb, bytecode.POW, pc, a, b, c)
}

// emitUNM emits R(A) := -R(B) with an inline number fast path:
//
//	mov rax, [rbx+B*8]
//	mov rdx, qNanBoxBase
//	cmp rax, rdx
//	jae slow                   ; non-number -> exit-reason
//	mov rdx, 0x8000000000000000
//	xor rax, rdx               ; flip IEEE-754 sign bit
//	mov rdx, qNanBoxBase
//	cmp rax, rdx               ; result guard: NaN input aliases
//	jae slow                   ; the tag space after the flip
//	mov [rbx+A*8], rax
//	jmp done
//	slow: <exit-reason HelperUnm>
//	done:
//
// Negating a float by flipping the sign bit matches the interpreter's
// `-x` on non-NaN numbers exactly (including inf / -0 payload bits).
// The result guard catches NaN input: canonNaN (0x7FF8...) sign-flips
// to 0xFFF8... — exactly value.Nil's bit pattern — so the flipped
// result must be re-checked against the tag space and routed to
// host.Unm, which canonicalizes via NumberValue. Same NaN-aliasing
// family as the arith result guard (fuzz seed f7f0bb1a); found by the
// arm64 exit-reason port's NaN e2e (issue #37 step 5) and fixed on
// both arches in the same change. The slow path also covers string
// coercion + __unm.
func emitUNM(cb *codeBuf, pc int32, a, b uint8) {
	// mov rax, [rbx+B*8]
	cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(b)*8))
	// mov rdx, qNanBoxBase; cmp rax, rdx; jae slow
	cb.emit(jitamd64.EmitMovRdxImm64(nil, qNanBoxBaseU64))
	cb.emit([]byte{0x48, 0x39, 0xD0}) // cmp rax, rdx
	cb.emit(jitamd64.EmitJaeRel32(nil, 0))
	slowFixup1 := int(cb.pos()) - 4
	// mov rdx, signBit; xor rax, rdx
	cb.emit(jitamd64.EmitMovRdxImm64(nil, 0x8000_0000_0000_0000))
	cb.emit([]byte{0x48, 0x31, 0xD0}) // xor rax, rdx
	// Result guard: reload qNanBoxBase, re-check the flipped bits.
	cb.emit(jitamd64.EmitMovRdxImm64(nil, qNanBoxBaseU64))
	cb.emit([]byte{0x48, 0x39, 0xD0}) // cmp rax, rdx
	cb.emit(jitamd64.EmitJaeRel32(nil, 0))
	slowFixup2 := int(cb.pos()) - 4
	// mov [rbx+A*8], rax
	cb.emit(jitamd64.EmitMovqMemRegFromRax(nil, regRBX, int32(a)*8))
	// jmp done
	cb.emit([]byte{0xE9, 0, 0, 0, 0})
	doneFixup := int(cb.pos()) - 4
	// slow:
	slowOff := int(cb.pos())
	writeRel32(cb, slowFixup1, int32(slowOff)-int32(slowFixup1+4))
	writeRel32(cb, slowFixup2, int32(slowOff)-int32(slowFixup2+4))
	emitExitReason(cb, jit.HelperUnm, pc, int32(a), int32(b), 0)
	// done:
	doneOff := int(cb.pos())
	writeRel32(cb, doneFixup, int32(doneOff)-int32(doneFixup+4))
}

// emitLEN emits R(A) := #R(B) via the HelperLen exit-reason (the
// dispatcher runs host.Len; string/table length or __len-less raise).
func emitLEN(cb *codeBuf, pc int32, a, b uint8) {
	emitExitReason(cb, jit.HelperLen, pc, int32(a), int32(b), 0)
}

// emitCONCAT emits R(A) := R(B) .. .. R(C) via the HelperConcat
// exit-reason. B/C here are register range endpoints (0-MaxStack,
// always in uint8 range).
func emitCONCAT(cb *codeBuf, pc int32, a, b, c uint8) {
	emitExitReason(cb, jit.HelperConcat, pc, int32(a), int32(b), int32(c))
}

// emitNOT emits R(A) := not R(B) inline. NOT never raises (no metamethod
// in Lua 5.1). Purely memory operations, no shim call.
//
// Emit sequence:
//
//	mov rax, [rbx+B*8]         ; load R(B) as u64
//	mov rcx, nilBits           ; nil = 0xFFF8_0000_0000_0000
//	cmp rax, rcx
//	je storeTrue
//	mov rcx, falseBits         ; false = 0xFFF9_0000_0000_0000
//	cmp rax, rcx
//	je storeTrue
//	mov rax, falseBits         ; result = false
//	jmp done
//
// storeTrue:
//
//	mov rax, trueBits          ; result = true
//
// done:
//
//	mov [rbx+A*8], rax
//
// Layout (byte offsets, total ~50 bytes):
//
//	[00..06]  mov rax, [rbx+B*8]        (7 bytes, disp32)
//	[07..16]  mov rcx, nilBits          (10 bytes)
//	[17..19]  cmp rax, rcx              (3 bytes)
//	[20..21]  jz +N to storeTrue        (2 bytes, rel8)
//	[22..31]  mov rcx, falseBits        (10 bytes)
//	[32..34]  cmp rax, rcx              (3 bytes)
//	[35..36]  jz +M to storeTrue        (2 bytes, rel8)
//	[37..46]  mov rax, falseBits        (10 bytes)
//	[47..48]  jmp +K to done            (2 bytes, rel8)
//	[49..58]  mov rax, trueBits         (10 bytes, storeTrue label)
//	[59..65]  mov [rbx+A*8], rax        (7 bytes, disp32, done label)
//
// rel8 targets: first jz skips to offset 49 (from 22), delta = 27.
// second jz skips to offset 49 (from 37), delta = 12.
// jmp done skips to offset 59 (from 49), delta = 10.
func emitNOT(cb *codeBuf, a, b uint8) {
	nilBits := uint64(value.Nil)
	falseBits := uint64(value.False)
	trueBits := uint64(value.True)

	// mov rax, [rbx+B*8]
	cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(b)*8))
	// mov rcx, nilBits
	cb.emit(jitamd64.EmitMovRcxImm64(nil, nilBits))
	// cmp rax, rcx
	cb.emit(jitamd64.EmitCmpRaxRcx(nil))
	// jz +27 (relative to end of this jz instruction) -> storeTrue label
	cb.emit([]byte{0x74, 27})
	// mov rcx, falseBits
	cb.emit(jitamd64.EmitMovRcxImm64(nil, falseBits))
	// cmp rax, rcx
	cb.emit(jitamd64.EmitCmpRaxRcx(nil))
	// jz +12 -> storeTrue label
	cb.emit([]byte{0x74, 12})
	// mov rax, falseBits  (result = false)
	cb.emit(jitamd64.EmitMovRaxImm64(nil, falseBits))
	// jmp +10 -> done label
	cb.emit([]byte{0xEB, 10})
	// storeTrue: mov rax, trueBits
	cb.emit(jitamd64.EmitMovRaxImm64(nil, trueBits))
	// done: mov [rbx+A*8], rax
	cb.emit(jitamd64.EmitMovqMemRegFromRax(nil, regRBX, int32(a)*8))
}

// unused, kept as an alias; explicit to avoid vet warnings on unsafe.
var _ = unsafe.Pointer(nil)

// emitEQ / emitLT / emitLE emit a compare op followed by the JMP-skip
// logic. In Lua bytecode a compare op is followed by a JMP; the compare
// decides whether to execute the JMP or skip it. In the CFG we split
// this into two successors (succExec = execute JMP, succSkip = pc+2).
//
// Semantics: `if (RK(B) <op> RK(C)) ~= A then pc++`
// - If (result == A) => execute JMP (succExec)
// - If (result != A) => pc++ skips JMP (succSkip)
//
// Slow shapes ride the HelperCompareSlow exit-reason: the dispatcher
// runs host.Compare (packed bit0=result, bit1=error) and hands the
// result bit back through exitArg0; the in-segment resume block
// branches on it (see emitCompareExitTail).
//
// Since this needs branch to specific BB targets, the emit function
// takes those as parameters and records fixups.
func emitCompare(cb *codeBuf, op bytecode.OpCode, pc int32, a uint8, b, c int, succExec, succSkip int) {
	// Fast path: inline numeric compare via UCOMISd. Supports LT/LE
	// with reg-reg, reg-K, K-reg, K-K where the K operand is numeric.
	// Reg operands get IsNumber guards that fall back to the exit tail.
	// EQ inline uses raw 64-bit bit comparison (Lua rawequal for
	// primitives; skips __eq metamethod, safe for numeric-only Protos).
	if op == bytecode.LT || op == bytecode.LE {
		if inlineNumericCompare(cb, op, pc, a, b, c, succExec, succSkip) {
			return
		}
	}
	if op == bytecode.EQ {
		if inlineRawEq(cb, a, b, c, succExec, succSkip) {
			return
		}
	}
	emitCompareExitTail(cb, op, pc, a, b, c, succExec, succSkip)
}

// inlineRawEq emits inline 64-bit raw-bit equality for EQ. Lua 5.1 EQ
// semantics: if types differ, false. If same type and primitive
// (number/nil/bool), bit-equal is correct. For GCRef types (table,
// userdata, string, function, thread) bit-equal reflects pointer
// identity; Lua 5.1 == on strings uses interning so ptr-equal ↔
// string-equal; other GCRefs are always ptr-equal by design. __eq
// metamethod is skipped — safe for numeric-only Protos (the current
// gate's use case).
//
// Two IEEE exceptions break the raw-bit rule for numbers (issue #103):
//
//   - NaN: the value world canonicalizes every NaN to the single
//     canonNaN bit pattern, so two NaN operands are bit-identical —
//     yet PUC's luai_numeq says NaN == NaN is FALSE. Equal bits that
//     are canonNaN route to the condition-false successor.
//   - Negative zero: -0.0 and +0.0 differ in bits but compare EQUAL.
//     Differing bits whose OR has zero magnitude (at most the sign bit
//     set) route to the condition-true successor.
//
// Each check is emitted only when a K operand doesn't already rule it
// out: a K != canonNaN pins the equal-bits value (skip the NaN check);
// a K with non-zero magnitude cannot be one side of a ±0 pair (skip
// the zero check). The common `x == 1` / `x == "key"` shapes pay
// nothing; `x == 0` pays the zero check on its differ path only.
//
// Semantics: cond = (RK(B) == RK(C)); if cond != A then pc++ (succSkip);
// else fall through to JMP (succExec).
//
// For K operands the 64-bit imm is baked; for reg it's from [rbx+X*8].
// Returns false if any K operand is out of range (caller falls back).
func inlineRawEq(cb *codeBuf, a uint8, b, c int, succExec, succSkip int) bool {
	needNaN := true  // equal bits could be canonNaN
	needZero := true // differing bits could be a +/-0 pair
	for _, rk := range [2]int{b, c} {
		if rk >= 256 {
			kidx := rk - 256
			if cb.proto == nil || kidx < 0 || kidx >= len(cb.proto.Consts) {
				return false
			}
			if cb.proto.Consts[kidx] != value.CanonNaN() {
				needNaN = false
			}
			if cb.proto.Consts[kidx]<<1 != 0 {
				needZero = false
			}
		}
	}
	// Load RK(B) into RAX.
	if b < 256 {
		// mov rax, [rbx + B*8]
		cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(b)*8))
	} else {
		cb.emit(jitamd64.EmitMovRaxImm64(nil, cb.proto.Consts[b-256]))
	}
	if !needNaN && !needZero {
		// Fast two-branch form: raw bit equality is exact.
		// Compare RAX with RK(C).
		if c < 256 {
			// cmp rax, [rbx + C*8]  (48 3B 83 disp32; 7 bytes)
			disp := int32(c) * 8
			cb.emit([]byte{0x48, 0x3B, 0x83,
				byte(disp), byte(disp >> 8), byte(disp >> 16), byte(disp >> 24)})
		} else {
			// mov rcx, imm64; cmp rax, rcx
			cb.emit(jitamd64.EmitMovRcxImm64(nil, cb.proto.Consts[c-256]))
			cb.emit(jitamd64.EmitCmpRaxRcx(nil))
		}
		// Lua: if (B == C) != A then pc++. Branch to succExec when the
		// condition matches A.
		//   A=0: match when (B==C)==0, i.e., B!=C. Use jne (0x85).
		//   A=1: match when (B==C)==1, i.e., B==C. Use je  (0x84).
		var jccOpcode byte
		if a == 0 {
			jccOpcode = 0x85 // jne
		} else {
			jccOpcode = 0x84 // je
		}
		// 0F <jcc> rel32: 6 bytes
		cb.emit([]byte{0x0F, jccOpcode, 0x00, 0x00, 0x00, 0x00})
		cb.addFixup(cb.pos()-4, cb.pos(), succExec)
		// jmp rel32 -> succSkip
		patchOff2 := cb.pos() + 1
		cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00})
		cb.addFixup(patchOff2, cb.pos(), succSkip)
		return true
	}
	// IEEE-aware form. RK(C) goes through RCX so the +/-0 check can OR
	// the raw operands.
	if c < 256 {
		// mov rcx, [rbx + C*8]  (48 8B 8B disp32; 7 bytes)
		disp := int32(c) * 8
		cb.emit([]byte{0x48, 0x8B, 0x8B,
			byte(disp), byte(disp >> 8), byte(disp >> 16), byte(disp >> 24)})
	} else {
		cb.emit(jitamd64.EmitMovRcxImm64(nil, cb.proto.Consts[c-256]))
	}
	cb.emit(jitamd64.EmitCmpRaxRcx(nil))
	// condTrue/condFalse successors by A.
	condTrue, condFalse := succExec, succSkip
	if a == 0 {
		condTrue, condFalse = succSkip, succExec
	}
	if needNaN {
		// jne differ (local forward rel32, patched below)
		cb.emit([]byte{0x0F, 0x85, 0x00, 0x00, 0x00, 0x00})
		differPatch := int(cb.pos()) - 4
		// Equal bits — condition false anyway if the value is canonNaN.
		cb.emit(jitamd64.EmitMovRcxImm64(nil, value.CanonNaN()))
		cb.emit(jitamd64.EmitCmpRaxRcx(nil))
		cb.emit([]byte{0x0F, 0x84, 0x00, 0x00, 0x00, 0x00}) // je condFalse
		cb.addFixup(cb.pos()-4, cb.pos(), condFalse)
		cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00}) // jmp condTrue
		cb.addFixup(cb.pos()-4, cb.pos(), condTrue)
		// differ:
		writeRel32(cb, differPatch, int32(cb.pos())-int32(differPatch+4))
	} else {
		// je condTrue (equal bits are exact when NaN is ruled out)
		cb.emit([]byte{0x0F, 0x84, 0x00, 0x00, 0x00, 0x00})
		cb.addFixup(cb.pos()-4, cb.pos(), condTrue)
	}
	if needZero {
		// Differing bits — still equal iff both are +/-0 (OR of the
		// raw operands has zero magnitude: at most the sign bit set).
		cb.emit([]byte{0x48, 0x09, 0xC1})                   // or rcx, rax
		cb.emit([]byte{0x48, 0xD1, 0xE1})                   // shl rcx, 1
		cb.emit([]byte{0x0F, 0x84, 0x00, 0x00, 0x00, 0x00}) // je condTrue
		cb.addFixup(cb.pos()-4, cb.pos(), condTrue)
	}
	cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00}) // jmp condFalse
	cb.addFixup(cb.pos()-4, cb.pos(), condFalse)
	return true
}

// shim path.
//
// Semantics: cond = (RK(B) op RK(C)); if cond != A then pc++ (succSkip);
// else fall through to JMP target (succExec).
//
// Assumes both operands are numbers (no IsNumber guard); safe for
// numeric-hot loops.
//
// Sequence for LT (A=0):
//
//	movsd/movq xmm0, {B}
//	movsd/movq xmm1, {C}
//	ucomisd xmm0, xmm1
//	jb succExec              ; xmm0 < xmm1: cond true, A=0, cond!=A false
//	jmp succSkip
//
// For LE, use jbe. For A=1, senses invert (jae / ja).
func inlineNumericCompare(cb *codeBuf, op bytecode.OpCode, pc int32, a uint8, b, c int, succExec, succSkip int) bool {
	if op != bytecode.LT && op != bytecode.LE {
		return false
	}
	// K operands must be numeric (AnalyzeNative enforces; double-check
	// so a stale gate can't emit a bogus imm compare).
	for _, rk := range [2]int{b, c} {
		if rk >= 256 {
			kidx := rk - 256
			if cb.proto == nil || kidx < 0 || kidx >= len(cb.proto.Consts) {
				return false
			}
			if !value.IsNumber(value.Value(cb.proto.Consts[kidx])) {
				return false
			}
		}
	}
	// IsNumber guards for reg operands. A non-number reg (upvalue-fed
	// booleans, strings needing coercion, metamethod operands) must
	// route to host.Compare for byte-equal raise/coercion semantics —
	// fuzz seed d9bce2e240b2d69e caught the unguarded variant (P1
	// raised `attempt to compare number with boolean`, P4 silently
	// compared garbage bits).
	var guardFixups []int
	emitRegNumberGuard := func(reg int) {
		if reg >= 256 {
			return
		}
		cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(reg)*8))
		cb.emit(jitamd64.EmitMovRcxImm64(nil, qNanBoxBaseU64))
		cb.emit(jitamd64.EmitCmpRaxRcx(nil))
		cb.emit(jitamd64.EmitJaeRel32(nil, 0))
		guardFixups = append(guardFixups, int(cb.pos())-4)
	}
	emitRegNumberGuard(b)
	emitRegNumberGuard(c)
	if !inlineLoadOperandToXmm(cb, 0, b) {
		return false
	}
	if !inlineLoadOperandToXmm(cb, 1, c) {
		return false
	}
	// ucomisd xmm0, xmm1
	cb.emit(jitamd64.EmitUcomisdXmmXmm(nil, 0, 1))
	// Lua LT/LE semantics: if (RK(B) op RK(C)) != A then pc++ (skip
	// JMP). Fallthrough (skip JMP) == succs[1] == succSkip. JMP taken
	// == succs[0] == succExec. So we branch to succExec when the
	// condition MATCHES A.
	//
	// UCOMISD sets CF=1 iff xmm0 < xmm1. Cases:
	//
	//	LT + A=0: match when (B<C)==0, i.e., B>=C. Use jae (0x83).
	//	LT + A=1: match when (B<C)==1, i.e., B<C.  Use jb  (0x82).
	//	LE + A=0: match when (B<=C)==0, i.e., B>C. Use ja  (0x87).
	//	LE + A=1: match when (B<=C)==1, i.e., B<=C. Use jbe (0x86).
	//
	// Unordered (NaN operand) sets ZF=CF=PF=1, which the naive jcc
	// above resolves to the WRONG successor in all four cases (issue
	// #103, fuzz seed 765ba4598e721c69: `NaN < 0` judged true made a
	// non-terminating recursion terminate). PUC semantics: any ordered
	// comparison with NaN is FALSE, so the condition (B op C) is false
	// and "match" = (false == A):
	//
	//	A=1: no match -> succSkip.   A=0: match -> succExec.
	//
	// x86 has no jcc that tests the ordered relation AND routes
	// unordered to a chosen side, so pre-branch on PF (jp = unordered)
	// to the correct successor before the relation jcc — the mirror of
	// arm64's FP-safe MI/PL/LS/HI condition family (issue #37 step 7),
	// which needed no extra branch.
	nanTarget := succSkip
	if a == 0 {
		nanTarget = succExec
	}
	// 0F 8A rel32: jp (PF=1, unordered) -> nanTarget
	cb.emit([]byte{0x0F, 0x8A, 0x00, 0x00, 0x00, 0x00})
	cb.addFixup(cb.pos()-4, cb.pos(), nanTarget)
	var jccOpcode byte
	switch op {
	case bytecode.LT:
		if a == 0 {
			jccOpcode = 0x83 // jae -> succExec when B>=C
		} else {
			jccOpcode = 0x82 // jb  -> succExec when B<C
		}
	case bytecode.LE:
		if a == 0 {
			jccOpcode = 0x87 // ja  -> succExec when B>C
		} else {
			jccOpcode = 0x86 // jbe -> succExec when B<=C
		}
	}
	// 0F <jcc> rel32: 6 bytes
	cb.emit([]byte{0x0F, jccOpcode, 0x00, 0x00, 0x00, 0x00})
	patchOff := cb.pos() - 4
	cb.addFixup(patchOff, cb.pos(), succExec)
	// unconditional jmp to succSkip
	patchOff2 := cb.pos() + 1
	cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00})
	cb.addFixup(patchOff2, cb.pos(), succSkip)
	// Slow block: guard misses land here. Same exit-reason + branch
	// logic as emitCompare's non-inline tail (host.Compare packed result
	// via exitArg0).
	if len(guardFixups) > 0 {
		slowOff := int(cb.pos())
		for _, po := range guardFixups {
			writeRel32(cb, po, int32(slowOff)-int32(po+4))
		}
		emitCompareExitTail(cb, op, pc, a, b, c, succExec, succSkip)
	}
	return true
}

// emitCompareExitTail emits the HelperCompareSlow exit-reason + packed-
// result branch to succExec/succSkip. Shared by emitCompare's non-inline
// path and inlineNumericCompare's guard-miss slow block.
//
// The dispatcher runs host.Compare (op re-derived from proto.Code[pc] —
// EQ rides the same helper; host.Compare's doCompare handles all three)
// and stores packed&1 into exitArg0; the resume block (bound immediately
// — the branch decision must happen in-segment, not at the next op like
// linear exit-reason ops) reads it back and branches: result == A ->
// succExec, else succSkip. Errors (packed bit1) return status=1 from
// the dispatcher without reentry.
func emitCompareExitTail(cb *codeBuf, op bytecode.OpCode, pc int32, a uint8, b, c int, succExec, succSkip int) {
	// Seg2seg callee deopt: a compare guard miss while running as a
	// segment-to-segment callee unwinds the call chain instead of
	// exiting to host.Compare (issue #50 Spike 5).
	emitSegCallDeoptGuard(cb)
	emitExitReason(cb, jit.HelperCompareSlow, pc, int32(a), int32(b), int32(c))
	// Bind the resume entry right here (the only pending fixup is
	// ours: any prior op's was bound by this terminator's prelude).
	emitResumePreludeIfPending(cb)
	// mov rax, [r15 + exitArg0Off]  (7B: 49 8B 87 disp32)
	{
		off := int32(jit.JITContextExitArg0Offset)
		cb.emit([]byte{0x49, 0x8B, 0x87,
			byte(uint32(off)),
			byte(uint32(off) >> 8),
			byte(uint32(off) >> 16),
			byte(uint32(off) >> 24)})
	}
	// cmp rax, A
	cb.emit([]byte{0x48, 0x83, 0xF8, byte(a)})
	cb.emit([]byte{0x0F, 0x84, 0, 0, 0, 0}) // je succExec
	cb.addFixup(cb.pos()-4, cb.pos(), succExec)
	patchOff := cb.pos() + 1
	cb.emit([]byte{0xE9, 0, 0, 0, 0}) // jmp succSkip
	cb.addFixup(patchOff, cb.pos(), succSkip)
}

// emitEQ / LT / LE are thin wrappers.
func emitEQ(cb *codeBuf, pc int32, a uint8, b, c int, succExec, succSkip int) {
	emitCompare(cb, bytecode.EQ, pc, a, b, c, succExec, succSkip)
}
func emitLT(cb *codeBuf, pc int32, a uint8, b, c int, succExec, succSkip int) {
	emitCompare(cb, bytecode.LT, pc, a, b, c, succExec, succSkip)
}
func emitLE(cb *codeBuf, pc int32, a uint8, b, c int, succExec, succSkip int) {
	emitCompare(cb, bytecode.LE, pc, a, b, c, succExec, succSkip)
}

// emitTEST emits `if Truthy(R(A)) != C then pc++`. Inline (no shim):
//
//	mov rax, [rbx+A*8]
//	mov rcx, nilBits
//	cmp rax, rcx
//	je notTruthy
//	mov rcx, falseBits
//	cmp rax, rcx
//	je notTruthy
//	; truthy
//	<if C != 0: jmp succExec else jmp succSkip>
//
// notTruthy:
//
//	<if C == 0: jmp succExec else jmp succSkip>
//
// The decision "which succ on truthy" hinges on C.
//
// This lands as a shim call for now (simpler): use shimCompare? No,
// TEST has no shim. Inline it:
func emitTEST(cb *codeBuf, a, c uint8, succExec, succSkip int) {
	// mov rax, [rbx+A*8]
	cb.emit([]byte{0x48, 0x8B, 0x83,
		byte(uint32(int32(a) * 8)),
		byte(uint32(int32(a)*8) >> 8),
		byte(uint32(int32(a)*8) >> 16),
		byte(uint32(int32(a)*8) >> 24)})
	// mov rcx, nilBits (0xFFF8_0000_0000_0000)
	nilBits := uint64(0xFFF8000000000000)
	cb.emit([]byte{0x48, 0xB9})
	for i := 0; i < 8; i++ {
		cb.emit([]byte{byte(nilBits >> (8 * i))})
	}
	// cmp rax, rcx
	cb.emit([]byte{0x48, 0x39, 0xC8})
	// jz notTruthy: rel8 placeholder; patch after the truthy arm is
	// emitted so we don't have to hand-count intermediate bytes. Byte-
	// counting bit us in past rounds (see reflection
	// [[2026-07-01-p4-pj10-native-round]] lesson 6). rel8 range fits
	// easily for our worst-case layout (mov+cmp+jz+jmp rel32 = 20 bytes).
	jz1Off := cb.pos() + 1 // location of the rel8 byte
	cb.emit([]byte{0x74, 0x00})
	// mov rcx, falseBits (0xFFF9_0000_0000_0000)
	falseBits := uint64(0xFFF9000000000000)
	cb.emit([]byte{0x48, 0xB9})
	for i := 0; i < 8; i++ {
		cb.emit([]byte{byte(falseBits >> (8 * i))})
	}
	// cmp rax, rcx
	cb.emit([]byte{0x48, 0x39, 0xC8})
	// jz notTruthy (same rel8 patch)
	jz2Off := cb.pos() + 1
	cb.emit([]byte{0x74, 0x00})
	// truthy branch: pick succ based on C
	if c != 0 {
		// C != 0 && truthy => execute JMP => succExec
		patchOff := cb.pos() + 1
		cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00})
		cb.addFixup(patchOff, cb.pos(), succExec)
	} else {
		// C == 0 && truthy => pc++ => succSkip
		patchOff := cb.pos() + 1
		cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00})
		cb.addFixup(patchOff, cb.pos(), succSkip)
	}
	// notTruthy label position (this is where both jz's land).
	notTruthyOff := cb.pos()
	// Patch the two jz rel8 placeholders. rel8 is signed byte from the
	// end of the jz instruction (jz1Off+1 / jz2Off+1) to notTruthyOff.
	rel1 := int32(notTruthyOff) - (int32(jz1Off) + 1)
	rel2 := int32(notTruthyOff) - (int32(jz2Off) + 1)
	if rel1 < -128 || rel1 > 127 || rel2 < -128 || rel2 > 127 {
		panic("emitTEST: rel8 out of range - use rel32")
	}
	cb.bytes[jz1Off] = byte(int8(rel1))
	cb.bytes[jz2Off] = byte(int8(rel2))
	// falsy branch:
	if c != 0 {
		// C != 0 && falsy => pc++ => succSkip
		patchOff := cb.pos() + 1
		cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00})
		cb.addFixup(patchOff, cb.pos(), succSkip)
	} else {
		// C == 0 && falsy => execute JMP => succExec
		patchOff := cb.pos() + 1
		cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00})
		cb.addFixup(patchOff, cb.pos(), succExec)
	}
}

// emitTESTSET emits `if Truthy(R(B)) == C then R(A) := R(B) else pc++`.
// Inline (no shim), similar to emitTEST but with a source register B
// and destination A when the truthiness matches C.
//
// Semantics:
//   - truthy(R(B)) == (C != 0): R(A) := R(B), branch to succExec
//   - truthy(R(B)) != (C != 0): pc++, branch to succSkip
//
// Layout (byte offsets):
//
//	[00..06] mov rax, [rbx+B*8]        (7 bytes)
//	[07..16] mov rcx, nilBits          (10 bytes)
//	[17..19] cmp rax, rcx              (3 bytes)
//	[20..21] jz +N notTruthy           (2 bytes, rel8)
//	[22..31] mov rcx, falseBits        (10 bytes)
//	[32..34] cmp rax, rcx              (3 bytes)
//	[35..36] jz +M notTruthy           (2 bytes, rel8)
//	; truthy path
//	[37..NN] if C != 0: mov [rbx+A*8], rax; jmp succExec
//	         else:      jmp succSkip
//	notTruthy:
//	[NN..MM] if C != 0: jmp succSkip
//	         else:      mov [rbx+A*8], rax; jmp succExec
func emitTESTSET(cb *codeBuf, a, b, c uint8, succExec, succSkip int) {
	nilBits := uint64(value.Nil)
	falseBits := uint64(value.False)

	// mov rax, [rbx+B*8]
	cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(b)*8))
	// mov rcx, nilBits
	cb.emit(jitamd64.EmitMovRcxImm64(nil, nilBits))
	// cmp rax, rcx
	cb.emit(jitamd64.EmitCmpRaxRcx(nil))

	// We need the distance from the jz's next instruction to the
	// notTruthy label. The truthy path emits either:
	//   - c != 0: mov [rbx+A*8], rax (7 bytes) + jmp succExec (5 rel32) = 12
	//   - c == 0: jmp succSkip (5 rel32) = 5
	//
	// Then we have: mov rcx, falseBits (10) + cmp (3) + jz (2) = 15
	// which is between the first jz and the truthy path.
	//
	// First jz forward distance = 15 (rest of pre-truthy) + truthy-path-len.
	truthyLen := 5
	if c != 0 {
		truthyLen = 12
	}
	firstJzDelta := int8(15 + truthyLen)
	// jz +firstJzDelta
	cb.emit([]byte{0x74, byte(firstJzDelta)})
	// mov rcx, falseBits
	cb.emit(jitamd64.EmitMovRcxImm64(nil, falseBits))
	// cmp rax, rcx
	cb.emit(jitamd64.EmitCmpRaxRcx(nil))
	// jz +truthyLen (skip truthy path)
	cb.emit([]byte{0x74, byte(truthyLen)})
	// Truthy path
	if c != 0 {
		// mov [rbx+A*8], rax
		cb.emit(jitamd64.EmitMovqMemRegFromRax(nil, regRBX, int32(a)*8))
		// jmp succExec
		patchOff := cb.pos() + 1
		cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00})
		cb.addFixup(patchOff, cb.pos(), succExec)
	} else {
		// jmp succSkip
		patchOff := cb.pos() + 1
		cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00})
		cb.addFixup(patchOff, cb.pos(), succSkip)
	}
	// notTruthy path
	if c != 0 {
		// jmp succSkip
		patchOff := cb.pos() + 1
		cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00})
		cb.addFixup(patchOff, cb.pos(), succSkip)
	} else {
		// mov [rbx+A*8], rax
		cb.emit(jitamd64.EmitMovqMemRegFromRax(nil, regRBX, int32(a)*8))
		// jmp succExec
		patchOff := cb.pos() + 1
		cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00})
		cb.addFixup(patchOff, cb.pos(), succExec)
	}
}

// ---------------------------------------------------------------------
// Loop ops (PJ10c)
// ---------------------------------------------------------------------

// emitFORPREP: preps a numeric for loop.
//
// Semantics (per Lua 5.1): coerce R(A), R(A+1), R(A+2) (init, limit,
// step) to number — raising "'for' initial value/limit/step must be a
// number" when coercion fails — then R(A) -= R(A+2) and jmp to FORLOOP.
//
// Fast path guards all three slots with IsNumber (raw < qNanBoxBase),
// then does the pre-decrement inline:
//
//	<IsNumber guard R(A)>      ; miss -> slow
//	<IsNumber guard R(A+1)>    ; miss -> slow
//	<IsNumber guard R(A+2)>    ; miss -> slow
//	movsd xmm0, [rbx+A*8]      ; R(A)
//	movsd xmm1, [rbx+(A+2)*8]  ; R(A+2)
//	subsd xmm0, xmm1
//	movsd [rbx+A*8], xmm0
//	jmp   succBB               ; FORLOOP block
//	slow:
//	  <seg2seg deopt guard> + HelperForPrep exit-reason
//	  <resume: jmp succBB>     ; host.ForPrep normalized the slots
//
// The guards close issue #78: without them a non-number limit's
// NaN-box was consumed as a NaN double, FORLOOP's comparison went
// false, and the loop exited zero-iteration WITHOUT the error the
// interpreter raises. String limits that coerce ("10") now also
// match the interpreter (host.ForPrep coerces, then the resumed
// FORLOOP sees normalized numbers). NaN doubles (0/0) are genuine
// numbers below qNanBoxBase, so they stay on the fast path, where
// subsd/FORLOOP's unordered comparison matches the interpreter's
// zero-iteration semantics.
//
// This dodges the shim-from-mmap crash and keeps FORPREP + FORLOOP body
// entirely inline — enabling heavy_arith and heavy_recursion kernels to
// go native.
func emitFORPREP(cb *codeBuf, pc int32, a uint8, targetBB int) {
	// IsNumber guards on R(A), R(A+1), R(A+2).
	var guardFixups []int
	for _, reg := range []uint8{a, a + 1, a + 2} {
		// mov rax, [rbx + reg*8]
		cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(reg)*8))
		// mov rcx, qNanBoxBase
		cb.emit(jitamd64.EmitMovRcxImm64(nil, qNanBoxBaseU64))
		// cmp rax, rcx
		cb.emit(jitamd64.EmitCmpRaxRcx(nil))
		// jae slow
		cb.emit(jitamd64.EmitJaeRel32(nil, 0))
		guardFixups = append(guardFixups, int(cb.pos())-4)
	}
	// movsd xmm0, [rbx+A*8]
	cb.emit(jitamd64.EmitMovsdXmmFromMem(nil, 0, regRBX, int32(a)*8))
	// movsd xmm1, [rbx+(A+2)*8]
	cb.emit(jitamd64.EmitMovsdXmmFromMem(nil, 1, regRBX, int32(a+2)*8))
	// subsd xmm0, xmm1
	cb.emit(jitamd64.EmitSubsdXmmXmm(nil, 0, 1))
	// movsd [rbx+A*8], xmm0
	cb.emit(jitamd64.EmitMovsdMemFromXmm(nil, 0, regRBX, int32(a)*8))
	// jmp targetBB
	emitJMP(cb, targetBB)

	// Slow block: guard misses land here.
	slowOff := int(cb.pos())
	for _, po := range guardFixups {
		writeRel32(cb, po, int32(slowOff)-int32(po+4))
	}
	// Running as a seg2seg callee: deopt-redo instead of exiting
	// mid-segment (the redo is safe: host.ForPrep either raises — no
	// partial state — or the whole call redoes on the baseline).
	emitSegCallDeoptGuard(cb)
	emitExitReason(cb, jit.HelperForPrep, pc, int32(a), 0, 0)
	// Resume entry: host.ForPrep normalized the slots and did the
	// pre-decrement; jump straight to the FORLOOP block. FORPREP is a
	// terminator, so the only pending fixup is ours — bind it here
	// (same pattern as emitCompareExitTail).
	emitResumePreludeIfPending(cb)
	emitJMP(cb, targetBB)
}

// emitLoopFuelBackEdge emits the issue #102 loop back-edge fuel guard
// followed by the back-edge jump:
//
//	sub dword [r15 + loopFuelOff], 1
//	jnz succBack                    ; fuel remaining — take the back edge
//	<seg2seg deopt guard>           ; callee: deopt-redo, never exit mid-seg
//	<exit-reason HelperLoopFuel>    ; host.LoopPreempt bills + refills + checks
//	resume: mov rbx, [r15+vsBaseOff]
//	jmp succBack
//
// A fully-inline loop body otherwise never reaches a Go-side billing
// point, so a budgeted State would run the whole loop to completion
// (277M iterations vs the interpreter's ms-scale budget error — see the
// issue). loopFuel is a separate counter from segCallFuel because the
// dispatcher refills segCallFuel on every resume, which would erase the
// back-edge drain for loops whose bodies round-trip through an
// exit-reason helper each iteration (see the loopFuel field doc).
// Unbudgeted States refill SegCallFuelUnlimited (1<<31), keeping the
// steady-state cost at one dec+jnz per iteration; budgeted States get a
// billing point every SegCallFuelBudgeted back-edges.
//
// Deopt on the exhausted path mirrors emitFORPREP's slow tail: a
// seg2seg callee must not exit mid-segment (the caller's `call` would
// misread the exit-reason RET as a normal return), so it deopts and the
// top-level redo re-runs the call on the host path, which bills per
// back-edge normally. host.LoopPreempt either raises — no partial
// state — or refills, so the redo is safe.
func emitLoopFuelBackEdge(cb *codeBuf, pc int32, succBack int) {
	// sub dword [r15 + loopFuelOff], 1   (41 83 AF disp32 01)
	{
		off := int32(jit.JITContextLoopFuelOffset)
		cb.emit([]byte{0x41, 0x83, 0xAF,
			byte(uint32(off)), byte(uint32(off) >> 8),
			byte(uint32(off) >> 16), byte(uint32(off) >> 24), 0x01})
	}
	// jnz succBack   (0F 85 rel32 fixup) — fuel remaining
	{
		patchOff := cb.pos() + 2
		cb.emit([]byte{0x0F, 0x85, 0, 0, 0, 0})
		cb.addFixup(patchOff, cb.pos(), succBack)
	}
	// Fuel exhausted.
	emitSegCallDeoptGuard(cb)
	emitExitReason(cb, jit.HelperLoopFuel, pc, 0, 0, 0)
	// Resume: LoopPreempt refilled the fuel; take the back edge. The
	// terminator cleared earlier pendings, so the only pending fixup is
	// ours (same pattern as emitFORPREP's resume bind).
	emitResumePreludeIfPending(cb)
	emitJMP(cb, succBack)
}

// emitFORLOOP emits the numeric for-loop back-edge:
//
//	R(A) += R(A+2)                   ; step index
//	if step > 0 then R(A) <= R(A+1)  ; check
//	   else            R(A) >= R(A+1)
//	if cond then R(A+3) := R(A); jmp back-edge (succBack)
//	else jmp fall-out (succOut)
//
// All slots are Lua number values (NaN-boxed doubles). Inline SSE:
//
//	movsd xmm0, [rbx+A*8]           ; index
//	movsd xmm2, [rbx+(A+2)*8]       ; step
//	addsd xmm0, xmm2                ; index += step
//	movsd [rbx+A*8], xmm0           ; store back
//	movsd xmm1, [rbx+(A+1)*8]       ; limit
//	xorpd xmm3, xmm3                ; zero
//	ucomisd xmm2, xmm3              ; step vs 0
//	ja stepPositive                 ; unordered/greater
//
//	; step <= 0:  cond = (index >= limit)
//	ucomisd xmm0, xmm1
//	jae condTrue
//	jmp condFalse
//
// stepPositive:
//
//	; step > 0:  cond = (index <= limit)
//	ucomisd xmm1, xmm0              ; compare limit vs index
//	jae condTrue
//	jmp condFalse
//
// condTrue:
//
//	movsd [rbx+(A+3)*8], xmm0       ; visible index
//	jmp fuel                        ; issue #102 back-edge fuel guard
//
// condFalse:
//
//	jmp succOut
//
// fuel:
//
//	<emitLoopFuelBackEdge>          ; dec fuel; jnz succBack; else
//	                                ; HelperLoopFuel round trip
//
// Total ~85 bytes plus the fuel guard. Non-trivial but avoids a shim
// call for the hot loop body.
func emitFORLOOP(cb *codeBuf, pc int32, a uint8, succBack, succOut int) {
	// movsd xmm0, [rbx+A*8]
	cb.emit(jitamd64.EmitMovsdXmmFromMem(nil, 0, regRBX, int32(a)*8))
	// movsd xmm2, [rbx+(A+2)*8]
	cb.emit(jitamd64.EmitMovsdXmmFromMem(nil, 2, regRBX, int32(a+2)*8))
	// addsd xmm0, xmm2
	cb.emit(jitamd64.EmitAddsdXmmXmm(nil, 0, 2))
	// movsd [rbx+A*8], xmm0
	cb.emit(jitamd64.EmitMovsdMemFromXmm(nil, 0, regRBX, int32(a)*8))
	// movsd xmm1, [rbx+(A+1)*8]
	cb.emit(jitamd64.EmitMovsdXmmFromMem(nil, 1, regRBX, int32(a+1)*8))
	// xorpd xmm3, xmm3  (0.66 f 57 db)
	cb.emit([]byte{0x66, 0x0F, 0x57, 0xDB})
	// ucomisd xmm2, xmm3
	cb.emit(jitamd64.EmitUcomisdXmmXmm(nil, 2, 3))
	// ja stepPositive (forward). Distance = size of (step<=0 branch).
	//   step<=0 block:
	//     ucomisd xmm0, xmm1     (4 bytes)
	//     jae condTrue           (rel8 2 bytes) forward to condTrue
	//     jmp condFalse          (rel8 2 bytes) forward to condFalse
	//   = 8 bytes
	//
	// stepPositive block:
	//     ucomisd xmm1, xmm0     (4 bytes)
	//     jae condTrue           (rel8 2 bytes) forward to condTrue
	//     jmp condFalse          (rel8 2 bytes) forward to condFalse
	//   = 8 bytes
	//
	// After both blocks: condTrue block:
	//     movsd [rbx+(A+3)*8], xmm0   (5 bytes disp8 or 9 disp32)
	//     jmp fuel (rel32, 5 bytes)   ; static +5 over condFalse
	//   = 14 bytes (with disp32 movsd)
	//
	// condFalse block:
	//     jmp succOut (rel32, 5 bytes)
	//   = 5 bytes
	//
	// fuel block (issue #102, emitLoopFuelBackEdge):
	//     sub fuel; jnz succBack; deopt guard + HelperLoopFuel tail
	//
	// Layout:
	//   [00..]  step<=0 (8 bytes)
	//   [08..]  stepPositive (8 bytes)
	//   [16..]  condTrue (14 bytes)
	//   [30..]  condFalse (5 bytes)
	//   [35..]  fuel
	//
	// ja to stepPositive: delta = 8 (skip step<=0 block).
	cb.emit([]byte{0x77, 8}) // ja +8

	// step <= 0 block (offset 0 relative to end of ja):
	// ucomisd xmm0, xmm1
	cb.emit(jitamd64.EmitUcomisdXmmXmm(nil, 0, 1))
	// Layout after `ja +8`, position P = pos just after `ja +8` (before this ucomisd).
	// step<=0 (8 bytes):     [P..P+7]
	//   ucomisd (4b):          [P..P+3]
	//   jae +N to condTrue:    [P+4..P+5]
	//   jmp +M to condFalse:   [P+6..P+7]
	// stepPositive (8 bytes): [P+8..P+15]
	//   ucomisd (4b):          [P+8..P+11]
	//   jae +N to condTrue:    [P+12..P+13]
	//   jmp +M to condFalse:   [P+14..P+15]
	// condTrue (13 bytes):    [P+16..P+28]
	//   movsd disp32 (8b):     [P+16..P+23]
	//   jmp rel32 (5b):        [P+24..P+28]
	// condFalse (5 bytes):    [P+29..P+33]
	//   jmp rel32 (5b):        [P+29..P+33]
	//
	// step<=0 jae: end at P+6, target condTrue P+16 => rel8 = +10
	// step<=0 jmp: end at P+8, target condFalse P+29 => rel8 = +21
	// stepPositive jae: end at P+14, target condTrue P+16 => rel8 = +2
	// stepPositive jmp: end at P+16, target condFalse P+29 => rel8 = +13
	// jae +10 to condTrue
	cb.emit([]byte{0x73, 10})
	// jmp +21 to condFalse
	cb.emit([]byte{0xEB, 21})

	// stepPositive block:
	// ucomisd xmm1, xmm0
	cb.emit(jitamd64.EmitUcomisdXmmXmm(nil, 1, 0))
	// jae +2 to condTrue
	cb.emit([]byte{0x73, 2})
	// jmp +13 to condFalse
	cb.emit([]byte{0xEB, 13})

	// condTrue block:
	// movsd [rbx+(A+3)*8], xmm0    (disp32 form always used)
	cb.emit(jitamd64.EmitMovsdMemFromXmm(nil, 0, regRBX, int32(a+3)*8))
	// jmp rel32 +5 -> fuel block (static: skip condFalse's 5-byte jmp).
	// The back edge itself is taken inside emitLoopFuelBackEdge — via
	// its jnz on the fuel decrement (issue #102).
	cb.emit([]byte{0xE9, 0x05, 0x00, 0x00, 0x00})

	// condFalse block:
	// jmp rel32 -> succOut
	{
		patchOff := cb.pos() + 1
		cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00})
		cb.addFixup(patchOff, cb.pos(), succOut)
	}

	// fuel block: dec fuel; jnz succBack; exhausted -> HelperLoopFuel.
	emitLoopFuelBackEdge(cb, pc, succBack)
}

// emitTFORLOOP was removed with the shim-call channel (issue #45):
// TFORLOOP is not in opSupported, and its resume must branch on the
// iterator result inside the segment — it needs a dedicated exit-reason
// shape (like emitCompareExitTail's immediate resume bind) when it
// earns acceptance. The emit loop errors out on it defensively.

// ---------------------------------------------------------------------
// Table + call ops (PJ10d)
// ---------------------------------------------------------------------

func emitGETTABLE(cb *codeBuf, pc int32, a, b uint8, c int) {
	// GETTABLE is a shim-based op — under issue #38 the mmap segment
	// can't safely call the Go shim, so we lower to the exit-reason
	// protocol: emit an inline fast path (ArrayHit) that runs entirely
	// inside the segment, plus a miss/slow tail that packs (op, args,
	// pc, resume-off) into jitCtx and RETs. nativeCode.Run's
	// dispatcher unpacks, calls host.GetTable, and reenters at
	// resume-off. The reload of RBX = vsBase from jitCtx at the resume
	// entry is handled by an emitPreloadVsBase call inserted before
	// the *next* linear op.
	emitGetTableExitReason(cb, pc, a, b, c)
}

// emitGetTableExitReason emits the GETTABLE lowering: optional inline
// ArrayHit fast path -> slow tail -> exit-reason payload -> RET.
//
// Slow-tail layout when the inline fast path is present:
//
//	<fast path>            ; guards + slot load + store R(A) + jmp done
//	slow:                  ; guards patched to jump here
//	  <exit-reason emit>   ; pack args + write resumeOff + ret
//	done:                  ; next op emits here (per-op preloadVsBase)
//
// When the inline path can't be built (no IC data / non-ArrayHit),
// we skip straight to the slow tail.
func emitGetTableExitReason(cb *codeBuf, pc int32, a, b uint8, c int) {
	// Fast path when the IC snapshot indicates ArrayHit: inline
	// guards + slot load in the mmap segment, miss falls through
	// to the exit-reason emit which routes the slow work
	// Go-side. Non-ArrayHit protos skip straight to the exit path.
	if emitInlineGetTableArrayHit(cb, pc, a, b, c) {
		return
	}
	if emitInlineGetTableNodeHit(cb, pc, a, b, c) {
		return
	}
	emitGetTableExitOnly(cb, pc, a, b, c)
}

// emitGetTableExitOnly emits just the exit-reason payload + RET,
// packing (a, b, c, pc, HelperGetTable) into jitCtx.exitArg0 and
// writing the next-op offset into jitCtx.resumeOff as a fixup that
// the codeBuf resolves after the next op begins emitting.
func emitGetTableExitOnly(cb *codeBuf, pc int32, a, b uint8, c int) {
	emitExitReason(cb, jit.HelperGetTable, pc, int32(a), int32(b), int32(c))
}

// emitExitReason is the shared exit-reason emit used by every shim-
// based op lowered through the exit-reason protocol (issue #38).
// Packs (helperCode, a, b, c, pc) into jitCtx.exitArg0, writes a
// pending resumeOff fixup into jitCtx.resumeOff, sets RAX to
// ExitInlineHelper, and RETs.
//
// Callers use per-op wrappers that pass their opcode's helper code
// and the (a, b, c) args in the same slots the corresponding host
// method expects. b and c fit up to 511 (RK width); a fits up to 255
// (register). pc up to 22 bits (4M instructions, plenty for any real
// Lua source).
func emitExitReason(cb *codeBuf, helperCode uint64, pc int32, a, b, c int32) {
	packed := helperCode |
		(uint64(uint32(a)&0xFF) << 16) |
		(uint64(uint32(b)&0x1FF) << 24) |
		(uint64(uint32(c)&0x1FF) << 33) |
		(uint64(uint32(pc)&0x3FFFFF) << 42)
	// mov rax, packed (10B)
	cb.emit(jitamd64.EmitMovRaxImm64(nil, packed))
	// mov [r15 + exitArg0Off], rax  (7B: 49 89 87 disp32)
	{
		off := int32(jit.JITContextExitArg0Offset)
		cb.emit([]byte{0x49, 0x89, 0x87,
			byte(uint32(off)),
			byte(uint32(off) >> 8),
			byte(uint32(off) >> 16),
			byte(uint32(off) >> 24)})
	}
	// mov dword [r15 + resumeOffOff], imm32  (8B: 41 C7 87 disp32 imm32)
	{
		off := int32(jit.JITContextResumeOffOffset)
		cb.emit([]byte{0x41, 0xC7, 0x87,
			byte(uint32(off)),
			byte(uint32(off) >> 8),
			byte(uint32(off) >> 16),
			byte(uint32(off) >> 24),
			0, 0, 0, 0, // placeholder imm32 patched by resume prelude
		})
		cb.markResumeOffFixup(int(cb.pos()) - 4)
	}
	// mov rax, ExitInlineHelper (10B) — segment exit status.
	cb.emit(jitamd64.EmitMovRaxImm64(nil, uint64(jit.ExitInlineHelper)))
	// ret
	cb.emit(jitamd64.EmitRet(nil))
}

// emitInlineGetTableArrayHit emits an inline array-hit fast path for
// GETTABLE when the compile-time IC snapshot at pc shows Kind ==
// ArrayHit. The fast path handles the extremely common
// `t[i]` where `i` is a small positive integer and `t`'s shape
// (tableRef + gen) matches the snapshot; anything else falls through
// to the shim.
//
// Returns true if the inline path was emitted (including the shim
// fallback tail). Caller then skips the plain shim emit.
//
// Register usage (all caller-saved from Go's POV — the mmap segment
// never calls a Go function on the fast path):
//
//	RAX / RCX / RDX / RSI / R11  scratch
//	XMM0 / XMM1                  scratch (SSE key conversion)
//	RBX = vsBase, R14 = G, R15 = jitCtx      preserved
//
// Layout:
//
//	[Guard 1: R(B) is Table (tag == 0xFFFC)]
//	[Guard 2: taddr low32 == snap.tableRef]
//	[Guard 3: word5 high32 (gen) == snap.gen]
//	[Load key from R(C) or K bake]
//	[Guard 4: IsNumber(key) — key < qNanBoxBase]
//	[SSE: convert f64→i32, verify integer, i32 in RDX]
//	[Guard 5: 1 <= idx <= asize (word1 low 32)]
//	[Load slot = *(arenaBase + arrayRef + (idx-1)*8)]
//	[Guard 6: slot != Nil]
//	[Store slot → R(A)]
//	[jmp done]
//	miss:
//	  <exit-reason HelperGetTable>   ; dispatcher runs host.GetTable
//	done:
//
// Each guard's `jae miss` / `jne miss` / `je miss` records a rel32
// patch site; once the miss block offset is known all fixups are patched.
func emitInlineGetTableArrayHit(cb *codeBuf, pc int32, a, b uint8, c int) bool {
	if cb.proto == nil || int(pc) < 0 || int(pc) >= len(cb.proto.IC) {
		return false
	}
	snap := cb.proto.IC[pc]
	if snap.Kind != bytecode.ICKindArrayHit {
		return false
	}
	// Pre-emit K sanity check (bot review 2026-07-02): if the K operand
	// index is out of range, we must bail BEFORE emitting any bytes —
	// bailing mid-emit would leave forward jne/jae/je rel32 fixups
	// unpatched and corrupt the mmap segment. Fail-early keeps the
	// caller's fallthrough-to-shim byte-exact.
	if c >= 256 {
		kidx := c - 256
		if kidx < 0 || kidx >= len(cb.proto.Consts) {
			return false
		}
	}

	arenaBaseOff := int32(jit.JITContextArenaBaseOffset)
	var guardFixups []int

	// Helper: patch a guard's rel32 forward jump to the shim block.
	// Records patch offset = pos-4 (rel32 is last 4 bytes of the emit).
	recordFixup := func() {
		guardFixups = append(guardFixups, int(cb.pos())-4)
	}

	// -----------------------------------------------------------------
	// Guard 1: R(B) is Table (high 16 bits == 0xFFFC).
	// -----------------------------------------------------------------
	// mov rax, [rbx + B*8]   (7B)
	cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(b)*8))
	// mov rcx, rax            (3B: 48 89 C1)
	cb.emit([]byte{0x48, 0x89, 0xC1})
	// shr rcx, 48             (4B: 48 C1 E9 30)
	cb.emit([]byte{0x48, 0xC1, 0xE9, 0x30})
	// cmp ecx, 0xFFFC         (6B: 81 F9 FC FF 00 00)
	cb.emit([]byte{0x81, 0xF9, 0xFC, 0xFF, 0x00, 0x00})
	// jne shim                (6B: 0F 85 rel32)
	cb.emit(jitamd64.EmitJneRel32(nil, 0))
	recordFixup()

	// -----------------------------------------------------------------
	// GCRef extract: rcx = rax & payloadMask (0x0000_FFFF_FFFF_FFFF).
	// -----------------------------------------------------------------
	// mov rcx, rax  (3B)
	cb.emit([]byte{0x48, 0x89, 0xC1})
	// mov rdx, payloadMask  (10B)
	cb.emit(jitamd64.EmitMovRdxImm64(nil, 0x0000_FFFF_FFFF_FFFF))
	// and rcx, rdx  (3B: 48 21 D1)
	cb.emit([]byte{0x48, 0x21, 0xD1})

	// NOTE: no TableRef / gen identity guards here. The fast path reads
	// asize and arrayRef from the live table at runtime, so it is
	// correct for ANY table value in R(B): a non-Nil array slot read
	// never consults __index per Lua 5.1 semantics, and the bounds
	// check uses the live asize. The IC snapshot only gates WHICH pc
	// sites get the inline emit (AnalyzeNative requires ArrayHit); it
	// does not pin the table identity. This matters for workloads that
	// rebuild tables per call (e.g. fannkuch's fresh p/q/s per run) —
	// identity guards would miss on every access after the first run.

	// -----------------------------------------------------------------
	// Load arena base to R11: mov r11, [r15 + arenaBaseOff]  (7B)
	// Encoding: 4D 8B 9F disp32   (REX.W|R|B = 0x4D, opcode 0x8B, ModRM
	// mod=10 reg=011(R11) rm=111(R15)).
	// -----------------------------------------------------------------
	cb.emit([]byte{0x4D, 0x8B, 0x9F,
		byte(arenaBaseOff),
		byte(arenaBaseOff >> 8),
		byte(arenaBaseOff >> 16),
		byte(arenaBaseOff >> 24)})

	// -----------------------------------------------------------------
	// Load key from RK(C) into RAX. K path goes through the const table.
	// -----------------------------------------------------------------
	if c < 256 {
		// mov rax, [rbx + C*8]
		cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(c)*8))
	} else {
		// Pre-check at function entry guaranteed kidx is in range;
		// index directly without a mid-emit bailout.
		cb.emit(jitamd64.EmitMovRaxImm64(nil, cb.proto.Consts[c-256]))
	}

	// -----------------------------------------------------------------
	// Guard 4: IsNumber(key) — key < qNanBoxBase (0xFFF8_0000_0000_0000).
	// -----------------------------------------------------------------
	// mov rdx, qNanBoxBase  (10B)
	cb.emit(jitamd64.EmitMovRdxImm64(nil, qNanBoxBaseU64))
	// cmp rax, rdx  (3B: 48 39 D0)
	cb.emit([]byte{0x48, 0x39, 0xD0})
	// jae shim (>= qNanBoxBase means non-number)
	cb.emit(jitamd64.EmitJaeRel32(nil, 0))
	recordFixup()

	// -----------------------------------------------------------------
	// Convert f64 key to i32 index: RDX = trunc(f64 key). Verify the
	// key was integer-valued via cvtsi2sd + ucomisd round-trip.
	// -----------------------------------------------------------------
	// movq xmm0, rax  (5B: 66 48 0F 6E C0)
	cb.emit([]byte{0x66, 0x48, 0x0F, 0x6E, 0xC0})
	// cvttsd2si edx, xmm0  (4B: F2 0F 2C D0)
	cb.emit([]byte{0xF2, 0x0F, 0x2C, 0xD0})
	// cvtsi2sd xmm1, edx   (4B: F2 0F 2A CA)
	cb.emit([]byte{0xF2, 0x0F, 0x2A, 0xCA})
	// ucomisd xmm0, xmm1   (4B: 66 0F 2E C1)
	cb.emit([]byte{0x66, 0x0F, 0x2E, 0xC1})
	// jne shim   (fractional part or unordered)
	cb.emit(jitamd64.EmitJneRel32(nil, 0))
	recordFixup()
	// jp  shim   (NaN → PF set)
	// 0F 8A rel32  (6B)
	cb.emit([]byte{0x0F, 0x8A, 0, 0, 0, 0})
	recordFixup()

	// -----------------------------------------------------------------
	// Guard 5a: 1 <= idx (signed). cmp edx, 1; jl shim.
	// -----------------------------------------------------------------
	// cmp edx, 1  (3B: 83 FA 01)
	cb.emit([]byte{0x83, 0xFA, 0x01})
	// jl shim  (6B: 0F 8C rel32)
	cb.emit([]byte{0x0F, 0x8C, 0, 0, 0, 0})
	recordFixup()

	// -----------------------------------------------------------------
	// Guard 5b: idx <= asize. asize = word1 low 32 = [r11+rcx+8].
	// mov eax, [r11 + rcx + 8]  (5B: 41 8B 44 0B 08)
	//   REX.B = 0x41 (only R11); opcode 8B; ModRM mod=01 reg=000(EAX) rm=100(SIB)
	//   SIB = 0x0B (scale=00 index=001(RCX) base=011(R11)); disp8 = 8
	// -----------------------------------------------------------------
	cb.emit([]byte{0x41, 0x8B, 0x44, 0x0B, 0x08})
	// cmp edx, eax  (2B: 39 C2)
	cb.emit([]byte{0x39, 0xC2})
	// ja shim  (idx > asize; edx >= 1 verified so unsigned ja is correct)
	cb.emit(jitamd64.EmitJaRel32(nil, 0))
	recordFixup()

	// -----------------------------------------------------------------
	// Load arrayRef = word2 = [r11 + rcx + 16] → RAX.
	// mov rax, [r11 + rcx + 16]   (5B: 49 8B 44 0B 10)
	// -----------------------------------------------------------------
	cb.emit([]byte{0x49, 0x8B, 0x44, 0x0B, 0x10})
	// add rax, r11    (3B: 4C 01 D8)
	//   REX.W|R = 0x4C; opcode 01; ModRM mod=11 reg=011(R11) rm=000(RAX)
	cb.emit([]byte{0x4C, 0x01, 0xD8})

	// -----------------------------------------------------------------
	// Load slot = [rax + rdx*8 - 8]  (Lua indices are 1-based)
	// Encoding: 48 8B 54 D0 F8  (5B)
	//   REX.W = 0x48; opcode 8B; ModRM mod=01 reg=010(RDX) rm=100(SIB)
	//   SIB = 0xD0 (scale=11(*8) index=010(RDX) base=000(RAX)); disp8 = -8 = 0xF8
	// -----------------------------------------------------------------
	cb.emit([]byte{0x48, 0x8B, 0x54, 0xD0, 0xF8})

	// -----------------------------------------------------------------
	// Guard 6: slot != Nil (0xFFFE_0000_0000_0000).
	// -----------------------------------------------------------------
	// mov rax, NilBits  (10B)
	cb.emit(jitamd64.EmitMovRaxImm64(nil, 0xFFFE_0000_0000_0000))
	// cmp rdx, rax  (3B: 48 39 C2)
	cb.emit([]byte{0x48, 0x39, 0xC2})
	// je shim  (slot is Nil → helper handles __index / miss path)
	cb.emit(jitamd64.EmitJeRel32(nil, 0))
	recordFixup()

	// -----------------------------------------------------------------
	// Store R(A) = rdx.  mov [rbx + A*8], rdx   (7B: 48 89 93 disp32)
	// -----------------------------------------------------------------
	{
		disp := int32(a) * 8
		cb.emit([]byte{0x48, 0x89, 0x93,
			byte(uint32(disp)),
			byte(uint32(disp) >> 8),
			byte(uint32(disp) >> 16),
			byte(uint32(disp) >> 24)})
	}

	// jmp done (5B; rel32 patched after shim emit).
	cb.emit([]byte{0xE9, 0, 0, 0, 0})
	fastPathJmpOff := int(cb.pos()) - 4

	// Miss block starts here — patch all guard fixups. The miss
	// path used to `emitCallShim(shimGetTable)` inside the mmap
	// segment, but that's unsafe under concurrent load (issue #38);
	// use the exit-reason protocol instead so the dispatcher does
	// the shim work Go-side. Since exit-reason emits a RET, the
	// fastPathJmpOff jmp lands past the whole miss block onto the
	// next op's entry. When running as a seg2seg callee the miss
	// deopts instead (issue #50: GETTABLE ArrayHit sites are
	// seg2seg-eligible — the inline read is side-effect free, so a
	// deopt redo is idempotent).
	shimOff := int(cb.pos())
	for _, po := range guardFixups {
		rel := int32(shimOff) - int32(po+4)
		writeRel32(cb, po, rel)
	}
	emitSegCallDeoptGuard(cb)
	emitExitReason(cb, jit.HelperGetTable, pc, int32(a), int32(b), int32(c))

	// Done block — patch fast-path jmp so it skips past the exit
	// block on hit. Also mark the fastPathJmp offset as landing at
	// the next op's entry (not the exit block).
	doneOff := int(cb.pos())
	writeRel32(cb, fastPathJmpOff, int32(doneOff)-int32(fastPathJmpOff+4))
	return true
}

func emitSETTABLE(cb *codeBuf, pc int32, a uint8, b, c int) {
	if emitInlineSetTableArrayHit(cb, pc, a, b, c) {
		return
	}
	if emitInlineSetTableNodeHit(cb, pc, a, b, c) {
		return
	}
	emitExitReason(cb, jit.HelperSetTable, pc, int32(a), int32(b), int32(c))
}

// emitTableNodeHitPrelude emits the shared table prelude for the amd64
// GETTABLE/SETTABLE NodeHit inline fast paths (issue #67, ported from the
// arm64 version after M5 Pro measurement corrected the guard set): IsTable
// guard on R(tblReg), GCRef extract, arena base load, hmask bounds guard,
// gen (Shape) guard, and nodeRef resolution. Guard misses append their
// rel32 patch offsets to *guardFixups.
//
// No table-identity (TableRef) guard — deliberately. The identity guard
// made the inline useless on the workloads it exists for (n-body, M5 Pro:
// 875k dispatches/run with the guard, 0 inline hits): sites like
// `bodies[i].x` rotate over same-shaped tables, and tables built per Run
// land at fresh arena offsets, so a single baked TableRef misses 100%
// across Runs. Correctness never needed identity: the node's OWN key field
// identifies the entry — the caller's NodeKey guard (node[Index].key ==
// stableKey) proves node[Index] is THIS table's entry for K (a key occurs
// in at most one node), so reading/writing its val is byte-equal to the
// host path for whatever table is in R(tblReg). The hmask bounds guard
// (Index <= hmask, word1[63:32]) plus the nodeRef != 0 guard keep the
// node[Index] access in-bounds for smaller-hashed / hash-less tables,
// which then miss here (or on NodeKey) and fall to the host — slower,
// never wrong.
//
// Register state on fall-through (all guards passed):
//
//	R11 = arena base
//	RCX = absolute node segment base (nodeRef + arena base)
//
// RAX/RDX are scratch and not preserved.
func emitTableNodeHitPrelude(cb *codeBuf, tblReg uint8, index, shape uint32, guardFixups *[]int) {
	arenaBaseOff := int32(jit.JITContextArenaBaseOffset)
	recordFixup := func() {
		*guardFixups = append(*guardFixups, int(cb.pos())-4)
	}

	// Guard 1: R(tblReg) is Table (high 16 bits == 0xFFFC). RAX = R(tblReg).
	cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(tblReg)*8))
	cb.emit([]byte{0x48, 0x89, 0xC1})                   // mov rcx, rax
	cb.emit([]byte{0x48, 0xC1, 0xE9, 0x30})             // shr rcx, 48
	cb.emit([]byte{0x81, 0xF9, 0xFC, 0xFF, 0x00, 0x00}) // cmp ecx, 0xFFFC
	cb.emit(jitamd64.EmitJneRel32(nil, 0))
	recordFixup()

	// rcx = R(tblReg) payload (GCRef) = rax & 0x0000_FFFF_FFFF_FFFF.
	cb.emit([]byte{0x48, 0x89, 0xC1})                             // mov rcx, rax
	cb.emit(jitamd64.EmitMovRdxImm64(nil, 0x0000_FFFF_FFFF_FFFF)) // mov rdx, mask
	cb.emit([]byte{0x48, 0x21, 0xD1})                             // and rcx, rdx

	// r11 = arena base. Table words are read as [r11 + rcx + off].
	cb.emit([]byte{0x4D, 0x8B, 0x9F,
		byte(arenaBaseOff), byte(arenaBaseOff >> 8),
		byte(arenaBaseOff >> 16), byte(arenaBaseOff >> 24)}) // mov r11, [r15+arenaBaseOff]

	// Guard 2: hmask bounds — index <= hmask (word1[63:32] at [r11+rcx+8]).
	// Miss (unsigned hmask < index) → node[Index] would be out of bounds;
	// fall to the host.
	// mov rax, [r11+rcx+8]  (5B: 49 8B 44 0B 08); shr rax, 32; cmp eax, index; jb miss
	cb.emit([]byte{0x49, 0x8B, 0x44, 0x0B, 8})
	cb.emit([]byte{0x48, 0xC1, 0xE8, 0x20}) // shr rax, 32
	cb.emit([]byte{0x3D,
		byte(index), byte(index >> 8),
		byte(index >> 16), byte(index >> 24)}) // cmp eax, imm32
	cb.emit(jitamd64.EmitJbRel32(nil, 0))
	recordFixup()

	// Guard 3: gen == shape. gen = word5[63:32] = [r11+rcx+40] >> 32.
	cb.emit([]byte{0x49, 0x8B, 0x44, 0x0B, 40}) // mov rax, [r11+rcx+40]
	cb.emit([]byte{0x48, 0xC1, 0xE8, 0x20})     // shr rax, 32
	cb.emit([]byte{0x3D,
		byte(shape), byte(shape >> 8),
		byte(shape >> 16), byte(shape >> 24)}) // cmp eax, imm32
	cb.emit(jitamd64.EmitJneRel32(nil, 0))
	recordFixup()

	// rax = nodeRef (word3 at [r11+rcx+24]). Guard 4: nodeRef != 0 —
	// hmask==0 is ambiguous between "hash size 1" and "no hash segment"
	// (nodeRef==0); the latter would alias node[0] onto arena offset 0.
	cb.emit([]byte{0x49, 0x8B, 0x44, 0x0B, 24}) // mov rax, [r11+rcx+24]
	cb.emit([]byte{0x48, 0x85, 0xC0})           // test rax, rax
	cb.emit(jitamd64.EmitJeRel32(nil, 0))
	recordFixup()

	// rcx = abs node base = nodeRef (rax) + arena base (r11).
	cb.emit([]byte{0x48, 0x89, 0xC1}) // mov rcx, rax
	cb.emit([]byte{0x4C, 0x01, 0xD9}) // add rcx, r11
}

func emitInlineGetTableNodeHit(cb *codeBuf, pc int32, a, b uint8, c int) bool {
	if cb.proto == nil || int(pc) < 0 || int(pc) >= len(cb.proto.IC) {
		return false
	}
	snap := cb.proto.IC[pc]
	if snap.Kind != bytecode.ICKindNodeHit {
		return false
	}
	if c < 256 {
		return false
	}
	kidx := c - 256
	if kidx < 0 || kidx >= len(cb.proto.Consts) {
		return false
	}
	stableKey := cb.proto.Consts[kidx]
	if value.Value(stableKey) == value.Nil {
		return false
	}
	var guardFixups []int
	emitTableNodeHitPrelude(cb, b, snap.Index, snap.Shape, &guardFixups)

	keyOff := int32(snap.Index) * 24
	valOff := int32(snap.Index)*24 + 8

	// Guard 5: NodeKey ([rcx+keyOff]) == stableKey.
	cb.emit([]byte{0x48, 0x8B, 0x81,
		byte(uint32(keyOff)), byte(uint32(keyOff) >> 8),
		byte(uint32(keyOff) >> 16), byte(uint32(keyOff) >> 24)}) // mov rax, [rcx+keyOff]
	cb.emit(jitamd64.EmitMovRdxImm64(nil, stableKey))
	cb.emit([]byte{0x48, 0x39, 0xD0}) // cmp rax, rdx
	cb.emit(jitamd64.EmitJneRel32(nil, 0))
	guardFixups = append(guardFixups, int(cb.pos())-4)

	// rax = NodeVal ([rcx+valOff]); Guard 6: rax != Nil.
	cb.emit([]byte{0x48, 0x8B, 0x81,
		byte(uint32(valOff)), byte(uint32(valOff) >> 8),
		byte(uint32(valOff) >> 16), byte(uint32(valOff) >> 24)}) // mov rax, [rcx+valOff]
	cb.emit(jitamd64.EmitMovRdxImm64(nil, 0xFFFE_0000_0000_0000)) // NilBits
	cb.emit([]byte{0x48, 0x39, 0xD0})                             // cmp rax, rdx
	cb.emit(jitamd64.EmitJeRel32(nil, 0))
	guardFixups = append(guardFixups, int(cb.pos())-4)

	// Store R(A) = rax; jmp done.
	cb.emit(jitamd64.EmitMovqMemRegFromRax(nil, regRBX, int32(a)*8))
	cb.emit([]byte{0xE9, 0, 0, 0, 0})
	fastPathJmpOff := int(cb.pos()) - 4

	// miss: seg2seg deopt guard (read idempotent) + exit-reason.
	missOff := int(cb.pos())
	for _, po := range guardFixups {
		writeRel32(cb, po, int32(missOff)-int32(po+4))
	}
	emitSegCallDeoptGuard(cb)
	emitExitReason(cb, jit.HelperGetTable, pc, int32(a), int32(b), int32(c))

	// done:
	doneOff := int(cb.pos())
	writeRel32(cb, fastPathJmpOff, int32(doneOff)-int32(fastPathJmpOff+4))
	return true
}

// emitInlineSetTableNodeHit emits the amd64 SETTABLE NodeHit inline fast
// path for `R(A)[K(B)] := RK(C)` when the IC snapshot is NodeHit and the
// key is a CONSTANT (B >= 256). Overwrites an existing key's value — a raw
// arena store, no rehash / __newindex / gen bump. Byte-equal to host
// icSetTable's NodeHit hit. Miss tail has NO seg2seg deopt guard (write
// has side effects). Uses the same identity-free prelude as GET.
func emitInlineSetTableNodeHit(cb *codeBuf, pc int32, a uint8, b, c int) bool {
	if cb.proto == nil || int(pc) < 0 || int(pc) >= len(cb.proto.IC) {
		return false
	}
	snap := cb.proto.IC[pc]
	if snap.Kind != bytecode.ICKindNodeHit {
		return false
	}
	if b < 256 {
		return false
	}
	bkidx := b - 256
	if bkidx < 0 || bkidx >= len(cb.proto.Consts) {
		return false
	}
	stableKey := cb.proto.Consts[bkidx]
	if value.Value(stableKey) == value.Nil {
		return false
	}
	if c >= 256 {
		if ckidx := c - 256; ckidx < 0 || ckidx >= len(cb.proto.Consts) {
			return false
		}
	}
	var guardFixups []int
	emitTableNodeHitPrelude(cb, a, snap.Index, snap.Shape, &guardFixups)

	keyOff := int32(snap.Index) * 24
	valOff := int32(snap.Index)*24 + 8

	// Guard 5: NodeKey ([rcx+keyOff]) == stableKey.
	cb.emit([]byte{0x48, 0x8B, 0x81,
		byte(uint32(keyOff)), byte(uint32(keyOff) >> 8),
		byte(uint32(keyOff) >> 16), byte(uint32(keyOff) >> 24)}) // mov rax, [rcx+keyOff]
	cb.emit(jitamd64.EmitMovRdxImm64(nil, stableKey))
	cb.emit([]byte{0x48, 0x39, 0xD0}) // cmp rax, rdx
	cb.emit(jitamd64.EmitJneRel32(nil, 0))
	guardFixups = append(guardFixups, int(cb.pos())-4)

	// Guard 6: existing NodeVal ([rcx+valOff]) != Nil (key exists).
	cb.emit([]byte{0x48, 0x8B, 0x81,
		byte(uint32(valOff)), byte(uint32(valOff) >> 8),
		byte(uint32(valOff) >> 16), byte(uint32(valOff) >> 24)}) // mov rax, [rcx+valOff]
	cb.emit(jitamd64.EmitMovRdxImm64(nil, 0xFFFE_0000_0000_0000)) // NilBits
	cb.emit([]byte{0x48, 0x39, 0xD0})                             // cmp rax, rdx
	cb.emit(jitamd64.EmitJeRel32(nil, 0))
	guardFixups = append(guardFixups, int(cb.pos())-4)

	// rax = RK(C) value; Guard 7: rax != Nil (writing Nil deletes).
	if c < 256 {
		cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(c)*8)) // mov rax, [rbx+C*8]
	} else {
		cb.emit(jitamd64.EmitMovRaxImm64(nil, cb.proto.Consts[c-256]))
	}
	// rdx still holds NilBits from Guard 6.
	cb.emit([]byte{0x48, 0x39, 0xD0}) // cmp rax, rdx
	cb.emit(jitamd64.EmitJeRel32(nil, 0))
	guardFixups = append(guardFixups, int(cb.pos())-4)

	// Store [rcx+valOff] = rax; jmp done.
	cb.emit([]byte{0x48, 0x89, 0x81,
		byte(uint32(valOff)), byte(uint32(valOff) >> 8),
		byte(uint32(valOff) >> 16), byte(uint32(valOff) >> 24)}) // mov [rcx+valOff], rax
	cb.emit([]byte{0xE9, 0, 0, 0, 0})
	fastPathJmpOff := int(cb.pos()) - 4

	// miss: NO seg2seg deopt guard (write has side effects) + exit-reason.
	missOff := int(cb.pos())
	for _, po := range guardFixups {
		writeRel32(cb, po, int32(missOff)-int32(po+4))
	}
	emitExitReason(cb, jit.HelperSetTable, pc, int32(a), int32(b), int32(c))

	// done:
	doneOff := int(cb.pos())
	writeRel32(cb, fastPathJmpOff, int32(doneOff)-int32(fastPathJmpOff+4))
	return true
}

// emitInlineSetTableArrayHit emits an inline array-hit fast path for
// SETTABLE (semantic: `R(A)[RK(B)] := RK(C)`). Mirrors
// emitInlineGetTableArrayHit but writes instead of reads, and adds an
// extra guard: the target slot must be non-Nil (writing Nil, or
// writing to a Nil slot, can trigger a rehash / gen bump and would
// invalidate the IC snapshot -- deopt to the exit-reason path).
//
// Register usage (fast path, all caller-saved):
//
//	RAX / RCX / RDX / RSI / R11         scratch
//	XMM0 / XMM1                          scratch (SSE key conversion)
//	RBX = vsBase, R14 = G, R15 = jitCtx  preserved
//
// Layout:
//
//	[Guard 1: R(A) is Table (tag == 0xFFFC)]
//	[Guard 2: taddr low32 == snap.tableRef]
//	[Guard 3: word5 high32 (gen) == snap.gen]
//	[Load key from RK(B)]
//	[Guard 4: IsNumber(key)]
//	[SSE: f64 -> i32 integer check, i32 in RDX]
//	[Guard 5: 1 <= idx <= asize (word1 low 32)]
//	[Load arrayRef (word2), compute absolute slot addr in RCX]
//	[Guard 6: slot != Nil (existing key)]
//	[Load value RK(C) -> RAX]
//	[Guard 7: value != Nil (writing Nil rehashes)]
//	[Store slot = value]
//	[jmp done]
//	miss:
//	  <exit-reason emit for HelperSetTable>
//	done:
func emitInlineSetTableArrayHit(cb *codeBuf, pc int32, a uint8, b, c int) bool {
	if cb.proto == nil || int(pc) < 0 || int(pc) >= len(cb.proto.IC) {
		return false
	}
	snap := cb.proto.IC[pc]
	if snap.Kind != bytecode.ICKindArrayHit {
		return false
	}
	// Pre-emit K-idx sanity for the RK(B) key + RK(C) value slots.
	for _, rk := range [2]int{b, c} {
		if rk >= 256 {
			kidx := rk - 256
			if kidx < 0 || kidx >= len(cb.proto.Consts) {
				return false
			}
		}
	}

	arenaBaseOff := int32(jit.JITContextArenaBaseOffset)
	var guardFixups []int
	recordFixup := func() {
		guardFixups = append(guardFixups, int(cb.pos())-4)
	}

	// --- Guard 1: R(A) is Table (high 16 == 0xFFFC) ---
	cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(a)*8))
	cb.emit([]byte{0x48, 0x89, 0xC1})                   // mov rcx, rax
	cb.emit([]byte{0x48, 0xC1, 0xE9, 0x30})             // shr rcx, 48
	cb.emit([]byte{0x81, 0xF9, 0xFC, 0xFF, 0x00, 0x00}) // cmp ecx, 0xFFFC
	cb.emit(jitamd64.EmitJneRel32(nil, 0))
	recordFixup()

	// --- GCRef extract: rcx = rax & payloadMask ---
	cb.emit([]byte{0x48, 0x89, 0xC1}) // mov rcx, rax
	cb.emit(jitamd64.EmitMovRdxImm64(nil, 0x0000_FFFF_FFFF_FFFF))
	cb.emit([]byte{0x48, 0x21, 0xD1}) // and rcx, rdx

	// NOTE: no TableRef / gen identity guards — same reasoning as
	// emitInlineGetTableArrayHit. Overwriting an existing non-Nil array
	// slot with a non-Nil value is a raw store for ANY table (no
	// __newindex, no rehash), and the bounds check reads the live asize.
	// The IC snapshot only gates which pc sites get the inline emit.

	// --- Load arena base to r11 ---
	cb.emit([]byte{0x4D, 0x8B, 0x9F,
		byte(arenaBaseOff),
		byte(arenaBaseOff >> 8),
		byte(arenaBaseOff >> 16),
		byte(arenaBaseOff >> 24)})

	// --- Load key from RK(B) into RAX ---
	if b < 256 {
		cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(b)*8))
	} else {
		cb.emit(jitamd64.EmitMovRaxImm64(nil, cb.proto.Consts[b-256]))
	}

	// --- Guard 4: IsNumber(key) ---
	cb.emit(jitamd64.EmitMovRdxImm64(nil, qNanBoxBaseU64))
	cb.emit([]byte{0x48, 0x39, 0xD0}) // cmp rax, rdx
	cb.emit(jitamd64.EmitJaeRel32(nil, 0))
	recordFixup()

	// --- Convert f64 to i32 (RDX) + verify integer via round-trip ---
	cb.emit([]byte{0x66, 0x48, 0x0F, 0x6E, 0xC0}) // movq xmm0, rax
	cb.emit([]byte{0xF2, 0x0F, 0x2C, 0xD0})       // cvttsd2si edx, xmm0
	cb.emit([]byte{0xF2, 0x0F, 0x2A, 0xCA})       // cvtsi2sd xmm1, edx
	cb.emit([]byte{0x66, 0x0F, 0x2E, 0xC1})       // ucomisd xmm0, xmm1
	cb.emit(jitamd64.EmitJneRel32(nil, 0))
	recordFixup()
	cb.emit([]byte{0x0F, 0x8A, 0, 0, 0, 0}) // jp miss
	recordFixup()

	// --- Guard 5a: 1 <= idx ---
	cb.emit([]byte{0x83, 0xFA, 0x01})       // cmp edx, 1
	cb.emit([]byte{0x0F, 0x8C, 0, 0, 0, 0}) // jl miss
	recordFixup()

	// --- Guard 5b: idx <= asize (word1 low32) ---
	cb.emit([]byte{0x41, 0x8B, 0x44, 0x0B, 0x08}) // mov eax, [r11 + rcx + 8]
	cb.emit([]byte{0x39, 0xC2})                   // cmp edx, eax
	cb.emit(jitamd64.EmitJaRel32(nil, 0))
	recordFixup()

	// --- Load arrayRef (word2) into RAX, compute absolute base in RCX ---
	cb.emit([]byte{0x49, 0x8B, 0x44, 0x0B, 0x10}) // mov rax, [r11 + rcx + 16]
	cb.emit([]byte{0x4C, 0x01, 0xD8})             // add rax, r11
	cb.emit([]byte{0x48, 0x89, 0xC1})             // mov rcx, rax  (rcx = absolute array base)

	// --- Load current slot value = [rcx + rdx*8 - 8] into RAX for Nil check ---
	// mov rax, [rcx + rdx*8 - 8]  (48 8B 44 D1 F8)
	cb.emit([]byte{0x48, 0x8B, 0x44, 0xD1, 0xF8})

	// --- Guard 6: existing slot != Nil (avoid gen-bump insert path) ---
	cb.emit(jitamd64.EmitMovRdxImm64(nil, 0xFFFE_0000_0000_0000)) // rdx = NilBits (clobbers our idx)
	cb.emit([]byte{0x48, 0x39, 0xD0})                             // cmp rax, rdx
	cb.emit(jitamd64.EmitJeRel32(nil, 0))
	recordFixup()

	// --- Load value from RK(C) into RAX ---
	if c < 256 {
		cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(c)*8))
	} else {
		cb.emit(jitamd64.EmitMovRaxImm64(nil, cb.proto.Consts[c-256]))
	}

	// --- Guard 7: value != Nil (writing Nil triggers a rehash) ---
	// rdx already has NilBits from Guard 6; reuse.
	cb.emit([]byte{0x48, 0x39, 0xD0}) // cmp rax, rdx
	cb.emit(jitamd64.EmitJeRel32(nil, 0))
	recordFixup()

	// --- Store slot: [rcx + <same offset>] = rax ---
	// But we clobbered rdx above and lost the index. Rather than
	// re-derive, keep the index alive. Simplification: recompute the
	// slot address via a scratch that survives Nil-loading, or
	// (cheaper) recompute idx from key here. We already have the
	// slot address baked in RCX + a fixed (idx-1)*8; but RDX
	// (holding idx) was clobbered. Re-derive from the key we still
	// have in xmm0 -> RDX.
	cb.emit([]byte{0xF2, 0x0F, 0x2C, 0xD0}) // cvttsd2si edx, xmm0 (recover idx)
	// mov [rcx + rdx*8 - 8], rax  (48 89 44 D1 F8)
	cb.emit([]byte{0x48, 0x89, 0x44, 0xD1, 0xF8})

	// --- Success: jmp done ---
	cb.emit([]byte{0xE9, 0, 0, 0, 0})
	fastPathJmpOff := int(cb.pos()) - 4

	// --- Miss block: exit-reason emit ---
	missOff := int(cb.pos())
	for _, po := range guardFixups {
		rel := int32(missOff) - int32(po+4)
		writeRel32(cb, po, rel)
	}
	emitExitReason(cb, jit.HelperSetTable, pc, int32(a), int32(b), int32(c))

	// --- Done: patch fast-path jump ---
	doneOff := int(cb.pos())
	writeRel32(cb, fastPathJmpOff, int32(doneOff)-int32(fastPathJmpOff+4))
	return true
}

func emitGETGLOBAL(cb *codeBuf, pc int32, a uint8, bx uint16) {
	// Inline NodeHit fast path first (mirrors P3 wasm emitGetGlobal):
	// globals table identity + key are compile-time constants, so a
	// gen check + node slot load suffices. Miss falls to exit-reason.
	if emitInlineGetGlobalNodeHit(cb, pc, a, bx) {
		return
	}
	// Exit-reason path: bx is up to 18 bits which doesn't fit a single
	// 9-bit arg slot, so split it across the b (low 9) and c (high 9)
	// slots; the dispatcher reassembles bx = b | c<<9.
	emitExitReason(cb, jit.HelperGetGlobal, pc, int32(a), int32(bx)&0x1FF, (int32(bx)>>9)&0x1FF)
}

// emitInlineGetGlobalNodeHit emits the GETGLOBAL NodeHit inline fast
// path. GETGLOBAL A Bx reads Gtable[K(Bx)]: the globals table byte
// offset (taddr) and the IC snapshot (node index + gen) are both
// compile-time constants, so the fast path is just:
//
//	[Guard: gen (word5 high32 at taddr+40) == snap.Shape]
//	[Load nodeRef = word3 at taddr+24, val = nodeRef + Index*24 + 8]
//	[Guard: val != Nil]
//	[Store val -> R(A)]
//	[jmp done]
//	miss: <exit-reason HelperGetGlobal>
//	done:
//
// Gen bumps on rehash / key insert-delete / setmetatable, so a stale
// snapshot fails the gen guard and routes to host.DoGetGlobal —
// byte-equal to the interpreter's icGetTable path.
func emitInlineGetGlobalNodeHit(cb *codeBuf, pc int32, a uint8, bx uint16) bool {
	if cb.proto == nil || cb.proto.GlobalsTaddr == 0 || int(pc) >= len(cb.proto.IC) {
		return false
	}
	snap := cb.proto.IC[pc]
	if snap.Kind != bytecode.ICKindNodeHit {
		return false
	}
	taddr := int32(cb.proto.GlobalsTaddr)
	arenaBaseOff := int32(jit.JITContextArenaBaseOffset)
	var guardFixups []int
	recordFixup := func() {
		guardFixups = append(guardFixups, int(cb.pos())-4)
	}

	// r11 = arena base
	cb.emit([]byte{0x4D, 0x8B, 0x9F,
		byte(arenaBaseOff), byte(arenaBaseOff >> 8),
		byte(arenaBaseOff >> 16), byte(arenaBaseOff >> 24)})

	// Guard: gen == snap.Shape. mov rax, [r11 + taddr + 40]; shr 32; cmp
	{
		disp := taddr + 40
		// mov rax, [r11 + disp32]  (49 8B 83 disp32)
		cb.emit([]byte{0x49, 0x8B, 0x83,
			byte(uint32(disp)), byte(uint32(disp) >> 8),
			byte(uint32(disp) >> 16), byte(uint32(disp) >> 24)})
	}
	cb.emit([]byte{0x48, 0xC1, 0xE8, 0x20}) // shr rax, 32
	cb.emit([]byte{0x3D,
		byte(snap.Shape), byte(snap.Shape >> 8),
		byte(snap.Shape >> 16), byte(snap.Shape >> 24)}) // cmp eax, imm32
	cb.emit(jitamd64.EmitJneRel32(nil, 0))
	recordFixup()

	// rcx = nodeRef (word3) = [r11 + taddr + 24]; make absolute
	{
		disp := taddr + 24
		// mov rcx, [r11 + disp32]  (49 8B 8B disp32)
		cb.emit([]byte{0x49, 0x8B, 0x8B,
			byte(uint32(disp)), byte(uint32(disp) >> 8),
			byte(uint32(disp) >> 16), byte(uint32(disp) >> 24)})
	}
	cb.emit([]byte{0x4C, 0x01, 0xD9}) // add rcx, r11

	// rax = node val = [rcx + Index*24 + 8]
	{
		valOff := int32(snap.Index)*24 + 8
		// mov rax, [rcx + disp32]  (48 8B 81 disp32)
		cb.emit([]byte{0x48, 0x8B, 0x81,
			byte(uint32(valOff)), byte(uint32(valOff) >> 8),
			byte(uint32(valOff) >> 16), byte(uint32(valOff) >> 24)})
	}

	// Guard: val != Nil
	cb.emit(jitamd64.EmitMovRdxImm64(nil, 0xFFFE_0000_0000_0000))
	cb.emit([]byte{0x48, 0x39, 0xD0}) // cmp rax, rdx
	cb.emit(jitamd64.EmitJeRel32(nil, 0))
	recordFixup()

	// Store R(A) = rax
	cb.emit(jitamd64.EmitMovqMemRegFromRax(nil, regRBX, int32(a)*8))

	// jmp done
	cb.emit([]byte{0xE9, 0, 0, 0, 0})
	fastPathJmpOff := int(cb.pos()) - 4

	// miss: exit-reason
	missOff := int(cb.pos())
	for _, po := range guardFixups {
		writeRel32(cb, po, int32(missOff)-int32(po+4))
	}
	emitExitReason(cb, jit.HelperGetGlobal, pc, int32(a), int32(bx)&0x1FF, (int32(bx)>>9)&0x1FF)

	// done:
	doneOff := int(cb.pos())
	writeRel32(cb, fastPathJmpOff, int32(doneOff)-int32(fastPathJmpOff+4))
	return true
}

func emitSETGLOBAL(cb *codeBuf, pc int32, a uint8, bx uint16) {
	// Inline NodeHit fast path (existing-key value overwrite; mirrors
	// P3 wasm emitSetGlobal). Miss falls to exit-reason.
	if emitInlineSetGlobalNodeHit(cb, pc, a, bx) {
		return
	}
	// Same bx split as emitGETGLOBAL.
	emitExitReason(cb, jit.HelperSetGlobal, pc, int32(a), int32(bx)&0x1FF, (int32(bx)>>9)&0x1FF)
}

// emitInlineSetGlobalNodeHit emits the SETGLOBAL NodeHit inline fast
// path: Gtable[K(Bx)] := R(A) when the key already exists (slot val
// non-Nil) and gen matches. Overwriting an existing non-Nil value
// never rehashes and never consults __newindex, so a raw store is
// byte-equal to the interpreter. Writing Nil (key deletion semantics)
// or a gen miss routes to host.DoSetGlobal.
func emitInlineSetGlobalNodeHit(cb *codeBuf, pc int32, a uint8, bx uint16) bool {
	if cb.proto == nil || cb.proto.GlobalsTaddr == 0 || int(pc) >= len(cb.proto.IC) {
		return false
	}
	snap := cb.proto.IC[pc]
	if snap.Kind != bytecode.ICKindNodeHit {
		return false
	}
	taddr := int32(cb.proto.GlobalsTaddr)
	arenaBaseOff := int32(jit.JITContextArenaBaseOffset)
	var guardFixups []int
	recordFixup := func() {
		guardFixups = append(guardFixups, int(cb.pos())-4)
	}

	// r11 = arena base
	cb.emit([]byte{0x4D, 0x8B, 0x9F,
		byte(arenaBaseOff), byte(arenaBaseOff >> 8),
		byte(arenaBaseOff >> 16), byte(arenaBaseOff >> 24)})

	// Guard: gen == snap.Shape
	{
		disp := taddr + 40
		cb.emit([]byte{0x49, 0x8B, 0x83,
			byte(uint32(disp)), byte(uint32(disp) >> 8),
			byte(uint32(disp) >> 16), byte(uint32(disp) >> 24)})
	}
	cb.emit([]byte{0x48, 0xC1, 0xE8, 0x20})
	cb.emit([]byte{0x3D,
		byte(snap.Shape), byte(snap.Shape >> 8),
		byte(snap.Shape >> 16), byte(snap.Shape >> 24)})
	cb.emit(jitamd64.EmitJneRel32(nil, 0))
	recordFixup()

	// rcx = absolute node slot addr base
	{
		disp := taddr + 24
		cb.emit([]byte{0x49, 0x8B, 0x8B,
			byte(uint32(disp)), byte(uint32(disp) >> 8),
			byte(uint32(disp) >> 16), byte(uint32(disp) >> 24)})
	}
	cb.emit([]byte{0x4C, 0x01, 0xD9}) // add rcx, r11

	valOff := int32(snap.Index)*24 + 8

	// Guard: existing slot val != Nil (key exists; delete goes slow)
	cb.emit([]byte{0x48, 0x8B, 0x81,
		byte(uint32(valOff)), byte(uint32(valOff) >> 8),
		byte(uint32(valOff) >> 16), byte(uint32(valOff) >> 24)}) // mov rax, [rcx+valOff]
	cb.emit(jitamd64.EmitMovRdxImm64(nil, 0xFFFE_0000_0000_0000))
	cb.emit([]byte{0x48, 0x39, 0xD0}) // cmp rax, rdx
	cb.emit(jitamd64.EmitJeRel32(nil, 0))
	recordFixup()

	// rax = R(A); Guard: new value != Nil (writing Nil deletes -> slow)
	cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(a)*8))
	cb.emit([]byte{0x48, 0x39, 0xD0}) // cmp rax, rdx (rdx still NilBits)
	cb.emit(jitamd64.EmitJeRel32(nil, 0))
	recordFixup()

	// Store [rcx + valOff] = rax
	cb.emit([]byte{0x48, 0x89, 0x81,
		byte(uint32(valOff)), byte(uint32(valOff) >> 8),
		byte(uint32(valOff) >> 16), byte(uint32(valOff) >> 24)})

	// jmp done
	cb.emit([]byte{0xE9, 0, 0, 0, 0})
	fastPathJmpOff := int(cb.pos()) - 4

	// miss:
	missOff := int(cb.pos())
	for _, po := range guardFixups {
		writeRel32(cb, po, int32(missOff)-int32(po+4))
	}
	emitExitReason(cb, jit.HelperSetGlobal, pc, int32(a), int32(bx)&0x1FF, (int32(bx)>>9)&0x1FF)

	// done:
	doneOff := int(cb.pos())
	writeRel32(cb, fastPathJmpOff, int32(doneOff)-int32(fastPathJmpOff+4))
	return true
}

// emitReturnDualSemantics emits a RETURN that branches on
// jitCtx.segCallDepth (issue #50 Spike 5):
//
//	mov rax, [r15 + segCallDepthOff]
//	test eax, eax
//	jz go_ret                     ; depth == 0 → Go-entered, exit segment
//	; --- in-segment teardown (segment-to-segment callee) ---
//	; moveResults: for k in 0..nret-1: [rbx - 8 + k*8] = [rbx + (A+k)*8]
//	;   (funcIdx = callee.base - 1, so its byte addr is rbx - 8)
//	ret                           ; return into the caller segment
//	go_ret:
//	xor eax, eax
//	ret                           ; exit segment; Go does DoReturn
//
// nret = b - 1 (RETURN.B). b == 0 (multret to top) is not produced for
// never-exits leaf callees (AnalyzeNative + ProtoNeverExitsSegment keep
// those on the exit-reason path), but guard against it by treating
// b == 0 as "no in-segment move" (falls through to the Go path via the
// depth check being the only branch — safe because a multret callee
// won't be a segment-to-segment target).
func emitReturnDualSemantics(cb *codeBuf, a, b uint8, pc int32, multiReturn bool) {
	// Stash retA/retB/retPC so the single-return Go-entered path's
	// DoReturn still works (multi-return sites carry a/b/pc in the
	// HelperReturn exit-reason instead).
	if !multiReturn && cb.proto != nil {
		cb.proto.RetA = int32(a)
		cb.proto.RetB = int32(b)
		cb.proto.RetPC = pc
	}
	// mov rax, [r15 + segCallDepthOff]   (49 8B 87 disp32)
	{
		off := int32(jit.JITContextSegCallDepthOffset)
		cb.emit([]byte{0x49, 0x8B, 0x87,
			byte(uint32(off)), byte(uint32(off) >> 8),
			byte(uint32(off) >> 16), byte(uint32(off) >> 24)})
	}
	// test eax, eax   (85 C0)
	cb.emit([]byte{0x85, 0xC0})
	// jz go_ret   (0F 84 rel32) — patched below
	jzOff := len(cb.bytes) + 2
	cb.emit([]byte{0x0F, 0x84, 0, 0, 0, 0})

	// In-segment teardown: moveResults via rbx-relative addressing.
	// funcIdx = callee.base - 1 → byte addr rbx - 8. Multret (b==0) is
	// rejected from seg2seg eligibility, so nret is statically known.
	nret := int32(b) - 1
	if b != 0 {
		for k := int32(0); k < nret; k++ {
			// mov rax, [rbx + (A+k)*8]   (48 8B 83 disp32)
			src := (int32(a) + k) * 8
			cb.emit([]byte{0x48, 0x8B, 0x83,
				byte(uint32(src)), byte(uint32(src) >> 8),
				byte(uint32(src) >> 16), byte(uint32(src) >> 24)})
			// mov [rbx - 8 + k*8], rax   (48 89 83 disp32)
			dst := -8 + k*8
			cb.emit([]byte{0x48, 0x89, 0x83,
				byte(uint32(dst)), byte(uint32(dst) >> 8),
				byte(uint32(dst) >> 16), byte(uint32(dst) >> 24)})
		}
	}
	// ret   (C3) — return into caller segment
	cb.emit([]byte{0xC3})

	// go_ret: (patch jz target here) — segCallDepth == 0, exit the segment.
	goRetPos := len(cb.bytes)
	writeRel32(cb, jzOff, int32(goRetPos)-int32(jzOff+4))
	if multiReturn {
		// Multi-return Proto: exit via HelperReturn carrying this site's
		// (a, b, pc); Run's dispatcher runs host.DoReturn and terminates.
		emitExitReason(cb, jit.HelperReturn, pc, int32(a), int32(b), 0)
		cb.pendingResumeOffFixups = cb.pendingResumeOffFixups[:0]
		return
	}
	// Single-return Proto: xor eax, eax; ret → Go-side DoReturn.
	cb.emit([]byte{0x31, 0xC0, 0xC3})
}

// emitGETUPVALInline emits GETUPVAL A B inline (issue #50 Spike 5) so a
// segment-to-segment callee never exits mid-execution to fetch an
// upvalue. It resolves the running frame's closure via
// jitCtx.currentClosureRef, reads upvalRef = closure word(2+B), then:
//
//   - closed upvalue: value = upval word2 (self-held).
//   - open upvalue, inline-safe (single thread → owner is the running
//     thread): value = owner.slot(stackIdx) via jitCtx.threadStackBase0.
//   - open upvalue, not inline-safe (coroutine may own it): fall back to
//     the HelperGetUpval exit-reason (or deopt when segCallDepth>0, so a
//     seg2seg subtree redoes at the top instead of exiting mid-segment).
//
// All arena reads recompute from jitCtx.arenaBase, so they survive an
// arena grow between Run entries. Clobbers rax/rcx/rdx (scratch between
// ops in the store-to-stack native model).
func emitGETUPVALInline(cb *codeBuf, a, b uint8) {
	// mov rdx, [r15 + arenaBaseOff]        (49 8B 97 disp32) — rdx = arenaBase
	emitMovRegFromR15Disp(cb, 0x97, int32(jit.JITContextArenaBaseOffset))
	// mov rax, [r15 + currentClosureRefOff](49 8B 87 disp32) — rax = closureRef
	emitMovRegFromR15Disp(cb, 0x87, int32(jit.JITContextCurrentClosureRefOffset))
	// add rax, rdx                         (48 01 D0) — rax = closureAddr
	cb.emit([]byte{0x48, 0x01, 0xD0})
	// mov rax, [rax + (2+B)*8]             (48 8B 80 disp32) — rax = upvalRef (GCRef)
	{
		disp := (2 + int32(b)) * 8
		cb.emit([]byte{0x48, 0x8B, 0x80,
			byte(uint32(disp)), byte(uint32(disp) >> 8),
			byte(uint32(disp) >> 16), byte(uint32(disp) >> 24)})
	}
	// add rax, rdx                         (48 01 D0) — rax = upvalAddr
	cb.emit([]byte{0x48, 0x01, 0xD0})
	// test dword [rax], upvalFlagClosed<<hdrFlagsShift (0x1000)  (F7 00 imm32)
	cb.emit([]byte{0xF7, 0x00, 0x00, 0x10, 0x00, 0x00})
	// jz open   (0F 84 rel32) — not closed → open path
	jzOpenOff := len(cb.bytes) + 2
	cb.emit([]byte{0x0F, 0x84, 0, 0, 0, 0})
	// --- closed: rax = [upvalAddr + 16] (upval word2) ---
	cb.emit([]byte{0x48, 0x8B, 0x40, 0x10}) // mov rax, [rax+16]
	// jmp store (E9 rel32) — patched below
	jmpStore1Off := len(cb.bytes) + 1
	cb.emit([]byte{0xE9, 0, 0, 0, 0})
	// open: (patch jz here)
	openPos := len(cb.bytes)
	writeRel32(cb, jzOpenOff, int32(openPos)-int32(jzOpenOff+4))
	// mov ecx, [r15 + inlineUpvalSafeOff]  (41 8B 8F disp32) — rcx = safe flag
	emitMovRegFromR15Disp32NoW(cb, 0x8F, int32(jit.JITContextInlineUpvalSafeOffset))
	// test ecx, ecx                        (85 C9)
	cb.emit([]byte{0x85, 0xC9})
	// jz fallback   (0F 84 rel32) — not safe → exit-reason / deopt
	jzFallbackOff := len(cb.bytes) + 2
	cb.emit([]byte{0x0F, 0x84, 0, 0, 0, 0})
	// mov ecx, [rax + 8]                   (8B 48 08) — rcx = stackIdx (low 32, zero-extended)
	cb.emit([]byte{0x8B, 0x48, 0x08})
	// mov rax, [r15 + threadStackBase0Off] (49 8B 87 disp32) — rax = threadStackBase0
	emitMovRegFromR15Disp(cb, 0x87, int32(jit.JITContextThreadStackBase0Offset))
	// mov rax, [rax + rcx*8]               (48 8B 04 C8) — rax = owner.slot(stackIdx)
	cb.emit([]byte{0x48, 0x8B, 0x04, 0xC8})
	// store: (patch jmp from closed here) — value in rax → R(A)
	storePos := len(cb.bytes)
	writeRel32(cb, jmpStore1Off, int32(storePos)-int32(jmpStore1Off+4))
	cb.emit(jitamd64.EmitMovqMemRegFromRax(nil, regRBX, int32(a)*8))
	// jmp done (E9 rel32) — skip the fallback block
	jmpDoneOff := len(cb.bytes) + 1
	cb.emit([]byte{0xE9, 0, 0, 0, 0})
	// fallback: (patch jz here) — deopt if nested, else exit-reason
	fallbackPos := len(cb.bytes)
	writeRel32(cb, jzFallbackOff, int32(fallbackPos)-int32(jzFallbackOff+4))
	emitSegCallDeoptGuard(cb)
	emitGETUPVAL(cb, a, b)
	// done: (patch inline-path jmp here)
	donePos := len(cb.bytes)
	writeRel32(cb, jmpDoneOff, int32(donePos)-int32(jmpDoneOff+4))
}

// emitMovRegFromR15Disp emits `mov <reg64>, [r15 + disp32]` given the
// ModRM middle byte (49 8B <modrm> disp32); modrm encodes the dest reg
// with mod=10, rm=111 (r15). E.g. 0x87 = rax, 0x97 = rdx.
func emitMovRegFromR15Disp(cb *codeBuf, modrm byte, off int32) {
	cb.emit([]byte{0x49, 0x8B, modrm,
		byte(uint32(off)), byte(uint32(off) >> 8),
		byte(uint32(off) >> 16), byte(uint32(off) >> 24)})
}

// emitMovRegFromR15Disp32NoW emits `mov <reg32>, [r15 + disp32]` (32-bit,
// no REX.W): 41 8B <modrm> disp32. modrm e.g. 0x8F = ecx.
func emitMovRegFromR15Disp32NoW(cb *codeBuf, modrm byte, off int32) {
	cb.emit([]byte{0x41, 0x8B, modrm,
		byte(uint32(off)), byte(uint32(off) >> 8),
		byte(uint32(off) >> 16), byte(uint32(off) >> 24)})
}

func emitNEWTABLE(cb *codeBuf, pc int32, a, b, c uint8) {
	emitExitReason(cb, jit.HelperNewTable, pc, int32(a), int32(b), int32(c))
}

func emitSETLIST(cb *codeBuf, pc int32, a, b, c uint8) {
	emitExitReason(cb, jit.HelperSetList, pc, int32(a), int32(b), int32(c))
}

func emitCALL(cb *codeBuf, pc int32, a, b, c uint8) {
	// Issue #50 EmitCallInline fast path — fires when:
	//   - callInlineEnabled = true (on since Spike 2; see the flag's
	//     doc in translator_native.go),
	//   - the CALL site has an IC slot in codeBufProto.CallSitePCs,
	//   - CALL shape is guardable: B != 0 (fixed nargs) and C != 0
	//     (fixed nresults; multret rejected — segment can't sync top
	//     mid-call).
	if callInlineEnabled && b != 0 && c != 0 && cb.proto != nil {
		if callSiteIdx := findCallSiteIndex(cb.proto.CallSitePCs, pc); callSiteIdx >= 0 {
			if emitCallInlineFastPath(cb, pc, a, b, c, callSiteIdx) {
				return
			}
		}
	}
	// Exit-reason path (issue #38): the mmap segment can't safely call
	// Go shims under nested/concurrent load. Run's dispatcher invokes
	// host.CallBaseline (synchronous callee completion) and reenters.
	emitExitReason(cb, jit.HelperCall, pc, int32(a), int32(b), int32(c))
}

// emitCallIntrinsicFastPath emits the issue #77 math intrinsic guard +
// inline body, called from emitCallInlineFastPath's guard-miss slow path
// (a math intrinsic callee is a host closure, so it always fails the
// normal protoID guard and lands here — meaning the hot Lua-callee
// seg2seg path pays ZERO intrinsic overhead). It appends "jmp done"
// rel32 fixup offsets to *doneFixups (the caller patches them to land
// past the whole CALL emit, alongside the seg2seg done jump). All guard
// misses fall through to the next byte, which the caller fills with the
// deopt guard + HelperCall exit-reason. The shape is pre-checked by the
// caller: c == 2 (single result), b == 2 (unary) or b == 3 (max/min).
//
// Guard (amd64):
//
//	mov rax, [rbx+A*8]        ; R(A)
//	mov rcx, rax              ; save full callee value for identity cmp
//	mov rdx, icSlotAddr       ; imm64
//	mov al, [rdx+6]           ; Flags
//	and al, IsIntrinsic|Stuck ; 0x90
//	cmp al, IsIntrinsic       ; 0x10 — intrinsic AND not stuck?
//	jne miss
//	mov rax, [rdx+24]         ; IntrinsicCalleeVal
//	cmp rax, rcx              ; R(A) == recorded closure?
//	jne miss
//	<IsNumber guard on each arg slot>
//	movzx eax, [rdx+7]        ; IntrinsicID
//	<dispatch to the body for this arity's candidate kinds>
//
// Each body computes the result into R(A), bumps IntrinsicHitCount, and
// jmp's done. sqrt/floor/ceil add a result-NaN guard (an SSE NaN in the
// tag-aliasing range routes to the host, which canonicalizes) mirroring
// emitInlineArithWithSlowPath; abs/max/min can't produce a tag-range
// value (result is one of the < qNanBoxBase inputs) so need none.
func emitCallIntrinsicFastPath(cb *codeBuf, a, b uint8, callSiteIdx int, doneFixups *[]int) {
	if cb.proto == nil || callSiteIdx < 0 || callSiteIdx >= len(cb.proto.CallICs) {
		return
	}
	icSlotAddr := uintptr(unsafe.Pointer(&cb.proto.CallICs[callSiteIdx]))

	var missFixups []int

	// rax = R(A); rcx = R(A) saved for the identity compare.
	cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(a)*8))
	cb.emit([]byte{0x48, 0x89, 0xC1}) // mov rcx, rax
	// rdx = icSlotAddr.
	cb.emit(jitamd64.EmitMovRdxImm64(nil, uint64(icSlotAddr)))
	// Flags gate: (Flags & (IsIntrinsic|Stuck)) == IsIntrinsic.
	cb.emit([]byte{0x8A, 0x42, callICFlagsByteOffset})             // mov al, [rdx+6]
	cb.emit([]byte{0x24, CallICFlagIsIntrinsic | CallICFlagStuck}) // and al, 0x90
	cb.emit([]byte{0x3C, CallICFlagIsIntrinsic})                   // cmp al, 0x10
	cb.emit([]byte{0x0F, 0x85, 0, 0, 0, 0})                        // jne miss
	missFixups = append(missFixups, len(cb.bytes)-4)
	// Identity gate: R(A) == IntrinsicCalleeVal.
	cb.emit([]byte{0x48, 0x8B, 0x42, callICIntrinsicValByteOffset}) // mov rax, [rdx+24]
	cb.emit([]byte{0x48, 0x39, 0xC8})                               // cmp rax, rcx
	cb.emit([]byte{0x0F, 0x85, 0, 0, 0, 0})                         // jne miss
	missFixups = append(missFixups, len(cb.bytes)-4)

	// IsNumber guard on each fixed argument (R(A+1) [, R(A+2)]). A number
	// is raw < qNanBoxBase; anything >= (tagged value, indefinite NaN)
	// routes to the host, which does string coercion / raises. rdx
	// (icSlotAddr) is preserved — these use rax/rcx only.
	nargs := int(b) - 1
	for i := 1; i <= nargs; i++ {
		cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(a+uint8(i))*8))
		cb.emit(jitamd64.EmitMovRcxImm64(nil, qNanBoxBaseU64))
		cb.emit(jitamd64.EmitCmpRaxRcx(nil))
		cb.emit(jitamd64.EmitJaeRel32(nil, 0)) // jae miss
		missFixups = append(missFixups, len(cb.bytes)-4)
	}

	// Dispatch on IntrinsicID (rdx still = icSlotAddr).
	cb.emit([]byte{0x0F, 0xB6, 0x42, callICIntrinsicIDByteOffset}) // movzx eax, byte [rdx+7]
	// Candidate kinds for this arity. je fixups resolved to each body.
	var kinds []uint8
	if b == 2 {
		kinds = []uint8{jit.IntrinsicSqrt, jit.IntrinsicFloor, jit.IntrinsicCeil, jit.IntrinsicAbs}
	} else { // b == 3
		kinds = []uint8{jit.IntrinsicMax, jit.IntrinsicMin}
	}
	jeFixups := make(map[uint8]int, len(kinds))
	for _, k := range kinds {
		cb.emit([]byte{0x3C, k})                // cmp al, kind
		cb.emit([]byte{0x0F, 0x84, 0, 0, 0, 0}) // je body_k
		jeFixups[k] = len(cb.bytes) - 4
	}
	// No candidate matched (e.g. max recorded at a 1-arg site): miss.
	cb.emit([]byte{0xE9, 0, 0, 0, 0}) // jmp miss
	missFixups = append(missFixups, len(cb.bytes)-4)

	emitBody := func(k uint8, emitOp func()) {
		writeRel32(cb, jeFixups[k], int32(len(cb.bytes))-int32(jeFixups[k]+4))
		emitOp()
		// inc qword [IntrinsicHitCountAddr]  (prove-the-path)
		cb.emit(jitamd64.EmitMovRaxImm64(nil, IntrinsicHitCountAddr()))
		cb.emit([]byte{0x48, 0xFF, 0x00})
		cb.emit([]byte{0xE9, 0, 0, 0, 0}) // jmp done
		*doneFixups = append(*doneFixups, len(cb.bytes)-4)
	}

	argOff := func(i int) int32 { return int32(a+uint8(i)) * 8 }
	dstOff := int32(a) * 8

	// Result-NaN guard shared by sqrt/floor/ceil: an SSE NaN in the
	// tag-aliasing range (>= qNanBoxBase) would be misread as a boxed
	// value; route it to the host, which canonicalizes (mirrors
	// emitInlineArithWithSlowPath). xmm0 holds the result.
	emitResultNaNGuard := func() {
		cb.emit([]byte{0x66, 0x48, 0x0F, 0x7E, 0xC0}) // movq rax, xmm0
		cb.emit(jitamd64.EmitMovRcxImm64(nil, qNanBoxBaseU64))
		cb.emit(jitamd64.EmitCmpRaxRcx(nil))
		cb.emit(jitamd64.EmitJaeRel32(nil, 0)) // jae miss
		missFixups = append(missFixups, len(cb.bytes)-4)
	}

	if b == 2 {
		emitBody(jit.IntrinsicSqrt, func() {
			cb.emit(jitamd64.EmitSqrtsdXmmFromMem(nil, 0, regRBX, argOff(1)))
			emitResultNaNGuard()
			cb.emit(jitamd64.EmitMovsdMemFromXmm(nil, 0, regRBX, dstOff))
		})
		emitBody(jit.IntrinsicFloor, func() {
			cb.emit(jitamd64.EmitRoundsdXmmFromMem(nil, 0, regRBX, argOff(1), 1))
			emitResultNaNGuard()
			cb.emit(jitamd64.EmitMovsdMemFromXmm(nil, 0, regRBX, dstOff))
		})
		emitBody(jit.IntrinsicCeil, func() {
			cb.emit(jitamd64.EmitRoundsdXmmFromMem(nil, 0, regRBX, argOff(1), 2))
			emitResultNaNGuard()
			cb.emit(jitamd64.EmitMovsdMemFromXmm(nil, 0, regRBX, dstOff))
		})
		emitBody(jit.IntrinsicAbs, func() {
			// mov rax, [rbx+arg]; btr rax, 63 (clear sign); mov [rbx+A], rax.
			cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, argOff(1)))
			cb.emit([]byte{0x48, 0x0F, 0xBA, 0xF0, 0x3F}) // btr rax, 63
			cb.emit(jitamd64.EmitMovqMemRegFromRax(nil, regRBX, dstOff))
		})
	} else { // b == 3: max / min
		// Both load a into xmm0, b into xmm1, then select per Lua
		// semantics (out = a; replace with b only when the strict
		// comparison holds — NaN keeps a, matching Go >/< on float64).
		emitMinMax := func(cmpFirst uint8, cmpSecond uint8) {
			cb.emit(jitamd64.EmitMovsdXmmFromMem(nil, 0, regRBX, argOff(1))) // xmm0 = a
			cb.emit(jitamd64.EmitMovsdXmmFromMem(nil, 1, regRBX, argOff(2))) // xmm1 = b
			cb.emit(jitamd64.EmitUcomisdXmmXmm(nil, cmpFirst, cmpSecond))    // compare
			cb.emit([]byte{0x76, 0x04})                                      // jbe keepA (skip 4B movsd)
			cb.emit(jitamd64.EmitMovsdXmmXmm(nil, 0, 1))                     // xmm0 = b (replace)
			// keepA:
			cb.emit(jitamd64.EmitMovsdMemFromXmm(nil, 0, regRBX, dstOff))
		}
		emitBody(jit.IntrinsicMax, func() {
			// max: out=a; if b>a out=b. ucomisd xmm1,xmm0 (b vs a); b>a
			// (CF=0,ZF=0) falls through to replace; jbe (b<=a / NaN) keeps a.
			emitMinMax(1, 0)
		})
		emitBody(jit.IntrinsicMin, func() {
			// min: out=a; if b<a out=b. ucomisd xmm0,xmm1 (a vs b); a>b
			// (i.e. b<a) falls through to replace; jbe keeps a.
			emitMinMax(0, 1)
		})
	}

	// miss: patch every guard-miss branch to fall through here (the next
	// byte is the caller's deopt guard + HelperCall exit-reason).
	missPos := len(cb.bytes)
	for _, off := range missFixups {
		writeRel32(cb, off, int32(missPos)-int32(off+4))
	}
}

// emitCallInlineFastPath emits the segment-side guard + fast body for
// the PJ10 CALL EmitCallInline fast path (issue #50). Returns true if
// the fast path emit consumed the CALL; false if the caller should
// fall through to the plain exit-reason lowering.
//
// A successful guard runs the fast body: seg2seg direct dispatch when
// the IC carries a callee segment address (Spike 5), else the
// HelperExecutePlainCall exit-reason (Go-side in-frame execution,
// Spike 4). A failed guard falls through to the HelperCall
// exit-reason slow path.
//
// Guard sequence (amd64):
//
//	; ---- guard ----
//	mov rax, [rbx + A*8]           ; R(A) NaN-box
//	mov rcx, rax                   ; save for payload extract
//	shr rax, 48                    ; high 16 bits = tag
//	cmp ax, 0xFFFD                 ; TagFunction
//	jne fallthrough
//	mov rdx, payloadMask           ; 0x0000_FFFF_FFFF_FFFF
//	and rcx, rdx                   ; rcx = closure GCRef offset
//	mov rdx, [r15 + arenaBaseOff]  ; rdx = arena base
//	add rcx, rdx                   ; rcx = closure abs addr
//	mov eax, [rcx + 8]             ; low 32 bits of word1 = protoID
//	inc eax                        ; unbias against IC.CalleeProtoID+1
//	mov rdx, icSlotAddr            ; bake IC slot's abs addr
//	cmp eax, [rdx + offsetof(CalleeProtoID)]
//	jne fallthrough
//	; ---- guard passed: fast body (seg2seg / HelperExecutePlainCall) ----
//	fallthrough:
//	; caller emits HelperCall exit-reason
func emitCallInlineFastPath(cb *codeBuf, pc int32, a, b, c uint8, callSiteIdx int) bool {
	// The IC slot's Go-heap address must be stable for the mmap
	// page's lifetime. cb.proto.CallICs is allocated once by
	// TranslateProtoNative before emit and never re-slice'd, so
	// &cb.proto.CallICs[callSiteIdx] is stable. Bake it as imm64.
	if cb.proto == nil || callSiteIdx < 0 || callSiteIdx >= len(cb.proto.CallICs) {
		return false
	}
	icSlotAddr := uintptr(unsafe.Pointer(&cb.proto.CallICs[callSiteIdx]))

	// 1. mov rax, [rbx + A*8]              (7B: 48 8B 43 disp8 for A<=15, disp32 otherwise)
	cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(a)*8))
	// 2. mov rcx, rax                (3B: 48 89 C1)
	cb.emit([]byte{0x48, 0x89, 0xC1})
	// 3. shr rax, 48                       (4B: 48 C1 E8 30)
	cb.emit([]byte{0x48, 0xC1, 0xE8, 0x30})
	// 4. cmp ax, 0xFFFD                    (4B: 66 3D FD FF)
	cb.emit([]byte{0x66, 0x3D, 0xFD, 0xFF})
	// 5. jne fallthrough                   (6B: 0F 85 rel32) — patched below
	jne1Off := len(cb.bytes) + 2 // rel32 field starts 2 bytes into the 6-byte insn
	cb.emit([]byte{0x0F, 0x85, 0, 0, 0, 0})
	// 6. mov rdx, payloadMask (imm64 = 0x0000_FFFF_FFFF_FFFF)  (10B: 48 BA imm64)
	cb.emit(jitamd64.EmitMovRdxImm64(nil, 0x0000_FFFF_FFFF_FFFF))
	// 7. and rcx, rdx                (3B: 48 21 D1)
	cb.emit([]byte{0x48, 0x21, 0xD1})
	// 8. mov rdx, [r15 + arenaBaseOff]     (7B: 49 8B 97 disp32)
	{
		off := int32(jit.JITContextArenaBaseOffset)
		cb.emit([]byte{0x49, 0x8B, 0x97,
			byte(uint32(off)),
			byte(uint32(off) >> 8),
			byte(uint32(off) >> 16),
			byte(uint32(off) >> 24)})
	}
	// 9. add rcx, rdx                (3B: 48 01 D1)
	cb.emit([]byte{0x48, 0x01, 0xD1})
	// 10. mov eax, [rcx + 8]                (3B: 8B 41 08 — no REX for 32-bit load)
	cb.emit([]byte{0x8B, 0x41, 0x08})
	// 11. inc eax                           (2B: FF C0)
	cb.emit([]byte{0xFF, 0xC0})
	// 12. mov rdx, icSlotAddr (imm64)       (10B: 48 BA imm64)
	cb.emit(jitamd64.EmitMovRdxImm64(nil, uint64(icSlotAddr)))
	// 13. cmp eax, [rdx + CalleeProtoID_offset]  (3B: 3B 42 disp8)
	//     CallIC.CalleeProtoID is the first field at offset 0.
	cb.emit([]byte{0x3B, 0x42, 0x00})
	// 14. jne fallthrough                   (6B: 0F 85 rel32) — patched below
	jne2Off := len(cb.bytes) + 2
	cb.emit([]byte{0x0F, 0x85, 0, 0, 0, 0})

	// 15. Flags / arity gate (issue #50 Spike 2 phase 4a, relaxed to
	//     N-arg fixed in Spike 3):
	//     Verify the IC-recorded flags don't include any the fast body
	//     can't handle (Vararg / NeedsArg / Host / Stuck), and the
	//     callee's NumParams matches the CALL's argument count
	//     (nargs = B-1). Equal arity means enterLuaFrame's fixed-param
	//     path applies with no vararg spill / nil-fill divergence.
	//
	//     CallIC layout has ProtoID at offset 0..3, then
	//     NumParams | MaxStack | Flags | pad at offset 4..7.
	//     A single 32-bit load pulls the whole meta word.
	//
	//     mov edx, [icSlotAddr + 4]          ; rdx already holds icSlotAddr
	cb.emit([]byte{0x8B, 0x52, 0x04})
	//     test edx, 0x00870000                ; Vararg|NeedsArg|IsHost|Stuck bits
	cb.emit([]byte{0xF7, 0xC2, 0x00, 0x00, 0x87, 0x00})
	//     jne fallthrough
	jne3Off := len(cb.bytes) + 2
	cb.emit([]byte{0x0F, 0x85, 0, 0, 0, 0})
	//     cmp dl, nargs                       ; NumParams == B-1 ?
	//     (80 FA ib = cmp dl, imm8)
	nargsGuard := byte(int32(b) - 1)
	cb.emit([]byte{0x80, 0xFA, nargsGuard})
	//     jne fallthrough
	jne4Off := len(cb.bytes) + 2
	cb.emit([]byte{0x0F, 0x85, 0, 0, 0, 0})

	// --- Segment-to-segment dispatch (issue #50 Spike 5). When the IC
	// says the callee is a never-exits native segment, `call` into it
	// directly instead of the HelperExecutePlainCall round trip. rdx
	// holds icSlotAddr from the guard (step 12), but step 15 clobbered
	// it (mov edx, [rdx+4]); reload it.
	var jmpDoneOff int = -1
	if segToSegEnabled {
		// mov rdx, icSlotAddr   (48 BA imm64)
		cb.emit(jitamd64.EmitMovRdxImm64(nil, uint64(icSlotAddr)))
		// test byte [rdx+6], NeverExits(0x08)   (F6 42 06 08)
		cb.emit([]byte{0xF6, 0x42, byte(callICFlagsByteOffset), CallICFlagNeverExits})
		// jz skip_seg   (0F 84 rel32) — patched to skip_seg (fast body)
		jzNeverExitsOff := len(cb.bytes) + 2
		cb.emit([]byte{0x0F, 0x84, 0, 0, 0, 0})
		// mov rax, [rdx + CalleeSegAddrOff]   (48 8B 42 disp8=CalleeSegAddr offset)
		cb.emit([]byte{0x48, 0x8B, 0x42, byte(callICSegAddrByteOffset)})
		// test rax, rax   (48 85 C0)
		cb.emit([]byte{0x48, 0x85, 0xC0})
		// jz skip_seg   (0F 84 rel32)
		jzSegZeroOff := len(cb.bytes) + 2
		cb.emit([]byte{0x0F, 0x84, 0, 0, 0, 0})
		// cap check: mov ecx, [r15 + segCallDepthOff]   (41 8B 8F disp32)
		{
			off := int32(jit.JITContextSegCallDepthOffset)
			cb.emit([]byte{0x41, 0x8B, 0x8F,
				byte(uint32(off)), byte(uint32(off) >> 8),
				byte(uint32(off) >> 16), byte(uint32(off) >> 24)})
		}
		// cmp ecx, segToSegDepthCap   (81 F9 imm32)
		cb.emit([]byte{0x81, 0xF9,
			byte(segToSegDepthCap), byte(segToSegDepthCap >> 8),
			byte(segToSegDepthCap >> 16), byte(segToSegDepthCap >> 24)})
		// jae skip_seg   (0F 83 rel32) — depth >= cap → fall back
		jaeCapOff := len(cb.bytes) + 2
		cb.emit([]byte{0x0F, 0x83, 0, 0, 0, 0})
		// Value-stack bound guard (issue #80): the callee frame
		// (vsBase + (A+1)*8 .. + CalleeMaxStack*8) must fit inside the
		// thread's stack segment. The interpreter path grows the stack
		// in enterLuaFrame; an in-segment dispatch cannot, so without
		// this check deep native recursion silently overruns the
		// segment into neighboring arena objects. rdx holds icSlotAddr
		// (CalleeMaxStack is the byte at +5); rcx is scratch.
		// movzx ecx, byte [rdx+5]   (0F B6 4A 05)
		cb.emit([]byte{0x0F, 0xB6, 0x4A, 0x05})
		// shl rcx, 3                (48 C1 E1 03) — MaxStack slots -> bytes
		cb.emit([]byte{0x48, 0xC1, 0xE1, 0x03})
		// lea rcx, [rbx + rcx + (A+1)*8]   (48 8D 8C 0B disp32)
		{
			disp := (int32(a) + 1) * 8
			cb.emit([]byte{0x48, 0x8D, 0x8C, 0x0B,
				byte(uint32(disp)), byte(uint32(disp) >> 8),
				byte(uint32(disp) >> 16), byte(uint32(disp) >> 24)})
		}
		// cmp rcx, [r15 + valueStackEndOff]   (49 3B 8F disp32)
		{
			off := int32(jit.JITContextValueStackEndOffset)
			cb.emit([]byte{0x49, 0x3B, 0x8F,
				byte(uint32(off)), byte(uint32(off) >> 8),
				byte(uint32(off) >> 16), byte(uint32(off) >> 24)})
		}
		// ja skip_seg   (0F 87 rel32) — callee end past stack end →
		// fall back to the host path, whose enterLuaFrame grows.
		jaStackOff := len(cb.bytes) + 2
		cb.emit([]byte{0x0F, 0x87, 0, 0, 0, 0})
		// Dispatch fuel guard (fuzz crasher f2165a93dd62892d): decrement
		// jitCtx.segCallFuel; at zero skip to the host path so the step
		// budget / cancel context gets a billing point (see the
		// segCallFuel field doc — in-segment dispatch otherwise never
		// reaches st.preempt()). The host refills fuel on every Run
		// entry / dispatcher resume, so unbudgeted States never trip it.
		// sub dword [r15 + segCallFuelOff], 1   (41 83 AF disp32 01)
		{
			off := int32(jit.JITContextSegCallFuelOffset)
			cb.emit([]byte{0x41, 0x83, 0xAF,
				byte(uint32(off)), byte(uint32(off) >> 8),
				byte(uint32(off) >> 16), byte(uint32(off) >> 24), 0x01})
		}
		// jz skip_seg   (0F 84 rel32) — fuel exhausted → host path
		jzFuelOff := len(cb.bytes) + 2
		cb.emit([]byte{0x0F, 0x84, 0, 0, 0, 0})
		// inc dword [r15 + segCallDepthOff]   (41 FF 87 disp32)
		{
			off := int32(jit.JITContextSegCallDepthOffset)
			cb.emit([]byte{0x41, 0xFF, 0x87,
				byte(uint32(off)), byte(uint32(off) >> 8),
				byte(uint32(off) >> 16), byte(uint32(off) >> 24)})
		}
		// Save the caller's closure ref and set jitCtx.currentClosureRef
		// to the callee closure, so the callee's inline GETUPVAL reads
		// its own upvalues (needed for cross-closure seg2seg; a self-
		// recursive fib would see the same value either way). rbx still
		// points at the caller frame here, so R(A) is the callee closure.
		// mov rcx, [r15 + currentClosureRefOff]   (49 8B 8F disp32)
		{
			off := int32(jit.JITContextCurrentClosureRefOffset)
			cb.emit([]byte{0x49, 0x8B, 0x8F,
				byte(uint32(off)), byte(uint32(off) >> 8),
				byte(uint32(off) >> 16), byte(uint32(off) >> 24)})
		}
		// push rcx   (51) — save caller closure ref
		cb.emit([]byte{0x51})
		// mov rcx, [rbx + A*8]   (48 8B 8B disp32) — caller R(A) = callee closure
		{
			disp := int32(a) * 8
			cb.emit([]byte{0x48, 0x8B, 0x8B,
				byte(uint32(disp)), byte(uint32(disp) >> 8),
				byte(uint32(disp) >> 16), byte(uint32(disp) >> 24)})
		}
		// mov rdx, payloadMask   (48 BA imm64)
		cb.emit(jitamd64.EmitMovRdxImm64(nil, 0x0000_FFFF_FFFF_FFFF))
		// and rcx, rdx   (48 21 D1) — rcx = callee closure GCRef
		cb.emit([]byte{0x48, 0x21, 0xD1})
		// mov [r15 + currentClosureRefOff], rcx   (49 89 8F disp32)
		{
			off := int32(jit.JITContextCurrentClosureRefOffset)
			cb.emit([]byte{0x49, 0x89, 0x8F,
				byte(uint32(off)), byte(uint32(off) >> 8),
				byte(uint32(off) >> 16), byte(uint32(off) >> 24)})
		}
		// push rbx   (53) — save caller vsBase
		cb.emit([]byte{0x53})
		// lea rbx, [rbx + (A+1)*8]   (48 8D 9B disp32) — callee vsBase
		{
			disp := (int32(a) + 1) * 8
			cb.emit([]byte{0x48, 0x8D, 0x9B,
				byte(uint32(disp)), byte(uint32(disp) >> 8),
				byte(uint32(disp) >> 16), byte(uint32(disp) >> 24)})
		}
		// mov [r15 + vsBaseOff], rbx   (49 89 9F disp32) — callee prologue reloads it
		{
			off := int32(jit.JITContextValueStackBaseOffset)
			cb.emit([]byte{0x49, 0x89, 0x9F,
				byte(uint32(off)), byte(uint32(off) >> 8),
				byte(uint32(off) >> 16), byte(uint32(off) >> 24)})
		}
		// call rax   (FF D0) — rax = callee seg addr
		cb.emit([]byte{0xFF, 0xD0})
		// pop rbx   (5B) — restore caller vsBase
		cb.emit([]byte{0x5B})
		// mov [r15 + vsBaseOff], rbx   (49 89 9F disp32)
		{
			off := int32(jit.JITContextValueStackBaseOffset)
			cb.emit([]byte{0x49, 0x89, 0x9F,
				byte(uint32(off)), byte(uint32(off) >> 8),
				byte(uint32(off) >> 16), byte(uint32(off) >> 24)})
		}
		// pop rcx   (59) — restore caller closure ref
		cb.emit([]byte{0x59})
		// mov [r15 + currentClosureRefOff], rcx   (49 89 8F disp32)
		{
			off := int32(jit.JITContextCurrentClosureRefOffset)
			cb.emit([]byte{0x49, 0x89, 0x8F,
				byte(uint32(off)), byte(uint32(off) >> 8),
				byte(uint32(off) >> 16), byte(uint32(off) >> 24)})
		}
		// dec dword [r15 + segCallDepthOff]   (41 FF 8F disp32)
		{
			off := int32(jit.JITContextSegCallDepthOffset)
			cb.emit([]byte{0x41, 0xFF, 0x8F,
				byte(uint32(off)), byte(uint32(off) >> 8),
				byte(uint32(off) >> 16), byte(uint32(off) >> 24)})
		}
		// Deopt check: the callee may have deopted mid-execution (arith
		// / compare guard miss while seg2seg).
		// cmp dword [r15 + segCallDeoptOff], 0
		{
			off := int32(jit.JITContextSegCallDeoptOffset)
			cb.emit([]byte{0x41, 0x83, 0xBF,
				byte(uint32(off)), byte(uint32(off) >> 8),
				byte(uint32(off) >> 16), byte(uint32(off) >> 24), 0x00})
		}
		// je no_deopt   (0F 84 rel32) — no deopt → continue normally
		jeNoDeoptOff := len(cb.bytes) + 2
		cb.emit([]byte{0x0F, 0x84, 0, 0, 0, 0})
		// Deopt path. If still nested (segCallDepth > 0) the deopt
		// propagates: ret to our caller's fast body. If depth == 0 we
		// are the top of the chain: clear the flag and jmp skip_seg to
		// redo via the exit-reason host path.
		// cmp dword [r15 + segCallDepthOff], 0
		{
			off := int32(jit.JITContextSegCallDepthOffset)
			cb.emit([]byte{0x41, 0x83, 0xBF,
				byte(uint32(off)), byte(uint32(off) >> 8),
				byte(uint32(off) >> 16), byte(uint32(off) >> 24), 0x00})
		}
		// jne propagate   (0F 85 rel32) — depth > 0 → ret to propagate
		jnePropOff := len(cb.bytes) + 2
		cb.emit([]byte{0x0F, 0x85, 0, 0, 0, 0})
		// top-level (depth == 0): clear deopt flag.
		// mov dword [r15 + segCallDeoptOff], 0   (41 C7 87 disp32 imm32)
		{
			off := int32(jit.JITContextSegCallDeoptOffset)
			cb.emit([]byte{0x41, 0xC7, 0x87,
				byte(uint32(off)), byte(uint32(off) >> 8),
				byte(uint32(off) >> 16), byte(uint32(off) >> 24),
				0x00, 0x00, 0x00, 0x00})
		}
		// Prove-the-path: inc qword [SegToSegDeoptCountAddr] — this is the
		// top-level deopt-redo branch (issue #66 subtask 3). rax is free
		// on this branch (the no_deopt branch below uses it separately).
		// mov rax, imm64 (48 B8) ; inc qword [rax] (48 FF 00)
		cb.emit(jitamd64.EmitMovRaxImm64(nil, SegToSegDeoptCountAddr()))
		cb.emit([]byte{0x48, 0xFF, 0x00})
		// jmp skip_seg   (E9 rel32) — patched once skip_seg known
		jmpRedoOff := len(cb.bytes) + 1
		cb.emit([]byte{0xE9, 0, 0, 0, 0})
		// propagate: ret (depth > 0)
		propagatePos := len(cb.bytes)
		writeRel32(cb, jnePropOff, int32(propagatePos)-int32(jnePropOff+4))
		cb.emit([]byte{0xC3}) // ret — propagate the deopt to our caller
		// no_deopt: (patch je here)
		noDeoptPos := len(cb.bytes)
		writeRel32(cb, jeNoDeoptOff, int32(noDeoptPos)-int32(jeNoDeoptOff+4))
		// Prove-the-path: inc qword [SegToSegHitCountAddr].
		// mov rax, imm64 (48 B8) ; inc qword [rax] (48 FF 00)
		cb.emit(jitamd64.EmitMovRaxImm64(nil, SegToSegHitCountAddr()))
		cb.emit([]byte{0x48, 0xFF, 0x00})
		// Result is already at caller R(A). Jump past the exit-reason
		// blocks to the end of the CALL emit; the next op continues.
		// jmp done   (E9 rel32) — patched at end
		cb.emit([]byte{0xE9, 0, 0, 0, 0})
		jmpDoneOff = len(cb.bytes) - 4
		// skip_seg: (patch the eligibility jumps + the deopt-redo jmp
		// here — the fast body begins next).
		skipSegPos := len(cb.bytes)
		writeRel32(cb, jzNeverExitsOff, int32(skipSegPos)-int32(jzNeverExitsOff+4))
		writeRel32(cb, jzSegZeroOff, int32(skipSegPos)-int32(jzSegZeroOff+4))
		writeRel32(cb, jaeCapOff, int32(skipSegPos)-int32(jaeCapOff+4))
		writeRel32(cb, jaStackOff, int32(skipSegPos)-int32(jaStackOff+4))
		writeRel32(cb, jzFuelOff, int32(skipSegPos)-int32(jzFuelOff+4))
		writeRel32(cb, jmpRedoOff, int32(skipSegPos)-int32(jmpRedoOff+4))
	}

	// --- Fast body: guard passed. Emit exit-reason to
	// HelperExecutePlainCall (issue #50 Spike 2 step 4b).
	//
	// Spike 2 keeps frame management fully Go-side: the segment only
	// guards (R(A) tag + IC protoID + Flags/NumParams) then exits via
	// HelperExecutePlainCall. The Go helper reads R(callA) directly
	// (th.cur is still the caller frame — the segment did NOT push a
	// CI slot or bump ciDepth) and runs enterLuaFrame + executeFrom,
	// or zero-crosses to the callee's P4 code. Because the segment
	// does no ciDepth manipulation, there's no PopFrame to emit and no
	// rebalance — the helper leaves ciDepth exactly where the caller
	// expects it after the CALL.
	//
	// Spike 5 will lift the frame build + segment-to-segment dispatch
	// into the segment; for Spike 2 the deliverable is correctness +
	// a proven guard path (CallInlineFastHitCount), not yet a perf win.
	//
	// Exit to HelperExecutePlainCall (nargs = B-1, nresults = C-1).
	// Seg2seg deopt: a nested CALL that couldn't dispatch seg2seg (cold
	// IC / cap reached / non-eligible callee) while we run as a seg2seg
	// callee (segCallDepth>0) must deopt instead of exiting to the host
	// mid-segment (which the caller segment would misread). At depth 0
	// the guard is a no-op and the exit-reason runs normally.
	if segToSegEnabled {
		emitSegCallDeoptGuard(cb)
	}
	nargs := int32(b) - 1
	nresults := int32(c) - 1
	emitExitReason(cb, jit.HelperExecutePlainCall, pc, int32(a), nargs, nresults)

	// After the fast-body exit-reason RET, emit the slow-path
	// HelperCall exit-reason. Guard failures (jne1..jne4) branch here;
	// the fast body ends with a RET so control never falls through
	// into this block except via a guard-fail jne.
	slowPathPos := len(cb.bytes)
	writeRel32(cb, jne1Off, int32(slowPathPos)-int32(jne1Off+4))
	writeRel32(cb, jne2Off, int32(slowPathPos)-int32(jne2Off+4))
	writeRel32(cb, jne3Off, int32(slowPathPos)-int32(jne3Off+4))
	writeRel32(cb, jne4Off, int32(slowPathPos)-int32(jne4Off+4))
	// Math intrinsic fast path (issue #77): a math.* host closure callee
	// always fails guard 2 (its protoID field is the hostFnID, never the
	// recorded Lua protoID), so it lands here — meaning the hot Lua-callee
	// seg2seg path above never runs any intrinsic guard. Only the hostable
	// shapes are eligible (c == 2 single result; b == 2 unary / b == 3
	// max/min). On a hit the body computes inline + jmp's done; on a miss
	// (not an intrinsic slot / different callee / non-number arg) it falls
	// through to the deopt guard + HelperCall below.
	var intrinsicDoneFixups []int
	if mathIntrinsicsEnabled && c == 2 && (b == 2 || b == 3) {
		emitCallIntrinsicFastPath(cb, a, b, callSiteIdx, &intrinsicDoneFixups)
	}
	// Same seg2seg deopt guard on the guard-fail slow path: a shape
	// mismatch (protoID / arity / flags) while nested must deopt, not
	// exit-reason mid-segment.
	if segToSegEnabled {
		emitSegCallDeoptGuard(cb)
	}
	emitExitReason(cb, jit.HelperCall, pc, int32(a), int32(b), int32(c))

	// Patch the segment-to-segment `jmp done` (if emitted) and every
	// intrinsic-hit `jmp done` to land here, past both exit-reason blocks.
	// The seg2seg / intrinsic paths complete in-segment and continue to
	// the next op; both exit-reason blocks end with RET so control only
	// reaches here via one of those done jumps.
	donePos := len(cb.bytes)
	if jmpDoneOff >= 0 {
		writeRel32(cb, jmpDoneOff, int32(donePos)-int32(jmpDoneOff+4))
	}
	for _, off := range intrinsicDoneFixups {
		writeRel32(cb, off, int32(donePos)-int32(off+4))
	}
	return true
}

// emitSELF emits SELF A B C via the HelperSelf exit-reason: the
// dispatcher runs host.Self (R(A+1)=R(B) receiver + R(A)=R(B)[RK(C)]
// method lookup through the IC / __index chain; can raise).
func emitSELF(cb *codeBuf, pc int32, a, b uint8, c int) {
	emitExitReason(cb, jit.HelperSelf, pc, int32(a), int32(b), int32(c))
}

// emitTAILCALL emits the TAILCALL A B C exit-reason (issue #52). Like
// HelperReturn it ALWAYS terminates the segment run: the dispatcher
// loop in Run handles HelperTailCall directly (before dispatchHelper),
// calling host.TailCall and branching on its tri-state return —
// 0 = Lua tail call completed (the caller frame was replaced and driven
// to completion; Run returns 0 WITHOUT DoReturn), 1 = err, 2 = host
// tail call (results are in R(A..); Run decodes the trailing dead
// RETURN luac always emits at pc+1 and finishes via host.DoReturn,
// mirroring the interpreter falling through to that RETURN). No arm
// reenters the segment, so the resume fixup is dropped like
// HelperReturn's.
//
// No seg2seg deopt guard: TAILCALL is not in seg2segOpsEligible, so a
// Proto containing it never runs as an in-segment callee (same as
// SELF / CONCAT / SETLIST).
func emitTAILCALL(cb *codeBuf, pc int32, a, b, c uint8) {
	emitExitReason(cb, jit.HelperTailCall, pc, int32(a), int32(b), int32(c))
	cb.pendingResumeOffFixups = cb.pendingResumeOffFixups[:0]
}

// emitTFORLOOP emits the TFORLOOP A C exit-reason + packed-result branch
// (issue #52; mirror of emitCompareExitTail's protocol). The dispatcher
// calls host.TForLoop (iterator invocation + R(A+3..A+2+C) writes +
// control-var update) and stores the continue verdict into exitArg0
// (1 = continue, 0 = exit); the resume block reads it back and branches
// to succBack (the back-edge JMP) or succOut — the branch must happen
// in-segment, only the segment knows the successor offsets.
func emitTFORLOOP(cb *codeBuf, pc int32, a, c uint8, succBack, succOut int) {
	emitExitReason(cb, jit.HelperTForLoop, pc, int32(a), 0, int32(c))
	// Bind the resume entry right here (mirror of emitCompareExitTail).
	emitResumePreludeIfPending(cb)
	// mov rax, [r15 + exitArg0Off]  (7B: 49 8B 87 disp32)
	{
		off := int32(jit.JITContextExitArg0Offset)
		cb.emit([]byte{0x49, 0x8B, 0x87,
			byte(uint32(off)),
			byte(uint32(off) >> 8),
			byte(uint32(off) >> 16),
			byte(uint32(off) >> 24)})
	}
	// test rax, rax; jnz succBack (continue); jmp succOut (exit)
	cb.emit([]byte{0x48, 0x85, 0xC0})       // test rax, rax
	cb.emit([]byte{0x0F, 0x85, 0, 0, 0, 0}) // jnz succBack
	cb.addFixup(cb.pos()-4, cb.pos(), succBack)
	patchOff := cb.pos() + 1
	cb.emit([]byte{0xE9, 0, 0, 0, 0}) // jmp succOut
	cb.addFixup(patchOff, cb.pos(), succOut)
}

// emitCLOSURE emits the CLOSURE A Bx exit-reason (issue #52). The
// dispatcher calls host.Closure, which builds the closure into R(A) and
// consumes the pseudo-instructions (MOVE/GETUPVAL per upvalue) that
// follow CLOSURE in the bytecode — the translator's walks skip those
// words (see realPCs in emitBB), so the segment's resume entry is the
// op after the pseudos. Bx exceeds the 9-bit b/c payload slots; pack it
// like GETGLOBAL's 18-bit split (b = low 9, c = high 9).
func emitCLOSURE(cb *codeBuf, pc int32, a uint8, bx int) {
	emitExitReason(cb, jit.HelperClosure, pc, int32(a),
		int32(bx&0x1FF), int32((bx>>9)&0x1FF))
}

// emitCLOSE emits the CLOSE A exit-reason (issue #52): the dispatcher
// calls host.Close (closes all open upvalues >= R(A); never raises).
func emitCLOSE(cb *codeBuf, pc int32, a uint8) {
	emitExitReason(cb, jit.HelperClose, pc, int32(a), 0, 0)
}
