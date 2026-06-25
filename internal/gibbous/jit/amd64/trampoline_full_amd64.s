// trampoline_full_amd64.s —— P4 PJ2 完整 trampoline asm 草稿(切 SP + 保存
// callee-saved + 装 jitContext)。
//
// 承 docs/design/p4-method-jit/05-system-pipeline.md §2.4 trampoline 进出
// 三出口协议 + §3.3 jitContext + §3.4 自管栈 + 06-backends.md §4.1 amd64
// 寄存器约定。
//
// **PJ2 状态:草稿(NOT WIRED)**——本文件实装完整 trampoline asm,但当前
// PJ2 范围内 GibbousCode.Run **不调用** callJITFull(留 PJ3+ 接入)。SupportsAllOpcodes
// 仍全 false ⇒ 无 Proto 走 P4 路径,本 asm 是 dead-code,不影响主库行为。
//
// 落地此 asm 的目的:让 PJ3+ 启动时 Go runtime 兼容性问题(切 SP 时 r14=G
// 寄存器保持 / Go ABI0 callee-saved 协议 / morestack 协议)在 PJ2 阶段就过编
// 译期检查,而不是 PJ3+ 启动时才发现 asm 写不出来。
//
// **关键技术约束**(承 P4 设计 + Go runtime 实测):
//   - r14 = Go G 寄存器(Go 1.17+ ABI0)——切 SP 期间必须保持不动,否则
//     Go runtime stop-the-world 找不到 g 立即 SEGV。本 asm 用 r15 装
//     jitContext(06 §4.1 单一事实源),不动 r14;
//   - callee-saved(amd64 SysV ABI):rbx, rbp, r12, r13, r14, r15 — 但
//     Go ABI0 内 r14 是 G,不能动;r15 是 Go ABI0 freely available — P4 用
//     它装 jitContext(承 06 §4.1);其它 callee-saved (rbx/rbp/r12/r13)
//     trampoline 入口 push、出口 pop;
//   - NOSPLIT|NOFRAME:trampoline 自身不分配 Go 栈帧,不被 morestack 检查
//     插桩——这是「JIT 段 + trampoline 路径不经 Go runtime 调度」前提。

#include "textflag.h"

// func callJITFull(codeAddr uintptr, jitCtx uintptr) uint64
//
// 入参:
//   codeAddr +0(FP)   uintptr  ← mmap 段起点(PROT_RX)
//   jitCtx   +8(FP)   uintptr  ← *JITContext(Go 堆,r15 装载)
// 返回:
//   ret      +16(FP)  uint64   ← mmap 段执行后 RAX 值
//
// 实现要点:
//   1. 保存 callee-saved(rbx/rbp/r12/r13;r14=G 不动;r15 即将被覆盖,先存);
//   2. 装 r15 = jitContext;
//   3. CALL codeAddr 跳进 mmap 段——段内可读 r15+offset 取 jitContext 字段;
//   4. 段返回后 r15 可能已被 mmap 段污染(若段调过 helper);恢复 callee-saved;
//   5. 把 RAX 写回 ret。
//
// **PJ2 简化形态**:不切 SP——继续在 Go 栈上跑 mmap 段(段瞬时,~ns,与 PJ1
// 同款风险面)。完整切 SP 留 PJ3+(算术 + helper 调用引入子 CALL 时启用)。

TEXT ·callJITFull(SB),NOSPLIT|NOFRAME,$0-24
	// 保存 callee-saved 寄存器(rbx/rbp/r12/r13/r15)
	// 注:r14 = Go G 寄存器,不动!
	PUSHQ BX
	PUSHQ BP
	PUSHQ R12
	PUSHQ R13
	PUSHQ R15

	// 装 r15 = jitContext(06 §4.1 amd64 寄存器约定)
	MOVQ jitCtx+8(FP), R15

	// 取 codeAddr 到 AX
	MOVQ codeAddr+0(FP), AX

	// CALL mmap 段(段以 RET 收尾,带 RAX 返回值)
	CALL AX

	// 段返回:RAX 已是返回值。恢复 callee-saved(逆序 pop)
	POPQ R15
	POPQ R13
	POPQ R12
	POPQ BP
	POPQ BX

	// 写回返回值
	MOVQ AX, ret+16(FP)
	RET
