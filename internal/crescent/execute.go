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
	entryDepth := len(th.cis) - 1
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
			v, e := st.tableGet(st.globals, key)
			if e != nil {
				return e
			}
			setReg(th, ci, bytecode.A(i), v)

		case bytecode.SETGLOBAL:
			key := ci.proto.Consts[bytecode.Bx(i)]
			if e := st.tableSet(st.globals, key, reg(th, ci, bytecode.A(i))); e != nil {
				return e
			}
			st.safepoint(th, ci)

		case bytecode.GETTABLE:
			tbl := reg(th, ci, bytecode.B(i))
			if value.Tag(tbl) != value.TagTable {
				return errf("attempt to index a %s value", typeName(tbl))
			}
			key := rk(th, ci, bytecode.C(i))
			v, e := st.tableGet(value.GCRefOf(tbl), key)
			if e != nil {
				return e
			}
			setReg(th, ci, bytecode.A(i), v)

		case bytecode.SETTABLE:
			tbl := reg(th, ci, bytecode.A(i))
			if value.Tag(tbl) != value.TagTable {
				return errf("attempt to index a %s value", typeName(tbl))
			}
			key := rk(th, ci, bytecode.B(i))
			val := rk(th, ci, bytecode.C(i))
			if e := st.tableSet(value.GCRefOf(tbl), key, val); e != nil {
				return e
			}
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
			if value.Tag(tbl) != value.TagTable {
				return errf("attempt to index a %s value", typeName(tbl))
			}
			key := rk(th, ci, bytecode.C(i))
			v, e := st.tableGet(value.GCRefOf(tbl), key)
			if e != nil {
				return e
			}
			setReg(th, ci, bytecode.A(i), v)

		case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV, bytecode.MOD, bytecode.POW:
			if e := st.doArith(th, ci, i); e != nil {
				return e
			}

		case bytecode.UNM:
			b := reg(th, ci, bytecode.B(i))
			if !value.IsNumber(b) {
				return errf("attempt to perform arithmetic on a %s value", typeName(b))
			}
			setReg(th, ci, bytecode.A(i), value.NumberValue(-value.AsNumber(b)))

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
				// border:M9 简化为线性扫描 array 部分
				border := tableBorder(st.arena, value.GCRefOf(b))
				setReg(th, ci, bytecode.A(i), value.NumberValue(float64(border)))
			default:
				if value.IsNumber(b) {
					return errf("attempt to get length of a number value")
				}
				return errf("attempt to get length of a %s value", typeName(b))
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
				code = ci.proto.Code
			}

		case bytecode.TAILCALL:
			next, e := st.doTailCall(th, ci, i)
			if e != nil {
				return e
			}
			if next != nil {
				ci = next
				code = ci.proto.Code
			}

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
			init, ok1 := toNumber(reg(th, ci, a))
			limit, ok2 := toNumber(reg(th, ci, a+1))
			step, ok3 := toNumber(reg(th, ci, a+2))
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
			return errf("TFORLOOP: generic for not yet supported (M12)")

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
// string 在 M9 暂只 raw bytes 经过 strconv 一种简单路径(M11 拉齐 parseLuaNumber)。
func toNumber(v value.Value) (float64, bool) {
	if value.IsNumber(v) {
		return value.AsNumber(v), true
	}
	return 0, false
}

// 算术辅助。
func (st *State) doArith(th *thread, ci *callInfo, i bytecode.Instruction) *LuaError {
	b := rk(th, ci, bytecode.B(i))
	c := rk(th, ci, bytecode.C(i))
	if !value.IsNumber(b) {
		return errf("attempt to perform arithmetic on a %s value", typeName(b))
	}
	if !value.IsNumber(c) {
		return errf("attempt to perform arithmetic on a %s value", typeName(c))
	}
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
	return nil
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
		// 混合类型不自动转(05 §4.4)
		if value.IsNumber(b) || value.Tag(b) == value.TagString {
			return false, errf("attempt to compare %s with %s", typeName(b), typeName(c))
		}
		return false, errf("attempt to compare %s with %s", typeName(b), typeName(c))
	}
	return false, errf("interpreter: bad compare op")
}

func (st *State) rawEqual(a, b value.Value) bool {
	if a == b {
		return true
	}
	// 数字:bits 不等但语义可能相等(+0/-0;NaN 永不等)
	if value.IsNumber(a) && value.IsNumber(b) {
		x, y := value.AsNumber(a), value.AsNumber(b)
		return x == y
	}
	// 异类不等
	if value.Tag(a) != value.Tag(b) {
		return false
	}
	// string:GCRef 已 intern 相等(由 a==b 命中);但 intern 可能在不同 Program 加载时复用,
	// 仍走对比 bytes 兜底以保正确(M9 简化:同 GCRef 即等,不等则不等)。
	return false
}
