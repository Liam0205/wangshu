//go:build wangshu_p4 && linux && amd64

package amd64

import "testing"

// TestPJ4_EmitCmpRaxImm32_RoundTrip 比较 + 跳转 mmap 段执行(承 06 §3.2
// IsNumber guard 物理基础)。
//
// 经构造「rax = sentA / cmp rax, sentA / je .skip / mov rax, sentB / .skip:
// ret」序列(若 cmp 不工作或 jcc 不跳,RAX 会是 sentB 而非 sentA)。
//
// **PJ4 简化形态边界**:本测试主验「cmp + jcc 字节编码工作 + jcc 真按
// flag 跳」,完整 IsNumber guard 模板(NaN-box 边界 + OSR exit 路径)留 PJ4+
// 启用切栈时同批落地。
func TestPJ4_EmitCmpRaxImm32_Equal(t *testing.T) {
	// rax = 100; cmp rax, 100; je after_branch; mov rax, 0xbad; after_branch: ret
	// JE rel8: 74 ii(2 字节,本测试用 6 字节 jae rel32 替代,因为 rel8 jcc 未实装;
	// 实际验证「rax >= 100 jae after_branch」, 100 >= 100 ⇒ jae 跳过 mov bad)
	const sent = 100
	const bad = uint64(0xbad)

	// 计算 jae rel32:跳过 mov rax, 0xbad(11 字节,EncodedMovRaxImm64Len)
	// jae rel32 自身 6 字节,rel32 是「下条指令起的偏移」,跳过的距离 = 11 字节
	// (mov rax, bad 占 10 字节 + 0 字节其它 = 10 字节;但 EncodedMovRaxImm64Len = 10)
	rel := int32(EncodedMovRaxImm64Len)

	var buf []byte
	buf = EmitMovRaxImm64(buf, sent) // 10 字节 — rax = 100
	buf = EmitCmpRaxImm32(buf, sent) // 7  字节 — cmp rax, 100
	buf = EmitJaeRel32(buf, rel)     // 6  字节 — jae +10(若 rax >= 100 跳过 mov bad)
	buf = EmitMovRaxImm64(buf, bad)  // 10 字节 — mov rax, 0xbad(被跳过)
	buf = EmitRet(buf)               // 1  字节 — ret(rax = sent)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	got := CallJIT(page.Addr())
	if got != sent {
		t.Errorf("RAX = 0x%x, want 0x%x (jae should skip mov bad)", got, sent)
	}
}

// TestPJ4_EmitJmpRel32_RoundTrip 无条件 jmp 跳过 dead code 段。
func TestPJ4_EmitJmpRel32_RoundTrip(t *testing.T) {
	const good = uint64(0xfeedface)
	const bad = uint64(0xdeadbeef)

	// jmp +10(跳过 mov rax, bad);mov rax, bad;mov rax, good;ret
	rel := int32(EncodedMovRaxImm64Len)

	var buf []byte
	buf = EmitJmpRel32(buf, rel)     // 5  字节 — jmp +10
	buf = EmitMovRaxImm64(buf, bad)  // 10 字节 — 被跳过
	buf = EmitMovRaxImm64(buf, good) // 10 字节 — rax = good
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	got := CallJIT(page.Addr())
	if got != good {
		t.Errorf("RAX = 0x%x, want 0x%x (jmp should skip mov bad)", got, good)
	}
}

// TestPJ4_EncodedLengths PJ4 新增编码长度常量验证。
func TestPJ4_EncodedLengths(t *testing.T) {
	cases := []struct {
		name     string
		encode   func() []byte
		expected int
	}{
		{
			name:     "cmp rax, imm32",
			encode:   func() []byte { return EmitCmpRaxImm32(nil, 0) },
			expected: EncodedCmpRaxImm32Len,
		},
		{
			name:     "jmp rel32",
			encode:   func() []byte { return EmitJmpRel32(nil, 0) },
			expected: EncodedJmpRel32Len,
		},
		{
			name:     "jae rel32",
			encode:   func() []byte { return EmitJaeRel32(nil, 0) },
			expected: EncodedJaeRel32Len,
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
