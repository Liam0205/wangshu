//go:build linux && amd64

// 闸门 ①②③:exec mmap + W^X 翻面 + Go → mmap 段 → ret 回 Go 最小 round-trip。
//
// 设计:
//   - 不切自管栈、不装 jitContext(那是 PJ1 完整 trampoline 的事;本 spike 只
//     验「mmap 段能跑 + ret 回 Go 不踩 runtime」);
//   - 发射「mov rax, IMM64; ret」9 字节序列(48 b8 IMM64-LE c3),Go 经
//     `runtime/cgo`-free 的 trampoline 跳进去拿 IMM64 值;
//   - trampoline asm stub:`callJIT(codeAddr, scratch *[8]uint64) uint64`
//     ——只把 codeAddr 当 CALL 目标(go runtime asm 内的 `JMP AX` 等价模式),
//     scratch 留给后续 PJ1 装 jitContext。
//
// 闸门红绿:① mmap RW + mprotect RX 不报错;② callJIT 不 SEGV;③ 返回值 == IMM64
// 入参——三件套全过即 spike 绿,可承诺 PJ1 emitter trait 落地。
package p4tramp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"

	"golang.org/x/sys/unix"
)

// CodePage 是一个 mmap 出来的可执行段(W^X 翻面后),hold 一段直到 Munmap。
//
// 对位 docs/design/p4-method-jit/05-system-pipeline.md §2.1 exec mmap 协议;
// PJ1 完整版会引入 codePagePool(per-Proto 段 + 释放策略),本 spike 只做单段。
type CodePage struct {
	// addr 是 mmap 段起点(unsafe-equivalent uintptr,但 spike 用 []byte 视图
	// 即可——避免引入 unsafe 依赖让 spike 闸门更简单)。
	mem []byte
	// length 是分配的字节数(>= len(code))。
	length int
}

// Addr 返回段起点的 uintptr——CALL 目标地址。
func (c *CodePage) Addr() uintptr {
	if c == nil || len(c.mem) == 0 {
		return 0
	}
	// &c.mem[0] 是 []byte 底层数组首地址,mmap 出来的页对齐稳定指针——闸门 ①
	// 的物理来源(mprotect 后,这一块就是可执行段起点)。
	return memAddr(c.mem)
}

// Munmap 释放段。
func (c *CodePage) Munmap() error {
	if c == nil || c.mem == nil {
		return nil
	}
	mem := c.mem
	c.mem = nil
	c.length = 0
	return unix.Munmap(mem)
}

// MmapCode 分配一段 W+X 内存,写入 code,W^X 翻面后返回(承 05 §2.1 协议)。
//
// 流程(对位 wazero internal/engine/wazevo 同款,但 P4 自付):
//  1. unix.Mmap MAP_ANON|MAP_PRIVATE PROT_READ|PROT_WRITE alloc;
//  2. copy code 进段;
//  3. unix.Mprotect PROT_READ|PROT_EXEC 翻面(W^X 任何时刻不持 RWX);
//  4. (linux/amd64)硬件保证 icache 一致性,无显式 flush;
//
// 失败时清理已分配段(Munmap)。
func MmapCode(code []byte) (*CodePage, error) {
	if len(code) == 0 {
		return nil, errors.New("p4tramp: empty code")
	}
	// 页对齐(linux 4 KiB,与代码段实际长度无关——mmap 只接受页粒度)
	pageSize := unix.Getpagesize()
	length := ((len(code) + pageSize - 1) / pageSize) * pageSize

	// step 1:mmap RW
	mem, err := unix.Mmap(
		-1, 0, length,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_ANON|unix.MAP_PRIVATE,
	)
	if err != nil {
		return nil, fmt.Errorf("p4tramp: mmap RW failed: %w", err)
	}

	// step 2:写入 code
	copy(mem, code)

	// step 3:mprotect RX(W^X 翻面;不持 RWX)
	if err := unix.Mprotect(mem, unix.PROT_READ|unix.PROT_EXEC); err != nil {
		_ = unix.Munmap(mem)
		return nil, fmt.Errorf("p4tramp: mprotect RX failed: %w", err)
	}

	// step 4:linux/amd64 硬件 icache 一致(无操作);arm64 在此处插 IC IVAU/DC CVAU
	// 序列(留 PJ8 darwin/arm64 spike + 06 §2.3 协议落地)。
	_ = runtime.GOOS

	return &CodePage{mem: mem, length: length}, nil
}

// EmitMovRaxImm64Ret 发射「mov rax, imm64; ret」9 字节序列。
//
// amd64 编码(intel manual §B.1):
//
//	48 b8 ii ii ii ii ii ii ii ii   ; REX.W + B8+rd  → mov rax, imm64
//	c3                                ; ret
//
// 9 字节固定(不带任何变长 prefix)——LOADK 模板的物理形态最小化。
func EmitMovRaxImm64Ret(imm uint64) []byte {
	buf := make([]byte, 11)
	buf[0] = 0x48 // REX.W
	buf[1] = 0xb8 // mov rax, imm64
	binary.LittleEndian.PutUint64(buf[2:10], imm)
	buf[10] = 0xc3 // ret
	return buf
}
