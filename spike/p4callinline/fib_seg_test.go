//go:build linux && amd64

package p4callinline

import (
	"testing"
)

// fibGo is the reference recursion for correctness.
func fibGo(n uint64) uint64 {
	if n < 2 {
		return n
	}
	return fibGo(n-1) + fibGo(n-2)
}

// TestFibSegment_Recursion validates the self-recursive fib segment
// computes the right value AND survives deep native recursion (fib(24)
// recurses 24 levels of native stack). This is the issue #50 Spike 5
// feasibility probe: segment-to-segment dispatch + native recursion.
func TestFibSegment_Recursion(t *testing.T) {
	seg, err := BuildFibSegment()
	if err != nil {
		t.Fatalf("BuildFibSegment: %v", err)
	}
	defer seg.Munmap() //nolint:errcheck // spike cleanup

	for _, n := range []uint64{0, 1, 2, 3, 5, 8, 13, 20, 24} {
		got := CallSeg(seg.Addr(), uintptr(n), 0, 0)
		want := fibGo(n)
		if got != want {
			t.Fatalf("fib(%d) = %d, want %d", n, got, want)
		}
	}
}

// BenchmarkFibSegment_24 measures the amortized cost of a full fib(24)
// computed entirely segment-to-segment (no host round trip). Compare to
// gopher-lua's fib(24) (~9.4ms) and the exit-reason CALL path (~18.9ms
// per the issue #50 amd64 profile). fib(24) issues ~150k recursive
// calls; the per-call cost is the number that decides whether
// EmitCallInline's segment-to-segment dispatch can beat gopher.
func BenchmarkFibSegment_24(b *testing.B) {
	seg, err := BuildFibSegment()
	if err != nil {
		b.Fatal(err)
	}
	defer seg.Munmap() //nolint:errcheck // spike cleanup

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := CallSeg(seg.Addr(), 24, 0, 0); got != 46368 {
			b.Fatalf("fib(24) = %d, want 46368", got)
		}
	}
}
