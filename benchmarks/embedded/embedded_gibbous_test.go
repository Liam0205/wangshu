//go:build wangshu_p3 && wangshu_profile

// 凸月(gibbous)档:embedded 边界基准的 evaluate() 经 force-all 升 wazero 执行,
// 与新月(crescent,WangshuCallInto)+ gopher 三方对比。
//
// 测的是「宿主每 item 跨边界 + 调一次脚本函数」路径:边界拷贝成本(CallInto 零
// 分配)与升层正交,凸月差异只体现在 evaluate() 函数体内部的 VM 执行。evaluate
// 是非 vararg 内层函数(host→Lua 调),force-all + 预热后升 gibbous,凸月路径真走到。
//
// 运行:go test -tags "wangshu_p3 wangshu_profile" -bench 'Gibbous' ./benchmarks/embedded/

package embedded

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// warmEvaluate:force-all + 预热升层后,经 CallInto 反复调 evaluate(零分配边界)。
// preset 在预热调用前设好脚本依赖的 globals(避免读 nil)。
func warmEvaluate(b *testing.B, st *wangshu.State, preset func()) wangshu.Value {
	b.Helper()
	st.SetForceAllPromote(true)
	fn := st.GetGlobal("evaluate")
	if preset != nil {
		preset()
	}
	// 预热:首调驱动 evaluate 升 gibbous(首调 crescent)。
	var dst [1]wangshu.Value
	if _, err := st.CallInto(dst[:], fn); err != nil {
		b.Fatalf("warmup: %v", err)
	}
	return fn
}

func BenchmarkMiniCallOnly_Gibbous(b *testing.B) {
	prog, _ := wangshu.Compile([]byte(constPredicateScript), "bench")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	fn := warmEvaluate(b, st, nil) // const predicate 不读 globals
	defer fn.Release()
	var dst [1]wangshu.Value
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := st.CallInto(dst[:], fn); err != nil {
			b.Fatal(err)
		}
		_ = dst[0].Bool()
	}
}

func BenchmarkRealworldPredicate_Gibbous(b *testing.B) {
	items := makeItems()
	prog, _ := wangshu.Compile([]byte(predicateScript), "pred")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	fn := warmEvaluate(b, st, func() {
		it := items[0]
		st.SetGlobal("user_id", wangshu.String(it.userID))
		st.SetGlobal("age", wangshu.Number(it.age))
		st.SetGlobal("is_active", wangshu.Bool(it.isActive))
		st.SetGlobal("score", wangshu.Number(it.score))
	})
	defer fn.Release()
	var dst [1]wangshu.Value
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, it := range items {
			st.SetGlobal("user_id", wangshu.String(it.userID))
			st.SetGlobal("age", wangshu.Number(it.age))
			st.SetGlobal("is_active", wangshu.Bool(it.isActive))
			st.SetGlobal("score", wangshu.Number(it.score))
			if _, err := st.CallInto(dst[:], fn); err != nil {
				b.Fatal(err)
			}
			_ = dst[0].Bool()
		}
	}
}

func BenchmarkRealworldTransform_Gibbous(b *testing.B) {
	items := makeItems()
	prog, _ := wangshu.Compile([]byte(transformScript), "xform")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	fn := warmEvaluate(b, st, func() {
		it := items[0]
		st.SetGlobal("raw_score", wangshu.Number(it.rawScore))
		st.SetGlobal("base_bias", wangshu.Number(it.baseBias))
		st.SetGlobal("recency_factor", wangshu.Number(it.recencyFactor))
	})
	defer fn.Release()
	var dst [1]wangshu.Value
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, it := range items {
			st.SetGlobal("raw_score", wangshu.Number(it.rawScore))
			st.SetGlobal("base_bias", wangshu.Number(it.baseBias))
			st.SetGlobal("recency_factor", wangshu.Number(it.recencyFactor))
			if _, err := st.CallInto(dst[:], fn); err != nil {
				b.Fatal(err)
			}
			_ = dst[0].Number()
		}
	}
}
