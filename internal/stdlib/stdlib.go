// Package stdlib provides the P1 minimal Lua 5.1 standard library subset.
//
// Design: docs/design/p1-interpreter/10-stdlib.md (the P1 trim table). The M12
// scope implements the minimal usable subset of the base sublibrary + common
// math arithmetic + part of string. The full pattern matcher, io / os /
// coroutine, etc. are deferred to later P1 work.
package stdlib

import (
	"fmt"
	"math"
	"strings"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/crescent"
	"github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// OpenAll registers the P1 minimal standard-library surface on the state
// (a subset of what gopher-lua provides by default).
//
// The full "libsSafe / Libs / Exclude three-tier disable" mechanism (10 §12.1)
// is deferred. For now every builtin function is unconditionally registered
// into globals.
func OpenAll(st *crescent.State) {
	for _, e := range baseFns {
		id := st.RegisterHostFn(e.fn)
		cl := st.MakeHostClosure(id)
		st.SetGlobal(e.name, value.MakeGC(value.TagFunction, cl))
	}
	// The internal stepping iterator of ipairs (referenced indirectly via a
	// global name; script-side invisibility is not required -- 5.1 exposes
	// next too).
	{
		id := st.RegisterHostFn(ipairsIter)
		cl := st.MakeHostClosure(id)
		st.SetGlobal("__ipairs_iter", value.MakeGC(value.TagFunction, cl))
	}
	registerNamespaced(st, "math", append(mathFns, mathExtraFns...), mathIntrinsics)
	strTbl := registerNamespaced(st, "string", stringFns, nil)
	st.SetStringLib(strTbl) // per-type __index for string values (`("x"):upper()`)
	// PUC parity: the shared string metatable is a REAL table {__index =
	// string} -- getmetatable("") returns it, and a script may mutate it
	// (allowed by official 5.1.5; the change takes effect globally).
	{
		mt := st.NewLibTable(1)
		st.SetTableField(mt, "__index", value.MakeGC(value.TagTable, strTbl))
		st.SetStringMeta(mt)
	}
	// LUA_COMPAT_GFIND: gfind must be the same function object as gmatch
	// (the official test suite asserts string.gfind == string.gmatch).
	{
		gm, _ := st.RawGet(strTbl, intern(st, "gmatch"))
		st.SetTableField(strTbl, "gfind", gm)
	}
	tblTbl := registerNamespaced(st, "table", tableFns, nil)
	// table.unpack alias (in 5.1 the main entry is the global unpack; in
	// 5.2+ it is table.unpack; provide both).
	{
		id := st.RegisterHostFn(baseFnUnpackImpl)
		cl := st.MakeHostClosure(id)
		st.SetTableField(tblTbl, "unpack", value.MakeGC(value.TagFunction, cl))
	}
	registerNamespaced(st, "os", osFns, nil)
	ioTbl := registerNamespaced(st, "io", ioFns, nil)
	registerStdStreams(st, ioTbl)
	registerNamespaced(st, "coroutine", coroutineFns, nil)
	registerNamespaced(st, "debug", debugFns, nil)
	registerBaseEnv(st) // _G/_VERSION/collectgarbage/gcinfo/loadfile/dofile
	// math constants
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

// Generic helper: convert a Value to a string (used by print/tostring;
// __tostring goes through valueToStringMeta -- this function is the raw form
// without metamethods).
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
	// Userdata is included: PUC's file handles carry __tostring, and gating the lookup on
	// table-or-string meant tostring(io.stdout) printed the raw "userdata: 0x..." form.
	if value.Tag(v) == value.TagTable || value.Tag(v) == value.TagString ||
		value.Tag(v) == value.TagUserdata {
		// MetaFieldOf covers both table metatables and the shared
		// string metatable (PUC: a __tostring installed on
		// getmetatable("") applies to every string).
		// ANY non-nil metafield is called, not just functions: PUC's
		// luaL_callmeta pushes the field and lua_call's it, so a
		// non-callable __tostring (number, string, table without
		// __call) raises "attempt to call a X value" while a table
		// WITH __call goes through — the call machinery decides, not
		// a type filter here (nightly oracle fuzz c3a6ed137f361957:
		// __tostring=0 must raise; wangshu silently fell back to the
		// address form).
		h := st.MetaFieldOf(v, "__tostring")
		if value.Tag(h) != value.TagNil {
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

// Generic helper: Value -> float64 (numbers + convertible strings); returns
// (0, false) on failure.
func toNumberStr(st *crescent.State, v value.Value) (float64, bool) {
	if value.IsNumber(v) {
		return value.AsNumber(v), true
	}
	if value.Tag(v) == value.TagString {
		// Use the VM's Lua-number parser (crescent.ParseLuaNumber, aligned
		// with PUC luaO_str2d) rather than a bare strconv.ParseFloat: the
		// latter rejects Lua hex integers like "0X0"/"0xff" (it only accepts
		// C99 hex FLOATS, which need a mantissa/p-exponent). PUC's
		// luaL_checknumber coerces such strings, so string.rep("x", "0X0")
		// and friends must too (oracle diff fuzz #174).
		s := string(object.StringBytes(st.Arena(), value.GCRefOf(v)))
		return crescent.ParseLuaNumber(s)
	}
	return 0, false
}

// Intern a string into the state arena, yielding a string Value.
func intern(st *crescent.State, s string) value.Value {
	ref := st.InternForEmbed([]byte(s))
	return value.MakeGC(value.TagString, ref)
}

// ----- base sublibrary -----

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

// baseFnLoad: load(func [, chunkname]) (10 §4.7, the full reader loop form).
//
// 5.1: repeatedly call the reader function to get source fragments; a return of
// nil/empty-string/no-value signals end, and the fragments are concatenated
// into a complete chunk before compiling. A string argument is also accepted
// (equivalent to loadstring, the lenient form).
func baseFnLoad(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	// PUC 5.1's load is reader-function ONLY (luaL_checktype
	// LUA_TFUNCTION); accepting a string is 5.2 behavior. The oracle
	// diff arg sweep caught load("") diverging (PUC raises).
	if len(args) == 0 {
		return nil, crescent.NewArgError(1, "function expected, got no value")
	}
	if value.Tag(args[0]) != value.TagFunction {
		return nil, crescent.NewArgError(1, "function expected, got "+st.TypeName(args[0]))
	}
	chunkname := "=(load)"
	if len(args) >= 2 && value.Tag(args[1]) == value.TagString {
		chunkname = string(object.StringBytes(st.Arena(), value.GCRefOf(args[1])))
	}
	var srcBuf []byte
	const maxReaderPieces = 1 << 20 // guardrail: guard against a malicious reader that never returns nil
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
			break // empty string = end (5.1)
		}
		srcBuf = append(srcBuf, piece...)
	}
	if !done {
		// Silently truncating on overrun would compile incomplete source as
		// a complete chunk (a puzzling syntax error, or a silent wrong result
		// when the truncation point happens to be syntactically complete) --
		// report an explicit error.
		return []value.Value{value.Nil, intern(st, "reader function: too many pieces")}, nil
	}
	fn, err := st.CompileAndLoad(srcBuf, chunkname)
	if err != nil {
		return []value.Value{value.Nil, intern(st, err.Error())}, nil
	}
	return []value.Value{fn}, nil
}

// baseFnLoadstring: loadstring(s [, chunkname]) -> function | (nil, errmsg).
func baseFnLoadstring(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	// strArg (not a raw tag check): PUC's luaL_checklstring coerces
	// numbers, so loadstring(0) compiles the chunk "0" (a syntax
	// error returned as (nil, errmsg), not a raised type error).
	src, e := strArg(st, args, 0, "loadstring")
	if e != nil {
		return nil, e
	}
	// Default chunkname = the source string itself (official
	// luaL_optstring(L,2,s)); the error prefix is shown as
	// [string "first line..."] (luaO_chunkid truncation).
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

// baseFnNext: next(t [, key]) -> (nextKey, nextVal) | nil.
func baseFnNext(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 || value.Tag(args[0]) != value.TagTable {
		return nil, crescent.NewArgError(1, "table expected, got "+argTypeName(args, 0))
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

// baseFnPairs: pairs(t) -> (next, t, nil).
func baseFnPairs(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 || value.Tag(args[0]) != value.TagTable {
		return nil, crescent.NewArgError(1, "table expected, got "+argTypeName(args, 0))
	}
	nextFn, _ := st.RawGet(st.Globals(), intern(st, "next"))
	return []value.Value{nextFn, args[0], value.Nil}, nil
}

// baseFnIpairs: ipairs(t) -> (iter, t, 0); iter(t, i) -> (i+1, t[i+1]) | nil.
func baseFnIpairs(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 || value.Tag(args[0]) != value.TagTable {
		return nil, crescent.NewArgError(1, "table expected, got "+argTypeName(args, 0))
	}
	iterFn, _ := st.RawGet(st.Globals(), intern(st, "__ipairs_iter"))
	return []value.Value{iterFn, args[0], value.NumberValue(0)}, nil
}

// ipairsIter is the stepping iterator of ipairs (registered as the internal
// global __ipairs_iter).
func ipairsIter(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	// PUC ipairsaux: luaL_checkint(L, 2) FIRST, then checktype(L, 1, TABLE).
	if len(args) < 2 || !value.IsNumber(args[1]) {
		got := "no value"
		if len(args) >= 2 {
			got = st.TypeName(args[1])
		}
		return nil, crescent.NewArgError(2, "number expected, got "+got)
	}
	if value.Tag(args[0]) != value.TagTable {
		return nil, crescent.NewArgError(1, "table expected, got "+st.TypeName(args[0]))
	}
	// PUC's ipairsaux reads the control value with luaL_checkint, so it narrows
	// to int32 and truncates a fraction: __ipairs_iter(t, 2^32+2) continues from
	// 2, and (t, 1.5) continues from 1. Taking the raw double skipped straight to
	// nil for both. This site sat outside the earlier sweep, which enumerated
	// public stdlib functions -- the iterator is installed as a private global.
	// The increment happens in int32 too, so it WRAPS like PUC's C int does:
	// from 2147483647 the next index is -2147483648, not 2147483648. Adding 1 to
	// the narrowed value as a float64 never wraps and missed that key.
	i32 := cCharCastInt32(value.AsNumber(args[1])) + 1
	i := float64(i32)
	v, e := st.RawGet(value.GCRefOf(args[0]), value.NumberValue(i))
	if e != nil {
		return nil, e
	}
	if v == value.Nil {
		// PUC's ipairsaux returns 0 values when the element is nil, not one
		// explicit nil. Visible through select("#", ...).
		return nil, nil
	}
	return []value.Value{value.NumberValue(i), v}, nil
}

// baseFnPcall: pcall(f, ...) -> (true, results...) | (false, errval) (09 §pcall; 05 §9.3).
func baseFnPcall(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewArgError(1, "value expected")
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
	// The two arguments are checked SEPARATELY. Folding a missing arg 2 into the
	// arg-1 test produced the self-contradicting "bad argument #1 (table
	// expected, got table)" for setmetatable({}); PUC does
	// luaL_checktype(L, 1, LUA_TTABLE) then luaL_argcheck on arg 2, reporting
	// "bad argument #2 (nil or table expected)".
	if len(args) < 1 || value.Tag(args[0]) != value.TagTable {
		return nil, crescent.NewArgError(1, "table expected, got "+argTypeName(args, 0))
	}
	if len(args) < 2 {
		return nil, crescent.NewArgError(2, "nil or table expected")
	}
	t := value.GCRefOf(args[0])
	// A protected metatable (the __metatable field) cannot be changed (5.1)
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
		return nil, crescent.NewArgError(2, "nil or table expected")
	}
	return []value.Value{args[0]}, nil
}

func baseFnGetMetatable(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	// PUC luaB_getmetatable: luaL_checkany raises on a missing
	// argument (oracle diff fuzz catch: getmetatable() errors);
	// any PRESENT value, nil included, returns nil for no metatable.
	if len(args) == 0 {
		return nil, crescent.NewArgError(1, "value expected")
	}
	if value.Tag(args[0]) == value.TagString {
		// PUC: strings share one real metatable ({__index = string}).
		if smt := st.StringMeta(); smt != 0 {
			return []value.Value{value.MakeGC(value.TagTable, smt)}, nil
		}
		return []value.Value{value.Nil}, nil
	}
	if value.Tag(args[0]) == value.TagUserdata {
		// Userdata carries its own metatable too; returning nil unconditionally hid
		// io.stdout's, where PUC reports a table.
		if mt := st.UserdataMeta(value.GCRefOf(args[0])); mt != 0 {
			// __metatable, if present, is what getmetatable reports (5.1 protection).
			if shield, e := st.RawGet(mt, intern(st, "__metatable")); e == nil && shield != value.Nil {
				return []value.Value{shield}, nil
			}
			return []value.Value{value.MakeGC(value.TagTable, mt)}, nil
		}
		return []value.Value{value.Nil}, nil
	}
	if value.Tag(args[0]) != value.TagTable {
		return []value.Value{value.Nil}, nil
	}
	mt := st.MetaOf(value.GCRefOf(args[0]))
	if mt == 0 {
		return []value.Value{value.Nil}, nil
	}
	// __metatable shield (5.1): when the metatable has a __metatable field,
	// return that field rather than the metatable itself
	shield, e := st.RawGet(mt, intern(st, "__metatable"))
	if e == nil && shield != value.Nil {
		return []value.Value{shield}, nil
	}
	return []value.Value{value.MakeGC(value.TagTable, mt)}, nil
}

func baseFnRawGet(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 1 || value.Tag(args[0]) != value.TagTable {
		got := "no value"
		if len(args) >= 1 {
			got = st.TypeName(args[0])
		}
		return nil, crescent.NewArgError(1, "table expected, got "+got)
	}
	if len(args) < 2 {
		return nil, crescent.NewArgError(2, "value expected")
	}
	v, e := st.RawGet(value.GCRefOf(args[0]), args[1])
	if e != nil {
		return nil, e
	}
	return []value.Value{v}, nil
}

func baseFnRawSet(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 1 || value.Tag(args[0]) != value.TagTable {
		got := "no value"
		if len(args) >= 1 {
			got = st.TypeName(args[0])
		}
		return nil, crescent.NewArgError(1, "table expected, got "+got)
	}
	if len(args) < 2 {
		return nil, crescent.NewArgError(2, "value expected")
	}
	if len(args) < 3 {
		return nil, crescent.NewArgError(3, "value expected")
	}
	if e := st.RawSet(value.GCRefOf(args[0]), args[1], args[2]); e != nil {
		return nil, e
	}
	return []value.Value{args[0]}, nil
}

func baseFnRawEqual(_ *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 1 {
		return nil, crescent.NewArgError(1, "value expected")
	}
	if len(args) < 2 {
		return nil, crescent.NewArgError(2, "value expected")
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
	// Embedded NULs are WRITTEN, not truncated -- a deliberate deviation.
	//
	// PUC's luaB_print uses fputs, which stops at the first NUL, so
	// print("a\0b") emits just "a" and silently drops the rest. That is an
	// artifact of the C string call, not a Lua semantic: 5.1 strings are
	// explicitly 8-bit clean and may contain NULs, and PUC's own io.write uses
	// fwrite WITH the length and does not truncate -- so the reference is
	// internally inconsistent here. Matching the artifact would mean losing user
	// data on purpose.
	//
	// The differential harness captures print through its own accumulator rather
	// than a C FILE*, so this does not surface as a divergence there.
	fmt.Println(strings.Join(parts, "\t"))
	return nil, nil
}

func baseFnToString(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewArgError(1, "value expected")
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
		// Official luaL_checkany: no argument is an error (unlike
		// tonumber(nil), which returns nil)
		return nil, crescent.NewArgError(1, "value expected")
	}
	if len(args) >= 2 && args[1] != value.Nil {
		// tonumber(s, base): base 2-36, parsed character by character in the
		// given radix, including C strtoul's unsigned negation: '-ff' at base
		// 16 wraps to 2^64-255, matching the official build)
		baseF, ok := toNumberStr(st, args[1])
		if !ok {
			return nil, crescent.NewArgError(2, "number expected, got "+st.TypeName(args[1]))
		}
		// Arg 2 goes through the same luaL_checkint as any int argument, so it is
		// narrowed to int32 before the range check: tonumber("10", 2^32+10) has a
		// base of 10 on PUC, not an out-of-range error.
		base := int(cCharCastInt32(baseF))
		// PUC checks base == 10 BEFORE validating the range and before
		// touching arg 1, and routes it to the standard conversion --
		// luaB_tonumber's `if (base == 10)` branch is the same code path as
		// tonumber(x) with no base at all. Only a non-10 base reaches
		// strtoul's digit-by-digit parse.
		//
		// Falling into the digit loop for base 10 rejected every form the
		// digit loop cannot express: tonumber("1.5",10), ("0x10",10),
		// ("1e3",10), ("inf",10), ("nan",10) and even tonumber(1.5,10) all
		// returned nil where PUC returns the number. Found by sweeping which
		// channels reach the number parser, not by the issue that prompted
		// the change -- #192 was only the "nan(...)" instance of it.
		if base == 10 {
			return baseToNumberStandard(st, args)
		}
		// Arg 1 is read BEFORE the base range check: PUC's non-10 branch is
		// `const char *s1 = luaL_checkstring(L, 1);` followed by luaL_argcheck on
		// the base, so tonumber({}, 1) reports #1 (string expected), not #2.
		if value.Tag(args[0]) != value.TagString && !value.IsNumber(args[0]) {
			return nil, crescent.NewArgError(1, "string expected, got "+st.TypeName(args[0]))
		}
		if base < 2 || base > 36 {
			return nil, crescent.NewArgError(2, "base out of range")
		}
		// PUC tonumber(x, base) reads arg 1 via luaL_checkstring, which
		// coerces a number to its string form (tonumber(0, "2") sees "0").
		// Accept string or number; only other types raise (oracle diff
		// fuzz #175).
		var s string
		switch {
		case value.Tag(args[0]) == value.TagString:
			s = string(object.StringBytes(st.Arena(), value.GCRefOf(args[0])))
		case value.IsNumber(args[0]):
			s = crescent.FormatLuaNumber(value.AsNumber(args[0]))
		default:
			return nil, crescent.NewArgError(1, "string expected, got "+st.TypeName(args[0]))
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return []value.Value{value.Nil}, nil
		}
		neg := false
		if s[0] == '+' || s[0] == '-' {
			neg = s[0] == '-'
			s = s[1:]
		}
		// strtoul accepts an optional 0x/0X prefix for base=16
		// (tonumber("0x10", 16) = 16; writing "0xff" in config and then
		// converting with base 16 is a common form).
		if base == 16 && len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
			s = s[2:]
		}
		if s == "" {
			return []value.Value{value.Nil}, nil
		}
		acc := 0.0
		accU := uint64(0)
		ovf := false
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
			// Accumulate in uint64 with strtoul's saturation, not in float64.
			// C strtoul clamps to ULONG_MAX on overflow (and sets ERANGE, which
			// PUC ignores), so 20 'f' digits at base 16 yield 2^64-1 -- whereas
			// float64 accumulation keeps growing and produced 1.2e24.
			if !ovf {
				hi := accU > (^uint64(0)-uint64(d))/uint64(base)
				if hi {
					ovf = true
				} else {
					accU = accU*uint64(base) + uint64(d)
				}
			}
		}
		if ovf {
			// strtoul returns ULONG_MAX on overflow and does NOT then negate it:
			// the sign is consumed before the digits, and the saturated result is
			// what the function returns. Negating it afterwards yielded 1 for
			// every overflowing negative, e.g. tonumber("-1"..string.rep("0",16),
			// 16). So return here rather than falling into the negation below.
			return []value.Value{value.NumberValue(float64(^uint64(0)))}, nil
		}
		if neg {
			// C strtoul negates in UNSIGNED arithmetic: "-7" at base 8 yields
			// (unsigned long)(-7) == 2^64-7, which PUC pushes as a lua_Number.
			// Returning the intuitive -7 diverged from the oracle for every
			// negative input at a non-10 base.
			//
			// Done here, on the uint64 accumulator, and converted to float64
			// only afterwards: 2^64-7 is not representable as a float64, so
			// negating the rounded float gives a different answer than negating
			// the integer and rounding once. PUC wraps in unsigned long and only
			// then converts, so the order matters.
			//
			// This is well-defined C, unlike the double->int casts elsewhere, so
			// it is matched rather than skipped. A comment here used to call it a
			// "registered diff exemption" -- nothing implemented that exemption,
			// so the divergence was live and the fuzzer could have filed it.
			accU = -accU
		}
		acc = float64(accU)
		return []value.Value{value.NumberValue(acc)}, nil
	}
	return baseToNumberStandard(st, args)
}

// argTypeName is luaL_typename for an argument slot: the type name when the
// argument was passed, and the literal "no value" when it was not. PUC's
// lua_typename maps LUA_TNONE to "no value", and every "X expected, got Y"
// message carries it, so omitting the clause diverges for a missing argument.
func argTypeName(args []value.Value, n int) string {
	if n >= len(args) {
		return "no value"
	}
	return crescent.TypeNameOf(args[n])
}

// baseToNumberStandard is tonumber's standard conversion, shared by the no-base
// call and by an explicit base of 10 -- PUC's luaB_tonumber routes both through
// the same branch, so they must not diverge.
func baseToNumberStandard(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	f, ok := crescentToNumber(st, args[0])
	if !ok {
		return []value.Value{value.Nil}, nil
	}
	return []value.Value{value.NumberValue(f)}, nil
}

// crescentToNumber goes through the single ParseLuaNumber entry point
// (supports 0x hex, 07 §5.2).
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
		return nil, crescent.NewArgError(1, "value expected")
	}
	if st.IsCoroutineHandle(args[0]) {
		return []value.Value{intern(st, "thread")}, nil
	}
	return []value.Value{intern(st, st.TypeName(args[0]))}, nil
}

func baseFnAssert(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		// PUC runs luaL_checkany(L, 1) first, so a missing argument is an
		// argument error, not an assertion failure.
		return nil, crescent.NewArgError(1, "value expected")
	}
	if !value.Truthy(args[0]) {
		// PUC: luaL_error(L, "%s", luaL_optstring(L, 2, "assertion failed!")).
		// luaL_optstring accepts only a string or a number (which it coerces), so
		// a table or boolean message raises "bad argument #2 (string expected,
		// got X)" rather than being stringified. Using valueToString accepted
		// anything and returned the table itself as the error value.
		msg := "assertion failed!"
		if len(args) >= 2 && args[1] != value.Nil {
			switch {
			case value.Tag(args[1]) == value.TagString:
				msg = valueToString(st, args[1])
			case value.IsNumber(args[1]):
				msg = crescent.FormatLuaNumber(value.AsNumber(args[1]))
			default:
				return nil, crescent.NewArgError(2, "string expected, got "+st.TypeName(args[1]))
			}
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
			level = int(cCharCastInt32(f)) // luaL_optint narrowing
		}
	}
	// Official luaB_error's prefixing condition is lua_isstring (true for
	// numbers too, since lua_concat converts them to strings):
	// error(0) -> the string "file:line: 0".
	if level > 0 && value.IsNumber(v) {
		v = intern(st, crescent.FormatLuaNumber(value.AsNumber(v)))
	}
	e := crescent.NewErrorVal(v, valueToString(st, v))
	e.Level = level
	if level <= 0 || value.Tag(v) != value.TagString {
		// PUC prefixes only when level > 0, so NEGATIVE levels get no prefix
		// either -- error("b", -1) is a bare "b". Testing == 0 missed that, and
		// narrowing widened the reach: error("b", 2^31) narrows to INT32_MIN,
		// where PUC also drops the prefix.
		e.Level = 0
		e.MarkAnnotated()
	}
	return nil, e
}

func baseFnSelect(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewArgError(1, "number expected, got no value")
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
		return nil, crescent.NewArgError(1, "number expected, got "+st.TypeName(args[0]))
	}
	idx := int(cCharCastInt32(f)) // luaL_checkint narrowing
	n := len(args) - 1
	if idx < 0 {
		// Negative index: count from the tail (5.1); out of range errors
		idx = n + idx + 1
		if idx < 1 {
			return nil, crescent.NewArgError(1, "index out of range")
		}
	} else if idx == 0 {
		return nil, crescent.NewArgError(1, "index out of range")
	}
	if idx > n {
		return nil, nil
	}
	return args[idx:], nil
}

// baseFnUnpack: unpack(t [, i [, j]]) (implementation delegates to tablelib's baseFnUnpackImpl).
func baseFnUnpack(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	return baseFnUnpackImpl(st, args)
}

// ----- math sublibrary -----

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

// mathFn1 wraps a unary math function; the error wording matches the official
// luaL_checknumber (bad argument #1 to 'sin' (number expected, got string)).
func mathFn1(name string, f func(float64) float64) crescent.HostFn {
	return func(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
		if len(args) == 0 {
			return nil, crescent.NewArgError(1, "number expected, got no value")
		}
		x, ok := toNumberStr(st, args[0])
		if !ok {
			return nil, crescent.NewArgError(1, fmt.Sprintf("number expected, got %s", st.TypeName(args[0])))
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

// mathMinMax consolidates max/min: all arguments (including the first) are
// validated uniformly, with error wording matching the official
// luaL_checknumber (the first argument used to be silently swallowed:
// math.max("x", 2) wrongly returned 2).
func mathMinMax(st *crescent.State, args []value.Value, name string, better func(a, b float64) bool) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewArgError(1, "number expected, got no value")
	}
	out, ok := toNumberStr(st, args[0])
	if !ok {
		return nil, crescent.NewArgError(1, fmt.Sprintf("number expected, got %s", st.TypeName(args[0])))
	}
	for i, a := range args[1:] {
		f, ok := toNumberStr(st, a)
		if !ok {
			return nil, crescent.NewArgError(i+2, fmt.Sprintf("number expected, got %s", st.TypeName(a)))
		}
		if better(f, out) {
			out = f
		}
	}
	return []value.Value{value.NumberValue(out)}, nil
}

// ----- string sublibrary -----

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
		return nil, crescent.NewArgError(2, "number expected, got no value")
	}
	s := string(sb)
	startF, ok := toNumberStr(st, args[1])
	if !ok {
		return nil, crescent.NewArgError(2, "number expected, got "+st.TypeName(args[1]))
	}
	endF := float64(-1)
	// PUC str_sub: end is luaL_optinteger(L, 3, -1) -- an explicit nil
	// third argument counts as absent (default -1 = end of string).
	// Oracle diff fuzz caught rejecting sub(0, nil)-shaped calls where
	// the nil comes from an undefined global.
	if len(args) >= 3 && args[2] != value.Nil {
		f, ok := toNumberStr(st, args[2])
		if !ok {
			return nil, crescent.NewArgError(3, "number expected, got "+st.TypeName(args[2]))
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
		return nil, crescent.NewArgError(2, "number expected, got no value")
	}
	s := string(sb)
	nF, ok := toNumberStr(st, args[1])
	if !ok {
		return nil, crescent.NewArgError(2, "number expected, got "+st.TypeName(args[1]))
	}
	// Narrowed like any other luaL_checkint argument, positives included.
	//
	// Two earlier attempts got this wrong in opposite directions. Narrowing
	// nothing made string.rep("x", -(2^32-3)) yield "" where PUC yields "xxx";
	// narrowing only negatives made string.rep("x", 2^32+3) raise "string length
	// overflow" where PUC yields "xxx" (checked against lua5.1 on this host).
	//
	// The worry that narrowing would disarm the OOM guard below was unfounded:
	// after narrowing, n is at most 2^31-1, and a genuinely large request such as
	// rep(1000-byte string, 2147483647) still exceeds the 1 GiB threshold. What
	// narrowing removes is only the FALSE positives, where a huge raw count
	// narrows to something small that PUC happily produces.
	n := int(cCharCastInt32(nF))
	if n < 0 {
		n = 0
	}
	// Embedded hardening: block a script from triggering a host-process OOM
	// crash via string.rep. The 1 GiB threshold is a compromise between
	// "enough for real use" and "blocks hundreds of millions of variants";
	// it disagrees with PUC 5.1.5 / gopher-lua (neither defends against
	// direct OOM), but "the host process must not crash" has higher priority
	// than byte parity (12 §10 exemption). Can be caught by pcall.
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

// normIdx handles Lua-style negative indices (-1 = last position).
func normIdx(i, n int) int {
	if i < 0 {
		return n + i + 1
	}
	return i
}
