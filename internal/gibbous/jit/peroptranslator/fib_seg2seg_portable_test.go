//go:build wangshu_p4 && wangshu_profile

// fib_seg2seg_portable_test.go — arch-neutral validation of the issue #50
// segment-to-segment CALL dispatch. Unlike the amd64+linux-tagged e2e
// tests, this runs on ANY GOARCH/GOOS (amd64, linux/arm64, darwin/arm64),
// so it validates the arm64 seg2seg machine code on real arm64 hardware
// (issue #61). fib references itself via an open upvalue and recurses,
// exercising inline GETUPVAL + arith/compare deopt guards + seg2seg
// dispatch (blr into the callee segment) + dual-semantics RETURN.

package peroptranslator_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

func TestSeg2SegFibPortable(t *testing.T) {
	cases := []struct{ n, want string }{
		{"10", "55"},
		{"20", "6765"},
		{"24", "46368"},
	}
	for _, tc := range cases {
		src := `local function fib(n) if n < 2 then return n else return fib(n-1)+fib(n-2) end end return fib(` + tc.n + `)`
		prog, err := wangshu.Compile([]byte(src), "fibport")
		if err != nil {
			t.Fatalf("fib(%s) compile: %v", tc.n, err)
		}
		st := wangshu.NewState(wangshu.Options{})
		st.SetForceAllPromote(true)
		before := peroptranslator.SegToSegHitCount.Load()
		res, err := prog.Run(st)
		if err != nil {
			t.Fatalf("fib(%s) run: %v", tc.n, err)
		}
		// Correctness is the hard gate (must be byte-equal to the
		// interpreter on every arch).
		if len(res) != 1 || res[0].Display() != tc.want {
			t.Fatalf("fib(%s) = %v, want [%s]", tc.n, res, tc.want)
		}
		// seg2seg firing proves the native segment-to-segment path was
		// actually exercised (not the interpreter / exit-reason). If this
		// is 0 on a given platform, native promotion or seg2seg dispatch
		// did not engage there — informative, but only the correctness
		// check above is a hard failure.
		seg := peroptranslator.SegToSegHitCount.Load() - before
		t.Logf("fib(%s) = %s OK; seg2seg hits = %d", tc.n, tc.want, seg)
		if seg == 0 {
			t.Errorf("fib(%s): seg2seg never fired (SegToSegHitCount stalled) "+
				"— native seg2seg dispatch did not engage on this platform", tc.n)
		}
	}
}

// TestCallInlineWideMultiReturnPortable crosses the 15 -> 16 nresults
// boundary: a CALL capturing 17 fixed return values (bytecode C = 18,
// nresults = 17) exceeds 4 bits. The arch-shared dispatcher used to
// mask nresults with 0xF (PR #62 review finding), silently truncating
// 17 to 1 — result registers r2..r17 kept stale values while the run
// "succeeded". The callee reads a global so it is NOT seg2seg-eligible
// and must ride the HelperExecutePlainCall dispatcher path (the
// truncation point) — a pure-arith callee would go seg2seg and never
// reach it. The sum assertion catches any dropped result; the
// interpreter is the oracle for the expected value.
func TestCallInlineWideMultiReturnPortable(t *testing.T) {
	src := `
gWideBase = 0
local function seventeen(x)
  local g = gWideBase
  return x+1+g, x+2, x+3, x+4, x+5, x+6, x+7, x+8, x+9,
         x+10, x+11, x+12, x+13, x+14, x+15, x+16, x+17
end
local function kernel(n)
  local s = 0
  for i = 1, n do
    local t = i + 1 + 2 + 3 + 4 + 5 + 6 + 7 + 8 + 9 + 10
    local r1, r2, r3, r4, r5, r6, r7, r8, r9,
          r10, r11, r12, r13, r14, r15, r16, r17 = seventeen(t)
    s = s + r1 + r2 + r3 + r4 + r5 + r6 + r7 + r8 + r9 +
        r10 + r11 + r12 + r13 + r14 + r15 + r16 + r17
  end
  return s
end
return kernel(20)
`
	prog, err := wangshu.Compile([]byte(src), "wideret")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Per iteration: sum_{k=1..17} (t+k) = 17t + 153, with t = i + 55.
	// Sum over i=1..20: 17*(210 + 20*55) + 20*153 = 17*1310 + 3060 = 25330.
	if len(res) != 1 || res[0].Display() != "25330" {
		t.Fatalf("kernel(20) = %v, want [25330] (nresults > 15 truncated?)", res)
	}
	if st.PromotionCount() == 0 {
		t.Fatal("PromotionCount = 0; kernel did not promote")
	}
}
