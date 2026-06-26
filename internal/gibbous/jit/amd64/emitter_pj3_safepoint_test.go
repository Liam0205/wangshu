//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"testing"
)

// emitter_pj3_safepoint_test.go —— PJ3 safepoint check 字节级编码 +
// 字节布局验证(承 docs/design/p4-method-jit/05-system-pipeline.md §1.2.2
// + §6.3 回边检查点)。
//
// 字节级测试 only——真在 mmap+RX 跑 safepoint check 需要 jitContext
// 接入 + r15 装载,这块工程留 PJ3 真接入。

// TestPJ3_EmitCmpByteR15DispImm8_Encoding 字节级验证「cmp byte [r15+disp], 0」
// 编码:41 80 BF disp32 imm8。
func TestPJ3_EmitCmpByteR15DispImm8_Encoding(t *testing.T) {
	cases := []struct {
		disp int32
		imm  byte
		want []byte
	}{
		{0, 0, []byte{0x41, 0x80, 0xBF, 0x00, 0x00, 0x00, 0x00, 0x00}},
		{0x10, 0, []byte{0x41, 0x80, 0xBF, 0x10, 0x00, 0x00, 0x00, 0x00}},
		{0x10, 1, []byte{0x41, 0x80, 0xBF, 0x10, 0x00, 0x00, 0x00, 0x01}},
		{0x100, 0, []byte{0x41, 0x80, 0xBF, 0x00, 0x01, 0x00, 0x00, 0x00}},
		// 负 disp32
		{-1, 0, []byte{0x41, 0x80, 0xBF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00}},
	}
	for i, tc := range cases {
		got := EmitCmpByteR15DispImm8(nil, tc.disp, tc.imm)
		if len(got) != EncodedCmpByteR15DispImm8Len {
			t.Errorf("case %d: len=%d, want %d", i, len(got), EncodedCmpByteR15DispImm8Len)
		}
		if string(got) != string(tc.want) {
			t.Errorf("case %d: got %x, want %x", i, got, tc.want)
		}
	}
}

// TestPJ3_SafepointCheck_Encoding 复合字节序列:cmp + jne(safepoint
// check 真实形态)。验证组合后字节级布局,后续 PJ3 真接入 FORLOOP 字节级
// 内联时用。
func TestPJ3_SafepointCheck_Encoding(t *testing.T) {
	// 形态:cmp byte [r15+0x10], 0; jne rel32_to_exit_stub(rel32=0x100)
	var buf []byte
	buf = EmitCmpByteR15DispImm8(buf, 0x10, 0)
	buf = EmitJneRel32(buf, 0x100)

	wantLen := EncodedCmpByteR15DispImm8Len + EncodedJccRel32Len
	if len(buf) != wantLen {
		t.Fatalf("safepoint check len=%d, want %d", len(buf), wantLen)
	}

	// 前 8 字节 = cmp byte [r15+0x10], 0
	wantCmp := []byte{0x41, 0x80, 0xBF, 0x10, 0x00, 0x00, 0x00, 0x00}
	if string(buf[:8]) != string(wantCmp) {
		t.Errorf("cmp byte = %x, want %x", buf[:8], wantCmp)
	}
	// 后 6 字节 = jne rel32(0F 85 rel32)
	wantJne := []byte{0x0F, 0x85, 0x00, 0x01, 0x00, 0x00}
	if string(buf[8:]) != string(wantJne) {
		t.Errorf("jne = %x, want %x", buf[8:], wantJne)
	}
}
