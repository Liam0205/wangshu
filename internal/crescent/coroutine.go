// Coroutines — route B (08 §3): single goroutine, resume starts a new layer of
// execute, the yield signal bubbles up to the resume boundary via yieldRequested.
//
// In P1 the thread is still a Go struct (moving the value stack into an arena is
// separate work); a coroutine object is represented by a lightuserdata handle
// (coID) + a registry on State, and the Lua-side type() recognizes it via the
// registry to return "thread".
package crescent

import (
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
	th      *thread
	status  CoStatus
	fn      value.Value // main function (started on the first resume)
	started bool
	// yield transfer area (on yield: co→resumer; on resume: resumer→co)
	xfer []value.Value
}

// coRegistry registers coroutines on State (coID → *coroutine).
type coRegistry struct {
	cos []*coroutine
}

// NewCoroutine creates a suspended coroutine and returns its coID (lightuserdata handle).
func (st *State) NewCoroutine(fn value.Value) (uint64, *LuaError) {
	// PUC luaB_cocreate: lua_isfunction && !lua_iscfunction -- host
	// closures (C functions) are rejected too, with "Lua function
	// expected" (issue #133 patrol: we used to accept host fns, so
	// coroutine.create(print) diverged from the oracle).
	if value.Tag(fn) != value.TagFunction || object.IsHostClosure(st.arena, value.GCRefOf(fn)) {
		return 0, NewArgError(1, "Lua function expected")
	}
	co := &coroutine{
		th:     st.newThread(),
		status: CoSuspended,
		fn:     fn,
	}
	st.cos.cos = append(st.cos.cos, co)
	return uint64(len(st.cos.cos) - 1), nil
}

// coByID fetches a coroutine (returns nil on out-of-range).
func (st *State) coByID(id uint64) *coroutine {
	if int(id) >= len(st.cos.cos) {
		return nil
	}
	return st.cos.cos[id]
}

// IsCoroutineHandle reports whether a Value is a coroutine handle (lightuserdata + in registry).
func (st *State) IsCoroutineHandle(v value.Value) bool {
	if value.Tag(v) != value.TagLightUD {
		return false
	}
	return st.coByID(value.AsLightUD(v)) != nil
}

// CoStatusOf returns the coroutine's status name (coroutine.status).
func (st *State) CoStatusOf(id uint64) string {
	co := st.coByID(id)
	if co == nil {
		return "dead"
	}
	return co.status.String()
}

// Resume resumes (or starts) a coroutine (08 §3.5 / §4.2).
//
// Returns (results, ok, err): when ok=false, results[0] is the error value (the
// "resume turns an error into (false, errval)" semantics are assembled on the
// stdlib side; here we return the raw information).
func (st *State) Resume(id uint64, args []value.Value) ([]value.Value, bool, *LuaError) {
	co := st.coByID(id)
	if co == nil {
		return nil, false, errf("cannot resume dead coroutine")
	}
	switch co.status {
	case CoDead:
		return nil, false, errf("cannot resume dead coroutine")
	case CoRunning, CoNormal:
		return nil, false, errf("cannot resume non-suspended coroutine")
	}
	resumerTh := st.runningThread
	co.status = CoRunning

	// resume starts a new layer of execute (Go stack +1); the nested resume
	// chain and host→Lua reentry share the same limit (05 §7.4).
	if st.nCcalls >= maxCCallDepth {
		co.status = CoSuspended
		return nil, false, errf("C stack overflow")
	}
	st.nCcalls++
	defer func() { st.nCcalls-- }()

	// Nested resume: the calling coroutine turns normal (5.1 state machine;
	// visible to coroutine.status, and findRunningCo also relies on "only one
	// CoRunning" to decide yield ownership).
	if resumer := st.findRunningCo(); resumer != nil {
		resumer.status = CoNormal
		defer func() { resumer.status = CoRunning }()
	}

	// The suspended calling thread enters the resume chain (GC root; 06 §5.1 R4).
	if resumerTh != nil {
		st.threadChain = append(st.threadChain, resumerTh)
		defer func() { st.threadChain = st.threadChain[:len(st.threadChain)-1] }()
	}

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
		// Error: the coroutine dies (leftover xfer values stay resident in the
		// registry along with the dead coroutine — the registry does not shrink;
		// clearing it follows the same hygiene standard as returning to the pool)
		co.status = CoDead
		co.xfer = nil
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
func (st *State) Yield(args []value.Value) *LuaError {
	co := st.findRunningCo()
	if co == nil {
		return errf("attempt to yield from outside a coroutine")
	}
	co.xfer = append(co.xfer[:0], args...) // copy: args is a pooled buffer
	return errYieldSentinel
}

// findRunningCo finds the coroutine corresponding to runningThread (nil if none = main thread).
func (st *State) findRunningCo() *coroutine {
	for _, co := range st.cos.cos {
		if co.th == st.runningThread && co.status == CoRunning {
			return co
		}
	}
	return nil
}

// RunningCoID returns the ID of the currently running coroutine (returns false for the main thread).
func (st *State) RunningCoID() (uint64, bool) {
	for i, co := range st.cos.cos {
		if co.th == st.runningThread && co.status == CoRunning {
			return uint64(i), true
		}
	}
	return 0, false
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
// The recovery information saved at yield lives on co.th's pendingResume (recorded
// by doCall when yield bubbles through callHost).
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
	nresults   int // expected number of results of the yield CALL
	entryDepth int // execute's entry depth (the bubble boundary is unchanged after resume)
}
