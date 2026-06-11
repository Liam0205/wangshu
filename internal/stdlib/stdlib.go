// Package stdlib provides the P1 minimal Lua 5.1 standard library subset.
//
// 设计:docs/design/p1-interpreter/10-stdlib.md(P1 裁剪表)。M12 范围实现
// base 子库的最小可用子集 + math 算术常用 + string 部分。完整的 pattern matcher、
// io / os / coroutine 等留 P1 后续推进。
package stdlib

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Liam0205/wangshu/internal/crescent"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// OpenAll 在 state 上注册 P1 最小标准库面(对齐 gopher-lua 默认提供面的子集)。
//
// 完整的"libsSafe / Libs / Exclude 三层禁用"机制(10 §12.1)留后续。当前一律
// 注册全部内建函数到 globals。
func OpenAll(st *crescent.State) {
	for _, e := range baseFns {
		id := st.RegisterHostFn(e.fn)
		cl := st.MakeHostClosure(id)
		st.SetGlobal(e.name, value.MakeGC(value.TagFunction, cl))
	}
	registerNamespaced(st, "math", mathFns)
	registerNamespaced(st, "string", stringFns)
}

type entry struct {
	name string
	fn   crescent.HostFn
}

func registerNamespaced(st *crescent.State, ns string, fns []entry) {
	tbl := st.NewLibTable(uint32(len(fns)))
	for _, e := range fns {
		id := st.RegisterHostFn(e.fn)
		cl := st.MakeHostClosure(id)
		st.SetTableField(tbl, e.name, value.MakeGC(value.TagFunction, cl))
	}
	st.SetGlobal(ns, value.MakeGC(value.TagTable, tbl))
}

// 通用辅助:把 Value 转 string(用于 print/tostring)。
func valueToString(st *crescent.State, v value.Value) string {
	if value.IsNumber(v) {
		return strconv.FormatFloat(value.AsNumber(v), 'g', 14, 64)
	}
	switch value.Tag(v) {
	case value.TagNil:
		return "nil"
	case value.TagBool:
		if value.AsBool(v) {
			return "true"
		}
		return "false"
	case value.TagString:
		return string(object.StringBytes(st.Arena(), value.GCRefOf(v)))
	case value.TagFunction:
		return "function: 0x?"
	case value.TagTable:
		return "table: 0x?"
	}
	return "<?>"
}

// 通用辅助:Value → float64(数字 + 可转字符串);失败返回 (0, false)。
func toNumberStr(st *crescent.State, v value.Value) (float64, bool) {
	if value.IsNumber(v) {
		return value.AsNumber(v), true
	}
	if value.Tag(v) == value.TagString {
		s := strings.TrimSpace(string(object.StringBytes(st.Arena(), value.GCRefOf(v))))
		f, err := strconv.ParseFloat(s, 64)
		if err == nil {
			return f, true
		}
	}
	return 0, false
}

// 把字符串 intern 进 state arena,得 string Value。
func intern(st *crescent.State, s string) value.Value {
	ref := st.InternForEmbed([]byte(s))
	return value.MakeGC(value.TagString, ref)
}

// ----- base 子库 -----

var baseFns = []entry{
	{"print", baseFnPrint},
	{"tostring", baseFnToString},
	{"tonumber", baseFnToNumber},
	{"type", baseFnType},
	{"assert", baseFnAssert},
	{"error", baseFnError},
	{"select", baseFnSelect},
	{"unpack", baseFnUnpack},
}

func baseFnPrint(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = valueToString(st, a)
	}
	fmt.Println(strings.Join(parts, "\t"))
	return nil, nil
}

func baseFnToString(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewError("bad argument #1 to 'tostring' (value expected)")
	}
	return []value.Value{intern(st, valueToString(st, args[0]))}, nil
}

func baseFnToNumber(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return []value.Value{value.Nil}, nil
	}
	f, ok := toNumberStr(st, args[0])
	if !ok {
		return []value.Value{value.Nil}, nil
	}
	return []value.Value{value.NumberValue(f)}, nil
}

func baseFnType(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewError("bad argument #1 to 'type' (value expected)")
	}
	return []value.Value{intern(st, crescent.TypeNameOf(args[0]))}, nil
}

func baseFnAssert(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewError("assertion failed!")
	}
	if !value.Truthy(args[0]) {
		msg := "assertion failed!"
		if len(args) >= 2 {
			msg = valueToString(st, args[1])
		}
		return nil, crescent.NewError(msg)
	}
	return args, nil
}

func baseFnError(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewErrorVal(value.Nil, "")
	}
	v := args[0]
	return nil, crescent.NewErrorVal(v, valueToString(st, v))
}

func baseFnSelect(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewError("bad argument #1 to 'select'")
	}
	first := args[0]
	if value.Tag(first) == value.TagString {
		s := string(object.StringBytes(st.Arena(), value.GCRefOf(first)))
		if s == "#" {
			return []value.Value{value.NumberValue(float64(len(args) - 1))}, nil
		}
	}
	f, ok := toNumberStr(st, first)
	if !ok {
		return nil, crescent.NewError("bad argument #1 to 'select' (number expected)")
	}
	idx := int(f)
	if idx < 1 || idx > len(args)-1 {
		return nil, nil
	}
	return args[idx:], nil
}

func baseFnUnpack(_ *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, nil
	}
	// M12 简化:只支持"已存在的连续数组键 1..n";完整版 M11/M12 元表 + table 接入后再做。
	// 当前直接报错让用户使用 table.unpack 之类的替代(P1 简化)。
	return nil, crescent.NewError("unpack: not yet supported (M14)")
}

// ----- math 子库 -----

var mathFns = []entry{
	{"abs", mathFn1(math.Abs)},
	{"ceil", mathFn1(math.Ceil)},
	{"floor", mathFn1(math.Floor)},
	{"sqrt", mathFn1(math.Sqrt)},
	{"sin", mathFn1(math.Sin)},
	{"cos", mathFn1(math.Cos)},
	{"tan", mathFn1(math.Tan)},
	{"exp", mathFn1(math.Exp)},
	{"log", mathFn1(math.Log)},
	{"max", mathFnMax},
	{"min", mathFnMin},
}

func mathFn1(f func(float64) float64) crescent.HostFn {
	return func(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
		if len(args) == 0 {
			return nil, crescent.NewError("bad argument")
		}
		x, ok := toNumberStr(st, args[0])
		if !ok {
			return nil, crescent.NewError("bad argument (number expected)")
		}
		return []value.Value{value.NumberValue(f(x))}, nil
	}
}

func mathFnMax(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewError("bad argument")
	}
	out, _ := toNumberStr(st, args[0])
	for _, a := range args[1:] {
		f, ok := toNumberStr(st, a)
		if !ok {
			return nil, crescent.NewError("bad argument (number expected)")
		}
		if f > out {
			out = f
		}
	}
	return []value.Value{value.NumberValue(out)}, nil
}

func mathFnMin(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewError("bad argument")
	}
	out, _ := toNumberStr(st, args[0])
	for _, a := range args[1:] {
		f, ok := toNumberStr(st, a)
		if !ok {
			return nil, crescent.NewError("bad argument (number expected)")
		}
		if f < out {
			out = f
		}
	}
	return []value.Value{value.NumberValue(out)}, nil
}

// ----- string 子库 -----

var stringFns = []entry{
	{"len", stringFnLen},
	{"upper", stringFnUpper},
	{"lower", stringFnLower},
	{"sub", stringFnSub},
	{"rep", stringFnRep},
	{"reverse", stringFnReverse},
}

func stringFnLen(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 || value.Tag(args[0]) != value.TagString {
		return nil, crescent.NewError("bad argument #1 to 'len' (string expected)")
	}
	n := object.StringLen(st.Arena(), value.GCRefOf(args[0]))
	return []value.Value{value.NumberValue(float64(n))}, nil
}

func stringFnUpper(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 || value.Tag(args[0]) != value.TagString {
		return nil, crescent.NewError("bad argument #1 to 'upper'")
	}
	s := string(object.StringBytes(st.Arena(), value.GCRefOf(args[0])))
	return []value.Value{intern(st, strings.ToUpper(s))}, nil
}

func stringFnLower(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 || value.Tag(args[0]) != value.TagString {
		return nil, crescent.NewError("bad argument #1 to 'lower'")
	}
	s := string(object.StringBytes(st.Arena(), value.GCRefOf(args[0])))
	return []value.Value{intern(st, strings.ToLower(s))}, nil
}

func stringFnSub(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 2 || value.Tag(args[0]) != value.TagString {
		return nil, crescent.NewError("bad argument to 'sub'")
	}
	s := string(object.StringBytes(st.Arena(), value.GCRefOf(args[0])))
	startF, ok := toNumberStr(st, args[1])
	if !ok {
		return nil, crescent.NewError("bad argument #2 to 'sub'")
	}
	endF := float64(len(s))
	if len(args) >= 3 {
		f, ok := toNumberStr(st, args[2])
		if !ok {
			return nil, crescent.NewError("bad argument #3 to 'sub'")
		}
		endF = f
	}
	start := normIdx(int(startF), len(s))
	end := normIdx(int(endF), len(s))
	if start < 1 {
		start = 1
	}
	if end > len(s) {
		end = len(s)
	}
	if start > end {
		return []value.Value{intern(st, "")}, nil
	}
	return []value.Value{intern(st, s[start-1:end])}, nil
}

func stringFnRep(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 2 || value.Tag(args[0]) != value.TagString {
		return nil, crescent.NewError("bad argument to 'rep'")
	}
	s := string(object.StringBytes(st.Arena(), value.GCRefOf(args[0])))
	nF, ok := toNumberStr(st, args[1])
	if !ok {
		return nil, crescent.NewError("bad argument #2 to 'rep'")
	}
	n := int(nF)
	if n < 0 {
		n = 0
	}
	return []value.Value{intern(st, strings.Repeat(s, n))}, nil
}

func stringFnReverse(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 || value.Tag(args[0]) != value.TagString {
		return nil, crescent.NewError("bad argument #1 to 'reverse'")
	}
	s := object.StringBytes(st.Arena(), value.GCRefOf(args[0]))
	out := make([]byte, len(s))
	for i := range s {
		out[len(s)-1-i] = s[i]
	}
	return []value.Value{intern(st, string(out))}, nil
}

// normIdx 处理 Lua 风格的负索引(-1 = 末位)。
func normIdx(i, n int) int {
	if i < 0 {
		return n + i + 1
	}
	return i
}
