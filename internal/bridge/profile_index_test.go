package bridge

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// TestProfileIndex_IDAndMapPathsShareProfileData asserts the ProtoID-
// indexed fast path (issue #94) and the map path resolve to the SAME
// ProfileData for the same proto — counters accumulated through either
// entry point must land on one record, or heat would split and the
// promotion decision would drift.
func TestProfileIndex_IDAndMapPathsShareProfileData(t *testing.T) {
	b := NewBridge()
	p := &bytecode.Proto{Code: make([]bytecode.Instruction, MinPromotableCodeLen)}
	b.GrowProfileIndex(4)

	// Interleave map-path and ID-path entries; both must hit one record.
	b.OnEnter(p, true)
	b.OnEnterID(p, 2, true)
	b.OnEnter(p, true)
	b.OnEnterID(p, 2, true)

	pd := b.ProfileOf(p)
	if pd.EntryCount != 4 {
		t.Fatalf("EntryCount = %d, want 4 (ID and map paths split the record)", pd.EntryCount)
	}
}

// TestProfileIndex_OutOfRangePIDFallsBack: a pid beyond the grown index
// (or with no grow at all) must fall back to the map path, not panic —
// mock drivers and tests call OnEnter without a real LoadProgram.
func TestProfileIndex_OutOfRangePIDFallsBack(t *testing.T) {
	b := NewBridge()
	p := &bytecode.Proto{Code: make([]bytecode.Instruction, MinPromotableCodeLen)}

	// No GrowProfileIndex at all: every pid is out of range.
	b.OnEnterID(p, 7, true)
	if pd := b.ProfileOf(p); pd.EntryCount != 1 {
		t.Fatalf("EntryCount = %d, want 1", pd.EntryCount)
	}

	// Grow smaller than the pid: still out of range, still works.
	b.GrowProfileIndex(3)
	b.OnEnterID(p, 7, true)
	if pd := b.ProfileOf(p); pd.EntryCount != 2 {
		t.Fatalf("EntryCount = %d, want 2", pd.EntryCount)
	}
}

// TestGrowProfileIndex_NeverShrinks: growing to a smaller n keeps the
// existing slots (multiple LoadProgram calls only ever extend).
func TestGrowProfileIndex_NeverShrinks(t *testing.T) {
	b := NewBridge()
	p := &bytecode.Proto{Code: make([]bytecode.Instruction, MinPromotableCodeLen)}
	b.GrowProfileIndex(8)
	b.OnEnterID(p, 5, true) // caches pd at slot 5
	b.GrowProfileIndex(2)   // must be a no-op
	if len(b.pdByID) != 8 {
		t.Fatalf("index length = %d, want 8 (shrank)", len(b.pdByID))
	}
	b.OnEnterID(p, 5, true)
	if pd := b.ProfileOf(p); pd.EntryCount != 2 {
		t.Fatalf("EntryCount = %d, want 2 (cached slot lost)", pd.EntryCount)
	}
}

// TestProfileIndex_BackEdgeIDSharesRecord mirrors the OnEnter test for
// the back-edge hook.
func TestProfileIndex_BackEdgeIDSharesRecord(t *testing.T) {
	b := NewBridge()
	p := &bytecode.Proto{Code: make([]bytecode.Instruction, MinPromotableCodeLen)}
	b.GrowProfileIndex(1)

	b.OnBackEdge(p, 0, true)
	b.OnBackEdgeID(p, 0, 0, true)

	pd := b.ProfileOf(p)
	if len(pd.BackEdge) == 0 || pd.BackEdge[0] != 2 {
		t.Fatalf("BackEdge[0] = %v, want 2 (paths split the record)", pd.BackEdge)
	}
}
