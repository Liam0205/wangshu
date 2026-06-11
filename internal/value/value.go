// Package value implements Lua's NaN-boxed Value, GCRef encoding, and tag predicates.
//
// 设计:docs/design/p1-interpreter/01-value-object-model.md §3。
//
// Value 是一个 uint64,利用 IEEE-754 double 的 NaN 空间编码非数字类型:
//   - 数字:整个 64-bit 是 IEEE-754 double(IsNumber ⟺ v < 0xFFF8_0000_0000_0000)。
//   - 非数字 boxed:高 16 bit 是 tag(0xFFF8..0xFFFF),低 48 bit 是 payload。
//
// 不变式(贯穿全 VM):
//   - NaN 规范化:值世界中任何 NaN 数字必须是 0x7FF8_0000_0000_0000(canonNaN)。
//     入口 NumberValue 兜底:NaN 一律规范成 canonNaN,防外部负 NaN 渗入(否则会被误判 boxed)。
//   - 8 个 tag 用满 0xFFF8..0xFFFF,Lua 5.1 类型集封闭(无空槽是设计意图)。
//   - 可回收类型 = tag ∈ [TagString, TagThread] (即 0xFFFB..0xFFFF) — IsCollectable 单比较。
package value

import (
	"math"

	"github.com/Liam0205/wangshu/internal/arena"
)

// Value is a 64-bit NaN-boxed Lua value.
type Value uint64

// 边界 / payload 掩码 / canonical NaN(01 §3.5)。
const (
	// qNanBoxBase 是非数字 boxed 段的下界:v < qNanBoxBase ⟺ v 是数字。
	qNanBoxBase uint64 = 0xFFF8_0000_0000_0000
	payloadMask uint64 = 0x0000_FFFF_FFFF_FFFF
	// canonNaN 是值世界唯一允许的 NaN bits(IEEE 正 quiet NaN)。
	canonNaN uint64 = 0x7FF8_0000_0000_0000
)

// 8 个非数字 tag,用满 0xFFF8..0xFFFF。
const (
	TagNil      uint16 = 0xFFF8
	TagBool     uint16 = 0xFFF9
	TagLightUD  uint16 = 0xFFFA
	TagString   uint16 = 0xFFFB
	TagTable    uint16 = 0xFFFC
	TagFunction uint16 = 0xFFFD
	TagUserdata uint16 = 0xFFFE
	TagThread   uint16 = 0xFFFF

	collectableMin uint16 = TagString // 可回收下限,见 IsCollectable
)

// 常量值。
const (
	Nil   Value = Value(uint64(TagNil) << 48)    // 0xFFF8_0000_0000_0000
	False Value = Value(uint64(TagBool) << 48)   // 0xFFF9_0000_0000_0000
	True  Value = Value(uint64(TagBool)<<48 | 1) // 0xFFF9_0000_0000_0001
)

// IsNumber reports whether v encodes a number (single uint64 comparison).
func IsNumber(v Value) bool { return uint64(v) < qNanBoxBase }

// IsCollectable reports whether v references an arena GC object (single 16-bit comparison).
// 等价于 tag ∈ [TagString, TagThread] = [0xFFFB, 0xFFFF]。
func IsCollectable(v Value) bool {
	return uint64(v) >= uint64(collectableMin)<<48
}

// Tag returns the 16-bit tag (only valid when !IsNumber).
func Tag(v Value) uint16 { return uint16(uint64(v) >> 48) }

// Truthy implements Lua truth: only nil and false are false; 0/""/NaN are true.
func Truthy(v Value) bool { return v != Nil && v != False }

// NumberValue boxes a float64 with NaN canonicalization (01 §3.4).
//
// 任何 NaN(包括外部负 NaN、0/0、Inf-Inf 产生的实现 NaN)都被规范成 canonNaN。
// 这一处兜底使整个值世界的 NaN bits 唯一,IsNumber 边界判定可信。
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
// 仅当 v 是 boolean 时有意义:TagBool 的 payload 0 = false,1 = true。
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
// handle 必须 ≤ payloadMask;高 16 bit 会被截断(01 §3.5 lightuserdata 限制)。
func LightUDValue(handle uint64) Value {
	return Value(uint64(TagLightUD)<<48 | handle&payloadMask)
}

// AsLightUD extracts the 48-bit handle from a lightuserdata Value.
func AsLightUD(v Value) uint64 { return uint64(v) & payloadMask }

// CanonNaN returns the canonical NaN bits (exposed for diagnostics / tests).
func CanonNaN() uint64 { return canonNaN }
