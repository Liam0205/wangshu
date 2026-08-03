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
// The budget bounded the work but left under 1.5x headroom for four of six family seeds. Halving it
// restores an order of magnitude, and it costs no coverage: every one of these shapes trips the
// budget either way, so the fuzzer explores the same paths and simply stops sooner. What shrinks is
// the wall-clock a single input can consume, which is the quantity the watchdog measures.
const fuzzStepBudget = 1 << 19
