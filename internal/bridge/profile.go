// Heat counters and ProfileData (`docs/design/p2-bridge/01-profiling.md`).
package bridge

import "github.com/Liam0205/wangshu/internal/bytecode"

// Threshold constants (01 §5.1 suggested values — they do not affect
// correctness, only when compilation kicks in; calibrated after measurement).
const (
	// HotBackEdgeThreshold: a single back-edge pc becomes a promotion
	// candidate once its back-jump count reaches this value.
	// 1000 is a "conservative yet fast enough to cross" compromise (LuaJIT
	// hotloop=56 / V8 OSR=256 are both lower, but they have OSR; wangshu
	// uses try-compile and can afford to stay one notch more conservative).
	HotBackEdgeThreshold uint32 = 1000

	// HotEntryThreshold: the cumulative call-count threshold at a function
	// entry. 200 is enough to judge it hot — the typical case: an outer loop
	// of 1000 iterations each calling a helper, and by 200 the helper is
	// already confirmed to be a hot spot.
	HotEntryThreshold uint32 = 200

	// MinPromotableCodeLen: the lower bound on opcode count for a Proto to be
	// a promotion candidate. When Proto.Code length is strictly below this
	// value, OnEnter/OnBackEdge **still accumulate the EntryCount/BackEdge
	// counters** (profile diagnostics stay complete), but **skip the
	// considerPromotion call** once the threshold is crossed — for such
	// "short workload" protos, after promotion the wasm dispatch + host↔wasm
	// boundary overhead outweighs the interpreter dispatch gain (measured:
	// a pineapple-style 4-opcode arithmetic f ran 19% slower than the
	// interpreter after promotion; see issue #21 profile evidence: in the
	// cpu profile top 200, wasm overhead dominates and the hook path is
	// negligible). The guard is placed "after the threshold but before
	// considerPromotion" rather than "before counter accumulation", to keep
	// the profile_test diagnostic assertions valid (EntryCount/MaxBackEdge
	// accurately reflect the call count of short protos).
	//
	// **10 is an empirical initial value**: it covers pure arithmetic of
	// 4-6 opcodes (GETGLOBAL/MUL/ADD/RETURN kind) plus a little margin,
	// without affecting promotion of functions with real loops / multi-step
	// arithmetic / table lookups at 10+ opcodes. It can later be pinned down
	// precisely via micro-benchmark (the P3_Kernels calibration shape).
	// **It does not affect correctness** — the promotion decision is a perf
	// optimization, and leaving a short proto on the interpreter path is
	// equivalent to P1-only build behavior.
	//
	// **Relation to the F1-F7 gates**: F1-F6 are "non-compilable shapes"
	// (structural exclusions like vararg/coroutine/oversize), F7 is a
	// "backend capability query"; MinPromotableCodeLen is "compilable but
	// not worth compiling" — independent of the compilability decision, it
	// does not write ReasonsBitmap, and is merely a fast-path guard at the
	// sampler entry.
	MinPromotableCodeLen = 10
)

// ProfileData is the profile data of a Proto on a given State (01 §2.2).
//
// Design points:
//   - Its physical storage is the State-private profileTable (01 §6.3 scheme
//     (B)), not a field alongside Proto — this avoids the race of multiple
//     States concurrently writing the counters, consistent with wangshu's
//     concurrency convention that a Program is read-only shared across
//     States (11 §1.4 / §8).
//   - It does not enter the arena or the GC root set (01 §2.4): it lives on
//     the Go heap with the same lifetime as Proto.
//   - Counter accumulation semantics: cross-call function-level aggregation
//     (non-CallInfo-frame-level).
//
// Field ownership (single-source-of-truth division):
//   - EntryCount / BackEdge:  owned by 01-profiling (back-edge / entry sampling)
//   - Feedback:               owned by 02-ic-feedback (P2 writes, P3/P4 read; P2 does not consume)
//   - Compilable / Reasons:   owned by 03-compilability-analysis (written once at Compile, read-only afterward)
//   - TierState / CompileTried: owned by 04-try-compile-fallback (state-machine fields)
type ProfileData struct {
	// —— Counters (01-profiling §2.2) ——
	EntryCount uint32   // function entry count: incremented on each enterLuaFrame
	BackEdge   []uint32 // back-jump counts indexed by back-edge pc (dense array, lazily allocated)

	// —— IC feedback (02-ic-feedback §4.5) ——
	// One-shot aggregation (the P2 initial version aggregates only once, on
	// first promotion, 02 §4.5); P3/P4 consume it read-only.
	Feedback *TypeFeedback

	// —— Compilability (03-compilability-analysis §5.3) ——
	// Written once at Compile, read-only at runtime; visibility of concurrent
	// reads is guaranteed automatically by the Go memory model
	// (write-once before any reader, 03 §5.4).
	Compilable Compilability
	Reasons    ReasonsBitmap // F1-F7 rejection-reason bitmask (03 §5.3), used for diagnostic logging

	// —— State machine (04-try-compile-fallback §3) ——
	TierState    TierState // TierInterp / TierGibbous / TierStuck
	CompileTried bool      // whether compilation has been attempted (prevents TierStuck from retrying repeatedly, 04 §3.2)

	// recheckedAtEntry dedups recheckCompilabilityRuntime while the
	// forceAll retry window holds a declined proto on TierInterp
	// (issue #40). It stores EntryCount+1 as of the last recheck
	// (0 = never ran). Promotion only takes effect on a later entry
	// (there is no OSR), so re-running the full backend analysis on
	// every back edge of the same entry buys nothing — on a declined
	// 2M-back-edge kernel it measured 22% CPU and 1.5 GB/op.
	// OnBackEdge clears it once when a back edge crosses
	// HotBackEdgeThreshold, granting one extra warm-IC recheck per pc.
	recheckedAtEntry uint32

	// floorExempt caches the backend's FloorExempter verdict for a
	// below-floor proto (issue #67): asked once, when the proto first
	// crosses the heat threshold (its ICs are as warm as they will
	// get by then, so the answer is stable — same absorption rationale
	// as the PromotionGater verdict). Without the cache the eligibility
	// scan (CFG build + opcode walk) would run on every OnEnter past
	// the threshold — per call on exactly the hottest protos.
	floorExempt floorExemptState
}

// floorExemptState is the cached FloorExempter verdict (issue #67).
type floorExemptState uint8

const (
	floorExemptUnasked floorExemptState = iota // backend not yet consulted
	floorExemptYes                             // exempt: floor does not block
	floorExemptNo                              // not exempt: floor blocks
)

// MaxBackEdge returns the largest single-back-edge cumulative count in this
// ProfileData.
//
// A single back edge crossing the threshold approximates "the function is hot"
// (01 §5.2): there is no need to sum all back edges every time — as long as
// some one back edge has accumulated enough heat, the function is considered
// worth compiling. This function is used mainly for diagnostic logging to
// show "N cumulative back edges" (04 §6.1 promotion log format).
func (pd *ProfileData) MaxBackEdge() uint32 {
	var m uint32
	for _, c := range pd.BackEdge {
		if c > m {
			m = c
		}
	}
	return m
}

// resetCountersForReuse clears only the back-edge/entry counters, preserving
// the state-machine fields (TierState / Compilable / Reasons). **Currently
// unused** — reserved for scheme (C), the dual-table hybrid, under a
// sync.Pool short-lived State shape (01 §6.4). This interface is a
// placeholder for the current phase; the real aggregation lands once
// measurement shows that scheme (B)'s uneven accumulation speed seriously
// affects when the heat threshold takes effect, at which point it is enabled.
//
//nolint:unused // interface placeholder, used when scheme (C) is enabled.
func (pd *ProfileData) resetCountersForReuse() {
	pd.EntryCount = 0
	for i := range pd.BackEdge {
		pd.BackEdge[i] = 0
	}
	pd.recheckedAtEntry = 0
}

// allocBackEdge lazily allocates the backEdge array sized to the Code length on the first back-edge hit (01 §2.3).
//
// This avoids the waste of "a cold Proto that never has a back edge yet still has an array
// reserved for it". Returns in a method-chaining-friendly form of ProfileData.
func (pd *ProfileData) allocBackEdge(proto *bytecode.Proto) {
	if pd.BackEdge == nil {
		pd.BackEdge = make([]uint32, len(proto.Code))
	}
}
