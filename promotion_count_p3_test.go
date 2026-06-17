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
