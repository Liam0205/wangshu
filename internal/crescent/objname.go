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
// 顺序:① 活跃局部(LocVars);② 回看产生指令(MOVE → 跟到源寄存器,
// GETGLOBAL → global,GETTABLE 常量键 → field,SELF → method)。P1 不做
// 完整 symbexec,只回看紧邻产生点(覆盖 `f()`/`t.x()`/`t:m()` 的典型形状)。
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
	// 回看:从 pc-1 向前找最近一条把结果写进 reg 的取值指令
	for back := pc - 1; back >= 0 && back >= pc-8; back-- {
		ins := proto.Code[back]
		if bytecode.A(ins) != reg {
			continue
		}
		switch bytecode.Op(ins) {
		case bytecode.MOVE:
			// 临时是局部的副本:跟到源寄存器(`local f; f()` 的 CALL 形状)
			return describeRegDepth(proto, back, bytecode.B(ins), depth+1)
		case bytecode.GETGLOBAL:
			if name, ok := constStringAt(proto, bytecode.Bx(ins)); ok {
				return fmt.Sprintf("global '%s'", name)
			}
		case bytecode.GETUPVAL:
			// 闭包捕获的外层变量(官方 getupvalname 分支):
			// `local x; pcall(function() return x + 1 end)` 中 x 是 upvalue。
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
		return "" // 找到产生指令但不是可命名形态
	}
	return ""
}

// constStringAt 取常量池 k 槽的字符串字面量(经 StringLits 原文,Compile 期
// 形态;装载后 Consts 已是 GCRef,从 StringLits 取最稳)。
func constStringAt(proto *bytecode.Proto, k int) (string, bool) {
	if k < len(proto.StringLitIdx) && proto.StringLitIdx[k] >= 0 {
		return proto.StringLits[proto.StringLitIdx[k]], true
	}
	return "", false
}

// errWithName 构造带名字描述的类型错误(5.1 格式:
// "attempt to <verb> <name> (a <type> value)";无名退回 "attempt to <verb> a <type> value")。
func (st *State) errWithName(ci *callInfo, verb string, rkOperand int, v value.Value) *LuaError {
	name := ""
	if !bytecode.IsK(rkOperand) {
		// pc-1:出错指令本身(主循环已自增)
		name = describeReg(ci.proto, ci.pc-1, rkOperand)
	}
	if name != "" {
		return errf("attempt to %s %s (a %s value)", verb, name, typeName(v))
	}
	return errf("attempt to %s a %s value", verb, typeName(v))
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
	plain := fmt.Sprintf("attempt to index a %s value", typeName(obj))
	if e.Msg != plain {
		return e
	}
	if name := describeReg(ci.proto, ci.pc-1, reg); name != "" {
		e.Msg = fmt.Sprintf("attempt to index %s (a %s value)", name, typeName(obj))
	}
	return e
}
