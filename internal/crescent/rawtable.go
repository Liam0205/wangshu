// Raw table operations on the arena-native array+hash layout (01 §5.2).
//
// Replaces the Go map side table (tableSide) used during M9-M14:
//   - array part: integer keys 1..asize map directly to array[k-1];
//   - hash part: main position = hash(key) & hmask, open addressing +
//     collision chain (next index);
//   - rehash: array and hash sizes are recomputed following Lua 5.1's
//     luaH_resize approach (load factor > 50%); rehash and array<->hash
//     migration call BumpGen (IC invalidation, 05 §6.5);
//   - border: binary search over the array part (# semantics, 01 §5.2).
package crescent

import (
	"math"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// hashValue hashes an arbitrary Lua key into 32 bits (following 5.1's
// per-type hashing approach).
//
// -0.0 is normalized to +0.0 before hashing (key classification rule,
// 01 §5.2). Strings use the JSHash cached in the object header. Other boxed
// values fold their bits.
func (st *State) hashValue(key value.Value) uint32 {
	if value.IsNumber(key) {
		f := value.AsNumber(key)
		if f == 0 { // normalize -0.0
			f = 0
		}
		bits := math.Float64bits(f)
		return uint32(bits) ^ uint32(bits>>32)
	}
	if value.Tag(key) == value.TagString {
		return object.StringHash(st.arena, value.GCRefOf(key))
	}
	bits := uint64(key)
	return uint32(bits) ^ uint32(bits>>32)
}

// normKey normalizes a -0.0 numeric key to +0.0 (keeps key comparison and
// hashing consistent).
func normKey(key value.Value) value.Value {
	if value.IsNumber(key) && value.AsNumber(key) == 0 {
		return value.NumberValue(0)
	}
	return key
}

// keyEqual reports whether two keys are equal (rawequal semantics: numbers use
// float comparison, everything else compares bits; strings are interned so their
// bits are equivalent to their content).
func keyEqual(a, b value.Value) bool {
	if value.IsNumber(a) && value.IsNumber(b) {
		return value.AsNumber(a) == value.AsNumber(b)
	}
	return a == b
}

// arrayIndex reports whether key falls into the array part: k == floor(k) and
// 1 <= k <= asize. Returns (idx0based, true) or (0, false).
func arrayIndex(key value.Value, asize uint32) (uint32, bool) {
	if !value.IsNumber(key) {
		return 0, false
	}
	f := value.AsNumber(key)
	k := uint32(f)
	if float64(k) == f && k >= 1 && k <= asize {
		return k - 1, true
	}
	return 0, false
}

// rawGet looks up key on the arena-native layout (no metamethods).
func (st *State) rawGet(t arena.GCRef, key value.Value) value.Value {
	if key == value.Nil {
		return value.Nil
	}
	key = normKey(key)
	asize := object.TableASize(st.arena, t)
	if idx, ok := arrayIndex(key, asize); ok {
		return object.TableArrayAt(st.arena, t, idx)
	}
	hsize := object.TableHSize(st.arena, t)
	if hsize == 0 {
		return value.Nil
	}
	hmask := hsize - 1
	i := int32(st.hashValue(key) & hmask)
	for i >= 0 {
		k := object.NodeKey(st.arena, t, uint32(i))
		if keyEqual(k, key) {
			return object.NodeVal(st.arena, t, uint32(i))
		}
		i = object.NodeNext(st.arena, t, uint32(i))
	}
	return value.Nil
}

// rawSet writes key=val on the arena-native layout (no metamethods).
//
// val==Nil means delete (the slot keeps key set to Nil, the chain does not
// shrink — same as 5.1, cleaned up on the next rehash).
func (st *State) rawSet(t arena.GCRef, key, val value.Value) *LuaError {
	if key == value.Nil {
		return errf("table index is nil")
	}
	if value.IsNumber(key) {
		f := value.AsNumber(key)
		if f != f {
			return errf("table index is NaN")
		}
	}
	key = normKey(key)
	asize := object.TableASize(st.arena, t)
	if idx, ok := arrayIndex(key, asize); ok {
		object.SetTableArrayAt(st.arena, t, idx, val)
		return nil
	}
	// Look up an existing slot in the hash part.
	hsize := object.TableHSize(st.arena, t)
	if hsize > 0 {
		hmask := hsize - 1
		i := int32(st.hashValue(key) & hmask)
		for i >= 0 {
			k := object.NodeKey(st.arena, t, uint32(i))
			if keyEqual(k, key) {
				// Already present: update the value (val=Nil means delete;
				// the key slot is set to Nil, to be reclaimed on rehash).
				if val == value.Nil {
					st.nodeSetKV(t, uint32(i), value.Nil, value.Nil)
				} else {
					st.nodeSetVal(t, uint32(i), val)
				}
				return nil
			}
			i = object.NodeNext(st.arena, t, uint32(i))
		}
	}
	if val == value.Nil {
		return nil // deleting a nonexistent key = no-op
	}
	// Insert a new key.
	return st.insertNewKey(t, key, val)
}

// nodeSetVal updates the val of a hash slot (keeps key/next).
func (st *State) nodeSetVal(t arena.GCRef, idx uint32, val value.Value) {
	k := object.NodeKey(st.arena, t, idx)
	next := object.NodeNext(st.arena, t, idx)
	object.SetNode(st.arena, t, idx, k, val, next)
}

// nodeSetKV updates the key+val of a hash slot (keeps next).
func (st *State) nodeSetKV(t arena.GCRef, idx uint32, key, val value.Value) {
	next := object.NodeNext(st.arena, t, idx)
	object.SetNode(st.arena, t, idx, key, val, next)
}

// insertNewKey inserts a new key (following 5.1 luaH_newkey's chaining strategy,
// a simplified Brent: when the main position is occupied, find a free slot, and
// if the occupant's main position differs, relocate the occupant).
func (st *State) insertNewKey(t arena.GCRef, key, val value.Value) *LuaError {
	hsize := object.TableHSize(st.arena, t)
	if hsize == 0 {
		st.rehash(t, key)
		return st.rawSet(t, key, val)
	}
	hmask := hsize - 1
	mainPos := st.hashValue(key) & hmask
	mk := object.NodeKey(st.arena, t, mainPos)
	if mk == value.Nil && object.NodeVal(st.arena, t, mainPos) == value.Nil &&
		object.NodeNext(st.arena, t, mainPos) < 0 {
		// Main position empty: place directly.
		object.SetNode(st.arena, t, mainPos, key, val, -1)
		return nil
	}
	// Main position occupied: find a free slot.
	free, ok := st.findFreeNode(t, hsize)
	if !ok {
		// Hash full: rehash and reinsert.
		st.rehash(t, key)
		return st.rawSet(t, key, val)
	}
	occKey := object.NodeKey(st.arena, t, mainPos)
	occMain := st.hashValue(occKey) & hmask
	if occKey != value.Nil && occMain != mainPos {
		// Occupant is not in its own main position: relocate the occupant to
		// free, freeing the main position for the new key.
		// 1) Find the occupant's chain predecessor (walk from occMain along the
		//    chain to the node pointing at mainPos).
		prev := int32(occMain)
		for object.NodeNext(st.arena, t, uint32(prev)) != int32(mainPos) {
			prev = object.NodeNext(st.arena, t, uint32(prev))
		}
		// 2) Relocate the occupant.
		occVal := object.NodeVal(st.arena, t, mainPos)
		occNext := object.NodeNext(st.arena, t, mainPos)
		object.SetNode(st.arena, t, free, occKey, occVal, occNext)
		// 3) Point the predecessor at the new position.
		pk := object.NodeKey(st.arena, t, uint32(prev))
		pv := object.NodeVal(st.arena, t, uint32(prev))
		object.SetNode(st.arena, t, uint32(prev), pk, pv, int32(free))
		// 4) Place the new key at the main position.
		object.SetNode(st.arena, t, mainPos, key, val, -1)
		// Relocating an existing key changes the key->slot mapping, which
		// is exactly what gen guards: gen-only inline fast paths (P3 wasm
		// emitGetGlobal / P4 native GETGLOBAL NodeHit) bake the node index
		// at compile time and do NOT re-verify NodeKey per access (the
		// interpreter's icGetTable does, so it never noticed). Without
		// this bump the baked index silently reads the relocated slot's
		// new occupant (P4 fuzz seed 4b3d10ff17c418d4).
		object.BumpGen(st.arena, t)
		return nil
	}
	// Occupant is already in its own main position: place the new key at free,
	// chaining it into the main position's chain.
	mainNext := object.NodeNext(st.arena, t, mainPos)
	object.SetNode(st.arena, t, free, key, val, mainNext)
	mv := object.NodeVal(st.arena, t, mainPos)
	object.SetNode(st.arena, t, mainPos, occKey, mv, int32(free))
	return nil
}

// findFreeNode scans backward from lastfree for a free slot (key=Nil && val=Nil
// && next=-1).
func (st *State) findFreeNode(t arena.GCRef, hsize uint32) (uint32, bool) {
	lf := object.TableLastFree(st.arena, t)
	if lf >= hsize {
		lf = hsize
	}
	for lf > 0 {
		lf--
		if object.NodeKey(st.arena, t, lf) == value.Nil &&
			object.NodeVal(st.arena, t, lf) == value.Nil &&
			object.NodeNext(st.arena, t, lf) < 0 {
			object.SetTableLastFree(st.arena, t, lf)
			return lf, true
		}
	}
	return 0, false
}

// rehash recomputes the optimal asize/hsize and reinserts all live keys
// (extraKey is the key about to be inserted, counted into the statistics).
// Follows 5.1 luaH_resize's approach: array load factor > 50%.
func (st *State) rehash(t arena.GCRef, extraKey value.Value) {
	// 1) Collect all live key-value pairs.
	type kv struct{ k, v value.Value }
	var all []kv
	asize := object.TableASize(st.arena, t)
	for i := uint32(0); i < asize; i++ {
		v := object.TableArrayAt(st.arena, t, i)
		if v != value.Nil {
			all = append(all, kv{value.NumberValue(float64(i + 1)), v})
		}
	}
	hsize := object.TableHSize(st.arena, t)
	for i := uint32(0); i < hsize; i++ {
		k := object.NodeKey(st.arena, t, i)
		v := object.NodeVal(st.arena, t, i)
		if k != value.Nil && v != value.Nil {
			all = append(all, kv{k, v})
		}
	}
	// 2) Tally the distribution of integer keys and choose the optimal asize
	//    (power-of-two buckets: load factor > 50%).
	intCount := map[uint32]uint32{} // count of integer keys in bucket 2^i
	totalInt := uint32(0)
	maxKey := uint32(0)
	countIntKey := func(k value.Value) {
		if !value.IsNumber(k) {
			return
		}
		f := value.AsNumber(k)
		u := uint32(f)
		if float64(u) != f || u < 1 {
			return
		}
		totalInt++
		if u > maxKey {
			maxKey = u
		}
		// Fall into bucket ceil(log2(u)). Script-controlled integer keys can
		// reach the uint32 boundary (2^32-1), where `(1 << 32) = 0` also wraps
		// to 0 in Go (uint32 shift semantics), making `< u` always true and the
		// naive `for (1<<b) < u` loop spin forever — hence the b < 31 guard: b
		// tops out at 31 and exits directly (size 1<<31 = 2^31 already exceeds
		// the embedded-hardening array cap maxArraySize, so it gets clamped when
		// asize is chosen later in §2). fuzz corpus 5095a0fd13d76273
		// (`t[3333170000]=""`) hits exactly this path, triggering the infinite
		// loop that superficially looks like OOM (actually a CPU spin).
		b := uint32(0)
		for b < 31 && (uint32(1)<<b) < u {
			b++
		}
		intCount[b]++
	}
	for _, e := range all {
		countIntKey(e.k)
	}
	countIntKey(extraKey)
	// Choose asize = the largest 2^b such that integer keys in [1..2^b] > 2^(b-1).
	// Embedded hardening: the array part size is capped at maxArraySize (1<<24,
	// ~16M slots = ~128 MiB). Consistent with the loop-class thresholds of the
	// stdlib mainline table.concat range / string.rep etc. (12 §4.9 embedded-
	// hardening threshold discipline: the host process must not crash > byte-for-
	// byte parity). Array-ifying sparse keys beyond the threshold is pointless —
	// the excess naturally falls into the hash part, behavior is correct with no
	// overflow risk.
	const maxArraySize = uint32(1 << 24)
	bestASize := uint32(0)
	acc := uint32(0)
	for b := uint32(0); b <= 31; b++ {
		acc += intCount[b]
		size := uint32(1) << b
		if size > maxArraySize {
			break
		}
		if acc > size/2 {
			bestASize = size
		}
		if size >= maxKey {
			break
		}
	}
	// 3) hsize = the smallest power of two holding the remaining keys (leave 1
	//    free slot of headroom).
	nHash := uint32(len(all)) + 1 // +1 for extraKey
	if bestASize > 0 {
		// Keys landing in the array part do not occupy the hash part.
		inArray := uint32(0)
		for _, e := range all {
			if _, ok := arrayIndex(normKey(e.k), bestASize); ok {
				inArray++
			}
		}
		if _, ok := arrayIndex(normKey(extraKey), bestASize); ok {
			inArray++
		}
		nHash -= inArray
	}
	newHSize := uint32(1)
	for newHSize < nHash+1 {
		newHSize <<= 1
	}
	if nHash == 0 {
		newHSize = 0
	}
	// 4) Allocate the new parts and swap them in; return the old parts to the
	//    freelist (the attached blocks are exclusively owned by the head object,
	//    no aliasing).
	oldArr := object.TableArrayRef(st.arena, t)
	oldASize := asize
	oldNode := object.TableNodeRef(st.arena, t)
	oldHSize := hsize
	var newArr arena.GCRef
	if bestASize > 0 {
		newArr = object.AllocTableArray(st.arena, bestASize)
		st.gc.AllocCharge(bestASize * 8)
	}
	var newNode arena.GCRef
	if newHSize > 0 {
		newNode = object.AllocTableNode(st.arena, newHSize)
		st.gc.AllocCharge(newHSize * 3 * 8)
	}
	object.SetTableArray(st.arena, t, newArr, bestASize)
	object.SetTableNode(st.arena, t, newNode, newHSize)
	object.SetTableLastFree(st.arena, t, newHSize)
	object.BumpGen(st.arena, t) // shape change → IC invalidation (05 §6.5)
	if !oldArr.IsNull() {
		st.arena.Free(oldArr, oldASize*8)
	}
	if !oldNode.IsNull() {
		st.arena.Free(oldNode, oldHSize*3*8)
	}
	// 5) Reinsert all key-value pairs.
	for _, e := range all {
		_ = st.rawSet(t, e.k, e.v)
	}
}

// rawBorder computes #t: binary search over the array part (t[n]~=nil &&
// t[n+1]==nil); if the array is full, probe the hash part.
func (st *State) rawBorder(t arena.GCRef) uint32 {
	asize := object.TableASize(st.arena, t)
	if asize > 0 && object.TableArrayAt(st.arena, t, asize-1) == value.Nil {
		// There is a border inside the array part: binary search.
		lo, hi := uint32(0), asize
		for hi-lo > 1 {
			m := (lo + hi) / 2
			if object.TableArrayAt(st.arena, t, m-1) == value.Nil {
				hi = m
			} else {
				lo = m
			}
		}
		return lo
	}
	// Array full (or no array part): linearly probe the hash starting from asize.
	n := asize
	for {
		if st.rawGet(t, value.NumberValue(float64(n+1))) == value.Nil {
			return n
		}
		n++
	}
}

// rawNext implements next(t, key)'s iteration order: array part first (index
// order), then hash part (slot order).
//
// key=Nil starts from the beginning. Returns (nextKey, nextVal, ok); ok=false
// means iteration is done. Iteration-order determinism: the order is stable for
// the same table with the same shape (the "strictly byte-for-byte" premise of
// the 12 pairs ordering rule).
func (st *State) rawNext(t arena.GCRef, key value.Value) (value.Value, value.Value, bool, *LuaError) {
	asize := object.TableASize(st.arena, t)
	hsize := object.TableHSize(st.arena, t)
	// Decide the starting position.
	startArr := uint32(0)  // next array index to check
	startNode := uint32(0) // next hash slot to check
	inHash := false
	if key != value.Nil {
		key = normKey(key)
		if idx, ok := arrayIndex(key, asize); ok {
			startArr = idx + 1
		} else {
			// Found key's slot in the hash part; continue from the next slot.
			found := false
			if hsize > 0 {
				hmask := hsize - 1
				i := int32(st.hashValue(key) & hmask)
				for i >= 0 {
					if keyEqual(object.NodeKey(st.arena, t, uint32(i)), key) {
						startNode = uint32(i) + 1
						found = true
						break
					}
					i = object.NodeNext(st.arena, t, uint32(i))
				}
			}
			if !found {
				return value.Nil, value.Nil, false, errf("invalid key to 'next'")
			}
			inHash = true
		}
	}
	if !inHash {
		for i := startArr; i < asize; i++ {
			v := object.TableArrayAt(st.arena, t, i)
			if v != value.Nil {
				return value.NumberValue(float64(i + 1)), v, true, nil
			}
		}
	}
	for i := startNode; i < hsize; i++ {
		k := object.NodeKey(st.arena, t, i)
		v := object.NodeVal(st.arena, t, i)
		if k != value.Nil && v != value.Nil {
			return k, v, true, nil
		}
	}
	return value.Nil, value.Nil, false, nil
}
