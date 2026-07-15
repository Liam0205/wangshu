// describeReg — a P1-simplified getobjname (09 §8.3: local/global/field/method).
//
// Supplies "local 'x'" / "global 'f'" / "field 'k'" / "method 'm'" name
// descriptions for error messages; returns "" on miss (the caller falls
// back to the nameless form "a nil value").
package crescent

import (
	"fmt"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// localName returns the live local name that corresponds to register reg at
// pc (isomorphic to luaF_getlocalname: register r = the (r+1)-th live local,
// locals occupy contiguous low registers).
func localName(proto *bytecode.Proto, pc int32, reg int) string {
	n := reg + 1
	for i := range proto.LocVars {
		lv := &proto.LocVars[i]
		if lv.StartPC > pc {
			break
		}
		if pc < lv.EndPC || lv.EndPC == 0 && lv.StartPC <= pc {
			// Live (EndPC==0 means the scope only closes at the function end and
			// has not yet been backpatched; treat such cases as live).
			n--
			if n == 0 {
				return lv.Name
			}
		}
	}
	return ""
}

// describeReg finds a name description for a register operand.
//
// Order: ① live locals (LocVars); ② forward symbolic execution (isomorphic
// to the official ldebug.c symbexec): walk from the function head to the
// faulting pc, tracking last, "the instruction that last wrote reg", with
// **forward JMPs followed as taken** (skipped writes do not count); when a
// test-class instruction (TEST/TESTSET, etc.) hits reg, last lands on it
// (unnameable → nameless degradation, matching the official nameless report
// for `(aaa or aaa)()`). A naive reverse scan would run into "unexecuted"
// instructions skipped by a JMP and produce a wrong name.
func describeReg(proto *bytecode.Proto, pc int32, reg int) string {
	return describeRegDepth(proto, pc, reg, 0)
}

func describeRegDepth(proto *bytecode.Proto, pc int32, reg int, depth int) string {
	if depth > 4 {
		return ""
	}
	if name := localName(proto, pc, reg); name != "" {
		// Internal control variables ((for index), etc.) are not exposed.
		if name[0] == '(' {
			return ""
		}
		return fmt.Sprintf("local '%s'", name)
	}
	ins, ok := symbexec(proto, pc, reg)
	if !ok {
		return ""
	}
	switch bytecode.Op(ins) {
	case bytecode.MOVE:
		// A temporary is a copy of a local: follow through to the source
		// register (the CALL shape of `local f; f()`); the official code
		// restricts b < a (only follow "high temporary ← low local" copies).
		if b := bytecode.B(ins); b < bytecode.A(ins) {
			return describeRegDepth(proto, pc, b, depth+1)
		}
	case bytecode.GETGLOBAL:
		if name, ok := constStringAt(proto, bytecode.Bx(ins)); ok {
			return fmt.Sprintf("global '%s'", name)
		}
	case bytecode.GETUPVAL:
		// Outer variable captured by a closure (the OP_GETUPVAL branch of the
		// official getobjname).
		if idx := bytecode.B(ins); idx < len(proto.UpvalDescs) {
			if name := proto.UpvalDescs[idx].Name; name != "" {
				return fmt.Sprintf("upvalue '%s'", name)
			}
		}
	case bytecode.GETTABLE:
		if rk := bytecode.C(ins); bytecode.IsK(rk) {
			if name, ok := constStringAt(proto, bytecode.KIdx(rk)); ok {
				return fmt.Sprintf("field '%s'", name)
			}
		}
	case bytecode.SELF:
		if rk := bytecode.C(ins); bytecode.IsK(rk) {
			if name, ok := constStringAt(proto, bytecode.KIdx(rk)); ok {
				return fmt.Sprintf("method '%s'", name)
			}
		}
	}
	return ""
}

// symbexec forward-symbolically-executes up to lastpc and returns the
// instruction that last wrote reg (a named subset of the official symbexec:
// drops bytecode validity checks, keeping only last tracking and control
// flow).
func symbexec(proto *bytecode.Proto, lastpc int32, reg int) (bytecode.Instruction, bool) {
	last := int32(-1)
	for pc := int32(0); pc < lastpc && pc < int32(len(proto.Code)); pc++ {
		ins := proto.Code[pc]
		op := bytecode.Op(ins)
		a := bytecode.A(ins)
		switch op {
		case bytecode.LOADNIL:
			if a <= reg && reg <= bytecode.B(ins) {
				last = pc
			}
		case bytecode.TFORLOOP:
			if reg >= a+2 {
				last = pc
			}
		case bytecode.CALL, bytecode.TAILCALL:
			if reg >= a {
				last = pc
			}
		case bytecode.SELF:
			if reg == a || reg == a+1 {
				last = pc
			}
		case bytecode.FORLOOP, bytecode.FORPREP:
			if reg >= a && reg <= a+3 {
				last = pc
			}
			// The official code also runs FORLOOP/FORPREP through the JMP
			// jump logic; in the naming scenario the back edge (negative
			// offset) is not followed and never appears forward, so it is
			// omitted.
		case bytecode.JMP:
			dest := pc + 1 + int32(bytecode.SBx(ins))
			// A forward JMP that does not skip past lastpc is treated as taken
			// (skipped writes do not count).
			if pc < dest && dest <= lastpc {
				pc = dest - 1 // after the for-loop increment, = dest
			}
		case bytecode.CLOSURE:
			if a == reg {
				last = pc
			}
			// Precisely skip the upvalue pseudo-instructions (the official code
			// goes via p->p[bx]->nups; this implementation has codegen store the
			// pseudo-instruction count alongside CLOSURE in SubNUps, indexed by
			// the Protos subscript). Guessing the shape would swallow the real
			// MOVE/GETUPVAL after a 0-upvalue CLOSURE, losing the naming
			// information of the argument load.
			if idx := bytecode.Bx(ins); idx < len(proto.SubNUps) {
				pc += int32(proto.SubNUps[idx])
			}
		case bytecode.SETLIST:
			if bytecode.C(ins) == 0 {
				pc++ // skip the trailing raw batch-count word
			}
		default:
			// testAMode class (writes or marks A): MOVE/LOADK/LOADBOOL/GET*/
			// arithmetic/UNM/NOT/LEN/CONCAT/TEST/TESTSET/NEWTABLE/VARARG
			if opWritesA(op) && a == reg {
				last = pc
			}
		}
	}
	if last < 0 {
		return 0, false
	}
	return proto.Code[last], true
}

// opWritesA matches the official testAMode bit (the instruction
// "modifies/marks" register A).
func opWritesA(op bytecode.OpCode) bool {
	switch op {
	case bytecode.MOVE, bytecode.LOADK, bytecode.LOADBOOL, bytecode.LOADNIL,
		bytecode.GETUPVAL, bytecode.GETGLOBAL, bytecode.GETTABLE,
		bytecode.NEWTABLE, bytecode.ADD, bytecode.SUB, bytecode.MUL,
		bytecode.DIV, bytecode.MOD, bytecode.POW, bytecode.UNM,
		bytecode.NOT, bytecode.LEN, bytecode.CONCAT,
		bytecode.TEST, bytecode.TESTSET, bytecode.VARARG:
		return true
	}
	return false
}

// constStringAt fetches the string literal in constant-pool slot k (the
// original text via StringLits, the Compile-time form; after loading, Consts
// is already a GCRef, so reading from StringLits is the most robust).
func constStringAt(proto *bytecode.Proto, k int) (string, bool) {
	if k < len(proto.StringLitIdx) && proto.StringLitIdx[k] >= 0 {
		return proto.StringLits[proto.StringLitIdx[k]], true
	}
	return "", false
}

// callSiteFuncName mirrors PUC getfuncname/getobjname for luaL_argerror
// (issue #133): derive the callee's NAME as seen at the caller's call
// site. Unlike describeReg it does not filter compiler-internal "(...)"
// locals — "(for generator)" is a legal PUC arg-error name — and it
// returns name and namewhat separately ("method" drives the self-arg
// decrement in resolveArgError).
func callSiteFuncName(proto *bytecode.Proto, pc int32, reg int) (name, namewhat string) {
	return callSiteFuncNameDepth(proto, pc, reg, 0)
}

func callSiteFuncNameDepth(proto *bytecode.Proto, pc int32, reg int, depth int) (string, string) {
	if depth > 4 {
		return "", ""
	}
	if n := localName(proto, pc, reg); n != "" {
		return n, "local"
	}
	ins, ok := symbexec(proto, pc, reg)
	if !ok {
		return "", ""
	}
	switch bytecode.Op(ins) {
	case bytecode.MOVE:
		if b := bytecode.B(ins); b < bytecode.A(ins) {
			return callSiteFuncNameDepth(proto, pc, b, depth+1)
		}
	case bytecode.GETGLOBAL:
		if n, ok := constStringAt(proto, bytecode.Bx(ins)); ok {
			return n, "global"
		}
	case bytecode.GETUPVAL:
		if idx := bytecode.B(ins); idx < len(proto.UpvalDescs) {
			if n := proto.UpvalDescs[idx].Name; n != "" {
				return n, "upvalue"
			}
		}
	case bytecode.GETTABLE:
		if rk := bytecode.C(ins); bytecode.IsK(rk) {
			if n, ok := constStringAt(proto, bytecode.KIdx(rk)); ok {
				return n, "field"
			}
		}
	case bytecode.SELF:
		if rk := bytecode.C(ins); bytecode.IsK(rk) {
			if n, ok := constStringAt(proto, bytecode.KIdx(rk)); ok {
				return n, "method"
			}
		}
	}
	return "", ""
}

// resolveArgError rewrites a host-raised arg error (NewArgError) with
// the caller-derived function name, mirroring PUC luaL_argerror +
// getfuncname: the name in "bad argument #N to 'name'" comes from
// getobjname on the caller's call operand, NOT from the callee's own
// identity (a local alias `local r = coroutine.resume; r(nil)` blames
// 'r'; oracle diff fuzz corpus e8534c580042ec44). "method" call sites
// do not count self (narg-1); the self argument itself being bad
// becomes the "calling 'X' on bad self" form. callPC is the pc of the
// CALL/TAILCALL/TFORLOOP instruction in the caller frame (conventions
// differ per site: the interpreter loop has already incremented ci.pc,
// gibbous helpers receive the raw pc); funcReg is the caller-frame
// register holding the callee. Resolution happens once, at the
// innermost Lua call boundary; errors crossing only host-to-host
// boundaries (pcall, sort comparators, metamethod handlers) keep the
// C-caller fallback '?', matching PUC.
func (st *State) resolveArgError(e *LuaError, ci *callInfo, callPC int32, funcReg int) *LuaError {
	if e == nil || e == errYieldSentinel || e.argNarg == 0 || e.annotated {
		return e
	}
	narg := e.argNarg
	e.argNarg = 0
	name, namewhat := callSiteFuncName(st.protoOf(ci), callPC, funcReg)
	if namewhat == "method" {
		narg--
		if narg == 0 {
			e.Msg = fmt.Sprintf("calling '%s' on bad self (%s)", name, e.argExtra)
			return e
		}
	}
	if name == "" {
		name = "?"
	}
	e.Msg = fmt.Sprintf("bad argument #%d to '%s' (%s)", narg, name, e.argExtra)
	return e
}

// errWithName builds a type error with a name description (5.1 format:
// "attempt to <verb> <name> (a <type> value)"; nameless falls back to
// "attempt to <verb> a <type> value").
func (st *State) errWithName(ci *callInfo, verb string, rkOperand int, v value.Value) *LuaError {
	name := ""
	if !bytecode.IsK(rkOperand) {
		// pc-1: the faulting instruction itself (the main loop has already
		// incremented).
		name = describeReg(st.protoOf(ci), ci.pc-1, rkOperand)
	}
	if name != "" {
		return errf("attempt to %s %s (a %s value)", verb, name, st.typeNameOf(v))
	}
	return errf("attempt to %s a %s value", verb, st.typeNameOf(v))
}

// arithErrWithName finds the faulting operand for an arithmetic error (b
// before c: whichever cannot be converted to a number).
func (st *State) arithErrWithName(ci *callInfo, i bytecode.Instruction, b, c value.Value) *LuaError {
	badV := b
	badRK := bytecode.B(i)
	if value.IsNumber(b) || coercibleToNumber(st, b) {
		badV = c
		badRK = bytecode.C(i)
	}
	return st.errWithName(ci, "perform arithmetic on", badRK, badV)
}

// coercibleToNumber decides whether v can be converted to a number via 5.1
// arithmetic coercion (the string form).
func coercibleToNumber(st *State, v value.Value) bool {
	if value.Tag(v) != value.TagString {
		return false
	}
	_, ok := parseLuaNumberBytes(object.StringBytes(st.arena, value.GCRefOf(v)))
	return ok
}

// enhanceIndexErr adds a name description to an "attempt to index a X value"
// error (when the faulting object is the obj held in register reg). Other
// errors (e.g. errors inside an __index handler) are passed through
// unchanged.
func (st *State) enhanceIndexErr(e *LuaError, ci *callInfo, reg int, obj value.Value) *LuaError {
	if e == errYieldSentinel || e.annotated {
		return e
	}
	plain := fmt.Sprintf("attempt to index a %s value", st.typeNameOf(obj))
	if e.Msg != plain {
		return e
	}
	if name := describeReg(st.protoOf(ci), ci.pc-1, reg); name != "" {
		e.Msg = fmt.Sprintf("attempt to index %s (a %s value)", name, st.typeNameOf(obj))
	}
	return e
}
