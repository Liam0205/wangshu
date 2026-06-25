//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"testing"
	"unsafe"
)

// TestPJ2_CallJITFull_RoundTrip 验证完整 trampoline asm 工作:
//   - 保存 callee-saved 寄存器(rbx/rbp/r12/r13/r15)
//   - 装 r15 = jitCtx(实参传入 uintptr)
//   - CALL mmap 段 + ret 拿 RAX
//   - 恢复 callee-saved
//
// 模板与 PJ1 简化形态同款(`mov rax, imm; ret`),验证「带 callee-saved 保护
// 的完整路径」也能跑通——这是 PJ3+ 启动时引入 helper CALL 子调用的物理基础。
func TestPJ2_CallJITFull_RoundTrip(t *testing.T) {
	cases := []uint64{
		0,
		1,
		0xdeadbeef,
		0xcafebabedeadbeef,
		^uint64(0),
	}
	// 准备一个 jitCtx(本测试不解引用,只验 trampoline 路径正确)
	ctx := struct{ dummy uint64 }{dummy: 0xfeedface}
	jitCtxAddr := uintptr(unsafe.Pointer(&ctx))

	for _, imm := range cases {
		t.Run("", func(t *testing.T) {
			var buf []byte
			buf = EmitMovRaxImm64(buf, imm)
			buf = EmitRet(buf)

			page, err := MmapCode(buf)
			if err != nil {
				t.Fatalf("MmapCode failed: %v", err)
			}
			defer func() { _ = page.Munmap() }()

			got := CallJITFull(page.Addr(), jitCtxAddr)
			if got != imm {
				t.Errorf("CallJITFull returned 0x%x, expected 0x%x", got, imm)
			}
		})
	}
}

// TestPJ2_CallJITFull_CalleeSavedPreserved 完整 trampoline 不污染 caller 的
// callee-saved 寄存器(rbx/rbp/r12/r13/r15)。
//
// 验证手段:Go 调多次 CallJITFull 之后跑一些 Go 代码(分配 + 计算),若
// callee-saved 被破坏,Go runtime 在调度 / GC 时会 SEGV 或得到错值。本测试
// 主要靠「不 SEGV + Go runtime 行为正常」做隐式验证;-race 加压更严。
func TestPJ2_CallJITFull_CalleeSavedPreserved(t *testing.T) {
	imm := uint64(0xfeedface)
	var buf []byte
	buf = EmitMovRaxImm64(buf, imm)
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	ctx := struct{ dummy uint64 }{dummy: 0xfeedface}
	jitCtxAddr := uintptr(unsafe.Pointer(&ctx))

	const N = 1000
	// Go 端跑一些会用到 callee-saved 的代码(slice 分配触发 GC,计算用 r12+)
	accum := uint64(0)
	for i := 0; i < N; i++ {
		got := CallJITFull(page.Addr(), jitCtxAddr)
		if got != imm {
			t.Fatalf("call #%d: got 0x%x, want 0x%x", i, got, imm)
		}
		// 显式做点会用 callee-saved 的 Go 计算
		s := make([]uint64, 4)
		for j := range s {
			s[j] = uint64(i + j)
			accum += s[j]
		}
	}
	if accum == 0 {
		t.Fatal("accumulator should be non-zero after N iterations")
	}
}

// BenchmarkPJ2_CallJITFull 完整 trampoline 单 CALL 成本(对位 PJ1 简化形态
// 1.96ns,完整版预期 ~3-5ns,即 PJ2 引入的「保存恢复 5 个 callee-saved」开销)。
func BenchmarkPJ2_CallJITFull(b *testing.B) {
	imm := uint64(0xdeadbeef)
	var buf []byte
	buf = EmitMovRaxImm64(buf, imm)
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		b.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	ctx := struct{ dummy uint64 }{dummy: 0xfeedface}
	jitCtxAddr := uintptr(unsafe.Pointer(&ctx))

	addr := page.Addr()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got := CallJITFull(addr, jitCtxAddr)
		if got != imm {
			b.Fatalf("call #%d: got 0x%x, want 0x%x", i, got, imm)
		}
	}
}
