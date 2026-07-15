// Bridge — the P2 main structure, threading through counting / IC feedback /
// compilability / state machine / promotion logging
// (`docs/design/p2-bridge/01-profiling.md` §6 + `04-try-compile-fallback.md` §3-§5).
package bridge

import (
	"fmt"
	"sync"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// Bridge is the P2 decision engine's main structure (one per State;
// profileTable is State-private and not shared across goroutines — the
// 01 §6.3 (B) design).
//
// Injection-style wiring (avoids a reverse dependency on internal/crescent):
//   - p3 is injected by the facade layer (wangshu.NewState) once P3 is live;
//     P1-only builds / P2 PB0 do not inject it, so p3 == nil →
//     SupportsAllOpcodes returns false → F7 permanently deems it
//     uncompilable, matching P1 behavior.
//   - logger is injected by the facade layer with the stdLogger default
//     implementation; tests may inject a capturing Logger to verify the
//     promotion log format (the 04 §6.5 test assertions).
type Bridge struct {
	// profileTable: Proto -> ProfileData (State-private, 01 §6.3).
	// Lazily built: allocated the first time onBackEdge / onEnter hits a Proto.
	profileTable map[*bytecode.Proto]*ProfileData

	// pdByID is a ProtoID-indexed fast path over profileTable (issue
	// #94): OnEnter runs on every Lua frame entry, and on declined
	// call-dense workloads (the [^p3-gate] rows) ~94% of those calls do
	// nothing but "look up pd, see TierState != TierInterp, return" — a
	// map access costing ~6% of total run time. The interpreter always
	// has the ProtoID at hand (callInfo.protoID / enterLuaFrame's pid),
	// so a slice index replaces the map on the hot path. Grown by
	// GrowProfileIndex after LoadProgram; a pid outside the slice (or a
	// nil slot from a proto first seen by a map-only path) falls back to
	// profileTable, so callers without a real pid (tests, mock drivers)
	// keep working unchanged.
	pdByID []*ProfileData

	// p3: the P3/P4 compiler interface (05 §2). Injection-style; nil means
	// P1-only / mock not loaded.
	p3 P3Compiler

	// gibbousCodes: promoted Proto → GibbousCode (attached here after 04 §4.4
	// installGibbous installs it, keeping the wasm module / native code
	// section from being GC'd early, until the Bridge itself is released).
	gibbousCodes map[*bytecode.Proto]GibbousCode

	// compileMu: the global mutex for the try-compile + installGibbous
	// critical section (the 04 §4.5 (A) design — Bridge-level coarse
	// granularity, simple and reliable; not compiling multiple Protos in
	// parallel is a reasonable cost, since P2 is off the hot path).
	compileMu sync.Mutex

	// logger: the promotion-log diagnostic interface (04 §6.4).
	// Injection-style; silentLogger substitutes when nil (the default unit
	// test scenario).
	logger Logger

	// aggregator: the IC feedback aggregator (02 §6.4). The Bridge embeds
	// one; on promotion, considerPromotion calls Aggregate(proto) to produce
	// the TypeFeedback fed to P3.
	aggregator *Aggregator

	// forceAll: force-promote-all mode (the P3 PW9 test entry, 08 §2.2). Once
	// set, the first OnEnter/OnBackEdge call triggers considerPromotion
	// (bypassing the heat threshold), so every CompCompilable Proto compiles
	// directly on its first execution — this removes the timing
	// nondeterminism of "which Protos get hot enough", making cross-tier
	// differential tests reproducible and maximizing coverage. It does NOT
	// bypass the compilability gate (F1-F7 excluded shapes still go through
	// crescent, 08 §2.3.1). testing-only.
	forceAll bool

	// hotEntry / hotBackEdge are the effective heat thresholds,
	// defaulting to the package constants (HotEntryThreshold /
	// HotBackEdgeThreshold) in NewBridge. SetHotThresholds overrides
	// them for auto-mode testing: LOWERING them lets short fuzz inputs
	// and conformance cases reach the natural-heat promotion decision
	// (recheckCompilabilityRuntime, PromotionGater, the short-proto
	// floor and its FloorExempter — none of which forceAll exercises),
	// while RAISING them to MaxUint32 yields a guaranteed
	// never-promoted interpreter baseline on a P3/P4 build. The
	// override changes WHEN the auto decision runs, never WHAT it
	// decides. testing-only, same discipline as forceAll.
	hotEntry    uint32
	hotBackEdge uint32

	// minPromotableLen is the effective short-proto floor, snapshotted
	// from the backend in SetP3Compiler (MinPromotableCodeLen default,
	// or the backend's MinPromotableLen override). Zero means "no
	// backend injected yet" — OnEnter/OnBackEdge fall back to the
	// package default in that window.
	minPromotableLen int

	// floorExempter is the backend's optional per-proto floor exemption
	// hook (issue #67), snapshotted in SetP3Compiler alongside
	// minPromotableLen. nil when the backend does not implement
	// FloorExempter — the floor then applies unconditionally.
	floorExempter FloorExempter

	// tierOff is the runtime tier kill switch (production admin API,
	// unlike forceAll/hotEntry which are testing-only). When set:
	//   - OnEnter/OnBackEdge short-circuit before counting, so hot
	//     counters do not accrue and no new promotions happen. (A
	//     zero-counted ProfileData slot may still be created by the
	//     get-or-create profileOf on the way in — the slot exists,
	//     nothing is written to it. Once the switch flips back on,
	//     the slot resumes counting normally.)
	//   - GibbousCodeOf returns nil, so every dispatch decision falls
	//     back to the interpreter — including protos promoted BEFORE
	//     the switch flipped. Installed GibbousCode stays in
	//     gibbousCodes (no recompile on re-enable).
	// The Bridge is per-State and single-goroutine (01 §6.3 (B)), so a
	// plain bool is race-free, same as forceAll. A native/wasm run
	// already in flight when the host flips the switch finishes
	// normally; the switch takes effect at the next dispatch decision.
	tierOff bool

	// stuckNotCompilable / stuckDeclined / stuckCompileFailed break the
	// TierStuck absorbing state down by cause for TierStatsSnapshot
	// (the three Stuck transition sites in considerPromotion each bump
	// exactly one). Kept as counters rather than re-derived from
	// ProfileData because CompileTried is set on every Stuck path and
	// cannot distinguish them after the fact.
	stuckNotCompilable int
	stuckDeclined      int
	stuckCompileFailed int
}

// MinPromotableLener is an optional interface a P3Compiler may
// implement to override the package-level MinPromotableCodeLen floor.
// P4's native backend dispatches through a direct mmap call (no wasm
// module boundary), so tiny protos that lose money on P3 wasm can
// still win on P4.
type MinPromotableLener interface {
	MinPromotableLen() int
}

// PromotionGater is an optional interface a P3Compiler may implement
// to decline promotion of protos it CAN compile but expects to run
// slower than the interpreter (profitability, not capability — the
// capability answer stays in SupportsAllOpcodes). The bridge consults
// it in auto mode only; forceAll bypasses it so differential-test
// coverage keeps promoting every compilable shape (issue #39).
//
// Declined protos absorb to TierStuck: the judgment is static (op-mix
// density), so re-asking on later entries cannot change the answer.
type PromotionGater interface {
	WorthPromoting(proto *bytecode.Proto) bool
}

// FloorExempter is an optional interface a P3Compiler may implement to
// exempt specific protos from the short-proto floor (issue #67). The
// floor exists because promoted tiny protos lose to the backend's fixed
// per-call dispatch costs — but a backend may know that a proto's hot
// dispatch channel skips those costs entirely. P4's case: a
// seg2seg-eligible proto called from an already-promoted caller is
// entered by an in-segment call, never paying nativeCode.Run's fixed
// overhead, so the floor's calibration does not apply to it
// (spectral-norm's 9-op `A(i,j)` at 144k calls/run was floored to the
// interpreter, costing an ExecutePlainCall round trip per call).
//
// Consulted in auto mode only (forceAll already bypasses the floor),
// and only for protos below the floor, after they cross the heat
// threshold — so the proto's ICs are warm and the answer is stable.
// The verdict is cached per proto in ProfileData (see floorExempt).
type FloorExempter interface {
	ExemptFromFloor(proto *bytecode.Proto) bool
}

// NewBridge constructs an empty Bridge, attached to a State (injected via a
// crescent-side setter).
//
// Both p3 and logger can be injected later via SetXxx; the constructor does
// not require them (this supports the "build the Bridge first, inject P3
// later" transitional form before P3 has landed).
func NewBridge() *Bridge {
	return &Bridge{
		profileTable: make(map[*bytecode.Proto]*ProfileData),
		gibbousCodes: make(map[*bytecode.Proto]GibbousCode),
		aggregator:   NewAggregator(),
		hotEntry:     HotEntryThreshold,
		hotBackEdge:  HotBackEdgeThreshold,
	}
}

// SetHotThresholds overrides the natural-heat promotion thresholds
// (testing-only; see the hotEntry field doc). Zero keeps the current
// value for that threshold.
func (b *Bridge) SetHotThresholds(entry, backEdge uint32) {
	if entry > 0 {
		b.hotEntry = entry
	}
	if backEdge > 0 {
		b.hotBackEdge = backEdge
	}
}

// SetP3Compiler injects the P3/P4 compiler. It may be called at any time
// after the Bridge is created, but must be wired in before compilation is
// actually triggered (considerPromotion taking the try-compile path).
func (b *Bridge) SetP3Compiler(p3 P3Compiler) {
	b.p3 = p3
	// Backends with cheaper dispatch than P3 wasm may opt into a lower
	// short-proto floor (see MinPromotableLener). Snapshot it once here
	// so the hot OnEnter/OnBackEdge guard stays a plain int compare.
	b.minPromotableLen = MinPromotableCodeLen
	if ml, ok := p3.(MinPromotableLener); ok {
		if v := ml.MinPromotableLen(); v > 0 {
			b.minPromotableLen = v
		}
	}
	// Optional per-proto floor exemption (issue #67); nil when the
	// backend does not implement it.
	b.floorExempter, _ = p3.(FloorExempter)
}

// SetLogger injects the promotion-log interface (capturable by tests; the
// facade layer wires in the stdLogger default implementation).
func (b *Bridge) SetLogger(l Logger) { b.logger = l }

// effectiveMinPromotableLen returns the short-proto floor in effect:
// the backend-snapshotted value when a backend has been injected, or
// the package default before injection.
func (b *Bridge) effectiveMinPromotableLen() int {
	if b.minPromotableLen > 0 {
		return b.minPromotableLen
	}
	return MinPromotableCodeLen
}

// Aggregator exposes the IC feedback aggregator for the considerPromotion
// promotion path (Aggregate(proto) → *TypeFeedback). P2 writes it but does
// NOT consume it (02 §7): installFeedback writes ProfileData.Feedback, and
// P2 itself never reads that field.
func (b *Bridge) Aggregator() *Aggregator { return b.aggregator }

// ProfileOf returns the Proto's ProfileData on this State, lazily building
// the table.
//
// NOT a commonly used hot-path interface: onBackEdge / onEnter internally
// use profileOf to get pd, but that path is designed for internal calls;
// external diagnostic tools use this interface.
func (b *Bridge) ProfileOf(proto *bytecode.Proto) *ProfileData { return b.profileOf(proto) }

// profileOf is the internal alias of ProfileOf. Naming consistency:
// Bridge-internal private helpers use lowercase, public APIs uppercase.
//
// When lazily building the table, it syncs Compilability from the Proto's
// side field (already written at Compile time, consistent across States;
// profileTable is a State-private copy, copied once the first time a Proto
// is seen).
func (b *Bridge) profileOf(proto *bytecode.Proto) *ProfileData {
	pd, ok := b.profileTable[proto]
	if !ok {
		pd = &ProfileData{
			Compilable: Compilability(proto.Compilability),
			Reasons:    ReasonsBitmap(proto.CompReasons),
		}
		b.profileTable[proto] = pd
	}
	return pd
}

// GrowProfileIndex sizes the ProtoID-indexed ProfileData fast path
// (issue #94). crescent calls it after LoadProgram with the new total
// proto count; pids ≥ the previous length get nil slots that
// profileOfID fills lazily. Never shrinks.
func (b *Bridge) GrowProfileIndex(n int) {
	if n <= len(b.pdByID) {
		return
	}
	grown := make([]*ProfileData, n)
	copy(grown, b.pdByID)
	b.pdByID = grown
}

// profileOfID is profileOf with a ProtoID-indexed slice fast path
// (issue #94). The hot case — steady-state OnEnter on an
// already-decided proto — is one bounds check and one slice load. A
// miss (pid out of range, or first sight of this proto) delegates to
// profileOf so the map stays the authority, then caches the pointer.
func (b *Bridge) profileOfID(proto *bytecode.Proto, pid uint32) *ProfileData {
	if int(pid) < len(b.pdByID) {
		if pd := b.pdByID[pid]; pd != nil {
			return pd
		}
		pd := b.profileOf(proto)
		b.pdByID[pid] = pd
		return pd
	}
	return b.profileOf(proto)
}

// CompilabilityOf is the 04 state machine's query entry (read-only, 03 §5.3).
//
// It prefers the Proto's side field (written once at Compile time, shared
// read-only across States); if the Proto field is zero (P1-only, AnalyzeProto
// not run) it falls back to profileTable (supporting the direct
// SetCompilability writes that may appear on the considerPromotion path —
// mainly used by tests).
//
// The field is written once at compile time and read-only at runtime
// (03 §5.4), so no atomic / mutex is needed.
func (b *Bridge) CompilabilityOf(proto *bytecode.Proto) Compilability {
	if c := Compilability(proto.Compilability); c != CompUnknown {
		return c
	}
	pd, ok := b.profileTable[proto]
	if !ok {
		return CompUnknown
	}
	return pd.Compilable
}

// SetCompilability is written once at Compile time (called by AnalyzeProto,
// landed in PB3).
//
// It prefers to write the Proto's side field (shared read-only across
// States); it also writes the profileTable copy so that the pd.Compilable
// read inside considerPromotion stays consistent (this iteration has
// considerPromotion go through pd.Compilable rather than CompilabilityOf, so
// the two must stay in sync).
//
// Invariant (03 §5.4): the field is written only once, during the Compile
// phase; no runtime path modifies Compilable / Reasons.
func (b *Bridge) SetCompilability(proto *bytecode.Proto, c Compilability, r ReasonsBitmap) {
	proto.Compilability = uint8(c)
	proto.CompReasons = uint16(r)
	pd := b.profileOf(proto)
	pd.Compilable = c
	pd.Reasons = r
}

// SetForceAllPromote toggles force-promote-all mode (the P3 PW9 test entry,
// 08 §2.2).
//
// on=true: thereafter the first OnEnter/OnBackEdge call triggers
// considerPromotion (bypassing the heat threshold) — every CompCompilable
// Proto compiles directly on its first execution. Used in cross-tier
// differential testing to remove the timing nondeterminism of "which Protos
// get hot enough" (reproducible + maximal coverage).
//
// It does NOT bypass the compilability gate: F1-F7 excluded shapes
// (vararg/coroutine/SupportsAllOpcodes-unsupported, etc.) go through crescent
// even under forceAll (inside considerPromotion, comp != CompCompilable →
// Stuck, 08 §2.3.1). testing-only — not part of the wangshu public API
// (force-promote-all is a test switch for removing timing effects, not a
// supported run mode).
func (b *Bridge) SetForceAllPromote(on bool) { b.forceAll = on }

// SetTierEnabled flips the runtime tier kill switch (production admin
// API — see the tierOff field doc). enabled=false stops new promotions
// AND routes already-promoted protos back to the interpreter at the
// next dispatch decision; enabled=true restores tiered execution,
// reusing previously installed GibbousCode without recompiling.
func (b *Bridge) SetTierEnabled(enabled bool) { b.tierOff = !enabled }

// TierEnabled reports the runtime tier kill switch state.
func (b *Bridge) TierEnabled() bool { return !b.tierOff }

// TierStats is the per-State tier observability snapshot (production
// admin API, sister of the SetTierEnabled kill switch). All counts
// come from this Bridge's own profileTable / counters, so with one
// Bridge per State the numbers are State-scoped.
type TierStats struct {
	// Promoted is the number of protos with installed gibbous code
	// (P3 wasm or P4 native, whichever backend is wired).
	Promoted int
	// StuckNotCompilable counts protos absorbed to permanent
	// interpreter because the compilability gate excluded their shape
	// (vararg / coroutine / unsupported opcodes...). Expected and
	// harmless — these shapes are designed to stay interpreted.
	StuckNotCompilable int
	// StuckDeclined counts protos the backend could compile but
	// declined as unprofitable (PromotionGater). Also expected.
	StuckDeclined int
	// StuckCompileFailed counts protos that reached try-compile and
	// failed (backend error or panic). A nonzero value here is worth
	// investigating, unlike the two expected Stuck classes above.
	StuckCompileFailed int
	// Profiled is the number of protos with any profiling data
	// (entered at least once through a sampling hook).
	Profiled int
	// TierEnabled mirrors the runtime kill switch state.
	TierEnabled bool
}

// TierStatsSnapshot returns the current per-State tier distribution.
// Diagnostics path — cheap (counter reads plus two map length reads),
// but not intended for per-frame polling.
func (b *Bridge) TierStatsSnapshot() TierStats {
	return TierStats{
		Promoted:           len(b.gibbousCodes),
		StuckNotCompilable: b.stuckNotCompilable,
		StuckDeclined:      b.stuckDeclined,
		StuckCompileFailed: b.stuckCompileFailed,
		Profiled:           len(b.profileTable),
		TierEnabled:        !b.tierOff,
	}
}

// recheckCompilabilityRuntime re-decides compilability against the real
// backend at runtime (issue #18 / 08 §2.2).
//
// Why it is needed: at compile time analyzeCompilability runs F7
// (checkF7BackendSupport) with a temporary Bridge, where b.p3 == nil → F7
// always fires → every Proto is burned as CompNotCompilable +
// ReasonBackendUnsupp (the analyze_on.go §F7 behavior). This is not a
// statement that "the backend really doesn't support it", only a placeholder
// for "at compile time we don't yet know which P3 will be injected at
// runtime". Re-running F7 at runtime is the "extension after P3 injection"
// that analyze_on.go left open, and this function implements that step.
//
// It does NOT bypass the real compilability gate: it only clears the
// "compile-time placeholder bits" (`ReasonBackendUnsupp` + `ReasonSelfCall`
// — following the P4 PJ5 SELF inline shape's "re-decide after backend
// injection" clearing, the SELF placeholder bit uses the same technique).
// The F1-F6 + F2-a/F2-b real exclusions
// (vararg/coroutine/yield/unknownCall/debug/setfenv/oversize/nested, already
// burned into proto.CompReasons and not AST-dependent) are preserved as-is —
// if any remains set, it stays CompNotCompilable. After clearing the
// placeholders it queries SupportsAllOpcodes against the actually-injected
// backend (the true answer to F7 + the SELF inline shape gate is finished by
// P4 analyzeShape).
//
// Call paths: considerPromotion calls this function in two scenarios:
//
//	(a) the forceAll test entry (08 §2.2 PW9 diff), which force-promotes
//	    before the heat threshold;
//	(b) the natural-heat path (issue #18): after heat crosses
//	    HotEntryThreshold, on seeing pd.Compilable == CompNotCompilable +
//	    placeholder bit + b.p3 != nil, it calls this function to re-decide —
//	    this is the true activation point of the natural-heat promotion path
//	    on a p3/p4 build.
func (b *Bridge) recheckCompilabilityRuntime(proto *bytecode.Proto) Compilability {
	// Take the compile-time-burned F1-F6 + F2-a/F2-b structural exclusion
	// bits (dropping the F7 + SELF placeholders).
	const placeholderReasons = ReasonBackendUnsupp | ReasonSelfCall
	structural := ReasonsBitmap(proto.CompReasons) &^ placeholderReasons
	if structural.HasAny() {
		return CompNotCompilable // F1-F6 / F2-a/F2-b real exclusion, keep the explanation
	}
	// Re-decide F7 against the real backend (b.p3 is now injected); the SELF
	// inline shape is finished inside P4 jit analyzeShape (called within P4
	// SupportsAllOpcodes) — if the PJ5 SELF inline shape guard is not hit,
	// SupportsAllOpcodes returns false, equivalent to declining promotion.
	if b.checkF7BackendSupport(proto) {
		return CompNotCompilable // the real backend does not support this Proto's opcode shape
	}
	return CompCompilable
}

// OnBackEdge is the back-edge sampling hook (01 §4.1).
//
// Call contract (called by the crescent main loop after a FORLOOP / JMP
// back-jump, wired in PB1):
//   - proto: the current frame's Proto;
//   - pc: the back-edge target pc (the value after += SBx).
//
// Guarantees zero allocation (in the steady state, when the threshold isn't
// crossed): a map lookup + slice index + increment + compare — roughly a
// 24ns budget per call (the 01 §4.5 estimate).
//
// MinPromotableCodeLen guard (issue #21): a short proto (Code length <
// MinPromotableCodeLen) still accumulates its counter after the threshold is
// crossed, and only returns before calling considerPromotion — keeping the
// profile diagnostics complete (accurate EntryCount / BackEdge, as profile_test
// expects) while skipping the promotion action (avoiding wasm's downside).
func (b *Bridge) OnBackEdge(proto *bytecode.Proto, pc int32, onMain bool) {
	b.onBackEdgePD(b.profileOf(proto), proto, pc, onMain)
}

// OnBackEdgeID is OnBackEdge with the caller-supplied ProtoID driving the
// slice-indexed ProfileData fast path (issue #94). The interpreter's back
// edges always have the pid at hand (callInfo.protoID).
func (b *Bridge) OnBackEdgeID(proto *bytecode.Proto, pid uint32, pc int32, onMain bool) {
	b.onBackEdgePD(b.profileOfID(proto, pid), proto, pc, onMain)
}

func (b *Bridge) onBackEdgePD(pd *ProfileData, proto *bytecode.Proto, pc int32, onMain bool) {
	if b.tierOff {
		return // runtime kill switch: no counting, no promotion
	}
	if pd.TierState != TierInterp {
		return // already promoted to Gibbous / stuck in Stuck: no need to keep counting (01 §4.1 guard)
	}
	pd.allocBackEdge(proto)
	if pc < 0 || int(pc) >= len(pd.BackEdge) {
		return // defensive bounds check (should not happen in theory)
	}
	pd.BackEdge[pc]++
	// Re-arm the per-entry recheck dedup at two warmth milestones per pc
	// (issue #40; see the dedup in considerPromotion): the first back
	// edge (the loop body has run once, so its ICs are now observed —
	// the point where IC-gated backends start accepting) and
	// HotBackEdgeThreshold (auto mode's own trigger; the ICs are as warm
	// as they will get). Keeps forceAll promoting everything it used to,
	// at the same points it used to, without paying a full backend
	// re-analysis on every back edge in between.
	if pd.BackEdge[pc] == 1 || pd.BackEdge[pc] == b.hotBackEdge {
		pd.recheckedAtEntry = 0
		// Re-arm the floor-exemption verdict at the same milestones
		// (issue #67): ExemptFromFloor reads IC state (P4's
		// ProtoSeg2SegEligible requires ArrayHit at GETTABLE sites), so
		// a verdict cached while a deep-pc branch's IC was still cold
		// (the binary-trees `check` shape, issue #40) would wrongly
		// pin the proto to floorExemptNo forever. Clearing here grants
		// one warm-IC re-ask per milestone — off the steady-state path.
		if pd.floorExempt == floorExemptNo {
			pd.floorExempt = floorExemptUnasked
		}
	}
	if pd.BackEdge[pc] >= b.hotBackEdge || b.forceAll {
		if !b.forceAll && b.flooredOut(proto, pd) {
			return // short proto: dispatch downside > interpreter gain, skip promotion (issue #21)
		}
		b.considerPromotion(proto, pd, onMain)
	}
}

// OnEnter is the function-entry sampling hook (01 §4.2).
//
// Call contract (called by crescent enterLuaFrame / after TAILCALL reload,
// wired in PB1).
//
// onMain: whether the currently executing thread is the main thread (the
// thread-level tier rule, 07 §2.4). Profile still accumulates on coroutine
// threads (diagnostic value, 07 §2.4 chooses (A)), but crossing the
// threshold does not trigger promotion.
//
// MinPromotableCodeLen guard (issue #21): same as OnBackEdge, a short proto
// accumulates EntryCount and then returns before the considerPromotion call —
// profile diagnostics stay complete, only the promotion action is skipped.
func (b *Bridge) OnEnter(proto *bytecode.Proto, onMain bool) {
	b.onEnterPD(b.profileOf(proto), proto, onMain)
}

// OnEnterID is OnEnter with the caller-supplied ProtoID driving the
// slice-indexed ProfileData fast path (issue #94): frame entry is the
// hottest sampling hook, and the interpreter's enterLuaFrame always has
// the pid at hand.
func (b *Bridge) OnEnterID(proto *bytecode.Proto, pid uint32, onMain bool) {
	b.onEnterPD(b.profileOfID(proto, pid), proto, onMain)
}

func (b *Bridge) onEnterPD(pd *ProfileData, proto *bytecode.Proto, onMain bool) {
	if b.tierOff {
		return // runtime kill switch: no counting, no promotion
	}
	if pd.TierState != TierInterp {
		return
	}
	pd.EntryCount++
	if pd.EntryCount >= b.hotEntry || b.forceAll {
		if !b.forceAll && b.flooredOut(proto, pd) {
			return // short proto: dispatch downside > interpreter gain, skip promotion (issue #21)
		}
		b.considerPromotion(proto, pd, onMain)
	}
}

// flooredOut reports whether the short-proto floor blocks this proto's
// promotion (issue #21), after consulting the backend's optional
// per-proto exemption (issue #67). Called on the auto-mode promotion
// path only, and only past the heat threshold — the exemption verdict
// is computed once there (warm ICs) and cached in pd.floorExempt, so
// steady-state calls stay a length compare plus a byte load.
func (b *Bridge) flooredOut(proto *bytecode.Proto, pd *ProfileData) bool {
	if len(proto.Code) >= b.effectiveMinPromotableLen() {
		return false
	}
	if b.floorExempter == nil {
		return true
	}
	if pd.floorExempt == floorExemptUnasked {
		if b.floorExempter.ExemptFromFloor(proto) {
			pd.floorExempt = floorExemptYes
		} else {
			pd.floorExempt = floorExemptNo
		}
	}
	return pd.floorExempt == floorExemptNo
}

// considerPromotion is the promotion decision entry (04 §3).
//
// Call contract (see [04 §3.1] + [01 §4.3]):
//  1. Idempotent: multiple calls do not error — the function guards itself
//     with pd.TierState != TierInterp;
//  2. Does not reload the frame — the onBackEdge/onEnter caller needs no
//     reloadFrame;
//  3. Not on the hottest path — happens only at the threshold crossing,
//     amortized to tens of ns per back edge;
//  4. No return value — promote/stay/fail are all expressed via pd.TierState.
//
// Processing paths (four, corresponding to 04 §3.2):
//
//	(P1) Already in an absorbing state (TierGibbous / TierStuck) → return directly (debounce)
//	(P2) Compilable != CompCompilable (includes CompUnknown / CompNotCompilable) → go to Stuck
//	(P3) Compilable → try-compile; success → Gibbous install; failure → Stuck.
func (b *Bridge) considerPromotion(proto *bytecode.Proto, pd *ProfileData, onMain bool) {
	// (P1) already in an absorbing state → no-op (double debounce, the
	// onBackEdge/OnEnter guard already caught one pass)
	if pd.TierState != TierInterp {
		return
	}

	// (P0') thread-level tier rule (07 §2.4): even if heat crosses the
	// threshold on a coroutine thread, do not promote — a gibbous promotion
	// is unusable inside a coroutine (the doCall gibbous branch's th==mainTh
	// guard forces crescent), so the compile work is wasted; and a gibbous
	// frame cannot cross a yield (07 §1), so coroutines must stay in crescent.
	// Profile has already accumulated (diagnostics kept, 07 §2.4 chooses (A));
	// this is only the decision gate.
	if !onMain {
		return
	}

	comp := pd.Compilable
	// Re-decide compilability at runtime (issue #18): at compile time
	// analyzeCompilability uses a temporary Bridge with no P3 injected, so
	// every Proto burns the ReasonBackendUnsupp placeholder. When P3 is
	// injected at runtime this bit needs re-deciding, otherwise any Proto —
	// even after reaching HotEntryThreshold — goes straight to Stuck, making
	// the p3 build's natural-heat promotion path a dead letter.
	// recheckCompilabilityRuntime implements "clear the F7 placeholder +
	// re-decide against the real backend + conservatively preserve F1-F6",
	// and is called in two scenarios:
	//   (a) forceAll: the PW9 diff test entry, force-promoting past the heat threshold;
	//   (b) natural heat + placeholder bit: the issue #18 fix, the production
	//       promotion path.
	needsAutoRecheck := b.p3 != nil &&
		ReasonsBitmap(proto.CompReasons)&(ReasonBackendUnsupp|ReasonSelfCall) != 0
	if comp != CompCompilable && (b.forceAll || needsAutoRecheck) {
		// Per-entry dedup (issue #40): while the forceAll retry window
		// below keeps a declined proto on TierInterp, every back edge
		// lands here again. Promotion only takes effect on a later entry
		// (no OSR), so mid-entry re-analysis cannot change the outcome —
		// but it costs a full CFG build + opcode scan per call (measured
		// 22% CPU / 1.5 GB/op on a declined 2M-back-edge kernel). Run the
		// recheck at most once per EntryCount; OnBackEdge re-arms it once
		// per pc at HotBackEdgeThreshold for a final warm-IC look.
		if pd.recheckedAtEntry == pd.EntryCount+1 {
			return // this entry already declined; retry on a later event
		}
		pd.recheckedAtEntry = pd.EntryCount + 1
		comp = b.recheckCompilabilityRuntime(proto)
		if comp == CompCompilable {
			// Sync the pd copy (State-private): later CompilabilityOf /
			// secondary-diagnostic paths reading pd see the real result
			// rather than the compile-time-burned placeholder.
			// proto.Compilability is left untouched — it is shared read-only
			// across States, and P3 instances injected by different States
			// may have different capabilities; this is the boundary between
			// the "written once at compile time" invariant (03 §5.4) and the
			// "State-private mutable at runtime" state.
			pd.Compilable = CompCompilable
			pd.Reasons = 0
		}
	}
	if comp != CompCompilable {
		// forceAll retry window: force mode promotes on the very first
		// entry, when the proto's IC slots are still cold (Kind==None).
		// IC-gated backends (P4 native NodeHit/ArrayHit acceptance)
		// decline cold protos that they would accept after one or two
		// interpreter runs warm the IC. Sticking on the first decline
		// would leave such protos on the interpreter forever, so give
		// the backend some warm-up entries before absorbing to Stuck.
		// The window must cover RECURSIVE protos whose later-pc IC slots
		// only warm after a subtree completes: binary-trees' `check`
		// executes its third GETTABLE (tree[2]) for the first time only
		// after the whole left subtree returned — around entry 14 for a
		// depth-12 tree. A window of 4 stuck it permanently; 64 covers
		// realistic recursion depths and costs at most 64 rechecks per
		// declined proto (force mode only, dedup'd to one per entry).
		if b.forceAll && pd.EntryCount < 64 {
			return // stay TierInterp; retry on a later entry
		}
		// (P2) uncompilable / unanalyzed → permanently interpreted (04 §1.4 static fallback)
		pd.TierState = TierStuck
		pd.CompileTried = true
		b.stuckNotCompilable++
		if b.logger != nil {
			b.logger.LogStuck(proto, pd, comp)
		}
		return
	}

	// (P2') Profitability gate (issue #39): the backend can compile
	// this proto but predicts it runs slower promoted than interpreted
	// (e.g. P3 wasm on helper-dense table kernels — nbody hot protos
	// promoted to 2x slower than the interpreter after 45b8b53 let
	// them pass F2-b). Auto mode only: forceAll keeps promoting every
	// compilable shape so differential coverage doesn't shrink. The
	// judgment is static op-mix density, so absorb to Stuck rather
	// than re-asking every entry.
	if !b.forceAll {
		if g, ok := b.p3.(PromotionGater); ok && !g.WorthPromoting(proto) {
			pd.TierState = TierStuck
			pd.CompileTried = true
			b.stuckDeclined++
			if b.logger != nil {
				b.logger.LogStuck(proto, pd, comp)
			}
			return
		}
	}

	// (P3) try-compile
	// Lock: when multiple States share a Proto, let only one State actually
	// compile (the 04 §4.5 (A) design). profileTable is State-private, but
	// gibbousCodes is Bridge-shared — the lock guards the latter together
	// with the trampoline-registration critical section.
	b.compileMu.Lock()
	defer b.compileMu.Unlock()

	// Double-check gibbousCodes after locking: another State compiled and
	// installed first → reuse the existing GibbousCode, do not recompile
	// (04 §4.5).
	if existing, ok := b.gibbousCodes[proto]; ok {
		_ = existing
		pd.TierState = TierGibbous
		pd.CompileTried = true
		if b.logger != nil {
			b.logger.LogPromoted(proto, pd)
		}
		return
	}

	// Aggregate IC feedback to feed P3 (02 §4.5 one-shot aggregation)
	fb := b.aggregator.Aggregate(proto)
	pd.Feedback = fb
	pd.CompileTried = true

	code, err := b.tryCompile(proto, fb)
	if err != nil {
		// (P3-fail) compilation failed → permanently interpreted (04 §1.4 try-compile fallback)
		pd.TierState = TierStuck
		b.stuckCompileFailed++
		if b.logger != nil {
			b.logger.LogCompileFail(proto, pd, err)
		}
		return
	}

	// (P3-success) promotion succeeded: register the gibbous code + go to TierGibbous
	b.installGibbous(proto, code)
	pd.TierState = TierGibbous
	if b.logger != nil {
		b.logger.LogPromoted(proto, pd)
	}
}

// tryCompile wraps P3.Compile + a defer recover fallback (04 §5.2).
//
// A backend panic does NOT cross this interface — P2 must not let a backend
// bug crash the P1 main loop; it can only fall back that Proto. recover turns
// the panic into a *CompileError (Kind=Panic), and considerPromotion, seeing
// err != nil, takes the fallback path to Stuck.
func (b *Bridge) tryCompile(proto *bytecode.Proto, fb *TypeFeedback) (code GibbousCode, err error) {
	if b.p3 == nil {
		// Reaching here with no P3 injected should not happen (F7 should
		// catch it in the AnalyzeProto phase); defensive fallback.
		return nil, &CompileError{
			Kind:   CompileErrBackendUnsupported(),
			Proto:  proto,
			Reason: "P3 compiler not injected",
		}
	}
	defer func() {
		if r := recover(); r != nil {
			err = &CompileError{
				Kind:   CompileErrBackendPanic,
				Proto:  proto,
				Reason: fmtPanic(r),
			}
			code = nil
			if b.logger != nil {
				b.logger.LogPanic(proto, r)
			}
		}
	}()
	return b.p3.Compile(proto, fb)
}

// installGibbous installs the gibbous code after a successful promotion
// (04 §4.4).
//
// Attached to the gibbousCodes map (GC anti-early-release + trampoline query
// source). The Proto's tierState transition to TierGibbous is written by
// considerPromotion into ProfileData.TierState; crescent doCall queries this
// table via GibbousCodeOf to decide whether to take the gibbous branch (VS0-d).
func (b *Bridge) installGibbous(proto *bytecode.Proto, code GibbousCode) {
	b.gibbousCodes[proto] = code
}

// PromotionCount returns the number of Protos already promoted on the current
// Bridge (testing-only).
//
// Purpose: called when a benchmark / e2e test wants to white-box assert "this
// run really triggered a promotion, rather than degrading to the
// interpreter". Especially important under the auto-lifting form
// (SetForceAllPromote(false)) — if HotEntryThreshold never fires, the numbers
// a p3 build measures are the interpreter-path numbers, nearly indistinguishable
// from p1, and unreadable (see the prove-the-path-under-test guide).
//
// Form: non-decreasing (promotion only grows, never shrinks); when multiple
// States share one Bridge it returns the total; under a single State it is
// enough as a "promoted at least one" criterion. The return value is an upper
// bound on the number of installGibbous calls (in fact just the gibbousCodes
// map size, essentially equivalent but with clearer semantics).
//
// testing-only: on a non-wangshu_p3 build / when P3 is not injected, the
// Bridge is nil, and the public-facing State.PromotionCount returns 0 directly
// (equivalent no-op).
func (b *Bridge) PromotionCount() int {
	return len(b.gibbousCodes)
}

// GibbousCodeOf checks whether a Proto has been promoted and gets its
// GibbousCode (the VS0-d trampoline entry).
//
// crescent doCall calls this query in the Lua closure branch: a non-nil
// return ⇒ the Proto has been promoted to gibbous, take the trampoline to
// wazero execution; nil ⇒ take the interpreter (not promoted / TierStuck).
//
// Reads the gibbousCodes map (only grows after installGibbous, stable at
// runtime; multiple States share the same GibbousCode, 04 §6.4). Hot-path
// query — a map hit is one hash; when profileEnabled is off, crescent does
// not call this function at all (compile-time elimination).
func (b *Bridge) GibbousCodeOf(proto *bytecode.Proto) GibbousCode {
	if b.tierOff || len(b.gibbousCodes) == 0 {
		return nil
	}
	return b.gibbousCodes[proto]
}

// fmtPanic formats the panic value obtained from recover (avoiding a direct
// %v on interface{} that would miss the stack info). Simplified version —
// upgraded when P2 PB5 lands full stack support.
func fmtPanic(r interface{}) string {
	return fmt.Sprintf("%v", r)
}

// CompileErrBackendUnsupported is the internal error code for "P3 not
// injected" (04 §5.2 edge case). Not exposed in the CompileErrKind enum
// constants — a helper function is used to avoid the enum accruing
// unnecessary external semantics (P3 injection is a Bridge-assembly-phase
// matter, not a runtime norm).
func CompileErrBackendUnsupported() CompileErrKind { return CompileErrBackendDeclined }
