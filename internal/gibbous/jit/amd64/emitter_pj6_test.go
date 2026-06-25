//go:build wangshu_p4 && linux && amd64

package amd64

import "testing"

// TestPJ6_LoadKReturnTemplate 完整模板封装等价于「mov rax, imm; ret」。
//
// 验证 EmitLoadKReturnTemplate 字节级完全等同于 EmitMovRaxImm64 + EmitRet 拼接。
func TestPJ6_LoadKReturnTemplate_Equiv(t *testing.T) {
	const konst = uint64(0xdeadbeef)

	var single []byte
	single = EmitLoadKReturnTemplate(single, konst)

	var combined []byte
	combined = EmitMovRaxImm64(combined, konst)
	combined = EmitRet(combined)

	if len(single) != EncodedLoadKReturnTemplateLen {
		t.Fatalf("template length = %d, want %d", len(single), EncodedLoadKReturnTemplateLen)
	}
	if len(single) != len(combined) {
		t.Fatalf("template vs combined length differ: %d vs %d", len(single), len(combined))
	}
	for i := range single {
		if single[i] != combined[i] {
			t.Errorf("byte %d differs: 0x%02x vs 0x%02x", i, single[i], combined[i])
		}
	}
}

// TestPJ6_PrologEpilog_RoundTrip prolog + epilog 完整 round-trip,
// rax 中间被改但通过 push/pop 恢复(简化版仅验 prolog/epilog 字节编码无崩,
// 不验真实 callee-saved 协议——那由 trampoline_full_amd64.s 直接实装)。
func TestPJ6_PrologEpilog_RoundTrip(t *testing.T) {
	const sent = uint64(0xfeedface)

	// prolog; mov rax, sent; epilog; ret
	var buf []byte
	buf = EmitProlog(buf)
	buf = EmitMovRaxImm64(buf, sent)
	buf = EmitEpilog(buf)
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	got := CallJIT(page.Addr())
	if got != sent {
		t.Errorf("RAX with prolog/epilog = 0x%x, want 0x%x", got, sent)
	}
}

// TestPJ6_PrologEpilog_StackPreserved 多次调用 prolog/epilog 嵌套不破坏栈。
//
// 验证 callee-saved push/pop 配对,栈指针前后一致(否则下次 CALL 会段错)。
func TestPJ6_PrologEpilog_StackPreserved(t *testing.T) {
	const sent = uint64(0x123456789abcdef0)

	// prolog; mov rax, sent; epilog; ret
	var buf []byte
	buf = EmitProlog(buf)
	buf = EmitMovRaxImm64(buf, sent)
	buf = EmitEpilog(buf)
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	addr := page.Addr()
	for i := 0; i < 10000; i++ {
		got := CallJIT(addr)
		if got != sent {
			t.Fatalf("call #%d: RAX = 0x%x, want 0x%x", i, got, sent)
		}
	}
}

// TestPJ6_EncodedLengths PJ6 新增长度常量。
func TestPJ6_EncodedLengths(t *testing.T) {
	if got := len(EmitLoadKReturnTemplate(nil, 0)); got != EncodedLoadKReturnTemplateLen {
		t.Errorf("EmitLoadKReturnTemplate = %d, want %d", got, EncodedLoadKReturnTemplateLen)
	}
	if got := len(EmitProlog(nil)); got != EncodedPrologLen {
		t.Errorf("EmitProlog = %d, want %d", got, EncodedPrologLen)
	}
	if got := len(EmitEpilog(nil)); got != EncodedEpilogLen {
		t.Errorf("EmitEpilog = %d, want %d", got, EncodedEpilogLen)
	}
}
