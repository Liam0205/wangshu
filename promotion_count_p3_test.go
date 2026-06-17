//go:build wangshu_p3 && wangshu_profile

// promotion_count_p3_test.go:State.PromotionCount() 在 p3 build + force-all
// 下随升层发生而递增,在 p3 build + 非 force-all + 无热度时仍为 0。
package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestPromotionCount_P3_ForceAll_Increments 验证 SetForceAllPromote(true)
// 下执行内层(非 vararg)函数首次即升,PromotionCount 至少 +1。
//
// **关键事实**:顶层 chunk 是 vararg(Lua 5.1 main chunk 隐式 `...`),F1
// 结构性排除永不升层。所以测试用内层非 vararg 函数 `f`,顶层 chunk 只是
// 调用入口。
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

// TestPromotionCount_P3_NoForce_StaysCold 验证 p3 build 默认形态下,
// 一次性小脚本(入口次数=1)达不到 HotEntryThreshold,PromotionCount = 0。
// 这条恰好是 conformance-p3 注释承诺凸月路径的反例——印证「auto-lifting
// 形态不调 SetForceAllPromote 会退化到解释器路径」的事实。
func TestPromotionCount_P3_NoForce_StaysCold(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	// 故意不调 SetForceAllPromote

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

// TestPromotionCount_P3_NoForce_HotEntry_Lifts 验证 issue #18 修复:p3 build
// 默认形态(不调 SetForceAllPromote)下,内层函数 f 被同入口调 N>HotEntryThreshold
// 次时,运行期 considerPromotion 看到编译期 F7 占位(ReasonBackendUnsupp)+ b.p3
// 已注入,调 recheckCompilabilityRuntime 重判 → f 升 gibbous → PromotionCount > 0。
//
// 修复前(issue #18):编译期 analyzeCompilability 用临时 Bridge 无 P3,所有 Proto
// 被烧 CompNotCompilable + ReasonBackendUnsupp;运行期 considerPromotion 看 comp
// != CompCompilable && !forceAll → 直接 TierStuck,**任何 Proto 即便达 1000 次
// 调用也不升层**。本测之前的 reflection 用同样脚本实证 PromotionCount==0,这是
// 反向断言;修复后此处转正向断言 > 0。
//
// HotEntryThreshold=200,N=1000 远超之。f 是 `x*2` 纯算术(F1-F6 全过、F7 真实
// 后端支持 MUL/RETURN),应升层成功。
func TestPromotionCount_P3_NoForce_HotEntry_Lifts(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	// 故意不调 SetForceAllPromote——本测就是要走自然热度路径

	prog, err := wangshu.Compile([]byte(`
		local function f(x) return x * 2 end
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
