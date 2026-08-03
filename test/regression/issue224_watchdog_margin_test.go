//go:build wangshu_p4

package regression

import (
	"testing"
	"time"

	wangshu "github.com/Liam0205/wangshu"
)

// TestConcatStormKeepsWatchdogMargin covers #224 and #225, the seventh and eighth filings of the
// concat-storm family, and pins the quantity those two were actually about.
//
// Neither seed crashes or diverges, and both are correctly bounded by the byte accounting -- on the
// pre-#222 commit too, so nothing in that round is what fixed them. What was wrong is the MARGIN. A
// 1<<20 step budget permits about 64 MiB of concat, which cost 0.7-1.3s per fuzz subtest locally,
// and CI runners are roughly 10x slower (see chargeBulkWork's note in internal/crescent/state.go).
// The slowest family seeds therefore landed at 12-13s against go-fuzz's 10-second per-input
// watchdog, and the nightly log says exactly that: "panic: deadlocked".
//
// So a budget that merely BOUNDS the work is not enough when the bound itself sits within one order
// of magnitude of a wall-clock watchdog on the machine that matters. The fuzz targets now use
// fuzzStepBudget (1<<19), which costs no coverage -- these shapes trip either way, the fuzzer walks
// the same paths and stops sooner -- and restores about 1.8x margin at CI speed.
//
// This test measures the budget's cost at the harness's own setting rather than the corpus timing,
// so it fails if the budget is raised back or if the per-byte rate is loosened.
func TestConcatStormKeepsWatchdogMargin(t *testing.T) {
	// The family's shape: a ~90-byte literal concatenated in a very long loop. Both #224 and #225
	// are this, with the target of the assignment misspelled so the accumulator never grows -- which
	// is why the minimized seeds are light and replay clean. Both forms are covered.
	for _, tc := range []struct{ name, src string }{
		{"accumulator never grows",
			`local function cat(i) return string.rep("x",90) .. i end
			 local out="" for i=1,777777776 do qut = out .. cat(i) end return out`},
		{"accumulator grows quadratically",
			`local function cat(i) return string.rep("x",90) .. i end
			 local out="" for i=1,777777776 do out = out .. cat(i) end return out`},
		// The shapes that actually BIND the budget. The two above are the filed seeds, but they are
		// cheap enough to pass at 1<<19 too, so a test using only them could not tell 1<<16 from
		// 1<<19 -- which an audit demonstrated. These three are the most expensive reachable
		// neighbours: at 1<<19 they projected to 14s, 18s and 41s for four Runs at 10x CI speed.
		//
		// gsub is the binding one because it bills roughly twice its subject per call while doing
		// considerably more work than an equally-billed concat: equal billing is not equal
		// wall-clock, which is why the budget has to be set by the worst BILLED-EQUAL shape rather
		// than by the shape that happened to be filed.
		{"plain concat loop",
			`local out="" for i=1,777777776 do out=out.."x" end return out`},
		{"table with string keys",
			`local t={} for i=1,777777776 do t[tostring(i)]=i end return 1`},
		{"gsub loop",
			`local s=string.rep("a",4096) for i=1,777777776 do s:gsub("%a","x") end return 1`},
	} {
		prog, err := wangshu.Compile([]byte(tc.src), "r")
		if err != nil {
			t.Fatalf("%s: compile: %v", tc.name, err)
		}
		st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
		st.SetStepBudget(fuzzBudgetUnderTest)
		start := time.Now()
		_, rerr := prog.Run(st)
		elapsed := time.Since(start)

		if rerr == nil {
			t.Errorf("%s: ran to completion; a 777-million-iteration concat loop must trip the budget",
				tc.name)
		}
		// One Run must stay under 10s / 10x / 4 Runs = 250ms.
		//
		// The first version of this test used a 1-second bound and an audit showed it passed with
		// the budget restored to 1<<20 -- the exact regression it claimed to guard. Its own comment
		// derived 250ms and then used four times that, and it compared a four-Run projection
		// against a one-Run measurement. The bound is now the derived number.
		if elapsed > 250*time.Millisecond {
			t.Errorf("%s: one Run took %v at the fuzz budget; four of these on a 10x-slower CI "+
				"runner would pass go-fuzz's 10s per-input watchdog, which is how #224/#225 were "+
				"filed", tc.name, elapsed.Round(time.Millisecond))
		}
	}
}

// fuzzBudgetUnderTest mirrors fuzzStepBudget in the root package's fuzz harness, which this package
// cannot import (it lives in wangshu_test behind profile build tags). Kept in sync deliberately: if
// the harness budget is raised, this test measures the raised value's cost and fails.
const fuzzBudgetUnderTest = 1 << 16
