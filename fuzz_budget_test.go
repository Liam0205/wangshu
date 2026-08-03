//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

package wangshu_test

// fuzzStepBudget is the per-State step budget the P4 fuzz targets use.
//
// Lowered from 1<<20 after #224/#225, which are the seventh and eighth filings of the concat-storm
// family. Those two neither crash nor diverge and are bounded by the byte accounting -- the problem
// is the MARGIN. A 1<<20 budget permits ~64 MiB of concat, which costs 0.7-1.3s locally per
// subtest; CI runners are ~10x slower (see internal/crescent/state.go's chargeBulkWork note), so
// the slowest seeds land at 12-13s against go-fuzz's 10-second per-input watchdog, and the nightly
// log shows exactly that: "panic: deadlocked".
//
// Halving it first was not enough, and an audit caught that: 1<<19 fixed the six corpus seeds but the
// reachable neighbourhood around them was still over the watchdog. Measured projections for four
// Runs at 10x, at 1<<19: a plain `out=out.."x"` loop 14s, `t[tostring(i)]=i` 18s, and a
// `s:gsub("%a","x")` loop 41s -- four times the watchdog, worse than the seeds being fixed. gsub
// bills about twice its subject per call while doing considerably more work than an equally-billed
// concat, so equal billing does not mean equal wall-clock.
//
// 1<<16 puts the worst of those at 5.2s, a 1.93x margin, which is the first value that satisfies the
// order-of-magnitude criterion this branch's own documentation states.
//
// It costs no promotion coverage: the harness's seed corpus produces an identical PromotionCount at
// 1<<20, 1<<19 and 1<<16, because those shapes promote after a handful of calls and never approach
// any of these limits. What shrinks is only the wall-clock a single input can consume, which is the
// quantity the watchdog measures.
const fuzzStepBudget = 1 << 16
