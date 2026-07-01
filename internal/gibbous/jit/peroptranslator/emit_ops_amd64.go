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

// Shim addr helpers -- one per shim, using the direct-unsafe pattern
// (see emit_shim_amd64.go rationale).

func shimArithAddr() uint64 {
	f := shimArith
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimUnmAddr() uint64 {
	f := shimUnm
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimLenAddr() uint64 {
	f := shimLen
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimConcatAddr() uint64 {
	f := shimConcat
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimEqAddr() uint64 {
	f := shimEq
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimCompareAddr() uint64 {
	f := shimCompare
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimGetTableAddr() uint64 {
	f := shimGetTable
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimSetTableAddr() uint64 {
	f := shimSetTable
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimGetGlobalAddr() uint64 {
	f := shimGetGlobal
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimSetGlobalAddr() uint64 {
	f := shimSetGlobal
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimNewTableAddr() uint64 {
	f := shimNewTable
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimSetListAddr() uint64 {
	f := shimSetList
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimForPrepAddr() uint64 {
	f := shimForPrep
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimCallAddr() uint64 {
	f := shimCall
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimTailCallAddr() uint64 {
	f := shimTailCall
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimSelfAddr() uint64 {
	f := shimSelf
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimClosureAddr() uint64 {
	f := shimClosure
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimCloseAddr() uint64 {
	f := shimClose
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

func shimTForLoopAddr() uint64 {
	f := shimTForLoop
	p := *(*unsafe.Pointer)(unsafe.Pointer(&f))
	return uint64(*(*uintptr)(p))
}

// ---------------------------------------------------------------------
// Arithmetic ops (PJ10b): all go through host.Arith slow path
// ---------------------------------------------------------------------

// emitARITH emits R(A) := RK(B) <op> RK(C) via shimArith. The op field
// is passed as int32(bytecode.OpCode).
//
// After the shim call, if RAX != 0 the caller should emit
// emitStatusCheckAndBubble to propagate the error. Callers embedded
// inside a larger emit sequence typically do that at end-of-op.
func emitARITH(cb *codeBuf, op bytecode.OpCode, pc int32, a, b, c uint8) {
	addr := shimArithAddr()
	// shimArith(ctx, base, pc, op, b, c, a)
	emitCallShim(cb, addr, []int32{0, pc, int32(op), int32(b), int32(c), int32(a)})
	emitStatusCheckAndBubble(cb)
}

// emitADD/SUB/MUL/DIV/MOD/POW are thin wrappers on emitARITH with a
// baked op code.
func emitADD(cb *codeBuf, pc int32, a, b, c uint8) {
	emitARITH(cb, bytecode.ADD, pc, a, b, c)
}
func emitSUB(cb *codeBuf, pc int32, a, b, c uint8) {
	emitARITH(cb, bytecode.SUB, pc, a, b, c)
}
func emitMUL(cb *codeBuf, pc int32, a, b, c uint8) {
	emitARITH(cb, bytecode.MUL, pc, a, b, c)
}
func emitDIV(cb *codeBuf, pc int32, a, b, c uint8) {
	emitARITH(cb, bytecode.DIV, pc, a, b, c)
}
func emitMOD(cb *codeBuf, pc int32, a, b, c uint8) {
	emitARITH(cb, bytecode.MOD, pc, a, b, c)
}
func emitPOW(cb *codeBuf, pc int32, a, b, c uint8) {
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

// emitCONCAT emits R(A) := R(B) .. .. R(C) via shimConcat.
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
func emitCompare(cb *codeBuf, op bytecode.OpCode, pc int32, a, b, c uint8, succExec, succSkip int) {
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

// emitEQ / LT / LE are thin wrappers.
func emitEQ(cb *codeBuf, pc int32, a, b, c uint8, succExec, succSkip int) {
	emitCompare(cb, bytecode.EQ, pc, a, b, c, succExec, succSkip)
}
func emitLT(cb *codeBuf, pc int32, a, b, c uint8, succExec, succSkip int) {
	emitCompare(cb, bytecode.LT, pc, a, b, c, succExec, succSkip)
}
func emitLE(cb *codeBuf, pc int32, a, b, c uint8, succExec, succSkip int) {
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
	// je notTruthy: forward jmp to `notTruthy` label. We use rel8 for
	// space efficiency but hard to know distance yet. Use rel32 via jne+jmp
	// pattern is simpler:
	//   je toNotTruthy (rel8 forward, jz over the "check false" block +
	//     the "onTruthy" arm)
	// Use rel32 for safety:
	// je +30 (skip false-check and truthy-jmp, land on notTruthy label)
	// We'll patch later. Simpler: use fixups within codeBuf's BB labels.
	// For inline mini-flow within one emit, use a small internal label:
	//
	// The instructions after this:
	//   [17 bytes] mov rcx, falseBits
	//   [3 bytes]  cmp rax, rcx
	//   [2 bytes]  je notTruthyRel8
	//   [either succExec branch or succSkip branch] -- rel32 6-byte jmp
	// notTruthyRel8:
	//   [either succSkip branch or succExec branch] -- rel32 6-byte jmp
	//
	// So the je-after-nil-cmp jumps rel8 to notTruthyRel8 which is
	// 17+3+2+6 = 28 bytes forward.
	//
	// Use rel8 jz:
	cb.emit([]byte{0x74, 0x1C}) // jz +28 (to notTruthyRel8)
	// mov rcx, falseBits (0xFFF9_0000_0000_0000)
	falseBits := uint64(0xFFF9000000000000)
	cb.emit([]byte{0x48, 0xB9})
	for i := 0; i < 8; i++ {
		cb.emit([]byte{byte(falseBits >> (8 * i))})
	}
	// cmp rax, rcx
	cb.emit([]byte{0x48, 0x39, 0xC8})
	// jz +6 (to notTruthyRel8)
	cb.emit([]byte{0x74, 0x06})
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
	// notTruthyRel8: (label position)
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

// emitTESTSET: if Truthy(R(B)) == C then R(A) := R(B) else pc++.
// Similar inline logic to emitTEST but with an R(B) source and A dest.
// TODO: inline emit; for now defer via panic to catch un-covered paths.
func emitTESTSET(cb *codeBuf, a, b, c uint8, succExec, succSkip int) {
	_, _, _, _, _ = a, b, c, succExec, succSkip
	panic("emitTESTSET: not yet implemented")
}

// ---------------------------------------------------------------------
// Loop ops (PJ10c)
// ---------------------------------------------------------------------

// emitFORPREP: preps a numeric for loop via shimForPrep, then JMPs to
// the FORLOOP block. The JMP target BB is passed in.
func emitFORPREP(cb *codeBuf, pc int32, a uint8, targetBB int) {
	emitCallShim(cb, shimForPrepAddr(), []int32{0, pc, int32(a)})
	emitStatusCheckAndBubble(cb)
	emitJMP(cb, targetBB)
}

// emitFORLOOP: shim call would need to advance and check. Not in the
// P4HostState interface directly (no ForLoop method separate). For now,
// emit an inline loop step + call test, matching the interpreter's
// FORLOOP semantics. Deferred: use host.Arith or host.ForPrep pattern.
//
// TODO: implement using SSE add/compare on R(A), R(A+1), R(A+2) slots.
func emitFORLOOP(cb *codeBuf, pc int32, a uint8, succBack, succOut int) {
	_, _, _, _ = pc, a, succBack, succOut
	panic("emitFORLOOP: not yet implemented (needs inline SSE step)")
}

// emitTFORLOOP: generic for iteration, via shimTForLoop. The shim
// returns int64: -2 = exit, -1 = error, >= 0 = continue.
// TODO: implement with proper int64 handling and BB branch.
func emitTFORLOOP(cb *codeBuf, pc int32, a, c uint8, succBack, succOut int) {
	_, _, _, _, _ = pc, a, c, succBack, succOut
	panic("emitTFORLOOP: not yet implemented (needs int64 shim return handling)")
}

// ---------------------------------------------------------------------
// Table + call ops (PJ10d)
// ---------------------------------------------------------------------

func emitGETTABLE(cb *codeBuf, pc int32, a, b, c uint8) {
	emitCallShim(cb, shimGetTableAddr(), []int32{0, pc, int32(a), int32(b), int32(c)})
	emitStatusCheckAndBubble(cb)
}

func emitSETTABLE(cb *codeBuf, pc int32, a, b, c uint8) {
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

func emitSELF(cb *codeBuf, pc int32, a, b, c uint8) {
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
