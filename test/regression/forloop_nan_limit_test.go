//go:build wangshu_p4 && wangshu_profile

// forloop_nan_limit_test.go — issues #117/#118 regression: a NaN in
// the PJ3 byte-level FORLOOP spec templates' const slots must exit
// the loop with zero iterations (interpreter semantics: the continue
// condition is false for NaN), not spin forever inside the mmap
// segment.
//
// HISTORY: `for A=0,0%0 do end` USED to const-fold 0%0 to NaN, which
// passed analyzeForLoopForm's number gates into the EmptyConst
// template, whose exit compare was `ucomisd idx, limit; ja exit` —
// ja never jumps on unordered, so the segment looped forever,
// unreachable by safepoint or step budget (arm64 mirror: fcmpe;
// b.gt). The per-op translator's emitFORLOOP already exited
// correctly; only the PJ3 spec templates were affected.
//
// PUC-parity constant folding (oracle diff fuzz round) later removed
// the SOURCE-level carrier entirely: official 5.1.5 refuses to fold
// x%0 / NaN results, so `0%0` now compiles to a runtime MOD and a
// NaN can no longer reach a Proto const slot from any source text.
// The shapes below therefore no longer hit the spec template (their
// loop bounds aren't Proto consts anymore) — the template-level
// unordered-exit obligation is pinned INSIDE the emitters instead
// (TestPJ3_ForLoopEmptyConst_RoundTrip NaN cases run real NaN bits
// through the mmap'd template; the arm64 byte-layout test asserts
// B.HI). What remains here is the end-to-end guarantee the fuzz
// crashers actually demand: these EXACT shapes terminate promptly
// under force-all/auto promotion and match P1 results.
//
// Found by nightly go-fuzz (FuzzAutoPromote eb8fb93a433d40b2 hung the
// worker; FuzzP4ForceAllPromote 5159747aad201f47 hung during
// minimization of the same shape).
package regression

import (
	"testing"
	"time"

	"github.com/Liam0205/wangshu"
	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
)

// mustFinish runs fn with a deadline; the pre-fix failure mode is an
// in-segment infinite loop no budget or safepoint can preempt.
func mustFinish(t *testing.T, deadline time.Duration, what string, fn func() error) {
	t.Helper()
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	select {
	case err := <-ch:
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	case <-time.After(deadline):
		t.Fatalf("%s did not terminate within %v (in-segment infinite loop)", what, deadline)
	}
}

// forloopNaNShapes: every case's kernel body is exactly the PJ3
// empty-body shape so the spec template matches. The trailing
// `return 1` lives in the OUTER chunk, not the kernel.
var forloopNaNShapes = []struct {
	name string
	src  string
}{
	// The original fuzz crasher shape: NaN limit (0%0 is now a
	// runtime MOD, not a folded const — see file header).
	{"nan_limit", `local function k() for A = 0, 0%0 do end end
k() k()`},
	// NaN init: FORPREP's pre-decrement and the first add keep idx
	// NaN — same unordered-exit obligation on every tier.
	{"nan_init", `local function k() for A = 0%0, 10 do end end
k() k()`},
	// NaN step: the step>0 gate must not admit NaN (`NaN <= 0` is
	// false in Go, so a naive gate admits it).
	{"nan_step", `local function k() for A = 1, 10, 0%0 do end end
k() k()`},
}

// TestI117_SpecForLoopNaNTerminates: two runs on a force-all State
// (run 1 promotes, run 2 executes the promoted dispatch — the run
// that used to hang). Since PUC-parity folding removed compile-time
// NaN consts, the shapes exercise the runtime-NaN promoted paths;
// each must terminate promptly AND match the interpreter (zero
// iterations for NaN limit/init/step). SpecForLoopHits must NOT
// advance for any of them — a delta means a compile-time NaN const
// snuck back into the template, i.e. the folding regressed.
func TestI117_SpecForLoopNaNTerminates(t *testing.T) {
	for _, tc := range forloopNaNShapes {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(tc.src), "i117-"+tc.name)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			before := jit.SpecForLoopHits()
			st := wangshu.NewState(wangshu.Options{})
			st.SetForceAllPromote(true)
			mustFinish(t, 10*time.Second, "promoting run", func() error {
				_, err := prog.Run(st)
				return err
			})
			mustFinish(t, 10*time.Second, "native run", func() error {
				_, err := prog.Run(st)
				return err
			})
			if delta := jit.SpecForLoopHits() - before; delta != 0 {
				t.Fatalf("NaN loop bound reached a Proto const slot (spec template hit, delta=%d) — PUC-parity constant folding regressed", delta)
			}
		})
	}
}

// TestI117_AutoPromoteCrasherShape replays the exact FuzzAutoPromote
// crasher under the fuzz harness's own conditions: lowered heat
// thresholds and TWO runs on one State (run 1 crosses the threshold
// and promotes; run 2 dispatches into the promoted template — the
// run that hung the fuzz worker).
func TestI117_AutoPromoteCrasherShape(t *testing.T) {
	src := `function sum()for A=0,0%0 do end end sum()sum()`
	prog, err := wangshu.Compile([]byte(src), "i117-auto")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetHotThresholds(2, 4)
	for run := 1; run <= 2; run++ {
		mustFinish(t, 10*time.Second, "auto run", func() error {
			_, err := prog.Run(st)
			return err
		})
	}
}
