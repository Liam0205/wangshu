//go:build wangshu_p4 && darwin && arm64 && cgo

// Package jitcgo darwin/arm64 后端 cgo 隔离子包 —— 只导出 cgo 不可避免的两个
// 原语(pthread_jit_write_protect_np + sys_icache_invalidate),其余 mmap /
// munmap 由父包用 golang.org/x/sys/unix 纯 Go syscall 直接调。
//
// **独立子包目的**(F1 修复):承父包 internal/gibbous/jit/arm64 含 Plan 9
// arm64 汇编(trampoline_arm64.s / flushcache_arm64.s),Go 工具链规则
// 「同一 package 启用 cgo 时不能含 Plan 9 .s 文件」(macos-latest CI 实证报
// `package using cgo has Go assembly file trampoline_arm64.s`)。
//
// 解法:把 cgo 实装隔离到本独立 package,父包通过函数调用走 forward,父包
// 自己不 import "C",仍是非 cgo 包,Plan 9 asm 兼容。
//
// 承 docs/design/p4-method-jit/05-system-pipeline.md §2.1 exec mmap 协议 +
// §2.3 arm64 icache flush 协议 + tmp/wangshu-p4-todo.md §三 darwin/arm64
// W^X 真实装。
//
// **macOS 专属约束**:
//  1. mmap 必须带 MAP_JIT(0x800)flag — 由父包传 unix.MAP_JIT 给 unix.Mmap;
//  2. W^X 翻面经 pthread_jit_write_protect_np(0=可写,1=可执行)切换;
//     **线程局部状态**,故 caller(父包)负责 runtime.LockOSThread + defer
//     UnlockOSThread 钉线程防 goroutine 调度污染其它 goroutine 可写态;
//  3. icache flush 用 sys_icache_invalidate(libkern/OSCacheControl.h)
//     而非 linux 端的手写 DC CVAU/IC IVAU。
//
// **cgo 隔离纪律**(承用户拍板方案 I):build tag 第四位 `cgo` 严守 — 主库
// 默认 build(CGO_ENABLED=0 cross-build 或 amd64 主路径)走父包
// codepage_other.go stub,只有 macos-latest CI 启用 cgo 时才链本子包真实装。
package jitcgo

/*
#include <pthread.h>
#include <libkern/OSCacheControl.h>
*/
import "C"

import "unsafe"

// JITWriteProtectEnter 切线程的 JIT 段为「可写不可执行」态。
//
// macOS arm64 强制 W^X,经 pthread_jit_write_protect_np(0)进入可写态。
// **caller 必须在 runtime.LockOSThread 区内调用**(pthread_jit_write_protect_np
// 影响线程局部状态)。
//
// 用例:父包 codepage_darwin.go::MmapCode 在 unix.Mmap MAP_JIT 段后、
// copy(mem, code) 前调本函数进入可写态。
func JITWriteProtectEnter() {
	C.pthread_jit_write_protect_np(0)
}

// JITWriteProtectExit 切线程的 JIT 段为「可执行不可写」态(RX)。
//
// 对位 JITWriteProtectEnter,经 pthread_jit_write_protect_np(1)翻 RX。
// **caller 必须在 runtime.LockOSThread 区内调用**。
//
// 用例:父包 codepage_darwin.go::MmapCode 在 copy(mem, code) 后调本函数
// 翻 RX,接下来调 ICacheInvalidate 刷 i-cache。
func JITWriteProtectExit() {
	C.pthread_jit_write_protect_np(1)
}

// ICacheInvalidate 经 libSystem sys_icache_invalidate 刷段的 i-cache。
//
// macOS arm64 必需 —— 写完段后必须刷 i-cache,否则 CPU 取指会取到 i-cache
// 旧内容(承 05 §2.3.1)。macOS 不暴露 EL0 cache 维护指令(DC CVAU / IC IVAU),
// 必须经 libSystem syscall。
//
// **参数**:addr / length 描述要刷的段范围(unsafe.Pointer 来自父包 mmap
// 返回的 mem[0] 地址)。
func ICacheInvalidate(addr unsafe.Pointer, length uintptr) {
	C.sys_icache_invalidate(addr, C.size_t(length))
}
