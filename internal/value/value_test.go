package value

import (
	"math"
	"testing"

	"github.com/Liam0205/wangshu/internal/arena"
)

func TestConstantsBitPattern(t *testing.T) {
	if uint64(Nil) != 0xFFF8_0000_0000_0000 {
		t.Errorf("Nil = %#x, want 0xFFF8_0000_0000_0000", uint64(Nil))
	}
	if uint64(False) != 0xFFF9_0000_0000_0000 {
		t.Errorf("False = %#x", uint64(False))
	}
	if uint64(True) != 0xFFF9_0000_0000_0001 {
		t.Errorf("True = %#x", uint64(True))
	}
	if canonNaN != 0x7FF8_0000_0000_0000 {
		t.Errorf("canonNaN = %#x", canonNaN)
	}
}

func TestIsNumberBoundary(t *testing.T) {
	cases := []struct {
		name   string
		bits   uint64
		number bool
	}{
		{"+0", math.Float64bits(0), true},
		{"-0", math.Float64bits(math.Copysign(0, -1)), true},
		{"+1", math.Float64bits(1), true},
		{"-1", math.Float64bits(-1), true},
		{"+Inf", math.Float64bits(math.Inf(1)), true},
		{"-Inf", math.Float64bits(math.Inf(-1)), true},
		{"+qNaN", canonNaN, true}, // 0x7FF8_..
		{"max-finite", math.Float64bits(math.MaxFloat64), true},
		{"just below qNanBoxBase", qNanBoxBase - 1, true},
		{"qNanBoxBase exact", qNanBoxBase, false}, // tag TagNil
		{"Nil", uint64(Nil), false},
		{"True", uint64(True), false},
		{"TagThread max payload", uint64(TagThread)<<48 | payloadMask, false},
	}
	for _, c := range cases {
		if got := IsNumber(Value(c.bits)); got != c.number {
			t.Errorf("%s (bits=%#x): IsNumber=%v, want %v", c.name, c.bits, got, c.number)
		}
	}
}

func TestIsCollectable(t *testing.T) {
	cases := []struct {
		v           Value
		collectable bool
	}{
		{Nil, false},
		{False, false},
		{True, false},
		{LightUDValue(0xDEAD), false},
		{NumberValue(42), false},
		{NumberValue(math.NaN()), false}, // canonical NaN 仍是 number
		{MakeGC(TagString, arena.GCRef(8)), true},
		{MakeGC(TagTable, arena.GCRef(16)), true},
		{MakeGC(TagFunction, arena.GCRef(24)), true},
		{MakeGC(TagUserdata, arena.GCRef(32)), true},
		{MakeGC(TagThread, arena.GCRef(40)), true},
	}
	for _, c := range cases {
		if got := IsCollectable(c.v); got != c.collectable {
			t.Errorf("IsCollectable(%#x) = %v, want %v", uint64(c.v), got, c.collectable)
		}
	}
}

func TestNaNCanonicalization(t *testing.T) {
	// 任何 NaN(含负 NaN、quiet/signaling、非规范 mantissa)经 NumberValue 都得到 canonNaN。
	nans := []float64{
		math.NaN(),
		math.Float64frombits(0xFFF8_0000_0000_0001), // 负 quiet NaN(与 boxed tag 段重叠!)
		math.Float64frombits(0x7FFF_FFFF_FFFF_FFFF), // 各种 NaN bits
		math.Float64frombits(0xFFFF_FFFF_FFFF_FFFF),
		runtimeNaN(),     // Inf - Inf,运行期产生
		runtimeZeroDiv(), // 0/0,运行期产生(避开编译期常量折叠)
	}
	for i, f := range nans {
		v := NumberValue(f)
		if uint64(v) != canonNaN {
			t.Errorf("nan #%d: NumberValue bits = %#x, want %#x", i, uint64(v), canonNaN)
		}
		if !IsNumber(v) {
			t.Errorf("nan #%d: not classified as number", i)
		}
	}
}

func TestNumberRoundTrip(t *testing.T) {
	cases := []float64{0, 1, -1, 0.5, -0.5, 1e308, math.Pi, math.SmallestNonzeroFloat64, math.MaxFloat64,
		math.Inf(1), math.Inf(-1)}
	for _, f := range cases {
		v := NumberValue(f)
		if !IsNumber(v) {
			t.Errorf("%v not a number", f)
		}
		got := AsNumber(v)
		if got != f {
			// 唯一例外是 NaN(此用例不含)。
			t.Errorf("round trip %v -> %v", f, got)
		}
	}
}

func TestBoolRoundTrip(t *testing.T) {
	if !AsBool(BoolValue(true)) || AsBool(BoolValue(false)) {
		t.Errorf("bool round trip broken")
	}
	if Tag(True) != TagBool || Tag(False) != TagBool {
		t.Errorf("bool tag broken")
	}
}

func TestTruthy(t *testing.T) {
	cases := []struct {
		v    Value
		want bool
	}{
		{Nil, false},
		{False, false},
		{True, true},
		{NumberValue(0), true},          // Lua: 0 真
		{NumberValue(math.NaN()), true}, // Lua: NaN 真
		{LightUDValue(0), true},
		{MakeGC(TagString, arena.GCRef(8)), true},
	}
	for _, c := range cases {
		if got := Truthy(c.v); got != c.want {
			t.Errorf("Truthy(%#x) = %v, want %v", uint64(c.v), got, c.want)
		}
	}
}

func TestGCRoundTrip(t *testing.T) {
	for _, tag := range []uint16{TagString, TagTable, TagFunction, TagUserdata, TagThread} {
		for _, off := range []uint64{8, 16, 4096, payloadMask & ^uint64(7)} { // 8-aligned offsets
			ref := arena.GCRef(off)
			v := MakeGC(tag, ref)
			if Tag(v) != tag {
				t.Errorf("tag round trip: tag=%#x off=%d, got tag=%#x", tag, off, Tag(v))
			}
			if GCRefOf(v) != ref {
				t.Errorf("ref round trip: tag=%#x off=%d, got ref=%d", tag, off, GCRefOf(v))
			}
			if !IsCollectable(v) {
				t.Errorf("tag %#x not collectable", tag)
			}
		}
	}
}

func TestLightUDPayload(t *testing.T) {
	// 48-bit payload 截断:高 16 bit 被丢弃(01 §3.5)。
	v := LightUDValue(0xDEAD_DEAD_BEEF_CAFE)
	if Tag(v) != TagLightUD {
		t.Errorf("light ud tag = %#x", Tag(v))
	}
	if got := AsLightUD(v); got != 0xDEAD_BEEF_CAFE {
		t.Errorf("light ud payload = %#x, want 0xDEADBEEFCAFE", got)
	}
}

func TestTagFullCoverage(t *testing.T) {
	// 8 个非数字 tag 用满 0xFFF8..0xFFFF(01 §3.3 不变式)。
	tags := []uint16{TagNil, TagBool, TagLightUD, TagString, TagTable, TagFunction, TagUserdata, TagThread}
	for i, tag := range tags {
		want := uint16(0xFFF8 + i)
		if tag != want {
			t.Errorf("tag #%d = %#x, want %#x", i, tag, want)
		}
	}
}

// 通过 noinline 函数让编译器不把表达式折成常量。
//
//go:noinline
func runtimeNaN() float64 {
	a, b := math.Inf(1), math.Inf(1)
	return a - b
}

//go:noinline
func runtimeZeroDiv() float64 {
	a, b := 0.0, 0.0
	return a / b
}
