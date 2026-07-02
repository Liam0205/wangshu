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
)

// **§9.20.9 协议状态码常量** (Spike 1 真接入 + future helper request 路由):

// JIT exit reason codes(承 §9.20.9 (3) 协议状态码 + exitReasonCode 字段):
const (
	ExitNormal       uint32 = 0 // 正常 RET 出段
	ExitError        uint32 = 1 // ERR 冒泡(state.pendingErr 已置)
	ExitOSR          uint32 = 2 // 投机失败 OSR exit
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

	// exitReasonCode 是 OSR exit 原因(05 §3.3)。
	//
	// PJ5+ 实装:guard 失败时 JIT 段写本字段标 OSR 类别(IsNumber 失败 / 同表
	// 同代次失败 / 等),trampoline 出口读本字段决定再训练协议。
	//
	// PJ1+2 阶段恒 0(无 OSR exit 路径)。
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
	// **当前 Spike 1 阶段 archSupportsFrameInline=false 屏蔽真触发**,本字段
	// 为 future Spike 1 真接入 commit-1 准备(承 §9.20.9 实装顺序 5 commits)。
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
	// **当前 Spike 1 阶段 archSupportsFrameInline=false 屏蔽真触发**,本字段
	// wireP4 / installGibbous 时注入。
	codePageAddr uintptr

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
}

// NewJITContext 构造 P4 JIT 执行上下文。
//
// 调用方:crescent.State 在 wireP4 后单例建一份(留 PJ2 实装时接入)。
//
// **PJ1+2 阶段**:本函数返回的 JITContext 字段全 0——尚不接入 trampoline。
// PJ2 完整接入时改为 wireP4 同批传 arena base + 分配自管栈 + 填 spillBase/Top。
func NewJITContext() *JITContext {
	return &JITContext{}
}

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

// SetExitArg0 设置 helper request code(承 §9.20.9 协议:Spike 1 真接入 +
// future helper request 路由)。当前 archSupportsFrameInline=false 屏蔽真触发。
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

// SetHostRef stores the opaque host interface header ([2]uintptr:
// itab + data). PJ10 native shims read this to reconstruct the
// P4HostState interface and dispatch to methods.
func (c *JITContext) SetHostRef(h [2]uintptr) { c.hostRef = h }

// HostRef returns the opaque host interface header.
func (c *JITContext) HostRef() [2]uintptr { return c.hostRef }
