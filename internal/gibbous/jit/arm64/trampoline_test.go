//go:build wangshu_p4 && arm64 && (linux || (darwin && cgo)) && !wangshu_qemu

// 本测试真去 MmapCode + CallJITFull / CallJITSpec 跳进 mmap 段执行 arm64
// 指令(承 §9.20.9 trampoline 协议)。
//
// **build tag 设计**(承 F2 QEMU + F3-#3b 真物理 M1 SIGSEGV 修复):
//   - linux || (darwin && cgo):**两端共用同份 Plan 9 ABI0 asm**(承 509d5af
//     build tag 扩展 + F3-#3b STP/LDP 偏移 +8 修复 LR slot 覆盖 bug,真物理
//     M1 上 round-trip 5/5 + spec round-trip 4/4 + 1000 轮 callee-saved 验证
//     全过)。原本只 linux only 的限制是 F3-#3a 调试期占位,根因 isolate 到
//     trampoline 后改回。
//   - !wangshu_qemu:QEMU user-mode emulation 不真模拟 i-cache flush /
//     PROT_EXEC,实测 mmap 段 RET 字节处 SIGSEGV(承 F2 + ci.yml L94-99)。
//
// CI 当前 linux/amd64 主路径 + linux/arm64 QEMU(skip 本测试)+ macos-latest
// M1 物理 e2e(跑本测试)三路覆盖。

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
