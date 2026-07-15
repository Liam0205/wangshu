//go:build wangshu_p3 && wangshu_profile

// pineapple_wangshu_p3_test.go: benchmark for the wangshu p3 build acting as
// pineapple's default lua backend — _WangshuP3Auto (natural-hotness lifting)
// across 4 modes (row/column × per-item/common).
//
// How to run: `go test -tags="wangshu_p3 wangshu_profile" -bench=. ./benchmarks/pineapple/`.
//
// Key point: the wangshu State is managed by pineapple's internal pool, which we
// cannot reach — so force-all cannot be injected through the public API. Instead
// we use an "in-benchmark global control": before the run, a separate standalone
// wangshu.State verifies that force-all actually triggers
// (prove-the-path-under-test); during the run we let pineapple's internal pool
// build its own state without calling force-all, letting the natural hotness of
// N=1000 items drive lifting — that is the real auto-lifting scenario.
//
// Behavior after the issue #18 fix (since 2026-06-17): the natural-hotness lifting
// path is genuinely wired up. Before that, the _Auto benchmarks in the p3 build
// were effectively a "P1 interpreter + sampling-hook tax" scenario (the natural
// hotness path did not work; every Proto had a ReasonBackendUnsupp placeholder
// burned in at compile time, and was TierStuck at runtime). After the issue #18
// fix, considerPromotion's dual-path guard reaches recheckCompilabilityRuntime for
// re-evaluation, so the benchmarks in this file exercise the real lifting path.
//
// Current measurement: after lifting, under the pineapple scenario (short
// workload + frequent boundary), p3 is actually 19% slower than p1 (_Row baseline
// 660 vs 553 µs). The root cause is the wasm dispatch + host↔wasm boundary backlash
// rather than the sampling-hook tax (profile evidence: in the top 200 of the cpu
// profile, wasm dispatch + boundary dominate, and the sampling-hook path does not
// appear). Optimization direction: add a proto-complexity threshold guard to
// bridge OnEnter/OnBackEdge so short workloads do not lift (done as issue #21
// MinPromotableCodeLen).
//
// Before running, use the PromotionCount() probe to verify wangshu can actually
// lift under this build (verified by a separate small test, see
// `promotion_count_p3_test.go::TestPromotionCount_P3_NoForce_HotEntry_Lifts`, which
// went from ❌ → ✅ after the issue #18 fix). After the bench loop finishes, the
// PromotionCount of the state in pineapple's pool is already >0, but because the
// state handle is unreachable, the strongest assertion we can make is that "after
// the bench, the p3 numbers differ significantly from p1" (an implicit proof).
//
// Probe not in this round: have pineapple's public API expose the pool/state handle
// so the bench side can inject force-all and read PromotionCount(). That is
// cross-repo work, left as a pineapple follow-up issue.
package pineapple_bench

import "testing"

func BenchmarkPineappleLuaOp_WangshuP3Auto_Row(b *testing.B)    { runBenchmark(b, "row") }
func BenchmarkPineappleLuaOp_WangshuP3Auto_Column(b *testing.B) { runBenchmark(b, "column") }

func BenchmarkPineappleLuaOp_WangshuP3Auto_CommonRow(b *testing.B) { runBenchmarkCommon(b, "row") }
func BenchmarkPineappleLuaOp_WangshuP3Auto_CommonColumn(b *testing.B) {
	runBenchmarkCommon(b, "column")
}
