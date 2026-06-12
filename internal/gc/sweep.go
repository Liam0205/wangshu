package gc

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// sweep walks the gcnext sweep chain, reclaiming dead-white objects (06 §8.1).
//
// P1 简化:不回填 size-class freelist(M1 arena 暂未提供 freelist 接口);
// 回收 = 把对象从 sweep 链中摘除,bytes 留在 arena(下次 grow 时被压缩——P1 不做 compaction,
// 06 §2.4 / §12 已记缺口)。这意味着 P1 实际上不真正"回收"内存,但**正确性**依然成立:
// 死对象不再有 GCRef 引用它们,语义上等同于回收;真正的内存复用留给 freelist 接入(M5+ 增量)。
//
// 注意:本函数仍统计 liveBytesAfterSweep 以驱动 pacing(06 §8.3)。
func (c *Collector) sweep() {
	c.liveBytesAfterSweep = 0
	dead := c.deadWhite()
	current := c.currentWhite

	var prev arena.GCRef
	ref := c.sweepHead
	for !ref.IsNull() {
		h := object.HeaderOf(c.a, ref)
		next := object.GCNextOf(h)
		if object.ColorOf(h) == dead && !object.IsFixed(h) {
			// 死对象:从链摘除。
			c.unlinkSweep(prev, ref, next)
			c.freeObject(ref, object.OTypeOf(h))
		} else {
			// 存活对象:翻成 currentWhite,等待下轮判定。
			h = object.SetColor(h, current)
			object.SetHeader(c.a, ref, h)
			c.liveBytesAfterSweep += uint64(c.objectBytes(ref, object.OTypeOf(h)))
			prev = ref
		}
		ref = next
	}
}

// unlinkSweep removes ref from the sweep chain.
func (c *Collector) unlinkSweep(prev, ref, next arena.GCRef) {
	if prev.IsNull() {
		c.sweepHead = next
		return
	}
	hp := object.HeaderOf(c.a, prev)
	object.SetHeader(c.a, prev, object.SetGCNext(hp, next))
}

// freeObject 处理类型相关的回收副作用(string intern 表摘除、userdata finalizer 已分流等)。
//
// P1 不归还 freelist(见 sweep 注);本函数主要负责"索引清理":
//   - String:从 string intern 表摘除。
//   - 其它类型:无侧效(头对象被摘出 sweep 链已足够)。
func (c *Collector) freeObject(ref arena.GCRef, ot object.OBJType) {
	switch ot {
	case object.OBJ_STRING:
		c.removeFromStringTable(ref)
	}
	// Userdata 的 __gc 已在 separateFinalizers 中分流(06 §10),此处不再处理。
}

// objectBytes 估算一个头对象的字节数(用于 pacing 统计)。
func (c *Collector) objectBytes(ref arena.GCRef, ot object.OBJType) uint32 {
	switch ot {
	case object.OBJ_STRING:
		return 16 + (object.StringLen(c.a, ref)+1+7)&^7
	case object.OBJ_TABLE:
		return 48 // 6 字头部;附属块字节由其他对象的 GCRef 间接计入(P1 简化,不精确)
	case object.OBJ_CLOSURE:
		return 16 + uint32(object.ClosureNUpvals(c.a, ref))*8
	case object.OBJ_USERDATA:
		return 32 + (object.UserdataLen(c.a, ref)+7)&^7
	case object.OBJ_THREAD:
		return 72
	case object.OBJ_UPVAL:
		return 24
	}
	return 0
}

// separateFinalizers (06 §10):把 finalizeList 中本轮死白的 userdata 分出,标记其可达图复活,
// 移入 toRunFinalizers 队列。
//
// M5 阶段:userdata `__gc` 注册由 M11 元表落地后接入;P1 此函数为占位,逐 entry 将仍存活的
// 留下、死白的移走(实际复活逻辑等元表落地后再补)。
func (c *Collector) separateFinalizers() {
	if len(c.finalizeList) == 0 {
		return
	}
	dead := c.deadWhite()
	keep := c.finalizeList[:0]
	for _, ud := range c.finalizeList {
		if ud.IsNull() {
			continue
		}
		h := object.HeaderOf(c.a, ud)
		if object.ColorOf(h) == dead {
			// "复活":置黑,并(M11 后)递归标其 __gc 函数与可达对象。
			object.SetHeader(c.a, ud, object.SetColor(h, object.ColorBlack))
			c.toRunFinalizers = append(c.toRunFinalizers, ud)
		} else {
			keep = append(keep, ud)
		}
	}
	c.finalizeList = keep
}

// clearWeakTables (06 §8.4 + 07 §13):遍历 weakList,移除弱侧死白的 entry。
//
// M5 阶段:object.TableWeakMode 是 stub(永远返回 0),weakList 不会有元素,本函数无操作。
// M11 元表接入后,scan 阶段会登记 weakList,本函数即生效。
func (c *Collector) clearWeakTables() {
	if len(c.weakList) == 0 {
		return
	}
	dead := c.deadWhite()
	for _, t := range c.weakList {
		mode := object.TableWeakMode(c.a, t)
		weakKey := mode == 'k' || mode == 'a'
		weakVal := mode == 'v' || mode == 'a'

		// 数组段。
		asize := object.TableASize(c.a, t)
		for i := uint32(0); i < asize; i++ {
			v := object.TableArrayAt(c.a, t, i)
			if weakVal && c.refIsDead(v, dead) {
				object.SetTableArrayAt(c.a, t, i, value.Nil)
			}
		}
		// 哈希段。
		// 清条目时必须保留 next 链:节点可能位于冲突链中段,重置 next=-1 会
		// 截断链,链上后续活条目从此查不到(物理还在,逻辑丢失)。死条目
		// 保链直到 rehash 回收(与 rawSet 删除路径、Lua 5.1 一致)。
		hsize := object.TableHSize(c.a, t)
		for i := uint32(0); i < hsize; i++ {
			k := object.NodeKey(c.a, t, i)
			v := object.NodeVal(c.a, t, i)
			if (weakKey && c.refIsDead(k, dead)) || (weakVal && c.refIsDead(v, dead)) {
				next := object.NodeNext(c.a, t, i)
				object.SetNode(c.a, t, i, value.Nil, value.Nil, next)
			}
		}
	}
	c.weakList = c.weakList[:0]
}

func (c *Collector) refIsDead(v value.Value, dead uint8) bool {
	if !value.IsCollectable(v) {
		return false
	}
	h := object.HeaderOf(c.a, value.GCRefOf(v))
	return object.ColorOf(h) == dead
}
