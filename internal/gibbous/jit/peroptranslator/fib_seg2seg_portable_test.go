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
