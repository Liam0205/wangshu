// Package gc implements the stop-the-world mark-sweep collector, shadow stack,
// safepoint discipline, string intern, and write-barrier interface for the arena
// GC objects defined in package object. See docs/design/p1-interpreter/06-memory-gc.md.
//
// 范围(P1):
//   - STW full GC,无写屏障(写屏障接口占位,P3+ 增量 GC 才填 — 06 §9.4)。
//   - 双白翻转保留(06 §4.3:与未来增量 GC 同构)。
//   - 显式 gray stack(06 §5.3)做迭代式标记。
//   - shadow stack:host 显式 push/defer pop;Lua 执行现场用「栈即根」零登记(06 §6)。
//   - string intern(JSHash + 弱可达索引,06 §9.3):rawequal 字符串退化为 GCRef 比较。
//   - 弱表 cleartable(06 §8.4 / 07 §13)stub:语义在 07 落地后接入。
package gc

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// Color 编码与 object 包共享(为避免循环依赖,这里复用 object.ColorXXX 常量)。

// Collector 是 mark-sweep 收集器(单实例,挂在 State 上)。
type Collector struct {
	a *arena.Arena

	// 所有头对象的 sweep 全链(linkSweep 在 Alloc 路径调用)。
	sweepHead arena.GCRef

	// 双白:每轮 GC 末翻转(06 §4.3)。
	currentWhite uint8 // 0 或 1;新分配对象的白色

	// mark 工作集(显式 gray stack;06 §5.3)。
	gray []arena.GCRef

	// shadow stack(host 显式登记;06 §6)。
	shadow []value.Value

	// 根集合(R1..R9;06 §5.1):由 State 注入。
	roots Roots

	// pacing(06 §8.3)。
	threshold           uint64 // 下次 GC 的分配字节阈值
	gcPauseRatio        int    // GCPAUSE,默认 200(2.0x)
	bytesAllocSince     uint64 // 距上次 GC 的累计分配量(包含已被 sweep 回收的)
	liveBytesAfterSweep uint64 // 本轮 sweep 末的存活字节(由 sweep 累加)
	stressMode          bool   // 高频压力模式:每个 safepoint 强制 Collect(12 §5)
	stopped             bool   // collectgarbage("stop"):自动 GC 暂停(显式 Collect 不受影响)
	collecting          bool   // Collect 期间为 true(host-trigger AllocCharge 防递归,issue #9 方向 1)
	hostTrigger         bool   // host alloc 跨阈触发 collect(issue #9 方向 1,opt-in;现有 stdlib/intern 路径有 mid-construction transient GCRef 未 pin,默认 false 不破坏)

	// gcPending 标志(P3 PW9):反映「MaybeCollect 是否会真正 Collect」,镜像到
	// arena 一个固定字(linear memory),供 gibbous FORLOOP 回边 inline i32.load
	// 检查——只在 GC 真正 due 时才跨层调 h_safepoint(否则热循环每迭代无条件跨层
	// 吞掉消灭 dispatch 的收益,05 §3 / 08 §5.1.2)。**保守正确**:flag 为真覆盖
	// 「stressMode 或 bytesAllocSince≥threshold」,GC 该触发时 flag 必为 1(漏置 1
	// 才危险,多置 1 只是多一次无害跨层)。gcPendingRef=0 时全 no-op(非 p3 build)。
	gcPendingRef  arena.GCRef // 标志字在 arena 的 GCRef(字节偏移);0 = 未装(gibbous 不读)
	gcPendingLast bool        // 上次写入标志字的值(transition-only 写,免每 alloc 重复 store)

	// 弱表登记(06 §8.4 / 07 §13)。
	weakList []arena.GCRef

	// finalizer 队列(06 §10)。
	finalizeList    []arena.GCRef // 已登记 __gc 的 userdata(创建序)
	hasFinalizer    map[arena.GCRef]bool
	toRunFinalizers []arena.GCRef     // 本轮待运行的 __gc(创建逆序遍历时反向)
	runFinalizer    func(arena.GCRef) // __gc 调度回调(State 注入,M11+)

	// host closure 回收回调(State 注入):sweep 回收 host closure 时通知
	// 注册表释放槽位引用(hostFn 槽复用,防长驻 State 注册表无界增长)。
	releaseHostFn func(hostFnID uint32)

	// string intern 表(06 §9.1)。
	strBuckets [][]arena.GCRef
	strMask    uint32
	strCount   uint32
}

// Roots is the runtime root set, supplied by the State / VM at GC time (06 §5.1).
//
// 字段名按 R 编号(R5 = running thread 经其 valueStack/CallInfo 自动覆盖,无需显式列出栈
// 槽——只要登记 RunningThread,mark 阶段顺着 Thread 头扫到栈即可)。
type Roots struct {
	Globals           arena.GCRef                   // R1
	Registry          arena.GCRef                   // R2
	MainThread        arena.GCRef                   // R3
	RunningThread     arena.GCRef                   // R4 / R5(顺其 valueStack/CallInfo 扫到全部活跃寄存器)
	Threads           []arena.GCRef                 // R4:其它活跃 Thread(协程链)
	ProgramStringRefs func(visit func(arena.GCRef)) // R6:State 中所有 programStringRefs 的字符串 GCRef(承 01 §5.7 / 06 §5.1 R6 改写)
	TypeMetatables    [9]arena.GCRef                // R9:per-type 元表(07 §1.2)

	// ExtraValues 暴露 Go 侧持有的活跃 Value(M9/M10 thread 值栈住 Go 切片时使用;
	// M13 切到 arena 视图后改由 RunningThread 扫栈接管)。任何 Go 堆上 transient
	// Value(table get/set 中 callInfo 暂存值、CONCAT 半成品等)也走这条根。
	ExtraValues func(visit func(value.Value))

	// ExtraRefs 暴露 Go 侧持有的活跃 GCRef(用于 thread 上的 open upvalue 等
	// 直接以 GCRef 形式持有的对象)。
	ExtraRefs func(visit func(arena.GCRef))

	// R7 shadow stack 由 Collector 自己持有;R8 临时根落在 R5/R7,无需独立字段。
}

// Options configures the collector.
type Options struct {
	GCPause int // 默认 200;0 = 用默认。06 §8.3 LUAI_GCPAUSE。
}

// New constructs a Collector around the given arena.
func New(a *arena.Arena, opts Options) *Collector {
	pause := opts.GCPause
	if pause == 0 {
		pause = 200
	}
	c := &Collector{
		a:            a,
		gcPauseRatio: pause,
		threshold:    uint64(a.Cap()) / 4, // 首次:容量 1/4 时即触发(防极早 GC 又防过晚)
		hasFinalizer: make(map[arena.GCRef]bool),
		strBuckets:   make([][]arena.GCRef, 16), // 起步 16 桶,装填超 1 时 rehash
		strMask:      15,
	}
	return c
}

// SetRoots installs / refreshes the root set provider (called by State during init
// and whenever RunningThread changes).
func (c *Collector) SetRoots(r Roots) { c.roots = r }

// SetFinalizerRunner 注入 __gc 调度回调(06 §10;State 在 init 时注入)。
func (c *Collector) SetFinalizerRunner(fn func(arena.GCRef)) { c.runFinalizer = fn }

// SetHostFnReleaser 注入 host closure 槽位释放回调(State 在 init 时注入)。
func (c *Collector) SetHostFnReleaser(fn func(uint32)) { c.releaseHostFn = fn }

// RegisterFinalizer 登记一个带 __gc 的 userdata(setmetatable 含 __gc 时调用)。
func (c *Collector) RegisterFinalizer(ud arena.GCRef) {
	if c.hasFinalizer[ud] {
		return
	}
	c.hasFinalizer[ud] = true
	c.finalizeList = append(c.finalizeList, ud)
}

// LinkSweep 把新分配的对象挂入 sweep 全链头部(06 §2.1)。
//
// 必须在「写完 GCHeader 之后」立刻调用——这是 collector 看到新对象的唯一渠道。
// allocator 路径:object.allocateRaw(M3) 之后由 caller(State 的 Alloc helper)调用 LinkSweep。
//
// 颜色语义:**新对象置为 deadWhite**(下轮回收候选色,与 Lua 5.1 `luaC_link` 一致)。
// 第一轮 mark 把可达的染黑,sweep 把仍是 deadWhite 的(= 不可达)回收。
// 这避免「LinkSweep 置 currentWhite ⇒ 第一轮 deadWhite 不匹配 ⇒ 一轮也不回收」的退化。
func (c *Collector) LinkSweep(ref arena.GCRef) {
	h := object.HeaderOf(c.a, ref)
	h = object.SetColor(h, c.deadWhite())
	h = object.SetGCNext(h, c.sweepHead)
	object.SetHeader(c.a, ref, h)
	c.sweepHead = ref
}

// AllocCharge 通知 collector 一次分配的字节数(供 pacing 使用)。
//
// State 的 Alloc helper 在每次 arena.AllocBytes 之后调用本函数累加。
//
// **issue #9 方向 1**:跨 threshold 时直接触发 collect。这避免 boundary-dominated
// 工作负载(host 反复 NewTable + 短脚本,VM opcode safepoint 不被频繁穿过)下
// 「accounting 上涨但 sweep 触发不到」的 starvation。
//
// **半构造对象安全**:host 公共 API 在调 AllocCharge 之前:① 已 pin 的对象(NewTable
// 返回的 Table 经 PinRef 立即登记)在 GC 根可达;② 中间分配的 transient GCRef(如
// rehash 内 newArr/newNode)虽未挂 sweep chain,但 sweep 只走 chain → 不被回收。
// 故 host 路径的「中段触发 collect」对所有合法路径都是安全的。
//
// **不重入保护**:collect 内部触发任何 AllocCharge(如 finalizer 调宿主代码,
// 进而调宿主公共 API)由 collecting 守卫拦截,避免递归 collect。
func (c *Collector) AllocCharge(nbytes uint32) {
	c.bytesAllocSince += uint64(nbytes)
	c.updateGCPending()
	if c.hostTrigger && !c.stopped && !c.collecting && c.bytesAllocSince >= c.threshold {
		c.Collect()
	}
}

// SetHostTriggeredCollect 切换 host alloc 跨阈直接触发 collect(issue #9 方向 1)。
//
// **opt-in 契约**:开启后,任何 AllocCharge 调用都可能在 bytesAllocSince 跨阈时
// 立即 Collect。调用方(host 公共 API / stdlib 等)必须保证:** all transient
// GCRef are reachable from a GC root**(pin 表 / shadow stack push / 已挂 sweep
// chain 且 mark-able)。否则 mid-construction 的 GCRef 会被误回收 = UAF。
//
// **wangshu 公共 API 安全性**(以 wangshu.NewState 开启为目标):
//   - NewTable/NewArrayTable 返回值经 PinRef 立即登记 GC 根 ✓
//   - rehash 中 transient newArr/newNode 未 LinkSweep → sweep 不回收 ✓
//   - SetGlobal 路径 globals 是 R5 根 ✓
//
// **不安全**(默认 false,故现 stdlib/intern 通过):
//   - intern 中段(b []byte 还在 Go 栈,新 strRef 未挂表)
//   - 元方法回调持 transient Lua-level Value
//   - 公共面 fromInnerWithPin 之前的 transient
//
// 故 SetHostTriggeredCollect(true) 仅在「调用者保证全程 pin」时安全开启。
// 推荐:host 嵌入层每周期手动 st.Collect() / st.MaybeCollectNow() 作 cadence
// 控制(issue #9 方向 2,已经过 #60 提供)。
func (c *Collector) SetHostTriggeredCollect(on bool) { c.hostTrigger = on }

// SetGCPendingRef 装入 gcPending 标志字的 arena GCRef(P3 PW9,wangshu_p3 build
// 由 State 在 init 时分配一个 arena 字并传入)。装入后 collector 在状态转移点
// (越阈值 / Collect / stressMode 切换)把 flag 镜像到该字,gibbous inline 读它。
func (c *Collector) SetGCPendingRef(ref arena.GCRef) {
	c.gcPendingRef = ref
	c.gcPendingLast = false
	c.updateGCPending()
}

// gcPendingNow 当前是否「MaybeCollect 会真正 Collect」(stopped 时恒 false)。
func (c *Collector) gcPendingNow() bool {
	if c.stopped {
		return false
	}
	return c.stressMode || c.bytesAllocSince >= c.threshold
}

// updateGCPending 把 gcPendingNow() 镜像到 arena 标志字——仅在值变化时写
// (transition-only,免每次 AllocCharge 都 store)。gcPendingRef=0(非 p3 build)
// 时 no-op。
func (c *Collector) updateGCPending() {
	if c.gcPendingRef == 0 {
		return
	}
	now := c.gcPendingNow()
	if now == c.gcPendingLast {
		return
	}
	c.gcPendingLast = now
	v := uint64(0)
	if now {
		v = 1
	}
	c.a.SetWordAt(c.gcPendingRef, v)
}

// MaybeCollect 在分配点检查阈值,必要时启动一次 STW full GC(06 §7.1)。
//
// 调用方契约:调用前所有活跃 Value 必须从根可达(在 Lua 栈上,或已 push 进 shadow stack);
// 否则它们会被误回收。这是 06 §6.3 host function 纪律的实现侧。
func (c *Collector) MaybeCollect() {
	if c.stopped {
		return
	}
	if c.stressMode || c.bytesAllocSince >= c.threshold {
		c.Collect()
	}
}

// SetStopped 暂停/恢复自动 GC(collectgarbage("stop"/"restart");官方语义:
// stop 后只有显式 collectgarbage 触发回收,分配不再自动 GC)。
//
// updateGCPending:与 SetStressMode / Collect 同——stopped 是 gcPendingNow 的
// 输入(stopped 时恒返 false),状态转移后须同步标志字,否则 restart 后
// (停期已累积 bytesAllocSince≥threshold)标志滞留 0 → gibbous 回边漏跨层
// (虽下次 AllocCharge 自愈,但对称处理消除滞后窗口)。
func (c *Collector) SetStopped(on bool) { c.stopped = on; c.updateGCPending() }

// SetStressMode 开关高频 GC 压力模式(06 §11 / 12 §5):每个 safepoint 都
// 强制 full Collect。GC 透明性 fuzz 用——压力模式下输出必须与正常模式
// byte-equal,否则就是漏根/早回收。
func (c *Collector) SetStressMode(on bool) { c.stressMode = on; c.updateGCPending() }

// Collect 执行一次 STW full GC(06 §8.2 主流程)。
func (c *Collector) Collect() {
	if c.collecting {
		return // 防递归(host-trigger AllocCharge 在 Collect 内 finalizer/sweep alloc 时)
	}
	c.collecting = true
	defer func() { c.collecting = false }()
	c.markRoots()
	c.markAll()
	c.separateFinalizers()
	c.clearWeakTables()
	c.sweep()
	// 运行 finalizer(06 §10):separateFinalizers 已把本轮死白的 userdata
	// 复活并搬入 toRunFinalizers;此处经回调逐个调度(回调由 State 注入,
	// 调用 __gc 元方法)。创建逆序执行(5.1 语义)。
	if len(c.toRunFinalizers) > 0 && c.runFinalizer != nil {
		for i := len(c.toRunFinalizers) - 1; i >= 0; i-- {
			c.runFinalizer(c.toRunFinalizers[i])
		}
	}
	c.toRunFinalizers = c.toRunFinalizers[:0]
	// issue #11 方向 1:sweep 后尝试缩 backing slab 放回 Go 堆。
	// Compact 内部判定 cap 可缩(cap > bump + 余量)才动;P3 InPlaceBacking 模式
	// 与紧 cap 稳态下都 no-op O(1)。典型受益:transient peak 触发 grow doubling
	// 后,Release 让 bump-area 大头空闲 → Compact 缩 cap 到 bump 量级,Go runtime
	// 回收旧大 slab(latched high-water 解除,pineapple#105 类长寿命 pool fat
	// state 现象缓解)。
	c.a.Compact()
	// pacing:本轮存活字节由 sweep 时累加在 c.liveBytesAfterSweep。
	c.threshold = c.liveBytesAfterSweep * uint64(c.gcPauseRatio) / 100
	if c.threshold < uint64(c.a.Cap())/16 {
		c.threshold = uint64(c.a.Cap()) / 16 // 下限,防极小堆下 threshold 退化成 0
	}
	c.bytesAllocSince = 0
	// 翻转 currentWhite。
	c.currentWhite ^= 1
	// Collect 后 bytesAllocSince 归零 → gcPending 落 0(除非 stressMode 恒置 1)。
	c.updateGCPending()
}

// liveBytesAfterSweep 字段定义见结构体(由 sweep() 在每轮末赋值,Collect() 读取)。
