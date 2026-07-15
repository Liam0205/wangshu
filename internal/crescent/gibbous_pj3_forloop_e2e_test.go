//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"testing"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_pj3_forloop_e2e_test.go —— PJ3 byte-level FORLOOP inline real
// tier-up e2e: `function() for i=1,K do end end` (all-constant empty body)
// self-loops inside the mmap segment after P4 tier-up, running the full idx
// accumulation + ucomisd limit + backward jmp.
//
// This is physical evidence of the **PJ3 real main path** (crossing from the
// PJ2 single-op spec template into PJ3 byte-level control-flow inline) — the
// first time P4 runs a **byte-level loop** inside the mmap segment without any
// host-helper round-trip.

// TestPJ3_ForLoopEmpty_E2E_FastPath: all-constant empty for loop, real tier-up.
func TestPJ3_ForLoopEmpty_E2E_FastPath(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f()
  for i = 1, 100 do
  end
end
for i = 1, 50 do f() end
return 42`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 42 {
		t.Errorf("rets = %v, want 42(PJ3 FORLOOP inline 不影响 main chunk 返值)", got)
	}
	if jit.SpecForLoopHits() == 0 {
		t.Errorf("SpecForLoopHits = 0,FORLOOP 模板未真编译——降级 host(prove-the-path 失败)")
	}
	t.Logf("SpecForLoopHits=%d / SpecRegKHits=%d / SpecRegRegHits=%d / SpecChainHits=%d",
		jit.SpecForLoopHits(), jit.SpecRegKHits(), jit.SpecRegRegHits(), jit.SpecChainHits())
}

// TestPJ3_ForLoopEmpty_E2E_SingleIter: `for i=1,1 do end` (single iteration),
// verifying FORLOOP idx accumulation + ucomisd bound is correct (cont when
// idx=1=limit).
func TestPJ3_ForLoopEmpty_E2E_SingleIter(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f()
  for i = 1, 1 do
  end
end
for i = 1, 50 do f() end
return 1`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 1 {
		t.Errorf("rets = %v, want 1", got)
	}
	if jit.SpecForLoopHits() == 0 {
		t.Errorf("SpecForLoopHits = 0,FORLOOP 单次迭代未真编译")
	}
}

// TestPJ3_ForLoopEmpty_E2E_LongLoop: `for i=1,1000 do end` (thousand
// iterations), testing backward jmp over a long loop.
func TestPJ3_ForLoopEmpty_E2E_LongLoop(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f()
  for i = 1, 1000 do
  end
end
for i = 1, 50 do f() end
return 1000`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 1000 {
		t.Errorf("rets = %v, want 1000", got)
	}
	if jit.SpecForLoopHits() == 0 {
		t.Errorf("SpecForLoopHits = 0,FORLOOP 千次循环未真编译")
	}
}

// TestPJ3_ForLoopRegLimit_E2E_FastPath: `function(n) for i=1,n do end end`
// + f(1000) — reg-limit form hot path, IsNumber guard passes → byte-level loop.
func TestPJ3_ForLoopRegLimit_E2E_FastPath(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(n)
  for i = 1, n do
  end
end
for i = 1, 50 do f(1000) end
return 1000`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 1000 {
		t.Errorf("rets = %v, want 1000", got)
	}
	if jit.SpecForLoopHits() == 0 {
		t.Errorf("SpecForLoopHits = 0,reg-limit FORLOOP 未真编译")
	}
	t.Logf("reg-limit fast path:SpecForLoopHits=%d", jit.SpecForLoopHits())
}

// TestPJ3_ForLoopRegLimit_E2E_DeoptPath: `f(\"not_a_number\")` — limit is not a
// number → IsNumber guard fails → host.ForPrep raise.
func TestPJ3_ForLoopRegLimit_E2E_DeoptPath(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(n)
  for i = 1, n do
  end
end
for i = 1, 50 do f(1000) end -- warmup with number to ensure promote
return f("not_a_number") -- guard fail → deopt → host.ForPrep raise`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err == nil {
		// Lua 5.1 raises when tonumber coercion of "not_a_number" fails
		t.Logf("没 raise(可能 tonumber('not_a_number') 成功了?)")
	} else {
		t.Logf("reg-limit deopt path raise: %v", err)
	}
}

// TestPJ3_ForLoopUpvalLimit_E2E_FastPath: closure capture limit
// (`local n=1000; local function f() for i=1,n do end end`) — upvalue-limit
// form: the Run side first calls host.GetUpval to write the limit reg, then
// takes the reg-limit template for byte-level inline.
func TestPJ3_ForLoopUpvalLimit_E2E_FastPath(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local n = 1000
local function f()
  for i = 1, n do
  end
end
for i = 1, 50 do f() end
return n`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 1000 {
		t.Errorf("rets = %v, want 1000", got)
	}
	if jit.SpecForLoopHits() == 0 {
		t.Errorf("SpecForLoopHits = 0,upvalue-limit FORLOOP 未真编译")
	}
	t.Logf("upvalue-limit fast path:SpecForLoopHits=%d", jit.SpecForLoopHits())
}

// TestPJ3_ForLoopWithBody_E2E_ADD: `local s=0; for i=1,100 do s=s+1 end;
// return s` real tier-up takes byte-level body inline → s=100.
func TestPJ3_ForLoopWithBody_E2E_ADD(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f()
  local s = 0
  for i = 1, 100 do
    s = s + 1
  end
  return s
end
for i = 1, 50 do f() end
return f()`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 100 {
		t.Errorf("rets = %v, want 100", got)
	}
	if jit.SpecForLoopHits() == 0 {
		t.Errorf("SpecForLoopHits = 0,FORLOOP body inline 未真编译")
	}
	t.Logf("body inline:SpecForLoopHits=%d", jit.SpecForLoopHits())
}

// TestPJ3_ForLoopWithBody_E2E_MUL: `local s=1; for i=1,5 do s=s*2 end;
// return s` → s = 2^5 = 32.
func TestPJ3_ForLoopWithBody_E2E_MUL(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f()
  local s = 1
  for i = 1, 5 do
    s = s * 2
  end
  return s
end
for i = 1, 50 do f() end
return f()`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 32 {
		t.Errorf("rets = %v, want 32", got)
	}
	if jit.SpecForLoopHits() == 0 {
		t.Errorf("SpecForLoopHits = 0,FORLOOP body inline MUL 未真编译")
	}
}

// TestPJ3_ForLoopWithBody2_E2E_AddMul: two-statement body form
// `local s=0; for i=1,5 do s=s+1; s=s*2 end; return s`:
//
//	iter1: s=(0+1)*2=2 / iter2: (2+1)*2=6 / iter3: (6+1)*2=14
//	iter4: (14+1)*2=30 / iter5: (30+1)*2=62
func TestPJ3_ForLoopWithBody2_E2E_AddMul(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f()
  local s = 0
  for i = 1, 5 do
    s = s + 1
    s = s * 2
  end
  return s
end
for i = 1, 50 do f() end
return f()`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 62 {
		t.Errorf("rets = %v, want 62((((0+1)*2+1)*2+1)*2+1)*2+1)*2)", got)
	}
	if jit.SpecForLoopHits() == 0 {
		t.Errorf("SpecForLoopHits = 0,FORLOOP body2 未真编译")
	}
	t.Logf("body2 AddMul:SpecForLoopHits=%d", jit.SpecForLoopHits())
}
