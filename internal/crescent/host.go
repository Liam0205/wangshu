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
// HostFn 不直接操作 thread 栈,这避免了对 Go callback 的栈协议依赖。
//
// **args 生命期契约**:args 来自 State 的实参缓冲池,仅在本次调用内有效
// (含作为返回值返回——callHost 在结果拷贝进栈之后才归还缓冲)。不得把
// args(或其子切片)存进任何越过本次调用的位置(闭包、全局、协程传值区
// 等);需要保留请显式拷贝。违约症状是"返回值被后续 host 调用覆写",
// 离根因极远,排障时可开 wangshu_trace 构建(归还时填毒值,违约即现)。
type HostFn func(st *State, args []value.Value) ([]value.Value, *LuaError)

// hostFnRegistry 是 State 上的 host function 注册表(整数 HostFnID 引用)。
//
// 槽位可回收:每个槽带引用计数(MakeHostClosure +1 / host closure 被 GC -1),
// 归零的槽进 free 链供 RegisterHostFn 复用。否则 gmatch 每次调用、mountArena
// 每次 Call 都永久追加闭包(长驻 State 反复执行的规则引擎形态下无界泄漏,
// 且每个 entry 经 Go 闭包持有 src/pat 字节拷贝)。
type hostFnRegistry struct {
	fns  []HostFn
	refs []int32  // 槽位引用计数(活跃 host closure 数)
	free []uint32 // 已归零可复用的槽位
}

// RegisterHostFn 注册一个 HostFn,返回它在 State 内的 HostFnID(复用空闲槽)。
func (st *State) RegisterHostFn(fn HostFn) uint32 {
	r := &st.hostFns
	if n := len(r.free); n > 0 {
		id := r.free[n-1]
		r.free = r.free[:n-1]
		r.fns[id] = fn
		r.refs[id] = 0
		return id
	}
	id := uint32(len(r.fns))
	r.fns = append(r.fns, fn)
	r.refs = append(r.refs, 0)
	return id
}

// MakeHostClosure 包装一个已注册的 HostFnID 为 host closure(0 upvalue)。
func (st *State) MakeHostClosure(id uint32) arena.GCRef {
	cl := object.AllocHostClosure(st.arena, id, 0)
	st.gc.LinkSweep(cl)
	st.gc.AllocCharge(2 * 8)
	st.hostFns.refs[id]++
	return cl
}

// releaseHostFn 在 host closure 被 GC 回收时释放其槽位引用(gc 包回调)。
func (st *State) releaseHostFn(id uint32) {
	r := &st.hostFns
	if int(id) >= len(r.refs) || r.refs[id] <= 0 {
		return
	}
	r.refs[id]--
	if r.refs[id] == 0 {
		r.fns[id] = nil // 释放 Go 闭包(及其捕获的 src/pat 等)
		r.free = append(r.free, id)
	}
}

// SetGlobal 把一个值挂到 globals 表的字符串键上(供 stdlib 注册 / 公共面转发)。
func (st *State) SetGlobal(name string, v value.Value) {
	ref := st.gc.Intern([]byte(name))
	key := value.MakeGC(value.TagString, ref)
	_ = st.tableSet(st.globals, key, v)
}

// GetGlobal 读取 globals 表的字符串键(与 SetGlobal 对称,公共面转发用)。
// 缺失键返回 value.Nil(对齐 Lua 5.1 `_G[k]` 语义)。
func (st *State) GetGlobal(name string) value.Value {
	ref := st.gc.Intern([]byte(name))
	key := value.MakeGC(value.TagString, ref)
	v, _ := st.tableGet(st.globals, key)
	return v
}

// PinRef 在 pin 表中登记一个 GCRef,返回句柄索引。pin 表被 GC mark 为根,
// 保证宿主 Go 侧持有期间该对象不被回收(globals 覆盖旧值后旧值仍可调)。
// 复用 freePins 空闲槽,槽位无界增长仅在「长驻 State 反复 GetGlobal 不同名
// 函数且不 Release」时出现——公共 API 用户应配套调用 Release(11 §6.2 mind)。
func (st *State) PinRef(ref arena.GCRef) uint32 {
	if n := len(st.freePins); n > 0 {
		idx := st.freePins[n-1]
		st.freePins = st.freePins[:n-1]
		st.pinnedRefs[idx] = ref
		return idx
	}
	idx := uint32(len(st.pinnedRefs))
	st.pinnedRefs = append(st.pinnedRefs, ref)
	return idx
}

// UnpinRef 释放 pin 句柄。越界 / 已释放槽位为 no-op(公共面 Value.Release
// 可能被重复调用,容错)。
func (st *State) UnpinRef(idx uint32) {
	if int(idx) >= len(st.pinnedRefs) {
		return
	}
	if st.pinnedRefs[idx].IsNull() {
		return
	}
	st.pinnedRefs[idx] = arena.GCRef(0)
	st.freePins = append(st.freePins, idx)
}

// PinnedRefAt 取回 pin 句柄对应的 GCRef;越界 / 已释放返回 Null。
// 供门面 State.Call 在用 Value(function) 实参时取出底层 closure。
func (st *State) PinnedRefAt(idx uint32) arena.GCRef {
	if int(idx) >= len(st.pinnedRefs) {
		return arena.GCRef(0)
	}
	return st.pinnedRefs[idx]
}

// baselineEntry 是 globals baseline 的一项:key 是字符串(intern 后),
// val 是 baseline 时的 value。复合值(table/function)的 GCRef 在 val 内,
// 但 baseline 表本身常驻 State,经 visitBaselineRefs 加入 GC 根防回收
// (issue #6:不接根则 baseline value 在两次 Reset 之间被 GC 死掉)。
type baselineEntry struct {
	key string
	val value.Value
}

// MarkGlobalsBaseline 拍下当前 _G 的快照作为基线(issue #6)。
// 重复调用覆盖旧 baseline。RawNext 遍历当前 globals 把所有 (string key,
// value) 拷到 State 上的 baseline 切片。
//
// 调用方契约:典型时机是 NewState 装载 stdlib 完成后立即调用,
// 把 stdlib 提供面定为基线。之后 ResetGlobalsToBaseline 可在每次
// Borrow 之间恢复该基线。
//
// **限定**:仅遍历字符串 key(stdlib 与宿主自己的全局都是字符串 key);
// 数字 / 表 / 函数等 key 跳过(stdlib 不用,实际场景不存在)。这样可
// 避免把 baseline 设计扩到任意 key 形态、保持转换简单。
func (st *State) MarkGlobalsBaseline() {
	st.baseline = st.baseline[:0]
	key := value.Nil
	for {
		nextKey, nextVal, ok, e := st.rawNext(st.globals, key)
		if e != nil || !ok {
			break
		}
		if value.Tag(nextKey) == value.TagString {
			// key 已 intern,直接拷字节(intern 池保证后续 lookup 同 ref)
			keyStr := string(object.StringBytes(st.arena, value.GCRefOf(nextKey)))
			st.baseline = append(st.baseline, baselineEntry{key: keyStr, val: nextVal})
		}
		key = nextKey
	}
}

// ResetGlobalsToBaseline 把 _G 恢复到上一次 MarkGlobalsBaseline 拍下的
// 状态(issue #6):非 baseline 字符串 key 删除(置 Nil),baseline 字符串
// key 写回 baseline value。未 Mark 过(baseline 空)则等价 ClearScriptGlobals
// 行为——全部字符串 key globals 删除,慎用。
//
// 实现:① 先 rawNext 遍历当前 globals 收集所有字符串 key;② 对 baseline
// 中的 key 写 baseline value;③ 对未在 baseline 的 key 写 Nil。
func (st *State) ResetGlobalsToBaseline() {
	// 用 baseline 建 set(短表 O(N) 线性扫即可,无需 map)
	inBaseline := func(k string) (value.Value, bool) {
		for i := range st.baseline {
			if st.baseline[i].key == k {
				return st.baseline[i].val, true
			}
		}
		return value.Nil, false
	}
	// 步骤 1:遍历当前 _G 收集字符串 key(rawNext 期间不改 globals,
	// 避免迭代器失效)
	var currentKeys []string
	key := value.Nil
	for {
		nextKey, _, ok, e := st.rawNext(st.globals, key)
		if e != nil || !ok {
			break
		}
		if value.Tag(nextKey) == value.TagString {
			currentKeys = append(currentKeys,
				string(object.StringBytes(st.arena, value.GCRefOf(nextKey))))
		}
		key = nextKey
	}
	// 步骤 2:删除非 baseline keys
	for _, k := range currentKeys {
		if _, in := inBaseline(k); !in {
			st.SetGlobal(k, value.Nil)
		}
	}
	// 步骤 3:恢复 baseline keys 到 baseline values
	for i := range st.baseline {
		st.SetGlobal(st.baseline[i].key, st.baseline[i].val)
	}
}

// BaselineSize 报告当前 baseline 中的 key 数(诊断/测试用)。
func (st *State) BaselineSize() int { return len(st.baseline) }

// callHost 同步调用一个 host closure(05 §7.5)。
//
// funcIdx:host closure 在栈上的索引;参数紧随其后;
// nresults < 0 = 调用者要可变(栈上保留全部);否则按个数补/裁。
func (st *State) callHost(th *thread, funcIdx, nargs, nresults int) *LuaError {
	cl := value.GCRefOf(th.slot(funcIdx))
	hid := object.ClosureProtoID(st.arena, cl)
	fn := st.hostFns.fns[hid]
	// args 走 State 缓冲池(host 调用是 nbody 级负载的分配大头,91%)。
	// HostFn 契约:不得越过本次调用保留 args 切片(返回 args 子切片如
	// select 合法——归还在结果拷贝进栈之后);coroutine xfer 已改拷贝。
	// host→Lua→host 嵌套时池 LIFO 自然加深。
	args := st.getArgsBuf(nargs)
	defer st.putArgsBuf(args)
	for i := 0; i < nargs; i++ {
		args[i] = th.slot(funcIdx + 1 + i)
	}
	results, e := fn(st, args)
	if e != nil {
		return e
	}
	dst := funcIdx
	n := len(results)
	if nresults < 0 {
		// 可变:全部 results 落 dst,top = dst + n
		if dst+n > th.size() {
			th.ensureStack(dst + n)
		}
		for k := 0; k < n; k++ {
			th.setSlot(dst+k, results[k])
		}
		th.setTop(dst + n)
		return nil
	}
	want := nresults
	if dst+want > th.size() {
		th.ensureStack(dst + want)
	}
	for k := 0; k < want; k++ {
		if k < n {
			th.setSlot(dst+k, results[k])
		} else {
			th.setSlot(dst+k, value.Nil)
		}
	}
	// 定长结果:恢复 top 到当前帧逻辑顶(05 §1.2 CallInfo.top 维护;对齐
	// 5.1 "L->top = ci->top")。否则前一条多值 CALL(C=0)留下的低 top 会让
	// 后续 callLuaFromHost 的脚手架覆写活跃寄存器(TFORLOOP state 槽被毁)。
	if th.ciDepth > 0 {
		ci := currentCI(th)
		frameTop := ci.base + int(st.protoOf(ci).MaxStack)
		th.ensureStack(frameTop)
		th.setTop(frameTop)
	}
	return nil
}
