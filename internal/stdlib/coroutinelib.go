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
				return nil, wrapError(ist, e)
			}
			return nil, crescent.NewError("cannot resume coroutine")
		}
		return results, nil
	}
	fid := st.RegisterHostFn(wrapped)
	cl := st.MakeHostClosure(fid)
	return []value.Value{value.MakeGC(value.TagFunction, cl)}, nil
}

// wrapError rethrows a coroutine's error out of a coroutine.wrap function the way lbaselib.c's
// auxwrap does: a string error gets luaL_where(L, 1) prepended -- the position of whoever called
// the wrap function -- and anything else propagates untouched.
//
// The coroutine's own error is already final (it carries the position inside the coroutine), so
// returning it as-is dropped that outer prefix (#276). A fresh level-1 error with the same string
// gets it from the same machinery that prefixes a luaL_error: the interpreter's CALL site adds the
// caller's line, and a host caller such as pcall freezes it bare, which is what luaL_where gives
// for a C frame.
//
// Any other value is rethrown unchanged, but in a fresh error object too: the coroutine's one
// carries raise-point state about the coroutine's own stack (how many host frames sat above its
// innermost Lua frame, whether the dying coroutine's frames were already kept), which on the
// caller's thread would give an xpcall handler's traceback the wrong [C] lines and leave an outer
// coroutine dying of this error with an empty traceback.
func wrapError(st *crescent.State, e *crescent.LuaError) *crescent.LuaError {
	if e.HasValue && value.Tag(e.Value) != value.TagString && !value.IsNumber(e.Value) {
		ne := crescent.NewErrorVal(e.Value, e.Msg)
		ne.MarkAnnotated()
		return ne
	}
	msg := e.Msg
	if e.HasValue {
		msg = valueToString(st, e.Value)
	}
	return crescent.NewErrorVal(intern(st, msg), msg)
}

// coFnRunning: coroutine.running() → co | nil (main thread returns nil, 5.1 semantics).
func coFnRunning(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	id, ok := st.RunningCoID()
	if !ok {
		return []value.Value{value.Nil}, nil
	}
	return []value.Value{value.LightUDValue(id)}, nil
}
