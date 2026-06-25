//go:build wangshu_p3 && wangshu_profile

// pineapple_wangshu_p3_test.go:wangshu p3 build 作 pineapple 默认 lua backend
// 时的 benchmark——_WangshuP3Auto(自然热度升层)× 4 模式(row/column × per-item/common)。
//
// 跑法:`go test -tags="wangshu_p3 wangshu_profile" -bench=. ./benchmarks/pineapple/`。
//
// 关键:wangshu State 由 pineapple 内部 pool 管理,我们够不到——所以 force-all
// **没法**经公共 API 注入。改用「同 benchmark 内全局对照」:跑前用一份独立
// wangshu.State 验证 force-all 真触发(prove-the-path-under-test)、跑测时
// 让 pineapple 内部 pool 自己造 state 不调 force-all,**让 N=1000 items 的
// 自然热度推升层** —— 这就是真 auto-lifting 形态。
//
// **issue #18 修复后的行为**(2026-06-17 起):自然热度升层路径真正接通——之前 p3
// build 的 _Auto benchmarks 实际是「P1 解释器 + 采样钩税」形态(自然热度路径不
// 工作,所有 Proto 编译期被烧 ReasonBackendUnsupp 占位,运行期 TierStuck)。
// issue #18 fix 后 considerPromotion 双路守卫接通 recheckCompilabilityRuntime
// 重判,本文件 benchmarks 测的才是真升层路径。
//
// **当前实测**:升层后 pineapple 形态(短工作量 + 频繁 boundary)下 p3 反而比 p1
// 慢 19%(_Row baseline 660 vs 553 µs),根因是 wasm dispatch + host↔wasm boundary
// 反噬而非采样钩税(profile 实证:cpu profile top 200 中 wasm dispatch + boundary
// 主导,采样钩路径不出现)。优化方向:bridge OnEnter/OnBackEdge 加 proto 复杂度阈值
// 守卫,让 short workload 不升层(已落地为 issue #21 MinPromotableCodeLen)。
//
// 跑前用 PromotionCount() 探针验证 wangshu 在该 build 下确实能升层(独立小
// test 验,见 `promotion_count_p3_test.go::TestPromotionCount_P3_NoForce_HotEntry_Lifts`,
// issue #18 修复后此测从 ❌ → ✅);bench loop 跑完后,pineapple pool 内 state
// 的 PromotionCount 已 >0,但因为 state 句柄不可达,我们能做的最强断言是「bench
// 完后 p3 数字显著 ≠ p1」(隐式证)。
//
// **不在本轮的探针**:让 pineapple 公共 API 暴露 pool/state 句柄供 bench 端
// 注入 force-all 与读 PromotionCount(),这是 cross-repo 工程,留 pineapple
// follow-up issue。
package pineapple_bench

import "testing"

func BenchmarkPineappleLuaOp_WangshuP3Auto_Row(b *testing.B)    { runBenchmark(b, "row") }
func BenchmarkPineappleLuaOp_WangshuP3Auto_Column(b *testing.B) { runBenchmark(b, "column") }

func BenchmarkPineappleLuaOp_WangshuP3Auto_CommonRow(b *testing.B) { runBenchmarkCommon(b, "row") }
func BenchmarkPineappleLuaOp_WangshuP3Auto_CommonColumn(b *testing.B) {
	runBenchmarkCommon(b, "column")
}
