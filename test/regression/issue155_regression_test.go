//go:build wangshu_p4 && wangshu_profile

// issue155_regression_test.go — issue #155: seg2seg in-segment RETURN
// teardown vs mixed-width RETURN callees.
//
// The seg2seg dispatch `call`s directly into a native callee segment and
// relies on emitReturnDualSemantics' in-segment teardown, which moves
// exactly nret = RETURN.B-1 values to the caller's R(A..) window. Unlike
// the interpreter's doReturn, it CANNOT nil-fill up to the caller's
// expected C-1 — the caller has no way to learn which RETURN site
// actually executed. A callee mixing `return` (0 values) with
// `return x, y` (2 values) left the caller reading stale registers
// (the callee closure!) as call results on the 0-value path.
//
// Fuzz crasher 0412a26ad22eaedf (nightly run 29571821156): the auto
// harness diverged on run 2 with P1="nil" vs auto="function".
//
// Fix: populateCallIC's NeverExits flag now also requires
// seg2SegRetWidthOK — a uniform reachable-RETURN count >= the CALL's
// C-1 (ProtoSeg2SegRetCount). Mixed-width callees stay on the
// exit-reason path whose host.DoReturn nil-fills like the interpreter.

package regression

import (
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

// TestIssue155_MixedReturnWidthByteEqual is the crasher shape: a
// recursive callee with a bare `return` on one path and a 2-value
// return on the other, called with C=2 (one result consumed). Run
// twice on one auto State (the miscompile only shows on run 2+ when
// the IC is warm and seg2seg engages).
func TestIssue155_MixedReturnWidthByteEqual(t *testing.T) {
	src := `local function fib(n)if n <2 then return  end return fib(n-1) ,(000)end  return fib(10)`
	prog, err := wangshu.Compile([]byte(src), "i155")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st1 := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	st1.SetStepBudget(1 << 20)
	st1.SetHotThresholds(^uint32(0), ^uint32(0))
	stA := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	stA.SetStepBudget(1 << 20)
	stA.SetHotThresholds(2, 4)
	for run := 1; run <= 3; run++ {
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

// TestIssue155_UniformRetStillSeg2Seg proves the gate does not
// over-reject: a uniform single-value-return recursive callee (fib)
// must still engage seg2seg (prove-the-path via SegToSegHitCount).
func TestIssue155_UniformRetStillSeg2Seg(t *testing.T) {
	src := `local function fib(n) if n < 2 then return n end return fib(n-1) + fib(n-2) end return fib(20)`
	prog, err := wangshu.Compile([]byte(src), "i155u")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	before := peroptranslator.SegToSegHitCount.Load()
	r, e := prog.Run(st)
	if e != nil {
		t.Fatalf("run: %v", e)
	}
	if r[0].Number() != 6765 {
		t.Fatalf("fib(20) = %s, want 6765", r[0].Display())
	}
	if delta := peroptranslator.SegToSegHitCount.Load() - before; delta == 0 {
		t.Fatal("seg2seg never engaged for uniform-ret fib — the #155 gate over-rejects")
	}
}
