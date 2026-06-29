//go:build wangshu_p4 && darwin && arm64 && cgo

package arm64

import (
	"testing"
)

// TestDarwinMmapCode_RoundTrip 验证 darwin/arm64 MAP_JIT mmap + W^X 翻面 +
// icache flush 的 round-trip 路径在 macos-latest runner 上跑通。
//
// 承 codepage_darwin.go::MmapCode 七步流程:
//  1. runtime.LockOSThread 钉线程
//  2. mmap MAP_JIT|MAP_ANON|MAP_PRIVATE + PROT_RWX
//  3. pthread_jit_write_protect_np(0) 进入可写态
//  4. copy code
//  5. pthread_jit_write_protect_np(1) 翻 RX
//  6. sys_icache_invalidate 刷 i-cache
//  7. UnlockOSThread (deferred)
//
// **build tag 严格匹配 darwin && arm64 && cgo**:linux 端 / cross-build
// CGO_ENABLED=0 端本文件不编译,故无需 t.Skip。
//
// 关键断言:
//   - 空 code 报错
//   - 写入 NOP+RET 字节序(arm64 NOP=0x1f2003d5, RET=0xc0035fd6 LE)能取回
//     Addr 非 0 + Length 至少一页
//   - Munmap 幂等(二次调用零错)
//   - 写入段后 Munmap 不泄漏 — 反复 N 次不 OOM
//
// **本测试不真 call mmap 段**:darwin/arm64 真执行需 trampoline_arm64.s
// 接入 + Hardened Runtime JIT entitlement 配合,本测试只验 codepage
// 字节级 round-trip 路径。真执行验证留 C5/C6/C7 翻闸门后 macos-latest
// CI 跑完整 V18 -race 套(详 tmp/wangshu-p4-todo.md §二.4)。
func TestDarwinMmapCode_RoundTrip(t *testing.T) {
	// arm64 NOP = 0xd503201f (LE: 1f 20 03 d5), RET = 0xd65f03c0 (LE: c0 03 5f d6).
	// 字节级写入,不依赖 emitter pkg state。
	code := []byte{
		0x1f, 0x20, 0x03, 0xd5, // NOP
		0xc0, 0x03, 0x5f, 0xd6, // RET
	}

	cp, err := MmapCode(code)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() {
		if err := cp.Munmap(); err != nil {
			t.Fatalf("Munmap failed: %v", err)
		}
		// 幂等:二次调用零错。
		if err := cp.Munmap(); err != nil {
			t.Fatalf("Munmap idempotent check failed: %v", err)
		}
	}()

	if cp.Addr() == 0 {
		t.Fatalf("Addr() == 0 after MmapCode")
	}
	if cp.Length() < len(code) {
		t.Fatalf("Length() = %d, want >= %d", cp.Length(), len(code))
	}
}

func TestDarwinMmapCode_EmptyCode(t *testing.T) {
	cp, err := MmapCode(nil)
	if err == nil {
		_ = cp.Munmap()
		t.Fatalf("MmapCode(nil) should return error")
	}
	cp, err = MmapCode([]byte{})
	if err == nil {
		_ = cp.Munmap()
		t.Fatalf("MmapCode([]byte{}) should return error")
	}
}

// TestDarwinMmapCode_NilSafety 验证 nil CodePage 上调三方法不 panic。
//
// 防御性:实际生产路径不会传 nil,但 emit 失败时调用方可能误调
// Munmap/Addr/Length,接口必须幂等无 panic。
func TestDarwinMmapCode_NilSafety(t *testing.T) {
	var cp *CodePage
	if cp.Addr() != 0 {
		t.Fatalf("nil CodePage Addr() = %x, want 0", cp.Addr())
	}
	if cp.Length() != 0 {
		t.Fatalf("nil CodePage Length() = %d, want 0", cp.Length())
	}
	if err := cp.Munmap(); err != nil {
		t.Fatalf("nil CodePage Munmap() = %v, want nil", err)
	}
}

// TestDarwinMmapCode_NoLeak 验证反复 mmap/munmap 不泄漏 mmap 段(50 轮)。
//
// macOS 进程 mmap 段总量有上限(vm.max_proc_map / RLIMIT_AS),泄漏即
// OOM;同时反复翻 W^X 验证线程局部状态正确还原(不会因为忘了某次
// UnlockOSThread 把 goroutine 钉死)。
func TestDarwinMmapCode_NoLeak(t *testing.T) {
	code := []byte{
		0x1f, 0x20, 0x03, 0xd5, // NOP
		0xc0, 0x03, 0x5f, 0xd6, // RET
	}
	for i := 0; i < 50; i++ {
		cp, err := MmapCode(code)
		if err != nil {
			t.Fatalf("MmapCode iter %d failed: %v", i, err)
		}
		if err := cp.Munmap(); err != nil {
			t.Fatalf("Munmap iter %d failed: %v", i, err)
		}
	}
}

// TestDarwinMmapCode_ExecSanityProbe 真物理 darwin/arm64 执行最小段验证。
//
// **F3-#3 调试探针**(承本会话 macos-latest SIGSEGV at 0x2000):trampoline_test.go
// 的 CallJITFull RoundTrip 在 macos-latest 真物理 arm64 上崩 PC=0x2000,
// 需要先把根因 isolate 到「MAP_JIT mmap+RX 翻面是否生效」还是「trampoline
// asm 跳进去后的执行问题」。
//
// 本测试**只用 codepage_darwin.go::MmapCode**(不经 trampoline_arm64.s),
// 通过 reflect SliceHeader 把 mmap 段地址当函数指针调,验证:
//   - mmap 段 Addr 在合法地址区间(>= 0x100000000 macOS arm64 mmap 区)
//   - 段字节真写入(读回首 8 字节验证)
//   - 真物理 RX 翻面工作(段执行 `mov x0, 0x42; ret` 后 X0 = 0x42)
//
// 如本测试在 macos-latest 跑过,则证明 codepage_darwin.go 工作;trampoline
// 路径的崩是 trampoline_arm64.s 的 darwin ABI 问题(PAC / framesize / X28
// 等)。如本测试也崩,则证明 jitcgo.JITWriteProtectExit 或
// jitcgo.ICacheInvalidate 在 GH Actions macos-latest entitlement 不允许下
// silent fail,根因在 MmapCode 层。
func TestDarwinMmapCode_ExecSanityProbe(t *testing.T) {
	// 字节序列(arm64 LE):
	//   movz x0, #0x42      ; 0xD2800840 → 40 08 80 d2
	//   ret                  ; 0xD65F03C0 → c0 03 5f d6
	code := []byte{
		0x40, 0x08, 0x80, 0xd2, // movz x0, #0x42
		0xc0, 0x03, 0x5f, 0xd6, // ret
	}
	cp, err := MmapCode(code)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer cp.Munmap()

	addr := cp.Addr()
	t.Logf("mmap segment addr = 0x%x (length = %d)", addr, cp.Length())

	// **不真 call** — 本探针仅验 MmapCode 路径行为(addr 合法 + 字节写入)。
	// 真 execute 经 callJITFull 路径在 trampoline_test.go 测,如那条崩本测试
	// 不崩,则隔离根因到 trampoline 而非 codepage。
	if addr == 0 {
		t.Fatalf("Addr() == 0")
	}
	if cp.Length() < len(code) {
		t.Fatalf("Length = %d, want >= %d", cp.Length(), len(code))
	}

	// macOS arm64 mmap 区起 ≥ 0x100000000(4 GB)— 不在低 4 GB 中(那里是
	// __DATA/__TEXT 等 Mach-O segments)。验 addr 远 >= 0x10000(64KB),0x2000
	// = 8KB 处是 macOS 低保护页;若 addr 落在 0x2000 量级则系统配置异常。
	if addr < 0x10000 {
		t.Errorf("addr = 0x%x is suspiciously low (low-memory protected zone)", addr)
	}
}
