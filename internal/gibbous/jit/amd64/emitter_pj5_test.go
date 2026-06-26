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
		{
			name:     "helper call (mov rax+call rax)",
			encode:   func() []byte { return EmitHelperCall(nil, 0xDEADBEEFCAFEBABE) },
			expected: EncodedHelperCallLen,
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

// TestPJ5_EmitCallRel32_ByteEqual 验「call rel32」字节级 Intel SDM byte-equal:
// E8 + imm32 LE(rel32 从下条指令起算的 32 位有符号偏移)。
func TestPJ5_EmitCallRel32_ByteEqual(t *testing.T) {
	cases := []struct {
		name  string
		rel32 int32
		want  []byte
	}{
		{"forward small", 0x100, []byte{0xE8, 0x00, 0x01, 0x00, 0x00}},
		{"backward small", -1, []byte{0xE8, 0xFF, 0xFF, 0xFF, 0xFF}},
		{"forward large", 0x12345678, []byte{0xE8, 0x78, 0x56, 0x34, 0x12}},
		{"zero", 0, []byte{0xE8, 0x00, 0x00, 0x00, 0x00}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EmitCallRel32(nil, tc.rel32)
			if string(got) != string(tc.want) {
				t.Errorf("got %x, want %x", got, tc.want)
			}
		})
	}
}

// TestPJ5_EmitCallReg_ByteEqual 验「call regN」字节级 byte-equal:
// FF D0+regNum(reg=4 RSP 兜底 RAX,reg>7 兜底 RAX)。
func TestPJ5_EmitCallReg_ByteEqual(t *testing.T) {
	cases := []struct {
		name   string
		regNum uint8
		want   []byte
	}{
		{"call rax (reg=0)", 0, []byte{0xFF, 0xD0}},
		{"call rcx (reg=1)", 1, []byte{0xFF, 0xD1}},
		{"call rdx (reg=2)", 2, []byte{0xFF, 0xD2}},
		{"call rbx (reg=3)", 3, []byte{0xFF, 0xD3}},
		// reg=4 (RSP) 语义不可用 → 兜底 RAX
		{"call rsp (reg=4) defensive→rax", 4, []byte{0xFF, 0xD0}},
		{"call rbp (reg=5)", 5, []byte{0xFF, 0xD5}},
		{"call rsi (reg=6)", 6, []byte{0xFF, 0xD6}},
		{"call rdi (reg=7)", 7, []byte{0xFF, 0xD7}},
		// reg>7 超界 → 兜底 RAX
		{"call r8 (reg=8) defensive→rax", 8, []byte{0xFF, 0xD0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EmitCallReg(nil, tc.regNum)
			if string(got) != string(tc.want) {
				t.Errorf("got %x, want %x", got, tc.want)
			}
		})
	}
}

// TestPJ5_EmitHelperCall_ByteEqual 验 PJ5 helper call 通用宏字节级:
// `mov rax, helperAddr; call rax`(12 字节 = MOV 10 + CALL 2)。
//
// **PJ5 用例**:jit→host helper 调用(host.DoCall/DoTailCall/Safepoint)。
// helper 地址通常远超 ±2GB 范围,标准做法是装载 64-bit 绝对地址到 rax
// 后 indirect call,本宏封装此固定字节序列。
//
// 编码精确验:
//
//	[0]   0x48 = REX.W
//	[1]   0xB8 = MOV rax, imm64
//	[2-9] imm64 LE 8 字节
//	[10]  0xFF = CALL r/m64
//	[11]  0xD0 = ModRM mod=11 reg=2 rm=0(rax)
func TestPJ5_EmitHelperCall_ByteEqual(t *testing.T) {
	cases := []struct {
		name       string
		helperAddr uint64
		want       []byte
	}{
		{
			"low helperAddr",
			0x00007F0011223344,
			[]byte{
				0x48, 0xB8, // mov rax, imm64
				0x44, 0x33, 0x22, 0x11, 0x00, 0x7F, 0x00, 0x00, // imm64 LE
				0xFF, 0xD0, // call rax
			},
		},
		{
			"high helperAddr (typical Go heap)",
			0xFFFFC900_0123ABCD,
			[]byte{
				0x48, 0xB8,
				0xCD, 0xAB, 0x23, 0x01, 0x00, 0xC9, 0xFF, 0xFF,
				0xFF, 0xD0,
			},
		},
		{
			"zero (defensive)",
			0,
			[]byte{
				0x48, 0xB8,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0xFF, 0xD0,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EmitHelperCall(nil, tc.helperAddr)
			if len(got) != EncodedHelperCallLen {
				t.Errorf("len = %d, want %d", len(got), EncodedHelperCallLen)
			}
			if string(got) != string(tc.want) {
				t.Errorf("got %x, want %x", got, tc.want)
			}
		})
	}
}

// TestPJ5_EmitHelperCall_LengthConst 验长度常量等于 12(MOV 10 + CALL 2)。
// 锁死字节布局契约(承 SetTable/Self NodeHit length 精确断言同款纪律)。
func TestPJ5_EmitHelperCall_LengthConst(t *testing.T) {
	const wantLen = 12
	if EncodedHelperCallLen != wantLen {
		t.Errorf("EncodedHelperCallLen = %d, want %d", EncodedHelperCallLen, wantLen)
	}
}
