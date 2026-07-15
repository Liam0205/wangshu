package gc

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// sweep walks the gcnext sweep chain, reclaiming dead-white objects (06 §8.1).
//
// Reclaim = unlink from the sweep chain + return the bytes to the arena
// freelist (06 §2: fixed-size size-class buckets / first-fit for LARGE). Once
// returned, the block's contents are treated as dirty memory and zeroed by
// AllocBytes on reuse.
//
// Note: this function still accumulates liveBytesAfterSweep to drive pacing (06 §8.3).
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
			// Dead object: unlink from the chain + return to the freelist.
			c.unlinkSweep(prev, ref, next)
			c.freeObject(ref, object.OTypeOf(h))
		} else {
			// Live object: flip to currentWhite, pending the next round's decision.
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

// freeObject reclaims a single dead object: type-specific index cleanup + returning
// the bytes to the arena freelist.
//
// Sizes always go through object.SizeOf / the attachment-block Bytes helpers (single
// source of truth) — if the freed size is off by even one word, the freelist puts the
// block in the wrong size-class bucket and adjacent objects overlap in memory on reuse.
//
//   - String: first unlink from the intern table (the hash must be read before Free
//     overwrites word0/word1).
//   - Table: the attached array/node blocks are returned along with it (attachment blocks
//     are owned exclusively by the header object; when rehash swaps segments the old segment
//     has already been explicitly returned by rawtable, so only the current segment remains
//     here, with no double free).
//   - Closure: a host closure notifies the registry to release its slot reference.
//   - Userdata: clear the hasFinalizer entry (prevents a stale record from blocking the __gc
//     registration of a new userdata after block reuse).
//   - Thread: header + value-stack/CallInfo attachment blocks (P1 runtime coroutines live on
//     the Go side, so this type is only reached by tests).
func (c *Collector) freeObject(ref arena.GCRef, ot object.OBJType) {
	switch ot {
	case object.OBJ_STRING:
		c.removeFromStringTable(ref)
	case object.OBJ_TABLE:
		if arr := object.TableArrayRef(c.a, ref); !arr.IsNull() {
			c.a.Free(arr, object.TableArrayBytes(object.TableASize(c.a, ref)))
		}
		if node := object.TableNodeRef(c.a, ref); !node.IsNull() {
			c.a.Free(node, object.TableNodeBytes(object.TableHSize(c.a, ref)))
		}
	case object.OBJ_CLOSURE:
		if c.releaseHostFn != nil && object.IsHostClosure(c.a, ref) {
			c.releaseHostFn(object.ClosureProtoID(c.a, ref))
		}
	case object.OBJ_USERDATA:
		delete(c.hasFinalizer, ref)
	case object.OBJ_THREAD:
		if stk := object.ThreadValueStackRef(c.a, ref); !stk.IsNull() {
			c.a.Free(stk, object.ThreadStackBytes(object.ThreadStackCap(c.a, ref)))
		}
		if cis := object.ThreadCallInfoRef(c.a, ref); !cis.IsNull() {
			c.a.Free(cis, object.ThreadCIBytes(object.ThreadCICap(c.a, ref)))
		}
	}
	// The header object itself. SizeOf reads header fields, so the cleanup above must
	// complete before Free overwrites word0.
	c.a.Free(ref, object.SizeOf(c.a, ref, ot))
	// Userdata's __gc has already been split out in separateFinalizers (06 §10); nothing to do here.
}

// objectBytes returns the live byte count of the header object (for pacing stats),
// **including the attachment blocks of Table/Thread**: the allocation side (AllocCharge)
// counts the full size, so counting only the header on the stats side would make
// liveBytesAfterSweep underestimate resident large tables by orders of magnitude →
// threshold = live×pause too small → GC frequency far exceeding the design intent
// (the main drag in the binary-trees pattern).
// The header object itself goes through object.SizeOf (single source of truth); attachment
// blocks are added per their layout.
func (c *Collector) objectBytes(ref arena.GCRef, ot object.OBJType) uint32 {
	n := object.SizeOf(c.a, ref, ot)
	switch ot {
	case object.OBJ_TABLE:
		if !object.TableArrayRef(c.a, ref).IsNull() {
			n += object.TableArrayBytes(object.TableASize(c.a, ref))
		}
		if !object.TableNodeRef(c.a, ref).IsNull() {
			n += object.TableNodeBytes(object.TableHSize(c.a, ref))
		}
	case object.OBJ_THREAD:
		if !object.ThreadValueStackRef(c.a, ref).IsNull() {
			n += object.ThreadStackBytes(object.ThreadStackCap(c.a, ref))
		}
		if !object.ThreadCallInfoRef(c.a, ref).IsNull() {
			n += object.ThreadCIBytes(object.ThreadCICap(c.a, ref))
		}
	}
	return n
}

// separateFinalizers (06 §10): splits out the dead-white userdata in finalizeList this round,
// marks their reachable graphs resurrected, and moves them into the toRunFinalizers queue.
//
// M5 stage: userdata `__gc` registration is wired in once M11 metatables land; in P1 this
// function is a placeholder — per entry it keeps the still-live ones and moves out the
// dead-white ones (the actual resurrection logic will be filled in after metatables land).
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
			// "Resurrect": mark black, and (after M11) recursively mark its __gc function
			// and reachable objects.
			object.SetHeader(c.a, ud, object.SetColor(h, object.ColorBlack))
			c.toRunFinalizers = append(c.toRunFinalizers, ud)
		} else {
			keep = append(keep, ud)
		}
	}
	c.finalizeList = keep
}

// clearWeakTables (06 §8.4 + 07 §13): walks weakList, removing entries whose weak side is dead-white.
//
// M5 stage: object.TableWeakMode is a stub (always returns 0), so weakList never has elements and
// this function is a no-op. Once M11 metatables are wired in, the scan phase will register weakList
// and this function takes effect.
func (c *Collector) clearWeakTables() {
	if len(c.weakList) == 0 {
		return
	}
	dead := c.deadWhite()
	for _, t := range c.weakList {
		mode := object.TableWeakMode(c.a, t)
		weakKey := mode == 'k' || mode == 'a'
		weakVal := mode == 'v' || mode == 'a'

		// Array segment.
		asize := object.TableASize(c.a, t)
		for i := uint32(0); i < asize; i++ {
			v := object.TableArrayAt(c.a, t, i)
			if weakVal && c.refIsDead(v, dead) {
				object.SetTableArrayAt(c.a, t, i, value.Nil)
			}
		}
		// Hash segment.
		// When clearing entries the next chain must be preserved: a node may sit in the
		// middle of a collision chain, and resetting next=-1 would truncate the chain,
		// making later live entries on it unreachable (physically still there, logically
		// lost). Dead entries keep their chain link until rehash reclaims them (consistent
		// with the rawSet delete path and Lua 5.1).
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
