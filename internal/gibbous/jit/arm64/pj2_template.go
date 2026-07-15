//go:build wangshu_p4 && arm64

// pj2_template_arm64.go —— PJ8 arm64 byte-level template for PJ2
// speculative ADD (arm64-side mirror of amd64
// pj2_template.go::EmitArithSpeculativeBinopWithGuard, the 92-byte SSE2
// version).
//
// **Not wired up yet** (per §9.12, remaining work stated explicitly): the
// arm64 trampoline asm (`x28=Go G untouched / x27=jitContext /
// x26=valueStackBase`) is left to the self-hosted runner. This batch only
// does byte-level template assembly plus byte-level unit tests to verify
// layout, laying the groundwork for the next stage to connect it.
//
// **arm64 vs amd64 PJ2 template correspondence**:
//   - amd64: guard×2 (26 bytes×2) + binop (29 bytes) + deopt (11 bytes) = 92 bytes
//   - arm64: guard×2 (28 bytes×2) + binop (32 bytes) + deopt (20 bytes) = 108 bytes
//
// arm64 instructions are a fixed 4 bytes, but the MOV imm64 sequence
// (4×movz/movk = 16 bytes) is longer than amd64 MOV rax, imm64 (10 bytes),
// so the total is 16 bytes larger.
//
// **Preset register protocol** (per 06-backends.md §4.2 + arm64 trampoline
// asm, reserved for PJ8+):
//   - x26 = valueStackBase (matches amd64 rbx)
//   - x27 = jitContext (matches amd64 r15)
//   - x28 = Go G (reserved by the Go runtime, untouched)
//   - x0/x1 = scratch general-purpose registers
//   - d0/d1 = floating-point scratch

package arm64

// qNanBoxBase is the NaN-box number upper bound (per
// internal/value/value.go::qNanBoxBase = 0xFFF8 << 48). Number raw bits <
// qNanBoxBase is a valid number.
const qNanBoxBase uint64 = 0xFFF8_0000_0000_0000

// EmitIsNumberGuardArm64 assembles the arm64 "IsNumber guard" byte-level
// sequence (matches amd64 EmitIsNumberGuard). It verifies that R(reg)'s
// NaN-box is a number (< qNanBoxBase), branching to deopt (imm19 word
// offset) on failure.
//
// Sequence (28 bytes):
//
//	LDR x0, [x26 + reg*8]    ; 4 bytes, load R(reg)
//	MOVZ x1, qNanBoxBase[15:0]  ; 4
//	MOVK x1, qNanBoxBase[31:16] LSL 16  ; 4
//	MOVK x1, qNanBoxBase[47:32] LSL 32  ; 4
//	MOVK x1, qNanBoxBase[63:48] LSL 48  ; 4 (= mov x1, qNanBoxBase imm64, 16 bytes total)
//	CMP x0, x1               ; 4
//	B.HS deopt (imm19)       ; 4 (unsigned >= branches to deopt, equivalent to amd64 jae)
//	——— 28 bytes total ———
//
// **imm19 word offset** (arm64 B.cond uses a 19-bit word offset, matches
// EmitBCond): imm19 = (deopt start - this B.cond instruction address) / 4.
// The caller computes it and writes it in directly; there is no separate
// patch stage (in this template the deopt position is known at compile
// time).
//
// Use: each of the two guards in the PJ2 speculative ADD/SUB/MUL/DIV
// double-guard.
func EmitIsNumberGuardArm64(buf []byte, reg uint8, imm19 int32) []byte {
	if reg > 254 {
		reg = 0
	}
	// LDR x0, [x26 + reg*8] (byteOff <= 32760)
	buf = EmitLdrXtFromXnDisp(buf, 0 /*x0*/, 26 /*x26 vsBase*/, uint16(reg)*8)
	// MOV x1, qNanBoxBase imm64 (16 bytes)
	buf = EmitMovXdImm64(buf, 1 /*x1*/, qNanBoxBase)
	// CMP x0, x1
	buf = EmitCmpXnXm(buf, 0, 1)
	// B.HS deopt (imm19)
	buf = EmitBCond(buf, CondHS, imm19)
	return buf
}

// EncodedIsNumberGuardArm64Len is the arm64 IsNumber guard byte count
// (4+16+4+4 = 28).
const EncodedIsNumberGuardArm64Len = EncodedLdrXtFromXnDispLen +
	EncodedMovXdImm64Len + EncodedCmpXnXmLen + EncodedBCondLen

// EmitArithSpeculativeBinopArm64 assembles the arm64 PJ2 speculative BINOP
// fast-path core (no guard section, matches amd64
// EmitArithSpeculativeBinop, the 29-byte SSE2 version).
//
// Sequence (32 bytes):
//
//	LDR x0, [x26 + B*8]      ; 4 (load R(B))
//	FMOV d0, x0              ; 4 (GP→FP)
//	LDR x0, [x26 + C*8]      ; 4 (load R(C))
//	FMOV d1, x0              ; 4 (GP→FP)
//	FADD/FSUB/FMUL/FDIV d0, d0, d1  ; 4 (double-precision binop, sseOp picks add/sub/mul/div)
//	FMOV x0, d0              ; 4 (FP→GP)
//	STR x0, [x26 + A*8]      ; 4 (store R(A))
//	RET                       ; 4
//	——— 32 bytes total ———
//
// **arithOp** parameter: the EmitFadd/Fsub/Fmul/Fdiv function pointers
// cannot encode the base at byte-level precision, so an "op base byte" is
// used to select instead:
//   - 0x28 → FADD (0x1E602800)
//   - 0x38 → FSUB (0x1E603800)
//   - 0x08 → FMUL (0x1E600800)
//   - 0x18 → FDIV (0x1E601800)
//
// This batch dispatches via the emitArithOpArm64 helper based on opSel
// (defined below).
func EmitArithSpeculativeBinopArm64(buf []byte, opSel uint8, a, b, c uint8) []byte {
	if a > 254 {
		a = 0
	}
	if b > 254 {
		b = 0
	}
	if c > 254 {
		c = 0
	}
	// LDR x0, [x26 + B*8] + FMOV d0, x0
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(b)*8)
	buf = EmitFmovDdFromXn(buf, 0, 0) // fmov d0, x0
	// LDR x0, [x26 + C*8] + FMOV d1, x0
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(c)*8)
	buf = EmitFmovDdFromXn(buf, 1, 0) // fmov d1, x0
	// op d0, d0, d1
	buf = emitArithOpArm64(buf, opSel, 0, 0, 1)
	// FMOV x0, d0
	buf = EmitFmovXdFromDn(buf, 0, 0) // fmov x0, d0
	// STR x0, [x26 + A*8]
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(a)*8)
	buf = EmitRet(buf)
	return buf
}

// EncodedArithSpecBinopArm64Len is the arm64 PJ2 binop fast-path byte count
// (8 instructions × 4 = 32).
const EncodedArithSpecBinopArm64Len = 32

// emitArithOpArm64 dispatches to Fadd/Fsub/Fmul/Fdiv based on the opSel
// byte.
//
// opSel byte (per EmitArithSpeculativeBinopArm64 godoc):
//
//	0x28 → FADD
//	0x38 → FSUB
//	0x08 → FMUL
//	0x18 → FDIV
//
// Unknown opSel falls back to FADD.
func emitArithOpArm64(buf []byte, opSel uint8, dd, dn, dm uint8) []byte {
	switch opSel {
	case 0x28:
		return EmitFaddDdDnDm(buf, dd, dn, dm)
	case 0x38:
		return EmitFsubDdDnDm(buf, dd, dn, dm)
	case 0x08:
		return EmitFmulDdDnDm(buf, dd, dn, dm)
	case 0x18:
		return EmitFdivDdDnDm(buf, dd, dn, dm)
	default:
		return EmitFaddDdDnDm(buf, dd, dn, dm)
	}
}

// arm64 PJ2 speculative arithmetic op selection bytes (matches amd64
// SseOpAddsd/Subsd/Mulsd/Divsd).
// **Note**: this is the opcode discriminator byte (arm64 floating-point
// binop instruction format bits[15:8] distinguishes FADD/FSUB/FMUL/FDIV),
// not the low byte of base (base's low byte is always 0x00).
const (
	ArithOpAddArm64 uint8 = 0x28 // FADD opcode discriminator byte (0x1E60_2800)
	ArithOpSubArm64 uint8 = 0x38 // FSUB opcode discriminator byte (0x1E60_3800)
	ArithOpMulArm64 uint8 = 0x08 // FMUL opcode discriminator byte (0x1E60_0800)
	ArithOpDivArm64 uint8 = 0x18 // FDIV opcode discriminator byte (0x1E60_1800)
)

// EmitArithSpeculativeBinopWithGuardArm64 assembles the full PJ2
// speculative template (IsNumber guard×2 + double-number fast path + deopt
// block) byte-level sequence, matching amd64
// EmitArithSpeculativeBinopWithGuard, the 92-byte SSE2 version.
//
// Sequence (108 bytes):
//
//	[guard-B] 28 bytes: LDR R(B) + CMP qNanBoxBase + B.HS deopt
//	[guard-C] 28 bytes: LDR R(C) + CMP qNanBoxBase + B.HS deopt
//	[fast]    32 bytes: LDR + FMOV + LDR + FMOV + op + FMOV + STR + RET
//	[deopt]   20 bytes: MOV x0, deoptCode + RET
//	——— 108 bytes total ———
//
// **imm19 computation** (arm64 B.cond uses a 19-bit word offset, LSL 2 →
// byte offset):
//   - PC after guard1 B.cond = 28
//   - PC after guard2 B.cond = 56
//   - PC at end of fast section = 88
//   - PC at deopt start = 88
//   - imm19_1 (guard1→deopt word offset) = (88 - 24)/4 = 16 (B.cond sits at guard1 offset 24)
//   - imm19_2 (guard2→deopt word offset) = (88 - 52)/4 = 9
//
// Actual computation: imm19 is the word offset of B.cond relative to its
// own instruction address (arm64 PC-relative computes PC = this B.cond's
// address, unlike amd64 rel32 which is the post-jmp PC).
//
// So imm19 = (deopt_offset - b_cond_offset) / 4 (B.cond's own word offset
// to the target).
//
//	guard1 B.cond word offset = 24/4 = 6, deopt word offset = 88/4 = 22 → imm19_1 = 22-6 = 16
//	guard2 B.cond word offset = 52/4 = 13 → imm19_2 = 22-13 = 9
//
// This function emits placeholder imm19 values, writing the computed values
// in directly during assembly (no separate PatchImm19 stage, since the
// deopt position is known at emit time).
func EmitArithSpeculativeBinopWithGuardArm64(buf []byte, opSel uint8, a, b, c uint8, deoptCode uint64) []byte {
	// A single guard section is 28 bytes, the fast section is 32 bytes, so
	// deopt start = 28*2 + 32 = 88.
	// guard1 B.cond's own position = 24 (within guard1: LDR 4 + MOV imm 16 + CMP 4 = 24)
	// imm19_1 = (88 - 24)/4 = 16 (word offset)
	// guard2 B.cond's own position = 28 + 24 = 52
	// imm19_2 = (88 - 52)/4 = 9
	imm19Guard1 := int32(16)
	imm19Guard2 := int32(9)

	buf = EmitIsNumberGuardArm64(buf, b, imm19Guard1)
	buf = EmitIsNumberGuardArm64(buf, c, imm19Guard2)

	// fast section (32 bytes)
	buf = EmitArithSpeculativeBinopArm64(buf, opSel, a, b, c)

	// deopt block (20 bytes): MOV x0, deoptCode imm64 (16) + RET (4)
	buf = EmitMovXdImm64(buf, 0, deoptCode)
	buf = EmitRet(buf)

	return buf
}

// EncodedArithSpecBinopWithGuardArm64Len is the arm64 PJ2 full speculative
// template byte count (28×2 + 32 + 20 = 108).
const EncodedArithSpecBinopWithGuardArm64Len = 2*EncodedIsNumberGuardArm64Len +
	EncodedArithSpecBinopArm64Len + EncodedMovXdImm64Len + EncodedRetLen

// EmitArithSpeculativeBinopRegKArm64 assembles the arm64 PJ2 speculative
// reg-K fast-path core (no guard section, matches amd64
// EmitArithSpeculativeBinopRegK, 36 bytes).
//
// Sequence (44 bytes):
//
//	LDR x0, [x26 + B*8]      ; 4 (load R(B))
//	FMOV d0, x0              ; 4 (GP→FP)
//	MOV x0, kvalue imm64     ; 16 (load K NaN-box bits)
//	FMOV d1, x0              ; 4 (GP→FP)
//	FADD/FSUB/FMUL/FDIV d0, d0, d1  ; 4 (double-precision binop)
//	FMOV x0, d0              ; 4 (FP→GP)
//	STR x0, [x26 + A*8]      ; 4 (store R(A))
//	RET                       ; 4
//	——— 44 bytes total ———
//
// **vs amd64 reg-K 36 bytes**: arm64 is 8 bytes larger (MOV imm64 sequence
// 16 vs amd64 movq 10 accumulated + LDR-then-FMOV vs amd64 single movsd is 2
// steps more, though RISC fixed-length offsets part of it).
func EmitArithSpeculativeBinopRegKArm64(buf []byte, opSel uint8, a, b uint8, kvalue uint64) []byte {
	if a > 254 {
		a = 0
	}
	if b > 254 {
		b = 0
	}
	// LDR x0, [x26 + B*8] + FMOV d0, x0
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(b)*8)
	buf = EmitFmovDdFromXn(buf, 0, 0)
	// MOV x0, kvalue + FMOV d1, x0
	buf = EmitMovXdImm64(buf, 0, kvalue)
	buf = EmitFmovDdFromXn(buf, 1, 0)
	// op d0, d0, d1
	buf = emitArithOpArm64(buf, opSel, 0, 0, 1)
	// FMOV x0, d0 + STR R(A)
	buf = EmitFmovXdFromDn(buf, 0, 0)
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(a)*8)
	buf = EmitRet(buf)
	return buf
}

// EncodedArithSpecBinopRegKArm64Len is the arm64 PJ2 reg-K fast-path byte
// count (44 = LDR 4 + FMOV 4 + MOV imm64 16 + FMOV 4 + FOP 4 + FMOV 4 + STR
// 4 + RET 4).
const EncodedArithSpecBinopRegKArm64Len = 44

// EmitArithSpeculativeBinopRegKWithGuardArm64 assembles the full arm64 PJ2
// speculative reg-K template (with IsNumber guard + deopt block, matches
// amd64 EmitArithSpeculativeBinopRegKWithGuard, 73 bytes).
//
// Byte layout (92 bytes):
//
//	[ 0-27]  IsNumber guard R(B)   ; 28 (LDR + MOV qNanBoxBase + CMP + B.HS deopt)
//	[28-71]  reg-K fast path        ; 44 (LDR/FMOV + MOV K/FMOV + FOP + FMOV/STR + RET)
//	[72-91]  deopt block            ; 20 (MOV x0, deoptCode + RET)
//	——— 92 bytes total ———
//
// **vs amd64 reg-K WithGuard 73 bytes**: arm64 is 19 bytes larger (guard 28
// vs amd64 26 +2, fast 44 vs amd64 36 +8, deopt 20 vs amd64 11 +9).
//
// **Precondition**: if R(B)'s IsNumber guard fails → branch to the deopt
// block and return deoptCode; K is verified as a number at compile time and
// is no longer guarded at runtime (same form as amd64).
func EmitArithSpeculativeBinopRegKWithGuardArm64(buf []byte, opSel uint8,
	a, b uint8, kvalue uint64, deoptCode uint64) []byte {
	// imm19 = (deopt start - guard B.HS itself) / 4
	// guard B.HS is at offset 24 (LDR+MOV+CMP+B.HS, B.HS sits at 24-27)
	// fast section length 44, deopt at offset 28+44 = 72
	// imm19 = (72 - 24) / 4 = 12
	const guardBHSPos = 24
	const deoptStart = 28 + 44
	imm19Guard := int32(deoptStart-guardBHSPos) / 4

	buf = EmitIsNumberGuardArm64(buf, b, imm19Guard)
	buf = EmitArithSpeculativeBinopRegKArm64(buf, opSel, a, b, kvalue)
	// deopt block: MOV x0, deoptCode + RET
	buf = EmitMovXdImm64(buf, 0, deoptCode)
	buf = EmitRet(buf)
	return buf
}

// EncodedArithSpecBinopRegKWithGuardArm64Len is the arm64 PJ2 reg-K
// WithGuard full byte count (28 + 44 + 20 = 92).
const EncodedArithSpecBinopRegKWithGuardArm64Len = 92

// EmitArithSpeculativeChainKKWithGuardArm64 assembles the arm64 PJ2
// speculative chain-KK template (R(A) = R(B) op1 K1 op2 K2 form, matches
// amd64 EmitArithSpeculativeChainKKWithGuard, 92 bytes).
//
// Byte layout (116 bytes):
//
//	[ 0-27]  IsNumber guard R(B)        ; 28
//	[28-95]  fast: LDR/FMOV + K1 section (MOV+FMOV+FOP1) + K2 section (MOV+FMOV+FOP2) + FMOV/STR + RET
//	[28-31]  LDR x0, [x26+B*8]          ; 4
//	[32-35]  FMOV d0, x0                ; 4
//	[36-51]  MOV x0, K1 imm64           ; 16
//	[52-55]  FMOV d1, x0                ; 4
//	[56-59]  FOP1 d0, d0, d1            ; 4
//	[60-75]  MOV x0, K2 imm64           ; 16
//	[76-79]  FMOV d1, x0                ; 4 (overwrites d1)
//	[80-83]  FOP2 d0, d0, d1            ; 4
//	[84-87]  FMOV x0, d0                ; 4
//	[88-91]  STR x0, [x26+A*8]          ; 4
//	[92-95]  RET                         ; 4
//	[96-115] deopt block (MOV deopt + RET); 20
//	——— 116 bytes total ———
//
// **vs amd64 chain-KK 92 bytes**: arm64 is 24 bytes larger (guard +2, fast
// MOV imm64 sequence 16×2 vs amd64 movq 10×2 accumulated +12, deopt +9,
// remaining +1).
//
// **chain advantage**: the intermediate value d0 is reused without being
// written back to the stack, equivalent to host.Arith × 2 but saving one
// boundary crossing (same as amd64).
func EmitArithSpeculativeChainKKWithGuardArm64(buf []byte, opSel1, opSel2 uint8,
	a, b uint8, k1value, k2value, deoptCode uint64) []byte {
	// imm19 = (deopt start - guard B.HS itself) / 4
	// fast section length = 4(LDR) + 4(FMOV d0) + 20(MOV K1+FMOV d1) + 4(FOP1)
	//          + 20(MOV K2+FMOV d1) + 4(FOP2) + 4(FMOV x0) + 4(STR) + 4(RET) = 68
	const guardBHSPos = 24
	const fastLen = 68
	const deoptStart = 28 + fastLen
	imm19Guard := int32(deoptStart-guardBHSPos) / 4

	buf = EmitIsNumberGuardArm64(buf, b, imm19Guard)
	if a > 254 {
		a = 0
	}
	if b > 254 {
		b = 0
	}

	// fast path
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(b)*8)
	buf = EmitFmovDdFromXn(buf, 0, 0)
	// K1 section
	buf = EmitMovXdImm64(buf, 0, k1value)
	buf = EmitFmovDdFromXn(buf, 1, 0)
	buf = emitArithOpArm64(buf, opSel1, 0, 0, 1)
	// K2 section (overwrites d1)
	buf = EmitMovXdImm64(buf, 0, k2value)
	buf = EmitFmovDdFromXn(buf, 1, 0)
	buf = emitArithOpArm64(buf, opSel2, 0, 0, 1)
	// store R(A) + ret
	buf = EmitFmovXdFromDn(buf, 0, 0)
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(a)*8)
	buf = EmitRet(buf)
	// deopt
	buf = EmitMovXdImm64(buf, 0, deoptCode)
	buf = EmitRet(buf)
	return buf
}

// EncodedArithSpecChainKKWithGuardArm64Len is the arm64 PJ2 chain-KK
// WithGuard full byte count (28 + 68 + 20 = 116).
const EncodedArithSpecChainKKWithGuardArm64Len = 116
