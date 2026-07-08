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
