//go:build wangshu_p4 && wangshu_profile

// conformance_p4_test.go —— P4 build conformance 测试 P4 路径触达守卫。
//
// **背景**(承外部 review 🔴 阻塞):make conformance-p4 跑通用 cases,但
// ~91% conformance 用例不达 P4 升层闸门(short proto + 单次调用),
// 故 conformance-p4 "全过" 不代表 P4 路径真被走到。
//
// 本测试加一个**专门为 P4 升层形态设计的 conformance 用例**(重复调用 +
// SupportsAllOpcodes 白名单内单 BB 形态)+ PromotionCount>0 fail-stop
// 守卫,确保 conformance-p4 至少有一个 P4 路径真触达。

package conformance

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestConformance_P4PathTriggered P4 build 下专门验「conformance 全套至少
// 有一个 P4 路径真触达」(fail-stop 守卫)。
//
// 形态:单 BB 函数 + 重复调用(对位 p4_test.go::TestP4_PromotionTriggered
// 同款设计)。force-all 真升 inner kernel 后,PromotionCount > 0 才能过。
//
// **prove-the-path 工程纪律**:防止 conformance-p4 "21 binary 全过" 是
// 静默空绿(force-all 形式上调用但实际 0 个 Proto 升层)。
func TestConformance_P4PathTriggered(t *testing.T) {
	// 选 P4 SupportsAllOpcodes 白名单形态:reg-K arith chain
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
