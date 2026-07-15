//go:build wangshu_p4

package jit

import "sync/atomic"

// probes.go —— P4 in-package white-box hit counters for the jit package
// (per llmdoc/guides/prove-the-path-under-test §4 positive-side remedy:
// tests use SpecRegKHits() to assert the reg-K template is actually
// Compile-emitted, not a fall-back to the slow host-helper path).
//
// No functional meaning in production; the atomic monotonic increment cost
// is negligible (one per Compile, far below Compile's own µs-scale time).
// Read only in tests.

// specRegKHits is the compile hit count for the reg-K speculative template.
// ++1 each time Compile takes the useSpecRegK branch. SpecRegKHits() /
// ResetSpecRegKHits() are the public test interfaces.
var specRegKHits uint64

// specRegRegHits is the compile hit count for the reg-reg speculative template
// (counterpart to reg-K).
var specRegRegHits uint64

// specChainHits is the compile hit count for the two-stage chain-KK
// speculative template.
var specChainHits uint64

// specForLoopHits is the compile hit count for PJ3 FORLOOP byte-level inline
// (empty body, all-constant form).
var specForLoopHits uint64

// specTableHits is the compile hit count for PJ4 table-IC ArrayHit byte-level
// inline.
var specTableHits uint64

// specCallVoidHits is the Compile hit count for the PJ5 CALL void form
// (MOVE+CALL+RETURN void). The Run prelude path calls host.CallBaseline to
// complete the baseline doCall — on hit it skips the P3 R3 indirect sentinel,
// equivalent to the P1 interpreter doCall.
var specCallVoidHits uint64

// specTailCallHits is the Compile hit count for the PJ5 TAILCALL form
// (MOVE/GETUPVAL+...+TAILCALL+dead RETURN B=0+implicit RETURN B=1). The Run
// prelude path calls host.TailCall's tri-state branch (0=Lua tail complete /
// 1=ERR / 2=host tail complete).
var specTailCallHits uint64

// specSelfCallHits is the Compile hit count for the PJ5 SELF method call inline
// form (MOVE/GETUPVAL + SELF + ... + CALL/TAILCALL + RETURN). The Run prelude
// path first calls host.Self to fetch the method + load self, then calls
// host.CallBaseline / TailCall to complete the byte-equal P1 doCall dispatch
// (SELF + CALL = baseline + DoReturn; SELF + TAILCALL = tri-state branch).
var specSelfCallHits uint64

// specSelfCallSpecHits is the Compile hit count for the PJ5 SELF + CALL spec
// template form (on IC NodeHit the SELF segment takes the byte-level
// EmitSelfNodeHit template and skips host.Self). It is a subset of
// specSelfCallHits (the spec path increments both counters).
var specSelfCallSpecHits uint64

// specFrameInlineHits is the Compile hit count for PJ5 Option B Spike 1 frame
// building inline (per §9.20). Incremented when the useFrameInline=true path
// emits BuildVoid0ArgSkeleton + archEmitHelperCall(HelperRunCalleeAfterFrameInline)
// + PopVoid0ArgSkeleton.
//
// **Spike 1 phase**: archSupportsFrameInline=false blocks real triggering, so
// this counter is currently always 0; it only increments after Step C-2 wires
// it up + Step D flips archSupportsFrameInline=true, as prove-the-path hit
// evidence.
var specFrameInlineHits uint64

// specFrameInlineRunHits is the PJ5 Option B Spike 1 frame building inline
// Run-time reach count (how many times runFrameInlineDispatcher is called, the
// raxSpec==ExitInlineHelper path actually firing). Per §9.20.9 commit-5i,
// distinguishes Compile hit vs Run-time reach.
var specFrameInlineRunHits uint64

// SpecRegKHits returns the cumulative reg-K template compile hit count. Test
// use only.
func SpecRegKHits() uint64 { return atomic.LoadUint64(&specRegKHits) }

// SpecRegRegHits returns the cumulative reg-reg template compile hit count.
// Test use only.
func SpecRegRegHits() uint64 { return atomic.LoadUint64(&specRegRegHits) }

// SpecChainHits returns the cumulative chain-KK template compile hit count.
// Test use only.
func SpecChainHits() uint64 { return atomic.LoadUint64(&specChainHits) }

// SpecForLoopHits returns the cumulative FORLOOP template compile hit count.
// Test use only.
func SpecForLoopHits() uint64 { return atomic.LoadUint64(&specForLoopHits) }

// SpecTableHits returns the cumulative IC ArrayHit template compile hit count.
// Test use only.
func SpecTableHits() uint64 { return atomic.LoadUint64(&specTableHits) }

// SpecCallVoidHits returns the cumulative PJ5 CALL void form Compile hit count.
// Test use only.
func SpecCallVoidHits() uint64 { return atomic.LoadUint64(&specCallVoidHits) }

// SpecTailCallHits returns the cumulative PJ5 TAILCALL form Compile hit count.
// Test use only.
func SpecTailCallHits() uint64 { return atomic.LoadUint64(&specTailCallHits) }

// SpecSelfCallHits returns the cumulative PJ5 SELF method call inline form
// Compile hit count. Test use only.
func SpecSelfCallHits() uint64 { return atomic.LoadUint64(&specSelfCallHits) }

// SpecSelfCallSpecHits returns the cumulative PJ5 SELF + CALL spec template
// form Compile hit count (on IC NodeHit taking the byte-level template). Test
// use only.
func SpecSelfCallSpecHits() uint64 { return atomic.LoadUint64(&specSelfCallSpecHits) }

// SpecFrameInlineHits returns the cumulative PJ5 Option B Spike 1 frame
// building inline Compile hit count (BuildVoid0ArgSkeleton + helper call +
// PopVoid0ArgSkeleton). Test use only. Currently always 0 in the Spike 1 phase
// (blocked by archSupportsFrameInline=false).
func SpecFrameInlineHits() uint64 { return atomic.LoadUint64(&specFrameInlineHits) }

// SpecFrameInlineRunHits returns the cumulative PJ5 Option B Spike 1 frame
// building inline Run-time reach count (how many times runFrameInlineDispatcher
// is called, the raxSpec==ExitInlineHelper path actually firing).
// **Difference from SpecFrameInlineHits**: a Compile hit only proves the emit
// segment was produced; a Run-time reach proves the actual mmap segment's SELF
// NodeHit guard passed + the ExitHelperRequest segment returned RAX=3 + the Run
// side dispatcher actually took over. Spike 1's wired-up prove-the-path strong
// assertion uses this probe.
func SpecFrameInlineRunHits() uint64 { return atomic.LoadUint64(&specFrameInlineRunHits) }

// Note: the zero-cross path hit probe lives in
// crescent.State.frameInlineZeroCrossHits (per §9.20.12 commit-5u); because
// crescent cannot import jit (circular dependency) + needs to access
// st.bridge.GibbousCodeOf, the counter is stored at State level + read
// directly from the st field by e2e tests.

// ResetSpecHits zeroes all spec hit counters (called before a test starts, to
// prevent leftover accumulation from earlier tests affecting assertions). Test
// use only.
func ResetSpecHits() {
	atomic.StoreUint64(&specRegKHits, 0)
	atomic.StoreUint64(&specRegRegHits, 0)
	atomic.StoreUint64(&specChainHits, 0)
	atomic.StoreUint64(&specForLoopHits, 0)
	atomic.StoreUint64(&specTableHits, 0)
	atomic.StoreUint64(&specCallVoidHits, 0)
	atomic.StoreUint64(&specTailCallHits, 0)
	atomic.StoreUint64(&specSelfCallHits, 0)
	atomic.StoreUint64(&specSelfCallSpecHits, 0)
	atomic.StoreUint64(&specFrameInlineHits, 0)
	atomic.StoreUint64(&specFrameInlineRunHits, 0)
	atomic.StoreUint64(&specP4DeoptHits, 0)
	atomic.StoreUint64(&specP4StuckHits, 0)
}

// incSpecRegKHits in-package ++ (called when Compile triggers useSpecRegK).
func incSpecRegKHits() { atomic.AddUint64(&specRegKHits, 1) }

// incSpecRegRegHits in-package ++ (called when Compile triggers useSpec reg-reg).
func incSpecRegRegHits() { atomic.AddUint64(&specRegRegHits, 1) }

// incSpecChainHits in-package ++ (called when Compile triggers useSpecChain).
func incSpecChainHits() { atomic.AddUint64(&specChainHits, 1) }

// incSpecForLoopHits in-package ++ (called when Compile triggers FORLOOP inline).
func incSpecForLoopHits() { atomic.AddUint64(&specForLoopHits, 1) }

// incSpecTableHits in-package ++ (called when Compile triggers IC ArrayHit inline).
func incSpecTableHits() { atomic.AddUint64(&specTableHits, 1) }

// incSpecCallVoidHits in-package ++ (called when Compile triggers PJ5 CALL void form inline).
func incSpecCallVoidHits() { atomic.AddUint64(&specCallVoidHits, 1) }

// incSpecTailCallHits in-package ++ (called when Compile triggers PJ5 TAILCALL form inline).
func incSpecTailCallHits() { atomic.AddUint64(&specTailCallHits, 1) }

// incSpecSelfCallHits in-package ++ (called when Compile triggers PJ5 SELF method call inline).
func incSpecSelfCallHits() { atomic.AddUint64(&specSelfCallHits, 1) }

// incSpecSelfCallSpecHits in-package ++ (called when Compile triggers PJ5 SELF + CALL spec template).
func incSpecSelfCallSpecHits() { atomic.AddUint64(&specSelfCallSpecHits, 1) }

// incSpecFrameInlineHits in-package ++ (called when Compile triggers PJ5
// Option B Spike 1 frame building inline). Per §9.20 Spike 1. Currently blocked
// by archSupportsFrameInline=false; the call site is left for Step C-2 wire-up
// (the compileSpecSelfCall useFrameInline branch).
func incSpecFrameInlineHits() { atomic.AddUint64(&specFrameInlineHits, 1) }

// incSpecFrameInlineRunHits Run-time ++ (on entering runFrameInlineDispatcher).
// Per §9.20.9 commit-5i, distinguishes Compile vs Run-time.
func incSpecFrameInlineRunHits() { atomic.AddUint64(&specFrameInlineRunHits, 1) }
