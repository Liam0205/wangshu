//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"testing"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_spec_chain_e2e_test.go — PJ2 two-stage chained chain-KK speculative
// template real promotion e2e: `function(x) return x*2+1 end` etc. compile to a
// MUL+ADD chain (K1/K2 baked in at compile time; a single mmap segment call does
// both arithmetic ops, saving one boundary crossing).

// TestPJ2_SpecChain_MulAdd_FastPath:f(x)=x*2+1 → f(3)=7.
func TestPJ2_SpecChain_MulAdd_FastPath(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(x) return x*2+1 end
for i = 1, 100 do f(i) end
return f(3)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 7 {
		t.Errorf("f(3) = %v, want 7(chain MUL+ADD 3*2+1)", got)
	}
	if jit.SpecChainHits() == 0 {
		t.Errorf("SpecChainHits = 0,chain 模板未真编译——降级 host 双调")
	}
	t.Logf("SpecChainHits=%d / SpecRegKHits=%d / SpecRegRegHits=%d",
		jit.SpecChainHits(), jit.SpecRegKHits(), jit.SpecRegRegHits())
}

// TestPJ2_SpecChain_AddMul_FastPath: f(x)=(x+1)*2 → f(3)=8 (note: by Lua
// precedence this is actually x + 1*2 = 5; parentheses make it (x+1)*2, and luac
// compiles (x+1)*2 into an ADD+MUL chain). The test uses explicit parentheses.
func TestPJ2_SpecChain_AddMul_FastPath(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(x) return (x+1)*2 end
for i = 1, 100 do f(i) end
return f(3)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 8 {
		t.Errorf("f(3) = %v, want 8((3+1)*2)", got)
	}
	if jit.SpecChainHits() == 0 {
		t.Errorf("SpecChainHits = 0,(x+1)*2 chain 未真编译")
	}
}

// TestPJ2_SpecChain_DeoptPath: table*2+1 → guard fails → host.Arith × 2
// → raise byte-equal with the interpreter (table*number errors).
func TestPJ2_SpecChain_DeoptPath(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(x) return x*2+1 end
for i = 1, 100 do f(i) end
return f({})`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err == nil {
		t.Fatal("table*number+1 应 raise(chain deopt → host.Arith × 2 → raise)")
	}
}
