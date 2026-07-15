// Package object provides field-level read/write helpers for the six arena-resident
// GC object types defined in docs/design/p1-interpreter/01-value-object-model.md §4-§5.
//
// This stage (M3): provides layout helpers and "manually allocated" constructors.
// The GCHeader color/sweep chain is written by the gc package (M5); here we only
// define the bit layout and field access API, and the allocateRaw caller is
// responsible for setting the word count and otype.
//
// Key invariants (all inherited from the upstream design docs):
//   - the word-count formula matches 06 §1.3;
//   - GCRef offset addressing stays valid after grow (the arena dividend);
//   - "head objects" enter the sweep chain (M5); array/node/valueStack/callInfo
//     are attached blocks that do not enter the chain (06 §1.3);
//   - Table's gen field, Upvalue's open-state nextOpen, Proto's LocVars, and
//     other backfill requests are all honored per 01.
package object

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/value"
)

// OBJType is the enum in GCHeader that identifies the actual type of a "head
// object" (01 §4).
//
// Unlike a value tag: a value tag only distinguishes the 8 non-number types;
// OBJType distinguishes the head objects that CARRY a GCHeader, including
// Upvalue (which has no corresponding value tag, because an upvalue never
// appears directly in a script-visible Value).
type OBJType uint8

const (
	OBJ_NONE     OBJType = 0
	OBJ_STRING   OBJType = 1
	OBJ_TABLE    OBJType = 2
	OBJ_CLOSURE  OBJType = 3
	OBJ_USERDATA OBJType = 4
	OBJ_THREAD   OBJType = 5
	OBJ_PROTO    OBJType = 6 // unused in P1 (Proto lives on the Go heap); enum reserved for a future in-arena Proto
	OBJ_UPVAL    OBJType = 7
)

// GCHeader bit layout (01 §4):
//
//	bits [7:0]    otype     OBJType
//	bits [9:8]    color     0=white0 / 1=white1 / 2=gray / 3=black
//	bits [10]     fixed     1=never collected
//	bits [11]     hasGCNext linked-list flag
//	bits [15:12]  flags     type-private flags (e.g. table's fast bit for whether it has a metatable)
//	bits [63:16]  gcnext    48-bit offset of the next object in the sweep chain (0=chain tail)
const (
	hdrOTypeShift   = 0
	hdrOTypeMask    = uint64(0xFF)
	hdrColorShift   = 8
	hdrColorMask    = uint64(0x3) << hdrColorShift
	hdrFixedBit     = uint64(1) << 10
	hdrHasGCNextBit = uint64(1) << 11
	hdrFlagsShift   = 12
	hdrFlagsMask    = uint64(0xF) << hdrFlagsShift
	hdrGCNextShift  = 16
)

// HeaderOf reads the GCHeader word of a head object.
func HeaderOf(a *arena.Arena, ref arena.GCRef) uint64 { return a.WordAt(ref) }

// SetHeader writes the GCHeader word.
func SetHeader(a *arena.Arena, ref arena.GCRef, h uint64) { a.SetWordAt(ref, h) }

// MakeHeader composes a GCHeader (caller-controlled color/flags/gcnext usually start at 0).
func MakeHeader(otype OBJType, color uint8, fixed bool, flags uint8, gcnext arena.GCRef) uint64 {
	h := uint64(otype) & hdrOTypeMask
	h |= (uint64(color) & 0x3) << hdrColorShift
	if fixed {
		h |= hdrFixedBit
	}
	h |= (uint64(flags) & 0xF) << hdrFlagsShift
	if gcnext != 0 {
		h |= hdrHasGCNextBit | (uint64(gcnext) << hdrGCNextShift)
	}
	return h
}

// Field accessors on a header word.
func OTypeOf(h uint64) OBJType      { return OBJType(h & hdrOTypeMask) }
func ColorOf(h uint64) uint8        { return uint8((h & hdrColorMask) >> hdrColorShift) }
func IsFixed(h uint64) bool         { return h&hdrFixedBit != 0 }
func HasGCNext(h uint64) bool       { return h&hdrHasGCNextBit != 0 }
func FlagsOf(h uint64) uint8        { return uint8((h & hdrFlagsMask) >> hdrFlagsShift) }
func GCNextOf(h uint64) arena.GCRef { return arena.GCRef(h >> hdrGCNextShift) }

// SetColor returns h with the color field replaced.
func SetColor(h uint64, c uint8) uint64 {
	return (h &^ hdrColorMask) | ((uint64(c) & 0x3) << hdrColorShift)
}

// SetFlags returns h with the flags field replaced.
func SetFlags(h uint64, f uint8) uint64 {
	return (h &^ hdrFlagsMask) | ((uint64(f) & 0xF) << hdrFlagsShift)
}

// SetGCNext returns h with the gcnext field replaced (and HasGCNext bit toggled).
func SetGCNext(h uint64, ref arena.GCRef) uint64 {
	h &^= (uint64(0xFFFFFFFFFFFF) << hdrGCNextShift) | hdrHasGCNextBit
	if ref != 0 {
		h |= hdrHasGCNextBit | (uint64(ref) << hdrGCNextShift)
	}
	return h
}

// Color constants (01 §4.2 / 06 §4.2).
const (
	ColorWhite0 = 0
	ColorWhite1 = 1
	ColorGray   = 2
	ColorBlack  = 3
)

// allocateRaw allocates a head object, writes the initial GCHeader, and returns
// its GCRef.
//
// M3 stage: exposed for use by same-package constructors; color defaults to
// white0 and gcnext=0 (once the M5 GC is wired in, the collector rewrites these
// and links the object into the sweep chain). flags are defined by the caller
// per the object type's semantics.
func allocateRaw(a *arena.Arena, otype OBJType, words uint32, flags uint8) arena.GCRef {
	ref := a.AllocWords(words)
	SetHeader(a, ref, MakeHeader(otype, ColorWhite0, false, flags, 0))
	return ref
}

// Field read/write helpers: turn a given word offset within an object into an
// arena GCRef and read/write it.
func wordAt(a *arena.Arena, ref arena.GCRef, idx uint32) uint64 {
	return a.WordAt(ref + arena.GCRef(idx*8))
}

func setWordAt(a *arena.Arena, ref arena.GCRef, idx uint32, v uint64) {
	a.SetWordAt(ref+arena.GCRef(idx*8), v)
}

// valueAt / setValueAt: used to read/write NaN-boxed Value fields.
func valueAt(a *arena.Arena, ref arena.GCRef, idx uint32) value.Value {
	return value.Value(wordAt(a, ref, idx))
}

func setValueAt(a *arena.Arena, ref arena.GCRef, idx uint32, v value.Value) {
	setWordAt(a, ref, idx, uint64(v))
}
