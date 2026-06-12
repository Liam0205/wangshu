// Host function infrastructure — Go 函数挂在 Lua 闭包上、由 CALL 路径同步调用。
//
// 设计:05 §7.5 + 10 §3。M12 范围内提供最小 host function 调用:
// - HostFn 签名 = func(*State, *thread, args []value.Value) (results []value.Value, err *LuaError);
// - 注册一个 HostFn 得到一个 HostFnID,用 object.AllocHostClosure 包装为 closure。
package crescent

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// HostFn 是宿主 Go 函数签名(10 §3 的 P1 版本)。
//
// args 是被调时的实参快照(由 callHost 拷出);返回 results / error。
// HostFn 不直接操作 thread 栈,这避免了对 Go callback 的栈协议依赖,代价是
// 多值传递时多一次小切片分配——M12 范围内可接受。
type HostFn func(st *State, args []value.Value) ([]value.Value, *LuaError)

// hostFnRegistry 是 State 上的 host function 注册表(整数 HostFnID 引用)。
type hostFnRegistry struct {
	fns []HostFn
}

// RegisterHostFn 注册一个 HostFn,返回它在 State 内的 HostFnID。
func (st *State) RegisterHostFn(fn HostFn) uint32 {
	id := uint32(len(st.hostFns.fns))
	st.hostFns.fns = append(st.hostFns.fns, fn)
	return id
}

// MakeHostClosure 包装一个已注册的 HostFnID 为 host closure(0 upvalue)。
func (st *State) MakeHostClosure(id uint32) arena.GCRef {
	cl := object.AllocHostClosure(st.arena, id, 0)
	st.gc.LinkSweep(cl)
	st.gc.AllocCharge(2 * 8)
	return cl
}

// SetGlobal 把一个值挂到 globals 表的字符串键上(供 stdlib 注册)。
func (st *State) SetGlobal(name string, v value.Value) {
	ref := st.gc.Intern([]byte(name))
	key := value.MakeGC(value.TagString, ref)
	_ = st.tableSet(st.globals, key, v)
}

// callHost 同步调用一个 host closure(05 §7.5)。
//
// funcIdx:host closure 在栈上的索引;参数紧随其后;
// nresults < 0 = 调用者要可变(栈上保留全部);否则按个数补/裁。
func (st *State) callHost(th *thread, funcIdx, nargs, nresults int) *LuaError {
	cl := value.GCRefOf(th.stack[funcIdx])
	hid := object.ClosureProtoID(st.arena, cl)
	fn := st.hostFns.fns[hid]
	args := make([]value.Value, nargs)
	for i := 0; i < nargs; i++ {
		args[i] = th.stack[funcIdx+1+i]
	}
	results, e := fn(st, args)
	if e != nil {
		return e
	}
	dst := funcIdx
	n := len(results)
	if nresults < 0 {
		// 可变:全部 results 落 dst,top = dst + n
		if dst+n > len(th.stack) {
			th.ensureStack(dst + n)
		}
		for k := 0; k < n; k++ {
			th.stack[dst+k] = results[k]
		}
		th.top = dst + n
		return nil
	}
	want := nresults
	if dst+want > len(th.stack) {
		th.ensureStack(dst + want)
	}
	for k := 0; k < want; k++ {
		if k < n {
			th.stack[dst+k] = results[k]
		} else {
			th.stack[dst+k] = value.Nil
		}
	}
	// 定长结果:恢复 top 到当前帧逻辑顶(05 §1.2 CallInfo.top 维护;对齐
	// 5.1 "L->top = ci->top")。否则前一条多值 CALL(C=0)留下的低 top 会让
	// 后续 callLuaFromHost 的脚手架覆写活跃寄存器(TFORLOOP state 槽被毁)。
	if len(th.cis) > 0 {
		ci := currentCI(th)
		frameTop := ci.base + int(ci.proto.MaxStack)
		th.ensureStack(frameTop)
		th.top = frameTop
	}
	return nil
}
