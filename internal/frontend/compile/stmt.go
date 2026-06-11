// stmt — 语句级 codegen(04 §6 控制结构 + §7 调用/赋值 + §8 函数定义/闭包)。
package compile

import (
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/ast"
)

func (fs *funcState) block(b *ast.Block) {
	for _, s := range b.Stmts {
		fs.stmt(s)
		// 不变式:每条语句结束 freereg 必须收敛回 nactvar
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

// stmtLocal:local a, b = e1, e2(04 §5.8)
func (fs *funcState) stmtLocal(s *ast.LocalStmt) {
	nWant := len(s.Names)
	fs.adjustExprList(s.Line, s.Exprs, nWant)
	for _, n := range s.Names {
		fs.registerLocal(s.Line, n)
	}
}

// stmtLocalFunc:local function f(): 先注册局部 f,再 codegen Fn 到该寄存器(04 §5.8)
func (fs *funcState) stmtLocalFunc(s *ast.LocalFuncStmt) {
	fs.registerLocal(s.Line, s.Name)
	reg := fs.freereg
	fs.reserveRegs(s.Line, 1)
	fnExp := fs.exprFunc(s.Fn)
	fs.exp2reg(s.Line, &fnExp, reg)
}

// adjustExprList 把 exprs 调整到 nWant 个值,落在 R(freereg) 起的连续寄存器(04 §6.2)。
//
// 末位是多值源(Call/Vararg)时,把其取值数 = nWant - (前面已固定值数);否则不足补 LOADNIL,
// 多余丢弃(余者副作用仍执行)。
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
		// 仅取前 nWant 个,末位也固定 1 值;
		// 但前面的循环已经把前 n-1 个固定。如果 nWant >= n-1,末位可作多值;否则丢弃末位。
		switch {
		case nWant == n-1:
			// 末位丢弃(求值副作用仍执行)
			fs.exp2NextReg(line, &last)
			fs.freereg-- // 立刻丢弃
		case nWant < n-1:
			// 设计期未明确"多余表达式不执行 vs 求值丢弃"的细节,Lua 5.1 是"求值丢弃"。
			fs.exp2NextReg(line, &last)
			over := n - nWant
			fs.freereg -= over
		}
		return
	}
	// nWant >= n:末位可能多值,补 nWant - n 个 nil
	switch last.k {
	case eCall:
		extra := nWant - (n - 1)
		fs.setReturns(&last, extra)
		// CALL 的 A 已在 exprCall 设为 fnReg(= freereg-1 当时),其结果就落 R(fnReg);
		// extra>1 时把 freereg 抬高到容纳全部返回值。
		if extra > 1 {
			fs.reserveRegs(line, extra-1)
		}
	case eVararg:
		extra := nWant - (n - 1)
		fs.setReturns(&last, extra)
		// VARARG 的 A 是发射时的占位 0,落点 = 当前 freereg(多值起点)。
		fs.proto.Code[last.info] = bytecode.SetA(fs.proto.Code[last.info], fs.freereg)
		fs.reserveRegs(line, extra)
	default:
		fs.exp2NextReg(line, &last)
		if nWant > n {
			fs.emitABC(line, bytecode.LOADNIL, fs.freereg, fs.freereg+(nWant-n)-1, 0)
			fs.reserveRegs(line, nWant-n)
		}
	}
}

// stmtAssign:lhs1, lhs2 = rhs1, rhs2(04 §5.8 + §7)。
//
// Lua 5.1 语义:RHS 全部求值后再执行赋值;若 LHS 含 t[k] 形式,需先 evaluate 表/键。
//
// 单 LHS=单 RHS 走"直落 storeVar"快路径,跳过 freereg 临时(对齐 Lua 5.1 luaK_storevar
// 的 VLOCAL 路径,可以避免无意义的 MOVE/RETURN)。
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
	// 求 RHS:落到 freereg 起的连续寄存器,数 = len(s.Targets)
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

// storeVar 把 rhs 值"直接"存入 lhs 变量(单赋值快路径)。
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
	// CallStmt 取 0 返回值 ⇒ C=1
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
		// 不是最后一个 clause(还有 else 或更多 elseif)需要跳到末尾
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
	// 回边
	back := fs.jump(s.Line)
	fs.patchList(back, loopStart)
	fs.leaveBlock(s.Line)
	fs.patchToHere(exitList)
}

func (fs *funcState) stmtRepeat(s *ast.RepeatStmt) {
	loopStart := fs.getLabel()
	fs.enterBlock(true)
	fs.block(s.Body)
	// cond 在 body 作用域内可见局部
	ce := fs.expr(s.Cond)
	fs.goIfTrue(s.Cond.Pos(), &ce)
	// cond 假 ⇒ 回跳;真 ⇒ 落出
	fs.patchList(ce.fJmp, loopStart)
	// 注意:Lua 5.1 在 cond 求值之后才 leaveBlock(允许 until x>0 中 x 是 body 局部)
	fs.leaveBlock(s.Line)
}

// stmtNumFor:数值 for(04 §6.5)。占 4 槽 R(base..base+3)。
//
// 对齐 Lua 5.1 forstat/forbody 的两层块:外层 breakable 块包含 FORLOOP
// (break 跳到 FORLOOP 之后),内层非 breakable 块只是循环体变量作用域。
func (fs *funcState) stmtNumFor(s *ast.NumForStmt) {
	fs.enterBlock(true) // 外层 breakable(含三个内部控制槽)
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
	fs.enterBlock(false) // 内层:循环体变量作用域
	fs.registerLocal(s.Line, s.Var)
	fs.reserveRegs(s.Line, 1)
	fs.block(s.Body)
	fs.leaveBlock(s.Line) // 关闭 v 作用域(FORLOOP 之前)
	loopPC := fs.pc()
	fs.fixJump(s.Line, prep, loopPC)
	fs.emitAsBx(s.Line, bytecode.FORLOOP, base, prep+1-(loopPC+1))
	fs.leaveBlock(s.Line) // 外层:break 落到 FORLOOP 之后;退三个控制槽
}

// stmtGenFor:泛型 for(04 §6.6)。占 R(base..base+2) + 循环变量。两层块同上。
func (fs *funcState) stmtGenFor(s *ast.GenForStmt) {
	fs.enterBlock(true)
	base := fs.freereg
	fs.adjustExprList(s.Line, s.Exprs, 3)
	fs.registerLocal(s.Line, "(for generator)")
	fs.registerLocal(s.Line, "(for state)")
	fs.registerLocal(s.Line, "(for control)")
	prep := fs.jump(s.Line) // 跳到 TFORLOOP
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

// stmtFunc:function a.b.c:m() ...end(04 §8.2)
func (fs *funcState) stmtFunc(s *ast.FuncStmt) {
	// 转换为 AssignStmt 的等价形式
	assign := &ast.AssignStmt{
		Line:    s.Line,
		Targets: []ast.Expr{s.Target},
		Exprs:   []ast.Expr{s.Fn},
	}
	fs.stmtAssign(assign)
}

// stmtReturn:尾调用识别(04 §9.4)。
func (fs *funcState) stmtReturn(s *ast.ReturnStmt) {
	if len(s.Exprs) == 0 {
		fs.emitABC(s.Line, bytecode.RETURN, 0, 1, 0)
		return
	}
	if len(s.Exprs) == 1 {
		switch ce := s.Exprs[0].(type) {
		case *ast.CallExpr:
			// 尾调用
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
			// return ... 返回全部 vararg(多值到 top),不可收敛单值
			base := fs.freereg
			ve := fs.expr(ce)
			fs.setReturns(&ve, -1)
			fs.proto.Code[ve.info] = bytecode.SetA(fs.proto.Code[ve.info], base)
			fs.emitABC(s.Line, bytecode.RETURN, base, 0, 0)
			return
		}
		// 单值非 Call:若是 ELocal/ENonReloc 直接用其寄存器(避免无意义 MOVE)
		single := fs.expr(s.Exprs[0])
		fs.dischargeVars(s.Line, &single)
		switch single.k {
		case eNonReloc:
			fs.emitABC(s.Line, bytecode.RETURN, single.info, 2, 0)
			return
		}
		base := fs.freereg
		fs.exp2NextReg(s.Line, &single)
		fs.emitABC(s.Line, bytecode.RETURN, base, 2, 0)
		fs.freereg = base
		return
	}
	// 普通 return:多值末位走 B=0 到 top
	base := fs.freereg
	n := len(s.Exprs)
	for i := 0; i < n-1; i++ {
		ei := fs.expr(s.Exprs[i])
		fs.exp2NextReg(s.Line, &ei)
	}
	last := fs.expr(s.Exprs[n-1])
	switch last.k {
	case eCall, eVararg:
		fs.setReturns(&last, -1)
		fs.proto.Code[last.info] = bytecode.SetA(fs.proto.Code[last.info], fs.freereg)
		fs.emitABC(s.Line, bytecode.RETURN, base, 0, 0)
		return
	}
	fs.exp2NextReg(s.Line, &last)
	fs.emitABC(s.Line, bytecode.RETURN, base, n+1, 0)
	fs.freereg = base
}

func (fs *funcState) stmtBreak(s *ast.BreakStmt) {
	bl := fs.innerLoopBlock()
	if bl == nil {
		raise(fs, s.Line, "no loop to break")
	}
	if bl.hasUpval {
		fs.emitABC(s.Line, bytecode.CLOSE, bl.nactvarSnap, 0, 0)
	}
	pc := fs.jump(s.Line)
	fs.concat(&bl.breakList, pc)
}
