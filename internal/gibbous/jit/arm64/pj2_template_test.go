//go:build wangshu_p4 && arm64 && linux

package arm64

import (
	"encoding/binary"
	"testing"
)

// pj2_template_test.go —— PJ8 arm64 PJ2 投机 ADD/SUB/MUL/DIV 字节级模板单测。
//
// **不真 mmap+RX 跑**(arm64 trampoline asm 留物理 self-hosted runner);
// 本测试纯字节级布局验证:每条指令字节编码 + 模板总长度 + 段间偏移。

// TestPJ8_EmitIsNumberGuardArm64_Layout 验 arm64 IsNumber guard 字节级布局
// (28 字节 = LDR 4 + MOV imm64 16 + CMP 4 + B.cond 4)。
func TestPJ8_EmitIsNumberGuardArm64_Layout(t *testing.T) {
	var buf []byte
	buf = EmitIsNumberGuardArm64(buf, 5, 16) // reg=5, imm19=16(到 deopt)

	if len(buf) != EncodedIsNumberGuardArm64Len {
		t.Errorf("len = %d, want %d", len(buf), EncodedIsNumberGuardArm64Len)
	}
	const wantLen = 28
	if len(buf) != wantLen {
		t.Errorf("总长度 = %d, want %d", len(buf), wantLen)
	}

	// [0-3] LDR x0, [x26 + 40](reg=5 → byteOff 40)
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

// TestPJ8_EmitArithSpeculativeBinopArm64_Layout 验 fast path 字节级布局
// (32 字节 = LDR + FMOV + LDR + FMOV + binop + FMOV + STR + RET)。
func TestPJ8_EmitArithSpeculativeBinopArm64_Layout(t *testing.T) {
	var buf []byte
	buf = EmitArithSpeculativeBinopArm64(buf, ArithOpAddArm64, 2, 0, 1)

	if len(buf) != EncodedArithSpecBinopArm64Len {
		t.Errorf("len = %d, want %d", len(buf), EncodedArithSpecBinopArm64Len)
	}

	// [4-7] FMOV d0, x0(0x9E670000)
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

	// [24-27] STR x0, [x26 + 16](a=2, imm12=2)
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

// TestPJ8_EmitArithSpeculativeBinopWithGuardArm64_Length 验完整模板字节
// 长度(108 字节)。
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

// TestPJ8_EmitArithSpeculativeBinopWithGuardArm64_DeoptBlock 验 deopt block
// 在模板末尾(MOV x0, deoptCode + RET = 20 字节)。
func TestPJ8_EmitArithSpeculativeBinopWithGuardArm64_DeoptBlock(t *testing.T) {
	const deoptCode uint64 = 0xFFFCDEAD_DEADBEEF
	var buf []byte
	buf = EmitArithSpeculativeBinopWithGuardArm64(buf, ArithOpAddArm64,
		2, 0, 1, deoptCode)

	// deopt block 在 buf[88..107] 共 20 字节(MOV x0, imm64 16 + RET 4)
	const deoptStart = 88
	if len(buf) < deoptStart+20 {
		t.Fatalf("buf too short for deopt block")
	}

	// 第 1 条 MOVZ x0, deoptCode[15:0] = 0xBEEF
	insn0 := binary.LittleEndian.Uint32(buf[deoptStart : deoptStart+4])
	if (insn0 & 0xFFE00000) != 0xD2800000 {
		t.Errorf("deopt MOVZ base wrong")
	}
	imm0 := (insn0 >> 5) & 0xFFFF
	if imm0 != 0xBEEF {
		t.Errorf("deopt MOVZ imm[15:0] = 0x%04x, want 0xBEEF", imm0)
	}

	// 末 4 字节是 RET
	retInsn := binary.LittleEndian.Uint32(buf[deoptStart+16 : deoptStart+20])
	if retInsn != 0xd65f03c0 {
		t.Errorf("deopt RET = 0x%08x, want 0xd65f03c0", retInsn)
	}
}

// TestPJ8_EmitArithSpeculativeBinopRegKWithGuardArm64_Length 验 reg-K
// WithGuard 模板字节长度(92 = 28 + 44 + 20)。
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

// TestPJ8_EmitArithSpeculativeBinopRegKWithGuardArm64_KValueBurnedIn 验
// kvalue 经 MOVZ+MOVK×3 烧进 [32-47] 段(guard 28 + LDR 4 + FMOV 4 + MOV imm64)。
func TestPJ8_EmitArithSpeculativeBinopRegKWithGuardArm64_KValueBurnedIn(t *testing.T) {
	const kvalue uint64 = 0xDEAD_BEEF_CAFE_FACE
	var buf []byte
	buf = EmitArithSpeculativeBinopRegKWithGuardArm64(buf,
		ArithOpAddArm64, 2, 0, kvalue, 0xCAFEBABE)

	if len(buf) < 48 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// MOV imm64 起 offset = 28(guard)+ 4(LDR)+ 4(FMOV) = 36
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

// TestPJ8_EmitArithSpeculativeBinopRegKWithGuardArm64_DeoptBlock 验 deopt
// block 在 [72-91]:MOV x0, deoptCode + RET。
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

// TestPJ8_EmitArithSpeculativeChainKKWithGuardArm64_Length 验 chain-KK
// WithGuard 模板字节长度(116 = 28 + 68 + 20)。
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

// TestPJ8_EmitArithSpeculativeChainKKWithGuardArm64_DeoptBlock 验 deopt
// block 在 [96-115]。
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
