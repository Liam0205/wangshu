//go:build wangshu_p4 && arm64

// pj3_template.go — PJ8 arm64 PJ3 FORLOOP empty-body byte-level template
// (mirrors amd64 pj3_template.go::EmitForLoopEmptyConst's 69/83-byte SSE2
// version on arm64).
//
// **Integration status**: this batch's archEmitForLoopEmptyConst stub -> real
// delegate has landed, and the Compile main path can call this template via
// callJITFull; the real mmap+RX end-to-end test is left to a physical
// self-hosted runner (the arm64 trampoline asm is already in place,
// trampoline_arm64.s).
//
// **arm64 vs amd64 PJ3 template mapping**:
//   - amd64 EmitForLoopEmptyConst 69/83 bytes (without/with safepoint):
//     mov+movq×3 (15*3=45) + subsd+addsd+ucomisd+ja+jmp+ret (4+4+4+6+5+1=24)
//     + safepoint (cmp 8 + jne 6 = 14)
//   - arm64 84/92 bytes (without/with safepoint):
//     mov+fmov×3 (20*3=60) + fsub+fadd+fcmpe+b.cond+b+ret (4*6=24)
//     + safepoint (ldrb 4 + cbnz 4 = 8)
//
// Byte layout (arm64, without-safepoint form):
//
//	[ 0-15]  mov x0, K_init imm64       ; 16
//	[16-19]  fmov d0, x0                 ; 4
//	[20-35]  mov x0, K_limit imm64       ; 16
//	[36-39]  fmov d1, x0                 ; 4
//	[40-55]  mov x0, K_step imm64        ; 16
//	[56-59]  fmov d2, x0                 ; 4
//	[60-63]  fsub d0, d0, d2             ; 4 (FORPREP pre-decrement)
//	[64-67]  ; loop_start
//	[64-67]  fadd d0, d0, d2             ; 4 (idx += step)
//	[68-71]  fcmpe d0, d1                ; 4 (cmp idx, limit)
//	[72-75]  b.gt after_loop             ; 4 (forward, idx > limit -> exit)
//	[76-79]  b loop_start                ; 4 (backward jmp)
//	[80-83]  ; after_loop
//	[80-83]  ret                          ; 4
//	--- total 84 bytes (without safepoint) ---
//
// With-safepoint form (preemptFlagOff>=0, 92 bytes): insert ldrb w0,[x27+pfOff]
// 4 + cbnz w0, after_loop 4 = 8 bytes **after** b.gt (limit exit) and **before**
// b loop_start (back-edge), the same hot-path pattern as
// RegLimit/WithBody/WithBody2 + amd64 EmptyConst (per review COMMENT 17 S-1
// unified position); versus amd64's 14B safepoint check, arm64 is only 8B (RISC
// fixed-length is compact).

package arm64

// emitPJ3LoopFuelBackEdgeArm64 emits the arm64 loopFuel back-edge decrement
// + exhausted tail (mirrors emitPJ3LoopFuelBackEdge on amd64):
//
//	ldr  w16, [x27, #loopFuelOff]    ; 4 bytes
//	sub  w16, w16, #1                ; 4 bytes
//	str  w16, [x27, #loopFuelOff]    ; 4 bytes
//	cbnz w16, loop_start             ; 4 bytes (backward, fuel remaining)
//	; exhausted tail:
//	str  d0, [x27, #spill0Off]       ; 4 bytes (idx)
//	str  d1, [x27, #spill1Off]       ; 4 bytes (limit)
//	str  d2, [x27, #spill2Off]       ; 4 bytes (step)
//	mov  x0, loopFuelCode            ; 16 bytes
//	ret                              ; 4 bytes
func emitPJ3LoopFuelBackEdgeArm64(buf []byte, loopStart int,
	loopFuelOff uint16, loopSpillOff uint16, loopFuelCode uint64) []byte {
	// fuel dec: ldr w16 + sub + str
	buf = EmitLdrWtFromXnDisp(buf, 16, 27, loopFuelOff)
	buf = EmitSubXdImm12(buf, 16, 16, 1)
	buf = EmitStrWtToXnDisp(buf, 16, 27, loopFuelOff)
	// cbnz w16, loop_start (backward)
	cbnzOff := len(buf)
	imm19 := int32(loopStart-cbnzOff) / 4
	buf = EmitCbnzW(buf, 16, imm19)
	// exhausted tail: spill d0/d1/d2
	buf = EmitStrDtToXnDisp(buf, 0, 27, loopSpillOff)
	buf = EmitStrDtToXnDisp(buf, 1, 27, loopSpillOff+8)
	buf = EmitStrDtToXnDisp(buf, 2, 27, loopSpillOff+16)
	// mov x0, loopFuelCode; ret
	buf = EmitMovXdImm64(buf, 0, loopFuelCode)
	buf = EmitRet(buf)
	return buf
}

// emitPJ3LoopFuelResumeArm64 emits the loopFuel resume entry and returns its
// byte offset (mirrors emitPJ3LoopFuelResume on amd64):
//
//	ldr d0, [x27, #spill0Off]        ; 4 bytes
//	ldr d1, [x27, #spill1Off]        ; 4 bytes
//	ldr d2, [x27, #spill2Off]        ; 4 bytes
//	b   loop_start                   ; 4 bytes
func emitPJ3LoopFuelResumeArm64(buf []byte, loopStart int, loopSpillOff uint16) ([]byte, int) {
	resumeOff := len(buf)
	buf = EmitLdrDtFromXnDisp(buf, 0, 27, loopSpillOff)
	buf = EmitLdrDtFromXnDisp(buf, 1, 27, loopSpillOff+8)
	buf = EmitLdrDtFromXnDisp(buf, 2, 27, loopSpillOff+16)
	bOff := len(buf)
	imm26 := int32(loopStart-bOff) / 4
	buf = EmitB(buf, imm26)
	return buf, resumeOff
}

// EmitForLoopEmptyConstArm64 assembles the arm64 "all-constant init/limit/step +
// empty-body FORLOOP byte-level template" (mirrors amd64 EmitForLoopEmptyConst,
// with an optional safepoint check section).
//
// Parameters:
//   - kInit / kLimit / kStep: the three constants' NaN-box raw bits (computed by
//     the caller via value.NumberValue(K).Bits(), same as amd64)
//   - preemptFlagOff: the byte offset of the preempt field at r27+disp
//   - >= 0: enable the safepoint check (per V18 -race preemption discipline)
//   - <  0: skip the safepoint (for tests / strict single-segment compute cases)
//
// Returns the appended buf.
//
// **Byte layout** (84 bytes without safepoint / 92 bytes with safepoint):
//   - without safepoint: mov+fmov×3 60 + fsub 4 + fadd 4 + fcmpe 4 + b.gt 4
//   - b 4 + ret 4 = 84
//   - with safepoint: 84 + ldrb 4 + cbnz 4 = 92 (the safepoint check is inserted
//     **after** b.gt and **before** the b loop_start back-edge — the same
//     hot-path pattern as RegLimit/WithBody/WithBody2 + amd64 EmptyConst, per
//     review COMMENT 17 S-1 unified position)
//
// **Preconditions**:
//   - arm64 trampoline protocol (per 06 §4.2): x27=jitContext /
//     x26=valueStackBase; when safepoint is enabled, read one byte at
//     [x27+preemptFlagOff]
//   - the R(A) idx slot is not written (an empty body does not need it)
//   - the returned x0 is a dummy (the host does not read it after the segment
//     returns)
//
// Use case: byte-level demonstration of the PJ3 FORLOOP empty-body form on the
// arm64 side (the 7.15-25.41x speedup vs the amd64 same-form is left to the
// arm64 physical runner to measure).
func EmitForLoopEmptyConstArm64(buf []byte, kInit, kLimit, kStep uint64, preemptFlagOff int32,
	loopFuelOff, loopSpillOff uint16, loopFuelCode uint64) ([]byte, int) {
	// Load init/limit/step into d0/d1/d2 (20 bytes each: mov x0 imm64 16 + fmov 4)
	buf = EmitMovXdImm64(buf, 0, kInit) // mov x0, kInit
	buf = EmitFmovDdFromXn(buf, 0, 0)   // fmov d0, x0

	buf = EmitMovXdImm64(buf, 0, kLimit) // mov x0, kLimit
	buf = EmitFmovDdFromXn(buf, 1, 0)    // fmov d1, x0

	buf = EmitMovXdImm64(buf, 0, kStep) // mov x0, kStep
	buf = EmitFmovDdFromXn(buf, 2, 0)   // fmov d2, x0

	// FORPREP pre-decrement: d0 = init - step (4 bytes)
	buf = EmitFsubDdDnDm(buf, 0, 0, 2)

	// loop_start label
	loopStart := len(buf)

	// FORLOOP idx+=step: d0 += d2 (4 bytes)
	buf = EmitFaddDdDnDm(buf, 0, 0, 2)

	// cmp idx, limit: fcmpe d0, d1 (4 bytes, signaling ordered)
	buf = EmitFcmpeDnDm(buf, 0, 1)

	// b.hi after_loop placeholder (forward, after fixup).
	// HI (C=1 && Z=0) exits on idx > limit OR unordered — fcmpe sets
	// C=1,Z=0 for NaN operands, so a NaN limit/init leaves with zero
	// iterations like the interpreter (issues #117/#118; b.gt is false
	// on unordered and looped forever).
	bHiOff := len(buf)
	buf = EmitBCond(buf, CondHI, 0) // placeholder imm19=0

	// (optional) safepoint check: ldrb w0, [x27+pfOff]; cbnz w0, after_loop
	// (mirrors amd64 cmp byte [r15+pfOff],0 + jne after_loop 14B; the arm64
	//  ldrb+cbnz is 8B total)
	//
	// **Position**: **after** b.gt (limit exit) and **before** b loop_start
	// (back-edge) — the same hot-path pattern as
	// RegLimit/WithBody/WithBody2 + amd64 EmptyConst (compute + check-for-exit
	// first, rather than checking right after loop_start); this avoids an extra
	// ldrb on an empty iteration, and keeps the position unified across the four
	// forms (per review COMMENT 17 S-1).
	var safepointCbnzOff int = -1
	if preemptFlagOff >= 0 {
		buf = EmitLdrbWtFromXnDisp(buf, 0, 27, uint16(preemptFlagOff))
		safepointCbnzOff = len(buf)
		buf = EmitCbnzW(buf, 0, 0) // placeholder imm19=0
	}

	// b loop_start backward or loopFuel back-edge (4 bytes plain / ~40 bytes with fuel)
	if loopFuelCode != 0 {
		buf = emitPJ3LoopFuelBackEdgeArm64(buf, loopStart, loopFuelOff, loopSpillOff, loopFuelCode)
	} else {
		bLoopOff := len(buf)
		imm26 := int32(loopStart-bLoopOff) / 4
		buf = EmitB(buf, imm26)
	}

	// after_loop label
	afterLoopOff := len(buf)

	// ret (4 bytes)
	buf = EmitRet(buf)

	// resume entry (loopFuelCode != 0): reload d0/d1/d2 + b loop_start
	resumeOff := 0
	if loopFuelCode != 0 {
		buf, resumeOff = emitPJ3LoopFuelResumeArm64(buf, loopStart, loopSpillOff)
	}

	// patch b.gt imm19 = (after_loop - b.gt's own position) / 4 word offset
	imm19BGt := int32(afterLoopOff-bHiOff) / 4
	patchBCondImm19(buf, bHiOff, imm19BGt)

	// patch safepoint cbnz forward (if enabled)
	if safepointCbnzOff >= 0 {
		safepointImm19 := int32(afterLoopOff-safepointCbnzOff) / 4
		patchCbnzImm19(buf, safepointCbnzOff, safepointImm19)
	}

	return buf, resumeOff
}

// EncodedForLoopEmptyConstArm64Len is the arm64 PJ3 FORLOOP empty-body template
// byte count (84 bytes without safepoint / 92 bytes with safepoint; here refers
// to the without-safepoint upper bound this batch's caller cares about, with the
// with-safepoint case added by the caller via EncodedSafepointCheckLen).
const EncodedForLoopEmptyConstArm64Len = 3*(EncodedMovXdImm64Len+EncodedFmovDdFromXnLen) +
	EncodedFsubDdDnDmLen + EncodedFaddDdDnDmLen + EncodedFcmpeDnDmLen +
	EncodedBCondLen + EncodedBLen + EncodedRetLen

// EmitForLoopRegLimitArm64 assembles the "init/step constant + limit is a reg +
// empty-body FORLOOP" arm64 template (mirrors amd64 EmitForLoopRegLimit's
// 103/117 bytes; arm64 accumulates longer due to the MOV imm64 sequence + RISC
// fixed-length).
//
// Byte layout (with step>0 + safepoint enabled as an example, 128 bytes):
//
//	[ 0-3 ] LDR x0, [x26+limitReg*8]      ; 4 (load R(limitReg))
//	[ 4-19] MOV x1, qNanBoxBase imm64     ; 16
//	[20-23] CMP x0, x1                    ; 4
//	[24-27] B.HS deopt                    ; 4 (if R(limitReg) >= qNanBoxBase, not a number, deopt)
//	[28-43] MOV x0, K_init imm64           ; 16
//	[44-47] FMOV d0, x0                    ; 4
//	[48-51] LDR x0, [x26+limitReg*8]       ; 4 (re-load limit, as f64 bits)
//	[52-55] FMOV d1, x0                    ; 4
//	[56-71] MOV x0, K_step imm64           ; 16
//	[72-75] FMOV d2, x0                    ; 4
//	[76-79] FSUB d0, d0, d2                ; 4 (FORPREP pre-decrement)
//	[80-83] ; loop_start
//	[80-83] FADD d0, d0, d2                ; 4
//	[84-87] FCMPE d0, d1                   ; 4
//	[88-91] B.GT after_loop                ; 4 (forward)
//	[92-95] LDRB W0, [x27+pfOff]           ; 4 (safepoint)
//	[96-99] CBNZ W0, after_loop            ; 4
//	[100-103] B loop_start backward        ; 4
//	[104-107] ; after_loop
//	[104-107] RET                          ; 4
//	[108-123] MOV x0, deoptCode imm64      ; 16 (deopt block)
//	[124-127] RET                          ; 4
//	--- with safepoint: 128 bytes ---
//	--- without safepoint (preemptFlagOff<0, saves 8 bytes): 120 bytes ---
//
// **Preconditions**:
//   - x26 = valueStackBase (per 06 §4.2 trampoline load)
//   - x27 = jitContext (used by the safepoint check)
//   - limitReg in [0, 254]
//
// **deopt path** (byte-equal P1): this template does not write the R(A) idx slot
// (empty-body form), so on deopt it returns deoptCode directly, and the caller
// degrades to calling the host via the Run path (same as amd64).
func EmitForLoopRegLimitArm64(buf []byte, kInit, kStep uint64,
	limitReg uint8, deoptCode uint64, preemptFlagOff int32,
	loopFuelOff, loopSpillOff uint16, loopFuelCode uint64) ([]byte, int) {
	// guard: LDR R(limitReg) -> MOV qNanBoxBase -> CMP -> B.HS deopt
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(limitReg)*8)
	buf = EmitMovXdImm64(buf, 1, qNanBoxBase)
	buf = EmitCmpXnXm(buf, 0, 1)
	bHsDeoptOff := len(buf)
	buf = EmitBCond(buf, CondHS, 0) // placeholder imm19=0

	// Load init/limit/step
	buf = EmitMovXdImm64(buf, 0, kInit)
	buf = EmitFmovDdFromXn(buf, 0, 0)

	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(limitReg)*8)
	buf = EmitFmovDdFromXn(buf, 1, 0)

	buf = EmitMovXdImm64(buf, 0, kStep)
	buf = EmitFmovDdFromXn(buf, 2, 0)

	// FORPREP pre-decrement
	buf = EmitFsubDdDnDm(buf, 0, 0, 2)

	// loop_start label
	loopStart := len(buf)

	// FORLOOP idx+=step
	buf = EmitFaddDdDnDm(buf, 0, 0, 2)
	buf = EmitFcmpeDnDm(buf, 0, 1)

	// b.hi after_loop placeholder (exit on unordered too — #117/#118)
	bHiOff := len(buf)
	buf = EmitBCond(buf, CondHI, 0)

	// (optional) safepoint check
	var safepointCbnzOff int = -1
	if preemptFlagOff >= 0 {
		buf = EmitLdrbWtFromXnDisp(buf, 0, 27, uint16(preemptFlagOff))
		safepointCbnzOff = len(buf)
		buf = EmitCbnzW(buf, 0, 0) // placeholder imm19=0
	}

	// b loop_start backward or loopFuel back-edge
	if loopFuelCode != 0 {
		buf = emitPJ3LoopFuelBackEdgeArm64(buf, loopStart, loopFuelOff, loopSpillOff, loopFuelCode)
	} else {
		bLoopOff := len(buf)
		imm26 := int32(loopStart-bLoopOff) / 4
		buf = EmitB(buf, imm26)
	}

	// after_loop label
	afterLoopOff := len(buf)

	// ret
	buf = EmitRet(buf)

	// deopt block
	deoptStart := len(buf)
	buf = EmitMovXdImm64(buf, 0, deoptCode)
	buf = EmitRet(buf)

	// resume entry (loopFuelCode != 0)
	resumeOff := 0
	if loopFuelCode != 0 {
		buf, resumeOff = emitPJ3LoopFuelResumeArm64(buf, loopStart, loopSpillOff)
	}

	// patch B.GT forward (target = after_loop)
	patchBCondImm19(buf, bHiOff, int32(afterLoopOff-bHiOff)/4)

	// patch safepoint CBNZ forward (target = after_loop)
	if safepointCbnzOff >= 0 {
		patchCbnzImm19(buf, safepointCbnzOff, int32(afterLoopOff-safepointCbnzOff)/4)
	}

	// patch B.HS deopt (target = deopt_block start)
	patchBCondImm19(buf, bHsDeoptOff, int32(deoptStart-bHsDeoptOff)/4)

	return buf, resumeOff
}

// EncodedForLoopRegLimitArm64NoSafepointLen is the without-safepoint form byte count (120).
const EncodedForLoopRegLimitArm64NoSafepointLen = 120

// EncodedForLoopRegLimitArm64WithSafepointLen is the with-safepoint form byte count (128).
const EncodedForLoopRegLimitArm64WithSafepointLen = 128

// arm64ArithOpForSseOp translates the xx of an amd64 SSE opcode byte (F2 0F xx
// ModRM) into an arm64 float binop selector (0/1/2/3 -> FADD/FSUB/FMUL/FDIV).
//
// amd64 SSE opcode values (per amd64 pj2_template.go::SseOp constants):
//   - 0x58 ADDSD -> FADD (arm64 0x1E602800)
//   - 0x5C SUBSD -> FSUB (arm64 0x1E603800)
//   - 0x59 MULSD -> FMUL (arm64 0x1E600800)
//   - 0x5E DIVSD -> FDIV (arm64 0x1E601800)
//
// Returns the emit function pointer (`func(buf []byte, dd, dn, dm uint8) []byte`).
// An unrecognized op returns nil (the caller must guarantee op in {0x58,0x59,0x5C,0x5E}).
func arm64ArithOpForSseOp(sseOp byte) func([]byte, uint8, uint8, uint8) []byte {
	switch sseOp {
	case 0x58: // ADDSD
		return EmitFaddDdDnDm
	case 0x5C: // SUBSD
		return EmitFsubDdDnDm
	case 0x59: // MULSD
		return EmitFmulDdDnDm
	case 0x5E: // DIVSD
		return EmitFdivDdDnDm
	default:
		return nil
	}
}

// EmitForLoopWithRegKBodyArm64 assembles the "all-constant init/limit/step +
// reg-K body FORLOOP" arm64 template (mirrors amd64 EmitForLoopWithRegKBody's
// 121/135 bytes).
//
// Form: `local s=K_s; for i=K_init, K_limit, K_step do s = s op K_body end;
// return s`, with sseOp deciding the body arithmetic (ADD/SUB/MUL/DIV).
//
// Byte layout (with safepoint, 152 bytes):
//
//	[ 0-15]  MOV x0, K_s imm64                ; 16
//	[16-19]  STR x0, [x26+aS*8]               ; 4 (init R(aS)=s)
//	[20-35]  MOV x0, K_init imm64              ; 16
//	[36-39]  FMOV d0, x0                       ; 4
//	[40-55]  MOV x0, K_limit imm64             ; 16
//	[56-59]  FMOV d1, x0                       ; 4
//	[60-75]  MOV x0, K_step imm64              ; 16
//	[76-79]  FMOV d2, x0                       ; 4
//	[80-83]  FSUB d0, d0, d2                   ; 4 (FORPREP)
//	[84-87]  ; loop_start
//	[84-87]  FADD d0, d0, d2                   ; 4
//	[88-91]  FCMPE d0, d1                      ; 4
//	[92-95]  B.GT after_loop                   ; 4
//	[96-99]  LDR x0, [x26+aS*8]                ; 4 (load s via GP then FMOV)
//	[100-103] FMOV d3, x0                       ; 4
//	[104-119] MOV x0, K_body imm64              ; 16
//	[120-123] FMOV d4, x0                       ; 4
//	[124-127] <FOP> d3, d3, d4                  ; 4 (body s op K)
//	[128-131] FMOV x0, d3                       ; 4 (back to GP to prepare STR)
//	[132-135] STR x0, [x26+aS*8]                ; 4 (store s)
//	[136-139] LDRB W0, [x27+pfOff]              ; 4 (safepoint)
//	[140-143] CBNZ W0, after_loop               ; 4
//	[144-147] B loop_start                      ; 4
//	[148-151] ; after_loop
//	[148-151] RET                               ; 4
//	--- with safepoint: 152 bytes ---
//	--- without safepoint (pfOff<0, saves 8 bytes): 144 bytes ---
//
// **Preconditions**:
//   - x26 = valueStackBase, x27 = jitContext
//   - aS in [0, 254], independent of the idx/limit/step register numbers
//     (d0/d1/d2)
//   - sseOp in {SseOpAddsd 0x58, SseOpSubsd 0x5C, SseOpMulsd 0x59,
//     SseOpDivsd 0x5E}; an unrecognized one returns buf untouched (this function
//     silently gives up on a nil op)
//
// **deopt path**: no guard, no deopt block (the body is all-constant K, no
// runtime form check); mirrors amd64's same minimal form.
func EmitForLoopWithRegKBodyArm64(buf []byte, kS, kInit, kLimit, kStep, kBody uint64,
	aS uint8, sseOp byte, preemptFlagOff int32,
	loopFuelOff, loopSpillOff uint16, loopFuelCode uint64) ([]byte, int) {
	emitFop := arm64ArithOpForSseOp(sseOp)
	if emitFop == nil {
		return buf, 0
	}

	// 1. Init R(aS) = K_s
	buf = EmitMovXdImm64(buf, 0, kS)
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(aS)*8)

	// 2. FORLOOP setup: load init/limit/step into d0/d1/d2
	buf = EmitMovXdImm64(buf, 0, kInit)
	buf = EmitFmovDdFromXn(buf, 0, 0)
	buf = EmitMovXdImm64(buf, 0, kLimit)
	buf = EmitFmovDdFromXn(buf, 1, 0)
	buf = EmitMovXdImm64(buf, 0, kStep)
	buf = EmitFmovDdFromXn(buf, 2, 0)

	// 3. FORPREP pre-decrement
	buf = EmitFsubDdDnDm(buf, 0, 0, 2)

	// 4. loop_start label
	loopStart := len(buf)

	// 5. FORLOOP idx+=step + cmp
	buf = EmitFaddDdDnDm(buf, 0, 0, 2)
	buf = EmitFcmpeDnDm(buf, 0, 1)

	// 6. b.hi after_loop placeholder (exit on unordered too — #117/#118)
	bHiOff := len(buf)
	buf = EmitBCond(buf, CondHI, 0)

	// 7. body: R(aS) = R(aS) op K_body
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(aS)*8) // load s
	buf = EmitFmovDdFromXn(buf, 3, 0)                   // d3 = s
	buf = EmitMovXdImm64(buf, 0, kBody)                 // x0 = K_body
	buf = EmitFmovDdFromXn(buf, 4, 0)                   // d4 = K_body
	buf = emitFop(buf, 3, 3, 4)                         // d3 = d3 op d4
	buf = EmitFmovXdFromDn(buf, 0, 3)                   // x0 = d3
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(aS)*8)   // store s

	// 8. (optional) safepoint check
	var safepointCbnzOff int = -1
	if preemptFlagOff >= 0 {
		buf = EmitLdrbWtFromXnDisp(buf, 0, 27, uint16(preemptFlagOff))
		safepointCbnzOff = len(buf)
		buf = EmitCbnzW(buf, 0, 0)
	}

	// 9. b loop_start backward or loopFuel back-edge
	if loopFuelCode != 0 {
		buf = emitPJ3LoopFuelBackEdgeArm64(buf, loopStart, loopFuelOff, loopSpillOff, loopFuelCode)
	} else {
		bLoopOff := len(buf)
		imm26 := int32(loopStart-bLoopOff) / 4
		buf = EmitB(buf, imm26)
	}

	// 10. after_loop label
	afterLoopOff := len(buf)

	// 11. ret
	buf = EmitRet(buf)

	// resume entry (loopFuelCode != 0)
	resumeOff := 0
	if loopFuelCode != 0 {
		buf, resumeOff = emitPJ3LoopFuelResumeArm64(buf, loopStart, loopSpillOff)
	}

	// 12. patch forward fixups
	patchBCondImm19(buf, bHiOff, int32(afterLoopOff-bHiOff)/4)
	if safepointCbnzOff >= 0 {
		patchCbnzImm19(buf, safepointCbnzOff, int32(afterLoopOff-safepointCbnzOff)/4)
	}

	return buf, resumeOff
}

// EncodedForLoopWithRegKBodyArm64NoSafepointLen is the without-safepoint form byte count (144).
const EncodedForLoopWithRegKBodyArm64NoSafepointLen = 144

// EncodedForLoopWithRegKBodyArm64WithSafepointLen is the with-safepoint form byte count (152).
const EncodedForLoopWithRegKBodyArm64WithSafepointLen = 152

// EmitForLoopWithRegKBody2Arm64 assembles the "all-constant + reg-K two-section
// body FORLOOP" arm64 template (mirrors amd64 EmitForLoopWithRegKBody2's 140/154
// bytes).
//
// Form: `local s=K_s; for i=K1,K2,K3 do s = s op1 K_body1; s = s op2 K_body2
// end; return s`. The two reg-K ops in the body share d3 across both sections
// (saving one LDR/STR R(aS) round-trip; mirrors amd64's same xmm3-sharing form).
//
// Byte layout (with safepoint, 176 bytes):
//
//	[ 0-19] MOV K_s + STR R(aS)         ; 20 (init s)
//	[20-79] setup d0/d1/d2 + FORPREP     ; 60
//	[80-83] FSUB d0,d0,d2                ; 4
//	[84-87] ; loop_start
//	[84-95] FADD + FCMPE + B.GT          ; 12
//	[96-99] LDR x0, [x26+aS*8]           ; 4 (load s once)
//	[100-103] FMOV d3, x0                ; 4
//	[104-119] MOV x0, K_body1 imm64      ; 16
//	[120-123] FMOV d4, x0                ; 4
//	[124-127] <FOP1> d3, d3, d4          ; 4 (s op1 K1)
//	[128-143] MOV x0, K_body2 imm64      ; 16
//	[144-147] FMOV d4, x0                ; 4
//	[148-151] <FOP2> d3, d3, d4          ; 4 (s op2 K2)
//	[152-155] FMOV x0, d3                ; 4 (back to GP)
//	[156-159] STR x0, [x26+aS*8]         ; 4 (store s once)
//	[160-163] LDRB W0, [x27+pfOff]       ; 4 (safepoint)
//	[164-167] CBNZ W0, after_loop        ; 4
//	[168-171] B loop_start backward      ; 4
//	[172-175] ; after_loop
//	[172-175] RET                         ; 4
//	--- with safepoint: 176 bytes ---
//	--- without safepoint (pfOff<0): 168 bytes ---
//
// **Preconditions**:
//   - x26 = valueStackBase, x27 = jitContext
//   - aS in [0, 254]
//   - sseOp1/sseOp2 in {0x58 ADDSD, 0x5C SUBSD, 0x59 MULSD, 0x5E DIVSD}
//
// **deopt path**: no guard, no deopt block (K_body1/K_body2 are both constants;
// mirrors amd64's same minimal form).
func EmitForLoopWithRegKBody2Arm64(buf []byte, kS, kInit, kLimit, kStep, kBody1, kBody2 uint64,
	aS uint8, sseOp1, sseOp2 byte, preemptFlagOff int32,
	loopFuelOff, loopSpillOff uint16, loopFuelCode uint64) ([]byte, int) {
	emitFop1 := arm64ArithOpForSseOp(sseOp1)
	emitFop2 := arm64ArithOpForSseOp(sseOp2)
	if emitFop1 == nil || emitFop2 == nil {
		return buf, 0
	}

	// 1. Init R(aS) = K_s
	buf = EmitMovXdImm64(buf, 0, kS)
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(aS)*8)

	// 2. setup
	buf = EmitMovXdImm64(buf, 0, kInit)
	buf = EmitFmovDdFromXn(buf, 0, 0)
	buf = EmitMovXdImm64(buf, 0, kLimit)
	buf = EmitFmovDdFromXn(buf, 1, 0)
	buf = EmitMovXdImm64(buf, 0, kStep)
	buf = EmitFmovDdFromXn(buf, 2, 0)

	// 3. FORPREP
	buf = EmitFsubDdDnDm(buf, 0, 0, 2)

	// 4. loop_start
	loopStart := len(buf)

	// 5. FORLOOP idx+=step + cmp
	buf = EmitFaddDdDnDm(buf, 0, 0, 2)
	buf = EmitFcmpeDnDm(buf, 0, 1)

	// 6. b.hi after_loop (exit on unordered too — #117/#118)
	bHiOff := len(buf)
	buf = EmitBCond(buf, CondHI, 0)

	// 7. body: load s once, then the two op sections share d3
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(aS)*8)
	buf = EmitFmovDdFromXn(buf, 3, 0) // d3 = s

	// op1
	buf = EmitMovXdImm64(buf, 0, kBody1)
	buf = EmitFmovDdFromXn(buf, 4, 0)
	buf = emitFop1(buf, 3, 3, 4) // d3 = d3 op1 d4

	// op2
	buf = EmitMovXdImm64(buf, 0, kBody2)
	buf = EmitFmovDdFromXn(buf, 4, 0)
	buf = emitFop2(buf, 3, 3, 4) // d3 = d3 op2 d4

	// store s once
	buf = EmitFmovXdFromDn(buf, 0, 3)
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(aS)*8)

	// 8. (optional) safepoint
	var safepointCbnzOff int = -1
	if preemptFlagOff >= 0 {
		buf = EmitLdrbWtFromXnDisp(buf, 0, 27, uint16(preemptFlagOff))
		safepointCbnzOff = len(buf)
		buf = EmitCbnzW(buf, 0, 0)
	}

	// 9. b loop_start backward or loopFuel back-edge
	if loopFuelCode != 0 {
		buf = emitPJ3LoopFuelBackEdgeArm64(buf, loopStart, loopFuelOff, loopSpillOff, loopFuelCode)
	} else {
		bLoopOff := len(buf)
		imm26 := int32(loopStart-bLoopOff) / 4
		buf = EmitB(buf, imm26)
	}

	// 10. after_loop
	afterLoopOff := len(buf)

	// 11. ret
	buf = EmitRet(buf)

	// resume entry (loopFuelCode != 0)
	resumeOff := 0
	if loopFuelCode != 0 {
		buf, resumeOff = emitPJ3LoopFuelResumeArm64(buf, loopStart, loopSpillOff)
	}

	// 12. patch forward fixups
	patchBCondImm19(buf, bHiOff, int32(afterLoopOff-bHiOff)/4)
	if safepointCbnzOff >= 0 {
		patchCbnzImm19(buf, safepointCbnzOff, int32(afterLoopOff-safepointCbnzOff)/4)
	}

	return buf, resumeOff
}

// EncodedForLoopWithRegKBody2Arm64NoSafepointLen is the without-safepoint form byte count (168).
const EncodedForLoopWithRegKBody2Arm64NoSafepointLen = 168

// EncodedForLoopWithRegKBody2Arm64WithSafepointLen is the with-safepoint form byte count (176).
const EncodedForLoopWithRegKBody2Arm64WithSafepointLen = 176
