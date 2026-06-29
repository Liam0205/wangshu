// trampoline_arm64.s —— P4 PJ8 arm64 完整 trampoline asm。
//
// 承 docs/design/p4-method-jit/05-system-pipeline.md §2.4 + 06-backends.md §4.2.
//
// **关键技术约束**:
//   - X28 = Go G 寄存器(Go 1.17+ ABI0 arm64)——必须保持不动;
//   - X27 装 jitContext(callee-saved);
//   - Plan 9 arm64 framesize 决定 Go runtime stack walker 如何 unwind——
//     framesize > 0 时 Go 自动 prologue/epilogue 管 LR/FP + SP;NOFRAME +
//     手动 SUB SP 会让 runtime stack walker 错算 caller LR 位置 → sigpanic
//     时 unwind 失败「unexpected return pc」(承上批 1c74df9 失败教训)。
//
// 故本文件用 framesize=$80(让 Go runtime 自动管 prologue/epilogue + LR/FP
// 保存),我们仅手存 X19-X27 进 frame 内。
//
// **Go arm64 auto-prologue 实际 frame 布局**(F3-#3b 真物理 M1 实证,LR
// slot 位置承 PR#27 SIGSEGV 调试):Go 编译器对 `TEXT name,NOSPLIT,$N-arglen`
// 在 arm64 上生成的 prologue 等价于:
//
//   STR.W X30, [SP, #-(N+16)]!   // SP -= N+16,LR 存 [SP+0]
//   STUR  X29, [SP, #-8]         // FP 存 [SP-8](user space 外、SP 下方)
//   SUB   $8, SP, X29            // X29 = SP - 8
//   <user code 见此处 SP 即 user space 起点,大小 N 字节>
//
// 所以 user space `[SP+0 .. SP+N)` 的**首 8 字节 [SP+0..8) 被 Go 写为 LR**,
// epilogue `LDR.P X30, [SP], #(N+16)` 从同一 slot 读 LR + 弹栈。**user 不可
// 写入 [SP+0..8),否则 LR 被覆盖、段 RET 后取错值 → SIGSEGV/SIGBUS**。
//
// **frame 布局**(framesize=$80):
//   [SP+ 0 .. SP+ 8) — Go auto-prologue 写入 LR(user 必须避让)
//   [SP+ 8 .. SP+72) — user 手存 X19-X26(8 寄存器,STP×4)
//   [SP+72 .. SP+80) — user 手存 X27(MOVD,正好 80 字节充满,无 padding)

//go:build wangshu_p4 && arm64 && (linux || (darwin && cgo))

#include "textflag.h"

// func callJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64
//
// 入参:
//   codeAddr   +0(FP)   uintptr
//   jitCtxAddr +8(FP)   uintptr
// 返回:
//   ret        +16(FP)  uint64
//
// 实现:
//   - Go arm64 auto-prologue 自动 STR LR 进 [SP+0] + 弹 FP 协议管 SP 96;
//   - 函数体起 SP 指向 user space 起点(80 字节);
//   - 我们从 user offset 8 开始手存 X19-X27,**避让 [SP+0..8) LR slot**;
//   - BL (R8) 跳进 mmap 段,X30 被 BL 改写;
//   - 段 RET 弹回 BL 下一条;
//   - 手动 LDP 恢复 X19-X27(从 user offset 8);
//   - Go auto-epilogue 自动恢复 LR(从 [SP+0])+ FP + ADD SP + RET。

TEXT ·callJITFull(SB),NOSPLIT,$80-24
	// Go auto-prologue 已 STR.W X30 [SP,#-96]! → LR 在 [SP+0],占 [SP+0..8)。
	// user space 起 [SP+0],但 [SP+0..8) 是 LR slot,我们从 [SP+8) 起手存
	// X19-X27(对位 80 字节 frame:8 LR + 9 寄存器 × 8B = 80,无 padding)。
	STP	(R19, R20), 8(RSP)
	STP	(R21, R22), 24(RSP)
	STP	(R23, R24), 40(RSP)
	STP	(R25, R26), 56(RSP)
	MOVD	R27, 72(RSP)

	// 装 R27 = jitContext(06 §4.2)
	MOVD	jitCtxAddr+8(FP), R27

	// 取 codeAddr 到 R8(caller-saved)
	MOVD	codeAddr+0(FP), R8

	// 间接调用 mmap 段——Plan 9 arm64 用 BL (Reg) 表 BLR
	BL	(R8)

	// 段返回:R0 已是返回值。手动恢复 X19-X27(LR/FP 由 epilogue 恢复)
	LDP	8(RSP), (R19, R20)
	LDP	24(RSP), (R21, R22)
	LDP	40(RSP), (R23, R24)
	LDP	56(RSP), (R25, R26)
	MOVD	72(RSP), R27

	// 写回返回值(此时 R0 仍是 mmap 段产生的 X0)
	MOVD	R0, ret+16(FP)
	RET

// func callJITSpec(codeAddr uintptr, jitCtxAddr uintptr, vsBaseAddr uintptr) uint64
//
// 入参:
//   codeAddr   +0(FP)   uintptr
//   jitCtxAddr +8(FP)   uintptr
//   vsBaseAddr +16(FP)  uintptr
// 返回:
//   ret        +24(FP)  uint64
//
// **vs callJITFull**:多装 X26 = vsBaseAddr(承 06 §4.2 arm64 trampoline
// 协议,对位 amd64 callJITSpec 装 rbx=vsBase)。Spec 模板段(PJ2 投机
// + PJ3 FORLOOP body/RegLimit)需要值栈寻址 [x26+disp],本 trampoline
// 装好后段内可直接用。
//
// 与 callJITFull 同款 framesize=$80 + auto-prologue 管 LR/FP,callee-saved
// X19-X27 手存 frame 内([SP+8..SP+80),避让 [SP+0..8) LR slot);X28=G
// 不动;X26/X27 由本函数在 STP 后装入。
TEXT ·callJITSpec(SB),NOSPLIT,$80-32
	// frame 布局对位 callJITFull:[SP+0..8) Go LR slot / [SP+8..72) X19-X26 /
	// [SP+72..80) X27。
	STP	(R19, R20), 8(RSP)
	STP	(R21, R22), 24(RSP)
	STP	(R23, R24), 40(RSP)
	STP	(R25, R26), 56(RSP)
	MOVD	R27, 72(RSP)

	// 装 R26 = vsBaseAddr + R27 = jitCtxAddr(承 06 §4.2)
	// **顺序**:STP 已保存 R26/R27 原值,现可安全覆盖。
	MOVD	jitCtxAddr+8(FP), R27
	MOVD	vsBaseAddr+16(FP), R26

	// 取 codeAddr 到 R8(caller-saved)
	MOVD	codeAddr+0(FP), R8

	// 间接调用 mmap 段
	BL	(R8)

	// 段返回:R0 是返回值。
	//
	// **§9.20.9 trampoline exit-resume 协议 Run-end dispatcher 实装**(commit-5a
	// 修正 commit-3c):改用 Run 端 Go 函数做 dispatcher,trampoline asm 段返
	// 后直接走常规弹栈;Run 检 r0==ExitInlineHelper → 调 dispatchInlineHelper
	// → 二次 callJITSpec 跳 resume entry(全 Go 端,对位 amd64 同款手法)。
	// 原 commit-3c 的 CMP + BRK 段撤(避免 commit-5 真发出 ExitInlineHelper
	// 后真触发 SIGTRAP)。
	//
	// 手动恢复 X19-X27(LR/FP 由 epilogue 恢复)
	LDP	8(RSP), (R19, R20)
	LDP	24(RSP), (R21, R22)
	LDP	40(RSP), (R23, R24)
	LDP	56(RSP), (R25, R26)
	MOVD	72(RSP), R27

	// 写回返回值
	MOVD	R0, ret+24(FP)
	RET
