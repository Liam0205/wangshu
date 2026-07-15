//go:build wangshu_p3 && wangshu_profile

// Behavioral equivalence of the public-facing API under the gibbous wasm build +
// force-all promotion runtime verification.
//
// Covers the behavioral correctness of the issue #9/#10/#11 trio under the
// wangshu_p3 build:
//
//	① arena_p3.go newStateArena passes Options.InitialArenaBytes/MaxArenaBytes
//	   through to the wazero memadapter (mirroring the default-build
//	   embedding_admin_test.go assertions)
//	② Compact() is a no-op under the wangshu_p3 build because InPlaceBacking=true
//	   (wazero linear memory cannot shrink)
//	③ Preallocate/NewArrayTable/Collect/MaybeCollectNow still work under the
//	   gibbous build
//	④ Under ForceAllPromote, with gibbous actually promoted at runtime, the
//	   public-facing API still works
package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestP3_ArenaOptions_InitialBytesTransitsToMemAdapter verifies that under the
// P3 build InitialArenaBytes is actually passed to the wazero memadapter
// (arena_p3.go lines 33-44 path).
func TestP3_ArenaOptions_InitialBytesTransitsToMemAdapter(t *testing.T) {
	const want = uint32(2 << 20) // 2 MiB
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: want})
	got := uint32(st.ArenaCapKB() * 1024)
	if got < want {
		t.Errorf("InitialArenaBytes=%d not transited: ArenaCapKB=%d bytes", want, got)
	}
}

// TestP3_ArenaOptions_MaxBytesTransitsToMemAdapter verifies that the
// MaxArenaBytes cap also takes effect under the P3 build — when MaxArenaBytes is
// exceeded, wazero memory.grow / arena.grow64 fail-fast.
func TestP3_ArenaOptions_MaxBytesTransitsToMemAdapter(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 256 * 1024})
	defer func() {
		if r := recover(); r == nil {
			t.Error("P3 build expected panic on MaxArenaBytes exceedance, got none")
		}
	}()
	for i := 0; i < 10000; i++ {
		tv := st.NewTable()
		for j := 0; j < 500; j++ {
			_ = tv.AsTable().SetIndex(j+1, wangshu.Number(float64(j)))
		}
	}
}

// TestP3_Compact_NoOpInP3Mode verifies that Compact is a no-op under the
// wangshu_p3 build (newStateArena sets InPlaceBacking=true, wazero linear memory
// cannot shrink).
func TestP3_Compact_NoOpInP3Mode(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 64 * 1024})
	// transient triggers grow
	tv := st.NewArrayTable(make([]wangshu.Value, 100000))
	capPeak := st.ArenaCapKB()
	tv.Release()
	st.Collect()
	capAfter := st.ArenaCapKB()
	// Under the P3 build Compact is a no-op → cap should not shrink (InPlaceBacking guard)
	if capAfter != capPeak {
		t.Errorf("P3 build Compact should be no-op (InPlaceBacking) but cap changed: peak=%.1f after=%.1f",
			capPeak, capAfter)
	}
}

// TestP3_Preallocate_Works verifies that Preallocate behaves correctly under the
// gibbous build.
func TestP3_Preallocate_Works(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	defer tv.Release()
	tt := tv.AsTable()
	if e := tt.Preallocate(1000); e != nil {
		t.Fatalf("P3 Preallocate: %v", e)
	}
	for i := 1; i <= 1000; i++ {
		if e := tt.SetIndex(i, wangshu.Number(float64(i))); e != nil {
			t.Fatalf("P3 SetIndex(%d): %v", i, e)
		}
	}
	for i := 1; i <= 1000; i++ {
		if v := tt.GetIndex(i).Number(); v != float64(i) {
			t.Errorf("P3 Preallocate+SetIndex[%d] = %v, want %d", i, v, i)
		}
	}
}

// TestP3_NewArrayTable_Works verifies that NewArrayTable is correct under the
// gibbous build.
func TestP3_NewArrayTable_Works(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	vals := make([]wangshu.Value, 500)
	for i := range vals {
		vals[i] = wangshu.Number(float64(i + 1))
	}
	tv := st.NewArrayTable(vals)
	defer tv.Release()
	tt := tv.AsTable()
	if got := tt.Len(); got != 500 {
		t.Errorf("P3 NewArrayTable: Len = %d, want 500", got)
	}
	for i := 1; i <= 500; i++ {
		if v := tt.GetIndex(i).Number(); v != float64(i) {
			t.Errorf("P3 NewArrayTable[%d] = %v, want %d", i, v, i)
		}
	}
}

// TestP3_Collect_Works verifies that an explicit Collect correctly triggers a
// sweep under the gibbous build.
func TestP3_Collect_Works(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
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
		t.Errorf("P3 Collect did not reduce GCCountKB: before=%.1f after=%.1f", used0, used1)
	}
}

// TestP3_ArenaCapKB_Works verifies that ArenaCapKB rises monotonically with grow
// under the gibbous build (wazero memory.grow also expands the backing, which is
// reflected in arena.Cap).
func TestP3_ArenaCapKB_Works(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 64 * 1024})
	cap0 := st.ArenaCapKB()
	// large allocation triggers grow
	tv := st.NewArrayTable(make([]wangshu.Value, 50000))
	defer tv.Release()
	cap1 := st.ArenaCapKB()
	if cap1 <= cap0 {
		t.Errorf("P3 ArenaCapKB did not grow with large alloc: before=%.1f after=%.1f", cap0, cap1)
	}
}

// TestP3_ForceAllPromote_PublicAPI verifies that with ForceAll gibbous promoted
// at runtime, the public-facing API
// (Preallocate/NewArrayTable/Collect/SetGlobal/GetGlobal/Call) still works
// correctly. force-all makes all promotable protos run wasm, which is the most
// common form for embedding users after enabling P3.
func TestP3_ForceAllPromote_PublicAPI(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)

	// build vals
	vals := make([]wangshu.Value, 100)
	for i := range vals {
		vals[i] = wangshu.Number(float64(i + 1))
	}
	xs := st.NewArrayTable(vals)
	defer xs.Release()
	st.SetGlobal("xs", xs)

	// run a simple inner function (will be promoted into gibbous by force-all)
	prog, err := wangshu.Compile([]byte(`
		local function sum(t)
			local s = 0
			for i = 1, 100 do s = s + t[i] end
			return s
		end
		return sum(xs)
	`), "p3-test")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 1 || results[0].Number() != 5050.0 {
		t.Errorf("forceAll sum(1..100) = %v, want 5050.0", results)
	}

	// after force-all + gibbous promotion, the public-facing GC API still works
	st.Collect() // passes if it does not panic
	cap := st.ArenaCapKB()
	if cap <= 0 {
		t.Errorf("ArenaCapKB invalid after force-all + Collect: %.1f", cap)
	}
}

// TestP3_LargeFreelist_FragmentationAvoidance_Lifelike end-to-end verifies that
// the LARGE multi-bucket also eliminates the O(N²) degradation of #10 under the
// gibbous build (arena multi-bucket is compatible with wazero linear-memory
// adoption).
func TestP3_LargeFreelist_FragmentationAvoidance_Lifelike(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 1 << 20})
	// warmup + multiple rounds of naive SetIndex (if LARGE single-chain regresses, this blows up to O(N²))
	for i := 0; i < 100; i++ {
		tv := st.NewTable()
		tt := tv.AsTable()
		for j := 0; j < 500; j++ {
			_ = tt.SetIndex(j+1, wangshu.Number(float64(j)))
		}
		tv.Release()
	}
	// no expectation of an absolute ns, only: no OOM / no hang / all succeed
	tv := st.NewTable()
	defer tv.Release()
	tt := tv.AsTable()
	for j := 0; j < 1000; j++ {
		if e := tt.SetIndex(j+1, wangshu.Number(float64(j))); e != nil {
			t.Fatalf("SetIndex(%d): %v", j+1, e)
		}
	}
	if got := tt.Len(); got != 1000 {
		t.Errorf("after naive SetIndex(1..1000): Len = %d, want 1000", got)
	}
}
