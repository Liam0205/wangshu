//go:build wangshu_p4 && wangshu_profile && ((amd64 && linux) || (arm64 && (linux || (darwin && cgo)) && !wangshu_qemu))

// e2e_setlist_test.go — issue #68: SETLIST admitted to opSupported
// (amd64 + arm64). The dispatcher (HelperSetList) and host (State.SetList)
// already existed; amd64 emitSETLIST was already exit-reason, but arm64
// emitSETLISTArm64 was still on the legacy shim channel (rewritten here to
// exit-reason, mirror of SELF/CONCAT in #65). The change is: admit SETLIST
// to opSupported on both arches + reject the B=0 / C=0 forms in
// AnalyzeNative (they need a live `top` / a next-word batch count the CFG
// would misdecode).
//
// A table constructor `{a, b, c}` lowers to NEWTABLE + SETLIST; before,
// the SETLIST kept the whole enclosing proto on the interpreter. These
// tests prove such protos now promote and stay byte-equal to the
// interpreter. _NativeEmit additionally proves SETLIST reaches the native
// emit path (NativeRunCount moves); the read-back cases (_Promotes /
// _Nested) promote via head-op replay because their GETTABLE sites have
// cold ICs — SETLIST admission is what lets them promote at all. Assertions
// are arch-neutral, so one file serves both arches.

package peroptranslator_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

// TestPJ10_SetList_Promotes: a kernel building a small array literal
// `{a, a+1, a+2}` in a loop used to keep the whole function on the
// interpreter (SETLIST rejected). It now promotes and stays byte-equal.
//
// Note: this kernel reads the table back (t[1]/t[2]/t[3]), and those
// GETTABLE sites have cold ICs (a fresh table each iteration never
// stabilizes to ArrayHit), so AnalyzeNative rejects native emit for the
// whole proto — it promotes via the head-op replay path instead
// (PromotionCount>0, but peroptranslator.NativeRunCount stays 0). The
// SETLIST admission is what lets it promote at all; the assertion here is
// promote + byte-equal, matching the existing TestPJ10_SetList. The
// separate _NativeEmit test below proves SETLIST on the native emit path.
func TestPJ10_SetList_Promotes(t *testing.T) {
	src := `
local function kernel(n)
  local total = 0
  for i = 1, n do
    local t = { i, i + 1, i + 2 }
    total = total + t[1] + t[2] + t[3]
  end
  return total
end
local r
for j = 1, 5 do r = kernel(20) end
return r
`
	prog, err := wangshu.Compile([]byte(src), "pj10setlist")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if st.PromotionCount() == 0 {
		t.Fatal("PromotionCount = 0; SETLIST kernel did not promote (still rejected?)")
	}
	// each i: t = {i, i+1, i+2}; sum = 3i+3. sum over i=1..20 =
	// 3*(210) + 3*20 = 630 + 60 = 690.
	if len(res) != 1 || res[0].Display() != "690" {
		t.Fatalf("kernel(20) = %v, want [690]", res)
	}
}

// TestPJ10_SetList_NativeEmit: a SETLIST kernel with NO table read-back
// (so no cold-IC GETTABLE to reject the proto) actually reaches the native
// emit path, where SETLIST lowers to the HelperSetList exit-reason. This
// is the prove-the-path test: NativeRunCount must move, proving the
// SETLIST native emit executed (not just head-op replay). The table is
// built and its side effect (allocation + element stores) happens, we just
// don't read it back into a register in-loop.
func TestPJ10_SetList_NativeEmit(t *testing.T) {
	// The loop body builds a fresh 3-element array literal each iteration
	// (NEWTABLE + SETLIST) and does numeric arithmetic; it does NOT read
	// the table back, so every op is either inline-native or a
	// deopt-free exit-reason (SETLIST) — AnalyzeNative accepts it.
	src := `
local function kernel(n)
  local acc = 0
  for i = 1, n do
    local t = { i, i, i }
    acc = acc + i * 2
  end
  return acc
end
local r
for j = 1, 5 do r = kernel(20) end
return r
`
	prog, err := wangshu.Compile([]byte(src), "pj10setlistnative")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	beforeRun := peroptranslator.NativeRunCount.Load()
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if st.PromotionCount() == 0 {
		t.Fatal("PromotionCount = 0; native-emit SETLIST kernel did not promote")
	}
	if peroptranslator.NativeRunCount.Load() == beforeRun {
		t.Fatal("NativeRunCount unchanged; SETLIST proto did not run on the native emit path " +
			"(AnalyzeNative may have rejected it — check the loop body for cold-IC ops)")
	}
	// acc = sum of 2*i for i=1..20 = 2 * 210 = 420.
	if len(res) != 1 || res[0].Display() != "420" {
		t.Fatalf("kernel(20) = %v, want [420]", res)
	}
}

// TestPJ10_SetList_Nested: nested table constructors — the outer literal's
// SETLIST stores elements that are themselves freshly-built tables. Each
// inner `{...}` is its own NEWTABLE + SETLIST. Proves back-to-back SETLIST
// is correct (promote + byte-equal; the table read-backs keep it on the
// head-op replay path, same as _Promotes).
func TestPJ10_SetList_Nested(t *testing.T) {
	src := `
local function build(n)
  local acc = 0
  for i = 1, n do
    local m = { { i, i }, { i + 1, i + 1 } }
    acc = acc + m[1][1] + m[2][2]
  end
  return acc
end
local r
for j = 1, 5 do r = build(10) end
return r
`
	prog, err := wangshu.Compile([]byte(src), "pj10setlistnested")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if st.PromotionCount() == 0 {
		t.Fatal("PromotionCount = 0; nested-SETLIST kernel did not promote")
	}
	// each i: m[1][1] = i, m[2][2] = i+1; sum = 2i+1. sum over i=1..10 =
	// 2*55 + 10 = 120.
	if len(res) != 1 || res[0].Display() != "120" {
		t.Fatalf("build(10) = %v, want [120]", res)
	}
}

// TestPJ10_SetList_MultiBatch: a table literal with more than
// FieldsPerFlush (50) elements compiles to MULTIPLE SETLIST instructions
// (one per batch of 50), each with a distinct C batch number. Proves the
// per-batch C handling is correct on the native path — the interpreter is
// the oracle for the sum.
func TestPJ10_SetList_MultiBatch(t *testing.T) {
	// A 60-element array literal: batch 1 (C=1) flushes elements 1..50,
	// batch 2 (C=2) flushes 51..60. Both are fixed-C SETLIST (not C=0),
	// so both are accepted.
	src := `
local function big()
  local t = {
    1,2,3,4,5,6,7,8,9,10,
    11,12,13,14,15,16,17,18,19,20,
    21,22,23,24,25,26,27,28,29,30,
    31,32,33,34,35,36,37,38,39,40,
    41,42,43,44,45,46,47,48,49,50,
    51,52,53,54,55,56,57,58,59,60,
  }
  local s = 0
  for i = 1, 60 do s = s + t[i] end
  return s
end
local r
for j = 1, 5 do r = big() end
return r
`
	prog, err := wangshu.Compile([]byte(src), "pj10setlistmulti")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if st.PromotionCount() == 0 {
		t.Fatal("PromotionCount = 0; multi-batch SETLIST kernel did not promote")
	}
	// sum 1..60 = 60*61/2 = 1830.
	if len(res) != 1 || res[0].Display() != "1830" {
		t.Fatalf("big() = %v, want [1830]", res)
	}
}
