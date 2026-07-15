// expdesc — deferred-materialization expression description (04 §5.2), isomorphic
// with Lua 5.1 lcode.c.
//
// An expression is first computed into an expDesc (describing "where this value is
// now, how to fetch it"), and is only discharged when it must land in a
// register/serve as an RK/participate in a jump. Short-circuit logic (and/or) and
// comparisons simultaneously drive the t/f jump chains.
package compile

import (
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// expKind lists the deferred-materialization forms of an expression (04 §5.2).
type expKind uint8

const (
	eVoid      expKind = iota // valueless placeholder
	eNil                      // literal nil (no instruction emitted yet)
	eTrue                     // literal true
	eFalse                    // literal false
	eKNum                     // number literal (not yet in constant pool), value in nval
	eK                        // already in constant pool, info = K index
	eLocal                    // local variable, info = register number
	eUpval                    // upvalue, info = upvalue index
	eGlobal                   // global, info = K index of the name
	eIndexed                  // t[k]: info = table register, aux = key RK
	eJmp                      // comparison (EQ/LT/LE) emitted, info = JMP instruction pc (subsequent JMP pending/chained)
	eRelocable                // instruction whose result register is undetermined (GETGLOBAL/GETTABLE/NEWTABLE/CALL single value), info = instruction pc
	eNonReloc                 // result already landed in some register, info = register number
	eCall                     // function call, info = CALL instruction pc (C can be changed)
	eVararg                   // ... , info = VARARG instruction pc
)

// expDesc is the expression descriptor (04 §5.2).
type expDesc struct {
	k    expKind
	info int     // meaning follows k (see above)
	aux  int     // key RK of eIndexed
	nval float64 // number of eKNum
	tJmp int     // patch chain to jump when true (NoJump=empty)
	fJmp int     // patch chain to jump when false
}

func newExp(k expKind, info int) expDesc {
	return expDesc{k: k, info: info, tJmp: NoJump, fJmp: NoJump}
}

func (e *expDesc) hasJumps() bool { return e.tJmp != NoJump || e.fJmp != NoJump }

// dischargeVars translates EGlobal/EUpval/ELocal/EIndexed into a "fetchable form"
// (may still be ERelocable).
//
// 04 §5.3: corresponds to Lua 5.1 luaK_dischargevars.
func (fs *funcState) dischargeVars(line int32, e *expDesc) {
	switch e.k {
	case eLocal:
		e.k = eNonReloc
	case eUpval:
		pc := fs.emitABC(line, bytecode.GETUPVAL, 0, e.info, 0)
		e.k = eRelocable
		e.info = pc
	case eGlobal:
		pc := fs.emitABx(line, bytecode.GETGLOBAL, 0, e.info)
		e.k = eRelocable
		e.info = pc
	case eIndexed:
		// First return the register occupied by the RK (if any), then emit GETTABLE.
		if !bytecode.IsK(e.aux) {
			fs.freeReg(e.aux)
		}
		fs.freeReg(e.info)
		pc := fs.emitABC(line, bytecode.GETTABLE, 0, e.info, e.aux)
		e.k = eRelocable
		e.info = pc
	case eVararg, eCall:
		fs.setOneRet(e)
	}
}

// setOneRet sets a Call/Vararg expression to take only 1 return value.
func (fs *funcState) setOneRet(e *expDesc) {
	switch e.k {
	case eCall:
		// CALL's C = number of values taken + 1. 1 value ⟹ C=2.
		ins := fs.proto.Code[e.info]
		fs.proto.Code[e.info] = bytecode.SetC(ins, 2)
		e.k = eNonReloc
		e.info = bytecode.A(ins) // call result lands in R(A)
	case eVararg:
		// VARARG's B = number of values taken + 1. 1 value ⟹ B=2.
		ins := fs.proto.Code[e.info]
		fs.proto.Code[e.info] = bytecode.SetB(ins, 2)
		e.k = eRelocable
	}
}

// setReturns sets the number of return values of a Call/Vararg expression (used for
// the last position multi-value in an explist).
// nResults < 0 means "to top" (B/C=0).
func (fs *funcState) setReturns(e *expDesc, nResults int) {
	switch e.k {
	case eCall:
		c := nResults + 1
		if nResults < 0 {
			c = 0
		}
		fs.proto.Code[e.info] = bytecode.SetC(fs.proto.Code[e.info], c)
	case eVararg:
		b := nResults + 1
		if nResults < 0 {
			b = 0
		}
		fs.proto.Code[e.info] = bytecode.SetB(fs.proto.Code[e.info], b)
	}
}

// openMultiRet expands a last-position multi-value source (eCall/eVararg) into
// nResults values (<0 = to top), returning true when e is indeed a multi-value
// source (the caller takes the multi-value branch).
//
// Isomorphic logic single-point consolidation — historically stmtReturn /
// compileArgList / exprTable were hand-written in three places, and the same-family
// bug occurred three times (eCall mistakenly SetA overwriting "the slot the function
// is in", so return values land in the wrong register):
//   - eCall: CALL's A is already fnReg (the result landing spot), must never be overwritten;
//   - eVararg: when VARARG is emitted A=0 as placeholder, here we uniformly backfill A=freereg (multi-value start).
func (fs *funcState) openMultiRet(e *expDesc, nResults int) bool {
	switch e.k {
	case eCall:
		fs.setReturns(e, nResults)
		return true
	case eVararg:
		fs.setReturns(e, nResults)
		fs.proto.Code[e.info] = bytecode.SetA(fs.proto.Code[e.info], fs.freereg)
		return true
	}
	return false
}

// dischargeToAnyReg materializes e into some register (in place if already there).
func (fs *funcState) dischargeToAnyReg(line int32, e *expDesc) {
	if e.k != eNonReloc {
		fs.reserveRegs(line, 1)
		fs.dischargeToReg(line, e, fs.freereg-1)
	}
}

// exp2AnyReg materializes e into some register and returns the register number.
func (fs *funcState) exp2AnyReg(line int32, e *expDesc) int {
	fs.dischargeVars(line, e)
	if e.k == eNonReloc {
		if !e.hasJumps() {
			return e.info
		}
		if e.info >= fs.nactvar { // is temporary: can materialize in the original register
			fs.exp2reg(line, e, e.info)
			return e.info
		}
	}
	fs.exp2NextReg(line, e)
	return e.info
}

// exp2NextReg lands e in freereg and bumps the watermark +1 (becomes ENonReloc).
func (fs *funcState) exp2NextReg(line int32, e *expDesc) {
	fs.dischargeVars(line, e)
	fs.freeExp(e)
	fs.reserveRegs(line, 1)
	fs.exp2reg(line, e, fs.freereg-1)
}

// dischargeToReg lands e in the specified register reg (does not handle t/f jump chains).
func (fs *funcState) dischargeToReg(line int32, e *expDesc, reg int) {
	fs.dischargeVars(line, e)
	switch e.k {
	case eNil:
		fs.emitABC(line, bytecode.LOADNIL, reg, reg, 0)
	case eTrue:
		fs.emitABC(line, bytecode.LOADBOOL, reg, 1, 0)
	case eFalse:
		fs.emitABC(line, bytecode.LOADBOOL, reg, 0, 0)
	case eK:
		fs.emitABx(line, bytecode.LOADK, reg, e.info)
	case eKNum:
		k := fs.numK(line, e.nval)
		fs.emitABx(line, bytecode.LOADK, reg, k)
	case eRelocable:
		fs.proto.Code[e.info] = bytecode.SetA(fs.proto.Code[e.info], reg)
	case eNonReloc:
		if reg != e.info {
			fs.emitABC(line, bytecode.MOVE, reg, e.info, 0)
		}
	case eJmp:
		// leave for exp2reg to handle (no instruction emitted here)
		return
	default:
		// eVoid should not reach here
	}
	e.k = eNonReloc
	e.info = reg
}

// exp2reg materializes a "jump-carrying expression" into a specific register (04 §5.7).
func (fs *funcState) exp2reg(line int32, e *expDesc, reg int) {
	fs.dischargeToReg(line, e, reg)
	if e.k == eJmp { // the comparison's own JMP counts into the t chain
		fs.concat(&e.tJmp, e.info)
	}
	if e.hasJumps() {
		var pf, pt, final int
		if needValue(fs, e.tJmp) || needValue(fs, e.fJmp) {
			fj := NoJump
			if e.k != eJmp {
				fj = fs.jump(line)
			}
			pf = fs.codeLoadBool(line, reg, 0, 1) // false and skip the next one
			pt = fs.codeLoadBool(line, reg, 1, 0)
			fs.patchToHere(fj)
			final = fs.getLabel()
		} else {
			pf = NoJump
			pt = NoJump
			final = fs.getLabel()
		}
		fs.patchListAux(e.fJmp, final, reg, pf)
		fs.patchListAux(e.tJmp, final, reg, pt)
	}
	e.fJmp, e.tJmp = NoJump, NoJump
	e.k = eNonReloc
	e.info = reg
}

// codeLoadBool emits LOADBOOL R(A)=B, if C!=0 then pc++ (returns pc).
func (fs *funcState) codeLoadBool(line int32, a, b, c int) int {
	return fs.emitABC(line, bytecode.LOADBOOL, a, b, c)
}

// needValue determines whether the chain contains a "non-TESTSET" JMP (those need
// LOADBOOL as a fallback to land the value).
func needValue(fs *funcState, list int) bool {
	for ; list != NoJump; list = fs.getJump(list) {
		if list == 0 {
			return true
		}
		ins := fs.proto.Code[list-1]
		if bytecode.Op(ins) != bytecode.TESTSET {
			return true
		}
	}
	return false
}

// patchTestReg: if the instruction before list is a TESTSET, set its A=reg and
// backfill the jump to vtarget; otherwise degrade the TESTSET to TEST and backfill
// the jump to dtarget (04 §5.4).
//
// Returns true meaning handled (this JMP is wired to vtarget), false meaning
// degraded (wired to dtarget).
func (fs *funcState) patchTestReg(node, reg int) bool {
	if node == 0 {
		return false
	}
	ctrl := node - 1
	ins := fs.proto.Code[ctrl]
	if bytecode.Op(ins) != bytecode.TESTSET {
		return false
	}
	if reg != bytecode.NoRegister && reg != bytecode.B(ins) {
		fs.proto.Code[ctrl] = bytecode.SetA(ins, reg)
	} else {
		// degrade to TEST: A=B (source register), C unchanged
		fs.proto.Code[ctrl] = bytecode.EncodeABC(bytecode.TEST,
			bytecode.B(ins), 0, bytecode.C(ins))
	}
	return true
}

// patchListAux traverses the chain: for each node uses patchTestReg(node, reg) to
// decide whether to go to vtarget or dtarget.
func (fs *funcState) patchListAux(list, vtarget, reg, dtarget int) {
	for list != NoJump {
		nxt := fs.getJump(list)
		if fs.patchTestReg(list, reg) {
			fs.fixJump(0, list, vtarget)
		} else {
			fs.fixJump(0, list, dtarget)
		}
		list = nxt
	}
}

// exp2RK tries to fold e into RK form (returning the RK operand); otherwise
// materializes it into a register and returns the register number (04 §5.3).
//
// Note: nil/true/false do not enter the constant pool (02 §5); when going through RK
// they land in a register (LOADNIL/LOADBOOL).
func (fs *funcState) exp2RK(line int32, e *expDesc) int {
	fs.exp2Val(line, e)
	switch e.k {
	case eK:
		if e.info < bytecode.MaxK {
			return e.info + bytecode.MaxK
		}
	case eKNum:
		k := fs.numK(line, e.nval)
		if k < bytecode.MaxK {
			e.k = eK
			e.info = k
			return k + bytecode.MaxK
		}
		e.k = eK
		e.info = k
	}
	return fs.exp2AnyReg(line, e)
}

// exp2Val: if e carries a pending jump chain, merge and materialize first (before
// exp2RK / exp2AnyReg).
func (fs *funcState) exp2Val(line int32, e *expDesc) {
	if e.hasJumps() {
		fs.exp2AnyReg(line, e)
	} else {
		fs.dischargeVars(line, e)
	}
}

// goIfTrue: if e is true then continue, if false then jump (chain the jump into
// e.fJmp). Works with 04 §5.6 short-circuit.
//
// Strictly aligned with 5.1 luaK_goiftrue: VK/VKNUM/VTRUE are constant-true so
// don't jump; VFALSE always jumps (the short-circuit value is exactly false,
// LOADBOOL materializes correctly); VNIL falls to the default jumpOnCond (TESTSET
// preserves the original value — `nil and 2` must return nil, LOADBOOL would wrongly
// produce false).
func (fs *funcState) goIfTrue(line int32, e *expDesc) {
	fs.dischargeVars(line, e)
	var pc int
	switch e.k {
	case eK, eKNum, eTrue:
		pc = NoJump
	case eFalse:
		pc = fs.jump(line)
	case eJmp:
		fs.invertJmp(e)
		pc = e.info
	default:
		pc = fs.jumpOnCond(line, e, 0)
	}
	fs.concat(&e.fJmp, pc)
	fs.patchToHere(e.tJmp)
	e.tJmp = NoJump
}

// goIfFalse: if e is false then continue, if true then jump (chain the jump into
// e.tJmp).
//
// Strictly aligned with 5.1 luaK_goiffalse: VNIL/VFALSE are constant-false so don't
// jump; VTRUE always jumps (the short-circuit value is exactly true); VK/VKNUM fall
// to the default jumpOnCond (TESTSET preserves the original value — `1 or 2` must
// return 1 rather than true).
func (fs *funcState) goIfFalse(line int32, e *expDesc) {
	fs.dischargeVars(line, e)
	var pc int
	switch e.k {
	case eNil, eFalse:
		pc = NoJump
	case eTrue:
		pc = fs.jump(line)
	case eJmp:
		pc = e.info
	default:
		pc = fs.jumpOnCond(line, e, 1)
	}
	fs.concat(&e.tJmp, pc)
	fs.patchToHere(e.fJmp)
	e.fJmp = NoJump
}

// invertJmp: flips the expected boolean of a comparison instruction (used for not / goIfTrue).
func (fs *funcState) invertJmp(e *expDesc) {
	pc := e.info
	ctrl := pc - 1
	ins := fs.proto.Code[ctrl]
	op := bytecode.Op(ins)
	a := bytecode.A(ins)
	fs.proto.Code[ctrl] = bytecode.EncodeABC(op, 1-a, bytecode.B(ins), bytecode.C(ins))
}

// jumpOnCond emits TEST/TESTSET + JMP, returning the JMP's pc; cond=0 jump when false, 1 jump when true.
func (fs *funcState) jumpOnCond(line int32, e *expDesc, cond int) int {
	if e.k == eRelocable {
		ins := fs.proto.Code[e.info]
		if bytecode.Op(ins) == bytecode.NOT {
			// undo that NOT, change to TEST with negated cond (aligned with Lua 5.1 jumponcond optimization)
			fs.proto.Code = fs.proto.Code[:e.info]
			fs.proto.LineInfo = fs.proto.LineInfo[:e.info]
			fs.proto.IC = fs.proto.IC[:e.info]
			return fs.condJump(line, bytecode.TEST, bytecode.B(ins), 0, 1-cond)
		}
	}
	fs.dischargeToAnyReg(line, e)
	fs.freeExp(e)
	return fs.condJump(line, bytecode.TESTSET, bytecode.NoRegister, e.info, cond)
}

// condJump emits one comparison-type instruction + JMP, returning the JMP pc.
func (fs *funcState) condJump(line int32, op bytecode.OpCode, a, b, c int) int {
	fs.emitABC(line, op, a, b, c)
	return fs.jump(line)
}
