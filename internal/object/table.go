// Table 对象布局(01 §5.2,含回填:word5 高 32 位 = gen 代次):
//
//	word0: GCHeader (otype=TABLE; flags bit0 = 有 metatable)
//	word1: [31:0] asize(数组槽数) | [63:32] hmask(哈希槽数-1,2 的幂)
//	word2: arrayRef  (GCRef→ Value[asize])
//	word3: nodeRef   (GCRef→ Node[hmask+1])
//	word4: metaRef   (GCRef→ metatable Table,或 0)
//	word5: [31:0] lastfree(哈希空闲槽搜索游标) | [63:32] gen(IC 代次)
//
// Node(哈希槽,3 字 = 24 字节):
//
//	word0: key   (Value)
//	word1: val   (Value)
//	word2: [31:0] next(int32,链入冲突链;-1=无) | [63:32] reserved
//
// 关键:Table 头是 6 字头对象;array/node 段是无 GCHeader 的附属块。
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

// 标志位(GCHeader.flags,4 bit;01 §5.2 bit0 = hasMeta,bit1/bit2 = weak k/v 缓存)
const (
	tableFlagHasMeta uint8 = 1 << 0
	tableFlagWeakKey uint8 = 1 << 1
	tableFlagWeakVal uint8 = 1 << 2
)

// AllocTable allocates a Table head with the given pre-allocated array/hash sizes.
// asize/hsize 0 时不分配对应附属块(arrayRef/nodeRef 留 0);hsize 必须为 2 的幂或 0。
func AllocTable(a *arena.Arena, asize, hsize uint32) arena.GCRef {
	if hsize != 0 && (hsize&(hsize-1)) != 0 {
		panic("object: hsize must be a power of two")
	}
	headRef := allocateRaw(a, OBJ_TABLE, tableHeadWords, 0)
	var arrayRef, nodeRef arena.GCRef
	if asize > 0 {
		arrayRef = a.AllocWords(asize) // 附属块,无 GCHeader
		// 数组槽初始化为 Nil:raw 0 会被解读为数字 +0.0(合法 Value),
		// 不能依赖零值,必须显式写 NaN-boxed Nil。
		for i := uint32(0); i < asize; i++ {
			a.SetWordAt(arrayRef+arena.GCRef(i*8), uint64(value.Nil))
		}
	}
	if hsize > 0 {
		nodeRef = a.AllocWords(nodeWords * hsize)
		for i := uint32(0); i < hsize; i++ {
			base := nodeRef + arena.GCRef(i*nodeWords*8)
			a.SetWordAt(base, uint64(value.Nil))             // key
			a.SetWordAt(base+8, uint64(value.Nil))           // val
			a.SetWordAt(base+16, uint64(uint32(0xFFFFFFFF))) // next = -1
		}
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

// TableASize / THMask / TableArrayRef / TableNodeRef / TableMetaRef 字段访问。
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

// SetTableMeta 设置 metatable,同步更新 flags 的 hasMeta 位 + bump gen(05 §6.5)。
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
	BumpGen(a, t) // metatable 改变 → IC 代次失效
}

// TableHasMeta:flags bit0 的快判位。
func TableHasMeta(a *arena.Arena, t arena.GCRef) bool {
	return FlagsOf(HeaderOf(a, t))&tableFlagHasMeta != 0
}

// TableGen / BumpGen:IC 代次(05 §6 / 01 §5.2 回填)。
func TableGen(a *arena.Arena, t arena.GCRef) uint32 {
	return uint32(wordAt(a, t, tableLastFreeGen) >> 32)
}

func BumpGen(a *arena.Arena, t arena.GCRef) {
	w := wordAt(a, t, tableLastFreeGen)
	gen := uint32(w>>32) + 1
	setWordAt(a, t, tableLastFreeGen, uint64(uint32(w))|uint64(gen)<<32)
}

// TableLastFree / SetTableLastFree:哈希空闲槽搜索游标(P1 简化:存槽索引,实现期再调)。
func TableLastFree(a *arena.Arena, t arena.GCRef) uint32 {
	return uint32(wordAt(a, t, tableLastFreeGen))
}

func SetTableLastFree(a *arena.Arena, t arena.GCRef, lf uint32) {
	w := wordAt(a, t, tableLastFreeGen)
	setWordAt(a, t, tableLastFreeGen, uint64(lf)|(w&0xFFFFFFFF00000000))
}

// SetTableArray 替换数组段(rehash 用):arrayRef 0 = 无数组段。
func SetTableArray(a *arena.Arena, t arena.GCRef, arrayRef arena.GCRef, asize uint32) {
	w := wordAt(a, t, tableSizesIdx)
	setWordAt(a, t, tableSizesIdx, uint64(asize)|(w&0xFFFFFFFF00000000))
	setWordAt(a, t, tableArrayIdx, uint64(arrayRef))
}

// SetTableNode 替换哈希段(rehash 用):nodeRef 0 = 无哈希段(hmask 同时清 0)。
func SetTableNode(a *arena.Arena, t arena.GCRef, nodeRef arena.GCRef, hsize uint32) {
	hmask := uint32(0)
	if hsize > 0 {
		hmask = hsize - 1
	}
	w := wordAt(a, t, tableSizesIdx)
	setWordAt(a, t, tableSizesIdx, (w&0xFFFFFFFF)|uint64(hmask)<<32)
	setWordAt(a, t, tableNodeIdx, uint64(nodeRef))
}

// AllocTableArray 分配一个 asize 槽的数组附属块(全 Nil 初始化)。
func AllocTableArray(a *arena.Arena, asize uint32) arena.GCRef {
	ref := a.AllocWords(asize)
	for i := uint32(0); i < asize; i++ {
		a.SetWordAt(ref+arena.GCRef(i*8), uint64(value.Nil))
	}
	return ref
}

// AllocTableNode 分配一个 hsize 槽的哈希附属块(key/val=Nil, next=-1)。
// hsize 必须是 2 的幂。
func AllocTableNode(a *arena.Arena, hsize uint32) arena.GCRef {
	ref := a.AllocWords(nodeWords * hsize)
	for i := uint32(0); i < hsize; i++ {
		base := ref + arena.GCRef(i*nodeWords*8)
		a.SetWordAt(base, uint64(value.Nil))
		a.SetWordAt(base+8, uint64(value.Nil))
		a.SetWordAt(base+16, uint64(uint32(0xFFFFFFFF)))
	}
	return ref
}

// 数组段访问。
func TableArrayAt(a *arena.Arena, t arena.GCRef, i uint32) value.Value {
	arr := TableArrayRef(a, t)
	return value.Value(a.WordAt(arr + arena.GCRef(i*8)))
}

func SetTableArrayAt(a *arena.Arena, t arena.GCRef, i uint32, v value.Value) {
	arr := TableArrayRef(a, t)
	a.SetWordAt(arr+arena.GCRef(i*8), uint64(v))
}

// 哈希节点访问(idx 0..hmask)。
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
	// next 占低 32 位,高 32 位 reserved。保留高 32 位现状(目前恒 0)。
	a.SetWordAt(base+16, uint64(uint32(next)))
}

// TableWeakMode 返回弱表模式:'k'/'v'/'a'(both)/0(强表)。
//
// 模式缓存在 GCHeader.flags 的 bit1/bit2(setmetatable 时由 SetTableWeakFlags
// 写入——GC 不在 mark 阶段解析 __mode 字符串,06 §8.4 协作纪律)。
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

// SetTableWeakFlags 写弱表模式缓存位(setmetatable 时调用,07 §13)。
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
