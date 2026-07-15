// Environment/GC/loading functions of the base library (10 §4.6/§4.7 + §11 surface).
package stdlib

import (
	"os"

	"github.com/Liam0205/wangshu/internal/crescent"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// registerBaseEnv registers the _G/_VERSION globals and the GC control functions.
func registerBaseEnv(st *crescent.State) {
	// _G: the globals table itself (_G._G == _G, 01 §1)
	st.SetGlobal("_G", value.MakeGC(value.TagTable, st.Globals()))
	// _VERSION: always "Lua 5.1" (roadmap §6)
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

// baseFnCollectGarbage: collectgarbage([opt [, arg]]) (10 §4.6; 06 GC control).
//
// P1 support: collect (full GC) / count (KB) / stop / restart / setpause (no-op
// placeholder, STW GC has no incremental knobs) / step (= collect, STW has no
// stepping).
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
			return []value.Value{value.True}, nil // step completes one round → true (5.1)
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
		// STW GC has no incremental tuning; placeholder returns 0 (observable but not byte-comparable, 10 §13)
		return []value.Value{value.NumberValue(0)}, nil
	}
	return nil, crescent.NewArgError(1, "invalid option '"+opt+"'")
}

// baseFnGcInfo: gcinfo() = the 5.1 legacy integer form of collectgarbage("count") (10 §4.6).
func baseFnGcInfo(st *crescent.State, _ []value.Value) ([]value.Value, *crescent.LuaError) {
	return []value.Value{value.NumberValue(float64(int64(st.GCCountKB())))}, nil
}

// baseFnLoadfile: loadfile([filename]) → function | (nil, errmsg) (10 §4.7).
//
// Filesystem reads are off by default (when an embedded VM runs untrusted
// scripts, loadfile("/etc/passwd") is a privilege-probing surface; only the
// official standalone opens it by default). The host enables it explicitly via
// Options.AllowFileLoad; when disabled it returns the same (nil, errmsg) soft
// error shape as "file not found".
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

// baseFnDofile: dofile([filename]) = loadfile + immediate call (10 §4.7).
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
