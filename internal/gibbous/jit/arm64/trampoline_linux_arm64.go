//go:build wangshu_p4 && linux && arm64

package arm64

// callJITFull 跳进 mmap 段执行,期望段以 RET(0xd65f03c0)收尾(返回值在
// X0)。trampoline 内保存 callee-saved X19-X27 + LR + 装 X27=jitContext。
//
// **PJ8 简化形态**(对位 amd64 PJ2):不切 SP——继续在 Go 栈上跑 mmap 段。
// 完整切 SP 留 PJ3+(算术 + helper 调用引入子 BLR 时启用)。
//
// 实参 codeAddr 必须指向 PROT_READ|PROT_EXEC 段(MmapCode 返回的
// CodePage.Addr()),且段已 icache flush(否则取指错误)。
//
// 实现:trampoline_arm64.s::callJITFull(NOSPLIT,$80 framesize——LR/FP 由
// Go 编译器自动管,callee-saved X19-X27 经 5 对 STP 手动保存对齐 16 字节)。
//
//go:noescape
func callJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64

// CallJITFull 是 callJITFull 的可见包装。Test/调用方经此调用,允许包内
// 单测断言「mmap 段确实被走到」(prove-the-path-under-test 纪律)。
//
// 注:arm64 端 mmap 段必须经 flushICacheArm64 后才能执行,否则 i-cache
// 与 d-cache 不一致(取指错误,03 §2.3.1)。MmapCode 已在末尾 flush。
func CallJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64 {
	return callJITFull(codeAddr, jitCtxAddr)
}
