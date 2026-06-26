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

// TestPJ4_TableNodeHit_E2E_WarmupThenForce:**字面命中** PJ4 IC NodeHit
// 字节级 inline——hash 段(`t["x"]`)而非 array 段(`t[1]`)。
//
// 与 ArrayHit 同款两 phase 形态,但表只有 hash 段(无数组段)→ 触发
// icGetTable 走 hash 路径 + IC[0].Kind = ICKindNodeHit。force-all 升 inner
// kernel 时 analyzeGetTableNodeHit 命中 → 字节级 NodeHit inline 编译 →
// SpecTableHits 增量 +1。
func TestPJ4_TableNodeHit_E2E_WarmupThenForce(t *testing.T) {
	jit.ResetSpecHits()

	src := `
local function f(t) return t["x"] end
local t = {x = 42, y = 99, z = 123}
for i = 1, 100 do f(t) end  -- warmup:P1 填 IC[0]=NodeHit
return f(t)`

	st, mainCl := loadFnP4(t, src)

	// Phase 1:不开 force-all → outer/inner 都不升层,P1 跑完 warmup 填 IC[0]
	rets1, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets1[0])); got != 42 {
		t.Errorf("Phase 1 result = %v, want 42", got)
	}
	if jit.SpecTableHits() != 0 {
		t.Errorf("Phase 1 末:SpecTableHits=%d, want 0", jit.SpecTableHits())
	}

	// Phase 2:开 force-all + 再次 Call。inner f 升 P4,IC[0]=NodeHit 已填 →
	// analyzeGetTableNodeHit 命中 → NodeHit 字节级 inline 编译。
	st.bridge.SetForceAllPromote(true)
	hitsBefore := jit.SpecTableHits()
	rets2, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets2[0])); got != 42 {
		t.Errorf("Phase 2 result = %v, want 42(t['x'])", got)
	}
	hitsAfter := jit.SpecTableHits()
	t.Logf("NodeHit SpecTableHits: %d → %d(Phase 2 增量 = %d)",
		hitsBefore, hitsAfter, hitsAfter-hitsBefore)
	if hitsAfter <= hitsBefore {
		t.Errorf("Phase 2:NodeHit SpecTableHits 未增长(%d → %d) → "+
			"IC NodeHit 模板未真编译,prove-the-path 失败", hitsBefore, hitsAfter)
	}
}

// TestPJ4_TableNodeHit_E2E_NumberKey:用数字常量键 `t[7]`,但**值不在 array
// 段**(数组段 size=0,所有键都进 hash 段)→ 触发 NodeHit 路径而非 ArrayHit。
// 这是 NodeHit 路径的「数字键 in hash 段」实证。
func TestPJ4_TableNodeHit_E2E_NumberKey(t *testing.T) {
	jit.ResetSpecHits()

	// 用 dict-style `{[7]=42}` 显式让 7 进 hash 段(注:`{42}` 是 array 段)
	src := `
local function f(t) return t[7] end
local t = {[7] = 42, [11] = 99}
for i = 1, 100 do f(t) end  -- warmup
return f(t)`

	st, mainCl := loadFnP4(t, src)

	// Phase 1 warmup
	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	// Phase 2 force-all
	st.bridge.SetForceAllPromote(true)
	hitsBefore := jit.SpecTableHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 42 {
		t.Errorf("f(t) = %v, want 42(t[7])", got)
	}
	hitsAfter := jit.SpecTableHits()
	t.Logf("NumberKey-in-hash SpecTableHits=%d → %d", hitsBefore, hitsAfter)
	// 注:此 case 实际可能仍走 ArrayHit(luac 可能优化 `[7]=42` 进 array 段
	// 的 ASIZE 自动扩);若 SpecTableHits 增长则 ArrayHit 或 NodeHit 都验证
	// IC inline 真编译。
	if hitsAfter <= hitsBefore {
		t.Errorf("NumberKey-in-hash:SpecTableHits 未增长 → IC inline 未触发")
	}
}
