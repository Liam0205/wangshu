//go:build !wangshu_p3

// Tests specific to the default build (non-wangshu_p3).
//
// Under the P3 build, newStateArena sets InPlaceBacking=true (adopting wazero
// linear memory), and arena.Compact is deliberately a no-op (wazero memory.grow
// only grows, never shrinks) — the "Compact really shrinks cap" semantics that
// this file tests only hold on the default build. For the corresponding P3-build
// behavior, see embedding_admin_p3_test.go TestP3_Compact_NoOpInP3Mode.
package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestCompact_ShrinkAfterTransientPeak verifies issue #11 direction 1: after a
// transient large allocation triggers grow doubling, Release + Collect →
// arena.Compact shrinks cap and reclaims the backing. ArenaCapKB should fall
// back from the grow-doubling high-water mark.
func TestCompact_ShrinkAfterTransientPeak(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 64 * 1024})
	// transient allocation triggers grow up to ~1 MB
	tv := st.NewArrayTable(make([]wangshu.Value, 100000))
	capPeak := st.ArenaCapKB()
	if capPeak < 800 { // should grow to at least a few hundred KB
		t.Fatalf("ArenaCapKB did not grow as expected: %.1f", capPeak)
	}
	tv.Release()
	st.Collect()
	capAfter := st.ArenaCapKB()
	t.Logf("ArenaCapKB: peak=%.1f after Collect=%.1f", capPeak, capAfter)
	if capAfter >= capPeak {
		t.Fatalf("Compact did not shrink ArenaCapKB: peak=%.1f after=%.1f", capPeak, capAfter)
	}
}
