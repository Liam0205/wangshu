//go:build wangshu_p4 && linux && amd64

package amd64

// callJIT 跳进 mmap 段执行,期望段以 RET 收尾(返回值在 RAX)。
//
// **PJ1 简化版**(承 spike/p4tramp 同款):不切自管栈、不装 jitContext。完整
// 形态(切 SP / 装 r15=jitContext / r14=arena base / rbx=值栈 base + 保存
// callee-saved)留 PJ2-PJ5 渐进填实。
//
// 本简化形态仅用于 PJ1 直线模板(LOADK 烧 imm 到 RAX、LOADBOOL 烧 0/1、等)——
// 这些 opcode 不调 helper、不动栈、不持外部状态,trampoline 单一 CALL +
// RET 即可。PJ2 算术 / PJ4 表 IC 引入 helper 调用时,本 stub 升级到完整版。
//
// 实参 codeAddr 必须指向 PROT_READ|PROT_EXEC 段(MmapCode 返回的 CodePage.Addr())。
//
// 实现:trampoline_amd64.s::callJIT(NOSPLIT|NOFRAME)。
//
//go:noescape
func callJIT(codeAddr uintptr) uint64

// CallJIT 是 callJIT 的可见包装。Test/调用方经此调用,允许包内单测断言
// 「mmap 段确实被走到」(prove-the-path-under-test 纪律)。
//
// 注:本 PJ1 阶段不引入 codeAddr 校验(nil ptr / 越界检查留 PJ2+ 完整版)。
// 调用方负责传合法地址(MmapCode 返回的 *CodePage).Addr())。
func CallJIT(codeAddr uintptr) uint64 {
	return callJIT(codeAddr)
}
