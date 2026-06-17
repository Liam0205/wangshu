//go:build wangshu_p3 && wangshu_profile

// pineapple_wangshu_p3_test.go:wangshu p3 build 作 pineapple 默认 lua backend
// 时的两个 benchmark——P3Force(force-all 升层)+ P3Auto(自然热度升层)。
//
// 跑法:`go test -tags="wangshu_p3 wangshu_profile" -bench=. ./benchmarks/pineapple/`。
//
// 关键:wangshu State 由 pineapple 内部 pool 管理,我们够不到——所以 force-all
// **没法**经公共 API 注入。改用「同 benchmark 内全局对照」:跑前用一份独立
// wangshu.State 验证 force-all 真触发(prove-the-path-under-test)、跑测时
// 让 pineapple 内部 pool 自己造 state 不调 force-all,**让 N=1000 items 的
// 自然热度推升层** —— 这就是真 auto-lifting 形态。
//
// 因此实际上 p3 binary 只跑一个 BenchmarkPineappleLuaOp_WangshuP3Auto:
// pineapple 内部 pool 创建的 state 没法外部注入 force-all,只能靠自然热度。
// 跑前用 PromotionCount() 探针验证 wangshu 在该 build 下确实能升层(独立小
// test 验);bench loop 跑完后,理论上 pineapple pool 内 state 的 PromotionCount
// 已 >0,但因为 state 句柄不可达,我们能做的最强断言是「bench 完后 p3 数字
// 显著 ≠ p1」(隐式证)。
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
