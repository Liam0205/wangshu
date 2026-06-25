// trampoline_arm64.s —— P4 PJ8 arm64 完整 trampoline asm 草稿(切 SP +
// 保存 callee-saved + 装 jitContext)。
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
//   - NOSPLIT|NOFRAME:trampoline 自身不分配 Go 栈帧,不被 morestack 检查
//     插桩——这是「JIT 段 + trampoline 路径不经 Go runtime 调度」前提。

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
//   1. 保存 callee-saved 寄存器 X19-X27 + LR(X30)——X28=G 不动;
//   2. 装 X27 = jitContext;
//   3. BLR codeAddr 跳进 mmap 段——段以 RET(指令 0xd65f03c0)收尾,X0 持值;
//   4. 段返回后 X27 可能已被 mmap 段污染(若段调过 helper);恢复 callee-saved;
//   5. 把 X0 写回 ret。
//
// **PJ8 简化形态**(对位 amd64 trampoline_full PJ2):不切 SP——继续在 Go
// 栈上跑 mmap 段(段瞬时,~ns,与 amd64 同款风险面)。完整切 SP 留 PJ3+
// (算术 + helper 调用引入子 BLR 时启用)。

TEXT ·callJITFull(SB),NOSPLIT|NOFRAME,$0-24
	// 保存 callee-saved 寄存器 X19-X27 + LR(X30)
	// 注:X28 = Go G 寄存器,不动!
	// arm64 SP 必须 16-byte 对齐;每对 16 bytes(XX 双寄存器 stp)。
	// 共 5 对 = 80 字节。
	SUB	$80, RSP
	STP	(R19, R20), 0(RSP)
	STP	(R21, R22), 16(RSP)
	STP	(R23, R24), 32(RSP)
	STP	(R25, R26), 48(RSP)
	STP	(R27, R30), 64(RSP)

	// 装 R27 = jitContext(06 §4.2 arm64 寄存器约定)
	MOVD	jitCtxAddr+8(FP), R27

	// 取 codeAddr 到 R8(临时)
	MOVD	codeAddr+0(FP), R8

	// BLR R8(分支并链接到段)
	BL	(R8)

	// 段返回:R0 已是返回值。恢复 callee-saved(逆序 ldp)
	LDP	0(RSP), (R19, R20)
	LDP	16(RSP), (R21, R22)
	LDP	32(RSP), (R23, R24)
	LDP	48(RSP), (R25, R26)
	LDP	64(RSP), (R27, R30)
	ADD	$80, RSP

	// 写回返回值
	MOVD	R0, ret+16(FP)
	RET
