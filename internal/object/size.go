// SizeOf — the single source of truth for the byte sizes of the six header
// object kinds.
//
// The gc package previously held four separate hand-written formulas (Intern
// accounting / objectBytes stats / freeObject release). A one-word size error
// in freeObject = wrong freelist bucket = overlapping memory of adjacent
// objects on reuse (UAF-level, with no detection in production mode). Layout
// changes touch this file only.
package object

import "github.com/Liam0205/wangshu/internal/arena"

// StringObjectBytes returns the total bytes of a String object (2-word header +
// content + NUL, 8-aligned).
func StringObjectBytes(byteLen uint32) uint32 {
	return stringWords(byteLen) * 8
}

// TableHeadBytes returns the bytes of a Table header object (6 words; the
// array/node sub-blocks are counted separately).
func TableHeadBytes() uint32 { return tableHeadWords * 8 }

// TableArrayBytes / TableNodeBytes return the bytes of the sub-blocks.
func TableArrayBytes(asize uint32) uint32 { return asize * 8 }
func TableNodeBytes(hsize uint32) uint32  { return hsize * nodeWords * 8 }

// ClosureBytes returns the bytes of a closure object (2-word header + nupvals
// slots).
func ClosureBytes(nupvals uint16) uint32 { return (2 + uint32(nupvals)) * 8 }

// UserdataBytes returns the bytes of a userdata object (4-word header + payload,
// 8-aligned).
func UserdataBytes(payloadLen uint32) uint32 {
	return userdataWords(payloadLen) * 8
}

// ThreadHeadBytes / ThreadStackBytes / ThreadCIBytes return the bytes of the
// Thread header and its sub-blocks.
func ThreadHeadBytes() uint32                 { return threadHeadWords * 8 }
func ThreadStackBytes(stackCap uint32) uint32 { return stackCap * 8 }
func ThreadCIBytes(ciCap uint32) uint32       { return ciCap * 4 * 8 }

// UpvalueBytes returns the bytes of an Upvalue object (3 words).
func UpvalueBytes() uint32 { return 3 * 8 }

// SizeOf returns the byte count of the header object itself (excluding the
// sub-blocks of Table/Thread—those are queried separately by the caller as
// needed, because the accounting view (pacing estimate) and the release view
// (block-by-block return) differ).
func SizeOf(a *arena.Arena, ref arena.GCRef, ot OBJType) uint32 {
	switch ot {
	case OBJ_STRING:
		return StringObjectBytes(StringLen(a, ref))
	case OBJ_TABLE:
		return TableHeadBytes()
	case OBJ_CLOSURE:
		return ClosureBytes(ClosureNUpvals(a, ref))
	case OBJ_USERDATA:
		return UserdataBytes(UserdataLen(a, ref))
	case OBJ_THREAD:
		return ThreadHeadBytes()
	case OBJ_UPVAL:
		return UpvalueBytes()
	}
	return 0
}
