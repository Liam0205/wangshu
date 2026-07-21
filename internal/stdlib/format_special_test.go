// string.format NaN/Inf rendering unit tests (issues #170/#171): PUC 5.1.5
// routes %f/%e/%g/%E/%G of a non-finite through C sprintf, whose glibc output
// differs from Go's fmt ("NaN"/"+Inf"/"-Inf"). cFormatSpecialFloat reproduces
// glibc byte-for-byte. Every expectation here was verified against the
// embedded PUC 5.1.5 oracle (see the FuzzOracleDiff sweep in the fix commit).
package stdlib

import (
	"math"
	"testing"
)

func TestCFormatSpecialFloat(t *testing.T) {
	nan := math.NaN()
	pinf := math.Inf(1)
	ninf := math.Inf(-1)

	cases := []struct {
		spec string // the "%...": flags + width + optional .prec (no verb)
		verb byte
		f    float64
		want string
	}{
		// Bare verbs: lowercase -> nan/inf, uppercase -> -NAN/INF.
		{"%", 'f', nan, "nan"},
		{"%", 'e', nan, "nan"},
		{"%", 'g', nan, "nan"},
		{"%", 'E', nan, "-NAN"},
		{"%", 'G', nan, "-NAN"},
		{"%", 'f', pinf, "inf"},
		{"%", 'e', pinf, "inf"},
		{"%", 'E', pinf, "INF"},
		{"%", 'G', pinf, "INF"},
		{"%", 'f', ninf, "-inf"},
		{"%", 'E', ninf, "-INF"},

		// Sign flags: NaN ignores +/space under lowercase; Inf honors them.
		{"%+", 'f', nan, "nan"},
		{"%+", 'f', pinf, "+inf"},
		{"% ", 'f', pinf, " inf"},

		// Width. Lowercase NaN pads to width-1 (glibc reserves an unshown
		// sign column); Inf and uppercase NaN pad to the full width.
		{"%5", 'f', nan, " nan"},         // width 5 -> effective 4
		{"%10.3", 'f', nan, "      nan"}, // width 10 -> effective 9, prec ignored
		{"%8.2", 'f', nan, "    nan"},    // width 8 -> effective 7
		{"%4", 'f', nan, "nan"},          // width 4 -> effective 3 == len, no pad
		{"%2", 'f', nan, "nan"},          // width < len, no pad
		{"%5", 'f', pinf, "  inf"},       // Inf: full width 5
		{"%10", 'f', pinf, "       inf"}, // Inf: full width 10
		{"%5", 'E', nan, " -NAN"},        // uppercase NaN: full width 5 (sign shown)
		{"%8", 'G', nan, "    -NAN"},     // uppercase NaN: full width 8

		// Left-justify.
		{"%-10", 'f', nan, "nan      "},   // width 10 -> effective 9, left
		{"%-8", 'E', nan, "-NAN    "},     // uppercase: full width 8, left
		{"%-10", 'f', pinf, "inf       "}, // Inf: full width 10, left
	}
	for _, tc := range cases {
		got := string(cFormatSpecialFloat([]byte(tc.spec), tc.verb, tc.f))
		if got != tc.want {
			t.Errorf("cFormatSpecialFloat(%q, %q, %v) = %q, want %q",
				tc.spec, string(tc.verb), tc.f, got, tc.want)
		}
	}
}
