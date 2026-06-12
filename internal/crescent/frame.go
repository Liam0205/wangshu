// Frame management — enterLuaFrame / popCallInfo / execute 的栈布局。
package crescent

import (
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// 调用深度上限(05 §7.4,对齐官方 5.1.5 luaconf.h)。
const (
	maxLuaCallDepth = 20000 // LUAI_MAXCALLS:CallInfo 链长上限,超限抛 "stack overflow"
	maxCCallDepth   = 200   // LUAI_MAXCCALLS:host→Lua 重入(真 Go 栈)上限,超限抛 "C stack overflow"
)

// enterLuaFrame 准备一帧并压 CallInfo(05 §1.4)。
//
// funcIdx 是被调 closure 在栈上的索引;实参紧随其后(funcIdx+1..funcIdx+1+nargs)。
// nresults<0 表示调用者要"全部返回"。entry=true 标 callStatus_fresh(execute 边界,
// RETURN 退到此帧之下即终止 execute)。
func (st *State) enterLuaFrame(th *thread, funcIdx, nargs, nresults int, entry bool) *LuaError {
	// Lua 调用深度上限(05 §7.4;LUAI_MAXCALLS=20000 等价,对齐 5.1.5 luaconf.h)。
	// TAILCALL 先 pop 再 enter,净深度不变,proper tail call 不受限。
	if len(th.cis) >= maxLuaCallDepth {
		return errf("stack overflow")
	}
	// 指令预算的调用计费点:纯递归风暴(蹦床式互递归在深度限内反复进出)
	// 不经回边,只在此计费才兜得住。预算关闭且 ctx 未注入时 chargeStep
	// 内部短路。
	if e := st.chargeStep(); e != nil {
		return e
	}
	v := th.stack[funcIdx]
	if value.Tag(v) != value.TagFunction {
		return errf("attempt to call a %s value", typeName(v))
	}
	cl := value.GCRefOf(v)
	if object.IsHostClosure(st.arena, cl) {
		// 防御:正常 Lua → host 走 doCall/doTailCall 的 callHost 分支;
		// 走到 enterLuaFrame 意味着调用入口绕过了 dispatch(internal bug)。
		return errf("call: host closure cannot enter Lua frame (internal dispatch bug)")
	}
	pid := object.ClosureProtoID(st.arena, cl)
	proto := st.protos[pid]
	base := funcIdx + 1
	// vararg 与多/少补 nil
	numFixed := int(proto.NumParams)
	var varargs []value.Value
	switch {
	case nargs > numFixed && proto.IsVararg:
		// 把超出固定参的部分拷贝到 ci.varargs(M13 简化版,详细布局见 05 §8.5)
		varargs = make([]value.Value, nargs-numFixed)
		for i := 0; i < nargs-numFixed; i++ {
			varargs[i] = th.stack[base+numFixed+i]
		}
	case nargs > numFixed && !proto.IsVararg:
		// 实参超出固定形参,直接丢弃(Lua 5.1 行为)
	case nargs < numFixed:
		for i := nargs; i < numFixed; i++ {
			if base+i >= len(th.stack) {
				th.ensureStack(base + i + 1)
			}
			th.stack[base+i] = value.Nil
		}
	}
	// 备栈到 MaxStack
	need := base + int(proto.MaxStack)
	if need > len(th.stack) {
		th.ensureStack(need)
	}
	// 把 base..base+MaxStack 的剩余区清 nil(防止读到旧值)
	for i := base + numFixed; i < base+int(proto.MaxStack); i++ {
		th.stack[i] = value.Nil
	}
	// LUA_COMPAT_VARARG:隐式 arg 表(5.1 默认 compat;arg = {n=#varargs, ...},
	// 占形参后第一个寄存器,codegen 已 registerLocal("arg") 预留)
	if proto.NeedsArg {
		argTbl := st.allocTable(uint32(len(varargs)), 8)
		for i, v := range varargs {
			st.tableSetInt(argTbl, uint32(i+1), v)
		}
		nKey := value.MakeGC(value.TagString, st.gc.Intern([]byte("n")))
		_ = st.tableSet(argTbl, nKey, value.NumberValue(float64(len(varargs))))
		th.stack[base+numFixed] = value.MakeGC(value.TagTable, argTbl)
	}
	// 压 CallInfo
	ci := callInfo{
		base:     base,
		funcIdx:  funcIdx,
		top:      base + numFixed,
		proto:    proto,
		cl:       cl,
		nresults: nresults,
		fresh:    entry,
		pc:       0,
		varargs:  varargs,
	}
	th.cis = append(th.cis, ci)
	th.top = base + int(proto.MaxStack)
	return nil
}

// popCallInfo 弹出栈顶 CallInfo,返回它(供 doReturn 拿 nresults 等)。
func (st *State) popCallInfo(th *thread) callInfo {
	ci := th.cis[len(th.cis)-1]
	th.cis = th.cis[:len(th.cis)-1]
	return ci
}

// currentCI 返回栈顶 CallInfo 的指针(便于直接修改 pc/top)。
func currentCI(th *thread) *callInfo { return &th.cis[len(th.cis)-1] }

// rk 取一个 RK 操作数:< 256 取寄存器 R(rk);>=256 取常量 K(rk-256)。
func rk(th *thread, ci *callInfo, rk int) value.Value {
	if rk < bytecode.MaxK {
		return th.stack[ci.base+rk]
	}
	return ci.proto.Consts[rk-bytecode.MaxK]
}

// reg 简便寄存器读。
func reg(th *thread, ci *callInfo, r int) value.Value { return th.stack[ci.base+r] }

// setReg 简便寄存器写。
func setReg(th *thread, ci *callInfo, r int, v value.Value) {
	th.stack[ci.base+r] = v
}

// errf 构造一个 LuaError(M9 简化:Value 直接是错误字符串内容,
// 暂不 intern 进 arena;M11 错误模块再拉齐)。
func errf(format string, args ...any) *LuaError {
	msg := sprintf(format, args...)
	return &LuaError{Msg: msg}
}

// typeName 返回 Lua 类型名(用于错误消息)。
func typeName(v value.Value) string {
	if value.IsNumber(v) {
		return "number"
	}
	switch value.Tag(v) {
	case value.TagNil:
		return "nil"
	case value.TagBool:
		return "boolean"
	case value.TagLightUD, value.TagUserdata:
		return "userdata"
	case value.TagString:
		return "string"
	case value.TagTable:
		return "table"
	case value.TagFunction:
		return "function"
	case value.TagThread:
		return "thread"
	}
	return "unknown"
}
