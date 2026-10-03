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
	// Userdata carries its own metatable, like a table. Without this a userdata's
	// __index is never consulted, so io.stdout:write(...) fails to resolve.
	if value.Tag(v) == value.TagUserdata {
		if mt := object.UserdataMetaRef(st.arena, value.GCRefOf(v)); mt != 0 {
			key := value.MakeGC(value.TagString, st.gc.Intern([]byte(name)))
			h, _ := st.tableGet(mt, key)
			return h
		}
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
		if value.Tag(obj) == value.TagUserdata {
			// Userdata has no raw fields of its own, so indexing goes straight to
			// __index -- this is the path io.stdout:write(...) takes. PUC raises
			// "attempt to index a userdata value" when there is no metatable.
			h := st.metaFieldOfValue(obj, "__index")
			if h == value.Nil {
				return value.Nil, errf("attempt to index a userdata value")
			}
			if value.Tag(h) == value.TagFunction {
				return st.callMetaHandler(th, h, []value.Value{obj, key}, 1)
			}
			obj = h
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
	// luaV_gettable after MAXTAGLOOP (100) steps; the "'__index' chain too long" wording is 5.2's (#287).
	return value.Nil, errf("loop in gettable")
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
			// luaV_settable always does the primitive luaH_set first, so a nil or NaN key fails here,
			// in the frame doing the assignment, before __newindex is even looked up: with
			// `__newindex = rawset` the error carries the SETTABLE line, and a handler that would
			// swallow the write never runs.
			if e := checkKey(key); e != nil {
				return e
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
	// luaV_settable after MAXTAGLOOP (100) steps (#287).
	return errf("loop in settable")
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
	// Metamethod dispatch adds NO level to error()'s frame walk.
	//
	// wangshu runs a Lua handler through the same host->Lua reentry as a host
	// function call, but PUC interposes no C frame for a metamethod: the handler's
	// caller is the Lua function that triggered it, which debug.getinfo confirms.
	// Counting the reentry made every error(msg, level>=2) inside __index, __add,
	// __concat, __eq, __lt, __unm, __newindex and the for-in iterator name a frame
	// one step too shallow.
	// Undo exactly the ONE level callLuaFromHost adds, and only for this dispatch.
	//
	// PUC interposes no C frame for a metamethod, so the dispatch itself must add
	// nothing -- but calls made INSIDE the handler (a pcall, a sort comparator) do
	// have real C frames and must still count. A State-wide suppression flag held
	// for the whole handler body swallowed those too; decrementing right after the
	// wrapper's increment targets only the dispatch.
	saved := st.pendingHostFrames
	results, e := st.callLuaFromHostNoLevel(th, fn, args)
	st.pendingHostFrames = saved
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
// callLuaFromHostNoLevel is callLuaFromHost without the error-level host frame, for
// metamethod dispatch: PUC interposes no C frame there, while calls made INSIDE the
// handler still get theirs from callLuaFromHost.
func (st *State) callLuaFromHostNoLevel(th *thread, fn value.Value, args []value.Value) ([]value.Value, *LuaError) {
	out, e := st.callLuaFromHostNamed(th, fn, args)
	if e != nil && e != errYieldSentinel { // the sentinel is shared by every State: never write it
		e.argNarg = 0
	}
	return out, e
}

func (st *State) callLuaFromHost(th *thread, fn value.Value, args []value.Value) ([]value.Value, *LuaError) {
	// The host-frame count for error()'s level walk lives HERE, not in the Named
	// variant, and the split matters. This wrapper is the ordinary host call
	// boundary (pcall, sort's comparator, gsub's replacement) and PUC has a real C
	// frame for each; the Named variant is TFORLOOP, where PUC dispatches the
	// iterator with no interposed frame. Counting in the shared callee gave the
	// iterator a level it should not have; counting in neither left pcall inside a
	// metamethod handler short by one.
	// Save and restore HERE, around the increment. The inner
	// callLuaFromHostNamed's save/restore reads the counter after this increment,
	// so its defer restored the incremented value and every host boundary leaked a
	// permanent +1 -- the first error(msg, level>=2) was right and each later one
	// walked a step deeper. Repeating the same protected call is what exposes it,
	// which no single-shot probe does.
	outer := st.pendingHostFrames
	defer func() { st.pendingHostFrames = outer }()
	st.pendingHostFrames++
	out, e := st.callLuaFromHostNamed(th, fn, args)
	if e != nil && e != errYieldSentinel { // the sentinel is shared by every State: never write it
		e.argNarg = 0
		// Every caller of this wrapper is a host function (pcall, sort's comparator, gsub's
		// replacement, foreach), so whatever escapes has its final text. A Lua callee's error
		// was annotated in its own execute layer; a host callee's is a luaL_error-style message
		// whose luaL_where(L, 1) is the calling host function -- a C frame, no position. Left
		// unfrozen, the error crossed that host function and the interpreter's CALL site
		// prefixed the Lua line: gsub("a", "a", error) reported "x:N: a" where lua5.1 says "a"
		// (#278). Not done in callLuaFromHostNamed: there the caller is TFORLOOP, a Lua frame,
		// which PUC does name.
		e.MarkAnnotated()
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
	if e := st.cCallCheck(); e != nil {
		return nil, e
	}
	st.nCcalls++
	// One more host frame stands between the caller and the Lua function about to
	// run. It is consumed by the next Lua frame push, so consecutive host entries
	// with no Lua frame in between accumulate: pcall(pcall, f) leaves 2.
	// This is the TFORLOOP entry point, and it adds NO level: PUC dispatches the
	// for-in iterator without interposing a C frame, exactly like a metamethod, so
	// counting it shifted every iterator's levels >= 2 by one.
	//
	// The counter is SAVED and RESTORED rather than decremented. Decrementing
	// underflowed to 255 whenever a frame push had already consumed it, and the
	// next frame then saturated to 15 phantom levels -- any pcall/sort/gsub inside
	// a handler reached that.
	outer := st.pendingHostFrames
	defer func() {
		st.nCcalls--
		st.pendingHostFrames = outer
	}()
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
			// A host function raising goes straight back out without re-entering
			// the interpreter loop, so execute.go's annotateError never sees it.
			// pcall(error,"m",2) therefore lost its position prefix entirely, where
			// PUC names pcall's caller. Annotate here instead; annotateError is
			// idempotent, so an error that does reach the loop later is unaffected.
			// Only an error() with an explicit level >= 2 needs annotating here.
			//
			// Annotating every host error was wrong: PUC's argument and library
			// errors from a host callee (luaL_error, luaL_argerror) carry no
			// position when raised through pcall -- table.concat({1},",",0) reports a
			// bare "invalid value (nil) at index 0 ...". Those already have their
			// final text, so leave them alone.
			if e.Level >= 2 {
				e.hostRaised = true
				e = st.annotateError(e, currentCI(th), th)
			}
			// Above the innermost Lua frame sit the host callee and the host functions that
			// led to it (pendingHostFrames): sort calling error is "[C]: ?" over "[C]: in
			// function 'sort'". With a host caller (pendingHostFrames > 0) the message is final
			// now -- the wrapper only freezes it -- so this is the raise point. With a Lua
			// caller (TFORLOOP, the VM's own metamethod dispatch) that frame still has to name
			// and position it, so the raise point is processed there.
			markHostRaise(e, int(st.pendingHostFrames)+1)
			if st.pendingHostFrames > 0 {
				st.atRaisePoint(th, e, 0)
			}
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
		// luaD_pcall closes the frames' open upvalues before it unwinds them (luaF_close(L, oldtop)),
		// so a closure made inside keeps the value its variable had when the error was raised.
		// Unwinding without closing left the upvalues pointing at stack slots that the next call
		// reuses, or that the collector clears once they are above top (#285).
		st.closeUpvals(th, funcIdx)
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
// Only pcall/xpcall use it: it marks the call as protected, so an error inside does not pay for a
// traceback nobody will see.
func (st *State) ProtectedCall(fn value.Value, args []value.Value) ([]value.Value, *LuaError) {
	th := st.runningThread
	if th == nil {
		return nil, errf("pcall: no running thread")
	}
	st.protectDepth++
	st.catchDepth++
	outerErrFunc := st.errFunc
	st.errFunc = value.Nil // lua_pcall(..., 0): no message handler inside
	defer func() { st.protectDepth--; st.catchDepth--; st.errFunc = outerErrFunc }()
	return st.callLuaFromHost(th, fn, args)
}

// RaiseCaughtInHost is a luaL_error raised by the running host function and caught by that same
// function without changing the message handler (lua_load's reader errors): it gets
// luaL_where(L, 1)'s position -- the caller's line when the caller is Lua -- and its raise-point
// processing, then comes back to the host function instead of propagating.
func (st *State) RaiseCaughtInHost(e *LuaError) *LuaError {
	th := st.runningThread
	if th == nil || th.ciDepth == 0 {
		return e
	}
	if st.pendingHostFrames == 0 {
		e = st.annotateError(e, currentCI(th), th)
	} else {
		e.MarkAnnotated()
	}
	markHostRaise(e, int(st.pendingHostFrames)+1)
	st.catchDepth++
	st.atRaisePoint(th, e, 0)
	st.catchDepth--
	return e
}

// ProtectedCallKeepingHandler catches like ProtectedCall but leaves the message handler alone, as
// luaD_protectedparser does for load's reader: an error the reader raises still runs the
// enclosing xpcall's handler (whose result becomes the caught value), and at top level still gets
// the uncaught-error traceback lua.c's handler would add. Only the coroutine-death bookkeeping
// sees it as caught.
func (st *State) ProtectedCallKeepingHandler(fn value.Value, args []value.Value) ([]value.Value, *LuaError) {
	th := st.runningThread
	if th == nil {
		return nil, errf("pcall: no running thread")
	}
	st.catchDepth++
	defer func() { st.catchDepth-- }()
	return st.callLuaFromHost(th, fn, args)
}

// ProtectedCallWithHandler is xpcall's protected call: handler becomes the message handler for every
// error raised inside, run at the raise point (see atRaisePoint). A caught error then carries the
// handler's result, read with LuaError.ErrFuncResult.
func (st *State) ProtectedCallWithHandler(fn value.Value, args []value.Value, handler value.Value) ([]value.Value, *LuaError) {
	th := st.runningThread
	if th == nil {
		return nil, errf("pcall: no running thread")
	}
	st.protectDepth++
	st.catchDepth++
	outerErrFunc, outerResult := st.errFunc, st.errFuncResult
	st.errFunc = handler
	// Restoring errFuncResult drops the root on the handler's result once xpcall has it: from here
	// it is a host function's return value like any other, on its way to the Lua stack.
	defer func() {
		st.protectDepth--
		st.catchDepth--
		st.errFunc, st.errFuncResult = outerErrFunc, outerResult
	}()
	results, e := st.callLuaFromHost(th, fn, args)
	if e != nil && e != errYieldSentinel && !e.handled {
		// An error that never reached a raise point with the handler installed (one raised while
		// entering a frame, say) still gets the handler, on whatever stack is left.
		st.runErrFunc(th, e, int(st.pendingHostFrames)+1)
	}
	return results, e
}

// ProtectedCallDirect calls back into Lua from a stdlib host function (gsub's function repl,
// table.sort's comparator, foreach's callback). It is the same host->Lua boundary as ProtectedCall
// but does NOT catch: the host function propagates the error, so an uncaught one must still get
// its traceback at the raise point.
func (st *State) ProtectedCallDirect(fn value.Value, args []value.Value) ([]value.Value, *LuaError) {
	th := st.runningThread
	if th == nil {
		return nil, errf("pcall: no running thread")
	}
	return st.callLuaFromHost(th, fn, args)
}

// HostCallCheck is the C-depth check of a call a host function makes (luaD_call's ++nCcalls), for
// host code that computes a call's result itself instead of making it: print formats a value the
// builtin tostring would only format, but in lua5.1 that is still a lua_call, which raises
// "C stack overflow" at the limit. The error is the one ProtectedCallDirect would return.
func (st *State) HostCallCheck() *LuaError {
	e := st.cCallCheck()
	if e != nil {
		e.MarkAnnotated()
	}
	return e
}

// MetaOf exposes metaOf (used by stdlib getmetatable).
func (st *State) MetaOf(t arena.GCRef) arena.GCRef { return st.metaOf(t) }

// IndexWithMeta exposes the table read with the __index chain (used by stdlib gsub's table repl —
// PUC's gsub fetches the replacement value via lua_gettable, which triggers metamethods).
//
// The caller is a host function, so an __index handler reached from here runs above a real C
// frame: PUC's stack is [handler, gsub(C), caller]. callMetaHandler deliberately adds no level
// (the VM's own metamethod dispatch interposes no C frame), so the host frame is counted here,
// exactly as callLuaFromHost counts it for a comparator or replacement function. Without it,
// error(m, 2) in the handler named gsub's caller instead of the C frame (#277).
func (st *State) IndexWithMeta(obj, key value.Value) (value.Value, *LuaError) {
	th := st.runningThread
	if th == nil {
		return value.Nil, errf("IndexWithMeta: no running thread")
	}
	outer := st.pendingHostFrames
	st.pendingHostFrames++
	v, e := st.indexWithMeta(th, obj, key)
	st.pendingHostFrames = outer
	if e != nil {
		// Raised under lua_gettable called from a C function: no position (#278).
		e.MarkAnnotated()
	}
	return v, e
}

// LessThan exposes the full `<` semantics (number/string fast path + __lt metamethod;
// used by table.sort's default comparator — PUC's sort_comp goes through lua_lessthan).
//
// Counts the calling host function's C frame for an __lt handler, for the reason given on
// IndexWithMeta: inside table.sort the handler's caller is sort, so error(m, 2) must land on
// that C frame (no position) and error(m, 3) on sort's Lua caller (#277).
func (st *State) LessThan(a, b value.Value) (bool, *LuaError) {
	th := st.runningThread
	if th == nil {
		return false, errf("LessThan: no running thread")
	}
	outer := st.pendingHostFrames
	st.pendingHostFrames++
	r, e := st.lessThan(th, a, b)
	st.pendingHostFrames = outer
	return r, e
}

// MetaFieldOf exposes metamethod lookup for an arbitrary Value (used by stdlib __tostring etc.).
func (st *State) MetaFieldOf(v value.Value, name string) value.Value {
	return st.metaFieldOfValue(v, name)
}

// RawGet / RawSet expose raw table access (used by stdlib rawget/rawset).
func (st *State) RawGet(t arena.GCRef, key value.Value) (value.Value, *LuaError) {
	return st.tableGet(t, key)
}

// RawSet's errors ("table index is nil" / "is NaN") are luaH_set's luaG_runerror, raised while
// the running frame is the C function that called lua_rawset (rawset, table.insert, sort's
// swap). luaG_runerror adds a position only for a Lua frame, so lua5.1 reports them bare; left
// unmarked, the host-call boundary prefixed the Lua caller's line (#278).
func (st *State) RawSet(t arena.GCRef, key, val value.Value) *LuaError {
	e := st.tableSet(t, key, val)
	if e != nil {
		e.MarkAnnotated()
	}
	return e
}

// RawNext exposes iteration (used by stdlib next/pairs/table.foreach).
//
// "invalid key to 'next'" is luaH_next's luaG_runerror, raised inside the C function next (or
// foreach), so it carries no position in lua5.1 -- the same reasoning as RawSet (#278).
func (st *State) RawNext(t arena.GCRef, key value.Value) (value.Value, value.Value, bool, *LuaError) {
	k, v, ok, e := st.rawNext(t, key)
	if e != nil {
		e.MarkAnnotated()
	}
	return k, v, ok, e
}

// RawBorder exposes #t (used by stdlib table.*).
func (st *State) RawBorder(t arena.GCRef) uint32 { return st.rawBorder(t) }
