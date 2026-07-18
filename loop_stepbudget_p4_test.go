//go:build wangshu_p4 && wangshu_profile

// loop_stepbudget_p4_test.go — P4 PJ3 spec FORLOOP step-budget
// accounting regression tests (issue #143: the 3rd billing-gap instance
// after #102 P4 native emit and #135 P3 wasm). A PJ3 spec FORLOOP
// template that runs an in-segment loop must charge the step budget at
// back-edges so an infinite loop raises "instruction budget exceeded"
// byte-equal to the interpreter, instead of hanging.

package wangshu_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestP4LoopBudget_InfiniteForRaises is the #143 regression test: the PJ3
// empty-body FORLOOP template with limit=inf must raise the budget error,
// byte-equal to the interpreter. Before the fix, `for i=0,n do end` with
// n=inf hung forever on P4 because the template had no loopFuel
// decrement.
func TestP4LoopBudget_InfiniteForRaises(t *testing.T) {
	src := `function sum(n)for i=0,n do end return end return sum(12)%sum(5/0)`
	prog, err := wangshu.Compile([]byte(src), "lb4")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// interpreter baseline
	st1 := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	st1.SetStepBudget(1 << 20)
	_, e1 := prog.Run(st1)
	if e1 == nil {
		t.Fatalf("interpreter did not raise on infinite loop")
	}
	if !strings.Contains(e1.Error(), "instruction budget exceeded") {
		t.Fatalf("interpreter error %q does not contain 'instruction budget exceeded'", e1.Error())
	}
	// P4 auto-promote (two runs so the loop is promoted mid-flight)
	stA := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
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
		t.Fatalf("P4 did not raise on infinite loop (PJ3 back-edge not charging budget)")
	}
	if !strings.Contains(eA.Error(), "instruction budget exceeded") {
		t.Errorf("P4 error %q does not contain 'instruction budget exceeded'", eA.Error())
	}
}

// TestP4LoopBudget_RegLimitInfRaises: the PJ3 reg-limit template (the
// `for i=1,n` form) with n=inf must also raise.
func TestP4LoopBudget_RegLimitInfRaises(t *testing.T) {
	src := `function f(n) for i=1,n do end end f(10) return f(1/0)`
	prog, err := wangshu.Compile([]byte(src), "rl4")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st1 := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	st1.SetStepBudget(1 << 20)
	_, e1 := prog.Run(st1)
	if e1 == nil {
		t.Fatalf("interpreter did not raise")
	}
	stA := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
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
		t.Fatalf("P4 reg-limit did not raise on inf limit")
	}
}

// TestP4LoopBudget_BodyInfRaises: the PJ3 body template (`local s=0;
// for i=1,n do s=s+1 end`) with n=inf must raise.
func TestP4LoopBudget_BodyInfRaises(t *testing.T) {
	src := `function f(n) local s=0 for i=1,n do s=s+1 end return s end f(10) return f(1/0)`
	prog, err := wangshu.Compile([]byte(src), "bd4")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st1 := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	st1.SetStepBudget(1 << 20)
	_, e1 := prog.Run(st1)
	if e1 == nil {
		t.Fatalf("interpreter did not raise")
	}
	stA := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
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
		t.Fatalf("P4 body template did not raise on inf limit")
	}
}

// TestP4LoopBudget_FiniteLoopResultCorrect: a finite loop must still
// produce the correct result (the loopFuel machinery must not break the
// normal-exit path or the accumulator). Uses the body template shape
// (`local s=0; for i=1,100 do s=s+1 end; return s` → 100).
func TestP4LoopBudget_FiniteLoopResultCorrect(t *testing.T) {
	src := `function f() local s=0 for i=1,100 do s=s+1 end return s end return f()`
	prog, err := wangshu.Compile([]byte(src), "fin4")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	stA := wangshu.NewState(wangshu.Options{})
	stA.SetStepBudget(1 << 28) // large enough
	stA.SetHotThresholds(2, 4)
	var res []wangshu.Value
	var eA error
	for run := 1; run <= 3; run++ {
		res, eA = prog.Run(stA)
	}
	if eA != nil {
		t.Fatalf("P4 finite loop raised: %v", eA)
	}
	if len(res) != 1 || res[0].Number() != 100 {
		t.Errorf("wrong result: %v (want 100)", res)
	}
}

// TestP4LoopBudget_FuelResumeCorrectness: a budgeted State running a
// long but finite loop must produce the same result as the interpreter.
// This proves that the xmm spill/reload across LoopPreempt round trips
// preserves the induction variable correctly (idx, limit, step all
// survive). Uses a loop long enough to cross the SegCallFuelBudgeted
// (4096) boundary multiple times.
func TestP4LoopBudget_FuelResumeCorrectness(t *testing.T) {
	src := `function f() local s=0 for i=1,50000 do s=s+i end return s end return f()`
	prog, err := wangshu.Compile([]byte(src), "res4")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// interpreter
	st1 := wangshu.NewState(wangshu.Options{})
	st1.SetStepBudget(1 << 28)
	r1, e1 := prog.Run(st1)
	if e1 != nil {
		t.Fatalf("interp: %v", e1)
	}
	// P4
	stA := wangshu.NewState(wangshu.Options{})
	stA.SetStepBudget(1 << 28)
	stA.SetHotThresholds(2, 4)
	var rA []wangshu.Value
	var eA error
	for run := 1; run <= 3; run++ {
		rA, eA = prog.Run(stA)
	}
	if eA != nil {
		t.Fatalf("P4: %v", eA)
	}
	if len(r1) != 1 || len(rA) != 1 {
		t.Fatalf("result count: interp=%d P4=%d", len(r1), len(rA))
	}
	if r1[0].Display() != rA[0].Display() {
		t.Errorf("P4 vs interp mismatch: P4=%s interp=%s", rA[0].Display(), r1[0].Display())
	}
}

// TestP4LoopBudget_CtxThenBudgetNoStaleBilling is the P4 sibling of the
// P3 test of the same name (code-review increment-3 finding 1,
// probe-confirmed on P4 too): with a live Context already armed —
// budgeted quantum (4096) refills in effect — arming a budget later is
// a configuration change the old aggregate armed-boolean could not
// see, and the partial drain accrued during the ctx-only phase was
// billed by the next LoopPreempt/RefreshJitCtxAddrs into the fresh
// budget. Scan warm lengths across a whole quantum window so at least
// one phase exposes stale billing regardless of where the warm phase
// stops in the window.
func TestP4LoopBudget_CtxThenBudgetNoStaleBilling(t *testing.T) {
	src := `function sum(n)local s=0 for i=0,n do end return s end return sum(N)`
	// Budget 3000 fits the tiny run's ~2500 back-edges comfortably;
	// only a stale ctx-phase drain (window of 4096) can trip it.
	const budget = 3000
	const tinyN = 2500
	for warmN := 6000; warmN < 6000+4096; warmN += 256 {
		prog, err := wangshu.Compile([]byte(src), "cb4")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
		st.SetHotThresholds(2, 4)
		st.SetContext(context.Background()) // armed from the start
		st.SetGlobal("N", wangshu.Number(float64(warmN)))
		for run := 1; run <= 2; run++ {
			if _, err := prog.Run(st); err != nil {
				t.Fatalf("warmN=%d ctx-armed warm run %d: %v", warmN, run, err)
			}
		}
		if st.PromotionCount() == 0 {
			t.Fatalf("harness broken: loop not promoted")
		}
		st.SetStepBudget(budget)
		st.SetGlobal("N", wangshu.Number(tinyN))
		if _, err := prog.Run(st); err != nil {
			t.Fatalf("warmN=%d: ~%d post-arming back-edges tripped budget=%d — ctx-phase drain billed to the new budget: %v",
				warmN, tinyN, budget, err)
		}
	}
}
