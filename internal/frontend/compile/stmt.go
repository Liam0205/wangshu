// stmt — statement-level codegen (04 §6 control structures + §7 calls/assignments + §8 function definitions/closures).
package compile

import (
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/ast"
)

func (fs *funcState) block(b *ast.Block) {
	for _, s := range b.Stmts {
		fs.stmt(s)
		// invariant: freereg must converge back to nactvar at the end of each statement
		fs.freereg = fs.nactvar
	}
}

func (fs *funcState) stmt(node ast.Stmt) {
	switch s := node.(type) {
	case *ast.LocalStmt:
		fs.stmtLocal(s)
	case *ast.LocalFuncStmt:
		fs.stmtLocalFunc(s)
	case *ast.AssignStmt:
		fs.stmtAssign(s)
	case *ast.CallStmt:
		fs.stmtCall(s)
	case *ast.DoStmt:
		fs.enterBlock(false)
		fs.block(s.Body)
		fs.leaveBlock(s.Line)
	case *ast.IfStmt:
		fs.stmtIf(s)
	case *ast.WhileStmt:
		fs.stmtWhile(s)
	case *ast.RepeatStmt:
		fs.stmtRepeat(s)
	case *ast.NumForStmt:
		fs.stmtNumFor(s)
	case *ast.GenForStmt:
		fs.stmtGenFor(s)
	case *ast.FuncStmt:
		fs.stmtFunc(s)
	case *ast.ReturnStmt:
		fs.stmtReturn(s)
	case *ast.BreakStmt:
		fs.stmtBreak(s)
	default:
		raise(fs, 0, "compile: unsupported stmt node %T", node)
	}
}

// stmtLocal: local a, b = e1, e2 (04 §5.8)
func (fs *funcState) stmtLocal(s *ast.LocalStmt) {
	nWant := len(s.Names)
	fs.adjustExprList(s.Line, s.Exprs, nWant)
	for i, n := range s.Names {
		fs.registerLocal(s.Line, n)
		// Track `local sqrt = math.sqrt`-style bindings (RHS is a bare
		// NameExpr or IndexExpr shape) so inner Protos' compilability
		// analysis can resolve safe stdlib alias calls. The analyzer
		// applies its own whitelist; here we only record the dataflow
		// fact. Any other RHS shape clears a stale entry.
		if i < len(s.Exprs) {
			switch s.Exprs[i].(type) {
			case *ast.NameExpr, *ast.IndexExpr:
				fs.localAliasAsts[n] = s.Exprs[i]
				continue
			}
		}
		delete(fs.localAliasAsts, n)
	}
}

// stmtLocalFunc: local function f(): register the local f first, then codegen Fn into that register (04 §5.8)
func (fs *funcState) stmtLocalFunc(s *ast.LocalFuncStmt) {
	fs.registerLocal(s.Line, s.Name)
	// Record localFnAsts so that nested / sibling closures' AnalyzeProto knows
	// this fn is a known local (carries the P4 PJ5 scope-aware analyzer extension).
	fs.localFnAsts[s.Name] = s.Fn
	reg := fs.freereg
	fs.reserveRegs(s.Line, 1)
	fnExp := fs.exprFunc(s.Fn)
	fs.exp2reg(s.Line, &fnExp, reg)
}

// adjustExprList adjusts exprs to nWant values, landing in consecutive registers starting at R(freereg) (04 §6.2).
//
// When the last element is a multi-value source (Call/Vararg), it takes nWant - (number of fixed values before it)
// values; otherwise a shortfall is padded with LOADNIL and the excess is discarded (side effects of the discarded
// expressions still run).
func (fs *funcState) adjustExprList(line int32, exprs []ast.Expr, nWant int) {
	n := len(exprs)
	if n == 0 {
		if nWant > 0 {
			fs.emitABC(line, bytecode.LOADNIL, fs.freereg, fs.freereg+nWant-1, 0)
			fs.reserveRegs(line, nWant)
		}
		return
	}
	for i := 0; i < n-1; i++ {
		ei := fs.expr(exprs[i])
		fs.exp2NextReg(line, &ei)
	}
	last := fs.expr(exprs[n-1])
	if nWant < n {
		// Take only the first nWant; the last element is also fixed to 1 value.
		// But the loop above already fixed the first n-1. If nWant >= n-1, the last element can be multi-value; otherwise discard the last element.
		switch {
		case nWant == n-1:
			// discard the last element (its evaluation side effects still run)
			fs.exp2NextReg(line, &last)
			fs.freereg-- // discard immediately
		case nWant < n-1:
			// The design did not pin down the "excess expressions not executed vs. evaluated then discarded" detail; Lua 5.1 is "evaluate then discard".
			fs.exp2NextReg(line, &last)
			over := n - nWant
			fs.freereg -= over
		}
		return
	}
	// nWant >= n: the last element may be multi-value, pad nWant - n nils
	if extra := nWant - (n - 1); fs.openMultiRet(&last, extra) {
		// openMultiRet uniformly handles the A-field discipline (eCall's A must not change;
		// eVararg backfills freereg). Reserved difference: CALL result lands in fnReg (already
		// occupies 1 slot) then reserve extra-1 more; VARARG lands in freereg (no slot occupied) reserve extra.
		if last.k == eCall {
			if extra > 1 {
				fs.reserveRegs(line, extra-1)
			}
		} else {
			fs.reserveRegs(line, extra)
		}
		return
	}
	fs.exp2NextReg(line, &last)
	if nWant > n {
		fs.emitABC(line, bytecode.LOADNIL, fs.freereg, fs.freereg+(nWant-n)-1, 0)
		fs.reserveRegs(line, nWant-n)
	}
}

// stmtAssign: lhs1, lhs2 = rhs1, rhs2 (04 §5.8 + §7).
//
// Lua 5.1 semantics: all RHS are evaluated first, then the assignments are performed; if an LHS has the t[k] form, the table/key must be evaluated first.
//
// A single LHS = single RHS takes the "direct storeVar" fast path, skipping the freereg temporary (aligns with Lua 5.1 luaK_storevar's
// VLOCAL path, which avoids a pointless MOVE/RETURN).
func (fs *funcState) stmtAssign(s *ast.AssignStmt) {
	if len(s.Targets) == 1 && len(s.Exprs) == 1 {
		fs.storeVar(s.Line, s.Targets[0], s.Exprs[0])
		fs.freereg = fs.nactvar
		return
	}
	type target struct {
		isLocal   bool
		isGlobal  bool
		isUpval   bool
		isIndexed bool
		regOrK    int
		tableReg  int
		keyRK     int
	}
	tgts := make([]target, len(s.Targets))
	for i, t := range s.Targets {
		switch tn := t.(type) {
		case *ast.NameExpr:
			ne := fs.resolveName(tn.Line, tn.Name)
			switch ne.k {
			case eLocal:
				tgts[i] = target{isLocal: true, regOrK: ne.info}
			case eUpval:
				tgts[i] = target{isUpval: true, regOrK: ne.info}
			case eGlobal:
				tgts[i] = target{isGlobal: true, regOrK: ne.info}
			}
		case *ast.IndexExpr:
			obj := fs.expr(tn.Obj)
			tableReg := fs.exp2AnyReg(tn.Line, &obj)
			key := fs.expr(tn.Key)
			rk := fs.exp2RK(tn.Line, &key)
			tgts[i] = target{isIndexed: true, tableReg: tableReg, keyRK: rk}
		default:
			raise(fs, s.Line, "syntax error")
		}
	}
	// evaluate RHS: land in consecutive registers starting at freereg, count = len(s.Targets)
	fs.adjustExprList(s.Line, s.Exprs, len(s.Targets))
	rhsTop := fs.freereg - 1
	for i := len(s.Targets) - 1; i >= 0; i-- {
		src := rhsTop
		rhsTop--
		t := tgts[i]
		switch {
		case t.isLocal:
			if t.regOrK != src {
				fs.emitABC(s.Line, bytecode.MOVE, t.regOrK, src, 0)
			}
		case t.isIndexed:
			fs.emitABC(s.Line, bytecode.SETTABLE, t.tableReg, t.keyRK, src)
		case t.isGlobal:
			fs.emitABx(s.Line, bytecode.SETGLOBAL, src, t.regOrK)
		case t.isUpval:
			fs.emitABC(s.Line, bytecode.SETUPVAL, src, t.regOrK, 0)
		}
	}
	fs.freereg = fs.nactvar
}

// storeVar stores the rhs value "directly" into the lhs variable (single-assignment fast path).
func (fs *funcState) storeVar(line int32, lhs, rhs ast.Expr) {
	switch tn := lhs.(type) {
	case *ast.NameExpr:
		ne := fs.resolveName(tn.Line, tn.Name)
		re := fs.expr(rhs)
		switch ne.k {
		case eLocal:
			fs.exp2reg(line, &re, ne.info)
		case eUpval:
			r := fs.exp2AnyReg(line, &re)
			fs.emitABC(line, bytecode.SETUPVAL, r, ne.info, 0)
		case eGlobal:
			r := fs.exp2AnyReg(line, &re)
			fs.emitABx(line, bytecode.SETGLOBAL, r, ne.info)
		}
	case *ast.IndexExpr:
		obj := fs.expr(tn.Obj)
		tableReg := fs.exp2AnyReg(tn.Line, &obj)
		key := fs.expr(tn.Key)
		rkK := fs.exp2RK(tn.Line, &key)
		re := fs.expr(rhs)
		rkV := fs.exp2RK(line, &re)
		fs.emitABC(line, bytecode.SETTABLE, tableReg, rkK, rkV)
	default:
		raise(fs, line, "syntax error")
	}
}

func (fs *funcState) stmtCall(s *ast.CallStmt) {
	e := fs.expr(s.Call)
	if e.k != eCall {
		raise(fs, s.Line, "syntax error")
	}
	// CallStmt takes 0 return values ⇒ C=1
	fs.proto.Code[e.info] = bytecode.SetC(fs.proto.Code[e.info], 1)
}

func (fs *funcState) stmtIf(s *ast.IfStmt) {
	endList := NoJump
	for i, cl := range s.Clauses {
		ce := fs.expr(cl.Cond)
		fs.goIfTrue(cl.Cond.Pos(), &ce)
		falseList := ce.fJmp
		fs.enterBlock(false)
		fs.block(cl.Body)
		fs.leaveBlock(s.Line)
		// not the last clause (there is an else or more elseif) needs to jump to the end
		hasElse := s.Else != nil
		isLast := i == len(s.Clauses)-1
		if !isLast || hasElse {
			j := fs.jump(s.Line)
			fs.concat(&endList, j)
		}
		fs.patchToHere(falseList)
	}
	if s.Else != nil {
		fs.enterBlock(false)
		fs.block(s.Else)
		fs.leaveBlock(s.Line)
	}
	fs.patchToHere(endList)
}

func (fs *funcState) stmtWhile(s *ast.WhileStmt) {
	loopStart := fs.getLabel()
	ce := fs.expr(s.Cond)
	fs.goIfTrue(s.Cond.Pos(), &ce)
	exitList := ce.fJmp
	fs.enterBlock(true)
	fs.block(s.Body)
	// back edge
	back := fs.jump(s.Line)
	fs.patchList(back, loopStart)
	fs.leaveBlock(s.Line)
	fs.patchToHere(exitList)
}

func (fs *funcState) stmtRepeat(s *ast.RepeatStmt) {
	// Align with the official repeatstat's two-level blocks: outer loop (breakable), inner scope.
	// cond is evaluated inside the scope (until can see body locals). When the scope captures upvalues,
	// take the "full semantics": cond true → break (CLOSE + jump out); false → leaveBlock emits
	// CLOSE then jumps back — each iteration closes the captures of that iteration's locals, otherwise
	// closures of all iterations share the same open stack slot (in a repeat, `local x=i; f=function() return x end`
	// should capture a separate copy per iteration).
	loopStart := fs.getLabel()
	fs.enterBlock(true)  // loop block
	fs.enterBlock(false) // scope block
	fs.block(s.Body)
	ce := fs.expr(s.Cond)
	fs.goIfTrue(s.Cond.Pos(), &ce)
	if !fs.bl.hasUpval {
		fs.leaveBlock(s.Line)            // finish scope (no CLOSE)
		fs.patchList(ce.fJmp, loopStart) // cond false ⇒ jump back
	} else {
		// cond true ⇒ break path (stmtBreak emits CLOSE and joins breakList)
		fs.stmtBreak(&ast.BreakStmt{Line: s.Line})
		fs.patchToHere(ce.fJmp)                  // cond false lands here
		fs.leaveBlock(s.Line)                    // finish scope (emit CLOSE)
		fs.patchList(fs.jump(s.Line), loopStart) // jump back again
	}
	fs.leaveBlock(s.Line) // finish loop (break landing point)
}

// stmtNumFor: numeric for (04 §6.5). Occupies 4 slots R(base..base+3).
//
// Aligns with Lua 5.1 forstat/forbody's two-level blocks: the outer breakable block contains FORLOOP
// (break jumps to after FORLOOP), the inner non-breakable block is just the loop-body variable scope.
func (fs *funcState) stmtNumFor(s *ast.NumForStmt) {
	fs.enterBlock(true) // outer breakable (holds three internal control slots)
	base := fs.freereg
	initE := fs.expr(s.Init)
	fs.exp2NextReg(s.Line, &initE) // R(base)
	limitE := fs.expr(s.Limit)
	fs.exp2NextReg(s.Line, &limitE) // R(base+1)
	if s.Step != nil {
		stepE := fs.expr(s.Step)
		fs.exp2NextReg(s.Line, &stepE) // R(base+2)
	} else {
		k := fs.numK(s.Line, 1)
		fs.emitABx(s.Line, bytecode.LOADK, base+2, k)
		fs.reserveRegs(s.Line, 1)
	}
	fs.registerLocal(s.Line, "(for index)")
	fs.registerLocal(s.Line, "(for limit)")
	fs.registerLocal(s.Line, "(for step)")
	prep := fs.emitAsBx(s.Line, bytecode.FORPREP, base, NoJump)
	fs.enterBlock(false) // inner: loop-body variable scope
	fs.registerLocal(s.Line, s.Var)
	fs.reserveRegs(s.Line, 1)
	fs.block(s.Body)
	fs.leaveBlock(s.Line) // close v's scope (before FORLOOP)
	loopPC := fs.pc()
	fs.fixJump(s.Line, prep, loopPC)
	fs.emitAsBx(s.Line, bytecode.FORLOOP, base, prep+1-(loopPC+1))
	fs.leaveBlock(s.Line) // outer: break lands after FORLOOP; pop the three control slots
}

// stmtGenFor: generic for (04 §6.6). Occupies R(base..base+2) + loop variables. Two-level blocks as above.
func (fs *funcState) stmtGenFor(s *ast.GenForStmt) {
	fs.enterBlock(true)
	base := fs.freereg
	fs.adjustExprList(s.Line, s.Exprs, 3)
	fs.registerLocal(s.Line, "(for generator)")
	fs.registerLocal(s.Line, "(for state)")
	fs.registerLocal(s.Line, "(for control)")
	prep := fs.jump(s.Line) // jump to TFORLOOP
	bodyPC := fs.pc()
	fs.enterBlock(false)
	for _, n := range s.Names {
		fs.registerLocal(s.Line, n)
		fs.reserveRegs(s.Line, 1)
	}
	fs.block(s.Body)
	fs.leaveBlock(s.Line)
	fs.patchToHere(prep)
	fs.emitABC(s.Line, bytecode.TFORLOOP, base, 0, len(s.Names))
	back := fs.jump(s.Line)
	fs.patchList(back, bodyPC)
	fs.leaveBlock(s.Line)
}

// stmtFunc: function a.b.c:m() ...end (04 §8.2)
func (fs *funcState) stmtFunc(s *ast.FuncStmt) {
	// convert to the equivalent AssignStmt form
	assign := &ast.AssignStmt{
		Line:    s.Line,
		Targets: []ast.Expr{s.Target},
		Exprs:   []ast.Expr{s.Fn},
	}
	fs.stmtAssign(assign)
}

// stmtReturn: tail-call recognition (04 §9.4).
func (fs *funcState) stmtReturn(s *ast.ReturnStmt) {
	if len(s.Exprs) == 0 {
		fs.emitABC(s.Line, bytecode.RETURN, 0, 1, 0)
		return
	}
	if len(s.Exprs) == 1 {
		switch ce := s.Exprs[0].(type) {
		case *ast.CallExpr:
			// tail call
			e := fs.exprCall(ce)
			ins := fs.proto.Code[e.info]
			fs.proto.Code[e.info] = bytecode.EncodeABC(bytecode.TAILCALL,
				bytecode.A(ins), bytecode.B(ins), 0)
			fs.emitABC(s.Line, bytecode.RETURN, bytecode.A(ins), 0, 0)
			return
		case *ast.MethodCallExpr:
			e := fs.exprMethodCall(ce)
			ins := fs.proto.Code[e.info]
			fs.proto.Code[e.info] = bytecode.EncodeABC(bytecode.TAILCALL,
				bytecode.A(ins), bytecode.B(ins), 0)
			fs.emitABC(s.Line, bytecode.RETURN, bytecode.A(ins), 0, 0)
			return
		case *ast.VarargExpr:
			// return ... returns all varargs (multi-value to top), cannot be collapsed to a single value
			base := fs.freereg
			ve := fs.expr(ce)
			fs.openMultiRet(&ve, -1)
			fs.emitABC(s.Line, bytecode.RETURN, base, 0, 0)
			return
		}
		// Single non-Call value: if it is ELocal/ENonReloc, use its register directly (avoids a pointless MOVE).
		// An expression with a pending jump chain (e.g. the short-circuit chain of `return (not c) and m`) cannot take
		// the fast path — a direct RETURN skips exp2reg's chain backfill, leaving the chain's JMP landing point dangling
		// (sBx self-loop, an infinite loop that hangs at runtime).
		single := fs.expr(s.Exprs[0])
		fs.dischargeVars(s.Line, &single)
		if single.k == eNonReloc && !single.hasJumps() {
			fs.emitABC(s.Line, bytecode.RETURN, single.info, 2, 0)
			return
		}
		// RETURN must read the register exp2reg actually materialized
		// into (single.info), not a pre-captured freereg: exp2NextReg
		// first frees the expression's own temp (freereg drops back)
		// and re-materializes one slot lower, so a base captured
		// BEFORE the call can point one past the value. The or-chain
		// `return f()or(f())` landed its result in R(1) but emitted
		// RETURN A=2, returning stale stack garbage (issue #125; PUC
		// luac emits RETURN 1 2 here).
		fs.exp2NextReg(s.Line, &single)
		fs.emitABC(s.Line, bytecode.RETURN, single.info, 2, 0)
		fs.freereg = single.info
		return
	}
	// ordinary return: multi-value, the last element goes to top via B=0
	base := fs.freereg
	n := len(s.Exprs)
	for i := 0; i < n-1; i++ {
		ei := fs.expr(s.Exprs[i])
		fs.exp2NextReg(s.Line, &ei)
	}
	last := fs.expr(s.Exprs[n-1])
	if fs.openMultiRet(&last, -1) {
		fs.emitABC(s.Line, bytecode.RETURN, base, 0, 0)
		return
	}
	fs.exp2NextReg(s.Line, &last)
	fs.emitABC(s.Line, bytecode.RETURN, base, n+1, 0)
	fs.freereg = base
}

func (fs *funcState) stmtBreak(s *ast.BreakStmt) {
	// Align with the official breakstat: walk from the innermost block up to the breakable block, accumulating the upval
	// flag along the way — the capture marks for loop variables / body locals live on the inner blocks; looking only at
	// the loop block itself would miss emitting CLOSE, and after break the open upvalues would still point at the stack
	// slots about to be overwritten (reading stale values / nil).
	upval := false
	var bl *blockCnt
	for b := fs.bl; b != nil; b = b.prev {
		if b.isLoop {
			bl = b
			break
		}
		upval = upval || b.hasUpval
	}
	if bl == nil {
		raise(fs, s.Line, "no loop to break")
	}
	if upval || bl.hasUpval {
		fs.emitABC(s.Line, bytecode.CLOSE, bl.nactvarSnap, 0, 0)
	}
	pc := fs.jump(s.Line)
	fs.concat(&bl.breakList, pc)
}
