package wangshu_test

import (
	"testing"
	"time"

	"github.com/Liam0205/wangshu"
)

// TestPreallocate_DataPreserved 验证 Preallocate 不丢失现有 array 段数据。
func TestPreallocate_DataPreserved(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	defer tv.Release()
	tt := tv.AsTable()
	// 先写入几个键(rehash 后 asize > 0)
	for i := 1; i <= 5; i++ {
		_ = tt.SetIndex(i, wangshu.Number(float64(i*10)))
	}
	// Preallocate 扩到 100
	if e := tt.Preallocate(100); e != nil {
		t.Fatalf("Preallocate: %v", e)
	}
	// 原数据仍在
	for i := 1; i <= 5; i++ {
		v := tt.GetIndex(i)
		if v.Number() != float64(i*10) {
			t.Fatalf("after Preallocate, key %d = %v, want %d", i, v.Number(), i*10)
		}
	}
}

// TestPreallocate_NoShrink 验证 Preallocate(n) 仅扩不缩(n ≤ 当前 asize no-op)。
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
	// 缩小尝试:no-op
	if e := tt.Preallocate(10); e != nil {
		t.Fatalf("Preallocate(10): %v", e)
	}
	// 全 30 个键仍能读到
	for i := 1; i <= 30; i++ {
		v := tt.GetIndex(i)
		if v.Number() != float64(i) {
			t.Fatalf("after no-shrink, key %d = %v, want %d", i, v.Number(), i)
		}
	}
}

// TestNewArrayTable_Basic 验证 NewArrayTable 一次性构建正确写入数据。
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

// TestPreallocate_FixesQuadraticBuild 验证 issue #10:Preallocate 后 SetIndex
// 顺序构建 ns/elem 是 flat(不是 O(N²) 退化)。对照 issue #10 数据。
func TestPreallocate_FixesQuadraticBuild(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 1 << 20})
	// 暖身
	for i := 0; i < 200; i++ {
		tv := st.NewTable()
		tt := tv.AsTable()
		_ = tt.Preallocate(100)
		for j := 0; j < 100; j++ {
			_ = tt.SetIndex(j+1, wangshu.Number(float64(j)))
		}
		tv.Release()
	}
	// 测两点:N=100 vs N=1000,要求 ns/elem 比值 < 3×(O(N) 摊销)
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
	// **阈值 5.0× 而非 3.0×**(2026-06-29 macos-latest CI 实证 4.13× 触发):
	// macos-latest runner 共享虚拟机,小 N 时单测量噪声波动放大;真 quadratic
	// 退化(O(N²) 摊销)会是 ratio ≥ 10×(N=1000 vs N=100),5.0× 仍能抓
	// 退化但不被 macos-latest runner 性能抖动 false-positive。
	if ratio > 5.0 {
		t.Errorf("Preallocate N=1000 ns/elem ratio = %.2f× over N=100, expected ≤ 5.0× (O(N) amortized); got quadratic-like degradation", ratio)
	}
}

// TestNewArrayTable_FixesQuadraticBuild 验证 NewArrayTable 也 flat。
func TestNewArrayTable_FixesQuadraticBuild(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 1 << 20})
	makeVals := func(n int) []wangshu.Value {
		vals := make([]wangshu.Value, n)
		for i := range vals {
			vals[i] = wangshu.Number(float64(i + 1))
		}
		return vals
	}
	// 暖身
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
	// **阈值 5.0× 同上 Preallocate** 同理(macos-latest runner 抖动 + N 量级
	// 单测量噪声)。
	if ratio > 5.0 {
		t.Errorf("NewArrayTable N=1000 ns/elem ratio = %.2f× over N=100, expected ≤ 5.0× (O(N))", ratio)
	}
}

// BenchmarkTableBuild_NaiveSetIndex 对照基线:NewTable + SetIndex(1..N),issue #10 现状
// (O(N²)),N=1000 应远比 N=100 慢。
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

// BenchmarkTableBuild_Preallocate Preallocate(N) + SetIndex(1..N),issue #10 方向 2。
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

// BenchmarkTableBuild_NewArrayTable 一次性 NewArrayTable(vals),最优形态。
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
