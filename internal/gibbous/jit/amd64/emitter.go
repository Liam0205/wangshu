//go:build wangshu_p4 && amd64

// emitter.go —— P4 amd64 backend straight-line template emitter (PJ1 scope).
//
// Follows docs/design/p4-method-jit/06-backends.md §2.4 emitter trait interface
// + §3.7 straight-line family (MOVE/LOADK/LOADBOOL/LOADNIL, opcodes 0-3)
// + §3.4 control-flow family JMP (opcode 22)
// + §3.5 call family RETURN (opcode 30).
//
// **PJ1 minimal form** (following spike DECISION.md "limits of the minimal form"
// + the emitter interface unlocked once the spike gate went green): this emitter
// introduces no jitContext, does not switch SP, and manages no stack of its own;
// straight-line templates take the shape of a minimal "mov rax, imm; ret"-style
// sequence (same as spike S1). This lets the smallest genuinely working subset of
// PJ1 land — a single-BB straight-line Proto of "LOADK bakes imm, RETURN exits"
// executed through the mmap segment, returning RAX to the trampoline → callJIT
// receives the value.
//
// **Within PJ1 scope supported = LOADK + RETURN** (single-BB straight line, no
// jump) — this is the first intersection of the spike gate's four tiers and the
// "minimal executable P4 form". MOVE / LOADBOOL / LOADNIL / JMP are extended
// incrementally starting from PJ2 (they involve multiple registers or forward
// fixups, outside the PJ1 minimal form).
//
// The full Emitter trait + per-opcode emit functions (following 06 §3.x families)
// are filled in incrementally across PJ2-PJ7 — this file first builds the minimal
// skeleton so that PJ1's "end-to-end mmap segment round-trip works" holds.
package amd64

import "encoding/binary"

// EmitMovRaxImm64 emits the "mov rax, imm64" 9-byte sequence (REX.W + B8+rd).
//
// amd64 encoding: 48 b8 ii ii ii ii ii ii ii ii (10 bytes)
//   - 0x48: REX prefix, W=1 means 64-bit operand
//   - 0xb8: B8+rd opcode, rd=0 (RAX)
//   - imm64: little-endian 8-byte immediate
//
// Used by the LOADK straight-line fast path — bakes in the NaN-box u64 of
// Proto.Constants[Bx]. PJ1 does not implement constant-pool-to-NaN-box
// conversion (that belongs to the value-representation module); this emitter
// interface only exposes the "write u64 imm" primitive, and the caller decides
// how to use it.
func EmitMovRaxImm64(buf []byte, imm uint64) []byte {
	buf = append(buf, 0x48, 0xb8) // REX.W mov rax, imm64
	var imm8 [8]byte
	binary.LittleEndian.PutUint64(imm8[:], imm)
	buf = append(buf, imm8[:]...)
	return buf
}

// EmitRet emits the "ret" single-byte sequence (0xc3).
//
// Used at the tail of the RETURN template — returns the current RAX value to the
// trampoline. Under the PJ1 minimal form, RAX = the most recent imm baked in by
// the template (the constant after LOADK), which the trampoline receives via
// callJIT.
func EmitRet(buf []byte) []byte {
	return append(buf, 0xc3)
}

// EncodedMovRaxImm64Len is the encoded byte length of "mov rax, imm64" (constant, fixed 10).
const EncodedMovRaxImm64Len = 10

// EncodedRetLen is the encoded byte length of "ret" (constant, fixed 1).
const EncodedRetLen = 1

// =============================================================================
// PJ3+ Emitter primitive extensions (incremental allowlist, following 06 §3.x families)
// =============================================================================
//
// This section extends PJ1's two primitives EmitMovRaxImm64 + EmitRet, adding the
// minimal instruction-encoding primitives for the control-flow / arithmetic /
// table-IC families. These primitives do not themselves form complete opcode
// templates (a complete template also needs jitContext field inline access +
// helper calls + guard-failure OSR exit mechanisms, left for later PJ extensions),
// but building out the emitter interface surface lets PJ4+ use them directly on
// startup.
//
// **Within PJ3 scope SupportsAllOpcodes is still all-false** — this section's
// primitives are prove-the-path tested by unit tests (each EmitXxx's bytecode
// sequence is validated by execution through the mmap segment), but the bridge
// main path does not reach them.

// EmitMovImm64ToReg emits the "mov regNum, imm64" 10-byte sequence (following 06
// §3.7 straight-line family, regNum ∈ [0, 7] = RAX/RCX/RDX/RBX/RSP/RBP/RSI/RDI).
//
// **Key defense** (following review 🟠 #1): reg=4 (RSP) / reg=5 (RBP) are legal
// amd64 encodings, but semantically mov rsp/rbp, imm64 would corrupt the
// trampoline stack protocol (on return, ret jumps to an invalid address → SEGV).
// This function falls back to RAX (0) for 4/5. reg 6/7 (RSI/RDI) are legal and
// usable.
func EmitMovImm64ToReg(buf []byte, regNum uint8, imm uint64) []byte {
	if regNum > 7 || regNum == 4 || regNum == 5 {
		// RSP/RBP unsafe, defensively fall back to RAX
		regNum = 0
	}
	buf = append(buf, 0x48, 0xb8|regNum)
	for i := 0; i < 8; i++ {
		buf = append(buf, byte(imm>>(8*i)))
	}
	return buf
}

// EmitNop emits the "nop" single-byte sequence (0x90) — for padding / debugging.
//
// PJ4+ use case: alignment padding between templates (the amd64 fast path
// occasionally needs to align a loop entry to 16 bytes, and nop padding is the
// common technique).
func EmitNop(buf []byte) []byte {
	return append(buf, 0x90)
}

// EncodedMovImm64ToRegLen is the encoded byte length of "mov regN, imm64" (constant, fixed 10).
const EncodedMovImm64ToRegLen = 10

// EncodedNopLen is the encoded byte length of "nop" (constant, fixed 1).
const EncodedNopLen = 1

// =============================================================================
// PJ4+ compare family + jump encoding primitives (following 06 §3.2 compare family + §3.4 control-flow JMP)
// =============================================================================

// EmitCmpRaxImm32 emits the "cmp rax, imm32" 6-byte sequence (following 06 §3.2 compare-family basics).
//
// amd64 encoding: 48 3d ii ii ii ii (REX.W cmp rax, imm32). imm32 is a
// sign-extended-to-64-bit immediate; the caller is responsible for ensuring imm
// is within [-2^31, 2^31).
//
// PJ4+ use case: the core comparison of the IsNumber guard — "cmp rax, NaNBoxBase;
// jae .deopt" is the NaN-box single-u64 comparison pattern (following 03 §2.2 +
// design-premises premise four). The current imm32 implementation suffices for
// the high-32-bit scenario of NaN-box boundary decisions; extending to imm64 is
// left for the PJ4 implementation.
func EmitCmpRaxImm32(buf []byte, imm int32) []byte {
	buf = append(buf, 0x48, 0x3d)
	for i := 0; i < 4; i++ {
		buf = append(buf, byte(imm>>(8*i)))
	}
	return buf
}

// EmitJmpRel32 emits the "jmp rel32" 5-byte sequence (following 06 §3.4 JMP direct jump).
//
// amd64 encoding: e9 ii ii ii ii (JMP rel32, 32-bit signed offset, relative to
// the next instruction).
//
// PJ4+ use case: JMP instruction translation — the machine address of the target
// PC is computed at compile time (the forwardJump fixup table, following 06
// §2.2.1 PatchJump protocol) and written into rel32. The caller is responsible
// for ensuring imm is "target address - (this instruction's address + 5)".
func EmitJmpRel32(buf []byte, rel32 int32) []byte {
	buf = append(buf, 0xe9)
	for i := 0; i < 4; i++ {
		buf = append(buf, byte(rel32>>(8*i)))
	}
	return buf
}

// EmitJaeRel32 emits the "jae rel32" 6-byte sequence (following 06 §3.2 IsNumber guard exit).
//
// amd64 encoding: 0f 83 ii ii ii ii (JAE rel32 = if CF=0 jump, i.e. the unsigned
// ">=" comparison jump).
//
// PJ4+ use case: on IsNumber guard failure, jump to the OSR exit — "cmp rax,
// NaNBoxBase; jae .deopt" (rax >= NaNBoxBase means rax is a boxed non-number ⇒
// speculation failed).
func EmitJaeRel32(buf []byte, rel32 int32) []byte {
	buf = append(buf, 0x0f, 0x83)
	for i := 0; i < 4; i++ {
		buf = append(buf, byte(rel32>>(8*i)))
	}
	return buf
}

// EncodedCmpRaxImm32Len is the encoded byte length of "cmp rax, imm32" (constant,
// fixed 6: REX.W 1 byte + opcode 1 byte + imm32 4 bytes).
const EncodedCmpRaxImm32Len = 6

// EncodedJmpRel32Len is the encoded byte length of "jmp rel32" (constant, fixed 5).
const EncodedJmpRel32Len = 5

// EncodedJaeRel32Len is the encoded byte length of "jae rel32" (constant, fixed 6).
const EncodedJaeRel32Len = 6

// =============================================================================
// PJ5+ call-family emitter primitives (following 06 §3.5 CALL/TAILCALL/RETURN call family)
// =============================================================================

// EmitCallRel32 emits the "call rel32" 5-byte sequence (following 06 §3.5 helper-call basics).
//
// amd64 encoding: e8 ii ii ii ii (CALL rel32, 32-bit signed offset, relative to
// the next instruction).
//
// PJ5+ use case: gibbous-jit→host helper calls — the helper function address
// lives in the jitContext helper table (following 05 §4.3), and rel32 =
// helperAddr - (this instruction's address + 5) is computed at compile time. But
// helpers usually far exceed the ±2GB range, so the actual implementation is "mov
// rax, helperAddr; call rax" (indirect CALL, with EmitCallReg added in PJ5+).
// This primitive is kept as a fallback.
func EmitCallRel32(buf []byte, rel32 int32) []byte {
	buf = append(buf, 0xe8)
	for i := 0; i < 4; i++ {
		buf = append(buf, byte(rel32>>(8*i)))
	}
	return buf
}

// EmitCallReg emits the "call regN" 2-byte sequence (following 06 §3.5 indirect CALL helper).
//
// amd64 encoding: ff (d0 + regN) (CALL r/m64, FF /2, reg field encoded in modrm).
// Low 8 registers only (RAX-RDI); reg=4 (RSP) is semantically unusable
// (following review 🟢 #2) and defended against.
func EmitCallReg(buf []byte, regNum uint8) []byte {
	if regNum > 7 || regNum == 4 {
		regNum = 0 // RSP/out-of-range falls back to RAX
	}
	buf = append(buf, 0xff, 0xd0|regNum)
	return buf
}

// EmitPushReg emits the "push regN" single-byte sequence. reg=4 (RSP) / reg=5
// (RBP) are semantically dangerous — RBP is already saved by the trampoline
// prolog and business code must not modify it; pushing RSP is meaningless. This
// function falls back to RAX (0) for 4/5.
func EmitPushReg(buf []byte, regNum uint8) []byte {
	if regNum > 7 || regNum == 4 || regNum == 5 {
		regNum = 0
	}
	buf = append(buf, 0x50|regNum)
	return buf
}

// EmitPopReg emits the "pop regN" single-byte sequence (the pop counterpart of
// EmitPushReg). reg=4/5 use the same defense as EmitPushReg.
func EmitPopReg(buf []byte, regNum uint8) []byte {
	if regNum > 7 || regNum == 4 || regNum == 5 {
		regNum = 0
	}
	buf = append(buf, 0x58|regNum)
	return buf
}

// EncodedCallRel32Len is the encoded byte length of "call rel32" (constant, fixed 5).
const EncodedCallRel32Len = 5

// EncodedCallRegLen is the encoded byte length of "call regN" (constant, fixed 2).
const EncodedCallRegLen = 2

// EncodedPushRegLen is the encoded byte length of "push regN" (constant, fixed 1, low 8 registers).
const EncodedPushRegLen = 1

// EncodedPopRegLen is the encoded byte length of "pop regN" (constant, fixed 1).
const EncodedPopRegLen = 1

// EmitHelperCall emits the "mov rax, helperAddr; call rax" composite template
// (12 bytes) for PJ5 jit→host helper indirect calls.
//
// **Background**: helper function addresses usually far exceed the ±2GB range,
// so a direct `call rel32` is unusable; the standard approach loads the 64-bit
// absolute address into rax and does an indirect call. This macro wraps the
// fixed byte sequence of a PJ5 helper call (`mov rax, imm64` 10 bytes + `call
// rax` 2 bytes = 12 bytes), avoiding primitive-by-primitive assembly at each
// helper call site.
//
// **PJ5 use cases**:
//   - CALL helper inline: `mov rax, &host.DoCall; call rax`
//   - TAILCALL helper inline: `mov rax, &host.DoTailCall; call rax`
//   - safepoint helper: `mov rax, &host.Safepoint; call rax`
//
// **Calling convention** (following 06 §3.5 + amd64 SysV ABI):
//   - arguments passed via rdi/rsi/rdx/rcx/r8/r9 (in the P4 trampoline r15=jitCtx,
//     business arguments go through SysV ABI registers)
//   - return value in rax
//   - before the call, business code must save the current register state
//     (callee-clobbered); the P4 trampoline entry prolog already saves
//     r12-r15 + rbx/rbp, so the helper only preserves the SysV ABI callee-saved set
//
// Encoding:
//
//	[0-9]  mov rax, imm64 (0x48 0xB8 + imm64 LE × 8 bytes = 10 bytes)
//	[10-11] call rax (0xFF 0xD0 = 2 bytes)
//	——— 12 bytes total (EmitMovRaxImm64Len=10 + EncodedCallRegLen=2) ———
//
// Note: EmitCallReg(0) = 0xFF 0xD0 = 2 bytes (reg=0=rax).
func EmitHelperCall(buf []byte, helperAddr uint64) []byte {
	buf = EmitMovRaxImm64(buf, helperAddr)
	buf = EmitCallReg(buf, 0) // call rax (reg=0)
	return buf
}

// EncodedHelperCallLen is the encoded byte length of "mov rax, helperAddr; call
// rax" (constant, fixed 12 = 10 + 2).
const EncodedHelperCallLen = EncodedMovRaxImm64Len + EncodedCallRegLen

// =============================================================================
// PJ6+ template-composition primitives (following 06 §3.6 closure family + §3.7 straight-line family + §3.5 RETURN)
// =============================================================================

// EmitLoadKReturnTemplate emits the complete "LOADK A K(0); RETURN A 1" template (11 bytes).
//
// Equivalent to EmitMovRaxImm64(buf, konst) + EmitRet(buf), but exposed as a
// named template for caller readability.
//
// PJ6+ use case: core Compile-path template — a single-BB "return CONST" calls
// this function directly, without primitive-by-primitive assembly.
func EmitLoadKReturnTemplate(buf []byte, konst uint64) []byte {
	buf = EmitMovRaxImm64(buf, konst)
	buf = EmitRet(buf)
	return buf
}

// EncodedLoadKReturnTemplateLen is the byte length of the "LOADK + RETURN single-BB" template (11).
const EncodedLoadKReturnTemplateLen = EncodedMovRaxImm64Len + EncodedRetLen

// EmitProlog emits a simplified trampoline entry prolog (push rbx + push rbp, 2 bytes).
//
// **Relation to trampoline_full_amd64.s**: this emitter primitive only aligns
// the emit interface (letting jit.Compile generate the trampoline prolog through
// the emit path on demand, when it "needs to save callee-saved registers before
// running a template"); the **full 5-register prolog** (push rbx/rbp/r12/r13/r15,
// with r14=Go G untouched) is implemented directly in trampoline_full_amd64.s.
// This simplified version only covers the low 8 registers (rbx/rbp); r12-r15
// need a REX.B prefix, left for PJ7+ to add via EmitPushRegHi.
//
// **Bypassing the RBP defense in EmitPushReg/EmitPopReg**: saving RBP in the
// trampoline prolog is a legal use of the callee-saved protocol (restored by pop
// at the exit), different from business code modifying RBP. It emits the push
// bytes directly (0x55 = push rbp / 0x53 = push rbx).
func EmitProlog(buf []byte) []byte {
	buf = append(buf, 0x53) // push rbx
	buf = append(buf, 0x55) // push rbp
	return buf
}

// EmitEpilog emits the trampoline exit epilog (the counterpart of EmitProlog, popping in reverse order).
func EmitEpilog(buf []byte) []byte {
	buf = append(buf, 0x5d) // pop rbp
	buf = append(buf, 0x5b) // pop rbx
	return buf
}

// EncodedPrologLen is the byte length of EmitProlog (2, simplified version).
const EncodedPrologLen = 2

// EncodedEpilogLen is the byte length of EmitEpilog (2, simplified version).
const EncodedEpilogLen = 2

// --- PJ2 byte-level arithmetic emit primitives ---
//
// Following docs/design/p4-method-jit/03-speculation-ic.md §2 IsNumber×2
// speculation template + 06-backends.md §3.2 amd64 arithmetic family: the
// double-number fast path emits SSE2 floating-point instructions directly
// (movsd / addsd / subsd / mulsd / divsd), with no host helper call needed.
//
// **PJ2 physical basis** (the primitives in this section are themselves usable,
// but the full speculation template needs jitContext SP switching + register
// allocation + IsNumber guard codegen, left for the full PJ2-PJ5 wiring).

// EmitMovsdXmmFromMem emits "movsd xmm0, [reg+disp32]", loading a 64-bit double
// from memory into xmm0. Instruction: F2 REX 0F 10 /0 modrm + disp32 (8 bytes).
//
// The baseReg parameter is the base register number ([0,7] low 8 registers; the
// high 8 need REX.B, left for PJ3+). disp32 is a signed 32-bit offset.
//
// Encoding: F2 0F 10 80+baseReg disp32 (when baseReg<8 + no REX.W needed; movsd
// is an SSE2 instruction and xmm register encoding does not need REX.W).
func EmitMovsdXmmFromMem(buf []byte, xmmDst uint8, baseReg uint8, disp32 int32) []byte {
	// Defensive fallback: xmm range [0,7], base range [0,7] (high 8 registers left for PJ3+)
	if xmmDst > 7 {
		xmmDst = 0
	}
	if baseReg > 7 {
		baseReg = 0
	}
	// F2 prefix(scalar double),0F 10 = MOVSD xmm, xmm/m64
	buf = append(buf, 0xF2, 0x0F, 0x10)
	// modrm:mod=10(disp32) reg=xmmDst rm=baseReg
	modrm := byte(0x80) | (xmmDst&0x7)<<3 | (baseReg & 0x7)
	buf = append(buf, modrm)
	// disp32 LE
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EmitMovsdMemFromXmm emits "movsd [reg+disp32], xmm0", storing xmm0 to memory.
//
// Instruction: F2 0F 11 modrm + disp32 (8 bytes).
func EmitMovsdMemFromXmm(buf []byte, xmmSrc uint8, baseReg uint8, disp32 int32) []byte {
	if xmmSrc > 7 {
		xmmSrc = 0
	}
	if baseReg > 7 {
		baseReg = 0
	}
	// F2 0F 11 = MOVSD xmm/m64, xmm
	buf = append(buf, 0xF2, 0x0F, 0x11)
	modrm := byte(0x80) | (xmmSrc&0x7)<<3 | (baseReg & 0x7)
	buf = append(buf, modrm)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EmitAddsdXmmXmm emits "addsd xmmDst, xmmSrc" (xmm double-double add, 4 bytes).
// Instruction: F2 0F 58 modrm.
func EmitAddsdXmmXmm(buf []byte, xmmDst uint8, xmmSrc uint8) []byte {
	if xmmDst > 7 {
		xmmDst = 0
	}
	if xmmSrc > 7 {
		xmmSrc = 0
	}
	buf = append(buf, 0xF2, 0x0F, 0x58)
	modrm := byte(0xC0) | (xmmDst&0x7)<<3 | (xmmSrc & 0x7)
	buf = append(buf, modrm)
	return buf
}

// EmitSubsdXmmXmm emits "subsd xmmDst, xmmSrc" (instruction: F2 0F 5C modrm).
func EmitSubsdXmmXmm(buf []byte, xmmDst uint8, xmmSrc uint8) []byte {
	if xmmDst > 7 {
		xmmDst = 0
	}
	if xmmSrc > 7 {
		xmmSrc = 0
	}
	buf = append(buf, 0xF2, 0x0F, 0x5C)
	modrm := byte(0xC0) | (xmmDst&0x7)<<3 | (xmmSrc & 0x7)
	buf = append(buf, modrm)
	return buf
}

// EmitMulsdXmmXmm emits "mulsd xmmDst, xmmSrc" (instruction: F2 0F 59 modrm).
func EmitMulsdXmmXmm(buf []byte, xmmDst uint8, xmmSrc uint8) []byte {
	if xmmDst > 7 {
		xmmDst = 0
	}
	if xmmSrc > 7 {
		xmmSrc = 0
	}
	buf = append(buf, 0xF2, 0x0F, 0x59)
	modrm := byte(0xC0) | (xmmDst&0x7)<<3 | (xmmSrc & 0x7)
	buf = append(buf, modrm)
	return buf
}

// EmitDivsdXmmXmm emits "divsd xmmDst, xmmSrc" (instruction: F2 0F 5E modrm).
func EmitDivsdXmmXmm(buf []byte, xmmDst uint8, xmmSrc uint8) []byte {
	if xmmDst > 7 {
		xmmDst = 0
	}
	if xmmSrc > 7 {
		xmmSrc = 0
	}
	buf = append(buf, 0xF2, 0x0F, 0x5E)
	modrm := byte(0xC0) | (xmmDst&0x7)<<3 | (xmmSrc & 0x7)
	buf = append(buf, modrm)
	return buf
}

// EncodedMovsdMemLen is the byte length of the MOVSD xmm <-> [base+disp32] sequence (8).
const EncodedMovsdMemLen = 8

// EncodedSseBinopLen is the byte length of ADDSD/SUBSD/MULSD/DIVSD xmm,xmm (4).
const EncodedSseBinopLen = 4

// EmitMovqRaxFromR15Disp emits "mov rax, [r15+disp32]", loading 64 bits from
// r15+disp32 into rax (instruction: 4C would be REX.WR, which is wrong; we use
// REX.B=1 with base=r15; the actual encoding is 49 8B 87 disp32 = REX.W+B 8B /0 modrm).
//
// Use case: PJ2 full speculation template — the mmap segment reads jitContext
// fields via r15 (arenaBase / valueStackBase / preemptFlag etc.).
//
// Encoding: 49 8B 87 disp32 (7 bytes).
//   - 49 = REX prefix (W=1 64-bit + B=1 makes the rm field use r15 instead of r7)
//   - 8B = MOV r64, r/m64
//   - 87 = ModR/M: mod=10 (disp32) reg=000 (rax) rm=111 (r15 with REX.B)
func EmitMovqRaxFromR15Disp(buf []byte, disp32 int32) []byte {
	// REX.W (0x48) | REX.B (0x01) = 0x49
	buf = append(buf, 0x49, 0x8B, 0x87)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EmitMovsdR15DispFromXmm emits "movsd [r15+disp32], xmm" — spilling a 64-bit
// double from an xmm register into a JITContext field addressed via r15.
//
// Unlike EmitMovsdMemFromXmm (which clamps the base register to [0,7] and so
// cannot address r15), this uses REX.B to select r15 as the rm base. Used by
// the PJ3 loopFuel exhausted-tail to save idx/limit/step (xmm0/1/2) into the
// loopSpill0/1/2 slots before the HelperLoopFuel exit-reason round trip
// (issue #143 — the PJ3 templates keep loop state in xmm, which no ABI
// preserves across the RET-to-Go dispatch, so it must be spilled).
//
// Encoding: F2 41 0F 11 /r disp32 (9 bytes).
//   - F2       = scalar-double prefix
//   - 41       = REX.B (selects r15 as the rm base)
//   - 0F 11    = MOVSD xmm/m64, xmm
//   - ModRM    = mod=10 (disp32) reg=xmmSrc rm=111 (r15 via REX.B)
func EmitMovsdR15DispFromXmm(buf []byte, xmmSrc uint8, disp32 int32) []byte {
	if xmmSrc > 7 {
		xmmSrc = 0
	}
	buf = append(buf, 0xF2, 0x41, 0x0F, 0x11)
	modrm := byte(0x80) | (xmmSrc&0x7)<<3 | 0x7 // rm=111 (r15)
	buf = append(buf, modrm)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EmitMovsdXmmFromR15Disp emits "movsd xmm, [r15+disp32]" — the reload
// counterpart of EmitMovsdR15DispFromXmm, restoring idx/limit/step into
// xmm0/1/2 at the PJ3 loopFuel resume entry (issue #143).
//
// Encoding: F2 41 0F 10 /r disp32 (9 bytes).
func EmitMovsdXmmFromR15Disp(buf []byte, xmmDst uint8, disp32 int32) []byte {
	if xmmDst > 7 {
		xmmDst = 0
	}
	buf = append(buf, 0xF2, 0x41, 0x0F, 0x10)
	modrm := byte(0x80) | (xmmDst&0x7)<<3 | 0x7 // rm=111 (r15)
	buf = append(buf, modrm)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EncodedMovsdR15DispLen is the byte length of the MOVSD xmm <-> [r15+disp32]
// spill/reload sequence (9): F2(1) + REX.B(1) + 0F(1) + opcode(1) + ModRM(1) +
// disp32(4).
const EncodedMovsdR15DispLen = 9

// EmitSubDwordR15DispImm8 emits "sub dword [r15+disp32], imm8" — the loopFuel
// back-edge decrement (issue #143 PJ3, mirroring the per-op native emit's
// emitLoopFuelBackEdge at emit_ops_amd64.go).
//
// Encoding: 41 83 AF disp32 imm8 (8 bytes).
//   - 41    = REX.B (selects r15 as the rm base)
//   - 83    = group-1 r/m32, imm8 (sign-extended)
//   - AF    = ModRM: mod=10 (disp32) reg=101 (/5 = SUB) rm=111 (r15)
//   - disp32, imm8
func EmitSubDwordR15DispImm8(buf []byte, disp32 int32, imm8 byte) []byte {
	buf = append(buf, 0x41, 0x83, 0xAF)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	buf = append(buf, imm8)
	return buf
}

// EncodedSubDwordR15DispImm8Len is the byte length of "sub dword [r15+disp32],
// imm8" (8): REX.B(1) + opcode(1) + ModRM(1) + disp32(4) + imm8(1).
const EncodedSubDwordR15DispImm8Len = 8

// EmitMovqRaxFromMemReg emits "mov rax, [reg+disp32]", loading into rax from a
// given base register (used to read the value-stack slot at valueStackBase +
// reg*8 — but valueStackBase must first be loaded into some base register).
//
// Encoding example: 48 8B 80+rd disp32 (REX.W=1, no REX.B needed, reg<8).
// Low 8 registers only (rax-rdi, reg<8) — high 8 registers need REX.B, left for PJ3+.
//
// **Note**: this primitive simply reads register+offset, with no SIB addressing
// (no [base+index*8]), so it cannot directly emit "mov rax, [valueStackBase +
// reg_idx*8]" (that would need SIB). The PJ2 simplification computes reg_idx*8 on
// the Go side (computing disp32 = idx*8 at emit time), so the mmap segment only
// needs base+disp32 addressing.
func EmitMovqRaxFromMemReg(buf []byte, baseReg uint8, disp32 int32) []byte {
	if baseReg > 7 {
		baseReg = 0
	}
	buf = append(buf, 0x48, 0x8B)
	modrm := byte(0x80) | (baseReg & 0x7) // mod=10 reg=000 (rax) rm=baseReg
	buf = append(buf, modrm)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EncodedMovqFromR15DispLen is the byte length of "mov rax, [r15+disp32]" (7).
const EncodedMovqFromR15DispLen = 7

// EncodedMovqFromMemRegLen is the byte length of "mov rax, [low_reg+disp32]" (7).
const EncodedMovqFromMemRegLen = 7

// EmitMovqMemRegFromRax emits "mov [reg+disp32], rax", storing rax to memory.
// Encoding: 48 89 80+r disp32 (7 bytes).
func EmitMovqMemRegFromRax(buf []byte, baseReg uint8, disp32 int32) []byte {
	if baseReg > 7 {
		baseReg = 0
	}
	buf = append(buf, 0x48, 0x89)
	modrm := byte(0x80) | (baseReg & 0x7)
	buf = append(buf, modrm)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EmitUcomisdXmmXmm emits "ucomisd xmmDst, xmmSrc" (unordered SD compare, sets ZF/PF/CF).
// Used for the jcc following the IsNumber guard's NaN detection.
// Instruction: 66 0F 2E modrm (4 bytes).
func EmitUcomisdXmmXmm(buf []byte, xmmDst uint8, xmmSrc uint8) []byte {
	if xmmDst > 7 {
		xmmDst = 0
	}
	if xmmSrc > 7 {
		xmmSrc = 0
	}
	buf = append(buf, 0x66, 0x0F, 0x2E)
	modrm := byte(0xC0) | (xmmDst&0x7)<<3 | (xmmSrc & 0x7)
	buf = append(buf, modrm)
	return buf
}

// EmitSqrtsdXmmFromMem emits "sqrtsd xmmDst, [baseReg+disp32]" — reads an f64
// from memory and takes its square root (issue #77 math.sqrt intrinsic).
// Instruction: F2 0F 51 modrm + disp32.
func EmitSqrtsdXmmFromMem(buf []byte, xmmDst uint8, baseReg uint8, disp32 int32) []byte {
	if xmmDst > 7 {
		xmmDst = 0
	}
	if baseReg > 7 {
		baseReg = 0
	}
	buf = append(buf, 0xF2, 0x0F, 0x51)
	modrm := byte(0x80) | (xmmDst&0x7)<<3 | (baseReg & 0x7)
	buf = append(buf, modrm)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EmitRoundsdXmmFromMem emits "roundsd xmmDst, [baseReg+disp32], imm8" —
// SSE4.1 directed rounding (issue #77 math.floor/ceil intrinsic). mode: 1=floor
// (round toward -inf), 2=ceil (toward +inf), byte-for-byte consistent with Go's
// math.Floor / math.Ceil (whose amd64 asm is also ROUNDSD). Instruction: 66 0F 3A 0B modrm
// + disp32 + imm8.
func EmitRoundsdXmmFromMem(buf []byte, xmmDst uint8, baseReg uint8, disp32 int32, mode uint8) []byte {
	if xmmDst > 7 {
		xmmDst = 0
	}
	if baseReg > 7 {
		baseReg = 0
	}
	buf = append(buf, 0x66, 0x0F, 0x3A, 0x0B)
	modrm := byte(0x80) | (xmmDst&0x7)<<3 | (baseReg & 0x7)
	buf = append(buf, modrm)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	buf = append(buf, mode)
	return buf
}

// EmitMovsdXmmXmm emits "movsd xmmDst, xmmSrc" (reg-reg, 4 bytes) — used to move
// the winning operand into the result register during max/min selection.
// Instruction: F2 0F 10 modrm (mod=11).
func EmitMovsdXmmXmm(buf []byte, xmmDst uint8, xmmSrc uint8) []byte {
	if xmmDst > 7 {
		xmmDst = 0
	}
	if xmmSrc > 7 {
		xmmSrc = 0
	}
	buf = append(buf, 0xF2, 0x0F, 0x10)
	modrm := byte(0xC0) | (xmmDst&0x7)<<3 | (xmmSrc & 0x7)
	buf = append(buf, modrm)
	return buf
}

// EmitJeRel32 emits "je rel32" (0F 84 rel32, 6 bytes) and similar conditional jumps.
func EmitJeRel32(buf []byte, rel32 int32) []byte {
	buf = append(buf, 0x0F, 0x84)
	buf = append(buf,
		byte(uint32(rel32)),
		byte(uint32(rel32)>>8),
		byte(uint32(rel32)>>16),
		byte(uint32(rel32)>>24))
	return buf
}

// EmitJneRel32 emits "jne rel32" (0F 85 rel32).
func EmitJneRel32(buf []byte, rel32 int32) []byte {
	buf = append(buf, 0x0F, 0x85)
	buf = append(buf,
		byte(uint32(rel32)),
		byte(uint32(rel32)>>8),
		byte(uint32(rel32)>>16),
		byte(uint32(rel32)>>24))
	return buf
}

// EmitJbRel32 emits "jb rel32" (0F 82 rel32, unsigned <).
func EmitJbRel32(buf []byte, rel32 int32) []byte {
	buf = append(buf, 0x0F, 0x82)
	buf = append(buf,
		byte(uint32(rel32)),
		byte(uint32(rel32)>>8),
		byte(uint32(rel32)>>16),
		byte(uint32(rel32)>>24))
	return buf
}

// EmitJbeRel32 emits "jbe rel32" (0F 86 rel32, unsigned <=).
func EmitJbeRel32(buf []byte, rel32 int32) []byte {
	buf = append(buf, 0x0F, 0x86)
	buf = append(buf,
		byte(uint32(rel32)),
		byte(uint32(rel32)>>8),
		byte(uint32(rel32)>>16),
		byte(uint32(rel32)>>24))
	return buf
}

// EmitJaRel32 emits "ja rel32" (0F 87 rel32, unsigned >).
func EmitJaRel32(buf []byte, rel32 int32) []byte {
	buf = append(buf, 0x0F, 0x87)
	buf = append(buf,
		byte(uint32(rel32)),
		byte(uint32(rel32)>>8),
		byte(uint32(rel32)>>16),
		byte(uint32(rel32)>>24))
	return buf
}

// EncodedMovqMemFromRaxLen is the byte length of "mov [reg+disp32], rax" (7).
const EncodedMovqMemFromRaxLen = 7

// EncodedUcomisdLen is the byte length of "ucomisd xmm,xmm" (4).
const EncodedUcomisdLen = 4

// EncodedJccRel32Len is the byte length of a 0F 8x rel32 conditional jump (6).
const EncodedJccRel32Len = 6

// EmitMovRcxImm64 emits "mov rcx, imm64" (REX.W + B9+rd imm64, 10 bytes).
// Used to load a NaN-box threshold constant into rcx before doing a cmp rax, rcx comparison.
func EmitMovRcxImm64(buf []byte, imm uint64) []byte {
	buf = append(buf, 0x48, 0xB9) // REX.W mov rcx, imm64
	buf = append(buf,
		byte(imm), byte(imm>>8), byte(imm>>16), byte(imm>>24),
		byte(imm>>32), byte(imm>>40), byte(imm>>48), byte(imm>>56))
	return buf
}

// EmitCmpRaxRcx emits "cmp rax, rcx" (REX.W + 39 modrm, 3 bytes).
// Encoding: 48 39 C8 (modrm: mod=11 reg=001=rcx rm=000=rax).
func EmitCmpRaxRcx(buf []byte) []byte {
	return append(buf, 0x48, 0x39, 0xC8)
}

// EncodedMovRcxImm64Len is the byte length of "mov rcx, imm64" (10).
const EncodedMovRcxImm64Len = 10

// EncodedCmpRaxRcxLen is the byte length of "cmp rax, rcx" (3).
const EncodedCmpRaxRcxLen = 3

// EmitMovqXmmFromRax emits "movq xmmDst, rax", copying 64 bits from rax into xmm
// (instruction: MOVQ xmm, r/m64 = 66 REX.W 0F 6E /r, 5 bytes).
//
// Use case: PJ2 reg-K speculation template — moves a constant value (baked into
// rax via movabs rax, K_value) into xmm1 for an SSE binop, avoiding occupying a
// value-stack slot.
//
// Encoding: 66 48 0F 6E modrm
//   - 66 = operand-size prefix (SSE)
//   - 48 = REX.W (64-bit operand)
//   - 0F 6E = MOVD/MOVQ xmm, r/m32/64
//   - modrm = 11_xxx_yyy (mod=11 register direct; reg=xmm number 0-7; rm=GPR number)
//
// xmm0-7 only (high registers need REX.R, left for PJ3+); the rm field is always rax (reg=0).
func EmitMovqXmmFromRax(buf []byte, xmmDst uint8) []byte {
	if xmmDst > 7 {
		xmmDst = 0
	}
	modrm := byte(0xC0) | (xmmDst&0x7)<<3 // rm=000 (rax)
	return append(buf, 0x66, 0x48, 0x0F, 0x6E, modrm)
}

// EncodedMovqXmmFromRaxLen is the byte length of "movq xmm, rax" (5).
const EncodedMovqXmmFromRaxLen = 5

// EmitCmpByteR15DispImm8 emits "cmp byte ptr [r15+disp32], imm8" (instruction:
// 41 80 BF disp32 imm8, 8 bytes).
//
// Use case: PJ3 safepoint check — on a back edge, the JIT template reads
// preemptFlag (jit.JITContextPreemptFlagOffset) via r15 (=jitContext), then cmp
// 0, followed by jne to the exit stub.
//
// Encoding:
//   - 41 = REX.B (makes the rm field 7 = r15)
//   - 80 = CMP r/m8, imm8 (opcode + /7 encoded into the ModRM reg field)
//   - BF = ModRM: mod=10 (disp32) reg=7 (/7=CMP) rm=7 (+REX.B=r15)
//   - disp32 = 4-byte signed offset
//   - imm8 = 1-byte immediate (usually 0)
func EmitCmpByteR15DispImm8(buf []byte, disp32 int32, imm8 byte) []byte {
	buf = append(buf, 0x41, 0x80, 0xBF)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	buf = append(buf, imm8)
	return buf
}

// EncodedCmpByteR15DispImm8Len is the byte length of "cmp byte [r15+disp32], imm8" (8):
// REX(1) + opcode(1) + ModRM(1) + disp32(4) + imm8(1) = 8.
const EncodedCmpByteR15DispImm8Len = 8

// EmitIncReg64 emits "inc r64" (instruction: REX.W FF /0 modrm, 3 bytes).
//
// Use case: PJ3 byte-level inline integer counter accumulation (FORLOOP idx accumulation).
//
// Encoding: 48 FF C0+rd (REX.W + FF + ModRM=11_000_rd i.e. 0xC0|rd)
//   - 48 = REX.W
//   - FF = opcode for INC/DEC r/m64
//   - C0|rd = ModRM: mod=11 (reg-direct), reg=0 (/0 = INC), rm=rd
//
// reg range [0,7] (rax-rdi); high 8 registers need REX.B, left for PJ3+.
func EmitIncReg64(buf []byte, reg uint8) []byte {
	if reg > 7 {
		reg = 0
	}
	return append(buf, 0x48, 0xFF, 0xC0|(reg&0x7))
}

// EmitDecReg64 emits "dec r64" (instruction: REX.W FF /1 modrm, 3 bytes).
//
// Encoding: 48 FF C8+rd (ModRM=11_001_rd i.e. 0xC8|rd, /1 = DEC)
func EmitDecReg64(buf []byte, reg uint8) []byte {
	if reg > 7 {
		reg = 0
	}
	return append(buf, 0x48, 0xFF, 0xC8|(reg&0x7))
}

// EncodedIncDecReg64Len is the byte length of "inc/dec r64" (3).
const EncodedIncDecReg64Len = 3

// EmitIncQwordPtrAtRax emits "inc qword ptr [rax]" (instruction: REX.W FF /0 modrm,
// 3 bytes). Following §9.20 Option B Spike 1: the mmap segment dereferences
// ciDepthAddr into rax via r15, then does a byte-level inc of ciDepth
// (enterLuaFrame inline).
//
// Encoding: 48 FF 00 (ModRM=00_000_000, mod=00 memory-indirect + reg=/0 INC + rm=000 rax)
func EmitIncQwordPtrAtRax(buf []byte) []byte {
	return append(buf, 0x48, 0xFF, 0x00)
}

// EmitDecQwordPtrAtRax emits "dec qword ptr [rax]" (instruction: REX.W FF /1 modrm,
// 3 bytes). Following §9.20: popCallInfo inline.
//
// Encoding: 48 FF 08 (ModRM=00_001_000, mod=00 memory-indirect + reg=/1 DEC + rm=000 rax)
func EmitDecQwordPtrAtRax(buf []byte) []byte {
	return append(buf, 0x48, 0xFF, 0x08)
}

// EncodedIncDecQwordPtrAtRaxLen is the byte length of "inc/dec qword ptr [rax]" (3).
const EncodedIncDecQwordPtrAtRaxLen = 3

// EmitMovReg64Imm32SignExt emits the "mov r64, imm32-sign-extended" short form
// (REX.W C7 /0 modrm imm32, 7 bytes) — used to load a smaller imm into r64,
// saving 3 bytes over REX.W B8+rd imm64 (10 bytes).
//
// Encoding: 48 C7 C0+rd imm32 (ModRM=11_000_rd i.e. 0xC0|rd, /0 = MOV imm)
//
// A negative imm is sign-extended to 64-bit; for [0, 2^31) it is equivalent to a
// full imm64. PJ3 byte-level integer imm (small loop counters etc.) uses this
// primitive to save bytes; >= 2^32 still uses EmitMovRaxImm64 (10 bytes, imm64).
//
// reg range [0,7]; high 8 registers need REX.B, left for PJ3+.
func EmitMovReg64Imm32SignExt(buf []byte, reg uint8, imm32 int32) []byte {
	if reg > 7 {
		reg = 0
	}
	buf = append(buf, 0x48, 0xC7, 0xC0|(reg&0x7))
	buf = append(buf,
		byte(uint32(imm32)),
		byte(uint32(imm32)>>8),
		byte(uint32(imm32)>>16),
		byte(uint32(imm32)>>24))
	return buf
}

// EncodedMovReg64Imm32SignExtLen is the byte length of "mov r64, imm32-sign-extended" (7).
const EncodedMovReg64Imm32SignExtLen = 7

// PatchRel32 overwrites the 4 bytes at the given position (the rel32 start) in
// buf with newRel32. Use case: during PJ3 byte-level codegen, a forward jmp
// first emits a placeholder rel32=0, then backfills the real rel32 once the jump
// target has been emitted and the in-segment offset is known.
//
// Parameters:
//   - buf: the byte slice being emitted;
//   - rel32Off: the byte offset of the rel32 start (for a jcc shaped like
//     `0F 8x rel32`, rel32Off is the jcc start + 2; for a prefix-less jmp
//     `E9 rel32`, rel32Off = the jmp start + 1);
//   - newRel32: the backfill value (the target address relative to (rel32Off + 4)).
//
// Does no bounds checking — the caller ensures rel32Off+4 ≤ len(buf) via len(buf).
func PatchRel32(buf []byte, rel32Off int, newRel32 int32) {
	buf[rel32Off+0] = byte(uint32(newRel32))
	buf[rel32Off+1] = byte(uint32(newRel32) >> 8)
	buf[rel32Off+2] = byte(uint32(newRel32) >> 16)
	buf[rel32Off+3] = byte(uint32(newRel32) >> 24)
}
