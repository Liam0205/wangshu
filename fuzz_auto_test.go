// fuzz_auto_test.go — AUTO-mode promotion fuzz harness (issue: CI
// fuzzed P3/P4 through force-all only; production runs auto).
//
// FuzzP4ForceAllPromote / the P3 difftest suites drive promotion with
// SetForceAllPromote(true), which bypasses the natural-heat half of
// the bridge: threshold crossing MID-RUN, recheckCompilabilityRuntime
// on the natural path, PromotionGater, and the short-proto floor +
// FloorExempter (all auto-only; issue #67 was an auto-only bug). This
// harness fuzzes that half: same P1-vs-tiered differential shape, but
// the tiered State promotes via lowered natural-heat thresholds
// (SetHotThresholds(2, 4)) instead of force. Each input is Run TWICE
// on the same tiered State — run 1 crosses thresholds and promotes
// mid-run, run 2 executes tier-mixed from the first call — and each
// run must byte-equal the interpreter result.
//
// The threshold override changes only WHEN the auto decision runs;
// the decision chain itself (capability recheck, profitability gate,
// floor, exemption) is production code exercised unchanged.

//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// FuzzAutoPromote: arbitrary sources must not panic and must stay
// byte-equal between the never-promoting interpreter path and the
// auto-promoting tiered path (P3 or P4, per build tag), including
// across the mid-run promotion flip.
func FuzzAutoPromote(f *testing.F) {
	// Seeds bias toward shapes that CROSS the lowered thresholds
	// (repeat-called kernels, loops) plus decline shapes (vararg,
	// metamethods) so the promote-some-decline-some mix is explored.
	seeds := []string{
		`local function f(x) return x * 2 + 1 end; local s = 0; for i = 1, 10 do s = s + f(i) end; return s`,
		`local function sum(n) local s = 0; for i = 1, n do s = s + i end; return s end; return sum(10) + sum(20)`,
		`local t = {x = 1}; local function g(tt) return tt.x end; local s = 0; for i = 1, 8 do s = s + g(t) end; return s`,
		`local a = {1, 2, 3}; local function p(arr, i) return arr[i] end; local s = 0; for i = 1, 9 do s = s + p(a, (i % 3) + 1) end; return s`,
		`local o = {n = 0}; function o:add(x) self.n = self.n + x; return self.n end; local r = 0; for i = 1, 8 do r = o:add(i) end; return r`,
		`local function fib(n) if n < 2 then return n end return fib(n-1) + fib(n-2) end; return fib(10)`,
		`local function make() local c = 0; return function() c = c + 1; return c end end; local f1 = make(); return f1() + f1() + f1()`,
		`local function tiny(x) return x + 1 end; local s = 0; for i = 1, 8 do s = s + tiny(i) end; return s`,
		`local function v(...) local a, b = ...; return (a or 0) + (b or 0) end; local s = 0; for i = 1, 6 do s = s + v(i, 1) end; return s`,
		`local function cat(i) return "x" .. i end; local out = ""; for i = 1, 6 do out = out .. cat(i) end; return out`,
		`local mt = {__add = function(a, b) return {v = a.v + b.v} end}; local x = setmetatable({v = 1}, mt); local y = setmetatable({v = 2}, mt); return (x + y).v`,
		`local function alloc(i) return {i, i * 2} end; local s = 0; for i = 1, 8 do local p = alloc(i); s = s + p[1] + p[2] end; return s`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, src string) {
		if raceEnabled {
			// Same boundary as FuzzP4ForceAllPromote: P4 mmap-segment
			// execution trips the race runtime's stack unwinder; the
			// correctness assertions are covered by non-race jobs.
			t.Skip("tiered mmap/wasm paths not race-safe; covered by non-race jobs")
		}
		if len(src) > 1<<14 {
			t.Skip()
		}
		prog, err := wangshu.Compile([]byte(src), "fuzz-auto")
		if err != nil {
			return // compile errors are a legal outcome
		}

		// Interpreter baseline: thresholds unreachable on this same
		// build — promotion cannot happen. It runs the SAME number of
		// times as the auto State and is compared run-for-run: scripts
		// that mutate globals legitimately behave differently on later
		// runs (state persists across Run on one State), so comparing
		// auto run 2 against a single baseline run would flag ordinary
		// cross-run state drift as a tier divergence (found by this
		// fuzz target's first CI run, seed 861f54880d2009d5).
		st1 := wangshu.NewState(wangshu.Options{})
		st1.SetStepBudget(1 << 20)
		st1.SetHotThresholds(^uint32(0), ^uint32(0))

		// Auto path: lowered thresholds, two runs on one State. Run 1
		// promotes mid-run; run 2 is tier-mixed from the first call.
		stA := wangshu.NewState(wangshu.Options{})
		stA.SetStepBudget(1 << 20)
		stA.SetHotThresholds(2, 4)
		for run := 1; run <= 2; run++ {
			resP1, errP1 := prog.Run(st1)
			resA, errA := prog.Run(stA)

			// Error-existence divergence: budget/timing class is
			// exempted (step counting differs between tiers on
			// boundary inputs); semantic divergence hard-fails —
			// same discipline as FuzzP4ForceAllPromote.
			if (errP1 == nil) != (errA == nil) {
				budgetTiming := (errP1 != nil && strings.Contains(errP1.Error(), "instruction budget exceeded")) ||
					(errA != nil && strings.Contains(errA.Error(), "instruction budget exceeded"))
				if budgetTiming {
					t.Skipf("budget/timing divergence (not a byte-equal violation): P1=%v auto=%v", errP1, errA)
					return
				}
				t.Errorf("error-existence divergence on auto run %d (suspected miscompile): P1=%v auto=%v",
					run, errP1, errA)
				return
			}
			if errP1 != nil {
				return // both errored: existence equivalence is the contract here
			}
			if len(resP1) != len(resA) {
				t.Errorf("auto run %d: result count P1=%d auto=%d", run, len(resP1), len(resA))
				return
			}
			for i := range resP1 {
				if resP1[i].Display() != resA[i].Display() {
					t.Errorf("auto run %d: result[%d] mismatch: P1=%q auto=%q",
						run, i, resP1[i].Display(), resA[i].Display())
					return
				}
			}
		}
	})
}
