// Public API end-to-end tests — SetGlobal / GetGlobal / Call / Register
// (per-item gopher-lua drop-in 形态;issue #1 / 11 §7.1+§9.1)。
package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

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
	// pineapple transform_by_lua 形态:取 fn 一次,循环里 SetGlobal+Call。
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
		if r[0].Number() != want {
			t.Errorf("f[%d] = %v, want %v", i, r[0].Number(), want)
		}
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
		// Release 后 fnState == nil → IsFunction() = false → "not a function" 路径
		t.Errorf("after release err = %v", err)
	}
	// 重复 Release 无副作用
	fn.Release()
}

func TestGetGlobal_PinSurvivesGlobalOverwrite(t *testing.T) {
	// GetGlobal 取出后,SetGlobal 覆盖同名键 → 旧 fn Value 仍可调
	// (pin 表把 ref 当根,GC 不回收;否则 freelist 复用 = UAF)。
	prog, _ := wangshu.Compile([]byte(`function f() return 7 end`), "x")
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	fn := st.GetGlobal("f")
	defer fn.Release()
	// 覆盖 globals f
	st.SetGlobal("f", wangshu.Nil())
	// 触发 GC(压力模式下每个 safepoint 都 full collect)
	st.SetGCStressMode(true)
	defer st.SetGCStressMode(false)
	// fn 仍指向原 closure
	r, err := st.Call(fn)
	if err != nil {
		t.Fatalf("Call after overwrite: %v", err)
	}
	if len(r) != 1 || r[0].Number() != 7 {
		t.Errorf("r = %s, want 7", r[0].Display())
	}
}
