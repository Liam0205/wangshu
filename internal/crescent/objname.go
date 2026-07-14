// describeReg — getobjname 的 P1 简化版(09 §8.3:local/global/field/method)。
//
// 给错误消息提供 "local 'x'" / "global 'f'" / "field 'k'" / "method 'm'" 名字
// 描述;查不到返回 ""(调用方退回无名形态 "a nil value")。
package crescent

import (
	"fmt"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// localName 返回第 reg 个寄存器在 pc 处对应的活跃局部名(luaF_getlocalname
// 同构:寄存器 r = 第 r+1 个活跃局部,局部占低位寄存器连续)。
func localName(proto *bytecode.Proto, pc int32, reg int) string {
	n := reg + 1
	for i := range proto.LocVars {
		lv := &proto.LocVars[i]
		if lv.StartPC > pc {
			break
		}
		if pc < lv.EndPC || lv.EndPC == 0 && lv.StartPC <= pc {
			// 活跃(EndPC==0 表示函数末尾才闭合且尚未回填的场景按活跃处理)
			n--
			if n == 0 {
				return lv.Name
			}
		}
	}
	return ""
}

// describeReg 给寄存器操作数找名字描述。
//
// 顺序:① 活跃局部(LocVars);② 正向符号执行(官方 ldebug.c symbexec
// 同构):从函数头走到出错 pc,维护"最后写 reg 的指令" last,**前向 JMP
// 按跳转执行**(被跳过的写指令不算)、TEST/TESTSET 等测试类指令命中 reg
// 时 last 落它(不可命名 → 无名退化,对齐官方 `(aaa or aaa)()` 报无名)。
// 倒序朴素回看会撞上被 JMP 跳过的"未执行"指令,产出错误名字。
func describeReg(proto *bytecode.Proto, pc int32, reg int) string {
	return describeRegDepth(proto, pc, reg, 0)
}

func describeRegDepth(proto *bytecode.Proto, pc int32, reg int, depth int) string {
	if depth > 4 {
		return ""
	}
	if name := localName(proto, pc, reg); name != "" {
		// 内部控制变量((for index) 等)不暴露
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
		// 临时是局部的副本:跟到源寄存器(`local f; f()` 的 CALL 形状);
		// 官方限制 b < a(只跟"高位临时 ← 低位局部"的拷贝)
		if b := bytecode.B(ins); b < bytecode.A(ins) {
			return describeRegDepth(proto, pc, b, depth+1)
		}
	case bytecode.GETGLOBAL:
		if name, ok := constStringAt(proto, bytecode.Bx(ins)); ok {
			return fmt.Sprintf("global '%s'", name)
		}
	case bytecode.GETUPVAL:
		// 闭包捕获的外层变量(官方 getobjname 的 OP_GETUPVAL 分支)
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

// symbexec 正向符号执行到 lastpc,返回最后写 reg 的指令(官方 symbexec
// 的命名子集:省去字节码合法性 check,只保留 last 追踪与控制流)。
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
			// 官方对 FORLOOP/FORPREP 也走 JMP 跳转逻辑;命名场景下回边
			// (负位移)不跟随,前向不出现,略。
		case bytecode.JMP:
			dest := pc + 1 + int32(bytecode.SBx(ins))
			// 前向且不跳过 lastpc 的 JMP 按执行处理(被跳过的写指令不算)
			if pc < dest && dest <= lastpc {
				pc = dest - 1 // for 自增后 = dest
			}
		case bytecode.CLOSURE:
			if a == reg {
				last = pc
			}
			// 精确跳过 upvalue 伪指令(官方经 p->p[bx]->nups;本实现 codegen
			// 把伪指令数随 CLOSURE 存进 SubNUps,按 Protos 下标对齐)。
			// 形态猜测会把 0-upvalue CLOSURE 后的真实 MOVE/GETUPVAL 吞掉,
			// 丢失实参装载的命名信息。
			if idx := bytecode.Bx(ins); idx < len(proto.SubNUps) {
				pc += int32(proto.SubNUps[idx])
			}
		case bytecode.SETLIST:
			if bytecode.C(ins) == 0 {
				pc++ // 跳过后随裸批号字
			}
		default:
			// testAMode 类(写 A 或标记 A):MOVE/LOADK/LOADBOOL/GET*/
			// 算术/UNM/NOT/LEN/CONCAT/TEST/TESTSET/NEWTABLE/VARARG
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

// opWritesA 对齐官方 testAMode 位(指令"修改/标记"寄存器 A)。
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

// constStringAt 取常量池 k 槽的字符串字面量(经 StringLits 原文,Compile 期
// 形态;装载后 Consts 已是 GCRef,从 StringLits 取最稳)。
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

// errWithName 构造带名字描述的类型错误(5.1 格式:
// "attempt to <verb> <name> (a <type> value)";无名退回 "attempt to <verb> a <type> value")。
func (st *State) errWithName(ci *callInfo, verb string, rkOperand int, v value.Value) *LuaError {
	name := ""
	if !bytecode.IsK(rkOperand) {
		// pc-1:出错指令本身(主循环已自增)
		name = describeReg(st.protoOf(ci), ci.pc-1, rkOperand)
	}
	if name != "" {
		return errf("attempt to %s %s (a %s value)", verb, name, st.typeNameOf(v))
	}
	return errf("attempt to %s a %s value", verb, st.typeNameOf(v))
}

// describeRKForArith 给算术错误找出错操作数(b 先 c 后:不能转数字的那个)。
func (st *State) arithErrWithName(ci *callInfo, i bytecode.Instruction, b, c value.Value) *LuaError {
	badV := b
	badRK := bytecode.B(i)
	if value.IsNumber(b) || coercibleToNumber(st, b) {
		badV = c
		badRK = bytecode.C(i)
	}
	return st.errWithName(ci, "perform arithmetic on", badRK, badV)
}

// coercibleToNumber 判定 v 是否可经 5.1 算术 coercion 转数字(string 形态)。
func coercibleToNumber(st *State, v value.Value) bool {
	if value.Tag(v) != value.TagString {
		return false
	}
	_, ok := parseLuaNumberBytes(object.StringBytes(st.arena, value.GCRefOf(v)))
	return ok
}

// enhanceIndexErr 给 "attempt to index a X value" 错误补名字描述(出错对象
// 是寄存器 reg 上的 obj 时)。其它错误(如 __index handler 内部错误)原样透传。
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
