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

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/crescent"
	"github.com/Liam0205/wangshu/internal/gibbous/jit"
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
	// ipairs 的内部步进迭代器(经全局名间接引用,脚本不可见性不作要求——5.1 也暴露 next)
	{
		id := st.RegisterHostFn(ipairsIter)
		cl := st.MakeHostClosure(id)
		st.SetGlobal("__ipairs_iter", value.MakeGC(value.TagFunction, cl))
	}
	registerNamespaced(st, "math", append(mathFns, mathExtraFns...), mathIntrinsics)
	strTbl := registerNamespaced(st, "string", stringFns, nil)
	st.SetStringLib(strTbl) // string 值的 per-type __index(`("x"):upper()`)
	// LUA_COMPAT_GFIND:gfind 必须与 gmatch 是同一函数对象
	// (官方测试套断言 string.gfind == string.gmatch)
	{
		gm, _ := st.RawGet(strTbl, intern(st, "gmatch"))
		st.SetTableField(strTbl, "gfind", gm)
	}
	tblTbl := registerNamespaced(st, "table", tableFns, nil)
	// table.unpack 别名(5.1 主入口是全局 unpack,5.2+ 是 table.unpack;两者都给)
	{
		id := st.RegisterHostFn(baseFnUnpackImpl)
		cl := st.MakeHostClosure(id)
		st.SetTableField(tblTbl, "unpack", value.MakeGC(value.TagFunction, cl))
	}
	registerNamespaced(st, "os", osFns, nil)
	registerNamespaced(st, "io", ioFns, nil)
	registerNamespaced(st, "coroutine", coroutineFns, nil)
	registerBaseEnv(st) // _G/_VERSION/collectgarbage/gcinfo/loadfile/dofile
	// math 常量
	{
		mathTblV, _ := st.RawGet(st.Globals(), intern(st, "math"))
		if value.Tag(mathTblV) == value.TagTable {
			mt := value.GCRefOf(mathTblV)
			st.SetTableField(mt, "pi", value.NumberValue(luaPi))
			st.SetTableField(mt, "huge", value.NumberValue(luaHuge()))
		}
	}
}

type entry struct {
	name string
	fn   crescent.HostFn
}

// registerNamespaced installs a namespaced stdlib table. The optional
// intrinsics map tags recognized function names with a P4 native
// intrinsic kind (jit.Intrinsic*) so the JIT can emit them inline
// (issue #77); pass nil for namespaces with no intrinsics.
func registerNamespaced(st *crescent.State, ns string, fns []entry, intrinsics map[string]uint8) arena.GCRef {
	tbl := st.NewLibTable(uint32(len(fns)))
	for _, e := range fns {
		id := st.RegisterHostFn(e.fn)
		if kind := intrinsics[e.name]; kind != 0 {
			st.RegisterIntrinsic(id, kind)
		}
		cl := st.MakeHostClosure(id)
		st.SetTableField(tbl, e.name, value.MakeGC(value.TagFunction, cl))
	}
	st.SetGlobal(ns, value.MakeGC(value.TagTable, tbl))
	return tbl
}

// 通用辅助:把 Value 转 string(用于 print/tostring;__tostring 经
// valueToStringMeta,本函数是无元方法的 raw 形态)。
func valueToString(st *crescent.State, v value.Value) string {
	if value.IsNumber(v) {
		return crescent.FormatLuaNumber(value.AsNumber(v))
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
		return fmt.Sprintf("function: 0x%08x", uint64(value.GCRefOf(v)))
	case value.TagTable:
		return fmt.Sprintf("table: 0x%08x", uint64(value.GCRefOf(v)))
	case value.TagLightUD:
		if st.IsCoroutineHandle(v) {
			return fmt.Sprintf("thread: 0x%08x", value.AsLightUD(v))
		}
		return fmt.Sprintf("userdata: 0x%08x", value.AsLightUD(v))
	case value.TagUserdata:
		return fmt.Sprintf("userdata: 0x%08x", uint64(value.GCRefOf(v)))
	}
	return "<?>"
}

// valueToStringMeta is tostring's full semantics: consult the
// __tostring metamethod first (07).
//
// PUC 5.1 (luaB_tostring) passes the metamethod's return value through
// UNCHANGED -- nil/number/table results are not folded into strings;
// the "must be a string" requirement belongs to consumers like print
// (lua_tostring returns NULL for anything but string/number, which is
// what raises). Mirrored here: with hadMeta=true, raw is the
// metamethod's first return verbatim (no returns -> Nil).
func valueToStringMeta(st *crescent.State, v value.Value) (raw value.Value, hadMeta bool, e *crescent.LuaError) {
	if value.Tag(v) == value.TagTable {
		h := st.MetaFieldOf(v, "__tostring")
		if value.Tag(h) == value.TagFunction {
			results, e := st.ProtectedCallDirect(h, []value.Value{v})
			if e != nil {
				return value.Nil, true, e
			}
			if len(results) > 0 {
				return results[0], true, nil
			}
			return value.Nil, true, nil
		}
	}
	return v, false, nil
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
	{"pcall", baseFnPcall},
	{"setmetatable", baseFnSetMetatable},
	{"getmetatable", baseFnGetMetatable},
	{"rawget", baseFnRawGet},
	{"rawset", baseFnRawSet},
	{"rawequal", baseFnRawEqual},
	{"next", baseFnNext},
	{"pairs", baseFnPairs},
	{"ipairs", baseFnIpairs},
	{"xpcall", baseFnXpcall},
	{"loadstring", baseFnLoadstring},
	{"load", baseFnLoad},
}

// baseFnLoad:load(func [, chunkname])(10 §4.7 reader 循环完整形态)。
//
// 5.1:反复调 reader 函数拿源码片段,返回 nil/空串/无值表示结束,拼成完整
// chunk 再编译。字符串实参也容(等价 loadstring,宽容形态)。
func baseFnLoad(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewError("bad argument #1 to 'load' (function expected)")
	}
	if value.Tag(args[0]) == value.TagString {
		return baseFnLoadstring(st, args)
	}
	if value.Tag(args[0]) != value.TagFunction {
		return nil, crescent.NewError("bad argument #1 to 'load' (function expected, got " +
			st.TypeName(args[0]) + ")")
	}
	chunkname := "=(load)"
	if len(args) >= 2 && value.Tag(args[1]) == value.TagString {
		chunkname = string(object.StringBytes(st.Arena(), value.GCRefOf(args[1])))
	}
	var srcBuf []byte
	const maxReaderPieces = 1 << 20 // 护栏:防恶意 reader 永不返回 nil
	done := false
	for i := 0; i < maxReaderPieces; i++ {
		results, e := st.ProtectedCallDirect(args[0], nil)
		if e != nil {
			// PUC's lua_load runs the reader inside
			// luaD_protectedparser: an error raised BY the reader is
			// caught and surfaces as load's (nil, errmsg) result
			// instead of propagating (oracle diff fuzz catch:
			// load(load) -- a reader error is a load failure, not a
			// caller error).
			return []value.Value{value.Nil, intern(st, e.Error())}, nil
		}
		if len(results) == 0 || results[0] == value.Nil {
			done = true
			break
		}
		if value.Tag(results[0]) != value.TagString {
			return []value.Value{value.Nil, intern(st, "reader function must return a string")}, nil
		}
		piece := object.StringBytes(st.Arena(), value.GCRefOf(results[0]))
		if len(piece) == 0 {
			done = true
			break // 空串 = 结束(5.1)
		}
		srcBuf = append(srcBuf, piece...)
	}
	if !done {
		// 超限静默截断会把不完整源码当完整 chunk 编译(莫名 syntax error,
		// 或截断点恰好语法完整时静默错果)——显式报错。
		return []value.Value{value.Nil, intern(st, "reader function: too many pieces")}, nil
	}
	fn, err := st.CompileAndLoad(srcBuf, chunkname)
	if err != nil {
		return []value.Value{value.Nil, intern(st, err.Error())}, nil
	}
	return []value.Value{fn}, nil
}

// baseFnLoadstring:loadstring(s [, chunkname]) → function | (nil, errmsg)。
func baseFnLoadstring(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 || value.Tag(args[0]) != value.TagString {
		return nil, crescent.NewError("bad argument #1 to 'loadstring' (string expected)")
	}
	src := object.StringBytes(st.Arena(), value.GCRefOf(args[0]))
	// 默认 chunkname = 源串本身(官方 luaL_optstring(L,2,s)),错误前缀
	// 显示为 [string "首行..."](luaO_chunkid 截断)。
	chunkname := string(src)
	if len(args) >= 2 && value.Tag(args[1]) == value.TagString {
		chunkname = string(object.StringBytes(st.Arena(), value.GCRefOf(args[1])))
	}
	fn, err := st.CompileAndLoad(src, chunkname)
	if err != nil {
		return []value.Value{value.Nil, intern(st, err.Error())}, nil
	}
	return []value.Value{fn}, nil
}

// baseFnNext:next(t [, key]) → (nextKey, nextVal) | nil。
func baseFnNext(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 || value.Tag(args[0]) != value.TagTable {
		return nil, crescent.NewError("bad argument #1 to 'next' (table expected)")
	}
	key := value.Nil
	if len(args) >= 2 {
		key = args[1]
	}
	k, v, ok, e := st.RawNext(value.GCRefOf(args[0]), key)
	if e != nil {
		return nil, e
	}
	if !ok {
		return []value.Value{value.Nil}, nil
	}
	return []value.Value{k, v}, nil
}

// baseFnPairs:pairs(t) → (next, t, nil)。
func baseFnPairs(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 || value.Tag(args[0]) != value.TagTable {
		return nil, crescent.NewError("bad argument #1 to 'pairs' (table expected)")
	}
	nextFn, _ := st.RawGet(st.Globals(), intern(st, "next"))
	return []value.Value{nextFn, args[0], value.Nil}, nil
}

// baseFnIpairs:ipairs(t) → (iter, t, 0);iter(t, i) → (i+1, t[i+1]) | nil。
func baseFnIpairs(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 || value.Tag(args[0]) != value.TagTable {
		return nil, crescent.NewError("bad argument #1 to 'ipairs' (table expected)")
	}
	iterFn, _ := st.RawGet(st.Globals(), intern(st, "__ipairs_iter"))
	return []value.Value{iterFn, args[0], value.NumberValue(0)}, nil
}

// ipairsIter 是 ipairs 的步进迭代器(注册为内部全局 __ipairs_iter)。
func ipairsIter(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 2 || value.Tag(args[0]) != value.TagTable || !value.IsNumber(args[1]) {
		return nil, crescent.NewError("bad argument to ipairs iterator")
	}
	i := value.AsNumber(args[1]) + 1
	v, e := st.RawGet(value.GCRefOf(args[0]), value.NumberValue(i))
	if e != nil {
		return nil, e
	}
	if v == value.Nil {
		return []value.Value{value.Nil}, nil
	}
	return []value.Value{value.NumberValue(i), v}, nil
}

// baseFnPcall:pcall(f, ...) → (true, results...) | (false, errval)(09 §pcall;05 §9.3)。
func baseFnPcall(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewError("bad argument #1 to 'pcall' (value expected)")
	}
	results, e := st.ProtectedCall(args[0], args[1:])
	if e != nil {
		errVal := e.Value
		if !e.HasValue {
			errVal = intern(st, e.Msg)
		}
		return []value.Value{value.False, errVal}, nil
	}
	out := make([]value.Value, 0, len(results)+1)
	out = append(out, value.True)
	out = append(out, results...)
	return out, nil
}

func baseFnSetMetatable(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 2 || value.Tag(args[0]) != value.TagTable {
		return nil, crescent.NewError("bad argument #1 to 'setmetatable' (table expected)")
	}
	t := value.GCRefOf(args[0])
	// 受保护元表(__metatable 域)不可改(5.1)
	if old := st.MetaOf(t); old != 0 {
		if shield, e := st.RawGet(old, intern(st, "__metatable")); e == nil && shield != value.Nil {
			return nil, crescent.NewError("cannot change a protected metatable")
		}
	}
	switch value.Tag(args[1]) {
	case value.TagTable:
		st.SetMeta(t, value.GCRefOf(args[1]))
	case value.TagNil:
		st.SetMeta(t, 0)
	default:
		return nil, crescent.NewError("bad argument #2 to 'setmetatable' (nil or table expected)")
	}
	return []value.Value{args[0]}, nil
}

func baseFnGetMetatable(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 || value.Tag(args[0]) != value.TagTable {
		return []value.Value{value.Nil}, nil
	}
	mt := st.MetaOf(value.GCRefOf(args[0]))
	if mt == 0 {
		return []value.Value{value.Nil}, nil
	}
	// __metatable shield(5.1):元表带 __metatable 域时返回该域而非元表本身
	shield, e := st.RawGet(mt, intern(st, "__metatable"))
	if e == nil && shield != value.Nil {
		return []value.Value{shield}, nil
	}
	return []value.Value{value.MakeGC(value.TagTable, mt)}, nil
}

func baseFnRawGet(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 2 || value.Tag(args[0]) != value.TagTable {
		return nil, crescent.NewError("bad argument to 'rawget'")
	}
	v, e := st.RawGet(value.GCRefOf(args[0]), args[1])
	if e != nil {
		return nil, e
	}
	return []value.Value{v}, nil
}

func baseFnRawSet(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 3 || value.Tag(args[0]) != value.TagTable {
		return nil, crescent.NewError("bad argument to 'rawset'")
	}
	if e := st.RawSet(value.GCRefOf(args[0]), args[1], args[2]); e != nil {
		return nil, e
	}
	return []value.Value{args[0]}, nil
}

func baseFnRawEqual(_ *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 2 {
		return nil, crescent.NewError("bad argument to 'rawequal'")
	}
	eq := args[0] == args[1]
	if !eq && value.IsNumber(args[0]) && value.IsNumber(args[1]) {
		eq = value.AsNumber(args[0]) == value.AsNumber(args[1])
	}
	return []value.Value{value.BoolValue(eq)}, nil
}

func baseFnPrint(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	parts := make([]string, len(args))
	for i, a := range args {
		raw, hadMeta, e := valueToStringMeta(st, a) // print goes through tostring semantics (__tostring applies)
		if e != nil {
			return nil, e
		}
		// PUC print runs lua_tostring on the tostring result: anything
		// but string/number raises "'tostring' must return a string to
		// 'print'".
		if hadMeta && value.Tag(raw) != value.TagString && !value.IsNumber(raw) {
			return nil, crescent.NewError("'tostring' must return a string to 'print'")
		}
		parts[i] = valueToString(st, raw)
	}
	fmt.Println(strings.Join(parts, "\t"))
	return nil, nil
}

func baseFnToString(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewError("bad argument #1 to 'tostring' (value expected)")
	}
	raw, hadMeta, e := valueToStringMeta(st, args[0])
	if e != nil {
		return nil, e
	}
	if hadMeta {
		// PUC passes the metamethod result through verbatim
		// (nil/table/... are not folded into strings).
		return []value.Value{raw}, nil
	}
	return []value.Value{intern(st, valueToString(st, raw))}, nil
}

func baseFnToNumber(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		// 官方 luaL_checkany:无参是错误(≠ tonumber(nil) 的返回 nil)
		return nil, crescent.NewError("bad argument #1 to 'tonumber' (value expected)")
	}
	if len(args) >= 2 && args[1] != value.Nil {
		// tonumber(s, base):base 2-36,逐字符按进制解析(5.1 strtoul 语义,
		// 负数回绕除外——官方 '-ff' 经 C strtoul 回绕成 2^64-255,本实现取
		// 直觉的 -255;已登记差分豁免)
		baseF, ok := toNumberStr(st, args[1])
		if !ok {
			return nil, crescent.NewError("bad argument #2 to 'tonumber' (number expected)")
		}
		base := int(baseF)
		if base < 2 || base > 36 {
			return nil, crescent.NewError("bad argument #2 to 'tonumber' (base out of range)")
		}
		if value.Tag(args[0]) != value.TagString {
			return nil, crescent.NewError("bad argument #1 to 'tonumber' (string expected, got " +
				st.TypeName(args[0]) + ")")
		}
		s := strings.TrimSpace(string(object.StringBytes(st.Arena(), value.GCRefOf(args[0]))))
		if s == "" {
			return []value.Value{value.Nil}, nil
		}
		neg := false
		if s[0] == '+' || s[0] == '-' {
			neg = s[0] == '-'
			s = s[1:]
		}
		// strtoul 接受 base=16 的可选 0x/0X 前缀(tonumber("0x10", 16) = 16;
		// 配置写 "0xff" 再按 16 转换是常见形态)。
		if base == 16 && len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
			s = s[2:]
		}
		if s == "" {
			return []value.Value{value.Nil}, nil
		}
		acc := 0.0
		for i := 0; i < len(s); i++ {
			c := s[i]
			var d int
			switch {
			case c >= '0' && c <= '9':
				d = int(c - '0')
			case c >= 'a' && c <= 'z':
				d = int(c-'a') + 10
			case c >= 'A' && c <= 'Z':
				d = int(c-'A') + 10
			default:
				return []value.Value{value.Nil}, nil
			}
			if d >= base {
				return []value.Value{value.Nil}, nil
			}
			acc = acc*float64(base) + float64(d)
		}
		if neg {
			acc = -acc
		}
		return []value.Value{value.NumberValue(acc)}, nil
	}
	f, ok := crescentToNumber(st, args[0])
	if !ok {
		return []value.Value{value.Nil}, nil
	}
	return []value.Value{value.NumberValue(f)}, nil
}

// crescentToNumber 走 ParseLuaNumber 唯一入口(支持 0x 十六进制,07 §5.2)。
func crescentToNumber(st *crescent.State, v value.Value) (float64, bool) {
	if value.IsNumber(v) {
		return value.AsNumber(v), true
	}
	if value.Tag(v) == value.TagString {
		return crescent.ParseLuaNumber(string(object.StringBytes(st.Arena(), value.GCRefOf(v))))
	}
	return 0, false
}

func baseFnType(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewError("bad argument #1 to 'type' (value expected)")
	}
	if st.IsCoroutineHandle(args[0]) {
		return []value.Value{intern(st, "thread")}, nil
	}
	return []value.Value{intern(st, st.TypeName(args[0]))}, nil
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
	level := 1
	if len(args) >= 2 {
		if f, ok := toNumberStr(st, args[1]); ok {
			level = int(f)
		}
	}
	// 官方 luaB_error 的加前缀条件是 lua_isstring(对 number 也真,
	// lua_concat 把它转成字符串):error(0) → "file:line: 0" 字符串。
	if level > 0 && value.IsNumber(v) {
		v = intern(st, crescent.FormatLuaNumber(value.AsNumber(v)))
	}
	e := crescent.NewErrorVal(v, valueToString(st, v))
	e.Level = level
	if level == 0 || value.Tag(v) != value.TagString {
		// level=0 或非字符串错误值:不加位置前缀(5.1 语义)
		e.Level = 0
		e.MarkAnnotated()
	}
	return nil, e
}

func baseFnSelect(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewError("bad argument #1 to 'select'")
	}
	first := args[0]
	if value.Tag(first) == value.TagString {
		s := string(object.StringBytes(st.Arena(), value.GCRefOf(first)))
		// PUC luaB_select tests only the first character
		// (*lua_tostring == '#'): select("#0") takes the "#" branch
		// too. The oracle diff fuzz caught the whole-string compare.
		if len(s) > 0 && s[0] == '#' {
			return []value.Value{value.NumberValue(float64(len(args) - 1))}, nil
		}
	}
	f, ok := toNumberStr(st, first)
	if !ok {
		return nil, crescent.NewError("bad argument #1 to 'select' (number expected)")
	}
	idx := int(f)
	n := len(args) - 1
	if idx < 0 {
		// 负索引:从尾部数(5.1);越界报错
		idx = n + idx + 1
		if idx < 1 {
			return nil, crescent.NewError("bad argument #1 to 'select' (index out of range)")
		}
	} else if idx == 0 {
		return nil, crescent.NewError("bad argument #1 to 'select' (index out of range)")
	}
	if idx > n {
		return nil, nil
	}
	return args[idx:], nil
}

// baseFnUnpack:unpack(t [, i [, j]])(实现委托 tablelib 的 baseFnUnpackImpl)。
func baseFnUnpack(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	return baseFnUnpackImpl(st, args)
}

// ----- math 子库 -----

// mathIntrinsics maps math.* names the P4 native JIT can emit inline to
// their intrinsic kind (issue #77). Only pure-numeric functions whose
// result is byte-equal to a hardware SSE/NEON instruction are listed;
// sin/cos/tan/exp/log stay host-only (no exact single-instruction
// equivalent). Passed to registerNamespaced for the "math" namespace.
var mathIntrinsics = map[string]uint8{
	"sqrt":  jit.IntrinsicSqrt,
	"floor": jit.IntrinsicFloor,
	"ceil":  jit.IntrinsicCeil,
	"abs":   jit.IntrinsicAbs,
	"max":   jit.IntrinsicMax,
	"min":   jit.IntrinsicMin,
}

var mathFns = []entry{
	{"abs", mathFn1("abs", math.Abs)},
	{"ceil", mathFn1("ceil", math.Ceil)},
	{"floor", mathFn1("floor", math.Floor)},
	{"sqrt", mathFn1("sqrt", math.Sqrt)},
	{"sin", mathFn1("sin", math.Sin)},
	{"cos", mathFn1("cos", math.Cos)},
	{"tan", mathFn1("tan", math.Tan)},
	{"exp", mathFn1("exp", math.Exp)},
	{"log", mathFn1("log", math.Log)},
	{"max", mathFnMax},
	{"min", mathFnMin},
}

// mathFn1 包装一元 math 函数;错误措辞对齐官方 luaL_checknumber
// (bad argument #1 to 'sin' (number expected, got string))。
func mathFn1(name string, f func(float64) float64) crescent.HostFn {
	return func(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
		if len(args) == 0 {
			return nil, crescent.NewError(fmt.Sprintf("bad argument #1 to '%s' (number expected, got no value)", name))
		}
		x, ok := toNumberStr(st, args[0])
		if !ok {
			return nil, crescent.NewError(fmt.Sprintf("bad argument #1 to '%s' (number expected, got %s)", name, st.TypeName(args[0])))
		}
		return []value.Value{value.NumberValue(f(x))}, nil
	}
}

func mathFnMax(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	return mathMinMax(st, args, "max", func(a, b float64) bool { return a > b })
}

func mathFnMin(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	return mathMinMax(st, args, "min", func(a, b float64) bool { return a < b })
}

// mathMinMax 收口 max/min:全部参数(含首参)统一校验,错误措辞对齐官方
// luaL_checknumber(首参曾被静默吞错:math.max("x", 2) 错误地返回 2)。
func mathMinMax(st *crescent.State, args []value.Value, name string, better func(a, b float64) bool) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewError(fmt.Sprintf("bad argument #1 to '%s' (number expected, got no value)", name))
	}
	out, ok := toNumberStr(st, args[0])
	if !ok {
		return nil, crescent.NewError(fmt.Sprintf("bad argument #1 to '%s' (number expected, got %s)", name, st.TypeName(args[0])))
	}
	for i, a := range args[1:] {
		f, ok := toNumberStr(st, a)
		if !ok {
			return nil, crescent.NewError(fmt.Sprintf("bad argument #%d to '%s' (number expected, got %s)", i+2, name, st.TypeName(a)))
		}
		if better(f, out) {
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
	{"find", stringFnFind},
	{"match", stringFnMatch},
	{"gmatch", stringFnGmatch},
	{"gsub", stringFnGsub},
	{"format", stringFnFormat},
	{"byte", stringFnByte},
	{"char", stringFnChar},
}

func stringFnLen(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	// strArg, not a raw tag check: PUC's luaL_checklstring coerces
	// numbers to strings (string.len(123) == 3). Caught by the cgo
	// oracle diff fuzz; find/match/gsub already went through strArg.
	s, e := strArg(st, args, 0, "len")
	if e != nil {
		return nil, e
	}
	return []value.Value{value.NumberValue(float64(len(s)))}, nil
}

func stringFnUpper(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	s, e := strArg(st, args, 0, "upper")
	if e != nil {
		return nil, e
	}
	return []value.Value{intern(st, asciiMapCase(s, 'a', 'z', -32))}, nil
}

func stringFnLower(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	s, e := strArg(st, args, 0, "lower")
	if e != nil {
		return nil, e
	}
	return []value.Value{intern(st, asciiMapCase(s, 'A', 'Z', 32))}, nil
}

// asciiMapCase shifts bytes in [lo, hi] by delta, all other bytes pass
// through untouched. PUC 5.1 upper/lower are per-byte C toupper/
// tolower ("C" locale = ASCII); Go's strings.ToUpper decodes UTF-8
// and rewrites invalid bytes to U+FFFD, corrupting binary strings
// (oracle diff fuzz: ("\x95"):upper()).
func asciiMapCase(s []byte, lo, hi byte, delta int) string {
	out := make([]byte, len(s))
	for i, c := range s {
		if c >= lo && c <= hi {
			c = byte(int(c) + delta)
		}
		out[i] = c
	}
	return string(out)
}

func stringFnSub(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	sb, e := strArg(st, args, 0, "sub")
	if e != nil {
		return nil, e
	}
	if len(args) < 2 {
		return nil, crescent.NewError("bad argument to 'sub'")
	}
	s := string(sb)
	startF, ok := toNumberStr(st, args[1])
	if !ok {
		return nil, crescent.NewError("bad argument #2 to 'sub'")
	}
	endF := float64(-1)
	// PUC str_sub: end is luaL_optinteger(L, 3, -1) -- an explicit nil
	// third argument counts as absent (default -1 = end of string).
	// Oracle diff fuzz caught rejecting sub(0, nil)-shaped calls where
	// the nil comes from an undefined global.
	if len(args) >= 3 && args[2] != value.Nil {
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
	sb, e := strArg(st, args, 0, "rep")
	if e != nil {
		return nil, e
	}
	if len(args) < 2 {
		return nil, crescent.NewError("bad argument to 'rep'")
	}
	s := string(sb)
	nF, ok := toNumberStr(st, args[1])
	if !ok {
		return nil, crescent.NewError("bad argument #2 to 'rep'")
	}
	n := int(nF)
	if n < 0 {
		n = 0
	}
	// 嵌入式 hardening:阻断脚本经 string.rep 触发宿主进程 OOM crash。
	// 1 GiB 阈值是「实际使用足够 + 拦住上亿变体」的折中,与 PUC 5.1.5 /
	// gopher-lua 不一致(两者均不防御直接 OOM),但「宿主进程不可崩」
	// 优先级高于字节一致(12 §10 豁免)。可被 pcall 兜住。
	const maxRepBytes = 1 << 30
	if len(s) > 0 && n > 0 && len(s) > maxRepBytes/n {
		return nil, crescent.NewError("string length overflow")
	}
	return []value.Value{intern(st, strings.Repeat(s, n))}, nil
}

func stringFnReverse(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	s, e := strArg(st, args, 0, "reverse")
	if e != nil {
		return nil, e
	}
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
