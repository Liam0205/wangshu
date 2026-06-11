package gc

import "github.com/Liam0205/wangshu/internal/value"

// Push 登记一个临时持有的 arena 引用为根(06 §6.2)。返回 handle(== 当前栈深度)
// 用于配对校验。
//
// host 代码模式:
//
//	ref := arena.NewString(...)
//	h := gc.Push(value.MakeGC(value.TagString, ref))
//	defer gc.Pop(h)
//	... use ref ...
func (c *Collector) Push(v value.Value) int {
	c.shadow = append(c.shadow, v)
	return len(c.shadow)
}

// Pop 弹出到指定深度。配对校验:popTo 必须等于 Push 返回的 handle,防漏配对。
func (c *Collector) Pop(handle int) {
	if handle <= 0 || handle > len(c.shadow) {
		// 防御:漏配对的链路被截断,后续 Push/Pop 的 handle 都将偏移。
		// 这是 06 §6.3 host 纪律违反的兜底报错入口;P1 panic,M11 host fn 接入后改为 vm.raise。
		panic("gc: shadow stack pop out of range")
	}
	c.shadow = c.shadow[:handle-1]
}

// ShadowDepth returns the current shadow stack depth (diagnostic).
func (c *Collector) ShadowDepth() int { return len(c.shadow) }

// WriteBarrier is the placeholder write-barrier interface (06 §9.4).
//
// P1: no-op (STW GC, no incremental marking → three-color invariant trivially holds).
// P3+ (incremental GC): if isBlack(parent) && isWhite(child) && incrementalMarking { ... }.
//
// 望舒不复刻 Go runtime.gcWriteBarrier(roadmap §6 非目标)——arena 内引用是 GCRef 整数,
// Go 编译器**不会**对它们插屏障,所以 Go 屏障税我们一分不付。
func (c *Collector) WriteBarrier(parent, child value.Value) {
	_ = parent
	_ = child
	// P1 no-op.
}
