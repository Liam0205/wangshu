// AggregateProfile — a global aggregate table beside each Proto (P2+ #3 (C)
// sync.Pool dual-table hybrid scheme, `docs/design/p2-bridge/01-profiling.md`
// §6.4 design skeleton).
//
// Solves the degradation of scheme (B) under the sync.Pool short-lived State
// form: each request gets a new State + Pool reuse → profileTable is frequently
// Reset and cleared → the hotness signal never accumulates to the threshold, so
// promotion never triggers.
//
// Form:
//   - Main table: State-private profileTable (scheme B; hot paths OnBackEdge /
//     OnEnter write directly, no lock, no atomic);
//   - Side aggregate table (this file): globally shared per Proto,
//     atomic.Uint32 counters, accumulated across States. When State.Reset /
//     Bridge explicitly calls FlushToAggregate, the private table's data is
//     merged into the aggregate table.
//
// Current status: the interface is a preset placeholder + real aggregation can
// be enabled (the real aggregation is implemented in this file, but the
// State.Reset path does not yet call FlushToAggregate automatically — the
// sync.Pool form will be wired up after the wangshu public API lands).
package bridge

import (
	"sync"
	"sync/atomic"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// AggregateProfile is the cross-State aggregate data for a single Proto.
//
// All counters are atomic — no race when multiple States Flush concurrently. The
// aggregate table is the dual of the State-private profileTable fields: the
// private one uses plain uint32 writes (hot fast path), while the global one uses
// atomic.Uint32 (cold aggregate path).
type AggregateProfile struct {
	EntryCount atomic.Uint32 // cross-State cumulative entry count
	// BackEdge is the back-jump count indexed by pc. Lazily allocated: the table
	// is built in one shot on first Flush by len(Proto.Code) (same lazy-alloc
	// semantics as ProfileData.BackEdge, 01 §2.3).
	BackEdge []atomic.Uint32
}

// aggregateRegistry is the global per-Proto aggregate table (package-level var,
// shared across all Bridge instances). sync.Map provides a "read-mostly"
// optimized form — the typical scenario is that the many OnEnter / OnBackEdge
// calls do not write the aggregate table (they only write the State-private
// one); it is only written occasionally on Reset.
//
// Memory: one AggregateProfile entry per Proto, same lifetime as the Proto; when
// a Proto is GC'd the sync.Map entry is not deleted automatically (Go's stdlib
// sync.Map has no finalizer hook) — this is a known minor leak (a few dozen bytes
// per Proto); if measurements show large accumulation, switch to a weak reference
// or a Program destructor hook.
var aggregateRegistry sync.Map // map[*bytecode.Proto]*AggregateProfile

// AggregateOf gets the global aggregate table for a Proto (lazy table build).
//
// Concurrent access from multiple States is OK — sync.Map.LoadOrStore is atomic;
// the first call builds the table, and all States afterward share the same one.
func AggregateOf(proto *bytecode.Proto) *AggregateProfile {
	if v, ok := aggregateRegistry.Load(proto); ok {
		return v.(*AggregateProfile)
	}
	agg := &AggregateProfile{
		BackEdge: make([]atomic.Uint32, len(proto.Code)),
	}
	actual, _ := aggregateRegistry.LoadOrStore(proto, agg)
	return actual.(*AggregateProfile)
}

// FlushToAggregate accumulates the counts from the State-private ProfileData
// into the global aggregate table beside the Proto. Called when: State.Reset is
// about to return to the Pool.
//
// Properties:
//   - Does not clear the State-private ProfileData (the caller decides the Reset
//     policy; 01 §6.4.1 scheme (C) recommends "clear all + accumulate into
//     aggregate table", but this function only handles the latter half).
//   - atomic.AddUint32 guarantees no race when multiple States Flush concurrently.
//   - Does not propagate the state machine (TierState / Compilable): the
//     aggregate table only cares about the "hotness signal"; the state-machine
//     fields are State-private / Proto-shared (the latter written once across
//     States) and do not participate in cross-State accumulation.
func (b *Bridge) FlushToAggregate() {
	for proto, pd := range b.profileTable {
		if pd.EntryCount == 0 && len(pd.BackEdge) == 0 {
			continue // this Proto has no accumulation on this State, skip
		}
		agg := AggregateOf(proto)
		if pd.EntryCount > 0 {
			agg.EntryCount.Add(pd.EntryCount)
		}
		for pc, c := range pd.BackEdge {
			if c > 0 && pc < len(agg.BackEdge) {
				agg.BackEdge[pc].Add(c)
			}
		}
	}
}

// ResetAggregate clears the global aggregate table (for tests — avoids state
// bleeding between tests). Should not be called in production; the aggregate
// table has the same lifetime as the Program and is GC'd after the Program is
// destroyed.
func ResetAggregate() {
	aggregateRegistry = sync.Map{}
}

// considerPromotionWithAggregate is the "dual-table hybrid" version of the
// promotion decision (P2+ #3 (C)): before considerPromotion, it first checks the
// global aggregate table, and if the aggregate's cumulative count has crossed the
// threshold, it triggers a promotion attempt even if the State-private count has
// not.
//
// **Not enabled automatically on the main considerPromotion path** — keeps the
// (B) default form stable; under the sync.Pool form the user can call this
// function explicitly (or the Bridge can add an EnableAggregateMode toggle,
// pending the wangshu public API).
//
//nolint:unused // reserved to be enabled when the wangshu public API is wired up; currently a preset placeholder.
func (b *Bridge) considerPromotionWithAggregate(proto *bytecode.Proto, pd *ProfileData, onMain bool) {
	if pd.TierState != TierInterp {
		return
	}
	// query the global aggregate table
	agg := AggregateOf(proto)
	aggEntry := agg.EntryCount.Load()
	var aggMaxBack uint32
	for i := range agg.BackEdge {
		if c := agg.BackEdge[i].Load(); c > aggMaxBack {
			aggMaxBack = c
		}
	}
	// Trigger if either (local or global) crosses the threshold — this lets the
	// global accumulation drive promotion under the sync.Pool form even when this
	// State's count was just cleared.
	if pd.EntryCount >= b.hotEntry ||
		pd.MaxBackEdge() >= b.hotBackEdge ||
		aggEntry >= b.hotEntry ||
		aggMaxBack >= b.hotBackEdge {
		b.considerPromotion(proto, pd, onMain)
	}
}
