// deep_recursion_test.go — issue #85 regression: deep pure-Lua
// recursion on a PROMOTED proto must match the interpreter's depth
// semantics.
//
// The interpreter drives Lua→Lua recursion flat (one executeFrom loop
// walks the CallInfo chain; no Go stack per level), bounded only by
// maxLuaCallDepth (20000, PUC LUAI_MAXCALLS). The tiered path used to
// burn one nCcalls (maxCCallDepth = 200, PUC LUAI_MAXCCALLS) per call
// level past the seg2seg depth cap — a real Go re-entry chain
// (Run → dispatcher → ExecutePlainCallInlineFrame → enterGibbous →
// Run …) — so a Lua-legal depth-1000 recursion raised "C stack
// overflow" only when promoted (FuzzAutoPromote seed 41aacb7ebe17996d).
//
// The fix (gibbousReentryCCallCap watermark in frame.go) makes gibbous
// dispatch entry points fall back to the flat interpreter once nCcalls
// crosses the watermark, restoring maxLuaCallDepth as the only bound.

//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

package wangshu_test

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/Liam0205/wangshu"
)

// tieredStates returns fresh States for the promotion modes under test:
// auto (lowered natural-heat thresholds, the mode the fuzz crasher ran
// in) and force-all.
func tieredStates() map[string]*wangshu.State {
	auto := wangshu.NewState(wangshu.Options{})
	auto.SetHotThresholds(2, 4)
	force := wangshu.NewState(wangshu.Options{})
	force.SetForceAllPromote(true)
	return map[string]*wangshu.State{"auto": auto, "force": force}
}

// TestI85_DeepRecursionPromoted is the minimized crasher shape: depth-N
// self-recursion with no accumulation, run twice on the same State (run
// 1 promotes mid-run; run 2 is tiered from the first call). Depth 1000
// is far below maxLuaCallDepth, so any error is a regression. Depth 300
// additionally pins the seg2seg + watermark interplay on P4 (the cap
// absorbs the first segToSegDepthCap levels; the rest must survive the
// Go re-entry budget).
func TestI85_DeepRecursionPromoted(t *testing.T) {
	for _, depth := range []int{300, 1000} {
		src := fmt.Sprintf(
			`local function fib(n)if n<0 then return end fib(n-2)end fib(%d)`, depth)
		for mode, st := range tieredStates() {
			prog, err := wangshu.Compile([]byte(src), "i85")
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			for run := 1; run <= 2; run++ {
				if _, err := prog.Run(st); err != nil {
					t.Errorf("depth=%d %s run %d: unexpected error %v (interpreter succeeds; issue #85)",
						depth, mode, run, err)
				}
			}
		}
	}
}

// TestI85_DeepRecursionAccumulates guards result correctness (not just
// error existence) across the native→interpreter watermark switch: the
// value must be computed identically no matter where the fallback cut
// the descent. s(n) = n + s(n-1), s(2000) = 2001000.
func TestI85_DeepRecursionAccumulates(t *testing.T) {
	src := `local function s(n)if n<=0 then return 0 end return n+s(n-1)end return s(2000)`
	const want = "2001000"
	for mode, st := range tieredStates() {
		prog, err := wangshu.Compile([]byte(src), "i85")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		for run := 1; run <= 2; run++ {
			res, err := prog.Run(st)
			if err != nil {
				t.Fatalf("%s run %d: %v", mode, run, err)
			}
			if len(res) != 1 || res[0].Display() != want {
				t.Errorf("%s run %d: got %v, want [%s]", mode, run, res, want)
			}
		}
	}
}

// TestI85_LuaCallDepthBoundaryParity pins the OTHER side of the depth
// contract: past maxLuaCallDepth the promoted path must still raise
// "stack overflow" exactly like the interpreter (the watermark fallback
// must not lift the Lua-frame bound).
func TestI85_LuaCallDepthBoundaryParity(t *testing.T) {
	src := `local function down(n)if n<=0 then return 0 end down(n-1)end down(21000)`
	for mode, st := range tieredStates() {
		prog, err := wangshu.Compile([]byte(src), "i85")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		for run := 1; run <= 2; run++ {
			_, err := prog.Run(st)
			if err == nil || !strings.Contains(err.Error(), "stack overflow") ||
				strings.Contains(err.Error(), "C stack overflow") {
				t.Errorf("%s run %d: want plain \"stack overflow\", got %v", mode, run, err)
			}
		}
	}
}

// TestI85_ProperTailRecursionPromoted: proper tail calls are O(1) depth
// in PUC 5.1 (frame reuse) — depth 50000 exceeds BOTH caps and must
// still succeed on the promoted path (the gibbous TAILCALL dispatch in
// executeFrom re-enters Go per level, so it rides the same watermark).
func TestI85_ProperTailRecursionPromoted(t *testing.T) {
	src := `local function loop(n)if n<=0 then return 0 end return loop(n-1)end return loop(50000)`
	for mode, st := range tieredStates() {
		prog, err := wangshu.Compile([]byte(src), "i85")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		for run := 1; run <= 2; run++ {
			res, err := prog.Run(st)
			if err != nil {
				t.Fatalf("%s run %d: %v", mode, run, err)
			}
			if len(res) != 1 || res[0].Display() != "0" {
				t.Errorf("%s run %d: got %v, want [0]", mode, run, res)
			}
		}
	}
}

// TestI86_DeepRecursionGCStress pins the seg2seg NOSPLIT-window stack
// overflow (FuzzAutoPromote seed 7f161a85c466adbf, PR #86): the mmap
// segment's per-level `sub sp` is invisible to Go's nosplit accounting,
// so with segToSegDepthCap past the ~800 B NOSPLIT allowance the native
// descent silently underran the goroutine stack and corrupted adjacent
// heap objects — surfacing later as a GC "found pointer to free object"
// abort. The crasher shape recurses to maxLuaCallDepth (the fib(88886)
// branch never bottoms out) under a live native re-entry chain, then
// unwinds via "stack overflow"; iterating with forced GCs makes the
// corruption deterministic at GC percent 1 (set below so the guard is
// self-contained; at default GOGC the repro is only probabilistic —
// though still effective enough that the fuzz smoke hit it in seconds).
func TestI86_DeepRecursionGCStress(t *testing.T) {
	prev := debug.SetGCPercent(1)
	t.Cleanup(func() { debug.SetGCPercent(prev) })
	// Exact FuzzAutoPromote harness shape: the interpreter baseline
	// State runs interleaved with the auto State — the extra allocation
	// traffic is what positions the goroutine SP near the stack guard
	// when the native descent begins (without it the corruption window
	// rarely lines up, even at the broken cap).
	src := `local function fib(n)if n<0 then return 0 end return fib(n-1)+fib(88888-2) end  return fib(70)`
	for iter := 0; iter < 20; iter++ {
		prog, err := wangshu.Compile([]byte(src), "i86")
		if err != nil {
			t.Fatal(err)
		}
		st1 := wangshu.NewState(wangshu.Options{})
		st1.SetStepBudget(1 << 20)
		st1.SetHotThresholds(^uint32(0), ^uint32(0))
		stA := wangshu.NewState(wangshu.Options{})
		stA.SetStepBudget(1 << 20)
		stA.SetHotThresholds(2, 4)
		for run := 1; run <= 2; run++ {
			// Both runs raise "stack overflow" (Lua-legal outcome);
			// the assertion is the process surviving GC afterwards.
			_, _ = prog.Run(st1)
			_, _ = prog.Run(stA)
		}
		runtime.GC()
	}
}

// TestI89_BudgetPreemptsInSegment pins the seg2seg dispatch-fuel guard
// (fuzz crasher f2165a93dd62892d, PR #95): with segToSegDepthCap back at
// 128 (issue #89 spill stack), a deep fib call tree runs subtrees of
// depth <=cap entirely in-segment — ~phi^cap calls with no Go re-entry.
// The step budget bills synchronously in st.preempt() at call/back-edge
// points, ALL of which the in-segment dispatch bypasses, so fib(5510)
// under SetStepBudget hung effectively forever while the interpreter
// erred in 50ms. The segCallFuel guard makes the fast body fall back to
// the host path every SegCallFuelBudgeted dispatches when a budget or
// cancel context is armed, restoring the billing points (and charging
// the spent dispatches to the budget).
func TestI89_BudgetPreemptsInSegment(t *testing.T) {
	src := `local function fib(n) if n < 2 then return n end return fib(n-1) + fib(n-2) end; return fib(5510)`
	for _, tiered := range []bool{false, true} {
		prog, err := wangshu.Compile([]byte(src), "i89")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		st := wangshu.NewState(wangshu.Options{})
		st.SetStepBudget(1 << 20)
		if tiered {
			st.SetHotThresholds(2, 4)
		} else {
			st.SetHotThresholds(^uint32(0), ^uint32(0))
		}
		for run := 1; run <= 2; run++ {
			done := make(chan error, 1)
			go func() {
				_, err := prog.Run(st)
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "instruction budget exceeded") {
					t.Errorf("tiered=%v run %d: want budget error, got %v", tiered, run, err)
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("tiered=%v run %d: hung — in-segment dispatch not billing the budget", tiered, run)
			}
		}
	}
}
