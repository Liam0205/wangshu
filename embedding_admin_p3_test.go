//go:build wangshu_p3 && wangshu_profile

// 凸月(gibbous wasm)build 下的公共面 API 行为等价 + force-all 升层运行期验证。
//
// 覆盖 issue #9/#10/#11 三件套在 wangshu_p3 build 的行为正确性:
//
//	① arena_p3.go newStateArena 透传 Options.InitialArenaBytes/MaxArenaBytes 到
//	   wazero memadapter(对照默认 build embedding_admin_test.go 同款断言)
//	② Compact() 在 wangshu_p3 build 下因 InPlaceBacking=true no-op(wazero linear
//	   memory 不可缩)
//	③ Preallocate/NewArrayTable/Collect/MaybeCollectNow 在凸月 build 下仍正确
//	④ ForceAllPromote 下凸月真升层执行期,公共面 API 仍工作
package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestP3_ArenaOptions_InitialBytesTransitsToMemAdapter 验证 P3 build 下
// InitialArenaBytes 真传给 wazero memadapter(arena_p3.go 行 33-44 路径)。
func TestP3_ArenaOptions_InitialBytesTransitsToMemAdapter(t *testing.T) {
	const want = uint32(2 << 20) // 2 MiB
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: want})
	got := uint32(st.ArenaCapKB() * 1024)
	if got < want {
		t.Errorf("InitialArenaBytes=%d not transited: ArenaCapKB=%d bytes", want, got)
	}
}

// TestP3_ArenaOptions_MaxBytesTransitsToMemAdapter 验证 MaxArenaBytes 上限在 P3
// build 下也生效——超 MaxArenaBytes 时 wazero memory.grow / arena.grow64 fail-fast。
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

// TestP3_Compact_NoOpInP3Mode 验证 wangshu_p3 build 下 Compact no-op
// (newStateArena 设置 InPlaceBacking=true,wazero linear memory 不可缩)。
func TestP3_Compact_NoOpInP3Mode(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 64 * 1024})
	// transient 触发 grow
	tv := st.NewArrayTable(make([]wangshu.Value, 100000))
	capPeak := st.ArenaCapKB()
	tv.Release()
	st.Collect()
	capAfter := st.ArenaCapKB()
	// P3 build 下 Compact 是 no-op → cap 不应缩(InPlaceBacking 守卫)
	if capAfter != capPeak {
		t.Errorf("P3 build Compact should be no-op (InPlaceBacking) but cap changed: peak=%.1f after=%.1f",
			capPeak, capAfter)
	}
}

// TestP3_Preallocate_Works 验证 Preallocate 在凸月 build 下行为正确。
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

// TestP3_NewArrayTable_Works 验证 NewArrayTable 在凸月 build 下正确。
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

// TestP3_Collect_Works 验证显式 Collect 在凸月 build 下正确触发 sweep。
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

// TestP3_ArenaCapKB_Works 验证 ArenaCapKB 在凸月 build 下随 grow 单调上涨
// (wazero memory.grow 也会扩 backing,反映到 arena.Cap 上)。
func TestP3_ArenaCapKB_Works(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 64 * 1024})
	cap0 := st.ArenaCapKB()
	// 大分配触发 grow
	tv := st.NewArrayTable(make([]wangshu.Value, 50000))
	defer tv.Release()
	cap1 := st.ArenaCapKB()
	if cap1 <= cap0 {
		t.Errorf("P3 ArenaCapKB did not grow with large alloc: before=%.1f after=%.1f", cap0, cap1)
	}
}

// TestP3_ForceAllPromote_PublicAPI 验证 ForceAll 凸月真升层执行期,公共面 API
// (Preallocate/NewArrayTable/Collect/SetGlobal/GetGlobal/Call)仍正确工作。
// force-all 让所有可升 proto 跑 wasm,这是嵌入用户开 P3 后最常用形态。
func TestP3_ForceAllPromote_PublicAPI(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)

	// 构造 vals
	vals := make([]wangshu.Value, 100)
	for i := range vals {
		vals[i] = wangshu.Number(float64(i + 1))
	}
	xs := st.NewArrayTable(vals)
	defer xs.Release()
	st.SetGlobal("xs", xs)

	// 跑一个简单内层函数(会被 force-all 升层进凸月)
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

	// 在 force-all + 凸月已升层后,公共面 GC API 仍工作
	st.Collect() // 不 panic 即过
	cap := st.ArenaCapKB()
	if cap <= 0 {
		t.Errorf("ArenaCapKB invalid after force-all + Collect: %.1f", cap)
	}
}

// TestP3_LargeFreelist_FragmentationAvoidance_Lifelike 端到端验证 LARGE multi-bucket
// 在凸月 build 下也消除 #10 的 O(N²) 退化(arena multi-bucket 与 wazero linear
// memory 收养兼容)。
func TestP3_LargeFreelist_FragmentationAvoidance_Lifelike(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{InitialArenaBytes: 1 << 20})
	// 暖身 + naive SetIndex 多轮(若 LARGE 单链回归,会爆 O(N²))
	for i := 0; i < 100; i++ {
		tv := st.NewTable()
		tt := tv.AsTable()
		for j := 0; j < 500; j++ {
			_ = tt.SetIndex(j+1, wangshu.Number(float64(j)))
		}
		tv.Release()
	}
	// 不期望某个绝对 ns,只期望 不 OOM / 不 hang / 全部成功
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
