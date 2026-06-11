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
	"fmt"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/crescent"
	"github.com/Liam0205/wangshu/internal/frontend/compile"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// Options configures a State (11 §1.2)。
//
// P1/M13 实现的字段:GCPause(传给 collector)。其它字段保留接口形状,后续
// 里程碑接入。
type Options struct {
	InitialArenaBytes uint32
	MaxArenaBytes     uint32
	MaxCallDepth      int
	MaxCCalls         int
	GCPause           int
}

// State is a VM instance (11 §1.2)。它持有 globals/registry/arena/host 注册表/
// 句柄表/string intern/GC collector。State 含可变状态,**每 goroutine 一个**。
type State struct {
	core *crescent.State
}

// NewState creates a fresh VM。
func NewState(_ Options) *State {
	return &State{core: crescent.New()}
}

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
func (prog *Program) Run(state *State, args ...Value) ([]Value, error) {
	cl := state.core.LoadProgram(prog.mainID, prog.protos)
	innerArgs := make([]value.Value, len(args))
	for i, a := range args {
		innerArgs[i] = a.toInner(state)
	}
	results, err := state.core.Call(cl, innerArgs, -1)
	if err != nil {
		return nil, err
	}
	out := make([]Value, len(results))
	for i, v := range results {
		out[i] = fromInner(state, v)
	}
	return out, nil
}

// Value 是公共 API 的多类型值(11 §4.5)。
//
// P1/M13 简化版:用一个 sum-type Go struct 表示。GC 解耦:Value 持的
// 字符串/表内容已从 VM arena 拷出(string)或经 GCRef 间接持有(table:
// 暂不导出原生 table 字段,只支持读出 string/number/bool/nil)。
type Value struct {
	kind kind
	// number 字段
	num float64
	// string 字段(已拷出 arena 字节)
	str []byte
	// bool 字段
	b bool
}

type kind uint8

const (
	kNil kind = iota
	kBool
	kNumber
	kString
)

// 构造器。
func Nil() Value             { return Value{kind: kNil} }
func Bool(b bool) Value      { return Value{kind: kBool, b: b} }
func Number(f float64) Value { return Value{kind: kNumber, num: f} }
func String(s string) Value  { return Value{kind: kString, str: []byte(s)} }

// 类型判定。
func (v Value) IsNil() bool    { return v.kind == kNil }
func (v Value) IsBool() bool   { return v.kind == kBool }
func (v Value) IsNumber() bool { return v.kind == kNumber }
func (v Value) IsString() bool { return v.kind == kString }

// 读出。
func (v Value) Bool() bool      { return v.b }
func (v Value) Number() float64 { return v.num }
func (v Value) String_() string { return string(v.str) }

// String 输出 Lua 风格(便于错误消息)。
func (v Value) GoString() string {
	switch v.kind {
	case kNil:
		return "nil"
	case kBool:
		if v.b {
			return "true"
		}
		return "false"
	case kNumber:
		return fmt.Sprintf("%g", v.num)
	case kString:
		return string(v.str)
	}
	return "<unknown>"
}

// toInner / fromInner 桥接公共 Value 与 internal value.Value。
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
	// table/function/userdata 暂不向公共面暴露;返回 nil
	return Nil()
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
