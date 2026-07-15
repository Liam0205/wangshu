// Table object layout (01 §5.2, incl. backfill: word5 high 32 bits = gen generation):
//
//	word0: GCHeader (otype=TABLE; flags bit0 = has metatable)
//	word1: [31:0] asize (array slot count) | [63:32] hmask (hash slot count-1, power of two)
//	word2: arrayRef  (GCRef→ Value[asize])
//	word3: nodeRef   (GCRef→ Node[hmask+1])
//	word4: metaRef   (GCRef→ metatable Table, or 0)
//	word5: [31:0] lastfree (hash free-slot search cursor) | [63:32] gen (IC generation)
//
// Node (hash slot, 3 words = 24 bytes):
//
//	word0: key   (Value)
//	word1: val   (Value)
//	word2: [31:0] next (int32, links into collision chain; -1=none) | [63:32] reserved
//
// Key point: the Table head is a 6-word head object; the array/node segments are
// attached blocks without a GCHeader.
package object

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/value"
)

const (
	tableSizesIdx    = 1 // word1: asize | hmask
	tableArrayIdx    = 2
	tableNodeIdx     = 3
	tableMetaIdx     = 4
	tableLastFreeGen = 5 // word5: lastfree | gen
	tableHeadWords   = 6
	nodeWords        = 3
)

// Flag bits (GCHeader.flags, 4 bits; 01 §5.2 bit0 = hasMeta, bit1/bit2 = weak k/v cache)
const (
	tableFlagHasMeta uint8 = 1 << 0
	tableFlagWeakKey uint8 = 1 << 1
	tableFlagWeakVal uint8 = 1 << 2
)

// AllocTable allocates a Table head with the given pre-allocated array/hash sizes.
// When asize/hsize is 0 the corresponding attached block is not allocated
// (arrayRef/nodeRef left as 0); hsize must be a power of two or 0.
func AllocTable(a *arena.Arena, asize, hsize uint32) arena.GCRef {
	if hsize != 0 && (hsize&(hsize-1)) != 0 {
		panic("object: hsize must be a power of two")
	}
	headRef := allocateRaw(a, OBJ_TABLE, tableHeadWords, 0)
	var arrayRef, nodeRef arena.GCRef
	if asize > 0 {
		arrayRef = a.AllocWords(asize) // attached block, no GCHeader
		initArraySlots(a, arrayRef, asize)
	}
	if hsize > 0 {
		nodeRef = a.AllocWords(nodeWords * hsize)
		initNodeSlots(a, nodeRef, hsize)
	}
	hmask := uint32(0)
	if hsize > 0 {
		hmask = hsize - 1
	}
	setWordAt(a, headRef, tableSizesIdx, uint64(asize)|uint64(hmask)<<32)
	setWordAt(a, headRef, tableArrayIdx, uint64(arrayRef))
	setWordAt(a, headRef, tableNodeIdx, uint64(nodeRef))
	setWordAt(a, headRef, tableMetaIdx, 0)
	setWordAt(a, headRef, tableLastFreeGen, 0)
	return headRef
}

// TableASize / THMask / TableArrayRef / TableNodeRef / TableMetaRef field accessors.
func TableASize(a *arena.Arena, t arena.GCRef) uint32 {
	return uint32(wordAt(a, t, tableSizesIdx))
}

func TableHMask(a *arena.Arena, t arena.GCRef) uint32 {
	return uint32(wordAt(a, t, tableSizesIdx) >> 32)
}

// TableHSize returns the hash part slot count (hmask + 1, or 0 when no hash part).
func TableHSize(a *arena.Arena, t arena.GCRef) uint32 {
	w := wordAt(a, t, tableSizesIdx) >> 32
	if w == 0 && wordAt(a, t, tableNodeIdx) == 0 {
		return 0
	}
	return uint32(w) + 1
}

func TableArrayRef(a *arena.Arena, t arena.GCRef) arena.GCRef {
	return arena.GCRef(wordAt(a, t, tableArrayIdx))
}

func TableNodeRef(a *arena.Arena, t arena.GCRef) arena.GCRef {
	return arena.GCRef(wordAt(a, t, tableNodeIdx))
}

func TableMetaRef(a *arena.Arena, t arena.GCRef) arena.GCRef {
	return arena.GCRef(wordAt(a, t, tableMetaIdx))
}

// SetTableMeta sets the metatable, syncing the hasMeta flag bit + bumping gen (05 §6.5).
func SetTableMeta(a *arena.Arena, t arena.GCRef, meta arena.GCRef) {
	setWordAt(a, t, tableMetaIdx, uint64(meta))
	h := HeaderOf(a, t)
	flags := FlagsOf(h)
	if meta == 0 {
		flags &^= tableFlagHasMeta
	} else {
		flags |= tableFlagHasMeta
	}
	SetHeader(a, t, SetFlags(h, flags))
	BumpGen(a, t) // metatable changed → IC generation invalidated
}

// TableHasMeta: fast check of flags bit0.
func TableHasMeta(a *arena.Arena, t arena.GCRef) bool {
	return FlagsOf(HeaderOf(a, t))&tableFlagHasMeta != 0
}

// TableGen / BumpGen: IC generation (05 §6 / 01 §5.2 backfill).
func TableGen(a *arena.Arena, t arena.GCRef) uint32 {
	return uint32(wordAt(a, t, tableLastFreeGen) >> 32)
}

func BumpGen(a *arena.Arena, t arena.GCRef) {
	w := wordAt(a, t, tableLastFreeGen)
	gen := uint32(w>>32) + 1
	setWordAt(a, t, tableLastFreeGen, uint64(uint32(w))|uint64(gen)<<32)
}

// TableLastFree / SetTableLastFree: hash free-slot search cursor (P1 simplification:
// stores the slot index, to be revised during implementation).
func TableLastFree(a *arena.Arena, t arena.GCRef) uint32 {
	return uint32(wordAt(a, t, tableLastFreeGen))
}

func SetTableLastFree(a *arena.Arena, t arena.GCRef, lf uint32) {
	w := wordAt(a, t, tableLastFreeGen)
	setWordAt(a, t, tableLastFreeGen, uint64(lf)|(w&0xFFFFFFFF00000000))
}

// SetTableArray replaces the array segment (used by rehash): arrayRef 0 = no array segment.
func SetTableArray(a *arena.Arena, t arena.GCRef, arrayRef arena.GCRef, asize uint32) {
	w := wordAt(a, t, tableSizesIdx)
	setWordAt(a, t, tableSizesIdx, uint64(asize)|(w&0xFFFFFFFF00000000))
	setWordAt(a, t, tableArrayIdx, uint64(arrayRef))
}

// SetTableNode replaces the hash segment (used by rehash): nodeRef 0 = no hash segment
// (hmask cleared to 0 at the same time).
func SetTableNode(a *arena.Arena, t arena.GCRef, nodeRef arena.GCRef, hsize uint32) {
	hmask := uint32(0)
	if hsize > 0 {
		hmask = hsize - 1
	}
	w := wordAt(a, t, tableSizesIdx)
	setWordAt(a, t, tableSizesIdx, (w&0xFFFFFFFF)|uint64(hmask)<<32)
	setWordAt(a, t, tableNodeIdx, uint64(nodeRef))
}

// initArraySlots bulk-initializes the array segment to Nil: a raw 0 would be read
// as the number +0.0 (a valid Value), so the zero value cannot be relied upon. It
// slices the word view directly and writes in a tight loop, avoiding the per-slot
// alignment/bounds checks of SetWordAt (NEWTABLE is an allocation hot path).
func initArraySlots(a *arena.Arena, ref arena.GCRef, asize uint32) {
	w := a.Words()[ref>>3 : (ref>>3)+arena.GCRef(asize)]
	for i := range w {
		w[i] = uint64(value.Nil)
	}
}

// initNodeSlots bulk-initializes the hash segment (key/val=Nil, next=-1).
func initNodeSlots(a *arena.Arena, ref arena.GCRef, hsize uint32) {
	w := a.Words()[ref>>3 : (ref>>3)+arena.GCRef(hsize*nodeWords)]
	for i := uint32(0); i < hsize; i++ {
		base := i * nodeWords
		w[base] = uint64(value.Nil)
		w[base+1] = uint64(value.Nil)
		w[base+2] = uint64(uint32(0xFFFFFFFF))
	}
}

// AllocTableArray allocates an asize-slot array attached block (all-Nil initialized).
func AllocTableArray(a *arena.Arena, asize uint32) arena.GCRef {
	ref := a.AllocWords(asize)
	initArraySlots(a, ref, asize)
	return ref
}

// AllocTableNode allocates an hsize-slot hash attached block (key/val=Nil, next=-1).
// hsize must be a power of two.
func AllocTableNode(a *arena.Arena, hsize uint32) arena.GCRef {
	ref := a.AllocWords(nodeWords * hsize)
	initNodeSlots(a, ref, hsize)
	return ref
}

// Array segment accessors.
func TableArrayAt(a *arena.Arena, t arena.GCRef, i uint32) value.Value {
	arr := TableArrayRef(a, t)
	return value.Value(a.WordAt(arr + arena.GCRef(i*8)))
}

func SetTableArrayAt(a *arena.Arena, t arena.GCRef, i uint32, v value.Value) {
	arr := TableArrayRef(a, t)
	a.SetWordAt(arr+arena.GCRef(i*8), uint64(v))
}

// Hash node accessors (idx 0..hmask).
func NodeKey(a *arena.Arena, t arena.GCRef, idx uint32) value.Value {
	node := TableNodeRef(a, t)
	return value.Value(a.WordAt(node + arena.GCRef(idx*nodeWords*8)))
}

func NodeVal(a *arena.Arena, t arena.GCRef, idx uint32) value.Value {
	node := TableNodeRef(a, t)
	return value.Value(a.WordAt(node + arena.GCRef(idx*nodeWords*8+8)))
}

func NodeNext(a *arena.Arena, t arena.GCRef, idx uint32) int32 {
	node := TableNodeRef(a, t)
	return int32(a.WordAt(node + arena.GCRef(idx*nodeWords*8+16)))
}

func SetNode(a *arena.Arena, t arena.GCRef, idx uint32, key, val value.Value, next int32) {
	node := TableNodeRef(a, t)
	base := node + arena.GCRef(idx*nodeWords*8)
	a.SetWordAt(base, uint64(key))
	a.SetWordAt(base+8, uint64(val))
	// next occupies the low 32 bits, the high 32 bits are reserved. Preserve the
	// current high 32 bits (currently always 0).
	a.SetWordAt(base+16, uint64(uint32(next)))
}

// TableWeakMode returns the weak-table mode: 'k'/'v'/'a'(both)/0(strong table).
//
// The mode is cached in GCHeader.flags bit1/bit2 (written by SetTableWeakFlags at
// setmetatable time -- GC does not parse the __mode string during the mark phase,
// 06 §8.4 cooperation discipline).
func TableWeakMode(a *arena.Arena, t arena.GCRef) byte {
	f := FlagsOf(HeaderOf(a, t))
	wk := f&tableFlagWeakKey != 0
	wv := f&tableFlagWeakVal != 0
	switch {
	case wk && wv:
		return 'a'
	case wk:
		return 'k'
	case wv:
		return 'v'
	}
	return 0
}

// SetTableWeakFlags writes the weak-table mode cache bits (called at setmetatable time, 07 §13).
func SetTableWeakFlags(a *arena.Arena, t arena.GCRef, weakKey, weakVal bool) {
	h := HeaderOf(a, t)
	f := FlagsOf(h)
	f &^= tableFlagWeakKey | tableFlagWeakVal
	if weakKey {
		f |= tableFlagWeakKey
	}
	if weakVal {
		f |= tableFlagWeakVal
	}
	SetHeader(a, t, SetFlags(h, f))
}
