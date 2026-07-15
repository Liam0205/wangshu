//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"runtime"
	"runtime/debug"
	"sync"
	"testing"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_p4_r14_abi_test.go — post-fix verification for the PJ4/PJ5 mmap segment
// R14 ABI violation (follows PR #26 external review 5b28c8a + PUSH/POP R14 in
// trampoline_spec_amd64.s).
//
// External review warning: R14 is the Go amd64 ABIInternal g register. The PJ4 IC
// template + PJ5 SELF spec template byte-level emit loads the arena base into R14,
// so a RET at the segment tail directly corrupts the Go G; under production load,
// SEGV occurs when morestack/preemption/sync fetch g. The fix uses trampoline
// PUSH/POP R14 as relief: the segment transiently overwrites it, and the POP at the
// tail restores the Go G so subsequent Go runtime operations see the correct G value.
//
// This test **directly triggers** the Go runtime's g-fetch paths (morestack / GC /
// preemption) mixed with the spec template path, exposing any G corruption caused by
// R14 residue:
//   1. TestPJ4PJ5_R14ABI_GCStress: repeatedly run the spec template path + force GC,
//      verifying g is fetched correctly during GC mark/sweep (if R14 lingers, GC SEGVs)
//   2. TestPJ4PJ5_R14ABI_ConcurrentGC: run the spec template concurrently across
//      goroutines + GC stress, verifying g is correct when the Go runtime fetches all
//      g's during stop-the-world
//   3. TestPJ4PJ5_R14ABI_DeepStack: deep recursion triggering a morestack stack copy,
//      verifying g is fetched correctly during morestack

// TestPJ4PJ5_R14ABI_GCStress repeatedly runs the mmap segment + force GC + finalize,
// verifying the post-fix R14 relief succeeds (if R14 lingers, GC's g-fetch SEGVs).
func TestPJ4PJ5_R14ABI_GCStress(t *testing.T) {
	src := `
local o = { m = function(self) return 42 end }
local function caller(t) return t:m() end
local sum = 0
for i = 1, 100 do sum = sum + caller(o) end  -- warmup
sum = sum + caller(o)
return sum`
	st, mainCl := loadFnP4(t, src)

	// warmup phase 1 (populate IC NodeHit + FBSelfMono feedback)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)

	// alternate running the spec template + forced GC (50 rounds)
	for i := 0; i < 50; i++ {
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("iter %d run: %v", i, err)
		}
		if got := value.AsNumber(value.Value(rets[0])); got != 101*42 {
			t.Errorf("iter %d result = %v, want %d", i, got, 101*42)
		}
		// force GC + finalize (during GC mark/sweep all g's are fetched; if R14 lingers, SEGV)
		runtime.GC()
		debug.FreeOSMemory()
	}
}

// TestPJ4PJ5_R14ABI_ConcurrentGC runs the spec template concurrently across
// goroutines + GC stress, verifying g is correct when the Go runtime fetches all g's
// during stop-the-world.
//
// **Revision (important suggestions from PR #26 comments d8a5899..83f0b2e)**:
//   - callersWG separately tracks caller goroutine completion; the GC stresser runs
//     for the full duration of the callers, substantially widening the "concurrent GC +
//     stop-the-world g-fetch" overlap window
//   - t.Fatalf inside loadFnP4 is only safe on the main goroutine, so all States are
//     constructed on the main thread up front; goroutines only run st.Call (error-return path)
func TestPJ4PJ5_R14ABI_ConcurrentGC(t *testing.T) {
	const goroutines = 8
	const itersPerGoroutine = 30

	src := `
local mt = { m = function(self, k) return k+1 end }
local function caller(_, t, v) return t:m(v) end
local sum = 0
for i = 1, 50 do sum = sum + caller(nil, mt, i) end  -- warmup
sum = sum + caller(nil, mt, 100)
return sum`

	// precompile N States on the main thread (loadFnP4 uses t.Fatalf, only safe on the main goroutine)
	type stateBundle struct {
		st     *State
		mainCl arena.GCRef
	}
	bundles := make([]stateBundle, goroutines)
	for g := 0; g < goroutines; g++ {
		st, mainCl := loadFnP4(t, src)
		// warmup phase 1 + force-all (run sequentially on the main thread to avoid t.Fatalf inside a goroutine)
		if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
			t.Fatalf("bundle %d warmup: %v", g, err)
		}
		st.bridge.SetForceAllPromote(true)
		bundles[g] = stateBundle{st: st, mainCl: value.GCRefOf(mainCl)}
	}

	// callers wg + GC stresser as an independent goroutine
	var callersWG sync.WaitGroup
	callersWG.Add(goroutines)

	stopGC := make(chan struct{})
	var gcWG sync.WaitGroup
	gcWG.Add(1)
	go func() {
		defer gcWG.Done()
		for {
			select {
			case <-stopGC:
				return
			default:
				runtime.GC()
				runtime.Gosched()
			}
		}
	}()

	// callers: run the spec template concurrently
	for g := 0; g < goroutines; g++ {
		go func(idx int) {
			defer callersWG.Done()
			b := bundles[idx]
			for i := 0; i < itersPerGoroutine; i++ {
				if _, err := b.st.Call(b.mainCl, nil, 1); err != nil {
					t.Errorf("goroutine %d iter %d: %v", idx, i, err)
					return
				}
			}
		}(g)
	}

	// wait for all callers to finish, then stop the GC stresser (per the review revision:
	// GC stress running for the full caller duration exposes "concurrent GC + stop-the-world g-fetch")
	callersWG.Wait()
	close(stopGC)
	gcWG.Wait()
}

// TestPJ4PJ5_R14ABI_DeepStack triggers a morestack stack copy via deep recursion
// (20 levels), verifying g is fetched correctly during morestack.
//
// **morestack trigger condition**: a Go goroutine's default stack is 8KB; when a
// function frame is large enough, the stack runs short and triggers morestack to copy
// to a bigger stack; the morestack path uses r14=g to address g's fields. If the spec
// template segment leaves garbage in R14, morestack's g-fetch SEGVs / copies the wrong stack.
//
// This test triggers Go-side morestack via Lua recursive calls + a large-frame caller:
//   - the lua function `recurse(n)` self-calls 20 levels, each calling the `t:m()` spec template
//   - the 20-level Lua call stack + each level's host helper / executeFrom stack triggers Go morestack
func TestPJ4PJ5_R14ABI_DeepStack(t *testing.T) {
	src := `
local o = { m = function(self) return 1 end }
local function recurse(n)
  if n <= 0 then return 0 end
  return o:m() + recurse(n - 1)
end
local result = recurse(20)  -- 20 层递归 + 每层 SELF spec template
return result`
	st, mainCl := loadFnP4(t, src)

	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("deep recursion: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 20 {
		t.Errorf("recurse(20) = %v, want 20", got)
	}
}

// TestPJ4_R14ABI_GCStress_GetTable is the post-fix verification for the PJ4 GETTABLE
// IC ArrayHit/NodeHit segment R14 ABI: like the PJ5 SELF spec template path, the IC
// segment also loads the arena base into R14; verifies Go G correctness under GC + repeated runs.
func TestPJ4_R14ABI_GCStress_GetTable(t *testing.T) {
	src := `
local function f(t) return t[1] end
local t = {42, 43, 44}
local sum = 0
for i = 1, 100 do sum = sum + f(t) end  -- warmup
sum = sum + f(t)
return sum`
	st, mainCl := loadFnP4(t, src)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)

	for i := 0; i < 50; i++ {
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if got := value.AsNumber(value.Value(rets[0])); got != 101*42 {
			t.Errorf("iter %d result = %v, want %d", i, got, 101*42)
		}
		runtime.GC()
		debug.FreeOSMemory()
	}
}

// TestPJ4_R14ABI_GCStress_SetTable is the post-fix verification for the PJ4 SETTABLE
// IC ArrayHit/NodeHit segment R14 ABI: same check that the setter path leaves no R14
// residue + Go G stays correct.
func TestPJ4_R14ABI_GCStress_SetTable(t *testing.T) {
	src := `
local function setter(t, v) t["x"] = v end
local t = {x = 0}
for i = 1, 100 do setter(t, i) end  -- warmup
setter(t, 999)
return t.x`
	st, mainCl := loadFnP4(t, src)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)

	for i := 0; i < 50; i++ {
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if got := value.AsNumber(value.Value(rets[0])); got != 999 {
			t.Errorf("iter %d result = %v, want 999", i, got)
		}
		runtime.GC()
		debug.FreeOSMemory()
	}
}

// TestPJ3_R14ABI_GCStress_FORLOOP is the post-fix verification for the PJ3 FORLOOP
// byte-level inline segment R14 ABI: FORLOOP goes through the callJITFull main path
// (not the spec trampoline) but likewise loads the arena base into R14; verifies Go G
// correctness under GC + repeated runs.
//
// Follows §8 PJ3 FORLOOP 7-25x over gopher-lua + this batch's R14 fix with trampoline
// PUSH/POP R14 relief.
func TestPJ3_R14ABI_GCStress_FORLOOP(t *testing.T) {
	src := `
local function loop(n)
  local s = 0
  for i = 1, n do s = s + i end
  return s
end
local sum = 0
for i = 1, 100 do sum = sum + loop(50) end  -- warmup 100 + 50 = 100 次 inner
sum = sum + loop(50)
return sum`
	st, mainCl := loadFnP4(t, src)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)

	for i := 0; i < 50; i++ {
		_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		runtime.GC()
		debug.FreeOSMemory()
	}
}

// TestPJ7_R14ABI_GCStress_Arith is the post-fix verification for the PJ7 arithmetic
// inline segment R14 ABI: ADD..POW 6 ops + UNM/LEN/NOT go through the callJITFull main
// path; verifies R14 relief correctness.
func TestPJ7_R14ABI_GCStress_Arith(t *testing.T) {
	src := `
local function arith(x) return x * 2 + 1 end
local sum = 0
for i = 1, 100 do sum = sum + arith(i) end  -- warmup
sum = sum + arith(7)
return sum`
	st, mainCl := loadFnP4(t, src)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)

	for i := 0; i < 50; i++ {
		_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		runtime.GC()
		debug.FreeOSMemory()
	}
}
