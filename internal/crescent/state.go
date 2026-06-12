// Package crescent is the tier-0 interpreter (P1 main loop) — the single
// execution layer of P1 and the deopt landing point for all future tiers
// (roadmap §5 原则 1)。
//
// 设计:docs/design/p1-interpreter/05-interpreter-loop.md。M9 范围内只跑
// 算术 / 循环 / 调用三档;IC、元表、协程、GC 留 M10/M11。
package crescent

import (
	"fmt"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/gc"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// LuaError carries a Lua-level error value (05 §9.2)。
type LuaError struct {
	Value     value.Value
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
}

// SetStringLib 注册 string 库表为 string 值的 per-type 元表 __index
// (`("x"):upper()` 语法支撑,07 §1.2)。
func (st *State) SetStringLib(t arena.GCRef) { st.stringLib = t }

// New constructs a fresh State (arena + collector + empty globals)。
func New() *State {
	a := arena.New(arena.Options{})
	c := gc.New(a, gc.Options{})
	st := &State{arena: a, gc: c}
	st.globals = object.AllocTable(a, 0, 8)
	c.LinkSweep(st.globals)
	st.installRoots()
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

// visitExtraValues 暴露 thread 栈持有的 Value(值栈住 Go 切片期间的旁路根)。
//
// 表数据已住 arena 原生布局,经 Globals/栈上的表引用从 markRef 正常扫到,
// 不再需要旁路根。
func (st *State) visitExtraValues(visit func(value.Value)) {
	if st.runningThread != nil {
		th := st.runningThread
		for i := 0; i < th.top; i++ {
			visit(th.stack[i])
		}
	}
}

// visitExtraRefs 暴露 thread 上 ci/openUvs 直接以 GCRef 形式持有的对象。
func (st *State) visitExtraRefs(visit func(arena.GCRef)) {
	if st.runningThread != nil {
		th := st.runningThread
		for _, ci := range th.cis {
			if !ci.cl.IsNull() {
				visit(ci.cl)
			}
		}
		for _, uv := range th.openUvs {
			if !uv.IsNull() {
				visit(uv)
			}
		}
	}
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

// NewError 构造一个带消息的 LuaError(供 stdlib 等 host 函数使用)。
func NewError(msg string) *LuaError {
	return &LuaError{Msg: msg}
}

// NewErrorVal 构造一个携带 Lua Value 的错误(对应 error(v) 内建)。
func NewErrorVal(v value.Value, msg string) *LuaError {
	return &LuaError{Value: v, Msg: msg, Level: 1}
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
	return cl
}

// Call executes a Lua closure with the given args, returning all results.
//
// args 是按值传入的实参;返回值数受被调函数控制(显式 RETURN 给出多少返回多少)。
// nresults < 0 表示"全部返回";否则按个数裁剪/补 nil。
func (st *State) Call(cl arena.GCRef, args []value.Value, nresults int) ([]value.Value, error) {
	if object.IsHostClosure(st.arena, cl) {
		return nil, fmt.Errorf("Call: host closure not yet supported (M12)")
	}
	th := newThread()
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
	// 顶层执行结束后返回值在栈底起若干个(由 RETURN 落点 dst=funcIdx 决定)
	rets := append([]value.Value(nil), th.stack[:th.top]...)
	if nresults >= 0 {
		if len(rets) > nresults {
			rets = rets[:nresults]
		} else {
			for len(rets) < nresults {
				rets = append(rets, value.Nil)
			}
		}
	}
	return rets, nil
}

// thread 是 M9 简化版的执行线程:值栈与 CallInfo 都住 Go 切片。
//
// 后续 M13 把 stack/cis 切到 arena 上(走 newBacking 注入点)即可保留接口形状。
type thread struct {
	stack   []value.Value
	top     int // 当前栈顶(超过 ci.top 的临时区)
	cis     []callInfo
	openUvs map[uint32]arena.GCRef // stackIdx → open Upvalue ref(M9 简化,M10 改降序链)

	// pendingResume 在 yield 冒泡出 execute 时记录恢复信息(08 §3.3 saveFrame
	// 的 P1 形态);resume 时由 executeResume 消费。
	pendingResume *pendingResumeInfo
}

func newThread() *thread {
	return &thread{
		stack: make([]value.Value, 0, 64),
	}
}

func (th *thread) push(v value.Value) {
	if cap(th.stack) <= th.top {
		ns := make([]value.Value, th.top, max(cap(th.stack)*2, th.top+8))
		copy(ns, th.stack)
		th.stack = ns
	}
	th.stack = th.stack[:th.top+1]
	th.stack[th.top] = v
	th.top++
}

func (th *thread) ensureStack(n int) {
	if cap(th.stack) >= n {
		if len(th.stack) < n {
			th.stack = th.stack[:n]
		}
		return
	}
	ns := make([]value.Value, n, max(cap(th.stack)*2, n+8))
	copy(ns, th.stack)
	th.stack = ns
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// callInfo 持久化每个活跃 Lua 调用的状态(05 §1.2)。M9 简化字段。
//
// pc 字段在 M9 是"当前正在执行的指令位置"(主循环直接读写它,不像设计文档的
// savedPC 是"返回时恢复的 pc")。M11 协程接入时把 pc/top 落回 ci 与 saveFrame
// 抽象拉齐(05 §1.3 reloadFrame/saveFrame 对称约定)。
type callInfo struct {
	base     int             // R0 在 stack 的绝对索引
	funcIdx  int             // 被调 closure 槽(funcIdx = base-1)
	top      int             // 本帧逻辑顶
	proto    *bytecode.Proto // 当前 Proto
	cl       arena.GCRef     // 当前 closure
	nresults int             // 调用者期望的返回数;-1 = 可变
	tailcall bool
	fresh    bool // execute 重入边界

	// vararg 区(M13 接入):IsVararg 函数的多余实参(数量 nVarargs)拷贝到一个独立
	// Go 切片(简化版,后续 M14 切到栈下区)。这样 VARARG 指令直接读 ci.varargs。
	varargs []value.Value
	pc      int32
}
