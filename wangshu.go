// Package wangshu is the public API facade for the Wangshu Lua VM.
//
// 设计:docs/design/p1-interpreter/11-embedding-arena-abi.md §1。
// P1/M13 范围:实现 Compile / Program / State / Value 的最小子集;arena ABI
// 列数据接口与 lightuserdata 句柄表留 P1 后续(M14 / P2 接入列内核宿主时再做)。
//
// 用法示例:
//
//	prog, err := wangshu.Compile([]byte("return 1+2"), "snippet")
//	if err != nil { ... }
//	st := wangshu.NewState(wangshu.Options{})
//	results, err := prog.Run(st)
//	// results[0].Number() == 3
package wangshu

import (
	"context"
	"fmt"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/crescent"
	"github.com/Liam0205/wangshu/internal/frontend/compile"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/stdlib"
	"github.com/Liam0205/wangshu/internal/value"
)

// Options configures a State (11 §1.2)。
//
// P1 实现的字段:GCPause(传给 collector)、AllowFileLoad(loadfile/dofile
// 的文件系统读门控,默认关)、HideFileLoaders(从 globals 刮除 loader 四件
// 套,gopher-lua 对位)。其它字段保留接口形状,后续里程碑接入。
type Options struct {
	InitialArenaBytes uint32
	MaxArenaBytes     uint32
	MaxCallDepth      int
	MaxCCalls         int
	GCPause           int
	// AllowFileLoad 开启 loadfile/dofile 读宿主文件系统的能力。
	// 默认 false:嵌入式 VM 接不可信脚本时文件读是越权探测面。
	AllowFileLoad bool
	// HideFileLoaders 严格沙箱模式:NewState 装载 stdlib 后,从 globals
	// 表里刮除 loadfile / dofile / loadstring / load 四件套(置 Nil)。脚本调用
	// 它们会得到 "attempt to call a nil value (global 'X')" fatal 错误,
	// 对齐 gopher-lua 嵌入式沙箱传统(issue #3)——无法以 (nil, errmsg)
	// 形式优雅降级。
	//
	// 默认 false:保留 PUC Lua 5.1.5 对位行为(AllowFileLoad=false 时
	// loadfile 返回 (nil, errmsg)),官方 5.1.5 oracle 对拍不退化。
	//
	// 与 AllowFileLoad=true 同时设为 true 自相矛盾(允许读文件但刮除
	// 入口函数),NewState 检测后 panic fail-fast。
	HideFileLoaders bool
}

// State is a VM instance (11 §1.2)。它持有 globals/registry/arena/host 注册表/
// 句柄表/string intern/GC collector。State 含可变状态,**每 goroutine 一个**。
type State struct {
	core *crescent.State
	// loaded 缓存"已在本 State 装载过的 Program"(11 §1.3:字符串常量首次
	// Call 时惰性 intern,此后复用同一 closure;每次 Run 重复 LoadProgram
	// 会重复拷贝 Proto + 分配 IC)。
	loaded map[*Program]loadedProg
	// mounted 缓存"已挂载过的宿主 Arena"→ 挂载时的列数:同一 *Arena 重复
	// Call 不重复 RegisterHostFn/建代理表(列内核负载是「每批一次 Call」,
	// 否则每批泄漏 2×列数 个闭包 + 表);列数变化(挂载后 AddColumn)重挂。
	mounted map[*Arena]int
}

type loadedProg struct {
	cl arena.GCRef
}

// NewState creates a fresh VM with the P1 minimal stdlib loaded.
func NewState(opts Options) *State {
	if opts.AllowFileLoad && opts.HideFileLoaders {
		// 语义自相矛盾:允许读文件但刮除入口函数,这种组合永远是配置错。
		panic("wangshu: NewState: AllowFileLoad and HideFileLoaders are mutually exclusive")
	}
	st := &State{core: crescent.New()}
	st.core.SetAllowFileLoad(opts.AllowFileLoad)
	// loadstring 的编译回调(经门面注入,避免 crescent → frontend 反向依赖)
	st.core.SetCompileFn(func(src []byte, chunkname string) (uint32, []*bytecode.Proto, error) {
		lx := lex.New(src, chunkname)
		block, err := parse.Parse(lx, chunkname)
		if err != nil {
			return 0, nil, err
		}
		return compile.Compile(block, chunkname)
	})
	stdlib.OpenAll(st.core)
	if opts.HideFileLoaders {
		// gopher-lua 对位:把 loader 四件套从 globals 刮除(置 Nil),
		// 脚本调用 → "attempt to call a nil value"。loadstring 同源风险
		// 面一并刮(动态编译亦是嵌入式沙箱常见关注点)。
		st.core.SetGlobal("loadfile", value.Nil)
		st.core.SetGlobal("dofile", value.Nil)
		st.core.SetGlobal("loadstring", value.Nil)
		st.core.SetGlobal("load", value.Nil)
	}
	return st
}

// SetGCStressMode 开关高频 GC 压力模式(每个 safepoint 强制 full Collect)。
// GC 透明性测试用:压力模式下输出必须与正常模式 byte-equal(12 §5)。
func (st *State) SetGCStressMode(on bool) { st.core.SetGCStressMode(on) }

// GCCountKB 返回 arena 当前已用 KB(= bump 指针;含 freelist 上待复用的空闲块)。
// 长稳观测用:稳态下 freelist 循环复用,本值应有界。
func (st *State) GCCountKB() float64 { return st.core.GCCountKB() }

// SetStepBudget 设置回边指令预算(<=0 关闭):超额时脚本以可恢复错误
// "instruction budget exceeded" 终止。宿主对不可信脚本的执行配额。
func (st *State) SetStepBudget(n int64) { st.core.SetStepBudget(n) }

// SetContext 绑定一个 context.Context 到 State——VM 在每个抢占点
// (回边 / 函数进帧 / TFORLOOP 等)检查 ctx.Err();非空时中止当前
// Run/Call,返回包装 ctx.Err() 的 Go error(可被 Lua 端 pcall 捕获,
// err.Error() 含 "context canceled: <原始 ctx 文本>")。对位 gopher-lua
// `L.SetContext`(issue #4)。
//
// 形态:精确到 wall-clock 中断(`ctx.WithTimeout` / 上游 `Cancel` 都生效);
// 与 SetStepBudget 并存,谁先触发谁终止——后者按指令计数,前者按事件。
//
// 跨 goroutine:context 取消的典型用法是 goroutine A 跑 VM,goroutine B
// 调 `cancel()`——内部用 atomic.Pointer 包裹,跨 goroutine 安全。VM
// State 本身仍单 goroutine。
//
// Return-to-pool:State 复用前(若做 State 池)应 RemoveContext 清掉,
// 否则下次 Borrow 复用陈旧 ctx。
//
// 性能:开销在 chargeStep 的同一抢占点,加一个 atomic.Pointer.Load +
// nil 判;未注入 ctx 时为 nil 比较快路径,接近零成本(性能轮基准未
// 观测影响)。
func (st *State) SetContext(ctx context.Context) {
	if ctx == nil {
		st.core.SetCancelHook(nil)
		return
	}
	st.core.SetCancelHook(ctx.Err)
}

// RemoveContext 清除当前 State 上绑定的 Context(SetContext 配对)。
// 重复调用、未 SetContext 直接 Remove 都无副作用。
func (st *State) RemoveContext() { st.core.SetCancelHook(nil) }

// Program is an immutable compilation product (11 §1.4)。可跨 goroutine 共享;
// 字符串常量首次被某 State Run 时惰性 intern 进该 State 的 arena。
type Program struct {
	mainID uint32
	protos []*bytecode.Proto
}

// Compile turns Lua 5.1 source into a Program (11 §1.3)。
//
// 词法 → 语法 → codegen;编译错误转 Go error 返回。
func Compile(source []byte, chunkname string) (*Program, error) {
	lx := lex.New(source, chunkname)
	block, err := parse.Parse(lx, chunkname)
	if err != nil {
		return nil, err
	}
	mainID, protos, err := compile.Compile(block, chunkname)
	if err != nil {
		return nil, err
	}
	return &Program{mainID: mainID, protos: protos}, nil
}

// Run 在 state 上执行 prog 的主 chunk,可选传入参数(args 是 Value 切片)。
//
// 返回主 chunk 的全部返回值。Lua 运行期错误被转成 Go error。
// 同一 Program 在同一 State 上重复 Run 复用首次装载的 closure(惰性 intern
// 只发生一次,IC 跨 Run 持续生效)。
//
// 返回值生命期:脚本返回 table / function 时,返回的 Value 经 State pin 表
// 登记为 GC 根——调用方应配套 v.Release() 释放,否则长驻 State 下 pin 槽
// 累积。**v0.1.1 → v0.1.2 行为变更**:此前 table / function 返回值被静默
// 映射为 Nil,现在能在 Go 端读出(table 经 v.AsTable()、function 经
// state.Call(v, ...))。只消费标量(nil/bool/number/string)的宿主无需改动。
func (prog *Program) Run(state *State, args ...Value) ([]Value, error) {
	return prog.call(state, nil, args)
}

// Call 在 state 上执行 prog,并把宿主列数据 arena 暴露给脚本(11 §1.5)。
//
// arena 以全局名 `arena` 注入:`arena.<col>[i]` 读第 i 行(1-based,Lua 习惯),
// 零拷贝即时装箱;null 行读出 nil;`arena.rows` 是行数。列只读(11 §5.3)。
// arena 为 nil 等价 Run。
//
// 返回值生命期同 Run:复合值(table/function)调用方应配套 Release()。
func (prog *Program) Call(state *State, arena *Arena, args ...Value) ([]Value, error) {
	return prog.call(state, arena, args)
}

func (prog *Program) call(state *State, ar *Arena, args []Value) (results []Value, err error) {
	// 防线纵深:VM 内部缺陷导致的 Go panic 兜底转 error——嵌入式 VM 的
	// 「宿主进程不可崩」是底线承诺,即使未来再有编译器/解释器漏洞也不带崩宿主。
	defer func() {
		if r := recover(); r != nil {
			results, err = nil, fmt.Errorf("wangshu: internal VM panic: %v", r)
		}
	}()
	lp, ok := state.loaded[prog]
	if !ok {
		cl := state.core.LoadProgram(prog.mainID, prog.protos)
		lp = loadedProg{cl: cl}
		if state.loaded == nil {
			state.loaded = map[*Program]loadedProg{}
		}
		state.loaded[prog] = lp
	}
	if ar != nil {
		// 同一 Arena 只挂载一次:挂载产物(代理表 + HostFn)被 `arena` 全局
		// 持有,重复挂载即重复注册泄漏。Arena 数据就地更新(宿主复用同一
		// *Arena 填下一批)时代理闭包读到的就是新数据,无需重挂;挂载后
		// AddColumn(列数变化)则重挂以暴露新列。
		if state.mounted == nil {
			state.mounted = map[*Arena]int{}
		}
		if n, ok := state.mounted[ar]; !ok || n != len(ar.cols) {
			state.mountArena(ar)
			state.mounted[ar] = len(ar.cols)
		}
	}
	innerArgs := make([]value.Value, len(args))
	for i, a := range args {
		innerArgs[i] = a.toInner(state)
	}
	inner, err := state.core.Call(lp.cl, innerArgs, -1)
	if err != nil {
		return nil, err
	}
	out := make([]Value, len(inner))
	for i, v := range inner {
		out[i] = fromInnerWithPin(state, v)
	}
	return out, nil
}

// SetGlobal 把一个值挂到全局表的 name 键上(对标 gopher-lua `L.SetGlobal`)。
//
// 形态:per-item 栈式 API 的「按名写全局」一项(11 §7.1 / §9.1)。配合
// GetGlobal/Call 完成 gopher-lua drop-in 形式的最小调用循环。
//
// 性能档位:gopher-lua 风格 per-item 跨界,落在被边界成本主导的那一档
// (design-premises 前提一)。低频/原型/迁移期可用;高频热路径请改 arena 列轨。
func (st *State) SetGlobal(name string, v Value) {
	st.core.SetGlobal(name, v.toInner(st))
}

// GetGlobal 读取全局表的 name 键(对标 gopher-lua `L.GetGlobal`)。
//
// 缺失键返回 Nil。若读出的是 function,其底层引用经 State pin 表登记
// 为 GC 根——Value 析构前请配合 v.Release() 显式释放槽位(可选;不释放
// 仅在长驻 State 反复 GetGlobal 不同名 fn 时累积小量内存)。table 自 v0.1.2
// 起经 v.AsTable() 暴露(issue #2);userdata 仍不暴露,映射为 Nil。
func (st *State) GetGlobal(name string) Value {
	v := st.core.GetGlobal(name)
	return fromInnerWithPin(st, v)
}

// Call 在 state 上调用一个 function Value(对标 gopher-lua
// `L.CallByParam(P{Fn: fn, NRet: -1, Protect: true}, args...)`)。
//
// 典型用法(对标 pineapple transform_by_lua 形态):
//
//	prog, _ := wangshu.Compile([]byte(`function f(x) return x*2 end`), "rules")
//	prog.Run(st)                                // 顶层定义 f 进 globals
//	fn := st.GetGlobal("f")
//	defer fn.Release()
//	for _, x := range items {
//	    r, _ := st.Call(fn, wangshu.Number(x))
//	    use(r[0])
//	}
//
// 形态(11 §7.1 / §9.1):per-item 跨界,落在被边界成本主导的那一档
// (design-premises 前提一)。低频/原型/迁移期用,高频热路径请改 arena 列轨。
//
// 约束:
//   - fn 必须 IsFunction() 且来自同一 State(GetGlobal 取出);跨 State 报错
//   - 仅支持 Lua function(脚本里 `function f() end` 定义)。Register 注册的
//     host closure 从 Go 端 Call 暂未支持(只能从 Lua 内调用)。
//
// 返回:被调函数 RETURN 的全部值;运行期错误转 Go error(含 traceback)。
// Go panic 兜底转 error(防线纵深,同 Program.Call)。
//
// 返回值生命期同 Program.Run/Call:复合值(table/function)经 pin 表登记,
// 调用方应配套 Release()。
func (st *State) Call(fn Value, args ...Value) (results []Value, err error) {
	if !fn.IsFunction() {
		return nil, fmt.Errorf("wangshu: Call: value is not a function (kind=%s)", fn.Display())
	}
	if fn.fnState != st {
		return nil, fmt.Errorf("wangshu: Call: function belongs to a different State")
	}
	defer func() {
		if r := recover(); r != nil {
			results, err = nil, fmt.Errorf("wangshu: internal VM panic: %v", r)
		}
	}()
	ref := st.core.PinnedRefAt(fn.pinIdx)
	if ref.IsNull() {
		return nil, fmt.Errorf("wangshu: Call: function has been released")
	}
	innerArgs := make([]value.Value, len(args))
	for i, a := range args {
		innerArgs[i] = a.toInner(st)
	}
	inner, callErr := st.core.Call(ref, innerArgs, -1)
	if callErr != nil {
		return nil, callErr
	}
	out := make([]Value, len(inner))
	for i, v := range inner {
		out[i] = fromInnerWithPin(st, v)
	}
	return out, nil
}

// mountArena 把宿主 Arena 映射进 VM 可读视图(11 §5.1-§5.3)。
//
// P1 形态:arena = Lua table { rows = n, <col> = 列代理 };列代理 = 空表 +
// metatable{__index = ReadCell 闭包, __newindex = 只读报错}。整列从不复制,
// proxy[i] 每次读即时 NaN-box(11 §4.1 零拷贝读)。
func (st *State) mountArena(ar *Arena) {
	core := st.core
	arenaTbl := core.NewLibTable(uint32(len(ar.cols) + 1))
	core.SetTableField(arenaTbl, "rows", value.NumberValue(float64(ar.nrows)))
	for ci := range ar.cols {
		col := &ar.cols[ci]
		proxy := core.NewLibTable(0)
		meta := core.NewLibTable(2)
		colRef := col // 闭包捕获列指针
		nrows := ar.nrows
		strBytes := ar.strBytes
		readCell := func(ist *crescent.State, cargs []value.Value) ([]value.Value, *crescent.LuaError) {
			// __index(proxy, i)
			if len(cargs) < 2 || !value.IsNumber(cargs[1]) {
				return []value.Value{value.Nil}, nil
			}
			i := int64(value.AsNumber(cargs[1]))
			if i < 1 || uint32(i) > nrows {
				return []value.Value{value.Nil}, nil
			}
			row := uint32(i - 1)
			if !colRef.present(row) {
				return []value.Value{value.Nil}, nil
			}
			switch colRef.tag {
			case colFloat64:
				return []value.Value{value.NumberValue(colRef.f64[row])}, nil
			case colInt64:
				v := colRef.i64[row]
				if v > 1<<53 || v < -(1<<53) {
					return nil, crescent.NewError("arena int64 value exceeds 2^53 precision range")
				}
				return []value.Value{value.NumberValue(float64(v))}, nil
			case colBool:
				bit := colRef.boolBits[row/64]&(1<<(row%64)) != 0
				return []value.Value{value.BoolValue(bit)}, nil
			case colString:
				slot := colRef.strSlots[row]
				b := strBytes[slot.off : slot.off+slot.len]
				ref := ist.InternForEmbed(b)
				return []value.Value{value.MakeGC(value.TagString, ref)}, nil
			}
			return []value.Value{value.Nil}, nil
		}
		readonly := func(_ *crescent.State, _ []value.Value) ([]value.Value, *crescent.LuaError) {
			return nil, crescent.NewError("arena column is read-only")
		}
		idxID := core.RegisterHostFn(readCell)
		nwID := core.RegisterHostFn(readonly)
		core.SetTableField(meta, "__index", value.MakeGC(value.TagFunction, core.MakeHostClosure(idxID)))
		core.SetTableField(meta, "__newindex", value.MakeGC(value.TagFunction, core.MakeHostClosure(nwID)))
		core.SetMeta(proxy, meta)
		core.SetTableField(arenaTbl, col.name, value.MakeGC(value.TagTable, proxy))
	}
	core.SetGlobal("arena", value.MakeGC(value.TagTable, arenaTbl))
}

// Value 是公共 API 的多类型值(11 §4.5)。
//
// P1/M13 简化版:用一个 sum-type Go struct 表示。GC 解耦:Value 持的
// 字符串内容已从 VM arena 拷出(string),函数 / 表值通过所属 State 的
// pin 表间接持有(kFunction / kTable:外部不可直接构造 GCRef,只能从
// NewTable / GetGlobal / Call 返回值取出,经 pin 表登记为 GC 根)。
// userdata 仍不暴露。
type Value struct {
	kind kind
	// number 字段
	num float64
	// string 字段(已拷出 arena 字节)
	str []byte
	// bool 字段
	b bool
	// function / table 字段:fnState 为所属 State,pinIdx 是其 pin 表索引。
	// fnState != nil 表示有效;Release 后置 nil。
	fnState *State
	pinIdx  uint32
}

type kind uint8

const (
	kNil kind = iota
	kBool
	kNumber
	kString
	kFunction
	kTable
)

// 构造器(function/table 无公共构造器:function 由 GetGlobal 取出,
// table 由 State.NewTable 创建)。
func Nil() Value             { return Value{kind: kNil} }
func Bool(b bool) Value      { return Value{kind: kBool, b: b} }
func Number(f float64) Value { return Value{kind: kNumber, num: f} }
func String(s string) Value  { return Value{kind: kString, str: []byte(s)} }

// 类型判定。
func (v Value) IsNil() bool      { return v.kind == kNil }
func (v Value) IsBool() bool     { return v.kind == kBool }
func (v Value) IsNumber() bool   { return v.kind == kNumber }
func (v Value) IsString() bool   { return v.kind == kString }
func (v Value) IsFunction() bool { return v.kind == kFunction && v.fnState != nil }
func (v Value) IsTable() bool    { return v.kind == kTable && v.fnState != nil }

// 读出。
func (v Value) Bool() bool      { return v.b }
func (v Value) Number() float64 { return v.num }
func (v Value) Str() string     { return string(v.str) }

// Release 显式释放 function / table Value 的 pin 表槽位。重复 Release /
// 其它 kind 上调用均无副作用。长驻 State 下若反复 NewTable / GetGlobal
// 取出 function 且不 Release,pin 表会按槽位累积——pineapple 一类「每脚本
// 一次取出」的形态无此问题,可以省略 Release;高吞吐场景应配对。
func (v *Value) Release() {
	if v.fnState == nil {
		return
	}
	if v.kind != kFunction && v.kind != kTable {
		return
	}
	v.fnState.core.UnpinRef(v.pinIdx)
	v.fnState = nil
}

// String 输出 Lua 风格(便于错误消息)。
func (v Value) Display() string {
	switch v.kind {
	case kNil:
		return "nil"
	case kBool:
		if v.b {
			return "true"
		}
		return "false"
	case kNumber:
		return crescent.FormatLuaNumber(v.num)
	case kString:
		return string(v.str)
	case kFunction:
		return "function"
	case kTable:
		return "table"
	}
	return "<unknown>"
}

// toInner / fromInner 桥接公共 Value 与 internal value.Value。
//
// kFunction / kTable 桥接到内部 TagFunction / TagTable 时,直接复用所属
// State 的 pin 表槽(caller 已在 State.Call 层校验 fnState == 目标 state);
// 跨 State 的 function/table Value 在此被映射为 Nil 兜底(防 GCRef 错绑
// arena 引发 UAF)。
func (v Value) toInner(state *State) value.Value {
	switch v.kind {
	case kNil:
		return value.Nil
	case kBool:
		return value.BoolValue(v.b)
	case kNumber:
		return value.NumberValue(v.num)
	case kString:
		// 经 collector intern 进 state arena
		ref := state.coreInternBytes(v.str)
		return value.MakeGC(value.TagString, ref)
	case kFunction:
		if v.fnState != state {
			return value.Nil
		}
		ref := state.core.PinnedRefAt(v.pinIdx)
		if ref.IsNull() {
			return value.Nil
		}
		return value.MakeGC(value.TagFunction, ref)
	case kTable:
		if v.fnState != state {
			return value.Nil
		}
		ref := state.core.PinnedRefAt(v.pinIdx)
		if ref.IsNull() {
			return value.Nil
		}
		return value.MakeGC(value.TagTable, ref)
	}
	return value.Nil
}

func fromInner(state *State, v value.Value) Value {
	if value.IsNumber(v) {
		return Number(value.AsNumber(v))
	}
	switch value.Tag(v) {
	case value.TagNil:
		return Nil()
	case value.TagBool:
		return Bool(value.AsBool(v))
	case value.TagString:
		// 拷出 arena 字节
		bytes := state.coreStringBytes(value.GCRefOf(v))
		out := make([]byte, len(bytes))
		copy(out, bytes)
		return Value{kind: kString, str: out}
	}
	// table/function/userdata 默认不暴露(返回 nil);
	// function/table 的暴露由 fromInnerWithPin 经 pin 表显式登记,
	// 避免静默副作用(每次 raw 读都把 GCRef 钉住会无界泄漏)。
	return Nil()
}

// fromInnerWithPin 是「可携带 function / table 引用」的桥接,仅
// GetGlobal / NewTable / State.Call 返回值一类「公共面取出 / 创建
// 复合值供 Go 长期持有」的入口调用。function / table 走 PinRef 登记
// 到 State pin 表(GC 根)以隔离 globals 覆盖与 freelist 复用风险。
func fromInnerWithPin(state *State, v value.Value) Value {
	switch value.Tag(v) {
	case value.TagFunction:
		ref := value.GCRefOf(v)
		idx := state.core.PinRef(ref)
		return Value{kind: kFunction, fnState: state, pinIdx: idx}
	case value.TagTable:
		ref := value.GCRefOf(v)
		idx := state.core.PinRef(ref)
		return Value{kind: kTable, fnState: state, pinIdx: idx}
	}
	return fromInner(state, v)
}

// coreInternBytes / coreStringBytes 是 State 内部的便捷桥(避免暴露 internal/gc)。
func (st *State) coreInternBytes(b []byte) arena.GCRef {
	// 通过 Run 路径,String value 创建在 inner state 上;走 collector 的 Intern。
	// 我们在公共 API 上不暴露 collector,这里是 helper(internal/crescent.State 不直接暴露),
	// 因此通过 arena 访问 helper。
	return st.core.InternForEmbed(b)
}

func (st *State) coreStringBytes(ref arena.GCRef) []byte {
	return object.StringBytes(st.core.Arena(), ref)
}
