// Coroutines — route B (08 §3): single goroutine, resume starts a new layer of
// execute, the yield signal bubbles up to the resume boundary via yieldRequested.
//
// The coroutine's state lives in a Go record in a registry on State (its stacks in
// arena segments of their own); the Lua value is a thread object, a Thread head in
// the arena that carries the record's index (coID). The collector reaches a
// coroutine's stack through that object, so a coroutine nothing refers to is
// collected like any other object (#291).
package crescent

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// CoStatus is the coroutine state machine (08 §2.3).
type CoStatus uint8

const (
	CoSuspended CoStatus = iota
	CoRunning
	CoNormal // resumed another coroutine; itself is suspended on the resume chain
	CoDead
)

func (s CoStatus) String() string {
	switch s {
	case CoSuspended:
		return "suspended"
	case CoRunning:
		return "running"
	case CoNormal:
		return "normal"
	case CoDead:
		return "dead"
	}
	return "?"
}

// coroutine is one coroutine instance: an independent thread (value stack +
// CallInfo chain) + status.
type coroutine struct {
	ref     arena.GCRef // the thread object that is this coroutine's Lua value
	th      *thread
	status  CoStatus
	fn      value.Value // main function (started on the first resume)
	started bool
	// yield transfer area (on yield: co→resumer; on resume: resumer→co)
	xfer []value.Value
	// baseCcalls is st.nCcalls just inside this coroutine's current resume: PUC's
	// L->baseCcalls. A yield is legal only while nCcalls is still at it -- anything
	// above means a host->Lua reentry (a metamethod, pcall, a sort comparator, a for-in
	// iterator, ...) sits between the yield and the resume, and that Go frame cannot be
	// suspended.
	baseCcalls int
	// hostAbove is the number of host frames above the innermost Lua frame of a coroutine that is
	// not running: the yield it is suspended in, or the resume (or wrap function) it is waiting in
	// while normal, plus the host functions that called it. debug.traceback(co) shows them.
	hostAbove int
	// deathLines keeps the traceback lines of the stack a coroutine died with by error.
	deathLines []string
}

// coRegistry registers coroutines on State (coID → *coroutine). The entry of a coroutine
// whose thread object was collected is nil, and its coID goes on free for reuse.
type coRegistry struct {
	cos  []*coroutine
	free []uint64
	live int // non-nil entries
}

// NewCoroutine creates a suspended coroutine and returns its thread value.
func (st *State) NewCoroutine(fn value.Value) (value.Value, *LuaError) {
	// PUC luaB_cocreate: lua_isfunction && !lua_iscfunction -- host
	// closures (C functions) are rejected too, with "Lua function
	// expected" (issue #133 patrol: we used to accept host fns, so
	// coroutine.create(print) diverged from the oracle).
	if value.Tag(fn) != value.TagFunction || object.IsHostClosure(st.arena, value.GCRefOf(fn)) {
		return value.Nil, NewArgError(1, "Lua function expected")
	}
	co := &coroutine{
		th:     st.newThread(),
		status: CoSuspended,
		fn:     fn,
	}
	var id uint64
	if n := len(st.cos.free); n > 0 {
		id = st.cos.free[n-1]
		st.cos.free = st.cos.free[:n-1]
		st.cos.cos[id] = co
	} else {
		id = uint64(len(st.cos.cos))
		st.cos.cos = append(st.cos.cos, co)
	}
	st.cos.live++
	co.ref = object.AllocThreadHandle(st.arena, id)
	st.gc.LinkSweep(co.ref)
	v := value.MakeGC(value.TagThread, co.ref)
	// Nothing refers to the new object yet: keep it through a collection the charge may start.
	h := st.gc.Push(v)
	st.gc.AllocCharge(object.ThreadHeadBytes())
	st.gc.Pop(h)
	return v, nil
}

// coOf returns the coroutine a thread value is, or nil for any other value.
func (st *State) coOf(v value.Value) *coroutine {
	if value.Tag(v) != value.TagThread {
		return nil
	}
	ref := value.GCRefOf(v)
	id := object.ThreadHandleID(st.arena, ref)
	if id >= uint64(len(st.cos.cos)) {
		return nil
	}
	if co := st.cos.cos[id]; co != nil && co.ref == ref {
		return co
	}
	return nil
}

// IsCoroutineHandle reports whether a Value is a coroutine (a thread value).
func (st *State) IsCoroutineHandle(v value.Value) bool { return st.coOf(v) != nil }

// CoStatusOf returns the coroutine's status name (coroutine.status).
func (st *State) CoStatusOf(v value.Value) string {
	co := st.coOf(v)
	if co == nil {
		return "dead"
	}
	return co.status.String()
}

// scanCoroutine is the collector's ScanThread hook: a reached coroutine keeps its stack, its main
// function (held only here before the first resume) and its transfer values. A dead one keeps
// nothing -- its upvalues were closed when it died.
func (st *State) scanCoroutine(ref arena.GCRef, visit func(value.Value), visitRef func(arena.GCRef)) {
	co := st.coOf(value.MakeGC(value.TagThread, ref))
	if co == nil || co.status == CoDead {
		return
	}
	st.visitThreadValues(co.th, nil, visit)
	st.visitThreadRefs(co.th, nil, visitRef)
	visit(co.fn)
	for _, v := range co.xfer {
		visit(v)
	}
}

// releaseCoroutines is the collector's ReleaseThreads hook: each coroutine whose thread object went
// unreached closes its upvalues, keeping the values a reached closure still sees (the collector
// marked them through openUpvalueValue), and gives its stack segments back to the arena, as
// luaE_freethread does. Running and normal coroutines are rooted, so only suspended and dead ones
// get here.
func (st *State) releaseCoroutines(isDead func(arena.GCRef) bool) {
	for id, co := range st.cos.cos {
		if co == nil || !isDead(co.ref) {
			continue
		}
		st.closeUpvals(co.th, 0)
		st.freeThread(co.th)
		co.th, co.fn, co.xfer = nil, value.Nil, nil
		st.cos.cos[id] = nil
		st.cos.free = append(st.cos.free, uint64(id))
		st.cos.live--
	}
}

// Resume resumes (or starts) a coroutine (08 §3.5 / §4.2).
//
// Returns (results, ok, err): when ok=false, results[0] is the error value (the
// "resume turns an error into (false, errval)" semantics are assembled on the
// stdlib side; here we return the raw information).
func (st *State) Resume(v value.Value, args []value.Value) ([]value.Value, bool, *LuaError) {
	co := st.coOf(v)
	if co == nil {
		return nil, false, errf("cannot resume dead coroutine")
	}
	switch co.status {
	case CoDead:
		return nil, false, errf("cannot resume dead coroutine")
	case CoRunning, CoNormal:
		// luaB_coresume / auxwrap check costatus first and name the state (statnames); lua_resume's own
		// "cannot resume non-suspended coroutine" is only reachable through the C API (#281).
		return nil, false, errf("cannot resume %s coroutine", co.status)
	}
	resumerTh := st.runningThread
	co.status = CoRunning

	// resume starts a new layer of execute (Go stack +1); the nested resume
	// chain and host→Lua reentry share the same limit (05 §7.4).
	// lua_resume's own check is a plain >= with no handler room (resume_error, not luaD_call), so
	// a handler running past the limit still cannot resume a coroutine.
	if st.nCcalls >= maxCCallDepth {
		co.status = CoSuspended
		if !co.started {
			// resume_error resets the coroutine's top to its base, dropping the function a fresh
			// coroutine still has to run: from then on costatus reports it dead.
			co.status = CoDead
		}
		return nil, false, errf("C stack overflow")
	}
	st.nCcalls++
	defer func() { st.nCcalls-- }()
	co.baseCcalls = st.nCcalls

	// Nested resume: the calling coroutine turns normal (5.1 state machine;
	// visible to coroutine.status, and findRunningCo also relies on "only one
	// CoRunning" to decide yield ownership).
	if resumer := st.findRunningCo(); resumer != nil {
		resumer.status = CoNormal
		resumer.hostAbove = int(st.pendingHostFrames) + 1 // Resume's own host function, and its callers
		defer func() { resumer.status = CoRunning }()
	}

	// The suspended calling thread enters the resume chain (GC root; 06 §5.1 R4).
	if resumerTh != nil {
		st.threadChain = append(st.threadChain, resumerTh)
		defer func() { st.threadChain = st.threadChain[:len(st.threadChain)-1] }()
	}
	// resume catches the coroutine's errors (they come back as (false, msg)), so nothing raised
	// inside needs an uncaught-error traceback.
	st.protectDepth++
	// The coroutine's errors come back to resume, not to any xpcall handler outside it: a new
	// thread starts with errfunc 0.
	outerErrFunc := st.errFunc
	st.errFunc = value.Nil
	st.catchDepth++
	outerDeathDepth := st.coDeathDepth
	st.coDeathDepth = st.catchDepth
	defer func() {
		st.protectDepth--
		st.catchDepth--
		st.errFunc = outerErrFunc
		st.coDeathDepth = outerDeathDepth
	}()
	// A coroutine's stack starts at its own body; PUC has no C frames below it. Host frames pending
	// on the RESUMER (pcall(coroutine.resume, co) leaves one) belong to the resumer's thread, so
	// the coroutine's first frame must not absorb them -- they would show up as a spurious "[C]"
	// at the bottom of a traceback taken inside, and as a phantom level for error().
	savedHostFrames := st.pendingHostFrames
	st.pendingHostFrames = 0
	defer func() { st.pendingHostFrames = savedHostFrames }()

	var sig *LuaError
	if !co.started {
		// First resume: start the main function on co's thread
		co.started = true
		st.runningThread = co.th
		co.th.push(co.fn)
		for _, a := range args {
			co.th.push(a)
		}
		if e := st.enterLuaFrame(co.th, 0, len(args), -1, true); e != nil {
			st.runningThread = resumerTh
			co.status = CoDead
			return nil, false, e
		}
		sig = st.execute(co.th)
	} else {
		// Resume from yield: pass the resume arguments as the return values of yield.
		// Copy: args may come from callHost's pooled buffer, must not outlive the call.
		co.xfer = append(co.xfer[:0], args...)
		st.runningThread = co.th
		sig = st.executeResume(co.th)
	}
	st.runningThread = resumerTh

	if sig != nil {
		if sig == errYieldSentinel {
			// yield: suspend, the xfer area holds the yielded values
			co.status = CoSuspended
			out := co.xfer
			co.xfer = nil
			return out, true, nil
		}
		// Error: the coroutine dies (its xfer values are dropped now; the record goes
		// when its thread object is collected)
		co.status = CoDead
		co.xfer = nil
		// lua_resume's error path leaves the dead thread's upvalues open in PUC, but the thread stays
		// reachable through them. Here a dead coroutine's stack is no GC root (visitExtraValues skips
		// it), so close them now, keeping the values the closures saw: otherwise a closure created
		// inside reads whatever the collector or a later reuse leaves in those slots (#285).
		st.closeUpvals(co.th, 0)
		return nil, false, sig
	}
	// Normal completion: return values are on co.th's stack at [0, top)
	co.status = CoDead
	co.xfer = nil
	out := make([]value.Value, co.th.top)
	co.th.copyOut(out, 0, co.th.top)
	return out, true, nil
}

// errYieldSentinel is the yield-signal sentinel (reuses the error-bubbling
// channel of 05 §9; the P1 realization of 08 §3.4 "yield ↔ error symmetric
// mechanism": one explicit return channel, distinguished by the sentinel).
var errYieldSentinel = &LuaError{Msg: "<yield>"}

// Yield suspends the current coroutine (the host implementation entry of coroutine.yield).
//
// Puts the yielded values into the current coroutine's xfer area and returns
// the sentinel error to let execute bubble up. On receiving the sentinel,
// callHost passes it straight up (not treated as an ordinary error).
//
// The boundary check is ldo.c lua_yield's `L->nCcalls > L->baseCcalls`, raised HERE,
// before anything is suspended. It used to be left to callLuaFromHost, which turns a
// sentinel bubbling out of a Lua function into the error -- but only a Lua function: a
// host function called directly as a metamethod or comparator (`__lt = coroutine.yield`,
// `table.sort(t, coroutine.yield)`) returned the sentinel through callMetaHandler or
// ProtectedCall without passing that check, so the coroutine suspended in the middle of
// a comparison and the next resume found no yield point (#272). The main thread has no
// coroutine to suspend and, like lua5.1's main state (nCcalls > baseCcalls there too),
// reports the same boundary error.
func (st *State) Yield(args []value.Value) *LuaError {
	co := st.findRunningCo()
	if co == nil || st.nCcalls > co.baseCcalls {
		// luaG_runerror from inside the C function yield: ci is not a Lua frame, so no
		// position is added. Left unmarked, the caller's line would be prefixed.
		e := errf("attempt to yield across metamethod/C-call boundary")
		e.MarkAnnotated()
		return e
	}
	co.xfer = append(co.xfer[:0], args...)       // copy: args is a pooled buffer
	co.hostAbove = int(st.pendingHostFrames) + 1 // yield itself, and the host functions that called it
	return errYieldSentinel
}

// findRunningCo finds the coroutine corresponding to runningThread (nil if none = main thread).
func (st *State) findRunningCo() *coroutine {
	for _, co := range st.cos.cos {
		if co != nil && co.th == st.runningThread && co.status == CoRunning {
			return co
		}
	}
	return nil
}

// RunningCoroutine returns the currently running coroutine (returns false for the main thread).
func (st *State) RunningCoroutine() (value.Value, bool) {
	if co := st.findRunningCo(); co != nil {
		return value.MakeGC(value.TagThread, co.ref), true
	}
	return value.Nil, false
}

// executeResume resumes execution from the yield point (08 §3.3 table: "the next
// resume rebuilds the frame from the saved-back CallInfo and continues from the
// instruction after yield").
//
// P1 implementation: when the yield sentinel bubbles up from callHost, the CALL
// instruction is half-executed — the host frame's return values are not written
// yet. On resume, write the resume arguments as the "return values of the yield
// call" into CALL's target registers, then continue the main loop from pc (which
// already points to the instruction after CALL).
//
// The recovery information saved at yield lives on co.th's pendingResume, recorded
// when yield bubbles through callHost: by doCall for an ordinary CALL, and by
// doTailCall for a host tail call (`return coroutine.yield(x)`), which asks for
// multret results at the callee slot so the RETURN A 0 right after the TAILCALL
// returns them.
func (st *State) executeResume(th *thread) *LuaError {
	pr := th.pendingResume
	th.pendingResume = nil
	if pr == nil {
		return errf("cannot resume: no pending yield point")
	}
	// Write the resume arguments into the result registers of the yield CALL
	co := st.findRunningCo()
	var vals []value.Value
	if co != nil {
		vals = co.xfer
		co.xfer = nil
	}
	// resume path: pr.ciIndex is the frame holding the yield CALL (yield bubbles
	// synchronously through callHost, pushing no new Lua frame and popping no
	// frame) → it is the current top frame (pr.ciIndex == th.ciDepth-1), and ciAt
	// returns th.cur's hot mirror for the top frame. Here we only read ci.base /
	// protoOf (read-only snapshot, not modifying the frame through ci).
	ci := th.ciAt(pr.ciIndex)
	want := pr.nresults
	if want < 0 {
		// variadic: drop them all and set top
		need := pr.dst + len(vals)
		th.ensureStack(need)
		th.copyIn(pr.dst, vals)
		th.setTop(need)
	} else {
		th.ensureStack(pr.dst + want)
		for k := 0; k < want; k++ {
			if k < len(vals) {
				th.setSlot(pr.dst+k, vals[k])
			} else {
				th.setSlot(pr.dst+k, value.Nil)
			}
		}
		th.setTop(ci.base + int(st.protoOf(&ci).MaxStack))
	}
	// Continue from the instruction after yield (pc already points to the next one; entryDepth uses the fresh frame depth)
	return st.executeFrom(th, pr.entryDepth)
}

// pendingResumeInfo records the recovery information of a yield point.
type pendingResumeInfo struct {
	ciIndex    int // ci index when yield occurred
	dst        int // result register of the yield CALL (absolute stack slot)
	nresults   int // expected number of results of the yield CALL (-1 = multret, always so for a tail call)
	entryDepth int // execute's entry depth (the bubble boundary is unchanged after resume)
}

// CoTraceback is debug.traceback(co, ...) for a coroutine handle: co's stack from level on (0 when
// the level is absent, as db_errorfb defaults it for another thread), laid out like any traceback.
// A suspended or normal coroutine shows the host function it waits in on top; a dead one shows the
// stack it died with by error, or nothing; one never started has no stack. ok is false when co is
// the running coroutine, which is the current thread's own traceback.
func (st *State) CoTraceback(v value.Value, level int, hasLevel bool) (tb string, ok bool) {
	co := st.coOf(v)
	if co == nil {
		return "stack traceback:", true
	}
	if co.status == CoRunning {
		return "", false
	}
	if !hasLevel {
		level = 0
	}
	var n int
	var line func(k int) string
	switch {
	case co.status == CoDead:
		lines := co.deathLines
		n, line = len(lines), func(k int) string { return lines[k] }
	case co.started:
		frames := st.tracebackFrames(co.th, co.hostAbove)
		n, line = len(frames), func(k int) string { return st.frameLine(co.th, frames, k) }
	}
	start, first := level, level
	if level < 0 {
		// lua_getstack takes a negative level for a lost tail call: each level below 0 is one
		// "(tail call): ?" line ahead of level 0, as on the current thread.
		tails, inner := -level, line
		n += tails
		line = func(k int) string {
			if k < tails {
				return "(tail call): ?"
			}
			return inner(k - tails)
		}
		first = 0
	}
	if first >= n {
		return "stack traceback:", true
	}
	return joinTraceback(n, first, start, line), true
}
