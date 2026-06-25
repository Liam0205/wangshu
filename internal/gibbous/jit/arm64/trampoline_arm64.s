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
// 故本文件用 framesize=$80(80 = 9 callee-saved 寄存器 padded 到 16-byte)
// 让 Go runtime 自动管 prologue/epilogue + LR 保存,我们仅手存 X19-X27
// 进 frame 内。

//go:build wangshu_p4 && linux && arm64

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
//   - Go arm64 auto-prologue 自动 STP X29 X30 + SUB SP 96(80 framesize +
//     16 LR/FP 区);函数体起 SP 指向 user space 起点;
//   - 我们手存 X19-X27(callee-saved 9 个;X28=G 不动)进 frame 内;
//   - BL (R8) 跳进 mmap 段,X30 被 BL 改写;
//   - 段 RET 弹回 BL 下一条;
//   - 手动 LDP 恢复 X19-X27;
//   - Go auto-epilogue 自动恢复 X29 X30 + ADD SP + RET。

TEXT ·callJITFull(SB),NOSPLIT,$80-24
	// Go auto-prologue 已 STP X29 X30 + SUB SP 96。SP 现指向 user space
	// 起点,我们用 frame[0..72] 存 X19-X27(9 寄存器,80 字节 16-byte 对齐
	// 后实际 frame 含 8 字节 padding 在末尾)。
	STP	(R19, R20), 0(RSP)
	STP	(R21, R22), 16(RSP)
	STP	(R23, R24), 32(RSP)
	STP	(R25, R26), 48(RSP)
	MOVD	R27, 64(RSP)

	// 装 R27 = jitContext(06 §4.2)
	MOVD	jitCtxAddr+8(FP), R27

	// 取 codeAddr 到 R8(caller-saved)
	MOVD	codeAddr+0(FP), R8

	// 间接调用 mmap 段——Plan 9 arm64 用 BL (Reg) 表 BLR
	BL	(R8)

	// 段返回:R0 已是返回值。手动恢复 X19-X27(LR/FP 由 epilogue 恢复)
	LDP	0(RSP), (R19, R20)
	LDP	16(RSP), (R21, R22)
	LDP	32(RSP), (R23, R24)
	LDP	48(RSP), (R25, R26)
	MOVD	64(RSP), R27

	// 写回返回值(此时 R0 仍是 mmap 段产生的 X0)
	MOVD	R0, ret+16(FP)
	RET
