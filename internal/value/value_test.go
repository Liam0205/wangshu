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
		{NumberValue(math.NaN()), false}, // canonical NaN is still a number
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
	// Any NaN (negative NaN, quiet/signaling, non-canonical mantissa) becomes canonNaN via NumberValue.
	nans := []float64{
		math.NaN(),
		math.Float64frombits(0xFFF8_0000_0000_0001), // negative quiet NaN (overlaps the boxed tag range!)
		math.Float64frombits(0x7FFF_FFFF_FFFF_FFFF), // assorted NaN bits
		math.Float64frombits(0xFFFF_FFFF_FFFF_FFFF),
		runtimeNaN(),     // Inf - Inf, produced at runtime
		runtimeZeroDiv(), // 0/0, produced at runtime (avoids compile-time constant folding)
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
			// The sole exception is NaN (not covered by this case).
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
		{NumberValue(0), true},          // Lua: 0 is truthy
		{NumberValue(math.NaN()), true}, // Lua: NaN is truthy
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
	// 48-bit payload truncation: the top 16 bits are dropped (01 §3.5).
	v := LightUDValue(0xDEAD_DEAD_BEEF_CAFE)
	if Tag(v) != TagLightUD {
		t.Errorf("light ud tag = %#x", Tag(v))
	}
	if got := AsLightUD(v); got != 0xDEAD_BEEF_CAFE {
		t.Errorf("light ud payload = %#x, want 0xDEADBEEFCAFE", got)
	}
}

func TestTagFullCoverage(t *testing.T) {
	// The 8 non-number tags fill 0xFFF8..0xFFFF exactly (01 §3.3 invariant).
	tags := []uint16{TagNil, TagBool, TagLightUD, TagString, TagTable, TagFunction, TagUserdata, TagThread}
	for i, tag := range tags {
		want := uint16(0xFFF8 + i)
		if tag != want {
			t.Errorf("tag #%d = %#x, want %#x", i, tag, want)
		}
	}
}

// Use noinline functions so the compiler won't fold the expressions into constants.
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
