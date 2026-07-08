//go:build linux && amd64

package p4spillstack

import (
	"runtime/debug"
	"testing"
)

// TestG1_SPSwitchWorks (gate G1): a trampoline that switches SP onto the
// spill stack, CALLs a shallow segment, and restores SP returns correctly.
func TestG1_SPSwitchWorks(t *testing.T) {
	const levels = 4
	code := EmitDescendSegment(levels)
	page, err := MmapCode(code)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	spill := NewSpillStack(64 * 1024)
	got := spill.CallOnSpillStack(page)
	if got != levels {
		t.Fatalf("survived canaries = %d, want %d (SP switch or canary corrupted)", got, levels)
	}
}

// TestG2_DeepDescentOnSpillStack (gate G2): a descent deep enough to blow
// the goroutine stack's NOSPLIT allowance runs fine on the spill stack,
// and every canary survives.
//
// cap=128 was the value that crashed under the goroutine stack (PR #86);
// here we go far past it — 1024 levels * 32 B = 32 KiB — inside a 64 KiB
// spill stack. On the goroutine stack this descent would punch through the
// NOSPLIT allowance; on the spill stack it must be clean.
func TestG2_DeepDescentOnSpillStack(t *testing.T) {
	const levels = 1024
	code := EmitDescendSegment(levels)
	page, err := MmapCode(code)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	// 64 KiB spill stack; 1024*32 = 32 KiB descent fits with margin.
	spill := NewSpillStack(64 * 1024)
	got := spill.CallOnSpillStack(page)
	if got != levels {
		t.Fatalf("survived canaries = %d, want %d (deep descent corrupted the spill stack)", got, levels)
	}
}

// TestG2_DepthComparison documents the depth that the fix unlocks: the
// same descent that fits the spill stack (cap far above 128) is exactly
// what PR #86 had to forbid on the goroutine stack.
func TestG2_DepthComparison(t *testing.T) {
	for _, levels := range []uint32{16, 128, 512, 1024, 1800} {
		code := EmitDescendSegment(levels)
		page, err := MmapCode(code)
		if err != nil {
			t.Fatalf("levels=%d MmapCode failed: %v", levels, err)
		}
		// 64 KiB / 32 B = 2048 max levels; 1800 leaves headroom.
		spill := NewSpillStack(64 * 1024)
		got := spill.CallOnSpillStack(page)
		_ = page.Munmap()
		if got != uint64(levels) {
			t.Fatalf("levels=%d: survived = %d, want %d", levels, got, levels)
		}
	}
}

// TestG4_GCStress (gate G4): the spike form of the main-module
// TestI86_DeepRecursionGCStress. Under GOGC=1, repeated deep descents on
// the spill stack must not corrupt neighboring heap objects (no "found
// pointer to free object" fatal). If the SP switch were wrong and the
// descent spilled onto the goroutine stack near the guard, GC would fault.
func TestG4_GCStress(t *testing.T) {
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
	// Allocate churn between runs so the GC has heap to sweep, mirroring
	// the interleaved-State pressure in the main-module regression.
	for i := 0; i < 200; i++ {
		got := spill.CallOnSpillStack(page)
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

// BenchmarkSPSwitch measures the cost of one trampoline entry with the SP
// switch, versus baseline (no switch). The switch is a few register moves;
// it must be cheap enough that raising the depth cap is a net win.
func BenchmarkSPSwitch(b *testing.B) {
	const levels = 16 // shallow: isolate the trampoline cost, not descent
	code := EmitDescendSegment(levels)
	page, err := MmapCode(code)
	if err != nil {
		b.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()
	spill := NewSpillStack(64 * 1024)

	b.Run("switch", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = spill.CallOnSpillStack(page)
		}
	})
	b.Run("baseline", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = CallBaseline(page)
		}
	})
}
