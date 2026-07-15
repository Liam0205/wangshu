// Closure (Lua and Host) + Upvalue object layout (01 §5.3 / §5.4).
//
// Lua closure:
//
//	word0: GCHeader (otype=CLOSURE; flags bit0=0)
//	word1: [31:0] protoID | [47:32] nupvals | [63:48] gibbousSlot+1 (0=unfilled, PW10 zero-cross)
//	word2..: upvalRef[nupvals]  (each GCRef→ Upvalue object)
//
// Host closure:
//
//	word0: GCHeader (otype=CLOSURE; flags bit0=1)
//	word1: [31:0] hostFnID | [47:32] nupvals
//	word2..: upval[nupvals]  (Value, captured directly; not an Upvalue object)
//
// Upvalue (open/closed two states; flags bit0 = 0 open / 1 closed):
//
//	word0: GCHeader (otype=UPVAL)
//	word1: open: [31:0] stackIdx | [63:32] low 32 bits of threadRef;  closed: unused
//	word2: open: nextOpen (GCRef→ next node in the descending chain);  closed: value (Value self-held)
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

	// closureSlotShift is the shift for the gibbous slot cached in closure word1
	// ([63:48] reserved region, PW10 zero-cross). Stores slot+1 (0=unfilled); the
	// Wasm-side emitCall reads these 16 bits to avoid a Go map lookup
	// (bridge.GibbousCodeOf+Slot). Mask 0xffff, so slot ≤ 65534.
	closureSlotShift uint   = 48
	closureSlotMask  uint64 = 0xffff << closureSlotShift
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

// ClosureGibbousSlot returns the cached gibbous table slot (PW10 zero-cross lazily
// filled IC). Returns (slot, true) if filled; (0, false) if unfilled (not promoted /
// before the first call / host closure). The Wasm-side emitCall reads the high 16
// bits of closure word1 == this encoding, avoiding a Go map.
func ClosureGibbousSlot(a *arena.Arena, c arena.GCRef) (uint32, bool) {
	enc := uint32(wordAt(a, c, closureMetaIdx) >> closureSlotShift)
	if enc == 0 {
		return 0, false
	}
	return enc - 1, true
}

// SetClosureGibbousSlot caches the gibbous slot into the high 16 bits of closure
// word1 (stores slot+1, 0=unfilled). tryIndirectCallee writes it back after the
// first Go-map lookup finds the slot; it only touches the high 16 bits, keeping
// protoID ([31:0]) / nupvals ([47:32]) unchanged. If slot exceeds 0xfffe
// (impossible, maxTableSlots=8192) it is not cached (returns the value unchanged),
// so Wasm always takes the fallback and correctness is not broken.
func SetClosureGibbousSlot(a *arena.Arena, c arena.GCRef, slot uint32) {
	if slot >= 0xffff {
		return // exceeds the 16-bit encoding domain (unreachable in theory); do not cache, Wasm falls back to h_call
	}
	w := wordAt(a, c, closureMetaIdx)
	w = (w &^ closureSlotMask) | (uint64(slot+1) << closureSlotShift)
	setWordAt(a, c, closureMetaIdx, w)
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
	// word1: stackIdx (low 32) | threadRef low 32 (high 32) - under offset addressing threadRef is usually ≤ 4GiB, low 32 bits suffice
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
// The arena is capped at 2 GiB (MaxBytes), so a GCRef is effectively ≤ 31 bits and the low 32 bits restore it losslessly.
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
