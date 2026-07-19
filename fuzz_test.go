// End-to-end fuzz: arbitrary source through Compile + Run must not panic
// (compile errors / runtime errors are returned as error; infinite/overlong
// loops are bounded by the back-edge instruction budget).
package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

func FuzzCompileRun(f *testing.F) {
	seeds := []string{
		`return 1 + 2`,
		`local t = {} t[1] = "x" return #t`,
		`return ("abc"):upper()`,
		`local ok, e = pcall(function() error("x") end) return ok, e`,
		`for i = 1, 3 do end return i`,
		`return select("#", 1, 2)`,
		`local co = coroutine.create(function() coroutine.yield(1) end)
return coroutine.resume(co)`,
		`return string.format("%d", 42)`,
		`return math.floor(1.5) + math.max(1, 2)`,
		`return nil == false`,
		`return 0/0 ~= 0/0`,
		`x = 1 return x`,
		`return string.rep("a", 3)`,
		`local a, b = 1 return b`,
		`for i = 1, 1e5 do end`,
		// 1e5 rather than 1e9: the fuzz framework's -fuzztime uses
		// context.WithTimeout internally for a wall-clock timeout; 1e9 runs
		// the interpreter for 100ms-1s+, close to the wall-clock scale, so on
		// a slower CI runner the framework reports "context deadline exceeded"
		// and force-fails. 1e5 equivalently tests the "loop + budget fallback
		// path" while leaving wall-clock headroom (1e6 also showed a 0/sec
		// tail on some fuzz-engine variants). A fuzz seed should not contain a
		// "near-infinite loop relying on the budget fallback" — that is project
		// discipline (engineering.md §1.1).
		`local n = 0 for i = 1, 1e5 do n = n + i end return n`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		recordFuzzExec("FuzzCompileRun", src)
		if len(src) > 1<<14 {
			t.Skip()
		}
		prog, err := wangshu.Compile([]byte(src), "fuzz")
		if err != nil {
			return // a compile error is a legal outcome
		}
		// MaxArenaBytes (issues #127/#130): quadratic-concat shapes
		// (`out = out .. f(i)` loops) balloon the default 2 GiB arena
		// before the step budget fires — one exec peaked at 13 GiB of
		// Go-side memory (grow doubling keeps old+new backings alive),
		// and 4 parallel fuzz workers multiplied that into silent
		// worker deaths. A 64 MiB cap bounds the shape to ~1s and a
		// few hundred MiB; hitting the cap is a legal error outcome
		// (same discipline as FuzzOracleDiff's runWangshuSide).
		st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
		// The back-edge instruction budget bounds overlong loops (for i=1,1e6
		// etc.); the fuzz engine also generates loop variants far exceeding
		// the budget, so SetStepBudget is a structural fallback. It is more
		// robust than filtering source substrings: loops built via
		// loadstring/concatenation are bounded just the same.
		st.SetStepBudget(1 << 20)
		_, _ = prog.Run(st) // a runtime error (incl. budget overrun) is a legal outcome; only a panic is a bug
	})
}
