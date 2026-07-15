//go:build wangshu_p3 && wangshu_profile

// P3 cross-tier differential suite (docs/design/p3-wasm-tier/08-testing-strategy.md V1-V13 / V17-V18).
//
// Three-way differential test; must be **fully byte-equal** to pass:
//   - oracle      = official lua5.1 (the semantic baseline shared with difftest_test.go);
//   - crescent    = wangshu with force-all OFF (pure interpreter, cross-tier baseline);
//   - gibbous     = wangshu with force-all ON (every compilable Proto promoted to run on wazero).
//
// Runs only under the `wangshu_p3 && wangshu_profile` build: p3 supplies the real gibbous Compiler
// + adopts the wazero memory; profile enables OnEnter/OnBackEdge sampling (force-all triggers
// considerPromotion through it).
//
// **Promotion timing**: doCall's gibbous branch (call.go §VS0-d) jumps to wazero only when a Proto
// is **already promoted**; under force-all OnEnter triggers promotion at frame entry, so a Proto's
// **first** call still runs crescent (promotion happens after it enters the frame) and only **from
// the second call on** does it take gibbous. Each kernel is therefore designed to be called
// repeatedly (loop/recursion/multiple invokes) to ensure the gibbous path is truly covered.

package difftest

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// runWangshuTiered runs the script with wangshu; force controls whether to force full promotion
// (true = gibbous path). Reuses difftest's return-value Display comparison form (same as runWangshu).
func runWangshuTiered(t *testing.T, src string, force bool) string {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "p3diff")
	if err != nil {
		t.Fatalf("wangshu compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(force)
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("wangshu run (force=%v): %v", force, err)
	}
	parts := make([]string, len(results))
	for i, r := range results {
		parts[i] = r.Display()
	}
	return strings.Join(parts, "\t") + "\n"
}

// p3Corpus holds cross-tier cases for the V1-V13 shapes. Each kernel is **called repeatedly**
// (a loop body or multiple invokes) to guarantee the gibbous branch is actually reached after
// promotion (first call runs crescent, second call onward runs gibbous).
//
// Each kernel is wrapped as a **non-vararg inner function** and then called multiple times — the
// Lua main chunk is vararg (excluded by F1) and never promotes; what actually promotes is the
// repeatedly-called inner function (whose Proto passes F7 on force-all recheck).
var p3Corpus = []diffCase{
	// —— V1 straight-line / MOVE / LOADNIL —— (called repeatedly, gibbous from the second call)
	{"p3_straight_move", `
local function id(x) local y = x; return y end
local s = 0
for i = 1, 50 do s = s + id(i) end
return s`},
	{"p3_loadnil_multi", `
local function f(a, b, c) local x, y, z; x = a; return x + b + c end
local s = 0
for i = 1, 30 do s = s + f(i, i+1, i+2) end
return s`},

	// —— V2 arithmetic fast path + NaN canonicalization ——
	{"p3_arith_chain", `
local function calc(a, b) return a + b * 2 - b / 2 end
local s = 0
for i = 1, 40 do s = s + calc(i, i+1) end
return s`},
	{"p3_arith_mod_pow", `
local function f(x) return (x % 7) + (x * x) end
local s = 0
for i = 1, 40 do s = s + f(i) end
return s`},
	{"p3_unm_not", `
local function f(x) local n = -x; if not (n > 0) then return -n end return n end
local s = 0
for i = 1, 40 do s = s + f(i) end
return s`},
	// UNM of canonNaN (issue #107): -(0%0) sign-flips canonNaN into
	// value.Nil's bit pattern; the unguarded wasm fast path stored that
	// Nil and the next arithmetic op raised "attempt to perform
	// arithmetic on a nil value" from the second Run on (post-promotion).
	{"p3_unm_nan_alias", `
local function f() local a = 0%0 a = -a%1 return tostring(a) end
local s
for i = 1, 40 do s = f() end
return s`},

	// —— V3-V4 comparison + control flow (if/while/for/relooper) ——
	{"p3_compare_branch", `
local function clamp(x) if x < 10 then return 10 elseif x > 90 then return 90 end return x end
local s = 0
for i = 1, 100 do s = s + clamp(i) end
return s`},
	{"p3_while_inner", `
local function countdown(n) local c = 0; while n > 0 do c = c + n; n = n - 1 end return c end
local s = 0
for i = 1, 40 do s = s + countdown(i) end
return s`},
	{"p3_nested_for", `
local function grid(n) local c = 0; for a = 1, n do for b = 1, n do c = c + 1 end end return c end
local s = 0
for i = 1, 20 do s = s + grid(i) end
return s`},

	// —— V5 numeric-for accumulation (back-edge safepoint dense) ——
	{"p3_for_accumulate", `
local function sumto(n) local s = 0; for i = 1, n do s = s + i * 2 - 1 end return s end
local total = 0
for k = 1, 30 do total = total + sumto(k) end
return total`},
	{"p3_for_step", `
local function f(n) local s = 0; for i = n, 1, -2 do s = s + i end return s end
local s = 0
for i = 1, 40 do s = s + f(i) end
return s`},

	// —— V6-V7 table IC: GETTABLE/SETTABLE/GETGLOBAL/SETGLOBAL/SELF/NEWTABLE/SETLIST ——
	{"p3_table_array_ic", `
local function f(n) local t = {}; for i = 1, n do t[i] = i * i end local s = 0; for i = 1, n do s = s + t[i] end return s end
local s = 0
for i = 1, 30 do s = s + f(i) end
return s`},
	{"p3_table_hash_ic", `
local function f(x) local t = { a = x, b = x * 2 }; t.c = t.a + t.b; return t.c end
local s = 0
for i = 1, 40 do s = s + f(i) end
return s`},
	{"p3_newtable_list", `
local function f(x) local t = { x, x+1, x+2 }; return t[1] + t[2] + t[3] end
local s = 0
for i = 1, 40 do s = s + f(i) end
return s`},
	{"p3_self_method", `
local function f(x) local o = { v = x }; function o:get() return self.v end return o:get() + o:get() end
local s = 0
for i = 1, 40 do s = s + f(i) end
return s`},

	// —— V8 CALL three-way + base refresh (nested call / recursion) ——
	{"p3_recursive_fib", `
local function fib(n) if n < 2 then return n end return fib(n-1) + fib(n-2) end
return fib(18)`},
	{"p3_nested_call", `
local function inner(x) return x * 3 end
local function outer(x) return inner(x) + inner(x + 1) end
local s = 0
for i = 1, 40 do s = s + outer(i) end
return s`},

	// —— V9 TAILCALL ——
	{"p3_tail_loop", `
local function loop(n, acc) if n == 0 then return acc end return loop(n - 1, acc + n) end
local s = 0
for i = 1, 30 do s = s + loop(i, 0) end
return s`},

	// —— V10 closure / upvalue (CLOSURE/CLOSE) ——
	{"p3_closure_counter", `
local function make() local n = 0; return function() n = n + 1; return n end end
local s = 0
for i = 1, 40 do local c = make(); c(); c(); s = s + c() end
return s`},
	{"p3_upvalue_share", `
local function f(base) local acc = base; local function add(x) acc = acc + x end add(1); add(2); add(3); return acc end
local s = 0
for i = 1, 40 do s = s + f(i) end
return s`},

	// —— V11 TFORLOOP generic for (custom iterator, avoids the ipairs import) ——
	{"p3_tforloop_custom", `
local function iter(t, i) i = i + 1; if t[i] then return i, t[i] end end
local function f(n) local t = {}; for k = 1, n do t[k] = k * 10 end local s = 0; for _, v in iter, t, 0 do s = s + v end return s end
local s = 0
for i = 1, 25 do s = s + f(i) end
return s`},

	// —— V12 slow path (string coercion / metamethod / comparison) ——
	{"p3_string_coerce", `
local function f(x) return ("" .. x) .. "!" end
local out = ""
for i = 1, 10 do out = out .. f(i) end
return out`},
	{"p3_meta_index", `
local base = { v = 100 }
local function f(x) local t = setmetatable({ k = x }, { __index = base }); return t.k + t.v end
local s = 0
for i = 1, 40 do s = s + f(i) end
return s`},

	// —— V13 mixed kernel (arithmetic + table + call + control flow combined) ——
	{"p3_mixed_kernel", `
local function process(data, n)
  local sum, max = 0, 0
  for i = 1, n do
    local v = data[i] * 2 + 1
    sum = sum + v
    if v > max then max = v end
  end
  return sum + max
end
local function f(n) local d = {}; for i = 1, n do d[i] = i end return process(d, n) end
local s = 0
for i = 1, 25 do s = s + f(i) end
return s`},

	// -- issue #91: top-level if/else diamonds (not inside any loop) --
	// the relooper's block-scope nesting repair missed the symmetric
	// partial-overlap direction, so these shapes all CFAILed and stayed
	// on the interpreter. After the fix they must promote and stay
	// byte-equal (the promotion assertion lives in
	// TestP3_TopLevelDiamondPromotes).
	{"p3_iss91_diamond", `
local function k(a, b) local r = 0 if a < b then r = a else r = b end return r end
local s = 0
for i = 1, 50 do s = s + k(i, 25) end
return s`},
	{"p3_iss91_single_arm_if", `
local function k(a, b) local r = b if a < b then r = a end return r end
local s = 0
for i = 1, 50 do s = s + k(i, 25) end
return s`},
	{"p3_iss91_nested_diamond", `
local function k(a, b, c)
  local r = 0
  if a < b then
    if a < c then r = a else r = c end
  else
    if b < c then r = b else r = c end
  end
  return r
end
local s = 0
for i = 1, 50 do s = s + k(i, 25, 37) end
return s`},
	{"p3_iss91_diamond_then_loop", `
local function k(a, b, n)
  local r = 0
  if a < b then r = a else r = b end
  for i = 1, n do r = r + 1 end
  return r
end
local s = 0
for i = 1, 50 do s = s + k(i, 25, 3) end
return s`},
	{"p3_iss91_early_return", `
local function k(a, b) if a < b then return a end return b end
local s = 0
for i = 1, 50 do s = s + k(i, 25) end
return s`},
	{"p3_iss91_elseif_chain", `
local function k(x)
  if x < 3 then return 1
  elseif x < 6 then return 2
  elseif x < 9 then return 3
  else return 4 end
end
local s = 0
for i = 1, 12 do s = s + k(i) end
return s`},
}

// TestP3_Tiered three-way differential: oracle / crescent / gibbous all byte-equal (V1-V13).
func TestP3_Tiered(t *testing.T) {
	oracle := findOracle()
	for _, c := range p3Corpus {
		t.Run(c.name, func(t *testing.T) {
			crescent := runWangshuTiered(t, c.src, false)
			gibbous := runWangshuTiered(t, c.src, true)
			// Cross-tier hard gate: crescent vs gibbous must be byte-for-byte identical
			// (the core of the P3 correctness axis).
			if crescent != gibbous {
				t.Errorf("层间分歧 (crescent vs gibbous):\n  crescent: %q\n  gibbous:  %q", crescent, gibbous)
			}
			// Anchor against official lua5.1 (when available) — ensures both tiers are
			// correct, not wrong in lockstep.
			if oracle != "" {
				want := runOracle(t, oracle, wrapForOracle(c.src))
				if gibbous != want {
					t.Errorf("gibbous vs oracle byte-diff:\n  gibbous: %q\n  oracle:  %q", gibbous, want)
				}
			}
		})
	}
}

// TestP3_TopLevelDiamondPromotes — issue #91 prove-the-path: beyond
// byte-equality we must prove the top-level-diamond kernels actually
// PROMOTED. Before the fix these shapes CFAILed (improper scope
// overlap) and silently fell back to the interpreter, and the tier
// diff stayed green regardless — green alone doesn't prove the
// promoted path was under test, hence the PromotionCount white-box
// assertion.
func TestP3_TopLevelDiamondPromotes(t *testing.T) {
	srcs := map[string]string{
		"diamond": `
local function k(a, b) local r = 0 if a < b then r = a else r = b end return r end
local s = 0
for i = 1, 50 do s = s + k(i, 25) end
return s`,
		"single_arm_if": `
local function k(a, b) local r = b if a < b then r = a end return r end
local s = 0
for i = 1, 50 do s = s + k(i, 25) end
return s`,
		"early_return": `
local function k(a, b) if a < b then return a end return b end
local s = 0
for i = 1, 50 do s = s + k(i, 25) end
return s`,
	}
	for name, src := range srcs {
		t.Run(name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(src), "p3iss91")
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			st := wangshu.NewState(wangshu.Options{})
			st.SetForceAllPromote(true)
			if _, err := prog.Run(st); err != nil {
				t.Fatalf("run: %v", err)
			}
			if st.PromotionCount() == 0 {
				t.Fatal("PromotionCount = 0; top-level diamond kernel did not promote " +
					"(relooper scope overlap regression, issue #91)")
			}
		})
	}
}

// TestP3_TieredSeedCorpus reuses the 71 difftest seeds for a crescent vs gibbous cross-tier diff.
//
// Most seed kernels are direct expressions in the main chunk (vararg, no promotion), but the
// loops/recursion/closure subfunctions they contain do promote under force-all — this test covers
// the overall byte-equality of "mixed promoted and non-promoted execution" (V13).
func TestP3_TieredSeedCorpus(t *testing.T) {
	oracle := findOracle()
	for _, c := range seedCorpus {
		t.Run(c.name, func(t *testing.T) {
			crescent := runWangshuTiered(t, c.src, false)
			gibbous := runWangshuTiered(t, c.src, true)
			if crescent != gibbous {
				t.Errorf("层间分歧 (crescent vs gibbous):\n  crescent: %q\n  gibbous:  %q", crescent, gibbous)
			}
			if oracle != "" {
				want := runOracle(t, oracle, wrapForOracle(c.src))
				if gibbous != want {
					t.Errorf("gibbous vs oracle byte-diff:\n  gibbous: %q\n  oracle:  %q", gibbous, want)
				}
			}
		})
	}
}

// TestP3_GCStressTiered V5/V13: still byte-equal across tiers under GC stress mode.
//
// Under stressMode the gcPending flag stays 1 → the gibbous back edge still cross-tier calls
// h_safepoint every iteration → every safepoint forces a full Collect. Verifies that gibbous and
// crescent are byte-for-byte identical under the "promotion + high-frequency GC" combination
// (cross-validation of GC transparency × cross-tier consistency).
func TestP3_GCStressTiered(t *testing.T) {
	runStress := func(t *testing.T, src string, force bool) string {
		t.Helper()
		prog, err := wangshu.Compile([]byte(src), "p3stress")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		st := wangshu.NewState(wangshu.Options{})
		st.SetGCStressMode(true)
		st.SetForceAllPromote(force)
		results, err := prog.Run(st)
		if err != nil {
			t.Fatalf("run (force=%v): %v", force, err)
		}
		parts := make([]string, len(results))
		for i, r := range results {
			parts[i] = r.Display()
		}
		return strings.Join(parts, "\t") + "\n"
	}
	// Allocation-heavy kernels (NEWTABLE/SETLIST/closures allocating repeatedly, triggering GC).
	allocHeavy := []diffCase{
		{"stress_table_alloc", `
local function f(n) local t = {}; for i = 1, n do t[i] = { i, i * 2 } end local s = 0; for i = 1, n do s = s + t[i][1] + t[i][2] end return s end
local s = 0
for i = 1, 20 do s = s + f(i) end
return s`},
		{"stress_closure_alloc", `
local function make(x) return function() return x * 2 end end
local s = 0
for i = 1, 40 do local c = make(i); s = s + c() end
return s`},
		{"stress_string_concat", `
local function f(n) local out = ""; for i = 1, n do out = out .. tostring(i) end return #out end
local s = 0
for i = 1, 20 do s = s + f(i) end
return s`},
	}
	for _, c := range allocHeavy {
		t.Run(c.name, func(t *testing.T) {
			crescent := runStress(t, c.src, false)
			gibbous := runStress(t, c.src, true)
			if crescent != gibbous {
				t.Errorf("GC stress 层间分歧:\n  crescent: %q\n  gibbous:  %q", crescent, gibbous)
			}
		})
	}
}

// TestP3_ConcurrentForceAll V18 (-race): multiple States concurrently force-all-gibbous.
//
// Each goroutine runs the same script with its own State + its own force-all, verifying that
// concurrent promotion is data-race free (the gibbousCodes map is guarded by compileMu; the
// profileTable is State-private). Under `go test -race` any race is reported. Result consistency
// is checked as a byproduct.
func TestP3_ConcurrentForceAll(t *testing.T) {
	src := `
local function fib(n) if n < 2 then return n end return fib(n-1) + fib(n-2) end
local function sumto(n) local s = 0; for i = 1, n do s = s + i end return s end
return fib(15) + sumto(100)`
	const goroutines = 8
	prog, err := wangshu.Compile([]byte(src), "p3race")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// Run once first to get the expected value.
	want := runWangshuTiered(t, src, true)

	results := make([]string, goroutines)
	done := make(chan int, goroutines)
	for g := 0; g < goroutines; g++ {
		go func(idx int) {
			defer func() { done <- idx }()
			st := wangshu.NewState(wangshu.Options{})
			st.SetForceAllPromote(true)
			out, e := prog.Run(st)
			if e != nil {
				results[idx] = "ERR: " + e.Error()
				return
			}
			parts := make([]string, len(out))
			for i, r := range out {
				parts[i] = r.Display()
			}
			results[idx] = strings.Join(parts, "\t") + "\n"
		}(g)
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
	for g := 0; g < goroutines; g++ {
		if results[g] != want {
			t.Errorf("goroutine %d 结果分歧:\n  got:  %q\n  want: %q", g, results[g], want)
		}
	}
}
