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
