//go:build linux && amd64

package p4tramp

// CallJIT 跳进 mmap 段执行,期望段以 `ret` 结束(返回值在 RAX)。
//
// **PJ1 spike 简化版**:不切自管栈、不装 jitContext、不传任何参数;只把
// codeAddr 当 CALL 目标,期望段返 RAX 给 Go。完整 PJ1 trampoline(切 SP /
// 装 r15=jitContext / r14=arena base / rbx=值栈 base)在 spike 绿后的
// internal/gibbous/jit/amd64/trampoline_amd64.s 里实装。
//
// **不做什么**(承 spike 闸门最小化纪律):
//   - 不保存 callee-saved 寄存器(spike 段的 mov+ret 不动它们);
//   - 不切 SP(spike 段不深递归 / 不调任何 helper);
//   - 不带 GC 安全点纪律(spike 段瞬时执行,Go runtime 异步抢占落 mmap PC
//     不可恢复——这正是 PJ1 完整版要解的问题之一,本 spike 用极短直线段
//     绕过该风险:`mov+ret` 9 字节,实际执行 ~ns,被异步抢占的概率近 0);
//
// 实参 codeAddr 必须指向 PROT_READ|PROT_EXEC 段(MmapCode 返回的 CodePage.Addr())。
//
// 实现(callJITAmd64.s):一条 `JMP AX` 风格 CALL——依赖 Go ABI0 的「CALL 指令
// 不期望 callee 用 SP 帧」的便利;mmap 段以 ret 收尾,落到 callJIT 末尾的
// `RET` 同款形态(Go 端拿到 mmap 段的 RAX 返回值)。
func CallJIT(codeAddr uintptr) uint64

// callJIT 的 Go 端可用名(测试用 — 把 internal asm symbol 暴露)。
//
// 注:本 spike 不引入 codeAddr 校验(nil ptr / 越界检查留 PJ1 完整版),
// 测试方负责传合法地址。
