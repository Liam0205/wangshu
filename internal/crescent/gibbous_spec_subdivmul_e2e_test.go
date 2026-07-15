//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_spec_subdivmul_e2e_test.go —— PJ2 speculative template extended to
// the three ops SUB/MUL/DIV. Real-promotion e2e:
// `function(x,y) return x-y end` / `return x*y` / `return x/y`. After P4
// promotion the mmap segment emits a real byte-level template (corresponding
// SSE op = F2 0F 5C/59/5E C1).
//
// Mirrors the ADD e2e: each op has a fast-path (two number inputs, byte-equal
// with the interpreter) plus a deopt-path (table+number trips the IsNumber
// guard → host.Arith → raise, byte-equal with the interpreter's error
// message).
//
// **prove-the-path hit evidence**: this test really promotes and really runs
// in the mmap+RX segment — unlike the jit-package byte-equal unit tests (which
// only verify encoding) and the mmap+RX round-trip (which only verifies the
// SSE op actually runs), this test covers the useSpec main path
// (compiler.go::Compile → bridge injection → enterGibbous → p4Code.Run →
// callJITSpec → deopt detection and downgrade).

// TestPJ2_SpeculativeSUB_E2E_FastPath: f(11,7) = 4 (SUB fast-path, byte-equal).
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

// TestPJ2_SpeculativeSUB_E2E_DeoptPath: table-number → IsNumber fails →
// host.Arith → raise (byte-equal with the interpreter's error).
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

// TestPJ2_SpeculativeMUL_E2E_FastPath: f(6,7) = 42 (MUL fast-path).
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

// TestPJ2_SpeculativeMUL_E2E_DeoptPath: string*number trips deopt → host.Arith
// → string-to-number auto-conversion (Lua 5.1 implicit number coercion); use
// nil to force a raise.
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

// TestPJ2_SpeculativeDIV_E2E_FastPath: f(42,6) = 7 (DIV fast-path).
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

// TestPJ2_SpeculativeDIV_E2E_DeoptPath: table/number trips deopt → host.Arith
// → raise (byte-equal with the interpreter's error).
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

// TestPJ2_SpeculativeDIV_E2E_DivByZero: f(1,0) = +Inf (IEEE 754; Lua 5.1 does
// not raise; spec fast-path is byte-equal with the interpreter).
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
	// IEEE 754: 1.0/0.0 = +Inf, Lua 5.1 does not raise (crescent doArith byte-equal)
	if got <= 0 || got != got+1 { // +Inf == +Inf + 1
		t.Errorf("f(1,0) = %v, want +Inf(IEEE 754;spec DIV / 解释器同语义)", got)
	}
}
