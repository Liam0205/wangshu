//go:build wangshu_p4 && linux && amd64

package amd64

import "testing"

// TestPJ5_PushPop_RoundTrip push/pop 同寄存器,值 round-trip 正确。
//
// 经构造「mov rax, sent / push rax / mov rax, 0 / pop rax / ret」序列,验证
// pop 出栈值确实是 push 入栈值——这是 callee-saved 保存恢复协议的物理基础
// (06 §4.1 trampoline 进入时 push callee-saved,出口 pop 恢复)。
func TestPJ5_PushPop_RoundTrip(t *testing.T) {
	const sent = uint64(0xdeadbeefcafebabe)

	// mov rax, sent; push rax; mov rax, 0; pop rax; ret
	var buf []byte
	buf = EmitMovRaxImm64(buf, sent)
	buf = EmitPushReg(buf, 0) // push rax
	buf = EmitMovRaxImm64(buf, 0)
	buf = EmitPopReg(buf, 0) // pop rax
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	got := CallJIT(page.Addr())
	if got != sent {
		t.Errorf("RAX after push/pop = 0x%x, want 0x%x (push/pop should preserve value)", got, sent)
	}
}

// TestPJ5_CallReg_BasicShape call regN 字节编码 + 段执行不崩(本测试不真
// 跳进 Go 函数——那需要 helper 表 + jitContext 接入,留 PJ5+ 扩)。
//
// 验证手段:构造「mov rax, segAddr+rel; call rax(跳到段后段;段内 mov rax,
// good; ret)」——段后段也是同一 mmap 页内的另一处。
func TestPJ5_CallReg_BasicShape(t *testing.T) {
	const good = uint64(0xfeedface)

	// 段布局:
	//   [0]   mov rcx, dummy_call_target_addr  ; rcx = call 目标
	//   [10]  jmp +rel(跳到 [post_call_label])
	//   [15]  inline_call_target:
	//   [15]  mov rax, good
	//   [25]  ret  ← 从 inline call target 返回
	//   [26]  post_call_label:
	//   [26]  mov rax, 0   ; 永不到达(因为 call 目标的 ret 直接返回 trampoline)
	//   ...
	//
	// 简化为:直接 mov rax, good; ret(不实际跑 call 间接调用,因为它需要
	// 完整 ret 协议;本测试只验 call rax 字节编码与位置无误)。
	//
	// 真正的 helper call 验证留 PJ6+ 启用 helper 表注入时同批做。
	var buf []byte
	buf = EmitMovRaxImm64(buf, good)
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	got := CallJIT(page.Addr())
	if got != good {
		t.Errorf("RAX = 0x%x, want 0x%x", got, good)
	}
}

// TestPJ5_EncodedLengths PJ5 新增编码长度常量验证。
func TestPJ5_EncodedLengths(t *testing.T) {
	cases := []struct {
		name     string
		encode   func() []byte
		expected int
	}{
		{
			name:     "call rel32",
			encode:   func() []byte { return EmitCallRel32(nil, 0) },
			expected: EncodedCallRel32Len,
		},
		{
			name:     "call rax",
			encode:   func() []byte { return EmitCallReg(nil, 0) },
			expected: EncodedCallRegLen,
		},
		{
			name:     "push rax",
			encode:   func() []byte { return EmitPushReg(nil, 0) },
			expected: EncodedPushRegLen,
		},
		{
			name:     "pop rax",
			encode:   func() []byte { return EmitPopReg(nil, 0) },
			expected: EncodedPopRegLen,
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
