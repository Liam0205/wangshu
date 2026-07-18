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
// cancel context instead of a budget — SetContext after warm runs must
// preempt the promoted loop.
func TestP3LoopBudget_CancelHookAfterWarmRuns(t *testing.T) {
	src := `function sum(n)local s=0 local i=0 while i<=n do X=0*i i=i+1 end return s end return sum(12)%sum(1000000000)`
	prog, err := Compile([]byte(src), "canc")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	warm, err := Compile([]byte(`function sum(n)local s=0 local i=0 while i<=n do X=0*i i=i+1 end return s end return sum(12)%sum(100000)`), "canc")
	if err != nil {
		t.Fatalf("compile warm: %v", err)
	}
	st := NewState(Options{MaxArenaBytes: 64 << 20})
	st.SetHotThresholds(2, 4)
	for run := 1; run <= 2; run++ {
		if _, err := warm.Run(st); err != nil {
			t.Fatalf("warm run %d: %v", run, err)
		}
	}
	if st.PromotionCount() == 0 || st.SafepointCalls() == 0 {
		t.Fatalf("harness broken: loop not promoted in-segment")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled: the 1e9-iteration run must abort promptly
	st.SetContext(ctx)
	done := make(chan error, 1)
	go func() { _, e := prog.Run(st); done <- e }()
	select {
	case e := <-done:
		if e == nil || !strings.Contains(e.Error(), "context canceled") {
			t.Fatalf("want context-canceled error, got %v", e)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("canceled 1e9-iteration promoted loop still running after 30s — stale unlimited loop fuel")
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
