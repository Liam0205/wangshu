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

// TestPJ3_PatchRel32 字节级验证 forward jmp fixup tool。
func TestPJ3_PatchRel32(t *testing.T) {
	// 模拟:emit 一个 jmp rel32 占位 0,然后回填真实 rel32
	var buf []byte
	buf = EmitJmpRel32(buf, 0) // 占位 5 字节:E9 00 00 00 00

	// rel32 起点 = jmp 起点 + 1(skip E9 opcode)
	PatchRel32(buf, 1, 0x12345678)

	want := []byte{0xE9, 0x78, 0x56, 0x34, 0x12}
	if string(buf) != string(want) {
		t.Errorf("after patch: got %x, want %x", buf, want)
	}

	// 负 rel32 patch
	var buf2 []byte
	buf2 = EmitJmpRel32(buf2, 0)
	PatchRel32(buf2, 1, -1)
	if string(buf2[1:]) != string([]byte{0xFF, 0xFF, 0xFF, 0xFF}) {
		t.Errorf("negative rel32 patch: got %x, want FF FF FF FF", buf2[1:])
	}
}

// TestPJ3_ForwardFixup_RoundTrip 模拟 PJ3 真接入路径:emit jcc placeholder
// rel32=0 → emit body → 知道 body 长度后 patch 回填 rel32。
func TestPJ3_ForwardFixup_RoundTrip(t *testing.T) {
	var buf []byte
	// 1. emit jne placeholder rel32=0(6 字节)
	buf = EmitJneRel32(buf, 0)
	jccRel32Off := len(buf) - 4 // rel32 起点

	// 2. emit body(假装 13 字节,实际任意)
	body := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	buf = append(buf, body...)

	// 3. patch:rel32 = body 长度(跳过 body 到目标)
	PatchRel32(buf, jccRel32Off, int32(len(body)))

	// 验证 patch 后 jcc rel32 字段
	gotRel32 := int32(buf[jccRel32Off]) |
		int32(buf[jccRel32Off+1])<<8 |
		int32(buf[jccRel32Off+2])<<16 |
		int32(buf[jccRel32Off+3])<<24
	if gotRel32 != int32(len(body)) {
		t.Errorf("patched rel32 = %d, want %d", gotRel32, len(body))
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
