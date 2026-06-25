// trampoline_amd64.s —— P4 PJ1 amd64 trampoline 进/出 stub。
//
// **PJ1 简化版**:不切自管栈、不装 jitContext、不保存 callee-saved。直接 CALL
// mmap 段,期望段以 RET 收尾(返 RAX);Go 端拿 RAX。
//
// 与 spike/p4tramp/trampoline_amd64.s 同款形态,本文件主库版本。完整 trampoline
// (切 SP / 装 jitContext / 保存 callee-saved)留 PJ2-PJ5 渐进填实(每加一类
// opcode 模板时配套扩 trampoline 形态)。
//
// 依据:docs/design/p4-method-jit/05-system-pipeline.md §2.4 trampoline 三出口
// + 06-backends.md §4.1 amd64 寄存器约定。
//
// 当前简化形态对应 PJ1 直线模板(MOVE/LOADK/LOADBOOL/LOADNIL/JMP/RETURN):
// 这些 opcode 不调任何 helper,模板内不需 jitContext / 自管栈;只需要从段
// 起点跳进 → 顺序执行模板字节 → RET 回来。RAX 在简化形态下用作模板的
// 「最近一次写值」(LOADK 烧 imm,LOADBOOL 烧 0/1,等)——与 spike S1 形态
// 完全同款。

#include "textflag.h"

// func callJIT(codeAddr uintptr) uint64
//
// PJ1 简化形态:
//   入参 codeAddr +0(FP)  uintptr  ← mmap 段起点(PROT_RX)
//   返回 ret      +8(FP)  uint64   ← mmap 段 RET 时 RAX 值
//
// frame size 0(NOFRAME):不分配 Go 栈帧。
// NOSPLIT:禁 morestack 检查(段瞬时,不深递归)。
//
// 实现:
//   MOVQ codeAddr+0(FP), AX
//   CALL AX
//   MOVQ AX, ret+8(FP)
//   RET
//
// **不踩 Go runtime 的关键点**(承 spike DECISION.md):
//   - 段执行期间不阻塞 Go 调度(~ns 级);
//   - 不调任何 Go 函数(段只跑直线模板);
//   - 段 RET 直接落到 callJIT 的下一条 MOVQ;
//   - Go ABI0 CALL 协议:caller 在 CALL 前已 push 返回地址,callee 的 c3 ret
//     跳回 push 的地址。

TEXT ·callJIT(SB),NOSPLIT|NOFRAME,$0-16
	MOVQ codeAddr+0(FP), AX
	CALL AX
	MOVQ AX, ret+8(FP)
	RET
