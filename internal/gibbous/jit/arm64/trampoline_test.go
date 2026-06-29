//go:build wangshu_p4 && arm64 && (linux || (darwin && cgo)) && !wangshu_qemu

// 本测试真去 MmapCode + CallJITFull / CallJITSpec 跳进 mmap 段执行 arm64
// 指令(承 §9.20.9 trampoline 协议)。
//
// **build tag !wangshu_qemu 排除 QEMU job**(承 F2 修复 + ci.yml L94-99
// 注释):QEMU user-mode emulation 不真模拟 i-cache flush / PROT_EXEC,
// 实测在 mmap 段 RET 字节处 SIGSEGV(addr=0xc308,即 0xd65f03c0 RET 的低
// 16 bit)。真物理 arm64 host(linux/arm64 self-hosted runner 或
// darwin/arm64 macos-latest)必跑本测试,QEMU 路径显式 skip 编译。
//
// CI 配合:.github/workflows/ci.yml test-arm64 QEMU job 跑 `go test
// -tags 'wangshu_p4 wangshu_profile wangshu_qemu' ./internal/gibbous/jit/arm64/...`
// 时,本文件不编译,只跑字节编码字节级单测(纯计算路径,QEMU 可信)。

package arm64

import (
	"testing"
	"unsafe"
)

// TestPJ8_CallJITFull_RoundTrip 验完整 trampoline asm 工作:
//   - 保存 callee-saved 寄存器(X19-X27)
//   - 装 X27 = jitCtx(实参传入 uintptr)
//   - BL mmap 段 + ret 拿 X0
//   - 恢复 callee-saved
//
// 模板与 PJ8 简化形态同款(mov X0, imm; ret),验证「带 callee-saved
// 保护的完整路径」也能跑通——这是 PJ3+ 启动时引入 helper CALL 子调用的
// 物理基础(对位 amd64 TestPJ2_CallJITFull_RoundTrip)。
func TestPJ8_CallJITFull_RoundTrip(t *testing.T) {
	cases := []uint64{
		0,
		1,
		0xdeadbeef,
		0xcafebabedeadbeef,
		^uint64(0),
	}
	ctx := struct{ dummy uint64 }{dummy: 0xfeedface}
	jitCtxAddr := uintptr(unsafe.Pointer(&ctx))

	for _, imm := range cases {
		t.Run("", func(t *testing.T) {
			var buf []byte
			buf = EmitMovX0Imm64(buf, imm)
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

// TestPJ8_CallJITSpec_RoundTrip 验 spec trampoline asm 工作:
//   - 保存 callee-saved 寄存器(X19-X27)
//   - 装 X27 = jitCtx + **X26 = vsBase**(spec 形态多装 vsBase)
//   - BL mmap 段 + ret 拿 X0
//   - 恢复 callee-saved
//
// 模板同 callJITFull 简化形态(mov X0, imm; ret),vsBase 实参不在段内
// 解引用(模板不读 X26),只验 trampoline 路径正确(对位 amd64 callJITSpec
// 的同款验证形态)。物理 runner 端到端再验 PJ2 投机模板 + PJ3 FORLOOP
// body/body2/RegLimit 真用 [x26+disp] 寻址值栈。
func TestPJ8_CallJITSpec_RoundTrip(t *testing.T) {
	cases := []uint64{
		0,
		0xdeadbeef,
		0xcafebabe_deadbeef,
		^uint64(0),
	}
	ctx := struct{ dummy uint64 }{dummy: 0xfeedface}
	jitCtxAddr := uintptr(unsafe.Pointer(&ctx))
	// vsBase 用占位 dummy 地址(模板不读它,trampoline 装入但 mmap 段不解引用)
	vsBase := struct{ dummy uint64 }{dummy: 0xcafefeed}
	vsBaseAddr := uintptr(unsafe.Pointer(&vsBase))

	for _, imm := range cases {
		t.Run("", func(t *testing.T) {
			var buf []byte
			buf = EmitMovX0Imm64(buf, imm)
			buf = EmitRet(buf)

			page, err := MmapCode(buf)
			if err != nil {
				t.Fatalf("MmapCode failed: %v", err)
			}
			defer func() { _ = page.Munmap() }()

			got := CallJITSpec(page.Addr(), jitCtxAddr, vsBaseAddr)
			if got != imm {
				t.Errorf("CallJITSpec returned 0x%x, expected 0x%x", got, imm)
			}
		})
	}
}

// TestPJ8_CallJITSpec_CalleeSavedPreserved spec trampoline 不污染 caller
// 的 callee-saved 寄存器(X19-X27)。
//
// 验证手段:Go 调多次 CallJITSpec 之后跑一些 Go 代码(分配 + 计算),若
// callee-saved 被破坏,Go runtime 在调度 / GC 时会 SEGV 或得到错值。本
// 测试主要靠「不 SEGV + Go runtime 行为正常」做隐式验证;-race 加压更严。
func TestPJ8_CallJITSpec_CalleeSavedPreserved(t *testing.T) {
	imm := uint64(0xfeedface)
	var buf []byte
	buf = EmitMovX0Imm64(buf, imm)
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	ctx := struct{ dummy uint64 }{dummy: 0xfeedface}
	jitCtxAddr := uintptr(unsafe.Pointer(&ctx))
	vsBase := struct{ dummy uint64 }{dummy: 0xcafefeed}
	vsBaseAddr := uintptr(unsafe.Pointer(&vsBase))

	// 跑 N 次 + 中间穿插 Go 分配 + 计算,若 callee-saved 被破坏,这里
	// 会 SEGV 或得到错值
	const N = 1000
	sum := uint64(0)
	for i := 0; i < N; i++ {
		got := CallJITSpec(page.Addr(), jitCtxAddr, vsBaseAddr)
		if got != imm {
			t.Errorf("iter %d: CallJITSpec returned 0x%x, expected 0x%x", i, got, imm)
			return
		}
		// 穿插 Go 计算 + 触发 GC 偶发场景(make 短生命周期 slice)
		s := make([]byte, 16)
		for j := range s {
			s[j] = byte(i + j)
		}
		sum += uint64(s[0])
	}
	if sum == 0 {
		// 防 sum 被 Go 编译器优化掉
		t.Errorf("sum should not be 0 (escape sentinel)")
	}
}
