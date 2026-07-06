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
	// and reenters at the next op's resume prelude.
	slowOff := int(cb.pos())
	for _, po := range guardFixups {
		rel := int32(slowOff) - int32(po+4)
		writeRel32(cb, po, rel)
	}
	emitExitReason(cb, jit.HelperArithSlow, pc, int32(a), int32(b), int32(c))

	// Done block starts here — patch fastPathJmpOff.
	doneOff := int(cb.pos())
	rel := int32(doneOff) - int32(fastPathJmpOff+4)
	writeRel32(cb, fastPathJmpOff, rel)
	return true
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
// Semantics: cond = (RK(B) == RK(C)); if cond != A then pc++ (succSkip);
// else fall through to JMP (succExec).
//
// Sequence:
//
//	mov rax, {B}      ; load 64 bits of RK(B) into RAX
//	<cmp rax, {C}>    ; compare with RK(C)
//	<je | jne> succExec  ; based on A
//	jmp succSkip
//
// For K operands the 64-bit imm is baked; for reg it's from [rbx+X*8].
// Returns false if any K operand is out of range (caller falls back).
func inlineRawEq(cb *codeBuf, a uint8, b, c int, succExec, succSkip int) bool {
	// Load RK(B) into RAX.
	if b < 256 {
		// mov rax, [rbx + B*8]
		cb.emit(jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(b)*8))
	} else {
		kidx := b - 256
		if cb.proto == nil || kidx < 0 || kidx >= len(cb.proto.Consts) {
			return false
		}
		cb.emit(jitamd64.EmitMovRaxImm64(nil, cb.proto.Consts[kidx]))
	}
	// Compare RAX with RK(C).
	if c < 256 {
		// cmp rax, [rbx + C*8]  (48 3B 83 disp32; 7 bytes)
		disp := int32(c) * 8
		cb.emit([]byte{0x48, 0x3B, 0x83,
			byte(disp), byte(disp >> 8), byte(disp >> 16), byte(disp >> 24)})
	} else {
		kidx := c - 256
		if cb.proto == nil || kidx < 0 || kidx >= len(cb.proto.Consts) {
			return false
		}
		// mov rcx, imm64
		cb.emit(jitamd64.EmitMovRcxImm64(nil, cb.proto.Consts[kidx]))
		// cmp rax, rcx
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
	patchOff := cb.pos() - 4
	cb.addFixup(patchOff, cb.pos(), succExec)
	// jmp rel32 -> succSkip
	patchOff2 := cb.pos() + 1
	cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00})
	cb.addFixup(patchOff2, cb.pos(), succSkip)
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
// Semantics (per Lua 5.1): R(A) -= R(A+2); jmp to FORLOOP. Three slots
// (init, limit, step) are pre-coerced to number before entering the
// loop; if any is non-numeric, coercion / error is needed.
//
// Fast path: assume all three slots are numbers (the same profile-hot
// assumption we make for inline arith and compare). Emit:
//
//	movsd xmm0, [rbx+A*8]      ; R(A)
//	movsd xmm1, [rbx+(A+2)*8]  ; R(A+2)
//	subsd xmm0, xmm1
//	movsd [rbx+A*8], xmm0
//	jmp   succBB               ; FORLOOP block
//
// This dodges the shim-from-mmap crash and keeps FORPREP + FORLOOP body
// entirely inline — enabling heavy_arith and heavy_recursion kernels to
// go native.
func emitFORPREP(cb *codeBuf, pc int32, a uint8, targetBB int) {
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
//	jmp succBack
//
// condFalse:
//
//	jmp succOut
//
// Total ~85 bytes. Non-trivial but avoids a shim call for the hot
// loop body.
func emitFORLOOP(cb *codeBuf, a uint8, succBack, succOut int) {
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
	//     jmp succBack (rel32, 5 bytes)
	//   = 14 bytes (with disp32 movsd)
	//
	// condFalse block:
	//     jmp succOut (rel32, 5 bytes)
	//   = 5 bytes
	//
	// Layout:
	//   [00..]  step<=0 (8 bytes)
	//   [08..]  stepPositive (8 bytes)
	//   [16..]  condTrue (14 bytes)
	//   [30..]  condFalse (5 bytes)
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
	// jmp rel32 -> succBack
	{
		patchOff := cb.pos() + 1
		cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00})
		cb.addFixup(patchOff, cb.pos(), succBack)
	}

	// condFalse block:
	// jmp rel32 -> succOut
	{
		patchOff := cb.pos() + 1
		cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00})
		cb.addFixup(patchOff, cb.pos(), succOut)
	}
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
	// next op's entry.
	shimOff := int(cb.pos())
	for _, po := range guardFixups {
		rel := int32(shimOff) - int32(po+4)
		writeRel32(cb, po, rel)
	}
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
	emitExitReason(cb, jit.HelperSetTable, pc, int32(a), int32(b), int32(c))
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

func emitNEWTABLE(cb *codeBuf, pc int32, a, b, c uint8) {
	emitExitReason(cb, jit.HelperNewTable, pc, int32(a), int32(b), int32(c))
}

func emitSETLIST(cb *codeBuf, pc int32, a, b, c uint8) {
	emitExitReason(cb, jit.HelperSetList, pc, int32(a), int32(b), int32(c))
}

func emitCALL(cb *codeBuf, pc int32, a, b, c uint8) {
	// Issue #50 Spike 2 EmitCallInline fast path — only fires when:
	//   - callInlineEnabled = true (arch flag, currently off during
	//     the segment-guard incubation),
	//   - the CALL site has an IC slot in codeBufProto.CallSitePCs,
	//   - CALL shape is guardable: B != 0 (fixed nargs) and C != 0
	//     (fixed nresults; multret rejected — segment can't sync top
	//     mid-call).
	// The fast emit itself lands in a follow-up commit; this branch
	// is a placeholder for the shape gate so callInlineEnabled can
	// be flipped in one motion once the emit body is written.
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

// findCallSiteIndex returns the CallIC index for a given pc, or -1 if
// the pc has no corresponding CallIC slot (e.g. CFG changed between
// translate-time and emit-time, or the pc slice is nil).
func findCallSiteIndex(callSitePCs []int32, pc int32) int {
	for i, sitePC := range callSitePCs {
		if sitePC == pc {
			return i
		}
	}
	return -1
}

// emitCallInlineFastPath emits the segment-side guard for the PJ10
// CALL EmitCallInline fast path (issue #50 Spike 2). Returns true if
// the fast path emit consumed the CALL; false if the caller should
// fall through to the plain exit-reason lowering.
//
// **Current status**: guard-only. This step lands the R(A) tag +
// protoID guards; a successful guard falls through to the exit-reason
// HelperCall (the historical slow path). Once the in-segment frame
// build lands in Step 4, the fall-through target becomes the fast
// HelperExecutePlainCall body instead.
//
// Emit sequence (amd64, guard-only phase):
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
//	; ---- guard passed (Spike 2 phase 2 will emit frame build here) ----
//	fallthrough:
//	; caller emits HelperCall exit-reason
//
// In this step, guard-passed and guard-failed both fall through to the
// same exit-reason. Returning false here signals emitCALL to emit the
// existing HelperCall lowering after us — which is exactly what we
// want for the guard-only phase.
func emitCallInlineFastPath(cb *codeBuf, pc int32, a, b, c uint8, callSiteIdx int) bool {
	_ = pc
	_ = b
	_ = c
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
	emitExitReason(cb, jit.HelperCall, pc, int32(a), int32(b), int32(c))
	return true
}

// emitSELF emits SELF A B C via the HelperSelf exit-reason: the
// dispatcher runs host.Self (R(A+1)=R(B) receiver + R(A)=R(B)[RK(C)]
// method lookup through the IC / __index chain; can raise).
func emitSELF(cb *codeBuf, pc int32, a, b uint8, c int) {
	emitExitReason(cb, jit.HelperSelf, pc, int32(a), int32(b), int32(c))
}

// TAILCALL / CLOSURE / CLOSE / TFORLOOP have no native emit: they are
// excluded from opSupported (AnalyzeNative rejects any Proto containing
// them) and their semantics don't fit the current exit-reason
// dispatcher — TAILCALL's tri-state return (0 = frame already replaced,
// skip DoReturn) needs a terminate-without-DoReturn channel like
// HelperReturn, and TFORLOOP's resume must branch on the iterator
// result inside the segment. The emit loop errors out on them
// defensively (issue #45 removed their legacy shim-call emitters —
// shim calls from mmap crash the Go stack unwinder under nested +
// concurrent load, see issue #38).
