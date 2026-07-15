// Public API end-to-end tests — SetGlobal / GetGlobal / Call / Register
// (per-item gopher-lua drop-in form; issue #1 / 11 §7.1+§9.1).
package wangshu_test

import (
	"math"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// floatNearlyEqual compares floats with a tolerance of ≤ 1 ULP.
//
// **Why ULP tolerance is needed**: on arm64 (Apple Silicon / Linux arm64)
// the Go compiler may lower the expression `a*b + c` into a single FMADD
// instruction (IEEE 754 fused multiply-add, **one rounding**); on amd64 it
// usually lowers to two instructions, MUL + ADD (**two roundings**). Both
// lowerings conform to the Go spec (§3.5 allows fused floating-point
// implementations), but the results may differ by 1 ULP.
//
// On the test side want = `computed directly by a Go expression`, on the VM
// side = Lua bytecode MUL+ADD with two roundings + the crescent f64 path does
// not fuse; the two are not byte-equal on arm64, yet both conform to the spec.
//
// Use case: TestCall_PerItemLoop / TestCallInto_PerItemReuseDst compare the
// public API Call/CallInto float return values against Go expected values.
// This session's macos-latest CI empirically showed the arm64 FMADD vs amd64
// MUL+ADD behavioral difference (not introduced by P4; it exposed a
// fragility of the existing measurement tests on the darwin/arm64 physical
// machine).
func floatNearlyEqual(a, b float64) bool {
	if a == b {
		return true
	}
	// 1 ULP tolerance: adjacent representable floats differ by 1. math.Nextafter gives the next representable value.
	if math.Nextafter(a, b) == b || math.Nextafter(b, a) == a {
		return true
	}
	return false
}

func TestSetGetGlobal_Scalars(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	st.SetGlobal("n", wangshu.Number(42))
	st.SetGlobal("s", wangshu.String("hello"))
	st.SetGlobal("b", wangshu.Bool(true))
	st.SetGlobal("z", wangshu.Nil())

	if v := st.GetGlobal("n"); !v.IsNumber() || v.Number() != 42 {
		t.Errorf("n = %s, want 42", v.Display())
	}
	if v := st.GetGlobal("s"); !v.IsString() || v.Str() != "hello" {
		t.Errorf("s = %q", v.Str())
	}
	if v := st.GetGlobal("b"); !v.IsBool() || v.Bool() != true {
		t.Errorf("b = %s", v.Display())
	}
	if v := st.GetGlobal("z"); !v.IsNil() {
		t.Errorf("z = %s, want nil", v.Display())
	}
	if v := st.GetGlobal("missing"); !v.IsNil() {
		t.Errorf("missing = %s, want nil", v.Display())
	}
}

func TestSetGlobal_ScriptVisible(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`return x + y`), "rd")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetGlobal("x", wangshu.Number(10))
	st.SetGlobal("y", wangshu.Number(32))
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 1 || results[0].Number() != 42 {
		t.Errorf("result = %s", results[0].Display())
	}
}

func TestGetGlobalFn_AndCall(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`
function f(x) return x * 2 end
function g(a, b) return a + b, a - b end
`), "defs")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	fn := st.GetGlobal("f")
	if !fn.IsFunction() {
		t.Fatalf("GetGlobal(\"f\") = %s, want function", fn.Display())
	}
	defer fn.Release()
	r, err := st.Call(fn, wangshu.Number(21))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(r) != 1 || r[0].Number() != 42 {
		t.Errorf("f(21) = %s, want 42", r[0].Display())
	}

	g := st.GetGlobal("g")
	defer g.Release()
	r, err = st.Call(g, wangshu.Number(10), wangshu.Number(3))
	if err != nil {
		t.Fatalf("Call g: %v", err)
	}
	if len(r) != 2 || r[0].Number() != 13 || r[1].Number() != 7 {
		t.Errorf("g(10,3) = %v / %v", r[0].Display(), r[1].Display())
	}
}

func TestCall_PerItemLoop(t *testing.T) {
	// pineapple transform_by_lua form: fetch fn once, SetGlobal+Call in the loop.
	src := []byte(`function f() return item_x * 0.85 + 10.0 end`)
	prog, err := wangshu.Compile(src, "rule")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	fn := st.GetGlobal("f")
	if !fn.IsFunction() {
		t.Fatalf("f not function")
	}
	defer fn.Release()
	for i := 0; i < 100; i++ {
		st.SetGlobal("item_x", wangshu.Number(float64(i)))
		r, err := st.Call(fn)
		if err != nil {
			t.Fatalf("Call[%d]: %v", i, err)
		}
		want := float64(i)*0.85 + 10.0
		if !floatNearlyEqual(r[0].Number(), want) {
			t.Errorf("f[%d] = %v, want %v (≤1 ULP)", i, r[0].Number(), want)
		}
	}
}

func TestCallInto_Scalars(t *testing.T) {
	// CallInto writes multiple return values into the caller's dst (issue #8 zero-alloc boundary path).
	prog, _ := wangshu.Compile([]byte(`function f() return 1, 2.5, true, "hi" end`), "ci")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	fn := st.GetGlobal("f")
	defer fn.Release()
	var dst [8]wangshu.Value
	n, err := st.CallInto(dst[:], fn)
	if err != nil {
		t.Fatalf("CallInto: %v", err)
	}
	if n != 4 {
		t.Fatalf("n = %d, want 4", n)
	}
	if dst[0].Number() != 1 || dst[1].Number() != 2.5 || dst[2].Bool() != true || dst[3].Str() != "hi" {
		t.Errorf("dst = %v/%v/%v/%q", dst[0].Number(), dst[1].Number(), dst[2].Bool(), dst[3].Str())
	}
}

func TestCallInto_DstTruncates(t *testing.T) {
	// dst capacity insufficient: only len(dst) are written, n reflects the actual count.
	prog, _ := wangshu.Compile([]byte(`function f() return 1, 2, 3 end`), "ci")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	fn := st.GetGlobal("f")
	defer fn.Release()
	var dst [2]wangshu.Value
	n, err := st.CallInto(dst[:], fn)
	if err != nil {
		t.Fatalf("CallInto: %v", err)
	}
	if n != 2 {
		t.Errorf("n = %d, want 2 (truncated to dst cap)", n)
	}
}

func TestCallInto_PerItemReuseDst(t *testing.T) {
	// pineapple form: fetch fn once, reuse the same dst in the loop (zero-alloc hot path).
	prog, _ := wangshu.Compile([]byte(`function f() return item_x * 0.85 + 10.0 end`), "ci")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	fn := st.GetGlobal("f")
	defer fn.Release()
	var dst [1]wangshu.Value
	for i := 0; i < 100; i++ {
		st.SetGlobal("item_x", wangshu.Number(float64(i)))
		n, err := st.CallInto(dst[:], fn)
		if err != nil {
			t.Fatalf("CallInto[%d]: %v", i, err)
		}
		want := float64(i)*0.85 + 10.0
		if n != 1 || !floatNearlyEqual(dst[0].Number(), want) {
			t.Errorf("f[%d] = %v, want %v (≤1 ULP)", i, dst[0].Number(), want)
		}
	}
}

func TestCallInto_GCStressStringNoUAF(t *testing.T) {
	// the string return value must still be readable under GC stress + reused dst (bytes already copied out of arena).
	prog, _ := wangshu.Compile([]byte(`function mk(s) return s .. "-suffix" end`), "ci")
	st := wangshu.NewState(wangshu.Options{})
	st.SetGCStressMode(true)
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	fn := st.GetGlobal("mk")
	defer fn.Release()
	var dst [4]wangshu.Value
	for i := 0; i < 500; i++ {
		n, err := st.CallInto(dst[:], fn, wangshu.String("item"))
		if err != nil {
			t.Fatalf("CallInto[%d]: %v", i, err)
		}
		if n != 1 || dst[0].Str() != "item-suffix" {
			t.Fatalf("iter %d: got %q", i, dst[0].Str())
		}
	}
}

func TestCallInto_ZeroAlloc(t *testing.T) {
	// the scalar-return boundary path must be truly zero-alloc (issue #8 acceptance criterion).
	prog, _ := wangshu.Compile([]byte(`function f() return x ~= nil end`), "ci")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	fn := st.GetGlobal("f")
	defer fn.Release()
	var dst [1]wangshu.Value
	got := testing.AllocsPerRun(1000, func() {
		st.SetGlobal("x", wangshu.Number(1))
		_, _ = st.CallInto(dst[:], fn)
		_ = dst[0].Bool()
	})
	if got != 0 {
		t.Errorf("CallInto scalar path = %v allocs/op, want 0", got)
	}
}

func TestCallInto_RejectsForeignFn(t *testing.T) {
	// a cross-State fn is rejected (same guard as Call).
	prog, _ := wangshu.Compile([]byte(`function f() return 1 end`), "ci")
	st1 := wangshu.NewState(wangshu.Options{})
	st2 := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st1); err != nil {
		t.Fatalf("run: %v", err)
	}
	fn := st1.GetGlobal("f")
	defer fn.Release()
	var dst [1]wangshu.Value
	if _, err := st2.CallInto(dst[:], fn); err == nil {
		t.Error("CallInto on foreign State should error")
	}
}

func TestCall_LuaRuntimeErrorToGoError(t *testing.T) {
	prog, _ := wangshu.Compile([]byte(`function bad() error("boom") end`), "err")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	fn := st.GetGlobal("bad")
	defer fn.Release()
	_, err := st.Call(fn)
	if err == nil {
		t.Fatalf("Call: want error, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %q, want contain boom", err.Error())
	}
}

func TestCall_NonFunction(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	_, err := st.Call(wangshu.Number(1))
	if err == nil || !strings.Contains(err.Error(), "not a function") {
		t.Errorf("err = %v, want 'not a function'", err)
	}
}

func TestCall_CrossState(t *testing.T) {
	prog, _ := wangshu.Compile([]byte(`function f() return 1 end`), "x")
	st1 := wangshu.NewState(wangshu.Options{})
	st2 := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st1); err != nil {
		t.Fatalf("run: %v", err)
	}
	fn := st1.GetGlobal("f")
	defer fn.Release()
	if _, err := st2.Call(fn); err == nil || !strings.Contains(err.Error(), "different State") {
		t.Errorf("cross-State err = %v, want 'different State'", err)
	}
}

func TestCall_AfterRelease(t *testing.T) {
	prog, _ := wangshu.Compile([]byte(`function f() return 1 end`), "x")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	fn := st.GetGlobal("f")
	fn.Release()
	if _, err := st.Call(fn); err == nil || !strings.Contains(err.Error(), "not a function") {
		// after Release, fnState == nil → IsFunction() = false → "not a function" path
		t.Errorf("after release err = %v", err)
	}
	// repeated Release has no side effect
	fn.Release()
}

func TestGetGlobal_PinSurvivesGlobalOverwrite(t *testing.T) {
	// after GetGlobal fetches it, SetGlobal overwrites the same key → the old fn Value is still callable
	// (the pin table treats the ref as a root, GC does not reclaim it; otherwise freelist reuse = UAF).
	prog, _ := wangshu.Compile([]byte(`function f() return 7 end`), "x")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	fn := st.GetGlobal("f")
	defer fn.Release()
	// overwrite globals f
	st.SetGlobal("f", wangshu.Nil())
	// trigger GC (under stress mode every safepoint does a full collect)
	st.SetGCStressMode(true)
	defer st.SetGCStressMode(false)
	// fn still points to the original closure
	r, err := st.Call(fn)
	if err != nil {
		t.Fatalf("Call after overwrite: %v", err)
	}
	if len(r) != 1 || r[0].Number() != 7 {
		t.Errorf("r = %s, want 7", r[0].Display())
	}
}
