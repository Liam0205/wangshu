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
	if c.roots.ProgramStringRefs != nil {
		c.roots.ProgramStringRefs(func(ref arena.GCRef) { c.markRef(ref) })
	}
	for _, mt := range c.roots.TypeMetatables {
		c.markRef(mt)
	}
	if c.roots.ExtraValues != nil {
		c.roots.ExtraValues(func(v value.Value) { c.markValue(v) })
	}
	if c.roots.ExtraRefs != nil {
		c.roots.ExtraRefs(func(ref arena.GCRef) { c.markRef(ref) })
	}
	// shadow stack (R7).
	for _, v := range c.shadow {
		c.markValue(v)
	}
}

// markValue grays and pushes a collectable Value (if it is currently dead-white).
func (c *Collector) markValue(v value.Value) {
	if !value.IsCollectable(v) {
		return
	}
	c.markRef(value.GCRefOf(v))
}

// markRef grays and pushes the object pointed to by ref (if it is currently dead-white).
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
		return // already gray/black/or current-white (processed or newly born), skip
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
		// blacken on pop (a string is a leaf but takes the same path; scanObject is a no-op).
		h := object.HeaderOf(c.a, ref)
		object.SetHeader(c.a, ref, object.SetColor(h, object.ColorBlack))
		c.scanObject(ref, object.OTypeOf(h))
	}
}

// scanObject traverses the GCRef-bearing fields of a head object (06 §5.2).
func (c *Collector) scanObject(ref arena.GCRef, ot object.OBJType) {
	switch ot {
	case object.OBJ_STRING:
		// leaf, no child references.
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
		// closed: scan the self-held value; open: its value lives in a Thread stack
		// slot and is reached when scanning the Thread.
		if object.UpvalIsClosed(c.a, ref) {
			c.markValue(object.UpvalClosedValue(c.a, ref))
		}
	}
}

// scanTable: all array-segment slots + hash node key/val + metaRef.
//
// Weak-table exception (06 §8.4): if the table metatable's __mode contains
// 'k'/'v', the weak side is not marked and the table is registered in weakList.
// M5 stage: object.TableWeakMode is a stub (returns 0), so everything uses
// strong-reference semantics; this branch activates once M11 wires up metatables.
func (c *Collector) scanTable(t arena.GCRef) {
	mode := object.TableWeakMode(c.a, t)
	weakKey := mode == 'k' || mode == 'a' // 'a' = both key+value weak (final TableWeakMode value)
	weakVal := mode == 'v' || mode == 'a'
	// Array segment: keys are numbers, so weak keys have no effect; values are governed by weakVal.
	asize := object.TableASize(c.a, t)
	for i := uint32(0); i < asize; i++ {
		v := object.TableArrayAt(c.a, t, i)
		if !weakVal {
			c.markValue(v)
		}
	}
	// Hash segment.
	hsize := object.TableHSize(c.a, t)
	for i := uint32(0); i < hsize; i++ {
		k := object.NodeKey(c.a, t, i)
		v := object.NodeVal(c.a, t, i)
		// An empty slot has key=Nil which is not collectable; markValue skips it automatically.
		if !weakKey {
			c.markValue(k)
		}
		if !weakVal {
			c.markValue(v)
		}
	}
	c.markRef(object.TableMetaRef(c.a, t))
	// Weak-table registration (mode only becomes non-zero after M11 lands metatables;
	// M5 does not reach this branch for now).
	if mode != 0 {
		c.weakList = append(c.weakList, t)
	}
}

// scanClosure: a Lua closure scans upvalRef[]; a host closure scans its direct Value upvalues.
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

// scanThread: value stack [0,top) + open upvalue chain + resumeFrom. The closure/Value
// fields of CallInfo frames are determined by the CallInfo layout fixed in 05; once M9
// wires up the interpreter the scan details are expanded, for now only the value stack and
// openUpvalues are scanned.
func (c *Collector) scanThread(th arena.GCRef) {
	top := object.ThreadTop(c.a, th)
	for i := uint32(0); i < top; i++ {
		c.markValue(object.ThreadValueStackAt(c.a, th, i))
	}
	// Open upvalue chain (descending order)
	uv := object.ThreadOpenUpvalHead(c.a, th)
	for !uv.IsNull() {
		c.markRef(uv)
		uv = object.UpvalNextOpen(c.a, uv)
	}
	c.markRef(object.ThreadResumeFrom(c.a, th))
}

// deadWhite returns this cycle's collection color (= last cycle's currentWhite).
func (c *Collector) deadWhite() uint8 { return c.currentWhite ^ 1 }
