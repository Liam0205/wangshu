// Main interpreter loop — fetch → decode → execute (05 §2.3 / §12). Within the
// M9 scope it hooks up no IC, metatables, coroutines, or GC, and emits no
// safepoints (M10 fills in §5 incrementally).
package crescent

import (
	"fmt"
	"math"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// execute runs the fresh CallInfo currently on top of the stack until it exits
// (05 §7.3 entry edge).
//
// Reentry model: Lua-call-Lua re-enters within the same Go stack frame by
// mutating the ci/proto/code local variables — Go stack depth stays fixed at 1
// (05 §7.1).
func (st *State) execute(th *thread) *LuaError {
	return st.executeFrom(th, th.ciDepth-1)
}

// executeFrom runs the main loop at the given entry depth (reused when a
// coroutine resumes, 08 §3.5).
//
// On error (other than the yield sentinel) it uniformly prepends the
// "chunkname:line:" position prefix (09).
func (st *State) executeFrom(th *thread, entryDepth int) *LuaError {
	e := st.executeLoop(th, entryDepth)
	if e != nil && e != errYieldSentinel && th.ciDepth > 0 {
		e = st.annotateError(e, currentCI(th))
	}
	return e
}

func (st *State) executeLoop(th *thread, entryDepth int) *LuaError {
	ci := currentCI(th)
	proto := st.protoOf(ci)
	code := proto.Code

	for {
		if int(ci.pc) >= len(code) {
			return errf("interpreter: pc out of range")
		}
		i := code[ci.pc]
		if traceExec {
			fmt.Printf("[trace] ciDepth=%d base=%d pc=%d top=%d %s A=%d B=%d C=%d\n",
				th.ciDepth, ci.base, ci.pc, th.top,
				bytecode.Op(i), bytecode.A(i), bytecode.B(i), bytecode.C(i))
		}
		ci.pc++

		switch bytecode.Op(i) {

		case bytecode.MOVE:
			setReg(th, ci, bytecode.A(i), reg(th, ci, bytecode.B(i)))

		case bytecode.LOADK:
			setReg(th, ci, bytecode.A(i), proto.Consts[bytecode.Bx(i)])

		case bytecode.LOADBOOL:
			setReg(th, ci, bytecode.A(i), value.BoolValue(bytecode.B(i) != 0))
			if bytecode.C(i) != 0 {
				ci.pc++
			}

		case bytecode.LOADNIL:
			a, b := bytecode.A(i), bytecode.B(i)
			for r := a; r <= b; r++ {
				setReg(th, ci, r, value.Nil)
			}

		case bytecode.GETUPVAL:
			uv := object.ClosureUpvalRef(st.arena, ci.Cl(), uint16(bytecode.B(i)))
			setReg(th, ci, bytecode.A(i), st.upvalGet(th, uv))

		case bytecode.SETUPVAL:
			uv := object.ClosureUpvalRef(st.arena, ci.Cl(), uint16(bytecode.B(i)))
			st.upvalSet(th, uv, reg(th, ci, bytecode.A(i)))

		case bytecode.GETGLOBAL:
			key := proto.Consts[bytecode.Bx(i)]
			gv := value.MakeGC(value.TagTable, st.globals)
			v, e := st.icGetTable(th, ci, ci.pc-1, gv, key)
			if e != nil {
				return e
			}
			ci = currentCI(th)
			proto = st.protoOf(ci)
			code = proto.Code
			setReg(th, ci, bytecode.A(i), v)

		case bytecode.SETGLOBAL:
			key := proto.Consts[bytecode.Bx(i)]
			gv := value.MakeGC(value.TagTable, st.globals)
			if e := st.icSetTable(th, ci, ci.pc-1, gv, key, reg(th, ci, bytecode.A(i))); e != nil {
				return e
			}
			ci = currentCI(th)
			proto = st.protoOf(ci)
			code = proto.Code
			st.safepoint(th, ci)

		case bytecode.GETTABLE:
			tbl := reg(th, ci, bytecode.B(i))
			key := rk(th, ci, proto, bytecode.C(i))
			v, e := st.icGetTable(th, ci, ci.pc-1, tbl, key)
			if e != nil {
				return st.enhanceIndexErr(e, ci, bytecode.B(i), tbl)
			}
			// __index handler may re-enter execute (append cis) → refresh the ci pointer
			ci = currentCI(th)
			proto = st.protoOf(ci)
			code = proto.Code
			setReg(th, ci, bytecode.A(i), v)

		case bytecode.SETTABLE:
			tbl := reg(th, ci, bytecode.A(i))
			key := rk(th, ci, proto, bytecode.B(i))
			val := rk(th, ci, proto, bytecode.C(i))
			if e := st.icSetTable(th, ci, ci.pc-1, tbl, key, val); e != nil {
				return st.enhanceIndexErr(e, ci, bytecode.A(i), tbl)
			}
			ci = currentCI(th)
			proto = st.protoOf(ci)
			code = proto.Code
			st.safepoint(th, ci)

		case bytecode.NEWTABLE:
			asz := bytecode.Fb2Int(uint32(bytecode.B(i)))
			hsz := bytecode.Fb2Int(uint32(bytecode.C(i)))
			// Lua 5.1 NEWTABLE's hsize is not necessarily a power of two after fb decoding; allocTable requires a power of two.
			t := st.allocTable(asz, roundUpPow2(hsz))
			setReg(th, ci, bytecode.A(i), value.MakeGC(value.TagTable, t))
			st.safepoint(th, ci)

		case bytecode.SELF:
			tbl := reg(th, ci, bytecode.B(i))
			setReg(th, ci, bytecode.A(i)+1, tbl)
			key := rk(th, ci, proto, bytecode.C(i))
			v, e := st.icGetTable(th, ci, ci.pc-1, tbl, key)
			if e != nil {
				return st.enhanceIndexErr(e, ci, bytecode.B(i), tbl)
			}
			ci = currentCI(th)
			proto = st.protoOf(ci)
			code = proto.Code
			setReg(th, ci, bytecode.A(i), v)

		case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV, bytecode.MOD, bytecode.POW:
			if e := st.doArith(th, ci, i); e != nil {
				return e
			}
			// __add and friends' handlers may re-enter execute → refresh
			ci = currentCI(th)
			proto = st.protoOf(ci)
			code = proto.Code

		case bytecode.UNM:
			b := reg(th, ci, bytecode.B(i))
			if value.IsNumber(b) {
				setReg(th, ci, bytecode.A(i), value.NumberValue(-value.AsNumber(b)))
				if profileEnabled {
					recordArithNumHit(&proto.IC[ci.pc-1])
				}
			} else if f, ok := st.toNumberCoerce(b); ok {
				setReg(th, ci, bytecode.A(i), value.NumberValue(-f))
				if profileEnabled {
					recordArithMetaHit(&proto.IC[ci.pc-1])
				}
			} else {
				h := st.metaFieldOfValue(b, "__unm")
				if h == value.Nil {
					return st.errWithName(ci, "perform arithmetic on", bytecode.B(i), b)
				}
				res, e := st.callMetaHandler(th, h, []value.Value{b, b}, 1)
				if e != nil {
					return e
				}
				ci = currentCI(th)
				proto = st.protoOf(ci)
				code = proto.Code
				setReg(th, ci, bytecode.A(i), res)
				if profileEnabled {
					recordArithMetaHit(&proto.IC[ci.pc-1])
				}
			}

		case bytecode.NOT:
			b := reg(th, ci, bytecode.B(i))
			setReg(th, ci, bytecode.A(i), value.BoolValue(!value.Truthy(b)))

		case bytecode.LEN:
			b := reg(th, ci, bytecode.B(i))
			switch value.Tag(b) {
			case value.TagString:
				n := object.StringLen(st.arena, value.GCRefOf(b))
				setReg(th, ci, bytecode.A(i), value.NumberValue(float64(n)))
			case value.TagTable:
				border := st.rawBorder(value.GCRefOf(b))
				setReg(th, ci, bytecode.A(i), value.NumberValue(float64(border)))
			default:
				return st.errWithName(ci, "get length of", bytecode.B(i), b)
			}

		case bytecode.CONCAT:
			if e := st.doConcat(th, ci, i); e != nil {
				return e
			}
			st.safepoint(th, ci)

		case bytecode.JMP:
			if bytecode.SBx(i) < 0 {
				if e := st.preempt(); e != nil {
					return e
				}
				if profileEnabled {
					st.bridge.OnBackEdgeID(proto, ci.protoID, ci.pc+int32(bytecode.SBx(i)), th == st.mainTh)
				}
			}
			ci.pc += int32(bytecode.SBx(i))

		case bytecode.EQ, bytecode.LT, bytecode.LE:
			// Fast-path inline: two numbers compared directly, zero function
			// calls, zero ci refresh (hot path in the loop preset `i < n`,
			// once per iteration, 05 §3.4).
			b := rk(th, ci, proto, bytecode.B(i))
			c := rk(th, ci, proto, bytecode.C(i))
			var res bool
			if value.IsNumber(b) && value.IsNumber(c) {
				x, y := value.AsNumber(b), value.AsNumber(c)
				switch bytecode.Op(i) {
				case bytecode.EQ:
					res = x == y
				case bytecode.LT:
					res = x < y
				default:
					res = x <= y
				}
				// Arithmetic IC double counting: LT/LE with two numbers takes
				// the fast path (02 §2.4 note: LT/LE's numHits do not
				// distinguish the number/string sub-branches, granularity loss
				// recorded in §9.2).
				// EQ carries no IC (02 §1.2 note 1).
				if profileEnabled && bytecode.Op(i) != bytecode.EQ {
					recordArithNumHit(&proto.IC[ci.pc-1])
				}
			} else {
				var e *LuaError
				res, e = st.doCompare(th, ci, i)
				if e != nil {
					return e
				}
				// __eq/__lt/__le handler may re-enter execute → refresh ci
				ci = currentCI(th)
				proto = st.protoOf(ci)
				code = proto.Code
				if profileEnabled && bytecode.Op(i) != bytecode.EQ {
					recordArithMetaHit(&proto.IC[ci.pc-1])
				}
			}
			if res != (bytecode.A(i) != 0) {
				ci.pc++
			}

		case bytecode.TEST:
			r := reg(th, ci, bytecode.A(i))
			if value.Truthy(r) != (bytecode.C(i) != 0) {
				ci.pc++
			}

		case bytecode.TESTSET:
			b := reg(th, ci, bytecode.B(i))
			if value.Truthy(b) == (bytecode.C(i) != 0) {
				setReg(th, ci, bytecode.A(i), b)
			} else {
				ci.pc++
			}

		case bytecode.CALL:
			next, e := st.doCall(th, ci, i)
			if e != nil {
				return e
			}
			if next != nil {
				ci = next
			} else {
				// host path: the host may internally re-enter execute (pcall
				// etc.) and change the frame depth, so the old ci pointer, which
				// before R2b-4 came from a relocatable segment, is refreshed to
				// the stable th.cur.
				ci = currentCI(th)
			}
			proto = st.protoOf(ci)
			code = proto.Code

		case bytecode.TAILCALL:
			next, e := st.doTailCall(th, ci, i)
			if e != nil {
				return e
			}
			if next != nil {
				ci = next
			} else {
				ci = currentCI(th)
			}
			proto = st.protoOf(ci)
			code = proto.Code
			// PJ10 gibbous tail-call dispatch: if the tail-callee proto has
			// installed GibbousCode (P3 wasm or P4 native), run it on the
			// tail-call frame we just entered. Mirrors doCall's gibbous
			// branch but done here (post-doTailCall) rather than inside
			// doTailCall, so the tail-call frame lifecycle (SetTailcall,
			// funcIdx = parent's FuncIdx) is untouched. code.Run's DoReturn
			// pops the tail-call frame and writes returns to funcIdx per
			// standard interp semantics; on OK we reload ci from
			// currentCI and continue execute() at the caller's next
			// instruction after CALL.
			//
			// nCcalls watermark (gibbousReentryCCallCap): a gibbous tail
			// callee that itself TAILCALLs re-enters Go per level (Run →
			// host.TailCall → executeFrom → here → Run ...), so unbounded
			// proper tail recursion — legal in PUC 5.1 — would trip
			// maxCCallDepth. Past the watermark, keep interpreting: the
			// interp TAILCALL is O(1) depth with zero Go re-entry.
			if profileEnabled && th == st.mainTh && !ci.Gibbous() &&
				st.nCcalls < gibbousReentryCCallCap {
				if gcode := st.bridge.GibbousCodeOf(proto); gcode != nil && isPJ10NativeCode(gcode) {
					ci.SetGibbous(true)
					th.reMirrorTop()
					baseByte := (th.stackBaseW + uint32(ci.base)) * 8
					status := gcode.Run(st.gibbousStack(), baseByte)
					th.syncCurFromSeg()
					if status != 0 {
						if st.gibbousPendingErr == nil {
							if gerr := gcode.PendingErr(); gerr != nil {
								st.gibbousPendingErr = &LuaError{Msg: "gibbous: " + gerr.Error()}
							} else {
								st.gibbousPendingErr = errf("gibbous: run failed (status=%d)", status)
							}
						}
						err := st.gibbousPendingErr
						st.gibbousPendingErr = nil
						if th.ciDepth > 0 && currentCI(th).Gibbous() {
							st.popCallInfo(th)
						}
						return err
					}
					// OK: DoReturn popped the tail-call frame + wrote
					// returns. Reload ci from segment; if we ran back
					// out of the entry frame, terminate.
					if th.ciDepth <= entryDepth {
						return nil
					}
					ci = currentCI(th)
					proto = st.protoOf(ci)
					code = proto.Code
				}
			}

		case bytecode.RETURN:
			next, terminate := st.doReturn(th, ci, i, entryDepth)
			if terminate {
				return nil
			}
			ci = next
			proto = st.protoOf(ci)
			code = proto.Code

		case bytecode.FORLOOP:
			a := bytecode.A(i)
			idx := value.AsNumber(reg(th, ci, a))
			step := value.AsNumber(reg(th, ci, a+2))
			limit := value.AsNumber(reg(th, ci, a+1))
			idx += step
			cont := false
			// PUC 5.1 lvm.c: `luai_numlt(0, step) ? idx<=limit :
			// limit<=idx` — step == 0 takes the DESCENDING branch, so
			// `for i=0,1,0` is zero iterations while `for i=1,0,0`
			// loops forever (issue #97: `step >= 0` here sent step=0
			// down the ascending branch, inverting both shapes vs the
			// oracle and vs the P4 native emit).
			if step > 0 {
				cont = idx <= limit
			} else {
				cont = idx >= limit
			}
			if cont {
				if e := st.preempt(); e != nil {
					return e
				}
				setReg(th, ci, a, value.NumberValue(idx))
				setReg(th, ci, a+3, value.NumberValue(idx))
				ci.pc += int32(bytecode.SBx(i))
				if profileEnabled {
					st.bridge.OnBackEdgeID(proto, ci.protoID, ci.pc, th == st.mainTh)
				}
			}

		case bytecode.FORPREP:
			a := bytecode.A(i)
			// Three-slot check: may go through string coercion (5.1 also runs tonumber for `for`, 07 §5.2)
			init, ok1 := st.toNumberCoerce(reg(th, ci, a))
			limit, ok2 := st.toNumberCoerce(reg(th, ci, a+1))
			step, ok3 := st.toNumberCoerce(reg(th, ci, a+2))
			if !ok1 {
				return errf("'for' initial value must be a number")
			}
			if !ok2 {
				return errf("'for' limit must be a number")
			}
			if !ok3 {
				return errf("'for' step must be a number")
			}
			setReg(th, ci, a, value.NumberValue(init-step))
			setReg(th, ci, a+1, value.NumberValue(limit))
			setReg(th, ci, a+2, value.NumberValue(step))
			ci.pc += int32(bytecode.SBx(i))

		case bytecode.TFORLOOP:
			// Call the iterator R(A)(R(A+1), R(A+2)); results land in R(A+3..A+2+C) (05 §10.2).
			// The iterator syncs its results via callLuaFromHost (Lua iterators go
			// host→Lua reentry; host iterators such as next are called directly host-side).
			a := bytecode.A(i)
			c := bytecode.C(i)
			iter := reg(th, ci, a)
			state := reg(th, ci, a+1)
			ctrl := reg(th, ci, a+2)
			results, e := st.callLuaFromHostNamed(th, iter, []value.Value{state, ctrl})
			if e != nil {
				// PUC getfuncname treats OP_TFORLOOP as a named call site: a
				// host iterator's arg error is named after R(A) (typically
				// "(for generator)", issue #133); the main loop has already done
				// ci.pc++, so TFORLOOP itself sits at ci.pc-1.
				return st.resolveArgError(e, ci, ci.pc-1, a)
			}
			ci = currentCI(th)
			proto = st.protoOf(ci)
			code = proto.Code
			for k := 0; k < c; k++ {
				v := value.Nil
				if k < len(results) {
					v = results[k]
				}
				setReg(th, ci, a+3+k, v)
			}
			if c >= 1 && len(results) >= 1 && results[0] != value.Nil {
				setReg(th, ci, a+2, results[0]) // control variable = first return value
				// fall through to the immediately following back-edge JMP
			} else {
				ci.pc++ // first value nil: skip the back edge, exit the loop
			}

		case bytecode.SETLIST:
			if e := st.doSetList(th, ci, i); e != nil {
				return e
			}
			st.safepoint(th, ci)

		case bytecode.CLOSE:
			st.closeUpvals(th, ci.base+bytecode.A(i))

		case bytecode.CLOSURE:
			cl := st.makeClosure(th, ci, i)
			setReg(th, ci, bytecode.A(i), value.MakeGC(value.TagFunction, cl))
			st.safepoint(th, ci)

		case bytecode.VARARG:
			if e := st.doVararg(th, ci, i); e != nil {
				return e
			}

		default:
			return errf("interpreter: unsupported opcode %s", bytecode.Op(i))
		}
	}
}

// toNumber converts a Value to float64; on success returns the value and true.
// number converts directly; string goes through ParseLuaNumber (07 §5.2, the
// single entry point shared by arithmetic / numeric for / tonumber).
func (st *State) toNumberCoerce(v value.Value) (float64, bool) {
	if value.IsNumber(v) {
		return value.AsNumber(v), true
	}
	if value.Tag(v) == value.TagString {
		return parseLuaNumberBytes(object.StringBytes(st.arena, value.GCRefOf(v)))
	}
	return 0, false
}

// Arithmetic helper. Fast path for two numbers; string coercion (07 §5.2);
// slow path for __add and other metamethods.
func (st *State) doArith(th *thread, ci *callInfo, i bytecode.Instruction) *LuaError {
	proto := st.protoOf(ci)
	b := rk(th, ci, proto, bytecode.B(i))
	c := rk(th, ci, proto, bytecode.C(i))
	// Fast path: two numbers computed directly (single comparison check, no
	// coercion overhead; 05 §4.1)
	if value.IsNumber(b) && value.IsNumber(c) {
		x, y := value.AsNumber(b), value.AsNumber(c)
		var r float64
		switch bytecode.Op(i) {
		case bytecode.ADD:
			r = x + y
		case bytecode.SUB:
			r = x - y
		case bytecode.MUL:
			r = x * y
		case bytecode.DIV:
			r = x / y
		case bytecode.MOD:
			r = x - math.Floor(x/y)*y
		case bytecode.POW:
			r = math.Pow(x, y)
		}
		setReg(th, ci, bytecode.A(i), value.NumberValue(r))
		if profileEnabled {
			recordArithNumHit(&proto.IC[ci.pc-1])
		}
		return nil
	}
	return st.doArithSlow(th, ci, i, b, c)
}

// doArithSlow: string coercion → metamethod → error with a name (split out of
// the fast path so doArith stays inlinable into the main loop).
func (st *State) doArithSlow(th *thread, ci *callInfo, i bytecode.Instruction, b, c value.Value) *LuaError {
	proto := st.protoOf(ci)
	x, okB := st.toNumberCoerce(b)
	y, okC := st.toNumberCoerce(c)
	if okB && okC {
		var r float64
		switch bytecode.Op(i) {
		case bytecode.ADD:
			r = x + y
		case bytecode.SUB:
			r = x - y
		case bytecode.MUL:
			r = x * y
		case bytecode.DIV:
			r = x / y
		case bytecode.MOD:
			r = x - math.Floor(x/y)*y
		case bytecode.POW:
			r = math.Pow(x, y)
		}
		setReg(th, ci, bytecode.A(i), value.NumberValue(r))
		// Any string coercion means "not a stable number" — record metaHits (02 §3.3 note 3)
		if profileEnabled {
			recordArithMetaHit(&proto.IC[ci.pc-1])
		}
		return nil
	}
	// Slow path: __add and other metamethods; when none exists, raise an error with a named description (09 §8.3)
	mmName := arithMetaName(bytecode.Op(i))
	h := st.metaFieldOfValue(b, mmName)
	if h == value.Nil {
		h = st.metaFieldOfValue(c, mmName)
	}
	if h == value.Nil {
		return st.arithErrWithName(ci, i, b, c)
	}
	res, e := st.arithMeta(th, mmName, b, c)
	if e != nil {
		return e
	}
	setReg(th, ci, bytecode.A(i), res)
	if profileEnabled {
		recordArithMetaHit(&proto.IC[ci.pc-1])
	}
	return nil
}

func arithMetaName(op bytecode.OpCode) string {
	switch op {
	case bytecode.ADD:
		return "__add"
	case bytecode.SUB:
		return "__sub"
	case bytecode.MUL:
		return "__mul"
	case bytecode.DIV:
		return "__div"
	case bytecode.MOD:
		return "__mod"
	case bytecode.POW:
		return "__pow"
	}
	return "__add"
}

// Comparison helper. Fast path for two numbers / two strings; slow path for the
// __eq/__lt/__le metamethods (07).
func (st *State) doCompare(th *thread, ci *callInfo, i bytecode.Instruction) (bool, *LuaError) {
	proto := st.protoOf(ci)
	b := rk(th, ci, proto, bytecode.B(i))
	c := rk(th, ci, proto, bytecode.C(i))
	switch bytecode.Op(i) {
	case bytecode.EQ:
		if st.rawEqual(b, c) {
			return true, nil
		}
		// __eq: only triggers when both operands are tables (or both userdata)
		// AND [both sides' metamethods are the same function] (5.1 get_compTM:
		// different handlers → false directly)
		if value.Tag(b) == value.TagTable && value.Tag(c) == value.TagTable {
			h := st.metaFieldOfValue(b, "__eq")
			h2 := st.metaFieldOfValue(c, "__eq")
			if value.Tag(h) == value.TagFunction && h == h2 {
				res, e := st.callMetaHandler(th, h, []value.Value{b, c}, 1)
				if e != nil {
					return false, e
				}
				return value.Truthy(res), nil
			}
		}
		return false, nil
	case bytecode.LT, bytecode.LE:
		if value.IsNumber(b) && value.IsNumber(c) {
			x, y := value.AsNumber(b), value.AsNumber(c)
			if bytecode.Op(i) == bytecode.LT {
				return x < y, nil
			}
			return x <= y, nil
		}
		if value.Tag(b) == value.TagString && value.Tag(c) == value.TagString {
			cmp := stringCompare(st, value.GCRefOf(b), value.GCRefOf(c))
			if bytecode.Op(i) == bytecode.LT {
				return cmp < 0, nil
			}
			return cmp <= 0, nil
		}
		// Metamethod slow path (07): __lt / __le; 5.1-specific: with no __le, fall back to not __lt(c, b)
		if bytecode.Op(i) == bytecode.LT {
			h := st.metaFieldOfValue(b, "__lt")
			if h == value.Nil {
				h = st.metaFieldOfValue(c, "__lt")
			}
			if value.Tag(h) == value.TagFunction {
				res, e := st.callMetaHandler(th, h, []value.Value{b, c}, 1)
				if e != nil {
					return false, e
				}
				return value.Truthy(res), nil
			}
		} else {
			h := st.metaFieldOfValue(b, "__le")
			if h == value.Nil {
				h = st.metaFieldOfValue(c, "__le")
			}
			if value.Tag(h) == value.TagFunction {
				res, e := st.callMetaHandler(th, h, []value.Value{b, c}, 1)
				if e != nil {
					return false, e
				}
				return value.Truthy(res), nil
			}
			// __le→__lt fallback: a <= b ⟺ not (b < a)
			h = st.metaFieldOfValue(b, "__lt")
			if h == value.Nil {
				h = st.metaFieldOfValue(c, "__lt")
			}
			if value.Tag(h) == value.TagFunction {
				res, e := st.callMetaHandler(th, h, []value.Value{c, b}, 1)
				if e != nil {
					return false, e
				}
				return !value.Truthy(res), nil
			}
		}
		// No metamethod: same type raises "two X values", different types raise "X with Y" (5.1)
		tb, tc := st.typeNameOf(b), st.typeNameOf(c)
		if tb == tc {
			return false, errf("attempt to compare two %s values", tb)
		}
		return false, errf("attempt to compare %s with %s", tb, tc)
	}
	return false, errf("interpreter: bad compare op")
}

func (st *State) rawEqual(a, b value.Value) bool {
	// Numbers must go through float comparison first: canonNaN bits compare equal but NaN ≠ NaN (IEEE); +0 == -0.
	if value.IsNumber(a) && value.IsNumber(b) {
		return value.AsNumber(a) == value.AsNumber(b)
	}
	return a == b
}
