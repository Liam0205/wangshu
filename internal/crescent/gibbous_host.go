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
	"unsafe"

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
	ci := st.gibCI(th)
	ci.SetGibbous(true) // bit50 callStatus_gibbous(04 §1.2):本帧走 Wasm 路径
	th.reMirrorTop()    // PW10 R2b-1:cold 字段(gibbous)变更后重镜像 ci 段

	// base 字节偏移:R0 在共见 linear memory 的字节地址 =
	//   (值栈段字偏移 stackBaseW + 帧 base 槽) * 8(每槽 8 字节 NaN-box u64)。
	// 这是对 04 §2.2 baseBytes 的精确化:栈段非零起始,基准 = 段基址 + 帧偏移。
	baseByte := (th.stackBaseW + uint32(ci.base)) * 8

	status := code.Run(st.gibbousStack(), baseByte)
	// 段为权威反向同步(PW10 零跨界 Stage 1b):Wasm 执行期可能 increment/decrement
	// ciDepth 字 + 写段帧(Stage 2/3),此处以段为准重载 th.ciDepth/th.cur。Stage 1b
	// 无 Wasm 写,字恒等 ciDepth → 验证性 no-op。
	th.syncCurFromSeg()
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
		if th.ciDepth > 0 && currentCI(th).Gibbous() {
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

// gibCI 取 gibbous 边界的当前帧(PW10 零跨界 ③b)。先 syncCurFromSeg 以**段为准**
// 反向同步——Wasm RETURN 快路径(emitReturnFast)在 wasm 执行期减段 ciDepth 字 +
// 拆帧、全程不回 Go,故 Go 的 th.cur/th.ciDepth 被冻结而段是活的。所有 HostState
// helper 入口经此取 ci,确保 Wasm 改段后 Go 侧读到最新帧(非陈旧的已死帧 → 寄存器
// 寻址错位腐蚀,Option A 风险 #1)。syncCurFromSeg 幂等廉价(字==Go 时 no-op),
// 慢路径 helper 本就已跨界,开销可忽略。
func (st *State) gibCI(th *thread) *callInfo {
	th.syncCurFromSeg()
	return &th.cur
}

// SetReg 直接写当前帧的 R(idx) 槽位为 val(NaN-box u64,gibbous-jit P4 PJ7
// 简化形态专用)。
//
// **依赖解环**(P4HostState 接口,jit/host.go):p4Code.Run 在 mmap 段执行后,
// 需要把 RAX(NaN-box 值)写到 R(retA) 槽位(arena 值栈本体)——不经 P3
// CallWithStack 的 1 槽 buffer 协议(P3 stack 协议与 P4 不兼容)。
//
// 参数:
//   - idx:寄存器号(R(idx),= ci.base + idx 即 thread 槽位下标)
//   - val:NaN-box u64 值
//
// 实装:经 ci.base + idx 算槽位,直接 setSlot 写入。本方法语义同
// `execute.go::SETREG`-类操作(arena 值栈直写,无 GC 屏障——因 NaN-box u64
// 写入是原子单字)。
func (st *State) SetReg(idx int32, val uint64) {
	th := st.runningThread
	ci := st.gibCI(th)
	th.setSlot(ci.base+int(idx), value.Value(val))
}

// GetReg 读取当前帧 R(idx)(P4HostState 接口,与 SetReg 对偶)。
func (st *State) GetReg(idx int32) uint64 {
	th := st.runningThread
	ci := st.gibCI(th)
	return uint64(th.slot(ci.base + int(idx)))
}

// SetUpvalFromReg 把 R(a) 写入当前 closure 的 upvalue b(execute.go SETUPVAL
// 段同款,P4HostState 专用「读 reg + 写 upvalue」原子 helper,避免引入
// 通用 GetReg+SetUpval 的两 round-trip)。
func (st *State) SetUpvalFromReg(base int32, a int32, b int32) {
	th := st.runningThread
	ci := st.gibCI(th)
	uv := object.ClosureUpvalRef(st.arena, ci.Cl(), uint16(b))
	st.upvalSet(th, uv, reg(th, ci, int(a)))
}

// ArenaBaseAddr 返回 arena `[]byte` 起点的 uintptr(承 05 §3.3 P4HostState
// 接口)。PJ2 完整投机模板预备——mmap 段经 r15+offset 读本字段后字节级
// 寻址值栈槽。当前 PJ7 简化形态不调用。
//
// **arena 重定位**:Words() 在 grow 时返新切片,本字段返当前 Words 起点。
// 调用方(jit.Compile)在 Run 入口现算,不缓存(承 05 §5 arena base 重载
// 协议——arena 视图别名 grow 雷区,见 [[feedback-arena-view-aliasing]])。
func (st *State) ArenaBaseAddr() uintptr {
	words := st.arena.Words()
	if len(words) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&words[0]))
}

// ValueStackBaseAddr 返回当前帧 R0 的字节地址(承 05 §3.3 + 06 §4.1
// rbx = valueStackBase)。
//
// 参数 base 是 enterGibbous 算的字节偏移(`(stackBaseW + ci.base) * 8`),
// 本函数返 arena.Words 起点 uintptr + base。**arena grow 雷区**:同
// ArenaBaseAddr,需 Run 入口现算不缓存。
func (st *State) ValueStackBaseAddr(base int32) uintptr {
	words := st.arena.Words()
	if len(words) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&words[0])) + uintptr(base)
}

// CIDepthHostAddr 返回 thread.ciDepth 镜像字的 host 字节地址(承
// docs/design/p4-method-jit/implementation-progress.md §9.20 Option B
// Spike 1 + P4HostState 接口)。
//
// **复用 P3 PW10 Stage 1a 镜像字**(st.ciDepthRef):同一 arena 镜像字,
// crescent 端经 thread.setCIDepth 写入(`a.SetWordAt(st.ciDepthRef, ...)`),
// P4 mmap 段经本字段返回的 host addr 字节级 inc/dec(enterLuaFrame +
// popCallInfo 字节级 inline)。
//
// 返回 = arena.Words().bytePtr + (ciDepthRef bytes)。**arena 重定位**:
// 同 ArenaBaseAddr,grow 出 JIT 世界后回来从 jitContext 重载;Spike 1
// 阶段每次 Run 入口现算注入(承 05 §5 arena base 重载协议)。
func (st *State) CIDepthHostAddr() uintptr {
	words := st.arena.Words()
	if len(words) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&words[0])) + uintptr(st.ciDepthRef)
}

// CISegBaseHostAddr 返回 CI 段当前字节基址镜像字的 host 字节地址(承 §9.20
// Option B Spike 1)。
//
// **复用 P3 PW10 Stage 2 镜像字**(st.ciSegBaseRef):CI 段可重定位
// (growCISeg / newThread 更新 ciBaseW),syncCISegBase 把 ciBaseW*8 镜像
// 到此 arena 字。P4 mmap 段经本字段返回 host addr 解引出当前 CI 段基址,
// 然后算 CallInfo[depth] 帧地址(基址 + depth*40)。
func (st *State) CISegBaseHostAddr() uintptr {
	words := st.arena.Words()
	if len(words) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&words[0])) + uintptr(st.ciSegBaseRef)
}

// TopHostAddr 返回 thread.top 镜像字的 host 字节地址(承 §9.20 Option B
// Spike 1)。
//
// **复用 P3 PW10 Stage 1a 镜像字**(st.topRef):top 是栈槽索引,
// enterLuaFrame 设 callee 帧顶时 P4 mmap 段写入(top = base + MaxStack)。
func (st *State) TopHostAddr() uintptr {
	words := st.arena.Words()
	if len(words) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&words[0])) + uintptr(st.topRef)
}

// GetUpval 取当前 closure 的 upvalue b(execute.go GETUPVAL 段同款)。
func (st *State) GetUpval(base int32, b int32) uint64 {
	th := st.runningThread
	ci := st.gibCI(th)
	uv := object.ClosureUpvalRef(st.arena, ci.Cl(), uint16(b))
	return uint64(st.upvalGet(th, uv))
}

// SetUpval 写当前 closure 的 upvalue b(execute.go SETUPVAL 段同款)。
func (st *State) SetUpval(base int32, b int32, val uint64) {
	th := st.runningThread
	ci := st.gibCI(th)
	uv := object.ClosureUpvalRef(st.arena, ci.Cl(), uint16(b))
	st.upvalSet(th, uv, value.Value(val))
}

// DoReturn 处理 gibbous RETURN A B(h_return,04 §4.7)。
//
// 镜像 doReturn 的非终止路径(call.go doReturn):moveResults 把 R(A..A+nret-1)
// 回填 funcIdx 起、按调用者 nresults 多退少补、弹本帧 CallInfo、恢复 caller top。
// gibbous 帧由 trampoline 经此弹出(对称于 enterLuaFrame 压入);返回 status=0。
func (st *State) DoReturn(base int32, pc int32, a int32, b int32) int32 {
	th := st.runningThread
	st.doReturnHits++ // PW10 零跨界 ③b 验证:快路径命中时不经此(计数停滞证快路径生效)
	ci := st.gibCI(th)
	ci.pc = pc // pc 物化(savedPC,04 §4.5;traceback 用)
	var nret int
	if b == 0 {
		nret = th.top - (ci.base + int(a))
	} else {
		nret = int(b) - 1
	}
	st.closeUpvals(th, ci.base)
	dst := ci.FuncIdx()
	src := ci.base + int(a)
	for k := 0; k < nret; k++ {
		th.setSlot(dst+k, th.slot(src+k))
	}
	wantedN := ci.NResults()
	st.popCallInfo(th)
	if wantedN < 0 {
		th.setTop(dst + nret)
	} else {
		for k := nret; k < wantedN; k++ {
			th.setSlot(dst+k, value.Nil)
		}
		if th.ciDepth > 0 {
			caller := st.gibCI(th)
			th.setTop(caller.base + int(st.protoOf(caller).MaxStack))
		} else {
			th.setTop(dst + wantedN)
		}
	}
	// PW10 R3:把弹帧后的 caller base 字节偏移写中转字,供 caller 的 call_indirect
	// 返回后读取续算(caller 是 gibbous-via-call_indirect 时需要刷新 base——被调可能
	// growStack 段重定位)。caller 非 gibbous 或走 baseline Run 路径时此写被忽略,无害。
	if th.ciDepth > 0 {
		caller := st.gibCI(th)
		st.arena.SetWordAt(st.ciTransferRef, uint64((th.stackBaseW+uint32(caller.base))*8))
	}
	return 0 // OK
}

// raiseGibbous 锚定并暂存 gibbous 帧抛出的错误(PW10 R3c-fix)。
//
// **为何在出错点锚定**:gibbous 错误经 status 链冒泡时,沿途各帧(gibbous 被调
// 由 PopErrFrame、gibbous caller 由其 enterGibbous launcher)会被弹出 → 等冒泡到
// 顶层 executeFrom 时 currentCI 已不是出错帧,annotateError 读到错的帧 → 行号/
// traceback 漂移(R3c 已知回归)。解法:在出错点(currentCI 仍是出错帧)立即
// annotateError 锚定 "chunkname:line:" 前缀 + 物化 traceback,并标 annotated;
// 顶层 executeFrom 见 annotated 跳过重标,行号不再受后续弹帧影响。
//
// 与解释器一致:解释器在顶层 executeFrom 标注时 currentCI 恰是出错帧(解释器不在
// 错误路径弹帧),故标注行号 = 出错帧行号。gibbous 在出错点标注达成同一行号,使
// gibbous 错误消息追平解释器(层间 byte-equal)。
//
// 幂等:e 已 annotated(来自更深 metamethod 子调用的解释器标注)时 annotateError
// 直接返回,不重复加前缀。返回 1(status 链 ERR 码),调用点 `return st.raiseGibbous(e)`。
func (st *State) raiseGibbous(e *LuaError) int32 {
	th := st.runningThread
	if th.ciDepth > 0 {
		e = st.annotateError(e, currentCI(th))
		if e != nil && e.Traceback == "" {
			e.Traceback = st.buildTraceback(th)
		}
	}
	st.gibbousPendingErr = e
	return 1
}

// PopErrFrame 弹出 call_indirect 直调失败时遗留的 gibbous 被调帧(PW10 R3)。
//
// gibbous 被调出错时自身 `return 1` 不弹帧(对称于 baseline:被调的 enterGibbous
// launcher 才弹)。R3 call_indirect 直调免去了中间 launcher,故 caller wasm 在
// call_indirect 返回非 0 时调本助手补弹——精确复刻 baseline enterGibbous 错误路径
// 的「currentCI 是 gibbous 帧才弹」条件(gibbous_host.go enterGibbous ERR 分支),使
// ciDepth/currentCI 轨迹逐帧一致。否则顶层 executeFrom 的 annotateError 读到错的
// currentCI → 错误行号前缀变 → 破层间 byte-equal 差分(V1-V13)。
//
// 被调出错却 currentCI 非 gibbous(被调经 fallback 同步路径跑 crescent 子帧、子帧
// 出错遗留)时不弹——与 baseline enterGibbous 同条件(留给 protected 边界 truncateCI)。
func (st *State) PopErrFrame() {
	th := st.runningThread
	if th.ciDepth > 0 && st.gibCI(th).Gibbous() {
		st.popCallInfo(th)
	}
}

// Safepoint 回边 GC 检查点(h_safepoint,04 §3.3;PW4 回边落地时接通,PW2 桩)。
func (st *State) Safepoint(base int32, pc int32) {
	st.gc.MaybeCollect()
}

// SetSavedPC 写回当前帧 savedPC(pc 物化,04 §4.5)。
func (st *State) SetSavedPC(base int32, pc int32) {
	st.gibCI(st.runningThread).pc = pc
}

// --- PW3 算术慢路径助手(快路径双 number 在 Wasm 内直发,失败回 Go)---
//
// 重建 bytecode.Instruction 复用解释器 doArith/doArithSlow/doConcat/LEN 逻辑,
// 保证 gibbous 慢路径与 crescent 逐字节同构。helper 内经 gibCI 取的即 gibbous
// 帧(enterGibbous 已压),寄存器寻址经 reg/setReg(已 VS0-c arena 化)。

// Arith 算术慢路径(ADD/SUB/MUL/DIV/MOD/POW)。op 是 bytecode.OpCode 值;
// 直接调 doArith(含快路径再判 + 慢路径 coercion/元方法),与解释器同构。
func (st *State) Arith(base, pc, op, b, c, a int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1 // pc 物化:解释器执行该 op 时 ci.pc 已 ++,errWithName 的 ci.pc-1==pc(R3c-fix)
	ins := bytecode.EncodeABC(bytecode.OpCode(op), int(a), int(b), int(c))
	if e := st.doArith(th, ci, ins); e != nil {
		return st.raiseGibbous(e)
	}
	return 0
}

// Unm UNM 慢路径(string coercion + __unm)。重建 UNM 指令复用 execute.go
// UNM 段逻辑(此处直接重跑该段的慢路径分支)。
func (st *State) Unm(base, pc, b, a int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1 // R3c-fix:errWithName 的 ci.pc-1==pc(失败 op 索引)
	bv := reg(th, ci, int(b))
	if f, ok := st.toNumberCoerce(bv); ok {
		setReg(th, ci, int(a), value.NumberValue(-f))
		return 0
	}
	h := st.metaFieldOfValue(bv, "__unm")
	if h == value.Nil {
		return st.raiseGibbous(st.errWithName(ci, "perform arithmetic on", int(b), bv))
	}
	res, e := st.callMetaHandler(th, h, []value.Value{bv, bv}, 1)
	if e != nil {
		return st.raiseGibbous(e)
	}
	setReg(th, ci, int(a), res)
	return 0
}

// Len LEN(string 长度 / table border / 异类报错;复用 execute.go LEN 段)。
func (st *State) Len(base, pc, b, a int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1 // R3c-fix
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
		return st.raiseGibbous(st.errWithName(ci, "get length of", int(b), bv))
	}
}

// Concat CONCAT(复用 execute.go doConcat 全逻辑 + safepoint)。
func (st *State) Concat(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1 // R3c-fix
	ins := bytecode.EncodeABC(bytecode.CONCAT, int(a), int(b), int(c))
	if e := st.doConcat(th, ci, ins); e != nil {
		return st.raiseGibbous(e)
	}
	st.safepoint(th, ci)
	return 0
}

// Compare LT/LE 慢路径(string 比较 / __lt/__le 元方法;复用 doCompare)。
// 返回 packed:bit0=比较结果,bit1=错误标志。
func (st *State) Compare(base, pc, op, b, c int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1 // R3c-fix
	ins := bytecode.EncodeABC(bytecode.OpCode(op), 0, int(b), int(c))
	res, e := st.doCompare(th, ci, ins)
	if e != nil {
		st.raiseGibbous(e)
		return 2 // bit1 = error(raiseGibbous 已置 pendingErr + 锚定行号)
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
	ci := st.gibCI(th)
	ci.pc = pc + 1 // R3c-fix
	ins := bytecode.EncodeABC(bytecode.EQ, 0, int(b), int(c))
	res, e := st.doCompare(th, ci, ins)
	if e != nil {
		st.raiseGibbous(e)
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
	ci := st.gibCI(th)
	ci.pc = pc + 1 // R3c-fix
	ra := int(a)
	init, ok1 := st.toNumberCoerce(reg(th, ci, ra))
	limit, ok2 := st.toNumberCoerce(reg(th, ci, ra+1))
	step, ok3 := st.toNumberCoerce(reg(th, ci, ra+2))
	if !ok1 {
		return st.raiseGibbous(errf("'for' initial value must be a number"))
	}
	if !ok2 {
		return st.raiseGibbous(errf("'for' limit must be a number"))
	}
	if !ok3 {
		return st.raiseGibbous(errf("'for' step must be a number"))
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
	ci := st.gibCI(th)
	ci.pc = pc + 1
	proto := st.protoOf(ci)
	tbl := reg(th, ci, int(b))
	key := rk(th, ci, proto, int(c))
	v, e := st.icGetTable(th, ci, pc, tbl, key)
	if e != nil {
		return st.raiseGibbous(st.enhanceIndexErr(e, ci, int(b), tbl))
	}
	ci = st.gibCI(th)
	setReg(th, ci, int(a), v)
	return 0
}

// SetTable 处理 SETTABLE A B C 慢路径(execute.go :114-124 同款 + safepoint)。
func (st *State) SetTable(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1
	proto := st.protoOf(ci)
	tbl := reg(th, ci, int(a))
	key := rk(th, ci, proto, int(b))
	val := rk(th, ci, proto, int(c))
	if e := st.icSetTable(th, ci, pc, tbl, key, val); e != nil {
		return st.raiseGibbous(st.enhanceIndexErr(e, ci, int(a), tbl))
	}
	ci = st.gibCI(th)
	st.safepoint(th, ci)
	return 0
}

// DoGetGlobal 处理 GETGLOBAL A Bx 慢路径(execute.go :78-88 同款)。
func (st *State) DoGetGlobal(base, pc, a, bx int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1
	proto := st.protoOf(ci)
	key := proto.Consts[bx]
	gv := value.MakeGC(value.TagTable, st.globals)
	v, e := st.icGetTable(th, ci, pc, gv, key)
	if e != nil {
		return st.raiseGibbous(e)
	}
	ci = st.gibCI(th)
	setReg(th, ci, int(a), v)
	return 0
}

// DoSetGlobal 处理 SETGLOBAL A Bx 慢路径(execute.go :90-99 同款 + safepoint)。
func (st *State) DoSetGlobal(base, pc, a, bx int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1
	proto := st.protoOf(ci)
	key := proto.Consts[bx]
	gv := value.MakeGC(value.TagTable, st.globals)
	if e := st.icSetTable(th, ci, pc, gv, key, reg(th, ci, int(a))); e != nil {
		return st.raiseGibbous(e)
	}
	ci = st.gibCI(th)
	st.safepoint(th, ci)
	return 0
}

// Self 处理 SELF A B C(execute.go :134-144 同款)。助手内含 self 传递 R(A+1):=R(B),
// 与 inline 快路径的 store 幂等(inline miss 时已 store,助手重做无副作用)。
func (st *State) Self(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1
	proto := st.protoOf(ci)
	tbl := reg(th, ci, int(b))
	setReg(th, ci, int(a)+1, tbl)
	key := rk(th, ci, proto, int(c))
	v, e := st.icGetTable(th, ci, pc, tbl, key)
	if e != nil {
		return st.raiseGibbous(st.enhanceIndexErr(e, ci, int(b), tbl))
	}
	ci = st.gibCI(th)
	setReg(th, ci, int(a), v)
	return 0
}

// NewTable 处理 NEWTABLE A B C(execute.go :126-132 同款,分配+GC 全助手内)。
func (st *State) NewTable(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
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
	ci := st.gibCI(th)
	ci.pc = pc + 1
	ins := bytecode.EncodeABC(bytecode.SETLIST, int(a), int(b), int(c))
	if e := st.doSetList(th, ci, ins); e != nil {
		return st.raiseGibbous(e)
	}
	ci = st.gibCI(th)
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

// CITransferAddr 返回 ci-transfer 中转字的 linear memory 字节地址(P3 PW10 R3)。
// gibbous→gibbous call_indirect 直调经此字传被调/刷新后 base 字节偏移。
func (st *State) CITransferAddr() uint32 {
	return uint32(st.ciTransferRef)
}

// CIDepthAddr 返回 ci-depth 游标字的 linear memory 字节地址(P3 PW10 零跨界 Stage 1a)。
// Wasm 侧帧建拆 increment/decrement 此 i32 字免回 Go 改 th.ciDepth。
func (st *State) CIDepthAddr() uint32 {
	return uint32(st.ciDepthRef)
}

// CISegBaseAddr 返回 ci-seg-base 字的 linear memory 字节地址(P3 PW10 零跨界 Stage 2)。
// 此字内含 CI 段当前字节基址(可重定位);Wasm 侧帧建拆读它现算帧地址(段基址 +
// depth*ciWords*8 + word*8)。
func (st *State) CISegBaseAddr() uint32 {
	return uint32(st.ciSegBaseRef)
}

// OpenGuardAddr 返回 open-upvalue 守卫字的 linear memory 字节地址(P3 PW10 零跨界
// Stage 2)。字值 = maxOpenIdx+1(有开放 upvalue)/ 0(无);Wasm RETURN 快路径守卫
// frameBase ≥ 此值 ⟺ 本帧无须关闭的开放 upvalue(closeUpvals no-op)。
func (st *State) OpenGuardAddr() uint32 {
	return uint32(st.openGuardRef)
}

// TopAddr 返回 top 镜像字的 linear memory 字节地址(P3 PW10 零跨界 ①)。字值 =
// th.top(槽索引);Wasm 建帧设 callee 帧顶 / caller 自恢复 top 时写它,GC 栈根
// 扫描读它定 [0,top) 上界。槽索引坐标(grow 安全)。State 生命期内地址恒定。
func (st *State) TopAddr() uint32 {
	return uint32(st.topRef)
}

// ProtoCacheBaseAddr 返回 proto 字段缓存段基址镜像字的字节地址(PW10 零跨界
// 基建-b)。Wasm ④ emitCall 守卫快路径现读此基址 + protoID*8 取 callee Proto 的
// MaxStack/NumParams/IsVararg/NeedsArg 缓存,免 Go map 查 Proto 字段。State 生命期
// 内此 mirror 字地址恒定;段本身可重定位(LoadProgram 重分配)经此字现读。
func (st *State) ProtoCacheBaseAddr() uint32 {
	return uint32(st.protoCacheBaseRef)
}

// FastCallHitsAddr 返回 ④ emitCall 守卫快路径命中计数字的字节地址(PW10 零跨界
// ④ 验证用)。Wasm 命中后 i64 ++;Go 测试读字合 indirectCalls 一起断言路径命中。
func (st *State) FastCallHitsAddr() uint32 {
	return uint32(st.fastCallHitsRef)
}

// --- PW7 闭包构造 + 作用域 upvalue 关闭(全经助手,复用解释器)---

// Closure 处理 CLOSURE A Bx(execute.go:394-397 同款)。makeClosure 读后随伪指令
// (ci.pc 处的 MOVE/GETUPVAL)消化 upvalue 捕获——故须先把 ci.pc 设到 CLOSURE 之后
// (pc+1),与解释器取指后状态一致。无需 base 刷新(不进嵌套帧、不 growStack)。
func (st *State) Closure(base, pc, a, bx int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
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
	ci := st.gibCI(th)
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
	ci := st.gibCI(th)
	ci.pc = pc + 1
	ra := int(a)
	iter := reg(th, ci, ra)
	state := reg(th, ci, ra+1)
	ctrl := reg(th, ci, ra+2)
	results, e := st.callLuaFromHost(th, iter, []value.Value{state, ctrl})
	if e != nil {
		st.raiseGibbous(e) // 锚定行号(callLuaFromHost 错误若未标注则按当前 TFORLOOP 帧标)
		return -1
	}
	ci = st.gibCI(th)
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

// tryIndirectCallee 判被调是否「gibbous-有-slot 的 Lua closure(主线程)」——是则
// 自己压帧 + 置 gibbous 位 + 写被调帧 base 到中转字,返回 (sentinel, true) 让 caller
// wasm 经 call_indirect 直达(免 code.Run 双跨层);否则返回 (0, false) 走回退。
//
// 与 doCall 的被调解析严格同构(nargs/nresults 解码、funcIdx 定位),但**只**拦截
// 「普通 Lua closure + 已升 gibbous + 有 slot」这一种;host / __call / 未升层 / 表满
// gibbous 一律不拦(handled=false),由 DoCall 回退路径的 doCall 统一分派,保正确性。
//
// **压帧等价 enterGibbous**:enterLuaFrame(标 fresh=false)+ SetGibbous(true) +
// reMirrorTop——与 crescent→gibbous 入口的 enterGibbous(gibbous_host.go)逐字段同款,
// 差别仅在「不在此处 code.Run,改由 caller call_indirect 跑」。被调 RETURN 经 DoReturn
// 弹本帧 + 写刷新后 caller base 到中转字(对称 enterGibbous 由 trampoline 弹帧)。
func (st *State) tryIndirectCallee(th *thread, ci *callInfo, a, b, c int32) (int64, bool) {
	if !profileEnabled || th != st.mainTh {
		return 0, false // 协程线程不升层(§5),非 profile build 无 gibbous
	}
	funcIdx := ci.base + int(a)
	callee := th.slot(funcIdx)
	if value.Tag(callee) != value.TagFunction {
		return 0, false // __call 元方法 / 非可调用 → 回退(doCall 处理)
	}
	cl := value.GCRefOf(callee)
	if object.IsHostClosure(st.arena, cl) {
		return 0, false // host fn → 回退
	}
	pid := object.ClosureProtoID(st.arena, cl)
	code := st.bridge.GibbousCodeOf(st.protos[pid])
	if code == nil {
		return 0, false // 未升层(crescent)→ 回退
	}
	slot, ok := code.Slot()
	if !ok {
		return 0, false // 表满哨兵(无 slot)→ 回退走 code.Run(baseline)
	}
	// PW10 零跨界惰性填充 IC:把 slot 缓存进 closure word1 高 16 位,后续 Wasm 侧
	// emitCall 直读免本路径的 Go map 查找(GibbousCodeOf + Slot)。幂等(每次写同值)。
	object.SetClosureGibbousSlot(st.arena, cl, slot)
	// nargs/nresults 解码(与 doCall 同款:B=0 取到 top,C=0 留到 top)。
	var nargs int
	if b == 0 {
		nargs = th.top - funcIdx - 1
	} else {
		nargs = int(b) - 1
	}
	nresults := int(c) - 1
	// 压帧(等价 enterGibbous:enterLuaFrame + 置 gibbous 位 + 重镜像)。
	if e := st.enterLuaFrame(th, funcIdx, nargs, nresults, false); e != nil {
		st.raiseGibbous(e) // 锚定行号(currentCI 仍是调用者帧)
		return -1, true
	}
	cci := st.gibCI(th)
	cci.SetGibbous(true)
	th.reMirrorTop()
	// 被调帧 base 字节偏移写中转字,供 caller wasm 读作 call_indirect 实参。
	calleeBaseByte := (th.stackBaseW + uint32(cci.base)) * 8
	st.arena.SetWordAt(st.ciTransferRef, uint64(calleeBaseByte))
	st.indirectCalls++              // 计直调命中(R3 验证用)
	return int64(slot)<<1 | 1, true // indirect 哨兵(奇数)
}

// Call 处理 gibbous 帧内的 CALL A B C(04-trampoline §3 + PW10 R3 直调)。
//
// **R3 快路径**:被调是 gibbous-有-slot 的 Lua closure ⟹ tryIndirectCallee 自己
// 压帧 + 置 gibbous 位 + 把被调帧 base 写中转字,返回 indirect 哨兵——caller wasm
// 据此 `call_indirect <slot>` 跨 module 直达(免 code.Run ~143ns 双跨层重入)。
//
// **回退**:host / crescent(未升层)/ __call 元方法 / 无-slot gibbous(表满)⟹
// 复用 doCall 统一分派同步跑完(baseline,返回值已落 R(A..),next==nil 或起一层
// executeFrom 驱动未升层 Lua 帧)。
//
// **base 刷新(PW6 核心,回退路径)**:被调帧可能 growStack 使值栈段在 arena 重定位,
// 本帧 $base 随之失效。回退路径返回时按当前 stackBaseW + ci.base 重算新 base 字节
// 偏移(偶数);R3 直调路径的 base 刷新由被调 RETURN 经 DoReturn 写中转字完成。
//
// 返回(i64 三态,Wasm 侧据此分派,负检查须先于奇偶):
//   - < 0(-1):错误,pendingErr 已置,status 链冒泡;
//   - 奇数 (slot<<1)|1:indirect 直调,被调帧 base 已写中转字;
//   - 偶数(8 的倍数):done,值 = 刷新后的本帧 base 字节偏移(回退路径同步跑完)。
func (st *State) DoCall(base, pc, a, b, c int32) int64 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc
	// R3 快路径:被调 gibbous-有-slot ⟹ 压帧 + 返 indirect 哨兵(caller call_indirect)。
	if ret, handled := st.tryIndirectCallee(th, ci, a, b, c); handled {
		return ret
	}
	// 回退:host / crescent / __call / 无-slot gibbous —— 同步跑完(baseline)。
	ins := bytecode.EncodeABC(bytecode.CALL, int(a), int(b), int(c))
	next, e := st.doCall(th, ci, ins)
	if e != nil {
		st.raiseGibbous(e)
		return -1
	}
	if next != nil {
		// 进入一个新 Lua 帧(被调是未升层 closure)——同步驱动到完成。
		// nCcalls 计费:executeFrom 是新的 Go 栈重入边界,防 gibbous↔crescent
		// 交替递归打爆 Go 栈(meta.go callLuaFromHost 同款守卫)。
		if st.nCcalls >= maxCCallDepth {
			st.raiseGibbous(errf("C stack overflow"))
			return -1
		}
		st.nCcalls++
		entryDepth := th.ciDepth - 1
		e2 := st.executeFrom(th, entryDepth)
		st.nCcalls--
		if e2 != nil {
			st.raiseGibbous(e2)
			return -1
		}
	}
	// 刷新 base(嵌套帧可能 growStack 段重定位,陈旧 base 指向已 Free 段 = UAF)。
	ci = st.gibCI(th)
	return int64((th.stackBaseW + uint32(ci.base)) * 8)
}

// CallBaseline 处理 P4 PJ5 简化形态的 CALL A B C(承
// internal/gibbous/jit/host.go::P4HostState.CallBaseline)。
//
// **与 DoCall 的差异**:DoCall 经 tryIndirectCallee 走 P3 R3 indirect 快路径
// (返回 (slot<<1)|1 哨兵让 caller wasm 经 call_indirect 跳被调 run);P4 PJ5
// 简化形态没有 wasm-level 段内 indirect 通道,所以 CallBaseline 直接走
// baseline doCall 分派(host/crescent/__call/全形态 gibbous 一律同步跑完),
// 避免压被调帧但永不执行的悬挂。
//
// 返回:0=OK / 1=ERR(pendingErr 已置,raiseGibbous)。本路径完成后被调帧
// 已结算 + 结果已落 R(A..A+C-2),caller 帧仍活,等待 Run 端 DoReturn 弹帧。
//
// **base 刷新**:本简化形态 mmap 段不读 valueStackBase(callBaseline 直走
// host 同步路径),所以 baseline 内 growStack 段重定位对 P4 段内不可见——
// 仅 host 端见到 stale base 风险,doCall 内已正确刷新 ci.base 续算。
func (st *State) CallBaseline(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc
	ins := bytecode.EncodeABC(bytecode.CALL, int(a), int(b), int(c))
	next, e := st.doCall(th, ci, ins)
	if e != nil {
		st.raiseGibbous(e)
		return 1
	}
	if next != nil {
		// 进入新 Lua 帧(被调是未升层 closure 或 gibbous 无-slot)——同步驱动
		// 到完成。nCcalls 计费同 DoCall(meta.go callLuaFromHost 同款守卫)。
		if st.nCcalls >= maxCCallDepth {
			st.raiseGibbous(errf("C stack overflow"))
			return 1
		}
		st.nCcalls++
		entryDepth := th.ciDepth - 1
		e2 := st.executeFrom(th, entryDepth)
		st.nCcalls--
		if e2 != nil {
			st.raiseGibbous(e2)
			return 1
		}
	}
	return 0
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
	ci := st.gibCI(th)
	ci.pc = pc
	ins := bytecode.EncodeABC(bytecode.TAILCALL, int(a), int(b), int(c))
	next, e := st.doTailCall(th, ci, ins)
	if e != nil {
		st.raiseGibbous(e)
		return 1
	}
	if next == nil {
		// host 尾调用:结果已落 base+a,G 帧未弹 → 回退给尾随 RETURN(DoReturn)。
		return 2
	}
	// Lua 尾调用:G 已被 callee 帧替换。同步驱动 callee 链到完成。
	if st.nCcalls >= maxCCallDepth {
		st.raiseGibbous(errf("C stack overflow"))
		return 1
	}
	st.nCcalls++
	entryDepth := th.ciDepth - 1
	e2 := st.executeFrom(th, entryDepth)
	st.nCcalls--
	if e2 != nil {
		st.raiseGibbous(e2)
		return 1
	}
	return 0
}

// ExecuteCalleeFromInlineFrame Spike 1 Step C-1 helper(承
// `docs/design/p4-method-jit/implementation-progress.md` §9.20.9 trampoline
// exit-resume 协议 commit-2 接口 + commit-5d 真实装)。
//
// **前置条件**(caller mmap 段 BuildVoid0ArgSkeleton 已保证):mmap 段已写完
// CallInfo[depth] 5 word 字段 + EmitFrameInlineCIDepthInc 做 ciDepth++,但
// thread.cur 字段未被 mmap 段更新(Go 端冷字段)。
//
// **流程**(承 §9.20.9 (1) 协议总览 + helper 真实装):
//  1. readCISegInto(th.ciDepth-1, &th.cur) — 重载 callee 帧到 th.cur 热镜像
//     (mmap 段已写好 ciDepth+1 帧;现在 th.ciDepth 已经是 callee 视角)
//  2. nCcalls 限深 + nCcalls++(防 C stack overflow,callee 是 Go 端同步驱动
//     的真 Go stack frame)
//  3. executeFrom(th, th.ciDepth-1) — 同步驱动 callee Lua 体到 RETURN(执行
//     期内嵌 popCallInfo 让 th.cur 回到 caller 视角,但 ciDepth 保留 caller+1
//     此时是 caller 视角)
//
// **wait**:executeFrom 内 callee RETURN 会经 doReturn → 移结果到 caller's
// R(retA..) + 弹 callee 帧(ciDepth--)。返回 nil 时 caller 帧已被 callee
// 完成 + ciDepth = caller 自身深度(不包括 callee)。
//
// **重要**:executeFrom 已经替我们完成了 popCallInfo,我们**不需要**额外调
// popCallInfo。但 mmap 段会再执行 PopVoid0ArgSkeleton(emit 字节级 ciDepth--),
// 那时已经被 callee RETURN doReturn 减过一次了,会 double-decrement!
//
// **解决**:helper 在 executeFrom 返回前**让 ciDepth 仅在 callee 帧弹后等于
// caller depth + 0**(callee 已 RETURN 自动 popCallInfo)。而 PopVoid0Arg 段
// emit 假设的是「helper 完成后 ciDepth 仍是 caller depth + 1」。两者矛盾。
//
// **简化**:helper 完成后,**手动 ciDepth++ 抵消 mmap 段后续 PopVoid0Arg 的
// dec**。即 helper 结束时 ciDepth = caller_depth + 1(虚拟 callee 帧仍占位),
// PopVoid0Arg 段 ciDepth-- 后变 caller_depth,正确。
//
// 4. nCcalls--
// 5. 返 0=OK / 1=ERR(err 已 raiseGibbous 置 pendingErr)
//
// **当前 Spike 1 阶段 archSupportsFrameInline=false 屏蔽真触发**,但本函数
// 真实装就位:commit-5f 翻闸门 + analyzeSelfCallSpecForm 设 useFrameInline
// 后启用,无需重写。
func (st *State) ExecuteCalleeFromInlineFrame(base, retA int32) int32 {
	_ = base // base 参数为 P3 PW10 对位 + future 多 callee 路径预留(不读 base 槽寻址)
	_ = retA // retA 同款预留(callee 返值落 R(retA..)由 callee RETURN doReturn 处理)
	th := st.runningThread
	// 1. mmap 段已 ciDepth++ 并写 CallInfo[ciDepth-1] 5 word,th.cur 仍是
	//    caller 视角(冷字段未刷)。先把 caller 视角的 th.cur 刷回段(否则
	//    callee RETURN 弹帧后 readCISegInto 重载 caller 时取的是过时数据)。
	if th.ciDepth >= 2 {
		// caller 在 ciDepth-2 帧(callee 在 ciDepth-1)
		th.writeCISeg(th.ciDepth-2, &th.cur)
	}
	// 2. 重载 callee 帧热镜像:th.cur = 段第 ciDepth-1 帧(mmap 段刚写好)
	th.readCISegInto(th.ciDepth-1, &th.cur)
	// 3. C stack 限深(callee 是 Go 端同步驱动,占用真 Go stack)
	if st.nCcalls >= maxCCallDepth {
		return st.raiseGibbous(errf("C stack overflow"))
	}
	st.nCcalls++
	// 4. 同步驱动 callee Lua 体到 RETURN(内嵌 popCallInfo 弹 callee 帧)
	entryDepth := th.ciDepth - 1
	err := st.executeFrom(th, entryDepth)
	st.nCcalls--
	if err != nil {
		return st.raiseGibbous(err)
	}
	// 5. executeFrom 已 popCallInfo(callee RETURN doReturn 内做),ciDepth 已
	//    --。但 mmap 段后续 PopVoid0Arg 会再 dec —— 我们 helper 出口需保持
	//    callee 视角 ciDepth = caller_depth + 1,让 PopVoid0Arg dec 到正确
	//    caller_depth。
	//
	//    手动 ciDepth++ 抵消(并把 caller 帧重新 writeCISeg 以保持镜像同步)。
	//    *等等*:executeFrom 弹 callee 帧后,ciDepth = caller_depth,th.cur =
	//    caller 视角(自动 readCISegInto)。我们要把 ciDepth 设回 caller_depth+1
	//    + th.cur 改回 callee 视角(因 PopVoid0Arg 准备 dec)。
	//
	//    但 PopVoid0Arg 段仅 dec ciDepth 镜像字 + ret,不改 th.cur。所以最
	//    简单做法:**让 mmap 段后续不再 dec ciDepth**——也即 helper 结束后
	//    ciDepth 是 caller_depth,PopVoid0Arg dec 后变 caller_depth - 1,错。
	//
	//    **正确做法**:helper 出口 manually set ciDepth = caller_depth + 1
	//    (虚拟 callee 占位),让 mmap PopVoid0Arg dec 到 caller_depth。但
	//    th.cur 已是 caller 视角,与 ciDepth 不一致——这正是 syncCurFromSeg
	//    Stage 1b/2 处理的不一致场景。
	//
	//    简化策略(Spike 1):helper 出口 ciDepth++ + writeCISeg(ciDepth-1,
	//    &th.cur) 让段镜像保持一致(th.cur 是 caller 视角写到 callee 槽位 —
	//    会被覆盖,但 PopVoid0Arg 不 readCISegInto,只 dec ciDepth 字)。
	//    PopVoid0Arg dec ciDepth 后 = caller_depth,正确。
	//
	//    再下次回 Go 控制流时(p4Code.Run 返回 + 进入 enterGibbous post-return
	//    + 后续 caller opcode 执行),syncCurFromSeg 经 ciDepth 字一致性检查,
	//    th.ciDepth 与字一致(都是 caller_depth),不重载 th.cur,行为正确。
	th.setCIDepth(th.ciDepth + 1)
	return 0
}
