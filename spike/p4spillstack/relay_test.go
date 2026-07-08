//go:build linux && amd64

package p4spillstack

import (
	"runtime/debug"
	"testing"
)

// TestG3_RelayFirstEntrySwitches (gate G3, part 1): the range-checked relay
// trampoline switches SP to the spill base on a normal entry (SP starts on
// the goroutine stack, outside the buffer) and the deep descent is clean.
func TestG3_RelayFirstEntrySwitches(t *testing.T) {
	const levels = 1024
	code := EmitDescendSegment(levels)
	page, err := MmapCode(code)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	spill := NewSpillStack(64 * 1024)
	got := spill.CallRelay(page)
	if got != levels {
		t.Fatalf("survived = %d, want %d (relay first-entry switch failed)", got, levels)
	}
}

// TestG3_RepeatedEntriesNoClobber (gate G3, part 2): the exit-reason resume
// model. Each CallJITSpec returns to Go (SP restored to the goroutine
// stack) before the next one; so repeated relay entries each switch cleanly
// from the goroutine stack and never overwrite a live outer frame. If the
// restore or the range check were wrong, an entry would descend into a
// stale region and canaries would corrupt.
//
// This is the key finding for issue #89: seg2seg deep recursion inside ONE
// segment call shares the spill stack (no re-entry); the exit-reason resume
// / HelperCall-driven callee Run are FRESH CallJITSpec calls that each
// enter from the goroutine stack (the outer trampoline already restored
// SP), so there is no nested-clobber hazard on the resume path.
func TestG3_RepeatedEntriesNoClobber(t *testing.T) {
	const levels = 1024
	code := EmitDescendSegment(levels)
	page, err := MmapCode(code)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	spill := NewSpillStack(64 * 1024)
	// Interleave 500 relay entries; each must independently see all canaries
	// survive. The relay reuses the same spill buffer every time — proving a
	// fresh entry from the goroutine stack always resets to spillBase.
	for i := 0; i < 500; i++ {
		got := spill.CallRelay(page)
		if got != levels {
			t.Fatalf("entry %d: survived = %d, want %d (clobber across entries)", i, got, levels)
		}
	}
}

// TestG3_RelayGCStress (gate G3 + G4): repeated relay entries under GOGC=1.
func TestG3_RelayGCStress(t *testing.T) {
	old := debug.SetGCPercent(1)
	defer debug.SetGCPercent(old)

	const levels = 1024
	code := EmitDescendSegment(levels)
	page, err := MmapCode(code)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	spill := NewSpillStack(64 * 1024)
	for i := 0; i < 200; i++ {
		got := spill.CallRelay(page)
		if got != levels {
			t.Fatalf("iter %d: survived = %d, want %d", i, got, levels)
		}
		churn := make([][]byte, 64)
		for j := range churn {
			churn[j] = make([]byte, 256)
		}
		_ = churn
	}
}

// TestG3_CurrentSPOnGoroutineStack asserts the exit-reason model's premise:
// between segment calls, control is on the goroutine stack (currentSP is
// NOT inside the spill buffer). This is why fresh CallJITSpec entries switch
// cleanly rather than nesting.
func TestG3_CurrentSPOnGoroutineStack(t *testing.T) {
	spill := NewSpillStack(64 * 1024)
	sp := currentSP()
	if spill.InRange(sp) {
		t.Fatalf("Go-side SP 0x%x unexpectedly inside spill buffer [0x%x,0x%x]",
			sp, spill.Lo(), spill.Base())
	}
}
