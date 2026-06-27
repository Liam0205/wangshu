// trampoline_spec_amd64.s —— P4 PJ2 投机模板 trampoline。
//
// **与 trampoline_full_amd64.s 区别**:full 版只装 r15=jitContext;spec
// 版同时装 rbx=valueStackBase(JIT 段经 rbx+disp32 直读/写值栈槽位,
// 跳过 host helper round-trip)。
//
// **寄存器约定**(承 06 §4.1):
//   - r15 = jitContext(callee-saved,P4 全局保留)
//   - rbx = valueStackBase(P4 PJ2+ 投机模板专用,callee-saved)
//   - r14 = Go G(进入 mmap 段前 PUSH 保存,出段后 POP 恢复;**spec 段内
//     可用 R14 作 scratch 装 arena base**——PJ4 IC 六路径 + PJ5 SELF
//     spec template 同款手法,承 PR #26 外部审查 R14 ABI 违约修复:
//     段内瞬时(~ns)覆写 R14 = arena base,段尾正常 RET 返 trampoline
//     时 POP R14 救回 Go G,Go runtime 后续任何 morestack/抢占/同步取 g
//     操作均见正确 g 值)
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
	// 保存 callee-saved 寄存器(rbx/rbp/r12/r13/r14/r15)。
	// **r14 = Go G**:本 trampoline NOSPLIT 段内不会触发 morestack/抢占
	// (CALL AX 是直接 indirect call,跳进 PROT_RX mmap 段;段内无 Go 函数
	// 调用,无栈分配,无回边检查点 = 不触发任何 Go runtime 取 g 操作)。
	// 故段内可借用 R14 作 arena base scratch——段尾 RET 返本 trampoline 时
	// POP R14 恢复 Go G,Go runtime 后续抢占/morestack/同步取 g 均见正确 G。
	// 承 PR #26 外部审查 R14 ABI 违约修复(2026-06-28):上一版 trampoline
	// 不 push/pop R14,段内 PJ4 IC 模板 mov r14, [r15+arenaBaseOff] 覆写
	// R14 = arena base 后段尾 RET 直接污染 Go G,生产负载 morestack/抢占
	// 取 g 时 SEGV;本批 PUSH/POP R14 救济保 Go G 正确性。
	PUSHQ BX
	PUSHQ BP
	PUSHQ R12
	PUSHQ R13
	PUSHQ R14
	PUSHQ R15

	// 装 r15 = jitContext, rbx = valueStackBase
	MOVQ jitCtx+8(FP), R15
	MOVQ vsBase+16(FP), BX

	// 取 codeAddr + CALL 段
	MOVQ codeAddr+0(FP), AX
	CALL AX

	// 段返回:RAX 已是返回值。
	//
	// **§9.20.9 trampoline exit-resume 协议 Run-end dispatcher 实装**(commit-5a
	// 修正 commit-3c):设计草案 (4) 假设 trampoline asm 内 CALL Go dispatcher,
	// 但实际跨包 + Plan 9 ABI 复杂度高;改用 **Run 端 Go 函数做 dispatcher**:
	// Run 检 raxSpec==ExitInlineHelper → 调 dispatchInlineHelper → 二次 callJITSpec
	// 跳 resume entry(全在 Go 端做,trampoline asm 透传 RAX 不解读)。
	//
	// 故 trampoline asm 段返后直接走常规弹栈,不再 CMP RAX——Run 端读 RAX 后
	// 路由。原 commit-3c 的 CMP + INT 3 段撤(若保留 INT 3 会在 commit-5
	// archSupportsFrameInline=true + emit ExitInlineHelper 段后真触发 SIGTRAP)。
	//
	// 段返回:RAX 已是返回值。恢复 callee-saved(逆序 pop)。
	// **R14 恢复 Go G 救济**:POP R14 把 entry 时 Go runtime 装的 G 值
	// 写回 R14,段内 mov r14, arenaBase 的覆写到此撤消。
	POPQ R15
	POPQ R14
	POPQ R13
	POPQ R12
	POPQ BP
	POPQ BX

	// 写回返回值
	MOVQ AX, ret+24(FP)
	RET
