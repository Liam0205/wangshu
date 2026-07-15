// Allocation helpers — every GC object allocation goes through here, doing
// LinkSweep + accounting.
//
// safepoint is called explicitly by each opcode at the end of an allocation
// point (st.safepoint(th, ci)); wired in M10.
package crescent

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// allocLuaClosure allocates a Lua closure and links it into the sweep chain +
// accounts for it.
func (st *State) allocLuaClosure(protoID uint32, nupvals uint16) arena.GCRef {
	ref := object.AllocLuaClosure(st.arena, protoID, nupvals)
	st.gc.LinkSweep(ref)
	st.gc.AllocCharge(uint32(2+nupvals) * 8)
	return ref
}

// allocOpenUpvalue allocates an open upvalue.
func (st *State) allocOpenUpvalue(threadRef arena.GCRef, stackIdx uint32, next arena.GCRef) arena.GCRef {
	ref := object.AllocOpenUpvalue(st.arena, threadRef, stackIdx, next)
	st.gc.LinkSweep(ref)
	st.gc.AllocCharge(3 * 8)
	return ref
}

// allocTable allocates a table header (plus its array/hash side blocks).
func (st *State) allocTable(asize, hsize uint32) arena.GCRef {
	ref := object.AllocTable(st.arena, asize, hsize)
	st.gc.LinkSweep(ref)
	bytes := uint32(6*8) + asize*8 + hsize*3*8
	st.gc.AllocCharge(bytes)
	return ref
}

// safepoint is called at the end of an allocating opcode (05 §5.2 / §5.3):
// it checks the threshold and, if needed, triggers a single STW Collect.
//
// Before the call, live Values must be reachable from roots — the thread stack
// and currentCI are covered automatically via ExtraValues, while GCRefs that
// are transient in intermediate Go locals must be explicitly pushed/popped by
// the caller on the shadow stack.
func (st *State) safepoint(_ *thread, _ *callInfo) {
	st.gc.MaybeCollect()
}

// PreallocateArray grows table t's array segment to n slots (issue #10
// direction 2). Grow only, never shrink; the old array segment's contents are
// copied into the new segment (form-Y read-now + rewrite), the old hash
// segment is left untouched. BumpGen (IC invalidation).
//
// Typical use: NewTable + PreallocateArray(t, n) + SetIndex(1..n) to sidestep
// repeated rehash storms — every SetIndex(1..n) lands in the array segment
// O(1), and the whole build is a net O(n).
func (st *State) PreallocateArray(t arena.GCRef, n uint32) {
	curr := object.TableASize(st.arena, t)
	if n <= curr {
		return // grow only, never shrink
	}
	// Read the old array segment's contents (form-Y read-now, avoids caching a
	// derived slice)
	oldArr := object.TableArrayRef(st.arena, t)
	var oldData []value.Value
	if curr > 0 {
		oldData = make([]value.Value, curr)
		for i := uint32(0); i < curr; i++ {
			oldData[i] = object.TableArrayAt(st.arena, t, i)
		}
	}
	// Allocate the new segment + replace
	newArr := object.AllocTableArray(st.arena, n)
	st.gc.AllocCharge(n * 8)
	object.SetTableArray(st.arena, t, newArr, n)
	// Copy the data (SetTableArray already points t at newArr, so
	// SetTableArrayAt writes the new segment)
	for i, v := range oldData {
		if v != value.Nil {
			object.SetTableArrayAt(st.arena, t, uint32(i), v)
		}
	}
	if !oldArr.IsNull() {
		st.arena.Free(oldArr, curr*8)
	}
	object.BumpGen(st.arena, t)
}

// NewArrayTableFromVals builds a table with the array segment preallocated to
// n=len(vals) slots, writing vals in directly; the hash segment is 0 (no hash).
// issue #10 direction 2: build a Lua array table from a Go slice in one shot,
// avoiding repeated rehash storms. Returns a table-kind GCRef (the caller is
// responsible for PinRef).
func (st *State) NewArrayTableFromVals(vals []value.Value) arena.GCRef {
	n := uint32(len(vals))
	t := st.allocTable(n, 0)
	for i, v := range vals {
		if v != value.Nil {
			object.SetTableArrayAt(st.arena, t, uint32(i), v)
		}
	}
	return t
}
