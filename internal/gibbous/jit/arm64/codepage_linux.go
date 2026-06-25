//go:build wangshu_p4 && linux && arm64

// Package arm64 amd64 后端代码页管理(W^X 翻面 + munmap + icache flush)。
//
// 承 docs/design/p4-method-jit/05-system-pipeline.md §2.1 exec mmap 协议 +
// §2.3 arm64 icache flush 协议 + 06-backends.md §4.2 arm64 寄存器约定。
//
// **PJ8 启动版**(2026-06-25):linux/arm64 基础 mmap+W^X+icache flush 落地;
// darwin/arm64 (MAP_JIT + pthread_jit_write_protect_np)留 PJ8+ 启动 spike。
package arm64

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// CodePage 是一段 mmap 出来的可执行段(W^X 翻面后)。
type CodePage struct {
	mem    []byte
	length int
}

// MmapCode 分配 W+X 段,写入 code,W^X 翻面 + arm64 icache flush。
//
// 流程(对位 amd64 版同款 + 加 icache flush 步骤):
//  1. unix.Mmap MAP_ANON|MAP_PRIVATE PROT_RW;
//  2. copy code 进段;
//  3. unix.Mprotect PROT_RX(W^X 翻面);
//  4. **arm64 必须**:flushICache(IC IVAU/DC CVAU + DSB ISH + ISB)——
//     否则 i-cache 与 d-cache 不一致,执行段会取到旧 i-cache 内容(05 §2.3.1)。
func MmapCode(code []byte) (*CodePage, error) {
	if len(code) == 0 {
		return nil, errors.New("internal/gibbous/jit/arm64: empty code")
	}
	pageSize := unix.Getpagesize()
	length := ((len(code) + pageSize - 1) / pageSize) * pageSize

	mem, err := unix.Mmap(
		-1, 0, length,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_ANON|unix.MAP_PRIVATE,
	)
	if err != nil {
		return nil, fmt.Errorf("internal/gibbous/jit/arm64: mmap RW failed: %w", err)
	}
	copy(mem, code)
	if err := unix.Mprotect(mem, unix.PROT_READ|unix.PROT_EXEC); err != nil {
		_ = unix.Munmap(mem)
		return nil, fmt.Errorf("internal/gibbous/jit/arm64: mprotect RX failed: %w", err)
	}
	// arm64 icache flush:必须显式 flush 否则取指错误(05 §2.3.1)。
	// **PJ8 简化形态**:linux 提供 __builtin___clear_cache 等价 syscall——
	// 经 syscall membarrier 或写一个 NOSPLIT asm stub 调 IC IVAU/DC CVAU。
	// 当前实装:留 PJ8+ 完整 asm stub(本 commit 范围内 mmap 段不真执行——
	// MmapCode 只是 emit 接口对齐,arm64 真执行路径未接入 SupportsAllOpcodes
	// 白名单)。
	flushICacheArm64(mem)
	return &CodePage{mem: mem, length: length}, nil
}

// Addr 返回段起点 uintptr。
func (c *CodePage) Addr() uintptr {
	if c == nil || len(c.mem) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&c.mem[0]))
}

// Munmap 释放段(幂等)。
func (c *CodePage) Munmap() error {
	if c == nil || c.mem == nil {
		return nil
	}
	mem := c.mem
	c.mem = nil
	c.length = 0
	return unix.Munmap(mem)
}

// Length 返回段实际分配字节数(页对齐)。
func (c *CodePage) Length() int {
	if c == nil {
		return 0
	}
	return c.length
}

// flushICacheArm64 是 arm64 icache flush 占位——完整 asm stub 实装留 PJ8+
// (本 commit 范围 SupportsAllOpcodes 不开 arm64 白名单,mmap 段不真执行)。
//
// 完整实装(承 05 §2.3.1):
//
//	for addr := start; addr < end; addr += cacheLineSize {
//	    DC CVAU, addr  // clean d-cache to point of unification
//	}
//	DSB ISH         // 确保 DC 完成
//	for addr := start; addr < end; addr += cacheLineSize {
//	    IC IVAU, addr  // invalidate i-cache to point of unification
//	}
//	DSB ISH         // 屏障
//	ISB             // 指令同步屏障,让 CPU 重新 fetch
//
// linux/arm64 也可经 syscall `__ARM_NR_cacheflush` 让内核做(慢但简单)。
//
// 当前 PJ8 启动版:no-op(段不真执行)——arm64 emitter 真发指令 + SupportsAllOpcodes
// 开 arm64 白名单时同批落地。
func flushICacheArm64(mem []byte) {
	_ = mem
	// PJ8+ 实装 clear_cache(start, end)经 asm stub 或 syscall。
}
