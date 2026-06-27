//go:build wangshu_p4

// helpers.go —— P4 Option B Spike 1 helper 函数集(承
// docs/design/p4-method-jit/implementation-progress.md §9.20.6 helper call
// ABI 协议设计)。
//
// **关键 ABI 约束**(承 §9.20.6):
//   - `//go:nosplit`:禁 morestack 插桩,helper 在自管栈上跑(承 05 §3.4 自管
//     栈协议)
//   - `//go:noinline`:避免 inlining 破坏栈帧协议(mmap 段经 archEmitHelperCall
//     间接 CALL helper 地址,inline 后地址消失)
//   - 首参 `*JITContext`(amd64 rdi / arm64 x0 SysV ABI 寄存器)
//   - 后续参数 SysV 顺序(rsi/rdx/rcx/r8/r9)
//   - 返值 rax / x0(0=OK / 1=ERR,错误状态写 jitContext.exitReason)
//   - r14=G(amd64)/ x28=G(arm64)严格不动
//   - mmap 段禁直接写 Go 堆指针(GC 三色不变式)
//
// **Spike 1 阶段状态**(2026-06-28):helper 函数声明就位 +
// archEmitHelperCall 调用站点预留地址,但实际 Run 端真接入留 Step E
// (compileSpecSelfCall 启用 useFrameInline=true + archSupportsFrameInline 翻
// true 同批)。当前 archSupportsFrameInline=false 屏蔽真调用 path,本 helper
// 在 production 路径不会被触达,但函数地址可被 archEmitHelperCall 引用作 emit。
package jit

// HelperRunCalleeAfterFrameInline mmap 段经 BuildVoid0ArgSkeleton 完成
// enterLuaFrame 字节级 inline 后,调本 helper 跑 callee Lua 体执行(等价
// 跳过 enterLuaFrame 的 doCall + executeFrom 流程)。
//
// 入参(SysV ABI,amd64 rdi/rsi/rdx 顺序):
//   - jitCtx:*JITContext(承 r15 → rdi)
//   - base:caller 帧 R0 偏移(承 jitContext.valueStackBase 算栈地址)
//   - retA:CALL 段 A 字段(callee 返值落 R(retA..retA+N-1))
//
// 返值:0=OK(callee 体已完成 + 返值已落 R(retA..))/ 1=ERR(错误状态写
// jitContext.exitReason,mmap 段段尾检 rax=1 跳 jitExit stub)。
//
// **Spike 1 阶段未实装**:本 stub panic 标识未真接入路径——archSupportsFrameInline
// 当前 false,Compile 路径不会真 emit 调用本函数;production 路径屏蔽 SIGSEGV
// 风险。真实装留 Step C-1 helper 实装批次(承 §9.20.6 + §9.20.3 工期估算)。
//
//go:nosplit
//go:noinline
func HelperRunCalleeAfterFrameInline(jitCtx *JITContext, base int32, retA int32) int32 {
	_ = jitCtx
	_ = base
	_ = retA
	// **未实装占位**:Step C-1 真实装时去掉 panic,加 doCall + executeFrom 逻辑。
	// 当前 archSupportsFrameInline=false 屏蔽真调用,本 panic 是工程基础锚点
	// (调用站点暴露 = 真接入未启用 / Compile bug)。
	panic("internal/gibbous/jit.HelperRunCalleeAfterFrameInline: not implemented (Spike 1 Step C-1 占位)")
}
