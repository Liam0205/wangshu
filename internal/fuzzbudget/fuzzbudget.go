// Package fuzzbudget holds the step budget the P4 fuzz harnesses use, in one place both the root
// fuzz targets and test/regression can import.
//
// It exists because the value has to be shared across packages to be testable. The regression test
// for #224/#225 first kept its own copy of the number, so raising the harness budget left it green --
// it guarded its own constant rather than the harness's. A duplicated constant cannot be pinned by a
// test; only a shared one can.
package fuzzbudget

// Steps is the per-State step budget for FuzzAutoPromote and FuzzP4ForceAllPromote.
//
// Lowered from 1<<20 after #224/#225, the concat-storm family's seventh and eighth filings. Those
// seeds neither crash nor diverge and are correctly bounded by the byte accounting -- the problem is
// the MARGIN. The budget's allowance, times the local cost per unit, times the ~10x CI slowdown (see
// chargeBulkWork's comment in internal/crescent/state.go), exceeded go-fuzz's 10-second per-input
// watchdog, and the nightly log says exactly that: "panic: deadlocked".
//
// The value comes from the most expensive shape at EQUAL BILLING, not from the shapes that happened
// to be filed. Halving to 1<<19 fixed those six seeds while a neighbouring s:gsub("%a","x") loop
// still projected to 41s, four times the watchdog. At 1<<16 the worst billed shape projects to about
// 5s.
//
// Raising this costs margin on every CI runner. The regression test in
// test/regression/issue224_watchdog_margin_test.go measures this exact constant, so it will fail.
const Steps = 1 << 16
