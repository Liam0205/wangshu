// Table get/set / upvalue / string utilities.
//
// tableGet/tableSet are the raw access entry points (no metamethod trigger; the
// metamethod chain is in meta.go). Since the P1 wrap-up round, table data uses
// the arena-native array+hash layout (rawtable.go); the bypass Go map has been
// removed.
package crescent

import (
	"math"
	"strconv"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// tableGet implements raw get.
func (st *State) tableGet(t arena.GCRef, key value.Value) (value.Value, *LuaError) {
	return st.rawGet(t, key), nil
}

// tableSet implements raw set.
func (st *State) tableSet(t arena.GCRef, key, val value.Value) *LuaError {
	return st.rawSet(t, key, val)
}

// tableSetInt is the SETLIST fast path: integer-key write.
func (st *State) tableSetInt(t arena.GCRef, idx uint32, val value.Value) {
	_ = st.rawSet(t, value.NumberValue(float64(idx)), val)
}

// upvalGet / upvalSet: open/closed dispatch (05 §8.1).
//
// An open upvalue finds its owner thread's stack via uvOwner (coroutines each
// have an independent stack, the threadRef semantics of 01 §5.4); when the owner
// is missing, fall back to the current thread (main-thread case).
func (st *State) upvalGet(th *thread, uv arena.GCRef) value.Value {
	if object.UpvalIsClosed(st.arena, uv) {
		return object.UpvalClosedValue(st.arena, uv)
	}
	owner := st.uvOwnerOf(uv, th)
	idx := object.UpvalStackIdx(st.arena, uv)
	return owner.slot(int(idx))
}

func (st *State) upvalSet(th *thread, uv arena.GCRef, v value.Value) {
	if object.UpvalIsClosed(st.arena, uv) {
		st.arena.SetWordAt(uv+8*2, uint64(v))
		return
	}
	owner := st.uvOwnerOf(uv, th)
	idx := object.UpvalStackIdx(st.arena, uv)
	owner.setSlot(int(idx), v)
}

func (st *State) uvOwnerOf(uv arena.GCRef, fallback *thread) *thread {
	if st.uvOwner != nil {
		if o, ok := st.uvOwner[uv]; ok {
			return o
		}
	}
	return fallback
}

// findOrCreateUpval finds or creates an open upvalue pointing at
// thread.stack[stackIdx] (descending chain by stackIdx; 05 §8.3).
//
// P1 simplification: use a Go map on the thread to cache stackIdx → uvRef (does
// not affect shared semantics); the full descending-chain structure is part of
// arena-izing the value stack (see implementation-progress).
func (st *State) findOrCreateUpval(th *thread, stackIdx uint32) arena.GCRef {
	if th.openUvs == nil {
		th.openUvs = map[uint32]arena.GCRef{}
	}
	if uv, ok := th.openUvs[stackIdx]; ok {
		return uv
	}
	uv := st.allocOpenUpvalue(0, stackIdx, 0)
	if len(th.openUvs) == 0 || stackIdx > th.maxOpenIdx {
		th.maxOpenIdx = stackIdx
	}
	th.openUvs[stackIdx] = uv
	th.syncOpenGuard() // PW10 Stage 2: mirror the guard word after openUvs/maxOpenIdx changes
	if st.uvOwner == nil {
		st.uvOwner = map[arena.GCRef]*thread{}
	}
	st.uvOwner[uv] = th
	return uv
}

// closeUpvals closes all open upvalues with stackIdx ≥ level.
//
// Fast path: return O(1) when level > maxOpenIdx (or no open uv) — every RETURN
// frame goes through here, so the slow-path full map iteration is a real hotspot
// under deep-recursion load.
func (st *State) closeUpvals(th *thread, level int) {
	if len(th.openUvs) == 0 || level > int(th.maxOpenIdx) {
		return
	}
	remainMax := uint32(0)
	for idx, uv := range th.openUvs {
		if int(idx) >= level {
			val := th.slot(int(idx))
			object.CloseUpvalue(st.arena, uv, val)
			delete(th.openUvs, idx)
			delete(st.uvOwner, uv)
		} else if idx > remainMax {
			remainMax = idx
		}
	}
	th.maxOpenIdx = remainMax
	th.syncOpenGuard() // PW10 Stage 2: mirror the guard word after closing (may become empty → guard value 0)
}

// toStringBytes converts a Value to []byte (used by CONCAT).
func (st *State) toStringBytes(v value.Value) ([]byte, bool) {
	if value.IsNumber(v) {
		f := value.AsNumber(v)
		return []byte(formatLuaNumber(f)), true
	}
	if value.Tag(v) == value.TagString {
		return object.StringBytes(st.arena, value.GCRefOf(v)), true
	}
	return nil, false
}

// FormatLuaNumber exposes the %.14g format (shared by stdlib tostring and
// difftest, ensuring the number format of tostring(x) and CONCAT is byte-for-byte
// identical).
func FormatLuaNumber(f float64) string { return formatLuaNumber(f) }

// formatLuaNumber formats with %.14g (05 §4.6).
//
// Inf/NaN wording matches Lua 5.1 C printf: "inf" / "-inf" / "nan" (Go defaults
// to "+Inf" etc., which would byte-diff in difftest, 12 §10 convention).
func formatLuaNumber(f float64) string {
	if math.IsInf(f, 1) {
		return "inf"
	}
	if math.IsInf(f, -1) {
		return "-inf"
	}
	if f != f {
		return "nan"
	}
	return strconv.FormatFloat(f, 'g', 14, 64)
}

// stringCompare compares byte-by-byte in lexicographic order (05 §4.4). Returns -1/0/+1.
func stringCompare(st *State, a, b arena.GCRef) int {
	ab := object.StringBytes(st.arena, a)
	bb := object.StringBytes(st.arena, b)
	for i := 0; i < len(ab) && i < len(bb); i++ {
		if ab[i] < bb[i] {
			return -1
		}
		if ab[i] > bb[i] {
			return +1
		}
	}
	if len(ab) < len(bb) {
		return -1
	}
	if len(ab) > len(bb) {
		return +1
	}
	return 0
}
