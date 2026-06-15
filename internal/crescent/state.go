// Package crescent is the tier-0 interpreter (P1 main loop) — the single
// execution layer of P1 and the deopt landing point for all future tiers
// (roadmap §5 原则 1)。
//
// 设计:docs/design/p1-interpreter/05-interpreter-loop.md。M9 范围内只跑
// 算术 / 循环 / 调用三档;IC、元表、协程、GC 留 M10/M11。
package crescent

import (
	"fmt"
	"sync/atomic"
	"unsafe"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/gc"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// ctxHolder 包裹 context.Context,通过 atomic.Pointer 跨 goroutine 安全
// 替换。剥离接口签名,避免 internal/crescent 直接依赖标准库 context。
type ctxHolder struct {
	err func() error
}

// LuaError carries a Lua-level error value (05 §9.2)。
type LuaError struct {
	Value value.Value
	// HasValue:Value 字段是否携带真实错误值。不能用 Value 的零值判
	// "未设置"——NaN-boxing 下 bits 0 恰是合法数字 +0.0,error(0, 0)
	// 的错误值会被误判替换成 Msg 字符串。
	HasValue  bool
	Msg       string // 缓存给 Go 错误接口
	Traceback string // 错误冒泡到顶层时构建(09;pcall 捕获的错误不带)
	Level     int    // error(msg, level) 的 level(09);0 = 不加位置前缀
	annotated bool   // 已加 chunkname:line: 前缀(只加一次)
}

func (e *LuaError) Error() string {
	if e.Traceback != "" {
		return e.Msg + "\n" + e.Traceback
	}
	return e.Msg
}

// State is the embedding-facing VM state.
//
// M9 范围简化:值栈用 Go slice,后续 M13 切到 arena 上的视图(arena backing
// 注入点;05 §1.3 / 06 §1.1 留口)。
type State struct {
	arena         *arena.Arena
	gc            *gc.Collector
	protos        []*bytecode.Proto // ProtoID → Proto(由 Compile 注入,见 LoadProgram)
	strRefs       [][]arena.GCRef   // protos[id] 内字面量 → 已 intern 的 GCRef(R6 根,详见 11 §1.4)
	globals       arena.GCRef       // _G(globals 表)
	runningThread *thread           // 当前正在执行的 thread(GC ExtraValues 来源)
	hostFns       hostFnRegistry    // host function 注册表(M12)
	stringLib     arena.GCRef       // string 库表(string 值的 per-type __index,07 §1.2)
	cos           coRegistry        // 协程注册表(08;coID = lightuserdata 句柄)

	// uvOwner 记录每个【开放】upvalue 属于哪个 thread 的栈(01 §5.4 的
	// (threadRef, stackIdx) 二元组的 Go 侧形态;值栈 arena 化后改存 threadRef)。
	// 关闭后从此表删除(自持值不再依赖 thread)。
	uvOwner map[arena.GCRef]*thread

	// compileFn 是 loadstring/load 的编译回调(由 wangshu 门面注入,
	// 避免 crescent → frontend 反向依赖)。返回 (mainID, protos, err)。
	compileFn func(src []byte, chunkname string) (uint32, []*bytecode.Proto, error)

	// nCcalls 是 host→Lua 重入深度(真 Go 栈消耗;05 §7.4 LUAI_MAXCCALLS 等价)。
	// callLuaFromHost 进入 +1 / 返回 -1;超 maxCCallDepth 抛 "C stack overflow"。
	nCcalls int

	// threadChain 是 resume 链上被挂起的调用者线程(06 §5.1 R4/R5:
	// runningThread 只覆盖当前线程,链上其余线程的栈也必须是根——
	// freelist 复用内存后漏根即 use-after-free)。Resume 进入时压入、返回时弹出。
	threadChain []*thread

	// loadedCls 是 LoadProgram 返回的主 chunk closure(R8 类常驻根:仅被
	// Go 侧 loaded 缓存持有,不入根会被回收,freelist 下块复用 = 串台执行
	// 另一个 Program)。State 生命周期内不清除(11 §1.3 loaded 不逐出)。
	loadedCls []arena.GCRef

	// stepBudget > 0 时启用指令预算:回边(JMP/FORLOOP 负位移)每跨过
	// stepQuantum 条指令检查一次,超额抛 "instruction budget exceeded"。
	// 宿主侧脚本配额特性的种子;fuzz 用它替代脆弱的源码子串过滤。
	stepBudget int64
	stepUsed   int64

	// ctx 是 SetContext 注入的取消信号(issue #4):preempt 的同一抢占
	// 点(回边 / 函数进帧 / CALL)做 ctx.Err() 检查。Atomic 包裹是因为
	// 跨 goroutine 取消的常见模式:VM 在 goroutine A 跑,timer/ctx 在
	// goroutine B 调 cancel()——context 实现本身已 race-safe,但我们这
	// 边对 ctx 字段的读写跨 goroutine,需 atomic 包裹避免 data race。
	ctx atomic.Pointer[ctxHolder]

	// gcSeen/gcSeenRefs 是根扫描去重 map 的缓存(多线程慢路径用;每轮
	// Collect 复用 clear,避免根扫描自身制造 Go 堆垃圾)。
	gcSeen     map[*thread]bool
	gcSeenRefs map[*thread]bool

	// argsPool 是 callHost 实参缓冲池(LIFO;host→Lua→host 嵌套自然加深)。
	// 池中空闲切片不持活跃 Value(归还即逻辑失效),不入 GC 根。
	argsPool [][]value.Value

	// mainTh 是主线程缓存(State.Call 跨 Run 复用,免每次 newThread)。
	mainTh *thread

	// pinnedRefs 是公共面 Value 持有的 GCRef 句柄表(R8 类常驻根:门面
	// GetGlobal 取出 function/table 后由用户长期持有,不入根则 globals 覆盖
	// 旧值就会被回收 → freelist 复用 → Value 调用即 UAF)。
	// 槽位回收:UnpinRef 把槽置 Null 并推入 freePins,PinRef 优先复用空闲槽
	// (公共面 Value.Release 显式释放走这条路;不 Release 仅累积槽 + GCRef,
	// 小害,见 wangshu.go godoc 提示)。
	pinnedRefs []arena.GCRef
	freePins   []uint32

	// baseline 是 globals 快照(issue #6):MarkGlobalsBaseline 拍下,
	// ResetGlobalsToBaseline 用之恢复。baseline 中的复合 value(table/
	// function GCRef)经 visitExtraValues 入 GC 根防 globals 覆盖后被
	// 回收(同 pin 表对偶面:pin 表管「公共 API 暴露的长持 GCRef」,
	// baseline 管「内部状态恢复需要的长持 GCRef」)。
	baseline []baselineEntry

	// allowFileLoad:loadfile/dofile 是否允许读宿主文件系统。默认 false
	// (嵌入式 VM 接不可信脚本,文件读是越权探测面;10 §12.1 LibsSafe 思路
	// 的最小落地)。宿主经 Options.AllowFileLoad 显式开启。
	allowFileLoad bool

	// bridge: P2 分层桥(`docs/design/p2-bridge/`)。State 私有,挂在
	// 这里让回边 / 入口采样钩点(crescent 主循环 + enterLuaFrame)调到
	// `bridge.OnBackEdge` / `OnEnter`(profileEnabled=true 时;否则编译期
	// 整段消去,零开销)。bridge 包不依赖 crescent(基建非执行层),反
	// 向钩点经此字段以接口形式注入。
	//
	// 生命期:与 State 同生(New 时构造;State 销毁则 Bridge 一同释放,
	// 包括其 profileTable / gibbousCodes 引用)。
	// 多 goroutine 并发不共享:01 §6.3 (B) 方案——profileTable 挂 State
	// 私有;-race 自然通过(00 §4 PB7 验收(d))。
	bridge *bridge.Bridge

	// arenaCleanup: State 销毁时释放 arena backing 持有的外部资源
	// (wangshu_p3 build 下 = 关闭收养的 wazero memory holder + runtime;
	// 默认 build 下 = nil)。由 newStateArena 返回,Close 时调。
	arenaCleanup func()

	// gibStack 是 crescent→gibbous 跨层复用栈缓冲(CallWithStack 零分配路径,
	// 04-trampoline §2.2)。惰性建,单 goroutine 复用。
	gibStack []uint64

	// gibbousPendingErr 是 gibbous helper(DoReturn/h_raise)冒泡的错误暂存
	// (04 §4 status 链):helper 内置 pendingErr,trampoline ERR 时取走。
	gibbousPendingErr *LuaError

	// p3env 持 wangshu_p3 build 下的 wazero Runtime + memadapter holder
	// (newStateArena 建,wireP3 取来构造 gibbous Compiler 共享同一 runtime/
	// memory)。默认 build 恒 nil。类型擦除为 any 避免全 build 依赖 wazero。
	p3env any

	// gcPendingRef 是 gcPending 标志字的 arena GCRef(P3 PW9):collector 在
	// GC 状态转移点把「是否 due」镜像到此字(linear memory),gibbous FORLOOP
	// 回边 inline i32.load 它,只在 due 时才跨层调 h_safepoint。0 = 未分配。
	gcPendingRef arena.GCRef

	// ciTransferRef 是 gibbous→gibbous call_indirect 直调的 base 中转字(PW10 R3)。
	// DoCall 判被调已升 gibbous 时,把被调帧 base 字节偏移写入此字 + 返回 indirect
	// 哨兵;caller wasm 读它作 call_indirect 实参。call_indirect 返回后 DoReturn 已
	// 把刷新后的 caller base 写回此字,caller 读它续算寻址。LIFO 安全(每次写后紧跟
	// 唯一读者,无交错)。0 = 未分配(非 p3 build 也分配,offset 逻辑统一)。
	ciTransferRef arena.GCRef
}

// SetCompileFn 注入编译回调(wangshu.NewState 时装配;loadstring 用)。
func (st *State) SetCompileFn(fn func(src []byte, chunkname string) (uint32, []*bytecode.Proto, error)) {
	st.compileFn = fn
}

// Bridge 暴露 P2 分层桥(`docs/design/p2-bridge/`)给 internal 包内部使用
// (主循环采样钩点 / Compile 期 AnalyzeProto)。
//
// **不暴露给公共 API**——门面层 wangshu.go 通过 SetBridgeP3Compiler /
// SetBridgeLogger 等 setter 注入,不直接拿 *bridge.Bridge,避免公共面对
// internal/bridge 类型形成依赖。
func (st *State) Bridge() *bridge.Bridge { return st.bridge }

// CompileAndLoad 编译一段源码并装载为 closure(loadstring 的核心)。
func (st *State) CompileAndLoad(src []byte, chunkname string) (value.Value, error) {
	if st.compileFn == nil {
		return value.Nil, errf("loadstring: compiler not available")
	}
	mainID, protos, err := st.compileFn(src, chunkname)
	if err != nil {
		return value.Nil, err
	}
	cl := st.LoadProgram(mainID, protos)
	return value.MakeGC(value.TagFunction, cl), nil
}

// SetStringLib 注册 string 库表为 string 值的 per-type 元表 __index
// (`("x"):upper()` 语法支撑,07 §1.2)。
func (st *State) SetStringLib(t arena.GCRef) { st.stringLib = t }

// New constructs a fresh State (arena + collector + empty globals)。
func New() *State {
	a, cleanup, p3env := newStateArena()
	c := gc.New(a, gc.Options{})
	st := &State{arena: a, gc: c, bridge: bridge.NewBridge(), arenaCleanup: cleanup, p3env: p3env}
	st.globals = object.AllocTable(a, 0, 8)
	c.LinkSweep(st.globals)
	// gcPending 标志字(P3 PW9):分配一个 arena 字,collector 镜像 GC due 状态,
	// gibbous FORLOOP 回边 inline 读它(免每迭代无条件跨层 h_safepoint)。
	// 早分配 → 偏移稳定;非 p3 build 也分配(1 字开销可忽略,offset 逻辑统一)。
	st.gcPendingRef = a.AllocWords(1)
	a.SetWordAt(st.gcPendingRef, 0)
	c.SetGCPendingRef(st.gcPendingRef)
	// ci-transfer 中转字(P3 PW10 R3):gibbous→gibbous call_indirect 直调经此字
	// 传被调/刷新后 base 字节偏移(详见字段注释)。早分配 → 偏移稳定。
	st.ciTransferRef = a.AllocWords(1)
	a.SetWordAt(st.ciTransferRef, 0)
	st.installRoots()
	st.wireP3() // wangshu_p3 build:构造 gibbous Compiler 注入 bridge;默认 build no-op
	// host closure 槽位回收(gmatch 迭代器、mountArena 列代理等动态注册的
	// HostFn 在其 closure 被 GC 后释放槽,注册表有界)。
	c.SetHostFnReleaser(st.releaseHostFn)
	// __gc finalizer 调度(06 §10):userdata 死亡复活后调用其 __gc 元方法。
	c.SetFinalizerRunner(func(ud arena.GCRef) {
		meta := object.UserdataMetaRef(st.arena, ud)
		if meta.IsNull() {
			return
		}
		key := value.MakeGC(value.TagString, st.gc.Intern([]byte("__gc")))
		h, _ := st.tableGet(meta, key)
		if value.Tag(h) != value.TagFunction || st.runningThread == nil {
			return
		}
		udv := value.MakeGC(value.TagUserdata, ud)
		// finalizer 出错被吞(5.1:错误不传播,GC 流程继续)
		_, _ = st.callLuaFromHost(st.runningThread, h, []value.Value{udv})
	})
	return st
}

// installRoots 把当前 State 的根集合注入 collector。
//
// 值栈住 Go 切片期间经 ExtraValues 暴露;表数据已住 arena 原生布局,
// 所有 Value 也走 ExtraValues(M11 切到 arena 哈希后撤销)。
func (st *State) installRoots() {
	st.gc.SetRoots(gc.Roots{
		Globals:           st.globals,
		ProgramStringRefs: st.visitProgramStringRefs,
		ExtraValues:       st.visitExtraValues,
		ExtraRefs:         st.visitExtraRefs,
	})
}

// visitProgramStringRefs 暴露 R6:每个 Proto 内字符串字面量的 intern GCRef。
func (st *State) visitProgramStringRefs(visit func(arena.GCRef)) {
	for _, refs := range st.strRefs {
		for _, r := range refs {
			if !r.IsNull() {
				visit(r)
			}
		}
	}
}

// visitExtraValues 暴露所有活线程栈持有的 Value(值栈住 Go 切片期间的旁路根)。
//
// freelist 复用内存后,漏根即 use-after-free:除 runningThread 外,
// resume 链上挂起的调用者线程(threadChain)、全部非 dead 协程的栈、
// 协程主函数(首次 resume 前仅 Go struct 持有)与 xfer 传值区都必须可达。
// globals baseline 的复合值(issue #6 ResetGlobalsToBaseline 的根)也在此扫。
func (st *State) visitExtraValues(visit func(value.Value)) {
	// globals baseline:复合值不接根 → 下次 Reset 时写进 _G 就是已死 GCRef
	for i := range st.baseline {
		visit(st.baseline[i].val)
	}
	// 快路径(绝大多数负载):无协程、无 resume 链 → 直扫 runningThread,
	// 零 map 分配(每轮 Collect 都走根扫描,慢路径的 seen map 是 GC 自伤)。
	if len(st.cos.cos) == 0 && len(st.threadChain) == 0 {
		st.visitThreadValues(st.runningThread, nil, visit)
		return
	}
	if st.gcSeen == nil {
		st.gcSeen = map[*thread]bool{}
	}
	clear(st.gcSeen)
	seen := st.visitThreadValues(st.runningThread, st.gcSeen, visit)
	for _, th := range st.threadChain {
		seen = st.visitThreadValues(th, seen, visit)
	}
	for _, co := range st.cos.cos {
		if co.status == CoDead {
			continue
		}
		seen = st.visitThreadValues(co.th, seen, visit)
		visit(co.fn)
		for _, v := range co.xfer {
			visit(v)
		}
	}
}

// visitThreadValues 扫一个线程的栈/varargs;seen=nil 表示单线程快路径
// (不去重、不分配)。
func (st *State) visitThreadValues(th *thread, seen map[*thread]bool, visit func(value.Value)) map[*thread]bool {
	if th == nil || (seen != nil && seen[th]) {
		return seen
	}
	if seen != nil {
		seen[th] = true
	}
	for i := 0; i < th.top; i++ {
		visit(th.slot(i))
	}
	// top 之上的陈旧残值清 nil(对齐官方 lgc.c traversestack):否则死引用
	// 留在槽里,top 回涨覆盖后下轮 GC 会把它当活根扫——freelist 复用内存下
	// 即 use-after-free(mark 写已释放块 = 腐蚀 freelist 链)。
	for i := th.top; i < th.size(); i++ {
		th.setSlot(i, value.Nil)
	}
	// varargs 住 Go th.ciVarargs(不进 linear memory),不在 stack[:top] 区间,
	// 必须单列为根。索引 = 帧深度,只扫活跃帧 [0,ciDepth)。
	for d := 0; d < th.ciDepth && d < len(th.ciVarargs); d++ {
		for _, v := range th.ciVarargs[d] {
			visit(v)
		}
	}
	return seen
}

// visitExtraRefs 暴露所有活线程上 ci/openUvs 直接以 GCRef 形式持有的对象,
// 以及 LoadProgram 产物 closure(loaded 缓存常驻根)、公共面 Value pin 表。
func (st *State) visitExtraRefs(visit func(arena.GCRef)) {
	for _, cl := range st.loadedCls {
		visit(cl)
	}
	for _, r := range st.pinnedRefs {
		if !r.IsNull() {
			visit(r)
		}
	}
	if len(st.cos.cos) == 0 && len(st.threadChain) == 0 {
		st.visitThreadRefs(st.runningThread, nil, visit)
		return
	}
	if st.gcSeenRefs == nil {
		st.gcSeenRefs = map[*thread]bool{}
	}
	clear(st.gcSeenRefs)
	seen := st.visitThreadRefs(st.runningThread, st.gcSeenRefs, visit)
	for _, th := range st.threadChain {
		seen = st.visitThreadRefs(th, seen, visit)
	}
	for _, co := range st.cos.cos {
		if co.status == CoDead {
			continue
		}
		seen = st.visitThreadRefs(co.th, seen, visit)
	}
}

func (st *State) visitThreadRefs(th *thread, seen map[*thread]bool, visit func(arena.GCRef)) map[*thread]bool {
	if th == nil || (seen != nil && seen[th]) {
		return seen
	}
	if seen != nil {
		seen[th] = true
	}
	// PW10 R2b-3:每帧 closure 根从 arena ci 段读(word3),段是根权威源。
	// cl 在 push 时写段、之后不变,故段对全部活跃帧 [0,ciDepth) 持正确 cl
	// (含当前帧——th.cur 是热镜像,但 cl 不在热路径改)。形态 Y 现算寻址。
	for depth := 0; depth < th.ciDepth; depth++ {
		if cl := th.ciSegCl(depth); !cl.IsNull() {
			visit(cl)
		}
	}
	for _, uv := range th.openUvs {
		if !uv.IsNull() {
			visit(uv)
		}
	}
	return seen
}

// Arena exposes the underlying arena (for tests / embedding APIs)。
func (st *State) Arena() *arena.Arena { return st.arena }

// Globals returns the GCRef of the globals table.
func (st *State) Globals() arena.GCRef { return st.globals }

// InternForEmbed exposes the collector's string intern path for the embedding
// API (11 §1.3 字符串常量惰性 intern;Value 桥接需要)。
func (st *State) InternForEmbed(b []byte) arena.GCRef {
	return st.gc.Intern(b)
}

// SetGCStressMode 开关高频 GC 压力模式(12 §5 GC 透明性 fuzz 用)。
func (st *State) SetGCStressMode(on bool) { st.gc.SetStressMode(on) }

// SetForceAllPromote 开关强制全升模式(P3 PW9 层间差分测试入口,08 §2.2)。
//
// 转发到 Bridge.SetForceAllPromote:置位后所有可编译 Proto 首次执行即升 gibbous
// (绕过热度阈值,**不绕可编译性闸门**),消除「哪些 Proto 够热」的时序不确定性,
// 使 crescent vs gibbous 层间差分可复现 + 覆盖最大化。bridge 为 nil(P1-only build)
// 时 no-op。**testing-only**——经门面层 testing 入口暴露,非支持的运行模式。
func (st *State) SetForceAllPromote(on bool) {
	if st.bridge != nil {
		st.bridge.SetForceAllPromote(on)
	}
}

// SetStepBudget 设置回边指令预算(<=0 关闭)。超额时脚本以
// "instruction budget exceeded" 可恢复错误终止——宿主侧脚本配额特性,
// fuzz 用它替代脆弱的源码子串过滤兜住无限/超长循环。
func (st *State) SetStepBudget(n int64) {
	st.stepBudget = n
	st.stepUsed = 0
}

// SetAllowFileLoad 开关 loadfile/dofile 的文件系统读能力(默认关)。
func (st *State) SetAllowFileLoad(on bool) { st.allowFileLoad = on }

// AllowFileLoad 查询文件读开关(stdlib loadfile/dofile 用)。
func (st *State) AllowFileLoad() bool { return st.allowFileLoad }

// preempt 是 VM 抢占点入口(回边 / 函数进帧 / TFORLOOP 各调一次)。
// 对齐 Go runtime preemption 语义:抢占点是「在指令边界检查是否要让
// 出执行权」。本实现的让出条件:
//   - stepBudget > 0 且 stepUsed 超额 → "instruction budget exceeded"
//   - ctx 注入且 ctx.Err() 非空 → "context canceled: <err>"
//
// 调用方契约:必须在指令边界(opcode/帧间)调用,不能在 opcode 中段。
// 三处抢占点(execute.go JMP 回边 / execute.go TFORLOOP 续跑 / frame.go
// enterLuaFrame 函数进帧)共用同一入口——P3+ JIT 生成代码里同款抢占点
// 也走这条逻辑(可直接复制或 inline 为机器码)。
//
// 性能:无 budget、无 ctx 的快路径是「两个字段判 0/nil → return nil」,
// 已被 inline;启用任一时多一次 atomic.Load 或 int 比较,性能轮基准
// 未观测可见影响。
//
// 历史:v0.1.2 前命名为 chargeStep(只算 budget),issue #4 上 SetContext
// 时并入 ctx 检查,审计后改名 preempt 反映「抢占点」而非「计费」语义。
func (st *State) preempt() *LuaError {
	if st.stepBudget > 0 {
		st.stepUsed++
		if st.stepUsed > st.stepBudget {
			return errf("instruction budget exceeded")
		}
	}
	if h := st.ctx.Load(); h != nil {
		if err := h.err(); err != nil {
			return errf("context canceled: %s", err.Error())
		}
	}
	return nil
}

// SetCancelHook 注入一个取消回调(issue #4 公共 SetContext 的内部桥)。
// fn 返回非 nil error 时,VM 在下一个抢占点中止当前 Call/Run。原子
// 替换,跨 goroutine 安全;传 nil 等价 RemoveCancelHook。
func (st *State) SetCancelHook(fn func() error) {
	if fn == nil {
		st.ctx.Store(nil)
		return
	}
	st.ctx.Store(&ctxHolder{err: fn})
}

// CancelHookActive 报告是否当前安装了取消回调(诊断/测试用)。
func (st *State) CancelHookActive() bool { return st.ctx.Load() != nil }

// GCCollect 触发一次 full GC(collectgarbage("collect") 用)。
func (st *State) GCCollect() { st.gc.Collect() }

// GCSetStopped 暂停/恢复自动 GC(collectgarbage("stop"/"restart"))。
func (st *State) GCSetStopped(on bool) { st.gc.SetStopped(on) }

// getArgsBuf / putArgsBuf:callHost 实参缓冲池。
func (st *State) getArgsBuf(n int) []value.Value {
	if last := len(st.argsPool) - 1; last >= 0 {
		buf := st.argsPool[last]
		st.argsPool = st.argsPool[:last]
		if cap(buf) >= n {
			return buf[:n]
		}
	}
	if n < 8 {
		return make([]value.Value, n, 8)
	}
	return make([]value.Value, n)
}

func (st *State) putArgsBuf(buf []value.Value) {
	// 清引用防池中切片延长 arena 对象的 Go 侧可见性(非 GC 根,纯卫生)。
	// trace 构建下填毒值:HostFn 违约保留 args 时读到毒值立即显形,
	// 而非"被后续调用静默覆写"的远端症状。
	poison := value.Nil
	if traceExec {
		poison = value.NumberValue(-6.66e66)
	}
	for i := range buf {
		buf[i] = poison
	}
	st.argsPool = append(st.argsPool, buf)
}

// GCCountKB 返回 arena 当前活跃 KB 数(collectgarbage("count") / gcinfo):
// bump - freelist 空闲字节,逼近官方 totalbytes 语义(GC 回收后回落,
// 官方测试套 gc.lua 的 "run until gc" 循环依赖该回落)。仍属可观察不可
// 逐字节比项(10 §13:freelist 粒度/附属块口径与官方分配器不同)。
func (st *State) GCCountKB() float64 {
	used := uint64(st.arena.Bump()) - st.arena.FreeBytes()
	return float64(used) / 1024.0
}

// NewError 构造一个带消息的 LuaError(供 stdlib 等 host 函数使用)。
func NewError(msg string) *LuaError {
	return &LuaError{Msg: msg}
}

// NewErrorVal 构造一个携带 Lua Value 的错误(对应 error(v) 内建)。
func NewErrorVal(v value.Value, msg string) *LuaError {
	return &LuaError{Value: v, HasValue: true, Msg: msg, Level: 1}
}

// MarkAnnotated 阻止位置前缀注解(error(v, 0) / 非字符串错误值)。
func (e *LuaError) MarkAnnotated() { e.annotated = true }

// TypeNameOf 暴露内部 typeName 给 stdlib 实现 type() 内建。
func TypeNameOf(v value.Value) string { return typeName(v) }

// NewLibTable 给 stdlib 提供一个新表(挂 stdlib 命名空间用)。
func (st *State) NewLibTable(approxFields uint32) arena.GCRef {
	hsz := uint32(8)
	for hsz < approxFields {
		hsz *= 2
	}
	t := st.allocTable(0, hsz)
	return t
}

// SetTableField 给 stdlib 提供"以字符串键写入表字段"的便捷接口。
func (st *State) SetTableField(t arena.GCRef, name string, v value.Value) {
	ref := st.gc.Intern([]byte(name))
	key := value.MakeGC(value.TagString, ref)
	_ = st.tableSet(t, key, v)
}

// LoadProgram registers the compiled Protos and lazy-interns their string
// literals (Proto §字面量惰性 intern;06 §5.1 R6 改写)。返回 mainID 对应的
// closure GCRef(0 upvalue;主 chunk)。
//
// Program 不可变、可跨 State 共享(11 §1.4):这里对每个 Proto 做 State 私有
// 浅拷贝——共享只读的 Code/StringLits/LineInfo,私有化 Consts(intern 进本
// State arena)、IC(运行期可写)与 Protos(相对下标 → 本 State 绝对 ProtoID)。
func (st *State) LoadProgram(mainID uint32, protos []*bytecode.Proto) arena.GCRef {
	base := uint32(len(st.protos))
	for _, p := range protos {
		cp := *p // 浅拷贝:Code/StringLits/LineInfo/UpvalDescs/LocVars 共享只读底层数组
		// 私有 Protos:相对下标修正为绝对 ProtoID
		cp.Protos = make([]uint32, len(p.Protos))
		for i, id := range p.Protos {
			cp.Protos[i] = base + id
		}
		// 私有 Consts:字符串字面量 intern 进本 State arena
		cp.Consts = make([]value.Value, len(p.Consts))
		copy(cp.Consts, p.Consts)
		refs := make([]arena.GCRef, len(p.Consts))
		for i := range cp.Consts {
			if p.IsStringConst(i) {
				lit := p.StringLits[p.StringLitIdx[i]]
				refs[i] = st.gc.Intern([]byte(lit))
				cp.Consts[i] = value.MakeGC(value.TagString, refs[i])
			}
		}
		// 私有 IC(运行期可写;不能跨 State 共享)
		cp.IC = make([]bytecode.ICSlot, len(p.Code))
		st.protos = append(st.protos, &cp)
		st.strRefs = append(st.strRefs, refs)
	}
	cl := st.allocLuaClosure(base+mainID, 0)
	st.loadedCls = append(st.loadedCls, cl)
	return cl
}

// Call executes a Lua closure with the given args, returning all results.
//
// args 是按值传入的实参;返回值数受被调函数控制(显式 RETURN 给出多少返回多少)。
// nresults < 0 表示"全部返回";否则按个数裁剪/补 nil。
func (st *State) Call(cl arena.GCRef, args []value.Value, nresults int) ([]value.Value, error) {
	rets, err := st.callOnStack(cl, args, nresults)
	if err != nil {
		return nil, err
	}
	// 拷出独立 slice:旧契约下返回值在下次 Call 后仍可读(调用方可长持)。
	return append([]value.Value(nil), rets...), nil
}

// callOnStack 执行闭包,返回值直接是主 thread 栈上的活动切片(零拷贝)。
//
// ⚠️ 切片底层是复用的 th.stack:下次 Call/Run 复位 top 后会被覆写。调用方
// 必须在下次进入 VM 前消费完(读出标量 / 拷贝 / 经 pin 表登记复合值)。
// runningThread 复位为 nil 后 mainTh 仍是 loadedCls 同级常驻根 → 返回值在
// GC 下保持可达(栈未缩容,槽位值仍被 mainTh.stack 引用)。
func (st *State) callOnStack(cl arena.GCRef, args []value.Value, nresults int) ([]value.Value, error) {
	if object.IsHostClosure(st.arena, cl) {
		// 从 Go 端直接 Call host closure 需要临时栈帧脚手架,本期未做;
		// 已 Register 的 host fn 由 Lua 内调用闭环工作(callHost 路径)。
		return nil, fmt.Errorf("Call: host closure cannot be called from Go end; invoke it from Lua side instead")
	}
	// 复用主 thread(规则引擎形状 = 长驻 State 高频 Run 短脚本;每次
	// newThread 的栈切片/扩容是无谓的 Go 堆 churn)。State 单 goroutine,
	// 主 thread 不会重入(host→Lua 重入走同一 th 的 execute 叠层,协程
	// 各有独立 th)。复位:top 清零 + 帧深度回退到 0(truncateCI),openUvs
	// 沿用(上次 Run 末 closeUpvals 已清空)。
	th := st.mainTh
	if th == nil {
		th = st.newThread()
		st.mainTh = th
	} else {
		th.top = 0
		th.truncateCI(0)
		th.pendingResume = nil
		// 上次 Run 经错误退出时 unwind 不走 closeUpvals,openUvs 可能残留
		// 指向已失效栈位的开放 uv——关闭它们(自持快照值),清 uvOwner。
		if len(th.openUvs) > 0 {
			st.closeUpvals(th, 0)
		}
	}
	st.runningThread = th
	defer func() { st.runningThread = nil }()
	// 推 callee + args 到栈底
	th.push(value.MakeGC(value.TagFunction, cl))
	for _, v := range args {
		th.push(v)
	}
	// 进入主 frame
	if err := st.enterLuaFrame(th, 0 /*funcIdx in stack*/, len(args), -1 /*caller wants all*/, true /*entry*/); err != nil {
		return nil, err
	}
	if err := st.execute(th); err != nil {
		if err.Traceback == "" {
			err.Traceback = st.buildTraceback(th)
		}
		return nil, err
	}
	// 顶层执行结束后返回值在栈底起若干个(由 RETURN 落点 dst=funcIdx 决定)。
	// 零拷贝:直接切 th.stack 活动区(契约见 callOnStack 文档)。
	rets := th.activeSlice(th.top)
	if nresults >= 0 {
		if len(rets) > nresults {
			rets = rets[:nresults]
		} else {
			for len(rets) < nresults {
				th.push(value.Nil)
			}
			rets = th.activeSlice(nresults)
		}
	}
	return rets, nil
}

// CallOnStack 是 callOnStack 的导出形,供门面层零分配 CallInto 使用。
// 返回值是主 thread 栈上的活动切片(零拷贝,下次进入 VM 前有效),契约
// 见 callOnStack 文档。
func (st *State) CallOnStack(cl arena.GCRef, args []value.Value, nresults int) ([]value.Value, error) {
	return st.callOnStack(cl, args, nresults)
}

// thread 是执行线程:值栈住 arena 段(VS0-c 形态 Y),CallInfo 仍住 Go 切片。
//
// **值栈 arena 化(VS0-c)**:stack 不再是 Go slice,而是 arena 内一段
// Value[stackCap](01 §5.6 valueStackRef)。寄存器 R(i) = arena 段第 i 槽
// (stackBaseW 字偏移 + i)。物理上与 gibbous wasm 的 i64.load offset=8*i
// 读写同一块 linear memory(P3 build 下)——这是端到端共见的基础。
//
// 形态 Y:slot/setSlot 每次经 arena.Words() 当前视图寻址(不缓存派生切片),
// 免疫 arena grow(grow 时 arena.words 字段由 setBacking 更新,下次 Words()
// 取到新视图)。段满时 growStack 在 arena 内重分配更大段 + 拷旧 + Free 旧;
// 开放 upvalue 经 owner.slot(idx) 寻址,stackBaseW 更新后自动指向新段同位置
// (无需额外重定位)。
type thread struct {
	arena      *arena.Arena           // 值栈段所在 arena(段寻址 + grow 用)
	stackBaseW uint32                 // 值栈段在 arena 的字偏移(= valueStackRef>>3)
	stackCap   int                    // 段容量(槽数;= size())
	top        int                    // 当前栈顶(超过 ci.top 的临时区)
	openUvs    map[uint32]arena.GCRef // stackIdx → open Upvalue ref(M9 简化,M10 改降序链)

	// --- CallInfo 状态(PW10 R2b-4:退役 Go []callInfo,arena 段为权威)---
	//
	// cur 是当前栈顶帧的工作副本(热镜像)。currentCI 返回 &cur——**地址稳定**,
	// 故热循环持 ci 指针永不悬垂(消除旧 &th.cis[len-1] 的 append 重定位雷区,
	// design-claims-vs-codebase-physics §2 构造性消解)。push/pop 同步 cur ↔ 段:
	// push 先把 cur 刷回段[ciDepth-1](保 caller pc/base/top)再载入 callee;
	// pop 弹 cur 后从段重载 caller。
	cur     callInfo
	ciDepth int // 逻辑帧深度(= 旧 len(th.cis));段[0..ciDepth-1] 是活跃帧
	ciBaseW uint32
	ciCap   int
	// ciVarargs 每帧 varargs(GC 根)。varargs 是 Go []value.Value,不进 linear
	// memory(VS0-e 子piece);与帧深度对齐(索引 = depth)。
	ciVarargs [][]value.Value

	// maxOpenIdx 是 openUvs 中最大的 stackIdx(官方降序链表头的等价物):
	// closeUpvals(level) 在 level > maxOpenIdx 时 O(1) 返回——RETURN 每帧
	// 都调 closeUpvals,无此快路径时每次都全量迭代 map(曾占 20% CPU)。
	// 不变式:openUvs 非空 ⇒ maxOpenIdx = max(keys);空 ⇒ 值无意义。
	maxOpenIdx uint32

	// pendingResume 在 yield 冒泡出 execute 时记录恢复信息(08 §3.3 saveFrame
	// 的 P1 形态);resume 时由 executeResume 消费。
	pendingResume *pendingResumeInfo
}

// initialStackSlots 是 thread 值栈段的初始槽数(对齐旧 Go slice cap 64)。
const initialStackSlots = 64

// ciWords 是每个 CallInfo 在 arena ci 段占的字数(4 word/帧)。
//
// **物理布局(承 04-trampoline §1.2 word2 packing)**:
//
//	word0 [31:0]base   [63:32]funcIdx
//	word1 [31:0]top    [63:32]pc(savedPC)
//	word2 [31:0]protoID [47:32]nresults [48]tailcall [49]fresh [50]gibbous
//	word3 cl(GCRef)
//
// ci 段是冷字段 + GC 根的权威源(R2b-3 起);varargs 住 Go th.ciVarargs(不进
// linear memory)。当前栈顶帧的工作副本在 th.cur(热镜像,currentCI 返回 &cur)。
const ciWords = 4

// initialCISlots 是 ci 段初始帧数(典型程序调用深度 ≪ 此值;深则 growCISeg)。
const initialCISlots = 64

// newThread 建一个值栈住 arena 段的 thread(VS0-c)。主线程与协程统一经此
// 入口,故主线程栈与协程栈一并 arena 化。
func (st *State) newThread() *thread {
	th := &thread{arena: st.arena}
	ref := st.arena.AllocWords(initialStackSlots)
	th.stackBaseW = uint32(ref) >> 3
	th.stackCap = initialStackSlots
	// CallInfo arena 段(PW10 R2b):每帧 ciWords 字,初始 initialCISlots 帧。
	ciRef := st.arena.AllocWords(initialCISlots * ciWords)
	th.ciBaseW = uint32(ciRef) >> 3
	th.ciCap = initialCISlots
	th.ciVarargs = make([][]value.Value, 0, initialCISlots)
	return th
}

// --- CallInfo arena 段写入(PW10 R2b-1:cold 字段只写镜像)---
//
// writeCISeg 把一个 callInfo 的字段打包进 ci 段第 depth 帧(4 word,布局见
// thread.ciBaseW)。R2b-1 在 enterLuaFrame 压帧后 + 任何 cold 字段变更后调,
// 使段与 Go cis 镜像同步;R2b-2 起 cold accessor 改读此段。
//
// 形态 Y:经 SetWordAt 现算地址(读 arena.words 当前值),不缓存派生切片,
// 免疫 grow(growCISeg / 任何 alloc 触发的物理 grow)。
func (th *thread) writeCISeg(depth int, ci *callInfo) {
	a := th.arena
	wordRef := func(w int) arena.GCRef {
		return arena.GCRef(th.ciBaseW+uint32(depth*ciWords+w)) << 3
	}
	a.SetWordAt(wordRef(0), uint64(uint32(ci.base))|uint64(uint32(ci.funcIdx))<<32)
	a.SetWordAt(wordRef(1), uint64(uint32(ci.top))|uint64(uint32(ci.pc))<<32)
	a.SetWordAt(wordRef(2), packCIWord2(ci))
	a.SetWordAt(wordRef(3), uint64(ci.cl))
}

// packCIWord2 打包 protoID/nresults/flags 进 word2(04-trampoline §1.2 布局)。
// nresults 是 int(-1 表可变),取低 16 位存(C-1 语义 ≤ 0xFFFF;-1 → 0xFFFF)。
func packCIWord2(ci *callInfo) uint64 {
	w := uint64(ci.protoID)
	w |= uint64(uint16(ci.nresults)) << 32
	if ci.tailcall {
		w |= 1 << 48
	}
	if ci.fresh {
		w |= 1 << 49
	}
	if ci.gibbous {
		w |= 1 << 50
	}
	return w
}

// readCISegInto 从 ci 段第 depth 帧解包到 out(R2b-1 往返自检 + R2b-2 起 accessor 用)。
func (th *thread) readCISegInto(depth int, out *callInfo) {
	a := th.arena
	wordRef := func(w int) arena.GCRef {
		return arena.GCRef(th.ciBaseW+uint32(depth*ciWords+w)) << 3
	}
	w0 := a.WordAt(wordRef(0))
	w1 := a.WordAt(wordRef(1))
	w2 := a.WordAt(wordRef(2))
	out.base = int(int32(uint32(w0)))
	out.funcIdx = int(int32(uint32(w0 >> 32)))
	out.top = int(int32(uint32(w1)))
	out.pc = int32(uint32(w1 >> 32))
	out.protoID = uint32(w2)
	out.nresults = int(int16(uint16(w2 >> 32))) // 符号扩展(-1 → 0xFFFF → -1)
	out.tailcall = w2&(1<<48) != 0
	out.fresh = w2&(1<<49) != 0
	out.gibbous = w2&(1<<50) != 0
	out.cl = arena.GCRef(a.WordAt(wordRef(3)))
}

// setVarargs 记录第 depth 帧的 varargs 到 Go 影子(GC 根;索引 = 深度)。
func (th *thread) setVarargs(depth int, va []value.Value) {
	for len(th.ciVarargs) <= depth {
		th.ciVarargs = append(th.ciVarargs, nil)
	}
	th.ciVarargs[depth] = va
}

// clearVarargs 弹帧时清第 depth 帧 varargs 引用(防 GC 误扫已死帧 + 防泄漏)。
func (th *thread) clearVarargs(depth int) {
	if depth >= 0 && depth < len(th.ciVarargs) {
		th.ciVarargs[depth] = nil
	}
}

// varargsAt 取第 depth 帧的 varargs(pop 后恢复 caller th.cur.varargs 用)。
func (th *thread) varargsAt(depth int) []value.Value {
	if depth >= 0 && depth < len(th.ciVarargs) {
		return th.ciVarargs[depth]
	}
	return nil
}

// ciAt 读第 depth 帧的 callInfo 值副本(非当前帧只读访问:traceback / 协程恢复)。
// 当前栈顶帧(depth==ciDepth-1)直接返回热镜像 th.cur(它可能 pc/top 比段新);
// 其余帧从段解包 + 补 varargs。**返回值副本**——调用方不得缓存指针跨分配
// (form-Y:每次按 depth 现读,消除 *callInfo 悬垂)。
func (th *thread) ciAt(depth int) callInfo {
	if depth == th.ciDepth-1 {
		return th.cur
	}
	var ci callInfo
	th.readCISegInto(depth, &ci)
	ci.varargs = th.varargsAt(depth)
	return ci
}

// truncateCI 把帧深度回退到 newDepth(pcall/元方法/yield 边界清理,替代旧
// th.cis = th.cis[:newDepth])。回退后从段重载新栈顶帧到 th.cur。R2b-4。
func (th *thread) truncateCI(newDepth int) {
	for d := newDepth; d < th.ciDepth; d++ {
		th.clearVarargs(d)
	}
	th.ciDepth = newDepth
	if newDepth > 0 {
		th.readCISegInto(newDepth-1, &th.cur)
		th.cur.varargs = th.varargsAt(newDepth - 1)
	}
}

// reMirrorTop 把当前栈顶帧热镜像(th.cur)刷回 ci 段(cold 字段经 currentCI 改
// th.cur 后调,如 SetTailcall/SetGibbous,R2b-4)。
func (th *thread) reMirrorTop() {
	if th.ciDepth > 0 {
		th.writeCISeg(th.ciDepth-1, &th.cur)
	}
}

// growCISeg 在 ci 段容量不足 need 帧时重分配更大段(仿 growStack,PW10 R2b-2)。
//
// 形态 Y:经 WordAt/SetWordAt 现算地址拷旧帧(读 arena.words 当前值,免缓存
// 派生切片,免疫 AllocWords 触发的物理 grow)。ci 段只经 ciBaseW + depth 现算
// 寻址(无跨 grow 存活的派生指针),重定位后下次 writeCISeg/readCISegInto 自动
// 指向新段;栈顶帧热镜像在 th.cur(地址稳定,不受段重定位影响)。
func (th *thread) growCISeg(need int) {
	newCap := need + 8
	if d := th.ciCap * 2; d > newCap {
		newCap = d
	}
	a := th.arena
	newRef := a.AllocWords(uint32(newCap * ciWords))
	oldRef := arena.GCRef(th.ciBaseW) << 3
	// 拷已有活跃帧 [0,ciDepth)(已镜像);新帧由调用方随后 writeCISeg。
	copyFrames := th.ciDepth
	if copyFrames > th.ciCap {
		copyFrames = th.ciCap
	}
	for w := 0; w < copyFrames*ciWords; w++ {
		a.SetWordAt(newRef+arena.GCRef(w*8), a.WordAt(oldRef+arena.GCRef(w*8)))
	}
	oldCap := th.ciCap
	th.ciBaseW = uint32(newRef) >> 3
	th.ciCap = newCap
	if !oldRef.IsNull() {
		a.Free(oldRef, uint32(oldCap*ciWords)*8)
	}
}

// ciSegCl 从 ci 段第 depth 帧读 cl(word3,GCRef)。R2b-3 GC 根扫描经此从 arena
// 段读每帧 closure 根(而非 Go cis[depth].cl)——证明段是正确的 GC 根源(漏根=UAF)。
// 形态 Y:WordAt 现算地址免缓存。
func (th *thread) ciSegCl(depth int) arena.GCRef {
	ref := arena.GCRef(th.ciBaseW+uint32(depth*ciWords+3)) << 3
	return arena.GCRef(th.arena.WordAt(ref))
}

// (PW10 R2b-1 安全网,仅 ciMirrorCheck=wangshu_trace 构建启用)。打包/解包
// bug 在此立即 panic 显形,而非 R2b-2 翻转读段后症状离根因很远。
func (th *thread) verifyCISeg(depth int, want *callInfo) {
	var got callInfo
	th.readCISegInto(depth, &got)
	if got.base != want.base || got.funcIdx != want.funcIdx || got.top != want.top ||
		got.protoID != want.protoID || got.cl != want.cl || got.nresults != want.nresults ||
		got.tailcall != want.tailcall || got.fresh != want.fresh || got.gibbous != want.gibbous ||
		got.pc != want.pc {
		panic(fmt.Sprintf("crescent: ci 段镜像不一致 depth=%d\n got  %+v\n want %+v", depth, got, *want))
	}
}

// --- 值栈访问收口(VS0 形态 Y)---
//
// slot/setSlot 是值栈槽的唯一标量读写收口;size/copyOut/copyIn/activeSlice
// 覆盖容量查询与批量搬移。形态 Y:每次经 arena.Words() 当前视图偏移寻址,
// 不缓存派生切片(免疫 arena grow),opcode 与调用约定代码经 VS0-a 收口后不动。

// slot 读绝对栈位 i 的值(arena 段第 stackBaseW+i 字)。
func (th *thread) slot(i int) value.Value {
	return value.Value(th.arena.Words()[th.stackBaseW+uint32(i)])
}

// setSlot 写绝对栈位 i。
func (th *thread) setSlot(i int, v value.Value) {
	th.arena.Words()[th.stackBaseW+uint32(i)] = uint64(v)
}

// size 返回值栈段容量(= stackCap;容量边界判断用)。
func (th *thread) size() int { return th.stackCap }

// copyOut 把 [lo,hi) 槽拷进调用方 Go slice dst(返回值搬移)。
func (th *thread) copyOut(dst []value.Value, lo, hi int) {
	w := th.arena.Words()
	for i := lo; i < hi; i++ {
		dst[i-lo] = value.Value(w[th.stackBaseW+uint32(i)])
	}
}

// copyIn 把 src 拷进 [lo,...) 槽(resume 写值)。
func (th *thread) copyIn(lo int, src []value.Value) {
	w := th.arena.Words()
	for i, v := range src {
		w[th.stackBaseW+uint32(lo+i)] = uint64(v)
	}
}

// activeSlice 返回 [0,hi) 的零拷贝活动切片(callOnStack 返回值;契约见
// callOnStack 文档:下次进 VM 前消费完——grow 只在进 VM 时发生,故此切片
// 在消费窗口内有效)。形态 Y 下经 unsafe 别名 arena 段(Value 底层 = uint64)。
func (th *thread) activeSlice(hi int) []value.Value {
	if hi == 0 {
		return nil
	}
	w := th.arena.Words()
	return unsafe.Slice((*value.Value)(unsafe.Pointer(&w[th.stackBaseW])), hi)
}

// push 压一个值到栈顶(段满则 growStack)。
func (th *thread) push(v value.Value) {
	if th.top >= th.stackCap {
		th.growStack(th.top + 1)
	}
	th.setSlot(th.top, v)
	th.top++
}

// ensureStack 确保段容量 ≥ n(段满则 growStack)。
func (th *thread) ensureStack(n int) {
	if n > th.stackCap {
		th.growStack(n)
	}
}

// growStack 在 arena 内重分配更大段、拷旧槽、改 stackBaseW、Free 旧段
// (lua_realloc stack 风格)。
//
// **grow 视图陷阱**:AllocWords 可能触发 arena 物理 grow64 使旧视图失效;
// 拷贝经 WordAt/SetWordAt 现算地址(读 arena.words 当前值,grow 后偏移不变),
// 不缓存任何派生切片(形态 Y 免疫的兑现)。
//
// **upvalue 自动重定位**:开放 upvalue 经 owner.slot(idx) 寻址,stackBaseW
// 更新后自动指向新段同位置,无需改 openUvs 的 stackIdx 键。
func (th *thread) growStack(need int) {
	newCap := need + 8
	if d := th.stackCap * 2; d > newCap {
		newCap = d
	}
	a := th.arena
	newRef := a.AllocWords(uint32(newCap))
	oldRef := arena.GCRef(th.stackBaseW) << 3
	for i := 0; i < th.top; i++ {
		a.SetWordAt(newRef+arena.GCRef(i*8), a.WordAt(oldRef+arena.GCRef(i*8)))
	}
	oldCap := th.stackCap
	th.stackBaseW = uint32(newRef) >> 3
	th.stackCap = newCap
	if !oldRef.IsNull() {
		a.Free(oldRef, uint32(oldCap)*8)
	}
}

// callInfo 持久化每个活跃 Lua 调用的状态(05 §1.2)。M9 简化字段。
//
// pc 字段在 M9 是"当前正在执行的指令位置"(主循环直接读写它,不像设计文档的
// savedPC 是"返回时恢复的 pc")。M11 协程接入时把 pc/top 落回 ci 与 saveFrame
// 抽象拉齐(05 §1.3 reloadFrame/saveFrame 对称约定)。
//
// **proto 经 protoID 引用(VS0-b)**:Go 指针 *bytecode.Proto 不能进 linear
// memory(03-memory-model §5 Go 堆侧资产划界);改存 protoID(uint32),用时
// st.protos[id] 查。这与 P3 trampoline 的 ci.protoID 接口拉齐(04 §2.2)。
//
// **gibbous 标识位**(p2-bridge/04 §4.4 word2 bit50 callStatus_gibbous):
// gibbous 帧入口 trampoline 压新 CallInfo 时置 1,标识此帧走 Wasm 路径
// (04 §1.2)。P1 解释器主循环不读它(对它透明,04 §1.3);trampoline 在
// 跨层调度/错误冒泡时读它判流向。形态 b 简化版(bool,与 tailcall/fresh
// 同款;word 位打包延后到 VS0-e)。
type callInfo struct {
	base     int         // R0 在 stack 的绝对索引
	funcIdx  int         // 被调 closure 槽(funcIdx = base-1)
	top      int         // 本帧逻辑顶
	protoID  uint32      // 当前 Proto 的 ID(st.protos[protoID];VS0-b 替换 *Proto)
	cl       arena.GCRef // 当前 closure
	nresults int         // 调用者期望的返回数;-1 = 可变
	tailcall bool
	fresh    bool // execute 重入边界
	gibbous  bool // 本帧在 gibbous(Wasm)编译码中执行(04 §1.2;P1 恒 false)

	// vararg 区(M13 接入):IsVararg 函数的多余实参(数量 nVarargs)拷贝到一个独立
	// Go 切片(简化版,后续 M14 切到栈下区)。这样 VARARG 指令直接读 ci.varargs。
	varargs []value.Value
	pc      int32
}

// protoOf 取 callInfo 的 Proto(VS0-b:protoID → *Proto 收口)。
func (st *State) protoOf(ci *callInfo) *bytecode.Proto { return st.protos[ci.protoID] }

// --- CallInfo 字段访问收口(R2a,PW10 VS0-e 前置)---
//
// 所有 callInfo 字段读写经以下 accessor,使 R2b 把 ci 物理迁入 linear memory
// 时只改方法体 + struct,不动 ~171 处调用点(同 VS0-a 值栈寻址收口的纪律)。
//
// **热/冷分野(R2b 物理布局依据)**:Base/SetBase 与 Pc/SetPc 是热寄存器(每
// 指令经 reg/setReg/主循环读写),R2b 拟保留为当前帧的 Go 镜像、仅在 push/pop/
// 层边界与 arena ci 段同步;其余字段(cl/nresults/funcIdx/protoID/top/flags/
// varargs)是冷字段(仅调用边界触),R2b 直接 arena 段读写。本轮 accessor 全部
// 是直通(返回/写 Go 字段),零行为变更。
func (ci *callInfo) Base() int              { return ci.base }
func (ci *callInfo) FuncIdx() int           { return ci.funcIdx }
func (ci *callInfo) Top() int               { return ci.top }
func (ci *callInfo) SetTop(v int)           { ci.top = v }
func (ci *callInfo) ProtoID() uint32        { return ci.protoID }
func (ci *callInfo) Cl() arena.GCRef        { return ci.cl }
func (ci *callInfo) NResults() int          { return ci.nresults }
func (ci *callInfo) Tailcall() bool         { return ci.tailcall }
func (ci *callInfo) SetTailcall(v bool)     { ci.tailcall = v }
func (ci *callInfo) Fresh() bool            { return ci.fresh }
func (ci *callInfo) Gibbous() bool          { return ci.gibbous }
func (ci *callInfo) SetGibbous(v bool)      { ci.gibbous = v }
func (ci *callInfo) Pc() int32              { return ci.pc }
func (ci *callInfo) SetPc(v int32)          { ci.pc = v }
func (ci *callInfo) Varargs() []value.Value { return ci.varargs }
