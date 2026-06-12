// Coroutines — 路线 B(08 §3):单 goroutine,resume 新起一层 execute,
// yield 信号经 yieldRequested 冒泡到 resume 边界。
//
// P1 的 thread 仍是 Go struct(值栈 arena 化是独立工作);协程对象用
// lightuserdata 句柄(coID)+ State 上的注册表表示,Lua 侧 type() 经
// 注册表识别返回 "thread"。
package crescent

import (
	"github.com/Liam0205/wangshu/internal/value"
)

// CoStatus 是协程状态机(08 §2.3)。
type CoStatus uint8

const (
	CoSuspended CoStatus = iota
	CoRunning
	CoNormal // resume 了别的协程,自己被挂在 resume 链上
	CoDead
)

func (s CoStatus) String() string {
	switch s {
	case CoSuspended:
		return "suspended"
	case CoRunning:
		return "running"
	case CoNormal:
		return "normal"
	case CoDead:
		return "dead"
	}
	return "?"
}

// coroutine 是一个协程实例:独立 thread(值栈 + CallInfo 链)+ 状态。
type coroutine struct {
	th      *thread
	status  CoStatus
	fn      value.Value // 主函数(首次 resume 时启动)
	started bool
	// yield 传值区(yield 时 co→resumer;resume 时 resumer→co)
	xfer []value.Value
}

// coRegistry 在 State 上注册协程(coID → *coroutine)。
type coRegistry struct {
	cos []*coroutine
}

// NewCoroutine 创建一个 suspended 协程,返回其 coID(lightuserdata 句柄)。
func (st *State) NewCoroutine(fn value.Value) (uint64, *LuaError) {
	if value.Tag(fn) != value.TagFunction {
		return 0, errf("bad argument #1 to 'create' (function expected)")
	}
	co := &coroutine{
		th:     newThread(),
		status: CoSuspended,
		fn:     fn,
	}
	st.cos.cos = append(st.cos.cos, co)
	return uint64(len(st.cos.cos) - 1), nil
}

// coByID 取协程(越界返回 nil)。
func (st *State) coByID(id uint64) *coroutine {
	if int(id) >= len(st.cos.cos) {
		return nil
	}
	return st.cos.cos[id]
}

// IsCoroutineHandle 判定一个 Value 是否协程句柄(lightuserdata + 注册表内)。
func (st *State) IsCoroutineHandle(v value.Value) bool {
	if value.Tag(v) != value.TagLightUD {
		return false
	}
	return st.coByID(value.AsLightUD(v)) != nil
}

// CoStatusOf 返回协程状态名(coroutine.status)。
func (st *State) CoStatusOf(id uint64) string {
	co := st.coByID(id)
	if co == nil {
		return "dead"
	}
	return co.status.String()
}

// Resume 恢复(或启动)协程(08 §3.5 / §4.2)。
//
// 返回 (results, ok, err):ok=false 时 results[0] 是错误值(resume 把错误转
// (false, errval) 语义在 stdlib 侧组装,这里返回原始信息)。
func (st *State) Resume(id uint64, args []value.Value) ([]value.Value, bool, *LuaError) {
	co := st.coByID(id)
	if co == nil {
		return nil, false, errf("cannot resume dead coroutine")
	}
	switch co.status {
	case CoDead:
		return nil, false, errf("cannot resume dead coroutine")
	case CoRunning, CoNormal:
		return nil, false, errf("cannot resume non-suspended coroutine")
	}
	resumerTh := st.runningThread
	co.status = CoRunning

	// resume 新起一层 execute(Go 栈 +1),嵌套 resume 链与 host→Lua 重入
	// 共享同一上限(05 §7.4)。
	if st.nCcalls >= maxCCallDepth {
		co.status = CoSuspended
		return nil, false, errf("C stack overflow")
	}
	st.nCcalls++
	defer func() { st.nCcalls-- }()

	// 嵌套 resume:调用者协程转 normal(5.1 状态机;coroutine.status 可见,
	// findRunningCo 也依赖"只有一个 CoRunning"判 yield 归属)。
	if resumer := st.findRunningCo(); resumer != nil {
		resumer.status = CoNormal
		defer func() { resumer.status = CoRunning }()
	}

	// 挂起的调用者线程入 resume 链(GC 根;06 §5.1 R4)。
	if resumerTh != nil {
		st.threadChain = append(st.threadChain, resumerTh)
		defer func() { st.threadChain = st.threadChain[:len(st.threadChain)-1] }()
	}

	var sig *LuaError
	if !co.started {
		// 首次 resume:在 co 的 thread 上启动主函数
		co.started = true
		st.runningThread = co.th
		co.th.push(co.fn)
		for _, a := range args {
			co.th.push(a)
		}
		if e := st.enterLuaFrame(co.th, 0, len(args), -1, true); e != nil {
			st.runningThread = resumerTh
			co.status = CoDead
			return nil, false, e
		}
		sig = st.execute(co.th)
	} else {
		// 从 yield 恢复:把 resume 参数作为 yield 的返回值传入
		co.xfer = args
		st.runningThread = co.th
		sig = st.executeResume(co.th)
	}
	st.runningThread = resumerTh

	if sig != nil {
		if sig == errYieldSentinel {
			// yield:挂起,xfer 区是 yield 的值
			co.status = CoSuspended
			out := co.xfer
			co.xfer = nil
			return out, true, nil
		}
		// 错误:协程死亡
		co.status = CoDead
		return nil, false, sig
	}
	// 正常结束:返回值在 co.th 栈上 [0, top)
	co.status = CoDead
	out := make([]value.Value, co.th.top)
	copy(out, co.th.stack[:co.th.top])
	return out, true, nil
}

// errYieldSentinel 是 yield 信号哨兵(05 §9 错误冒泡通道的复用;08 §3.4
// "yield ↔ error 对称机制"的 P1 落地:同一条显式返回通道,用哨兵区分)。
var errYieldSentinel = &LuaError{Msg: "<yield>"}

// Yield 触发当前协程挂起(coroutine.yield 的 host 实现入口)。
//
// 把 yield 值放进当前协程的 xfer 区,返回哨兵错误让 execute 冒泡。
// callHost 收到哨兵后直接向上传(不当普通错误处理)。
func (st *State) Yield(args []value.Value) *LuaError {
	co := st.findRunningCo()
	if co == nil {
		return errf("attempt to yield from outside a coroutine")
	}
	co.xfer = args
	return errYieldSentinel
}

// findRunningCo 找 runningThread 对应的协程(无则 nil = 主线程)。
func (st *State) findRunningCo() *coroutine {
	for _, co := range st.cos.cos {
		if co.th == st.runningThread && co.status == CoRunning {
			return co
		}
	}
	return nil
}

// RunningCoID 返回当前正在运行的协程 ID(主线程返回 false)。
func (st *State) RunningCoID() (uint64, bool) {
	for i, co := range st.cos.cos {
		if co.th == st.runningThread && co.status == CoRunning {
			return uint64(i), true
		}
	}
	return 0, false
}

// executeResume 从 yield 点恢复执行(08 §3.3 表格"下次 resume 从存回的
// CallInfo 重建 frame,从 yield 的下一条指令继续")。
//
// P1 实现:yield 哨兵从 callHost 冒泡时,CALL 指令已执行了一半——host 帧
// 的返回值还没写。恢复时把 resume 参数当作"yield 调用的返回值"写到
// CALL 的目标寄存器,然后从 pc(已指向 CALL 的下一条)继续主循环。
//
// yield 时保存的恢复信息在 co.th 的 pendingResume 上(yield 经 callHost
// 冒泡时由 doCall 记录)。
func (st *State) executeResume(th *thread) *LuaError {
	pr := th.pendingResume
	th.pendingResume = nil
	if pr == nil {
		return errf("cannot resume: no pending yield point")
	}
	// 把 resume 参数写到 yield CALL 的结果寄存器
	co := st.findRunningCo()
	var vals []value.Value
	if co != nil {
		vals = co.xfer
		co.xfer = nil
	}
	ci := &th.cis[pr.ciIndex]
	want := pr.nresults
	if want < 0 {
		// 可变:全部落下并设 top
		need := pr.dst + len(vals)
		th.ensureStack(need)
		copy(th.stack[pr.dst:], vals)
		th.top = need
	} else {
		th.ensureStack(pr.dst + want)
		for k := 0; k < want; k++ {
			if k < len(vals) {
				th.stack[pr.dst+k] = vals[k]
			} else {
				th.stack[pr.dst+k] = value.Nil
			}
		}
		th.top = ci.base + int(ci.proto.MaxStack)
	}
	// 从 yield 的下一条指令继续(pc 已指向下一条;entryDepth 用 fresh 帧深度)
	return st.executeFrom(th, pr.entryDepth)
}

// pendingResumeInfo 记录 yield 点的恢复信息。
type pendingResumeInfo struct {
	ciIndex    int // yield 发生时的 ci 下标
	dst        int // yield CALL 的结果寄存器(绝对栈位)
	nresults   int // yield CALL 期望的返回数
	entryDepth int // execute 的 entry 深度(恢复后冒泡边界不变)
}
