//go:build wangshu_p4 && wangshu_profile

// issue177_regression_test.go — issue #177: P4 shape-template FORLOOP
// deopt reads stale slots when the reg-limit is a coercible non-number.
//
// Shape: `function sum(n) for A=0,n do end end sum "7"` — a MOVE-limit
// empty-body FORLOOP whose limit at run time is a Lua-numeric STRING
// ("7"). The fast-path template bakes init/step as imm64 into the machine
// code and reads limit via `movsd xmm1, [rbx+limitReg*8]`; NONE of R(A),
// R(A+1), R(A+2) is spilled by the template. On IsNumber-guard miss the
// deopt path called host.ForPrep, which reads those three slots via ci.
// base — they were Nil, so ForPrep reported "'for' initial value must be
// a number" for a script where the interpreter would coerce "7" to 7 and
// run cleanly. Auto run 2 tripped the divergence (P1=<nil>, P4=raise);
// run 1 stayed on the interpreter and passed.
//
// Fix: p4Code carries the burnt-in initK/stepK on the deopt path and
// restores R(A)/R(A+1)/R(A+2) to interpreter shape (initK / GetReg(
// limitReg) / stepK) before calling host.ForPrep. host.ForPrep then sees
// live values and can toNumberCoerce the string limit as the interpreter
// does. Corpus 8305a8ceb22b8f41 is retained in testdata/fuzz/
// FuzzAutoPromote/ for continuous coverage.

package regression

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestIssue177_StringLimitAutoRun2 is the exact crasher shape: with
// auto-promote (natural thresholds) run 2 lands on the shape-template
// deopt path because the limit is the string "7". The interpreter run 1
// must produce the same result as tiered run 2 (both empty tuple after
// the for-loop exits at iteration limit).
func TestIssue177_StringLimitAutoRun2(t *testing.T) {
	src := `function sum(n) for A=0,n do end end return sum "7"`
	prog, err := wangshu.Compile([]byte(src), "i177")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// Interpreter baseline: thresholds unreachable, sum stays on P1.
	st1 := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	st1.SetStepBudget(1 << 20)
	st1.SetHotThresholds(^uint32(0), ^uint32(0))
	// Auto path: lowered thresholds so run 1 promotes sum mid-flight and
	// run 2 enters the tiered shape template (which then deopts on the
	// string limit).
	stA := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	stA.SetStepBudget(1 << 20)
	stA.SetHotThresholds(2, 4)
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
}

// TestIssue177_TrueNonNumberLimitStillRaises pins that the deopt-side
// restoration does NOT swallow the genuine "limit must be a number"
// error: a limit that cannot be coerced (a table) must raise, byte-equal
// to the interpreter. The fix restores R(A+1) with the raw NaN-box of
// the limit slot, so host.ForPrep's toNumberCoerce sees the same non-
// coercible value the interpreter would and picks the same slot's error
// message ("limit", not "initial value").
func TestIssue177_TrueNonNumberLimitStillRaises(t *testing.T) {
	src := `function sum(n) for A=0,n do end end return sum({})`
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
}
