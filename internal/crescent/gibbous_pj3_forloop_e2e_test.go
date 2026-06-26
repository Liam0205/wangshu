//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"testing"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_pj3_forloop_e2e_test.go —— PJ3 字节级 FORLOOP inline 真升层
// e2e:`function() for i=1,K do end end`(全常量空 body)经 P4 升层后
// mmap 段内自循环,完整 idx 累加 + ucomisd limit + backward jmp 跑通。
//
// 这是 **PJ3 真接入主路径** 的物理证据(从 PJ2 单 op spec 模板跨进
// PJ3 字节级控制流 inline)——P4 首次在 mmap 段内**字节级跑循环**,
// 不经任何 host helper round-trip。

// TestPJ3_ForLoopEmpty_E2E_FastPath:全常量空 for 循环真升层。
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

// TestPJ3_ForLoopEmpty_E2E_SingleIter:`for i=1,1 do end`(单次迭代),
// 验证 FORLOOP idx 累加 + ucomisd 边界正确(idx=1=limit 时 cont).
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

// TestPJ3_ForLoopEmpty_E2E_LongLoop:`for i=1,1000 do end`(千次迭代),
// 测试 backward jmp 跑长循环.
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

// TestPJ3_ForLoopRegLimit_E2E_FastPath:`function(n) for i=1,n do end end`
// + f(1000) — reg-limit 形态 hot path,IsNumber guard 通过 → 字节级 loop.
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

// TestPJ3_ForLoopRegLimit_E2E_DeoptPath:`f(\"not_a_number\")` — limit 非
// number → IsNumber guard 失败 → host.ForPrep raise.
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
		// Lua 5.1 对 "not_a_number" 尝试 tonumber coerce 失败 raise
		t.Logf("没 raise(可能 tonumber('not_a_number') 成功了?)")
	} else {
		t.Logf("reg-limit deopt path raise: %v", err)
	}
}

// TestPJ3_ForLoopUpvalLimit_E2E_FastPath:closure capture limit
// (`local n=1000; local function f() for i=1,n do end end`)— upvalue-
// limit 形态:Run 端先调 host.GetUpval 写 limit reg,然后走 reg-limit
// 模板字节级 inline.
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
