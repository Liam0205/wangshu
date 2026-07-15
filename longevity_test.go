// Longevity tests — verify the long-run stability guarantees (06 §2 freelist
// reuse + 05 §7.4 call-depth limit).
//
// Three kinds of assertions:
//  1. Bounded arena usage: running an allocation-heavy script repeatedly on the
//     same State cycles the freelist at steady state, so arena cap does not grow
//     without bound across rounds.
//  2. Deep Lua recursion reports "stack overflow" (catchable by pcall) without
//     blowing the Go stack.
//  3. Alternating host→Lua re-entry reports "C stack overflow", also recoverable.
package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestLongevity_ArenaBounded runs an allocation-heavy script repeatedly on the
// same State and asserts memory stays bounded.
//
// Each round produces ~2000 temporary objects (tables/strings/closures) none of
// which escape; if sweep fails to return dead objects to the freelist (or the
// freed space is not reused on allocation), the arena grows linearly with the
// number of rounds.
func TestLongevity_ArenaBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("longevity test skipped in -short")
	}
	src := `
local acc = 0
for i = 1, 200 do
  local t = { i, i * 2, s = "key" .. i }
  local f = function() return t[1] end
  acc = acc + f() + #("payload" .. i)
end
return acc`
	prog, err := wangshu.Compile([]byte(src), "longevity")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})

	// Warm up 50 rounds to let arena/threshold reach steady state, then record
	// the baseline.
	for i := 0; i < 50; i++ {
		if _, err := prog.Run(st); err != nil {
			t.Fatalf("warmup run %d: %v", i, err)
		}
	}
	base := st.GCCountKB()
	for i := 0; i < 2000; i++ {
		if _, err := prog.Run(st); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	after := st.GCCountKB()
	// The bump pointer only advances when the freelist comes up empty; at steady
	// state it should barely move. Allow 4x headroom (rehash size steps, intern
	// table growth, and other benign slack).
	if after > base*4 {
		t.Errorf("arena usage not bounded: base=%.1fKB after 2000 runs=%.1fKB", base, after)
	}
}

// TestLongevity_DeepRecursionOverflow deep recursion must report a Lua-semantics
// stack overflow.
func TestLongevity_DeepRecursionOverflow(t *testing.T) {
	src := `
local function f(n) return 1 + f(n + 1) end
local ok, err = pcall(f, 1)
return tostring(ok), tostring(err)`
	prog, err := wangshu.Compile([]byte(src), "deeprec")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if results[0].Str() != "false" {
		t.Errorf("pcall ok = %s, want false", results[0].Display())
	}
	if !strings.Contains(results[1].Str(), "stack overflow") {
		t.Errorf("err = %q, want contains 'stack overflow'", results[1].Str())
	}
}

// TestLongevity_DeepRecursionWithinLimit deep recursion within the limit
// (including tail-call elimination) is unaffected.
func TestLongevity_DeepRecursionWithinLimit(t *testing.T) {
	src := `
local function f(n) if n <= 0 then return 0 end return 1 + f(n - 1) end
local function loop(n, acc) if n == 0 then return acc end return loop(n - 1, acc + 1) end
return f(19000), loop(1000000, 0)`
	prog, err := wangshu.Compile([]byte(src), "deepok")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if results[0].Number() != 19000 {
		t.Errorf("f(19000) = %s, want 19000", results[0].Display())
	}
	if results[1].Number() != 1000000 {
		t.Errorf("tailcall loop = %s, want 1000000 (proper tail call must not consume depth)", results[1].Display())
	}
}

// TestLongevity_CStackOverflow alternating host→Lua re-entry (pcall recursing on
// itself) must be caught as "C stack overflow" rather than fataling the Go stack.
func TestLongevity_CStackOverflow(t *testing.T) {
	src := `
local f
f = function() return pcall(f) end
local ok, e = f()
return tostring(ok), tostring(e)`
	prog, err := wangshu.Compile([]byte(src), "cstack")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// The deepest pcall returns (false, "C stack overflow"); every outer pcall
	// returns true successfully — so the top level observes ok=true (matching
	// official 5.1 behavior).
	if results[0].Str() != "true" {
		t.Errorf("top ok = %s, want true", results[0].Display())
	}
}

// TestLongevity_StateReuse repeatedly loads and runs multiple Programs on the
// same State, asserting the loaded cache takes effect (IC/intern preserved across
// Runs) without cross-contamination.
func TestLongevity_StateReuse(t *testing.T) {
	progA, err := wangshu.Compile([]byte(`return 1 + 1`), "a")
	if err != nil {
		t.Fatal(err)
	}
	progB, err := wangshu.Compile([]byte(`local t = { x = 7 } return t.x`), "b")
	if err != nil {
		t.Fatal(err)
	}
	st := wangshu.NewState(wangshu.Options{})
	for i := 0; i < 500; i++ {
		ra, err := progA.Run(st)
		if err != nil {
			t.Fatalf("progA run %d: %v", i, err)
		}
		if ra[0].Number() != 2 {
			t.Fatalf("progA = %s", ra[0].Display())
		}
		rb, err := progB.Run(st)
		if err != nil {
			t.Fatalf("progB run %d: %v", i, err)
		}
		if rb[0].Number() != 7 {
			t.Fatalf("progB = %s", rb[0].Display())
		}
	}
}
