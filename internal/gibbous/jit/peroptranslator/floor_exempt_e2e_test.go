//go:build wangshu_p4 && wangshu_profile

// floor_exempt_e2e_test.go — issue #67 end-to-end: a below-floor
// seg2seg-eligible callee must promote under AUTO mode (natural heat,
// no forceAll) and be dispatched seg2seg from its promoted caller.
//
// This is the spectral-norm shape: `A(i, j)` is 9 opcodes — one below
// MinPromotableCodeLen — but is called 144k times per run from the
// promoted `Av`/`Atv` loops. Before the FloorExempter hook the floor
// kept it on the interpreter forever, so every call from the promoted
// caller paid an ExecutePlainCall exit-reason round trip (auto 3.7x
// slower than force). With the hook the bridge asks P4, P4 answers
// ProtoSeg2SegEligible = true, the callee promotes at the heat
// threshold, and the caller's CallIC upgrades to in-segment dispatch.
package peroptranslator_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

// TestFloorExemptSeg2SegCalleeAutoPromotes drives a spectral-norm-shaped
// kernel under auto mode. The tiny pure-arith callee `a(i, j)` compiles
// to fewer opcodes than the floor; the caller loop is hot enough to
// promote on back edges and calls the callee well past HotEntryThreshold
// (200). Assertions:
//
//  1. correctness: result matches the interpreter-computed constant;
//  2. the callee promoted (PromotionCount covers caller AND callee);
//  3. seg2seg dispatch fired (proves the callee runs as an in-segment
//     callee, not behind ExecutePlainCall round trips).
func TestFloorExemptSeg2SegCalleeAutoPromotes(t *testing.T) {
	// a(i, j) mirrors spectral-norm's A: pure register arithmetic,
	// single return — seg2seg-eligible and below the promotion floor.
	// kernel mirrors Av: a promotable loop that calls a() per iteration
	// (the main chunk itself never promotes — CLOSURE op — so the
	// seg2seg caller must be a function). kernel's 3000-iteration loop
	// crosses HotBackEdgeThreshold (1000) during its first call and
	// promotes; a() crosses HotEntryThreshold (200) almost immediately.
	// The outer driver re-enters kernel so post-promotion calls run
	// caller-native.
	src := `
local function a(i, j)
  local ij = i + j - 1
  return 1.0 / (ij * (ij - 1) * 0.5 + i)
end
local function kernel(n)
  local s = 0
  for i = 1, n do
    s = s + a(i, 7)
  end
  return s
end
local s
for r = 1, 5 do
  s = kernel(3000)
end
return s
`
	prog, err := wangshu.Compile([]byte(src), "floorexempt")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	// NO SetForceAllPromote: auto mode is the subject under test.

	// First run: crosses thresholds mid-run; promotion takes effect on
	// later entries. Second run: steady state, everything hot from the
	// first call.
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("warmup run: %v", err)
	}
	before := peroptranslator.SegToSegHitCount.Load()
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Correctness gate: sum of 1/(ij*(ij-1)*0.5+i) for i=1..3000, j=7.
	// The constant is the interpreter's Display() output for this source
	// on a default (P1-only) build — an in-test oracle run is impossible
	// here because any State on this build promotes under auto too.
	// IEEE-754 arithmetic is deterministic, so the digits are stable
	// across arches.
	if len(res) != 1 {
		t.Fatalf("results = %v, want 1 value", res)
	}
	if got, want := res[0].Display(), "0.31237343372631"; got != want {
		t.Fatalf("result = %s, want %s (P1 interpreter oracle)", got, want)
	}

	// Promotion gate: caller (main-chunk inner loop lives in the main
	// proto, which is F-gated; the hot promotable protos are the callee
	// `a` and nothing else below the floor). At minimum the callee must
	// have promoted — without the floor exemption PromotionCount stays 0
	// for this source (verified by reverting the exemption).
	if st.PromotionCount() == 0 {
		t.Fatal("PromotionCount = 0; below-floor seg2seg-eligible callee did not promote under auto")
	}

	// seg2seg gate: the promoted caller must dispatch the callee via the
	// in-segment channel. Zero means the callee is still being entered
	// through ExecutePlainCall round trips — the exact regression this
	// hook exists to prevent.
	if hits := peroptranslator.SegToSegHitCount.Load() - before; hits == 0 {
		t.Error("SegToSegHitCount stalled: callee not dispatched seg2seg under auto")
	} else {
		t.Logf("seg2seg hits in steady-state run: %d", hits)
	}
}
