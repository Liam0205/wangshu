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

	// gibbousReentryCCallCap is the soft watermark above which promoted
	// protos are dispatched to the INTERPRETER instead of their gibbous
	// code. Rationale: pure-Lua recursion costs no C stack on the
	// interpreter (the execute loop drives the CI chain; PUC 5.1 is the
	// same, which is why LUAI_MAXCALLS is 20000), but every native-tier
	// call level is a real Go re-entry chain (Run -> dispatcher ->
	// ExecutePlainCallInlineFrame -> enterGibbous -> Run ...), each
	// burning one nCcalls. Deep recursion on a promoted proto would
	// therefore hit maxCCallDepth (200) and raise "C stack overflow"
	// where the interpreter succeeds — an auto-mode-only divergence
	// (found by FuzzAutoPromote: `fib(1000)` self-recursion). Past this
	// watermark, gibbous entry points fall back to the interpreter: the
	// remaining descent runs on the CI chain with NO further Go
	// re-entry, bounded by maxLuaCallDepth exactly like P1. Results
	// stay byte-equal; only pathological-depth performance degrades.
	// Half the hard cap leaves the other half as headroom for
	// metamethod/host re-entries below the switch point.
	gibbousReentryCCallCap = maxCCallDepth / 2
)

// enterLuaFrame 准备一帧并压 CallInfo(05 §1.4)。
//
// funcIdx 是被调 closure 在栈上的索引;实参紧随其后(funcIdx+1..funcIdx+1+nargs)。
// nresults<0 表示调用者要"全部返回"。entry=true 标 callStatus_fresh(execute 边界,
// RETURN 退到此帧之下即终止 execute)。
func (st *State) enterLuaFrame(th *thread, funcIdx, nargs, nresults int, entry bool) *LuaError {
	// Lua 调用深度上限(05 §7.4;LUAI_MAXCALLS=20000 等价,对齐 5.1.5 luaconf.h)。
	// TAILCALL 先 pop 再 enter,净深度不变,proper tail call 不受限。
	if th.ciDepth >= maxLuaCallDepth {
		return errf("stack overflow")
	}
	// 指令预算的调用计费点:纯递归风暴(蹦床式互递归在深度限内反复进出)
	// 不经回边,只在此计费才兜得住。预算关闭且 ctx 未注入时 preempt
	// 内部短路。
	if e := st.preempt(); e != nil {
		return e
	}
	v := th.slot(funcIdx)
	if value.Tag(v) != value.TagFunction {
		return errf("attempt to call a %s value", st.typeNameOf(v))
	}
	cl := value.GCRefOf(v)
	if object.IsHostClosure(st.arena, cl) {
		// 防御:正常 Lua → host 走 doCall/doTailCall 的 callHost 分支;
		// 走到 enterLuaFrame 意味着调用入口绕过了 dispatch(internal bug)。
		return errf("call: host closure cannot enter Lua frame (internal dispatch bug)")
	}
	pid := object.ClosureProtoID(st.arena, cl)
	proto := st.protos[pid]
	numFixed := int(proto.NumParams)
	// VS0-e:base 重排(官方 Lua 5.1 真栈布局)。
	//
	// 原布局: [funcIdx | fix0..fixN-1 | extra0..extraM-1 | gap..MaxStack-1]
	// 新布局: [funcIdx | vararg0..varargM-1 | R(0)=fix0..R(N-1)=fixN-1 | gap..MaxStack-1]
	//         base = funcIdx + 1 + nVarargs
	// vararg 区在栈下区 stack[base-nVarargs..base);VARARG 经 th.slot(base-nV+k) 现读;
	// GC 扫栈 [0, top) 自然覆盖(vararg < base < top)。无独立 ciVarargs / ci.varargs
	// Go 切片(VS0-e 子步 ④ 退役)。
	nVarargs := 0
	if nargs > numFixed && proto.IsVararg {
		nVarargs = nargs - numFixed
	}
	base := funcIdx + 1 + nVarargs
	// 先备栈到新 base + MaxStack(覆盖重排目标区 + nil-clear 区;ensureStack 触发的段
	// 重定位经 slot/setSlot 形态 Y 现算寻址自动用新段视图)。
	need := base + int(proto.MaxStack)
	if need > th.size() {
		th.ensureStack(need)
	}
	if nVarargs > 0 {
		// 重排三步(避免覆盖):
		// ① vararg 临时读到 Go slice(短临时;子步 ④ 内 enterLuaFrame 本地,不入 ci/thread)
		// ② 固参从高到低搬到 stack[base+i] = stack[funcIdx+1+i](dst > src 防覆盖)
		// ③ vararg 写栈下区 stack[funcIdx+1+i] = vararg[i]
		buf := make([]value.Value, nVarargs)
		for i := 0; i < nVarargs; i++ {
			buf[i] = th.slot(funcIdx + 1 + numFixed + i)
		}
		for i := numFixed - 1; i >= 0; i-- {
			th.setSlot(base+i, th.slot(funcIdx+1+i))
		}
		for i := 0; i < nVarargs; i++ {
			th.setSlot(funcIdx+1+i, buf[i])
		}
	} else if nargs < numFixed {
		// 实参不足:nVarargs=0 ⟹ base=funcIdx+1=原,固参就位,补 nil 到 numFixed。
		for i := nargs; i < numFixed; i++ {
			th.setSlot(base+i, value.Nil)
		}
	}
	// nargs > numFixed && !IsVararg:超额实参在 [base+numFixed..base+nargs-1] 区,被
	// 下面 nil-clear 覆盖(原 Lua 5.1 行为:丢弃)。
	//
	// nil-clear 区 [base+numFixed, base+MaxStack)。
	for i := base + numFixed; i < base+int(proto.MaxStack); i++ {
		th.setSlot(i, value.Nil)
	}
	// LUA_COMPAT_VARARG:隐式 arg 表(5.1 默认 compat;arg = {n=#varargs, ...},
	// 占形参后第一个寄存器,codegen 已 registerLocal("arg") 预留)。VS0-e:从栈下区
	// stack[base-nVarargs..base) 现读 vararg(子步 ③ 落地后栈下区是权威 source)。
	if proto.NeedsArg {
		argTbl := st.allocTable(uint32(nVarargs), 8)
		for i := 0; i < nVarargs; i++ {
			st.tableSetInt(argTbl, uint32(i+1), th.slot(base-nVarargs+i))
		}
		nKey := value.MakeGC(value.TagString, st.gc.Intern([]byte("n")))
		_ = st.tableSet(argTbl, nKey, value.NumberValue(float64(nVarargs)))
		th.setSlot(base+numFixed, value.MakeGC(value.TagTable, argTbl))
	}
	// 压 CallInfo(PW10 R2b-4:arena 段为权威,th.cur 是栈顶帧热镜像)。
	ci := callInfo{
		base:     base,
		funcIdx:  funcIdx,
		top:      base + numFixed,
		protoID:  pid,
		cl:       cl,
		nresults: nresults,
		fresh:    entry,
		pc:       0,
		nVarargs: uint16(nVarargs), // 与栈下区 [base-nVarargs..base) 严格对齐 + 段 word4 镜像
	}
	// 先把当前栈顶帧(th.cur,可能 pc/top 已推进)刷回段,再载入新帧。
	if th.ciDepth > 0 {
		th.writeCISeg(th.ciDepth-1, &th.cur)
	}
	depth := th.ciDepth
	if depth >= th.ciCap {
		th.growCISeg(depth + 1)
	}
	th.cur = ci
	th.setCIDepth(depth + 1)
	th.writeCISeg(depth, &th.cur)
	if ciMirrorCheck {
		// wangshu_trace 安全网:回读段自检打包/解包与 th.cur 逐字段一致(R2b-1)。
		th.verifyCISeg(depth, &th.cur)
	}
	th.setTop(base + int(proto.MaxStack))
	if profileEnabled {
		st.bridge.OnEnterID(proto, pid, th == st.mainTh)
	}
	return nil
}

// popCallInfo 弹出栈顶帧,返回其副本(供 doReturn 拿 nresults 等)。弹出后从段
// 重载 caller 帧到 th.cur(若仍有 caller)。PW10 R2b-4 + VS0-e:vararg 区住栈下区
// 不再需要 ciVarargs 影子恢复;nVarargs 经段 word4 与 caller 一并解出。
func (st *State) popCallInfo(th *thread) callInfo {
	ci := th.cur
	th.setCIDepth(th.ciDepth - 1)
	if th.ciDepth > 0 {
		th.readCISegInto(th.ciDepth-1, &th.cur)
	}
	return ci
}

// currentCI 返回栈顶帧热镜像的指针。**地址稳定**(指向 th.cur,非可重定位的段/
// slice 元素)——故热循环持此指针跨 CALL/分配永不悬垂(PW10 R2b-4 消除 append
// 重定位雷区,design-claims-vs-codebase-physics §2)。修改经它直接改 th.cur,
// 下次 push/pop 边界由 writeCISeg 刷回段。
//
// **PW10 零跨界 Stage 1b 持有者审计**:Wasm 侧帧建拆(Stage 2/3)在 Wasm 执行期改
// ciDepth 字 + 段帧,但 th.cur 是**地址稳定**的固定 struct 字段——syncCurFromSeg 在
// 跨回 Go 的边界**原地更新 th.cur 内容**(非换地址),故任何持 &th.cur 的指针自动
// 见到 resync 后的内容、不悬垂。Go helper 仅在 Wasm 跨回时运行(此时 Wasm 不在栈上),
// 入口 fresh 取 currentCI;解释器调 gibbous 后按既有纪律 reload ci(execute.go)。
// ⟹ 无新陈旧持有者雷区。
func currentCI(th *thread) *callInfo { return &th.cur }

// rk 取一个 RK 操作数:< 256 取寄存器 R(rk);>=256 取常量 K(rk-256)。
// proto 由调用方传入(VS0-b:ci 不再持 *Proto,常量表经 proto.Consts 取)。
func rk(th *thread, ci *callInfo, proto *bytecode.Proto, rk int) value.Value {
	if rk < bytecode.MaxK {
		return th.slot(ci.base + rk)
	}
	return proto.Consts[rk-bytecode.MaxK]
}

// reg 简便寄存器读。
func reg(th *thread, ci *callInfo, r int) value.Value { return th.slot(ci.base + r) }

// setReg 简便寄存器写。
func setReg(th *thread, ci *callInfo, r int, v value.Value) {
	th.setSlot(ci.base+r, v)
}

// errf 构造一个 LuaError(M9 简化:Value 直接是错误字符串内容,
// 暂不 intern 进 arena;M11 错误模块再拉齐)。
func errf(format string, args ...any) *LuaError {
	msg := sprintf(format, args...)
	return &LuaError{Msg: msg}
}

// typeName returns the Lua type name (for error messages).
//
// Coroutine-handle caveat: wangshu models coroutines as lightuserdata
// handles (TagLightUD); without State context this function can only
// say "userdata". Error-message paths must use st.typeNameOf instead:
// PUC reports "thread" for thread values (cgo oracle diff fuzz catch:
// "attempt to call a thread value").
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
