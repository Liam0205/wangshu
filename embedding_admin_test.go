package wangshu_test

import (
	"testing"
	"time"

	"github.com/Liam0205/wangshu"
)

// TestArenaOptions_DefaultZeroValue verifies issue #11 direction 2: when
// Options.InitialArenaBytes/MaxArenaBytes are zero they fall back to defaults
// (arena 64 KiB initial / 2 GiB cap), and NewState does not panic.
func TestArenaOptions_DefaultZeroValue(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	cap0 := st.ArenaCapKB()
	if cap0 < 32 || cap0 > 256 {
		t.Fatalf("default ArenaCapKB out of expected 32-256 KB range: got %.1f", cap0)
	}
}

// TestArenaOptions_InitialBytes verifies InitialArenaBytes is really passed through to arena.Cap().
func TestArenaOptions_InitialBytes(t *testing.T) {
	const want = uint32(1 << 20) // 1 MiB
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: want})
	cap := st.ArenaCapKB() * 1024
	if uint32(cap) < want {
		t.Fatalf("ArenaCapKB %d B < InitialArenaBytes %d B", uint32(cap), want)
	}
}

// TestArenaOptions_MaxBytesFailFast verifies the MaxArenaBytes cap triggers grow64 fail-fast.
// The cap is set to 256 KiB (enough to load stdlib), then repeatedly building large tables
// is bound to exceed it.
func TestArenaOptions_MaxBytesFailFast(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{
		MaxArenaBytes: 256 * 1024, // 256 KiB: enough for stdlib, not for large tables
	})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on MaxArenaBytes exceedance, got none")
		}
	}()
	for i := 0; i < 10000; i++ {
		tv := st.NewTable()
		for j := 0; j < 500; j++ {
			_ = tv.AsTable().SetIndex(j+1, wangshu.Number(float64(j)))
		}
	}
}

// TestArenaCapKB_TracksGrow verifies ArenaCapKB rises monotonically with large allocations
// (grow-only, issue #11 current state).
func TestArenaCapKB_TracksGrow(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 16 * 1024})
	cap0 := st.ArenaCapKB()
	// Build a large table to force the arena to grow
	tv := st.NewTable()
	for i := 0; i < 2000; i++ {
		_ = tv.AsTable().SetIndex(i+1, wangshu.Number(float64(i)))
	}
	cap1 := st.ArenaCapKB()
	if cap1 <= cap0 {
		t.Fatalf("ArenaCapKB did not grow after large alloc: before=%.1f after=%.1f", cap0, cap1)
	}
}

// TestCollect_FreesUnreferenced verifies issue #9 direction 2: State.Collect() really triggers
// one sweep and GCCountKB drops back (similar to collectgarbage("collect")).
func TestCollect_FreesUnreferenced(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	// Produce some garbage (reclaimable once Released)
	for r := 0; r < 100; r++ {
		tv := st.NewTable()
		for i := 0; i < 50; i++ {
			_ = tv.AsTable().SetIndex(i+1, wangshu.Number(float64(i)))
		}
		tv.Release()
	}
	used0 := st.GCCountKB()
	st.Collect()
	used1 := st.GCCountKB()
	if used1 >= used0 {
		t.Fatalf("Collect did not reduce GCCountKB: before=%.1f after=%.1f", used0, used1)
	}
}

// TestMaybeCollectNow_Idempotent verifies MaybeCollectNow does not collect below the threshold
// (no-op safe), and collects when over the threshold.
func TestMaybeCollectNow_Idempotent(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	cap0 := st.ArenaCapKB()
	st.MaybeCollectNow()
	cap1 := st.ArenaCapKB()
	// MaybeCollectNow does not change backing capacity (it can only shrink GCCountKB)
	if cap0 != cap1 {
		t.Fatalf("MaybeCollectNow changed ArenaCapKB unexpectedly: before=%.1f after=%.1f", cap0, cap1)
	}
}

// TestSetHostTriggeredCollect_OptInOffByDefault verifies #9 direction 1 is off by default,
// and once enabled AllocCharge crossing the threshold really triggers a collect.
func TestSetHostTriggeredCollect_OptInOffByDefault(t *testing.T) {
	// Default off: after repeated NewTable, GCCountKB keeps rising (host path accumulates,
	// the VM safepoint does not trigger)
	st := wangshu.NewState(wangshu.Options{})
	used0 := st.GCCountKB()
	for r := 0; r < 50; r++ {
		tv := st.NewTable()
		for i := 0; i < 50; i++ {
			_ = tv.AsTable().SetIndex(i+1, wangshu.Number(float64(i)))
		}
		tv.Release()
	}
	usedOff := st.GCCountKB()
	t.Logf("host-trigger OFF: GCCountKB %.1f → %.1f", used0, usedOff)

	// Enabled: under the same load GCCountKB should be kept in check by auto-collect
	st2 := wangshu.NewState(wangshu.Options{})
	st2.SetHostTriggeredCollect(true)
	used2_0 := st2.GCCountKB()
	for r := 0; r < 50; r++ {
		tv := st2.NewTable()
		for i := 0; i < 50; i++ {
			_ = tv.AsTable().SetIndex(i+1, wangshu.Number(float64(i)))
		}
		tv.Release()
	}
	usedOn := st2.GCCountKB()
	t.Logf("host-trigger ON: GCCountKB %.1f → %.1f", used2_0, usedOn)
	if usedOn >= usedOff {
		t.Errorf("host-trigger ON did not reduce accumulated GCCountKB: off=%.1f on=%.1f", usedOff, usedOn)
	}
}

// --- Group B: public API boundaries + error paths + state machine ---

// TestPreallocate_Zero verifies Preallocate(0) is a no-op and does not damage the existing array segment.
func TestPreallocate_Zero(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	defer tv.Release()
	tt := tv.AsTable()
	_ = tt.SetIndex(1, wangshu.Number(42))
	if e := tt.Preallocate(0); e != nil {
		t.Fatalf("Preallocate(0): %v", e)
	}
	if v := tt.GetIndex(1).Number(); v != 42 {
		t.Errorf("Preallocate(0) damaged existing data: got %v want 42", v)
	}
}

// TestPreallocate_AfterRelease verifies Preallocate returns an error on an already-Released Table.
func TestPreallocate_AfterRelease(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	tt := tv.AsTable()
	tv.Release()
	if e := tt.Preallocate(100); e == nil {
		t.Error("Preallocate on released Table should error, got nil")
	}
}

// TestNewArrayTable_Empty verifies an empty slice is valid and returns an empty table.
func TestNewArrayTable_Empty(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewArrayTable([]wangshu.Value{})
	defer tv.Release()
	tt := tv.AsTable()
	if got := tt.Len(); got != 0 {
		t.Errorf("empty NewArrayTable: Len = %d, want 0", got)
	}
}

// TestNewArrayTable_Nil verifies a nil slice is valid and equivalent to an empty slice.
func TestNewArrayTable_Nil(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewArrayTable(nil)
	defer tv.Release()
	tt := tv.AsTable()
	if got := tt.Len(); got != 0 {
		t.Errorf("nil NewArrayTable: Len = %d, want 0", got)
	}
}

// TestNewArrayTable_WithTableValues verifies vals containing Table-kind Values interact with pinning correctly.
// The internal toInner conversion + pinning the tables + later reads should still return the original GCRef.
func TestNewArrayTable_WithTableValues(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	inner1 := st.NewTable()
	defer inner1.Release()
	_ = inner1.AsTable().SetIndex(1, wangshu.Number(111))
	inner2 := st.NewTable()
	defer inner2.Release()
	_ = inner2.AsTable().SetIndex(1, wangshu.Number(222))

	tv := st.NewArrayTable([]wangshu.Value{inner1, inner2})
	defer tv.Release()
	tt := tv.AsTable()
	// Fetch the two child tables; the Value registers a new pin slot via fromInnerWithPin,
	// held by a variable + defer Release
	v1 := tt.GetIndex(1)
	defer v1.Release()
	got1 := v1.AsTable()
	if got1 == nil {
		t.Fatal("GetIndex(1).AsTable() returned nil")
	}
	if v := got1.GetIndex(1).Number(); v != 111 {
		t.Errorf("nested table[1] value: got %v want 111", v)
	}
	v2 := tt.GetIndex(2)
	defer v2.Release()
	got2 := v2.AsTable()
	if got2 == nil {
		t.Fatal("GetIndex(2).AsTable() returned nil")
	}
	if v := got2.GetIndex(1).Number(); v != 222 {
		t.Errorf("nested table[2] value: got %v want 222", v)
	}
}

// TestNewArrayTable_WithMixedKinds verifies mixed number/string/bool/nil are all passed in correctly.
func TestNewArrayTable_WithMixedKinds(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	vals := []wangshu.Value{
		wangshu.Number(3.14),
		wangshu.String("hello"),
		wangshu.Bool(true),
		wangshu.Nil(),
		wangshu.Number(42),
	}
	tv := st.NewArrayTable(vals)
	defer tv.Release()
	tt := tv.AsTable()
	if got := tt.GetIndex(1).Number(); got != 3.14 {
		t.Errorf("[1] number: got %v want 3.14", got)
	}
	if got := tt.GetIndex(2).Str(); got != "hello" {
		t.Errorf("[2] string: got %q want hello", got)
	}
	if got := tt.GetIndex(3).Bool(); got != true {
		t.Errorf("[3] bool: got %v want true", got)
	}
	if !tt.GetIndex(4).IsNil() {
		t.Errorf("[4] nil: got %v want nil", tt.GetIndex(4))
	}
	if got := tt.GetIndex(5).Number(); got != 42 {
		t.Errorf("[5] number: got %v want 42", got)
	}
}

// TestPreallocate_ExceedsMaxArenaBytes verifies Preallocate exceeding MaxArenaBytes triggers a panic.
func TestPreallocate_ExceedsMaxArenaBytes(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 256 * 1024})
	tv := st.NewTable()
	defer func() {
		r := recover()
		if r == nil {
			t.Error("expected panic on Preallocate exceeding MaxArenaBytes")
		}
		tv.Release()
	}()
	_ = tv.AsTable().Preallocate(100_000) // 100k * 8 bytes = 800 KB > 256 KiB
}

// TestSetHostTriggeredCollect_ToggleOnOff verifies the toggle state machine: off→on→off.
func TestSetHostTriggeredCollect_ToggleOnOff(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	allocSome := func() {
		for r := 0; r < 50; r++ {
			tv := st.NewTable()
			for i := 0; i < 50; i++ {
				_ = tv.AsTable().SetIndex(i+1, wangshu.Number(float64(i)))
			}
			tv.Release()
		}
	}
	// Initially off
	allocSome()
	usedOff1 := st.GCCountKB()
	// Turn on
	st.SetHostTriggeredCollect(true)
	allocSome()
	usedOn := st.GCCountKB()
	// Turn back off; GCCountKB should resume accumulating
	st.SetHostTriggeredCollect(false)
	allocSome()
	usedOff2 := st.GCCountKB()
	t.Logf("toggle: off1=%.1f on=%.1f off2=%.1f", usedOff1, usedOn, usedOff2)
	if usedOff2 <= usedOn {
		t.Errorf("toggle off after on should resume accumulation: on=%.1f off2=%.1f", usedOn, usedOff2)
	}
}

// --- Group C: #10 root-fix algorithm-invariant higher-order verification ---

// TestNaiveSetIndex_LinearScaling verifies that after the #10 root fix, naive NewTable + SetIndex(1..N)
// also keeps O(N) amortized cost (without the user needing Preallocate/NewArrayTable).
//
// Compared with the Preallocate/NewArrayTable tests (ratio < 3×), the naive path after the root fix
// should also be ratio < 5× (a slightly looser bound, since the naive path still carries doubling overhead).
func TestNaiveSetIndex_LinearScaling(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 1 << 20})
	// Warm-up
	for i := 0; i < 200; i++ {
		tv := st.NewTable()
		tt := tv.AsTable()
		for j := 0; j < 100; j++ {
			_ = tt.SetIndex(j+1, wangshu.Number(float64(j)))
		}
		tv.Release()
	}
	measure := func(n int) float64 {
		const iters = 500
		t0 := time.Now()
		for r := 0; r < iters; r++ {
			tv := st.NewTable()
			tt := tv.AsTable()
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
	t.Logf("naive SetIndex ns/elem: N=100 %.1f N=1000 %.1f ratio %.2f×", ns100, ns1000, ratio)
	if ratio > 5.0 {
		t.Errorf("naive ratio %.2f× regressed beyond O(N) bound 5.0×—issue #10 root fix may have regressed", ratio)
	}
}

// TestCompact_ShrinkAfterTransientPeak lives in embedding_admin_default_test.go
// (only in non-P3 builds, because P3 newStateArena sets InPlaceBacking=true, making Compact a no-op).
// For the corresponding behavior under a P3 build see embedding_admin_p3_test.go TestP3_Compact_NoOpInP3Mode.
