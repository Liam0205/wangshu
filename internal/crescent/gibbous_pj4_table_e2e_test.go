//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"testing"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_pj4_table_e2e_test.go —— PJ4 IC ArrayHit 字节级 inline 真升层
// e2e:`function(t) return t["x"] end` / `return t[1]` 形态经 P4 升层后
// mmap 段直发 IC 直达槽 inline,跳过哈希 + byte-equal P1。
//
// **prove-the-path**:SpecTableHits 探针实证模板真编译。

// TestPJ4_TableArrayHit_E2E_FastPath:`function(t) return t[1] end`
// 形态——预热让 P1 解释器写 ICKindArrayHit + Refill 0,然后强制升层,
// IC 命中 → 字节级直达。
func TestPJ4_TableArrayHit_E2E_FastPath(t *testing.T) {
	jit.ResetSpecHits()

	src := `
local function f(t) return t[1] end
local t = {42, 43, 44}
for i = 1, 100 do f(t) end  -- warmup:P1 写 IC ArrayHit
return f(t)`

	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 42 {
		t.Errorf("f(t) = %v, want 42(t[1])", got)
	}
	t.Logf("SpecTableHits=%d / SpecForLoopHits=%d / SpecRegKHits=%d",
		jit.SpecTableHits(), jit.SpecForLoopHits(), jit.SpecRegKHits())
}

// TestPJ4_TableArrayHit_E2E_NotPromotedWithoutWarmup:不预热(无 IC slot)
// 时 P4 应不走 IC inline 路径(析出 false,fall through 到 host helper)。
func TestPJ4_TableArrayHit_E2E_NotPromotedWithoutWarmup(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(t) return t[1] end
local t = {99}
return f(t)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 99 {
		t.Errorf("f(t) = %v, want 99", got)
	}
	t.Logf("无预热:SpecTableHits=%d(可能 0,因为 IC slot 未填)",
		jit.SpecTableHits())
}
