package api_test

import (
	"strconv"
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

// TestPreallocate_KeepsIntegerKeysFromTheHashPart: an integer key that was stored before the array
// part covered it is still readable after Preallocate grows the array part over it, and a traversal
// still ends. (The key has to move from the hash part into the array part; left behind it could not
// be looked up, while the traversal kept returning it.)
func TestPreallocate_KeepsIntegerKeysFromTheHashPart(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	defer tv.Release()
	tt := tv.AsTable()
	_ = tt.SetIndex(100, wangshu.Number(7))
	_ = tt.Set(wangshu.String("x"), wangshu.Number(8))
	if e := tt.Preallocate(200); e != nil {
		t.Fatalf("Preallocate: %v", e)
	}
	if got := tt.GetIndex(100).Number(); got != 7 {
		t.Errorf("t[100] = %v after Preallocate(200), want 7", got)
	}
	if got := tt.Get(wangshu.String("x")).Number(); got != 8 {
		t.Errorf("t.x = %v after Preallocate(200), want 8", got)
	}
	n := 0
	if e := tt.ForEach(func(_, _ wangshu.Value) bool { n++; return n < 100 }); e != nil {
		t.Fatalf("ForEach: %v", e)
	}
	if n != 2 {
		t.Errorf("ForEach visited %d entries, want 2", n)
	}
}

// stringKeyed returns a table with n string keys k0..k(n-1), each holding its index.
func stringKeyed(st *wangshu.State, n int) wangshu.Value {
	tv := st.NewTable()
	for i := 0; i < n; i++ {
		_ = tv.AsTable().Set(wangshu.String("k"+strconv.Itoa(i)), wangshu.Number(float64(i)))
	}
	return tv
}

// TestPreallocate_DuringForEach: growing the array part from inside a ForEach callback does not
// disturb the traversal of a table with string keys only. Every entry is visited once, whether the
// callback only grows the table or also clears the entry it was given, as ForEach allows.
func TestPreallocate_DuringForEach(t *testing.T) {
	for _, clear := range []bool{false, true} {
		st := wangshu.NewState(wangshu.Options{})
		tv := stringKeyed(st, 20)
		tt := tv.AsTable()
		seen := map[string]int{}
		n := uint32(16)
		err := tt.ForEach(func(k, _ wangshu.Value) bool {
			seen[k.Str()]++
			if clear {
				_ = tt.Set(k, wangshu.Nil())
			}
			n += 8
			if e := tt.Preallocate(n); e != nil {
				t.Errorf("Preallocate: %v", e)
			}
			return len(seen) < 100
		})
		if err != nil {
			t.Errorf("clearing %v: ForEach: %v", clear, err)
		}
		if len(seen) != 20 {
			t.Errorf("clearing %v: ForEach visited %d distinct keys, want 20", clear, len(seen))
		}
		for k, c := range seen {
			if c != 1 {
				t.Errorf("clearing %v: key %s was visited %d times", clear, k, c)
			}
		}
		left := 0
		_ = tt.ForEach(func(_, _ wangshu.Value) bool { left++; return left < 100 })
		if want := map[bool]int{false: 20, true: 0}[clear]; left != want {
			t.Errorf("clearing %v: %d entries are left, want %d", clear, left, want)
		}
		tv.Release()
	}
}

// TestPreallocate_DuringForEachWithIntegerKeys: when the growth has integer keys to move, a
// traversal in progress still ends, and afterwards the table holds what it held.
func TestPreallocate_DuringForEachWithIntegerKeys(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := stringKeyed(st, 20)
	defer tv.Release()
	tt := tv.AsTable()
	for _, i := range []int{5, 9, 300} {
		_ = tt.SetIndex(i, wangshu.Number(float64(i)))
	}
	calls, grown := 0, false
	err := tt.ForEach(func(_, _ wangshu.Value) bool {
		calls++
		if !grown {
			grown = true
			_ = tt.Preallocate(16)
		}
		return calls < 1000
	})
	if err != nil {
		t.Errorf("ForEach: %v", err)
	}
	if calls >= 1000 {
		t.Errorf("the traversal did not end after the table grew under it")
	}
	n, sum := 0, 0.0
	_ = tt.ForEach(func(_, v wangshu.Value) bool { n++; sum += v.Number(); return n < 1000 })
	if n != 23 || sum != 190+5+9+300 {
		t.Errorf("after the growth the table has %d entries adding up to %v, want 23 and 504", n, sum)
	}
}

// TestPreallocate_FailureLeavesTheTableAlone: a growth the arena has no room for panics before
// anything about the table has changed, so a caller that recovers still has every entry.
func TestPreallocate_FailureLeavesTheTableAlone(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 256 * 1024})
	tv := stringKeyed(st, 20)
	defer tv.Release()
	tt := tv.AsTable()
	_ = tt.SetIndex(5, wangshu.Number(55))
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected a panic from a Preallocate beyond MaxArenaBytes")
			}
		}()
		_ = tt.Preallocate(100_000)
	}()
	if got := tt.GetIndex(5).Number(); got != 55 {
		t.Errorf("t[5] = %v after the failed Preallocate, want 55", got)
	}
	n := 0
	if e := tt.ForEach(func(_, _ wangshu.Value) bool { n++; return n < 1000 }); e != nil {
		t.Errorf("ForEach: %v", e)
	}
	if n != 21 {
		t.Errorf("ForEach visited %d entries after the failed Preallocate, want 21", n)
	}
	// A growth that fits still works afterwards.
	if e := tt.Preallocate(16); e != nil {
		t.Fatalf("Preallocate(16): %v", e)
	}
	if got := tt.GetIndex(5).Number(); got != 55 {
		t.Errorf("t[5] = %v after Preallocate(16), want 55", got)
	}
}

// TestPreallocate_KeepsEveryKindOfKey: growing the array part over a table that already has
// integer keys on both sides of the new size, a string key and a non-integer key leaves every
// entry readable, the length right, and the traversal complete.
func TestPreallocate_KeepsEveryKindOfKey(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	defer tv.Release()
	tt := tv.AsTable()
	for _, i := range []int{1, 2, 3, 5, 9, 10, 11, 40} {
		_ = tt.SetIndex(i, wangshu.Number(float64(i*10)))
	}
	_ = tt.Set(wangshu.String("x"), wangshu.Number(8))
	_ = tt.Set(wangshu.Number(2.5), wangshu.Number(25))
	if e := tt.Preallocate(10); e != nil {
		t.Fatalf("Preallocate: %v", e)
	}
	for _, i := range []int{1, 2, 3, 5, 9, 10, 11, 40} {
		if got := tt.GetIndex(i).Number(); got != float64(i*10) {
			t.Errorf("t[%d] = %v after Preallocate(10), want %d", i, got, i*10)
		}
	}
	if got := tt.Get(wangshu.String("x")).Number(); got != 8 {
		t.Errorf("t.x = %v, want 8", got)
	}
	if got := tt.Get(wangshu.Number(2.5)).Number(); got != 25 {
		t.Errorf("t[2.5] = %v, want 25", got)
	}
	// The array part now ends in a value (t[10]), so the border is looked for past it, in the
	// hash part: t[11] is there and t[12] is not.
	if got := tt.Len(); got != 11 {
		t.Errorf("Len = %d, want 11", got)
	}
	sum, n := 0.0, 0
	if e := tt.ForEach(func(_, v wangshu.Value) bool { n++; sum += v.Number(); return n < 100 }); e != nil {
		t.Fatalf("ForEach: %v", e)
	}
	if n != 10 || sum != 843 {
		t.Errorf("ForEach visited %d entries with values adding up to %v, want 10 and 843", n, sum)
	}
}

// TestPreallocate_ScriptAccessAfterwards: script code that has read and written an integer key of
// the hash part many times reads and writes the same key correctly after a host function grew the
// array part over it. (The accesses are cached by node; the cache has to stop matching once the
// key's value has moved into the array part.)
func TestPreallocate_ScriptAccessAfterwards(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	defer tv.Release()
	tt := tv.AsTable()
	_ = tt.SetIndex(5, wangshu.Number(50))
	_ = tt.Set(wangshu.String("x"), wangshu.Number(8))
	st.SetGlobal("T", tv)
	st.Register("grow", func(*wangshu.State, []wangshu.Value) ([]wangshu.Value, error) {
		return nil, tt.Preallocate(10)
	})
	prog, err := wangshu.Compile([]byte(`
		local t = T
		local function get() return t[5] end
		local function set(v) t[5] = v end
		local a
		for i = 1, 1000 do set(50) a = get() end
		grow()
		local b = get()
		set(77)
		local c = get()
		return a, b, c
	`), "preallocate")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(r) != 3 || r[0].Number() != 50 || r[1].Number() != 50 || r[2].Number() != 77 {
		t.Fatalf("the script returned %v, want 50 50 77", r)
	}
	if got := tt.GetIndex(5).Number(); got != 77 {
		t.Errorf("t[5] = %v after the script, want 77", got)
	}
	seen := map[string]int{}
	n := 0
	if e := tt.ForEach(func(k, _ wangshu.Value) bool {
		if k.IsNumber() {
			seen[strconv.Itoa(int(k.Number()))]++
		} else {
			seen[k.Str()]++
		}
		n++
		return n < 100
	}); e != nil {
		t.Fatalf("ForEach: %v", e)
	}
	if len(seen) != 2 || seen["5"] != 1 || seen["x"] != 1 {
		t.Errorf("ForEach visited %v, want 5 and x once each", seen)
	}
}

// TestPreallocate_DuringForEachOnARemovedIntegerKey: the callback removes the integer key it is
// given and then grows the array part over it. The iteration continues in the array part and goes
// through the hash part again, so entries repeat (see Preallocate); it ends without an error and
// reaches every entry.
func TestPreallocate_DuringForEachOnARemovedIntegerKey(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := stringKeyed(st, 10)
	defer tv.Release()
	tt := tv.AsTable()
	_ = tt.SetIndex(500, wangshu.Number(1))
	_ = tt.SetIndex(5, wangshu.Number(2))
	seen := map[string]int{}
	calls := 0
	if e := tt.ForEach(func(k, _ wangshu.Value) bool {
		calls++
		if k.IsNumber() {
			seen[strconv.Itoa(int(k.Number()))]++
			if k.Number() == 5 {
				_ = tt.SetIndex(5, wangshu.Nil())
				if e := tt.Preallocate(10); e != nil {
					t.Errorf("Preallocate: %v", e)
				}
			}
		} else {
			seen[k.Str()]++
		}
		return calls < 100
	}); e != nil {
		t.Fatalf("ForEach: %v", e)
	}
	t.Logf("ForEach called back %d times for 12 entries", calls)
	if len(seen) != 12 {
		t.Errorf("ForEach reached %d different keys, want 12", len(seen))
	}
	if seen["5"] != 1 {
		t.Errorf("the removed key 5 was given %d times, want 1", seen["5"])
	}
	if calls > 23 {
		t.Errorf("ForEach called back %d times; one pass and one repeat of the hash part are 23 at most", calls)
	}
	if !tt.GetIndex(5).IsNil() || tt.GetIndex(500).Number() != 1 {
		t.Errorf("after the iteration t[5] = %v and t[500] = %v, want nil and 1", tt.GetIndex(5), tt.GetIndex(500))
	}
}
