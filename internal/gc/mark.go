package gc

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// markRoots seeds the gray stack from the R1..R9 root set (06 §5.1).
func (c *Collector) markRoots() {
	c.gray = c.gray[:0]
	c.markRef(c.roots.Globals)
	c.markRef(c.roots.Registry)
	c.markRef(c.roots.MainThread)
	c.markRef(c.roots.RunningThread)
	for _, th := range c.roots.Threads {
		c.markRef(th)
	}
	if c.roots.ProtoConstants != nil {
		c.roots.ProtoConstants(func(v value.Value) { c.markValue(v) })
	}
	for _, mt := range c.roots.TypeMetatables {
		c.markRef(mt)
	}
	// shadow stack(R7)。
	for _, v := range c.shadow {
		c.markValue(v)
	}
}

// markValue 把可回收 Value 置灰入栈(若它当前是死白)。
func (c *Collector) markValue(v value.Value) {
	if !value.IsCollectable(v) {
		return
	}
	c.markRef(value.GCRefOf(v))
}

// markRef 把 ref 指向的对象置灰入栈(若它当前是死白)。
func (c *Collector) markRef(ref arena.GCRef) {
	if ref.IsNull() {
		return
	}
	h := object.HeaderOf(c.a, ref)
	if object.IsFixed(h) {
		return
	}
	color := object.ColorOf(h)
	if color != c.deadWhite() {
		return // 已灰/黑/或当前白(已处理或新生),跳过
	}
	object.SetHeader(c.a, ref, object.SetColor(h, object.ColorGray))
	c.gray = append(c.gray, ref)
}

// markAll drains the gray stack until empty.
func (c *Collector) markAll() {
	for len(c.gray) > 0 {
		n := len(c.gray) - 1
		ref := c.gray[n]
		c.gray = c.gray[:n]
		// 出栈即黑(string 是叶子节点也走相同路径,scanObject 是 no-op)。
		h := object.HeaderOf(c.a, ref)
		object.SetHeader(c.a, ref, object.SetColor(h, object.ColorBlack))
		c.scanObject(ref, object.OTypeOf(h))
	}
}

// scanObject traverses the GCRef-bearing fields of a head object (06 §5.2).
func (c *Collector) scanObject(ref arena.GCRef, ot object.OBJType) {
	switch ot {
	case object.OBJ_STRING:
		// 叶子,无子引用。
	case object.OBJ_TABLE:
		c.scanTable(ref)
	case object.OBJ_CLOSURE:
		c.scanClosure(ref)
	case object.OBJ_USERDATA:
		c.markRef(object.UserdataMetaRef(c.a, ref))
		c.markRef(object.UserdataEnvRef(c.a, ref))
	case object.OBJ_THREAD:
		c.scanThread(ref)
	case object.OBJ_UPVAL:
		// 关闭态:扫 self-held value;开放态:其值在 Thread 栈槽,由 Thread 扫到。
		if object.UpvalIsClosed(c.a, ref) {
			c.markValue(object.UpvalClosedValue(c.a, ref))
		}
	}
}

// scanTable: 数组段全槽 + 哈希节点 key/val + metaRef。
//
// 弱表例外(06 §8.4):若 table 元表 __mode 含 'k'/'v',弱侧不标记并登记 weakList。
// M5 阶段:object.TableWeakMode 是 stub(返回 0),故全部走强引用语义;M11 接入元表后此分支生效。
func (c *Collector) scanTable(t arena.GCRef) {
	mode := object.TableWeakMode(c.a, t)
	weakKey := mode == 'k' || mode == 'a' // 'a' 仅占位:某些约定用 'k'+'v' 表 both
	weakVal := mode == 'v' || mode == 'a'
	// 数组段:键是数字,弱键不影响;值受 weakVal 控。
	asize := object.TableASize(c.a, t)
	for i := uint32(0); i < asize; i++ {
		v := object.TableArrayAt(c.a, t, i)
		if !weakVal {
			c.markValue(v)
		}
	}
	// 哈希段。
	hsize := object.TableHSize(c.a, t)
	for i := uint32(0); i < hsize; i++ {
		k := object.NodeKey(c.a, t, i)
		v := object.NodeVal(c.a, t, i)
		// 空槽 key=Nil 不可回收,markValue 内自动跳过。
		if !weakKey {
			c.markValue(k)
		}
		if !weakVal {
			c.markValue(v)
		}
	}
	c.markRef(object.TableMetaRef(c.a, t))
	// 弱表登记(M11 元表落地后 mode 才会非 0;M5 暂不会进此分支)。
	if mode != 0 {
		c.weakList = append(c.weakList, t)
	}
}

// scanClosure: Lua 闭包扫 upvalRef[];host 闭包扫直接 Value upvalues。
func (c *Collector) scanClosure(cl arena.GCRef) {
	n := uint32(object.ClosureNUpvals(c.a, cl))
	if object.IsHostClosure(c.a, cl) {
		for i := uint32(0); i < n; i++ {
			c.markValue(object.HostClosureUpval(c.a, cl, uint16(i)))
		}
	} else {
		for i := uint32(0); i < n; i++ {
			c.markRef(object.ClosureUpvalRef(c.a, cl, uint16(i)))
		}
	}
}

// scanThread: 值栈 [0,top) + 开放 upvalue 链 + resumeFrom。CallInfo 帧的 closure/Value 字段
// 由 05 定的 CallInfo 布局决定;M9 接入解释器后再展开扫描细节,此处先扫值栈与 openUpvalues。
func (c *Collector) scanThread(th arena.GCRef) {
	top := object.ThreadTop(c.a, th)
	for i := uint32(0); i < top; i++ {
		c.markValue(object.ThreadValueStackAt(c.a, th, i))
	}
	// 开放 upvalue 链(降序)
	uv := object.ThreadOpenUpvalHead(c.a, th)
	for !uv.IsNull() {
		c.markRef(uv)
		uv = object.UpvalNextOpen(c.a, uv)
	}
	c.markRef(object.ThreadResumeFrom(c.a, th))
}

// deadWhite 返回本轮回收色(= 上轮的 currentWhite)。
func (c *Collector) deadWhite() uint8 { return c.currentWhite ^ 1 }
