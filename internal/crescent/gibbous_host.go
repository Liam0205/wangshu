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
