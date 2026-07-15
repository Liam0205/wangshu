// AggregateProfile unit tests (P2+ #3 (C) sync.Pool dual-table hybrid scheme).
package bridge

import (
	"sync"
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// TestAggregate_FlushAccumulates runs a single State, then calls
// FlushToAggregate to accumulate the private counters into the global
// aggregate table.
func TestAggregate_FlushAccumulates(t *testing.T) {
	defer ResetAggregate() // prevent this test from polluting the global table

	b := NewBridge()
	p := makeProtoWithCode(bytecode.ADD, bytecode.JMP)

	// Simulate back-edge/entry accumulation
	pd := b.ProfileOf(p)
	pd.EntryCount = 50
	pd.BackEdge = []uint32{100, 200}

	b.FlushToAggregate()

	agg := AggregateOf(p)
	if got := agg.EntryCount.Load(); got != 50 {
		t.Errorf("agg EntryCount = %d, want 50", got)
	}
	if got := agg.BackEdge[0].Load(); got != 100 {
		t.Errorf("agg BackEdge[0] = %d, want 100", got)
	}
	if got := agg.BackEdge[1].Load(); got != 200 {
		t.Errorf("agg BackEdge[1] = %d, want 200", got)
	}
}

// TestAggregate_MultiStateAccumulation flushes the same Proto concurrently from
// multiple States — the global aggregate table should accumulate correctly.
// This is the essence of scheme (C): cross-State accumulation lets promotion
// trigger even under a sync.Pool setup.
func TestAggregate_MultiStateAccumulation(t *testing.T) {
	defer ResetAggregate()

	p := makeProtoWithCode(bytecode.ADD)
	const nStates = 8
	const perStateEntry = 25

	var wg sync.WaitGroup
	wg.Add(nStates)
	for i := 0; i < nStates; i++ {
		go func() {
			defer wg.Done()
			b := NewBridge()
			pd := b.ProfileOf(p)
			pd.EntryCount = perStateEntry
			b.FlushToAggregate()
		}()
	}
	wg.Wait()

	agg := AggregateOf(p)
	want := uint32(nStates * perStateEntry)
	if got := agg.EntryCount.Load(); got != want {
		t.Errorf("multi-State agg EntryCount = %d, want %d", got, want)
	}
}

// TestAggregate_LoadOrStoreIdempotent verifies AggregateOf returns the same
// instance for the same Proto across repeated calls (lazy creation +
// sync.Map LoadOrStore atomicity).
func TestAggregate_LoadOrStoreIdempotent(t *testing.T) {
	defer ResetAggregate()

	p := makeProtoWithCode(bytecode.ADD)
	a1 := AggregateOf(p)
	a2 := AggregateOf(p)
	if a1 != a2 {
		t.Errorf("AggregateOf must return same instance for same Proto")
	}
}

// TestAggregate_ConsiderPromotionWithAggregate exercises the (C) mode entry:
// even when this State's EntryCount is far below the threshold,
// considerPromotionWithAggregate still triggers promotion once the global
// aggregate table has crossed the threshold. This simulates the short-lived
// sync.Pool State setup — this State has just been Reset and ran only a little,
// but the global table has already accumulated a lot.
func TestAggregate_ConsiderPromotionWithAggregate(t *testing.T) {
	defer ResetAggregate()

	b := NewBridge()
	b.SetP3Compiler(dummyCompileP3{})
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	// Simulate the global table having already crossed the threshold (left
	// behind by earlier Flushes from other States)
	agg := AggregateOf(p)
	agg.EntryCount.Store(HotEntryThreshold + 1)

	// This State ran only 5 times (far below the 200 threshold)
	pd.EntryCount = 5

	b.considerPromotionWithAggregate(p, pd, true)

	if pd.TierState != TierGibbous {
		t.Errorf("aggregate-driven promotion failed: TierState = %v, want TierGibbous", pd.TierState)
	}
}
