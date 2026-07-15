package wangshu_test

import (
	"testing"
	"time"

	"github.com/Liam0205/wangshu"
)

// TestPreallocate_DataPreserved verifies that Preallocate does not lose existing array-part data.
func TestPreallocate_DataPreserved(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	defer tv.Release()
	tt := tv.AsTable()
	// Write a few keys first (asize > 0 after rehash)
	for i := 1; i <= 5; i++ {
		_ = tt.SetIndex(i, wangshu.Number(float64(i*10)))
	}
	// Preallocate grows to 100
	if e := tt.Preallocate(100); e != nil {
		t.Fatalf("Preallocate: %v", e)
	}
	// Original data is still present
	for i := 1; i <= 5; i++ {
		v := tt.GetIndex(i)
		if v.Number() != float64(i*10) {
			t.Fatalf("after Preallocate, key %d = %v, want %d", i, v.Number(), i*10)
		}
	}
}

// TestPreallocate_NoShrink verifies that Preallocate(n) only grows, never shrinks (n ≤ current asize is a no-op).
func TestPreallocate_NoShrink(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	defer tv.Release()
	tt := tv.AsTable()
	if e := tt.Preallocate(50); e != nil {
		t.Fatalf("Preallocate(50): %v", e)
	}
	for i := 1; i <= 30; i++ {
		_ = tt.SetIndex(i, wangshu.Number(float64(i)))
	}
	// Shrink attempt: no-op
	if e := tt.Preallocate(10); e != nil {
		t.Fatalf("Preallocate(10): %v", e)
	}
	// All 30 keys are still readable
	for i := 1; i <= 30; i++ {
		v := tt.GetIndex(i)
		if v.Number() != float64(i) {
			t.Fatalf("after no-shrink, key %d = %v, want %d", i, v.Number(), i)
		}
	}
}

// TestNewArrayTable_Basic verifies that NewArrayTable builds a table in one shot with data written correctly.
func TestNewArrayTable_Basic(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	vals := make([]wangshu.Value, 100)
	for i := range vals {
		vals[i] = wangshu.Number(float64(i + 1))
	}
	tv := st.NewArrayTable(vals)
	defer tv.Release()
	tt := tv.AsTable()
	if got := tt.Len(); got != 100 {
		t.Errorf("Len = %d, want 100", got)
	}
	for i := 1; i <= 100; i++ {
		v := tt.GetIndex(i)
		if v.Number() != float64(i) {
			t.Errorf("GetIndex(%d) = %v, want %d", i, v.Number(), i)
		}
	}
}

// TestPreallocate_FixesQuadraticBuild verifies issue #10: after Preallocate, sequential
// SetIndex build keeps ns/elem flat (no O(N²) degradation). Compared against issue #10 data.
func TestPreallocate_FixesQuadraticBuild(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 1 << 20})
	// Warm-up
	for i := 0; i < 200; i++ {
		tv := st.NewTable()
		tt := tv.AsTable()
		_ = tt.Preallocate(100)
		for j := 0; j < 100; j++ {
			_ = tt.SetIndex(j+1, wangshu.Number(float64(j)))
		}
		tv.Release()
	}
	// Measure two points: N=100 vs N=1000, requiring the ns/elem ratio < 3× (O(N) amortized)
	measure := func(n int) float64 {
		const iters = 500
		t0 := time.Now()
		for r := 0; r < iters; r++ {
			tv := st.NewTable()
			tt := tv.AsTable()
			_ = tt.Preallocate(uint32(n))
			for j := 0; j < n; j++ {
				_ = tt.SetIndex(j+1, wangshu.Number(float64(j)))
			}
			tv.Release()
		}
		return float64(time.Since(t0).Nanoseconds()) / float64(iters) / float64(n)
	}
	ns100 := measure(100)
	ns1000 := measure(1000)
	ratio := ns1000 / ns100
	t.Logf("Preallocate ns/elem: N=100 %.1f ns, N=1000 %.1f ns, ratio %.2f×", ns100, ns1000, ratio)
	// **Threshold 5.0× rather than 3.0×** (2026-06-29 macos-latest CI observed 4.13× tripping it):
	// the macos-latest runner is a shared VM, so per-measurement noise is amplified at small N; a real
	// quadratic degradation (O(N²) amortized) would be ratio ≥ 10× (N=1000 vs N=100), and 5.0× still
	// catches degradation without being false-positived by macos-latest runner performance jitter.
	if ratio > 5.0 {
		t.Errorf("Preallocate N=1000 ns/elem ratio = %.2f× over N=100, expected ≤ 5.0× (O(N) amortized); got quadratic-like degradation", ratio)
	}
}

// TestNewArrayTable_FixesQuadraticBuild verifies NewArrayTable is flat too.
func TestNewArrayTable_FixesQuadraticBuild(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 1 << 20})
	makeVals := func(n int) []wangshu.Value {
		vals := make([]wangshu.Value, n)
		for i := range vals {
			vals[i] = wangshu.Number(float64(i + 1))
		}
		return vals
	}
	// Warm-up
	for i := 0; i < 200; i++ {
		tv := st.NewArrayTable(makeVals(100))
		tv.Release()
	}
	measure := func(n int) float64 {
		const iters = 500
		vals := makeVals(n)
		t0 := time.Now()
		for r := 0; r < iters; r++ {
			tv := st.NewArrayTable(vals)
			tv.Release()
		}
		return float64(time.Since(t0).Nanoseconds()) / float64(iters) / float64(n)
	}
	ns100 := measure(100)
	ns1000 := measure(1000)
	ratio := ns1000 / ns100
	t.Logf("NewArrayTable ns/elem: N=100 %.1f ns, N=1000 %.1f ns, ratio %.2f×", ns100, ns1000, ratio)
	// **Threshold 5.0×, same as Preallocate above** for the same reason (macos-latest runner jitter +
	// per-measurement noise at this order of N).
	if ratio > 5.0 {
		t.Errorf("NewArrayTable N=1000 ns/elem ratio = %.2f× over N=100, expected ≤ 5.0× (O(N))", ratio)
	}
}

// BenchmarkTableBuild_NaiveSetIndex baseline for comparison: NewTable + SetIndex(1..N), the current
// state of issue #10 (O(N²)); N=1000 should be far slower than N=100.
func BenchmarkTableBuild_NaiveSetIndex(b *testing.B) {
	for _, n := range []int{50, 100, 200, 400, 800, 1000} {
		b.Run("N="+itoa(n), func(b *testing.B) {
			st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 1 << 20})
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tv := st.NewTable()
				tt := tv.AsTable()
				for j := 0; j < n; j++ {
					_ = tt.SetIndex(j+1, wangshu.Number(float64(j)))
				}
				tv.Release()
			}
		})
	}
}

// BenchmarkTableBuild_Preallocate Preallocate(N) + SetIndex(1..N), issue #10 direction 2.
func BenchmarkTableBuild_Preallocate(b *testing.B) {
	for _, n := range []int{50, 100, 200, 400, 800, 1000} {
		b.Run("N="+itoa(n), func(b *testing.B) {
			st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 1 << 20})
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tv := st.NewTable()
				tt := tv.AsTable()
				_ = tt.Preallocate(uint32(n))
				for j := 0; j < n; j++ {
					_ = tt.SetIndex(j+1, wangshu.Number(float64(j)))
				}
				tv.Release()
			}
		})
	}
}

// BenchmarkTableBuild_NewArrayTable one-shot NewArrayTable(vals), the optimal form.
func BenchmarkTableBuild_NewArrayTable(b *testing.B) {
	for _, n := range []int{50, 100, 200, 400, 800, 1000} {
		b.Run("N="+itoa(n), func(b *testing.B) {
			st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 1 << 20})
			vals := make([]wangshu.Value, n)
			for i := range vals {
				vals[i] = wangshu.Number(float64(i + 1))
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tv := st.NewArrayTable(vals)
				tv.Release()
			}
		})
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
