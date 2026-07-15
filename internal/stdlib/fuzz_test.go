// Pattern matcher fuzz: any (src, pattern) must not panic
// (malformed patterns are returned as errors; recursion depth is guarded at 200 levels).
package stdlib

import (
	"testing"
)

func FuzzPattern(f *testing.F) {
	type seed struct{ src, pat string }
	seeds := []seed{
		{"hello world", "%w+"},
		{"a,b,c", "[^,]+"},
		{"key=val", "(%w+)=(%w+)"},
		{"(nested(parens))", "%b()"},
		{"abcabc", "(abc)%1"},
		{"", ""},
		{"x", "%"},
		{"x", "["},
		{"x", "(unclosed"},
		{"aaa", "a-"},
		{"<<x>>", "<.->"},
		{"abc", "^a.c$"},
		{"\x00\xff", "[\x00-\xff]+"},
		{"deep", "((((((((x))))))))"},
	}
	for _, s := range seeds {
		f.Add([]byte(s.src), []byte(s.pat))
	}
	f.Fuzz(func(t *testing.T, src, pat []byte) {
		if len(src) > 1024 || len(pat) > 256 {
			t.Skip() // cap size to avoid extreme backtracking time
		}
		_, _, _, _, _ = patternFind(src, pat, 0) // an error is a valid result; only a panic is a bug
	})
}
