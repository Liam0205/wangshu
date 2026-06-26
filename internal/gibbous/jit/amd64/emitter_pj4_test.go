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

// —— PJ4 表 IC inline 字节级原语(承 emitter_pj4.go)——

func TestPJ4_EmitMovqR14FromR15Disp(t *testing.T) {
	cases := []struct {
		disp int32
		want []byte
	}{
		{0, []byte{0x4D, 0x8B, 0xB7, 0, 0, 0, 0}},
		{8, []byte{0x4D, 0x8B, 0xB7, 8, 0, 0, 0}},
		{16, []byte{0x4D, 0x8B, 0xB7, 16, 0, 0, 0}},
		{-1, []byte{0x4D, 0x8B, 0xB7, 0xFF, 0xFF, 0xFF, 0xFF}},
	}
	for i, tc := range cases {
		got := EmitMovqR14FromR15Disp(nil, tc.disp)
		if len(got) != EncodedMovqR14FromR15DispLen {
			t.Errorf("case %d: len=%d, want %d", i, len(got), EncodedMovqR14FromR15DispLen)
		}
		if string(got) != string(tc.want) {
			t.Errorf("case %d disp=%d: got %x, want %x", i, tc.disp, got, tc.want)
		}
	}
}

func TestPJ4_EmitMovqRaxFromR14PlusRcxDisp(t *testing.T) {
	got := EmitMovqRaxFromR14PlusRcxDisp(nil, 40)
	want := []byte{0x49, 0x8B, 0x84, 0x0E, 40, 0, 0, 0}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
	if len(got) != EncodedMovqRaxFromR14PlusRcxDispLen {
		t.Errorf("len=%d, want %d", len(got), EncodedMovqRaxFromR14PlusRcxDispLen)
	}
}

func TestPJ4_EmitMovqRcxFromRax(t *testing.T) {
	got := EmitMovqRcxFromRax(nil)
	want := []byte{0x48, 0x89, 0xC1}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

func TestPJ4_EmitShrRcxImm8(t *testing.T) {
	got := EmitShrRcxImm8(nil, 32)
	want := []byte{0x48, 0xC1, 0xE9, 32}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

func TestPJ4_EmitCmpEcxImm32(t *testing.T) {
	got := EmitCmpEcxImm32(nil, 0x12345678)
	want := []byte{0x81, 0xF9, 0x78, 0x56, 0x34, 0x12}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

func TestPJ4_EmitAndRaxImm32(t *testing.T) {
	got := EmitAndRaxImm32(nil, -1)
	want := []byte{0x48, 0x81, 0xE0, 0xFF, 0xFF, 0xFF, 0xFF}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

// TestPJ4_EmitShrRaxImm8 —— shr rax, 48(严密 IsTable guard 字节级第一步)。
// 编码:48 C1 E8 30(30=48 十进制)。Intel SDM Vol.2B SHR /5。
func TestPJ4_EmitShrRaxImm8(t *testing.T) {
	got := EmitShrRaxImm8(nil, 48)
	want := []byte{0x48, 0xC1, 0xE8, 48}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

// TestPJ4_EmitCmpEaxImm32 —— cmp eax, 0xFFFC(严密 IsTable guard 字节级
// 第二步,验高 16 位 tag = TagTable 0xFFFC)。
// 编码:3D FC FF 00 00(short form,无 ModRM,RAX/EAX 隐式)。
func TestPJ4_EmitCmpEaxImm32(t *testing.T) {
	got := EmitCmpEaxImm32(nil, 0xFFFC)
	want := []byte{0x3D, 0xFC, 0xFF, 0x00, 0x00}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

// TestPJ4_EmitMovRdxImm64 —— mov rdx, imm64(NodeHit 烧 stableKey)。
// 编码:48 BA imm64_LE_bytes。
func TestPJ4_EmitMovRdxImm64(t *testing.T) {
	got := EmitMovRdxImm64(nil, 0xFFFB_DEAD_BEEF_CAFE) // 模拟 string NaN-box
	want := []byte{0x48, 0xBA,
		0xFE, 0xCA, 0xEF, 0xBE, 0xAD, 0xDE, 0xFB, 0xFF}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

// TestPJ4_EmitCmpRaxRdx —— cmp rax, rdx(NodeHit 验 NodeKey == stableKey)。
// 编码:48 39 D0(REX.W / opcode CMP r/m64,r64 / ModRM mod=11 reg=010 rm=000)。
func TestPJ4_EmitCmpRaxRdx(t *testing.T) {
	got := EmitCmpRaxRdx(nil)
	want := []byte{0x48, 0x39, 0xD0}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

// TestPJ4_EmitMovqMemR14PlusRcxFromRax —— 反向 SIB store:
// `mov [r14 + rcx*1 + disp32], rax`(8 字节,PJ4 SETTABLE IC inline 用)。
// 编码:49 89 84 0E disp32(LE)。
// SIB 0E = scale=00 index=001(rcx) base=110(r14 w/ REX.B)。
func TestPJ4_EmitMovqMemR14PlusRcxFromRax(t *testing.T) {
	got := EmitMovqMemR14PlusRcxFromRax(nil, 0x12345678)
	want := []byte{0x49, 0x89, 0x84, 0x0E, 0x78, 0x56, 0x34, 0x12}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}

	// 小 disp 也用 disp32(模板字节布局自洽,即便 disp 较小也保持 8 字节)
	got2 := EmitMovqMemR14PlusRcxFromRax(nil, 32)
	want2 := []byte{0x49, 0x89, 0x84, 0x0E, 0x20, 0x00, 0x00, 0x00}
	if string(got2) != string(want2) {
		t.Errorf("got %x, want %x", got2, want2)
	}
}
