//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

// issue144_regression_test.go — serial regression test for issue #144:
// a nightly fuzz worker died on the P3 leg with corpus c3665b55c170a11d
// (a 777M-iteration for-loop with quadratic-concat-shaped body, but the
// body's `out3` typo means the concat never grows and the step budget
// catches it immediately). The corpus replays 100% clean on current
// master and matches the unreproducible-crasher profile (fuzz
// coordinator parallel-replay amplification, not an input-determined VM
// bug).
//
// Per llmdoc/guides/unreproducible-crasher-triage.md: the corpus
// consumes ~670ms per Run (P3, budget-bounded), which is above the
// "several hundred ms" threshold for a heavy workload. To avoid the
// coordinator parallel-replay pressure that crashed the nightly worker
// (the same failure mode as #123), this is an explicit serial Go
// regression test rather than a `testdata/fuzz/` corpus entry. The
// functional assertion is: under a 1<<20 step budget, the loop hits
// "instruction budget exceeded" or an arena cap without hanging.
package regression

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

func TestIssue144_HeavyLoopBudgetBounded(t *testing.T) {
	// The exact corpus content from c3665b55c170a11d. Note `out3 =` is a
	// typo (assigns to global out3, so `out` stays "" — no actual
	// quadratic growth), making the loop body trivially cheap per
	// iteration but the iteration count (777M) is massive.
	src := "local function cat(i) return \"x\\x10\\x10\\x10v\\x1b\\x94V\\xa4\\xedbbbbb\\x800\\xce\\x06\\x9a\\xbd\\xc9q~|t7\\x95\\x88\\xe8<^\" .. i end; local out = \"\"; for i = 1, 777777776 do out3= out .. cat(i) end; return out"
	prog, err := wangshu.Compile([]byte(src), "i144")
	if err != nil {
		// Compile errors are a legal outcome for this corpus (binary
		// escapes may be ill-formed depending on the exact parser cut);
		// the point is that it does not hang.
		t.Skipf("compile error (legal for binary corpus): %v", err)
	}
	st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	st.SetStepBudget(1 << 20)
	_, err = prog.Run(st)
	if err == nil {
		t.Fatalf("expected budget/arena error, got nil (loop ran to completion?)")
	}
	msg := err.Error()
	if !strings.Contains(msg, "instruction budget exceeded") &&
		!strings.Contains(msg, "arena:") {
		t.Errorf("unexpected error: %q (expected budget or arena error)", msg)
	}
}
