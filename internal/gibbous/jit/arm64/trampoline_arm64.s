// trampoline_arm64.s —— P4 PJ8 arm64 完整 trampoline asm。
//
// 承 docs/design/p4-method-jit/05-system-pipeline.md §2.4 + 06-backends.md §4.2.
//
// **关键技术约束**:
//   - X28 = Go G 寄存器(Go 1.17+ ABI0 arm64)——必须保持不动;
//   - X27 装 jitContext(callee-saved,Go ABI0 不预留特殊用途);
//   - X29 = FP, X30 = LR(arm64 标准);
//   - NOSPLIT|NOFRAME:trampoline 自身不分配 Go 栈帧,直接动 SP 保存
//     callee-saved + LR + FP。

//go:build wangshu_p4 && linux && arm64

#include "textflag.h"

// func callJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64
//
// 入参:
//   codeAddr   +0(FP)   uintptr  ← mmap 段起点(PROT_RX)
//   jitCtxAddr +8(FP)   uintptr  ← *JITContext(Go 堆,X27 装载)
// 返回:
//   ret        +16(FP)  uint64   ← mmap 段执行后 X0 值
//
// 实现要点(NOFRAME 手动管理):
//   1. 手动 SUB SP, $96 给本帧分配空间(96 = 80 callee-saved + 16 LR/FP);
//   2. STP X29, X30 到栈顶(模拟 arm64 ABI 标准 frame 链);
//   3. STP X19-X27 进 frame 内(8 个寄存器 = 4 对 + 1 单 = 64 字节);
//   4. ADD X29, SP 让 Go runtime stack walker 能跟链;
//   5. 装 X27 = jitContext;
//   6. BLR codeAddr 跳进 mmap 段——段以 RET(0xd65f03c0)收尾,X0 持值;
//   7. 段返回:LDP 恢复 callee-saved + LR/FP + ADD SP;
//   8. 写回 X0 + RET(LR 已恢复,跳回 caller)。

TEXT ·callJITFull(SB),NOSPLIT|NOFRAME,$0-24
	// 分配 96 字节栈帧(arm64 SP 16-byte 对齐)
	SUB	$96, RSP

	// 存 LR(X30)+ FP(X29)到栈顶(arm64 ABI 标准 frame chain)
	STP	(R29, R30), 0(RSP)

	// 存 callee-saved X19-X27(9 寄存器,5 对其中末对 R27+0 占位)
	STP	(R19, R20), 16(RSP)
	STP	(R21, R22), 32(RSP)
	STP	(R23, R24), 48(RSP)
	STP	(R25, R26), 64(RSP)
	MOVD	R27, 80(RSP)

	// 让 X29 指向当前 frame(stack walker 能 unwind)
	MOVD	RSP, R29

	// 装 R27 = jitContext
	MOVD	jitCtxAddr+8(FP), R27

	// 取 codeAddr 到 R8(临时,X8 caller-saved)
	MOVD	codeAddr+0(FP), R8

	// BLR 跳进 mmap 段——X30 被设为下一条指令地址
	// Plan 9 arm64 用 BL 表 BLR 间接调用
	BL	(R8)

	// 段返回:X0 已是返回值。恢复 callee-saved
	LDP	0(RSP), (R29, R30)
	LDP	16(RSP), (R19, R20)
	LDP	32(RSP), (R21, R22)
	LDP	48(RSP), (R23, R24)
	LDP	64(RSP), (R25, R26)
	MOVD	80(RSP), R27

	// 写回返回值
	MOVD	R0, ret+16(FP)

	// 释放栈帧 + RET(LR 已恢复,跳回 caller)
	ADD	$96, RSP
	RET
