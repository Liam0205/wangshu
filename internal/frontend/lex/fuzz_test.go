// Go-native fuzz target (the runnable carrier for engineering.md section 1
// fuzz-smoke).
//
// Invariant: under any input, the lexer must not panic; lexical errors
// must be returned through the error channel.
package lex

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/frontend/token"
)

// fuzzMaxTokens caps the number of tokens consumed per iteration so each
// iteration stays well under 1 ms wall-clock.
//
// Background: with `1<<16` (65536) as the token iteration cap, pathological
// inputs (e.g. tens of thousands of single-byte tokens such as `;;;;...`)
// ran for hundreds of milliseconds per iteration. When such an iteration
// was running at the fuzz wall-clock deadline, the cancel race produced
// `context deadline exceeded` failures (per engineering.md section 1.1:
// a wall-clock budget out of step with per-iteration cost).
//
// `1<<12` (4096) keeps worst-case per-iteration cost in the sub-millisecond
// range. The input size limit stays at `1<<16` so the fuzz mutator has
// room to explore long-byte mutations without triggering t.Skip churn.
const fuzzMaxTokens = 1 << 12 // 4096

func FuzzLexer(f *testing.F) {
	seeds := []string{
		`local x = 1`,
		`-- comment`,
		`--[[ long comment ]]`,
		`"str\n\t\\"`,
		`[[long string]]`,
		`[==[nested]==]`,
		`0x1F 1e10 .5 3.`,
		`a..b ... == ~= <= >=`,
		"\xff\xfe\x00",
		`"unterminated`,
		`[[unterminated`,
		`1e`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		if len(src) > 1<<16 {
			t.Skip()
		}
		lx := New(src, "fuzz")
		for i := 0; i < fuzzMaxTokens; i++ {
			tok, err := lx.Next()
			if err != nil {
				return // a lexical error is a valid outcome; only panic is a bug
			}
			if tok.Kind == token.EOF {
				return
			}
		}
	})
}
