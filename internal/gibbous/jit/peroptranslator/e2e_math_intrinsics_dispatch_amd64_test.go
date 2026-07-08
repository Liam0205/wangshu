//go:build wangshu_p4 && wangshu_profile && amd64 && linux

// e2e_math_intrinsics_dispatch_amd64_test.go — issue #77: prove the
// math intrinsic inline eliminates the exit-reason round trips. With the
// intrinsic OFF a sqrt-heavy kernel round-trips to the host once per
// call (DispatchHelperCount grows ~ loop count); with it ON the sqrt is
// computed in-segment and the per-run dispatch drops to ~0. This is the
// amd64 efficiency proof that backs the issue #67 follow-up: n-body's
// remaining ~50k sqrt dispatches per run go to zero.

package peroptranslator_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

func TestPJ10_MathIntrinsic_DispatchDrop(t *testing.T) {
	// A kernel that does nothing but sqrt in a loop, aliased to a local so
	// it promotes (F2-b). Each iteration's sqrt is the only thing that
	// could exit-reason.
	src := `
local sqrt = math.sqrt
local function kernel(n)
  local acc = 0.0
  for i = 1, n do acc = acc + sqrt(i) end
  return acc
end
local r
for j = 1, 3 do r = kernel(2000) end
return r
`
	measure := func(intrinsicsOn bool) int64 {
		restore := peroptranslator.SetMathIntrinsicsEnabledForTest(intrinsicsOn)
		defer restore()
		prog, err := wangshu.Compile([]byte(src), "pj10intrdispatch")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		st := wangshu.NewState(wangshu.Options{})
		st.SetForceAllPromote(true)
		if _, err := prog.Run(st); err != nil { // warm + promote
			t.Fatalf("warmup: %v", err)
		}
		before := peroptranslator.DispatchHelperCount.Load()
		if _, err := prog.Run(st); err != nil {
			t.Fatalf("run: %v", err)
		}
		return peroptranslator.DispatchHelperCount.Load() - before
	}

	off := measure(false)
	on := measure(true)
	t.Logf("dispatch per run: intrinsics OFF = %d, ON = %d", off, on)

	// OFF: every sqrt round-trips. 3 kernel(2000) calls * 2000 = 6000
	// sqrt dispatches per run (plus a little). Sanity: it must be large.
	if off < 3000 {
		t.Fatalf("intrinsics OFF: dispatch=%d too low — the sqrt round trip "+
			"isn't being measured (kernel not promoted?)", off)
	}
	// ON: the sqrt inlines, so dispatch drops to a tiny constant
	// (cold-IC re-warm at most). Assert well below the OFF count.
	if on > 500 {
		t.Fatalf("intrinsics ON: dispatch=%d too high — math.sqrt not "+
			"inlining across runs (want ~0, OFF was %d)", on, off)
	}
}
