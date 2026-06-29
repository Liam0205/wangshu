// Public API end-to-end tests — SetGlobal / GetGlobal / Call / Register
// (per-item gopher-lua drop-in 形态;issue #1 / 11 §7.1+§9.1)。
package wangshu_test

import (
	"math"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// floatNearlyEqual 允许 ≤ 1 ULP 容差比较浮点数。
//
// **为什么需要 ULP 容差**:Go 编译器在 arm64 (Apple Silicon / Linux arm64)
// 可能把 `a*b + c` 表达式 lower 为单条 FMADD 指令(IEEE 754 fused
// multiply-add,**一次舍入**);amd64 通常 lower 为 MUL + ADD 两条
// (**两次舍入**)。两种 lowering 都符合 Go spec(§3.5 浮点运算允许 fused
// 实现),但结果可能差 1 ULP。
//
// 测试侧 want = `Go 表达式直接算`,VM 侧 = Lua 字节码 MUL+ADD 两次舍入 +
// crescent f64 路径不 fuse;两者在 arm64 上不字节相等,但都符合规范。
//
// 用例:TestCall_PerItemLoop / TestCallInto_PerItemReuseDst 比较公共 API
// Call/CallInto 浮点返回值与 Go 期望值。承本会话 macos-latest CI 实证 arm64
// FMADD vs amd64 MUL+ADD 行为差(非 P4 引入,既有 measurement 测试在
// darwin/arm64 物理机上的脆弱点暴露)。
func floatNearlyEqual(a, b float64) bool {
	if a == b {
		return true
	}
	// 1 ULP 容差:相邻可表示浮点数差为 1。math.Nextafter 给出下一个可表示值。
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
		if !floatNearlyEqual(r[0].Number(), want) {
			t.Errorf("f[%d] = %v, want %v (≤1 ULP)", i, r[0].Number(), want)
		}
	}
}

func TestCallInto_Scalars(t *testing.T) {
	// CallInto 多返回值写入调用方 dst(issue #8 零分配边界路径)。
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
	// dst 容量不足:只写 len(dst) 个,n 反映实写数。
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
	// pineapple 形态:fn 一次取出,循环里复用同一 dst(零分配热路径)。
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
	// string 返回值在 GC stress + 复用 dst 下必须仍可读(已拷出 arena 字节)。
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
	// 标量返回的边界路径必须真正零分配(issue #8 验收口径)。
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
	// 跨 State 的 fn 被拒(与 Call 同款防护)。
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
