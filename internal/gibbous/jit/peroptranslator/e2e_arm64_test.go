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
