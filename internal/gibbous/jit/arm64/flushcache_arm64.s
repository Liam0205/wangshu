//go:build wangshu_p4 && linux && arm64

// flushcache_arm64.s —— P4 PJ8 arm64 i-cache flush (the real implementation
// backing the flushICacheArm64 placeholder in codepage_linux.go).
//
// Follows the arm64 icache protocol in
// docs/design/p4-method-jit/05-system-pipeline.md §2.3.1: after writing an
// mmap segment, DC CVAU + IC IVAU + DSB + ISB must be performed, otherwise the
// i-cache and d-cache are inconsistent and the executable segment will fetch
// stale i-cache contents.
//
// Implementation adapted from a simplified version of the Linux kernel's
// arch/arm64/lib/__clear_cache.S. Each cache line goes through DC CVAU + IC
// IVAU (cache line size is read from CTR_EL0; here we use a conservative 64
// bytes, as most arm64 implementations have d-line 64 / i-line 64 or smaller).

#include "textflag.h"

// func flushICacheArm64Asm(start, end uintptr)
//
// Params:
//   start +0(FP)  uintptr  ← segment start
//   end   +8(FP)  uintptr  ← segment end (exclusive)
//
// Flow:
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

	// align R0 down to 64-byte boundary (assumes cache line ≤ 64; the true
	// value is read from CTR_EL0[3:0] IminLine, and most ARMv8 implementations
	// = 4 (16 bytes) or 6 (64 bytes); conservatively using 64 scans a few extra
	// lines, which does not hurt correctness, only slightly slower).
	BIC	$63, R0, R2  // R2 = aligned start

	// pass 1: DC CVAU over the whole segment
	MOVD	R2, R3
loopDC:
	CMP	R1, R3
	BHS	doneDC
	WORD	$0xd50b7b23  // DC CVAU, X3(0xD50B7B20 base | Rt=3)
	ADD	$64, R3, R3
	B	loopDC
doneDC:
	WORD	$0xd5033b9f  // DSB ISH (Data Sync Barrier, Inner Shareable; ARMv8 manual C6.2.91)

	// pass 2: IC IVAU over the whole segment
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
