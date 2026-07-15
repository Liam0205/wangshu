//go:build wangshu_p4 && arm64 && linux

package arm64

import (
	"encoding/binary"
	"testing"
)

// pj2_template_test.go —— PJ8 arm64 PJ2 speculative ADD/SUB/MUL/DIV byte-level
// template unit tests.
//
// **Does not actually mmap+RX and run** (arm64 trampoline asm is left to a
// self-hosted runner on real hardware); this test is a pure byte-level layout
// check: per-instruction byte encoding + total template length + inter-segment
// offsets.

// TestPJ8_EmitIsNumberGuardArm64_Layout verifies the arm64 IsNumber guard
// byte-level layout (28 bytes = LDR 4 + MOV imm64 16 + CMP 4 + B.cond 4).
func TestPJ8_EmitIsNumberGuardArm64_Layout(t *testing.T) {
	var buf []byte
	buf = EmitIsNumberGuardArm64(buf, 5, 16) // reg=5, imm19=16 (to deopt)

	if len(buf) != EncodedIsNumberGuardArm64Len {
		t.Errorf("len = %d, want %d", len(buf), EncodedIsNumberGuardArm64Len)
	}
	const wantLen = 28
	if len(buf) != wantLen {
		t.Errorf("总长度 = %d, want %d", len(buf), wantLen)
	}

	// [0-3] LDR x0, [x26 + 40] (reg=5 → byteOff 40)
	insn0 := binary.LittleEndian.Uint32(buf[0:4])
	if (insn0 & 0xFFC00000) != 0xF9400000 {
		t.Errorf("guard[0] LDR base = 0x%08x, want 0xF940 prefix", insn0&0xFFC00000)
	}
	// imm12 = 40/8 = 5
	if (insn0>>10)&0xFFF != 5 {
		t.Errorf("guard[0] imm12 = %d, want 5", (insn0>>10)&0xFFF)
	}
	if (insn0>>5)&0x1F != 26 {
		t.Errorf("guard[0] Rn = %d, want 26 (x26)", (insn0>>5)&0x1F)
	}

	// [4-7] MOVZ x1, qNanBoxBase[15:0] = 0x0000
	insn1 := binary.LittleEndian.Uint32(buf[4:8])
	if (insn1 & 0xFFE00000) != 0xD2800000 {
		t.Errorf("guard MOVZ base wrong")
	}

	// [20-23] CMP x0, x1
	insn5 := binary.LittleEndian.Uint32(buf[20:24])
	wantCmp := uint32(0xEB00001F) | uint32(1)<<16 | uint32(0)<<5
	if insn5 != wantCmp {
		t.Errorf("guard CMP = 0x%08x, want 0x%08x", insn5, wantCmp)
	}

	// [24-27] B.HS deopt (imm19=16)
	insn6 := binary.LittleEndian.Uint32(buf[24:28])
	if (insn6 & 0xFF000000) != 0x54000000 {
		t.Errorf("guard B.cond base wrong")
	}
	if insn6&0xF != uint32(CondHS) {
		t.Errorf("guard B.cond cond = 0x%x, want 0x%x (HS)", insn6&0xF, CondHS)
	}
	if (insn6>>5)&0x7FFFF != 16 {
		t.Errorf("guard B.HS imm19 = %d, want 16", (insn6>>5)&0x7FFFF)
	}
}

// TestPJ8_EmitArithSpeculativeBinopArm64_Layout verifies the fast path
// byte-level layout
// (32 bytes = LDR + FMOV + LDR + FMOV + binop + FMOV + STR + RET).
func TestPJ8_EmitArithSpeculativeBinopArm64_Layout(t *testing.T) {
	var buf []byte
	buf = EmitArithSpeculativeBinopArm64(buf, ArithOpAddArm64, 2, 0, 1)

	if len(buf) != EncodedArithSpecBinopArm64Len {
		t.Errorf("len = %d, want %d", len(buf), EncodedArithSpecBinopArm64Len)
	}

	// [4-7] FMOV d0, x0 (0x9E670000)
	insn1 := binary.LittleEndian.Uint32(buf[4:8])
	if insn1 != 0x9E670000 {
		t.Errorf("fast FMOV d0, x0 = 0x%08x, want 0x9E670000", insn1)
	}

	// [16-19] FADD d0, d0, d1
	insn4 := binary.LittleEndian.Uint32(buf[16:20])
	wantFadd := uint32(0x1E602800) | uint32(1)<<16
	if insn4 != wantFadd {
		t.Errorf("fast FADD = 0x%08x, want 0x%08x", insn4, wantFadd)
	}

	// [20-23] FMOV x0, d0
	insn5 := binary.LittleEndian.Uint32(buf[20:24])
	if insn5 != 0x9E660000 {
		t.Errorf("fast FMOV x0, d0 = 0x%08x, want 0x9E660000", insn5)
	}

	// [24-27] STR x0, [x26 + 16] (a=2, imm12=2)
	insn6 := binary.LittleEndian.Uint32(buf[24:28])
	if (insn6 & 0xFFC00000) != 0xF9000000 {
		t.Errorf("fast STR base wrong")
	}

	// [28-31] RET
	insn7 := binary.LittleEndian.Uint32(buf[28:32])
	if insn7 != 0xd65f03c0 {
		t.Errorf("fast RET = 0x%08x, want 0xd65f03c0", insn7)
	}
}

// TestPJ8_EmitArithSpeculativeBinopWithGuardArm64_Length verifies the full
// template byte length (108 bytes).
func TestPJ8_EmitArithSpeculativeBinopWithGuardArm64_Length(t *testing.T) {
	cases := []struct {
		name  string
		opSel uint8
	}{
		{"ADD", ArithOpAddArm64},
		{"SUB", ArithOpSubArm64},
		{"MUL", ArithOpMulArm64},
		{"DIV", ArithOpDivArm64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			buf = EmitArithSpeculativeBinopWithGuardArm64(buf,
				tc.opSel, 2, 0, 1, 0xFFFCDEAD_DEADBEEF)
			const wantLen = 108
			if len(buf) != wantLen {
				t.Errorf("总长度 = %d, want %d (%s)", len(buf), wantLen, tc.name)
			}
			if len(buf) != EncodedArithSpecBinopWithGuardArm64Len {
				t.Errorf("len = %d, want %d (%s)",
					len(buf), EncodedArithSpecBinopWithGuardArm64Len, tc.name)
			}
		})
	}
}

// TestPJ8_EmitArithSpeculativeBinopWithGuardArm64_DeoptBlock verifies the deopt
// block sits at the template tail (MOV x0, deoptCode + RET = 20 bytes).
func TestPJ8_EmitArithSpeculativeBinopWithGuardArm64_DeoptBlock(t *testing.T) {
	const deoptCode uint64 = 0xFFFCDEAD_DEADBEEF
	var buf []byte
	buf = EmitArithSpeculativeBinopWithGuardArm64(buf, ArithOpAddArm64,
		2, 0, 1, deoptCode)

	// deopt block sits at buf[88..107], 20 bytes total (MOV x0, imm64 16 + RET 4)
	const deoptStart = 88
	if len(buf) < deoptStart+20 {
		t.Fatalf("buf too short for deopt block")
	}

	// First instruction MOVZ x0, deoptCode[15:0] = 0xBEEF
	insn0 := binary.LittleEndian.Uint32(buf[deoptStart : deoptStart+4])
	if (insn0 & 0xFFE00000) != 0xD2800000 {
		t.Errorf("deopt MOVZ base wrong")
	}
	imm0 := (insn0 >> 5) & 0xFFFF
	if imm0 != 0xBEEF {
		t.Errorf("deopt MOVZ imm[15:0] = 0x%04x, want 0xBEEF", imm0)
	}

	// Last 4 bytes are RET
	retInsn := binary.LittleEndian.Uint32(buf[deoptStart+16 : deoptStart+20])
	if retInsn != 0xd65f03c0 {
		t.Errorf("deopt RET = 0x%08x, want 0xd65f03c0", retInsn)
	}
}

// TestPJ8_EmitArithSpeculativeBinopRegKWithGuardArm64_Length verifies the
// reg-K WithGuard template byte length (92 = 28 + 44 + 20).
func TestPJ8_EmitArithSpeculativeBinopRegKWithGuardArm64_Length(t *testing.T) {
	cases := []struct {
		name  string
		opSel uint8
	}{
		{"ADD", ArithOpAddArm64},
		{"SUB", ArithOpSubArm64},
		{"MUL", ArithOpMulArm64},
		{"DIV", ArithOpDivArm64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			buf = EmitArithSpeculativeBinopRegKWithGuardArm64(buf,
				tc.opSel, 2, 0, 0x3FF0000000000000, 0xCAFEBABE)
			const wantLen = 92
			if len(buf) != wantLen {
				t.Errorf("len = %d, want %d (%s)", len(buf), wantLen, tc.name)
			}
		})
	}
	if EncodedArithSpecBinopRegKWithGuardArm64Len != 92 {
		t.Errorf("EncodedArithSpecBinopRegKWithGuardArm64Len = %d, want 92",
			EncodedArithSpecBinopRegKWithGuardArm64Len)
	}
}

// TestPJ8_EmitArithSpeculativeBinopRegKWithGuardArm64_KValueBurnedIn verifies
// the kvalue is burned into the [32-47] segment via MOVZ+MOVK×3
// (guard 28 + LDR 4 + FMOV 4 + MOV imm64).
func TestPJ8_EmitArithSpeculativeBinopRegKWithGuardArm64_KValueBurnedIn(t *testing.T) {
	const kvalue uint64 = 0xDEAD_BEEF_CAFE_FACE
	var buf []byte
	buf = EmitArithSpeculativeBinopRegKWithGuardArm64(buf,
		ArithOpAddArm64, 2, 0, kvalue, 0xCAFEBABE)

	if len(buf) < 48 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// MOV imm64 starts at offset = 28 (guard) + 4 (LDR) + 4 (FMOV) = 36
	expectedImm16 := [4]uint16{
		uint16(kvalue & 0xFFFF),         // 0xFACE
		uint16((kvalue >> 16) & 0xFFFF), // 0xCAFE
		uint16((kvalue >> 32) & 0xFFFF), // 0xBEEF
		uint16((kvalue >> 48) & 0xFFFF), // 0xDEAD
	}
	for i, exp := range expectedImm16 {
		off := 36 + i*4
		insn := binary.LittleEndian.Uint32(buf[off : off+4])
		got := uint16((insn >> 5) & 0xFFFF)
		if got != exp {
			t.Errorf("kvalue movz/movk[%d]@%d imm16 = 0x%04x, want 0x%04x", i, off, got, exp)
		}
	}
}

// TestPJ8_EmitArithSpeculativeBinopRegKWithGuardArm64_DeoptBlock verifies the
// deopt block sits at [72-91]: MOV x0, deoptCode + RET.
func TestPJ8_EmitArithSpeculativeBinopRegKWithGuardArm64_DeoptBlock(t *testing.T) {
	const deoptCode uint64 = 0xDEAD_BEEF_CAFE_BABE
	var buf []byte
	buf = EmitArithSpeculativeBinopRegKWithGuardArm64(buf,
		ArithOpAddArm64, 2, 0, 0x3FF0000000000000, deoptCode)

	if len(buf) < 92 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [72-75] MOVZ x0, deoptCode[15:0]=0xBABE
	insn := binary.LittleEndian.Uint32(buf[72:76])
	imm0 := (insn >> 5) & 0xFFFF
	if imm0 != 0xBABE {
		t.Errorf("[72] MOVZ x0 imm[15:0] = 0x%04x, want 0xBABE", imm0)
	}

	// [88-91] RET
	insn = binary.LittleEndian.Uint32(buf[88:92])
	if insn != 0xd65f03c0 {
		t.Errorf("[88] RET = 0x%08x, want 0xd65f03c0", insn)
	}
}

// TestPJ8_EmitArithSpeculativeChainKKWithGuardArm64_Length verifies the
// chain-KK WithGuard template byte length (116 = 28 + 68 + 20).
func TestPJ8_EmitArithSpeculativeChainKKWithGuardArm64_Length(t *testing.T) {
	cases := []struct {
		name     string
		op1, op2 uint8
	}{
		{"MUL+ADD", ArithOpMulArm64, ArithOpAddArm64},
		{"SUB+DIV", ArithOpSubArm64, ArithOpDivArm64},
		{"ADD+SUB", ArithOpAddArm64, ArithOpSubArm64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			buf = EmitArithSpeculativeChainKKWithGuardArm64(buf,
				tc.op1, tc.op2, 2, 0,
				0x3FF0000000000000, 0x4000000000000000, 0xCAFEBABE)
			const wantLen = 116
			if len(buf) != wantLen {
				t.Errorf("len = %d, want %d (%s)", len(buf), wantLen, tc.name)
			}
		})
	}
	if EncodedArithSpecChainKKWithGuardArm64Len != 116 {
		t.Errorf("EncodedArithSpecChainKKWithGuardArm64Len = %d, want 116",
			EncodedArithSpecChainKKWithGuardArm64Len)
	}
}

// TestPJ8_EmitArithSpeculativeChainKKWithGuardArm64_DeoptBlock verifies the
// deopt block sits at [96-115].
func TestPJ8_EmitArithSpeculativeChainKKWithGuardArm64_DeoptBlock(t *testing.T) {
	const deoptCode uint64 = 0xDEAD_BEEF_CAFE_BABE
	var buf []byte
	buf = EmitArithSpeculativeChainKKWithGuardArm64(buf,
		ArithOpMulArm64, ArithOpAddArm64, 2, 0,
		0x4000000000000000, 0x3FF0000000000000, deoptCode)

	if len(buf) < 116 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [96-99] MOVZ x0, deoptCode[15:0]=0xBABE
	insn := binary.LittleEndian.Uint32(buf[96:100])
	imm0 := (insn >> 5) & 0xFFFF
	if imm0 != 0xBABE {
		t.Errorf("[96] MOVZ x0 imm[15:0] = 0x%04x, want 0xBABE", imm0)
	}

	// [112-115] RET
	insn = binary.LittleEndian.Uint32(buf[112:116])
	if insn != 0xd65f03c0 {
		t.Errorf("[112] RET = 0x%08x, want 0xd65f03c0", insn)
	}
}

// TestPJ8_EmitArithSpeculativeBinopRegKWithGuardArm64_GuardSegment verifies the
// guard segment IsNumber check sits at [0-27]: LDR R(B) + MOV qNanBoxBase +
// CMP + B.HS.
//   - [0-3]   LDR x0, [x26 + b*8]
//   - [4-19]  MOV x1, qNanBoxBase imm64
//   - [20-23] CMP x0, x1
//   - [24-27] B.HS deopt (imm19=12 = (72-24)/4)
func TestPJ8_EmitArithSpeculativeBinopRegKWithGuardArm64_GuardSegment(t *testing.T) {
	var buf []byte
	buf = EmitArithSpeculativeBinopRegKWithGuardArm64(buf,
		ArithOpAddArm64, 2, 5, 0x3FF0000000000000, 0xCAFEBABE)

	if len(buf) < 28 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// [0-3] LDR x0, [x26 + 40] (b=5, byteOff=40, imm12=5)
	insn := binary.LittleEndian.Uint32(buf[0:4])
	wantLdr := uint32(0xF9400000) | uint32(5)<<10 | uint32(26)<<5
	if insn != wantLdr {
		t.Errorf("[0] LDR x0, [x26 + 40] = 0x%08x, want 0x%08x", insn, wantLdr)
	}

	// [20-23] CMP x0, x1
	insn = binary.LittleEndian.Uint32(buf[20:24])
	wantCmp := uint32(0xEB00001F) | uint32(1)<<16 | uint32(0)<<5
	if insn != wantCmp {
		t.Errorf("[20] CMP x0, x1 = 0x%08x, want 0x%08x", insn, wantCmp)
	}

	// [24-27] B.HS deopt
	insn = binary.LittleEndian.Uint32(buf[24:28])
	if (insn & 0xFF000000) != 0x54000000 {
		t.Errorf("[24] B.cond base wrong: 0x%08x", insn)
	}
	if insn&0xF != uint32(CondHS) {
		t.Errorf("[24] B.cond cond = 0x%x, want 0x%x (HS)", insn&0xF, CondHS)
	}
	// imm19 = (72 - 24) / 4 = 12 (no-safepoint form)
	gotImm19 := (insn >> 5) & 0x7FFFF
	if gotImm19 != 12 {
		t.Errorf("[24] B.HS imm19 = %d, want 12 ((72-24)/4)", gotImm19)
	}
}

// TestPJ8_EmitArithSpeculativeBinopRegKWithGuardArm64_BodyFopBytes verifies the
// body segment FOP d0,d0,d1 byte encoding at offset 56-59 (FADD/FSUB/FMUL/FDIV).
func TestPJ8_EmitArithSpeculativeBinopRegKWithGuardArm64_BodyFopBytes(t *testing.T) {
	cases := []struct {
		name    string
		opSel   uint8
		fopBase uint32
	}{
		{"FADD d0, d0, d1", ArithOpAddArm64, 0x1E602800},
		{"FSUB d0, d0, d1", ArithOpSubArm64, 0x1E603800},
		{"FMUL d0, d0, d1", ArithOpMulArm64, 0x1E600800},
		{"FDIV d0, d0, d1", ArithOpDivArm64, 0x1E601800},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			buf = EmitArithSpeculativeBinopRegKWithGuardArm64(buf,
				tc.opSel, 2, 0, 0x3FF0000000000000, 0xCAFEBABE)
			if len(buf) < 60 {
				t.Fatalf("buf too short: %d", len(buf))
			}
			// FOP at offset 56-59 (guard 28 + LDR 4 + FMOV 4 + MOV imm64 16 + FMOV 4)
			insn := binary.LittleEndian.Uint32(buf[56:60])
			want := tc.fopBase | uint32(1)<<16 | uint32(0)<<5 | uint32(0)
			if insn != want {
				t.Errorf("[56] %s = 0x%08x, want 0x%08x", tc.name, insn, want)
			}
		})
	}
}

// TestPJ8_EmitArithSpeculativeChainKKWithGuardArm64_K1K2BurnedIn verifies K1/K2
// are burned into the [36-51] / [60-75] segments via MOVZ+MOVK×3 (guard 28 +
// LDR 4 + FMOV 4, then the first 16-byte imm64 segment; then FMOV 4 + FOP 4,
// then the second 16-byte imm64 segment).
func TestPJ8_EmitArithSpeculativeChainKKWithGuardArm64_K1K2BurnedIn(t *testing.T) {
	const k1value uint64 = 0xDEAD_BEEF_CAFE_FACE
	const k2value uint64 = 0xFEED_F00D_BABE_C001
	var buf []byte
	buf = EmitArithSpeculativeChainKKWithGuardArm64(buf,
		ArithOpMulArm64, ArithOpAddArm64, 2, 0, k1value, k2value, 0xCAFEBABE)

	if len(buf) < 76 {
		t.Fatalf("buf too short: %d", len(buf))
	}

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
	// K1 at [36-51] (guard 28 + LDR 4 + FMOV 4)
	verifyMov("K1", 36, k1value)
	// K2 at [60-75] (after K1 segment 24 bytes = 16 + FMOV 4 + FOP1 4)
	verifyMov("K2", 60, k2value)
}

// TestPJ8_EmitArithSpeculativeChainKKWithGuardArm64_BodyFopsBytes verifies FOP1
// at offset 56-59 + FOP2 at offset 80-83.
func TestPJ8_EmitArithSpeculativeChainKKWithGuardArm64_BodyFopsBytes(t *testing.T) {
	cases := []struct {
		name     string
		op1, op2 uint8
		fop1Base uint32
		fop2Base uint32
	}{
		{"MUL+ADD", ArithOpMulArm64, ArithOpAddArm64, 0x1E600800, 0x1E602800},
		{"SUB+DIV", ArithOpSubArm64, ArithOpDivArm64, 0x1E603800, 0x1E601800},
		{"DIV+MUL", ArithOpDivArm64, ArithOpMulArm64, 0x1E601800, 0x1E600800},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			buf = EmitArithSpeculativeChainKKWithGuardArm64(buf,
				tc.op1, tc.op2, 2, 0,
				0x3FF0000000000000, 0x4000000000000000, 0xCAFEBABE)
			if len(buf) < 84 {
				t.Fatalf("buf too short: %d", len(buf))
			}

			// FOP1 at offset 56-59
			insn := binary.LittleEndian.Uint32(buf[56:60])
			want1 := tc.fop1Base | uint32(1)<<16 | uint32(0)<<5 | uint32(0)
			if insn != want1 {
				t.Errorf("[56] op1 = 0x%08x, want 0x%08x", insn, want1)
			}
			// FOP2 at offset 80-83
			insn = binary.LittleEndian.Uint32(buf[80:84])
			want2 := tc.fop2Base | uint32(1)<<16 | uint32(0)<<5 | uint32(0)
			if insn != want2 {
				t.Errorf("[80] op2 = 0x%08x, want 0x%08x", insn, want2)
			}
		})
	}
}
