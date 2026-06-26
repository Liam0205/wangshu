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
		0x3FF0000000000000)

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
	buf = EmitForLoopEmptyConstArm64(buf, kInit, kLimit, kStep)

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
