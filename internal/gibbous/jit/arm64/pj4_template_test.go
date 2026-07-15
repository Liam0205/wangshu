//go:build wangshu_p4 && arm64 && linux

package arm64

import (
	"encoding/binary"
	"testing"
)

// pj4_template_test.go —— PJ8 arm64 PJ4-table IC six-path byte-level template unit tests.
//
// **Does not actually mmap+RX and run** (arm64 trampoline asm is left to a physical
// self-hosted runner); this test only validates byte-level layout.

// TestPJ8_EmitGetTableArrayHitArm64_Length verifies the byte length of the PJ4 IC
// ArrayHit arm64 template (168 bytes).
func TestPJ8_EmitGetTableArrayHitArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitGetTableArrayHitArm64(buf,
		1,          // aReg
		0,          // bReg
		7,          // stableShape
		3,          // stableIndex
		16,         // arenaBaseOff
		0xCAFEBABE, // deoptCode
	)
	const wantLen = 168
	if len(buf) != wantLen {
		t.Errorf("总长度 = %d, want %d", len(buf), wantLen)
	}
	if len(buf) != EncodedGetTableArrayHitArm64Len {
		t.Errorf("len = %d, want %d", len(buf), EncodedGetTableArrayHitArm64Len)
	}
}

// TestPJ8_EmitGetTableArrayHitArm64_StrictIsTableGuard verifies the byte sequence
// of the strict IsTable guard in the leading part of the template:
//   - [0-3]   LDR x0, [x26 + B*8]
//   - [4-7]   LSR x0, x0, #48
//   - [8-23]  MOV x1, 0xFFFC imm64
//   - [24-27] CMP x0, x1
//   - [28-31] B.NE deopt
func TestPJ8_EmitGetTableArrayHitArm64_StrictIsTableGuard(t *testing.T) {
	var buf []byte
	buf = EmitGetTableArrayHitArm64(buf, 1, 0, 7, 3, 16, 0xCAFEBABE)

	if len(buf) < 32 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [4-7] LSR x0, x0, #48 = 0xD340C000 + imm6=48<<16 + Rn=0<<5 + Rd=0
	// = 0xD340FC00 base | (48<<16=0x300000) = 0xD370FC00
	insn := binary.LittleEndian.Uint32(buf[4:8])
	wantLsr := uint32(0xD340FC00) | uint32(48)<<16
	if insn != wantLsr {
		t.Errorf("[4] LSR x0, x0, #48 = 0x%08x, want 0x%08x", insn, wantLsr)
	}

	// [24-27] CMP x0, x1 (0xEB00001F + Rm=1<<16)
	insn = binary.LittleEndian.Uint32(buf[24:28])
	wantCmp := uint32(0xEB00001F) | uint32(1)<<16
	if insn != wantCmp {
		t.Errorf("[24] CMP x0, x1 = 0x%08x, want 0x%08x", insn, wantCmp)
	}

	// [28-31] B.NE deopt (cond=NE=0x1)
	insn = binary.LittleEndian.Uint32(buf[28:32])
	if insn&0xF != uint32(CondNE) {
		t.Errorf("[28] B.NE cond = 0x%x, want 0x%x (NE)", insn&0xF, CondNE)
	}
	// imm19 = (deoptStart - 28) / 4. deoptStart = 148 → imm19 = (148-28)/4 = 30
	gotImm19 := (insn >> 5) & 0x7FFFF
	if gotImm19 != 30 {
		t.Errorf("[28] B.NE imm19 = %d, want 30 ((148-28)/4)", gotImm19)
	}
}

// TestPJ8_EmitGetTableArrayHitArm64_DeoptBlock verifies the tail of the deopt block.
//   - [148-163] MOV x0, deoptCode imm64
//   - [164-167] RET
func TestPJ8_EmitGetTableArrayHitArm64_DeoptBlock(t *testing.T) {
	const deoptCode uint64 = 0xDEAD_BEEF_CAFE_BABE
	var buf []byte
	buf = EmitGetTableArrayHitArm64(buf, 1, 0, 7, 3, 16, deoptCode)

	if len(buf) < 168 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [148-163] MOV x0, deoptCode imm64 (movz+3*movk)
	// MOVZ x0, deoptCode[15:0] = 0xBABE
	insn := binary.LittleEndian.Uint32(buf[148:152])
	imm0 := (insn >> 5) & 0xFFFF
	if imm0 != 0xBABE {
		t.Errorf("[148] MOVZ x0 imm[15:0] = 0x%04x, want 0xBABE", imm0)
	}

	// [164-167] RET
	insn = binary.LittleEndian.Uint32(buf[164:168])
	if insn != 0xd65f03c0 {
		t.Errorf("[164] RET = 0x%08x, want 0xd65f03c0", insn)
	}
}

// TestPJ8_EmitGetTableNodeHitArm64_Length verifies the byte length of the PJ4 IC
// NodeHit arm64 template (196 bytes).
func TestPJ8_EmitGetTableNodeHitArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitGetTableNodeHitArm64(buf,
		1,                  // aReg
		0,                  // bReg
		7,                  // stableShape
		3,                  // stableIndex
		0xFFFD000000000042, // stableKey (NaN-box short str)
		16,                 // arenaBaseOff
		0xCAFEBABE,         // deoptCode
	)
	const wantLen = 196
	if len(buf) != wantLen {
		t.Errorf("总长度 = %d, want %d", len(buf), wantLen)
	}
	if len(buf) != EncodedGetTableNodeHitArm64Len {
		t.Errorf("len = %d, want %d", len(buf), EncodedGetTableNodeHitArm64Len)
	}
}

// TestPJ8_EmitGetTableNodeHitArm64_NodeRefAndKey verifies the key byte layout
// of the NodeHit branch:
//   - [100-103] LDR x0, [x2, #24]            (nodeRef word3, **not** arrayRef word2 offset 16)
//   - [108-111] ADD x2, x14, x1               (new SIB base for node)
//   - [116-131] MOV x3, stableKey imm64       (key compare section, absent in ArrayHit)
//   - [136-139] B.NE deopt                    (NodeKey mismatch)
func TestPJ8_EmitGetTableNodeHitArm64_NodeRefAndKey(t *testing.T) {
	const stableKey uint64 = 0xFFFD_DEAD_BEEF_BABE
	var buf []byte
	buf = EmitGetTableNodeHitArm64(buf, 1, 0, 7, 3, stableKey, 16, 0xCAFEBABE)

	if len(buf) < 196 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [100-103] LDR x0, [x2, #24] = base 0xF9400000 | imm12=3<<10 | Rn=2<<5 | Rt=0
	insn := binary.LittleEndian.Uint32(buf[100:104])
	wantLdr := uint32(0xF9400000) | uint32(3)<<10 | uint32(2)<<5
	if insn != wantLdr {
		t.Errorf("[100] LDR x0, [x2, #24] = 0x%08x, want 0x%08x", insn, wantLdr)
	}

	// [108-111] ADD x2, x14, x1 = 0x8B000000 | Rm=1<<16 | Rn=14<<5 | Rd=2
	insn = binary.LittleEndian.Uint32(buf[108:112])
	wantAdd := uint32(0x8B000000) | uint32(1)<<16 | uint32(14)<<5 | uint32(2)
	if insn != wantAdd {
		t.Errorf("[108] ADD x2, x14, x1 = 0x%08x, want 0x%08x", insn, wantAdd)
	}

	// [116-119] MOVZ x3, stableKey[15:0] = 0xBABE
	insn = binary.LittleEndian.Uint32(buf[116:120])
	if (insn & 0xFFE00000) != 0xD2800000 {
		t.Errorf("[116] MOVZ base wrong")
	}
	imm0 := (insn >> 5) & 0xFFFF
	if imm0 != 0xBABE {
		t.Errorf("[116] MOVZ x3 imm[15:0] = 0x%04x, want 0xBABE", imm0)
	}
	if insn&0x1F != 3 {
		t.Errorf("[116] MOVZ Rd = %d, want 3 (x3)", insn&0x1F)
	}

	// [136-139] B.NE deopt (cond=NE=0x1)
	insn = binary.LittleEndian.Uint32(buf[136:140])
	if (insn & 0xFF000000) != 0x54000000 {
		t.Errorf("[136] B.cond base wrong: 0x%08x", insn)
	}
	if insn&0xF != uint32(CondNE) {
		t.Errorf("[136] B.NE cond = 0x%x, want 0x%x (NE)", insn&0xF, CondNE)
	}
	// imm19 = (deoptStart - 136) / 4 = (176-136)/4 = 10
	gotImm19 := (insn >> 5) & 0x7FFFF
	if gotImm19 != 10 {
		t.Errorf("[136] B.NE imm19 = %d, want 10 ((176-136)/4)", gotImm19)
	}
}

// TestPJ8_EmitGetTableNodeHitArm64_DeoptBlock verifies the tail of the deopt block (176-195).
func TestPJ8_EmitGetTableNodeHitArm64_DeoptBlock(t *testing.T) {
	const deoptCode uint64 = 0xDEAD_BEEF_CAFE_BABE
	var buf []byte
	buf = EmitGetTableNodeHitArm64(buf, 1, 0, 7, 3, 0xFFFD000000000042, 16, deoptCode)

	if len(buf) < 196 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [176-179] MOVZ x0, deoptCode[15:0] = 0xBABE
	insn := binary.LittleEndian.Uint32(buf[176:180])
	imm0 := (insn >> 5) & 0xFFFF
	if imm0 != 0xBABE {
		t.Errorf("[176] MOVZ x0 imm[15:0] = 0x%04x, want 0xBABE", imm0)
	}

	// [192-195] RET
	insn = binary.LittleEndian.Uint32(buf[192:196])
	if insn != 0xd65f03c0 {
		t.Errorf("[192] RET = 0x%08x, want 0xd65f03c0", insn)
	}
}

// TestPJ8_EmitSetTableArrayHitArm64_Length verifies the byte length of the PJ4
// SETTABLE ArrayHit arm64 template (144 bytes).
func TestPJ8_EmitSetTableArrayHitArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitSetTableArrayHitArm64(buf,
		1,          // aReg (table)
		2,          // cReg (value)
		7,          // stableShape
		3,          // stableIndex
		16,         // arenaBaseOff
		0xCAFEBABE, // deoptCode
	)
	const wantLen = 144
	if len(buf) != wantLen {
		t.Errorf("总长度 = %d, want %d", len(buf), wantLen)
	}
	if len(buf) != EncodedSetTableArrayHitArm64Len {
		t.Errorf("len = %d, want %d", len(buf), EncodedSetTableArrayHitArm64Len)
	}
}

// TestPJ8_EmitSetTableArrayHitArm64_StoreOp verifies the key store section of
// SETTABLE ArrayHit (value load + reverse store):
//   - [100-103] LDR x0, [x2, #16]            (arrayRef word2)
//   - [112-115] LDR x3, [x26 + C*8]          (load R(C) value)
//   - [116-119] STR x3, [x2, #stableIndex*8] (reverse store)
//   - [120-123] RET                          (setter has no R(A) write)
func TestPJ8_EmitSetTableArrayHitArm64_StoreOp(t *testing.T) {
	var buf []byte
	buf = EmitSetTableArrayHitArm64(buf, 1, 2, 7, 3, 16, 0xCAFEBABE)

	if len(buf) < 144 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [100-103] LDR x0, [x2, #16] = base 0xF9400000 | imm12=2<<10 | Rn=2<<5 | Rt=0
	insn := binary.LittleEndian.Uint32(buf[100:104])
	wantLdr := uint32(0xF9400000) | uint32(2)<<10 | uint32(2)<<5
	if insn != wantLdr {
		t.Errorf("[100] LDR x0, [x2, #16] = 0x%08x, want 0x%08x", insn, wantLdr)
	}

	// [112-115] LDR x3, [x26 + 16] (C=2, byteOff=16, imm12=2)
	insn = binary.LittleEndian.Uint32(buf[112:116])
	wantLdrC := uint32(0xF9400000) | uint32(2)<<10 | uint32(26)<<5 | uint32(3)
	if insn != wantLdrC {
		t.Errorf("[112] LDR x3, [x26 + C*8] = 0x%08x, want 0x%08x", insn, wantLdrC)
	}

	// [116-119] STR x3, [x2, #stableIndex*8] = STR base 0xF9000000
	// (Rt=3, Rn=2, byteOff=24, imm12=3)
	insn = binary.LittleEndian.Uint32(buf[116:120])
	wantStr := uint32(0xF9000000) | uint32(3)<<10 | uint32(2)<<5 | uint32(3)
	if insn != wantStr {
		t.Errorf("[116] STR x3, [x2, #stableIndex*8] = 0x%08x, want 0x%08x",
			insn, wantStr)
	}

	// [120-123] RET
	insn = binary.LittleEndian.Uint32(buf[120:124])
	if insn != 0xd65f03c0 {
		t.Errorf("[120] RET (no R(A) write) = 0x%08x, want 0xd65f03c0", insn)
	}
}

// TestPJ8_EmitSetTableArrayHitArm64_DeoptBlock verifies the deopt block (124-143).
func TestPJ8_EmitSetTableArrayHitArm64_DeoptBlock(t *testing.T) {
	const deoptCode uint64 = 0xDEAD_BEEF_CAFE_BABE
	var buf []byte
	buf = EmitSetTableArrayHitArm64(buf, 1, 2, 7, 3, 16, deoptCode)

	if len(buf) < 144 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [124-127] MOVZ x0, deoptCode[15:0] = 0xBABE
	insn := binary.LittleEndian.Uint32(buf[124:128])
	imm0 := (insn >> 5) & 0xFFFF
	if imm0 != 0xBABE {
		t.Errorf("[124] MOVZ x0 imm[15:0] = 0x%04x, want 0xBABE", imm0)
	}

	// [140-143] RET
	insn = binary.LittleEndian.Uint32(buf[140:144])
	if insn != 0xd65f03c0 {
		t.Errorf("[140] RET = 0x%08x, want 0xd65f03c0", insn)
	}
}

// TestPJ8_EmitSetTableNodeHitArm64_Length verifies the byte length of the PJ4
// SETTABLE NodeHit arm64 template (172 bytes).
func TestPJ8_EmitSetTableNodeHitArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitSetTableNodeHitArm64(buf,
		1,                  // aReg (table)
		2,                  // cReg (value)
		7,                  // stableShape
		3,                  // stableIndex
		0xFFFD000000000042, // stableKey
		16,                 // arenaBaseOff
		0xCAFEBABE,         // deoptCode
	)
	const wantLen = 172
	if len(buf) != wantLen {
		t.Errorf("总长度 = %d, want %d", len(buf), wantLen)
	}
	if len(buf) != EncodedSetTableNodeHitArm64Len {
		t.Errorf("len = %d, want %d", len(buf), EncodedSetTableNodeHitArm64Len)
	}
}

// TestPJ8_EmitSetTableNodeHitArm64_StoreOp verifies the key store section of
// SETTABLE NodeHit:
//   - [140-143] LDR x3, [x26 + C*8]              (load R(C) value)
//   - [144-147] STR x3, [x2, #stableIndex*24+8]  (reverse store NodeVal)
//   - [148-151] RET                              (setter has no R(A) write)
func TestPJ8_EmitSetTableNodeHitArm64_StoreOp(t *testing.T) {
	var buf []byte
	buf = EmitSetTableNodeHitArm64(buf, 1, 2, 7, 3, 0xFFFD000000000042,
		16, 0xCAFEBABE)

	if len(buf) < 172 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [140-143] LDR x3, [x26 + 16] (C=2, byteOff=16, imm12=2)
	insn := binary.LittleEndian.Uint32(buf[140:144])
	wantLdr := uint32(0xF9400000) | uint32(2)<<10 | uint32(26)<<5 | uint32(3)
	if insn != wantLdr {
		t.Errorf("[140] LDR x3, [x26 + C*8] = 0x%08x, want 0x%08x", insn, wantLdr)
	}

	// [144-147] STR x3, [x2, #stableIndex*24+8]
	// = STR base 0xF9000000 | imm12=(3*24+8)/8=10 | Rn=2<<5 | Rt=3
	insn = binary.LittleEndian.Uint32(buf[144:148])
	wantStr := uint32(0xF9000000) | uint32(10)<<10 | uint32(2)<<5 | uint32(3)
	if insn != wantStr {
		t.Errorf("[144] STR x3, [x2, #stableIndex*24+8] = 0x%08x, want 0x%08x",
			insn, wantStr)
	}

	// [148-151] RET (setter has no R(A) write)
	insn = binary.LittleEndian.Uint32(buf[148:152])
	if insn != 0xd65f03c0 {
		t.Errorf("[148] RET = 0x%08x, want 0xd65f03c0", insn)
	}
}

// TestPJ8_EmitSetTableNodeHitArm64_DeoptBlock verifies the deopt block (152-171).
func TestPJ8_EmitSetTableNodeHitArm64_DeoptBlock(t *testing.T) {
	const deoptCode uint64 = 0xDEAD_BEEF_CAFE_BABE
	var buf []byte
	buf = EmitSetTableNodeHitArm64(buf, 1, 2, 7, 3, 0xFFFD000000000042,
		16, deoptCode)

	if len(buf) < 172 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [152-155] MOVZ x0, deoptCode[15:0] = 0xBABE
	insn := binary.LittleEndian.Uint32(buf[152:156])
	imm0 := (insn >> 5) & 0xFFFF
	if imm0 != 0xBABE {
		t.Errorf("[152] MOVZ x0 imm[15:0] = 0x%04x, want 0xBABE", imm0)
	}

	// [168-171] RET
	insn = binary.LittleEndian.Uint32(buf[168:172])
	if insn != 0xd65f03c0 {
		t.Errorf("[168] RET = 0x%08x, want 0xd65f03c0", insn)
	}
}

// TestPJ8_EmitSelfArrayHitArm64_Length verifies the byte length of the PJ4 SELF
// ArrayHit arm64 template (172 bytes).
func TestPJ8_EmitSelfArrayHitArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitSelfArrayHitArm64(buf,
		1,          // aReg
		3,          // bReg
		7,          // stableShape
		3,          // stableIndex
		16,         // arenaBaseOff
		0xCAFEBABE, // deoptCode
	)
	const wantLen = 172
	if len(buf) != wantLen {
		t.Errorf("总长度 = %d, want %d", len(buf), wantLen)
	}
	if len(buf) != EncodedSelfArrayHitArm64Len {
		t.Errorf("len = %d, want %d", len(buf), EncodedSelfArrayHitArm64Len)
	}
}

// TestPJ8_EmitSelfArrayHitArm64_RAPlus1Store verifies the SELF characteristic:
// R(A+1) is written before the IsTable guard, ensuring that when the deopt path
// falls through to host.GetTable, R(A+1) is already set (same step as the
// byte-equal P1 SELF case).
//   - [0-3]   LDR x0, [x26 + B*8]            (load R(B) obj)
//   - [4-7]   STR x0, [x26 + (A+1)*8]        (**SELF step one**: R(A+1)=obj)
//   - [8-11]  LSR x0, x0, #48                (subsequent IsTable guard)
func TestPJ8_EmitSelfArrayHitArm64_RAPlus1Store(t *testing.T) {
	const aReg, bReg uint8 = 1, 3
	var buf []byte
	buf = EmitSelfArrayHitArm64(buf, aReg, bReg, 7, 3, 16, 0xCAFEBABE)

	if len(buf) < 16 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [0-3] LDR x0, [x26 + B*8] (B=3, byteOff=24, imm12=3)
	insn := binary.LittleEndian.Uint32(buf[0:4])
	wantLdrB := uint32(0xF9400000) | uint32(3)<<10 | uint32(26)<<5
	if insn != wantLdrB {
		t.Errorf("[0] LDR x0, [x26 + B*8] = 0x%08x, want 0x%08x", insn, wantLdrB)
	}

	// [4-7] STR x0, [x26 + (A+1)*8] = STR base 0xF9000000
	// ((A+1)*8=2*8=16, imm12=2)
	insn = binary.LittleEndian.Uint32(buf[4:8])
	wantStr := uint32(0xF9000000) | uint32(2)<<10 | uint32(26)<<5
	if insn != wantStr {
		t.Errorf("[4] STR x0, [x26 + (A+1)*8] = 0x%08x, want 0x%08x", insn, wantStr)
	}

	// [8-11] LSR x0, x0, #48
	insn = binary.LittleEndian.Uint32(buf[8:12])
	wantLsr := uint32(0xD340FC00) | uint32(48)<<16
	if insn != wantLsr {
		t.Errorf("[8] LSR x0, x0, #48 = 0x%08x, want 0x%08x", insn, wantLsr)
	}
}

// TestPJ8_EmitSelfArrayHitArm64_DeoptBlock verifies the deopt block at [152-171].
func TestPJ8_EmitSelfArrayHitArm64_DeoptBlock(t *testing.T) {
	const deoptCode uint64 = 0xDEAD_BEEF_CAFE_BABE
	var buf []byte
	buf = EmitSelfArrayHitArm64(buf, 1, 3, 7, 3, 16, deoptCode)

	if len(buf) < 172 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [152-155] MOVZ x0, deoptCode[15:0] = 0xBABE
	insn := binary.LittleEndian.Uint32(buf[152:156])
	imm0 := (insn >> 5) & 0xFFFF
	if imm0 != 0xBABE {
		t.Errorf("[152] MOVZ x0 imm[15:0] = 0x%04x, want 0xBABE", imm0)
	}

	// [168-171] RET
	insn = binary.LittleEndian.Uint32(buf[168:172])
	if insn != 0xd65f03c0 {
		t.Errorf("[168] RET = 0x%08x, want 0xd65f03c0", insn)
	}
}

// TestPJ8_EmitSelfNodeHitArm64_Length verifies the byte length of the PJ4 SELF
// NodeHit arm64 template (200 bytes).
func TestPJ8_EmitSelfNodeHitArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitSelfNodeHitArm64(buf,
		1,                  // aReg
		3,                  // bReg
		7,                  // stableShape
		2,                  // stableIndex
		0xFFF80000FEEDBEEF, // stableKey (string method name NaN-box)
		16,                 // arenaBaseOff
		0xCAFEBABE,         // deoptCode
	)
	const wantLen = 200
	if len(buf) != wantLen {
		t.Errorf("总长度 = %d, want %d", len(buf), wantLen)
	}
	if len(buf) != EncodedSelfNodeHitArm64Len {
		t.Errorf("len = %d, want %d", len(buf), EncodedSelfNodeHitArm64Len)
	}
}

// TestPJ8_EmitSelfNodeHitArm64_RAPlus1Store verifies the SELF characteristic:
// R(A+1) is written before the IsTable guard.
//   - [0-3]   LDR x0, [x26 + B*8]      (load R(B) obj)
//   - [4-7]   STR x0, [x26 + (A+1)*8]  (**SELF step one**: R(A+1)=obj)
//   - [8-11]  LSR x0, x0, #48          (subsequent IsTable shift)
func TestPJ8_EmitSelfNodeHitArm64_RAPlus1Store(t *testing.T) {
	const aReg, bReg uint8 = 1, 3
	var buf []byte
	buf = EmitSelfNodeHitArm64(buf, aReg, bReg, 7, 2, 0xCAFEFEED, 16, 0xCAFEBABE)

	if len(buf) < 16 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	insn := binary.LittleEndian.Uint32(buf[0:4])
	wantLdrB := uint32(0xF9400000) | uint32(3)<<10 | uint32(26)<<5
	if insn != wantLdrB {
		t.Errorf("[0] LDR x0, [x26 + B*8] = 0x%08x, want 0x%08x", insn, wantLdrB)
	}

	insn = binary.LittleEndian.Uint32(buf[4:8])
	wantStr := uint32(0xF9000000) | uint32(2)<<10 | uint32(26)<<5
	if insn != wantStr {
		t.Errorf("[4] STR x0, [x26 + (A+1)*8] = 0x%08x, want 0x%08x", insn, wantStr)
	}

	insn = binary.LittleEndian.Uint32(buf[8:12])
	wantLsr := uint32(0xD340FC00) | uint32(48)<<16
	if insn != wantLsr {
		t.Errorf("[8] LSR x0, x0, #48 = 0x%08x, want 0x%08x", insn, wantLsr)
	}
}

// TestPJ8_EmitSelfNodeHitArm64_StableKeyBurnedIn verifies that stableKey is
// burned into the NodeKey compare section [120-135] via movz+movk×3.
func TestPJ8_EmitSelfNodeHitArm64_StableKeyBurnedIn(t *testing.T) {
	const stableKey uint64 = 0xDEAD_BEEF_CAFE_FACE
	var buf []byte
	buf = EmitSelfNodeHitArm64(buf, 1, 3, 7, 2, stableKey, 16, 0xCAFEBABE)

	if len(buf) < 136 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	expectedImm16 := [4]uint16{
		uint16(stableKey & 0xFFFF),         // 0xFACE
		uint16((stableKey >> 16) & 0xFFFF), // 0xCAFE
		uint16((stableKey >> 32) & 0xFFFF), // 0xBEEF
		uint16((stableKey >> 48) & 0xFFFF), // 0xDEAD
	}
	for i, exp := range expectedImm16 {
		off := 120 + i*4
		insn := binary.LittleEndian.Uint32(buf[off : off+4])
		got := uint16((insn >> 5) & 0xFFFF)
		if got != exp {
			t.Errorf("stableKey movz/movk[%d] imm16 = 0x%04x, want 0x%04x", i, got, exp)
		}
		if (insn & 0x1F) != 3 {
			t.Errorf("stableKey movz/movk[%d] Rd = %d, want 3 (x3)", i, insn&0x1F)
		}
	}
}

// TestPJ8_EmitSelfNodeHitArm64_DeoptBlock verifies the deopt block at [180-199].
func TestPJ8_EmitSelfNodeHitArm64_DeoptBlock(t *testing.T) {
	const deoptCode uint64 = 0xDEAD_BEEF_CAFE_BABE
	var buf []byte
	buf = EmitSelfNodeHitArm64(buf, 1, 3, 7, 2, 0xCAFEFEED, 16, deoptCode)

	if len(buf) < 200 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	insn := binary.LittleEndian.Uint32(buf[180:184])
	imm0 := (insn >> 5) & 0xFFFF
	if imm0 != 0xBABE {
		t.Errorf("[180] MOVZ x0 imm[15:0] = 0x%04x, want 0xBABE", imm0)
	}

	insn = binary.LittleEndian.Uint32(buf[196:200])
	if insn != 0xd65f03c0 {
		t.Errorf("[196] RET = 0x%08x, want 0xd65f03c0", insn)
	}
}

// TestPJ8_EmitGetTableArrayHitArm64_StableShapeBurnedIn verifies that stableShape
// is burned into [76-91] via MOVZ+MOVK×3 (the word5 gen check section of the IC
// ArrayHit template).
// Steps: LDR R(B) 4 + LSR 48 4 + MOV TagTable 16 + CMP 4 + B.NE 4 +
//
//	re-load LDR 4 + MOV payloadMask 16 + AND 4 + MOV reg 4 +
//	LDR x14 4 + ADD SIB 4 + LDR word5 4 + LSR 32 4 = 76 bytes
//
// → MOV stableShape imm64 at offset 76-91.
func TestPJ8_EmitGetTableArrayHitArm64_StableShapeBurnedIn(t *testing.T) {
	const stableShape uint32 = 0xCAFE_BEEF
	var buf []byte
	buf = EmitGetTableArrayHitArm64(buf, 1, 0, stableShape, 3, 16, 0xDEADBEEF)

	if len(buf) < 92 {
		t.Fatalf("buf too short: %d", len(buf))
	}
	// MOV x3, stableShape imm64 section [76-91]
	expectedImm16 := [4]uint16{
		uint16(stableShape & 0xFFFF),
		uint16((stableShape >> 16) & 0xFFFF),
		0, // stableShape is uint32, high bits are 0
		0,
	}
	for i, exp := range expectedImm16 {
		off := 76 + i*4
		insn := binary.LittleEndian.Uint32(buf[off : off+4])
		got := uint16((insn >> 5) & 0xFFFF)
		if got != exp {
			t.Errorf("stableShape movz/movk[%d]@%d imm16 = 0x%04x, want 0x%04x",
				i, off, got, exp)
		}
	}
}

// TestPJ8_EmitGetTableNodeHitArm64_StableKeyBurnedIn verifies that stableKey is
// burned into [116-131] via MOVZ+MOVK×3 (the NodeKey compare section of the
// NodeHit template).
// Steps: GetTable ArrayHit prefix 76 bytes (word5+LSR) + MOV stableShape 16 +
//
//	CMP 4 + B.NE 4 + LDR nodeRef 4 + MOV reg 4 + ADD SIB 4 + LDR NodeKey 4
//
// = 116 bytes → MOV stableKey at offset 116-131.
func TestPJ8_EmitGetTableNodeHitArm64_StableKeyBurnedIn(t *testing.T) {
	const stableKey uint64 = 0xFFF8_0000_DEAD_BEEF
	var buf []byte
	buf = EmitGetTableNodeHitArm64(buf, 1, 0, 7, 2, stableKey, 16, 0xCAFEBABE)

	if len(buf) < 132 {
		t.Fatalf("buf too short: %d", len(buf))
	}
	expectedImm16 := [4]uint16{
		uint16(stableKey & 0xFFFF),
		uint16((stableKey >> 16) & 0xFFFF),
		uint16((stableKey >> 32) & 0xFFFF),
		uint16((stableKey >> 48) & 0xFFFF),
	}
	for i, exp := range expectedImm16 {
		off := 116 + i*4
		insn := binary.LittleEndian.Uint32(buf[off : off+4])
		got := uint16((insn >> 5) & 0xFFFF)
		if got != exp {
			t.Errorf("stableKey movz/movk[%d]@%d imm16 = 0x%04x, want 0x%04x",
				i, off, got, exp)
		}
		// Rd field must be 3 (x3)
		if (insn & 0x1F) != 3 {
			t.Errorf("stableKey movz/movk[%d]@%d Rd = %d, want 3 (x3)",
				i, off, insn&0x1F)
		}
	}
}

// TestPJ8_EmitSetTableNodeHitArm64_StableKeyBurnedIn verifies the stableKey burn-in
// location of the SETTABLE NodeHit template. SETTABLE NodeHit byte layout: guard 32
// (LDR+LSR+MOV+CMP+B.NE) + re-load 36 + word5+LSR 8 + MOV stableShape 16 + CMP 4
// + B.NE 4 = 100 → nodeRef section 12 (LDR+MOV+ADD) = 112 → LDR NodeKey 4 →
// MOV stableKey [116-131].
func TestPJ8_EmitSetTableNodeHitArm64_StableKeyBurnedIn(t *testing.T) {
	const stableKey uint64 = 0xFFF8_0000_CAFE_BABE
	var buf []byte
	buf = EmitSetTableNodeHitArm64(buf, 1, 2, 7, 2, stableKey, 16, 0xDEADBEEF)

	if len(buf) < 132 {
		t.Fatalf("buf too short: %d", len(buf))
	}
	expectedImm16 := [4]uint16{
		uint16(stableKey & 0xFFFF),
		uint16((stableKey >> 16) & 0xFFFF),
		uint16((stableKey >> 32) & 0xFFFF),
		uint16((stableKey >> 48) & 0xFFFF),
	}
	for i, exp := range expectedImm16 {
		off := 116 + i*4
		insn := binary.LittleEndian.Uint32(buf[off : off+4])
		got := uint16((insn >> 5) & 0xFFFF)
		if got != exp {
			t.Errorf("stableKey movz/movk[%d]@%d imm16 = 0x%04x, want 0x%04x",
				i, off, got, exp)
		}
	}
}

// TestPJ8_EmitSetTableArrayHitArm64_StableShapeBurnedIn verifies the stableShape
// burn-in location of the SETTABLE ArrayHit template. Byte layout same as
// GetTableArrayHit (no SELF STR / no nodeRef branch): guard 32 + re-load 36 +
// word5+LSR 8 = 76 → MOV stableShape at [76-91].
func TestPJ8_EmitSetTableArrayHitArm64_StableShapeBurnedIn(t *testing.T) {
	const stableShape uint32 = 0xBEEF_DEAD
	var buf []byte
	buf = EmitSetTableArrayHitArm64(buf, 1, 2, stableShape, 3, 16, 0xCAFEBABE)

	if len(buf) < 92 {
		t.Fatalf("buf too short: %d", len(buf))
	}
	expectedImm16 := [4]uint16{
		uint16(stableShape & 0xFFFF),
		uint16((stableShape >> 16) & 0xFFFF),
		0, // uint32 high bits
		0,
	}
	for i, exp := range expectedImm16 {
		off := 76 + i*4
		insn := binary.LittleEndian.Uint32(buf[off : off+4])
		got := uint16((insn >> 5) & 0xFFFF)
		if got != exp {
			t.Errorf("stableShape movz/movk[%d]@%d imm16 = 0x%04x, want 0x%04x",
				i, off, got, exp)
		}
	}
}

// TestPJ8_EmitSelfArrayHitArm64_StableShapeBurnedIn verifies the stableShape
// burn-in location of the SELF ArrayHit template. SELF has an extra step 2
// STR R(A+1) of 4 bytes:
//   - guard 36 (SELF: LDR 4 + STR 4 + LSR 4 + MOV 16 + CMP 4 + B.NE 4)
//   - re-load 36
//   - word5+LSR 8
//   - MOV stableShape starts at [80-95]
func TestPJ8_EmitSelfArrayHitArm64_StableShapeBurnedIn(t *testing.T) {
	const stableShape uint32 = 0xBEEF_DEAD
	var buf []byte
	buf = EmitSelfArrayHitArm64(buf, 1, 3, stableShape, 2, 16, 0xCAFEBABE)

	if len(buf) < 96 {
		t.Fatalf("buf too short: %d", len(buf))
	}
	expectedImm16 := [4]uint16{
		uint16(stableShape & 0xFFFF),
		uint16((stableShape >> 16) & 0xFFFF),
		0,
		0,
	}
	for i, exp := range expectedImm16 {
		off := 80 + i*4
		insn := binary.LittleEndian.Uint32(buf[off : off+4])
		got := uint16((insn >> 5) & 0xFFFF)
		if got != exp {
			t.Errorf("stableShape movz/movk[%d]@%d imm16 = 0x%04x, want 0x%04x",
				i, off, got, exp)
		}
	}
}

// TestPJ8_EmitSetTableNodeHitArm64_NodeRefAndKey verifies the key byte layout of
// the SET NodeHit branch (mirrors GetTableNodeHit, differs in the setter section):
//   - [100-103] LDR x0, [x2, #24]            (nodeRef word3)
//   - [108-111] ADD x2, x14, x1               (new SIB base for node)
//   - [116-131] MOV x3, stableKey imm64       (key compare section)
//   - [136-139] B.NE deopt                    (NodeKey mismatch)
func TestPJ8_EmitSetTableNodeHitArm64_NodeRefAndKey(t *testing.T) {
	const stableKey uint64 = 0xFFFD_BEEF_CAFE_FACE
	var buf []byte
	buf = EmitSetTableNodeHitArm64(buf, 1, 2, 7, 3, stableKey, 16, 0xCAFEBABE)

	if len(buf) < 172 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [100-103] LDR x0, [x2, #24] = base 0xF9400000 | imm12=3<<10 | Rn=2<<5 | Rt=0
	insn := binary.LittleEndian.Uint32(buf[100:104])
	wantLdr := uint32(0xF9400000) | uint32(3)<<10 | uint32(2)<<5
	if insn != wantLdr {
		t.Errorf("[100] LDR x0, [x2, #24] = 0x%08x, want 0x%08x", insn, wantLdr)
	}

	// [108-111] ADD x2, x14, x1
	insn = binary.LittleEndian.Uint32(buf[108:112])
	wantAdd := uint32(0x8B000000) | uint32(1)<<16 | uint32(14)<<5 | uint32(2)
	if insn != wantAdd {
		t.Errorf("[108] ADD x2, x14, x1 = 0x%08x, want 0x%08x", insn, wantAdd)
	}

	// [116-119] MOVZ x3, stableKey[15:0] = 0xFACE
	insn = binary.LittleEndian.Uint32(buf[116:120])
	if (insn & 0xFFE00000) != 0xD2800000 {
		t.Errorf("[116] MOVZ base wrong: 0x%08x", insn)
	}
	imm0 := (insn >> 5) & 0xFFFF
	if imm0 != 0xFACE {
		t.Errorf("[116] MOVZ x3 imm[15:0] = 0x%04x, want 0xFACE", imm0)
	}
	if insn&0x1F != 3 {
		t.Errorf("[116] MOVZ Rd = %d, want 3 (x3)", insn&0x1F)
	}

	// [136-139] B.NE deopt (cond=NE=0x1)
	insn = binary.LittleEndian.Uint32(buf[136:140])
	if insn&0xF != uint32(CondNE) {
		t.Errorf("[136] B.NE cond = 0x%x, want 0x%x (NE)", insn&0xF, CondNE)
	}
}

// TestPJ8_EmitSelfNodeHitArm64_NodeRefAndKey verifies the key byte layout of the
// SELF NodeHit branch. SELF has an extra step 2 STR R(A+1) of 4 bytes, so all
// subsequent offsets shift by +4:
//   - [104-107] LDR x0, [x2, #24]            (nodeRef word3)
//   - [112-115] ADD x2, x14, x1               (new SIB base for node)
//   - [120-135] MOV x3, stableKey imm64       (key compare section)
//   - [140-143] B.NE deopt
func TestPJ8_EmitSelfNodeHitArm64_NodeRefAndKey(t *testing.T) {
	const stableKey uint64 = 0xFFFD_DEAD_CAFE_FEED
	var buf []byte
	buf = EmitSelfNodeHitArm64(buf, 1, 3, 7, 2, stableKey, 16, 0xCAFEBABE)

	if len(buf) < 200 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [104-107] LDR x0, [x2, #24]
	insn := binary.LittleEndian.Uint32(buf[104:108])
	wantLdr := uint32(0xF9400000) | uint32(3)<<10 | uint32(2)<<5
	if insn != wantLdr {
		t.Errorf("[104] LDR x0, [x2, #24] = 0x%08x, want 0x%08x", insn, wantLdr)
	}

	// [112-115] ADD x2, x14, x1
	insn = binary.LittleEndian.Uint32(buf[112:116])
	wantAdd := uint32(0x8B000000) | uint32(1)<<16 | uint32(14)<<5 | uint32(2)
	if insn != wantAdd {
		t.Errorf("[112] ADD x2, x14, x1 = 0x%08x, want 0x%08x", insn, wantAdd)
	}

	// [120-123] MOVZ x3, stableKey[15:0] = 0xFEED
	insn = binary.LittleEndian.Uint32(buf[120:124])
	if (insn & 0xFFE00000) != 0xD2800000 {
		t.Errorf("[120] MOVZ base wrong: 0x%08x", insn)
	}
	imm0 := (insn >> 5) & 0xFFFF
	if imm0 != 0xFEED {
		t.Errorf("[120] MOVZ x3 imm[15:0] = 0x%04x, want 0xFEED", imm0)
	}
	if insn&0x1F != 3 {
		t.Errorf("[120] MOVZ Rd = %d, want 3 (x3)", insn&0x1F)
	}

	// [140-143] B.NE deopt
	insn = binary.LittleEndian.Uint32(buf[140:144])
	if insn&0xF != uint32(CondNE) {
		t.Errorf("[140] B.NE cond = 0x%x, want 0x%x (NE)", insn&0xF, CondNE)
	}
}

// TestPJ8_EmitGetTableNodeHitArm64_StableShapeBurnedIn verifies the stableShape
// burn-in location of the GETTABLE NodeHit template (NodeHit shares the same
// prefix form as ArrayHit, only branching later to nodeRef instead of arrayRef):
//   - guard 32 + re-load 36 + word5+LSR 8 = 76
//   - MOV stableShape at [76-91]
func TestPJ8_EmitGetTableNodeHitArm64_StableShapeBurnedIn(t *testing.T) {
	const stableShape uint32 = 0xBEEF_DEAD
	var buf []byte
	buf = EmitGetTableNodeHitArm64(buf, 1, 0, stableShape, 3, 0xFFFD_0000_0000_0042, 16, 0xCAFEBABE)

	if len(buf) < 92 {
		t.Fatalf("buf too short: %d", len(buf))
	}
	expectedImm16 := [4]uint16{
		uint16(stableShape & 0xFFFF),
		uint16((stableShape >> 16) & 0xFFFF),
		0,
		0,
	}
	for i, exp := range expectedImm16 {
		off := 76 + i*4
		insn := binary.LittleEndian.Uint32(buf[off : off+4])
		got := uint16((insn >> 5) & 0xFFFF)
		if got != exp {
			t.Errorf("stableShape movz/movk[%d]@%d imm16 = 0x%04x, want 0x%04x",
				i, off, got, exp)
		}
	}
}

// TestPJ8_EmitSetTableNodeHitArm64_StableShapeBurnedIn verifies the stableShape
// burn-in location of the SETTABLE NodeHit template (layout same as GETTABLE
// NodeHit, the leading 76 bytes are identical).
func TestPJ8_EmitSetTableNodeHitArm64_StableShapeBurnedIn(t *testing.T) {
	const stableShape uint32 = 0xCAFE_FEED
	var buf []byte
	buf = EmitSetTableNodeHitArm64(buf, 1, 2, stableShape, 3, 0xFFFD_0000_0000_0042, 16, 0xCAFEBABE)

	if len(buf) < 92 {
		t.Fatalf("buf too short: %d", len(buf))
	}
	expectedImm16 := [4]uint16{
		uint16(stableShape & 0xFFFF),
		uint16((stableShape >> 16) & 0xFFFF),
		0,
		0,
	}
	for i, exp := range expectedImm16 {
		off := 76 + i*4
		insn := binary.LittleEndian.Uint32(buf[off : off+4])
		got := uint16((insn >> 5) & 0xFFFF)
		if got != exp {
			t.Errorf("stableShape movz/movk[%d]@%d imm16 = 0x%04x, want 0x%04x",
				i, off, got, exp)
		}
	}
}

// TestPJ8_EmitSelfNodeHitArm64_StableShapeBurnedIn verifies the stableShape
// burn-in location of the SELF NodeHit template. SELF has an extra step 2
// STR R(A+1) of 4 bytes, shifting everything down:
//   - guard 36 + re-load 36 + word5+LSR 8 = 80
//   - MOV stableShape at [80-95]
func TestPJ8_EmitSelfNodeHitArm64_StableShapeBurnedIn(t *testing.T) {
	const stableShape uint32 = 0xBEEF_DEAD
	var buf []byte
	buf = EmitSelfNodeHitArm64(buf, 1, 3, stableShape, 2, 0xFFFD_0000_0000_0042, 16, 0xCAFEBABE)

	if len(buf) < 96 {
		t.Fatalf("buf too short: %d", len(buf))
	}
	expectedImm16 := [4]uint16{
		uint16(stableShape & 0xFFFF),
		uint16((stableShape >> 16) & 0xFFFF),
		0,
		0,
	}
	for i, exp := range expectedImm16 {
		off := 80 + i*4
		insn := binary.LittleEndian.Uint32(buf[off : off+4])
		got := uint16((insn >> 5) & 0xFFFF)
		if got != exp {
			t.Errorf("stableShape movz/movk[%d]@%d imm16 = 0x%04x, want 0x%04x",
				i, off, got, exp)
		}
	}
}

// TestPJ8_EmitGetTableArrayHitArm64_StableIndexBurnedIn verifies that stableIndex
// is burned into the array[idx] load section via the LDR imm12 field (byteOff/8
// scaled offset).
//
// GetTableArrayHit: guard 32 + re-load 36 + word5+LSR 8 + stableShape section
//
//	16 + CMP+B.NE 8 + arrayRef LDR+MOV+ADD 12 = 112 → LDR array[stableIndex]
//	at [112-115].
//
// LDR Xt, [Xn, #disp] encoding: base 0xF9400000 | imm12<<10 | Rn<<5 | Rt;
// imm12 = byteOff/8 = stableIndex (since array word size = 8 bytes).
func TestPJ8_EmitGetTableArrayHitArm64_StableIndexBurnedIn(t *testing.T) {
	const stableIndex uint32 = 42
	var buf []byte
	buf = EmitGetTableArrayHitArm64(buf, 1, 0, 7, stableIndex, 16, 0xCAFEBABE)

	if len(buf) < 116 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [112-115] LDR x0, [x2, #stableIndex*8] (array[stableIndex])
	insn := binary.LittleEndian.Uint32(buf[112:116])
	// imm12 = stableIndex (since array word size = 8 bytes → scaled offset)
	wantLdr := uint32(0xF9400000) | uint32(stableIndex)<<10 | uint32(2)<<5
	if insn != wantLdr {
		t.Errorf("[112] LDR x0, [x2, #stableIndex*8] = 0x%08x, want 0x%08x", insn, wantLdr)
	}

	// read back and verify the imm12 field
	gotImm12 := (insn >> 10) & 0xFFF
	if gotImm12 != stableIndex {
		t.Errorf("[112] LDR imm12 = %d, want %d (stableIndex)", gotImm12, stableIndex)
	}
}

// TestPJ8_EmitSetTableArrayHitArm64_StableIndexBurnedIn verifies the imm12 field
// in the SETTABLE ArrayHit reverse store section STR x3, [x2, #stableIndex*8].
//
// SET ArrayHit: guard 32 + re-load 36 + word5+LSR+stableShape+CMP+B.NE 32
//   - arrayRef LDR+MOV+ADD 12 + LDR R(C) 4 = 116 → STR at [116-119].
func TestPJ8_EmitSetTableArrayHitArm64_StableIndexBurnedIn(t *testing.T) {
	const stableIndex uint32 = 35
	var buf []byte
	buf = EmitSetTableArrayHitArm64(buf, 1, 2, 7, stableIndex, 16, 0xCAFEBABE)

	if len(buf) < 120 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [116-119] STR x3, [x2, #stableIndex*8]
	insn := binary.LittleEndian.Uint32(buf[116:120])
	// STR base 0xF9000000 | imm12<<10 | Rn<<5 | Rt
	wantStr := uint32(0xF9000000) | uint32(stableIndex)<<10 | uint32(2)<<5 | uint32(3)
	if insn != wantStr {
		t.Errorf("[116] STR x3, [x2, #stableIndex*8] = 0x%08x, want 0x%08x", insn, wantStr)
	}

	gotImm12 := (insn >> 10) & 0xFFF
	if gotImm12 != stableIndex {
		t.Errorf("[116] STR imm12 = %d, want %d (stableIndex)", gotImm12, stableIndex)
	}
}

// TestPJ8_EmitSpecArgLoadKArm64_Length verifies the length of the PJ5 SELF spec
// args K-load arm64 template (movz/movk × 4 + str = 5 × 4 = 20 bytes).
func TestPJ8_EmitSpecArgLoadKArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitSpecArgLoadKArm64(buf, 5, 0xDEADBEEF12345678)
	if len(buf) != 20 {
		t.Errorf("EmitSpecArgLoadKArm64 长度 = %d, want 20", len(buf))
	}
}

// TestPJ8_EmitSpecArgLoadRegArm64_Length verifies the length of the PJ5 SELF spec
// args reg-load arm64 template (LDR + STR = 2 × 4 = 8 bytes).
func TestPJ8_EmitSpecArgLoadRegArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitSpecArgLoadRegArm64(buf, 5, 3)
	if len(buf) != 8 {
		t.Errorf("EmitSpecArgLoadRegArm64 长度 = %d, want 8", len(buf))
	}
}

// TestPJ8_EmitFrameInlineCIDepthIncArm64_Length verifies the length of the arm64
// ciDepth++ byte-level inline template (LDR×2 + ADD + STR = 16 bytes, vs amd64 = 10).
// Follows §9.20 Option B Spike 1.
func TestPJ8_EmitFrameInlineCIDepthIncArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineCIDepthIncArm64(buf, 56)
	if len(buf) != EncodedFrameInlineCIDepthIncDecArm64Len {
		t.Errorf("EmitFrameInlineCIDepthIncArm64 长度 = %d, want %d",
			len(buf), EncodedFrameInlineCIDepthIncDecArm64Len)
	}
}

// TestPJ8_EmitFrameInlineCIDepthDecArm64_Length verifies the length of the arm64
// ciDepth-- byte-level inline template (LDR×2 + SUB + STR = 16 bytes).
func TestPJ8_EmitFrameInlineCIDepthDecArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineCIDepthDecArm64(buf, 56)
	if len(buf) != EncodedFrameInlineCIDepthIncDecArm64Len {
		t.Errorf("EmitFrameInlineCIDepthDecArm64 长度 = %d, want %d",
			len(buf), EncodedFrameInlineCIDepthIncDecArm64Len)
	}
}

// TestPJ8_EmitFrameInlineCIDepthDecArm64_Encoding verifies the arm64 SUB Xd Xn imm12
// byte-level encoding (little-endian arm64 instruction) — SUB x17, x17, #1 = 0xD1000631.
func TestPJ8_EmitFrameInlineCIDepthDecArm64_Encoding(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineCIDepthDecArm64(buf, 56)
	// SUB instruction at offset 8 (LDR×2 of 4 bytes each)
	subInsn := binary.LittleEndian.Uint32(buf[8:12])
	const wantSub = uint32(0xD1000631) // SUB x17, x17, #1
	if subInsn != wantSub {
		t.Errorf("SUB x17, x17, #1 = 0x%08X, want 0x%08X", subInsn, wantSub)
	}
}

// TestPJ8_EmitFrameInlineLoadCISlotAddrArm64_Length verifies the length of the
// arm64 template that loads the address of the depth-th frame in the CI section
// (LDR×4 + MovImm64 16 + MUL + ADD = 40 bytes, vs amd64 = 30). Follows §9.20
// Option B Spike 1.
func TestPJ8_EmitFrameInlineLoadCISlotAddrArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineLoadCISlotAddrArm64(buf, 56, 64)
	if len(buf) != EncodedFrameInlineLoadCISlotAddrArm64Len {
		t.Errorf("EmitFrameInlineLoadCISlotAddrArm64 长度 = %d, want %d",
			len(buf), EncodedFrameInlineLoadCISlotAddrArm64Len)
	}
}

// TestPJ8_EmitFrameInlineWriteCIWordArm64_Length verifies the length of the arm64
// CI frame word write template (16 bytes mov + 4 bytes str = 20 bytes, vs amd64 = 14).
func TestPJ8_EmitFrameInlineWriteCIWordArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineWriteCIWordArm64(buf, 0, 0xDEADBEEF)
	if len(buf) != EncodedFrameInlineWriteCIWordArm64Len {
		t.Errorf("EmitFrameInlineWriteCIWordArm64 长度 = %d, want %d",
			len(buf), EncodedFrameInlineWriteCIWordArm64Len)
	}
}

// TestPJ8_EmitFrameInlinePopVoid0ArgSkeletonArm64_CIDepthDecPlusRet verifies the
// arm64 Spike 1 popCallInfo skeleton byte-level = CIDepthDec 16 byte + movz w0 #0
// 4 byte + ret 4 byte = 24 byte (mirrors amd64 _CIDepthDecPlusRet; after F3-#3b
// fixed the missing-ret bug it changed from "pure alias" to "prefix alias +
// explicit ret tail").
func TestPJ8_EmitFrameInlinePopVoid0ArgSkeletonArm64_CIDepthDecPlusRet(t *testing.T) {
	var bufA []byte
	bufA = EmitFrameInlinePopVoid0ArgSkeletonArm64(bufA, 56)
	if len(bufA) != EncodedFrameInlinePopVoid0ArgSkeletonArm64Len {
		t.Errorf("PopVoid0ArgSkeletonArm64 长度 = %d, want %d",
			len(bufA), EncodedFrameInlinePopVoid0ArgSkeletonArm64Len)
	}
	// first 16 byte = CIDepthDec
	var bufB []byte
	bufB = EmitFrameInlineCIDepthDecArm64(bufB, 56)
	if len(bufB) != EncodedFrameInlineCIDepthIncDecArm64Len {
		t.Fatalf("CIDepthDec 长度 = %d, want %d(测试 fixture 失效)",
			len(bufB), EncodedFrameInlineCIDepthIncDecArm64Len)
	}
	for i := range bufB {
		if bufA[i] != bufB[i] {
			t.Errorf("前缀字节[%d] 差异:Pop=0x%02X, CIDepthDec=0x%02X",
				i, bufA[i], bufB[i])
		}
	}
	// last 4 byte (offset [20..24)) = ret (0xD65F03C0 LE = c0 03 5f d6)
	if bufA[20] != 0xC0 || bufA[21] != 0x03 || bufA[22] != 0x5F || bufA[23] != 0xD6 {
		t.Errorf("PopVoid0Arg 末 4 byte = 0x%02X%02X%02X%02X, want 0xC0035FD6(ret)",
			bufA[20], bufA[21], bufA[22], bufA[23])
	}
	// middle 4 byte (offset [16..20)) = movz w0, #0 (mirrors §9.20.9 commit-5l xor eax,eax)
	// arm64 movz w0, #0 encoding = 0x52800000 LE = 00 00 80 52
	if bufA[16] != 0x00 || bufA[17] != 0x00 || bufA[18] != 0x80 || bufA[19] != 0x52 {
		t.Errorf("PopVoid0Arg 中 4 byte = 0x%02X%02X%02X%02X, want 0x0000_8052(movz w0,#0)",
			bufA[16], bufA[17], bufA[18], bufA[19])
	}
}

// TestPJ8_EmitFrameInlineLoadClosureGCRefArm64_Length verifies the length of the
// arm64 closure GCRef NaN-box parse template (LDR 4 + MovImm64 16 + AND 4 = 24
// bytes, vs amd64 = 20).
func TestPJ8_EmitFrameInlineLoadClosureGCRefArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineLoadClosureGCRefArm64(buf, 5)
	if len(buf) != EncodedFrameInlineLoadClosureGCRefArm64Len {
		t.Errorf("EmitFrameInlineLoadClosureGCRefArm64 长度 = %d, want %d",
			len(buf), EncodedFrameInlineLoadClosureGCRefArm64Len)
	}
}

// TestPJ8_EmitFrameInlineLoadClosureGCRefArm64_AndEncoding verifies the key
// encoding of AND x16, x16, x17 (0x8A110210).
func TestPJ8_EmitFrameInlineLoadClosureGCRefArm64_AndEncoding(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineLoadClosureGCRefArm64(buf, 0)
	// AND x16, x16, x17 at offset 20 (LDR 4 + MovXdImm64 16 = 20)
	andInsn := binary.LittleEndian.Uint32(buf[20:24])
	// AND x16, x16, x17 = 0x8A000000 + (17<<16) + (16<<5) + 16
	// = 0x8A000000 + 0x110000 + 0x200 + 0x10 = 0x8A110210
	const wantAnd = uint32(0x8A110210)
	if andInsn != wantAnd {
		t.Errorf("AND x16, x16, x17 = 0x%08X, want 0x%08X", andInsn, wantAnd)
	}
}

// TestPJ8_EmitFrameInlineWriteCIWordFromXArm64_Length verifies the length of the
// arm64 word-write-from-Xt template (4 bytes).
func TestPJ8_EmitFrameInlineWriteCIWordFromXArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineWriteCIWordFromXArm64(buf, 3, 16)
	if len(buf) != EncodedFrameInlineWriteCIWordFromXArm64Len {
		t.Errorf("EmitFrameInlineWriteCIWordFromXArm64 长度 = %d, want %d",
			len(buf), EncodedFrameInlineWriteCIWordFromXArm64Len)
	}
}

// TestPJ8_EmitFrameInlineBuildVoid0ArgSkeletonArm64_Length verifies the total
// length of the arm64 Spike 1 enterLuaFrame byte-level inline skeleton v2
// (40 + 60 + 24 + 4 + 20 + 16 = 164 bytes, vs amd64 v2 = 120).
func TestPJ8_EmitFrameInlineBuildVoid0ArgSkeletonArm64_Length(t *testing.T) {
	var buf []byte
	words := FrameInlineCISlotWordsArm64{
		Word0: 0x0000000100000010,
		Word1: 0x0000000000000020,
		Word2: 0x0000000000000005,
		Word3: 0, // v2 ignores
		Word4: 0,
	}
	buf = EmitFrameInlineBuildVoid0ArgSkeletonArm64(buf, 56, 64, 5 /*callARecv*/, words)
	if len(buf) != EncodedFrameInlineBuildVoid0ArgSkeletonArm64Len {
		t.Errorf("EmitFrameInlineBuildVoid0ArgSkeletonArm64 长度 = %d, want %d",
			len(buf), EncodedFrameInlineBuildVoid0ArgSkeletonArm64Len)
	}
}

// TestPJ8_EmitFrameInlineWriteCIWordArm64_Encoding verifies the STR pimm12 encoding
// for each word_idx.
func TestPJ8_EmitFrameInlineWriteCIWordArm64_Encoding(t *testing.T) {
	for _, wordIdx := range []uint8{0, 1, 2, 3, 4} {
		var buf []byte
		buf = EmitFrameInlineWriteCIWordArm64(buf, wordIdx, 0xCAFEBABE12345678)

		// STR x16, [x0 + wordIdx*8] at offset 16 (MovXdImm64 takes 16 bytes)
		strInsn := binary.LittleEndian.Uint32(buf[16:20])
		// STR Xt, [Xn, #pimm12]:0xF9000000 base + (pimm12<<10) + (Xn<<5) + Xt
		// pimm12 = byteOff / 8 = wordIdx
		wantStr := uint32(0xF9000000) | uint32(wordIdx)<<10 | uint32(0)<<5 | uint32(16)
		if strInsn != wantStr {
			t.Errorf("word_idx=%d: STR = 0x%08X, want 0x%08X", wordIdx, strInsn, wantStr)
		}
	}
}

// TestPJ8_EmitFrameInlineLoadCISlotAddrArm64_Encoding verifies the byte-level
// encoding of the key MUL/ADD instructions.
func TestPJ8_EmitFrameInlineLoadCISlotAddrArm64_Encoding(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineLoadCISlotAddrArm64(buf, 56, 64)

	// MUL x17, x17, x9 at offset 32 (LDR×4 16 bytes + MovImm64 16 bytes)
	mulInsn := binary.LittleEndian.Uint32(buf[32:36])
	// MUL x17, x17, x9 = 0x9B007C00 + (9<<16) + (17<<5) + 17
	// = 0x9B007C00 + 0x90000 + 0x220 + 0x11 = 0x9B097E31
	const wantMul = uint32(0x9B097E31)
	if mulInsn != wantMul {
		t.Errorf("MUL x17, x17, x9 = 0x%08X, want 0x%08X", mulInsn, wantMul)
	}

	// ADD x0, x16, x17 at offset 36
	addInsn := binary.LittleEndian.Uint32(buf[36:40])
	// ADD x0, x16, x17 = 0x8B000000 + (17<<16) + (16<<5) + 0
	// = 0x8B000000 + 0x110000 + 0x200 + 0 = 0x8B110200
	const wantAdd = uint32(0x8B110200)
	if addInsn != wantAdd {
		t.Errorf("ADD x0, x16, x17 = 0x%08X, want 0x%08X", addInsn, wantAdd)
	}
}

// TestPJ8_EmitSelfNodeHitNoRetArm64_Length verifies that the NoRet variant has the
// same byte length as NodeHit (200 bytes, RET 4B replaced with B 4B).
func TestPJ8_EmitSelfNodeHitNoRetArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitSelfNodeHitNoRetArm64(buf,
		1, 3, 7, 2, 0xFFF80000FEEDBEEF, 16, 0xCAFEBABE)
	if len(buf) != 200 {
		t.Errorf("总长度 = %d, want 200", len(buf))
	}
	if len(buf) != EncodedSelfNodeHitNoRetArm64Len {
		t.Errorf("len = %d, want %d", len(buf), EncodedSelfNodeHitNoRetArm64Len)
	}
}

// TestPJ8_EmitSelfNodeHitNoRetArm64_SuccessFallThrough verifies the key byte
// difference for "success path does not RET":
//   - offset 176 should be B (unconditional jump), not RET
//   - B's imm26 should skip the deopt block to the end of the section (target = len(buf))
//
// vs EmitSelfNodeHitArm64:
//   - at the same offset 176 it is RET (0xd65f03c0)
//   - the NoRet version is a B instruction (base 0x14000000, bit 26-31 = 0b000101)
//
// Section layout (follows pj4_template.go::EmitSelfNodeHitNoRetArm64 comment):
//
//	[  0..172) SELF + guard + nodeRef + key + nil check + STR R(A) whole section
//	[172..176) STR R(A) = x0 (method function)
//	[176..180) success tail: NodeHit RET / NoRet B (forward to end of section)
//	[180..200) deopt block (MOV x0=deoptCode 16 + RET 4)
func TestPJ8_EmitSelfNodeHitNoRetArm64_SuccessFallThrough(t *testing.T) {
	var buf []byte
	buf = EmitSelfNodeHitNoRetArm64(buf,
		1, 3, 7, 2, 0xCAFEFEED, 16, 0xCAFEBABE)
	if len(buf) != 200 {
		t.Fatalf("len = %d, want 200", len(buf))
	}

	const bOff = 176
	insn := binary.LittleEndian.Uint32(buf[bOff : bOff+4])

	// verify base = 0x14000000 (B opcode, bit 26-31 = 0b000101)
	if (insn & 0xFC000000) != 0x14000000 {
		t.Errorf("[176] insn base = 0x%08X, want 0x14000000 (B unconditional)", insn&0xFC000000)
	}

	// verify imm26 = (target - bOff) / 4, target = 200 = len(buf)
	imm26 := int32(insn & 0x03FFFFFF)
	wantImm26 := int32(len(buf)-bOff) / 4 // (200-176)/4 = 6
	if imm26 != wantImm26 {
		t.Errorf("[176] B imm26 = %d, want %d (跳到段尾)", imm26, wantImm26)
	}

	// verify the deopt block is still at [180..200): offset 196 should be RET
	insn = binary.LittleEndian.Uint32(buf[196:200])
	if insn != 0xd65f03c0 {
		t.Errorf("[196] deopt RET = 0x%08X, want 0xd65f03c0", insn)
	}
}

// TestPJ8_EmitSelfNodeHitNoRetArm64_ByteEqualNodeHit verifies that NoRet and
// NodeHit are byte-identical over offset [0..176) (SELF section + IsTable + word5
// + nodeRef + key compare + nil check + store R(A) all the same), differing only
// in the success-path tail instruction at [176..180) (RET vs B); the deopt block
// [180..200) is byte-literal and patch-independent so it is equal too.
//
// This is the dual face of the prove-the-path-under-test discipline: it proves
// both that the two implementations share the same byte layout before fall-through,
// and that the difference is strictly confined to the "ret vs B + skip deopt" spot.
func TestPJ8_EmitSelfNodeHitNoRetArm64_ByteEqualNodeHit(t *testing.T) {
	var bufHit, bufNoRet []byte
	bufHit = EmitSelfNodeHitArm64(bufHit, 1, 3, 7, 2, 0xCAFEFEED, 16, 0xCAFEBABE)
	bufNoRet = EmitSelfNodeHitNoRetArm64(bufNoRet, 1, 3, 7, 2, 0xCAFEFEED, 16, 0xCAFEBABE)
	if len(bufHit) != len(bufNoRet) {
		t.Fatalf("len(NodeHit) = %d, len(NoRet) = %d (应等长 200)", len(bufHit), len(bufNoRet))
	}
	// section head [0..176) byte-identical (success path expanded + STR R(A) done at offset 176)
	const prefixEnd = 176
	for i := 0; i < prefixEnd; i++ {
		if bufHit[i] != bufNoRet[i] {
			t.Errorf("差异 [%d]: NodeHit=0x%02X, NoRet=0x%02X (应在 [0..176) 字节相等)", i, bufHit[i], bufNoRet[i])
		}
	}
	// deopt block [180..200) byte-literal equal (no patch dependency)
	for i := 180; i < 200; i++ {
		if bufHit[i] != bufNoRet[i] {
			t.Errorf("差异 [%d]: NodeHit=0x%02X, NoRet=0x%02X (deopt block 应字面相等)", i, bufHit[i], bufNoRet[i])
		}
	}
}

// TestPJ8_EmitFrameInlineExitHelperRequestArm64_Length verifies the byte length of
// the arm64 ExitHelperRequest section (36 bytes, vs amd64 24 bytes; arm64 has 12
// extra bytes due to fixed-length encoding + no register reuse).
func TestPJ8_EmitFrameInlineExitHelperRequestArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineExitHelperRequestArm64(buf,
		20, // exitReasonOff (jitContext.exitReasonCode field offset, uint32)
		64, // exitArg0Off   (jitContext.exitArg0 field offset, uint64)
		1,  // helperCode = HelperRunCallee
	)
	const wantLen = 36
	if len(buf) != wantLen {
		t.Errorf("总长度 = %d, want %d", len(buf), wantLen)
	}
	if len(buf) != EncodedFrameInlineExitHelperRequestArm64Len {
		t.Errorf("len = %d, want %d", len(buf), EncodedFrameInlineExitHelperRequestArm64Len)
	}
}

// TestPJ8_EmitFrameInlineExitHelperRequestArm64_Encoding verifies the key byte structure:
//   - [ 0-15] movz/movk x16, helperCode imm64 (4 × 32-bit)
//   - [16-19] str x16, [x27 + exitArg0Off] (64-bit STR)
//   - [20-23] movz w16, #3 (32-bit MOVZ, ExitInlineHelper)
//   - [24-27] str w16, [x27 + exitReasonOff] (32-bit STR)
//   - [28-31] movz w0, #3 (32-bit MOVZ, set return value)
//   - [32-35] ret (0xd65f03c0)
func TestPJ8_EmitFrameInlineExitHelperRequestArm64_Encoding(t *testing.T) {
	const helperCode uint64 = 0xCAFEBABEDEADBEEF
	const exitReasonOff = int32(20)
	const exitArg0Off = int32(64)

	var buf []byte
	buf = EmitFrameInlineExitHelperRequestArm64(buf,
		exitReasonOff, exitArg0Off, helperCode)
	if len(buf) != 36 {
		t.Fatalf("len = %d, want 36", len(buf))
	}

	// [ 0-15] movz/movk x16, helperCode imm64
	// verify 4 imm16 segments are burned in: one MOVZ/MOVK every 4 bytes in [0..16)
	for i := 0; i < 4; i++ {
		insn := binary.LittleEndian.Uint32(buf[i*4 : i*4+4])
		// imm16 field is in bit 5-20
		got := uint16((insn >> 5) & 0xFFFF)
		exp := uint16(helperCode >> (i * 16))
		if got != exp {
			t.Errorf("movz/movk[%d] imm16 = 0x%04X, want 0x%04X", i, got, exp)
		}
		// Rd in bit 0-4 should be x16 (16)
		if (insn & 0x1F) != 16 {
			t.Errorf("movz/movk[%d] Rd = %d, want 16 (x16)", i, insn&0x1F)
		}
	}

	// [16-19] str x16, [x27 + exitArg0Off] = 0xF9000000 + (imm12<<10) + (27<<5) + 16
	//   imm12 = exitArg0Off/8 = 8
	insn := binary.LittleEndian.Uint32(buf[16:20])
	wantStrX := uint32(0xF9000000) | uint32(8)<<10 | uint32(27)<<5 | uint32(16)
	if insn != wantStrX {
		t.Errorf("[16] STR x16, [x27 + %d] = 0x%08X, want 0x%08X", exitArg0Off, insn, wantStrX)
	}

	// [20-23] movz w16, #3 = 0x52800000 + (3<<5) + 16
	insn = binary.LittleEndian.Uint32(buf[20:24])
	wantMovzW16 := uint32(0x52800000) | uint32(3)<<5 | uint32(16)
	if insn != wantMovzW16 {
		t.Errorf("[20] MOVZ w16, #3 = 0x%08X, want 0x%08X", insn, wantMovzW16)
	}

	// [24-27] str w16, [x27 + exitReasonOff] = 0xB9000000 + (imm12<<10) + (27<<5) + 16
	//   imm12 = exitReasonOff/4 = 5
	insn = binary.LittleEndian.Uint32(buf[24:28])
	wantStrW := uint32(0xB9000000) | uint32(5)<<10 | uint32(27)<<5 | uint32(16)
	if insn != wantStrW {
		t.Errorf("[24] STR w16, [x27 + %d] = 0x%08X, want 0x%08X", exitReasonOff, insn, wantStrW)
	}

	// [28-31] movz w0, #3 = 0x52800000 + (3<<5) + 0
	insn = binary.LittleEndian.Uint32(buf[28:32])
	wantMovzW0 := uint32(0x52800000) | uint32(3)<<5 | uint32(0)
	if insn != wantMovzW0 {
		t.Errorf("[28] MOVZ w0, #3 = 0x%08X, want 0x%08X", insn, wantMovzW0)
	}

	// [32-35] ret
	insn = binary.LittleEndian.Uint32(buf[32:36])
	if insn != 0xd65f03c0 {
		t.Errorf("[32] RET = 0x%08X, want 0xd65f03c0", insn)
	}
}
