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
// Run: `go test -bench=Mini -benchmem ./benchmarks/embedded`
package embedded

import (
	"testing"

	glua "github.com/yuin/gopher-lua"

	"github.com/Liam0205/wangshu"
)

// official baseline "simple": data hardcoded as locals, no host data passed in.
const simpleSelfContained = `
local a, b = 1, 2
local r = 0
if a < b then r = a else r = b end
return r
`

// pineapple-shaped "if_" predicate: reads a GLOBAL (set by host per call).
const ifPredicateScript = `
function evaluate()
  if (user_id ~= nil and user_id ~= '' and user_id ~= '0') then
    return false
  else
    return true
  end
end
`

// const predicate: same control flow, reads NO globals (isolates call cost).
const constPredicateScript = `
function evaluate()
  local x = 1
  if x == 1 then return false else return true end
end
`

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
