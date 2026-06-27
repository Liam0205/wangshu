//go:build wangshu_p4

// dispatcher.go —— P4 Option B Spike 1 trampoline exit-resume 协议 Go 端
// dispatcher(承
// docs/design/p4-method-jit/implementation-progress.md §9.20.9 (5) Go 端
// dispatcher 详细设计 + 实装顺序 5 commits commit-3a)。
//
// **协议位置**(承 §9.20.9 (1)):
//
//	[mmap 段] exit-helper-request 段写 jitCtx.exitArg0=HelperRunCallee + ret
//	   │
//	   ▼
//	[trampoline asm] CMPQ AX, $ExitInlineHelper / CALL ·dispatchInlineHelper
//	   │
//	   ▼
//	[本文件 dispatchInlineHelper] switch jitCtx.exitArg0 路由:
//	   case HelperRunCallee: 经 P4HostState.ExecuteCalleeFromInlineFrame
//	   case HelperGrowStack: arena grow(未来)
//	   case HelperGCBarrier: GC 写屏障(未来,只在写 Go 堆时)
//	   │
//	   ▼
//	[本文件 返 resumeAddr] trampoline 用 codePageAddr + resumeOff 重新 CALL
//	   │
//	   ▼
//	[mmap 段 resume entry] PopVoid0Arg + ret(callee 帧已被 dispatcher 跑完)
//
// **当前 Spike 1 阶段状态**(2026-06-28,commit-3a):
//   - archSupportsFrameInline=false 屏蔽真触发,本 dispatcher 在 production
//     路径不会被触达,但被 trampoline asm CALL 调用站点(commit-3b)预留
//     地址 + 测试钩子使用
//   - 本批 panic 占位(承 jit.HelperRunCalleeAfterFrameInline 同款工程基础
//     锚点),真实装留 commit-5 翻 archSupportsFrameInline=true 同批落地
//   - host 路由经 *Compiler 注入的 P4HostState 接口(承
//     compiler.go::hostState 字段),不引入 jitContext.hostStatePtr 新字段
//     (减少 ABI 表面;真实装时由 trampoline asm 经独立 helper 函数转发)
//
// **未来真实装路径**(Step C-2,等 archSupportsFrameInline 翻 true):
//  1. dispatcher 接受 jitCtx 同时取 host(经独立 setter 注入)
//  2. switch jitCtx.exitArg0 case HelperRunCallee:host.ExecuteCalleeFromInlineFrame
//  3. 返 codePageAddr + resumeOff 让 trampoline 续跑
//
// 设计依据:
//   - §9.20.9 (5) Go 端 dispatcher 详细设计(switch + executeFrom)
//   - §9.20.9 (8) 风险点:dispatcher 内 executeFrom 非 nosplit → 必须切回
//     Go 栈再调(承 §9.20.6 (4) SP 切换协议)
//   - §9.20.9 (8) 错误冒泡:HelperRunCalleeAfterFrameInline 内 raise 时
//     设 jitCtx.exitReason=ExitError + pendingErr,dispatcher 返 0 让
//     trampoline 走错误路径
package jit

// dispatchInlineHelper 是 trampoline 出段后的 helper request 路由器(承
// §9.20.9 (5))。trampoline asm 检 AX=ExitInlineHelper 时 CALL 本函数,
// 本函数读 jitCtx.exitArg0 路由到对应 helper,返 resumeAddr(若 0 则错误
// 路径)。
//
// **入参**:
//   - jitCtx:*JITContext(承 trampoline asm 经 r15 拷给 rdi/SysV ABI)
//
// **返**:
//   - resumeAddr (uintptr):mmap 段内 resume entry 地址(codePageAddr +
//     resumeOff);0 表示错误(trampoline 走错误路径)
//
// **Spike 1 阶段未实装**:本 stub panic 标识未真接入路径——archSupportsFrameInline
// 当前 false,Compile 路径不会真 emit ExitInlineHelper 协议;trampoline asm
// 也不 CALL 本函数(commit-3b 加 dispatcher CALL 段但 dispatcher 不路由);
// production 路径屏蔽 SIGSEGV 风险。真实装留 commit-5 翻 archSupportsFrameInline
// =true 同批落地。
//
// **nosplit + noinline**:承 §9.20.6 (4) helper ABI 协议 + §9.20.9 (8) 风险
// 缓解。
//
//go:nosplit
//go:noinline
func dispatchInlineHelper(jitCtx *JITContext) uintptr {
	_ = jitCtx
	// **未实装占位**:commit-5 真实装时去 panic,加 switch jitCtx.exitArg0
	// + host 路由逻辑。当前 archSupportsFrameInline=false 屏蔽真调用站点,
	// 本 panic 是工程基础锚点(出现 = 真接入未启用 / Compile bug)。
	panic("internal/gibbous/jit.dispatchInlineHelper: not implemented (Spike 1 commit-5 占位)")
}
