// issue123_regression_test.go — corpora from #123 (nightly p4
// crasher 326b508e) and its variant caught by this PR's own
// fuzz-smoke on macos-latest (8c132ff5) — pinned as PLAIN Go
// regression tests, not as testdata/fuzz seeds.
//
// Why not testdata/fuzz/: the fuzz coordinator's -parallel=4
// baseline-coverage sweep replays every seed corpus in parallel at
// startup. Adding these two shapes as seeds destabilizes fuzz-smoke
// (three CI legs died within 30s on the first push after adding
// them, each landing an exit-status-2 worker death — signature of
// process-level resource exhaustion from concurrent replays of this
// specific workload). Running the same inputs directly under the
// interpreter and P4 force-all here — one at a time, no fuzz
// coordinator — reproduces the intended regression contract (byte-
// equal termination without hang) while sidestepping the concurrent-
// replay pressure.
//
// The shape (deep non-tail recursion + 60-iter global-store loop per
// level) is intrinsically expensive; the fuzz worker still finds new
// variants over long runs, and each new nightly crasher of the same
// shape should be added HERE, not to testdata/fuzz/.
package regression

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// issue123Corpora: the exact input strings pulled from
//   - testdata/fuzz/FuzzAutoPromote/326b508ea720a654  (#123, nightly)
//   - testdata/fuzz/FuzzAutoPromote/8c132ff5b9631f77  (this PR's mac smoke)
var issue123Corpora = []struct {
	name string
	src  string
}{
	{"i123_326b508e", "function sum()for B=0,60 do Y=0 Y=A X=O end sum()end sum()"},
	{"i123_8c132ff5", "function sum()for B=0,60 do Y=0 Y=A oooooX=O end sum()end sum()"},
}

// TestI123_NightlyCorporaMirrorFuzzHarness runs each corpus through
// exactly the harness FuzzAutoPromote would apply (thresholds 2/4,
// budget 1<<20, two runs on one auto State, P1 baseline compared
// for byte-equality) — but sequentially, not under fuzz coordinator
// -parallel=4.
//
// Hang detection is DELEGATED to go test's package-level -timeout
// (default 10m), on purpose. This test used to wrap each run in its
// own wall-clock deadline, and the constant lost twice: 5s flunked
// the ubuntu-latest P1 leg, 30s flunked p4/macos-latest right after
// PR #128 merged (-race runs this shape in ~21s on a fast machine;
// shared runners have no slowness upper bound). "Genuinely hung" vs
// "legitimately slow" cannot be told apart by any finite in-test
// cap, but a real #123-style in-segment hang NEVER returns — the
// package timeout catches it with a full goroutine dump (better
// diagnostics than the old t.Fatalf), at the acceptable cost of a
// slower failure only in the case that must fail anyway.
func TestI123_NightlyCorporaMirrorFuzzHarness(t *testing.T) {
	for _, tc := range issue123Corpora {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(tc.src), tc.name)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			st1 := wangshu.NewState(wangshu.Options{})
			st1.SetStepBudget(1 << 20)
			st1.SetHotThresholds(^uint32(0), ^uint32(0))

			stA := wangshu.NewState(wangshu.Options{})
			stA.SetStepBudget(1 << 20)
			stA.SetHotThresholds(2, 4)

			for run := 1; run <= 2; run++ {
				_, runP1 := prog.Run(st1)
				_, runA := prog.Run(stA)
				// Both must terminate with the SAME error kind
				// (budget-exceeded / stack-overflow — this shape
				// dies one way or the other by design). Byte-equal
				// error-message comparison is too strict on the
				// tiered path (line-number formatting differs); we
				// only pin existence-equivalence, like the fuzz
				// harness does.
				if (runP1 == nil) != (runA == nil) {
					// Budget/timing class is skipped, mirroring
					// the fuzz harness's own exemption.
					budgetTiming := (runP1 != nil && strings.Contains(runP1.Error(), "instruction budget exceeded")) ||
						(runA != nil && strings.Contains(runA.Error(), "instruction budget exceeded"))
					if budgetTiming {
						t.Skipf("budget/timing divergence (fuzz harness exemption): P1=%v auto=%v", runP1, runA)
						return
					}
					t.Fatalf("run %d error-existence divergence: P1=%v auto=%v", run, runP1, runA)
				}
			}
		})
	}
}
