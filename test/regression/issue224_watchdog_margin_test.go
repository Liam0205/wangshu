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
	} {
		prog, err := wangshu.Compile([]byte(tc.src), "r")
		if err != nil {
			t.Fatalf("%s: compile: %v", tc.name, err)
		}
		st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
		st.SetStepBudget(1 << 19) // the fuzz targets' budget
		start := time.Now()
		_, rerr := prog.Run(st)
		elapsed := time.Since(start)

		if rerr == nil {
			t.Errorf("%s: ran to completion; a 777-million-iteration concat loop must trip the budget",
				tc.name)
		}
		// One Run must stay far enough under the watchdog that four of them survive a 10x-slower
		// runner: 10s / 10 / 4 = 250ms. The bound below is deliberately looser than that so a
		// shared runner does not make this flaky, while still failing if the budget returns to
		// 1<<20 (which measured ~0.35s per Run here, i.e. 14s projected for four on CI).
		if elapsed > 1*time.Second {
			t.Errorf("%s: one Run took %v at the fuzz budget; four of these on a 10x-slower CI "+
				"runner would pass go-fuzz's 10s per-input watchdog, which is how #224/#225 were "+
				"filed", tc.name, elapsed.Round(time.Millisecond))
		}
	}
}
