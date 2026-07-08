// codegen — AST → bytecode.Proto 的主遍历(04 §5-§9)。
//
// expr 路径:expr() 返回 expDesc,延迟物化由调用方按需 exp2NextReg / exp2RK / goIfTrue 等驱动。
// stmt 路径:stmt() 直接产生指令并维护 freereg / nactvar 不变式。
package compile

import (
	"math"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/ast"
)

// resolveName 在词法链上解析一个 NameExpr,返回 expDesc 与匹配类型(local/upval/global)。
//
// 04 §8.4 词法作用域链查找:本函数局部 → 已捕获 upvalue → 外层链(沿 prev) → 全局穿透。
func (fs *funcState) resolveName(line int32, name string) expDesc {
	// 1) 本函数局部
	if r := fs.findLocal(name); r >= 0 {
		return newExp(eLocal, r)
	}
	// 2) 已登记 upvalue
	if u := fs.findUpval(name); u >= 0 {
		return newExp(eUpval, u)
	}
	// 3) 沿外层链
	if fs.prev != nil {
		outer := fs.prev.resolveName(line, name)
		switch outer.k {
		case eLocal:
			// 标记外层 block hasUpval,使其退出时 CLOSE
			fs.prev.markUpvalCapture(outer.info)
			idx := fs.addUpval(line, name, true, uint8(outer.info))
			return newExp(eUpval, idx)
		case eUpval:
			idx := fs.addUpval(line, name, false, uint8(outer.info))
			return newExp(eUpval, idx)
		case eGlobal:
			// 全局穿透
		}
	}
	// 4) 全局
	k := fs.strK(line, name)
	return newExp(eGlobal, k)
}

// markUpvalCapture 把第 reg 个局部所在 block(及其所有更内层块)标 hasUpval(04 §6.1 / §8.4)。
func (fs *funcState) markUpvalCapture(reg int) {
	for b := fs.bl; b != nil; b = b.prev {
		if reg >= b.nactvarSnap {
			b.hasUpval = true
			return
		}
	}
}

// expr 把一个 ast.Expr 编译为 expDesc(延迟物化)。
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
		// LUA_COMPAT_VARARG:函数体用了 `...` 即不再需要隐式 arg 表
		// (官方 lparser:fs->f->is_vararg &= ~VARARG_NEEDSARG;
		// "arg" 局部仍占寄存器,值留 nil)。
		fs.proto.NeedsArg = false
		pc := fs.emitABC(e.Line, bytecode.VARARG, 0, 1, 0)
		return newExp(eVararg, pc)
	case *ast.NameExpr:
		return fs.resolveName(e.Line, e.Name)
	case *ast.ParenExpr:
		// 括号强制单值:把内部的 Call/Vararg 收敛为单值形态(04 §9.4)
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

// exprIndex 编译 t[k] / t.field。
func (fs *funcState) exprIndex(e *ast.IndexExpr) expDesc {
	obj := fs.expr(e.Obj)
	tableReg := fs.exp2AnyReg(e.Line, &obj)
	key := fs.expr(e.Key)
	rk := fs.exp2RK(e.Line, &key)
	exp := newExp(eIndexed, tableReg)
	exp.aux = rk
	return exp
}

// exprCall 编译 f(args...);末位多值时设 B=0(到 top)。
func (fs *funcState) exprCall(e *ast.CallExpr) expDesc {
	fnReg := fs.freereg
	fnExp := fs.expr(e.Fn)
	fs.exp2NextReg(e.Line, &fnExp)
	nargs := fs.compileArgList(e.Args, e.Line)
	b := nargs + 1
	if nargs < 0 { // 末位多值
		b = 0
	}
	pc := fs.emitABC(e.Line, bytecode.CALL, fnReg, b, 2) // C=2 默认单值,后续可改
	fs.freereg = fnReg + 1                               // 调用结果默认占 R(fnReg) 1 个
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

// exprMethodCall 编译 obj:m(args)— SELF + CALL。
func (fs *funcState) exprMethodCall(e *ast.MethodCallExpr) expDesc {
	baseReg := fs.freereg
	recv := fs.expr(e.Recv)
	fs.exp2NextReg(e.Line, &recv) // R(baseReg) = obj
	// 方法名走 RK 常量
	method := newExp(eK, fs.strK(e.Line, e.Method))
	rk := fs.exp2RK(e.Line, &method)
	fs.emitABC(e.Line, bytecode.SELF, baseReg, baseReg, rk)
	fs.reserveRegs(e.Line, 1) // SELF 额外占 R(baseReg+1)(self)
	nargs := fs.compileArgList(e.Args, e.Line)
	b := nargs + 1 + 1 // self + nargs
	if nargs < 0 {
		b = 0
	}
	pc := fs.emitABC(e.Line, bytecode.CALL, baseReg, b, 2)
	fs.freereg = baseReg + 1
	return expDesc{k: eCall, info: pc, tJmp: NoJump, fJmp: NoJump}
}

// compileArgList 把 args 落到连续寄存器;末位多值返回 -1,固定个数返回个数。
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

// exprBin 编译二元表达式;算术折叠,比较走 EQ/LT/LE,逻辑走短路。
func (fs *funcState) exprBin(e *ast.BinExpr) expDesc {
	switch e.Op {
	case ast.OpAnd:
		l := fs.expr(e.L)
		fs.goIfTrue(e.Line, &l) // l 为假则跳(链入 fJmp);真则继续(落到右子)
		r := fs.expr(e.R)
		// 对齐 Lua 5.1 luaK_posfix(OPR_AND):先 dischargeVars(e2) 把 VCALL/VVARARG
		// 收敛为单值形态——否则带跳转链的 eCall 会被 adjustExprList 误走多值分支,
		// 跳转链永不回填(JMP sBx=-1 死循环)。
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
	// 算术
	// 对齐 Lua 5.1 luaK_infix:左操作数必须在编译右子表达式之前物化为 RK
	// ——否则右子的 CALL/GETGLOBAL 等会覆盖左结果将要落的寄存器。
	// 豁免条件 = 官方 isnumeral:eKNum 且无未决跳转链。带链的 eKNum
	// (如 `(a and 7 or -1)`)若延迟物化,链中 TESTSET 会跳过右子指令
	// (求值序错误),折叠则直接丢链(NoRegister 占位越界 panic)。
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
	rb := lrk
	if rb < 0 {
		rb = fs.exp2RK(e.Line, &l)
	}
	rc := fs.exp2RK(e.Line, &r)
	// 顺序:先归还高位临时再归还低位(维持栈式)
	if !bytecode.IsK(rc) {
		fs.freeReg(rc)
	}
	if !bytecode.IsK(rb) {
		fs.freeReg(rb)
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

// isNumeral 等价官方 lcode.c isnumeral:纯数字字面量(无未决跳转链)
// 才可延迟物化 / 参与常量折叠。
func isNumeral(e *expDesc) bool {
	return e.k == eKNum && !e.hasJumps()
}

// constFold 在双方都是纯数字字面量(isNumeral)时编译期算结果。
func constFold(op ast.BinOp, l, r *expDesc) (float64, bool) {
	if !isNumeral(l) || !isNumeral(r) {
		return 0, false
	}
	a, b := l.nval, r.nval
	switch op {
	case ast.OpAdd:
		return a + b, true
	case ast.OpSub:
		return a - b, true
	case ast.OpMul:
		return a * b, true
	case ast.OpDiv:
		return a / b, true
	case ast.OpMod:
		return a - math.Floor(a/b)*b, true
	case ast.OpPow:
		return math.Pow(a, b), true
	}
	return 0, false
}

// exprCompare 编译比较;EQ/LT/LE 三档,?= / > / >= 通过交换+取反映射。
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
	lrk := -1
	if !isNumeral(&l) {
		lrk = fs.exp2RK(e.Line, &l)
	}
	r := fs.expr(e.R)
	rb := lrk
	if rb < 0 {
		rb = fs.exp2RK(e.Line, &l)
	}
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

// exprConcat 编译 a..b..c — 右结合,折叠为单条 CONCAT(B..C)。
func (fs *funcState) exprConcat(e *ast.BinExpr) expDesc {
	// 收集右展开的所有操作数:a..(b..(c..d)) 平铺为 [a,b,c,d]
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
	// 释放 base..last(待 exp2reg 时回填 A,寄存器水位先回到 base 之前)
	for fs.freereg > base {
		fs.freereg--
	}
	return expDesc{k: eRelocable, info: pc, tJmp: NoJump, fJmp: NoJump}
}

// exprUn 编译一元;-/not/#。
func (fs *funcState) exprUn(e *ast.UnExpr) expDesc {
	sub := fs.expr(e.E)
	switch e.Op {
	case ast.OpUnm:
		// 常量折叠:-数字字面量(isnumeral:带跳转链不可折,链会被丢弃)
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
		// 短路:对带跳转链的表达式直接交换 t/f
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

// exprFunc 编译函数字面量(嵌套 Proto + CLOSURE)。
func (fs *funcState) exprFunc(e *ast.FuncExpr) expDesc {
	proto := fs.cg.compileFunc(fs, e)
	idx := uint32(len(fs.cg.protos) - 1) // 最近登记的 ProtoID
	fs.proto.Protos = append(fs.proto.Protos, idx)
	fs.proto.SubNUps = append(fs.proto.SubNUps, uint8(len(proto.UpvalDescs)))
	closureIdx := len(fs.proto.Protos) - 1
	pc := fs.emitABx(e.Line, bytecode.CLOSURE, 0, closureIdx)
	// 紧跟 nupvals 条伪指令
	for _, u := range proto.UpvalDescs {
		if u.InStack {
			fs.emitABC(e.Line, bytecode.MOVE, 0, int(u.Idx), 0)
		} else {
			fs.emitABC(e.Line, bytecode.GETUPVAL, 0, int(u.Idx), 0)
		}
	}
	return expDesc{k: eRelocable, info: pc, tJmp: NoJump, fJmp: NoJump}
}

// emitSetList 发射 SETLIST(对齐官方 luaK_setlist):批号 c 超 9-bit C 字段
// (MaxBC=511)时发 C=0 + 后随一条裸批号指令——旧实现 EncodeABC 对 C 静默
// &0x1FF 截断,第 512 批回绕成 0,解释器把下一条正常指令当批号吞掉
// (>25550 项的表构造挂死)。
func (fs *funcState) emitSetList(line int32, tReg, b, batchNo int) {
	if batchNo <= bytecode.MaxBC {
		fs.emitABC(line, bytecode.SETLIST, tReg, b, batchNo)
		return
	}
	fs.emitABC(line, bytecode.SETLIST, tReg, b, 0)
	fs.emit(line, bytecode.Instruction(batchNo)) // 官方同走 luaK_code 裸字
}

// exprTable 编译表构造:NEWTABLE + SETLIST(批量数组) + SETTABLE(哈希字段)。
func (fs *funcState) exprTable(e *ast.TableExpr) expDesc {
	tReg := fs.freereg
	pc := fs.emitABC(e.Line, bytecode.NEWTABLE, tReg, 0, 0) // B/C 后续回填
	fs.reserveRegs(e.Line, 1)

	// —— 数组部分 ——
	nArr := len(e.AKeys)
	flush := bytecode.FieldsPerFlush
	pending := 0 // 当前批已落到 R(tReg+1+pending) 的项数
	batchNo := 1 // 批号(SETLIST 的 C)
	for i, v := range e.AKeys {
		isLast := i == nArr-1
		ve := fs.expr(v)
		if isLast && fs.openMultiRet(&ve, -1) {
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

	// —— 哈希部分 ——
	for i := range e.HKeys {
		ke := fs.expr(e.HKeys[i])
		rkK := fs.exp2RK(e.Line, &ke)
		ve := fs.expr(e.HVals[i])
		rkV := fs.exp2RK(e.Line, &ve)
		fs.emitABC(e.Line, bytecode.SETTABLE, tReg, rkK, rkV)
		// 释放哈希字段的临时
		if !bytecode.IsK(rkV) {
			fs.freeReg(rkV)
		}
		if !bytecode.IsK(rkK) {
			fs.freeReg(rkK)
		}
	}

	// 回填 NEWTABLE 的 B/C
	asz := uint32(nArr)
	hsz := uint32(len(e.HKeys))
	ins := fs.proto.Code[pc]
	ins = bytecode.SetB(ins, int(bytecode.Int2Fb(asz)))
	ins = bytecode.SetC(ins, int(bytecode.Int2Fb(hsz)))
	fs.proto.Code[pc] = ins

	// 把 NEWTABLE 落点暴露为 ENonReloc(已在 R(tReg))
	return expDesc{k: eNonReloc, info: tReg, tJmp: NoJump, fJmp: NoJump}
}
