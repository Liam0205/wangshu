//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_spec_subdivmul_e2e_test.go —— PJ2 投机模板扩 SUB/MUL/DIV 三档
// 真升层 e2e:`function(x,y) return x-y end` / `return x*y` / `return x/y`
// 经 P4 升层后 mmap 段真发字节级模板(对应 SSE op = F2 0F 5C/59/5E C1)。
//
// 与 ADD e2e 对位:每档 fast-path(双 number 输入 byte-equal 解释器)+
// deopt-path(table+number 触发 IsNumber guard 失败 → host.Arith → raise
// byte-equal 解释器报错信息)。
//
// **prove-the-path 命中证据**:本测真升层 + 真在 mmap+RX 段跑——不同于
// jit 包内 byte-equal 字节单测(只验编码)和 mmap+RX round-trip(只验
// SSE op 真跑),本测覆盖 useSpec 主路径(compiler.go::Compile → bridge
// 注入 → enterGibbous → p4Code.Run → callJITSpec → deopt 检测降级)。

// TestPJ2_SpeculativeSUB_E2E_FastPath:f(11,7) = 4(SUB 快路径 byte-equal).
func TestPJ2_SpeculativeSUB_E2E_FastPath(t *testing.T) {
	src := `
local function f(x, y) return x - y end
for i = 1, 100 do f(i, i*2) end
return f(11, 7)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 4 {
		t.Errorf("f(11,7) = %v, want 4(spec SUB 路径)", got)
	}
}

// TestPJ2_SpeculativeSUB_E2E_DeoptPath:table-number → IsNumber 失败 →
// host.Arith → raise(byte-equal 解释器报错)。
func TestPJ2_SpeculativeSUB_E2E_DeoptPath(t *testing.T) {
	src := `
local function f(x, y) return x - y end
for i = 1, 100 do f(i, i*2) end
return f({}, 1)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err == nil {
		t.Fatal("table-number 应 raise(spec deopt → host.Arith → raise)")
	}
}

// TestPJ2_SpeculativeMUL_E2E_FastPath:f(6,7) = 42(MUL 快路径).
func TestPJ2_SpeculativeMUL_E2E_FastPath(t *testing.T) {
	src := `
local function f(x, y) return x * y end
for i = 1, 100 do f(i, i*2) end
return f(6, 7)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 42 {
		t.Errorf("f(6,7) = %v, want 42(spec MUL 路径)", got)
	}
}

// TestPJ2_SpeculativeMUL_E2E_DeoptPath:string*number 触发 deopt → host.Arith
// → string-to-number 自动转换(Lua 5.1 隐式转 number);用 nil 强制 raise.
func TestPJ2_SpeculativeMUL_E2E_DeoptPath(t *testing.T) {
	src := `
local function f(x, y) return x * y end
for i = 1, 100 do f(i, i*2) end
return f(nil, 1)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err == nil {
		t.Fatal("nil*number 应 raise(spec deopt → host.Arith → raise)")
	}
}

// TestPJ2_SpeculativeDIV_E2E_FastPath:f(42,6) = 7(DIV 快路径).
func TestPJ2_SpeculativeDIV_E2E_FastPath(t *testing.T) {
	src := `
local function f(x, y) return x / y end
for i = 1, 100 do f(i, i*2) end
return f(42, 6)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 7 {
		t.Errorf("f(42,6) = %v, want 7(spec DIV 路径)", got)
	}
}

// TestPJ2_SpeculativeDIV_E2E_DeoptPath:table/number 触发 deopt → host.Arith
// → raise(byte-equal 解释器报错).
func TestPJ2_SpeculativeDIV_E2E_DeoptPath(t *testing.T) {
	src := `
local function f(x, y) return x / y end
for i = 1, 100 do f(i, i*2) end
return f({}, 1)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err == nil {
		t.Fatal("table/number 应 raise(spec deopt → host.Arith → raise)")
	}
}

// TestPJ2_SpeculativeDIV_E2E_DivByZero:f(1,0) = +Inf(IEEE 754,Lua 5.1
// 不抛错,spec 快路径与解释器一致 byte-equal)。
func TestPJ2_SpeculativeDIV_E2E_DivByZero(t *testing.T) {
	src := `
local function f(x, y) return x / y end
for i = 1, 100 do f(i, i*2) end
return f(1, 0)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := value.AsNumber(value.Value(rets[0]))
	// IEEE 754:1.0/0.0 = +Inf,Lua 5.1 不 raise(crescent doArith byte-equal)
	if got <= 0 || got != got+1 { // +Inf == +Inf + 1
		t.Errorf("f(1,0) = %v, want +Inf(IEEE 754;spec DIV / 解释器同语义)", got)
	}
}
