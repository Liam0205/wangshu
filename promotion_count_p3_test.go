//go:build wangshu_p3 && wangshu_profile

// promotion_count_p3_test.go: State.PromotionCount() increments as promotions
// occur under p3 build + force-all, and stays 0 under p3 build + non-force-all
// with no hotness.
package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestPromotionCount_P3_ForceAll_Increments verifies that under
// SetForceAllPromote(true), an inner (non-vararg) function is promoted on its
// first execution, so PromotionCount goes up by at least 1.
//
// **Key fact**: the top-level chunk is vararg (a Lua 5.1 main chunk has an
// implicit `...`), which F1 structurally excludes from ever being promoted. So
// the test uses an inner non-vararg function `f`; the top-level chunk is merely
// the call entry point.
func TestPromotionCount_P3_ForceAll_Increments(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)

	if before := st.PromotionCount(); before != 0 {
		t.Fatalf("PromotionCount before any Run = %d, want 0", before)
	}

	prog, err := wangshu.Compile([]byte(`
		local function f(x) return x * 2 end
		return f(21)
	`), "test")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}

	if after := st.PromotionCount(); after == 0 {
		t.Errorf("p3 + force-all 跑完 PromotionCount = 0, want > 0(force-all 应让首次执行 f 即升)")
	}
}

// TestPromotionCount_P3_NoForce_StaysCold verifies that under the default p3
// build behavior, a one-shot small script (entry count = 1) never reaches
// HotEntryThreshold, so PromotionCount = 0. This is precisely the
// counterexample to the gibbous path that the conformance-p3 comment promises
// — it confirms that "the auto-lifting configuration degrades to the
// interpreter path unless SetForceAllPromote is called".
func TestPromotionCount_P3_NoForce_StaysCold(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	// Deliberately do not call SetForceAllPromote

	prog, err := wangshu.Compile([]byte(`
		local function f(x) return x * 2 end
		return f(21)
	`), "test")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := st.PromotionCount(); got != 0 {
		t.Errorf("p3 一次性脚本 PromotionCount = %d, want 0(达不到 HotEntryThreshold)", got)
	}
}

// TestPromotionCount_P3_NoForce_HotEntry_Lifts verifies the issue #18 fix:
// under the default p3 build behavior (without calling SetForceAllPromote),
// when a **long** inner function is called from the same entry
// N > HotEntryThreshold times, runtime considerPromotion sees the compile-time
// F7 placeholder (ReasonBackendUnsupp) plus b.p3 already injected, calls
// recheckCompilabilityRuntime to re-decide → f is lifted to gibbous →
// PromotionCount > 0.
//
// Before the fix (issue #18): compile-time analyzeCompilability used a
// temporary Bridge without P3, so every Proto got burned in as
// CompNotCompilable + ReasonBackendUnsupp; runtime considerPromotion saw
// comp != CompCompilable && !forceAll → straight to TierStuck, so **no Proto
// was promoted even at 1000 calls**. The reflection preceding this test used
// the same script to empirically show PromotionCount==0, which was the reverse
// assertion; after the fix this flips to the positive assertion > 0.
//
// **issue #21 guard** (MinPromotableCodeLen): this test must use an f with ≥10
// opcodes, otherwise the short-proto guard at the top of OnEnter returns
// directly and no lifting happens even at HotEntryThreshold. Use an f with a
// handful of arithmetic + comparison ops and local variables to reach the
// length, so F1-F7 all pass and real backend support is present.
func TestPromotionCount_P3_NoForce_HotEntry_Lifts(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	// Deliberately do not call SetForceAllPromote — this test is meant to take the natural hotness path

	// f contains ≥10 opcodes: Lua 5.1 uses RK encoding to save LOADK, so pure
	// arithmetic compiles very short; use multiple variables + multiple steps +
	// comparison to ensure the opcode count clears the threshold.
	//
	// **issue #92 guard** (straightLineMinCodeLen): f must also carry a back
	// edge (an inner for) — a straight-line small body with no back edge is
	// rejected by the back-edge dimension of WorthPromoting (each call pays a
	// wasm boundary round-trip with no loop to amortize it), so it is not lifted
	// even after clearing the length floor.
	prog, err := wangshu.Compile([]byte(`
		local function f(x)
			local a = x * 2
			local b = a + 3
			local c = b * x
			local acc = 0
			for k = 1, 4 do acc = acc + a + b - c end
			if acc > 0 then return acc + a end
			return acc - a
		end
		local sum = 0
		for i = 1, 1000 do sum = sum + f(i) end
		return sum
	`), "test")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := st.PromotionCount(); got == 0 {
		t.Errorf("p3 自然热度路径 PromotionCount = 0, want > 0(issue #18 修复后 f 应升 gibbous)")
	}
}

// TestPromotionCount_P3_NoForce_ShortProto_StaysCold verifies the issue #21
// guard: on the natural hotness path of a p3 build, a short proto (Code length
// < MinPromotableCodeLen=10) is **not lifted** even when called from the same
// entry N > HotEntryThreshold times — in the pineapple shape, a 4-opcode
// arithmetic f causes more wasm backlash after lifting than the interpreter
// gains, so the guard intercepts it early in OnEnter.
//
// Set against TestPromotionCount_P3_ForceAll_Increments (forceAll bypasses the
// guard and lifts successfully) and
// TestPromotionCount_P3_NoForce_HotEntry_Lifts (a long proto lifts on natural
// hotness), it forms a "short/long × forceAll/natural-hotness" comparison
// matrix.
//
// **Before the fix (issue #21)**: this test had PromotionCount > 0 (short proto
// backlash after lifting, pineapple p3 vs p1 was 19% slower); after the fix
// PromotionCount == 0 (short proto is not lifted and takes the interpreter
// path).
func TestPromotionCount_P3_NoForce_ShortProto_StaysCold(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})

	// f is 4 opcodes (LOADK loads the 0.85 constant + MUL + ADD + RETURN), < MinPromotableCodeLen=10.
	// Even at 1000 calls, the guard intercepts early, so PromotionCount should still be 0.
	prog, err := wangshu.Compile([]byte(`
		local function f(x) return x * 0.85 + 10.0 end
		local sum = 0
		for i = 1, 1000 do sum = sum + f(i) end
		return sum
	`), "test")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := st.PromotionCount(); got != 0 {
		t.Errorf("p3 short proto(< MinPromotableCodeLen)PromotionCount = %d, want 0(issue #21 守卫应拦截)", got)
	}
}
