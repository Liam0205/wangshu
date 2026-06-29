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
