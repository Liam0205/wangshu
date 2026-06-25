//go:build wangshu_p4 && linux && amd64

// Package amd64 amd64 后端代码页管理(W^X 翻面 + munmap)。
//
// 承 docs/design/p4-method-jit/05-system-pipeline.md §2.1 exec mmap 协议:
//  1. unix.Mmap MAP_ANON|MAP_PRIVATE PROT_READ|PROT_WRITE alloc;
//  2. copy code 进段;
//  3. unix.Mprotect PROT_READ|PROT_EXEC 翻面(W^X 任何时刻不持 RWX);
//  4. linux/amd64 硬件保证 icache 一致性,无显式 flush;
//
// **PJ1 spike 闸门 🟢 已绿**(spike/p4tramp/DECISION.md):闸门 ① mmap+W^X
// 翻面工作 + ④ 单 CALL ~1.95ns,P4 自管 codegen 物理基础已实证。本文件
// 主库版本以 spike 同款形态 + per-Proto 段释放策略落地。
package amd64

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// CodePage 是一段 mmap 出来的可执行段(W^X 翻面后)。
//
// 与 P3 wazero CompiledModule 对位:每个 CodePage = 一个升层 Proto 的原生
// 码段。Dispose 时 Munmap。
type CodePage struct {
	mem []byte // 底层 mmap 段(PROT_RX 后 read-only,Go GC 不感知)
	// length 实际分配字节数(>= len(code),按 4KiB 页向上取整)
	length int
}

// MmapCode 分配 W+X 段,写入 code,W^X 翻面后返回(承 spike/p4tramp 同款流程)。
//
// 失败时清理已分配段。
func MmapCode(code []byte) (*CodePage, error) {
	if len(code) == 0 {
		return nil, errors.New("internal/gibbous/jit/amd64: empty code")
	}
	pageSize := unix.Getpagesize()
	length := ((len(code) + pageSize - 1) / pageSize) * pageSize

	mem, err := unix.Mmap(
		-1, 0, length,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_ANON|unix.MAP_PRIVATE,
	)
	if err != nil {
		return nil, fmt.Errorf("internal/gibbous/jit/amd64: mmap RW failed: %w", err)
	}
	copy(mem, code)
	if err := unix.Mprotect(mem, unix.PROT_READ|unix.PROT_EXEC); err != nil {
		_ = unix.Munmap(mem)
		return nil, fmt.Errorf("internal/gibbous/jit/amd64: mprotect RX failed: %w", err)
	}
	// linux/amd64 硬件 icache 一致(无操作);arm64 在 06 §2.3 协议落地处插
	// IC IVAU/DC CVAU 序列(留 PJ8 启动)。
	return &CodePage{mem: mem, length: length}, nil
}

// Addr 返回段起点 uintptr——CALL 目标地址。
//
// **unsafe 范围**:仅用于把 mmap 段地址转 uintptr 给 trampoline asm。Go GC
// 不会移动 mmap 段(它不是 Go 堆对象,unix.Mmap 返回的 []byte 经 syscall 暴露
// 给 Go,但底层是 anonymous mmap,Go GC 不感知);因此 uintptr 稳定。
//
// 同源依赖见 spike/p4tramp/unsafe_linux_amd64.go::memAddr。
func (c *CodePage) Addr() uintptr {
	if c == nil || len(c.mem) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&c.mem[0]))
}

// Munmap 释放段。
//
// **关键纪律**(承 05 §2.1.3):Munmap 实际触发 munmap 必须保证「该段的所有
// 调用者都已退出」——若有 goroutine 正在该段执行,munmap 等于 UAF。在 P2
// 状态机里 Dispose 触发点都是「升层失败/降层」时刻,该段此刻没有活跃调用
// (状态转换前已经 quiesce);**多 State 并发**下若某 State A 触发某 Proto
// Dispose,而 State B 仍在该段执行则不安全——这是开放缺口,留 PJ7 验收期
// 落地(可能解法:引用计数 + 延迟 munmap)。
func (c *CodePage) Munmap() error {
	if c == nil || c.mem == nil {
		return nil
	}
	mem := c.mem
	c.mem = nil
	c.length = 0
	return unix.Munmap(mem)
}

// Length 返回段实际分配的字节数(页对齐后,可能 > len(code))。
//
// 调用方:codePagePool 释放策略;诊断日志。
func (c *CodePage) Length() int {
	if c == nil {
		return 0
	}
	return c.length
}
