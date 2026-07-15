//go:build wangshu_p4 && arm64 && linux

package arm64

import (
	"encoding/binary"
	"testing"
)

// pj3_template_test.go —— PJ8 arm64 PJ3 FORLOOP empty-body byte-level template unit tests.
//
// **Does not actually mmap+RX and run** (arm64 trampoline asm is left to a physical
// self-hosted runner); this test is pure byte-level layout verification.

// TestPJ8_EmitForLoopEmptyConstArm64_Length verifies the full template byte length (84 bytes).
func TestPJ8_EmitForLoopEmptyConstArm64_Length(t *testing.T) {
	var buf []byte
	// kInit=1.0 / kLimit=100.0 / kStep=1.0(IEEE 754 NaN-box bits)
	buf = EmitForLoopEmptyConstArm64(buf,
		0x3FF0000000000000, // 1.0
		0x4059000000000000, // 100.0
		0x3FF0000000000000, // 1.0
		-1,                 // no safepoint
	)

	const wantLen = 84
	if len(buf) != wantLen {
		t.Errorf("总长度 = %d, want %d", len(buf), wantLen)
	}
	if len(buf) != EncodedForLoopEmptyConstArm64Len {
		t.Errorf("len = %d, want %d", len(buf), EncodedForLoopEmptyConstArm64Len)
	}
}

// TestPJ8_EmitForLoopEmptyConstArm64_Layout verifies the byte-level layout of key instructions:
//   - [16-19] FMOV d0, x0 (load idx)
//   - [36-39] FMOV d1, x0 (load limit)
//   - [56-59] FMOV d2, x0 (load step)
//   - [60-63] FSUB d0, d0, d2 (FORPREP pre-decrement)
//   - [64-67] FADD d0, d0, d2 (loop body idx+=step)
//   - [68-71] FCMPE d0, d1
//   - [72-75] B.HI after_loop (unordered exit, #117/#118)
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

	// [16-19] FMOV d0, x0 (0x9E670000 + Rn=0<<5 + Rd=0)
	insn := binary.LittleEndian.Uint32(buf[16:20])
	if insn != 0x9E670000 {
		t.Errorf("[16] FMOV d0, x0 = 0x%08x, want 0x9E670000", insn)
	}

	// [36-39] FMOV d1, x0 (0x9E670000 + Rd=1)
	insn = binary.LittleEndian.Uint32(buf[36:40])
	if insn != 0x9E670001 {
		t.Errorf("[36] FMOV d1, x0 = 0x%08x, want 0x9E670001", insn)
	}

	// [56-59] FMOV d2, x0 (0x9E670002)
	insn = binary.LittleEndian.Uint32(buf[56:60])
	if insn != 0x9E670002 {
		t.Errorf("[56] FMOV d2, x0 = 0x%08x, want 0x9E670002", insn)
	}

	// [60-63] FSUB d0, d0, d2 (0x1E603800 + Rm=2<<16)
	insn = binary.LittleEndian.Uint32(buf[60:64])
	wantFsub := uint32(0x1E603800) | uint32(2)<<16
	if insn != wantFsub {
		t.Errorf("[60] FSUB d0, d0, d2 = 0x%08x, want 0x%08x", insn, wantFsub)
	}

	// [64-67] FADD d0, d0, d2 (0x1E602800 + Rm=2<<16)
	insn = binary.LittleEndian.Uint32(buf[64:68])
	wantFadd := uint32(0x1E602800) | uint32(2)<<16
	if insn != wantFadd {
		t.Errorf("[64] FADD d0, d0, d2 = 0x%08x, want 0x%08x", insn, wantFadd)
	}

	// [68-71] FCMPE d0, d1 (0x1E602010 + Rm=1<<16)
	insn = binary.LittleEndian.Uint32(buf[68:72])
	wantFcmpe := uint32(0x1E602010) | uint32(1)<<16
	if insn != wantFcmpe {
		t.Errorf("[68] FCMPE d0, d1 = 0x%08x, want 0x%08x", insn, wantFcmpe)
	}

	// [72-75] B.HI after_loop (0x54000000 + cond=HI=0x8 + imm19) —
	// HI, not GT: unordered (NaN limit) must exit (#117/#118).
	insn = binary.LittleEndian.Uint32(buf[72:76])
	if (insn & 0xFF00000F) != (0x54000000 | uint32(CondHI)) {
		t.Errorf("[72] B.HI base/cond bits wrong: 0x%08x", insn)
	}
	// imm19 = (after_loop - b.hi itself) / 4 = (80 - 72)/4 = 2
	gotImm19 := (insn >> 5) & 0x7FFFF
	if gotImm19 != 2 {
		t.Errorf("[72] B.HI imm19 = %d, want 2(forward 2 words to ret)", gotImm19)
	}

	// [76-79] B loop_start backward (0x14000000 + imm26 negative)
	insn = binary.LittleEndian.Uint32(buf[76:80])
	if (insn & 0xFC000000) != 0x14000000 {
		t.Errorf("[76] B base wrong: 0x%08x", insn)
	}
	// imm26 = (loop_start - b itself) / 4 = (64 - 76)/4 = -3
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

// TestPJ8_EmitForLoopEmptyConstArm64_ConstantsBurnedIn verifies the three constants kInit/
// kLimit/kStep are actually burned into the movz/movk imm16 sequences.
func TestPJ8_EmitForLoopEmptyConstArm64_ConstantsBurnedIn(t *testing.T) {
	const kInit uint64 = 0xDEADBEEFCAFEBABE
	const kLimit uint64 = 0x1234567890ABCDEF
	const kStep uint64 = 0xFFFF000011112222

	var buf []byte
	buf = EmitForLoopEmptyConstArm64(buf, kInit, kLimit, kStep, -1)

	// Each 16-byte segment = 1 movz + 3 movk; verify each imm16 field.
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

// TestPJ8_EmitForLoopEmptyConstArm64_WithSafepoint verifies that with safepoint enabled
// (preemptFlagOff>=0) the template byte length is 92 = 84 + ldrb 4 + cbnz 4.
// The safepoint sits after b.hi (limit exit) and before b loop_start (back edge),
// following the same hot-path template as RegLimit/WithBody/WithBody2 (per review S-1):
//   - [76-79] LDRB W0, [x27, #pfOff]
//   - [80-83] CBNZ W0, after_loop (imm19 forward)
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

	// [76-79] LDRB W0, [x27, #24]
	insn := binary.LittleEndian.Uint32(buf[76:80])
	wantLdrb := uint32(0x39400000) | uint32(pfOff)<<10 | uint32(27)<<5 | uint32(0)
	if insn != wantLdrb {
		t.Errorf("[76] LDRB W0, [x27, #%d] = 0x%08x, want 0x%08x", pfOff, insn, wantLdrb)
	}

	// [80-83] CBNZ W0, after_loop (forward; after_loop is at 88)
	insn = binary.LittleEndian.Uint32(buf[80:84])
	if (insn & 0xFF000000) != 0x35000000 {
		t.Errorf("[80] CBNZ base wrong: 0x%08x", insn)
	}
	if insn&0x1F != 0 {
		t.Errorf("[80] CBNZ Rt = %d, want 0 (w0)", insn&0x1F)
	}
	gotImm19 := (insn >> 5) & 0x7FFFF
	// imm19 = (88 - 80) / 4 = 2
	if gotImm19 != 2 {
		t.Errorf("[80] CBNZ imm19 = %d, want 2 ((88-80)/4)", gotImm19)
	}

	// [88-91] RET
	insn = binary.LittleEndian.Uint32(buf[88:92])
	if insn != 0xd65f03c0 {
		t.Errorf("[88] RET = 0x%08x, want 0xd65f03c0", insn)
	}
}

// TestPJ8_EmitForLoopRegLimitArm64_Length verifies the RegLimit template byte length
// (with/without safepoint, both forms).
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
	// Constants self-consistent
	if EncodedForLoopRegLimitArm64NoSafepointLen != 120 {
		t.Errorf("EncodedForLoopRegLimitArm64NoSafepointLen = %d, want 120",
			EncodedForLoopRegLimitArm64NoSafepointLen)
	}
	if EncodedForLoopRegLimitArm64WithSafepointLen != 128 {
		t.Errorf("EncodedForLoopRegLimitArm64WithSafepointLen = %d, want 128",
			EncodedForLoopRegLimitArm64WithSafepointLen)
	}
}

// TestPJ8_EmitForLoopRegLimitArm64_GuardSegment verifies the guard segment:
//   - [0-3]   LDR x0, [x26 + limitReg*8]
//   - [4-19]  MOV x1, qNanBoxBase imm64 (movz+movk×3)
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

	// [0-3] LDR x0, [x26 + 40] (limitReg=5 → byteOff 40 → imm12=5)
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

	// [24-27] B.HS deopt (cond=HS=0x2)
	insn = binary.LittleEndian.Uint32(buf[24:28])
	if (insn & 0xFF000000) != 0x54000000 {
		t.Errorf("[24] B.cond base wrong: 0x%08x", insn)
	}
	if insn&0xF != uint32(CondHS) {
		t.Errorf("[24] B.cond cond = 0x%x, want 0x%x (HS)", insn&0xF, CondHS)
	}
	// imm19 = (deoptStart - 24) / 4. This test uses the no-safepoint form (pfOff=-1),
	// so deoptStart = 100 → imm19 = (100-24)/4 = 19.
	gotImm19 := (insn >> 5) & 0x7FFFF
	if gotImm19 != 19 {
		t.Errorf("[24] B.HS imm19 = %d, want 19 ((100-24)/4)", gotImm19)
	}
}

// TestPJ8_EmitForLoopRegLimitArm64_DeoptBlock verifies the deopt block is at [100-119]
// (no-safepoint form).
func TestPJ8_EmitForLoopRegLimitArm64_DeoptBlock(t *testing.T) {
	const deoptCode uint64 = 0xDEAD_BEEF_CAFE_BABE
	var buf []byte
	buf = EmitForLoopRegLimitArm64(buf, 0x3FF0000000000000, 0x3FF0000000000000,
		5, deoptCode, -1)

	if len(buf) < 120 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [100-103] MOVZ x0, deoptCode[15:0]=0xBABE
	insn := binary.LittleEndian.Uint32(buf[100:104])
	imm0 := (insn >> 5) & 0xFFFF
	if imm0 != 0xBABE {
		t.Errorf("[100] MOVZ x0 imm[15:0] = 0x%04x, want 0xBABE", imm0)
	}

	// [116-119] RET
	insn = binary.LittleEndian.Uint32(buf[116:120])
	if insn != 0xd65f03c0 {
		t.Errorf("[116] RET = 0x%08x, want 0xd65f03c0", insn)
	}
}

// TestPJ8_EmitForLoopWithRegKBodyArm64_Length verifies the WithRegKBody template byte
// length (with/without safepoint, both forms).
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

// TestPJ8_EmitForLoopWithRegKBodyArm64_UnknownOp verifies an unrecognized sseOp does not touch buf.
func TestPJ8_EmitForLoopWithRegKBodyArm64_UnknownOp(t *testing.T) {
	var buf []byte
	buf = EmitForLoopWithRegKBodyArm64(buf, 0, 0, 0, 0, 0, 0, 0xFF, -1)
	if len(buf) != 0 {
		t.Errorf("unknown sseOp 应不操作 buf,实际 len=%d", len(buf))
	}
}

// TestPJ8_EmitForLoopWithRegKBodyArm64_BodyFopBytes verifies the body segment
// FADD/FSUB/FMUL/FDIV encoding matches the sseOp selection byte for byte (body at offset 124-127).
func TestPJ8_EmitForLoopWithRegKBodyArm64_BodyFopBytes(t *testing.T) {
	cases := []struct {
		name    string
		sseOp   byte
		fopBase uint32 // arm64 base (without Rm/Rn/Rd fields)
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

// TestPJ8_EmitForLoopWithRegKBody2Arm64_Length verifies the WithRegKBody2 template
// byte length (with/without safepoint, both forms).
func TestPJ8_EmitForLoopWithRegKBody2Arm64_Length(t *testing.T) {
	cases := []struct {
		name     string
		op1, op2 byte
		pfOff    int32
		wantLen  int
	}{
		{"ADD+ADD no safepoint", 0x58, 0x58, -1, 168},
		{"MUL+ADD with safepoint", 0x59, 0x58, 24, 176},
		{"SUB+MUL no safepoint", 0x5C, 0x59, -1, 168},
		{"DIV+SUB with safepoint", 0x5E, 0x5C, 24, 176},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			buf = EmitForLoopWithRegKBody2Arm64(buf,
				0x0,                // kS=0
				0x3FF0000000000000, // kInit
				0x4059000000000000, // kLimit
				0x3FF0000000000000, // kStep
				0x3FF0000000000000, // kBody1
				0x4000000000000000, // kBody2=2.0
				3,                  // aS
				tc.op1, tc.op2,
				tc.pfOff)
			if len(buf) != tc.wantLen {
				t.Errorf("总长度 = %d, want %d", len(buf), tc.wantLen)
			}
		})
	}
	if EncodedForLoopWithRegKBody2Arm64NoSafepointLen != 168 {
		t.Errorf("EncodedForLoopWithRegKBody2Arm64NoSafepointLen = %d, want 168",
			EncodedForLoopWithRegKBody2Arm64NoSafepointLen)
	}
	if EncodedForLoopWithRegKBody2Arm64WithSafepointLen != 176 {
		t.Errorf("EncodedForLoopWithRegKBody2Arm64WithSafepointLen = %d, want 176",
			EncodedForLoopWithRegKBody2Arm64WithSafepointLen)
	}
}

// TestPJ8_EmitForLoopWithRegKBody2Arm64_UnknownOp verifies that if either op is unrecognized buf is not touched.
func TestPJ8_EmitForLoopWithRegKBody2Arm64_UnknownOp(t *testing.T) {
	var buf []byte
	buf = EmitForLoopWithRegKBody2Arm64(buf, 0, 0, 0, 0, 0, 0, 0, 0xFF, 0x58, -1)
	if len(buf) != 0 {
		t.Errorf("unknown sseOp1 应不操作 buf,实际 len=%d", len(buf))
	}
	buf = EmitForLoopWithRegKBody2Arm64(buf, 0, 0, 0, 0, 0, 0, 0, 0x58, 0xFF, -1)
	if len(buf) != 0 {
		t.Errorf("unknown sseOp2 应不操作 buf,实际 len=%d", len(buf))
	}
}

// TestPJ8_EmitForLoopWithRegKBody2Arm64_BodyFopsBytes verifies the body segment op1
// (offset 124-127) and op2 (offset 148-151) encodings byte for byte.
func TestPJ8_EmitForLoopWithRegKBody2Arm64_BodyFopsBytes(t *testing.T) {
	cases := []struct {
		name     string
		op1, op2 byte
		fop1Base uint32
		fop2Base uint32
	}{
		{"MUL+ADD", 0x59, 0x58, 0x1E600800, 0x1E602800},
		{"SUB+DIV", 0x5C, 0x5E, 0x1E603800, 0x1E601800},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			buf = EmitForLoopWithRegKBody2Arm64(buf,
				0x0, 0x3FF0000000000000, 0x4059000000000000, 0x3FF0000000000000,
				0x3FF0000000000000, 0x4000000000000000, 3, tc.op1, tc.op2, -1)

			if len(buf) < 152 {
				t.Fatalf("buf too short: %d", len(buf))
			}

			insn := binary.LittleEndian.Uint32(buf[124:128])
			want1 := tc.fop1Base | uint32(4)<<16 | uint32(3)<<5 | uint32(3)
			if insn != want1 {
				t.Errorf("[124] op1 = 0x%08x, want 0x%08x", insn, want1)
			}
			insn = binary.LittleEndian.Uint32(buf[148:152])
			want2 := tc.fop2Base | uint32(4)<<16 | uint32(3)<<5 | uint32(3)
			if insn != want2 {
				t.Errorf("[148] op2 = 0x%08x, want 0x%08x", insn, want2)
			}
		})
	}
}

// TestPJ8_EmitForLoopRegLimitArm64_ConstantsBurnedIn verifies K_init / K_step are
// burned into each segment's imm16 fields via MOVZ+MOVK×3. RegLimit form:
//   - K_init at [28-43] (after guard at 28)
//   - K_step at [56-71] (K_init segment 16 + FMOV 4 + LDR limit 4 + FMOV 4 = offset 28)
func TestPJ8_EmitForLoopRegLimitArm64_ConstantsBurnedIn(t *testing.T) {
	const kInit uint64 = 0xDEAD_BEEF_CAFE_FACE
	const kStep uint64 = 0xFEED_F00D_BABE_C001

	var buf []byte
	buf = EmitForLoopRegLimitArm64(buf, kInit, kStep, 5, 0xCAFEBABE, -1)

	verifyMov := func(label string, offset int, want uint64) {
		expectedImm16 := [4]uint16{
			uint16(want & 0xFFFF),
			uint16((want >> 16) & 0xFFFF),
			uint16((want >> 32) & 0xFFFF),
			uint16((want >> 48) & 0xFFFF),
		}
		for i, exp := range expectedImm16 {
			off := offset + i*4
			insn := binary.LittleEndian.Uint32(buf[off : off+4])
			got := uint16((insn >> 5) & 0xFFFF)
			if got != exp {
				t.Errorf("%s movz/movk[%d]@%d imm16 = 0x%04x, want 0x%04x",
					label, i, off, got, exp)
			}
		}
	}
	// K_init at [28-43]
	verifyMov("K_init", 28, kInit)
	// K_step at [56-71]
	verifyMov("K_step", 56, kStep)
}

// TestPJ8_EmitForLoopWithRegKBodyArm64_ConstantsBurnedIn verifies the WithRegKBody
// five constants K_s / K_init / K_limit / K_step / K_body each occupy a 16-byte imm64 segment.
//   - K_s at [0-15]
//   - K_init at [20-35] (K_s 16 + STR 4)
//   - K_limit at [40-55]
//   - K_step at [60-75]
//   - K_body at [104-119] (init 20 + setup 60 + FSUB/FADD/FCMPE/B.GT 16
//   - LDR s 4 + FMOV 4)
func TestPJ8_EmitForLoopWithRegKBodyArm64_ConstantsBurnedIn(t *testing.T) {
	const kS uint64 = 0x4000_0000_0000_0000
	const kInit uint64 = 0x3FF0_0000_0000_0000
	const kLimit uint64 = 0x4059_0000_0000_0000
	const kStep uint64 = 0x3FF0_0000_0000_0000
	const kBody uint64 = 0xDEAD_BEEF_CAFE_BABE

	var buf []byte
	buf = EmitForLoopWithRegKBodyArm64(buf, kS, kInit, kLimit, kStep, kBody,
		3, 0x58 /* SseOpAddsd */, -1)

	verifyMov := func(label string, offset int, want uint64) {
		expectedImm16 := [4]uint16{
			uint16(want & 0xFFFF),
			uint16((want >> 16) & 0xFFFF),
			uint16((want >> 32) & 0xFFFF),
			uint16((want >> 48) & 0xFFFF),
		}
		for i, exp := range expectedImm16 {
			off := offset + i*4
			insn := binary.LittleEndian.Uint32(buf[off : off+4])
			got := uint16((insn >> 5) & 0xFFFF)
			if got != exp {
				t.Errorf("%s movz/movk[%d]@%d imm16 = 0x%04x, want 0x%04x",
					label, i, off, got, exp)
			}
		}
	}
	verifyMov("K_s", 0, kS)
	verifyMov("K_init", 20, kInit)
	verifyMov("K_limit", 40, kLimit)
	verifyMov("K_step", 60, kStep)
	verifyMov("K_body", 104, kBody)
}

// TestPJ8_EmitForLoopWithRegKBody2Arm64_ConstantsBurnedIn verifies the WithRegKBody2
// six constants K_s / K_init / K_limit / K_step / K_body1 / K_body2 each occupy an imm64 segment.
//   - K_s at [0-15]
//   - K_init at [20-35]
//   - K_limit at [40-55]
//   - K_step at [60-75]
//   - K_body1 at [104-119] (init 20 + setup 60 + FSUB 4 + FADD/FCMPE/B.GT 12
//   - LDR s 4 + FMOV d3 4)
//   - K_body2 at [128-143] (K_body1 16 + FMOV d4 4 + FOP1 4)
func TestPJ8_EmitForLoopWithRegKBody2Arm64_ConstantsBurnedIn(t *testing.T) {
	const kS uint64 = 0x4000_0000_0000_0000
	const kInit uint64 = 0x3FF0_0000_0000_0000
	const kLimit uint64 = 0x4059_0000_0000_0000
	const kStep uint64 = 0x3FF0_0000_0000_0000
	const kBody1 uint64 = 0xDEAD_BEEF_CAFE_BABE
	const kBody2 uint64 = 0xFEED_F00D_BABE_C001

	var buf []byte
	buf = EmitForLoopWithRegKBody2Arm64(buf, kS, kInit, kLimit, kStep,
		kBody1, kBody2, 3, 0x59 /* SseOpMulsd */, 0x58 /* SseOpAddsd */, -1)

	verifyMov := func(label string, offset int, want uint64) {
		expectedImm16 := [4]uint16{
			uint16(want & 0xFFFF),
			uint16((want >> 16) & 0xFFFF),
			uint16((want >> 32) & 0xFFFF),
			uint16((want >> 48) & 0xFFFF),
		}
		for i, exp := range expectedImm16 {
			off := offset + i*4
			insn := binary.LittleEndian.Uint32(buf[off : off+4])
			got := uint16((insn >> 5) & 0xFFFF)
			if got != exp {
				t.Errorf("%s movz/movk[%d]@%d imm16 = 0x%04x, want 0x%04x",
					label, i, off, got, exp)
			}
		}
	}
	verifyMov("K_s", 0, kS)
	verifyMov("K_init", 20, kInit)
	verifyMov("K_limit", 40, kLimit)
	verifyMov("K_step", 60, kStep)
	verifyMov("K_body1", 104, kBody1)
	verifyMov("K_body2", 128, kBody2)
}
