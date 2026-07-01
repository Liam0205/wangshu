//go:build wangshu_p4 && darwin && arm64 && cgo

// Package arm64 darwin/arm64 后端代码页管理(MAP_JIT + W^X 翻面 + icache flush)。
//
// **本文件无 cgo 直接 import**(承 F1 修复:父包 arm64 含 Plan 9 .s 文件,
// 启用 cgo 会触 Go 工具链「same-package cgo + Go asm 互斥」规则);cgo
// 不可避免的 pthread_jit_write_protect_np + sys_icache_invalidate 隔离到
// 子包 internal/gibbous/jit/arm64/jitcgo,父包经普通 Go 函数调用走 forward。
//
// 承 docs/design/p4-method-jit/05-system-pipeline.md §2.1 exec mmap 协议 +
// §2.3 arm64 icache flush 协议 + 06-backends.md §4.2 arm64 寄存器约定 +
// tmp/wangshu-p4-todo.md §三 darwin/arm64 W^X 真实装。
//
// **macOS 专属约束**(linux 路径见 codepage_linux.go):
//  1. mmap 必须带 MAP_JIT(0x800)flag — macOS Hardened Runtime 默认禁
//     PROT_EXEC,只有 MAP_JIT 段例外(需 `com.apple.security.cs.allow-jit`
//     entitlement,GH Actions macos-latest runner `go test` 子进程默认允许);
//  2. W^X 翻面不能用 mprotect — macOS arm64 强制 W^X,经
//     pthread_jit_write_protect_np(0=可写,1=可执行)切换;**线程局部状态**,
//     故 runtime.LockOSThread + defer UnlockOSThread 钉线程防 goroutine 调度
//     污染其它 goroutine 可写态;
//  3. icache flush 用 sys_icache_invalidate(libkern/OSCacheControl.h)
//     而非 linux 端的手写 DC CVAU/IC IVAU(macOS 不暴露 EL0 cache 维护指令)。
//
// **cgo 隔离纪律**(承用户拍板方案 I):build tag 第四位 `cgo` 严守 — 主库
// 默认 build(CGO_ENABLED=0 cross-build 或 amd64 主路径)走 codepage_other.go
// stub,只有 macos-latest CI 启用 cgo 时才链 jitcgo 子包真实装。**主库零
// cgo import 承诺不变**:父包 arm64 无 import "C",cgo 仅活在 jitcgo 子包内。
package arm64

import (
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"

	jitcgo "github.com/Liam0205/wangshu/internal/gibbous/jit/arm64/jitcgo"
)

// CodePage is a MAP_JIT mmap-allocated executable segment (post W^X flip),
// with the same refcount lifecycle as codepage_linux.go. See
// amd64/codepage_linux.go for the Enter/Exit/Dispose protocol rationale.
type CodePage struct {
	mem      []byte
	length   int
	refcount atomic.Int32
	disposed atomic.Bool
}

// MmapCode 分配 MAP_JIT 段,写入 code,W^X 翻面 + arm64 icache flush。
//
// 流程(对位 codepage_linux.go MmapCode 同款结构 + macOS 特定步骤):
//  1. runtime.LockOSThread:把当前 goroutine 钉死本 OS 线程,
//     pthread_jit_write_protect_np 影响线程局部状态;UnlockOSThread 经
//     defer 在翻 RX + icache flush 后释放;
//  2. unix.Mmap MAP_ANON|MAP_PRIVATE|MAP_JIT,PROT_READ|PROT_WRITE|PROT_EXEC
//     — macOS MAP_JIT 段必须一开始就带 PROT_EXEC(mmap flag 不带后续无法翻);
//  3. jitcgo.JITWriteProtectEnter():进入可写态(cgo forward);
//  4. copy code 进段;
//  5. jitcgo.JITWriteProtectExit():翻 RX(cgo forward);
//  6. jitcgo.ICacheInvalidate(addr, length):刷 i-cache(cgo forward);
//  7. runtime.UnlockOSThread(deferred)。
//
// 错误路径必须 unmap + UnlockOSThread,防资源 / 线程钉死泄漏。
func MmapCode(code []byte) (*CodePage, error) {
	if len(code) == 0 {
		return nil, errors.New("internal/gibbous/jit/arm64: empty code")
	}
	pageSize := unix.Getpagesize()
	length := ((len(code) + pageSize - 1) / pageSize) * pageSize

	// Step 1:钉线程 — pthread_jit_write_protect_np 是线程局部状态,
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

	// Step 3:进入可写态(jitcgo forward,线程局部)。
	jitcgo.JITWriteProtectEnter()

	// Step 4:copy code 进段。
	copy(mem, code)

	// Step 5:翻 RX(jitcgo forward)。
	jitcgo.JITWriteProtectExit()

	// Step 6:刷 i-cache(jitcgo forward;macOS arm64 必需)。
	jitcgo.ICacheInvalidate(unsafe.Pointer(&mem[0]), uintptr(length))

	cp := &CodePage{mem: mem, length: length}
	cp.refcount.Store(1)
	return cp, nil
}

// Addr returns the segment start uintptr.
func (c *CodePage) Addr() uintptr {
	if c == nil || len(c.mem) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&c.mem[0]))
}

// Enter acquires one reference. Returns false if the segment has been
// disposed or its refcount already reached zero. Same CAS-guarded bump
// pattern as amd64/codepage_linux.go.
func (c *CodePage) Enter() bool {
	if c == nil {
		return false
	}
	for {
		r := c.refcount.Load()
		if r == 0 {
			return false
		}
		if c.disposed.Load() {
			return false
		}
		if c.refcount.CompareAndSwap(r, r+1) {
			if c.disposed.Load() {
				c.Exit()
				return false
			}
			return true
		}
	}
}

// Exit releases one reference; if refcount hits zero, real munmap fires here.
func (c *CodePage) Exit() {
	if c == nil {
		return
	}
	if c.refcount.Add(-1) == 0 {
		c.doMunmap()
	}
}

// Dispose flips the disposed flag and drops the constructor's initial ref.
// Real munmap fires synchronously if no Run holds the segment. Idempotent.
func (c *CodePage) Dispose() error {
	if c == nil {
		return nil
	}
	if !c.disposed.CompareAndSwap(false, true) {
		return nil
	}
	if c.refcount.Add(-1) == 0 {
		return c.doMunmapCapturing()
	}
	return nil
}

// Munmap is a compatibility alias for Dispose. Deprecated: prefer Dispose.
func (c *CodePage) Munmap() error {
	return c.Dispose()
}

func (c *CodePage) doMunmap() {
	mem := c.mem
	c.mem = nil
	c.length = 0
	if mem != nil {
		_ = unix.Munmap(mem)
	}
}

func (c *CodePage) doMunmapCapturing() error {
	mem := c.mem
	c.mem = nil
	c.length = 0
	if mem == nil {
		return nil
	}
	return unix.Munmap(mem)
}

// Length returns the actual allocated byte count (page-aligned).
func (c *CodePage) Length() int {
	if c == nil {
		return 0
	}
	return c.length
}
