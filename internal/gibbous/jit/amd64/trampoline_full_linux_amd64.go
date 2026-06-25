//go:build wangshu_p4 && linux && amd64

package amd64

// CallJITFull 完整版 trampoline(切寄存器 + 装 jitContext + 保存 callee-saved)。
//
// **PJ2 状态:草稿(NOT WIRED)**——实装完整,但 GibbousCode.Run 当前**不调用**
// 本函数(SupportsAllOpcodes 仍全 false)。落地此函数的目的是让 Go runtime
// 兼容性问题(callee-saved 保存恢复 / r15 装 jitContext)在 PJ2 阶段就过编译
// 期检查 + 单测;PJ3+ 启动时直接接入 GibbousCode.Run。
//
// 入参:
//   - codeAddr:mmap 段起点(MmapCode 返回的 *CodePage).Addr())
//   - jitCtx:JITContext 对应的 uintptr(unsafe.Pointer(*JITContext))
//
// 返回:mmap 段 RET 时 RAX 值。
//
// 与 callJIT 的区别:
//   - callJIT:简化形态,不保存 callee-saved 不装 r15;模板内只跑 mov+ret;
//   - callJITFull:完整形态,保存 rbx/rbp/r12/r13/r15(r14 = Go G 寄存器不动)
//   - 装 r15 = jitContext;模板可读 r15+offset 取 arenaBase / 值栈 base /
//     preemptFlag / helper 表(留 PJ3+ 模板扩);
//
//go:noescape
func callJITFull(codeAddr uintptr, jitCtx uintptr) uint64

// CallJITFull 是 callJITFull 的可见包装(单测 + 调用方接入用)。
func CallJITFull(codeAddr uintptr, jitCtx uintptr) uint64 {
	return callJITFull(codeAddr, jitCtx)
}
