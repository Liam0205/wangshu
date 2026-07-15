//go:build wangshu_p4 && arm64

package arm64

import "encoding/binary"

// EmitMovX0Imm64 emits the arm64 "mov x0, imm64" sequence (per 06-backends.md
// §4.2 + §3.7 straight-line family arm64-side mirror).
//
// arm64 encoding: movz + 3×movk sequence (4 bytes each, 16 bytes total).
//
//	movz x0, imm[15:0]      ; 1 1010010 100 imm16 00000  → 0xd2800000 + imm0<<5
//	movk x0, imm[31:16] LSL #16
//	movk x0, imm[47:32] LSL #32
//	movk x0, imm[63:48] LSL #48
//
// hw (shift) field: 00=LSL #0, 01=LSL #16, 10=LSL #32, 11=LSL #48
//
// Full encoding (each 32-bit, written LE):
//   - movz Xd, #imm: 1101_0010_1hw_iiii_iiii_iiii_iiii_id_dd_dd
//     (sf=1, opc=10 movz, hw=00, imm16=...)
//   - movk Xd, #imm, LSL hw: 1111_0010_1hw_iiii_iiii_iiii_iiii_id_dd_dd
//     (sf=1, opc=11 movk, hw=01/10/11)
//
// Uses X0 as the target (d=0) — arm64's default return-value register.
func EmitMovX0Imm64(buf []byte, imm uint64) []byte {
	// movz X0, imm[15:0]
	buf = appendArm64Insn(buf, encodeMovzMovk(false, 0, 0, uint16(imm)))
	// movk X0, imm[31:16] LSL 16
	buf = appendArm64Insn(buf, encodeMovzMovk(true, 0, 1, uint16(imm>>16)))
	// movk X0, imm[47:32] LSL 32
	buf = appendArm64Insn(buf, encodeMovzMovk(true, 0, 2, uint16(imm>>32)))
	// movk X0, imm[63:48] LSL 48
	buf = appendArm64Insn(buf, encodeMovzMovk(true, 0, 3, uint16(imm>>48)))
	return buf
}

// EmitRet emits arm64 "ret" (return to LR/X30), 4 bytes (0xd65f03c0).
func EmitRet(buf []byte) []byte {
	return appendArm64Insn(buf, 0xd65f03c0)
}

// encodeMovzMovk encodes the movz/movk 32-bit instruction word.
//
// movz: sf=1 opc=10 100101 hw imm16 Rd  → bit pattern 0xD2800000 base
// movk: sf=1 opc=11 100101 hw imm16 Rd  → bit pattern 0xF2800000 base
func encodeMovzMovk(isMovk bool, rd uint8, hw uint8, imm uint16) uint32 {
	var base uint32
	if isMovk {
		base = 0xF2800000
	} else {
		base = 0xD2800000
	}
	return base | (uint32(hw)&0x3)<<21 | uint32(imm)<<5 | uint32(rd)&0x1F
}

func appendArm64Insn(buf []byte, insn uint32) []byte {
	var b4 [4]byte
	binary.LittleEndian.PutUint32(b4[:], insn)
	return append(buf, b4[:]...)
}

// EncodedMovX0Imm64Len is the byte length of the arm64 mov x0, imm64 sequence (4 × 4 = 16).
const EncodedMovX0Imm64Len = 16

// EncodedRetLen is the byte length of arm64 ret (4).
const EncodedRetLen = 4

// EmitMovXdImm64 emits the arm64 "mov Xd, imm64" sequence (any general-purpose
// Xd register, not just X0). reg ranges over [0, 30] (X31 = SP/XZR is special
// and not covered by this general encoding).
//
// Same encoding as EmitMovX0Imm64 but allows selecting a different Xd.
func EmitMovXdImm64(buf []byte, rd uint8, imm uint64) []byte {
	if rd > 30 {
		rd = 0
	}
	buf = appendArm64Insn(buf, encodeMovzMovk(false, rd, 0, uint16(imm)))
	buf = appendArm64Insn(buf, encodeMovzMovk(true, rd, 1, uint16(imm>>16)))
	buf = appendArm64Insn(buf, encodeMovzMovk(true, rd, 2, uint16(imm>>32)))
	buf = appendArm64Insn(buf, encodeMovzMovk(true, rd, 3, uint16(imm>>48)))
	return buf
}

// EncodedMovXdImm64Len = 16 (same as EncodedMovX0Imm64Len).
const EncodedMovXdImm64Len = 16

// EmitMovXdFromXn emits arm64 "mov Xd, Xn" (register move).
// Actual encoding: ORR Xd, XZR, Xn = 1010_1010_000n_nnnn_0000_0000_000d_dddd
// = 0xAA000000 | (rn << 16) | (31 << 5) | rd  ; XZR = R31
//
// Note: arm64 has no dedicated MOV reg-reg instruction; it is realized as
// ORR with XZR (assembler syntactic sugar).
func EmitMovXdFromXn(buf []byte, rd, rn uint8) []byte {
	if rd > 30 {
		rd = 0
	}
	if rn > 30 {
		rn = 0
	}
	// ORR Xd, XZR(=31), Xn: 0xAA000000 + (rn << 16) + (31 << 5) + rd
	insn := uint32(0xAA000000) | (uint32(rn)&0x1F)<<16 | (uint32(31)&0x1F)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedMovXdFromXnLen = 4 (a single arm64 instruction).
const EncodedMovXdFromXnLen = 4

// EmitAddXdImm12 emits arm64 "add Xd, Xn, #imm12" (unsigned 12-bit imm).
// Encoding: 1001_0001_00_iiiiiiiiiiii_nnnnn_ddddd = 0x91000000 base
//   - sf=1, op=0 (add), S=0
//   - imm12 <= 0xFFF
//
// Used for in-segment accumulation / register arithmetic.
func EmitAddXdImm12(buf []byte, rd, rn uint8, imm12 uint16) []byte {
	if rd > 30 {
		rd = 0
	}
	if rn > 30 {
		rn = 0
	}
	if imm12 > 0xFFF {
		imm12 = 0xFFF
	}
	insn := uint32(0x91000000) | (uint32(imm12)&0xFFF)<<10 | (uint32(rn)&0x1F)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedAddXdImm12Len = 4.
const EncodedAddXdImm12Len = 4

// EmitSubXdImm12 emits arm64 "sub Xd, Xn, #imm12".
// Encoding: 1101_0001_00_iiiiiiiiiiii_nnnnn_ddddd = 0xD1000000 base.
func EmitSubXdImm12(buf []byte, rd, rn uint8, imm12 uint16) []byte {
	if rd > 30 {
		rd = 0
	}
	if rn > 30 {
		rn = 0
	}
	if imm12 > 0xFFF {
		imm12 = 0xFFF
	}
	insn := uint32(0xD1000000) | (uint32(imm12)&0xFFF)<<10 | (uint32(rn)&0x1F)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedSubXdImm12Len = 4.
const EncodedSubXdImm12Len = 4

// EmitB emits arm64 "b label" unconditional jump; imm26 is a word offset
// (target address = PC + imm26 * 4). imm26 is signed [-2^25, 2^25-1].
//
// Encoding: 0001_01_iiii_iiii_iiii_iiii_iiii_iiii_ii = 0x14000000 base
func EmitB(buf []byte, imm26 int32) []byte {
	insn := uint32(0x14000000) | uint32(imm26)&0x03FFFFFF
	return appendArm64Insn(buf, insn)
}

// EncodedBLen = 4.
const EncodedBLen = 4

// EmitLdrXtFromXnDisp emits arm64 "ldr Xt, [Xn, #pimm12]" 64-bit load with
// unsigned 12-bit offset (per ARMv8 ARM A6.2 + 06-backends.md §4.2 arm64
// load primitive).
//
// arm64 encoding: 1111_1001_01_iiiiiiiiiiii_nnnnn_ttttt = 0xF9400000 base
//   - size=11 (64-bit)
//   - V=0 (general register)
//   - opc=01 (LDR unsigned offset)
//   - imm12 is an **8-byte scaled offset** (byte offset = imm12 * 8), range
//     [0, 32760], step 8
//
// **Parameter byteOff** is a byte offset (must be 8-byte aligned + ≤ 32760).
// This function divides it by 8 automatically to encode the imm12 field.
// Out-of-range or misaligned values fall back to imm12=0 (load Xt from [Xn+0]).
//
// Use case: PJ4 table IC inline arm64 side — load arena base / table.word5 /
// arrayRef / NodeKey/Val etc. (matching amd64 `mov rax, [r14+rcx+disp]`, 8 bytes).
func EmitLdrXtFromXnDisp(buf []byte, rt, rn uint8, byteOff uint16) []byte {
	if rt > 30 {
		rt = 0
	}
	if rn > 30 {
		rn = 0
	}
	// byteOff must be 8-byte aligned + ≤ 32760 (imm12 = byteOff/8 ≤ 4095)
	if byteOff%8 != 0 || byteOff > 32760 {
		byteOff = 0
	}
	imm12 := uint32(byteOff / 8)
	insn := uint32(0xF9400000) | (imm12&0xFFF)<<10 | (uint32(rn)&0x1F)<<5 | uint32(rt)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedLdrXtFromXnDispLen = 4 (a single arm64 instruction).
const EncodedLdrXtFromXnDispLen = 4

// EmitStrXtToXnDisp emits arm64 "str Xt, [Xn, #pimm12]" 64-bit store with
// unsigned 12-bit offset (mirror of EmitLdrXtFromXnDisp, in the write direction).
//
// arm64 encoding: 1111_1001_00_iiiiiiiiiiii_nnnnn_ttttt = 0xF9000000 base
//   - size=11 (64-bit), V=0, opc=00 (STR unsigned offset)
//   - imm12 is an 8-byte scaled offset, same as LDR
//
// Use case: PJ4 table IC SETTABLE arm64 side — store NodeVal / array[idx] in
// the write direction (matching amd64 `mov [r14+rcx+disp], rdx`, 8 bytes).
func EmitStrXtToXnDisp(buf []byte, rt, rn uint8, byteOff uint16) []byte {
	if rt > 30 {
		rt = 0
	}
	if rn > 30 {
		rn = 0
	}
	if byteOff%8 != 0 || byteOff > 32760 {
		byteOff = 0
	}
	imm12 := uint32(byteOff / 8)
	insn := uint32(0xF9000000) | (imm12&0xFFF)<<10 | (uint32(rn)&0x1F)<<5 | uint32(rt)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedStrXtToXnDispLen = 4.
const EncodedStrXtToXnDispLen = 4

// EmitStrWtToXnDisp emits arm64 "str Wt, [Xn, #pimm12]" 32-bit store with
// unsigned 12-bit offset (the 32-bit counterpart of EmitStrXtToXnDisp, used
// for writing uint32 fields).
//
// arm64 encoding: 1011_1001_00_iiiiiiiiiiii_nnnnn_ttttt = 0xB9000000 base
//   - size=10 (32-bit), V=0, opc=00 (STR unsigned offset)
//   - imm12 is a **4-byte scaled offset** (byte offset = imm12 * 4), range
//     [0, 16380], step 4
//
// Use case: C6 EmitFrameInlineExitHelperRequestArm64 writes jitContext.exitReasonCode
// (a uint32 field); matching amd64 `mov [r15+exitReason], eax`, 4 bytes.
func EmitStrWtToXnDisp(buf []byte, rt, rn uint8, byteOff uint16) []byte {
	if rt > 30 {
		rt = 0
	}
	if rn > 30 {
		rn = 0
	}
	if byteOff%4 != 0 || byteOff > 16380 {
		byteOff = 0
	}
	imm12 := uint32(byteOff / 4)
	insn := uint32(0xB9000000) | (imm12&0xFFF)<<10 | (uint32(rn)&0x1F)<<5 | uint32(rt)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedStrWtToXnDispLen = 4.
const EncodedStrWtToXnDispLen = 4

// EmitMovzWdImm16 emits arm64 "movz Wd, #imm16" (32-bit version, clearing
// the high 32 bits) as a single 4-byte instruction.
//
// arm64 encoding: 0101_0010_1_00_iiiiiiiiiiiiiiii_ddddd = 0x52800000 base
//   - sf=0 (32-bit), opc=10 (MOVZ), hw=00 (LSL #0)
//
// vs 64-bit EmitMovXdImm64 (16 bytes, movz+movk×3): collapses to a single
// instruction only when the target value is ≤ 0xFFFF; used by C6 writing
// jitContext.exitReasonCode = ExitInlineHelper (3).
//
// Use case: C6 EmitFrameInlineExitHelperRequestArm64 loads ExitInlineHelper=3
// into w16 (scratch) and w0 (return value); mirror of amd64
// `mov eax, ExitInlineHelper` (5 bytes).
func EmitMovzWdImm16(buf []byte, rd uint8, imm16 uint16) []byte {
	if rd > 30 {
		rd = 0
	}
	insn := uint32(0x52800000) | (uint32(imm16)&0xFFFF)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedMovzWdImm16Len = 4.
const EncodedMovzWdImm16Len = 4

// EmitMovkWdImm16Lsl16 emits arm64 "movk Wd, #imm16, LSL #16" (32-bit
// keep-move into the high half of Wd). Paired with EmitMovzWdImm16 it
// builds an arbitrary 32-bit immediate in two fixed-length instructions:
//
//	movz Wd, #(v & 0xFFFF)
//	movk Wd, #(v >> 16), LSL #16
//
// Encoding: 0111_0010_1_01_iiiiiiiiiiiiiiii_ddddd = 0x72A00000 base
//   - sf=0 (32-bit), opc=11 (MOVK), hw=01 (LSL #16)
//
// Use case: PJ10 exit-reason emit writes jitCtx.resumeOff (uint32 byte
// offset into the code page, can exceed 0xFFFF for large Protos). The
// two imm16 fields are placeholder-patched by the resume prelude once
// the next op's offset is known (see markResumeOffFixup).
func EmitMovkWdImm16Lsl16(buf []byte, rd uint8, imm16 uint16) []byte {
	if rd > 30 {
		rd = 0
	}
	insn := uint32(0x72A00000) | (uint32(imm16)&0xFFFF)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedMovkWdImm16Lsl16Len = 4.
const EncodedMovkWdImm16Lsl16Len = 4

// EmitCmpXnXm emits arm64 "cmp Xn, Xm" (register compare, setting the NZCV
// flags without writing a result). It is actually encoded as SUBS XZR, Xn, Xm:
//
// Encoding: 1110_1011_000_mmmmm_000000_nnnnn_11111 = 0xEB00001F base
//   - sf=1, op=1 (sub), S=1 (SUBS, set flags), shift=00
//   - Rd=11111 (XZR, discard the result and set flags only)
//
// **Parameters rn/rm** are both 0-30; out-of-range values fall back to 0.
//
// Use case: PJ4 table IC inline arm64 side — checking NodeKey == stableKey /
// gen == stableShape etc. (mirror of amd64 `cmp rax, rdx`, 3 bytes).
func EmitCmpXnXm(buf []byte, rn, rm uint8) []byte {
	if rn > 30 {
		rn = 0
	}
	if rm > 30 {
		rm = 0
	}
	insn := uint32(0xEB00001F) | (uint32(rm)&0x1F)<<16 | (uint32(rn)&0x1F)<<5
	return appendArm64Insn(buf, insn)
}

// EncodedCmpXnXmLen = 4.
const EncodedCmpXnXmLen = 4

// EmitBCond emits arm64 "b.cond label" conditional branch (per ARMv8 ARM
// C6.2 B.cond). imm19 is a word offset (target address = PC + imm19 * 4),
// range [-2^20, 2^20-1).
//
// Encoding: 0101_0100_iiiiiiiiiiiiiiiiiiii_0_cond = 0x54000000 base
//   - imm19 sign-extended word offset (LSL 2 → byte offset)
//   - cond 4-bit condition code
//
// Common cond codes (per ARMv8 ARM C1.2 Condition codes):
//   - 0x0 EQ (equal) / 0x1 NE (not equal)
//   - 0x2 CS/HS (carry set, unsigned >=) / 0x3 CC/LO (carry clear, unsigned <)
//   - 0x4 MI (minus) / 0x5 PL (plus or zero)
//   - 0x6 VS (overflow) / 0x7 VC (no overflow)
//   - 0x8 HI (unsigned >) / 0x9 LS (unsigned <=)
//   - 0xA GE (signed >=) / 0xB LT (signed <)
//   - 0xC GT (signed >) / 0xD LE (signed <=)
//
// Use case: PJ4 IC inline arm64 side — the deopt branch after `cmp` (mirror
// of amd64 jne/je rel32, 6 bytes).
func EmitBCond(buf []byte, cond uint8, imm19 int32) []byte {
	if cond > 0xF {
		cond = 0
	}
	insn := uint32(0x54000000) | (uint32(imm19)&0x7FFFF)<<5 | uint32(cond)&0xF
	return appendArm64Insn(buf, insn)
}

// EncodedBCondLen = 4.
const EncodedBCondLen = 4

// arm64 condition codes (per ARMv8 ARM C1.2).
const (
	CondEQ uint8 = 0x0 // equal
	CondNE uint8 = 0x1 // not equal
	CondHS uint8 = 0x2 // unsigned >=
	CondLO uint8 = 0x3 // unsigned <
	CondMI uint8 = 0x4 // negative
	CondPL uint8 = 0x5 // positive or zero
	CondHI uint8 = 0x8 // unsigned >
	CondLS uint8 = 0x9 // unsigned <=
	CondGE uint8 = 0xA // signed >=
	CondLT uint8 = 0xB // signed <
	CondGT uint8 = 0xC // signed >
	CondLE uint8 = 0xD // signed <=
)

// =============================================================================
// arm64 floating-point emit primitives (per 06-backends.md §4.2 + ARMv8 ARM
// C7.2 FP/SIMD)
// =============================================================================
//
// **arm64 vs amd64 floating-point correspondence**:
//   - amd64 SSE2 uses xmm0-xmm15 registers, with movsd / addsd / subsd / mulsd /
//     divsd / ucomisd etc. (F2 0F xx C0+reg, 5 bytes)
//   - arm64 uses D0-D31 registers (double precision), with FMOV / FADD / FSUB /
//     FMUL / FDIV / FCMPE etc. (4 bytes each)
//
// Double-precision NaN-box conversion: a Lua number is a NaN-box uint64 (high
// 16 bits tagged 0x7FF8 for a plain number / a NaN-box tag in the high 16 bits
// means a GCRef). FMOV Dd, Xn does a bit-level copy from a GP register to an FP
// register, semantically equivalent (IEEE 754 unbox).

// EmitFmovDdFromXn emits arm64 "fmov Dd, Xn" (GP→FP 64-bit bit-level copy).
//
// Encoding: 1001_1110_0110_0111_0000_00nn_nnnn_dddd = 0x9E670000 + Rn<<5 + Rd
//
// Use case: PJ2 speculative template arm64 side — after loading value-stack
// R(B) into X0 (via LDR), fmov it to D0, then FADD D0, D0, D1 (D1 from R(C))
// computing R(A) = R(B)+R(C). Mirror of amd64 movsd xmm0, [rbx+B*8] +
// addsd xmm0, [rbx+C*8].
func EmitFmovDdFromXn(buf []byte, dd, xn uint8) []byte {
	if dd > 31 {
		dd = 0
	}
	if xn > 30 {
		xn = 0
	}
	insn := uint32(0x9E670000) | (uint32(xn)&0x1F)<<5 | uint32(dd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedFmovDdFromXnLen = 4.
const EncodedFmovDdFromXnLen = 4

// EmitFmovXdFromDn emits arm64 "fmov Xd, Dn" (FP→GP bit-level copy, mirror
// of EmitFmovDdFromXn).
//
// Encoding: 1001_1110_0110_0110_0000_00nn_nnnn_dddd = 0x9E660000 + Rn<<5 + Rd
//
// Use case: PJ2 speculative template arm64 side, storing back to the value
// stack — after FADD D0..., fmov X0, D0, then STR X0, [Rb_vsBase + A*8].
// Mirror of amd64 movsd [rbx+A*8], xmm0.
func EmitFmovXdFromDn(buf []byte, xd, dn uint8) []byte {
	if xd > 30 {
		xd = 0
	}
	if dn > 31 {
		dn = 0
	}
	insn := uint32(0x9E660000) | (uint32(dn)&0x1F)<<5 | uint32(xd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedFmovXdFromDnLen = 4.
const EncodedFmovXdFromDnLen = 4

// EmitFsqrtDdDn emits arm64 "fsqrt Dd, Dn" (double-precision square root,
// issue #77 math.sqrt intrinsic). Encoding: 0001_1110_0110_0001_1100_00nn_nnnn_dddd
// = 0x1E61C000 + Rn<<5 + Rd.
func EmitFsqrtDdDn(buf []byte, dd, dn uint8) []byte {
	insn := uint32(0x1E61C000) | (uint32(dn)&0x1F)<<5 | uint32(dd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EmitFabsDdDn emits arm64 "fabs Dd, Dn" (double-precision absolute value =
// clear the sign bit, issue #77 math.abs intrinsic). Encoding:
// 0x1E60C000 + Rn<<5 + Rd.
func EmitFabsDdDn(buf []byte, dd, dn uint8) []byte {
	insn := uint32(0x1E60C000) | (uint32(dn)&0x1F)<<5 | uint32(dd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EmitFrintmDdDn emits arm64 "frintm Dd, Dn" (round toward -inf = floor,
// issue #77 math.floor intrinsic; byte-for-byte identical to Go math.Floor).
// Encoding: 0x1E654000 + Rn<<5 + Rd.
func EmitFrintmDdDn(buf []byte, dd, dn uint8) []byte {
	insn := uint32(0x1E654000) | (uint32(dn)&0x1F)<<5 | uint32(dd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EmitFrintpDdDn emits arm64 "frintp Dd, Dn" (round toward +inf = ceil,
// issue #77 math.ceil intrinsic; byte-for-byte identical to Go math.Ceil).
// Encoding: 0x1E64C000 + Rn<<5 + Rd.
func EmitFrintpDdDn(buf []byte, dd, dn uint8) []byte {
	insn := uint32(0x1E64C000) | (uint32(dn)&0x1F)<<5 | uint32(dd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EmitFcmpDnDm emits arm64 "fcmp Dn, Dm" (double-precision compare,
// non-signaling variant — a NaN only sets the FPSR flags without trapping,
// suitable for max/min that may see NaN). Encoding:
// 0x1E602000 + Rm<<16 + Rn<<5.
func EmitFcmpDnDm(buf []byte, dn, dm uint8) []byte {
	insn := uint32(0x1E602000) | (uint32(dm)&0x1F)<<16 | (uint32(dn)&0x1F)<<5
	return appendArm64Insn(buf, insn)
}

// EmitFcselDdDnDmCond emits arm64 "fcsel Dd, Dn, Dm, cond" (take Dn if cond
// holds, otherwise Dm, issue #77 max/min selection). Encoding:
// 0x1E600C00 + Rm<<16 + cond<<12 + Rn<<5 + Rd.
func EmitFcselDdDnDmCond(buf []byte, dd, dn, dm, cond uint8) []byte {
	insn := uint32(0x1E600C00) | (uint32(dm)&0x1F)<<16 |
		(uint32(cond)&0xF)<<12 | (uint32(dn)&0x1F)<<5 | uint32(dd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EmitFaddDdDnDm emits arm64 "fadd Dd, Dn, Dm" (double-precision add, IEEE 754).
//
// Encoding: 0001_1110_011_mmmmm_001010_nnnnn_ddddd = 0x1E602800 + Rm<<16 + Rn<<5 + Rd
//   - size=01 (double precision), op=0010 (FADD), Rm operand 2
//
// Use case: PJ2 speculative template arm64 side core arithmetic (mirror of
// amd64 ADDSD F2 0F 58 C0+reg).
func EmitFaddDdDnDm(buf []byte, dd, dn, dm uint8) []byte {
	if dd > 31 {
		dd = 0
	}
	if dn > 31 {
		dn = 0
	}
	if dm > 31 {
		dm = 0
	}
	insn := uint32(0x1E602800) | (uint32(dm)&0x1F)<<16 | (uint32(dn)&0x1F)<<5 | uint32(dd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedFaddDdDnDmLen = 4.
const EncodedFaddDdDnDmLen = 4

// EmitFsubDdDnDm emits arm64 "fsub Dd, Dn, Dm" (double-precision subtract).
// Encoding: 0x1E603800 base (same as FADD but op=0011). Mirror of amd64 SUBSD.
func EmitFsubDdDnDm(buf []byte, dd, dn, dm uint8) []byte {
	if dd > 31 {
		dd = 0
	}
	if dn > 31 {
		dn = 0
	}
	if dm > 31 {
		dm = 0
	}
	insn := uint32(0x1E603800) | (uint32(dm)&0x1F)<<16 | (uint32(dn)&0x1F)<<5 | uint32(dd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedFsubDdDnDmLen = 4.
const EncodedFsubDdDnDmLen = 4

// EmitFmulDdDnDm emits arm64 "fmul Dd, Dn, Dm" (double-precision multiply).
// Encoding: 0x1E600800 base (op=0000). Mirror of amd64 MULSD.
func EmitFmulDdDnDm(buf []byte, dd, dn, dm uint8) []byte {
	if dd > 31 {
		dd = 0
	}
	if dn > 31 {
		dn = 0
	}
	if dm > 31 {
		dm = 0
	}
	insn := uint32(0x1E600800) | (uint32(dm)&0x1F)<<16 | (uint32(dn)&0x1F)<<5 | uint32(dd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedFmulDdDnDmLen = 4.
const EncodedFmulDdDnDmLen = 4

// EmitFdivDdDnDm emits arm64 "fdiv Dd, Dn, Dm" (double-precision divide).
// Encoding: 0x1E601800 base (op=0001). Mirror of amd64 DIVSD.
func EmitFdivDdDnDm(buf []byte, dd, dn, dm uint8) []byte {
	if dd > 31 {
		dd = 0
	}
	if dn > 31 {
		dn = 0
	}
	if dm > 31 {
		dm = 0
	}
	insn := uint32(0x1E601800) | (uint32(dm)&0x1F)<<16 | (uint32(dn)&0x1F)<<5 | uint32(dd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedFdivDdDnDmLen = 4.
const EncodedFdivDdDnDmLen = 4

// EmitFcmpeDnDm emits arm64 "fcmpe Dn, Dm" (double-precision compare +
// signaling NaN, setting the NZCV flags for the following B.cond).
//
// Encoding: 0001_1110_011_mmmmm_001000_nnnnn_10000 = 0x1E602010 + Rm<<16 + Rn<<5
//   - Rd field = 10000 (opc2=10000 signaling mode)
//   - opcode2 = 10 (signaling QNaN raises an exception, stricter semantics
//     than fcmp, exactly equivalent to amd64 ucomisd-style ordered compare)
//
// Use case: PJ3 FORLOOP arm64 side limit compare (mirror of amd64
// ucomisd xmm0, xmm1, 4 bytes). A following EmitBCond(CondLE/GT/...) uses
// the NZCV flags set by fcmpe to branch.
func EmitFcmpeDnDm(buf []byte, dn, dm uint8) []byte {
	if dn > 31 {
		dn = 0
	}
	if dm > 31 {
		dm = 0
	}
	insn := uint32(0x1E602010) | (uint32(dm)&0x1F)<<16 | (uint32(dn)&0x1F)<<5
	return appendArm64Insn(buf, insn)
}

// EncodedFcmpeDnDmLen = 4.
const EncodedFcmpeDnDmLen = 4

// =============================================================================
// arm64 PJ4 IC template basic primitives (per 06-backends.md §4.2 + ARMv8 ARM
// C5/C6 integer family)
// =============================================================================

// EmitAddXdXnXm emits arm64 "add Xd, Xn, Xm" (shifted register, shift=00
// LSL 0).
//
// Encoding: 1000_1011_00_mmmmm_000000_nnnnn_ddddd = 0x8B000000 base
//   - sf=1, op=0 (add), S=0, shift=00, imm6=000000 (LSL 0)
//
// Use case: PJ4 IC byte-level inline arm64 side — SIB substitute
// (`add x2, x14, x1` adds arena base + GCRef offset together, then
// `ldr x0, [x2, #disp]` replaces amd64 `mov rax, [r14+rcx+disp]` single-SIB
// addressing).
func EmitAddXdXnXm(buf []byte, rd, rn, rm uint8) []byte {
	if rd > 30 {
		rd = 0
	}
	if rn > 30 {
		rn = 0
	}
	if rm > 30 {
		rm = 0
	}
	insn := uint32(0x8B000000) | (uint32(rm)&0x1F)<<16 | (uint32(rn)&0x1F)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedAddXdXnXmLen = 4.
const EncodedAddXdXnXmLen = 4

// EmitMulXdXnXm emits arm64 "mul Xd, Xn, Xm" (actually the MADD Xd, Xn, Xm,
// XZR alias). Per §9.20 Option B Spike 1 enterLuaFrame inline computing
// depth * 40.
//
// Encoding: MADD Xd, Xn, Xm, XZR: 1001_1011_000_mmmmm_011111_nnnnn_ddddd
//   - 0x9B007C00 base (Xa=31=XZR, bit 14-10=11111)
//   - + (Xm<<16) + (Xn<<5) + Xd
//
// Use case: CI segment depth-th frame address computed as
// `depth * ciSlotBytes(40)`, first mov X18, #40 then mul X17, X17, X18.
func EmitMulXdXnXm(buf []byte, rd, rn, rm uint8) []byte {
	if rd > 30 {
		rd = 0
	}
	if rn > 30 {
		rn = 0
	}
	if rm > 30 {
		rm = 0
	}
	insn := uint32(0x9B007C00) | (uint32(rm)&0x1F)<<16 | (uint32(rn)&0x1F)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedMulXdXnXmLen = 4.
const EncodedMulXdXnXmLen = 4

// EmitAndXdXnXm emits arm64 "and Xd, Xn, Xm" (shifted register, shift=00 LSL 0).
//
// Encoding: 1000_1010_00_mmmmm_000000_nnnnn_ddddd = 0x8A000000 base
//   - sf=1, opc=00 (AND), shift=00, imm6=000000 (LSL 0)
//   - + (Xm<<16) + (Xn<<5) + Xd
//
// Use case: PJ4 IC arm64 side — `and x0, x0, x1` (extract the GCRef payload
// from a NaN-box, mirror of amd64 `and rax, rcx`, 3 bytes);
// PJ5 Option B Spike 1 — `and x16, x16, x17` NaN-box payload mask parse.
func EmitAndXdXnXm(buf []byte, rd, rn, rm uint8) []byte {
	if rd > 30 {
		rd = 0
	}
	if rn > 30 {
		rn = 0
	}
	if rm > 30 {
		rm = 0
	}
	insn := uint32(0x8A000000) | (uint32(rm)&0x1F)<<16 | (uint32(rn)&0x1F)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedAndXdXnXmLen = 4.
const EncodedAndXdXnXmLen = 4

// EmitEorXdXnXm emits arm64 "eor Xd, Xn, Xm" (bitwise XOR, shifted
// register form with shift=00 LSL 0).
//
// Encoding: 1100_1010_000_mmmmm_000000_nnnnn_ddddd = 0xCA000000 base
//
// Use case: PJ10 arm64 UNM inline — flip the IEEE-754 sign bit
// (`eor x0, x0, x5` with X5 = 0x8000000000000000), mirror of amd64
// `xor rax, rdx`.
func EmitEorXdXnXm(buf []byte, rd, rn, rm uint8) []byte {
	if rd > 30 {
		rd = 0
	}
	if rn > 30 {
		rn = 0
	}
	if rm > 30 {
		rm = 0
	}
	insn := uint32(0xCA000000) | (uint32(rm)&0x1F)<<16 | (uint32(rn)&0x1F)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedEorXdXnXmLen = 4.
const EncodedEorXdXnXmLen = 4

// EmitOrrXdXnXm emits arm64 "orr Xd, Xn, Xm" (bitwise OR, shifted
// register form with shift=00 LSL 0).
//
// Encoding: 1010_1010_000_mmmmm_000000_nnnnn_ddddd = 0xAA000000 base
//
// Use case: PJ10 arm64 EQ inline +/-0 check (issue #103) — OR the two
// raw operands; a zero magnitude after shifting out the sign bit means
// both values are +/-0.
func EmitOrrXdXnXm(buf []byte, rd, rn, rm uint8) []byte {
	if rd > 30 {
		rd = 0
	}
	if rn > 30 {
		rn = 0
	}
	if rm > 30 {
		rm = 0
	}
	insn := uint32(0xAA000000) | (uint32(rm)&0x1F)<<16 | (uint32(rn)&0x1F)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedOrrXdXnXmLen = 4.
const EncodedOrrXdXnXmLen = 4

// EmitLsrXdImm6 emits arm64 "lsr Xd, Xn, #imm6" (logical shift right, unsigned
// 6-bit shift amount, equivalent to UBFM Xd, Xn, #imm6, #63).
//
// Encoding: 1101_0011_01_immr_111111_nnnnn_ddddd = 0xD340FC00 base
//   - immr = imm6 (shift amount 0-63)
//   - imms = 0x3F (=63, fixed, identifies the LSR variant)
//
// Use case: PJ4 IC arm64 side —
//   - `lsr x0, x0, #48` (tight IsTable guard: the shift moves the NaN-box tag
//     into the low 16 bits, mirror of amd64 `shr rax, 48`, 4 bytes)
//   - `lsr x0, x0, #32` (table.word5 gen is in the high 32 bits, mirror of
//     amd64 `shr rax, 32`, 4 bytes)
func EmitLsrXdImm6(buf []byte, rd, rn uint8, imm6 uint8) []byte {
	if rd > 30 {
		rd = 0
	}
	if rn > 30 {
		rn = 0
	}
	if imm6 > 63 {
		imm6 = 0
	}
	insn := uint32(0xD340FC00) | (uint32(imm6)&0x3F)<<16 | (uint32(rn)&0x1F)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedLsrXdImm6Len = 4.
const EncodedLsrXdImm6Len = 4

// EmitLslXdImm6 emits arm64 "lsl Xd, Xn, #imm6" (logical shift left,
// alias of UBFM Xd, Xn, #((64-sh) mod 64), #(63-sh)).
//
// Encoding: 1101_0011_01_immr_imms_nnnnn_ddddd = 0xD3400000 base
//   - immr = (64 - sh) % 64, imms = 63 - sh
//
// Use case: PJ10 arm64 GETTABLE/SETTABLE ArrayHit inline — scale the
// integer array index to a byte offset (`lsl x2, x2, #3`), the SIB-less
// substitute for amd64's [base + idx*8] addressing.
func EmitLslXdImm6(buf []byte, rd, rn uint8, imm6 uint8) []byte {
	if rd > 30 {
		rd = 0
	}
	if rn > 30 {
		rn = 0
	}
	if imm6 > 63 {
		imm6 = 0
	}
	immr := uint32(64-imm6) % 64
	imms := uint32(63 - imm6)
	insn := uint32(0xD3400000) | (immr&0x3F)<<16 | (imms&0x3F)<<10 |
		(uint32(rn)&0x1F)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedLslXdImm6Len = 4.
const EncodedLslXdImm6Len = 4

// EmitCmpXnImm12 emits arm64 "cmp Xn, #imm12" (SUBS XZR, Xn, #imm12 —
// flags only, unsigned 12-bit immediate).
//
// Encoding: 1111_0001_00_iiiiiiiiiiii_nnnnn_11111 = 0xF100001F base
//
// Use case: PJ10 arm64 ArrayHit inline bounds check (`cmp x2, #1`,
// mirror of amd64 `cmp edx, 1`).
func EmitCmpXnImm12(buf []byte, rn uint8, imm12 uint16) []byte {
	if rn > 30 {
		rn = 0
	}
	if imm12 > 0xFFF {
		imm12 = 0
	}
	insn := uint32(0xF100001F) | (uint32(imm12)&0xFFF)<<10 | (uint32(rn)&0x1F)<<5
	return appendArm64Insn(buf, insn)
}

// EncodedCmpXnImm12Len = 4.
const EncodedCmpXnImm12Len = 4

// EmitLdrWtFromXnDisp emits arm64 "ldr Wt, [Xn, #pimm12]" (32-bit
// zero-extending load with unsigned 12-bit scaled offset).
//
// Encoding: 1011_1001_01_iiiiiiiiiiii_nnnnn_ttttt = 0xB9400000 base
//   - size=10 (32-bit), V=0, opc=01 (LDR unsigned offset)
//   - imm12 is a **4-byte scaled offset** (byte offset = imm12 * 4),
//     range [0, 16380], step 4
//
// Use case: PJ10 arm64 ArrayHit inline — load table asize (word1 low
// 32 bits at +8; the upper 32 bits of Xt are zeroed so a full-width
// compare against the index is safe).
func EmitLdrWtFromXnDisp(buf []byte, rt, rn uint8, byteOff uint16) []byte {
	if rt > 30 {
		rt = 0
	}
	if rn > 30 {
		rn = 0
	}
	if byteOff%4 != 0 || byteOff > 16380 {
		byteOff = 0
	}
	imm12 := uint32(byteOff / 4)
	insn := uint32(0xB9400000) | (imm12&0xFFF)<<10 | (uint32(rn)&0x1F)<<5 | uint32(rt)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedLdrWtFromXnDispLen = 4.
const EncodedLdrWtFromXnDispLen = 4

// EmitFcvtzsXdDn emits arm64 "fcvtzs Xd, Dn" (double → signed 64-bit
// int, round toward zero; scalar variant).
//
// Encoding: 1001_1110_0111_1000_0000_00nn_nnnd_dddd = 0x9E780000 base
//   - sf=1, type=01 (double), rmode=11 (toward zero), opcode=000
//
// Use case: PJ10 arm64 ArrayHit inline key conversion (mirror of amd64
// cvttsd2si). Paired with EmitScvtfDdXn + FCMPE for the "key was an
// integer" round-trip check.
func EmitFcvtzsXdDn(buf []byte, xd, dn uint8) []byte {
	if xd > 30 {
		xd = 0
	}
	if dn > 31 {
		dn = 0
	}
	insn := uint32(0x9E780000) | (uint32(dn)&0x1F)<<5 | uint32(xd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedFcvtzsXdDnLen = 4.
const EncodedFcvtzsXdDnLen = 4

// EmitScvtfDdXn emits arm64 "scvtf Dd, Xn" (signed 64-bit int →
// double; scalar variant).
//
// Encoding: 1001_1110_0110_0010_0000_00nn_nnnd_dddd = 0x9E620000 base
//   - sf=1, type=01 (double), rmode=00, opcode=010
//
// Use case: the second half of the ArrayHit integer round-trip check
// (mirror of amd64 cvtsi2sd).
func EmitScvtfDdXn(buf []byte, dd, xn uint8) []byte {
	if dd > 31 {
		dd = 0
	}
	if xn > 30 {
		xn = 0
	}
	insn := uint32(0x9E620000) | (uint32(xn)&0x1F)<<5 | uint32(dd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedScvtfDdXnLen = 4.
const EncodedScvtfDdXnLen = 4

// EmitLdrbWtFromXnDisp emits arm64 "ldrb Wt, [Xn, #pimm12]" (32-bit
// zero-extended byte load with unsigned 12-bit byte offset).
//
// Encoding: 0011_1001_01_iiiiiiiiiiii_nnnnn_ttttt = 0x39400000 base
//   - size=00 (byte), V=0, opc=01 (LDRB unsigned offset)
//   - imm12 is a **byte offset** (no scale), range [0, 4095]
//
// Use case: PJ3 FORLOOP safepoint check arm64 side (mirror of amd64
// `cmp byte [r15+pfOff], 0`, 8 bytes; the arm64 side splits into ldrb + cbnz,
// same 8-byte length).
func EmitLdrbWtFromXnDisp(buf []byte, rt, rn uint8, byteOff uint16) []byte {
	if rt > 30 {
		rt = 0
	}
	if rn > 30 {
		rn = 0
	}
	if byteOff > 4095 {
		byteOff = 0
	}
	insn := uint32(0x39400000) | (uint32(byteOff)&0xFFF)<<10 | (uint32(rn)&0x1F)<<5 | uint32(rt)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedLdrbWtFromXnDispLen = 4.
const EncodedLdrbWtFromXnDispLen = 4

// EmitCbnzW emits arm64 "cbnz Wt, label" (compare-and-branch-if-nonzero,
// 32-bit register). imm19 is a word offset (target address = PC + imm19 * 4),
// range [-2^20, 2^20-1].
//
// Encoding: 0011_0101_iiiiiiiiiiiiiiiiiiii_ttttt = 0x35000000 base
//   - sf=0 (32-bit), op=0 (CBNZ)
//   - imm19 sign-extended word offset
//
// Use case: PJ3 FORLOOP safepoint check arm64 side —
// "ldrb W0, [x27+pfOff]; cbnz W0, after_loop" (mirror of amd64
// `cmp byte [r15+pfOff], 0; jne after_loop`, 14 bytes; arm64 side 8 bytes).
func EmitCbnzW(buf []byte, rt uint8, imm19 int32) []byte {
	if rt > 30 {
		rt = 0
	}
	insn := uint32(0x35000000) | (uint32(imm19)&0x7FFFF)<<5 | uint32(rt)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedCbnzWLen = 4.
const EncodedCbnzWLen = 4

// EmitCbzX emits arm64 `cbz Xt, label` (compare-and-branch-if-zero,
// 64-bit register). imm19 is word offset (target = PC + imm19 * 4).
// Encoding: 1011_0100_iiiiiiiiiiiiiiiiiiii_ttttt = 0xB4000000 base
// (sf=1 for 64-bit, op=0 for CBZ).
//
// Used by peroptranslator's arm64 status-check-and-bubble to skip a
// short "mov X0,1; ret" error tail when the shim returned 0.
func EmitCbzX(buf []byte, rt uint8, imm19 int32) []byte {
	if rt > 30 {
		rt = 0
	}
	insn := uint32(0xB4000000) | (uint32(imm19)&0x7FFFF)<<5 | uint32(rt)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedCbzXLen = 4.
const EncodedCbzXLen = 4

// patchCbnzImm19 patches the imm19 field (bit 5-23) inside the CBNZ/CBZ
// instruction word at buf[off..off+4]. The Rt field (bit 0-4) and the op/sf
// base (bit 24-31) are preserved; only imm19 is modified.
func patchCbnzImm19(buf []byte, off int, imm19 int32) {
	if off+4 > len(buf) {
		return
	}
	insn := uint32(buf[off]) | uint32(buf[off+1])<<8 |
		uint32(buf[off+2])<<16 | uint32(buf[off+3])<<24
	insn &= 0xFF00001F
	insn |= (uint32(imm19) & 0x7FFFF) << 5
	buf[off] = byte(insn)
	buf[off+1] = byte(insn >> 8)
	buf[off+2] = byte(insn >> 16)
	buf[off+3] = byte(insn >> 24)
}

// patchBCondImm19 patches the imm19 field (bit 5-23) inside the B.cond
// instruction word at buf[off..off+4]. The original word's cond field
// (bit 0-3) and 0x54 base (bit 24-31) are preserved; only the 19-bit imm19
// is modified.
//
// Use case: backfilling the forward B.cond placeholder deopt of PJ3 FORLOOP /
// PJ4 table IC templates (placeholder imm19=0 → actual
// (deoptStart - bCondOff) / 4).
func patchBCondImm19(buf []byte, off int, imm19 int32) {
	if off+4 > len(buf) {
		return
	}
	// read the original instruction word
	insn := uint32(buf[off]) | uint32(buf[off+1])<<8 |
		uint32(buf[off+2])<<16 | uint32(buf[off+3])<<24
	// clear the imm19 field (bit 5-23, 19 bits total = 0x7FFFF<<5 = 0x00FFFFE0)
	insn &= 0xFF00001F
	// write in the new imm19
	insn |= (uint32(imm19) & 0x7FFFF) << 5
	// write back to buf (LE)
	buf[off] = byte(insn)
	buf[off+1] = byte(insn >> 8)
	buf[off+2] = byte(insn >> 16)
	buf[off+3] = byte(insn >> 24)
}

// patchBImm26 patches the imm26 field (bit 0-25) inside the B (unconditional
// branch) instruction word at buf[off..off+4]. The original word's base
// (0x14000000, bit 26-31) is preserved; only the 26-bit imm26 is modified.
//
// Use case: mirror of amd64 `EmitJmpRel32 + PatchRel32` — backfilling the
// forward B placeholder deopt afterward, skipping the deopt block to the end
// of the segment (per EmitSelfNodeHitNoRetArm64's same fall-through form).
//
// imm26 = (target - bOff) / 4 (an arm64 B instruction uses a word offset,
// target = PC+imm26*4).
func patchBImm26(buf []byte, off int, imm26 int32) {
	if off+4 > len(buf) {
		return
	}
	insn := uint32(buf[off]) | uint32(buf[off+1])<<8 |
		uint32(buf[off+2])<<16 | uint32(buf[off+3])<<24
	// clear the imm26 field (bit 0-25, 26 bits total = 0x03FFFFFF)
	insn &= 0xFC000000
	insn |= uint32(imm26) & 0x03FFFFFF
	buf[off] = byte(insn)
	buf[off+1] = byte(insn >> 8)
	buf[off+2] = byte(insn >> 16)
	buf[off+3] = byte(insn >> 24)
}

// EmitBlrXn emits arm64 "blr Xn" (branch with link to register, indirect call,
// LR=X30 set to the return address). Mirror of amd64 `call regN`, 2 bytes
// (`FF D0+reg`).
//
// Encoding: 1101_0110_0011_1111_0000_00nn_nnn0_0000 = 0xD63F0000 base
//   - opc=0001 (BLR), Rn occupies bit[9:5]
//
// **Precondition**: Rn ∈ [0, 30] (X31 is XZR, BLR XZR is undefined behavior,
// sentinel falls back to Rn=0). The caller usually first EmitMovXdImm64 loads
// the target address into Xn, then BLR Xn.
//
// Use case: PJ5 helper call macro arm64 side (mirror of amd64 `mov rax, imm64
// + call rax`, 12 bytes; the arm64 side is `mov X16/17, imm64 + blr X16/17`,
// 20 bytes; X16/X17 are ARMv8 IP scratch registers, callee-saved not required).
func EmitBlrXn(buf []byte, rn uint8) []byte {
	if rn > 30 {
		rn = 0
	}
	insn := uint32(0xD63F0000) | (uint32(rn)&0x1F)<<5
	return appendArm64Insn(buf, insn)
}

// EncodedBlrXnLen = 4 (a single arm64 instruction).
const EncodedBlrXnLen = 4

// EmitHelperCallArm64 emits the arm64 helper call general macro:
//
//	mov X16, helperAddr imm64    ; 16 bytes (movz+movk×3)
//	blr X16                      ;  4 bytes
//	——— total length 20 bytes ———
//
// Mirror of amd64 `mov rax, imm64 + call rax`, 12 bytes (arm64 is 8 bytes
// longer because the MOV imm64 sequence is 16 vs amd64 mov rax imm64 10 +
// BLR vs CALL reg).
//
// Uses X16 (IP0) as the trampoline scratch register: the ARMv8 ABI reserves
// X16/X17 as intra-procedure-call scratch (per AAPCS), not required to be
// callee-preserved; the callee may overwrite them freely.
// After the call X16 need not be restored, and LR=X30 is set automatically
// by BLR.
//
// Use case: PJ5 CALL/TAILCALL real hookup arm64 side (mirror of amd64
// EmitHelperCall), calling a host helper (host.DoCall / host.GetTable /
// host.Arith etc.), where helperAddr is the helper function's physical
// address (resolved at compile time during jit Compile).
func EmitHelperCallArm64(buf []byte, helperAddr uint64) []byte {
	buf = EmitMovXdImm64(buf, 16, helperAddr) // mov x16, helperAddr
	buf = EmitBlrXn(buf, 16)                  // blr x16
	return buf
}

// EncodedHelperCallArm64Len = 20(MOV imm64 16 + BLR 4).
const EncodedHelperCallArm64Len = EncodedMovXdImm64Len + EncodedBlrXnLen
