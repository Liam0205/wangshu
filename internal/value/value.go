// Package value implements Lua's NaN-boxed Value, GCRef encoding, and tag predicates.
//
// Design: docs/design/p1-interpreter/01-value-object-model.md §3.
//
// Value is a uint64 that uses the NaN space of an IEEE-754 double to encode
// non-number types:
//   - number: the entire 64 bits is an IEEE-754 double
//     (IsNumber ⟺ v < 0xFFF8_0000_0000_0000).
//   - boxed non-number: the high 16 bits are the tag (0xFFF8..0xFFFF), the low
//     48 bits are the payload.
//
// Invariants (hold throughout the VM):
//   - NaN canonicalization: any NaN number in the value world must be
//     0x7FF8_0000_0000_0000 (canonNaN). The NumberValue entry point enforces
//     this: every NaN is canonicalized to canonNaN, preventing an external
//     negative NaN from leaking in (otherwise it would be misread as boxed).
//   - The 8 tags fill 0xFFF8..0xFFFF; the Lua 5.1 type set is closed (no empty
//     slot, by design).
//   - Collectable type = tag ∈ [TagString, TagThread] (i.e. 0xFFFB..0xFFFF) —
//     IsCollectable is a single comparison.
package value

import (
	"math"

	"github.com/Liam0205/wangshu/internal/arena"
)

// Value is a 64-bit NaN-boxed Lua value.
type Value uint64

// Boundary / payload mask / canonical NaN (01 §3.5).
const (
	// qNanBoxBase is the lower bound of the boxed non-number range:
	// v < qNanBoxBase ⟺ v is a number.
	qNanBoxBase uint64 = 0xFFF8_0000_0000_0000
	payloadMask uint64 = 0x0000_FFFF_FFFF_FFFF
	// canonNaN is the only NaN bits allowed in the value world (IEEE positive quiet NaN).
	canonNaN uint64 = 0x7FF8_0000_0000_0000
)

// The 8 non-number tags, filling 0xFFF8..0xFFFF.
const (
	TagNil      uint16 = 0xFFF8
	TagBool     uint16 = 0xFFF9
	TagLightUD  uint16 = 0xFFFA
	TagString   uint16 = 0xFFFB
	TagTable    uint16 = 0xFFFC
	TagFunction uint16 = 0xFFFD
	TagUserdata uint16 = 0xFFFE
	TagThread   uint16 = 0xFFFF

	collectableMin uint16 = TagString // collectable lower bound, see IsCollectable
)

// Constant values.
const (
	Nil   Value = Value(uint64(TagNil) << 48)    // 0xFFF8_0000_0000_0000
	False Value = Value(uint64(TagBool) << 48)   // 0xFFF9_0000_0000_0000
	True  Value = Value(uint64(TagBool)<<48 | 1) // 0xFFF9_0000_0000_0001
)

// IsNumber reports whether v encodes a number (single uint64 comparison).
func IsNumber(v Value) bool { return uint64(v) < qNanBoxBase }

// IsCollectable reports whether v references an arena GC object (single 16-bit comparison).
// Equivalent to tag ∈ [TagString, TagThread] = [0xFFFB, 0xFFFF].
func IsCollectable(v Value) bool {
	return uint64(v) >= uint64(collectableMin)<<48
}

// Tag returns the 16-bit tag (only valid when !IsNumber).
func Tag(v Value) uint16 { return uint16(uint64(v) >> 48) }

// Truthy implements Lua truth: only nil and false are false; 0/""/NaN are true.
func Truthy(v Value) bool { return v != Nil && v != False }

// NumberValue boxes a float64 with NaN canonicalization (01 §3.4).
//
// Any NaN (including an external negative NaN, or an implementation NaN produced
// by 0/0 or Inf-Inf) is canonicalized to canonNaN. This one fallback makes the
// NaN bits unique across the whole value world, so the IsNumber boundary test is
// trustworthy.
func NumberValue(f float64) Value {
	if f != f { // NaN
		return Value(canonNaN)
	}
	return Value(math.Float64bits(f))
}

// AsNumber extracts the float64 from a number Value. Caller must ensure IsNumber(v).
func AsNumber(v Value) float64 { return math.Float64frombits(uint64(v)) }

// BoolValue boxes a Go bool.
func BoolValue(b bool) Value {
	if b {
		return True
	}
	return False
}

// AsBool extracts the bool payload (caller ensures Tag(v) == TagBool).
// Only meaningful when v is a boolean: a TagBool payload of 0 = false, 1 = true.
func AsBool(v Value) bool { return uint64(v)&1 == 1 }

// MakeGC packages a (tag, GCRef) into a Value. Caller ensures tag is collectable
// (TagString..TagThread) and ref ≤ payloadMask.
func MakeGC(tag uint16, ref arena.GCRef) Value {
	return Value(uint64(tag)<<48 | uint64(ref)&payloadMask)
}

// GCRefOf extracts the arena reference from a collectable Value.
// Caller ensures IsCollectable(v).
func GCRefOf(v Value) arena.GCRef { return arena.GCRef(uint64(v) & payloadMask) }

// LightUDValue boxes a 48-bit opaque handle as lightuserdata.
// handle must be ≤ payloadMask; the high 16 bits are truncated
// (01 §3.5 lightuserdata restriction).
func LightUDValue(handle uint64) Value {
	return Value(uint64(TagLightUD)<<48 | handle&payloadMask)
}

// AsLightUD extracts the 48-bit handle from a lightuserdata Value.
func AsLightUD(v Value) uint64 { return uint64(v) & payloadMask }

// CanonNaN returns the canonical NaN bits (exposed for diagnostics / tests).
func CanonNaN() uint64 { return canonNaN }
