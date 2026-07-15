// Inline cache execution (05 §6) — mono IC on GETGLOBAL/SETGLOBAL/GETTABLE/
// SETTABLE/SELF.
//
// Hit = same table (low 32 bits of tableRef) + same generation (gen) → go
// straight to the array/node slot, skipping the hash and collision chain. Miss =
// full lookup + backfill slot. Write-side invalidation: rehash/setmetatable
// BumpGen (rawtable.go / object.SetTableMeta).
package crescent

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// icGetTable is the IC-backed table read (shared by GETTABLE/SELF/GETGLOBAL):
// first try the IC direct hit; on miss go through rawGetWithLoc to backfill; if
// raw is nil, go through the __index chain.
//
// Hit validation = same table + same generation + same key: GETTABLE/SETTABLE's
// key is a dynamic RK, and the same pc may rotate through different keys (typical:
// t[i] in a loop), so an array hit must validate arrayIndex(key)==Index and a
// node hit must validate NodeKey==key.
func (st *State) icGetTable(th *thread, ci *callInfo, pc int32, obj, key value.Value) (value.Value, *LuaError) {
	if value.Tag(obj) != value.TagTable {
		return st.indexWithMeta(th, obj, key)
	}
	t := value.GCRefOf(obj)
	slot := &st.protoOf(ci).IC[pc]
	if slot.Kind == bytecode.ICKindArrayHit &&
		slot.TableRef == uint32(t) &&
		slot.Shape == object.TableGen(st.arena, t) {
		if idx, ok := arrayIndex(normKey(key), object.TableASize(st.arena, t)); ok && idx == slot.Index {
			v := object.TableArrayAt(st.arena, t, slot.Index)
			if v != value.Nil {
				return v, nil
			}
		}
		// different key or the slot went nil: fall back to the slow path
	} else if slot.Kind == bytecode.ICKindNodeHit &&
		slot.TableRef == uint32(t) &&
		slot.Shape == object.TableGen(st.arena, t) {
		if keyEqual(object.NodeKey(st.arena, t, slot.Index), normKey(key)) {
			v := object.NodeVal(st.arena, t, slot.Index)
			if v != value.Nil {
				return v, nil
			}
		}
	}
	// —— Miss: full lookup + backfill ——
	v, where, idx := st.rawGetWithLoc(t, key)
	if where != locNone && v != value.Nil {
		// P2+ #4 megamorphic active detection: if this slot has already been
		// filled (kind != 0) and the table now being refilled differs
		// (TableRef != t) or the generation differs (Shape != gen), this is a
		// "miss-after-fill refill" event — accumulate the Refill count, and at P2
		// aggregation the threshold triggers translation to FBTableMega (02 §6.2
		// scheme (B) simplified version).
		if slot.Kind != bytecode.ICKindNone &&
			(slot.TableRef != uint32(t) || slot.Shape != object.TableGen(st.arena, t)) {
			if slot.Refill < ^uint8(0) {
				slot.Refill++
			}
		}
		slot.TableRef = uint32(t)
		slot.Shape = object.TableGen(st.arena, t)
		slot.Index = idx
		if where == locArray {
			slot.Kind = bytecode.ICKindArrayHit
		} else {
			slot.Kind = bytecode.ICKindNodeHit
		}
		return v, nil
	}
	// raw miss → __index chain
	return st.indexWithMeta(th, obj, key)
}

// icSetTable is the IC-backed table write (shared by SETTABLE/SETGLOBAL):
// an IC hit = the "change value" fast path for an existing key (changing the
// value does not bump gen, so the IC stays valid).
func (st *State) icSetTable(th *thread, ci *callInfo, pc int32, obj, key, val value.Value) *LuaError {
	if value.Tag(obj) != value.TagTable {
		return st.setIndexWithMeta(th, obj, key, val)
	}
	t := value.GCRefOf(obj)
	slot := &st.protoOf(ci).IC[pc]
	if val != value.Nil { // deletion (set to nil) takes the slow path (may have rehash semantics)
		if slot.Kind == bytecode.ICKindArrayHit &&
			slot.TableRef == uint32(t) &&
			slot.Shape == object.TableGen(st.arena, t) {
			if idx, ok := arrayIndex(normKey(key), object.TableASize(st.arena, t)); ok && idx == slot.Index {
				if object.TableArrayAt(st.arena, t, slot.Index) != value.Nil {
					object.SetTableArrayAt(st.arena, t, slot.Index, val)
					return nil
				}
			}
		} else if slot.Kind == bytecode.ICKindNodeHit &&
			slot.TableRef == uint32(t) &&
			slot.Shape == object.TableGen(st.arena, t) {
			if keyEqual(object.NodeKey(st.arena, t, slot.Index), normKey(key)) &&
				object.NodeVal(st.arena, t, slot.Index) != value.Nil {
				st.nodeSetVal(t, slot.Index, val)
				return nil
			}
		}
	}
	// Miss: full write path (__newindex chain); after the write, if the key is in place, backfill the IC
	if e := st.setIndexWithMeta(th, obj, key, val); e != nil {
		return e
	}
	if val != value.Nil {
		if _, where, idx := st.rawGetWithLoc(t, key); where != locNone {
			// P2+ #4 same as doGetTable: miss-after-fill refill count
			if slot.Kind != bytecode.ICKindNone &&
				(slot.TableRef != uint32(t) || slot.Shape != object.TableGen(st.arena, t)) {
				if slot.Refill < ^uint8(0) {
					slot.Refill++
				}
			}
			slot.TableRef = uint32(t)
			slot.Shape = object.TableGen(st.arena, t)
			slot.Index = idx
			if where == locArray {
				slot.Kind = bytecode.ICKindArrayHit
			} else {
				slot.Kind = bytecode.ICKindNodeHit
			}
		}
	}
	return nil
}

// lookup-location marker (05 §6.3 RawGetWithLoc).
type tableLoc uint8

const (
	locNone tableLoc = iota
	locArray
	locNode
)

// rawGetWithLoc does a full lookup and returns the hit location (array index /
// node slot number), for IC backfill.
func (st *State) rawGetWithLoc(t arena.GCRef, key value.Value) (value.Value, tableLoc, uint32) {
	if key == value.Nil {
		return value.Nil, locNone, 0
	}
	key = normKey(key)
	asize := object.TableASize(st.arena, t)
	if idx, ok := arrayIndex(key, asize); ok {
		return object.TableArrayAt(st.arena, t, idx), locArray, idx
	}
	hsize := object.TableHSize(st.arena, t)
	if hsize == 0 {
		return value.Nil, locNone, 0
	}
	hmask := hsize - 1
	i := int32(st.hashValue(key) & hmask)
	for i >= 0 {
		if keyEqual(object.NodeKey(st.arena, t, uint32(i)), key) {
			return object.NodeVal(st.arena, t, uint32(i)), locNode, uint32(i)
		}
		i = object.NodeNext(st.arena, t, uint32(i))
	}
	return value.Nil, locNone, 0
}
