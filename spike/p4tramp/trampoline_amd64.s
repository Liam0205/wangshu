// trampoline_amd64.s —— P4 PJ1 spike,Go → mmap 段最小 round-trip。
//
// Plan 9 asm(Go 风格)。对位 docs/design/p4-method-jit/05-system-pipeline.md
// §2.4(trampoline 进/出 stub)+ 06-backends.md §4.1(amd64 寄存器约定)。
//
// **PJ1 spike 极简版**:不切 SP / 不装 jitContext / 不保存 callee-saved。
// 完整版 trampoline 留 internal/gibbous/jit/amd64/trampoline_amd64.s 实装。

#include "textflag.h"

// func CallJIT(codeAddr uintptr) uint64
//
// 入参:
//   codeAddr +0(FP)  uintptr  ← mmap 段起点(PROT_RX)
// 返回:
//   ret      +8(FP)  uint64   ← mmap 段执行后 RAX 值
//
// frame size 0:不分配 Go 栈帧(`NOFRAME` 标志);Go 端调用方负责栈布局。
//
// 实现:
//   MOVQ codeAddr+0(FP), AX   ; AX = mmap 段起点
//   CALL AX                    ; 跳进 mmap 段;段以 ret 收尾,带 RAX 返回值
//   MOVQ AX, ret+8(FP)         ; 把 RAX 写回 Go 帧返回槽
//   RET                        ; 回 Go 调用方
//
// **不踩 Go runtime 的关键点**:
//   - mmap 段执行期间不阻塞 Go 调度(段瞬时,~ns);
//   - 不调任何 Go 函数(段只跑 mov + ret);
//   - callee-saved 不动(mov+ret 不动 r12-r15/rbp);
//   - Go ABI0 的 CALL 协议:caller 在 CALL 前已 push 返回地址到 SP,callee 的
//     ret 跳回该地址——mmap 段的 `c3` (ret) 直接落到我们 CALL 的下一条 MOVQ。
//
// NOSPLIT:不能让 Go runtime 在我们 CALL 前 / RET 后插 morestack 检查
// (通常没必要,但为了让 trampoline 路径行为可预测)。

TEXT ·CallJIT(SB),NOSPLIT|NOFRAME,$0-16
	MOVQ codeAddr+0(FP), AX
	CALL AX
	MOVQ AX, ret+8(FP)
	RET
