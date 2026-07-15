// Host function infrastructure — a Go function attached to a Lua closure,
// called synchronously by the CALL path.
//
// Design: 05 §7.5 + 10 §3. Within M12 scope this provides a minimal host
// function call:
// - HostFn signature = func(*State, *thread, args []value.Value) (results []value.Value, err *LuaError);
// - registering a HostFn yields a HostFnID, wrapped into a closure via object.AllocHostClosure.
package crescent

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// HostFn is the host Go function signature (the P1 version of 10 §3).
//
// args is a snapshot of the actual arguments at call time (copied out by
// callHost); it returns results / error. HostFn does not directly manipulate
// the thread stack, which avoids any dependence on the Go callback's stack
// protocol.
//
// **args lifetime contract**: args comes from State's argument buffer pool
// and is valid only within this call (including when returned as a return
// value — callHost returns the buffer only after the results have been copied
// onto the stack). Do not store args (or a sub-slice of it) anywhere that
// outlives this call (closure, global, coroutine transfer area, etc.); make an
// explicit copy if you need to keep it. Violating this manifests as "the
// return value is overwritten by a subsequent host call", far removed from the
// root cause; when debugging, enable the wangshu_trace build (it fills the
// buffer with a poison value on return, so a violation shows up immediately).
type HostFn func(st *State, args []value.Value) ([]value.Value, *LuaError)

// hostFnRegistry is the host function registry on State (referenced by the
// integer HostFnID).
//
// Slots are reclaimable: each slot carries a reference count (MakeHostClosure
// +1 / host closure collected by GC -1); a slot that reaches zero goes onto the
// free list for RegisterHostFn to reuse. Otherwise every gmatch call and every
// mountArena Call would permanently append a closure (an unbounded leak in the
// long-lived-State, repeatedly-executing rule-engine shape, and each entry
// holds a src/pat byte copy via its Go closure).
type hostFnRegistry struct {
	fns  []HostFn
	refs []int32  // per-slot reference count (number of live host closures)
	free []uint32 // slots that reached zero and can be reused
	// intrinsics maps a HostFnID to a math intrinsic kind (jit.Intrinsic*,
	// 0 = none) for the P4 native segment fast path (issue #77). Grown
	// lazily by RegisterIntrinsic; indexed by HostFnID in lockstep with
	// fns. A recycled slot (see RegisterHostFn's free-list branch) clears
	// its entry so a reused id can't masquerade as a stale intrinsic —
	// though in practice only globals-rooted stdlib math closures are ever
	// tagged, and those are never collected, so their id never recycles.
	intrinsics []uint8
}

// RegisterHostFn registers a HostFn and returns its HostFnID within State
// (reusing a free slot).
func (st *State) RegisterHostFn(fn HostFn) uint32 {
	r := &st.hostFns
	if n := len(r.free); n > 0 {
		id := r.free[n-1]
		r.free = r.free[:n-1]
		r.fns[id] = fn
		r.refs[id] = 0
		if int(id) < len(r.intrinsics) {
			r.intrinsics[id] = 0 // clear any stale intrinsic tag on reuse
		}
		return id
	}
	id := uint32(len(r.fns))
	r.fns = append(r.fns, fn)
	r.refs = append(r.refs, 0)
	return id
}

// RegisterIntrinsic tags a HostFnID with a math intrinsic kind
// (jit.Intrinsic*) so the P4 native CALL IC can recognize it and emit
// the operation inline instead of round-tripping through the Go closure
// (issue #77). kind == 0 is a no-op. Called by the stdlib right after
// RegisterHostFn for the recognized math.* functions.
func (st *State) RegisterIntrinsic(id uint32, kind uint8) {
	if kind == 0 {
		return
	}
	r := &st.hostFns
	if int(id) >= len(r.intrinsics) {
		grown := make([]uint8, len(r.fns))
		copy(grown, r.intrinsics)
		r.intrinsics = grown
	}
	if int(id) < len(r.intrinsics) {
		r.intrinsics[id] = kind
	}
}

// IntrinsicKindOf returns the math intrinsic kind tagged for a HostFnID,
// or 0 if the id is untagged / out of range. Consulted by
// ObserveCallCallee when a CALL site's callee is a host closure.
func (st *State) IntrinsicKindOf(id uint32) uint8 {
	r := &st.hostFns
	if int(id) < len(r.intrinsics) {
		return r.intrinsics[id]
	}
	return 0
}

// MakeHostClosure wraps an already-registered HostFnID into a host closure
// (0 upvalue).
func (st *State) MakeHostClosure(id uint32) arena.GCRef {
	cl := object.AllocHostClosure(st.arena, id, 0)
	st.gc.LinkSweep(cl)
	st.gc.AllocCharge(2 * 8)
	st.hostFns.refs[id]++
	return cl
}

// releaseHostFn releases a host closure's slot reference when the host closure
// is collected by GC (a callback from the gc package).
func (st *State) releaseHostFn(id uint32) {
	r := &st.hostFns
	if int(id) >= len(r.refs) || r.refs[id] <= 0 {
		return
	}
	r.refs[id]--
	if r.refs[id] == 0 {
		r.fns[id] = nil // release the Go closure (and its captured src/pat, etc.)
		r.free = append(r.free, id)
	}
}

// SetGlobal attaches a value to a string key in the globals table (for stdlib
// registration / public-facing forwarding).
func (st *State) SetGlobal(name string, v value.Value) {
	ref := st.gc.Intern([]byte(name))
	key := value.MakeGC(value.TagString, ref)
	_ = st.tableSet(st.globals, key, v)
}

// GetGlobal reads a string key from the globals table (symmetric with
// SetGlobal, used for public-facing forwarding). A missing key returns
// value.Nil (matching Lua 5.1 `_G[k]` semantics).
func (st *State) GetGlobal(name string) value.Value {
	ref := st.gc.Intern([]byte(name))
	key := value.MakeGC(value.TagString, ref)
	v, _ := st.tableGet(st.globals, key)
	return v
}

// SetGlobalByRef writes globals using a pre-interned string GCRef as the key,
// skipping the `gc.Intern([]byte(name))` step (issue #13 part B). `nameRef`
// must be a product of the same State's gc.Intern — behavior is undefined for a
// cross-State / non-string-tag GCRef. The public facing side calls this
// indirectly via wangshu.GlobalsSlot, whose Slot holds a pinned reference that
// keeps that nameRef alive.
func (st *State) SetGlobalByRef(nameRef arena.GCRef, v value.Value) {
	key := value.MakeGC(value.TagString, nameRef)
	_ = st.tableSet(st.globals, key, v)
}

// GetGlobalByRef reads globals using a pre-interned string GCRef as the key
// (the dual of SetGlobalByRef). Same constraints as SetGlobalByRef; the public
// facing side calls this indirectly via wangshu.GlobalsSlot.
func (st *State) GetGlobalByRef(nameRef arena.GCRef) value.Value {
	key := value.MakeGC(value.TagString, nameRef)
	v, _ := st.tableGet(st.globals, key)
	return v
}

// PinRef registers a GCRef in the pin table and returns a handle index. The
// pin table is marked as a GC root, guaranteeing the object is not collected
// while the host Go side holds it (an old value stays callable even after
// globals overwrites it). It reuses free slots from freePins; unbounded slot
// growth occurs only in the "long-lived State that repeatedly GetGlobals
// different-named functions without Release" case — public-API users should
// pair calls with Release (11 §6.2 mind).
func (st *State) PinRef(ref arena.GCRef) uint32 {
	if n := len(st.freePins); n > 0 {
		idx := st.freePins[n-1]
		st.freePins = st.freePins[:n-1]
		st.pinnedRefs[idx] = ref
		return idx
	}
	idx := uint32(len(st.pinnedRefs))
	st.pinnedRefs = append(st.pinnedRefs, ref)
	return idx
}

// UnpinRef releases a pin handle. An out-of-range / already-released slot is a
// no-op (the public-facing Value.Release may be called repeatedly, so this is
// fault-tolerant).
func (st *State) UnpinRef(idx uint32) {
	if int(idx) >= len(st.pinnedRefs) {
		return
	}
	if st.pinnedRefs[idx].IsNull() {
		return
	}
	st.pinnedRefs[idx] = arena.GCRef(0)
	st.freePins = append(st.freePins, idx)
}

// PinnedRefAt retrieves the GCRef corresponding to a pin handle; returns Null
// if out-of-range / already released. Used by the facade State.Call to extract
// the underlying closure when a Value(function) argument is passed.
func (st *State) PinnedRefAt(idx uint32) arena.GCRef {
	if int(idx) >= len(st.pinnedRefs) {
		return arena.GCRef(0)
	}
	return st.pinnedRefs[idx]
}

// baselineEntry is one item of the globals baseline: key is a string (after
// interning), val is the value at baseline time. The GCRef of a composite value
// (table/function) is inside val, but the baseline table itself resides in
// State and is added to the GC roots via visitBaselineRefs to prevent
// collection (issue #6: without rooting, a baseline value gets GC'd between two
// Resets).
type baselineEntry struct {
	key string
	val value.Value
}

// MarkGlobalsBaseline takes a snapshot of the current _G as the baseline
// (issue #6). A repeated call overwrites the old baseline. RawNext walks the
// current globals and copies all (string key, value) pairs to State's baseline
// slice.
//
// Caller contract: the typical timing is to call this right after NewState
// finishes loading stdlib, fixing the stdlib-provided surface as the baseline.
// Afterwards ResetGlobalsToBaseline can restore that baseline between each
// Borrow.
//
// **Limitation**: only string keys are walked (both stdlib and the host's own
// globals are string keys); number / table / function etc. keys are skipped
// (stdlib does not use them, and they do not occur in real scenarios). This
// avoids extending the baseline design to arbitrary key shapes and keeps the
// conversion simple.
func (st *State) MarkGlobalsBaseline() {
	st.baseline = st.baseline[:0]
	key := value.Nil
	for {
		nextKey, nextVal, ok, e := st.rawNext(st.globals, key)
		if e != nil || !ok {
			break
		}
		if value.Tag(nextKey) == value.TagString {
			// key is already interned, so copy the bytes directly (the intern
			// pool guarantees a subsequent lookup yields the same ref)
			keyStr := string(object.StringBytes(st.arena, value.GCRefOf(nextKey)))
			st.baseline = append(st.baseline, baselineEntry{key: keyStr, val: nextVal})
		}
		key = nextKey
	}
}

// ResetGlobalsToBaseline restores _G to the state snapshotted by the last
// MarkGlobalsBaseline (issue #6): non-baseline string keys are deleted (set to
// Nil), and baseline string keys are written back to their baseline value. If
// never Marked (baseline empty), this is equivalent to ClearScriptGlobals
// behavior — all string-key globals are deleted; use with care.
//
// Implementation: ① first rawNext-walk the current globals to collect all
// string keys; ② for keys in the baseline write the baseline value; ③ for keys
// not in the baseline write Nil.
func (st *State) ResetGlobalsToBaseline() {
	// Build a set from the baseline (a short table, so a linear O(N) scan
	// suffices, no map needed)
	inBaseline := func(k string) (value.Value, bool) {
		for i := range st.baseline {
			if st.baseline[i].key == k {
				return st.baseline[i].val, true
			}
		}
		return value.Nil, false
	}
	// Step 1: walk the current _G to collect string keys (globals is not
	// modified during rawNext, avoiding iterator invalidation)
	var currentKeys []string
	key := value.Nil
	for {
		nextKey, _, ok, e := st.rawNext(st.globals, key)
		if e != nil || !ok {
			break
		}
		if value.Tag(nextKey) == value.TagString {
			currentKeys = append(currentKeys,
				string(object.StringBytes(st.arena, value.GCRefOf(nextKey))))
		}
		key = nextKey
	}
	// Step 2: delete non-baseline keys
	for _, k := range currentKeys {
		if _, in := inBaseline(k); !in {
			st.SetGlobal(k, value.Nil)
		}
	}
	// Step 3: restore baseline keys to their baseline values
	for i := range st.baseline {
		st.SetGlobal(st.baseline[i].key, st.baseline[i].val)
	}
}

// BaselineSize reports the number of keys in the current baseline (for
// diagnostics/testing).
func (st *State) BaselineSize() int { return len(st.baseline) }

// callHost synchronously calls a host closure (05 §7.5).
//
// funcIdx: the host closure's index on the stack; arguments follow immediately;
// nresults < 0 = the caller wants variable results (keep all on the stack);
// otherwise pad/truncate to the given count.
func (st *State) callHost(th *thread, funcIdx, nargs, nresults int) *LuaError {
	cl := value.GCRefOf(th.slot(funcIdx))
	hid := object.ClosureProtoID(st.arena, cl)
	fn := st.hostFns.fns[hid]
	// args goes through State's buffer pool (host calls are the bulk of
	// nbody-level allocation load, 91%). HostFn contract: do not retain the
	// args slice beyond this call (returning an args sub-slice such as select
	// is legal — the buffer is returned after the results are copied onto the
	// stack); coroutine xfer has been changed to copy. On host→Lua→host
	// nesting the pool's LIFO naturally deepens.
	args := st.getArgsBuf(nargs)
	defer st.putArgsBuf(args)
	for i := 0; i < nargs; i++ {
		args[i] = th.slot(funcIdx + 1 + i)
	}
	results, e := fn(st, args)
	if e != nil {
		return e
	}
	dst := funcIdx
	n := len(results)
	if nresults < 0 {
		// variable: all results land at dst, top = dst + n
		if dst+n > th.size() {
			th.ensureStack(dst + n)
		}
		for k := 0; k < n; k++ {
			th.setSlot(dst+k, results[k])
		}
		th.setTop(dst + n)
		return nil
	}
	want := nresults
	if dst+want > th.size() {
		th.ensureStack(dst + want)
	}
	for k := 0; k < want; k++ {
		if k < n {
			th.setSlot(dst+k, results[k])
		} else {
			th.setSlot(dst+k, value.Nil)
		}
	}
	// Fixed-length results: restore top to the current frame's logical top
	// (05 §1.2 CallInfo.top maintenance; matches 5.1 "L->top = ci->top").
	// Otherwise the low top left by a preceding multi-value CALL (C=0) would let
	// the subsequent callLuaFromHost scaffold overwrite live registers (the
	// TFORLOOP state slot gets clobbered).
	if th.ciDepth > 0 {
		ci := currentCI(th)
		frameTop := ci.base + int(st.protoOf(ci).MaxStack)
		th.ensureStack(frameTop)
		th.setTop(frameTop)
	}
	return nil
}
