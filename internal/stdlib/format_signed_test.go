package stdlib

import (
	"math"
	"testing"
)

// TestCSignedFormat_EmptyDigitsKeepsSign pins the one corner where Go's fmt and
// C printf disagree on %d/%i: precision 0 with value 0 converts to no digits
// (C99 7.19.6.1), but a '+' or ' ' flag still emits its sign, because the sign
// is not one of the converted digits. Go drops the sign along with the digits.
//
// Everything else delegates to fmt, so the table also covers the neighbours that
// must NOT change: no flag, nonzero value, and no precision.
func TestCSignedFormat_EmptyDigitsKeepsSign(t *testing.T) {
	for _, tc := range []struct {
		spec string
		n    int64
		want string
	}{
		// the divergent corner
		{"%+.0", 0, "+"},
		{"% .0", 0, " "},
		{"%+5.0", 0, "    +"},
		{"% 5.0", 0, "     "}, // sign is a space, so the field is all spaces (C: len 5)
		{"%-+5.0", 0, "+    "},
		{"%- 5.0", 0, "     "}, // left-justified space sign: also 5 spaces (C: len 5)
		{"%+1.0", 0, "+"},
		{"%+0.0", 0, "+"},
		// no sign flag: empty stays empty, padding still applies
		{"%.0", 0, ""},
		{"%5.0", 0, "     "},
		{"%-5.0", 0, "     "},
		// nonzero value: ordinary path
		{"%+.0", 1, "+1"},
		{"%+.0", -1, "-1"},
		{"%+5.0", 7, "   +7"},
		// no precision: ordinary path, zero prints its digit
		{"%+", 0, "+0"},
		{"% ", 0, " 0"},
		{"%", 0, "0"},
		{"%5", 0, "    0"},
		// precision > 0 with zero: digits present
		{"%+.1", 0, "+0"},
		{"%.3", 0, "000"},
	} {
		got := string(cSignedFormat([]byte(tc.spec), tc.n))
		if got != tc.want {
			t.Errorf("cSignedFormat(%q, %d) = %q, want %q", tc.spec, tc.n, got, tc.want)
		}
	}
}

// TestCCharCast_X86Semantics pins the double->int cast PUC's luaL_checkint
// performs for string.char, on the x86-64 result that wangshu standardizes.
//
// The cast is two steps: double -> lua_Integer (ptrdiff_t, 64-bit) -> int. On
// x86-64 cvttsd2si maps NaN and every out-of-int64-range double to INT64_MIN,
// whose low 32 bits are 0, so string.char of those yields byte 0 rather than an
// error. arm64 saturates differently, which is why the differential harness skips
// this range rather than comparing it.
func TestCCharCast_X86Semantics(t *testing.T) {
	nan := math.NaN()
	inf := math.Inf(1)
	for _, tc := range []struct {
		name string
		in   float64
		want int32
	}{
		{"nan", nan, 0},
		{"+inf", inf, 0},
		{"-inf", math.Inf(-1), 0},
		{"above int64", 1e30, 0},
		{"below int64", -1e30, 0},
		// in range: plain truncation toward zero, which every arch agrees on
		{"zero", 0, 0},
		{"in range", 65, 65},
		{"fraction truncates", 65.9, 65},
		{"negative truncates", -1.9, -1},
		{"max byte", 255, 255},
		{"just past byte", 256, 256},
		{"2^53 low32", 9007199254740992, 0},
	} {
		if got := cCharCast(tc.in); got != tc.want {
			t.Errorf("%s: cCharCast(%v) = %d, want %d", tc.name, tc.in, got, tc.want)
		}
	}
}
