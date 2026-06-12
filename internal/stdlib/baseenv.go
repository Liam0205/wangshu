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
	if len(args) >= 1 && value.Tag(args[0]) == value.TagString {
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
	case "stop", "restart", "setpause", "setstepmul":
		// STW GC 无增量调参;占位返回 0(可观察但不可逐字节比项,10 §13)
		return []value.Value{value.NumberValue(0)}, nil
	}
	return nil, crescent.NewError("bad argument #1 to 'collectgarbage' (invalid option '" + opt + "')")
}

// baseFnGcInfo:gcinfo() = collectgarbage("count") 的 5.1 遗留整数形态(10 §4.6)。
func baseFnGcInfo(st *crescent.State, _ []value.Value) ([]value.Value, *crescent.LuaError) {
	return []value.Value{value.NumberValue(float64(int64(st.GCCountKB())))}, nil
}

// baseFnLoadfile:loadfile([filename]) → function | (nil, errmsg)(10 §4.7)。
func baseFnLoadfile(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 || args[0] == value.Nil {
		return []value.Value{value.Nil, intern(st, "loadfile: reading stdin not supported")}, nil
	}
	if value.Tag(args[0]) != value.TagString {
		return nil, crescent.NewError("bad argument #1 to 'loadfile' (string expected)")
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
