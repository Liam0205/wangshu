// trampoline_spec_amd64.s —— P4 PJ2 投机模板 trampoline。
//
// **与 trampoline_full_amd64.s 区别**:full 版只装 r15=jitContext;spec
// 版同时装 rbx=valueStackBase(JIT 段经 rbx+disp32 直读/写值栈槽位,
// 跳过 host helper round-trip)。
//
// **寄存器约定**(承 06 §4.1):
//   - r15 = jitContext(callee-saved,P4 全局保留)
//   - rbx = valueStackBase(P4 PJ2+ 投机模板专用,callee-saved)
//   - r14 = Go G(全程不动)
//
// **PJ2 简化形态**(对位 trampoline_full PJ2):不切 SP——继续 Go 栈跑
// mmap 段(段瞬时,~ns)。完整切 SP 留 PJ3+(算术调子 helper 时启用)。

//go:build wangshu_p4 && linux && amd64

#include "textflag.h"

// func callJITSpec(codeAddr uintptr, jitCtx uintptr, vsBase uintptr) uint64
//
// 入参:
//   codeAddr +0(FP)   uintptr  ← mmap 段起点
//   jitCtx   +8(FP)   uintptr  ← *JITContext(r15 装载)
//   vsBase   +16(FP)  uintptr  ← valueStackBase(rbx 装载)
// 返回:
//   ret      +24(FP)  uint64   ← 段执行后 RAX 值

TEXT ·callJITSpec(SB),NOSPLIT,$0-32
	// 保存 callee-saved 寄存器(rbx/rbp/r12/r13/r15)
	// 注:r14 = Go G 寄存器,不动!
	PUSHQ BX
	PUSHQ BP
	PUSHQ R12
	PUSHQ R13
	PUSHQ R15

	// 装 r15 = jitContext, rbx = valueStackBase
	MOVQ jitCtx+8(FP), R15
	MOVQ vsBase+16(FP), BX

	// 取 codeAddr + CALL 段
	MOVQ codeAddr+0(FP), AX
	CALL AX

	// 段返回:RAX 已是返回值。恢复 callee-saved(逆序 pop)
	POPQ R15
	POPQ R13
	POPQ R12
	POPQ BP
	POPQ BX

	// 写回返回值
	MOVQ AX, ret+24(FP)
	RET
