//go:build wangshu_p3 && wangshu_profile

// loop_stepbudget_p3_test.go — P3 gibbous loop back-edge step-budget
// accounting (the P3 dual of issue #102's P4 loop fuel). A fully-inline
// promoted loop back-edge (FORLOOP) must charge the step budget so an
// infinite loop raises "instruction budget exceeded" byte-equal to the
// interpreter, instead of hanging. Nightly oracle fuzz corpus
// bb525447c652d8d9: `for i=0,5/0 do X=0*i end` hung under P3.
package wangshu

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestP3LoopBudget_InfiniteLoopRaises: a P3-promoted infinite for-loop
// must raise the budget error, byte-equal to the interpreter.
func TestP3LoopBudget_InfiniteLoopRaises(t *testing.T) {
	src := `function sum(n)local s=0 for i=0,n do X=0*i end return s end return sum(12)%sum(5/0)`
	prog, err := Compile([]byte(src), "lb")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// interpreter baseline
	st1 := NewState(Options{MaxArenaBytes: 64 << 20})
	st1.SetStepBudget(1 << 20)
	_, e1 := prog.Run(st1)
	if e1 == nil {
		t.Fatalf("interpreter did not raise on infinite loop")
	}
	// P3 auto-promote (two runs so the loop is promoted mid-flight)
	stA := NewState(Options{MaxArenaBytes: 64 << 20})
	stA.SetStepBudget(1 << 20)
	stA.SetHotThresholds(2, 4)
	var eA error
	for run := 1; run <= 2; run++ {
		_, eA = prog.Run(stA)
		if eA != nil {
			break
		}
	}
	if eA == nil {
		t.Fatalf("P3 did not raise on infinite loop (loop back-edge not charging budget)")
	}
	if eA.Error() != e1.Error() {
		t.Errorf("budget error not byte-equal:\n  interp: %q\n  p3:     %q", e1.Error(), eA.Error())
	}
}

// TestP3LoopBudget_InfiniteWhileRaises: the P3 back-edge safepoint must
// cover while/repeat (negative-sBx JMP) back-edges too, not just numeric
// FORLOOP — otherwise a promoted infinite while-loop hangs. The step
// budget in P3 is charged ONLY at the back-edge safepoint (host helpers
// don't charge it), so a while-loop whose body promotes to a bare `br`
// back-edge would run forever. The crasher-shaped harness (top-level
// calls `sum` twice → auto-promote, `X=0*i` SETGLOBAL keeps the body in
// the segment) actually exercises the wasm segment's while back-edge.
func TestP3LoopBudget_InfiniteWhileRaises(t *testing.T) {
	src := `function sum(n)local s=0 local i=0 while i<=n do X=0*i i=i+1 end return s end return sum(12)%sum(5/0)`
	prog, err := Compile([]byte(src), "wlb")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st1 := NewState(Options{MaxArenaBytes: 64 << 20})
	st1.SetStepBudget(1 << 20)
	_, e1 := prog.Run(st1)
	if e1 == nil {
		t.Fatalf("interpreter did not raise on infinite while")
	}
	stA := NewState(Options{MaxArenaBytes: 64 << 20})
	stA.SetStepBudget(1 << 20)
	stA.SetHotThresholds(2, 4)
	var eA error
	for run := 1; run <= 2; run++ {
		_, eA = prog.Run(stA)
		if eA != nil {
			break
		}
	}
	if eA == nil {
		t.Fatalf("P3 did not raise on infinite while (JMP back-edge not charging budget)")
	}
	if eA.Error() != e1.Error() {
		t.Errorf("while budget error not byte-equal:\n  interp: %q\n  p3:     %q", e1.Error(), eA.Error())
	}
}

// TestP3LoopBudget_WhileChargesSafepoint proves the while (JMP) back-edge
// actually crosses to Safepoint in the segment (prove-the-path): a finite
// 3M-iteration while-loop under a budget must cross ~3M/quantum times, not
// zero. Zero would mean the back-edge runs a bare `br` with no accounting.
func TestP3LoopBudget_WhileChargesSafepoint(t *testing.T) {
	src := `function sum(n)local s=0 local i=0 while i<=n do X=0*i i=i+1 end return s end return sum(12)%sum(3000000)`
	prog, err := Compile([]byte(src), "wsp")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := NewState(Options{MaxArenaBytes: 64 << 20})
	st.SetStepBudget(1 << 28) // large enough to finish 3M iters
	st.SetHotThresholds(2, 4)
	before := st.SafepointCalls()
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	crossings := st.SafepointCalls() - before
	if crossings == 0 {
		t.Fatalf("while back-edge crossed Safepoint 0 times over 3M iters — " +
			"JMP back-edge not charging (budget leak)")
	}
	t.Logf("3M-iter budgeted while: %d Safepoint crossings (JMP back-edge charges)", crossings)
}

// TestP3LoopBudget_UnbudgetedHotLoopRarelyCrosses proves the fast path:
// a hot finite loop with NO step budget armed must not cross to
// host.Safepoint every iteration (unlimited fuel refill), or the
// dispatch-elimination win is eaten by ~143ns per-iteration boundary
// crossings. 1e6 iterations must produce only a tiny number of Safepoint
// crossings (unlimited refill = ~1<<30 iters per crossing).
func TestP3LoopBudget_UnbudgetedHotLoopRarelyCrosses(t *testing.T) {
	src := `local function f() local s=0 for i=1,1000000 do s=s+i end return s end return f()`
	prog, err := Compile([]byte(src), "hot")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := NewState(Options{})
	st.SetForceAllPromote(true) // force P3 so the loop runs in gibbous
	// warm-up run to promote, then a measured run
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("warmup run: %v", err)
	}
	before := st.SafepointCalls()
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res) != 1 || res[0].Display() != "500000500000" {
		t.Fatalf("wrong result: %v (want 500000500000)", res)
	}
	crossings := st.SafepointCalls() - before
	// GC may force a few crossings (gcPending), but a 1e6-iteration loop
	// must not cross anywhere near per-iteration. Allow generous slack for
	// GC-driven safepoints; the bug (cross-every-iteration) would be ~1e6.
	if crossings > 1000 {
		t.Errorf("unbudgeted hot loop crossed to Safepoint %d times over 1e6 iters "+
			"(fast path broken — should be near-zero via unlimited fuel refill)", crossings)
	}
	t.Logf("1e6-iter unbudgeted loop: %d Safepoint crossings (fast path intact)", crossings)
}

// TestP3LoopBudget_ArmAfterWarmRuns: arming a budget AFTER warm
// no-budget P3 runs must take effect on the next run (code-review
// finding: Safepoint refills loopFuelUnlimited while nothing is armed,
// and SetStepBudget only touches Go-side fields, so the stale 1<<30
// fuel word let a promoted in-segment loop run ~1e9 back-edges before
// consulting the new budget; enterGibbous now re-arms the fuel word on
// the armed-state transition).
func TestP3LoopBudget_ArmAfterWarmRuns(t *testing.T) {
	src := `function sum(n)local s=0 local i=0 while i<=n do X=0*i i=i+1 end return s end return sum(12)%sum(1000000)`
	prog, err := Compile([]byte(src), "arm")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := NewState(Options{MaxArenaBytes: 64 << 20})
	st.SetHotThresholds(2, 4)
	for run := 1; run <= 2; run++ {
		if _, err := prog.Run(st); err != nil {
			t.Fatalf("warm run %d: %v", run, err)
		}
	}
	if st.PromotionCount() == 0 || st.SafepointCalls() == 0 {
		t.Fatalf("harness broken: loop not promoted in-segment (promotions=%d safepoints=%d)",
			st.PromotionCount(), st.SafepointCalls())
	}
	st.SetStepBudget(10)
	if _, err := prog.Run(st); err == nil {
		t.Fatal("budget=10 armed after warm P3 runs did not raise — stale unlimited loop fuel")
	} else if err.Error() != `[string "arm"]:1: instruction budget exceeded` &&
		!strings.Contains(err.Error(), "instruction budget exceeded") {
		t.Fatalf("wrong error: %v", err)
	}
}

// TestP3LoopBudget_CancelHookAfterWarmRuns: same transition with a
// cancel context instead of a budget — a context canceled DURING a
// promoted in-segment loop must preempt it. The warm and long runs
// call the SAME loaded function (one Program, one State: `sum` is a
// global closure over one promoted Proto), and the canceled Run must
// itself cross the P3 back-edge safepoint (SafepointCalls delta > 0)
// — the first version of this test warmed a different Program and its
// canceled Run aborted at the interpreter's frame-entry preempt
// without ever entering P3, so it passed even with the loop-fuel
// re-arm logic deleted (code-review finding).
func TestP3LoopBudget_CancelHookAfterWarmRuns(t *testing.T) {
	// One Program: warm calls sum(100000), the long run calls sum(1e18)
	// via a global knob so both execute the SAME promoted Proto.
	src := `function sum(n)local s=0 local i=0 while i<=n do X=0*i i=i+1 end return s end return sum(12)%sum(N)`
	prog, err := Compile([]byte(src), "canc")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := NewState(Options{MaxArenaBytes: 64 << 20})
	st.SetHotThresholds(2, 4)
	st.SetGlobal("N", Number(100000))
	for run := 1; run <= 2; run++ {
		if _, err := prog.Run(st); err != nil {
			t.Fatalf("warm run %d: %v", run, err)
		}
	}
	if st.PromotionCount() == 0 || st.SafepointCalls() == 0 {
		t.Fatalf("harness broken: loop not promoted in-segment")
	}
	// Cancel mid-run: install a live context BEFORE the run, cancel it
	// from another goroutine once the run is underway. Frame-entry
	// preempt sees a live context at entry, so only the in-segment
	// back-edge safepoint can observe the later cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	st.SetContext(ctx)
	st.SetGlobal("N", Number(1e18))
	spBefore := st.SafepointCalls()
	done := make(chan error, 1)
	go func() { _, e := prog.Run(st); done <- e }()
	time.Sleep(50 * time.Millisecond) // let the run enter the promoted loop
	cancelAt := time.Now()
	cancel()
	select {
	case e := <-done:
		if e == nil || !strings.Contains(e.Error(), "context canceled") {
			t.Fatalf("want context-canceled error, got %v", e)
		}
		// Promptness: with the quantum refill armed, the cancellation
		// is observed within 64 back-edges (microseconds). Without the
		// re-arm fix the run drains the stale unlimited refill first —
		// ~1<<30 back-edges, empirically ~9s — before the safepoint
		// looks at the context. 5s cleanly separates the two even on a
		// slow CI runner.
		if waited := time.Since(cancelAt); waited > 5*time.Second {
			t.Fatalf("cancellation observed only after %v — the run drained a stale unlimited fuel window first", waited)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("canceled in-segment loop still running after 30s — cancellation not observed at the back-edge safepoint")
	}
	if delta := st.SafepointCalls() - spBefore; delta == 0 {
		t.Fatal("canceled run never crossed the P3 back-edge safepoint — the test did not exercise the in-segment path")
	}
}

// TestP3LoopBudget_CtxThenBudgetNoStaleBilling: with a live (never
// canceled) Context already armed — quantum refills in effect — arming
// a budget afterwards is a configuration CHANGE the old aggregate
// armed-boolean could not see. The partial drain accrued during the
// ctx-only phase (up to 64 back-edges) must be discarded, NOT billed
// to the brand-new budget: a follow-up call producing ~1 back-edge
// under budget=10 must succeed (code-review increment-3 finding 1: it
// raised "instruction budget exceeded" because the next Safepoint
// billed the ctx-phase drain into the fresh budget).
func TestP3LoopBudget_CtxThenBudgetNoStaleBilling(t *testing.T) {
	// Whether the stale billing trips a given budget depends on where in
	// the 64-back-edge quantum window the warm phase happens to stop, so
	// a single warm length could silently miss the bad phase. Scan warm
	// lengths across one full window width: with the drain correctly
	// discarded every phase passes; with stale billing at least one
	// phase in any 64-wide range bills a near-full window into the
	// fresh budget and trips it.
	src := `function sum(n)local s=0 local i=0 while i<=n do X=0*i i=i+1 end return s end return sum(12)%sum(N)`
	for warmN := 980; warmN < 980+64; warmN++ {
		prog, err := Compile([]byte(src), "cbst")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		st := NewState(Options{MaxArenaBytes: 64 << 20})
		st.SetHotThresholds(2, 4)
		st.SetContext(context.Background()) // armed from the start: quantum refills
		st.SetGlobal("N", Number(float64(warmN)))
		for run := 1; run <= 2; run++ {
			if _, err := prog.Run(st); err != nil {
				t.Fatalf("warmN=%d ctx-armed warm run %d: %v", warmN, run, err)
			}
		}
		if st.PromotionCount() == 0 || st.SafepointCalls() == 0 {
			t.Fatalf("harness broken: loop not promoted in-segment")
		}
		// Arm a tiny budget; the next call runs ~1 back-edge of its own.
		st.SetStepBudget(10)
		st.SetGlobal("N", Number(1))
		if _, err := prog.Run(st); err != nil {
			t.Fatalf("warmN=%d: ~1 post-arming back-edge tripped budget=10 — ctx-phase drain billed to the new budget: %v", warmN, err)
		}
	}
}

// TestP3LoopBudget_DisarmRestoresFastPath: dropping the budget after
// budgeted runs must restore the unlimited refill (the disarm leg of
// the enterGibbous transition), so an unbudgeted hot loop does not pay
// a Safepoint crossing every 64 back-edges forever after.
func TestP3LoopBudget_DisarmRestoresFastPath(t *testing.T) {
	src := `function sum(n)local s=0 local i=0 while i<=n do X=0*i i=i+1 end return s end return sum(12)%sum(1000000)`
	prog, err := Compile([]byte(src), "disarm")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := NewState(Options{MaxArenaBytes: 64 << 20})
	st.SetHotThresholds(2, 4)
	st.SetStepBudget(1 << 28)
	for run := 1; run <= 2; run++ {
		if _, err := prog.Run(st); err != nil {
			t.Fatalf("budgeted run %d: %v", run, err)
		}
	}
	if st.PromotionCount() == 0 {
		t.Fatalf("harness broken: loop not promoted")
	}
	st.SetStepBudget(0)
	before := st.SafepointCalls()
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("disarmed run: %v", err)
	}
	crossings := st.SafepointCalls() - before
	if crossings > 1000 {
		t.Errorf("disarmed 1M-iter loop crossed Safepoint %d times (quantum refill stuck; fast path not restored)", crossings)
	}
}

// TestP3LoopBudget_ArmDoesNotBillPreArmDrain: the back-edges consumed
// BEFORE arming must not be charged to the new budget. A budget large
// enough for the 1M-iteration loop must not spuriously trip because the
// warm runs' partial drain of the unlimited refill got billed at the
// first post-arm Safepoint crossing.
func TestP3LoopBudget_ArmDoesNotBillPreArmDrain(t *testing.T) {
	src := `function sum(n)local s=0 local i=0 while i<=n do X=0*i i=i+1 end return s end return sum(12)%sum(1000000)`
	prog, err := Compile([]byte(src), "nobill")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := NewState(Options{MaxArenaBytes: 64 << 20})
	st.SetHotThresholds(2, 4)
	for run := 1; run <= 2; run++ {
		if _, err := prog.Run(st); err != nil {
			t.Fatalf("warm run %d: %v", run, err)
		}
	}
	// ~1M back-edges per run plus interpreter-side steps; 1<<28 is ample
	// UNLESS the pre-arm drain (up to 1<<30) leaks into the billing.
	st.SetStepBudget(1 << 28)
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("ample budget tripped after arming — pre-arm drain billed to new budget: %v", err)
	}
}

// TestP3LoopBudget_CtxToggleDoesNotExtendBudget: a ctx set/replace/
// remove while a budget stays armed must NOT reset the fuel window
// (code-review increment-4: SetCancelHook bumped the shared budgetGen,
// so every toggle handed the segment a fresh unbilled quantum —
// budget=100 survived ~400 back-edges across 10 SetContext/
// RemoveContext toggles). The drain accrued before and after the
// toggle belongs to the same live budget and must accumulate into it.
func TestP3LoopBudget_CtxToggleDoesNotExtendBudget(t *testing.T) {
	src := `function sum(n)local s=0 local i=0 while i<=n do X=0*i i=i+1 end return s end return sum(12)%sum(N)`
	prog, err := Compile([]byte(src), "tog")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := NewState(Options{MaxArenaBytes: 64 << 20})
	st.SetHotThresholds(2, 4)
	st.SetGlobal("N", Number(1000))
	for run := 1; run <= 2; run++ {
		if _, err := prog.Run(st); err != nil {
			t.Fatalf("warm run %d: %v", run, err)
		}
	}
	if st.PromotionCount() == 0 {
		t.Fatal("harness broken: loop not promoted")
	}
	// budget=100; each round runs ~40 back-edges and toggles the ctx.
	// Without toggles the budget trips inside round 3; with the
	// increment-4 bug each toggle reset the window and all 10 rounds
	// (~400 back-edges) completed.
	st.SetStepBudget(100)
	spBefore := st.SafepointCalls()
	st.SetGlobal("N", Number(40))
	var raised error
	rounds := 0
	for round := 1; round <= 10; round++ {
		if round%2 == 1 {
			st.SetContext(context.Background())
		} else {
			st.RemoveContext()
		}
		rounds = round
		if _, err := prog.Run(st); err != nil {
			raised = err
			break
		}
	}
	if raised == nil {
		// With per-toggle window resets each ~40-back-edge round stays
		// under the fresh 64 quantum, so Safepoint is never crossed and
		// nothing is ever billed — the zero delta is the bug's
		// signature, not a harness problem.
		t.Fatalf("~400 back-edges under budget=100 never raised — ctx toggles keep resetting the fuel window (Safepoint delta=%d)",
			st.SafepointCalls()-spBefore)
	}
	if st.SafepointCalls() == spBefore {
		t.Fatal("harness broken: raised without ever crossing Safepoint")
	}
	if !strings.Contains(raised.Error(), "instruction budget exceeded") {
		t.Fatalf("wrong error: %v", raised)
	}
	if rounds > 5 {
		t.Errorf("budget=100 lasted %d rounds of ~40 back-edges — ctx toggles partially extend the quota", rounds)
	}
}
