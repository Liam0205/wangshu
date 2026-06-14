// gibbous trampoline + HostState 实装(VS0-d / PW2-d)。
//
// crescent ↔ gibbous(P3 wasm)跨层桥(docs/design/p3-wasm-tier/04-trampoline.md):
//   - enterGibbous:crescent doCall 检测到 Proto 已升 gibbous 时,经 bridge
//     的 GibbousCode.Run 跳进 wazero 执行(§2.2)。trampoline 逻辑住 crescent
//     全 build,经 bridge.GibbousCode 接口调 Run——不 import p3-build-only 的
//     gibbous 包(P3/P4 共用同一套 trampoline,§0.4)。
//   - HostState 方法:gibbous wasm 的 imported helper(h_getupval/h_setupval/
//     h_return/h_safepoint)回调入口(§3)。方法签名是原始类型(int32/uint64),
//     住全 build;p3 build 的 gibbous.NewCompiler 接 *State 作 HostState 注入
//     (binding 在 wangshu_p3 注入文件做)。
package crescent

import (
	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// enterGibbous 是 crescent → gibbous 升层入口(04 §2.2)。
//
// 调用方:doCall 的 gibbous 分支(仅 th==mainTh,§5 线程级 tier 规则)。
// 前置:形参已搬到 funcIdx+1..(与 host/Lua 调用同款,doCall 已备好)。
//
// 三步走:① enterLuaFrame 压帧(复用解释器备栈/vararg 逻辑,标 gibbous=true)
// ② 算 base 字节偏移(值栈段基址 + 帧 base) ③ code.Run 进 wazero。
// 返回值回填 + 弹帧由 gibbous RETURN 经 h_return(DoReturn)在 Run 内完成,
// 故本函数返回后栈状态等同解释器跑完该帧——doCall 返回 (nil, nil),execute
// 主循环 reload ci=currentCI(与 host 调用路径同款,call.go doCall)。
func (st *State) enterGibbous(th *thread, code bridge.GibbousCode, funcIdx, nargs, nresults int) *LuaError {
	if e := st.enterLuaFrame(th, funcIdx, nargs, nresults, false); e != nil {
		return e
	}
	ci := currentCI(th)
	ci.gibbous = true // bit50 callStatus_gibbous(04 §1.2):本帧走 Wasm 路径

	// base 字节偏移:R0 在共见 linear memory 的字节地址 =
	//   (值栈段字偏移 stackBaseW + 帧 base 槽) * 8(每槽 8 字节 NaN-box u64)。
	// 这是对 04 §2.2 baseBytes 的精确化:栈段非零起始,基准 = 段基址 + 帧偏移。
	baseByte := (th.stackBaseW + uint32(ci.base)) * 8

	status := code.Run(st.gibbousStack(), baseByte)
	if status != 0 {
		// ERR:DoReturn/h_raise 已置 pendingErr,或 wazero 内部错误(PendingErr)。
		if st.gibbousPendingErr == nil {
			if e := code.PendingErr(); e != nil {
				st.gibbousPendingErr = &LuaError{Msg: "gibbous: " + e.Error()}
			} else {
				st.gibbousPendingErr = errf("gibbous: run failed (status=%d)", status)
			}
		}
		e := st.gibbousPendingErr
		st.gibbousPendingErr = nil
		// 弹本帧 CallInfo(若 DoReturn 未弹——ERR 路径不经 RETURN)。
		if len(th.cis) > 0 && currentCI(th).gibbous {
			st.popCallInfo(th)
		}
		return e
	}
	// OK:返回值已由 h_return(DoReturn)回填 funcIdx 起 + 弹帧。
	return nil
}

// gibbousStack 返回复用的跨层栈缓冲(CallWithStack 零分配路径,04 §2.2 step3
// 注:PW0 spike 实测 14.8ns)。len≥1:stack[0]=base 入参,返回后 stack[0]=status。
func (st *State) gibbousStack() []uint64 {
	if st.gibStack == nil {
		st.gibStack = make([]uint64, 1)
	}
	return st.gibStack
}

// --- HostState 实装(gibbous imported helper 回调,04 §3)---
//
// 方法签名匹配 gibbous/wasm 的 HostState 接口(原始类型);p3 build 注入
// *State 作 HostState。所有方法以 base(本帧 R0 字节偏移)或 runningThread
// 当前帧为坐标——gibbous 帧与解释器帧共享同一值栈(03-memory-model)。

// GetUpval 取当前 closure 的 upvalue b(execute.go GETUPVAL 段同款)。
func (st *State) GetUpval(base int32, b int32) uint64 {
	th := st.runningThread
	ci := currentCI(th)
	uv := object.ClosureUpvalRef(st.arena, ci.cl, uint16(b))
	return uint64(st.upvalGet(th, uv))
}

// SetUpval 写当前 closure 的 upvalue b(execute.go SETUPVAL 段同款)。
func (st *State) SetUpval(base int32, b int32, val uint64) {
	th := st.runningThread
	ci := currentCI(th)
	uv := object.ClosureUpvalRef(st.arena, ci.cl, uint16(b))
	st.upvalSet(th, uv, value.Value(val))
}

// DoReturn 处理 gibbous RETURN A B(h_return,04 §4.7)。
//
// 镜像 doReturn 的非终止路径(call.go doReturn):moveResults 把 R(A..A+nret-1)
// 回填 funcIdx 起、按调用者 nresults 多退少补、弹本帧 CallInfo、恢复 caller top。
// gibbous 帧由 trampoline 经此弹出(对称于 enterLuaFrame 压入);返回 status=0。
func (st *State) DoReturn(base int32, pc int32, a int32, b int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc // pc 物化(savedPC,04 §4.5;traceback 用)
	var nret int
	if b == 0 {
		nret = th.top - (ci.base + int(a))
	} else {
		nret = int(b) - 1
	}
	st.closeUpvals(th, ci.base)
	dst := ci.funcIdx
	src := ci.base + int(a)
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
		if len(th.cis) > 0 {
			caller := currentCI(th)
			th.top = caller.base + int(st.protoOf(caller).MaxStack)
		} else {
			th.top = dst + wantedN
		}
	}
	return 0 // OK
}

// Safepoint 回边 GC 检查点(h_safepoint,04 §3.3;PW4 回边落地时接通,PW2 桩)。
func (st *State) Safepoint(base int32, pc int32) {
	st.gc.MaybeCollect()
}

// SetSavedPC 写回当前帧 savedPC(pc 物化,04 §4.5)。
func (st *State) SetSavedPC(base int32, pc int32) {
	currentCI(st.runningThread).pc = pc
}

// --- PW3 算术慢路径助手(快路径双 number 在 Wasm 内直发,失败回 Go)---
//
// 重建 bytecode.Instruction 复用解释器 doArith/doArithSlow/doConcat/LEN 逻辑,
// 保证 gibbous 慢路径与 crescent 逐字节同构。helper 内 currentCI(th) 即 gibbous
// 帧(enterGibbous 已压),寄存器寻址经 reg/setReg(已 VS0-c arena 化)。

// Arith 算术慢路径(ADD/SUB/MUL/DIV/MOD/POW)。op 是 bytecode.OpCode 值;
// 直接调 doArith(含快路径再判 + 慢路径 coercion/元方法),与解释器同构。
func (st *State) Arith(base, pc, op, b, c, a int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc
	ins := bytecode.EncodeABC(bytecode.OpCode(op), int(a), int(b), int(c))
	if e := st.doArith(th, ci, ins); e != nil {
		st.gibbousPendingErr = e
		return 1
	}
	return 0
}

// Unm UNM 慢路径(string coercion + __unm)。重建 UNM 指令复用 execute.go
// UNM 段逻辑(此处直接重跑该段的慢路径分支)。
func (st *State) Unm(base, pc, b, a int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc
	bv := reg(th, ci, int(b))
	if f, ok := st.toNumberCoerce(bv); ok {
		setReg(th, ci, int(a), value.NumberValue(-f))
		return 0
	}
	h := st.metaFieldOfValue(bv, "__unm")
	if h == value.Nil {
		st.gibbousPendingErr = st.errWithName(ci, "perform arithmetic on", int(b), bv)
		return 1
	}
	res, e := st.callMetaHandler(th, h, []value.Value{bv, bv}, 1)
	if e != nil {
		st.gibbousPendingErr = e
		return 1
	}
	setReg(th, ci, int(a), res)
	return 0
}

// Len LEN(string 长度 / table border / 异类报错;复用 execute.go LEN 段)。
func (st *State) Len(base, pc, b, a int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc
	bv := reg(th, ci, int(b))
	switch value.Tag(bv) {
	case value.TagString:
		n := object.StringLen(st.arena, value.GCRefOf(bv))
		setReg(th, ci, int(a), value.NumberValue(float64(n)))
		return 0
	case value.TagTable:
		border := st.rawBorder(value.GCRefOf(bv))
		setReg(th, ci, int(a), value.NumberValue(float64(border)))
		return 0
	default:
		st.gibbousPendingErr = st.errWithName(ci, "get length of", int(b), bv)
		return 1
	}
}

// Concat CONCAT(复用 execute.go doConcat 全逻辑 + safepoint)。
func (st *State) Concat(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc
	ins := bytecode.EncodeABC(bytecode.CONCAT, int(a), int(b), int(c))
	if e := st.doConcat(th, ci, ins); e != nil {
		st.gibbousPendingErr = e
		return 1
	}
	st.safepoint(th, ci)
	return 0
}

// Compare LT/LE 慢路径(string 比较 / __lt/__le 元方法;复用 doCompare)。
// 返回 packed:bit0=比较结果,bit1=错误标志。
func (st *State) Compare(base, pc, op, b, c int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc
	ins := bytecode.EncodeABC(bytecode.OpCode(op), 0, int(b), int(c))
	res, e := st.doCompare(th, ci, ins)
	if e != nil {
		st.gibbousPendingErr = e
		return 2 // bit1 = error
	}
	if res {
		return 1 // bit0 = true
	}
	return 0
}

// Eq EQ 的 __eq 元方法路径(raw 不等时;复用 doCompare EQ 分支)。
// 返回 packed:bit0=结果,bit1=错误。
func (st *State) Eq(base, pc, b, c int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc
	ins := bytecode.EncodeABC(bytecode.EQ, 0, int(b), int(c))
	res, e := st.doCompare(th, ci, ins)
	if e != nil {
		st.gibbousPendingErr = e
		return 2
	}
	if res {
		return 1
	}
	return 0
}

// ForPrep FORPREP 三槽校验 + coercion + 预减(复用 execute.go FORPREP 段逻辑)。
// 返回 status(0=OK / 1=ERR)。
func (st *State) ForPrep(base, pc, a int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc
	ra := int(a)
	init, ok1 := st.toNumberCoerce(reg(th, ci, ra))
	limit, ok2 := st.toNumberCoerce(reg(th, ci, ra+1))
	step, ok3 := st.toNumberCoerce(reg(th, ci, ra+2))
	if !ok1 {
		st.gibbousPendingErr = errf("'for' initial value must be a number")
		return 1
	}
	if !ok2 {
		st.gibbousPendingErr = errf("'for' limit must be a number")
		return 1
	}
	if !ok3 {
		st.gibbousPendingErr = errf("'for' step must be a number")
		return 1
	}
	// 预减 + 三槽规范化为 number(进入 FORLOOP 后快路径无须再校验类型)。
	setReg(th, ci, ra, value.NumberValue(init-step))
	setReg(th, ci, ra+1, value.NumberValue(limit))
	setReg(th, ci, ra+2, value.NumberValue(step))
	return 0
}

// --- PW5 表 IC 慢路径助手(快路径 inline 跳哈希,失效/复杂形态回 Go)---
//
// pc 物化:gibbous 传 opcode 索引 pc;解释器执行该 opcode 时 ci.pc 已 ++(指向
// 下一条),故设 ci.pc=pc+1 使 enhanceIndexErr 的 ci.pc-1 == pc(describeReg
// 取本指令)。icGetTable/icSetTable 的 pc 参数 = IC slot 索引 = opcode 索引。
// icGetTable 经 __index 元方法可能重入 execute(append cis)→ 返回后刷新 ci。

// GetTable 处理 GETTABLE A B C 慢路径(execute.go :101-112 同款)。
func (st *State) GetTable(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc + 1
	proto := st.protoOf(ci)
	tbl := reg(th, ci, int(b))
	key := rk(th, ci, proto, int(c))
	v, e := st.icGetTable(th, ci, pc, tbl, key)
	if e != nil {
		st.gibbousPendingErr = st.enhanceIndexErr(e, ci, int(b), tbl)
		return 1
	}
	ci = currentCI(th)
	setReg(th, ci, int(a), v)
	return 0
}

// SetTable 处理 SETTABLE A B C 慢路径(execute.go :114-124 同款 + safepoint)。
func (st *State) SetTable(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc + 1
	proto := st.protoOf(ci)
	tbl := reg(th, ci, int(a))
	key := rk(th, ci, proto, int(b))
	val := rk(th, ci, proto, int(c))
	if e := st.icSetTable(th, ci, pc, tbl, key, val); e != nil {
		st.gibbousPendingErr = st.enhanceIndexErr(e, ci, int(a), tbl)
		return 1
	}
	ci = currentCI(th)
	st.safepoint(th, ci)
	return 0
}

// DoGetGlobal 处理 GETGLOBAL A Bx 慢路径(execute.go :78-88 同款)。
func (st *State) DoGetGlobal(base, pc, a, bx int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc + 1
	proto := st.protoOf(ci)
	key := proto.Consts[bx]
	gv := value.MakeGC(value.TagTable, st.globals)
	v, e := st.icGetTable(th, ci, pc, gv, key)
	if e != nil {
		st.gibbousPendingErr = e
		return 1
	}
	ci = currentCI(th)
	setReg(th, ci, int(a), v)
	return 0
}

// DoSetGlobal 处理 SETGLOBAL A Bx 慢路径(execute.go :90-99 同款 + safepoint)。
func (st *State) DoSetGlobal(base, pc, a, bx int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc + 1
	proto := st.protoOf(ci)
	key := proto.Consts[bx]
	gv := value.MakeGC(value.TagTable, st.globals)
	if e := st.icSetTable(th, ci, pc, gv, key, reg(th, ci, int(a))); e != nil {
		st.gibbousPendingErr = e
		return 1
	}
	ci = currentCI(th)
	st.safepoint(th, ci)
	return 0
}

// Self 处理 SELF A B C(execute.go :134-144 同款)。助手内含 self 传递 R(A+1):=R(B),
// 与 inline 快路径的 store 幂等(inline miss 时已 store,助手重做无副作用)。
func (st *State) Self(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc + 1
	proto := st.protoOf(ci)
	tbl := reg(th, ci, int(b))
	setReg(th, ci, int(a)+1, tbl)
	key := rk(th, ci, proto, int(c))
	v, e := st.icGetTable(th, ci, pc, tbl, key)
	if e != nil {
		st.gibbousPendingErr = st.enhanceIndexErr(e, ci, int(b), tbl)
		return 1
	}
	ci = currentCI(th)
	setReg(th, ci, int(a), v)
	return 0
}

// NewTable 处理 NEWTABLE A B C(execute.go :126-132 同款,分配+GC 全助手内)。
func (st *State) NewTable(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc + 1
	asz := bytecode.Fb2Int(uint32(b))
	hsz := bytecode.Fb2Int(uint32(c))
	t := st.allocTable(asz, roundUpPow2(hsz))
	setReg(th, ci, int(a), value.MakeGC(value.TagTable, t))
	st.safepoint(th, ci)
	return 0
}

// SetList 处理 SETLIST A B C(execute.go :385-386 / doSetList 同款 + safepoint)。
// doSetList 可能消费 C=0 的「下一指令为大批次号」→ 读 Proto.Code[ci.pc] 并 ci.pc++,
// 故须先把 ci.pc 设成 opcode 之后(pc+1),与解释器取指后状态一致。
func (st *State) SetList(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc + 1
	ins := bytecode.EncodeABC(bytecode.SETLIST, int(a), int(b), int(c))
	if e := st.doSetList(th, ci, ins); e != nil {
		st.gibbousPendingErr = e
		return 1
	}
	ci = currentCI(th)
	st.safepoint(th, ci)
	return 0
}

// GlobalsRaw 返回 globals 表的 NaN-box u64(编译期烧立即数;GETGLOBAL/SETGLOBAL
// inline 快路径用)。globals 在 State 生命期内身份恒定不移动(arena 对象不迁移)。
func (st *State) GlobalsRaw() uint64 {
	return uint64(value.MakeGC(value.TagTable, st.globals))
}

// GCPendingAddr 返回 gcPending 标志字的 linear memory 字节地址(P3 PW9)。
// gibbous FORLOOP 回边 inline 读它(i32.load),非 0 才跨层调 h_safepoint。
func (st *State) GCPendingAddr() uint32 {
	return uint32(st.gcPendingRef)
}

// --- PW7 闭包构造 + 作用域 upvalue 关闭(全经助手,复用解释器)---

// Closure 处理 CLOSURE A Bx(execute.go:394-397 同款)。makeClosure 读后随伪指令
// (ci.pc 处的 MOVE/GETUPVAL)消化 upvalue 捕获——故须先把 ci.pc 设到 CLOSURE 之后
// (pc+1),与解释器取指后状态一致。无需 base 刷新(不进嵌套帧、不 growStack)。
func (st *State) Closure(base, pc, a, bx int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc + 1
	ins := bytecode.EncodeABx(bytecode.CLOSURE, int(a), int(bx))
	cl := st.makeClosure(th, ci, ins)
	setReg(th, ci, int(a), value.MakeGC(value.TagFunction, cl))
	st.safepoint(th, ci)
	return 0
}

// Close 处理 CLOSE A(execute.go:391-392 同款):关闭所有 ≥ base+A 的开放 upvalue。
func (st *State) Close(base, pc, a int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	st.closeUpvals(th, ci.base+int(a))
	return 0
}

// TForLoop 处理 TFORLOOP A C(execute.go:355-383 同款):调迭代器 R(A)(R(A+1),R(A+2)),
// 结果落 R(A+3..A+2+C)。首值非 nil → 控制变量 R(A+2):=首值,继续;首值 nil → 退出。
//
// **base 刷新(PW4b 核心)**:迭代器调用经 callLuaFromHost 可能 growStack 使值栈段
// 在 arena 重定位(stackBaseW 变),陈旧 base 失效 = UAF(同 PW6 h_call,见
// design-claims-vs-codebase-physics §2)。返回 i64:
//
//	≥0 = 刷新后的本帧 base 字节偏移(继续循环)/ -1 = ERR / -2 = 退出(首值 nil)。
func (st *State) TForLoop(base, pc, a, c int32) int64 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc + 1
	ra := int(a)
	iter := reg(th, ci, ra)
	state := reg(th, ci, ra+1)
	ctrl := reg(th, ci, ra+2)
	results, e := st.callLuaFromHost(th, iter, []value.Value{state, ctrl})
	if e != nil {
		st.gibbousPendingErr = e
		return -1
	}
	ci = currentCI(th)
	for k := 0; k < int(c); k++ {
		v := value.Nil
		if k < len(results) {
			v = results[k]
		}
		setReg(th, ci, ra+3+k, v)
	}
	if c >= 1 && len(results) >= 1 && results[0] != value.Nil {
		setReg(th, ci, ra+2, results[0]) // 控制变量 = 首返回值,继续循环
		return int64((th.stackBaseW + uint32(ci.base)) * 8)
	}
	return -2 // 首值 nil:退出循环
}

// Call 处理 gibbous 帧内的 CALL A B C 三向分派(04-trampoline §3)。
//
// 复用 doCall 的统一分派(host / __call / gibbous 升层 / 普通 Lua 四向):
//   - host / gibbous 升层:doCall 内同步跑完,返回值已落 R(A..),next==nil;
//   - 普通 Lua closure:doCall 压新帧返回 next!=nil——gibbous 语境无外层解释器
//     主循环续跑它,故本函数起一层 executeFrom 同步驱动该帧(及其子帧)到完成,
//     返回值留 R(A..) 共见栈槽。
//
// **base 刷新(PW6 核心)**:被调帧可能 growStack 使值栈段在 arena 重定位
// (state.go growStack 改 stackBaseW),本帧 $base 随之失效。返回时按当前
// stackBaseW + ci.base 重算新 base 字节偏移,gibbous 据此续算寻址。
// 返回:成功 = 新 base 字节偏移(≥0);错误 = -1(pendingErr 已置,status 链冒泡)。
func (st *State) DoCall(base, pc, a, b, c int32) int64 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc
	ins := bytecode.EncodeABC(bytecode.CALL, int(a), int(b), int(c))
	next, e := st.doCall(th, ci, ins)
	if e != nil {
		st.gibbousPendingErr = e
		return -1
	}
	if next != nil {
		// 进入一个新 Lua 帧(被调是未升层 closure)——同步驱动到完成。
		// nCcalls 计费:executeFrom 是新的 Go 栈重入边界,防 gibbous↔crescent
		// 交替递归打爆 Go 栈(meta.go callLuaFromHost 同款守卫)。
		if st.nCcalls >= maxCCallDepth {
			st.gibbousPendingErr = errf("C stack overflow")
			return -1
		}
		st.nCcalls++
		entryDepth := len(th.cis) - 1
		e2 := st.executeFrom(th, entryDepth)
		st.nCcalls--
		if e2 != nil {
			st.gibbousPendingErr = e2
			return -1
		}
	}
	// 刷新 base(嵌套帧可能 growStack 段重定位,陈旧 base 指向已 Free 段 = UAF)。
	ci = currentCI(th)
	return int64((th.stackBaseW + uint32(ci.base)) * 8)
}

// TailCall 处理 gibbous 帧内的 TAILCALL A B C(尾调用复用帧,04-trampoline §2.5)。
//
// 复用 doTailCall:
//   - 普通 Lua closure / __call:doTailCall 关 upvalue + 下移参数 + 弹本帧(G)+
//     压 callee 帧(复用 G 的 funcIdx,nresults 继承 G 的 nresults)。本函数随后
//     executeFrom 同步驱动 callee 链到完成——**尾递归在解释器内 O(1) 栈/CallInfo
//     深度迭代**(callee 自身再 TAILCALL 时 doTailCall 弹+压同深度,同一 execute
//     循环续跑),返回 0;gibbous 函数据此直接 return 0(本帧已被替换,跳过尾随
//     RETURN)。
//   - host fn:doTailCall 内 callHost(结果落 base+a),G 帧不弹 → 返回 2,
//     gibbous 落到尾随 RETURN 由 DoReturn 完成最终返回(镜像解释器)。
//
// 返回:0=Lua 尾调用完成(gibbous return 0)/ 1=ERR / 2=host(落尾随 RETURN)。
func (st *State) TailCall(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := currentCI(th)
	ci.pc = pc
	ins := bytecode.EncodeABC(bytecode.TAILCALL, int(a), int(b), int(c))
	next, e := st.doTailCall(th, ci, ins)
	if e != nil {
		st.gibbousPendingErr = e
		return 1
	}
	if next == nil {
		// host 尾调用:结果已落 base+a,G 帧未弹 → 回退给尾随 RETURN(DoReturn)。
		return 2
	}
	// Lua 尾调用:G 已被 callee 帧替换。同步驱动 callee 链到完成。
	if st.nCcalls >= maxCCallDepth {
		st.gibbousPendingErr = errf("C stack overflow")
		return 1
	}
	st.nCcalls++
	entryDepth := len(th.cis) - 1
	e2 := st.executeFrom(th, entryDepth)
	st.nCcalls--
	if e2 != nil {
		st.gibbousPendingErr = e2
		return 1
	}
	return 0
}
