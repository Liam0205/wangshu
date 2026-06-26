//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"testing"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_pj4_table_e2e_test.go —— PJ4 IC ArrayHit 字节级 inline 真升层
// e2e:`function(t) return t[1] end` 形态经 P4 升层后 mmap 段直发 IC 直达槽
// inline,跳过哈希 + byte-equal P1。
//
// **prove-the-path**:SpecTableHits 探针实证模板真编译。
//
// **关键路径理解**(下次踩坑前看清):
//
// PJ4 IC inline 走通的链路如下:
//
//  1. P1 解释器跑 inner f(t) 时,GETTABLE → icGetTable 填 IC[0].Kind=ArrayHit
//     + Shape + Index(per-State,经 LoadProgram 时 cp.IC=make);
//  2. inner f 升层时 considerPromotion → Aggregate 把 IC[0] 聚合为
//     feedback.Points[0] (Kind=FBTableMono, Confidence=1.0, StableShape/Index);
//  3. jit.Compile 经 analyzeGetTableArrayHit 验「proto 形态 + IC slot 已填
//     + feedback mono + shape/index 一致」,命中 → compileIcArrayHit;
//  4. compileIcArrayHit emit 129 字节模板 + incSpecTableHits() ++,Run 经
//     callJITSpec 直发 mmap 段。
//
// **第 1 步是关键**:若用 SetForceAllPromote(true) 让 outer 进入即升层,
// outer 调 inner 时 inner 也立即升层,IC[0] 还没被 P1 跑过 → 步骤 3 失败
// 「IC slot Kind=None」→ analyzeGetTableArrayHit 返 false → 降级到
// analyzeShape 的 GETTABLE host helper 路径(byte-equal 但无加速)。
//
// 正确测法:**先关 force-all 跑 warmup 让 P1 解释器填 IC[0],再开 force-all
// 让 considerPromotion 升 inner f**。

// TestPJ4_TableArrayHit_E2E_WarmupThenForce:**字面命中** PJ4 IC ArrayHit
// 字节级 inline。
//
// 步骤:
//  1. 第一次 Call 不开 force-all,outer chunk 跑 100 次 inner f(t),
//     icGetTable 填 IC[0]=ArrayHit;outer 自身不会升层(outer chunk 长度
//     5 op < MinPromotableCodeLen=10);inner f 也不升层(EntryCount=100 <
//     HotEntryThreshold=200,且 len(Code)=2<10 short-proto 守卫拒)。
//  2. 第二次 Call 开 force-all 重新跑 outer。outer 升层时 outer 进入即立
//     即升;但 outer 调 inner 时 inner 也 force-all 升层,此时 IC[0] 已
//     被第 1 步填好 → analyzeGetTableArrayHit 命中 → 字节级 inline。
func TestPJ4_TableArrayHit_E2E_WarmupThenForce(t *testing.T) {
	jit.ResetSpecHits()

	src := `
local function f(t) return t[1] end
local t = {42, 43, 44}
for i = 1, 100 do f(t) end  -- warmup:P1 填 IC[0]=ArrayHit
return f(t)`

	st, mainCl := loadFnP4(t, src)

	// Phase 1:不开 force-all → outer/inner 都不升层,P1 解释器跑完
	// warmup,IC[0] 被填(对照后续 Phase 2 force-all 升层路径)。
	rets1, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets1[0])); got != 42 {
		t.Errorf("Phase 1 result = %v, want 42", got)
	}
	// 此时 SpecTableHits 应为 0(P1 解释器路径未升 inner)
	if jit.SpecTableHits() != 0 {
		t.Errorf("Phase 1 末:SpecTableHits=%d, want 0(P1 路径不应触发 IC inline 编译)",
			jit.SpecTableHits())
	}

	// Phase 2:开 force-all + 再次 Call。inner f 此时被强制升层,IC[0] 已
	// 经 Phase 1 填好 → analyzeGetTableArrayHit 命中 → 字节级 inline 编译。
	st.bridge.SetForceAllPromote(true)
	hitsBefore := jit.SpecTableHits()
	rets2, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets2[0])); got != 42 {
		t.Errorf("Phase 2 result = %v, want 42", got)
	}
	hitsAfter := jit.SpecTableHits()
	t.Logf("SpecTableHits: %d → %d(Phase 2 增量 = %d)",
		hitsBefore, hitsAfter, hitsAfter-hitsBefore)
	if hitsAfter <= hitsBefore {
		t.Errorf("Phase 2:SpecTableHits 未增长(%d → %d)"+
			" → IC ArrayHit 模板未真编译,prove-the-path 失败",
			hitsBefore, hitsAfter)
	}
}

// TestPJ4_TableArrayHit_E2E_NumericKey:验数字键(`t[2]`)走 IC ArrayHit
// inline——数字键的 arrayIndex 就是键值本身,IC.Index = stableIndex 直接命中
// array 段。
func TestPJ4_TableArrayHit_E2E_NumericKey(t *testing.T) {
	jit.ResetSpecHits()

	src := `
local function f(t) return t[2] end
local t = {100, 200, 300}
for i = 1, 100 do f(t) end  -- warmup 填 IC[0]
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
	if got := value.AsNumber(value.Value(rets[0])); got != 200 {
		t.Errorf("f(t) = %v, want 200(t[2])", got)
	}
	hitsAfter := jit.SpecTableHits()
	if hitsAfter <= hitsBefore {
		t.Errorf("NumericKey Phase 2:SpecTableHits 未增长 → IC inline 未触发")
	}
	t.Logf("NumericKey SpecTableHits=%d → %d", hitsBefore, hitsAfter)
}

// TestPJ4_TableArrayHit_E2E_ForceAllFallsToHost:旧版形态——只开 force-all
// 不预热,inner kernel 一进入即升层时 IC slot 还没填(P1 解释器路径被
// SetForceAllPromote(true) 跳过),analyzeGetTableArrayHit 返 false →
// fall through 到 analyzeShape 的 GETTABLE host helper 路径,byte-equal
// 但无字节级 inline 加速。SpecTableHits 应恒为 0(prove-the-path
// negative side:无证据说 IC inline 路径触达)。
//
// 保留作 force-all 路径 byte-equal 正确性对照(返回值仍对)。
func TestPJ4_TableArrayHit_E2E_ForceAllFallsToHost(t *testing.T) {
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
	// force-all 路径下 SpecTableHits 应恒为 0(inner kernel 升层时 IC slot
	// 还没被 P1 解释器填,analyzeGetTableArrayHit 返 false → fall through
	// 到 GETTABLE host helper 路径)。
	if jit.SpecTableHits() != 0 {
		t.Errorf("ForceAllFallsToHost:SpecTableHits=%d, want 0"+
			"(force-all 应让 inner kernel 一进入即升,IC slot 未填→fall through)",
			jit.SpecTableHits())
	}
	t.Logf("ForceAllFallsToHost:SpecTableHits=%d / SpecForLoopHits=%d / SpecRegKHits=%d",
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
	t.Logf("无预热:SpecTableHits=%d(应为 0,因为 IC slot 未填)",
		jit.SpecTableHits())
	if jit.SpecTableHits() != 0 {
		t.Errorf("无预热路径 SpecTableHits=%d, want 0(IC slot 未填不应触发 inline)",
			jit.SpecTableHits())
	}
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

// TestPJ4_TableSetArrayHit_E2E_WarmupThenForce:**字面命中** PJ4 SETTABLE
// IC ArrayHit 字节级 inline——array 段反向写。
//
// 形态:`function(t, v) t[K] = v end`(setter,数字键 K,value 是 reg)。
// 两 phase 形态:Phase 1 warmup 让 P1 解释器跑 setter → icSetTable 填
// IC[0]=ArrayHit;Phase 2 force-all 升 inner kernel → analyzeSetTable
// ArrayHit 命中 → SETTABLE 字节级 inline 编译 → SpecTableHits++=1。
func TestPJ4_TableSetArrayHit_E2E_WarmupThenForce(t *testing.T) {
	jit.ResetSpecHits()

	src := `
local function setter(t, v) t[1] = v end
local t = {0, 0, 0}
for i = 1, 100 do setter(t, i) end  -- warmup:P1 填 IC[0]=ArrayHit + 写值
setter(t, 42)  -- 升 P4 后再调
return t[1]`

	st, mainCl := loadFnP4(t, src)

	// Phase 1:不开 force-all,P1 跑 warmup + 末次 setter 调 100 次
	rets1, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets1[0])); got != 42 {
		t.Errorf("Phase 1 result = %v, want 42(末次 setter 写入)", got)
	}
	if jit.SpecTableHits() != 0 {
		t.Errorf("Phase 1 末:SpecTableHits=%d, want 0", jit.SpecTableHits())
	}

	// Phase 2:开 force-all + 再次 Call。inner setter 升 P4,IC[0]=ArrayHit
	// 已填 → analyzeSetTableArrayHit 命中 → SETTABLE 字节级 inline 编译。
	st.bridge.SetForceAllPromote(true)
	hitsBefore := jit.SpecTableHits()
	rets2, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets2[0])); got != 42 {
		t.Errorf("Phase 2 result = %v, want 42", got)
	}
	hitsAfter := jit.SpecTableHits()
	t.Logf("SETTABLE SpecTableHits: %d → %d(Phase 2 增量 = %d)",
		hitsBefore, hitsAfter, hitsAfter-hitsBefore)
	if hitsAfter <= hitsBefore {
		t.Errorf("Phase 2:SETTABLE SpecTableHits 未增长(%d → %d) → "+
			"SETTABLE IC inline 模板未真编译,prove-the-path 失败",
			hitsBefore, hitsAfter)
	}
}
