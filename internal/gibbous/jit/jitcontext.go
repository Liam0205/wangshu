//go:build wangshu_p4

// jitcontext.go —— P4 JIT 执行上下文 struct(承
// docs/design/p4-method-jit/05-system-pipeline.md §3.3 jitContext 字段表)。
//
// **方案 A**(用户裁决,03 §4 + §8):**P4 投机生命周期 P4 自管**。jitContext
// 是 P4 跨 Go ↔ JIT 世界边界的「关键耦合点 #4」(00 §3 关键耦合点 4)——
// 承载「JIT 码所需的所有 Go 侧能力(arena base / helper 表 / preemptFlag /
// exit reason code)」装入 Go 堆上的 jitContext struct,经固定寄存器(amd64
// r15)传入。
//
// **PJ1+2 阶段简化形态**:本文件先建 struct 字段骨架 + 构造函数 + 测试
// 钩子,但**不在 PJ1 完整接入 trampoline asm**(PJ1 简化形态不切 SP 不装
// jitContext,trampoline 单 CALL+RET 即可)。完整接入(amd64 trampoline 切
// SP 时 MOVQ jitContext, R15)留 PJ2+ 启动时同批落地。
//
// 设计依据:
//   - 05 §3.3:jitContext struct 字段(arena base / 值栈 base / preemptFlag /
//     helper 表 / exit reason code / spill 区起点);per-arch 寄存器固定
//     (amd64 r15);Go 堆分配纪律。
//   - 06 §4.1:amd64 寄存器约定(r15 = jitContext);06 §4.2:arm64 寄存器约定
//     (x28 = jitContext)。
//   - 03 §8:P4 自管投机生命周期——p4SpecState 子状态字段挂 jitContext 还是
//     独立 map?当前定稿独立 map(per-Proto 状态,跨调用持久,jitContext 是
//     per-call scratch),减少 jitContext 字段量(承 03 §8.4 ✅ 自管 deopt 计数
//     与 P4StuckSpeculation 状态)。
package jit

import (
	"sync/atomic"
	"unsafe"
)

// JITContextPreemptFlagOffset 是 preemptFlag 字段相对 *JITContext 的字节
// 偏移——JIT 模板字节级 codegen 读取本偏移做 safepoint check
// (cmp byte ptr [r15 + JITContextPreemptFlagOffset], 0;jne deopt)。
//
// **byte 比较纪律**:preemptFlag 是 atomic.Uint32(4 字节)但实际只取
// 0/1 两值,crescent 端置 1 时设 low byte=1(little-endian),故 `cmpb 0`
// 检测 !=0 在当前协议下正确——避免发 dword cmp 浪费一字节 + ModRM SIB。
// 如未来扩展 preemptFlag 用高位 bit 需改 `cmpd` 形态(对位 EmitCmpDword
// 留 PJ3+ 工程基础)。
//
// 用 unsafe.Offsetof 算出而非硬编码:Go runtime 不保证 struct 内字段顺序
// 跨版本一致(虽然 64-bit 系统 + 顺序对齐通常稳定),Offsetof 一次性算
// 死编译期常量。PJ3+ FORLOOP 字节级内联回边检查点经本偏移直发。
const (
	JITContextArenaBaseOffset      = unsafe.Offsetof(JITContext{}.arenaBase)
	JITContextValueStackBaseOffset = unsafe.Offsetof(JITContext{}.valueStackBase)
	JITContextPreemptFlagOffset    = unsafe.Offsetof(JITContext{}.preemptFlag)
	JITContextExitReasonOffset     = unsafe.Offsetof(JITContext{}.exitReasonCode)
	// PJ5 Option B Spike 1+ 帧建立内联:暴露 ciDepth / ciSegBase / top 的 host
	// 字节地址(承 §9.20),mmap 段经 r15+offset 解引 uintptr 后字节级 inc/dec
	// CI 段。复用 P3 PW10 Stage 1a/2 镜像字(crescent.State.ciDepthRef /
	// ciSegBaseRef / topRef),但用 host addr(uintptr)而非 wasm linear memory
	// offset(P3 wasm 段 vs P4 mmap 段两端不同寻址协议)。
	JITContextCIDepthAddrOffset   = unsafe.Offsetof(JITContext{}.ciDepthAddr)
	JITContextCISegBaseAddrOffset = unsafe.Offsetof(JITContext{}.ciSegBaseAddr)
	JITContextTopAddrOffset       = unsafe.Offsetof(JITContext{}.topAddr)

	// **§9.20.9 trampoline exit-resume 协议字段** (Spike 1 真接入 commit-1):
	JITContextExitArg0Offset  = unsafe.Offsetof(JITContext{}.exitArg0)
	JITContextResumeOffOffset = unsafe.Offsetof(JITContext{}.resumeOff)

	// **§9.20.9 trampoline exit-resume 协议 codePageAddr 字段** (Spike 1
	// 真接入 commit-3b):dispatcher 算 resume entry 用 codePageAddr +
	// resumeOff;Run 端 emit 时记录 codePage 起点。承设计草案 (5)。
	JITContextCodePageAddrOffset = unsafe.Offsetof(JITContext{}.codePageAddr)

	// **PJ10-native addition**: savedGoG is used by mmap-segment helper
	// calls to preserve the Go ABIInternal invariant that R14 = G. The
	// Go-side Run wrapper writes G into it via saveGoG; the mmap segment
	// emits `mov r14, [r15+savedGoGOff]` before each Go helper call to
	// restore R14 = G, otherwise Go's function prologue crashes.
	// See [[project-pj10-native-longtask]] R14-save-wrapper.
	JITContextSavedGoGOffset = unsafe.Offsetof(JITContext{}.savedGoG)

	// JITContextSegCallDepthOffset is the byte offset of segCallDepth
	// (issue #50 Spike 5). The callee RETURN emit reads it via
	// `cmp dword [r15+off], 0` to branch between the segment-exit and
	// in-segment-teardown paths; the caller CALL fast body increments /
	// decrements it around the segment-to-segment `call`.
	JITContextSegCallDepthOffset = unsafe.Offsetof(JITContext{}.segCallDepth)

	// JITContextSegCallDeoptOffset is the byte offset of segCallDeopt
	// (issue #50 Spike 5). arith / compare / CALL slow blocks test
	// `cmp dword [r15+segCallDepthOff], 0` and, when nonzero, set
	// `mov dword [r15+segCallDeoptOff], 1` + ret to deopt the seg2seg
	// call chain.
	JITContextSegCallDeoptOffset = unsafe.Offsetof(JITContext{}.segCallDeopt)

	// Inline GETUPVAL ABI (issue #50 Spike 5): the running frame's
	// closure GCRef, the running thread's stack-slot-0 host address, and
	// the single-thread inline-safety flag. The inline GETUPVAL emit
	// reads all three via r15+offset.
	JITContextCurrentClosureRefOffset = unsafe.Offsetof(JITContext{}.currentClosureRef)
	JITContextThreadStackBase0Offset  = unsafe.Offsetof(JITContext{}.threadStackBase0)
	JITContextInlineUpvalSafeOffset   = unsafe.Offsetof(JITContext{}.inlineUpvalSafe)

	// JITContextValueStackEndOffset is the byte offset of valueStackEnd
	// (issue #80): the seg2seg CALL fast body's stack-bound guard reads
	// it to verify the callee frame fits in the current stack segment.
	JITContextValueStackEndOffset = unsafe.Offsetof(JITContext{}.valueStackEnd)

	// JITContextSpillBaseOffset / JITContextSpillTopOffset are the byte
	// offsets of the self-managed spill stack bounds (issue #89). The
	// trampoline reads spillBase to switch SP onto the spill stack before
	// entering the segment (MOVQ [r15+spillBaseOff], SP), so deep seg2seg
	// recursion descends on the spill buffer instead of the goroutine
	// stack's NOSPLIT allowance. spillBase is the high-address end (SP
	// entry point for a down-growing stack); spillTop is the low-address
	// growth limit. Both 0 => no switch (baseline, goroutine stack).
	JITContextSpillBaseOffset = unsafe.Offsetof(JITContext{}.spillBase)
	JITContextSpillTopOffset  = unsafe.Offsetof(JITContext{}.spillTop)

	// JITContextSavedGoSPOffset is the byte offset of savedGoSP (issue
	// #89): the trampoline stashes the goroutine SP here before switching
	// SP onto the spill stack, and reloads it on the way out.
	JITContextSavedGoSPOffset = unsafe.Offsetof(JITContext{}.savedGoSP)

	// JITContextSegCallFuelOffset is the byte offset of segCallFuel: the
	// seg2seg CALL fast body decrements it once per in-segment dispatch
	// and skips to the host path when it reaches zero, so the step
	// budget / cancel context can preempt execution that would otherwise
	// never leave the mmap segment (fuzz crasher f2165a93dd62892d:
	// fib(5510) under a step budget hung forever once segToSegDepthCap
	// made depth-128 subtrees fully in-segment — ~phi^128 calls with no
	// billing point).
	JITContextSegCallFuelOffset = unsafe.Offsetof(JITContext{}.segCallFuel)
)

// **§9.20.9 协议状态码常量** (Spike 1 真接入 + future helper request 路由):

// JIT exit reason codes(承 §9.20.9 (3) 协议状态码 + exitReasonCode 字段):
const (
	ExitNormal       uint32 = 0 // 正常 RET 出段
	ExitError        uint32 = 1 // ERR 冒泡(state.pendingErr 已置)
	ExitOSR          uint32 = 2 // reserved: spec-template deopt uses a RAX sentinel, not this code (see p4state.go); kept so ExitInlineHelper stays 3
	ExitInlineHelper uint32 = 3 // Spike 1 helper request(jitCtx.exitArg0 = helper code)
)

// JIT inline helper request codes(承 §9.20.9 (3) 协议状态码):
const (
	HelperRunCallee uint64 = 1 // Spike 1 Step C-1:跑 callee Lua 体
	HelperGrowStack uint64 = 2 // 未来:arena grow 触发
	HelperGCBarrier uint64 = 3 // 未来:GC 写屏障(只在写 Go 堆时)

	// PJ10 native emit exit-reason codes for shim-based ops that can't
	// be shim-called from inside the mmap segment (issue #38). The
	// segment writes the helper code into low bits of exitArg0 along
	// with packed op args, sets resumeOff to the next-op offset, and
	// RETs; nativeCode.Run's dispatcher reads exitArg0, invokes the
	// corresponding host method, then reenters the mmap segment via
	// codePage + resumeOff.
	HelperGetTable    uint64 = 10
	HelperSetTable    uint64 = 11
	HelperGetGlobal   uint64 = 12
	HelperSetGlobal   uint64 = 13
	HelperGetUpval    uint64 = 14
	HelperSetUpval    uint64 = 15
	HelperNewTable    uint64 = 16
	HelperSelf        uint64 = 17
	HelperUnm         uint64 = 18
	HelperLen         uint64 = 19
	HelperConcat      uint64 = 20
	HelperSetList     uint64 = 21
	HelperArithSlow   uint64 = 22
	HelperCompareSlow uint64 = 23
	HelperCall        uint64 = 24

	// HelperReturn is the exit-reason code for RETURN in multi-return
	// Protos. Unlike other helpers it terminates the segment run: the
	// dispatcher calls host.DoReturn with the packed (a, b, pc) and
	// does NOT reenter the mmap segment. Single-return Protos keep the
	// legacy `xor eax, eax; ret` + Go-side DoReturn(retA/retB/retPC)
	// path, which saves the exitArg0 packing on the hot exit.
	HelperReturn uint64 = 25

	// HelperExecutePlainCall is the exit-reason code the PJ10 native
	// EmitCallInline fast path emits after the segment has written the
	// callee CI slot and incremented ciDepth (issue #50 Spike 2). The
	// dispatcher's HelperExecutePlainCall case calls
	// host.ExecutePlainCallInlineFrame, which drives executeFrom (or
	// the zero-cross P4-callee path) and rebalances ciDepth for the
	// segment's PopFrame sequence.
	//
	// exitArg0 packing for HelperExecutePlainCall follows the standard
	// emitExitReason layout (bits 0..15 = helper code):
	//
	//	bits 16..23 : callA (CALL.A field, 0-255; the standard a slot)
	//	bits 24..32 : nargs (CALL.B - 1; the standard 9-bit b slot)
	//	bits 33..41 : nresults (CALL.C - 1; the standard 9-bit c slot —
	//	              multret rejected by the shape gate)
	//
	// The pc slot in the standard exit-reason packing isn't consumed
	// (the helper doesn't materialise a pc), so it can stay at zero
	// or reuse the standard packing without affecting behavior.
	HelperExecutePlainCall uint64 = 26

	// HelperForPrep is the exit-reason code for the FORPREP slow path
	// (issue #78): the inline FORPREP fast body assumes the three loop
	// slots (init at A, limit at A+1, step at A+2) are numbers; when
	// any slot's IsNumber guard misses, the segment exits here and the
	// dispatcher calls host.ForPrep, which performs the PUC 5.1
	// coercion-then-error semantics ("'for' initial value/limit/step
	// must be a number") and, on success, normalizes all three slots
	// to numbers and pre-decrements the index — so the resumed FORLOOP
	// can keep assuming numbers.
	HelperForPrep uint64 = 27
)

// HelperCodeMask masks off the low 16 bits of exitArg0 that hold the
// helper discriminator; the high 48 bits carry op-specific packed args.
const HelperCodeMask uint64 = 0xFFFF

// JITContext 是 P4 跨边界的执行上下文(05 §3.3)。
//
// **生命周期**:每 State 一份 jitContext(crescent.State 持有)?还是 per-call
// 一份(每次 P4 升层调用临时建)?——PJ1+2 阶段定**per-State 单例**(承
// 05 §3.4 自管栈布局:State 内复用,减少 GC 压力)。具体 State 字段挂载点
// 在 PJ2 wireP4 时同批补。
//
// **Go 堆分配纪律**(承 05 §1.3.4):
//   - jitContext 必须 Go 堆分配(`new(JITContext)`),不放栈上;
//   - Go GC 不会移动 Go 堆对象,但移动栈上对象——栈上分配会让 jitContext 指针
//     在 morestack 时 stale,违反 05 §1.3 「JIT 不持任何 Go 栈指针」纪律;
//   - 经 amd64 r15 传入 JIT 段,段内只 load/store r15+offset 不解引用为 Go
//     指针(写屏障白赚,承 05 §1.4)。
//
// **per-arch 一致性**(承 06 §4.1/§4.2):amd64 r15 = JITContext,arm64 x28 =
// JITContext。Go 端经 unsafe.Pointer 把 *JITContext 转 uintptr 装进 trampoline
// (留 PJ2 实装)。
type JITContext struct {
	// arenaBase 是 arena `[]byte` 起点的 uintptr(承 05 §1.3.3 / §3.3)。
	//
	// JIT 段经 r14 = arenaBase 寻址 GCRef offset → 字节地址。arena 扩容
	// (grow)时本字段会刷(承 05 §5 arena base 重载协议)。
	//
	// **PJ1+2 阶段不接入 trampoline**(简化形态不需要 arena base);PJ2 算术
	// 模板若涉及 NaN-box load/store 时启用。
	arenaBase uintptr

	// valueStackBase 是当前帧 R0 的 uintptr(承 05 §3.3 + 06 §4.1 amd64 rbx)。
	//
	// crescent 调 GibbousCode.Run 时传 base offset(uint32),trampoline 进入
	// 时算 valueStackBase = arenaBase + base*8 装入 rbx。本字段是「每次进入
	// JIT 前算好的栈槽起点」。
	valueStackBase uintptr

	// preemptFlag 是抢占信号(承 05 §1.2.2 + §6.3)。
	//
	// 异步抢占在 Go 下不可用(roadmap §2 runtime 所有权);P4 用回边检查点
	// + preemptFlag 协作终止——回边模板 inline `cmpb [r15+offset], 0` +
	// `jne exit_stub`。值非 0 即触发 OSR exit 退出 JIT 世界。
	//
	// crescent 端调度让出 / GC 触发时把本字段置 1;trampoline 出口时清 0。
	//
	// uint32 + atomic Load(crescent 写,JIT 读)避免数据竞争(`-race` 通过
	// 是 V18 验收口径)。
	preemptFlag atomic.Uint32

	// helperTable 是慢路径 helper 函数表(05 §4.3)。
	//
	// **PJ1 阶段空表**:LOADK/RETURN 直线模板不调 helper。PJ2 算术 + 慢路径
	// 启用时填表(每个慢路径一个 Go 函数指针),JIT 段经 r15+offset 间接
	// CALL。具体 helper 列表与 P3 04-trampoline §3.3 同款映射(arith /
	// gettable / call / safepoint)。
	//
	// 字段类型 [N]uintptr(N = helper 数,PJ2 起填)留 PJ2 同批扩。当前
	// 留空(unused)等 PJ2 启动时改 struct 加字段。

	// exitReasonCode carries the segment's exit reason (05 §3.3). ACTIVE:
	// the issue #50 frame-inline exit-helper-request protocol writes
	// ExitInlineHelper (3) here from the mmap segment; JITContextExitReasonOffset
	// is baked into the amd64/arm64 emitters (compiler.go emits it). Do not
	// remove this field or reorder it ahead of confirming those baked offsets.
	// The ExitOSR (2) value below is reserved but the spec-template deopt path
	// uses a RAX sentinel (specDeoptCode), not this field, to signal a miss.
	exitReasonCode uint32

	// spillBase 是自管机器栈 spill 区起点(05 §3.4)。
	//
	// **PJ1 阶段不切 SP**——本字段 0;PJ2 完整 trampoline 启用切 SP 时填实
	// (每 P4 编译产物分配一段 Go 堆 []byte 作自管栈,spillBase 指向其末尾,
	// trampoline 进入时 MOVQ spillBase, SP)。
	spillBase uintptr

	// spillTop 是自管机器栈 spill 区上界(承 05 §3.4)。
	//
	// PJ1 阶段 0;PJ2 启用切 SP 时 = spillBase + 自管栈大小(典型 64 KiB,
	// 承 implementation-progress §3.1 「自管机器栈大小」开放问题,PJ0/PJ1
	// 实测定)。
	spillTop uintptr

	// ciDepthAddr 是 thread.ciDepth 镜像字的 host 字节地址(承 §9.20
	// Option B Spike 1)。
	//
	// **复用 P3 PW10 Stage 1a 镜像字**(crescent.State.ciDepthRef):
	// crescent 端 wireP4 时把 `&arena.Words()[ciDepthRef].byte` 注入。
	// mmap 段 emit `mov rax, [r15+ciDepthAddr]; inc qword ptr [rax]`
	// 字节级 ciDepth++(enterLuaFrame inline)/ `dec ...`(popCallInfo inline)。
	//
	// **0 = 未接入**(Spike 0 阶段);Spike 1 真接入时 crescent setter 写入。
	ciDepthAddr uintptr

	// ciSegBaseAddr 是 CI 段当前字节基址镜像字的 host 字节地址(承 §9.20
	// Option B Spike 1)。
	//
	// **复用 P3 PW10 Stage 2 镜像字**(crescent.State.ciSegBaseRef):
	// CI 段是可重定位的(grow 后字节基址变),crescent 端 wireP4 时把
	// `&arena.Words()[ciSegBaseRef].byte` 注入。mmap 段 emit `mov rax,
	// [r15+ciSegBaseAddr]; mov rbx, [rax]` 先解引出当前 CI 段基址,然后
	// 算 `rbx + depth*ciSlotSize + word*8` 寻址 CallInfo[depth] 字段。
	ciSegBaseAddr uintptr

	// topAddr 是 thread.top 镜像字的 host 字节地址(承 §9.20 Option B Spike 1)。
	//
	// **复用 P3 PW10 Stage 1a 镜像字**(crescent.State.topRef):
	// thread.top 是栈槽索引(grow 安全坐标),enterLuaFrame 设 callee 帧顶
	// 时写本字(top = base + MaxStack)。mmap 段 emit `mov rax, [r15+topAddr];
	// mov qword ptr [rax], topVal` 字节级 top 设置。
	topAddr uintptr

	// exitArg0 是 mmap 段经 exit-helper-request 协议返 trampoline 时携带的
	// helper request code(承 §9.20.9 trampoline exit-resume 协议详细设计草案
	// + §9.20.6 helper call ABI 协议)。
	//
	// **协议流程**:mmap 段 emit `mov rax, HELPER_RUN_CALLEE; mov [r15+
	// exitArg0Off], rax`,然后 `ret` 出段。trampoline dispatcher 读 jitCtx.
	// exitArg0 决定 helper 路由:HELPER_RUN_CALLEE → executeFrom callee /
	// HELPER_GROW_STACK → arena grow / HELPER_GC_BARRIER → 写屏障(未来)。
	//
	// ACTIVE on amd64/arm64 (archSupportsFrameInline() returns true); only
	// the arch_other fallback leaves it dormant. Wired up by the issue #50
	// frame-inline path (commit chain §9.20.9).
	exitArg0 uint64

	// resumeOff 是 mmap 段内 resume entry 的字节偏移(承 §9.20.9 (2)):
	// BuildVoid0Arg 后 exit-helper-request 段返 trampoline → Go dispatcher
	// 跑 callee 完成 → trampoline 用 `codePageAddr + resumeOff` 求 resume
	// entry 地址 → 再次 CALL 跳进 mmap 段续跑 PopVoid0Arg(popCallInfo)+ ret。
	//
	// **codePage 不重定位**(mmap PROT_RX 段一次性 alloc),resumeOff 编译期
	// 确定即可。compileSpecSelfCall useFrameInline 分支 emit 时记录本字段。
	resumeOff uint32

	_ [4]byte // 8 字节对齐 padding(uint32 resumeOff 后)

	// codePageAddr 是 PROT_RX mmap 段起点的 host 字节地址(承 §9.20.9 (5)
	// resume entry 计算):dispatchInlineHelper 用 `codePageAddr + resumeOff`
	// 求 resume 入口绝对地址,经 Go wrapper 二次 CALL 重入 mmap 段。
	//
	// ACTIVE on amd64/arm64. Injected at wireP4 / installGibbous time.
	codePageAddr uintptr

	// segCallDepth is the native segment-to-segment call nesting depth
	// (issue #50 Spike 5). Zero means "entered from Go" (the top-level
	// nativeCode.Run via CallJITSpec); a caller segment increments it
	// before `call`ing into a callee segment and decrements after the
	// callee returns. The callee's RETURN emit branches on it:
	//
	//   - segCallDepth == 0: RETURN exits the segment (`xor eax,eax;ret`
	//     single-return, or HelperReturn exit-reason multi-return) and
	//     the Go-side Run does host.DoReturn — the historical path.
	//   - segCallDepth > 0: RETURN moves the results into the caller's
	//     target registers in-segment and `ret`s back into the caller
	//     segment, no Go round trip. (The virtual-frame model means the
	//     caller never bumped ciDepth, so there is nothing to unwind —
	//     see emitReturnDualSemantics.)
	//
	// It also bounds native recursion: past a conservative cap the
	// caller segment falls back to the exit-reason path so the Go
	// goroutine stack can't overflow inside the NOSPLIT window where
	// morestack can't fire (see spike DECISION.md option b).
	segCallDepth uint32

	// segCallDeopt is the segment-to-segment deopt flag (issue #50 Spike
	// 5). When a segment running as a seg2seg callee (segCallDepth > 0)
	// hits an operation that would normally exit to a Go helper (arith /
	// compare guard miss, a CALL that can't itself go seg2seg, or any
	// exit-reason op), it sets this to 1 and `ret`s instead of exiting.
	// Each caller fast body checks it after the `call`: if set and still
	// nested (segCallDepth > 0), it propagates by ret'ing; at the top
	// (segCallDepth == 0) it clears the flag and redoes the whole call
	// via the exit-reason host path (host.CallBaseline rebuilds a fresh
	// frame — idempotent because a seg2seg-eligible callee has no
	// observable side effect before the deopt point).
	segCallDeopt uint32

	// savedGoG is a snapshot of the Go G register (amd64 R14 / arm64 X28)
	// at Run entry. PJ10 native emit that calls Go helpers must first do
	// `mov r14, [r15+savedGoGOff]` inside the mmap segment to restore
	// R14=G, otherwise Go's ABIInternal prelude (morestack / stack-guard /
	// getg) reads garbage from R14 and crashes.
	//
	// Written by saveGoG at Run entry (see peroptranslator/save_g_amd64.s).
	// PJ0-PJ9 do not use this field - their mmap segments never call Go
	// functions (PR #26 R14 ABI fix relies on that invariant).
	savedGoG uintptr

	// hostRef holds the peroptranslator P4HostState value's interface
	// header as [2]uintptr (itab + data). The PJ10 native helper shims
	// reconstruct the P4HostState from this pair and then dispatch to
	// the appropriate method.
	//
	// **Not using interface{} field**: the JITContext package does not
	// import peroptranslator, so it can't declare a P4HostState-typed
	// field. Using an opaque [2]uintptr keeps the type dependency
	// one-way; peroptranslator restores the interface via unsafe.
	hostRef [2]uintptr

	// currentClosureRef is the GCRef (arena byte offset) of the Lua
	// closure whose Proto the running segment executes (issue #50 Spike 5
	// inline GETUPVAL). RefreshJitCtxAddrs sets it for the top-level
	// (Go-entered) frame; the segment-to-segment caller sets it to the
	// callee closure before `call [seg]` and restores it after. Inline
	// GETUPVAL reads closure word2+b through this to reach the upvalue
	// object, avoiding a HelperGetUpval exit-reason each recursive call.
	currentClosureRef uintptr

	// threadStackBase0 is the absolute host byte address of the running
	// thread's value-stack slot 0 (arenaBase + stackBaseW*8). Inline
	// GETUPVAL's open-upvalue path reads owner.slot(stackIdx) as
	// [threadStackBase0 + stackIdx*8]. Refreshed every Run entry from the
	// live arenaBase, so it survives arena grow.
	threadStackBase0 uintptr

	// inlineUpvalSafe is 1 when the running State has no coroutines and no
	// suspended resume chain, so every open upvalue is owned by the
	// running thread. The inline GETUPVAL open path is only valid then
	// (owner resolution otherwise needs the Go-side st.uvOwner map). When
	// 0, the open path falls back to HelperGetUpval (or deopts when
	// segCallDepth>0).
	inlineUpvalSafe uint32
	_               uint32

	// valueStackEnd is the absolute host byte address ONE PAST the
	// running thread's value-stack segment (arenaBase + (stackBaseW +
	// stackCap)*8). The seg2seg CALL fast body checks that the callee
	// frame (callee vsBase + CalleeMaxStack*8) fits below this before
	// dispatching in-segment (issue #80): the interpreter's
	// enterLuaFrame grows the stack via ensureStack, but a seg2seg
	// call never re-enters Go — without this bound, deep native
	// recursion silently reads/writes past the stack segment into
	// neighboring arena objects (wrong results, corrupted closures).
	// Refreshed on every Run entry / dispatcher re-entry alongside the
	// other addr fields, so it survives arena grow and stack
	// relocation.
	valueStackEnd uintptr

	// spillBacking holds the Go-heap []byte that backs the self-managed
	// spill stack (issue #89). spillBase / spillTop point into it. Kept as
	// a field so the GC never frees the buffer while the trampoline has SP
	// switched onto it (the trampoline holds only a raw uintptr, which the
	// GC does not treat as a reference). Nil until AllocSpillStack runs.
	spillBacking []byte

	// savedGoSP holds the goroutine stack pointer that the trampoline
	// stashes before switching SP onto the spill stack (issue #89), so it
	// can restore SP on the way out. It lives in the jitCtx (not a
	// register) because the segment CALL clobbers caller-saved registers
	// and the trampoline's own callee-saved slots are on the goroutine
	// stack we are switching away from. Nested trampoline entries (a fresh
	// CallJITSpec from the goroutine stack during exit-reason resume) each
	// overwrite and restore it in LIFO order, which is correct because the
	// outer entry has already restored SP to the goroutine stack before
	// the inner entry runs (see spike/p4spillstack DECISION.md G3).
	savedGoSP uintptr

	// segCallFuel bounds how many seg2seg in-segment CALL dispatches may
	// run between Go-side preemption points. The seg2seg fast body
	// decrements it before each in-segment dispatch; at zero the caller
	// falls back to the exit-reason host path, whose
	// ExecutePlainCallInlineFrame -> enterLuaFrame runs st.preempt()
	// (step budget + cancel context) and the host refills the fuel on
	// the next Run entry / dispatcher resume.
	//
	// Why fuel and not the preemptFlag: the step budget has no async
	// producer — nothing ever sets preemptFlag when a budget is armed;
	// billing happens synchronously in st.preempt() at call/back-edge
	// points. In-segment dispatch bypasses all of those, so a promoted
	// call-tree can run ~phi^depthCap calls with zero billing (fuzz
	// crasher f2165a93dd62892d: fib(5510) under SetStepBudget hung
	// "forever" while the interpreter erred in 50ms). A decrementing
	// fuel counter needs no async signal and costs one dec+jz pair per
	// dispatch.
	//
	// When no budget/context is armed the host refills with
	// SegCallFuelUnlimited, so steady-state workloads keep the full
	// in-segment speed and only budget-armed States pay periodic host
	// round trips.
	segCallFuel uint32

	// segCallFuelRefill records the value of the last SetSegCallFuel so
	// the host can bill (refill - segCallFuel) in-segment dispatches to
	// the step budget on the next refresh. Go-side only; the segment
	// never reads it.
	segCallFuelRefill uint32
}

// NewJITContext 构造 P4 JIT 执行上下文。
//
// 调用方:crescent.State 在 wireP4 后单例建一份(留 PJ2 实装时接入)。
//
// The self-managed spill stack (issue #89) is allocated here so every
// translation product gets one; the trampoline switches SP onto it before
// entering a segment, so deep seg2seg recursion descends on this buffer
// instead of the goroutine stack's NOSPLIT allowance.
func NewJITContext() *JITContext {
	c := &JITContext{}
	c.AllocSpillStack()
	return c
}

// SpillStackSize is the size of the self-managed spill stack (issue #89).
// 64 KiB per the 05 §3.4 design; at 32 B/level (the conservative seg2seg
// per-level cost) that is ~2048 levels of headroom, far above any depth
// cap we set. The buffer holds only register spills / return addresses,
// never Lua values or Go heap pointers, so it is invisible to the GC.
const SpillStackSize = 64 * 1024

// AllocSpillStack allocates the self-managed spill stack and fills
// spillBase / spillTop (issue #89). spillBase is the high-address end
// (the SP entry point for a down-growing stack), 16-byte aligned as the
// amd64/arm64 ABI requires; spillTop is the low-address growth limit. The
// trampoline reads spillBase via [r15+JITContextSpillBaseOffset] to switch
// SP before entering a segment. Idempotent: a second call keeps the
// existing buffer.
func (c *JITContext) AllocSpillStack() {
	if c.spillBacking != nil {
		return
	}
	c.spillBacking = make([]byte, SpillStackSize)
	lo := uintptr(unsafe.Pointer(&c.spillBacking[0]))
	c.spillTop = lo
	c.spillBase = (lo + uintptr(len(c.spillBacking))) &^ 0xf // 16-byte aligned
}

// SpillBase returns the spill-stack SP entry point (testing hook).
func (c *JITContext) SpillBase() uintptr { return c.spillBase }

// SetPreemptFlag 设置抢占标志(crescent 端调,JIT 端 atomic 读)。
//
// 调用方:GC 触发 / 调度让出时(crescent.State 持本 ctx 引用)。
func (c *JITContext) SetPreemptFlag() {
	c.preemptFlag.Store(1)
}

// ClearPreemptFlag 清抢占标志(trampoline 出口调)。
func (c *JITContext) ClearPreemptFlag() {
	c.preemptFlag.Store(0)
}

// PreemptFlagPending 返回抢占标志是否已置(测试钩子,prove-the-path 命中
// 计数器)。
func (c *JITContext) PreemptFlagPending() bool {
	return c.preemptFlag.Load() != 0
}

// SetArenaBase 设置 arena `[]byte` 起点的 uintptr(承 05 §1.3.3 / §3.3
// arena base 重载协议)。crescent 端 wireP4 + Run 入口经 host 接口算出
// 当前 arena.Words() 起点字节地址,经本 setter 注入。
//
// **PJ2 完整接入预备**:本字段配合 valueStackBase 在 PJ2 字节级算术
// codegen 时被 mmap 段经 r15+offset 读取——「movsd xmm0, [r15+arenaBase
// +vsbase+reg*8]」字节级模板。当前 PJ7 简化形态尚不读本字段(mmap 段
// 是 dummy mov+ret,prelude 经 host helper 接口取值)。
func (c *JITContext) SetArenaBase(addr uintptr) {
	c.arenaBase = addr
}

// SetValueStackBase 设置当前帧 R0 的 uintptr(承 05 §3.3 + 06 §4.1)。
//
// 调用契约:Run 入口算 valueStackBase = arena.Words().bytePtr +
// (stackBaseW + ci.base) * 8,装入 mmap 段经 r15+offset 间接寻址 R(idx)。
// 本 setter 在 Run 入口被调,确保每次 P4 帧执行前 valueStackBase 反映
// 当前帧的真实槽位起点。
func (c *JITContext) SetValueStackBase(addr uintptr) {
	c.valueStackBase = addr
}

// ArenaBase 返回 arena base(测试钩子)。
func (c *JITContext) ArenaBase() uintptr { return c.arenaBase }

// ValueStackBase 返回当前帧 R0 字节地址(测试钩子)。
func (c *JITContext) ValueStackBase() uintptr { return c.valueStackBase }

// SetCIDepthAddr 设置 thread.ciDepth 镜像字的 host 字节地址(承 §9.20
// Option B Spike 1)。承 crescent.State.wireP4 注入。
func (c *JITContext) SetCIDepthAddr(addr uintptr) {
	c.ciDepthAddr = addr
}

// CIDepthAddr 返回 thread.ciDepth 镜像字的 host 字节地址(测试钩子)。
func (c *JITContext) CIDepthAddr() uintptr { return c.ciDepthAddr }

// SetCISegBaseAddr 设置 CI 段当前字节基址镜像字的 host 字节地址(承 §9.20)。
func (c *JITContext) SetCISegBaseAddr(addr uintptr) {
	c.ciSegBaseAddr = addr
}

// CISegBaseAddr 返回 CI 段镜像字的 host 字节地址(测试钩子)。
func (c *JITContext) CISegBaseAddr() uintptr { return c.ciSegBaseAddr }

// SetTopAddr 设置 thread.top 镜像字的 host 字节地址(承 §9.20)。
func (c *JITContext) SetTopAddr(addr uintptr) {
	c.topAddr = addr
}

// TopAddr 返回 thread.top 镜像字的 host 字节地址(测试钩子)。
func (c *JITContext) TopAddr() uintptr { return c.topAddr }

// SetAllAddrs writes all five arena-relative address fields at once.
// Called by P4HostState.RefreshJitCtxAddrs so the host implementation
// can compute unsafe.Pointer(&arena.Words()[0]) exactly once and derive
// the four dependent fields from a single base + offset, instead of
// paying the slice-header walk five separate times.
//
// Individual setters (SetArenaBase / SetValueStackBase / SetCIDepthAddr
// / SetCISegBaseAddr / SetTopAddr) remain for callers that legitimately
// need one field.
func (c *JITContext) SetAllAddrs(arenaBase, valueStackBase, ciDepth, ciSegBase, top uintptr) {
	c.arenaBase = arenaBase
	c.valueStackBase = valueStackBase
	c.ciDepthAddr = ciDepth
	c.ciSegBaseAddr = ciSegBase
	c.topAddr = top
}

// SetExitArg0 sets the helper request code (§9.20.9 protocol). ACTIVE on
// amd64/arm64.
func (c *JITContext) SetExitArg0(arg uint64) {
	c.exitArg0 = arg
}

// ExitArg0 返回 helper request code(测试钩子,承 §9.20.9 真接入预备)。
func (c *JITContext) ExitArg0() uint64 { return c.exitArg0 }

// SetResumeOff 设置 mmap 段内 resume entry 字节偏移(承 §9.20.9 (2))。
// compileSpecSelfCall useFrameInline 分支 emit 时记录。
func (c *JITContext) SetResumeOff(off uint32) {
	c.resumeOff = off
}

// ResumeOff 返回 resume entry 字节偏移(测试钩子)。
func (c *JITContext) ResumeOff() uint32 { return c.resumeOff }

// SetCodePageAddr 设置 mmap 段起点(承 §9.20.9 (5) resume entry 计算)。
// installGibbous 时注入 PROT_RX 段起点字节地址。
func (c *JITContext) SetCodePageAddr(addr uintptr) {
	c.codePageAddr = addr
}

// CodePageAddr 返回 mmap 段起点(测试钩子)。
func (c *JITContext) CodePageAddr() uintptr { return c.codePageAddr }

// SavedGoGSlot returns &c.savedGoG for the saveGoG asm helper to write.
// PJ10 native usage: at Run entry `saveGoG(ctx.SavedGoGSlot())` snapshots
// the current R14=G into jitCtx; the mmap segment emits
// `mov r14, [r15+savedGoGOff]` before each Go helper call to restore G.
func (c *JITContext) SavedGoGSlot() *uintptr { return &c.savedGoG }

// SavedGoG returns the mirrored Go G value (test hook).
func (c *JITContext) SavedGoG() uintptr { return c.savedGoG }

// SegCallDepth returns the native segment-to-segment nesting depth
// (issue #50 Spike 5 test hook). Nonzero mid-Run would indicate a
// segment-to-segment call is in flight; a leaked nonzero after Run is
// a teardown bug.
func (c *JITContext) SegCallDepth() uint32 { return c.segCallDepth }

// SegCallDeopt returns the segment-to-segment deopt flag (issue #50
// Spike 5 test hook). A leaked nonzero after Run indicates a deopt
// propagation bug.
func (c *JITContext) SegCallDeopt() uint32 { return c.segCallDeopt }

// SetUpvalInlineFields wires the inline-GETUPVAL ABI (issue #50 Spike 5):
// the running frame's closure GCRef, the running thread's stack-slot-0
// host address, and whether it is safe to inline open-upvalue reads
// (single-thread, no coroutine-owned upvalues). Called from
// RefreshJitCtxAddrs on every Run entry / resume.
func (c *JITContext) SetUpvalInlineFields(closureRef, threadStackBase0 uintptr, safe bool) {
	c.currentClosureRef = closureRef
	c.threadStackBase0 = threadStackBase0
	if safe {
		c.inlineUpvalSafe = 1
	} else {
		c.inlineUpvalSafe = 0
	}
}

// SetValueStackEnd sets the running thread's value-stack end address
// (issue #80; see the valueStackEnd field doc). Refreshed alongside
// SetUpvalInlineFields on every Run entry / dispatcher re-entry.
func (c *JITContext) SetValueStackEnd(end uintptr) { c.valueStackEnd = end }

// SegCallFuelUnlimited is the fuel refill for States with no step budget
// and no cancel context: large enough that the dec+jz guard practically
// never fires (a segment call takes >=ns, so 2^31 dispatches is minutes
// of pure in-segment work — and any host exit refills anyway).
const SegCallFuelUnlimited = 1 << 31

// SegCallFuelBudgeted is the fuel refill while a step budget or cancel
// context is armed: the host regains control (and bills st.preempt())
// at least once per this many in-segment CALL dispatches. 4096 keeps
// the budget error latency low (~us at ns-scale dispatches) while
// amortizing the exit-reason round trip to noise.
const SegCallFuelBudgeted = 4096

// SetSegCallFuel refills the seg2seg dispatch fuel (see the segCallFuel
// field doc). Called by the host on every Run entry / dispatcher resume.
func (c *JITContext) SetSegCallFuel(n uint32) {
	c.segCallFuel = n
	c.segCallFuelRefill = n
}

// SegCallFuel returns the remaining fuel (test hook).
func (c *JITContext) SegCallFuel() uint32 { return c.segCallFuel }

// SegCallFuelSpent returns how many in-segment dispatches ran since the
// last SetSegCallFuel, for step-budget billing on the host side. Returns
// 0 when the last refill was SegCallFuelUnlimited: those dispatches ran
// while no budget was armed, and charging them to a budget armed later
// (between Runs on the same State) would spuriously exhaust it on entry.
func (c *JITContext) SegCallFuelSpent() uint32 {
	if c.segCallFuelRefill != SegCallFuelBudgeted {
		return 0
	}
	return c.segCallFuelRefill - c.segCallFuel
}

// CurrentClosureRef returns the running frame's closure GCRef (test hook
// + used by the segment-to-segment caller emit to bake the restore).
func (c *JITContext) CurrentClosureRef() uintptr { return c.currentClosureRef }

// SetCurrentClosureRef overrides the running frame's closure GCRef (test
// hook; the emit path writes it in-segment for seg2seg callees).
func (c *JITContext) SetCurrentClosureRef(ref uintptr) { c.currentClosureRef = ref }

// SetHostRef stores the opaque host interface header ([2]uintptr:
// itab + data). PJ10 native shims read this to reconstruct the
// P4HostState interface and dispatch to methods.
func (c *JITContext) SetHostRef(h [2]uintptr) { c.hostRef = h }

// HostRef returns the opaque host interface header.
func (c *JITContext) HostRef() [2]uintptr { return c.hostRef }
