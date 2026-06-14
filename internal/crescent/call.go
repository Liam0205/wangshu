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
// 若调用走 host 路径,host 函数同步执行后返回 (nil, nil),主循环不切 ci。
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
	callee := th.slot(funcIdx)
	if value.Tag(callee) != value.TagFunction {
		// __call 元方法(07):args 右移一格,原 callee 变第 1 实参,handler 上位
		h := st.metaFieldOfValue(callee, "__call")
		if value.Tag(h) != value.TagFunction {
			return nil, st.errWithName(ci, "call", a, callee)
		}
		st.insertCallSelf(th, funcIdx, nargs)
		th.setSlot(funcIdx, h)
		callee = h
		nargs++
	}
	cl := value.GCRefOf(callee)
	if object.IsHostClosure(st.arena, cl) {
		// 同步调用 host;调用后 ci 不变(主循环 next=nil 表示不切帧)
		e := st.callHost(th, funcIdx, nargs, nresults)
		if e == errYieldSentinel {
			// yield 冒泡(08 §3.4):记录恢复信息(从本 CALL 的下一条恢复;
			// resume 参数将写到本 CALL 的结果寄存器)。
			th.pendingResume = &pendingResumeInfo{
				ciIndex:    len(th.cis) - 1,
				dst:        funcIdx,
				nresults:   nresults,
				entryDepth: st.entryDepthOf(th),
			}
		}
		return nil, e
	}
	if e := st.enterLuaFrame(th, funcIdx, nargs, nresults, false); e != nil {
		return nil, e
	}
	return currentCI(th), nil
}

// entryDepthOf 找当前最内层 fresh 帧的深度(yield 恢复后的冒泡边界)。
func (st *State) entryDepthOf(th *thread) int {
	for i := len(th.cis) - 1; i >= 0; i-- {
		if th.cis[i].fresh {
			return i
		}
	}
	return 0
}

// insertCallSelf 为 __call 重排栈:args 右移一格,原 callee 留在 funcIdx+1
// 作第 1 实参,handler 由调用方写入 funcIdx(07 __call 语义)。
func (st *State) insertCallSelf(th *thread, funcIdx, nargs int) {
	need := funcIdx + 2 + nargs
	th.ensureStack(need)
	for k := nargs; k >= 0; k-- {
		th.setSlot(funcIdx+1+k, th.slot(funcIdx+k))
	}
	if need > th.top {
		th.top = need
	}
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
	callee := th.slot(funcIdx)
	if value.Tag(callee) != value.TagFunction {
		// __call 元方法(07):与 doCall 同构
		h := st.metaFieldOfValue(callee, "__call")
		if value.Tag(h) != value.TagFunction {
			return nil, st.errWithName(ci, "call", a, callee)
		}
		st.insertCallSelf(th, funcIdx, nargs)
		th.setSlot(funcIdx, h)
		callee = h
		nargs++
	}
	cl := value.GCRefOf(callee)
	if object.IsHostClosure(st.arena, cl) {
		// host 尾调用 = 普通 host 调用,结果作为本帧返回值。M12 简化:落到原 funcIdx 起,
		// 然后让本帧 RETURN(主循环紧随会执行 RETURN A=funcIdx, B=0,但 codegen 紧跟一条
		// RETURN A B=0 设计文档承诺存在);所以这里完成 host 后让 ci 继续即可。
		return nil, st.callHost(th, funcIdx, nargs, ci.nresults)
	}
	st.closeUpvals(th, ci.base)
	dst := ci.funcIdx
	for k := 0; k < nargs+1; k++ {
		th.setSlot(dst+k, th.slot(funcIdx+k))
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
		th.setSlot(dst+k, th.slot(src+k))
	}
	wantedN := ci.nresults
	st.popCallInfo(th)
	if wantedN < 0 {
		th.top = dst + nret
	} else {
		for k := nret; k < wantedN; k++ {
			th.setSlot(dst+k, value.Nil)
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
	restoreTop := false
	if b == 0 {
		n = th.top - (ci.base + a) - 1
		restoreTop = true
	} else {
		n = b
	}
	base0 := uint32((c - 1) * bytecode.FieldsPerFlush)
	for j := 1; j <= n; j++ {
		st.tableSetInt(tref, base0+uint32(j), reg(th, ci, a+j))
	}
	if restoreTop {
		// 消费完"到 top"的多值窗口后恢复帧逻辑顶(对齐 lvm.c OP_SETLIST
		// `L->top = L->ci->top`):否则后续指令写 top 之上的寄存器,GC 扫根
		// 只见 [0,top) → 活值漏标,freelist 复用内存下即 use-after-free。
		th.ensureStack(ci.base + int(ci.proto.MaxStack))
		th.top = ci.base + int(ci.proto.MaxStack)
	}
	return nil
}

// doConcat 实现 R(A) := R(B) .. .. R(C)。
//
// 快路径全 string/number 一次线性拼接;遇到非法操作数走 __concat 元方法
// (右结合,07);仍无则报带名字描述的错误(09 §8.3)。
func (st *State) doConcat(th *thread, ci *callInfo, i bytecode.Instruction) *LuaError {
	bIdx := bytecode.B(i)
	cIdx := bytecode.C(i)
	// 快路径检查:全部可串化
	allPlain := true
	for k := bIdx; k <= cIdx; k++ {
		v := reg(th, ci, k)
		if !value.IsNumber(v) && value.Tag(v) != value.TagString {
			allPlain = false
			break
		}
	}
	if allPlain {
		parts := make([]byte, 0, 64)
		for k := bIdx; k <= cIdx; k++ {
			s, _ := st.toStringBytes(reg(th, ci, k))
			parts = append(parts, s...)
		}
		ref := st.gc.Intern(parts)
		setReg(th, ci, bytecode.A(i), value.MakeGC(value.TagString, ref))
		return nil
	}
	// 慢路径:从右向左两两折叠(右结合);__concat 先左后右查
	acc := reg(th, ci, cIdx)
	for k := cIdx - 1; k >= bIdx; k-- {
		l := reg(th, ci, k)
		lOK := value.IsNumber(l) || value.Tag(l) == value.TagString
		rOK := value.IsNumber(acc) || value.Tag(acc) == value.TagString
		if lOK && rOK {
			lb, _ := st.toStringBytes(l)
			rb, _ := st.toStringBytes(acc)
			ref := st.gc.Intern(append(append([]byte{}, lb...), rb...))
			acc = value.MakeGC(value.TagString, ref)
			continue
		}
		h := st.metaFieldOfValue(l, "__concat")
		if h == value.Nil {
			h = st.metaFieldOfValue(acc, "__concat")
		}
		if h == value.Nil {
			bad := l
			badRK := k
			if lOK {
				bad = acc
				badRK = k + 1
			}
			return st.errWithName(ci, "concatenate", badRK, bad)
		}
		res, e := st.callMetaHandler(th, h, []value.Value{l, acc}, 1)
		if e != nil {
			return e
		}
		acc = res
	}
	setReg(th, ci, bytecode.A(i), acc)
	return nil
}

// doVararg 实现 VARARG A B:把 ci.varargs 的内容拷到 R(A..A+B-2);
// B=0 时全部拷贝并把 top 设到多值区末(对齐官方 `L->top = ra + n`)。
func (st *State) doVararg(th *thread, ci *callInfo, i bytecode.Instruction) *LuaError {
	a := bytecode.A(i)
	b := bytecode.B(i)
	n := len(ci.varargs)
	if b == 0 {
		// 全部 vararg 到 top。top 必须双向设置(不只抬不降):此前更高的
		// 残留 top 会让消费方(doCall 的 nargs = top-funcIdx-1)高估实参数。
		need := ci.base + a + n
		if need > th.size() {
			th.ensureStack(need)
		}
		for k := 0; k < n; k++ {
			th.setSlot(ci.base+a+k, ci.varargs[k])
		}
		th.top = need
		return nil
	}
	want := b - 1
	for k := 0; k < want; k++ {
		if k < n {
			setReg(th, ci, a+k, ci.varargs[k])
		} else {
			setReg(th, ci, a+k, value.Nil)
		}
	}
	return nil
}
