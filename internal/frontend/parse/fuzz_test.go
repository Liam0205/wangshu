// Parser fuzz: under any source, the parser must not panic (syntax errors
// are returned through the error channel; the parser stops at the first
// error).
package parse

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/frontend/lex"
)

// fuzzMaxInputBytes caps input size so each fuzz iteration stays well
// under 1 ms wall-clock.
//
// Background: with 1<<16 (65 KiB) as the input cap, pathological inputs
// with deep recursive descent (e.g. heavily nested parens / table
// constructors with mixed precedence) ran for hundreds of milliseconds.
// When such an iteration was running at the fuzz wall-clock deadline,
// the framework's worker-cleanup hard timeout fired, surfacing as
// `context deadline exceeded` (per engineering.md section 1.1: the
// wall-clock budget must stay in step with per-iteration cost; same
// pattern as FuzzLexer in the sibling package).
//
// 1<<12 (4 KiB) keeps the worst case in the sub-millisecond range; the
// fuzz mutator still has plenty of room to explore syntactic variety.
const fuzzMaxInputBytes = 1 << 12 // 4096

func FuzzParse(f *testing.F) {
	seeds := []string{
		`local x = 1 + 2`,
		`function f(a, ...) return a, ... end`,
		`for i = 1, 10 do print(i) end`,
		`for k, v in pairs(t) do end`,
		`while true do break end`,
		`repeat local x = 1 until x`,
		`if a then b() elseif c then d() else e() end`,
		`local t = { 1, 2, x = 3, [k] = v }`,
		`a.b.c:m(1, "s", {})`,
		`return f()(g())[h()]`,
		`local a, b, c = f()`,
		`x = -#t .. "s" ^ 2`,
		`function a.b.c:m() end`,
		`((((((((((x))))))))))`,
		`do do do end end end`,
		`local function f() return function() return f end end`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		if len(src) > fuzzMaxInputBytes {
			t.Skip()
		}
		lx := lex.New(src, "fuzz")
		_, _ = Parse(lx, "fuzz") // an error is a valid outcome; only panic is a bug
	})
}
