//go:build !wangshu_p3

// 默认 build(非 wangshu_p3)专属测试。
//
// P3 build 下 newStateArena 设 InPlaceBacking=true(wazero linear memory 收养),
// arena.Compact 故意 no-op(wazero memory.grow 只增不减)——本文件测试的「Compact
// 真缩 cap」语义只在默认 build 成立。P3 build 下的对应行为见
// embedding_admin_p3_test.go TestP3_Compact_NoOpInP3Mode。
package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestCompact_ShrinkAfterTransientPeak 验证 issue #11 方向 1:transient 大分配
// 触发 grow doubling 后,Release + Collect → arena.Compact 缩 cap,backing
// 回收。ArenaCapKB 应从 grow doubling 高水位降回。
func TestCompact_ShrinkAfterTransientPeak(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 64 * 1024})
	// transient 触发 grow 涨到 ~1 MB
	tv := st.NewArrayTable(make([]wangshu.Value, 100000))
	capPeak := st.ArenaCapKB()
	if capPeak < 800 { // 至少涨到几百 KB
		t.Fatalf("ArenaCapKB did not grow as expected: %.1f", capPeak)
	}
	tv.Release()
	st.Collect()
	capAfter := st.ArenaCapKB()
	t.Logf("ArenaCapKB: peak=%.1f after Collect=%.1f", capPeak, capAfter)
	if capAfter >= capPeak {
		t.Fatalf("Compact did not shrink ArenaCapKB: peak=%.1f after=%.1f", capPeak, capAfter)
	}
}
