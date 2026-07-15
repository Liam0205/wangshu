//go:build !wangshu_p3 && !wangshu_p4

// Realworld-embedded tier (issue #8 §"Why it matters"): a boundary-dominated
// workload shaped like pineapple's transform_by_lua hot path — for each of N
// items, set the item's fields as globals, call a script function, read the
// scalar result. This is the OPPOSITE end from realworld's pure-VM scripts
// (heavy compute, boundary amortized): here scripts are short and call
// frequency is high, so the per-call boundary floor dominates end-to-end.
//
// Two realistic shapes, mixed (matches issue #8's "per-item predicate or
// feature transform"):
//   - predicates: boolean gates over item fields (eligibility / filtering)
//   - transforms: numeric feature derivation (score * weight + bias style)
//
// Each shape is run on three host strategies:
//   - WangshuCall:     allocating Call() (the documented per-item path)
//   - WangshuCallInto: zero-alloc CallInto() (issue #8 fix)
//   - Gopher:          gopher-lua's CallByParam + Get/Pop equivalent
//
// build tag `!wangshu_p3`: mutually exclusive with `embedded_gibbous_test.go`
// (wangshu_p3), to keep the p3 build's wangshu_profile sampling hook from
// polluting the `_Wangshu` / `_Gopher` numbers, and to avoid duplicating
// bench-p1 (issue #15 review). The shared const / type / makeItems live in
// `consts_test.go` (no build tag).
//
// Run: `make bench-p1`
package embedded

import (
	"testing"

	glua "github.com/yuin/gopher-lua"

	"github.com/Liam0205/wangshu"
)

// ── Predicate: per-item boolean gate ───────────────────────────────────────

func BenchmarkRealworldPredicate_WangshuCall(b *testing.B) {
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

func BenchmarkRealworldPredicate_WangshuCallInto(b *testing.B) {
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

func BenchmarkRealworldPredicate_Gopher(b *testing.B) {
	items := makeItems()
	L := glua.NewState()
	defer L.Close()
	if err := L.DoString(predicateScript); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, it := range items {
			L.SetGlobal("user_id", glua.LString(it.userID))
			L.SetGlobal("age", glua.LNumber(it.age))
			L.SetGlobal("is_active", glua.LBool(it.isActive))
			L.SetGlobal("score", glua.LNumber(it.score))
			fn := L.GetGlobal("evaluate")
			if err := L.CallByParam(glua.P{Fn: fn, NRet: 1, Protect: true}); err != nil {
				b.Fatal(err)
			}
			_ = glua.LVAsBool(L.Get(-1))
			L.Pop(1)
		}
	}
}

// ── Transform: per-item numeric feature derivation ──────────────────────────

func BenchmarkRealworldTransform_WangshuCall(b *testing.B) {
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

func BenchmarkRealworldTransform_WangshuCallInto(b *testing.B) {
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

func BenchmarkRealworldTransform_Gopher(b *testing.B) {
	items := makeItems()
	L := glua.NewState()
	defer L.Close()
	if err := L.DoString(transformScript); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, it := range items {
			L.SetGlobal("raw_score", glua.LNumber(it.rawScore))
			L.SetGlobal("base_bias", glua.LNumber(it.baseBias))
			L.SetGlobal("recency_factor", glua.LNumber(it.recencyFactor))
			fn := L.GetGlobal("evaluate")
			if err := L.CallByParam(glua.P{Fn: fn, NRet: 1, Protect: true}); err != nil {
				b.Fatal(err)
			}
			_ = float64(glua.LVAsNumber(L.Get(-1)))
			L.Pop(1)
		}
	}
}
