//go:build wangshu_p4 && wangshu_profile

// forloop_nan_limit_test.go — issues #117/#118 regression: a NaN in
// the PJ3 byte-level FORLOOP spec templates' const slots must exit
// the loop with zero iterations (interpreter semantics: the continue
// condition is false for NaN), not spin forever inside the mmap
// segment.
//
// `for A=0,0%0 do end` const-folds 0%0 to NaN, which is a genuine
// number (below the NaN-box tag space) and so passes the template's
// analyzeForLoopForm number gates. The template's exit compare was
// `ucomisd idx, limit; ja exit` — ucomisd sets CF=ZF=PF=1 on
// unordered, so `ja` (CF=0 && ZF=0) never jumped and the segment
// looped forever, unreachable by safepoint or step budget (arm64
// mirror: `fcmpe; b.gt` — GT is false on unordered). The per-op
// translator's emitFORLOOP already exits correctly on unordered;
// only the PJ3 spec templates were affected.
//
// Test discipline (prove-the-path): the template only matches the
// EXACT 6/7-op empty-body shape (`for i=K1,K2 do end` + empty
// RETURN), and only executes on the promoted dispatch AFTER the
// promoting run — so each case runs prog twice on one State and
// asserts jit.SpecForLoopHits() advanced (a carrier that misses the
// template silently tests the per-op path instead, which was never
// broken — the first draft of this test did exactly that).
//
// Found by nightly go-fuzz (FuzzAutoPromote eb8fb93a433d40b2 hung the
// worker; FuzzP4ForceAllPromote 5159747aad201f47 hung during
// minimization of the same shape).
package wangshu_test

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
	// EmptyConst form, NaN limit (the fuzz crasher shape).
	{"nan_limit", `local function k() for A = 0, 0%0 do end end
k() k()`},
	// EmptyConst form, NaN init: FORPREP's pre-decrement and the
	// first addsd keep idx NaN — same unordered-exit obligation.
	{"nan_init", `local function k() for A = 0%0, 10 do end end
k() k()`},
	// EmptyConst form, NaN step: analyzeForLoopForm's step>0 gate
	// must decline NaN (`NaN <= 0` is false in Go, so a naive gate
	// admits it and emits the template with an unordered compare
	// every iteration).
	{"nan_step", `local function k() for A = 1, 10, 0%0 do end end
k() k()`},
}

// TestI117_SpecForLoopNaNTerminates pins the template fix: two runs
// on a force-all State (run 1 promotes, run 2 executes the mmap
// segment), with a SpecForLoopHits delta proving the spec template
// (not the per-op path) actually compiled the kernel. nan_step is
// the exception: the analyzer must DECLINE it, so its probe
// assertion is inverted.
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
			delta := jit.SpecForLoopHits() - before
			if tc.name == "nan_step" {
				if delta != 0 {
					t.Fatalf("NaN step must be declined by the shape gate, but spec template compiled (delta=%d)", delta)
				}
				return
			}
			if delta == 0 {
				t.Fatal("carrier missed the PJ3 spec template (SpecForLoopHits unchanged); test is vacuous")
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
