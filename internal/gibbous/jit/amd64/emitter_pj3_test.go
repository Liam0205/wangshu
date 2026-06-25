//go:build wangshu_p4 && linux && amd64

package amd64

import "testing"

// TestPJ3_EmitMovImm64ToReg 各寄存器 mov 编码 + mmap 执行(prove-the-path
// 命中证据,承 06 §3.7 直线族扩展)。
//
// 经 EmitMovImm64ToReg(reg=N) → mmap 段 → 段 ret → callJIT 拿 RAX 验证:
// 当 reg=RAX 时,RAX = imm;当 reg ≠ RAX 时,RAX = 段 RET 时的当前 RAX 值
// (= mov 之前的 RAX 值,本测试不依赖该值——仅测 mov 字节编码无 SEGV/SIGILL)。
//
// **PJ3 简化形态边界**:本测试主验「mov regN, imm64 字节编码工作」,完整
// MOVE 模板(R(A) := R(B))还需 jitContext.valueStackBase + load/store 序列
// 留 PJ4+ 启用切栈时同批落地。
func TestPJ3_EmitMovImm64ToReg(t *testing.T) {
	cases := []struct {
		regNum uint8
		imm    uint64
	}{
		{0, 0xdeadbeef},         // RAX
		{1, 0xcafebabe},         // RCX
		{2, 0xfeedface},         // RDX
		{3, 0x0123456789abcdef}, // RBX
		{6, 0xa5a5a5a5},         // RSI
		{7, ^uint64(0)},         // RDI
	}
	for _, tc := range cases {
		t.Run("", func(t *testing.T) {
			// emit:mov regN, imm; mov rax, sentinel; ret
			// (我们让 RAX 最后被加载为 sentinel 验证 trampoline 拿 RAX 工作;
			// regN 的写入只验证字节编码无崩——若编码错段会 SIGILL/SEGV)
			sentinel := uint64(0xfeedfacecafebabe)
			var buf []byte
			buf = EmitMovImm64ToReg(buf, tc.regNum, tc.imm)
			buf = EmitMovRaxImm64(buf, sentinel)
			buf = EmitRet(buf)

			page, err := MmapCode(buf)
			if err != nil {
				t.Fatalf("MmapCode failed: %v", err)
			}
			defer func() { _ = page.Munmap() }()

			got := CallJIT(page.Addr())
			if got != sentinel {
				t.Errorf("RAX after seq = 0x%x, want sentinel 0x%x (mov regN encoding may be wrong)", got, sentinel)
			}
		})
	}
}

// TestPJ3_EmitNop nop 字节编码 + mmap 执行不崩(承 06 §3.7 padding 用例)。
func TestPJ3_EmitNop(t *testing.T) {
	imm := uint64(0xdeadbeef)
	// emit:nop; nop; mov rax, imm; ret
	var buf []byte
	for i := 0; i < 16; i++ {
		buf = EmitNop(buf)
	}
	buf = EmitMovRaxImm64(buf, imm)
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	got := CallJIT(page.Addr())
	if got != imm {
		t.Errorf("RAX after seq with NOPs = 0x%x, want 0x%x", got, imm)
	}
}

// TestPJ3_EncodedLengths 编码长度常量与 emit 函数实际写入字节数一致(防漂移)。
func TestPJ3_EncodedLengths(t *testing.T) {
	cases := []struct {
		name     string
		encode   func() []byte
		expected int
	}{
		{
			name:     "mov rax, imm64",
			encode:   func() []byte { return EmitMovRaxImm64(nil, 0) },
			expected: EncodedMovRaxImm64Len,
		},
		{
			name:     "ret",
			encode:   func() []byte { return EmitRet(nil) },
			expected: EncodedRetLen,
		},
		{
			name:     "mov regN, imm64",
			encode:   func() []byte { return EmitMovImm64ToReg(nil, 3, 0) },
			expected: EncodedMovImm64ToRegLen,
		},
		{
			name:     "nop",
			encode:   func() []byte { return EmitNop(nil) },
			expected: EncodedNopLen,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := tc.encode()
			if len(buf) != tc.expected {
				t.Errorf("%s encoded %d bytes, want %d", tc.name, len(buf), tc.expected)
			}
		})
	}
}
