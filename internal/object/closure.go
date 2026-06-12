// Closure (Lua 与 Host)、Upvalue 对象布局(01 §5.3 / §5.4)。
//
// Lua 闭包:
//
//	word0: GCHeader (otype=CLOSURE; flags bit0=0)
//	word1: [31:0] protoID | [47:32] nupvals | [63:48] reserved
//	word2..: upvalRef[nupvals]  (各 GCRef→ Upvalue 对象)
//
// Host 闭包:
//
//	word0: GCHeader (otype=CLOSURE; flags bit0=1)
//	word1: [31:0] hostFnID | [47:32] nupvals
//	word2..: upval[nupvals]  (Value,直接捕获;非 Upvalue 对象)
//
// Upvalue(开放/关闭两态;flags bit0 = 0 开放 / 1 关闭):
//
//	word0: GCHeader (otype=UPVAL)
//	word1: 开放: [31:0] stackIdx | [63:32] threadRef 低 32 位;  关闭: 未用
//	word2: 开放: nextOpen(GCRef→ 降序链下一节点);              关闭: value (Value 自持值)
package object

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/value"
)

const (
	closureMetaIdx = 1 // word1
	closureSlotIdx = 2 // word2..
	upvalLocIdx    = 1 // word1
	upvalValueIdx  = 2 // word2

	closureFlagHost uint8 = 1 << 0
	upvalFlagClosed uint8 = 1 << 0
)

// AllocLuaClosure allocates a Lua closure with `nupvals` upvalue slots, all 0-initialized.
func AllocLuaClosure(a *arena.Arena, protoID uint32, nupvals uint16) arena.GCRef {
	words := uint32(2 + uint32(nupvals))
	ref := allocateRaw(a, OBJ_CLOSURE, words, 0)
	setWordAt(a, ref, closureMetaIdx, uint64(protoID)|uint64(nupvals)<<32)
	for i := uint32(0); i < uint32(nupvals); i++ {
		setWordAt(a, ref, closureSlotIdx+i, 0)
	}
	return ref
}

// AllocHostClosure allocates a host closure capturing nupvals direct Value upvalues.
func AllocHostClosure(a *arena.Arena, hostFnID uint32, nupvals uint16) arena.GCRef {
	words := uint32(2 + uint32(nupvals))
	ref := allocateRaw(a, OBJ_CLOSURE, words, closureFlagHost)
	setWordAt(a, ref, closureMetaIdx, uint64(hostFnID)|uint64(nupvals)<<32)
	for i := uint32(0); i < uint32(nupvals); i++ {
		setValueAt(a, ref, closureSlotIdx+i, value.Nil)
	}
	return ref
}

// IsHostClosure reports whether the closure is a host closure.
func IsHostClosure(a *arena.Arena, c arena.GCRef) bool {
	return FlagsOf(HeaderOf(a, c))&closureFlagHost != 0
}

// ClosureProtoID returns the embedded protoID (Lua closure) or hostFnID (host closure).
func ClosureProtoID(a *arena.Arena, c arena.GCRef) uint32 {
	return uint32(wordAt(a, c, closureMetaIdx))
}

// ClosureNUpvals returns the upvalue count.
func ClosureNUpvals(a *arena.Arena, c arena.GCRef) uint16 {
	return uint16(wordAt(a, c, closureMetaIdx) >> 32)
}

// ClosureUpvalRef returns the GCRef of the i-th Upvalue (Lua closure only).
func ClosureUpvalRef(a *arena.Arena, c arena.GCRef, i uint16) arena.GCRef {
	return arena.GCRef(wordAt(a, c, closureSlotIdx+uint32(i)))
}

// SetClosureUpvalRef sets the i-th Upvalue ref (Lua closure).
func SetClosureUpvalRef(a *arena.Arena, c arena.GCRef, i uint16, uv arena.GCRef) {
	setWordAt(a, c, closureSlotIdx+uint32(i), uint64(uv))
}

// HostClosureUpval / SetHostClosureUpval: direct Value upvalues for host closures.
func HostClosureUpval(a *arena.Arena, c arena.GCRef, i uint16) value.Value {
	return valueAt(a, c, closureSlotIdx+uint32(i))
}

func SetHostClosureUpval(a *arena.Arena, c arena.GCRef, i uint16, v value.Value) {
	setValueAt(a, c, closureSlotIdx+uint32(i), v)
}

// AllocOpenUpvalue allocates an open upvalue pointing to (threadRef, stackIdx).
// nextOpen is the head of the thread's open-upvalue descending chain (or 0 for tail).
func AllocOpenUpvalue(a *arena.Arena, threadRef arena.GCRef, stackIdx uint32, nextOpen arena.GCRef) arena.GCRef {
	ref := allocateRaw(a, OBJ_UPVAL, 3, 0) // flags = 0 (open)
	// word1: stackIdx (low 32) | threadRef low 32 (high 32) - 偏移寻址下 threadRef 通常 ≤ 4GiB,低 32 位足够
	setWordAt(a, ref, upvalLocIdx, uint64(stackIdx)|uint64(uint32(threadRef))<<32)
	setWordAt(a, ref, upvalValueIdx, uint64(nextOpen))
	return ref
}

// AllocClosedUpvalue allocates an upvalue in closed state holding the given value.
func AllocClosedUpvalue(a *arena.Arena, v value.Value) arena.GCRef {
	ref := allocateRaw(a, OBJ_UPVAL, 3, upvalFlagClosed)
	setWordAt(a, ref, upvalLocIdx, 0)
	setValueAt(a, ref, upvalValueIdx, v)
	return ref
}

// UpvalIsClosed reports whether the upvalue is in closed state.
func UpvalIsClosed(a *arena.Arena, uv arena.GCRef) bool {
	return FlagsOf(HeaderOf(a, uv))&upvalFlagClosed != 0
}

// UpvalStackIdx returns the (open) upvalue's referenced stack index.
func UpvalStackIdx(a *arena.Arena, uv arena.GCRef) uint32 {
	return uint32(wordAt(a, uv, upvalLocIdx))
}

// UpvalThreadRefLo returns the low 32 bits of the thread reference (open state).
// arena 上限 2 GiB(MaxBytes),GCRef 实际 ≤ 31 bit,低 32 位无损还原。
func UpvalThreadRefLo(a *arena.Arena, uv arena.GCRef) uint32 {
	return uint32(wordAt(a, uv, upvalLocIdx) >> 32)
}

// UpvalNextOpen returns the next open upvalue in the descending chain (open state only).
func UpvalNextOpen(a *arena.Arena, uv arena.GCRef) arena.GCRef {
	return arena.GCRef(wordAt(a, uv, upvalValueIdx))
}

// SetUpvalNextOpen rewires the open chain.
func SetUpvalNextOpen(a *arena.Arena, uv arena.GCRef, next arena.GCRef) {
	setWordAt(a, uv, upvalValueIdx, uint64(next))
}

// UpvalClosedValue returns the self-held value of a closed upvalue.
func UpvalClosedValue(a *arena.Arena, uv arena.GCRef) value.Value {
	return valueAt(a, uv, upvalValueIdx)
}

// CloseUpvalue copies the current stack-slot value into word2 and flips to closed state.
// Caller is responsible for unlinking from the thread's open chain before / after.
func CloseUpvalue(a *arena.Arena, uv arena.GCRef, current value.Value) {
	setWordAt(a, uv, upvalLocIdx, 0)
	setValueAt(a, uv, upvalValueIdx, current)
	h := HeaderOf(a, uv)
	SetHeader(a, uv, SetFlags(h, FlagsOf(h)|upvalFlagClosed))
}
