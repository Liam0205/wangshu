//go:build wangshu_p4 && wangshu_profile

// issue177_regression_test.go — issue #177: PJ3 reg-limit FORLOOP
// shape-template deopt path used to read stale R(A)/R(A+1)/R(A+2)
// slots via host.ForPrep. The fast-path template baked init/step
// into imm64 and never spilled the slots, so on deopt ForPrep saw
// Nil for R(A) and misreported "'for' initial value" for a script
// like `for A=0,n do end` where n was the Lua-numeric string "7".
//
// Fix (ae44621): p4Code carries the burnt-in initK/stepK, and the
// forLoopDeopt branch restores R(A)/R(A+1)/R(A+2) to interpreter
// shape before host.ForPrep, letting toNumberCoerce see live values.
//
// The three prove-the-path probes below make the fix explicit:
//   - PromotionCount() > 0            — sum promoted to P4
//   - jit.SpecForLoopHits() > 0       — the PJ3 FORLOOP template was
//                                       Compile-emitted
//   - jit.SpecForLoopDeoptHits() > 0  — the reg-limit deopt branch in
//                                       code.go actually fired at runtime
//                                       (this is the counter the fix's
//                                       restoration lives inside)

package regression

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Liam0205/wangshu"
	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
)

// TestIssue177_StringLimitAutoRun2 is the exact crasher shape from
// corpus 8305a8ceb22b8f41. Run 1 promotes sum on the interpreter,
// run 2 lands on the PJ3 reg-limit template, the IsNumber guard
// misses on the string "7" and the code enters the forLoopDeopt
// branch. The three probes together prove the path: PromotionCount
// > 0 (proto reached P4), SpecForLoopHits > 0 (template emitted),
// SpecForLoopDeoptHits > 0 (deopt branch actually ran).
func TestIssue177_StringLimitAutoRun2(t *testing.T) {
	jit.ResetSpecHits()
	// Corpus 8305a8ceb22b8f41 verbatim (no `return`, no whitespace); adding
	// `return` in front changes the top-level RETURN B and shifts pc's so
	// analyzeForLoopForm no longer matches the expected shape.
	src := `function sum(n)for A=0,n do end end sum"7"`
	prog, err := wangshu.Compile([]byte(src), "i177")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// Interpreter baseline: thresholds unreachable, sum stays on P1.
	st1 := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	st1.SetStepBudget(1 << 20)
	st1.SetHotThresholds(^uint32(0), ^uint32(0))
	// Auto path: lowered thresholds so run 1 promotes sum mid-flight
	// and run 2 enters the tiered shape template.
	stA := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	stA.SetStepBudget(1 << 20)
	stA.SetHotThresholds(2, 4)
	promoBefore := stA.PromotionCount()
	for run := 1; run <= 2; run++ {
		r1, e1 := prog.Run(st1)
		rA, eA := prog.Run(stA)
		if (e1 == nil) != (eA == nil) {
			t.Fatalf("run %d: error divergence P1=%v auto=%v", run, e1, eA)
		}
		if e1 != nil {
			continue
		}
		if len(r1) != len(rA) {
			t.Fatalf("run %d: result count P1=%d auto=%d", run, len(r1), len(rA))
		}
		for i := range r1 {
			if r1[i].Display() != rA[i].Display() {
				t.Errorf("run %d: result[%d] P1=%q auto=%q",
					run, i, r1[i].Display(), rA[i].Display())
			}
		}
	}
	// Prove-the-path: sum's proto must have promoted to P4 and the
	// PJ3 FORLOOP template must have been emitted. The reg-limit
	// deopt branch specifically must have fired at least once — this
	// is what pins the fix (without the SetReg restoration, ForPrep
	// would raise on Nil slots and the divergence check above would
	// have caught it, but the counter makes the path explicit).
	if got := stA.PromotionCount() - promoBefore; got == 0 {
		t.Errorf("PromotionCount delta = 0, want > 0 (sum did not promote to P4)")
	}
	if got := jit.SpecForLoopHits(); got == 0 {
		t.Errorf("SpecForLoopHits = 0, want > 0 (PJ3 FORLOOP template not emitted)")
	}
	if got := jit.SpecForLoopDeoptHits(); got == 0 {
		t.Errorf("SpecForLoopDeoptHits = 0, want > 0 (reg-limit deopt branch did not fire)")
	}
}

// TestIssue177_StringLimitStepBudgetExceeded pins the deopt path
// still bills step budget per iteration (external review BLOCKER on
// the initial fix: host.ForPrep was followed by an immediate
// DoReturn, skipping the loop entirely — a large-limit coercible
// string that P1 would burn budget on returned instantly under P4).
func TestIssue177_StringLimitStepBudgetExceeded(t *testing.T) {
	jit.ResetSpecHits()
	// Empty-body loop with one million iterations against a 4096-
	// step budget — the budget trips regardless of P1-vs-P4 pre-loop
	// overhead accounting differences. The warm-up promotes sum
	// before the terminal string-limit call enters the shape
	// template deopt path.
	src := `function sum(n) for i=1,n do end end
for i=1,5 do sum(1) end
sum "1000000"`
	prog, err := wangshu.Compile([]byte(src), "i177budget")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st1 := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	st1.SetStepBudget(4096)
	st1.SetHotThresholds(^uint32(0), ^uint32(0))
	stA := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	stA.SetStepBudget(4096)
	stA.SetHotThresholds(2, 4)
	promoBefore := stA.PromotionCount()
	_, e1 := prog.Run(st1)
	_, eA := prog.Run(stA)
	if e1 == nil {
		t.Fatalf("interpreter did not raise on 4096-step budget with 1e6 iterations")
	}
	if eA == nil {
		t.Fatalf("auto path did not raise on 4096-step budget with 1e6 iterations (loop was skipped — regression to pre-fix behavior)")
	}
	if !strings.Contains(e1.Error(), "instruction budget exceeded") {
		t.Fatalf("interpreter error unexpected: %v", e1)
	}
	if !strings.Contains(eA.Error(), "instruction budget exceeded") {
		t.Errorf("auto path error is not the budget error (loop iterations may be skipped):\n  P1:   %q\n  auto: %q",
			e1.Error(), eA.Error())
	}
	if got := stA.PromotionCount() - promoBefore; got == 0 {
		t.Errorf("PromotionCount delta = 0, want > 0")
	}
	if got := jit.SpecForLoopDeoptHits(); got == 0 {
		t.Errorf("SpecForLoopDeoptHits = 0, want > 0")
	}
}

// TestIssue177_StringLimitContextCanceled pins the deopt path still
// probes the cancel context per iteration (companion to the step-
// budget case: preempt() checks both, so a skipped iteration
// bypasses both).
func TestIssue177_StringLimitContextCanceled(t *testing.T) {
	jit.ResetSpecHits()
	src := `function sum(n) for i=1,n do end end
for i=1,5 do sum(1) end
sum "1000000"`
	prog, err := wangshu.Compile([]byte(src), "i177ctx")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	runOne := func(auto bool) error {
		st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
		st.SetStepBudget(1 << 30) // large budget so cancel wins the race
		if auto {
			st.SetHotThresholds(2, 4)
		} else {
			st.SetHotThresholds(^uint32(0), ^uint32(0))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		defer cancel()
		st.SetContext(ctx)
		_, err := prog.Run(st)
		return err
	}
	e1 := runOne(false)
	eA := runOne(true)
	if e1 == nil {
		t.Fatalf("interpreter did not raise before its 5ms context expired")
	}
	if eA == nil {
		t.Fatalf("auto path did not raise before its 5ms context expired (loop skipped — regression to pre-fix behavior)")
	}
	if !strings.Contains(e1.Error(), "context canceled") {
		t.Fatalf("interpreter error unexpected: %v", e1)
	}
	if !strings.Contains(eA.Error(), "context canceled") {
		t.Errorf("auto path error is not the context cancel (loop iterations may be skipped):\n  P1:   %q\n  auto: %q",
			e1.Error(), eA.Error())
	}
}

// TestIssue177_TrueNonNumberLimitStillRaises pins that the deopt-
// side restoration does NOT swallow the genuine "limit must be a
// number" error: a limit that cannot be coerced (a table) must
// raise byte-equal to the interpreter. The fix restores R(A+1) with
// the raw NaN-box of the limit slot, so host.ForPrep's
// toNumberCoerce sees the same non-coercible value the interpreter
// would and picks the same slot's error message ("limit", not
// "initial value").
func TestIssue177_TrueNonNumberLimitStillRaises(t *testing.T) {
	jit.ResetSpecHits()
	// Warm-up + trigger shape: the first five sum(i) calls promote sum
	// to P4 without erroring (numeric limit → IsNumber guard passes,
	// empty body exits at once). The sixth call is sum({}) — sum has
	// already been P4-compiled, so this call enters the shape template
	// and takes the reg-limit deopt branch when the IsNumber guard
	// misses on the table. host.ForPrep must then see the restored
	// R(A+1)=table and pick the "limit" slot's error, byte-equal to
	// the interpreter. Sending sum({}) on a cold proto would just
	// raise on the interpreter path with no P4 involvement.
	src := `function sum(n)for A=0,n do end end
for i=1,5 do sum(i) end
sum({})`
	prog, err := wangshu.Compile([]byte(src), "i177raise")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st1 := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	st1.SetStepBudget(1 << 20)
	st1.SetHotThresholds(^uint32(0), ^uint32(0))
	stA := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	stA.SetStepBudget(1 << 20)
	stA.SetHotThresholds(2, 4)
	promoBefore := stA.PromotionCount()
	for run := 1; run <= 2; run++ {
		_, e1 := prog.Run(st1)
		_, eA := prog.Run(stA)
		if e1 == nil {
			t.Fatalf("run %d: interpreter did not raise on table limit", run)
		}
		if eA == nil {
			t.Fatalf("run %d: tiered did not raise on table limit", run)
		}
		if e1.Error() != eA.Error() {
			t.Errorf("run %d: error mismatch\n  P1:   %q\n  auto: %q", run, e1.Error(), eA.Error())
		}
	}
	// Prove-the-path (mirrors the string-limit case): sum's proto
	// must have promoted and the FORLOOP template must have been
	// emitted, and the deopt branch must have taken the restoration
	// path so host.ForPrep sees the table in R(A+1) and reports the
	// "limit" slot's error rather than "initial value".
	if got := stA.PromotionCount() - promoBefore; got == 0 {
		t.Errorf("PromotionCount delta = 0, want > 0 (sum did not promote to P4)")
	}
	if got := jit.SpecForLoopHits(); got == 0 {
		t.Errorf("SpecForLoopHits = 0, want > 0 (PJ3 FORLOOP template not emitted)")
	}
	if got := jit.SpecForLoopDeoptHits(); got == 0 {
		t.Errorf("SpecForLoopDeoptHits = 0, want > 0 (reg-limit deopt branch did not fire)")
	}
}
