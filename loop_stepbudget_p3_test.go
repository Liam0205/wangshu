//go:build wangshu_p3 && wangshu_profile

// loop_stepbudget_p3_test.go — P3 gibbous loop back-edge step-budget
// accounting (the P3 dual of issue #102's P4 loop fuel). A fully-inline
// promoted loop back-edge (FORLOOP) must charge the step budget so an
// infinite loop raises "instruction budget exceeded" byte-equal to the
// interpreter, instead of hanging. Nightly oracle fuzz corpus
// bb525447c652d8d9: `for i=0,5/0 do X=0*i end` hung under P3.
package wangshu

import "testing"

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
