//go:build !wangshu_p3 && !wangshu_p4

// Package embedded benchmarks the host↔VM boundary cost — the path an embedder
// actually exercises (per-item: set inputs, call a function, read the result),
// on Wangshu vs gopher-lua. This is the "embedded mini-bench" tier from issue #8.
//
// Why this exists: the baseline micro-bench (PureVM) passes NO data across the
// Go↔Lua boundary and shows Wangshu's VM-core win. But a boundary-dominated
// embedder (many short calls, each exchanging a little host data) is dominated
// by per-call boundary cost, not VM-core speed. This tier measures that path
// honestly, and contrasts the allocating Call() against the zero-alloc CallInto().
//
// build tag `!wangshu_p3 && !wangshu_p4`:与 `embedded_gibbous_test.go`
// (wangshu_p3)/`embedded_gibbous_jit_test.go`(wangshu_p4)互斥,避免 p3/p4
// build 的 wangshu_profile 采样钩污染 `_Wangshu` / `_Gopher` 数字 + 与 bench-p1
// 重复(issue #15 review)。共享 const / type / makeItems 在 `consts_test.go`
// 里(无 build tag)。
//
// Run: `make bench-p1`
package embedded

import (
	"testing"

	glua "github.com/yuin/gopher-lua"

	"github.com/Liam0205/wangshu"
)

// ── A. PureVM baseline (official methodology, no boundary crossing) ─────────

func BenchmarkMiniPureVM_Wangshu(b *testing.B) {
	prog, err := wangshu.Compile([]byte(simpleSelfContained), "bench")
	if err != nil {
		b.Fatal(err)
	}
	st := wangshu.NewState(wangshu.Options{})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMiniPureVM_Gopher(b *testing.B) {
	L := glua.NewState()
	defer L.Close()
	fn, err := L.LoadString(simpleSelfContained)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		L.Push(fn)
		if err := L.PCall(0, glua.MultRet, nil); err != nil {
			b.Fatal(err)
		}
		L.SetTop(0)
	}
}

// ── B. CallOnly: call a pre-installed fn, read bool, NO host globals ─────────

func BenchmarkMiniCallOnly_WangshuCall(b *testing.B) {
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

func BenchmarkMiniCallOnly_WangshuCallInto(b *testing.B) {
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

func BenchmarkMiniCallOnly_Gopher(b *testing.B) {
	L := glua.NewState()
	defer L.Close()
	if err := L.DoString(constPredicateScript); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fn := L.GetGlobal("evaluate")
		if err := L.CallByParam(glua.P{Fn: fn, NRet: 1, Protect: true}); err != nil {
			b.Fatal(err)
		}
		ret := L.Get(-1)
		_ = glua.LVAsBool(ret)
		L.Pop(1)
	}
}

// ── C. Boundary: CallOnly + one SetGlobal per iter (realistic embed shape) ──

func BenchmarkMiniBoundary_WangshuCall(b *testing.B) {
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

func BenchmarkMiniBoundary_WangshuCallInto(b *testing.B) {
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

func BenchmarkMiniBoundary_Gopher(b *testing.B) {
	L := glua.NewState()
	defer L.Close()
	if err := L.DoString(ifPredicateScript); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		L.SetGlobal("user_id", glua.LString("12345"))
		fn := L.GetGlobal("evaluate")
		if err := L.CallByParam(glua.P{Fn: fn, NRet: 1, Protect: true}); err != nil {
			b.Fatal(err)
		}
		ret := L.Get(-1)
		_ = glua.LVAsBool(ret)
		L.Pop(1)
	}
}
