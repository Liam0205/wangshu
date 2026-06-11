// String 对象布局(01 §5.1):
//
//	word0: GCHeader (otype=STRING)
//	word1: [31:0] hash32 | [63:32] len(字节长)
//	word2..: 内容字节,按 8 字节向上对齐填充;末尾补 1 个 NUL(便于 C 互操作,不计入 len)
package object

import (
	"github.com/Liam0205/wangshu/internal/arena"
)

// 字段索引(以字为单位)。
const (
	strHashLenIdx = 1 // word1
	strDataIdx    = 2 // word2..
)

// stringWords returns the total word count (header + 1 hash/len word + content/padding).
func stringWords(byteLen uint32) uint32 {
	// content + 1 NUL byte,向上对齐到字。
	contentBytes := byteLen + 1
	contentWords := (contentBytes + 7) / 8
	return 2 + contentWords
}

// AllocString allocates a String object holding the given bytes. The caller is responsible
// for interning policy (06 §9):this helper merely places content and computes hash slot.
//
// hash 必须由调用方算好(JSHash;06 §9.3 单一事实源)并传入,这里不重复实现散列算法。
func AllocString(a *arena.Arena, b []byte, hash32 uint32) arena.GCRef {
	if len(b) > 0xFFFFFFFF {
		panic("object: string too long")
	}
	ref := allocateRaw(a, OBJ_STRING, stringWords(uint32(len(b))), 0)
	setWordAt(a, ref, strHashLenIdx, uint64(hash32)|uint64(uint32(len(b)))<<32)
	if len(b) > 0 {
		// 通过字节视图写入内容;末尾的对齐填充字节天然是零,NUL 终止天然成立。
		dst := a.Bytes()[uint32(ref)+strDataIdx*8:]
		copy(dst, b)
	}
	return ref
}

// StringHash returns the hash32 stored in the String header.
func StringHash(a *arena.Arena, ref arena.GCRef) uint32 {
	w := wordAt(a, ref, strHashLenIdx)
	return uint32(w)
}

// StringLen returns the byte length of the string.
func StringLen(a *arena.Arena, ref arena.GCRef) uint32 {
	w := wordAt(a, ref, strHashLenIdx)
	return uint32(w >> 32)
}

// StringBytes returns a slice aliasing the string content (no copy; caller must not mutate
// across allocations that may grow the arena).
func StringBytes(a *arena.Arena, ref arena.GCRef) []byte {
	n := StringLen(a, ref)
	if n == 0 {
		return nil
	}
	off := uint32(ref) + strDataIdx*8
	return a.Bytes()[off : off+n]
}

// StringEqual reports byte-equal content of two String objects.
func StringEqual(a *arena.Arena, x, y arena.GCRef) bool {
	if x == y {
		return true
	}
	if StringLen(a, x) != StringLen(a, y) {
		return false
	}
	xb := StringBytes(a, x)
	yb := StringBytes(a, y)
	for i := range xb {
		if xb[i] != yb[i] {
			return false
		}
	}
	return true
}
