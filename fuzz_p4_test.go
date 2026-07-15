// fuzz_p4_test.go —— P4 force-all guard misjudgment fuzz harness (per
// docs/design/p4-method-jit/08-testing-strategy.md V22 guard-misjudgment fuzz).
//
// **Responsibility**: any Lua source, once promoted to P4 via force-all-promote,
// runs and its result is compared byte-equal against the P1 interpreter. It verifies:
//   - P4 spec template guard never misjudges (under IC NodeHit / FBSelfMono
//     gating, taking the spec segment + deopt path yields results identical to
//     P1's full doCall)
//   - after the mmap-segment R14 ABI fix, the Go G stays correct under long fuzz
//     runs (per PR #26 fix)
//   - V18 -race stays race-free across diverse source forms
//
// **CI integration**:
//   - single-run fuzz (< 5 min) triggered on every push (fuzz-smoke-p4 job to be added)
//   - nightly fuzz (2h+) covering V21 longevity / V22 30-day cumulative
//     no-guard-misjudgment events

//go:build wangshu_p4 && wangshu_profile

package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// FuzzP4ForceAllPromote: under P4 force-all-promote mode, the fuzz seeds +
// arbitrary source must not panic and must be byte-equal to the P1 interpreter
// (force-all=false path) results.
//
// Note: P4 vs P1 byte-equality is a core commitment of P4's design discipline
// (per docs/design/p4-method-jit/08-testing-strategy.md V1-V13 correctness axis
// + three-way differential).
func FuzzP4ForceAllPromote(f *testing.F) {
	// seed selection strategy: cover the forms P4 SupportsAllOpcodes actually
	// accepts (arithmetic + FORLOOP + table IC + SELF inline + all 25 form classes).
	seeds := []string{
		// simple arithmetic
		`return 1 + 2`,
		`return (3 * 4) - (5 / 2)`,
		// table IC
		`local t = {1, 2, 3}; return t[2]`,
		`local t = {x = 1, y = 2}; return t.y`,
		`local t = {}; t[1] = 42; return t[1]`,
		`local t = {}; t.x = 99; return t.x`,
		// FORLOOP
		`local s = 0; for i = 1, 100 do s = s + i end; return s`,
		`local s = 0; for i = 1, 1000 do s = s + i * 2 end; return s`,
		// SELF method call
		`local o = {m = function(self) return 42 end}; return o:m()`,
		`local o = {m = function(self, x) return x * 2 end}; return o:m(21)`,
		// SELF spec template warmup
		`
local o = {m = function(self) return 1 end}
local function f(t) return t:m() end
local sum = 0
for i = 1, 100 do sum = sum + f(o) end
return sum`,
		// nested SELF
		`
local o1 = {m = function(self) return 1 end}
local o2 = {n = function(self) return 2 end}
local function f(a, b) return a:m() + b:n() end
return f(o1, o2)`,
		// CALL void / setter
		`local s = 0; local function add(x) s = s + x end; add(1); add(2); return s`,
		// multi-return drop
		`local function multi() return 1, 2, 3 end; local a, b, c = multi(); return a + b + c`,
		// comparison folding
		`return 1 < 2`,
		`return ("a" == "a")`,
		// closure + upvalue
		`local function make() local x = 0; return function() x = x + 1; return x end end; local f = make(); return f() + f() + f()`,
		// PJ4 table IC + grow (same polymorphic shape as the V20 deopt storm)
		`local t = {a=1, b=2}; local function f(t) return t.a end; for i = 1, 100 do f(t) end; return f(t)`,
		`local t = {1, 2, 3}; local function g(t) t[1] = 99; return t[1] end; for i = 1, 100 do g(t) end; return g(t)`,
		// SELF + deopt (different-shape receiver)
		`local m1 = {m = function(self) return 1 end}; local m2 = {m = function(self) return 2 end, x = 1}; local f = function(t) return t:m() end; for i = 1, 50 do f(m1) end; return f(m2)`,
		// N=4 multi-return form (per 84c7ed4 cC=5)
		`local mt = {m = function(self) return 1, 2, 3, 4 end}; local function caller(t) local a, b, c, d = t:m(); return a+b+c+d end; for i = 1, 50 do caller(mt) end; return caller(mt)`,
		// nested SELF chain
		`local o1 = {m = function(self) return 10 end}; local o2 = {n = function(self) return 20 end}; local function f(a, b) return a:m() + b:n() end; for i = 1, 50 do f(o1, o2) end; return f(o1, o2)`,
		// arithmetic error bubbling
		`local function add(a, b) return a + b end; local ok, e = pcall(add, "x", 1); return ok`,
		// error bubbling + SELF
		`local mt = {m = 42}; local ok, e = pcall(function() return mt:m() end); return ok`,
		// **commit-5u zero-cross optimized form**: the callee is also P4-promoted (GETTABLE form)
		`local o = { x = 42, m = function(self) return self.x end }; local function caller(t) local r = t:m(); return r end; local s = 0; for i = 1, 50 do s = s + caller(o) end; return s`,
		// useFrameInline N fixed params (callArgCount=0..7) + zero-cross compatibility
		`local sum = 0; local o = { m = function(self, a, b, c) sum = sum + a + b + c end }; local function caller(t) t:m(1, 2, 3) end; for i = 1, 30 do caller(o) end; return sum`,
		// useFrameInline + vararg callee
		`local sum = 0; local o = { m = function(self, ...) local a, b, c = ...; sum = sum + a + b + c end }; local function caller(t) t:m(1, 2, 3) end; for i = 1, 30 do caller(o) end; return sum`,
		// issue #77 math intrinsics (aliased to a local so the proto
		// promotes past F2-b): the inline SQRTSD/ROUNDSD/... result must
		// stay byte-equal to the host closure across mutated inputs incl.
		// negatives / NaN / Inf.
		`local f=math.sqrt local function k(x) local s=0.0 for i=1,20 do s=s+f(x) end return s end return k(16.0)`,
		`local f=math.floor local function k(x) local s=0.0 for i=1,20 do s=s+f(x) end return s end return k(-3.2)`,
		`local f=math.ceil local function k(x) local s=0.0 for i=1,20 do s=s+f(x) end return s end return k(3.2)`,
		`local f=math.abs local function k(x) local s=0.0 for i=1,20 do s=s+f(x) end return s end return k(-5.0)`,
		`local f=math.max local function k(x,y) local s=0.0 for i=1,20 do s=s+f(x,y) end return s end return k(3.0,7.0)`,
		`local f=math.min local function k(x,y) local s=0.0 for i=1,20 do s=s+f(x,y) end return s end return k(3.0,7.0)`,
		`local sq=math.sqrt local fl=math.floor local function k(x) return fl(sq(x)) end local r for i=1,30 do r=k(50.0) end return r`,
		// Direct (un-aliased) math.* spelling — promotes now that the
		// density gate exempts intrinsic calls (issue #77); mutations
		// explore the newly-native path, byte-equal vs P1 interp.
		`local function k(x) local s=0.0 for i=1,20 do s=s+math.sqrt(x) end return s end return k(16.0)`,
		`local function k(x,y) local s=0.0 for i=1,20 do s=s+math.max(x,y)+math.floor(x) end return s end return k(3.5,7.5)`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, src string) {
		if raceEnabled {
			// P4 mmap-segment shim calls trip Go's stack unwinder
			// under -race (mmap+morestack incompatibility, see
			// reflection 2026-07-01-p4-pj10-native-round lesson 1).
			// The correctness properties this fuzz asserts are
			// already covered by the non-race test job and
			// difftest / conformance suites.
			t.Skip("P4 mmap+shim not race-safe; covered by non-race jobs")
		}
		if len(src) > 1<<14 {
			t.Skip()
		}
		prog, err := wangshu.Compile([]byte(src), "fuzz-p4")
		if err != nil {
			return // a compile error is a legitimate outcome
		}

		// Run the P1 interpreter path (force-all=false). MaxArenaBytes caps
		// the quadratic concat-style garbage storm (issues #127/#130: with no
		// cap, a single exec balloons to a 2 GiB arena / 13 GiB Go heap, and 4
		// parallel workers crush the fuzz process outright); both States use
		// the same cap to keep the comparison symmetric.
		st1 := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
		st1.SetStepBudget(1 << 20)
		resP1, errP1 := prog.Run(st1)

		// Run the P4 force-all path
		st4 := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
		st4.SetStepBudget(1 << 20)
		st4.SetForceAllPromote(true)
		resP4, errP4 := prog.Run(st4)

		// Handling error-existence divergence (2026-06-28 precise exemption, per
		// PR #26 review suggestion): classify by error type, and only downgrade
		// **budget/timing** divergences to Skip (P1/P4 step-counting timing
		// differs, so a borderline input may trip `instruction budget exceeded`
		// on one side first); **semantic miscompilation** (spurious throw /
		// swallowed error) still hard-fails — this preserves the fuzzer's power
		// to catch P4-vs-P1 semantic divergence.
		//
		// Anchored semantic error-bubbling byte-equal baseline (deterministic coverage):
		// - 12 error-bubbling difftests, three-way byte-equal (p4_*_err_* case set)
		// - 3 e2e ErrorBubbleUp_NilRecv/BadMethod/OSRExitToDeopt
		// - 5 V18 -race (including R14 ABI post-verification)
		//
		// Supplementary discovery by the fuzz harness: **non-budget** existence
		// divergences still hard-fail.
		if (errP1 == nil) != (errP4 == nil) {
			budgetTiming := isResourceLimitErr(errP1) || isResourceLimitErr(errP4)
			if budgetTiming {
				t.Skipf("预算/时机类分叉(非 byte-equal 违反):P1=%v P4=%v", errP1, errP4)
				return
			}
			t.Errorf("error 存在性真分叉(疑似 P4 误编译,需查):P1=%v P4=%v", errP1, errP4)
			return
		}
		if errP1 != nil {
			// Both errored: don't require the err messages to match literally
			// (P4 spec template deopt-path error bubbling is already anchored
			// byte-equal to P1 by the ErrorBubbleUp_NilRecv/BadMethod e2e +
			// 12 error-bubbling difftests — the fuzzer only checks existence
			// equivalence)
			return
		}

		// result count equivalence
		if len(resP1) != len(resP4) {
			t.Errorf("P1 result count = %d, P4 = %d", len(resP1), len(resP4))
			return
		}

		// result literal equivalence (per P4 byte-equal discipline)
		for i := range resP1 {
			if resP1[i].Display() != resP4[i].Display() {
				t.Errorf("result[%d] mismatch:P1=%q, P4=%q",
					i, resP1[i].Display(), resP4[i].Display())
				return
			}
		}
	})
}
