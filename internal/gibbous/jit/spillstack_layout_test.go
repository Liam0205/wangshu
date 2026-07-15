//go:build wangshu_p4 && wangshu_profile

package jit

import (
	"testing"
	"unsafe"
)

// TestSpillStackLayout asserts the JITContext field offsets that the amd64
// and arm64 trampolines hardcode as #define constants (issue #89). Go asm
// cannot read unsafe.Offsetof, so the trampoline bakes the byte offsets of
// spillBase and savedGoSP; if a field is added or reordered ahead of them
// and the offsets drift, the trampoline would switch SP using a wrong
// offset and corrupt the stack silently. This test fails loudly instead.
//
// When these numbers change, update the #define lines in
// internal/gibbous/jit/amd64/trampoline_spec_amd64.s and
// internal/gibbous/jit/arm64/trampoline_arm64.s to match.
func TestSpillStackLayout(t *testing.T) {
	const (
		wantSpillBase = 24
		wantSavedGoSP = 176
	)
	if got := unsafe.Offsetof(JITContext{}.spillBase); got != wantSpillBase {
		t.Errorf("spillBase offset = %d, trampoline #define expects %d — update the .s files", got, wantSpillBase)
	}
	if got := unsafe.Offsetof(JITContext{}.savedGoSP); got != wantSavedGoSP {
		t.Errorf("savedGoSP offset = %d, trampoline #define expects %d — update the .s files", got, wantSavedGoSP)
	}
	// loopSpill0/1/2 contiguity: the PJ3 loopFuel templates address
	// spill1 as spill0Off+8 and spill2 as spill0Off+16 (both amd64 and
	// arm64). Go's adjacent-uint64 field layout with no padding guarantees
	// this, but assert it explicitly so a future struct reorder would fail
	// loudly rather than silently mis-addressing the spill slots.
	s0 := JITContextLoopSpill0Offset
	s1 := JITContextLoopSpill1Offset
	s2 := JITContextLoopSpill2Offset
	if s1 != s0+8 || s2 != s0+16 {
		t.Errorf("loopSpill0/1/2 not contiguous at +8/+16: offsets %d/%d/%d", s0, s1, s2)
	}
}

// TestAllocSpillStack checks the self-managed spill stack is allocated with
// spillBase at the aligned high-address end and spillTop at the low end.
func TestAllocSpillStack(t *testing.T) {
	c := NewJITContext()
	if c.spillBase == 0 || c.spillTop == 0 {
		t.Fatalf("spill stack not allocated: base=%#x top=%#x", c.spillBase, c.spillTop)
	}
	if c.spillBase <= c.spillTop {
		t.Fatalf("spillBase %#x should be above spillTop %#x (down-growing)", c.spillBase, c.spillTop)
	}
	if c.spillBase&0xf != 0 {
		t.Errorf("spillBase %#x not 16-byte aligned", c.spillBase)
	}
	if size := c.spillBase - c.spillTop; size < SpillStackSize-16 || size > SpillStackSize {
		t.Errorf("usable spill size %d outside [%d-16, %d]", size, SpillStackSize, SpillStackSize)
	}
}
