//go:build wangshu_p4 && amd64

// emit_ops_amd64.go - shim-based emit for the remaining PJ10 opcodes.
// All ops go through their P4HostState shim (slow path only). Fast
// paths (inline SSE for arithmetic, IC-inline for table ops) are a
// follow-up optimization; this file establishes correctness first.
package peroptranslator

import (
	"unsafe"

	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// Shim addr helpers moved to shims.go (arch-neutral).

// ---------------------------------------------------------------------
// Arithmetic ops (PJ10b): all go through host.Arith slow path
// ---------------------------------------------------------------------

// emitARITH emits R(A) := RK(B) <op> RK(C) via shimArith. The op field
// is passed as int32(bytecode.OpCode).
//
// After the shim call, if RAX != 0 the caller should emit
// emitStatusCheckAndBubble to propagate the error. Callers embedded
// inside a larger emit sequence typically do that at end-of-op.
func emitARITH(cb *codeBuf, op bytecode.OpCode, pc int32, a uint8, b, c int) {
	// Fast path: inline SSE for ADD/SUB/MUL/DIV, guarded by IsNumber on
	// each reg operand. If a reg holds nil / a GC ref, jump to the
	// shim block below so host.Arith can produce the correct __add /
	// coercion / raise semantics. Fuzz seed 4df9d8c82ce0d9f7 caught the
	// unguarded variant (P4 silently produced NaN while P1 raised
	// `attempt to perform arithmetic on nil`).
	if op == bytecode.ADD || op == bytecode.SUB || op == bytecode.MUL || op == bytecode.DIV {
		if emitInlineArithWithShimFallback(cb, op, pc, a, b, c) {
			return
		}
	}
	addr := shimArithAddr()
	// shimArith(ctx, base, pc, op, b, c, a)
	emitCallShim(cb, addr, []int32{0, pc, int32(op), int32(b), int32(c), int32(a)})
	emitStatusCheckAndBubble(cb)
}

// emitInlineArithWithShimFallback emits the guarded inline SSE fast
// path plus an inline shim fallback for miss cases. Returns true when
// it consumed the emit (inline succeeded); false when the shape can't
// be inlined (e.g. K operand is non-numeric) and the caller should
// fall through to the plain shim call.
//
// Layout:
//
//	[guard-B]        ; only if b is reg
//	  mov rax, [rbx+B*8]; mov rcx, qNanBoxBase; cmp rax, rcx
//	  jae shim         ; not a number → fallback
//	[guard-C]        ; only if c is reg (same shape)
//	[inline SSE]
//	  movsd xmm0, B; movsd xmm1, C; arith xmm0, xmm1
//	  movsd [rbx+A*8], xmm0
//	  jmp done
//	shim:
//	  emitCallShim(shimArith); emitStatusCheckAndBubble
//	done:
//
// Reg operands go through the guard; K operands don't need one because
// AnalyzeNative already rejected non-numeric K constants for arith ops.
func emitInlineArithWithShimFallback(cb *codeBuf, op bytecode.OpCode, pc int32, a uint8, b, c int) bool {
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
	// forward to the shim block; we record patch offsets and resolve
	// them once the shim block's byte offset is known.
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
	cb.emit(jitamd64.EmitMovsdMemFromXmm(nil, 0, regRBX, int32(a)*8))
	// jmp done (5-byte E9 rel32); rel32 patched once done offset known.
	cb.emit([]byte{0xE9, 0, 0, 0, 0})
	fastPathJmpOff := int(cb.pos()) - 4

	// Shim block starts here — patch guard fixups.
	shimOff := int(cb.pos())
	for _, po := range guardFixups {
		rel := int32(shimOff) - int32(po+4)
		writeRel32(cb, po, rel)
	}
	emitCallShim(cb, shimArithAddr(), []int32{0, pc, int32(op), int32(b), int32(c), int32(a)})
	emitStatusCheckAndBubble(cb)

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

// emitUNM emits R(A) := -R(B) via shimUnm.
func emitUNM(cb *codeBuf, pc int32, a, b uint8) {
	emitCallShim(cb, shimUnmAddr(), []int32{0, pc, int32(b), int32(a)})
	emitStatusCheckAndBubble(cb)
}

// emitLEN emits R(A) := #R(B) via shimLen.
func emitLEN(cb *codeBuf, pc int32, a, b uint8) {
	emitCallShim(cb, shimLenAddr(), []int32{0, pc, int32(b), int32(a)})
	emitStatusCheckAndBubble(cb)
}

// emitCONCAT emits R(A) := R(B) .. .. R(C) via shimConcat. B/C here
// are register range endpoints (0-MaxStack, always in uint8 range).
func emitCONCAT(cb *codeBuf, pc int32, a, b, c uint8) {
	emitCallShim(cb, shimConcatAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
	emitStatusCheckAndBubble(cb)
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
// The shim returns packed status: bit0 = result (0/1), bit1 = error.
//
// Emit sequence:
//
//	<call shim(base, pc, op, b, c)>
//	<if rax bit1 set: ret 1>       ; error bubble
//	test rax, 1
//	<if result-bit == A: jmp succExec else jmp succSkip>
//
// Since this needs branch to specific BB targets, the emit function
// takes those as parameters and records fixups.
func emitCompare(cb *codeBuf, op bytecode.OpCode, pc int32, a uint8, b, c int, succExec, succSkip int) {
	// Fast path: inline numeric compare via UCOMISd. Supports LT/LE
	// with reg-reg, reg-K, K-reg, K-K where the K operand is numeric.
	// EQ inline uses raw 64-bit bit comparison (Lua rawequal for
	// primitives; skips __eq metamethod, safe for numeric-only Protos).
	if op == bytecode.LT || op == bytecode.LE {
		if inlineNumericCompare(cb, op, a, b, c, succExec, succSkip) {
			return
		}
	}
	if op == bytecode.EQ {
		if inlineRawEq(cb, a, b, c, succExec, succSkip) {
			return
		}
	}
	// shim: for EQ use shimEq, for LT/LE use shimCompare(op, b, c)
	if op == bytecode.EQ {
		emitCallShim(cb, shimEqAddr(), []int32{0, pc, int32(b), int32(c)})
	} else {
		emitCallShim(cb, shimCompareAddr(), []int32{0, pc, int32(op), int32(b), int32(c)})
	}
	// After shim: RAX holds packed (bit0=result, bit1=err).
	// test rax, 2  ; check error bit
	cb.emit([]byte{0x48, 0xA9, 0x02, 0x00, 0x00, 0x00}) // test rax, 2
	// jz +5 (skip the ret block)
	cb.emit([]byte{0x74, 0x06})
	// mov rax, 1; ret
	cb.emit([]byte{0xB8, 0x01, 0x00, 0x00, 0x00, 0xC3}) // mov eax,1; ret
	// Now compare RAX bit0 vs A:
	// and rax, 1
	cb.emit([]byte{0x48, 0x83, 0xE0, 0x01})
	// cmp rax, A
	cb.emit([]byte{0x48, 0x83, 0xF8, byte(a)})
	// je -> succExec, else -> succSkip
	// Encode: je rel32 (0F 84 rel32, 6 bytes) with fixup to succExec.
	{
		cb.emit([]byte{0x0F, 0x84, 0x00, 0x00, 0x00, 0x00})
		patchOff := cb.pos() - 4
		cb.addFixup(patchOff, cb.pos(), succExec)
	}
	// Unconditional jmp -> succSkip
	{
		patchOff := cb.pos() + 1
		cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00})
		cb.addFixup(patchOff, cb.pos(), succSkip)
	}
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
func inlineNumericCompare(cb *codeBuf, op bytecode.OpCode, a uint8, b, c int, succExec, succSkip int) bool {
	if op != bytecode.LT && op != bytecode.LE {
		return false
	}
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
	return true
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

// emitTFORLOOP emits generic-for iteration via shimTForLoop. The shim
// returns int64: -2 = exit, -1 = error, >= 0 = continue.
//
// Semantics after the shim:
//
//	if ret == -1: bubble error
//	if ret == -2: pc++ (fall out, succOut)
//	else: fall through to succBack (loop body)
//
// Emit sequence:
//
//	<call shimTForLoop(base, pc, a, c)>
//	cmp rax, -1
//	je error_bubble
//	cmp rax, -2
//	je succOut
//	jmp succBack
//
// error_bubble:
//
//	mov rax, 1
//	ret
func emitTFORLOOP(cb *codeBuf, pc int32, a, c uint8, succBack, succOut int) {
	emitCallShim(cb, shimTForLoopAddr(), []int32{0, pc, int32(a), int32(c)})
	// cmp rax, -1
	cb.emit([]byte{0x48, 0x83, 0xF8, 0xFF})
	// jne skipErr; the skip target is right after the `mov eax, 1; ret`
	// error-return sequence below. `mov eax, imm32` (0xB8 + 4-byte imm)
	// is 5 bytes, `ret` (0xC3) is 1 byte -> 6 bytes to skip.
	cb.emit([]byte{0x75, 0x06})
	// mov eax, 1; ret
	cb.emit([]byte{0xB8, 0x01, 0x00, 0x00, 0x00, 0xC3})
	// cmp rax, -2
	cb.emit([]byte{0x48, 0x83, 0xF8, 0xFE})
	// je -> succOut (rel32)
	{
		cb.emit([]byte{0x0F, 0x84, 0x00, 0x00, 0x00, 0x00})
		patchOff := cb.pos() - 4
		cb.addFixup(patchOff, cb.pos(), succOut)
	}
	// jmp -> succBack (rel32)
	{
		patchOff := cb.pos() + 1
		cb.emit([]byte{0xE9, 0x00, 0x00, 0x00, 0x00})
		cb.addFixup(patchOff, cb.pos(), succBack)
	}
}

// ---------------------------------------------------------------------
// Table + call ops (PJ10d)
// ---------------------------------------------------------------------

func emitGETTABLE(cb *codeBuf, pc int32, a, b uint8, c int) {
	emitCallShim(cb, shimGetTableAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
	emitStatusCheckAndBubble(cb)
}

func emitSETTABLE(cb *codeBuf, pc int32, a uint8, b, c int) {
	emitCallShim(cb, shimSetTableAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
	emitStatusCheckAndBubble(cb)
}

func emitGETGLOBAL(cb *codeBuf, pc int32, a uint8, bx uint16) {
	emitCallShim(cb, shimGetGlobalAddr(), []int32{0, pc, int32(a), int32(bx)})
	emitStatusCheckAndBubble(cb)
}

func emitSETGLOBAL(cb *codeBuf, pc int32, a uint8, bx uint16) {
	emitCallShim(cb, shimSetGlobalAddr(), []int32{0, pc, int32(a), int32(bx)})
	emitStatusCheckAndBubble(cb)
}

func emitNEWTABLE(cb *codeBuf, pc int32, a, b, c uint8) {
	emitCallShim(cb, shimNewTableAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
	// NewTable never raises in practice
}

func emitSETLIST(cb *codeBuf, pc int32, a, b, c uint8) {
	emitCallShim(cb, shimSetListAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
	emitStatusCheckAndBubble(cb)
}

func emitCALL(cb *codeBuf, pc int32, a, b, c uint8) {
	emitCallShim(cb, shimCallAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
	emitStatusCheckAndBubble(cb)
}

func emitTAILCALL(cb *codeBuf, pc int32, a, b, c uint8) {
	emitCallShim(cb, shimTailCallAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
	// TailCall has tri-state return; we treat non-zero as bubble.
	emitStatusCheckAndBubble(cb)
	// The RETURN that follows TAILCALL in Lua bytecode is handled by
	// the CFG (or by tail-call collapsing in the emit path).
}

func emitSELF(cb *codeBuf, pc int32, a, b uint8, c int) {
	emitCallShim(cb, shimSelfAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
	emitStatusCheckAndBubble(cb)
}

func emitCLOSURE(cb *codeBuf, pc int32, a uint8, bx uint16) {
	emitCallShim(cb, shimClosureAddr(), []int32{0, pc, int32(a), int32(bx)})
	emitStatusCheckAndBubble(cb)
}

func emitCLOSE(cb *codeBuf, pc int32, a uint8) {
	emitCallShim(cb, shimCloseAddr(), []int32{0, pc, int32(a)})
	emitStatusCheckAndBubble(cb)
}
