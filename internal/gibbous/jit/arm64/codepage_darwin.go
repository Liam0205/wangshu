//go:build wangshu_p4 && darwin && arm64 && cgo

// Package arm64 darwin/arm64 后端代码页管理(MAP_JIT + W^X 翻面 + icache flush)。
//
// 承 docs/design/p4-method-jit/05-system-pipeline.md §2.1 exec mmap 协议 +
// §2.3 arm64 icache flush 协议 + 06-backends.md §4.2 arm64 寄存器约定 +
// tmp/wangshu-p4-todo.md §三 darwin/arm64 W^X 真实装。
//
// **macOS 专属约束**(linux 路径见 codepage_linux.go):
//  1. mmap 必须带 MAP_JIT(0x800)flag——macOS Hardened Runtime 默认禁
//     PROT_EXEC,只有 MAP_JIT 段例外(需 `com.apple.security.cs.allow-jit`
//     entitlement,GH Actions macos-latest runner `go test` 子进程默认允许);
//  2. W^X 翻面不能用 mprotect——macOS arm64 强制 W^X,经
//     pthread_jit_write_protect_np(0=可写,1=可执行)切换;**这是线程
//     局部状态**——同一线程内所有 MAP_JIT 段共享当前态;故必须
//     runtime.LockOSThread + defer UnlockOSThread,避免 goroutine 调度
//     把可写态泄露到其它 goroutine;
//  3. icache flush 用 sys_icache_invalidate(libkern/OSCacheControl.h)
//     而非 linux 端的手写 DC CVAU/IC IVAU(macOS 不暴露 EL0 cache 维护
//     指令,必须经 libSystem syscall)。
//
// **cgo 隔离纪律**(承 [feedback_no_session_self_limit] cgo 限制 + tmp/
// wangshu-p4-todo.md §三.3 风险点):build tag 第四位 `cgo` 严守——
// 主库默认 build(`CGO_ENABLED=0` cross-build 或 amd64 主路径)走
// codepage_other.go stub,只有 macos-latest CI 跑 P4 时启用 cgo 路径。
package arm64

/*
#include <sys/mman.h>
#include <pthread.h>
#include <libkern/OSCacheControl.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// CodePage 是一段 MAP_JIT mmap 出来的可执行段(W^X 翻面后)。
//
// 字段布局与 codepage_linux.go 一致,故 jit 主包跨 OS 路径用同一接口
// (Addr/Munmap/Length 三方法签名)。
type CodePage struct {
	mem    []byte
	length int
}

// MmapCode 分配 MAP_JIT 段,写入 code,W^X 翻面 + arm64 icache flush。
//
// 流程(对位 codepage_linux.go MmapCode 同款结构 + macOS 特定步骤):
//  1. runtime.LockOSThread:把当前 goroutine 钉死本 OS 线程,
//     pthread_jit_write_protect_np 影响线程局部状态,goroutine 调度
//     会污染其它 goroutine;UnlockOSThread 在翻 RX 后释放;
//  2. mmap MAP_ANON|MAP_PRIVATE|MAP_JIT,PROT_READ|PROT_WRITE|PROT_EXEC
//     ——macOS MAP_JIT 段必须一开始就带 PROT_EXEC(虽然实际能否取指
//     由 W^X 翻面决定,但 mmap flag 不带 PROT_EXEC 后续无法翻);
//  3. pthread_jit_write_protect_np(0):进入可写态;
//  4. copy code 进段;
//  5. pthread_jit_write_protect_np(1):翻 RX(可执行态);
//  6. sys_icache_invalidate(addr, length):刷 i-cache;
//  7. runtime.UnlockOSThread。
//
// 错误路径必须 unmap + UnlockOSThread,防资源 / 线程钉死泄漏。
func MmapCode(code []byte) (*CodePage, error) {
	if len(code) == 0 {
		return nil, errors.New("internal/gibbous/jit/arm64: empty code")
	}
	pageSize := unix.Getpagesize()
	length := ((len(code) + pageSize - 1) / pageSize) * pageSize

	// Step 1:钉线程。pthread_jit_write_protect_np 是线程局部状态,
	// goroutine 跨线程调度会污染其它 goroutine 的可写态。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Step 2:mmap MAP_JIT + PROT_RWX。
	mem, err := unix.Mmap(
		-1, 0, length,
		unix.PROT_READ|unix.PROT_WRITE|unix.PROT_EXEC,
		unix.MAP_ANON|unix.MAP_PRIVATE|unix.MAP_JIT,
	)
	if err != nil {
		return nil, fmt.Errorf("internal/gibbous/jit/arm64: mmap MAP_JIT failed: %w", err)
	}

	// Step 3:进入可写态(线程局部)。
	C.pthread_jit_write_protect_np(0)

	// Step 4:copy code 进段。
	copy(mem, code)

	// Step 5:翻 RX。
	C.pthread_jit_write_protect_np(1)

	// Step 6:刷 i-cache(macOS arm64 必需)。
	C.sys_icache_invalidate(unsafe.Pointer(&mem[0]), C.size_t(length))

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
