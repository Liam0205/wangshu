// Pattern matcher fuzz:任意 (src, pattern) 不得 panic
// (malformed pattern 经 error 返回;递归深度有 200 层护栏)。
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
			t.Skip() // 限尺寸防极端回溯耗时
		}
		_, _, _, _, _ = patternFind(src, pat, 0) // 错误是合法结果;panic 才是 bug
	})
}
