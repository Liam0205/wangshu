//go:build wangshu_p4 && arm64 && linux

package arm64

import (
	"encoding/binary"
	"testing"
)

// pj3_template_test.go —— PJ8 arm64 PJ3 FORLOOP 空 body 字节级模板单测。
//
// **不真 mmap+RX 跑**(arm64 trampoline asm 留物理 self-hosted runner);
// 本测试纯字节级布局验证。

// TestPJ8_EmitForLoopEmptyConstArm64_Length 验完整模板字节长度(84 字节)。
func TestPJ8_EmitForLoopEmptyConstArm64_Length(t *testing.T) {
	var buf []byte
	// kInit=1.0 / kLimit=100.0 / kStep=1.0(IEEE 754 NaN-box bits)
	buf = EmitForLoopEmptyConstArm64(buf,
		0x3FF0000000000000, // 1.0
		0x4059000000000000, // 100.0
		0x3FF0000000000000, // 1.0
		-1,                 // 无 safepoint
	)

	const wantLen = 84
	if len(buf) != wantLen {
		t.Errorf("总长度 = %d, want %d", len(buf), wantLen)
	}
	if len(buf) != EncodedForLoopEmptyConstArm64Len {
		t.Errorf("len = %d, want %d", len(buf), EncodedForLoopEmptyConstArm64Len)
	}
}

// TestPJ8_EmitForLoopEmptyConstArm64_Layout 验关键指令字节级布局:
//   - [16-19] FMOV d0, x0(load idx)
//   - [36-39] FMOV d1, x0(load limit)
//   - [56-59] FMOV d2, x0(load step)
//   - [60-63] FSUB d0, d0, d2(FORPREP 预减)
//   - [64-67] FADD d0, d0, d2(loop body idx+=step)
//   - [68-71] FCMPE d0, d1
//   - [72-75] B.GT after_loop
//   - [76-79] B loop_start backward
//   - [80-83] RET
func TestPJ8_EmitForLoopEmptyConstArm64_Layout(t *testing.T) {
	var buf []byte
	buf = EmitForLoopEmptyConstArm64(buf,
		0x3FF0000000000000,
		0x4059000000000000,
		0x3FF0000000000000,
		-1)

	if len(buf) < 84 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [16-19] FMOV d0, x0(0x9E670000 + Rn=0<<5 + Rd=0)
	insn := binary.LittleEndian.Uint32(buf[16:20])
	if insn != 0x9E670000 {
		t.Errorf("[16] FMOV d0, x0 = 0x%08x, want 0x9E670000", insn)
	}

	// [36-39] FMOV d1, x0(0x9E670000 + Rd=1)
	insn = binary.LittleEndian.Uint32(buf[36:40])
	if insn != 0x9E670001 {
		t.Errorf("[36] FMOV d1, x0 = 0x%08x, want 0x9E670001", insn)
	}

	// [56-59] FMOV d2, x0(0x9E670002)
	insn = binary.LittleEndian.Uint32(buf[56:60])
	if insn != 0x9E670002 {
		t.Errorf("[56] FMOV d2, x0 = 0x%08x, want 0x9E670002", insn)
	}

	// [60-63] FSUB d0, d0, d2(0x1E603800 + Rm=2<<16)
	insn = binary.LittleEndian.Uint32(buf[60:64])
	wantFsub := uint32(0x1E603800) | uint32(2)<<16
	if insn != wantFsub {
		t.Errorf("[60] FSUB d0, d0, d2 = 0x%08x, want 0x%08x", insn, wantFsub)
	}

	// [64-67] FADD d0, d0, d2(0x1E602800 + Rm=2<<16)
	insn = binary.LittleEndian.Uint32(buf[64:68])
	wantFadd := uint32(0x1E602800) | uint32(2)<<16
	if insn != wantFadd {
		t.Errorf("[64] FADD d0, d0, d2 = 0x%08x, want 0x%08x", insn, wantFadd)
	}

	// [68-71] FCMPE d0, d1(0x1E602010 + Rm=1<<16)
	insn = binary.LittleEndian.Uint32(buf[68:72])
	wantFcmpe := uint32(0x1E602010) | uint32(1)<<16
	if insn != wantFcmpe {
		t.Errorf("[68] FCMPE d0, d1 = 0x%08x, want 0x%08x", insn, wantFcmpe)
	}

	// [72-75] B.GT after_loop(0x54000000 + cond=GT=0xC + imm19)
	insn = binary.LittleEndian.Uint32(buf[72:76])
	if (insn & 0xFF00000F) != (0x54000000 | uint32(CondGT)) {
		t.Errorf("[72] B.GT base/cond bits wrong: 0x%08x", insn)
	}
	// imm19 = (after_loop - b.gt 自身) / 4 = (80 - 72)/4 = 2
	gotImm19 := (insn >> 5) & 0x7FFFF
	if gotImm19 != 2 {
		t.Errorf("[72] B.GT imm19 = %d, want 2(forward 2 words to ret)", gotImm19)
	}

	// [76-79] B loop_start backward(0x14000000 + imm26 negative)
	insn = binary.LittleEndian.Uint32(buf[76:80])
	if (insn & 0xFC000000) != 0x14000000 {
		t.Errorf("[76] B base wrong: 0x%08x", insn)
	}
	// imm26 = (loop_start - b 自身) / 4 = (64 - 76)/4 = -3
	// sign-extend 26-bit
	gotImm26 := int32(insn & 0x03FFFFFF)
	if gotImm26&0x02000000 != 0 { // bit 25 set → negative
		gotImm26 |= ^int32(0x03FFFFFF)
	}
	if gotImm26 != -3 {
		t.Errorf("[76] B imm26 = %d, want -3(backward 3 words to loop_start)", gotImm26)
	}

	// [80-83] RET
	insn = binary.LittleEndian.Uint32(buf[80:84])
	if insn != 0xd65f03c0 {
		t.Errorf("[80] RET = 0x%08x, want 0xd65f03c0", insn)
	}
}

// TestPJ8_EmitForLoopEmptyConstArm64_ConstantsBurnedIn 验三个常量 kInit/
// kLimit/kStep 真烧进 movz/movk imm16 序列。
func TestPJ8_EmitForLoopEmptyConstArm64_ConstantsBurnedIn(t *testing.T) {
	const kInit uint64 = 0xDEADBEEFCAFEBABE
	const kLimit uint64 = 0x1234567890ABCDEF
	const kStep uint64 = 0xFFFF000011112222

	var buf []byte
	buf = EmitForLoopEmptyConstArm64(buf, kInit, kLimit, kStep, -1)

	// 每 16 字节段 = 1 movz + 3 movk,验各 imm16 字段
	// [0-15] mov x0, kInit
	verifyMovImm64 := func(t *testing.T, name string, offset int, want uint64) {
		t.Helper()
		expectedImm16 := [4]uint16{
			uint16(want),
			uint16(want >> 16),
			uint16(want >> 32),
			uint16(want >> 48),
		}
		for i, exp := range expectedImm16 {
			insn := binary.LittleEndian.Uint32(buf[offset+i*4 : offset+(i+1)*4])
			got := uint16((insn >> 5) & 0xFFFF)
			if got != exp {
				t.Errorf("%s movz/movk[%d] imm16 = 0x%04x, want 0x%04x", name, i, got, exp)
			}
		}
	}
	verifyMovImm64(t, "kInit", 0, kInit)
	verifyMovImm64(t, "kLimit", 20, kLimit)
	verifyMovImm64(t, "kStep", 40, kStep)
}

// TestPJ8_EmitForLoopEmptyConstArm64_WithSafepoint 验启用 safepoint
// (preemptFlagOff>=0)模板字节长度 92 = 84 + ldrb 4 + cbnz 4。
//   - [64-67] LDRB W0, [x27, #pfOff]
//   - [68-71] CBNZ W0, after_loop(imm19 forward)
//   - [88-91] RET
func TestPJ8_EmitForLoopEmptyConstArm64_WithSafepoint(t *testing.T) {
	var buf []byte
	const pfOff int32 = 24
	buf = EmitForLoopEmptyConstArm64(buf,
		0x3FF0000000000000, 0x4059000000000000, 0x3FF0000000000000, pfOff)

	const wantLen = 92
	if len(buf) != wantLen {
		t.Fatalf("总长度 = %d, want %d(含 safepoint)", len(buf), wantLen)
	}

	// [64-67] LDRB W0, [x27, #24]
	insn := binary.LittleEndian.Uint32(buf[64:68])
	wantLdrb := uint32(0x39400000) | uint32(pfOff)<<10 | uint32(27)<<5 | uint32(0)
	if insn != wantLdrb {
		t.Errorf("[64] LDRB W0, [x27, #%d] = 0x%08x, want 0x%08x", pfOff, insn, wantLdrb)
	}

	// [68-71] CBNZ W0, after_loop(forward;after_loop 在 88)
	insn = binary.LittleEndian.Uint32(buf[68:72])
	if (insn & 0xFF000000) != 0x35000000 {
		t.Errorf("[68] CBNZ base wrong: 0x%08x", insn)
	}
	if insn&0x1F != 0 {
		t.Errorf("[68] CBNZ Rt = %d, want 0 (w0)", insn&0x1F)
	}
	gotImm19 := (insn >> 5) & 0x7FFFF
	// imm19 = (88 - 68) / 4 = 5
	if gotImm19 != 5 {
		t.Errorf("[68] CBNZ imm19 = %d, want 5 ((88-68)/4)", gotImm19)
	}

	// [88-91] RET
	insn = binary.LittleEndian.Uint32(buf[88:92])
	if insn != 0xd65f03c0 {
		t.Errorf("[88] RET = 0x%08x, want 0xd65f03c0", insn)
	}
}

// TestPJ8_EmitForLoopRegLimitArm64_Length 验 RegLimit 模板字节长度
// (含/无 safepoint 双形态)。
func TestPJ8_EmitForLoopRegLimitArm64_Length(t *testing.T) {
	cases := []struct {
		name    string
		pfOff   int32
		wantLen int
	}{
		{"no safepoint", -1, 120},
		{"with safepoint", 24, 128},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			buf = EmitForLoopRegLimitArm64(buf,
				0x3FF0000000000000, // kInit=1.0
				0x3FF0000000000000, // kStep=1.0
				5,                  // limitReg
				0xCAFEBABE,         // deoptCode
				tc.pfOff)
			if len(buf) != tc.wantLen {
				t.Errorf("总长度 = %d, want %d", len(buf), tc.wantLen)
			}
		})
	}
	// 常量自洽
	if EncodedForLoopRegLimitArm64NoSafepointLen != 120 {
		t.Errorf("EncodedForLoopRegLimitArm64NoSafepointLen = %d, want 120",
			EncodedForLoopRegLimitArm64NoSafepointLen)
	}
	if EncodedForLoopRegLimitArm64WithSafepointLen != 128 {
		t.Errorf("EncodedForLoopRegLimitArm64WithSafepointLen = %d, want 128",
			EncodedForLoopRegLimitArm64WithSafepointLen)
	}
}

// TestPJ8_EmitForLoopRegLimitArm64_GuardSegment 验 guard 段:
//   - [0-3]   LDR x0, [x26 + limitReg*8]
//   - [4-19]  MOV x1, qNanBoxBase imm64(movz+movk×3)
//   - [20-23] CMP x0, x1
//   - [24-27] B.HS deopt
func TestPJ8_EmitForLoopRegLimitArm64_GuardSegment(t *testing.T) {
	const limitReg uint8 = 5
	var buf []byte
	buf = EmitForLoopRegLimitArm64(buf, 0x3FF0000000000000, 0x3FF0000000000000,
		limitReg, 0xCAFEBABE, -1)

	if len(buf) < 28 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [0-3] LDR x0, [x26 + 40](limitReg=5 → byteOff 40 → imm12=5)
	insn := binary.LittleEndian.Uint32(buf[0:4])
	wantLdr := uint32(0xF9400000) | uint32(5)<<10 | uint32(26)<<5 | uint32(0)
	if insn != wantLdr {
		t.Errorf("[0] LDR x0, [x26, #40] = 0x%08x, want 0x%08x", insn, wantLdr)
	}

	// [4-7] MOVZ x1, qNanBoxBase[15:0]=0x0000
	insn = binary.LittleEndian.Uint32(buf[4:8])
	if (insn & 0xFFE00000) != 0xD2800000 {
		t.Errorf("[4] MOVZ x1 base wrong: 0x%08x", insn)
	}
	if (insn & 0x1F) != 1 {
		t.Errorf("[4] MOVZ Rd = %d, want 1 (x1)", insn&0x1F)
	}

	// [20-23] CMP x0, x1 = SUBS XZR, x0, x1
	insn = binary.LittleEndian.Uint32(buf[20:24])
	wantCmp := uint32(0xEB00001F) | uint32(1)<<16 | uint32(0)<<5
	if insn != wantCmp {
		t.Errorf("[20] CMP x0, x1 = 0x%08x, want 0x%08x", insn, wantCmp)
	}

	// [24-27] B.HS deopt(cond=HS=0x2)
	insn = binary.LittleEndian.Uint32(buf[24:28])
	if (insn & 0xFF000000) != 0x54000000 {
		t.Errorf("[24] B.cond base wrong: 0x%08x", insn)
	}
	if insn&0xF != uint32(CondHS) {
		t.Errorf("[24] B.cond cond = 0x%x, want 0x%x (HS)", insn&0xF, CondHS)
	}
	// imm19 = (deoptStart - 24) / 4。deoptStart = 108 → imm19 = (108-24)/4 = 21
	gotImm19 := (insn >> 5) & 0x7FFFF
	if gotImm19 != 21 {
		t.Errorf("[24] B.HS imm19 = %d, want 21 ((108-24)/4)", gotImm19)
	}
}

// TestPJ8_EmitForLoopRegLimitArm64_DeoptBlock 验 deopt block 在 [108-127]。
func TestPJ8_EmitForLoopRegLimitArm64_DeoptBlock(t *testing.T) {
	const deoptCode uint64 = 0xDEAD_BEEF_CAFE_BABE
	var buf []byte
	buf = EmitForLoopRegLimitArm64(buf, 0x3FF0000000000000, 0x3FF0000000000000,
		5, deoptCode, -1)

	if len(buf) < 128 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [108-111] MOVZ x0, deoptCode[15:0]=0xBABE
	insn := binary.LittleEndian.Uint32(buf[108:112])
	imm0 := (insn >> 5) & 0xFFFF
	if imm0 != 0xBABE {
		t.Errorf("[108] MOVZ x0 imm[15:0] = 0x%04x, want 0xBABE", imm0)
	}

	// [124-127] RET
	insn = binary.LittleEndian.Uint32(buf[124:128])
	if insn != 0xd65f03c0 {
		t.Errorf("[124] RET = 0x%08x, want 0xd65f03c0", insn)
	}
}

// TestPJ8_EmitForLoopWithRegKBodyArm64_Length 验 WithRegKBody 模板字节
// 长度(含/无 safepoint 双形态)。
func TestPJ8_EmitForLoopWithRegKBodyArm64_Length(t *testing.T) {
	cases := []struct {
		name    string
		sseOp   byte
		pfOff   int32
		wantLen int
	}{
		{"ADD no safepoint", 0x58, -1, 144},
		{"ADD with safepoint", 0x58, 24, 152},
		{"SUB no safepoint", 0x5C, -1, 144},
		{"MUL with safepoint", 0x59, 24, 152},
		{"DIV with safepoint", 0x5E, 24, 152},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			buf = EmitForLoopWithRegKBodyArm64(buf,
				0x0,                // kS=0
				0x3FF0000000000000, // kInit=1.0
				0x4059000000000000, // kLimit=100.0
				0x3FF0000000000000, // kStep=1.0
				0x3FF0000000000000, // kBody=1.0
				3,                  // aS
				tc.sseOp,
				tc.pfOff)
			if len(buf) != tc.wantLen {
				t.Errorf("总长度 = %d, want %d", len(buf), tc.wantLen)
			}
		})
	}
	if EncodedForLoopWithRegKBodyArm64NoSafepointLen != 144 {
		t.Errorf("EncodedForLoopWithRegKBodyArm64NoSafepointLen = %d, want 144",
			EncodedForLoopWithRegKBodyArm64NoSafepointLen)
	}
	if EncodedForLoopWithRegKBodyArm64WithSafepointLen != 152 {
		t.Errorf("EncodedForLoopWithRegKBodyArm64WithSafepointLen = %d, want 152",
			EncodedForLoopWithRegKBodyArm64WithSafepointLen)
	}
}

// TestPJ8_EmitForLoopWithRegKBodyArm64_UnknownOp 验未识别 sseOp 不操作 buf。
func TestPJ8_EmitForLoopWithRegKBodyArm64_UnknownOp(t *testing.T) {
	var buf []byte
	buf = EmitForLoopWithRegKBodyArm64(buf, 0, 0, 0, 0, 0, 0, 0xFF, -1)
	if len(buf) != 0 {
		t.Errorf("unknown sseOp 应不操作 buf,实际 len=%d", len(buf))
	}
}

// TestPJ8_EmitForLoopWithRegKBodyArm64_BodyFopBytes 验 body 段
// FADD/FSUB/FMUL/FDIV 编码逐字节匹配 sseOp 选择(body 在 offset 124-127)。
func TestPJ8_EmitForLoopWithRegKBodyArm64_BodyFopBytes(t *testing.T) {
	cases := []struct {
		name    string
		sseOp   byte
		fopBase uint32 // arm64 base(不含 Rm/Rn/Rd 字段)
	}{
		{"FADD d3, d3, d4", 0x58, 0x1E602800},
		{"FSUB d3, d3, d4", 0x5C, 0x1E603800},
		{"FMUL d3, d3, d4", 0x59, 0x1E600800},
		{"FDIV d3, d3, d4", 0x5E, 0x1E601800},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			buf = EmitForLoopWithRegKBodyArm64(buf,
				0x0, 0x3FF0000000000000, 0x4059000000000000, 0x3FF0000000000000,
				0x3FF0000000000000, 3, tc.sseOp, -1)

			if len(buf) < 128 {
				t.Fatalf("buf too short: %d", len(buf))
			}

			// [124-127] FOP d3, d3, d4
			insn := binary.LittleEndian.Uint32(buf[124:128])
			want := tc.fopBase | uint32(4)<<16 | uint32(3)<<5 | uint32(3)
			if insn != want {
				t.Errorf("[124] %s = 0x%08x, want 0x%08x", tc.name, insn, want)
			}
		})
	}
}
