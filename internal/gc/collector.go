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
// State 的 Alloc helper 在每次 arena.AllocBytes 之后调用本函数累加。本函数不直接触发 GC;
// 触发判定见 MaybeCollect。
func (c *Collector) AllocCharge(nbytes uint32) {
	c.bytesAllocSince += uint64(nbytes)
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
func (c *Collector) SetStopped(on bool) { c.stopped = on }

// SetStressMode 开关高频 GC 压力模式(06 §11 / 12 §5):每个 safepoint 都
// 强制 full Collect。GC 透明性 fuzz 用——压力模式下输出必须与正常模式
// byte-equal,否则就是漏根/早回收。
func (c *Collector) SetStressMode(on bool) { c.stressMode = on }

// Collect 执行一次 STW full GC(06 §8.2 主流程)。
func (c *Collector) Collect() {
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
	// pacing:本轮存活字节由 sweep 时累加在 c.liveBytesAfterSweep。
	c.threshold = c.liveBytesAfterSweep * uint64(c.gcPauseRatio) / 100
	if c.threshold < uint64(c.a.Cap())/16 {
		c.threshold = uint64(c.a.Cap()) / 16 // 下限,防极小堆下 threshold 退化成 0
	}
	c.bytesAllocSince = 0
	// 翻转 currentWhite。
	c.currentWhite ^= 1
}

// liveBytesAfterSweep 字段定义见结构体(由 sweep() 在每轮末赋值,Collect() 读取)。
