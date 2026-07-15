//go:build wangshu_p4

// p4state.go — P4 internal speculation sub-state machine (per the field
// definitions in docs/design/p4-method-jit/04-osr-deopt.md §5 + §11).
//
// Scheme A: the P2 tier enum stays three-state (TierInterp / TierGibbous /
// TierStuck); P4 keeps its own per-proto sub-state field p4SpecState[proto]
// (P4Speculative / P4Deoptimized / P4StuckSpeculation) layered on top of P2
// TierGibbous, invisible to P2.
//
// STATUS (active on amd64): the SELF + CALL spec template (`obj:m()` OOP
// shape) is wired up on amd64 (archSupportsSpec() returns true). When a spec
// segment's guard fails (receiver reshaped / key degraded / NodeVal==nil),
// code.go's runSpecSelfCall calls onOSRExit(proto) to account the miss and
// fall back to host.Self; once the count reaches DeoptThreshold the proto
// flips to P4Deoptimized and the speculative code is withdrawn. This
// spec-template deopt accounting is live in production and is proven by
// TestPJ5_SelfCall_E2E_SpecTemplate_OSRExitToDeopt / _DeoptStorm
// (SpecP4DeoptHits grows).
//
// Two distinct deopt mechanisms must not be conflated: this file is the
// SPEC-TEMPLATE guard-miss deopt accounting (active); issue #50's seg2seg
// uses a separate "virtual frame + deopt-redo" — a seg2seg callee whose guard
// misses re-runs the equivalent interpreter semantics (deopt-redo) without
// touching this file. The function-level OSR materialization (rebuilding an
// interpreter frame) that issue #51 sketched never materialized; it was
// superseded by seg2seg's deopt-redo (ruling: 04-osr-deopt.md header +
// spike/p4callinline/DECISION.md).
package jit

import (
	"sync"
	"sync/atomic"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// P4SpecState is the P4 internal speculation sub-state (per 04 §5.2 state diagram).
type P4SpecState uint8

const (
	// P4SpecUnknown: not tracked / first seen (the default value; once a Proto
	// is promoted to P4, the next install's compile phase sets it to
	// P4Speculative)
	P4SpecUnknown P4SpecState = iota

	// P4Speculative: the P4 speculative code is installed and its guard is
	// running. A single OSR exit failure does not switch state (keep observing,
	// deopt count += 1)
	P4Speculative

	// P4Deoptimized: the deopt count exceeded the threshold, the P4 side
	// withdrew the speculative code, and it waits for the interpreter-phase IC
	// to naturally dilute confidence before retraining + recompiling
	P4Deoptimized

	// P4StuckSpeculation: recompiles exceeded MaxRecompileTries and it still
	// keeps deopting, so the P4 side blacklists speculation (P2's view is still
	// TierGibbous, it just has no speculative GibbousCode installed right now)
	P4StuckSpeculation
)

// String returns the state name (for logging / probe readback).
func (s P4SpecState) String() string {
	switch s {
	case P4Speculative:
		return "P4Speculative"
	case P4Deoptimized:
		return "P4Deoptimized"
	case P4StuckSpeculation:
		return "P4StuckSpeculation"
	default:
		return "P4SpecUnknown"
	}
}

// p4SpecEntry is the per-Proto field for p4SpecState[proto] (per 04 §5.6 field set).
//
// **Scheme A** (P4 self-managed): it attaches **no** fields to P2's
// ProfileData / TierState — from P2's view the Proto is still TierGibbous, with
// the P4-side sub-state machine layered on top independently.
type p4SpecEntry struct {
	// state is the current sub-state (P4Speculative / P4Deoptimized / P4StuckSpeculation).
	state P4SpecState

	// deoptCount is the cumulative OSR exit count (per 04 §5.2, += 1 per failure).
	// Reaching DeoptThreshold switches to P4Deoptimized and withdraws the speculative code.
	deoptCount uint32

	// recompileCount is the cumulative recompile count (per 04 §5.3 hard cap on recompiles).
	// Reaching MaxRecompileTries switches to P4StuckSpeculation and blacklists speculation.
	recompileCount uint32
}

// p4SpecStateMap is the proto → p4SpecEntry map.
//
// **package-level global map** (per PR #26 external-review comment correction
// 2026-06-28): the implementation is the package-level global variable
// `var p4SpecState = make(map[*bytecode.Proto]*p4SpecEntry)`, **shared across
// multiple States** — guarded by `sync.Mutex` (load / store / increment all go
// through the lock), and V18 -race friendly (per the race-free discipline
// established in R3).
//
// **Multi-State concurrency-safety argument**:
//   - *bytecode.Proto is globally unique (a per-Proto singleton; frontend.compile
//     emits a fixed pointer)
//   - when multiple States run the same Proto they share the p4SpecEntry state
//     (the three fields deoptCount/state/recompileCount); the state semantics are
//     per-Proto rather than per-State, which is correct;
//   - across distinct Protos the map key isolates them, so there is no conflict;
//   - concurrent increments of deoptCount within a single entry are serialized by
//     p4SpecMu, so there is no race;
//   - a recompile trigger (deoptCount ≥ DeoptThreshold), though it may be triggered
//     concurrently by multiple States, also takes the lock on the onP4Install path,
//     so only the first state transition takes effect and subsequent state==
//     P4Speculative ⇒ equivalently idempotent.
//
// **Historical note** (corrected 2026-06-28): this was previously described as a
// "per-Compiler singleton (per-State, since jit.Compiler is per-State)", which
// does not match the implementation. p4SpecState is a package-level global,
// shared across multiple States.
//
// **OSR exit path heat**: OSR exit is a cold path (only triggered when a single
// frame's speculation fails), so the lock overhead is negligible. If the OSR exit
// trigger rate turns out to be high later, this can switch to sync.Map (this batch
// is the v0 simplification).
var (
	p4SpecMu    sync.Mutex
	p4SpecState = make(map[*bytecode.Proto]*p4SpecEntry)
)

// DeoptThreshold is the per-Proto cumulative OSR exit count threshold (per 04 §5.2 + §5.6 calibration).
// **Placeholder value**: to be calibrated during measurement (per 04 §5.6: typically 3-5); this batch's v0 uses a loose 16 to avoid false triggers.
const DeoptThreshold uint32 = 16

// MaxRecompileTries is the per-Proto cumulative recompile count cap (per 04 §5.3 + §5.6 calibration).
// **Placeholder value**: to be calibrated during measurement (per 04 §5.3: typically 1-2); this batch's v0 uses 2.
const MaxRecompileTries uint32 = 2

// onOSRExit handles a single spec-template guard-miss event (04 §5.1).
//
// It bumps the deopt count by 1, and on reaching the threshold withdraws the
// speculative code and flips the proto to P4Deoptimized. Does not touch the
// P2 tierState (Scheme A).
//
// STATUS (active on amd64): called from code.go's runSpecSelfCall when a SELF
// spec segment guard fails. See the file header for the active-vs-superseded
// deopt mechanism distinction.
func onOSRExit(proto *bytecode.Proto) {
	if proto == nil {
		return
	}
	p4SpecMu.Lock()
	defer p4SpecMu.Unlock()
	entry := p4SpecState[proto]
	if entry == nil {
		entry = &p4SpecEntry{state: P4Speculative}
		p4SpecState[proto] = entry
	}
	entry.deoptCount++
	if entry.deoptCount < DeoptThreshold {
		return // single failure, keep observing
	}
	// Threshold reached: withdraw the P4 speculative code + switch to P4Deoptimized
	if entry.recompileCount >= MaxRecompileTries {
		entry.state = P4StuckSpeculation // blacklist speculation (P4-internal absorbing state)
		incSpecP4StuckHits()
		return
	}
	entry.state = P4Deoptimized
	entry.deoptCount = 0 // reset the count, wait for the IC to naturally dilute confidence before retraining + recompiling
	incSpecP4DeoptHits()
}

// onP4Install registers a proto's first / recompiled P4 speculative code
// (04 §5.3 bumps recompileCount).
//
// Sets the proto state to P4Speculative, and bumps recompileCount on a
// recompile. Does not touch the P2 tierState (Scheme A).
//
// Call contract: invoked by compileSpecSelfCall after installing the SELF
// spec template (compiler.go). Active on amd64 for the OOP `obj:m()` shape.
func onP4Install(proto *bytecode.Proto) {
	if proto == nil {
		return
	}
	p4SpecMu.Lock()
	defer p4SpecMu.Unlock()
	entry := p4SpecState[proto]
	if entry == nil {
		entry = &p4SpecEntry{state: P4Speculative}
		p4SpecState[proto] = entry
		return
	}
	// Recompile: state goes from P4Deoptimized back to P4Speculative, recompileCount += 1
	if entry.state == P4Deoptimized {
		entry.state = P4Speculative
		entry.recompileCount++
	}
}

// P4SpecStateOf returns the proto's current sub-state (for tests + debugging).
func P4SpecStateOf(proto *bytecode.Proto) P4SpecState {
	if proto == nil {
		return P4SpecUnknown
	}
	p4SpecMu.Lock()
	defer p4SpecMu.Unlock()
	entry := p4SpecState[proto]
	if entry == nil {
		return P4SpecUnknown
	}
	return entry.state
}

// ResetP4SpecState clears p4SpecState entirely (for test isolation).
func ResetP4SpecState() {
	p4SpecMu.Lock()
	defer p4SpecMu.Unlock()
	p4SpecState = make(map[*bytecode.Proto]*p4SpecEntry)
}

// specP4DeoptHits is the P4Deoptimized state-transition hit count (prove-the-path probe).
var specP4DeoptHits uint64

// specP4StuckHits is the P4StuckSpeculation state-transition hit count.
var specP4StuckHits uint64

// SpecP4DeoptHits returns the cumulative P4Deoptimized state-transition hit count (for tests).
func SpecP4DeoptHits() uint64 { return atomic.LoadUint64(&specP4DeoptHits) }

// SpecP4StuckHits returns the cumulative P4StuckSpeculation state-transition hit count (for tests).
func SpecP4StuckHits() uint64 { return atomic.LoadUint64(&specP4StuckHits) }

// incSpecP4DeoptHits does an in-package ++ (called when onOSRExit reaches P4Deoptimized).
func incSpecP4DeoptHits() { atomic.AddUint64(&specP4DeoptHits, 1) }

// incSpecP4StuckHits does an in-package ++ (called when onOSRExit reaches P4StuckSpeculation).
func incSpecP4StuckHits() { atomic.AddUint64(&specP4StuckHits, 1) }
