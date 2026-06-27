//go:build wangshu_p4 && arm64 && linux

package arm64

import (
	"encoding/binary"
	"testing"
)

// pj4_template_test.go —— PJ8 arm64 PJ4 表 IC 六路径字节级模板单测。
//
// **不真 mmap+RX 跑**(arm64 trampoline asm 留物理 self-hosted runner);
// 本测试纯字节级布局验证。

// TestPJ8_EmitGetTableArrayHitArm64_Length 验 PJ4 IC ArrayHit arm64 模板
// 字节长度(168 字节)。
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

// TestPJ8_EmitGetTableArrayHitArm64_StrictIsTableGuard 验严密 IsTable guard
// 字节序列在模板前段:
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

	// [24-27] CMP x0, x1(0xEB00001F + Rm=1<<16)
	insn = binary.LittleEndian.Uint32(buf[24:28])
	wantCmp := uint32(0xEB00001F) | uint32(1)<<16
	if insn != wantCmp {
		t.Errorf("[24] CMP x0, x1 = 0x%08x, want 0x%08x", insn, wantCmp)
	}

	// [28-31] B.NE deopt(cond=NE=0x1)
	insn = binary.LittleEndian.Uint32(buf[28:32])
	if insn&0xF != uint32(CondNE) {
		t.Errorf("[28] B.NE cond = 0x%x, want 0x%x (NE)", insn&0xF, CondNE)
	}
	// imm19 = (deoptStart - 28) / 4。deoptStart = 148 → imm19 = (148-28)/4 = 30
	gotImm19 := (insn >> 5) & 0x7FFFF
	if gotImm19 != 30 {
		t.Errorf("[28] B.NE imm19 = %d, want 30 ((148-28)/4)", gotImm19)
	}
}

// TestPJ8_EmitGetTableArrayHitArm64_DeoptBlock 验 deopt block 末尾。
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

// TestPJ8_EmitGetTableNodeHitArm64_Length 验 PJ4 IC NodeHit arm64 模板
// 字节长度(196 字节)。
func TestPJ8_EmitGetTableNodeHitArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitGetTableNodeHitArm64(buf,
		1,                  // aReg
		0,                  // bReg
		7,                  // stableShape
		3,                  // stableIndex
		0xFFFD000000000042, // stableKey(NaN-box short str)
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

// TestPJ8_EmitGetTableNodeHitArm64_NodeRefAndKey 验 NodeHit 分流的关键
// 字节布局:
//   - [100-103] LDR x0, [x2, #24]            (nodeRef word3,**不是** arrayRef word2 offset 16)
//   - [108-111] ADD x2, x14, x1               (新 SIB base for node)
//   - [116-131] MOV x3, stableKey imm64       (key 比对段,ArrayHit 没有)
//   - [136-139] B.NE deopt                    (NodeKey 失配)
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

// TestPJ8_EmitGetTableNodeHitArm64_DeoptBlock 验 deopt block 末尾(176-195)。
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

// TestPJ8_EmitSetTableArrayHitArm64_Length 验 PJ4 SETTABLE ArrayHit arm64
// 模板字节长度(144 字节)。
func TestPJ8_EmitSetTableArrayHitArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitSetTableArrayHitArm64(buf,
		1,          // aReg(table)
		2,          // cReg(value)
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

// TestPJ8_EmitSetTableArrayHitArm64_StoreOp 验 SETTABLE ArrayHit 关键
// store 段(value load + 反向 store):
//   - [100-103] LDR x0, [x2, #16]            (arrayRef word2)
//   - [112-115] LDR x3, [x26 + C*8]          (load R(C) value)
//   - [116-119] STR x3, [x2, #stableIndex*8] (反向 store)
//   - [120-123] RET                          (setter 无 R(A) 写)
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

// TestPJ8_EmitSetTableArrayHitArm64_DeoptBlock 验 deopt block(124-143)。
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

// TestPJ8_EmitSetTableNodeHitArm64_Length 验 PJ4 SETTABLE NodeHit arm64
// 模板字节长度(172 字节)。
func TestPJ8_EmitSetTableNodeHitArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitSetTableNodeHitArm64(buf,
		1,                  // aReg(table)
		2,                  // cReg(value)
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

// TestPJ8_EmitSetTableNodeHitArm64_StoreOp 验 SETTABLE NodeHit 关键
// store 段:
//   - [140-143] LDR x3, [x26 + C*8]              (load R(C) value)
//   - [144-147] STR x3, [x2, #stableIndex*24+8]  (反向 store NodeVal)
//   - [148-151] RET                              (setter 无 R(A) 写)
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

	// [148-151] RET (setter 无 R(A) 写)
	insn = binary.LittleEndian.Uint32(buf[148:152])
	if insn != 0xd65f03c0 {
		t.Errorf("[148] RET = 0x%08x, want 0xd65f03c0", insn)
	}
}

// TestPJ8_EmitSetTableNodeHitArm64_DeoptBlock 验 deopt block(152-171)。
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

// TestPJ8_EmitSelfArrayHitArm64_Length 验 PJ4 SELF ArrayHit arm64 模板
// 字节长度(172 字节)。
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

// TestPJ8_EmitSelfArrayHitArm64_RAPlus1Store 验 SELF 特征:R(A+1) 先于
// IsTable guard 写,确保 deopt 路径走 host.GetTable 时 R(A+1) 已设
// (byte-equal P1 SELF case 同款步骤)。
//   - [0-3]   LDR x0, [x26 + B*8]            (load R(B) obj)
//   - [4-7]   STR x0, [x26 + (A+1)*8]        (**SELF 第一步**:R(A+1)=obj)
//   - [8-11]  LSR x0, x0, #48                (后续 IsTable guard)
func TestPJ8_EmitSelfArrayHitArm64_RAPlus1Store(t *testing.T) {
	const aReg, bReg uint8 = 1, 3
	var buf []byte
	buf = EmitSelfArrayHitArm64(buf, aReg, bReg, 7, 3, 16, 0xCAFEBABE)

	if len(buf) < 16 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [0-3] LDR x0, [x26 + B*8](B=3, byteOff=24, imm12=3)
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

// TestPJ8_EmitSelfArrayHitArm64_DeoptBlock 验 deopt block 在 [152-171]。
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

// TestPJ8_EmitSelfNodeHitArm64_Length 验 PJ4 SELF NodeHit arm64 模板
// 字节长度(200 字节)。
func TestPJ8_EmitSelfNodeHitArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitSelfNodeHitArm64(buf,
		1,                  // aReg
		3,                  // bReg
		7,                  // stableShape
		2,                  // stableIndex
		0xFFF80000FEEDBEEF, // stableKey(字符串 method name NaN-box)
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

// TestPJ8_EmitSelfNodeHitArm64_RAPlus1Store 验 SELF 特征:R(A+1) 先于
// IsTable guard 写。
//   - [0-3]   LDR x0, [x26 + B*8]      (load R(B) obj)
//   - [4-7]   STR x0, [x26 + (A+1)*8]  (**SELF 第一步**:R(A+1)=obj)
//   - [8-11]  LSR x0, x0, #48          (后续 IsTable shift)
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

// TestPJ8_EmitSelfNodeHitArm64_StableKeyBurnedIn 验 stableKey 经
// movz+movk×3 烧进 NodeKey 比对段 [120-135]。
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

// TestPJ8_EmitSelfNodeHitArm64_DeoptBlock 验 deopt block 在 [180-199]。
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

// TestPJ8_EmitGetTableArrayHitArm64_StableShapeBurnedIn 验 stableShape
// 经 MOVZ+MOVK×3 烧进 [76-91](IC ArrayHit 模板 word5 gen check 段)。
// 步骤:LDR R(B) 4 + LSR 48 4 + MOV TagTable 16 + CMP 4 + B.NE 4 +
//
//	re-load LDR 4 + MOV payloadMask 16 + AND 4 + MOV reg 4 +
//	LDR x14 4 + ADD SIB 4 + LDR word5 4 + LSR 32 4 = 76 字节
//
// → MOV stableShape imm64 在 offset 76-91。
func TestPJ8_EmitGetTableArrayHitArm64_StableShapeBurnedIn(t *testing.T) {
	const stableShape uint32 = 0xCAFE_BEEF
	var buf []byte
	buf = EmitGetTableArrayHitArm64(buf, 1, 0, stableShape, 3, 16, 0xDEADBEEF)

	if len(buf) < 92 {
		t.Fatalf("buf too short: %d", len(buf))
	}
	// MOV x3, stableShape imm64 段 [76-91]
	expectedImm16 := [4]uint16{
		uint16(stableShape & 0xFFFF),
		uint16((stableShape >> 16) & 0xFFFF),
		0, // stableShape 是 uint32,高位为 0
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

// TestPJ8_EmitGetTableNodeHitArm64_StableKeyBurnedIn 验 stableKey 经
// MOVZ+MOVK×3 烧进 [116-131](NodeHit 模板 NodeKey 比对段)。
// 步骤:GetTable ArrayHit 前缀 76 字节(word5+LSR)+ MOV stableShape 16 +
//
//	CMP 4 + B.NE 4 + LDR nodeRef 4 + MOV reg 4 + ADD SIB 4 + LDR NodeKey 4
//
// = 116 字节 → MOV stableKey 在 offset 116-131。
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
		// Rd 字段必须是 3(x3)
		if (insn & 0x1F) != 3 {
			t.Errorf("stableKey movz/movk[%d]@%d Rd = %d, want 3 (x3)",
				i, off, insn&0x1F)
		}
	}
}

// TestPJ8_EmitSetTableNodeHitArm64_StableKeyBurnedIn 验 SETTABLE NodeHit
// 模板 stableKey 烧入位置。SETTABLE NodeHit 字节布局:guard 32(LDR+LSR+
// MOV+CMP+B.NE)+ re-load 36 + word5+LSR 8 + MOV stableShape 16 + CMP 4
// + B.NE 4 = 100 → nodeRef 段 12(LDR+MOV+ADD)= 112 → LDR NodeKey 4 →
// MOV stableKey [116-131]。
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

// TestPJ8_EmitSetTableArrayHitArm64_StableShapeBurnedIn 验 SETTABLE
// ArrayHit 模板 stableShape 烧入位置。字节布局同 GetTableArrayHit(无
// SELF STR / 无 nodeRef 分流):guard 32 + re-load 36 + word5+LSR 8 = 76
// → MOV stableShape 在 [76-91]。
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
		0, // uint32 高位
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

// TestPJ8_EmitSelfArrayHitArm64_StableShapeBurnedIn 验 SELF ArrayHit
// 模板 stableShape 烧入位置。SELF 多 step 2 STR R(A+1) 4 字节:
//   - guard 36(SELF:LDR 4 + STR 4 + LSR 4 + MOV 16 + CMP 4 + B.NE 4)
//   - re-load 36
//   - word5+LSR 8
//   - MOV stableShape 起 [80-95]
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

// TestPJ8_EmitSetTableNodeHitArm64_NodeRefAndKey 验 SET NodeHit 分流的
// 关键字节布局(对位 GetTableNodeHit,差异在 setter 段):
//   - [100-103] LDR x0, [x2, #24]            (nodeRef word3)
//   - [108-111] ADD x2, x14, x1               (新 SIB base for node)
//   - [116-131] MOV x3, stableKey imm64       (key 比对段)
//   - [136-139] B.NE deopt                    (NodeKey 失配)
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

// TestPJ8_EmitSelfNodeHitArm64_NodeRefAndKey 验 SELF NodeHit 分流的
// 关键字节布局。SELF 多 step 2 STR R(A+1) 4 字节,后续偏移整体 +4:
//   - [104-107] LDR x0, [x2, #24]            (nodeRef word3)
//   - [112-115] ADD x2, x14, x1               (新 SIB base for node)
//   - [120-135] MOV x3, stableKey imm64       (key 比对段)
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

// TestPJ8_EmitGetTableNodeHitArm64_StableShapeBurnedIn 验 GETTABLE NodeHit
// 模板 stableShape 烧入位置(NodeHit 与 ArrayHit 形态前缀同款,只是后续
// 分流到 nodeRef 而非 arrayRef):
//   - guard 32 + re-load 36 + word5+LSR 8 = 76
//   - MOV stableShape 在 [76-91]
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

// TestPJ8_EmitSetTableNodeHitArm64_StableShapeBurnedIn 验 SETTABLE NodeHit
// 模板 stableShape 烧入位置(布局同 GETTABLE NodeHit,前缀 76 字节相同)。
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

// TestPJ8_EmitSelfNodeHitArm64_StableShapeBurnedIn 验 SELF NodeHit 模板
// stableShape 烧入位置。SELF 多 step 2 STR R(A+1) 4 字节,整体后移:
//   - guard 36 + re-load 36 + word5+LSR 8 = 80
//   - MOV stableShape 在 [80-95]
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

// TestPJ8_EmitGetTableArrayHitArm64_StableIndexBurnedIn 验 stableIndex
// 经 LDR imm12 字段(byteOff/8 scaled offset)烧入到 array[idx] 加载段。
//
// GetTableArrayHit:guard 32 + re-load 36 + word5+LSR 8 + stableShape 段
//
//	16 + CMP+B.NE 8 + arrayRef LDR+MOV+ADD 12 = 112 → LDR array[stableIndex]
//	在 [112-115]。
//
// LDR Xt, [Xn, #disp] 编码:base 0xF9400000 | imm12<<10 | Rn<<5 | Rt;
// imm12 = byteOff/8 = stableIndex(因 array word 大小 = 8 字节)。
func TestPJ8_EmitGetTableArrayHitArm64_StableIndexBurnedIn(t *testing.T) {
	const stableIndex uint32 = 42
	var buf []byte
	buf = EmitGetTableArrayHitArm64(buf, 1, 0, 7, stableIndex, 16, 0xCAFEBABE)

	if len(buf) < 116 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [112-115] LDR x0, [x2, #stableIndex*8](array[stableIndex])
	insn := binary.LittleEndian.Uint32(buf[112:116])
	// imm12 = stableIndex(因 array word 大小 = 8 字节 → scaled offset)
	wantLdr := uint32(0xF9400000) | uint32(stableIndex)<<10 | uint32(2)<<5
	if insn != wantLdr {
		t.Errorf("[112] LDR x0, [x2, #stableIndex*8] = 0x%08x, want 0x%08x", insn, wantLdr)
	}

	// imm12 字段读出验
	gotImm12 := (insn >> 10) & 0xFFF
	if gotImm12 != stableIndex {
		t.Errorf("[112] LDR imm12 = %d, want %d (stableIndex)", gotImm12, stableIndex)
	}
}

// TestPJ8_EmitSetTableArrayHitArm64_StableIndexBurnedIn 验 SETTABLE
// ArrayHit 反向 store 段 STR x3, [x2, #stableIndex*8] 中 imm12 字段。
//
// SET ArrayHit:guard 32 + re-load 36 + word5+LSR+stableShape+CMP+B.NE 32
//   - arrayRef LDR+MOV+ADD 12 + LDR R(C) 4 = 116 → STR 在 [116-119]。
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

// TestPJ8_EmitSpecArgLoadKArm64_Length 验 PJ5 SELF spec args 装载 K arm64
// 模板长度(movz/movk × 4 + str = 5 × 4 = 20 字节)。
func TestPJ8_EmitSpecArgLoadKArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitSpecArgLoadKArm64(buf, 5, 0xDEADBEEF12345678)
	if len(buf) != 20 {
		t.Errorf("EmitSpecArgLoadKArm64 长度 = %d, want 20", len(buf))
	}
}

// TestPJ8_EmitSpecArgLoadRegArm64_Length 验 PJ5 SELF spec args 装载 reg arm64
// 模板长度(LDR + STR = 2 × 4 = 8 字节)。
func TestPJ8_EmitSpecArgLoadRegArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitSpecArgLoadRegArm64(buf, 5, 3)
	if len(buf) != 8 {
		t.Errorf("EmitSpecArgLoadRegArm64 长度 = %d, want 8", len(buf))
	}
}

// TestPJ8_EmitFrameInlineCIDepthIncArm64_Length 验 arm64 ciDepth++ 字节级
// inline 模板长度(LDR×2 + ADD + STR = 16 字节,对位 amd64 = 10)。
// 承 §9.20 Option B Spike 1。
func TestPJ8_EmitFrameInlineCIDepthIncArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineCIDepthIncArm64(buf, 56)
	if len(buf) != EncodedFrameInlineCIDepthIncDecArm64Len {
		t.Errorf("EmitFrameInlineCIDepthIncArm64 长度 = %d, want %d",
			len(buf), EncodedFrameInlineCIDepthIncDecArm64Len)
	}
}

// TestPJ8_EmitFrameInlineCIDepthDecArm64_Length 验 arm64 ciDepth-- 字节级
// inline 模板长度(LDR×2 + SUB + STR = 16 字节)。
func TestPJ8_EmitFrameInlineCIDepthDecArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineCIDepthDecArm64(buf, 56)
	if len(buf) != EncodedFrameInlineCIDepthIncDecArm64Len {
		t.Errorf("EmitFrameInlineCIDepthDecArm64 长度 = %d, want %d",
			len(buf), EncodedFrameInlineCIDepthIncDecArm64Len)
	}
}

// TestPJ8_EmitFrameInlineCIDepthDecArm64_Encoding 验 arm64 SUB Xd Xn imm12 字节级
// 编码(little-endian arm64 指令)— SUB x17, x17, #1 = 0xD1000631。
func TestPJ8_EmitFrameInlineCIDepthDecArm64_Encoding(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineCIDepthDecArm64(buf, 56)
	// SUB 指令在 offset 8(LDR×2 各 4 字节)
	subInsn := binary.LittleEndian.Uint32(buf[8:12])
	const wantSub = uint32(0xD1000631) // SUB x17, x17, #1
	if subInsn != wantSub {
		t.Errorf("SUB x17, x17, #1 = 0x%08X, want 0x%08X", subInsn, wantSub)
	}
}
