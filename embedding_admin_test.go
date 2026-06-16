package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestArenaOptions_DefaultZeroValue 验证 issue #11 方向 2:Options.InitialArenaBytes/
// MaxArenaBytes 零值时退默认(arena 64 KiB 初始 / 2 GiB 上限),NewState 不 panic。
func TestArenaOptions_DefaultZeroValue(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	cap0 := st.ArenaCapKB()
	if cap0 < 32 || cap0 > 256 {
		t.Fatalf("default ArenaCapKB out of expected 32-256 KB range: got %.1f", cap0)
	}
}

// TestArenaOptions_InitialBytes 验证 InitialArenaBytes 真传到 arena.Cap()。
func TestArenaOptions_InitialBytes(t *testing.T) {
	const want = uint32(1 << 20) // 1 MiB
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: want})
	cap := st.ArenaCapKB() * 1024
	if uint32(cap) < want {
		t.Fatalf("ArenaCapKB %d B < InitialArenaBytes %d B", uint32(cap), want)
	}
}

// TestArenaOptions_MaxBytesFailFast 验证 MaxArenaBytes 上限触发 grow64 fail-fast。
// 上限设 256 KiB(足够 stdlib 装载),然后反复构造大表必然超限。
func TestArenaOptions_MaxBytesFailFast(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{
		MaxArenaBytes: 256 * 1024, // 256 KiB:够 stdlib,不够大表
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

// TestArenaCapKB_TracksGrow 验证 ArenaCapKB 随大分配单调上涨(grow-only,
// issue #11 现状)。
func TestArenaCapKB_TracksGrow(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 16 * 1024})
	cap0 := st.ArenaCapKB()
	// 构造一个大表,逼迫 arena grow
	tv := st.NewTable()
	for i := 0; i < 2000; i++ {
		_ = tv.AsTable().SetIndex(i+1, wangshu.Number(float64(i)))
	}
	cap1 := st.ArenaCapKB()
	if cap1 <= cap0 {
		t.Fatalf("ArenaCapKB did not grow after large alloc: before=%.1f after=%.1f", cap0, cap1)
	}
}

// TestCollect_FreesUnreferenced 验证 issue #9 方向 2:State.Collect() 真触发
// 一次 sweep,GCCountKB 落回(类似 collectgarbage("collect"))。
func TestCollect_FreesUnreferenced(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	// 制造一些垃圾(Release 后即可回收)
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

// TestMaybeCollectNow_Idempotent 验证 MaybeCollectNow 不超阈不 collect(no-op
// 安全),超阈时 collect。
func TestMaybeCollectNow_Idempotent(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	cap0 := st.ArenaCapKB()
	st.MaybeCollectNow()
	cap1 := st.ArenaCapKB()
	// MaybeCollectNow 不改 backing 容量(只可能缩 GCCountKB)
	if cap0 != cap1 {
		t.Fatalf("MaybeCollectNow changed ArenaCapKB unexpectedly: before=%.1f after=%.1f", cap0, cap1)
	}
}

// TestSetHostTriggeredCollect_OptInOffByDefault 验证 #9 方向 1 默认 off,
// 开启后 AllocCharge 跨阈真触发 collect。
func TestSetHostTriggeredCollect_OptInOffByDefault(t *testing.T) {
	// 默认 off:NewTable 反复后 GCCountKB 持续涨(host 路径累积,VM safepoint 不触发)
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

	// 开启:同样负载下 GCCountKB 应被 auto-collect 控制
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

// TestCompact_ShrinkAfterTransientPeak 验证 issue #11 方向 1:transient 大分配
// 触发 grow doubling 后,Release + Collect → arena.Compact 缩 cap,backing
// 回收。ArenaCapKB 应从 grow doubling 高水位降回。
func TestCompact_ShrinkAfterTransientPeak(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 64 * 1024})
	// transient 触发 grow 涨到 ~1 MB
	tv := st.NewArrayTable(make([]wangshu.Value, 100000))
	capPeak := st.ArenaCapKB()
	if capPeak < 800 { // 至少涨到几百 KB
		t.Fatalf("ArenaCapKB did not grow as expected: %.1f", capPeak)
	}
	tv.Release()
	st.Collect()
	capAfter := st.ArenaCapKB()
	t.Logf("ArenaCapKB: peak=%.1f after Collect=%.1f", capPeak, capAfter)
	if capAfter >= capPeak {
		t.Fatalf("Compact did not shrink ArenaCapKB: peak=%.1f after=%.1f", capPeak, capAfter)
	}
}
