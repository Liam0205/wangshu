package stdlib

import "testing"

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
