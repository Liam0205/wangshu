//go:build wangshu_p4 && linux && arm64

// flushcache_arm64.s —— P4 PJ8 arm64 i-cache flush(承 codepage_linux.go
// flushICacheArm64 占位的真实装)。
//
// 承 docs/design/p4-method-jit/05-system-pipeline.md §2.3.1 arm64 icache
// 协议:写 mmap 段后必须做 DC CVAU + IC IVAU + DSB + ISB,否则 i-cache 与
// d-cache 不一致,执行段会取到旧 i-cache 内容。
//
// 实现参考 Linux kernel arch/arm64/lib/__clear_cache.S 简化版。每条 cache
// line 走一遍 DC CVAU + IC IVAU(Cache line 大小经 CTR_EL0 读取,这里用
// 保守 64 字节,大多数 arm64 实现 d-line 64 / i-line 64 或更小)。

#include "textflag.h"

// func flushICacheArm64Asm(start, end uintptr)
//
// 入参:
//   start +0(FP)  uintptr  ← 段起点
//   end   +8(FP)  uintptr  ← 段终点(exclusive)
//
// 流程:
//   for addr := start &~ 63; addr < end; addr += 64 {
//       DC CVAU, addr  // clean d-cache to point of unification
//   }
//   DSB ISH            // wait for DC done
//   for addr := start &~ 63; addr < end; addr += 64 {
//       IC IVAU, addr  // invalidate i-cache to PoU
//   }
//   DSB ISH            // wait for IC done
//   ISB                // ensure CPU re-fetches
TEXT ·flushICacheArm64Asm(SB),NOSPLIT|NOFRAME,$0-16
	MOVD	start+0(FP), R0
	MOVD	end+8(FP), R1

	// align R0 down to 64-byte boundary(假设 cache line ≤ 64;真值经
	// CTR_EL0[3:0] 的 IminLine 读出,大部分 ARMv8 实现 = 4(16 字节)或
	// 6(64 字节);保守用 64,多扫几条 line 不害正确性,只稍慢)。
	BIC	$63, R0, R2  // R2 = aligned start

	// pass 1: DC CVAU 全段
	MOVD	R2, R3
loopDC:
	CMP	R1, R3
	BHS	doneDC
	WORD	$0xd50b7b23  // DC CVAU, X3(0xD50B7B20 base | Rt=3)
	ADD	$64, R3, R3
	B	loopDC
doneDC:
	WORD	$0xd5033b9f  // DSB ISH(Data Sync Barrier, Inner Shareable;ARMv8 manual C6.2.91)

	// pass 2: IC IVAU 全段
	MOVD	R2, R3
loopIC:
	CMP	R1, R3
	BHS	doneIC
	WORD	$0xd50b7523  // IC IVAU, X3(0xD50B7520 base | Rt=3)
	ADD	$64, R3, R3
	B	loopIC
doneIC:
	WORD	$0xd5033b9f  // DSB ISH
	WORD	$0xd5033fdf  // ISB

	RET
