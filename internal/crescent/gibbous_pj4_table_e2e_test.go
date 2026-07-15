//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"testing"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_pj4_table_e2e_test.go —— PJ4 IC ArrayHit byte-level inline real promotion
// e2e: `function(t) return t[1] end` shape, after P4 promotion the mmap segment
// dispatches the IC direct-hit slot inline, skipping the hash + byte-equal P1 path.
//
// **prove-the-path**: SpecTableHits probe proves the template is really compiled.
//
// **Critical-path understanding** (read before the next pitfall):
//
// The PJ4 IC inline path works through the following chain:
//
//  1. When the P1 interpreter runs inner f(t), GETTABLE → icGetTable fills
//     IC[0].Kind=ArrayHit + Shape + Index (per-State, via cp.IC=make at LoadProgram);
//  2. When inner f is promoted, considerPromotion → Aggregate folds IC[0] into
//     feedback.Points[0] (Kind=FBTableMono, Confidence=1.0, StableShape/Index);
//  3. jit.Compile goes through analyzeGetTableArrayHit to check "proto shape + IC slot
//     filled + feedback mono + shape/index consistent"; on a hit → compileIcArrayHit;
//  4. compileIcArrayHit emits the 129-byte template + incSpecTableHits() ++, and Run
//     dispatches straight to the mmap segment via callJITSpec.
//
// **Step 1 is the key one**: if SetForceAllPromote(true) makes outer promote on entry,
// then when outer calls inner, inner is promoted immediately too, so IC[0] has not yet
// been run by P1 → step 3 fails with "IC slot Kind=None" → analyzeGetTableArrayHit
// returns false → falls back to analyzeShape's GETTABLE host helper path (byte-equal
// but with no speedup).
//
// Correct way to test: **first turn force-all off and run warmup so the P1 interpreter
// fills IC[0], then turn force-all on so considerPromotion promotes inner f**.

// TestPJ4_TableArrayHit_E2E_WarmupThenForce: **literal hit** on PJ4 IC ArrayHit
// byte-level inline.
//
// Steps:
//  1. The first Call keeps force-all off; the outer chunk runs inner f(t) 100 times,
//     icGetTable fills IC[0]=ArrayHit; outer itself is not promoted (outer chunk length
//     5 op < MinPromotableCodeLen=10); inner f is not promoted either (EntryCount=100 <
//     HotEntryThreshold=200, and len(Code)=2<10 rejected by the short-proto guard).
//  2. The second Call turns force-all on and reruns outer. When outer is promoted it
//     promotes on entry; but when outer calls inner, inner is force-all promoted too,
//     and at this point IC[0] is already filled by step 1 → analyzeGetTableArrayHit
//     hits → byte-level inline.
func TestPJ4_TableArrayHit_E2E_WarmupThenForce(t *testing.T) {
	jit.ResetSpecHits()

	src := `
local function f(t) return t[1] end
local t = {42, 43, 44}
for i = 1, 100 do f(t) end  -- warmup:P1 填 IC[0]=ArrayHit
return f(t)`

	st, mainCl := loadFnP4(t, src)

	// Phase 1: force-all off → neither outer nor inner is promoted; the P1
	// interpreter finishes warmup and IC[0] gets filled (contrast with the
	// later Phase 2 force-all promotion path).
	rets1, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets1[0])); got != 42 {
		t.Errorf("Phase 1 result = %v, want 42", got)
	}
	// At this point SpecTableHits should be 0 (the P1 interpreter path did not promote inner)
	if jit.SpecTableHits() != 0 {
		t.Errorf("Phase 1 末:SpecTableHits=%d, want 0(P1 路径不应触发 IC inline 编译)",
			jit.SpecTableHits())
	}

	// Phase 2: turn force-all on + Call again. inner f is now force-promoted, IC[0]
	// is already filled by Phase 1 → analyzeGetTableArrayHit hits → byte-level inline compile.
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

// TestPJ4_TableArrayHit_E2E_NumericKey: verifies a numeric key (`t[2]`) takes the IC
// ArrayHit inline —— for a numeric key the arrayIndex is the key value itself, IC.Index =
// stableIndex directly hits the array segment.
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

// TestPJ4_TableArrayHit_E2E_ForceAllFallsToHost: the old-style shape —— only turn
// force-all on without warmup; when the inner kernel is promoted on entry the IC slot
// is not yet filled (the P1 interpreter path is skipped by SetForceAllPromote(true)),
// analyzeGetTableArrayHit returns false → falls through to analyzeShape's GETTABLE host
// helper path, byte-equal but with no byte-level inline speedup. SpecTableHits should
// stay 0 (prove-the-path negative side: no evidence that the IC inline path is reached).
//
// Kept as a byte-equal correctness reference for the force-all path (return value still correct).
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
	// On the force-all path SpecTableHits should stay 0 (when the inner kernel is
	// promoted the IC slot has not been filled by the P1 interpreter,
	// analyzeGetTableArrayHit returns false → falls through to the GETTABLE host
	// helper path).
	if jit.SpecTableHits() != 0 {
		t.Errorf("ForceAllFallsToHost:SpecTableHits=%d, want 0"+
			"(force-all 应让 inner kernel 一进入即升,IC slot 未填→fall through)",
			jit.SpecTableHits())
	}
	t.Logf("ForceAllFallsToHost:SpecTableHits=%d / SpecForLoopHits=%d / SpecRegKHits=%d",
		jit.SpecTableHits(), jit.SpecForLoopHits(), jit.SpecRegKHits())
}

// TestPJ4_TableArrayHit_E2E_NotPromotedWithoutWarmup: without warmup (no IC slot),
// P4 should not take the IC inline path (analysis returns false, falls through to the host helper).
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

// TestPJ4_TableNodeHit_E2E_WarmupThenForce: **literal hit** on PJ4 IC NodeHit
// byte-level inline —— the hash segment (`t["x"]`) rather than the array segment (`t[1]`).
//
// Same two-phase shape as ArrayHit, but the table has only a hash segment (no array
// segment) → icGetTable takes the hash path + IC[0].Kind = ICKindNodeHit. When force-all
// promotes the inner kernel, analyzeGetTableNodeHit hits → byte-level NodeHit inline
// compile → SpecTableHits increments by +1.
func TestPJ4_TableNodeHit_E2E_WarmupThenForce(t *testing.T) {
	jit.ResetSpecHits()

	src := `
local function f(t) return t["x"] end
local t = {x = 42, y = 99, z = 123}
for i = 1, 100 do f(t) end  -- warmup:P1 填 IC[0]=NodeHit
return f(t)`

	st, mainCl := loadFnP4(t, src)

	// Phase 1: force-all off → neither outer nor inner is promoted; P1 finishes warmup and fills IC[0]
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

	// Phase 2: turn force-all on + Call again. inner f is promoted to P4, IC[0]=NodeHit
	// is filled → analyzeGetTableNodeHit hits → NodeHit byte-level inline compile.
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

// TestPJ4_TableNodeHit_E2E_NumberKey: uses the numeric constant key `t[7]`, but **the
// value is not in the array segment** (array segment size=0, all keys go to the hash
// segment) → triggers the NodeHit path rather than ArrayHit. This proves the NodeHit
// path for a "numeric key in the hash segment".
func TestPJ4_TableNodeHit_E2E_NumberKey(t *testing.T) {
	jit.ResetSpecHits()

	// Use dict-style `{[7]=42}` to explicitly force 7 into the hash segment (note: `{42}` is the array segment)
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
	// Note: this case may in fact still take ArrayHit (luac may optimize `[7]=42` into
	// the array segment via automatic ASIZE expansion); if SpecTableHits grows then
	// either ArrayHit or NodeHit proves the IC inline was really compiled.
	if hitsAfter <= hitsBefore {
		t.Errorf("NumberKey-in-hash:SpecTableHits 未增长 → IC inline 未触发")
	}
}

// TestPJ4_TableSetArrayHit_E2E_WarmupThenForce: **literal hit** on PJ4 SETTABLE
// IC ArrayHit byte-level inline —— reverse write into the array segment.
//
// Shape: `function(t, v) t[K] = v end` (setter, numeric key K, value is a reg).
// Two-phase shape: Phase 1 warmup lets the P1 interpreter run setter → icSetTable fills
// IC[0]=ArrayHit; Phase 2 force-all promotes the inner kernel → analyzeSetTable
// ArrayHit hits → SETTABLE byte-level inline compile → SpecTableHits++=1.
//
// **Value-falsification hardening** (per external review feedback): Phase 2's last
// write uses a value (99) that differs from warmup's last value, so that `return t[1]`
// can distinguish "Phase 2 really wrote" from "Phase 2 wrote the wrong index / did not
// write and relies on Phase 1's leftover value".
func TestPJ4_TableSetArrayHit_E2E_WarmupThenForce(t *testing.T) {
	jit.ResetSpecHits()

	src := `
local function setter(t, v) t[1] = v end
local t = {0, 0, 0}
for i = 1, 100 do setter(t, i) end  -- warmup:末次写 t[1] = 100
setter(t, 99)  -- 升 P4 后写不同值,与 warmup 末值(100)区分
return t[1]`

	st, mainCl := loadFnP4(t, src)

	// Phase 1: force-all off, P1 runs warmup last write t[1]=100 + setter(t,99)
	// Note: Phase 1 actually runs the full chunk with P1 (including the trailing setter(t,99)) → t[1]=99
	rets1, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets1[0])); got != 99 {
		t.Errorf("Phase 1 result = %v, want 99(末次 setter(t,99) 写入)", got)
	}
	if jit.SpecTableHits() != 0 {
		t.Errorf("Phase 1 末:SpecTableHits=%d, want 0", jit.SpecTableHits())
	}

	// Phase 2: turn force-all on + Call again. inner setter is promoted to P4, IC[0]=ArrayHit
	// is filled → analyzeSetTableArrayHit hits → SETTABLE byte-level inline compile.
	st.bridge.SetForceAllPromote(true)
	hitsBefore := jit.SpecTableHits()
	rets2, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets2[0])); got != 99 {
		t.Errorf("Phase 2 result = %v, want 99(SETTABLE inline 写入 t[1]=99)", got)
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

// TestPJ4_TableSelfArrayHit_E2E_WarmupThenForce: **literal hit** on PJ4 SELF
// IC ArrayHit byte-level inline —— the `obj:method()` shape (numeric key method, rare
// but valid).
//
// **Shape**: `function(obj) local m = obj[1] end` (numeric key in array segment).
// **Note**: in reality `obj:method()` is compiled by luac to SELF+CALL+...; this test
// uses a single-BB SELF+RETURN shape (`local m = obj:method;return m`) close to the SELF
// ArrayHit shape-recognition condition. method is a numeric K hitting the array segment
// (real-world `obj:method()` uses a string method name, takes NodeHit, left for PJ4+).
func TestPJ4_TableSelfArrayHit_E2E_WarmupThenForce(t *testing.T) {
	jit.ResetSpecHits()

	// luac compiles the `local m = obj:method` shape to:
	//   SELF A=R(m) B=R(obj) C=K(numeric key method index)
	//   RETURN A 2 (take m)
	// Here obj is a table and method is a numeric key like obj[1].
	// The obj:f(...) syntax requires a method name — but a method name is a string (NodeHit);
	// using obj[1] directly does not take SELF (GETTABLE also works).
	// The only way to trigger the SELF shape = actually use `obj:something`, but something
	// must be an ident, not a number. So ArrayHit SELF almost never appears in luac output.
	//
	// This test states its boundary: it runs a non-SELF path, mainly testing the byte-level
	// correctness of the SELF template emit (already covered by byte-level unit tests in the
	// jit/amd64 package). The SELF main-path e2e real promotion is left for NodeHit SELF (next stage).
	src := `
local function f(t) return t[1] end
local t = {42}
for i = 1, 100 do f(t) end
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
		t.Errorf("f(t) = %v, want 42", got)
	}
	hitsAfter := jit.SpecTableHits()
	// The SELF ArrayHit shape is not really compiled by luac (a method name can only be an
	// ident, not a number) → this test actually takes the GETTABLE ArrayHit path, and
	// SpecTableHits still grows (ArrayHit path). This test verifies the SELF ArrayHit
	// main path **does not break** the existing ArrayHit path.
	t.Logf("SELF-ArrayHit edge:SpecTableHits=%d → %d(实际走 ArrayHit 路径)",
		hitsBefore, hitsAfter)
	if hitsAfter <= hitsBefore {
		t.Errorf("ArrayHit 路径 SpecTableHits 未增长 → 退化")
	}
}

// TestPJ4_TableSetNodeHit_E2E_WarmupThenForce: **literal hit** on PJ4 SETTABLE
// NodeHit byte-level inline —— reverse write with a string key in the hash segment.
//
// Shape: `function(t, v) t["x"] = v end` (setter, string key, value is a reg).
// Two-phase shape: Phase 1 warmup lets the P1 interpreter run setter → icSetTable fills
// IC[0]=NodeHit; Phase 2 force-all promotes the inner kernel → analyzeSetTableNodeHit
// hits → SETTABLE NodeHit byte-level inline compile → SpecTableHits++=1.
//
// **Value-falsification hardening**: Phase 2's last write of 99 differs from warmup's
// last value (100), so that `return t.x` can distinguish them.
func TestPJ4_TableSetNodeHit_E2E_WarmupThenForce(t *testing.T) {
	jit.ResetSpecHits()

	src := `
local function setter(t, v) t["x"] = v end
local t = {x = 0, y = 0}
for i = 1, 100 do setter(t, i) end  -- warmup:末次写 t.x = 100
setter(t, 99)  -- 升 P4 后写不同值
return t.x`

	st, mainCl := loadFnP4(t, src)

	// Phase 1: P1 runs warmup last write + setter(t, 99) → t.x = 99
	rets1, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets1[0])); got != 99 {
		t.Errorf("Phase 1 result = %v, want 99", got)
	}
	if jit.SpecTableHits() != 0 {
		t.Errorf("Phase 1 末:SpecTableHits=%d, want 0", jit.SpecTableHits())
	}

	// Phase 2: turn force-all on + Call again. inner setter is promoted to P4, IC[0]=NodeHit
	// is filled → analyzeSetTableNodeHit hits → SETTABLE NodeHit byte-level compile
	st.bridge.SetForceAllPromote(true)
	hitsBefore := jit.SpecTableHits()
	rets2, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets2[0])); got != 99 {
		t.Errorf("Phase 2 result = %v, want 99", got)
	}
	hitsAfter := jit.SpecTableHits()
	t.Logf("SETTABLE NodeHit SpecTableHits: %d → %d(Phase 2 增量 = %d)",
		hitsBefore, hitsAfter, hitsAfter-hitsBefore)
	if hitsAfter <= hitsBefore {
		t.Errorf("Phase 2:SETTABLE NodeHit SpecTableHits 未增长 → " +
			"NodeHit set IC inline 模板未真编译,prove-the-path 失败")
	}
}

// TestPJ4_TableSelfNodeHit_E2E_LuacBoundary states the SELF NodeHit e2e shape boundary.
//
// **luac shape boundary**: `obj:method` must have parentheses `()` to be valid Lua
// syntax, but `obj:method()` compiles to SELF + CALL + RETURN (3+ op) rather than the
// SELF + RETURN 2-op shape —— the latter does not match the analyzeSelfNodeHit shape guard.
//
// That is: **luac does not actually emit the "SELF + RETURN A 2" + NodeHit shape** (same
// as the SELF ArrayHit boundary). Wiring up the SELF NodeHit main path is the engineering
// foundation, but **real-promotion e2e requires the PJ5 CALL byte-level inline wiring**
// (SELF + CALL + RETURN shape recognition) before it can be reached.
//
// This test degrades to "run the `return obj.method` shape (GETTABLE NodeHit path)" to
// verify that wiring up the SELF NodeHit main path does not break the existing NodeHit
// get path. The SELF NodeHit template byte-level emit + main-path Compile call chain are
// backstopped by synthetic-driven unit tests (same shape as compiler_pj4_self_test.go in
// the jit package; the SELF NodeHit synthetic-driven unit test is added in the same commit).
func TestPJ4_TableSelfNodeHit_E2E_LuacBoundary(t *testing.T) {
	jit.ResetSpecHits()

	// Shape degradation: use the GETTABLE NodeHit path (obj.method is GETTABLE, not SELF)
	// to verify this batch of wiring does not break the NodeHit get path
	src := `
local function f(obj) return obj.method end
local obj = {method = 42, other = 99}
for i = 1, 100 do f(obj) end
return f(obj)`

	st, mainCl := loadFnP4(t, src)

	// Phase 1
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
		t.Errorf("f(obj) = %v, want 42", got)
	}
	hitsAfter := jit.SpecTableHits()
	t.Logf("SELF NodeHit edge:实测走 GETTABLE NodeHit 路径,SpecTableHits=%d→%d",
		hitsBefore, hitsAfter)
	// actually takes the GETTABLE NodeHit path (NodeHit get SpecTableHits grows)
	if hitsAfter <= hitsBefore {
		t.Errorf("GETTABLE NodeHit 路径 SpecTableHits 未增长 → 退化")
	}
}
