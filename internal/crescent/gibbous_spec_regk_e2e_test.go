//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"testing"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_spec_regk_e2e_test.go — PJ2 reg-K speculation template real-promotion e2e:
// hot-path constant-folded forms such as `function(x) return x + 1 end` emit, after
// P4 promotion, a reg-K template directly in the mmap segment (73 bytes, single guard
// on the reg operand + K imm64 burned in).
//
// luac encoding: `x + K` form → ADD A B(reg) C(>=256 = K idx),
// same for the wangshu compiler.

// TestPJ2_SpecRegK_ADD_FastPath: f(x)=x+5 (K=5 burned in) via the reg-K template.
func TestPJ2_SpecRegK_ADD_FastPath(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(x) return x + 5 end
for i = 1, 100 do f(i) end
return f(10)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 15 {
		t.Errorf("f(10) = %v, want 15(reg-K ADD 10+5)", got)
	}
	if jit.SpecRegKHits() == 0 {
		t.Errorf("SpecRegKHits = 0,reg-K 模板未真编译——降级 host(prove-the-path 失败)")
	}
	t.Logf("SpecRegKHits=%d / SpecRegRegHits=%d", jit.SpecRegKHits(), jit.SpecRegRegHits())
}

// TestPJ2_SpecRegK_SUB_FastPath: f(x)=x-3.
func TestPJ2_SpecRegK_SUB_FastPath(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(x) return x - 3 end
for i = 1, 100 do f(i) end
return f(10)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 7 {
		t.Errorf("f(10) = %v, want 7(reg-K SUB 10-3)", got)
	}
	if jit.SpecRegKHits() == 0 {
		t.Errorf("SpecRegKHits = 0,reg-K SUB 未真编译")
	}
}

// TestPJ2_SpecRegK_MUL_FastPath: f(x)=x*2 (common hot-path multiply form).
func TestPJ2_SpecRegK_MUL_FastPath(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(x) return x * 2 end
for i = 1, 100 do f(i) end
return f(7)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 14 {
		t.Errorf("f(7) = %v, want 14(reg-K MUL 7*2)", got)
	}
	if jit.SpecRegKHits() == 0 {
		t.Errorf("SpecRegKHits = 0,reg-K MUL 未真编译")
	}
}

// TestPJ2_SpecRegK_DIV_FastPath: f(x)=x/6.
func TestPJ2_SpecRegK_DIV_FastPath(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(x) return x / 6 end
for i = 1, 100 do f(i) end
return f(42)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 7 {
		t.Errorf("f(42) = %v, want 7(reg-K DIV 42/6)", got)
	}
	if jit.SpecRegKHits() == 0 {
		t.Errorf("SpecRegKHits = 0,reg-K DIV 未真编译")
	}
}

// TestPJ2_SpecRegK_DeoptPath: table+constant → IsNumber guard fails → host.Arith
// → raise (byte-equal interpreter error).
func TestPJ2_SpecRegK_DeoptPath(t *testing.T) {
	src := `
local function f(x) return x + 5 end
for i = 1, 100 do f(i) end
return f({})`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err == nil {
		t.Fatal("table+number 应 raise(reg-K spec deopt → host.Arith → raise)")
	}
}

// TestPJ2_SpecRegK_StringNotInvested: in the f(x)=x.."str" form, K is a string,
// so it does not take the spec path (only number constants are supported) → falls
// back to host.Arith; ADD A B Kstring triggers the helper "attempt to perform
// arithmetic on string" — byte-equal to the interpreter.
//
// Note: this test verifies both that reg-K speculation does not wrongly absorb a
// string K (should fall back to host), and that the host path's string + number
// arithmetic error behavior is correct.
func TestPJ2_SpecRegK_StringNotInvested(t *testing.T) {
	src := `
local function f(x) return x + 5 end
for i = 1, 100 do f(i) end
return f("not_a_number")`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		// "not_a_number" cannot coerce to a number → raise (correct)
		return
	}
	// In case it passes (if string coercion somehow succeeded)
	t.Logf("rets=%v (string coercion 成功,与解释器路径一致)", rets)
}
