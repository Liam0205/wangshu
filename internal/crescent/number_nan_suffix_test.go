package crescent

import (
	"math"
	"testing"
)

// TestParseLuaNumber_NaNCharSequence pins C99's optional nan(n-char-sequence),
// which PUC accepts through strtod and wangshu previously rejected (#192).
//
// The group is consumed only when the ')' is present: an unterminated "nan("
// must leave the bare word so the caller still rejects the trailing garbage,
// which is what strtod does. "inf(...)" has no such form in C99 and must stay
// rejected.
func TestParseLuaNumber_NaNCharSequence(t *testing.T) {
	for _, tc := range []struct {
		in     string
		wantOK bool
		isNaN  bool
	}{
		// accepted, all NaN
		{"nan(0)", true, true},
		{"nan(123)", true, true},
		{"nan(quiet)", true, true},
		{"nan(_)", true, true},
		{"nan(a1_Z)", true, true},
		{"nan()", true, true},
		{"NAN(0)", true, true},
		{"NaN(0)", true, true},
		{"-nan(x)", true, true},
		{"+nan(x)", true, true},
		{"  nan(0)  ", true, true},
		// bare word still works
		{"nan", true, true},
		{"-nan", true, true},
		// rejected: unterminated or ill-formed group leaves trailing garbage
		{"nan(", false, false},
		{"nan(0", false, false},
		{"nan(!)", false, false},
		{"nan(0)x", false, false},
		{"nan()extra", false, false},
		{"nan(a b)", false, false},
		{"nan(-1)", false, false},
		// inf takes no n-char-sequence
		{"inf(0)", false, false},
		{"infinity(0)", false, false},
		// inf itself unaffected
		{"inf", true, false},
		{"-inf", true, false},
	} {
		got, ok := ParseLuaNumber(tc.in)
		if ok != tc.wantOK {
			t.Errorf("ParseLuaNumber(%q) ok = %v, want %v (value %v)", tc.in, ok, tc.wantOK, got)
			continue
		}
		if ok && tc.isNaN && !math.IsNaN(got) {
			t.Errorf("ParseLuaNumber(%q) = %v, want NaN", tc.in, got)
		}
		if ok && !tc.isNaN && math.IsNaN(got) {
			t.Errorf("ParseLuaNumber(%q) = NaN, want a non-NaN value", tc.in)
		}
	}
}
