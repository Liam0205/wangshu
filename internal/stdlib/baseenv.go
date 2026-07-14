// base 库的环境/GC/加载函数(10 §4.6/§4.7 + §11 提供面)。
package stdlib

import (
	"os"

	"github.com/Liam0205/wangshu/internal/crescent"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// registerBaseEnv 注册 _G/_VERSION 全局变量与 GC 控制函数。
func registerBaseEnv(st *crescent.State) {
	// _G:globals 表自身(_G._G == _G,01 §1)
	st.SetGlobal("_G", value.MakeGC(value.TagTable, st.Globals()))
	// _VERSION:恒 "Lua 5.1"(roadmap §6)
	st.SetGlobal("_VERSION", intern(st, "Lua 5.1"))
	for _, e := range []entry{
		{"collectgarbage", baseFnCollectGarbage},
		{"gcinfo", baseFnGcInfo},
		{"loadfile", baseFnLoadfile},
		{"dofile", baseFnDofile},
	} {
		id := st.RegisterHostFn(e.fn)
		cl := st.MakeHostClosure(id)
		st.SetGlobal(e.name, value.MakeGC(value.TagFunction, cl))
	}
}

// baseFnCollectGarbage:collectgarbage([opt [, arg]])(10 §4.6;06 GC 控制)。
//
// P1 支持:collect(full GC)/count(KB)/stop/restart/setpause(空操作占位,
// STW GC 无增量参数)/step(= collect,STW 无步进)。
func baseFnCollectGarbage(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	opt := "collect"
	if len(args) >= 1 && args[0] != value.Nil {
		// PUC luaL_checkoption: a present non-nil option must be a
		// string; numbers do NOT coerce here (luaL_checklstring is
		// only reached via checkoption's string requirement, and
		// collectgarbage(0) raises "invalid option '0'" -- the number
		// stringifies for the message only). Simplest faithful shape:
		// non-string raises "string expected", unknown string raises
		// "invalid option".
		if value.Tag(args[0]) != value.TagString {
			if value.IsNumber(args[0]) {
				return nil, crescent.NewArgError(1, "invalid option '"+
					crescent.FormatLuaNumber(value.AsNumber(args[0]))+"'")
			}
			return nil, crescent.NewArgError(1, "string expected, got "+st.TypeName(args[0]))
		}
		opt = string(object.StringBytes(st.Arena(), value.GCRefOf(args[0])))
	}
	switch opt {
	case "collect", "step":
		st.GCCollect()
		if opt == "step" {
			return []value.Value{value.True}, nil // step 完成一轮 → true(5.1)
		}
		return []value.Value{value.NumberValue(0)}, nil
	case "count":
		return []value.Value{value.NumberValue(st.GCCountKB())}, nil
	case "stop":
		st.GCSetStopped(true)
		return []value.Value{value.NumberValue(0)}, nil
	case "restart":
		st.GCSetStopped(false)
		return []value.Value{value.NumberValue(0)}, nil
	case "setpause", "setstepmul":
		// STW GC 无增量调参;占位返回 0(可观察但不可逐字节比项,10 §13)
		return []value.Value{value.NumberValue(0)}, nil
	}
	return nil, crescent.NewArgError(1, "invalid option '"+opt+"'")
}

// baseFnGcInfo:gcinfo() = collectgarbage("count") 的 5.1 遗留整数形态(10 §4.6)。
func baseFnGcInfo(st *crescent.State, _ []value.Value) ([]value.Value, *crescent.LuaError) {
	return []value.Value{value.NumberValue(float64(int64(st.GCCountKB())))}, nil
}

// baseFnLoadfile:loadfile([filename]) → function | (nil, errmsg)(10 §4.7)。
//
// 文件系统读默认关闭(嵌入式 VM 接不可信脚本时,loadfile("/etc/passwd")
// 是越权探测面;官方 standalone 才默认开放)。宿主经 Options.AllowFileLoad
// 显式开启;关闭时与"文件不存在"同形态返回 (nil, errmsg) 软错误。
func baseFnLoadfile(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	// Argument type check FIRST (PUC luaL_optstring): loadfile(true)
	// raises even before the sandbox gate -- keeping the acceptance
	// surface byte-equal to official 5.1.5 whether or not file
	// loading is enabled (oracle diff arg sweep catch). Numbers
	// coerce like every luaL_*string reader.
	if len(args) >= 1 && args[0] != value.Nil &&
		value.Tag(args[0]) != value.TagString && !value.IsNumber(args[0]) {
		return nil, crescent.NewArgError(1, "string expected, got "+st.TypeName(args[0]))
	}
	if !st.AllowFileLoad() {
		return []value.Value{value.Nil, intern(st, "loadfile disabled (enable with Options.AllowFileLoad)")}, nil
	}
	if len(args) == 0 || args[0] == value.Nil {
		return []value.Value{value.Nil, intern(st, "loadfile: reading stdin not supported")}, nil
	}
	if value.Tag(args[0]) != value.TagString {
		return nil, crescent.NewArgError(1, "string expected")
	}
	path := string(object.StringBytes(st.Arena(), value.GCRefOf(args[0])))
	src, err := os.ReadFile(path)
	if err != nil {
		return []value.Value{value.Nil, intern(st, "cannot open "+path)}, nil
	}
	fn, cerr := st.CompileAndLoad(src, "@"+path)
	if cerr != nil {
		return []value.Value{value.Nil, intern(st, cerr.Error())}, nil
	}
	return []value.Value{fn}, nil
}

// baseFnDofile:dofile([filename]) = loadfile + 立即调用(10 §4.7)。
func baseFnDofile(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	r, e := baseFnLoadfile(st, args)
	if e != nil {
		return nil, e
	}
	if len(r) == 0 || value.Tag(r[0]) != value.TagFunction {
		msg := "dofile: load failed"
		if len(r) >= 2 && value.Tag(r[1]) == value.TagString {
			msg = string(object.StringBytes(st.Arena(), value.GCRefOf(r[1])))
		}
		return nil, crescent.NewError(msg)
	}
	return st.ProtectedCallDirect(r[0], nil)
}
