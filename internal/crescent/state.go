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
	Value value.Value
	Msg   string // 缓存给 Go 错误接口
}

func (e *LuaError) Error() string { return e.Msg }

// State is the embedding-facing VM state.
//
// M9 范围简化:值栈用 Go slice,后续 M13 切到 arena 上的视图(arena backing
// 注入点;05 §1.3 / 06 §1.1 留口)。
type State struct {
	arena      *arena.Arena
	gc         *gc.Collector
	protos     []*bytecode.Proto          // ProtoID → Proto(由 Compile 注入,见 LoadProgram)
	strRefs    [][]arena.GCRef            // protos[id] 内字面量 → 已 intern 的 GCRef(R6 根,详见 11 §1.4)
	globals    arena.GCRef                // _G(globals 表)
	tableSides map[arena.GCRef]*tableSide // M9 旁路存储(M10 替换为原生哈希)
}

// New constructs a fresh State (arena + collector + empty globals)。
func New() *State {
	a := arena.New(arena.Options{})
	c := gc.New(a, gc.Options{})
	st := &State{arena: a, gc: c}
	st.globals = object.AllocTable(a, 0, 8)
	return st
}

// Arena exposes the underlying arena (for tests / embedding APIs)。
func (st *State) Arena() *arena.Arena { return st.arena }

// Globals returns the GCRef of the globals table.
func (st *State) Globals() arena.GCRef { return st.globals }

// LoadProgram registers the compiled Protos and lazy-interns their string
// literals (Proto §字面量惰性 intern;06 §5.1 R6 改写)。返回 mainID 对应的
// closure GCRef(0 upvalue;主 chunk)。
func (st *State) LoadProgram(mainID uint32, protos []*bytecode.Proto) arena.GCRef {
	base := uint32(len(st.protos))
	for _, p := range protos {
		// 把 Proto.Protos 的相对下标修正为绝对 ProtoID。
		fixed := make([]uint32, len(p.Protos))
		for i, id := range p.Protos {
			fixed[i] = base + id
		}
		p.Protos = fixed
		st.protos = append(st.protos, p)
		// 惰性 intern 字符串字面量(R6)
		refs := make([]arena.GCRef, len(p.Consts))
		for i := range p.Consts {
			if p.IsStringConst(i) {
				lit := p.StringLits[p.StringLitIdx[i]]
				refs[i] = st.gc.Intern([]byte(lit))
				p.Consts[i] = value.MakeGC(value.TagString, refs[i])
			}
		}
		st.strRefs = append(st.strRefs, refs)
	}
	cl := object.AllocLuaClosure(st.arena, base+mainID, 0)
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
	pc       int32
}
