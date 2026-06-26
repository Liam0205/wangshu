//go:build wangshu_p4 && arm64 && linux

package arm64

import (
	"encoding/binary"
	"testing"
)

// TestPJ8_EmitMovX0Imm64Encoding 验证 arm64 movz+movk 序列字节编码正确。
//
// 不真执行段(linux/arm64 mmap+W^X 工程组件已落地,但端到端执行需 trampoline
// asm 留 PJ8+ 完整版接入);本测试只验「字节编码符合 ARM64 ISA」——以官方
// movz/movk 编码格式为参照(`Arm Architecture Reference Manual`)。
func TestPJ8_EmitMovX0Imm64Encoding(t *testing.T) {
	const imm = uint64(0xdead_beef_cafe_babe)

	var buf []byte
	buf = EmitMovX0Imm64(buf, imm)
	buf = EmitRet(buf)

	if len(buf) != EncodedMovX0Imm64Len+EncodedRetLen {
		t.Fatalf("encoded length = %d, want %d", len(buf), EncodedMovX0Imm64Len+EncodedRetLen)
	}

	// 解析每条 32-bit instruction,验证 movz/movk imm16 字段
	expected := []uint16{0xbabe, 0xcafe, 0xbeef, 0xdead}
	for i, want := range expected {
		insn := binary.LittleEndian.Uint32(buf[i*4 : (i+1)*4])
		// movz/movk 指令:base 0xD2800000 (movz) / 0xF2800000 (movk),
		// imm16 在 bit [20:5]
		gotImm16 := uint16((insn >> 5) & 0xFFFF)
		if gotImm16 != want {
			t.Errorf("insn %d imm16 = 0x%04x, want 0x%04x", i, gotImm16, want)
		}
	}

	// 第 5 条应是 ret(0xd65f03c0 LE)
	gotRet := binary.LittleEndian.Uint32(buf[16:20])
	if gotRet != 0xd65f03c0 {
		t.Errorf("ret encoding = 0x%08x, want 0xd65f03c0", gotRet)
	}
}

// TestPJ8_EmitMovXdImm64 通用 Xd 寄存器版本(rd != 0)。
func TestPJ8_EmitMovXdImm64(t *testing.T) {
	var buf []byte
	buf = EmitMovXdImm64(buf, 5, 0x12345678) // mov x5, ...

	if len(buf) != EncodedMovXdImm64Len {
		t.Fatalf("len = %d, want %d", len(buf), EncodedMovXdImm64Len)
	}
	// 第一条 movz x5, imm[15:0] = 0x5678,Rd 字段(low 5 bits)= 5
	insn0 := binary.LittleEndian.Uint32(buf[0:4])
	if insn0&0x1F != 5 {
		t.Errorf("Rd = %d, want 5", insn0&0x1F)
	}
	imm0 := uint16((insn0 >> 5) & 0xFFFF)
	if imm0 != 0x5678 {
		t.Errorf("imm[15:0] = 0x%04x, want 0x5678", imm0)
	}
}

// TestPJ8_EmitMovXdFromXn:mov Xd, Xn(reg-to-reg)= ORR Xd, XZR, Xn.
func TestPJ8_EmitMovXdFromXn(t *testing.T) {
	var buf []byte
	buf = EmitMovXdFromXn(buf, 3, 5) // mov x3, x5

	if len(buf) != EncodedMovXdFromXnLen {
		t.Fatalf("len = %d, want %d", len(buf), EncodedMovXdFromXnLen)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	// ORR base 0xAA000000,Rn=5 (bit 16-20),Rm/shift_reg=31(XZR,bit 16-20 等同 Rn 字段)
	// 实际编码:Rn=Rm=5(我们 emit 时 Rn 字段填 Rm=5),Rm 字段(bit 16-20)
	// wait — 我们的实现:Rn=5 在 bit 16-20,XZR(31)在 bit 5-9,Rd=3 bit 0-4
	if insn&0x1F != 3 {
		t.Errorf("Rd = %d, want 3", insn&0x1F)
	}
	if (insn>>5)&0x1F != 31 {
		t.Errorf("Rn(should be XZR=31) = %d", (insn>>5)&0x1F)
	}
	if (insn>>16)&0x1F != 5 {
		t.Errorf("Rm = %d, want 5", (insn>>16)&0x1F)
	}
}

// TestPJ8_EmitAddXdImm12:add Xd, Xn, #imm12.
func TestPJ8_EmitAddXdImm12(t *testing.T) {
	var buf []byte
	buf = EmitAddXdImm12(buf, 0, 0, 100) // add x0, x0, #100

	if len(buf) != EncodedAddXdImm12Len {
		t.Fatalf("len = %d, want %d", len(buf), EncodedAddXdImm12Len)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	// base 0x91000000,Rd=0,Rn=0,imm12=100
	if insn&0x1F != 0 {
		t.Errorf("Rd = %d, want 0", insn&0x1F)
	}
	if (insn>>10)&0xFFF != 100 {
		t.Errorf("imm12 = %d, want 100", (insn>>10)&0xFFF)
	}
}

// TestPJ8_EmitSubXdImm12:sub Xd, Xn, #imm12.
func TestPJ8_EmitSubXdImm12(t *testing.T) {
	var buf []byte
	buf = EmitSubXdImm12(buf, 1, 1, 1) // sub x1, x1, #1

	if len(buf) != EncodedSubXdImm12Len {
		t.Fatalf("len = %d, want %d", len(buf), EncodedSubXdImm12Len)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	// base 0xD1000000
	if (insn & 0xFFE00000) != 0xD1000000 {
		t.Errorf("opcode = 0x%08x, want base 0xD1000000", insn&0xFFE00000)
	}
}

// TestPJ8_EmitB:b imm26 unconditional branch.
func TestPJ8_EmitB(t *testing.T) {
	var buf []byte
	negImm := int32(-2)
	buf = EmitB(buf, negImm) // b backward 2 instructions

	if len(buf) != EncodedBLen {
		t.Fatalf("len = %d, want %d", len(buf), EncodedBLen)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	// base 0x14000000;imm26 = -2 in two's complement 26-bit = 0x3FFFFFE
	wantInsn := uint32(0x14000000) | (uint32(negImm) & 0x03FFFFFF)
	if insn != wantInsn {
		t.Errorf("b -2 = 0x%08x, want 0x%08x", insn, wantInsn)
	}
}
