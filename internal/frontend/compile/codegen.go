// codegen — the main AST → bytecode.Proto traversal (04 §5-§9).
//
// expr path: expr() returns an expDesc; deferred materialization is driven
// on demand by the caller via exp2NextReg / exp2RK / goIfTrue etc.
// stmt path: stmt() emits instructions directly and maintains the freereg /
// nactvar invariants.
package compile

import (
	"math"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/ast"
)

// resolveName resolves a NameExpr along the lexical chain, returning an
// expDesc and its match kind (local/upval/global).
//
// 04 §8.4 lexical scope-chain lookup: this function's locals → already
// captured upvalues → outer chain (following prev) → global fall-through.
func (fs *funcState) resolveName(line int32, name string) expDesc {
	// 1) local of this function
	if r := fs.findLocal(name); r >= 0 {
		return newExp(eLocal, r)
	}
	// 2) already-registered upvalue
	if u := fs.findUpval(name); u >= 0 {
		return newExp(eUpval, u)
	}
	// 3) along the outer chain
	if fs.prev != nil {
		outer := fs.prev.resolveName(line, name)
		switch outer.k {
		case eLocal:
			// mark the outer block hasUpval so it CLOSEs on exit
			fs.prev.markUpvalCapture(outer.info)
			idx := fs.addUpval(line, name, true, uint8(outer.info))
			return newExp(eUpval, idx)
		case eUpval:
			idx := fs.addUpval(line, name, false, uint8(outer.info))
			return newExp(eUpval, idx)
		case eGlobal:
			// global fall-through
		}
	}
	// 4) global
	k := fs.strK(line, name)
	return newExp(eGlobal, k)
}

// markUpvalCapture marks the block holding local reg (and all inner blocks)
// hasUpval (04 §6.1 / §8.4).
func (fs *funcState) markUpvalCapture(reg int) {
	for b := fs.bl; b != nil; b = b.prev {
		if reg >= b.nactvarSnap {
			b.hasUpval = true
			return
		}
	}
}

// expr compiles an ast.Expr into an expDesc (deferred materialization).
func (fs *funcState) expr(node ast.Expr) expDesc {
	switch e := node.(type) {
	case *ast.NilExpr:
		return newExp(eNil, 0)
	case *ast.TrueExpr:
		return newExp(eTrue, 0)
	case *ast.FalseExpr:
		return newExp(eFalse, 0)
	case *ast.NumberExpr:
		exp := newExp(eKNum, 0)
		exp.nval = e.Val
		return exp
	case *ast.StringExpr:
		k := fs.strK(e.Line, e.Val)
		return newExp(eK, k)
	case *ast.VarargExpr:
		if !fs.isVararg {
			raise(fs, e.Line, "cannot use '...' outside a vararg function")
		}
		// LUA_COMPAT_VARARG: once the body uses `...` the implicit arg table
		// is no longer needed (upstream lparser: fs->f->is_vararg &=
		// ~VARARG_NEEDSARG; the "arg" local still occupies a register, its
		// value left nil).
		fs.proto.NeedsArg = false
		pc := fs.emitABC(e.Line, bytecode.VARARG, 0, 1, 0)
		return newExp(eVararg, pc)
	case *ast.NameExpr:
		return fs.resolveName(e.Line, e.Name)
	case *ast.ParenExpr:
		// Parentheses force a single value: collapse an inner Call/Vararg
		// into single-value form (04 §9.4).
		inner := fs.expr(e.E)
		fs.dischargeVars(e.Line, &inner)
		return inner
	case *ast.IndexExpr:
		return fs.exprIndex(e)
	case *ast.CallExpr:
		return fs.exprCall(e)
	case *ast.MethodCallExpr:
		return fs.exprMethodCall(e)
	case *ast.BinExpr:
		return fs.exprBin(e)
	case *ast.UnExpr:
		return fs.exprUn(e)
	case *ast.FuncExpr:
		return fs.exprFunc(e)
	case *ast.TableExpr:
		return fs.exprTable(e)
	}
	raise(fs, 0, "compile: unsupported expr node %T", node)
	return expDesc{}
}

// exprIndex compiles t[k] / t.field.
func (fs *funcState) exprIndex(e *ast.IndexExpr) expDesc {
	obj := fs.expr(e.Obj)
	tableReg := fs.exp2AnyReg(e.Line, &obj)
	key := fs.expr(e.Key)
	rk := fs.exp2RK(e.Line, &key)
	exp := newExp(eIndexed, tableReg)
	exp.aux = rk
	return exp
}

// exprCall compiles f(args...); when the last arg is multi-value, sets B=0
// (up to top).
func (fs *funcState) exprCall(e *ast.CallExpr) expDesc {
	fnReg := fs.freereg
	fnExp := fs.expr(e.Fn)
	fs.exp2NextReg(e.Line, &fnExp)
	nargs := fs.compileArgList(e.Args, e.Line)
	b := nargs + 1
	if nargs < 0 { // last arg is multi-value
		b = 0
	}
	pc := fs.emitABC(e.Line, bytecode.CALL, fnReg, b, 2) // C=2 default single value, may be changed later
	fs.freereg = fnReg + 1                               // the call result occupies R(fnReg), 1 slot by default
	if fs.calleeIsMathIntrinsic(e.Fn) {
		fs.proto.IntrinsicCallPCs = append(fs.proto.IntrinsicCallPCs, int32(pc))
	}
	return expDesc{k: eCall, info: pc, tJmp: NoJump, fJmp: NoJump}
}

// calleeIsMathIntrinsic reports whether a CALL's callee expression names a
// recognized math intrinsic (issue #77): a direct `math.<name>` field
// access, or a bare name bound to one via `local f = math.<name>`
// (tracked in localAliasAsts). Only the syntactic shape is checked —
// `math` being shadowed / reassigned at run time is caught by the
// segment's runtime intrinsic-identity guard, so this only feeds the
// CALL-density promotion heuristic (a false positive risks an
// unprofitable promotion, never a wrong result).
func (fs *funcState) calleeIsMathIntrinsic(fn ast.Expr) bool {
	switch e := fn.(type) {
	case *ast.IndexExpr:
		return isMathIntrinsicIndex(e)
	case *ast.NameExpr:
		if rhs, ok := fs.localAliasAsts[e.Name]; ok {
			if idx, ok := rhs.(*ast.IndexExpr); ok {
				return isMathIntrinsicIndex(idx)
			}
		}
	}
	return false
}

// isMathIntrinsicIndex reports whether an IndexExpr is `math.<name>` with
// <name> in bytecode.MathIntrinsicNames (dot-field access parses to an
// IndexExpr with a StringExpr key).
func isMathIntrinsicIndex(idx *ast.IndexExpr) bool {
	obj, ok := idx.Obj.(*ast.NameExpr)
	if !ok || obj.Name != "math" {
		return false
	}
	key, ok := idx.Key.(*ast.StringExpr)
	if !ok {
		return false
	}
	return bytecode.MathIntrinsicNames[key.Val]
}

// exprMethodCall compiles obj:m(args) — SELF + CALL.
func (fs *funcState) exprMethodCall(e *ast.MethodCallExpr) expDesc {
	baseReg := fs.freereg
	recv := fs.expr(e.Recv)
	fs.exp2NextReg(e.Line, &recv) // R(baseReg) = obj
	// method name goes through an RK constant
	method := newExp(eK, fs.strK(e.Line, e.Method))
	rk := fs.exp2RK(e.Line, &method)
	fs.emitABC(e.Line, bytecode.SELF, baseReg, baseReg, rk)
	fs.reserveRegs(e.Line, 1) // SELF additionally occupies R(baseReg+1) (self)
	nargs := fs.compileArgList(e.Args, e.Line)
	b := nargs + 1 + 1 // self + nargs
	if nargs < 0 {
		b = 0
	}
	pc := fs.emitABC(e.Line, bytecode.CALL, baseReg, b, 2)
	fs.freereg = baseReg + 1
	return expDesc{k: eCall, info: pc, tJmp: NoJump, fJmp: NoJump}
}

// compileArgList lays args into consecutive registers; returns -1 when the
// last arg is multi-value, otherwise the fixed count.
func (fs *funcState) compileArgList(args []ast.Expr, line int32) int {
	n := len(args)
	if n == 0 {
		return 0
	}
	for i := 0; i < n-1; i++ {
		ai := fs.expr(args[i])
		fs.exp2NextReg(line, &ai)
	}
	last := fs.expr(args[n-1])
	if fs.openMultiRet(&last, -1) {
		return -1
	}
	fs.exp2NextReg(line, &last)
	return n
}

// exprBin compiles a binary expression; arithmetic folds, comparisons go
// through EQ/LT/LE, logicals go through short-circuit.
func (fs *funcState) exprBin(e *ast.BinExpr) expDesc {
	switch e.Op {
	case ast.OpAnd:
		l := fs.expr(e.L)
		fs.goIfTrue(e.Line, &l) // if l is false, jump (linked into fJmp); if true, continue (fall into right subexpr)
		r := fs.expr(e.R)
		// Match Lua 5.1 luaK_posfix(OPR_AND): first dischargeVars(e2) to
		// collapse VCALL/VVARARG into single-value form — otherwise an
		// eCall carrying a jump chain would be misrouted through
		// adjustExprList's multi-value branch and the jump chain would
		// never be patched (JMP sBx=-1 infinite loop).
		fs.dischargeVars(e.Line, &r)
		fs.concat(&r.fJmp, l.fJmp)
		return r
	case ast.OpOr:
		l := fs.expr(e.L)
		fs.goIfFalse(e.Line, &l)
		r := fs.expr(e.R)
		fs.dischargeVars(e.Line, &r)
		fs.concat(&r.tJmp, l.tJmp)
		return r
	case ast.OpEq, ast.OpNe, ast.OpLt, ast.OpLe, ast.OpGt, ast.OpGe:
		return fs.exprCompare(e)
	case ast.OpConcat:
		return fs.exprConcat(e)
	}
	// arithmetic
	// Match Lua 5.1 luaK_infix: the left operand must be materialized to RK
	// before the right subexpression is compiled — otherwise the right's
	// CALL/GETGLOBAL etc. would clobber the register the left result is about
	// to land in. The exemption = upstream isnumeral: eKNum with no pending
	// jump chain. A jump-chained eKNum (e.g. `(a and 7 or -1)`), if
	// deferred, has TESTSET in its chain skip the right subexpr's
	// instructions (wrong evaluation order); folding it drops the chain
	// outright (NoRegister placeholder → out-of-bounds panic).
	l := fs.expr(e.L)
	lrk := -1
	if !isNumeral(&l) {
		lrk = fs.exp2RK(e.Line, &l)
	}
	r := fs.expr(e.R)
	if folded, ok := constFold(e.Op, &l, &r); ok {
		out := newExp(eKNum, 0)
		out.nval = folded
		return out
	}
	// Materialization order in the numeral-left (delayed) path follows
	// PUC codearith: o2 = exp2RK(e2) BEFORE o1 = exp2RK(e1). The order
	// decides which literal registers a constant slot first, and +-0
	// dedup by numeric equality keeps the FIRST sign: 0 % -0 must put
	// -0 in the shared zero slot so a later literal 0 prints "-0"
	// (oracle diff fuzz catch). Non-numeral left was already
	// materialized before the right subtree (luaK_infix order).
	rb := lrk
	var rc int
	if rb < 0 {
		rc = fs.exp2RK(e.Line, &r)
		rb = fs.exp2RK(e.Line, &l)
	} else {
		rc = fs.exp2RK(e.Line, &r)
	}
	// Order: free the higher-numbered temp first, then the lower one
	// (keeps the stack discipline).
	if rb > rc {
		if !bytecode.IsK(rb) {
			fs.freeReg(rb)
		}
		if !bytecode.IsK(rc) {
			fs.freeReg(rc)
		}
	} else {
		if !bytecode.IsK(rc) {
			fs.freeReg(rc)
		}
		if !bytecode.IsK(rb) {
			fs.freeReg(rb)
		}
	}
	op := arithOpcode(e.Op)
	pc := fs.emitABC(e.Line, op, 0, rb, rc)
	return expDesc{k: eRelocable, info: pc, tJmp: NoJump, fJmp: NoJump}
}

func arithOpcode(op ast.BinOp) bytecode.OpCode {
	switch op {
	case ast.OpAdd:
		return bytecode.ADD
	case ast.OpSub:
		return bytecode.SUB
	case ast.OpMul:
		return bytecode.MUL
	case ast.OpDiv:
		return bytecode.DIV
	case ast.OpMod:
		return bytecode.MOD
	case ast.OpPow:
		return bytecode.POW
	}
	return bytecode.ADD
}

// isNumeral is equivalent to upstream lcode.c isnumeral: only a plain
// numeric literal (with no pending jump chain) may be deferred /
// participate in constant folding.
func isNumeral(e *expDesc) bool {
	return e.k == eKNum && !e.hasJumps()
}

// constFold computes the result at compile time when both sides are plain
// numeric literals (isNumeral).
func constFold(op ast.BinOp, l, r *expDesc) (float64, bool) {
	if !isNumeral(l) || !isNumeral(r) {
		return 0, false
	}
	a, b := l.nval, r.nval
	var res float64
	switch op {
	case ast.OpAdd:
		res = a + b
	case ast.OpSub:
		res = a - b
	case ast.OpMul:
		res = a * b
	case ast.OpDiv:
		// PUC constfolding: do not attempt to divide by 0.
		if b == 0 {
			return 0, false
		}
		res = a / b
	case ast.OpMod:
		if b == 0 {
			return 0, false
		}
		res = a - math.Floor(a/b)*b
	case ast.OpPow:
		res = math.Pow(a, b)
	default:
		return 0, false
	}
	// PUC constfolding: do not attempt to produce NaN. Beyond keeping
	// the constant table free of NaN, refusing changes which literal
	// registers the FIRST zero constant slot -- ±0 dedup by numeric
	// equality is first-come-wins, so folding here where PUC does not
	// flips print(-0) between "0" and "-0" (oracle diff fuzz catch:
	// print(000%0, -0)).
	if res != res {
		return 0, false
	}
	return res, true
}

// exprCompare compiles a comparison; three forms EQ/LT/LE, with ?= / > / >=
// mapped via swap + negate.
func (fs *funcState) exprCompare(e *ast.BinExpr) expDesc {
	op := e.Op
	swap := false
	want := 1
	var ic bytecode.OpCode
	switch op {
	case ast.OpEq:
		ic = bytecode.EQ
	case ast.OpNe:
		ic = bytecode.EQ
		want = 0
	case ast.OpLt:
		ic = bytecode.LT
	case ast.OpLe:
		ic = bytecode.LE
	case ast.OpGt:
		ic = bytecode.LT
		swap = true
	case ast.OpGe:
		ic = bytecode.LE
		swap = true
	}
	l := fs.expr(e.L)
	// PUC luaK_infix materializes a comparison's left operand with
	// luaK_exp2RK BEFORE the right subtree is parsed (the `default` arm,
	// which — unlike the arith arm — has no `if (!isnumeral(v))` deferral).
	// The order decides which literal registers a constant slot first, and
	// ±0 dedup is first-come-wins: in `0*-0 ~= 0%0` the folded -0 left must
	// claim the shared zero slot before `0%0`'s +0 literals, so a later
	// literal 0 prints "-0" (oracle diff fuzz catch: print(0*-0~=0%0,0)).
	rb := fs.exp2RK(e.Line, &l)
	r := fs.expr(e.R)
	rc := fs.exp2RK(e.Line, &r)
	if !bytecode.IsK(rc) {
		fs.freeReg(rc)
	}
	if !bytecode.IsK(rb) {
		fs.freeReg(rb)
	}
	if swap {
		rb, rc = rc, rb
	}
	fs.emitABC(e.Line, ic, want, rb, rc)
	pc := fs.jump(e.Line)
	return expDesc{k: eJmp, info: pc, tJmp: NoJump, fJmp: NoJump}
}

// exprConcat compiles a..b..c — right-associative, folded into a single
// CONCAT(B..C).
func (fs *funcState) exprConcat(e *ast.BinExpr) expDesc {
	// collect all right-expanded operands: a..(b..(c..d)) flattened to [a,b,c,d]
	parts := []ast.Expr{e.L}
	cur := e.R
	for {
		if be, ok := cur.(*ast.BinExpr); ok && be.Op == ast.OpConcat {
			parts = append(parts, be.L)
			cur = be.R
			continue
		}
		parts = append(parts, cur)
		break
	}
	base := fs.freereg
	for _, p := range parts {
		pe := fs.expr(p)
		fs.exp2NextReg(e.Line, &pe)
	}
	last := fs.freereg - 1
	pc := fs.emitABC(e.Line, bytecode.CONCAT, 0, base, last)
	// free base..last (A is patched later at exp2reg time; the register
	// watermark first drops back below base)
	for fs.freereg > base {
		fs.freereg--
	}
	return expDesc{k: eRelocable, info: pc, tJmp: NoJump, fJmp: NoJump}
}

// exprUn compiles a unary expression; -/not/#.
func (fs *funcState) exprUn(e *ast.UnExpr) expDesc {
	sub := fs.expr(e.E)
	switch e.Op {
	case ast.OpUnm:
		// constant folding: -numeric-literal (isnumeral: a jump-chained
		// value cannot be folded, the chain would be dropped)
		if isNumeral(&sub) {
			out := newExp(eKNum, 0)
			out.nval = -sub.nval
			return out
		}
		fs.exp2AnyReg(e.Line, &sub)
		fs.freeExp(&sub)
		pc := fs.emitABC(e.Line, bytecode.UNM, 0, sub.info, 0)
		return expDesc{k: eRelocable, info: pc, tJmp: NoJump, fJmp: NoJump}
	case ast.OpNot:
		// short-circuit: for a jump-chained expression just swap t/f
		fs.exp2AnyReg(e.Line, &sub)
		fs.freeExp(&sub)
		pc := fs.emitABC(e.Line, bytecode.NOT, 0, sub.info, 0)
		out := expDesc{k: eRelocable, info: pc, tJmp: sub.fJmp, fJmp: sub.tJmp}
		return out
	case ast.OpLen:
		fs.exp2AnyReg(e.Line, &sub)
		fs.freeExp(&sub)
		pc := fs.emitABC(e.Line, bytecode.LEN, 0, sub.info, 0)
		return expDesc{k: eRelocable, info: pc, tJmp: NoJump, fJmp: NoJump}
	}
	return expDesc{}
}

// exprFunc compiles a function literal (nested Proto + CLOSURE).
func (fs *funcState) exprFunc(e *ast.FuncExpr) expDesc {
	proto := fs.cg.compileFunc(fs, e)
	idx := uint32(len(fs.cg.protos) - 1) // most recently registered ProtoID
	fs.proto.Protos = append(fs.proto.Protos, idx)
	fs.proto.SubNUps = append(fs.proto.SubNUps, uint8(len(proto.UpvalDescs)))
	closureIdx := len(fs.proto.Protos) - 1
	pc := fs.emitABx(e.Line, bytecode.CLOSURE, 0, closureIdx)
	// followed by nupvals pseudo-instructions
	for _, u := range proto.UpvalDescs {
		if u.InStack {
			fs.emitABC(e.Line, bytecode.MOVE, 0, int(u.Idx), 0)
		} else {
			fs.emitABC(e.Line, bytecode.GETUPVAL, 0, int(u.Idx), 0)
		}
	}
	return expDesc{k: eRelocable, info: pc, tJmp: NoJump, fJmp: NoJump}
}

// emitSetList emits SETLIST (matching upstream luaK_setlist): when the batch
// number c exceeds the 9-bit C field (MaxBC=511), emit C=0 followed by a
// bare batch-number instruction — the old implementation had EncodeABC
// silently &0x1FF-truncate C, so batch 512 wrapped to 0 and the interpreter
// swallowed the next normal instruction as the batch number (a table
// constructor with >25550 items would hang).
func (fs *funcState) emitSetList(line int32, tReg, b, batchNo int) {
	if batchNo <= bytecode.MaxBC {
		fs.emitABC(line, bytecode.SETLIST, tReg, b, batchNo)
		return
	}
	fs.emitABC(line, bytecode.SETLIST, tReg, b, 0)
	fs.emit(line, bytecode.Instruction(batchNo)) // upstream also emits a bare word via luaK_code
}

// exprTable compiles a table constructor: NEWTABLE + interleaved emission in
// source order of SETTABLE (key-value fields, emitted inline) and SETLIST
// (positional fields, flushed once a batch fills).
//
// **Order is semantics** (same as the PUC lparser.c constructor loop):
// later writes overwrite earlier ones — in `{B,0,C,[1]=""}` the [1]=""
// SETTABLE runs first, then the trailing SETLIST overwrites t[1]=B.
// The old implementation split positional/key-value items into two phases
// (all SETLIST first, then all SETTABLE), reversing the overwrite direction
// (caught by cgo oracle diff fuzz).
func (fs *funcState) exprTable(e *ast.TableExpr) expDesc {
	tReg := fs.freereg
	pc := fs.emitABC(e.Line, bytecode.NEWTABLE, tReg, 0, 0) // B/C patched later
	fs.reserveRegs(e.Line, 1)

	flush := bytecode.FieldsPerFlush
	pending := 0 // items landed in R(tReg+1+pending) for the current batch
	batchNo := 1 // batch number (SETLIST's C)
	nArr := 0    // total positional items (patched into NEWTABLE B)
	nHash := 0   // total key-value items (patched into NEWTABLE C)

	// Find the index of the last positional item (multi-value expansion
	// applies only to it, matching PUC: only the trailing positional item
	// of a constructor keeps multi-value).
	lastPositional := -1
	for i := len(e.Items) - 1; i >= 0; i-- {
		if e.Items[i].Key == nil {
			lastPositional = i
			break
		}
	}
	lastIsMulti := lastPositional == len(e.Items)-1

	for i, it := range e.Items {
		if it.Key != nil {
			// key-value field: SETTABLE inline (this is where the order
			// semantics live).
			ke := fs.expr(it.Key)
			rkK := fs.exp2RK(e.Line, &ke)
			ve := fs.expr(it.Val)
			rkV := fs.exp2RK(e.Line, &ve)
			fs.emitABC(e.Line, bytecode.SETTABLE, tReg, rkK, rkV)
			if !bytecode.IsK(rkV) {
				fs.freeReg(rkV)
			}
			if !bytecode.IsK(rkK) {
				fs.freeReg(rkK)
			}
			nHash++
			continue
		}
		nArr++
		ve := fs.expr(it.Val)
		if i == lastPositional && lastIsMulti && fs.openMultiRet(&ve, -1) {
			fs.emitSetList(e.Line, tReg, 0, batchNo)
			fs.freereg = tReg + 1
			pending = 0
			continue
		}
		fs.exp2NextReg(e.Line, &ve)
		pending++
		if pending == flush {
			fs.emitSetList(e.Line, tReg, flush, batchNo)
			fs.freereg = tReg + 1
			pending = 0
			batchNo++
		}
	}
	if pending > 0 {
		fs.emitSetList(e.Line, tReg, pending, batchNo)
		fs.freereg = tReg + 1
	}

	// patch NEWTABLE's B/C
	ins := fs.proto.Code[pc]
	ins = bytecode.SetB(ins, int(bytecode.Int2Fb(uint32(nArr))))
	ins = bytecode.SetC(ins, int(bytecode.Int2Fb(uint32(nHash))))
	fs.proto.Code[pc] = ins

	// expose the NEWTABLE landing site as ENonReloc (already in R(tReg))
	return expDesc{k: eNonReloc, info: tReg, tJmp: NoJump, fJmp: NoJump}
}
