// Main interpreter loop — 取指 → 译码 → 执行 (05 §2.3 / §12)。M9 范围内不接
// IC、元表、协程、GC,不写 safepoint(M10 增量补 §5)。
package crescent

import (
	"fmt"
	"math"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TraceExec 打开逐指令 trace(调试用,默认关)。
var TraceExec = false

// execute 跑当前栈顶 fresh CallInfo 直到它退出(05 §7.3 entry edge)。
//
// reentry 模型:Lua-call-Lua 通过修改 ci/proto/code 局部变量在同一个 Go 栈帧里
// 重入 — Go 栈深度恒为 1(05 §7.1)。
func (st *State) execute(th *thread) *LuaError {
	return st.executeFrom(th, len(th.cis)-1)
}

// executeFrom 以指定 entry 深度跑主循环(协程 resume 恢复时复用,08 §3.5)。
//
// 出错时(非 yield 哨兵)统一加 "chunkname:line:" 位置前缀(09)。
func (st *State) executeFrom(th *thread, entryDepth int) *LuaError {
	e := st.executeLoop(th, entryDepth)
	if e != nil && e != errYieldSentinel && len(th.cis) > 0 {
		e = st.annotateError(e, currentCI(th))
	}
	return e
}

func (st *State) executeLoop(th *thread, entryDepth int) *LuaError {
	ci := currentCI(th)
	code := ci.proto.Code

	for {
		if int(ci.pc) >= len(code) {
			return errf("interpreter: pc out of range")
		}
		i := code[ci.pc]
		if TraceExec {
			fmt.Printf("[trace] ciDepth=%d base=%d pc=%d top=%d %s A=%d B=%d C=%d\n",
				len(th.cis), ci.base, ci.pc, th.top,
				bytecode.Op(i), bytecode.A(i), bytecode.B(i), bytecode.C(i))
		}
		ci.pc++

		switch bytecode.Op(i) {

		case bytecode.MOVE:
			setReg(th, ci, bytecode.A(i), reg(th, ci, bytecode.B(i)))

		case bytecode.LOADK:
			setReg(th, ci, bytecode.A(i), ci.proto.Consts[bytecode.Bx(i)])

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
			uv := object.ClosureUpvalRef(st.arena, ci.cl, uint16(bytecode.B(i)))
			setReg(th, ci, bytecode.A(i), st.upvalGet(th, uv))

		case bytecode.SETUPVAL:
			uv := object.ClosureUpvalRef(st.arena, ci.cl, uint16(bytecode.B(i)))
			st.upvalSet(th, uv, reg(th, ci, bytecode.A(i)))

		case bytecode.GETGLOBAL:
			key := ci.proto.Consts[bytecode.Bx(i)]
			gv := value.MakeGC(value.TagTable, st.globals)
			v, e := st.icGetTable(th, ci, ci.pc-1, gv, key)
			if e != nil {
				return e
			}
			ci = currentCI(th)
			code = ci.proto.Code
			setReg(th, ci, bytecode.A(i), v)

		case bytecode.SETGLOBAL:
			key := ci.proto.Consts[bytecode.Bx(i)]
			gv := value.MakeGC(value.TagTable, st.globals)
			if e := st.icSetTable(th, ci, ci.pc-1, gv, key, reg(th, ci, bytecode.A(i))); e != nil {
				return e
			}
			ci = currentCI(th)
			code = ci.proto.Code
			st.safepoint(th, ci)

		case bytecode.GETTABLE:
			tbl := reg(th, ci, bytecode.B(i))
			key := rk(th, ci, bytecode.C(i))
			v, e := st.icGetTable(th, ci, ci.pc-1, tbl, key)
			if e != nil {
				return st.enhanceIndexErr(e, ci, bytecode.B(i), tbl)
			}
			// __index handler 可能重入 execute(append cis)→ 刷新 ci 指针
			ci = currentCI(th)
			code = ci.proto.Code
			setReg(th, ci, bytecode.A(i), v)

		case bytecode.SETTABLE:
			tbl := reg(th, ci, bytecode.A(i))
			key := rk(th, ci, bytecode.B(i))
			val := rk(th, ci, bytecode.C(i))
			if e := st.icSetTable(th, ci, ci.pc-1, tbl, key, val); e != nil {
				return st.enhanceIndexErr(e, ci, bytecode.A(i), tbl)
			}
			ci = currentCI(th)
			code = ci.proto.Code
			st.safepoint(th, ci)

		case bytecode.NEWTABLE:
			asz := bytecode.Fb2Int(uint32(bytecode.B(i)))
			hsz := bytecode.Fb2Int(uint32(bytecode.C(i)))
			// Lua 5.1 NEWTABLE 的 hsize 在 fb 解码后未必是 2 的幂;allocTable 要求 2 的幂。
			t := st.allocTable(asz, roundUpPow2(hsz))
			setReg(th, ci, bytecode.A(i), value.MakeGC(value.TagTable, t))
			st.safepoint(th, ci)

		case bytecode.SELF:
			tbl := reg(th, ci, bytecode.B(i))
			setReg(th, ci, bytecode.A(i)+1, tbl)
			key := rk(th, ci, bytecode.C(i))
			v, e := st.icGetTable(th, ci, ci.pc-1, tbl, key)
			if e != nil {
				return e
			}
			ci = currentCI(th)
			code = ci.proto.Code
			setReg(th, ci, bytecode.A(i), v)

		case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV, bytecode.MOD, bytecode.POW:
			if e := st.doArith(th, ci, i); e != nil {
				return e
			}
			// __add 等 handler 可能重入 execute → 刷新
			ci = currentCI(th)
			code = ci.proto.Code

		case bytecode.UNM:
			b := reg(th, ci, bytecode.B(i))
			if f, ok := st.toNumberCoerce(b); ok {
				setReg(th, ci, bytecode.A(i), value.NumberValue(-f))
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
				code = ci.proto.Code
				setReg(th, ci, bytecode.A(i), res)
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
			ci.pc += int32(bytecode.SBx(i))

		case bytecode.EQ, bytecode.LT, bytecode.LE:
			res, e := st.doCompare(th, ci, i)
			if e != nil {
				return e
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
				// host 路径:host 内部可能重入 execute(pcall 等),th.cis 底层数组
				// 可能因 append 重分配 — 旧 ci 指针失效,必须刷新。
				ci = currentCI(th)
			}
			code = ci.proto.Code

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
			code = ci.proto.Code

		case bytecode.RETURN:
			next, terminate := st.doReturn(th, ci, i, entryDepth)
			if terminate {
				return nil
			}
			ci = next
			code = ci.proto.Code

		case bytecode.FORLOOP:
			a := bytecode.A(i)
			idx := value.AsNumber(reg(th, ci, a))
			step := value.AsNumber(reg(th, ci, a+2))
			limit := value.AsNumber(reg(th, ci, a+1))
			idx += step
			cont := false
			if step >= 0 {
				cont = idx <= limit
			} else {
				cont = idx >= limit
			}
			if cont {
				setReg(th, ci, a, value.NumberValue(idx))
				setReg(th, ci, a+3, value.NumberValue(idx))
				ci.pc += int32(bytecode.SBx(i))
			}

		case bytecode.FORPREP:
			a := bytecode.A(i)
			// 三槽校验:可经 string coercion(5.1 对 for 也做 tonumber,07 §5.2)
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
			// 调用迭代器 R(A)(R(A+1), R(A+2)),结果落 R(A+3..A+2+C)(05 §10.2)。
			// 迭代器经 callLuaFromHost 同步取结果(Lua 迭代器走 host→Lua 重入;
			// next 等 host 迭代器走 host 直调)。
			a := bytecode.A(i)
			c := bytecode.C(i)
			iter := reg(th, ci, a)
			state := reg(th, ci, a+1)
			ctrl := reg(th, ci, a+2)
			results, e := st.callLuaFromHost(th, iter, []value.Value{state, ctrl})
			if e != nil {
				return e
			}
			ci = currentCI(th)
			code = ci.proto.Code
			for k := 0; k < c; k++ {
				v := value.Nil
				if k < len(results) {
					v = results[k]
				}
				setReg(th, ci, a+3+k, v)
			}
			if c >= 1 && len(results) >= 1 && results[0] != value.Nil {
				setReg(th, ci, a+2, results[0]) // 控制变量 = 首返回值
				// 落到紧随的回边 JMP
			} else {
				ci.pc++ // 首值 nil:跳过回边,退出循环
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

// toNumber 把 Value 转 float64;成功返回值 + true。number 直接转;
// string 经 ParseLuaNumber(07 §5.2 唯一入口:算术/数值 for/tonumber 共用)。
func (st *State) toNumberCoerce(v value.Value) (float64, bool) {
	if value.IsNumber(v) {
		return value.AsNumber(v), true
	}
	if value.Tag(v) == value.TagString {
		return parseLuaNumberBytes(object.StringBytes(st.arena, value.GCRefOf(v)))
	}
	return 0, false
}

// 算术辅助。快路径双 number;string coercion(07 §5.2);慢路径 __add 等元方法。
func (st *State) doArith(th *thread, ci *callInfo, i bytecode.Instruction) *LuaError {
	b := rk(th, ci, bytecode.B(i))
	c := rk(th, ci, bytecode.C(i))
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
		return nil
	}
	// 慢路径:__add 等元方法;无元方法时报带名字描述的错误(09 §8.3)
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

// 比较辅助。
func (st *State) doCompare(th *thread, ci *callInfo, i bytecode.Instruction) (bool, *LuaError) {
	b := rk(th, ci, bytecode.B(i))
	c := rk(th, ci, bytecode.C(i))
	switch bytecode.Op(i) {
	case bytecode.EQ:
		return st.rawEqual(b, c), nil
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
		// 混合类型不自动转(05 §4.4);同类报 "two X values",异类报 "X with Y"(5.1)
		tb, tc := typeName(b), typeName(c)
		if tb == tc {
			return false, errf("attempt to compare two %s values", tb)
		}
		return false, errf("attempt to compare %s with %s", tb, tc)
	}
	return false, errf("interpreter: bad compare op")
}

func (st *State) rawEqual(a, b value.Value) bool {
	// 数字必须先走浮点比较:canonNaN bits 相等但 NaN ≠ NaN(IEEE);+0 == -0。
	if value.IsNumber(a) && value.IsNumber(b) {
		return value.AsNumber(a) == value.AsNumber(b)
	}
	return a == b
}
