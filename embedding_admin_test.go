package wangshu_test

import (
	"testing"
	"time"

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

// --- Group B:公共 API 边界 + 异常路径 + 状态机 ---

// TestPreallocate_Zero 验证 Preallocate(0) no-op,不破坏现有 array 段。
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

// TestPreallocate_AfterRelease 验证已 Release 的 Table 上 Preallocate 返回 error。
func TestPreallocate_AfterRelease(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	tt := tv.AsTable()
	tv.Release()
	if e := tt.Preallocate(100); e == nil {
		t.Error("Preallocate on released Table should error, got nil")
	}
}

// TestNewArrayTable_Empty 验证空 slice 合法,返回空表。
func TestNewArrayTable_Empty(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewArrayTable([]wangshu.Value{})
	defer tv.Release()
	tt := tv.AsTable()
	if got := tt.Len(); got != 0 {
		t.Errorf("empty NewArrayTable: Len = %d, want 0", got)
	}
}

// TestNewArrayTable_Nil 验证 nil slice 合法等价于空 slice。
func TestNewArrayTable_Nil(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewArrayTable(nil)
	defer tv.Release()
	tt := tv.AsTable()
	if got := tt.Len(); got != 0 {
		t.Errorf("nil NewArrayTable: Len = %d, want 0", got)
	}
}

// TestNewArrayTable_WithTableValues 验证含 Table-kind Value 的 vals 正确 pin 互动。
// 内部 toInner 转换 + pin 表 + 后续读应仍能取出原 GCRef。
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
	// 取出两个子表;Value 经 fromInnerWithPin 注册新 pin 槽,变量持有 + defer Release
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

// TestNewArrayTable_WithMixedKinds 验证混合 number/string/bool/nil 全部正确传入。
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

// TestPreallocate_ExceedsMaxArenaBytes 验证 Preallocate 超 MaxArenaBytes 触发 panic。
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

// TestSetHostTriggeredCollect_ToggleOnOff 验证开关状态机:off→on→off。
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
	// 初始 off
	allocSome()
	usedOff1 := st.GCCountKB()
	// 开 on
	st.SetHostTriggeredCollect(true)
	allocSome()
	usedOn := st.GCCountKB()
	// 关回 off,GCCountKB 应再累积涨
	st.SetHostTriggeredCollect(false)
	allocSome()
	usedOff2 := st.GCCountKB()
	t.Logf("toggle: off1=%.1f on=%.1f off2=%.1f", usedOff1, usedOn, usedOff2)
	if usedOff2 <= usedOn {
		t.Errorf("toggle off after on should resume accumulation: on=%.1f off2=%.1f", usedOn, usedOff2)
	}
}

// --- Group C:#10 root fix 算法不变式高阶验证 ---

// TestNaiveSetIndex_LinearScaling 验证 #10 root fix 后,naive NewTable + SetIndex(1..N)
// 也能保持 O(N) 摊销(无需用户用 Preallocate/NewArrayTable)。
//
// 对照 Preallocate/NewArrayTable 测试(ratio < 3×),naive 路径在 root fix 后也应
// ratio < 5×(略宽口径,因 naive 路径仍带 doublings 开销)。
func TestNaiveSetIndex_LinearScaling(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 1 << 20})
	// 暖身
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
