//go:build wangshu_p4 && wangshu_profile && arm64 && (linux || (darwin && cgo)) && !wangshu_qemu

// e2e_arm64_test.go - arm64 PJ10 native end-to-end through the wangshu
// public API (issue #37 exit-reason port).
//
// Mirrors the amd64 e2e_test.go harness but asserts the arm64-specific
// protocol probes: PromotionCount > 0 (the Proto really promoted),
// NativeRunCount increment (the mmap segment really ran), and
// DispatchHelperCount increment (the op really rode the exit-reason
// round trip rather than an interpreter fallback).
package peroptranslator_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

// runForceAllArm64 compiles src, runs it under force-all promotion, and
// returns the string results plus probe deltas.
func runForceAllArm64(t *testing.T, src string) (results []string, promoted int64, dispatched int64) {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "arm64e2e")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)

	runBefore := peroptranslator.NativeRunCount.Load()
	dispBefore := peroptranslator.DispatchHelperCount.Load()

	vals, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, v := range vals {
		results = append(results, v.Display())
	}
	if peroptranslator.NativeRunCount.Load() == runBefore {
		t.Fatal("NativeRunCount did not increase: arm64 native path was never executed " +
			"(the kernel Proto did not promote or fell back to the interpreter)")
	}
	return results, int64(st.PromotionCount()), peroptranslator.DispatchHelperCount.Load() - dispBefore
}

// TestArm64E2E_GETUPVAL: a hot closure reading + writing upvalues must
// promote on arm64 and ride the exit-reason protocol. The kernel body
// is 4+ ops (GETUPVAL ×2 + SETUPVAL + RETURN) so PJ7's analyzeShape
// rejects it (that path only takes 2-op prelude+RETURN forms) and
// Compile falls through to the PJ10 native translator. No arithmetic:
// the arm64 arith acceptance is still gated off until the inline
// NEON + exit-reason slow path lands (issue #37 step 7).
func TestArm64E2E_GETUPVAL(t *testing.T) {
	src := `
local u = 42
local function k()
  local a = u
  local b = u
  u = a
  return b
end
local r = 0
for i = 1, 200 do r = k() end
return r`
	results, promoted, dispatched := runForceAllArm64(t, src)
	if promoted == 0 {
		t.Fatal("PromotionCount = 0: GETUPVAL proto did not promote on arm64")
	}
	if dispatched == 0 {
		t.Fatal("DispatchHelperCount did not increase: GETUPVAL never rode the exit-reason protocol")
	}
	if len(results) != 1 || results[0] != "42" {
		t.Fatalf("results = %v, want [42]", results)
	}
}

// TestArm64E2E_CALL: a kernel whose body calls a known local function
// must promote and ride the exit-reason CALL (host.CallBaseline). The
// kernel pads with MOVE/LOADK chains to pass the CALL density gate
// (totalOps/callCount >= 16) and avoids arithmetic (still rejected on
// arm64 until step 7).
func TestArm64E2E_CALL(t *testing.T) {
	src := `
local function leaf() return 7 end
local function k()
  local a = 1
  local b = 2
  local c = 3
  local d = 4
  local e = 5
  local f = 6
  local g = a
  local h = b
  local p = c
  local q = d
  local r0 = e
  local s0 = f
  local v = leaf()
  local w = v
  local x = w
  local y = x
  return y
end
local r = 0
for i = 1, 200 do r = k() end
return r`
	results, promoted, dispatched := runForceAllArm64(t, src)
	if promoted == 0 {
		t.Fatal("PromotionCount = 0: CALL proto did not promote on arm64")
	}
	if dispatched == 0 {
		t.Fatal("DispatchHelperCount did not increase: CALL never rode the exit-reason protocol")
	}
	if len(results) != 1 || results[0] != "7" {
		t.Fatalf("results = %v, want [7]", results)
	}
}

// TestArm64E2E_CALL_ErrorBubbles: a callee that raises must bubble the
// error out of the dispatcher (dispatchHelper returns false → Run
// status 1 → public API error), byte-equal in existence to P1. Error
// paths are a structural blind spot of all-success corpora (see
// prove-the-path-under-test guide §error-path).
func TestArm64E2E_CALL_ErrorBubbles(t *testing.T) {
	src := `
local function boom() error("boom-arm64") end
local function k()
  local a = 1
  local b = 2
  local c = 3
  local d = 4
  local e = 5
  local f = 6
  local g = a
  local h = b
  local p = c
  local q = d
  local r0 = e
  local s0 = f
  local v = boom()
  return v
end
local r = k()
return r`
	prog, err := wangshu.Compile([]byte(src), "arm64e2e-err")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	_, err = prog.Run(st)
	if err == nil {
		t.Fatal("expected error from callee raise, got nil")
	}
	if !strings.Contains(err.Error(), "boom-arm64") {
		t.Fatalf("error %q does not carry the raise message", err)
	}
}
func TestArm64E2E_SETUPVAL(t *testing.T) {
	src := `
local x = 1
local y = 2
local function s()
  local a = x
  local b = y
  x = b
  y = a
  return x
end
local r = 0
for i = 1, 201 do r = s() end
return r, x, y`
	results, promoted, dispatched := runForceAllArm64(t, src)
	if promoted == 0 {
		t.Fatal("PromotionCount = 0: SETUPVAL proto did not promote on arm64")
	}
	if dispatched == 0 {
		t.Fatal("DispatchHelperCount did not increase: SETUPVAL never rode the exit-reason protocol")
	}
	// 201 swaps: x/y swapped an odd number of times → x=2, y=1, and the
	// last call returned the freshly-written x (= old y).
	if len(results) != 3 || results[0] != "2" || results[1] != "2" || results[2] != "1" {
		t.Fatalf("results = %v, want [2 2 1]", results)
	}
}

// TestArm64E2E_GETGLOBAL_SETGLOBAL: a kernel reading + writing warmed
// globals must promote (NodeHit IC gate) and produce correct values.
// The first passes of the outer loop run interpreted and warm the IC
// to NodeHit; force-all's retry window then re-checks and promotes.
func TestArm64E2E_GETGLOBAL_SETGLOBAL(t *testing.T) {
	src := `
G1 = 5
G2 = 0
local function k()
  local a = G1
  local b = G1
  G2 = a
  return b
end
local r = 0
for i = 1, 300 do r = k() end
return r, G2`
	results, promoted, dispatched := runForceAllArm64(t, src)
	if promoted == 0 {
		t.Fatal("PromotionCount = 0: GETGLOBAL/SETGLOBAL proto did not promote on arm64")
	}
	if len(results) != 2 || results[0] != "5" || results[1] != "5" {
		t.Fatalf("results = %v, want [5 5]", results)
	}
	// Inline-hit probe: with a warm NodeHit snapshot the ~299
	// post-promotion iterations × 3 global accesses (~900) must ride the
	// inline gen-check fast path, NOT the exit-reason round trip. If the
	// inline emit were silently broken (guards always missing), every
	// access would exit-reason and dispatched would be in the hundreds.
	// A loose < 100 bound tolerates warm-up and retry-window noise while
	// still distinguishing "inline path works" from "everything falls
	// back" (see prove-the-path-under-test §fast-path-hit blind spot).
	if dispatched >= 100 {
		t.Fatalf("dispatched = %d: inline NodeHit fast path never hits (all accesses ride exit-reason)", dispatched)
	}
}

// TestArm64E2E_SETGLOBAL_GenMissFallsBack: after the kernel promotes
// with a NodeHit snapshot, inserting new keys into _G bumps the table
// gen; the inline gen guard must miss and the exit-reason slow path
// must keep results byte-equal (no stale-slot write).
func TestArm64E2E_SETGLOBAL_GenMissFallsBack(t *testing.T) {
	src := `
GV = 1
local function k()
  local a = GV
  local b = GV
  GV = a
  return b
end
local r = 0
for i = 1, 300 do r = k() end
for i = 1, 40 do
  _G["fresh" .. i] = i
end
GV = 77
for i = 1, 50 do r = k() end
return r, GV`
	results, promoted, dispatched := runForceAllArm64(t, src)
	if promoted == 0 {
		t.Fatal("PromotionCount = 0: kernel did not promote")
	}
	if dispatched == 0 {
		t.Fatal("DispatchHelperCount did not increase: gen-miss slow path never rode exit-reason")
	}
	if len(results) != 2 || results[0] != "77" || results[1] != "77" {
		t.Fatalf("results = %v, want [77 77]", results)
	}
}

// TestArm64E2E_GETTABLE_SETTABLE_ArrayHit: a kernel iterating an array
// table (warm ArrayHit IC on both the read and the write site) must
// promote and produce values identical to the interpreter. The table
// arrives as a parameter (plain register) — an upvalue would insert a
// GETUPVAL exit-reason round trip per access and drown the fast-path
// probe. With a warm table the ~299 post-promotion iterations × 4
// accesses must ride the inline ArrayHit path, not exit-reason.
func TestArm64E2E_GETTABLE_SETTABLE_ArrayHit(t *testing.T) {
	src := `
local t = {10, 20, 30, 40}
local function k(tt)
  local a = tt[1]
  local b = tt[2]
  tt[3] = a
  local c = tt[3]
  return c
end
local r = 0
for i = 1, 300 do r = k(t) end
return r, t[3]`
	results, promoted, dispatched := runForceAllArm64(t, src)
	if promoted == 0 {
		t.Fatal("PromotionCount = 0: table kernel did not promote on arm64")
	}
	if len(results) != 2 || results[0] != "10" || results[1] != "10" {
		t.Fatalf("results = %v, want [10 10]", results)
	}
	if dispatched >= 100 {
		t.Fatalf("dispatched = %d: inline ArrayHit fast path never hits (all accesses ride exit-reason)", dispatched)
	}
}

// TestArm64E2E_NEWTABLE: a kernel allocating a fresh table per call
// rides the exit-reason NEWTABLE (allocation is host-side by design).
func TestArm64E2E_NEWTABLE(t *testing.T) {
	src := `
local seed = {5, 6, 7}
local function k()
  local n = {}
  n[1] = seed[1]
  n[2] = seed[2]
  local a = n[1]
  local b = n[2]
  return b
end
local r = 0
for i = 1, 300 do r = k() end
return r`
	results, promoted, dispatched := runForceAllArm64(t, src)
	if promoted == 0 {
		t.Fatal("PromotionCount = 0: NEWTABLE kernel did not promote on arm64")
	}
	if dispatched == 0 {
		t.Fatal("DispatchHelperCount did not increase: NEWTABLE never rode the exit-reason protocol")
	}
	if len(results) != 1 || results[0] != "6" {
		t.Fatalf("results = %v, want [6]", results)
	}
}

// TestArm64E2E_GETTABLE_MissFallsBack: after promotion with an
// ArrayHit snapshot, reading a slot that has become Nil (and an
// out-of-bounds index) must route through the exit-reason slow path
// and stay byte-equal (nil result / __index semantics preserved).
func TestArm64E2E_GETTABLE_MissFallsBack(t *testing.T) {
	src := `
local t = {1, 2, 3}
local idx = 2
local function k()
  local a = t[1]
  local b = t[idx]
  t[1] = a
  return b
end
local r = 0
for i = 1, 300 do r = k() end
idx = 7  -- out of bounds: live-asize check misses, helper returns nil
local r2 = k()
return r, tostring(r2)`
	results, promoted, _ := runForceAllArm64(t, src)
	if promoted == 0 {
		t.Fatal("PromotionCount = 0: kernel did not promote")
	}
	if len(results) != 2 || results[0] != "2" || results[1] != "nil" {
		t.Fatalf("results = %v, want [2 nil]", results)
	}
}

// TestArm64E2E_UNM: negation of a number rides the inline sign-flip;
// negation of a string ("5" coerces to -5 in Lua 5.1) must fall
// through the guard to the exit-reason slow path (host.Unm).
func TestArm64E2E_UNM(t *testing.T) {
	src := `
local n = 5
local s = "5"
local function k(x)
  local a = x
  local b = -a
  local c = b
  local d = c
  return d
end
local r = 0
for i = 1, 300 do r = k(n) end
local rs = k(s)
return r, rs`
	results, promoted, dispatched := runForceAllArm64(t, src)
	if promoted == 0 {
		t.Fatal("PromotionCount = 0: UNM kernel did not promote on arm64")
	}
	if len(results) != 2 || results[0] != "-5" || results[1] != "-5" {
		t.Fatalf("results = %v, want [-5 -5]", results)
	}
	// The 300 numeric calls must ride the inline sign-flip; only the
	// string-coercion call exits. A tight bound distinguishes "inline
	// path works" from "every UNM exits".
	if dispatched >= 100 {
		t.Fatalf("dispatched = %d: inline UNM fast path never hits", dispatched)
	}
	if dispatched == 0 {
		t.Fatal("dispatched = 0: string-coercion UNM never rode the exit-reason slow path")
	}
}
