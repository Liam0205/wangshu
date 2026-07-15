// String object layout (01 §5.1):
//
//	word0: GCHeader (otype=STRING)
//	word1: [31:0] hash32 | [63:32] len (byte length)
//	word2..: content bytes, padded up to 8-byte alignment; a trailing NUL is appended (for C interop, not counted in len)
package object

import (
	"github.com/Liam0205/wangshu/internal/arena"
)

// Field indices (in words).
const (
	strHashLenIdx = 1 // word1
	strDataIdx    = 2 // word2..
)

// stringWords returns the total word count (header + 1 hash/len word + content/padding).
func stringWords(byteLen uint32) uint32 {
	// content + 1 NUL byte, rounded up to a word.
	contentBytes := byteLen + 1
	contentWords := (contentBytes + 7) / 8
	return 2 + contentWords
}

// maxStrBytes is the upper bound on String content length: it reserves the 2-word header + 1 NUL + alignment slack,
// ensuring the byteLen+1 and word-count multiplication in stringWords do not wrap around (size-entry validation convention).
const maxStrBytes = uint64(arena.MaxBytes) - 64

// AllocString allocates a String object holding the given bytes. The caller is responsible
// for interning policy (06 §9): this helper merely places content and computes hash slot.
//
// hash must be computed by the caller (JSHash; 06 §9.3 single source of truth) and passed in;
// the hashing algorithm is not reimplemented here.
func AllocString(a *arena.Arena, b []byte, hash32 uint32) arena.GCRef {
	// uint64-domain comparison: ① on 32-bit GOARCH, len(b) is a 32-bit int, and the
	// untyped constant 0xFFFFFFFF overflows int, failing compilation; ② when len == 0xFFFFFFFF,
	// byteLen+1 wraps to 0 → only 2 words are allocated while len records an oversized value,
	// making StringBytes go out of bounds (off-by-one).
	if uint64(len(b)) > maxStrBytes {
		panic("object: string too long")
	}
	ref := allocateRaw(a, OBJ_STRING, stringWords(uint32(len(b))), 0)
	setWordAt(a, ref, strHashLenIdx, uint64(hash32)|uint64(uint32(len(b)))<<32)
	if len(b) > 0 {
		// Write content through the byte view; the block tail is already zeroed by AllocBytes, so NUL termination holds.
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
