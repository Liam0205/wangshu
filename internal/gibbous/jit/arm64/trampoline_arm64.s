// trampoline_arm64.s —— P4 PJ8 arm64 完整 trampoline asm。
//
// 承 docs/design/p4-method-jit/05-system-pipeline.md §2.4 trampoline 进出
// 三出口协议 + §3.3 jitContext + 06-backends.md §4.2 arm64 寄存器约定。
//
// **关键技术约束**(承 P4 设计 + Go arm64 ABI 实测):
//   - X28 = Go G 寄存器(Go 1.17+ ABI0 arm64)——切 SP 期间必须保持不动,
//     否则 Go runtime stop-the-world 找不到 g 立即 SEGV;
//   - callee-saved(arm64 AAPCS):X19-X28 + X29(FP) + X30(LR)——但
//     Go ABI0 内 X28 是 G,不能动;X27 callee-saved 且 Go ABI0 不预留特殊
//     用途 — P4 用它装 jitContext(承 06 §4.2);其它 callee-saved
//     (X19-X26)trampoline 入口 stp、出口 ldp;
//   - NOSPLIT:不被 morestack 检查插桩——「JIT 段 + trampoline 路径不经
//     Go runtime 调度」前提。让 Go 自动管 frame size(framesize=80,本函
//     数局部 stack 80 字节存 5 对 callee-saved 寄存器)。

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
// 实现要点:
//   1. Go runtime auto-prologue 分配 80 字节 frame(framesize 由 Plan 9
//      汇编器从 `$80-24` 自动管理,加自动 stp/ldp X29 X30 在 frame 头);
//   2. 保存 callee-saved 寄存器 X19-X27 进 frame 内;
//   3. 装 X27 = jitContext;
//   4. BL codeAddr 跳进 mmap 段——段以 RET(指令 0xd65f03c0)收尾,X0 持值;
//   5. 段返回后 X27 可能已被 mmap 段污染(若段调过 helper);恢复 callee-saved;
//   6. 把 X0 写回 ret;Go runtime auto-epilogue 恢复 X29 X30 + SP。
//
// **PJ8 简化形态**(对位 amd64 trampoline_full PJ2):不切 SP——继续在 Go
// 栈上跑 mmap 段(段瞬时,~ns,与 amd64 同款风险面)。完整切 SP 留 PJ3+
// (算术 + helper 调用引入子 BL 时启用)。

TEXT ·callJITFull(SB),NOSPLIT,$80-24
	// Go arm64 ABI0:framesize=80 让 Go 自动 stp X29 X30 + 调 SP;
	// 余 64 字节给我们存 X19-X27(4 对 = 64 字节)。
	// 注:Plan 9 arm64 frame layout:[saved X29 X30 | local stack]。
	// SP 指向 saved X29 X30 之上的 local stack 起点。
	STP	(R19, R20), 0(RSP)
	STP	(R21, R22), 16(RSP)
	STP	(R23, R24), 32(RSP)
	STP	(R25, R26), 48(RSP)
	MOVD	R27, 64(RSP)

	// 装 R27 = jitContext(06 §4.2 arm64 寄存器约定)
	MOVD	jitCtxAddr+8(FP), R27

	// 取 codeAddr 到 R8(临时,X8 caller-saved arm64)
	MOVD	codeAddr+0(FP), R8

	// CALL R8 → 间接调用,Go arm64 等价 BLR X8;Go epilogue 会处理 LR 恢复
	CALL	(R8)

	// 段返回:X0 已是返回值。恢复 callee-saved(逆序)
	LDP	0(RSP), (R19, R20)
	LDP	16(RSP), (R21, R22)
	LDP	32(RSP), (R23, R24)
	LDP	48(RSP), (R25, R26)
	MOVD	64(RSP), R27

	// 写回返回值
	MOVD	R0, ret+16(FP)
	RET
