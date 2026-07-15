//go:build wangshu_p3 && wangshu_profile

// Gibbous tier: the embedded boundary benchmark's evaluate() runs on wazero
// after a force-all promotion, giving a three-way comparison against crescent
// (WangshuCallInto) and gopher.
//
// This measures the "host crosses the boundary once per item + calls one script
// function" path: the boundary copy cost (CallInto is zero-alloc) is orthogonal
// to promotion, so the gibbous difference shows up only in the VM execution
// inside evaluate()'s body. evaluate is a non-vararg inner function (host→Lua
// call); with force-all + warmup it promotes to gibbous, so the gibbous path is
// actually exercised.
//
// Run: go test -tags "wangshu_p3 wangshu_profile" -bench 'Gibbous' ./benchmarks/embedded/

package embedded

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// warmEvaluate: after force-all + warmup promotion, repeatedly calls evaluate
// via CallInto (zero-alloc boundary). preset sets up the globals the script
// depends on before the warmup call (to avoid reading nil).
func warmEvaluate(b *testing.B, st *wangshu.State, preset func()) wangshu.Value {
	b.Helper()
	st.SetForceAllPromote(true)
	fn := st.GetGlobal("evaluate")
	if preset != nil {
		preset()
	}
	// Warmup: the first call drives evaluate to promote to gibbous (first call
	// runs on crescent).
	var dst [1]wangshu.Value
	if _, err := st.CallInto(dst[:], fn); err != nil {
		b.Fatalf("warmup: %v", err)
	}
	return fn
}

// Historical name preserved: this is the CallInto (zero-alloc) variant. See
// _GibbousCall below for the allocating variant added later.
func BenchmarkMiniCallOnly_Gibbous(b *testing.B) {
	prog, _ := wangshu.Compile([]byte(constPredicateScript), "bench")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	fn := warmEvaluate(b, st, nil) // const predicate reads no globals
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

// _GibbousCall variants exercise the allocating st.Call() path so the P3
// column can be split into Call/CallInto pairs matching the P4 side
// (embedded_gibbous_jit_test.go).

func BenchmarkMiniCallOnly_GibbousCall(b *testing.B) {
	prog, _ := wangshu.Compile([]byte(constPredicateScript), "bench")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	_ = warmEvaluate(b, st, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fn := st.GetGlobal("evaluate")
		res, err := st.Call(fn)
		if err != nil {
			b.Fatal(err)
		}
		_ = res[0].Bool()
		fn.Release()
	}
}

func BenchmarkMiniBoundary_GibbousCall(b *testing.B) {
	prog, _ := wangshu.Compile([]byte(ifPredicateScript), "bench")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	st.SetGlobal("user_id", wangshu.String("12345"))
	_ = warmEvaluate(b, st, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st.SetGlobal("user_id", wangshu.String("12345"))
		fn := st.GetGlobal("evaluate")
		res, err := st.Call(fn)
		if err != nil {
			b.Fatal(err)
		}
		_ = res[0].Bool()
		fn.Release()
	}
}

func BenchmarkMiniBoundary_GibbousCallInto(b *testing.B) {
	prog, _ := wangshu.Compile([]byte(ifPredicateScript), "bench")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	st.SetGlobal("user_id", wangshu.String("12345"))
	fn := warmEvaluate(b, st, nil)
	defer fn.Release()
	var dst [1]wangshu.Value
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st.SetGlobal("user_id", wangshu.String("12345"))
		if _, err := st.CallInto(dst[:], fn); err != nil {
			b.Fatal(err)
		}
		_ = dst[0].Bool()
	}
}

// Historical: CallInto path only. Call variant added below as
// _GibbousCall to align with the P4 gibbous-jit split.
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

func BenchmarkRealworldPredicate_GibbousCall(b *testing.B) {
	items := makeItems()
	prog, _ := wangshu.Compile([]byte(predicateScript), "pred")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	_ = warmEvaluate(b, st, func() {
		it := items[0]
		st.SetGlobal("user_id", wangshu.String(it.userID))
		st.SetGlobal("age", wangshu.Number(it.age))
		st.SetGlobal("is_active", wangshu.Bool(it.isActive))
		st.SetGlobal("score", wangshu.Number(it.score))
	})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, it := range items {
			st.SetGlobal("user_id", wangshu.String(it.userID))
			st.SetGlobal("age", wangshu.Number(it.age))
			st.SetGlobal("is_active", wangshu.Bool(it.isActive))
			st.SetGlobal("score", wangshu.Number(it.score))
			fn := st.GetGlobal("evaluate")
			res, err := st.Call(fn)
			if err != nil {
				b.Fatal(err)
			}
			_ = res[0].Bool()
			fn.Release()
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

func BenchmarkRealworldTransform_GibbousCall(b *testing.B) {
	items := makeItems()
	prog, _ := wangshu.Compile([]byte(transformScript), "xform")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	_ = warmEvaluate(b, st, func() {
		it := items[0]
		st.SetGlobal("raw_score", wangshu.Number(it.rawScore))
		st.SetGlobal("base_bias", wangshu.Number(it.baseBias))
		st.SetGlobal("recency_factor", wangshu.Number(it.recencyFactor))
	})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, it := range items {
			st.SetGlobal("raw_score", wangshu.Number(it.rawScore))
			st.SetGlobal("base_bias", wangshu.Number(it.baseBias))
			st.SetGlobal("recency_factor", wangshu.Number(it.recencyFactor))
			fn := st.GetGlobal("evaluate")
			res, err := st.Call(fn)
			if err != nil {
				b.Fatal(err)
			}
			_ = res[0].Number()
			fn.Release()
		}
	}
}

// ── Auto-mode variants: production heat threshold, long-lived State ───────
// State + Program stay alive across iterations (embedder-with-pool
// semantics). No force-all, no explicit warmup — the first ~200 calls
// stay on crescent until the natural threshold trips, then subsequent
// calls run on gibbous. The b.N average includes that warmup tail so the
// number reflects what a real embedder measures.

func BenchmarkMiniCallOnly_GibbousAutoCallInto(b *testing.B) {
	prog, _ := wangshu.Compile([]byte(constPredicateScript), "bench")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	fn := st.GetGlobal("evaluate")
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

func BenchmarkMiniCallOnly_GibbousAutoCall(b *testing.B) {
	prog, _ := wangshu.Compile([]byte(constPredicateScript), "bench")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fn := st.GetGlobal("evaluate")
		res, err := st.Call(fn)
		if err != nil {
			b.Fatal(err)
		}
		_ = res[0].Bool()
		fn.Release()
	}
}

func BenchmarkMiniBoundary_GibbousAutoCallInto(b *testing.B) {
	prog, _ := wangshu.Compile([]byte(ifPredicateScript), "bench")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	fn := st.GetGlobal("evaluate")
	defer fn.Release()
	var dst [1]wangshu.Value
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st.SetGlobal("user_id", wangshu.String("12345"))
		if _, err := st.CallInto(dst[:], fn); err != nil {
			b.Fatal(err)
		}
		_ = dst[0].Bool()
	}
}

func BenchmarkMiniBoundary_GibbousAutoCall(b *testing.B) {
	prog, _ := wangshu.Compile([]byte(ifPredicateScript), "bench")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st.SetGlobal("user_id", wangshu.String("12345"))
		fn := st.GetGlobal("evaluate")
		res, err := st.Call(fn)
		if err != nil {
			b.Fatal(err)
		}
		_ = res[0].Bool()
		fn.Release()
	}
}

func BenchmarkRealworldPredicate_GibbousAutoCallInto(b *testing.B) {
	items := makeItems()
	prog, _ := wangshu.Compile([]byte(predicateScript), "pred")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	fn := st.GetGlobal("evaluate")
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

func BenchmarkRealworldPredicate_GibbousAutoCall(b *testing.B) {
	items := makeItems()
	prog, _ := wangshu.Compile([]byte(predicateScript), "pred")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, it := range items {
			st.SetGlobal("user_id", wangshu.String(it.userID))
			st.SetGlobal("age", wangshu.Number(it.age))
			st.SetGlobal("is_active", wangshu.Bool(it.isActive))
			st.SetGlobal("score", wangshu.Number(it.score))
			fn := st.GetGlobal("evaluate")
			res, err := st.Call(fn)
			if err != nil {
				b.Fatal(err)
			}
			_ = res[0].Bool()
			fn.Release()
		}
	}
}

func BenchmarkRealworldTransform_GibbousAutoCallInto(b *testing.B) {
	items := makeItems()
	prog, _ := wangshu.Compile([]byte(transformScript), "xform")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	fn := st.GetGlobal("evaluate")
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

func BenchmarkRealworldTransform_GibbousAutoCall(b *testing.B) {
	items := makeItems()
	prog, _ := wangshu.Compile([]byte(transformScript), "xform")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, it := range items {
			st.SetGlobal("raw_score", wangshu.Number(it.rawScore))
			st.SetGlobal("base_bias", wangshu.Number(it.baseBias))
			st.SetGlobal("recency_factor", wangshu.Number(it.recencyFactor))
			fn := st.GetGlobal("evaluate")
			res, err := st.Call(fn)
			if err != nil {
				b.Fatal(err)
			}
			_ = res[0].Number()
			fn.Release()
		}
	}
}
