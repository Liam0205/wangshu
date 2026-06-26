//go:build wangshu_p4 && linux && amd64

package amd64

import "testing"

// emitter_pj3_loopops_test.go —— PJ3 整数循环 emit 原语(inc/dec r64 +
// mov r64,imm32-signed)字节级验证。承 docs/design/p4-method-jit/
// 05-system-pipeline.md §6.3 回边 + §1.2.2 抢占检查的整数侧支持。

// TestPJ3_EmitIncReg64_Encoding:48 FF C0+rd 各寄存器。
func TestPJ3_EmitIncReg64_Encoding(t *testing.T) {
	cases := []struct {
		reg  uint8
		want []byte
	}{
		{0, []byte{0x48, 0xFF, 0xC0}}, // inc rax
		{1, []byte{0x48, 0xFF, 0xC1}}, // inc rcx
		{2, []byte{0x48, 0xFF, 0xC2}}, // inc rdx
		{3, []byte{0x48, 0xFF, 0xC3}}, // inc rbx
		{7, []byte{0x48, 0xFF, 0xC7}}, // inc rdi
	}
	for i, tc := range cases {
		got := EmitIncReg64(nil, tc.reg)
		if len(got) != EncodedIncDecReg64Len {
			t.Errorf("case %d: len=%d, want %d", i, len(got), EncodedIncDecReg64Len)
		}
		if string(got) != string(tc.want) {
			t.Errorf("case %d reg=%d: got %x, want %x", i, tc.reg, got, tc.want)
		}
	}
}

// TestPJ3_EmitDecReg64_Encoding:48 FF C8+rd 各寄存器。
func TestPJ3_EmitDecReg64_Encoding(t *testing.T) {
	cases := []struct {
		reg  uint8
		want []byte
	}{
		{0, []byte{0x48, 0xFF, 0xC8}}, // dec rax
		{1, []byte{0x48, 0xFF, 0xC9}}, // dec rcx
		{2, []byte{0x48, 0xFF, 0xCA}}, // dec rdx
		{3, []byte{0x48, 0xFF, 0xCB}}, // dec rbx
		{7, []byte{0x48, 0xFF, 0xCF}}, // dec rdi
	}
	for i, tc := range cases {
		got := EmitDecReg64(nil, tc.reg)
		if len(got) != EncodedIncDecReg64Len {
			t.Errorf("case %d: len=%d, want %d", i, len(got), EncodedIncDecReg64Len)
		}
		if string(got) != string(tc.want) {
			t.Errorf("case %d reg=%d: got %x, want %x", i, tc.reg, got, tc.want)
		}
	}
}

// TestPJ3_EmitMovReg64Imm32SignExt_Encoding:48 C7 C0+rd imm32 各 imm.
func TestPJ3_EmitMovReg64Imm32SignExt_Encoding(t *testing.T) {
	cases := []struct {
		reg  uint8
		imm  int32
		want []byte
	}{
		{0, 0, []byte{0x48, 0xC7, 0xC0, 0, 0, 0, 0}},                      // mov rax, 0
		{1, 100, []byte{0x48, 0xC7, 0xC1, 100, 0, 0, 0}},                  // mov rcx, 100
		{0, -1, []byte{0x48, 0xC7, 0xC0, 0xFF, 0xFF, 0xFF, 0xFF}},         // mov rax, -1
		{2, 0x12345678, []byte{0x48, 0xC7, 0xC2, 0x78, 0x56, 0x34, 0x12}}, // mov rdx
	}
	for i, tc := range cases {
		got := EmitMovReg64Imm32SignExt(nil, tc.reg, tc.imm)
		if len(got) != EncodedMovReg64Imm32SignExtLen {
			t.Errorf("case %d: len=%d, want %d",
				i, len(got), EncodedMovReg64Imm32SignExtLen)
		}
		if string(got) != string(tc.want) {
			t.Errorf("case %d reg=%d imm=%d: got %x, want %x",
				i, tc.reg, tc.imm, got, tc.want)
		}
	}
}
