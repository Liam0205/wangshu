package gc

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// sweep walks the gcnext sweep chain, reclaiming dead-white objects (06 §8.1).
//
// 回收 = 从 sweep 链摘除 + 字节归还 arena freelist(06 §2:size-class 定长桶 /
// LARGE 首次适配)。归还后块内容视为脏内存,复用时由 AllocBytes 清零。
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
			// 死对象:从链摘除 + 归还 freelist。
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

// freeObject 回收一个死对象:类型相关索引清理 + 字节归还 arena freelist。
//
//   - String:先从 intern 表摘除(读 hash 须在 Free 覆写 word0/word1 之前)。
//   - Table:附属 array/node 块一并归还(附属块无 GCHeader,由头对象独占,
//     rehash 换段时旧段已由 rawtable 显式归还,此处只剩当前段,无双重释放)。
//   - Userdata:清 hasFinalizer 登记(防块复用后新 userdata 的 __gc 注册被旧记录挡掉)。
//   - Thread:头 + 值栈/CallInfo 附属块(P1 运行期协程在 Go 侧,此类型仅测试触达)。
func (c *Collector) freeObject(ref arena.GCRef, ot object.OBJType) {
	switch ot {
	case object.OBJ_STRING:
		c.removeFromStringTable(ref)
		c.a.Free(ref, 16+(object.StringLen(c.a, ref)+1+7)&^7)
	case object.OBJ_TABLE:
		asize := object.TableASize(c.a, ref)
		if arr := object.TableArrayRef(c.a, ref); !arr.IsNull() {
			c.a.Free(arr, asize*8)
		}
		hsize := object.TableHSize(c.a, ref)
		if node := object.TableNodeRef(c.a, ref); !node.IsNull() {
			c.a.Free(node, hsize*3*8)
		}
		c.a.Free(ref, 48)
	case object.OBJ_CLOSURE:
		c.a.Free(ref, 16+uint32(object.ClosureNUpvals(c.a, ref))*8)
	case object.OBJ_USERDATA:
		delete(c.hasFinalizer, ref)
		c.a.Free(ref, 32+(object.UserdataLen(c.a, ref)+7)&^7)
	case object.OBJ_THREAD:
		if stk := object.ThreadValueStackRef(c.a, ref); !stk.IsNull() {
			c.a.Free(stk, object.ThreadStackCap(c.a, ref)*8)
		}
		if cis := object.ThreadCallInfoRef(c.a, ref); !cis.IsNull() {
			c.a.Free(cis, object.ThreadCICap(c.a, ref)*4*8)
		}
		c.a.Free(ref, 72)
	case object.OBJ_UPVAL:
		c.a.Free(ref, 24)
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
