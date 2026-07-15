//go:build wangshu_p4 && wangshu_profile

// conformance_p4_test.go — P4-build conformance guard that the P4 path is
// actually reached.
//
// **Background** (from an external review 🔴 blocker): make conformance-p4 runs
// the generic cases, but ~91% of conformance cases don't reach the P4 promotion
// gate (short proto + a single call), so conformance-p4 "all pass" does not mean
// the P4 path is actually exercised.
//
// This test adds a conformance case **designed specifically for the P4 promotion
// shape** (repeated calls + a single-BB shape within the SupportsAllOpcodes
// whitelist) plus a PromotionCount>0 fail-stop guard, ensuring conformance-p4
// exercises at least one real P4 path.

package conformance

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestConformance_P4PathTriggered verifies, under the P4 build, that "the whole
// conformance suite exercises at least one real P4 path" (fail-stop guard).
//
// Shape: single-BB function + repeated calls (mirrors the design of
// p4_test.go::TestP4_PromotionTriggered). Once force-all really promotes the
// inner kernel, PromotionCount > 0 is required to pass.
//
// **prove-the-path engineering discipline**: prevents conformance-p4's "all 21
// binaries pass" from being a silent empty green (force-all is nominally invoked
// but 0 Protos are actually promoted).
func TestConformance_P4PathTriggered(t *testing.T) {
	// Pick a P4 SupportsAllOpcodes whitelist shape: reg-K arith chain
	src := `
local function f(x) return x * 2 + 1 end
local s = 0
for i = 1, 30 do s = s + f(i) end
return s`
	prog, err := wangshu.Compile([]byte(src), "p4-conformance-promo")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	promo := st.PromotionCount()
	t.Logf("conformance-p4 PromotionCount = %d", promo)
	if promo == 0 {
		t.Fatal("conformance-p4 PromotionCount = 0 → P4 路径未真触达。" +
			"本测试是 conformance-p4 全套 P4 路径触达的兜底守卫,fail-stop。" +
			"真 P4 路径验收以 test/difftest/p4_test.go 为准(p4Corpus 17 用例 +" +
			"重复调用 + PromotionCount > 0)。")
	}
}
