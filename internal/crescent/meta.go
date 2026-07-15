// Metatable support — __index / __newindex chain + arithmetic metamethods (07's minimal M11 set).
//
// The metatable is stored/loaded via object.TableMetaRef (arena-native Table layout word4);
// once the full arena hash is wired up, migrate back to object.TableMetaRef.
package crescent

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// metaOf returns t's metatable GCRef (0 if none).
func (st *State) metaOf(t arena.GCRef) arena.GCRef {
	return object.TableMetaRef(st.arena, t)
}

// SetMeta sets t's metatable (0 = clear). object.SetTableMeta calls BumpGen internally.
//
// It also resolves metatable.__mode in sync, writing the weak-table cache bits (07 §13:
// the GC does not parse strings during the mark phase; setmetatable is the sole write entry).
func (st *State) SetMeta(t, meta arena.GCRef) {
	object.SetTableMeta(st.arena, t, meta)
	weakKey, weakVal := false, false
	if meta != 0 {
		mode := st.metaField(t, "__mode")
		if value.Tag(mode) == value.TagString {
			for _, c := range object.StringBytes(st.arena, value.GCRefOf(mode)) {
				if c == 'k' {
					weakKey = true
				}
				if c == 'v' {
					weakVal = true
				}
			}
		}
	}
	object.SetTableWeakFlags(st.arena, t, weakKey, weakVal)
}

// metaField looks up t's metatable[name]; returns Nil if there is no metatable or no such field.
func (st *State) metaField(t arena.GCRef, name string) value.Value {
	mt := st.metaOf(t)
	if mt == 0 {
		return value.Nil
	}
	key := value.MakeGC(value.TagString, st.gc.Intern([]byte(name)))
	v, _ := st.tableGet(mt, key)
	return v
}

// metaFieldOfValue looks up a metamethod on an arbitrary Value: a table looks up its own
// metatable, a string looks up the shared string metatable (mirroring PUC's per-type
// metatable — __add/__concat/__lt/__call/__tostring etc. all take effect for strings
// through this entry, not just __index).
func (st *State) metaFieldOfValue(v value.Value, name string) value.Value {
	if value.Tag(v) == value.TagTable {
		return st.metaField(value.GCRefOf(v), name)
	}
	if value.Tag(v) == value.TagString && st.stringMeta != 0 {
		key := value.MakeGC(value.TagString, st.gc.Intern([]byte(name)))
		h, _ := st.tableGet(st.stringMeta, key)
		return h
	}
	return value.Nil
}

// indexWithMeta implements the full GETTABLE semantics: raw get → __index chain (07 §3).
//
// The chain is capped at 100 levels (to guard against __index cycles). For a string value,
// __index = the string library table (07 §1.2).
func (st *State) indexWithMeta(th *thread, obj, key value.Value) (value.Value, *LuaError) {
	for depth := 0; depth < 100; depth++ {
		if value.Tag(obj) == value.TagTable {
			tref := value.GCRefOf(obj)
			v, e := st.tableGet(tref, key)
			if e != nil {
				return value.Nil, e
			}
			if v != value.Nil {
				return v, nil
			}
			h := st.metaField(tref, "__index")
			if h == value.Nil {
				return value.Nil, nil // raw miss, no __index
			}
			if value.Tag(h) == value.TagFunction {
				return st.callMetaHandler(th, h, []value.Value{obj, key}, 1)
			}
			obj = h // __index is a table: keep looking up along the chain
			continue
		}
		if value.Tag(obj) == value.TagString {
			// string per-type metatable: PUC reads __index from the
			// shared string metatable LIVE (script mutation of
			// getmetatable("").__index takes effect). Falls back to
			// the stringLib shortcut when no metatable is registered.
			if st.stringMeta != 0 {
				h := st.metaFieldOfValue(obj, "__index")
				if h == value.Nil {
					return value.Nil, nil
				}
				if value.Tag(h) == value.TagFunction {
					return st.callMetaHandler(th, h, []value.Value{obj, key}, 1)
				}
				obj = h
				continue
			}
			if st.stringLib != 0 {
				obj = value.MakeGC(value.TagTable, st.stringLib)
				continue
			}
		}
		return value.Nil, errf("attempt to index a %s value", st.typeNameOf(obj))
	}
	return value.Nil, errf("'__index' chain too long; possible loop")
}

// setIndexWithMeta implements the full SETTABLE semantics: raw set → __newindex chain (07 §4).
func (st *State) setIndexWithMeta(th *thread, obj, key, val value.Value) *LuaError {
	for depth := 0; depth < 100; depth++ {
		if value.Tag(obj) == value.TagTable {
			tref := value.GCRefOf(obj)
			v, e := st.tableGet(tref, key)
			if e != nil {
				return e
			}
			if v != value.Nil {
				// existing key: raw set directly, does not trigger __newindex
				return st.tableSet(tref, key, val)
			}
			h := st.metaField(tref, "__newindex")
			if h == value.Nil {
				return st.tableSet(tref, key, val)
			}
			if value.Tag(h) == value.TagFunction {
				_, e := st.callMetaHandler(th, h, []value.Value{obj, key, val}, 0)
				return e
			}
			obj = h
			continue
		}
		// Non-table: PUC luaV_settable consults the per-type metatable
		// for TM_NEWINDEX before erroring -- a __newindex installed on
		// getmetatable("") intercepts every string write (PR #128
		// review round 2 blocking item; metaFieldOfValue covers the
		// string metatable).
		h := st.metaFieldOfValue(obj, "__newindex")
		if h == value.Nil {
			return errf("attempt to index a %s value", st.typeNameOf(obj))
		}
		if value.Tag(h) == value.TagFunction {
			_, e := st.callMetaHandler(th, h, []value.Value{obj, key, val}, 0)
			return e
		}
		obj = h
	}
	return errf("'__newindex' chain too long; possible loop")
}

// arithMeta is the arithmetic slow path: called when either b or c carries an __add etc.
// metamethod (07 §5).
//
// name is of the form "__add". Returns the result value.
func (st *State) arithMeta(th *thread, name string, b, c value.Value) (value.Value, *LuaError) {
	h := st.metaFieldOfValue(b, name)
	if h == value.Nil {
		h = st.metaFieldOfValue(c, name)
	}
	if h == value.Nil {
		bad := b
		if value.IsNumber(b) {
			bad = c
		}
		return value.Nil, errf("attempt to perform arithmetic on a %s value", st.typeNameOf(bad))
	}
	if value.Tag(h) != value.TagFunction {
		return value.Nil, errf("attempt to call a %s value", st.typeNameOf(h))
	}
	return st.callMetaHandler(th, h, []value.Value{b, c}, 1)
}

// callMetaHandler calls a metamethod handler (Lua or host), taking nWant return values
// (nWant=1 takes the first value; 0 takes none).
//
// A Lua handler goes through host→Lua reentry (05 §7.3: a new execute layer, Go stack +1).
func (st *State) callMetaHandler(th *thread, fn value.Value, args []value.Value, nWant int) (value.Value, *LuaError) {
	results, e := st.callLuaFromHost(th, fn, args)
	if e != nil {
		return value.Nil, e
	}
	if nWant == 0 || len(results) == 0 {
		return value.Nil, nil
	}
	return results[0], nil
}

// callLuaFromHost initiates a Lua/host call from a host context (05 §7.3).
//
// It pushes fn+args onto the stack top, then starts a new execute layer after
// enterLuaFrame(entry=true). This is the "Go stack +1" host→Lua reentry boundary.
// A non-function is forwarded through __call (07).
//
// Arg-error naming (issue #133): errors leaving this boundary get
// argNarg finalized to 0, freezing the C-caller fallback '?'. PUC's
// getfuncname requires the CALLING frame to be Lua — pcall(f, ...),
// sort comparators and metamethod handlers are C callers, so their
// callees' arg errors keep '?' even if a host fn propagates them up
// to an outer Lua CALL site. The TFORLOOP interpreter/gibbous sites
// are Lua callers (PUC names OP_TFORLOOP call sites) and use
// callLuaFromHostNamed instead.
func (st *State) callLuaFromHost(th *thread, fn value.Value, args []value.Value) ([]value.Value, *LuaError) {
	out, e := st.callLuaFromHostNamed(th, fn, args)
	if e != nil {
		e.argNarg = 0
	}
	return out, e
}

// callLuaFromHostNamed is callLuaFromHost without the argNarg
// finalization — for call boundaries that PUC's getfuncname treats as
// named Lua call sites (TFORLOOP), where the caller resolves the arg
// error itself via resolveArgError.
func (st *State) callLuaFromHostNamed(th *thread, fn value.Value, args []value.Value) ([]value.Value, *LuaError) {
	// host→Lua reentry depth cap (05 §7.4): guards against "Lua calls host calls Lua …"
	// alternation actually blowing the Go stack (a Go maxstacksize fatal is unrecoverable, so
	// it must be intercepted first with a recoverable error).
	if st.nCcalls >= maxCCallDepth {
		return nil, errf("C stack overflow")
	}
	st.nCcalls++
	defer func() { st.nCcalls-- }()
	if value.Tag(fn) != value.TagFunction {
		h := st.metaFieldOfValue(fn, "__call")
		if value.Tag(h) != value.TagFunction {
			return nil, errf("attempt to call a %s value", st.typeNameOf(fn))
		}
		args = append([]value.Value{fn}, args...)
		fn = h
	}
	cl := value.GCRefOf(fn)
	funcIdx := th.top
	need := funcIdx + 1 + len(args)
	th.ensureStack(need)
	th.setSlot(funcIdx, fn)
	for i, a := range args {
		th.setSlot(funcIdx+1+i, a)
	}
	th.setTop(need)
	if isHost := st.isHostClosure(cl); isHost {
		if e := st.callHost(th, funcIdx, len(args), -1); e != nil {
			return nil, e
		}
		n := th.top - funcIdx
		out := make([]value.Value, n)
		th.copyOut(out, funcIdx, th.top)
		th.setTop(funcIdx)
		return out, nil
	}
	savedDepth := th.ciDepth
	if e := st.enterLuaFrame(th, funcIdx, len(args), -1, true); e != nil {
		return nil, e
	}
	if e := st.execute(th); e != nil {
		// A yield sentinel reaching the host→Lua reentry boundary = a yield across a
		// pcall/metamethod/host callback. 5.1 does not support this (that's 5.2's
		// lua_yieldk business); the reference reports this error and the coroutine does
		// not suspend. If not intercepted, the sentinel gets caught by pcall as an
		// ordinary error and the internal string "<yield>" leaks to the script.
		if e == errYieldSentinel {
			th.truncateCI(savedDepth)
			th.setTop(funcIdx)
			// clear the resume info and value-transfer area registered by doCall/Yield during bubbling
			th.pendingResume = nil
			if co := st.findRunningCo(); co != nil {
				co.xfer = nil
			}
			return nil, errf("attempt to yield across metamethod/C-call boundary")
		}
		// failure: roll back CallInfo to before entry (05 §9.3 protected-boundary cleanup duty)
		th.truncateCI(savedDepth)
		th.setTop(funcIdx)
		return nil, e
	}
	// after execute returns, the return values start at funcIdx (doReturn dst=funcIdx); top is already set
	n := th.top - funcIdx
	if n < 0 {
		n = 0
	}
	out := make([]value.Value, n)
	th.copyOut(out, funcIdx, funcIdx+n)
	th.setTop(funcIdx)
	return out, nil
}

func (st *State) isHostClosure(cl arena.GCRef) bool {
	return object.IsHostClosure(st.arena, cl)
}

// ProtectedCall is the core of pcall's implementation (05 §9.3): calls fn inside a protected boundary.
//
// Errors are caught and returned (*LuaError non-nil); the CallInfo rollback is already handled by callLuaFromHost.
func (st *State) ProtectedCall(fn value.Value, args []value.Value) ([]value.Value, *LuaError) {
	th := st.runningThread
	if th == nil {
		return nil, errf("pcall: no running thread")
	}
	return st.callLuaFromHost(th, fn, args)
}

// ProtectedCallDirect is isomorphic to ProtectedCall (for stdlib internals that call back into
// Lua functions, such as gsub's function repl and table.sort's comparator).
func (st *State) ProtectedCallDirect(fn value.Value, args []value.Value) ([]value.Value, *LuaError) {
	return st.ProtectedCall(fn, args)
}

// MetaOf exposes metaOf (used by stdlib getmetatable).
func (st *State) MetaOf(t arena.GCRef) arena.GCRef { return st.metaOf(t) }

// IndexWithMeta exposes the table read with the __index chain (used by stdlib gsub's table repl —
// PUC's gsub fetches the replacement value via lua_gettable, which triggers metamethods).
func (st *State) IndexWithMeta(obj, key value.Value) (value.Value, *LuaError) {
	th := st.runningThread
	if th == nil {
		return value.Nil, errf("IndexWithMeta: no running thread")
	}
	return st.indexWithMeta(th, obj, key)
}

// LessThan exposes the full `<` semantics (number/string fast path + __lt metamethod;
// used by table.sort's default comparator — PUC's sort_comp goes through lua_lessthan).
func (st *State) LessThan(a, b value.Value) (bool, *LuaError) {
	if value.IsNumber(a) && value.IsNumber(b) {
		return value.AsNumber(a) < value.AsNumber(b), nil
	}
	if value.Tag(a) == value.TagString && value.Tag(b) == value.TagString {
		return stringCompare(st, value.GCRefOf(a), value.GCRefOf(b)) < 0, nil
	}
	th := st.runningThread
	if th == nil {
		return false, errf("LessThan: no running thread")
	}
	h := st.metaFieldOfValue(a, "__lt")
	if h == value.Nil {
		h = st.metaFieldOfValue(b, "__lt")
	}
	if value.Tag(h) == value.TagFunction {
		res, e := st.callMetaHandler(th, h, []value.Value{a, b}, 1)
		if e != nil {
			return false, e
		}
		return value.Truthy(res), nil
	}
	return false, errf("attempt to compare two %s values", st.typeNameOf(a))
}

// MetaFieldOf exposes metamethod lookup for an arbitrary Value (used by stdlib __tostring etc.).
func (st *State) MetaFieldOf(v value.Value, name string) value.Value {
	return st.metaFieldOfValue(v, name)
}

// RawGet / RawSet expose raw table access (used by stdlib rawget/rawset).
func (st *State) RawGet(t arena.GCRef, key value.Value) (value.Value, *LuaError) {
	return st.tableGet(t, key)
}

func (st *State) RawSet(t arena.GCRef, key, val value.Value) *LuaError {
	return st.tableSet(t, key, val)
}

// RawNext exposes iteration (used by stdlib next/pairs).
func (st *State) RawNext(t arena.GCRef, key value.Value) (value.Value, value.Value, bool, *LuaError) {
	return st.rawNext(t, key)
}

// RawBorder exposes #t (used by stdlib table.*).
func (st *State) RawBorder(t arena.GCRef) uint32 { return st.rawBorder(t) }
