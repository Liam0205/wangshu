// coroutine sublibrary (08 §4).
package stdlib

import (
	"github.com/Liam0205/wangshu/internal/crescent"
	"github.com/Liam0205/wangshu/internal/value"
)

var coroutineFns = []entry{
	{"create", coFnCreate},
	{"resume", coFnResume},
	{"yield", coFnYield},
	{"status", coFnStatus},
	{"wrap", coFnWrap},
	{"running", coFnRunning},
}

// coFnCreate: coroutine.create(f) → thread handle (lightuserdata).
func coFnCreate(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewArgError(1, "Lua function expected")
	}
	id, e := st.NewCoroutine(args[0])
	if e != nil {
		return nil, e
	}
	return []value.Value{value.LightUDValue(id)}, nil
}

// coFnResume: coroutine.resume(co, ...) → (true, ...) | (false, errmsg).
func coFnResume(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 || !st.IsCoroutineHandle(args[0]) {
		return nil, crescent.NewArgError(1, "coroutine expected")
	}
	id := value.AsLightUD(args[0])
	results, ok, e := st.Resume(id, args[1:])
	if !ok {
		errVal := value.Nil
		if e != nil {
			errVal = e.Value
			if !e.HasValue {
				errVal = intern(st, e.Msg)
			}
		}
		return []value.Value{value.False, errVal}, nil
	}
	out := make([]value.Value, 0, len(results)+1)
	out = append(out, value.True)
	out = append(out, results...)
	return out, nil
}

// coFnYield: coroutine.yield(...).
//
// Returns a sentinel error to trigger the execute bubble-up (08 §3.4 yield↔error
// symmetric channel); the resume side takes args as resume's return values.
func coFnYield(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	return nil, st.Yield(args)
}

// coFnStatus: coroutine.status(co).
func coFnStatus(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 || !st.IsCoroutineHandle(args[0]) {
		return nil, crescent.NewArgError(1, "coroutine expected")
	}
	return []value.Value{intern(st, st.CoStatusOf(value.AsLightUD(args[0])))}, nil
}

// coFnWrap: coroutine.wrap(f) → function; calling it = resume, errors are rethrown directly.
func coFnWrap(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) == 0 {
		return nil, crescent.NewArgError(1, "Lua function expected")
	}
	id, e := st.NewCoroutine(args[0])
	if e != nil {
		return nil, e
	}
	wrapped := func(ist *crescent.State, wargs []value.Value) ([]value.Value, *crescent.LuaError) {
		results, ok, e := ist.Resume(id, wargs)
		if !ok {
			if e != nil {
				return nil, e
			}
			return nil, crescent.NewError("cannot resume coroutine")
		}
		return results, nil
	}
	fid := st.RegisterHostFn(wrapped)
	cl := st.MakeHostClosure(fid)
	return []value.Value{value.MakeGC(value.TagFunction, cl)}, nil
}

// coFnRunning: coroutine.running() → co | nil (main thread returns nil, 5.1 semantics).
func coFnRunning(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	id, ok := st.RunningCoID()
	if !ok {
		return []value.Value{value.Nil}, nil
	}
	return []value.Value{value.LightUDValue(id)}, nil
}
