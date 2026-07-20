// selftail_test.go — issue #112 regression: mono self-tail-calls loop
// in-segment instead of paying a per-level segment exit + Go re-entry.
//
// A PUC 5.1 tail call reuses the caller frame; when the callee IS the
// running closure, P4 collapses the whole HelperTailCall round trip
// (which made HeavyRecursion 12% SLOWER than the P1 interpreter) into
// an in-segment arg move + nil-fill + fuel-guarded jmp to the entry
// BB. These tests pin the three obligations: (1) the fast path really
// executes (SelfTailCallHitCount white-box probe — result equality
// alone can't distinguish it from the HelperTailCall fallback), (2)
// semantics stay byte-equal with the interpreter across the shapes the
// guard admits and rejects, (3) the new in-segment back edge bills the
// step budget (issue #102 loopFuel guard — an unmetered
// `function f() return f() end` would hang forever under a budget).

//go:build wangshu_p4 && wangshu_profile

package regression

import (
	"strings"
	"testing"
	"time"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

// runP1P4 compiles src and runs it on a fresh P1 State and TWICE on a
// force-all P4 State (promotion happens at first frame entry, so the
// second Run is the one that executes native code).
func runP1P4(t *testing.T, src string) (p1 []wangshu.Value, p4 []wangshu.Value, e1, e4 error) {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "i112")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st1 := wangshu.NewState(wangshu.Options{})
	p1, e1 = prog.Run(st1)
	st4 := wangshu.NewState(wangshu.Options{})
	st4.SetForceAllPromote(true)
	_, _ = prog.Run(st4)
	p4, e4 = prog.Run(st4)
	return
}

// TestI112_SelfTailLoopHits: the collatz shape from HeavyRecursion and
// an accumulator countdown both take the in-segment loop (probe > 0)
// and match the interpreter's results.
func TestI112_SelfTailLoopHits(t *testing.T) {
	cases := []struct{ name, src string }{
		{"collatz", `local function collatz(n, steps)
  if n == 1 then return steps end
  if n % 2 == 0 then return collatz(n / 2, steps + 1) end
  return collatz(3 * n + 1, steps + 1)
end
local total = 0
for i = 1, 200 do total = total + collatz(i, 0) end
return total`},
		{"countdownAcc", `local function f(n, acc) if n <= 0 then return acc end return f(n-1, acc+n) end return f(50000, 0)`},
		// Fewer args than params: nil-fill must cover the missing
		// param exactly like enterLuaFrame (b==1, nargs=0 < NumParams=1).
		{"argUnderflow", `local function f(n)
  if n == nil then return -1 end
  if n <= 0 then return 0 end
  if n == 5 then return f() end
  return f(n-1)
end
return f(10)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := peroptranslator.SelfTailCallHitCount.Load()
			p1, p4, e1, e4 := runP1P4(t, tc.src)
			if e1 != nil || e4 != nil {
				t.Fatalf("errors: P1=%v P4=%v", e1, e4)
			}
			if len(p1) != len(p4) || len(p1) == 0 || p1[0].Number() != p4[0].Number() {
				t.Fatalf("result divergence: P1=%v P4=%v", p1, p4)
			}
			if delta := peroptranslator.SelfTailCallHitCount.Load() - before; delta == 0 {
				t.Errorf("SelfTailCallHitCount did not increment — fast path not taken")
			}
		})
	}
}

// TestI112_GuardMissFallsBack: non-self tail callees must miss the
// identity guard and ride the unchanged HelperTailCall path
// (TailCallRunCount increments, SelfTailCallHitCount does not).
func TestI112_GuardMissFallsBack(t *testing.T) {
	src := `local function g(n) return n * 2 end
local function f(n) if n <= 0 then return 0 end return g(n) end
local s = 0
for i = 1, 20 do s = s + f(i) end
return s`
	self0 := peroptranslator.SelfTailCallHitCount.Load()
	tail0 := peroptranslator.TailCallRunCount.Load()
	p1, p4, e1, e4 := runP1P4(t, src)
	if e1 != nil || e4 != nil {
		t.Fatalf("errors: P1=%v P4=%v", e1, e4)
	}
	if p1[0].Number() != p4[0].Number() {
		t.Fatalf("result divergence: P1=%v P4=%v", p1, p4)
	}
	if delta := peroptranslator.SelfTailCallHitCount.Load() - self0; delta != 0 {
		t.Errorf("SelfTailCallHitCount incremented %d on a non-self tail call", delta)
	}
	if delta := peroptranslator.TailCallRunCount.Load() - tail0; delta == 0 {
		t.Errorf("TailCallRunCount did not increment — HelperTailCall fallback not exercised")
	}
}

// TestI112_BudgetPreemptsSelfTailLoop: the new in-segment back edge is
// an airtight loop — without the #102 loopFuel guard a budgeted
// `function f() return f() end` would hang forever (PUC proper tail
// calls are O(1) depth, no stack overflow rescues us).
func TestI112_BudgetPreemptsSelfTailLoop(t *testing.T) {
	src := `local function f() return f() end return f()`
	prog, err := wangshu.Compile([]byte(src), "i112b")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetStepBudget(1 << 20)
	st.SetForceAllPromote(true)
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := prog.Run(st)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "instruction budget exceeded") {
			t.Fatalf("want budget error, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("budget error too slow: %v", elapsed)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("hung — self-tail-call back edge not billing the budget")
	}
}

// TestI112_ClosureGateDeclines: a self-tail-recursive body that
// CREATES a closure over its locals must not loop in-segment —
// doTailCall's closeUpvals runs per tail call, and skipping it would
// leave one shared upvalue across iterations (every capture would see
// the last iteration's value). The compile-time no-CLOSURE gate keeps
// such protos on the HelperTailCall path; this pins both the gate
// (probe stays 0) and the semantics (each closure captures its own
// iteration's value, byte-equal with P1).
func TestI112_ClosureGateDeclines(t *testing.T) {
	src := `local acc = {}
local function f(n)
  if n <= 0 then return 0 end
  local v = n * 10
  acc[#acc+1] = function() return v end
  return f(n-1)
end
f(3)
return acc[1](), acc[2](), acc[3]()`
	self0 := peroptranslator.SelfTailCallHitCount.Load()
	p1, p4, e1, e4 := runP1P4(t, src)
	if e1 != nil || e4 != nil {
		t.Fatalf("errors: P1=%v P4=%v", e1, e4)
	}
	if len(p1) != 3 || len(p4) != 3 {
		t.Fatalf("want 3 results: P1=%v P4=%v", p1, p4)
	}
	for i := range p1 {
		if p1[i].Number() != p4[i].Number() {
			t.Fatalf("result %d divergence: P1=%v P4=%v", i, p1[i].Number(), p4[i].Number())
		}
	}
	if delta := peroptranslator.SelfTailCallHitCount.Load() - self0; delta != 0 {
		t.Errorf("SelfTailCallHitCount incremented %d — CLOSURE gate failed to decline", delta)
	}
}
