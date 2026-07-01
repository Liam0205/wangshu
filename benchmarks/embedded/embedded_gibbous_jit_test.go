//go:build wangshu_p4 && wangshu_profile

// P4 gibbous-jit tier: embedded boundary benchmarks with evaluate() promoted
// to P4 self-managed native code through force-all, contrasted against
// crescent (WangshuCall / WangshuCallInto) + gopher-lua.
//
// The path measured is "host sets fields as globals + calls a script fn +
// reads the scalar result per item". Two branches per benchmark:
//   - _Call:     allocating st.Call() path, [] Value returned each call.
//   - _CallInto: zero-alloc st.CallInto() path, caller reuses dst slice.
// These are orthogonal to promotion: the boundary allocation cost and the
// VM tier are independent axes, so having Call/CallInto splits on both P3
// and P4 lets the reader see whether tier or boundary dominates.
//
// evaluate() is a non-vararg host->Lua function; force-all + warmup lifts
// it to P4 (first call stays on crescent, promotion happens at entry, next
// call goes through the P4 trampoline).
//
// Run:
//   go test -tags "wangshu_p4 wangshu_profile" -bench 'GibbousJIT' ./benchmarks/embedded/

package embedded

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// warmEvaluateJIT is the P4 counterpart of warmEvaluate in
// embedded_gibbous_test.go: force-all + warm the entry so evaluate is
// promoted, then hand the caller the fn.
func warmEvaluateJIT(b *testing.B, st *wangshu.State, preset func()) wangshu.Value {
	b.Helper()
	st.SetForceAllPromote(true)
	fn := st.GetGlobal("evaluate")
	if preset != nil {
		preset()
	}
	var dst [1]wangshu.Value
	if _, err := st.CallInto(dst[:], fn); err != nil {
		b.Fatalf("warmup: %v", err)
	}
	return fn
}

// ── Mini CallOnly (no host globals, evaluate returns a const) ──────────────

func BenchmarkMiniCallOnly_GibbousJITCall(b *testing.B) {
	prog, _ := wangshu.Compile([]byte(constPredicateScript), "bench")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	_ = warmEvaluateJIT(b, st, nil)
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

func BenchmarkMiniCallOnly_GibbousJITCallInto(b *testing.B) {
	prog, _ := wangshu.Compile([]byte(constPredicateScript), "bench")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	fn := warmEvaluateJIT(b, st, nil)
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

// ── Mini Boundary (CallOnly + one SetGlobal per iter) ───────────────────────

func BenchmarkMiniBoundary_GibbousJITCall(b *testing.B) {
	prog, _ := wangshu.Compile([]byte(ifPredicateScript), "bench")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	st.SetGlobal("user_id", wangshu.String("12345"))
	_ = warmEvaluateJIT(b, st, nil)
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

func BenchmarkMiniBoundary_GibbousJITCallInto(b *testing.B) {
	prog, _ := wangshu.Compile([]byte(ifPredicateScript), "bench")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	st.SetGlobal("user_id", wangshu.String("12345"))
	fn := warmEvaluateJIT(b, st, nil)
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

// ── Realworld Predicate: per-item field-set + evaluate() ────────────────────

func BenchmarkRealworldPredicate_GibbousJITCall(b *testing.B) {
	items := makeItems()
	prog, _ := wangshu.Compile([]byte(predicateScript), "pred")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	_ = warmEvaluateJIT(b, st, func() {
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

func BenchmarkRealworldPredicate_GibbousJITCallInto(b *testing.B) {
	items := makeItems()
	prog, _ := wangshu.Compile([]byte(predicateScript), "pred")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	fn := warmEvaluateJIT(b, st, func() {
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

// ── Realworld Transform: per-item numeric feature derivation ────────────────

func BenchmarkRealworldTransform_GibbousJITCall(b *testing.B) {
	items := makeItems()
	prog, _ := wangshu.Compile([]byte(transformScript), "xform")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	_ = warmEvaluateJIT(b, st, func() {
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

func BenchmarkRealworldTransform_GibbousJITCallInto(b *testing.B) {
	items := makeItems()
	prog, _ := wangshu.Compile([]byte(transformScript), "xform")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		b.Fatal(err)
	}
	fn := warmEvaluateJIT(b, st, func() {
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

// ── Auto-mode variants: production heat threshold, long-lived State ───────
// State + Program stay alive across iterations (embedder-with-pool
// semantics). No force-all, no explicit warmup — the first ~200 calls
// stay on crescent until the natural threshold trips, then subsequent
// calls run on P4 native code.

func BenchmarkMiniCallOnly_GibbousJITAutoCallInto(b *testing.B) {
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

func BenchmarkMiniCallOnly_GibbousJITAutoCall(b *testing.B) {
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

func BenchmarkMiniBoundary_GibbousJITAutoCallInto(b *testing.B) {
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

func BenchmarkMiniBoundary_GibbousJITAutoCall(b *testing.B) {
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

func BenchmarkRealworldPredicate_GibbousJITAutoCallInto(b *testing.B) {
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

func BenchmarkRealworldPredicate_GibbousJITAutoCall(b *testing.B) {
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

func BenchmarkRealworldTransform_GibbousJITAutoCallInto(b *testing.B) {
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

func BenchmarkRealworldTransform_GibbousJITAutoCall(b *testing.B) {
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
