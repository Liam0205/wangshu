// loop_budget_test.go — issue #102 regression: P4 in-segment loop
// back-edges must bill the step budget.
//
// A fully-inline loop body (arithmetic, or a #77 math intrinsic like
// `f(0)` → FABS emitted in-segment) never reaches a Go-side billing
// point: st.preempt() runs only on interpreter call/back-edge sites,
// all of which the native loop bypasses. Before the loopFuel guard a
// budgeted 277M-iteration loop ran to completion in ~9s while the
// interpreter raised "instruction budget exceeded" in 40ms (fuzz seed
// 3edb662d8f1525de tripped PR #101's CI hang detector on this).
//
// The fix mirrors issue #89's segCallFuel pattern on the back edge:
// every FORLOOP condTrue / negative-sBx JMP decrements
// jitCtx.loopFuel; at zero the segment exits via HelperLoopFuel and
// host.LoopPreempt bills + refills + runs the standard preemption
// check. These tests pin both halves: the budget error fires promptly,
// and the LoopFuelExitCount white-box probe proves it fired through
// the back-edge guard (not some other billing point).

//go:build wangshu_p4 && wangshu_profile

package regression

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

// runBudgeted compiles src, arms a 1<<20 step budget, runs under the
// given promotion mode, and returns (err, elapsed). A 30s watchdog
// converts "budget never billed" from a suite timeout into a test
// failure with a useful message.
func runBudgeted(t *testing.T, src string, forceAll bool) (error, time.Duration) {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "i102")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetStepBudget(1 << 20)
	if forceAll {
		st.SetForceAllPromote(true)
	} else {
		st.SetHotThresholds(2, 4)
	}
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := prog.Run(st)
		done <- err
	}()
	select {
	case err := <-done:
		return err, time.Since(start)
	case <-time.After(30 * time.Second):
		t.Fatalf("hung — in-segment loop back-edge not billing the budget")
		return nil, 0
	}
}

// TestI102_BudgetPreemptsInlineLoop pins the issue #102 repro shapes:
// each loop's body is fully inline in the P4 native segment, so
// without the back-edge fuel guard none of them would ever observe the
// armed budget. The white-box LoopFuelExitCount assertion proves the
// error came through the HelperLoopFuel round trip — asserted only for
// (shape, mode) pairs that deterministically ride the native emit
// path: arithBody compiles to the PJ3 spec template instead (whose own
// billing was probe-verified separately), and auto mode may trip the
// budget on interpreter iterations before promotion warms up.
func TestI102_BudgetPreemptsInlineLoop(t *testing.T) {
	cases := []struct {
		name, src     string
		wantFuelGuard bool // force-all mode rides the native back-edge guard
	}{
		// The original fuzz-derived repro: math intrinsic body (#77
		// emits FABS inline — zero Go crossings per iteration).
		{"intrinsicBody", `local f=math.abs function k(x)local s for A=0,277777770 do A=0 f(0)end end return k(0)`, true},
		// Plain 1-op arith body — promotes to the PJ3 spec template
		// (not the native emit), which has its own billing point; the
		// budget error is still mandatory.
		{"arithBody", `local function k() local s=0 for i=1,277777770 do s=s+1 end return s end return k()`, false},
		// while-true loop: the back edge is a negative-sBx JMP
		// terminator, not FORLOOP — must be metered the same way.
		{"whileBackEdge", `local function k() local s=0 while true do s=s+1 if s>277777770 then return s end end end return k()`, true},
	}
	for _, tc := range cases {
		for _, mode := range []struct {
			name     string
			forceAll bool
		}{{"force", true}, {"auto", false}} {
			t.Run(tc.name+"/"+mode.name, func(t *testing.T) {
				before := peroptranslator.LoopFuelExitCount.Load()
				err, elapsed := runBudgeted(t, tc.src, mode.forceAll)
				if err == nil || !strings.Contains(err.Error(), "instruction budget exceeded") {
					t.Fatalf("want budget error, got %v (elapsed %v)", err, elapsed)
				}
				if elapsed > 5*time.Second {
					t.Errorf("budget error too slow: %v (want ms-scale)", elapsed)
				}
				if tc.wantFuelGuard && mode.forceAll {
					if delta := peroptranslator.LoopFuelExitCount.Load() - before; delta == 0 {
						t.Errorf("LoopFuelExitCount did not increment — error did not come through the back-edge fuel guard")
					}
				}
			})
		}
	}
}

// TestI102_ContextCancelPreemptsInlineLoop: the cancel context rides
// the same LoopPreempt check as the budget (st.preempt() parity), so a
// canceled context must also stop an airtight loop.
func TestI102_ContextCancelPreemptsInlineLoop(t *testing.T) {
	src := `local function k() local s=0 for i=1,10000000000 do s=s+1 end return s end return k()`
	prog, err := wangshu.Compile([]byte(src), "i102ctx")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	st.SetContext(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := prog.Run(st)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("want context-canceled error, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("hung — cancel context not observed by in-segment loop")
	}
}

// TestI102_UnbudgetedLoopCompletes: without a budget the loop must
// still run to completion with the correct result — the Unlimited
// refill keeps the fuel guard at one dec+jnz per iteration and the
// single HelperLoopFuel round trip (initial 4096 quantum) must resume
// correctly at the back-edge continuation.
func TestI102_UnbudgetedLoopCompletes(t *testing.T) {
	src := `local s=0 for i=1,1000000 do s=s+i end return s`
	prog, err := wangshu.Compile([]byte(src), "i102u")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for _, forceAll := range []bool{false, true} {
		st := wangshu.NewState(wangshu.Options{})
		if forceAll {
			st.SetForceAllPromote(true)
		}
		res, err := prog.Run(st)
		if err != nil {
			t.Fatalf("forceAll=%v: %v", forceAll, err)
		}
		if len(res) != 1 || res[0].Number() != 500000500000 {
			t.Fatalf("forceAll=%v: want 500000500000, got %+v", forceAll, res)
		}
	}
}

// TestI102_DeoptStrandRepaired (PR #105 review): a seg2seg callee's
// loop that drains loopFuel at segCallDepth>0 deopts (set flag + ret)
// instead of exiting via HelperLoopFuel — LoopPreempt never runs and
// the CALLER's jitCtx counter parks at 0. Without repair, the caller's
// next inline back-edge wraps 0 -> 2^32 and runs unbilled — reopening
// exactly the #102 window this PR closes. RefreshJitCtxAddrs repairs
// the strand (armed && loopFuel==0 can only mean a deopt strand: a
// legit drain always refills through LoopPreempt before resume).
//
// Shape: inner's 10000-iteration loop exceeds the 4096 fuel quantum,
// so every seg2seg dispatch of inner deopts at depth>0 (inner's body
// needs >= 3 ops for AnalyzeNative to route it native — a 1-op body
// compiles to the PJ3 spec template, which is not a seg2seg target);
// outer's k-loop after the call sites must still trip the budget
// promptly. The SegToSegDeoptCount delta proves the deopt-strand path
// actually executed (prove-the-path; without it a cold IC would
// silently skip the scenario under test).
func TestI102_DeoptStrandRepaired(t *testing.T) {
	src := `local function inner()
  local s = 0
  local u = 0
  for i=1,10000 do s=s+1 u=u+2 s=s+u end
  return s
end
local function outer()
  local s=0
  for j=1,50 do s = s + inner() end
  for k=1,3000000000 do s=s+1 end
  return s
end
return outer()`
	deopt0 := peroptranslator.SegToSegDeoptCount.Load()
	err, elapsed := runBudgeted(t, src, true)
	if err == nil || !strings.Contains(err.Error(), "instruction budget exceeded") {
		t.Fatalf("want budget error, got %v (elapsed %v)", err, elapsed)
	}
	if elapsed > 5*time.Second {
		t.Errorf("budget error too slow: %v — loopFuel likely stranded at 0", elapsed)
	}
	if delta := peroptranslator.SegToSegDeoptCount.Load() - deopt0; delta == 0 {
		t.Errorf("SegToSegDeoptCount did not increment — the deopt-strand scenario was not exercised (seg2seg dispatch never engaged?)")
	}
}
