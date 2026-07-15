// HostFn registration and modular exposure -- attaches Go functions into the
// Lua global/module tables so the Lua side can call them.
// Design form: the P1 version of the 11 §10 list style (not the §10.1 stack-machine style HostCtx).
package wangshu

import (
	"github.com/Liam0205/wangshu/internal/crescent"
	"github.com/Liam0205/wangshu/internal/value"
)

// HostFn is the signature of a Go function that, once registered into the Lua
// global/module table, is called from the Lua side (the P1 list-style version
// of 11 §10 -- not the §10.1 stack-machine style HostCtx, see godoc note).
//
// st is the State that fn belongs to (single-goroutine convention, see 11 §8).
// args are the actual arguments passed in from the Lua side, in the same order
// as the script call. The returned results are the multiple return values the
// Lua side receives; when an error is returned, the Lua side can catch it via
// pcall/xpcall (err.Error() → error message).
//
// Current scope: table/function/userdata in args are still mapped to Nil
// (fromInner tightens this; this iteration only exposes the GetGlobal/Call
// round trip for function, not the Lua function received by a host fn); scalars
// (nil/bool/number/string) round-trip normally.
type HostFn = func(*State, []Value) ([]Value, error)

// wrapHostFn wraps a public HostFn into an internal crescent.HostFn (signature forms aligned).
func (st *State) wrapHostFn(fn HostFn) crescent.HostFn {
	return func(_ *crescent.State, iargs []value.Value) ([]value.Value, *crescent.LuaError) {
		args := make([]Value, len(iargs))
		for i, a := range iargs {
			args[i] = fromInner(st, a)
		}
		results, err := fn(st, args)
		if err != nil {
			return nil, crescent.NewError(err.Error())
		}
		out := make([]value.Value, len(results))
		for i, v := range results {
			out[i] = v.toInner(st)
		}
		return out, nil
	}
}

// Register registers the Go function fn as a Lua-callable function under the
// global name name (mirrors gopher-lua `L.SetGlobal(name, L.NewFunction(fn))`
// and `L.Register`).
//
// Call form: `name(args...)` on the Lua side triggers fn(st, args); the return
// values are the multiple return values the Lua side receives; when err != nil
// the Lua side catches it via pcall/xpcall.
//
// Calling a host closure directly from the Go side (state.Call(fn, ...)) is not
// enabled in this iteration; it can only be called from within Lua.
func (st *State) Register(name string, fn HostFn) {
	id := st.core.RegisterHostFn(st.wrapHostFn(fn))
	cl := st.core.MakeHostClosure(id)
	st.core.SetGlobal(name, value.MakeGC(value.TagFunction, cl))
}

// RegisterModule registers a group of Go functions as a single global table
// name (a simplification of gopher-lua
// `L.SetGlobal(name, L.SetFuncs(L.NewTable(), funcs))`).
//
// Usage: `name.fname(args...)` on the Lua side triggers the corresponding fn;
// the table itself is only populated at the moment of creation, and the ability
// of the Lua side to dynamically write to / overwrite this table follows normal
// Lua table semantics (no read-only protection).
func (st *State) RegisterModule(name string, fns map[string]HostFn) {
	tbl := st.core.NewLibTable(uint32(len(fns)))
	for fname, fn := range fns {
		id := st.core.RegisterHostFn(st.wrapHostFn(fn))
		cl := st.core.MakeHostClosure(id)
		st.core.SetTableField(tbl, fname, value.MakeGC(value.TagFunction, cl))
	}
	st.core.SetGlobal(name, value.MakeGC(value.TagTable, tbl))
}
