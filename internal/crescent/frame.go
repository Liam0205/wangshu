// Frame management — the stack layout of enterLuaFrame / popCallInfo / execute.
package crescent

import (
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// Call-depth limits (05 §7.4, aligned with the official 5.1.5 luaconf.h).
const (
	maxLuaCallDepth = 20000 // LUAI_MAXCALLS: upper bound on the CallInfo chain length; overflow raises "stack overflow"
	maxCCallDepth   = 200   // LUAI_MAXCCALLS: upper bound on host→Lua re-entry (the real Go stack); overflow raises "C stack overflow"

	// gibbousReentryCCallCap is the soft watermark above which promoted
	// protos are dispatched to the INTERPRETER instead of their gibbous
	// code. Rationale: pure-Lua recursion costs no C stack on the
	// interpreter (the execute loop drives the CI chain; PUC 5.1 is the
	// same, which is why LUAI_MAXCALLS is 20000), but every native-tier
	// call level is a real Go re-entry chain (Run -> dispatcher ->
	// ExecutePlainCallInlineFrame -> enterGibbous -> Run ...), each
	// burning one nCcalls. Deep recursion on a promoted proto would
	// therefore hit maxCCallDepth (200) and raise "C stack overflow"
	// where the interpreter succeeds — an auto-mode-only divergence
	// (found by FuzzAutoPromote: `fib(1000)` self-recursion). Past this
	// watermark, gibbous entry points fall back to the interpreter: the
	// remaining descent runs on the CI chain with NO further Go
	// re-entry, bounded by maxLuaCallDepth exactly like P1. Results
	// stay byte-equal; only pathological-depth performance degrades.
	// Half the hard cap leaves the other half as headroom for
	// metamethod/host re-entries below the switch point.
	gibbousReentryCCallCap = maxCCallDepth / 2
)

// enterLuaFrame prepares a frame and pushes its CallInfo (05 §1.4).
//
// funcIdx is the index of the callee closure on the stack; the arguments follow
// right after it (funcIdx+1..funcIdx+1+nargs). nresults<0 means the caller wants
// "all returns". entry=true marks callStatus_fresh (the execute boundary: once
// RETURN unwinds below this frame, execute terminates).
func (st *State) enterLuaFrame(th *thread, funcIdx, nargs, nresults int, entry bool) *LuaError {
	// Lua call-depth limit (05 §7.4; equivalent to LUAI_MAXCALLS=20000, aligned
	// with 5.1.5 luaconf.h). TAILCALL pops before it enters, so net depth is
	// unchanged and a proper tail call is not limited.
	if th.ciDepth >= maxLuaCallDepth {
		return errf("stack overflow")
	}
	// The call-billing point for the instruction budget: a pure-recursion storm
	// (trampoline-style mutual recursion that repeatedly enters/exits within the
	// depth limit) never crosses a back-edge, so only billing here catches it.
	// When the budget is off and no ctx is injected, preempt short-circuits
	// internally.
	if e := st.preempt(); e != nil {
		return e
	}
	v := th.slot(funcIdx)
	if value.Tag(v) != value.TagFunction {
		return errf("attempt to call a %s value", st.typeNameOf(v))
	}
	cl := value.GCRefOf(v)
	if object.IsHostClosure(st.arena, cl) {
		// Defense: a normal Lua → host call goes through the callHost branch of
		// doCall/doTailCall; reaching enterLuaFrame means the call entry bypassed
		// dispatch (internal bug).
		return errf("call: host closure cannot enter Lua frame (internal dispatch bug)")
	}
	pid := object.ClosureProtoID(st.arena, cl)
	proto := st.protos[pid]
	numFixed := int(proto.NumParams)
	// VS0-e: base reshuffle (the official Lua 5.1 real stack layout).
	//
	// Old layout: [funcIdx | fix0..fixN-1 | extra0..extraM-1 | gap..MaxStack-1]
	// New layout: [funcIdx | vararg0..varargM-1 | R(0)=fix0..R(N-1)=fixN-1 | gap..MaxStack-1]
	//         base = funcIdx + 1 + nVarargs
	// The vararg region sits in the below-stack area stack[base-nVarargs..base);
	// VARARG reads it live via th.slot(base-nV+k); GC scanning [0, top) covers it
	// naturally (vararg < base < top). No separate ciVarargs / ci.varargs Go
	// slice (retired in VS0-e substep ④).
	nVarargs := 0
	if nargs > numFixed && proto.IsVararg {
		nVarargs = nargs - numFixed
	}
	base := funcIdx + 1 + nVarargs
	// Reserve the stack up to the new base + MaxStack first (covers the reshuffle
	// target region + the nil-clear region; a segment relocation triggered by
	// ensureStack is automatically picked up by slot/setSlot form-Y live
	// addressing against the new segment view).
	need := base + int(proto.MaxStack)
	if need > th.size() {
		th.ensureStack(need)
	}
	if nVarargs > 0 {
		// Reshuffle in three steps (to avoid overwriting):
		// ① read varargs temporarily into a Go slice (a short temporary; local to
		//    enterLuaFrame in substep ④, does not enter ci/thread)
		// ② move fixed params high-to-low: stack[base+i] = stack[funcIdx+1+i] (dst > src prevents overwrite)
		// ③ write varargs into the below-stack area: stack[funcIdx+1+i] = vararg[i]
		buf := make([]value.Value, nVarargs)
		for i := 0; i < nVarargs; i++ {
			buf[i] = th.slot(funcIdx + 1 + numFixed + i)
		}
		for i := numFixed - 1; i >= 0; i-- {
			th.setSlot(base+i, th.slot(funcIdx+1+i))
		}
		for i := 0; i < nVarargs; i++ {
			th.setSlot(funcIdx+1+i, buf[i])
		}
	} else if nargs < numFixed {
		// Too few arguments: nVarargs=0 ⟹ base=funcIdx+1=original, fixed params
		// are in place, pad nil up to numFixed.
		for i := nargs; i < numFixed; i++ {
			th.setSlot(base+i, value.Nil)
		}
	}
	// nargs > numFixed && !IsVararg: the excess arguments in [base+numFixed..base+nargs-1]
	// are covered by the nil-clear below (original Lua 5.1 behavior: discarded).
	//
	// nil-clear region [base+numFixed, base+MaxStack).
	for i := base + numFixed; i < base+int(proto.MaxStack); i++ {
		th.setSlot(i, value.Nil)
	}
	// LUA_COMPAT_VARARG: the implicit arg table (5.1 default compat; arg = {n=#varargs, ...},
	// occupying the first register after the formal params; codegen has already reserved it
	// via registerLocal("arg")). VS0-e: read varargs live from the below-stack area
	// stack[base-nVarargs..base) (after substep ③ lands, the below-stack area is the
	// authoritative source).
	if proto.NeedsArg {
		argTbl := st.allocTable(uint32(nVarargs), 8)
		for i := 0; i < nVarargs; i++ {
			st.tableSetInt(argTbl, uint32(i+1), th.slot(base-nVarargs+i))
		}
		nKey := value.MakeGC(value.TagString, st.gc.Intern([]byte("n")))
		_ = st.tableSet(argTbl, nKey, value.NumberValue(float64(nVarargs)))
		th.setSlot(base+numFixed, value.MakeGC(value.TagTable, argTbl))
	}
	// Push CallInfo (PW10 R2b-4: the arena segment is authoritative, th.cur is the
	// hot mirror of the top frame).
	ci := callInfo{
		base:     base,
		funcIdx:  funcIdx,
		top:      base + numFixed,
		protoID:  pid,
		cl:       cl,
		nresults: nresults,
		fresh:    entry,
		pc:       0,
		nVarargs: uint16(nVarargs), // strictly aligned with the below-stack area [base-nVarargs..base) + segment word4 mirror
	}
	// Flush the current top frame (th.cur, whose pc/top may have advanced) back to
	// the segment first, then load the new frame.
	if th.ciDepth > 0 {
		th.writeCISeg(th.ciDepth-1, &th.cur)
	}
	depth := th.ciDepth
	if depth >= th.ciCap {
		th.growCISeg(depth + 1)
	}
	th.cur = ci
	th.setCIDepth(depth + 1)
	th.writeCISeg(depth, &th.cur)
	if ciMirrorCheck {
		// wangshu_trace safety net: read back the segment and self-check that pack/unpack
		// is field-for-field identical to th.cur (R2b-1).
		th.verifyCISeg(depth, &th.cur)
	}
	th.setTop(base + int(proto.MaxStack))
	if profileEnabled {
		st.bridge.OnEnterID(proto, pid, th == st.mainTh)
	}
	return nil
}

// popCallInfo pops the top frame and returns a copy of it (for doReturn to read
// nresults, etc.). After popping it reloads the caller frame from the segment
// into th.cur (if a caller still exists). PW10 R2b-4 + VS0-e: the vararg region
// lives in the below-stack area, so ciVarargs shadow restoration is no longer
// needed; nVarargs is decoded from segment word4 together with the caller.
func (st *State) popCallInfo(th *thread) callInfo {
	ci := th.cur
	th.setCIDepth(th.ciDepth - 1)
	if th.ciDepth > 0 {
		th.readCISegInto(th.ciDepth-1, &th.cur)
	}
	return ci
}

// currentCI returns a pointer to the hot mirror of the top frame. **The address is
// stable** (it points at th.cur, not a relocatable segment/slice element) — so a
// hot loop holding this pointer never dangles across a CALL/allocation (PW10 R2b-4
// eliminates the append-relocation minefield, design-claims-vs-codebase-physics §2).
// Mutations through it modify th.cur directly, and the next push/pop boundary
// flushes back to the segment via writeCISeg.
//
// **PW10 zero-crossing Stage 1b holder audit**: the Wasm-side frame build/teardown
// (Stage 2/3) modifies the ciDepth word + segment frames during Wasm execution, but
// th.cur is an **address-stable** fixed struct field — syncCurFromSeg **updates the
// contents of th.cur in place** (not swapping addresses) at the boundary back into Go,
// so any pointer holding &th.cur automatically sees the resync'd contents and does not
// dangle. The Go helper only runs when Wasm crosses back (at which point Wasm is not on
// the stack); the entry takes fresh from currentCI; after the interpreter calls gibbous
// it reloads ci per existing discipline (execute.go).
// ⟹ no new stale-holder minefield.
func currentCI(th *thread) *callInfo { return &th.cur }

// rk fetches an RK operand: < 256 reads register R(rk); >=256 reads constant K(rk-256).
// proto is passed in by the caller (VS0-b: ci no longer holds *Proto; the constant table
// is taken via proto.Consts).
func rk(th *thread, ci *callInfo, proto *bytecode.Proto, rk int) value.Value {
	if rk < bytecode.MaxK {
		return th.slot(ci.base + rk)
	}
	return proto.Consts[rk-bytecode.MaxK]
}

// reg is a convenience register read.
func reg(th *thread, ci *callInfo, r int) value.Value { return th.slot(ci.base + r) }

// setReg is a convenience register write.
func setReg(th *thread, ci *callInfo, r int, v value.Value) {
	th.setSlot(ci.base+r, v)
}

// errf builds a LuaError (M9 simplification: the Value is the error string content
// directly, not yet interned into the arena; the M11 error module will align this).
func errf(format string, args ...any) *LuaError {
	msg := sprintf(format, args...)
	return &LuaError{Msg: msg}
}

// typeName returns the Lua type name (for error messages).
//
// Coroutine-handle caveat: wangshu models coroutines as lightuserdata
// handles (TagLightUD); without State context this function can only
// say "userdata". Error-message paths must use st.typeNameOf instead:
// PUC reports "thread" for thread values (cgo oracle diff fuzz catch:
// "attempt to call a thread value").
func typeName(v value.Value) string {
	if value.IsNumber(v) {
		return "number"
	}
	switch value.Tag(v) {
	case value.TagNil:
		return "nil"
	case value.TagBool:
		return "boolean"
	case value.TagLightUD, value.TagUserdata:
		return "userdata"
	case value.TagString:
		return "string"
	case value.TagTable:
		return "table"
	case value.TagFunction:
		return "function"
	case value.TagThread:
		return "thread"
	}
	return "unknown"
}
