// CALL / TAILCALL / RETURN / VARARG / SETLIST / 闭包构造。
//
// 注:M9 范围内 generic for(TFORLOOP)只支持 host 迭代器(M12 提供 next 等);
// 用 Lua 函数当迭代器尚未在 M9 验收要求里(05 §10.2 是后置工作)。
package crescent

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// doCall 执行一条 CALL,返回新的 ci(若进入了一个新 Lua 帧);
// 若调用走 host 路径(M9 暂未支持),返回 nil ci 加 LuaError。
func (st *State) doCall(th *thread, ci *callInfo, i bytecode.Instruction) (*callInfo, *LuaError) {
	a := bytecode.A(i)
	b := bytecode.B(i)
	c := bytecode.C(i)
	funcIdx := ci.base + a
	var nargs int
	if b == 0 {
		nargs = th.top - funcIdx - 1
	} else {
		nargs = b - 1
	}
	nresults := c - 1
	callee := th.stack[funcIdx]
	if value.Tag(callee) != value.TagFunction {
		return nil, errf("attempt to call a %s value", typeName(callee))
	}
	cl := value.GCRefOf(callee)
	if object.IsHostClosure(st.arena, cl) {
		return nil, errf("call: host closure not yet supported (M12)")
	}
	if e := st.enterLuaFrame(th, funcIdx, nargs, nresults, false); e != nil {
		return nil, e
	}
	return currentCI(th), nil
}

// doTailCall 复用当前帧执行新 closure 的调用。
func (st *State) doTailCall(th *thread, ci *callInfo, i bytecode.Instruction) (*callInfo, *LuaError) {
	a := bytecode.A(i)
	b := bytecode.B(i)
	funcIdx := ci.base + a
	var nargs int
	if b == 0 {
		nargs = th.top - funcIdx - 1
	} else {
		nargs = b - 1
	}
	callee := th.stack[funcIdx]
	if value.Tag(callee) != value.TagFunction {
		return nil, errf("attempt to call a %s value", typeName(callee))
	}
	cl := value.GCRefOf(callee)
	if object.IsHostClosure(st.arena, cl) {
		return nil, errf("call: host closure not yet supported (M12)")
	}
	st.closeUpvals(th, ci.base)
	dst := ci.funcIdx
	for k := 0; k < nargs+1; k++ {
		th.stack[dst+k] = th.stack[funcIdx+k]
	}
	parentNRes := ci.nresults
	parentFresh := ci.fresh
	st.popCallInfo(th)
	if e := st.enterLuaFrame(th, dst, nargs, parentNRes, parentFresh); e != nil {
		return nil, e
	}
	cci := currentCI(th)
	cci.tailcall = true
	return cci, nil
}

// doReturn 退出当前帧。terminate=true 表示退出了 entry 帧 → execute 结束。
func (st *State) doReturn(th *thread, ci *callInfo, i bytecode.Instruction, entryDepth int) (*callInfo, bool) {
	a := bytecode.A(i)
	b := bytecode.B(i)
	var nret int
	if b == 0 {
		nret = th.top - (ci.base + a)
	} else {
		nret = b - 1
	}
	st.closeUpvals(th, ci.base)
	dst := ci.funcIdx
	src := ci.base + a
	for k := 0; k < nret; k++ {
		th.stack[dst+k] = th.stack[src+k]
	}
	wantedN := ci.nresults
	st.popCallInfo(th)
	if wantedN < 0 {
		th.top = dst + nret
	} else {
		for k := nret; k < wantedN; k++ {
			th.stack[dst+k] = value.Nil
		}
		if len(th.cis) > entryDepth {
			caller := currentCI(th)
			th.top = caller.base + int(caller.proto.MaxStack)
		} else {
			th.top = dst + wantedN
		}
	}
	if len(th.cis) <= entryDepth {
		if wantedN >= 0 {
			th.top = dst + wantedN
		}
		return nil, true
	}
	caller := currentCI(th)
	return caller, false
}

// makeClosure 构造一个 Lua closure 并按后随伪指令(MOVE/GETUPVAL)填充 upvalue。
func (st *State) makeClosure(th *thread, ci *callInfo, i bytecode.Instruction) arena.GCRef {
	pid := ci.proto.Protos[bytecode.Bx(i)]
	subProto := st.protos[pid]
	cl := st.allocLuaClosure(pid, uint16(len(subProto.UpvalDescs)))
	for j := uint16(0); j < uint16(len(subProto.UpvalDescs)); j++ {
		pseudo := ci.proto.Code[ci.pc]
		ci.pc++
		switch bytecode.Op(pseudo) {
		case bytecode.MOVE:
			stackIdx := uint32(ci.base + bytecode.B(pseudo))
			uv := st.findOrCreateUpval(th, stackIdx)
			object.SetClosureUpvalRef(st.arena, cl, j, uv)
		case bytecode.GETUPVAL:
			parent := object.ClosureUpvalRef(st.arena, ci.cl, uint16(bytecode.B(pseudo)))
			object.SetClosureUpvalRef(st.arena, cl, j, parent)
		}
	}
	return cl
}

// doSetList 批量填表 array 部分(05 §11.2)。
func (st *State) doSetList(th *thread, ci *callInfo, i bytecode.Instruction) *LuaError {
	a := bytecode.A(i)
	b := bytecode.B(i)
	c := bytecode.C(i)
	if c == 0 {
		c = int(ci.proto.Code[ci.pc])
		ci.pc++
	}
	tbl := reg(th, ci, a)
	if value.Tag(tbl) != value.TagTable {
		return errf("SETLIST: not a table")
	}
	tref := value.GCRefOf(tbl)
	var n int
	if b == 0 {
		n = th.top - (ci.base + a) - 1
	} else {
		n = b
	}
	base0 := uint32((c - 1) * bytecode.FieldsPerFlush)
	for j := 1; j <= n; j++ {
		st.tableSetInt(tref, base0+uint32(j), reg(th, ci, a+j))
	}
	return nil
}

// doConcat 实现 R(A) := R(B) .. .. R(C) — M9 仅支持 string + number 路径。
func (st *State) doConcat(th *thread, ci *callInfo, i bytecode.Instruction) *LuaError {
	bIdx := bytecode.B(i)
	cIdx := bytecode.C(i)
	parts := make([]byte, 0, 64)
	for k := bIdx; k <= cIdx; k++ {
		v := reg(th, ci, k)
		s, ok := st.toStringBytes(v)
		if !ok {
			return errf("attempt to concatenate a %s value", typeName(v))
		}
		parts = append(parts, s...)
	}
	ref := st.gc.Intern(parts)
	setReg(th, ci, bytecode.A(i), value.MakeGC(value.TagString, ref))
	return nil
}

// doVararg M9 简化版:只把目标寄存器置 nil(满足"无 vararg"主 chunk 的隐式 IsVararg)。
// 真实 ... 多值传播留 M11。
func (st *State) doVararg(th *thread, ci *callInfo, i bytecode.Instruction) *LuaError {
	a := bytecode.A(i)
	b := bytecode.B(i)
	if b == 0 {
		return nil
	}
	for k := 0; k < b-1; k++ {
		setReg(th, ci, a+k, value.Nil)
	}
	return nil
}
