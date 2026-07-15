// CALL / TAILCALL / RETURN / VARARG / SETLIST / closure construction.
//
// Note: within M9 scope, generic for (TFORLOOP) only supports host iterators
// (M12 provides next etc.); using a Lua function as an iterator is not yet in
// the M9 acceptance requirements (05 §10.2 is deferred work).
package crescent

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// doCall executes one CALL, returning the new ci (if it entered a new Lua frame);
// if the call takes the host path, the host function runs synchronously and
// returns (nil, nil), and the main loop does not switch ci.
func (st *State) doCall(th *thread, ci *callInfo, i bytecode.Instruction) (*callInfo, *LuaError) {
	a := bytecode.A(i)
	b := bytecode.B(i)
	c := bytecode.C(i)
	funcIdx := ci.base + a
	var nargs int
	if b == 0 {
		nargs = th.top - funcIdx - 1
	} else {
		nargs = b - 1
	}
	nresults := c - 1
	callee := th.slot(funcIdx)
	if value.Tag(callee) != value.TagFunction {
		// __call metamethod (07): shift args right by one, the original callee
		// becomes the 1st argument, the handler takes its place
		h := st.metaFieldOfValue(callee, "__call")
		if value.Tag(h) != value.TagFunction {
			return nil, st.errWithName(ci, "call", a, callee)
		}
		st.insertCallSelf(th, funcIdx, nargs)
		th.setSlot(funcIdx, h)
		callee = h
		nargs++
	}
	cl := value.GCRefOf(callee)
	if object.IsHostClosure(st.arena, cl) {
		// call host synchronously; ci is unchanged after the call (main loop next=nil means no frame switch)
		e := st.callHost(th, funcIdx, nargs, nresults)
		if e == errYieldSentinel {
			// yield bubbling (08 §3.4): record resume info (resume from the
			// instruction after this CALL; resume args will be written to this
			// CALL's result register).
			th.pendingResume = &pendingResumeInfo{
				ciIndex:    th.ciDepth - 1,
				dst:        funcIdx,
				nresults:   nresults,
				entryDepth: st.entryDepthOf(th),
			}
			return nil, e
		}
		// PUC luaL_argerror: the function name for an arg error is taken from
		// this CALL site (issue #133); the main loop has already done ci.pc++,
		// so the CALL itself is at ci.pc-1.
		return nil, st.resolveArgError(e, ci, ci.pc-1, a)
	}
	// gibbous promotion branch (VS0-d / 04-trampoline §2.2): the callee Proto
	// has been promoted to gibbous and is on the main thread (§5 thread-level
	// tier rule: coroutines are not promoted) → jump to wazero via trampoline.
	// Return-value writeback + frame pop are done by gibbous RETURN through
	// h_return, returning (nil, e) the same as the host path — the execute main
	// loop reloads ci=currentCI and keeps interpreting the caller frame.
	//
	// nCcalls watermark (gibbousReentryCCallCap): every gibbous call
	// level is a real Go re-entry chain, so deep recursion would trip
	// maxCCallDepth long before maxLuaCallDepth. Past the watermark,
	// fall through to enterLuaFrame below — a promoted proto keeps its
	// bytecode, so the remaining recursion interprets flat inside this
	// executeFrom loop with zero further Go re-entry, byte-equal to P1
	// semantics (see the constant's doc in frame.go).
	if profileEnabled && th == st.mainTh && st.nCcalls < gibbousReentryCCallCap {
		pid := object.ClosureProtoID(st.arena, cl)
		if code := st.bridge.GibbousCodeOf(st.protos[pid]); code != nil {
			return nil, st.enterGibbous(th, code, funcIdx, nargs, nresults)
		}
	}
	if e := st.enterLuaFrame(th, funcIdx, nargs, nresults, false); e != nil {
		return nil, e
	}
	return currentCI(th), nil
}

// entryDepthOf finds the depth of the current innermost fresh frame (the
// bubbling boundary after a yield resume).
func (st *State) entryDepthOf(th *thread) int {
	for i := th.ciDepth - 1; i >= 0; i-- {
		ci := th.ciAt(i)
		if ci.Fresh() {
			return i
		}
	}
	return 0
}

// insertCallSelf rearranges the stack for __call: shift args right by one, the
// original callee stays at funcIdx+1 as the 1st argument, and the handler is
// written to funcIdx by the caller (07 __call semantics).
func (st *State) insertCallSelf(th *thread, funcIdx, nargs int) {
	need := funcIdx + 2 + nargs
	th.ensureStack(need)
	for k := nargs; k >= 0; k-- {
		th.setSlot(funcIdx+1+k, th.slot(funcIdx+k))
	}
	if need > th.top {
		th.setTop(need)
	}
}

// doTailCall reuses the current frame to execute a call to a new closure.
func (st *State) doTailCall(th *thread, ci *callInfo, i bytecode.Instruction) (*callInfo, *LuaError) {
	a := bytecode.A(i)
	b := bytecode.B(i)
	funcIdx := ci.base + a
	var nargs int
	if b == 0 {
		nargs = th.top - funcIdx - 1
	} else {
		nargs = b - 1
	}
	callee := th.slot(funcIdx)
	if value.Tag(callee) != value.TagFunction {
		// __call metamethod (07): isomorphic to doCall
		h := st.metaFieldOfValue(callee, "__call")
		if value.Tag(h) != value.TagFunction {
			return nil, st.errWithName(ci, "call", a, callee)
		}
		st.insertCallSelf(th, funcIdx, nargs)
		th.setSlot(funcIdx, h)
		callee = h
		nargs++
	}
	cl := value.GCRefOf(callee)
	if object.IsHostClosure(st.arena, cl) {
		// host tail call = ordinary host call, with results as this frame's return
		// values. M12 simplification: place them starting at the original funcIdx,
		// then let this frame RETURN (the main loop will run RETURN A=funcIdx, B=0
		// right after; codegen guarantees a following RETURN A B=0 per the design
		// doc); so here we just finish the host call and let ci continue.
		//
		// nresults MUST be -1 (multret, PUC lvm.c OP_TAILCALL's
		// luaD_call(L, ra, LUA_MULTRET)): the trailing RETURN B=0 collects
		// results from the live top, which callHost's multret branch sets
		// to funcIdx + n exactly. A fixed nresults resets top and drops
		// trailing results (issue #52 P4 acceptance; shared by P1/P3/P4).
		return nil, st.resolveArgError(st.callHost(th, funcIdx, nargs, -1), ci, ci.pc-1, a)
	}
	st.closeUpvals(th, ci.base)
	dst := ci.FuncIdx()
	for k := 0; k < nargs+1; k++ {
		th.setSlot(dst+k, th.slot(funcIdx+k))
	}
	parentNRes := ci.NResults()
	parentFresh := ci.Fresh()
	st.popCallInfo(th)
	if e := st.enterLuaFrame(th, dst, nargs, parentNRes, parentFresh); e != nil {
		return nil, e
	}
	cci := currentCI(th)
	cci.SetTailcall(true)
	th.reMirrorTop() // PW10 R2b-1: re-mirror the ci segment after a cold field (tailcall) change
	return cci, nil
}

// doReturn exits the current frame. terminate=true means the entry frame was exited → execute ends.
func (st *State) doReturn(th *thread, ci *callInfo, i bytecode.Instruction, entryDepth int) (*callInfo, bool) {
	a := bytecode.A(i)
	b := bytecode.B(i)
	var nret int
	if b == 0 {
		nret = th.top - (ci.base + a)
	} else {
		nret = b - 1
	}
	st.closeUpvals(th, ci.base)
	dst := ci.FuncIdx()
	src := ci.base + a
	for k := 0; k < nret; k++ {
		th.setSlot(dst+k, th.slot(src+k))
	}
	wantedN := ci.NResults()
	st.popCallInfo(th)
	if wantedN < 0 {
		th.setTop(dst + nret)
	} else {
		for k := nret; k < wantedN; k++ {
			th.setSlot(dst+k, value.Nil)
		}
		if th.ciDepth > entryDepth {
			caller := currentCI(th)
			th.setTop(caller.base + int(st.protoOf(caller).MaxStack))
		} else {
			th.setTop(dst + wantedN)
		}
	}
	if th.ciDepth <= entryDepth {
		if wantedN >= 0 {
			th.setTop(dst + wantedN)
		}
		return nil, true
	}
	caller := currentCI(th)
	return caller, false
}

// makeClosure constructs a Lua closure and fills upvalues according to the
// following pseudo-instructions (MOVE/GETUPVAL).
func (st *State) makeClosure(th *thread, ci *callInfo, i bytecode.Instruction) arena.GCRef {
	pid := st.protoOf(ci).Protos[bytecode.Bx(i)]
	subProto := st.protos[pid]
	cl := st.allocLuaClosure(pid, uint16(len(subProto.UpvalDescs)))
	for j := uint16(0); j < uint16(len(subProto.UpvalDescs)); j++ {
		pseudo := st.protoOf(ci).Code[ci.pc]
		ci.pc++
		switch bytecode.Op(pseudo) {
		case bytecode.MOVE:
			stackIdx := uint32(ci.base + bytecode.B(pseudo))
			uv := st.findOrCreateUpval(th, stackIdx)
			object.SetClosureUpvalRef(st.arena, cl, j, uv)
		case bytecode.GETUPVAL:
			parent := object.ClosureUpvalRef(st.arena, ci.Cl(), uint16(bytecode.B(pseudo)))
			object.SetClosureUpvalRef(st.arena, cl, j, parent)
		}
	}
	return cl
}

// doSetList batch-fills the array part of a table (05 §11.2).
func (st *State) doSetList(th *thread, ci *callInfo, i bytecode.Instruction) *LuaError {
	a := bytecode.A(i)
	b := bytecode.B(i)
	c := bytecode.C(i)
	if c == 0 {
		c = int(st.protoOf(ci).Code[ci.pc])
		ci.pc++
	}
	tbl := reg(th, ci, a)
	if value.Tag(tbl) != value.TagTable {
		return errf("SETLIST: not a table")
	}
	tref := value.GCRefOf(tbl)
	var n int
	restoreTop := false
	if b == 0 {
		n = th.top - (ci.base + a) - 1
		restoreTop = true
	} else {
		n = b
	}
	base0 := uint32((c - 1) * bytecode.FieldsPerFlush)
	for j := 1; j <= n; j++ {
		st.tableSetInt(tref, base0+uint32(j), reg(th, ci, a+j))
	}
	if restoreTop {
		// After consuming the "to top" multi-value window, restore the frame's
		// logical top (aligning with lvm.c OP_SETLIST `L->top = L->ci->top`):
		// otherwise later instructions write registers above top, and GC root
		// scanning only sees [0,top) → live values are missed, becoming a
		// use-after-free once the freelist reuses the memory.
		th.ensureStack(ci.base + int(st.protoOf(ci).MaxStack))
		th.setTop(ci.base + int(st.protoOf(ci).MaxStack))
	}
	return nil
}

// doConcat implements R(A) := R(B) .. .. R(C).
//
// The fast path linearly concatenates all string/number operands in one pass;
// on an invalid operand it goes to the __concat metamethod (right-associative,
// 07); if still absent it reports an error with a name description (09 §8.3).
func (st *State) doConcat(th *thread, ci *callInfo, i bytecode.Instruction) *LuaError {
	bIdx := bytecode.B(i)
	cIdx := bytecode.C(i)
	// fast-path check: all operands are stringifiable
	allPlain := true
	for k := bIdx; k <= cIdx; k++ {
		v := reg(th, ci, k)
		if !value.IsNumber(v) && value.Tag(v) != value.TagString {
			allPlain = false
			break
		}
	}
	if allPlain {
		parts := make([]byte, 0, 64)
		for k := bIdx; k <= cIdx; k++ {
			s, _ := st.toStringBytes(reg(th, ci, k))
			parts = append(parts, s...)
		}
		ref := st.gc.Intern(parts)
		setReg(th, ci, bytecode.A(i), value.MakeGC(value.TagString, ref))
		return nil
	}
	// slow path: fold pairwise from right to left (right-associative); __concat is looked up left first, then right
	acc := reg(th, ci, cIdx)
	for k := cIdx - 1; k >= bIdx; k-- {
		l := reg(th, ci, k)
		lOK := value.IsNumber(l) || value.Tag(l) == value.TagString
		rOK := value.IsNumber(acc) || value.Tag(acc) == value.TagString
		if lOK && rOK {
			lb, _ := st.toStringBytes(l)
			rb, _ := st.toStringBytes(acc)
			ref := st.gc.Intern(append(append([]byte{}, lb...), rb...))
			acc = value.MakeGC(value.TagString, ref)
			continue
		}
		h := st.metaFieldOfValue(l, "__concat")
		if h == value.Nil {
			h = st.metaFieldOfValue(acc, "__concat")
		}
		if h == value.Nil {
			bad := l
			badRK := k
			if lOK {
				bad = acc
				badRK = k + 1
			}
			return st.errWithName(ci, "concatenate", badRK, bad)
		}
		res, e := st.callMetaHandler(th, h, []value.Value{l, acc}, 1)
		if e != nil {
			return e
		}
		acc = res
	}
	setReg(th, ci, bytecode.A(i), acc)
	return nil
}

// doVararg implements VARARG A B: copies the vararg contents in the below-stack
// region [base-nVarargs..base) to R(A..A+B-2); when B=0 it copies all and sets
// top to the end of the multi-value region (aligning with the official
// `L->top = ra + n`). VS0-e: the vararg data source is moved from the ci.varargs
// Go slice to the below-stack region and read live via th.slot (aligning with
// the official lvm.c `OP_VARARG`).
func (st *State) doVararg(th *thread, ci *callInfo, i bytecode.Instruction) *LuaError {
	a := bytecode.A(i)
	b := bytecode.B(i)
	n := int(ci.nVarargs)
	vbase := ci.base - n // start of the below-stack region (vararg0 slot)
	if b == 0 {
		// all varargs up to top. top must be set both ways (not just raised, not
		// only lowered): a higher leftover top would make the consumer (doCall's
		// nargs = top-funcIdx-1) overestimate the argument count.
		need := ci.base + a + n
		if need > th.size() {
			th.ensureStack(need)
		}
		for k := 0; k < n; k++ {
			th.setSlot(ci.base+a+k, th.slot(vbase+k))
		}
		th.setTop(need)
		return nil
	}
	want := b - 1
	for k := 0; k < want; k++ {
		if k < n {
			setReg(th, ci, a+k, th.slot(vbase+k))
		} else {
			setReg(th, ci, a+k, value.Nil)
		}
	}
	return nil
}
