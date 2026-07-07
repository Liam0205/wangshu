//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

// auto_test.go — AUTO-mode tier differential suite (issue: production
// runs auto, CI ran force-all only).
//
// Every P3/P4 differential suite in this package drives promotion with
// SetForceAllPromote(true). That leaves the natural-heat half of the
// bridge unexercised: threshold-crossing MID-RUN (a proto promotes
// between two calls inside one script run), the runtime compilability
// recheck on the natural path, the PromotionGater profitability gate,
// and the short-proto floor + FloorExempter — all of which are
// auto-only machinery that forceAll bypasses or never consults
// (issue #67 was exactly such an auto-only bug). The un-forced tests
// in difftest_test.go do NOT cover this either: their scripts never
// reach the production thresholds (entry 200 / back edge 1000), so
// "auto" there degenerates to the pure interpreter.
//
// Harness: SetHotThresholds lowers the thresholds (entry=3, backEdge=8)
// so small kernels cross them mid-run. The override changes only WHEN
// the auto decision runs — the decision chain itself (recheck, gater,
// floor, exemption) runs unchanged. Three-way comparison per case:
//
//   - baseline  = same build, thresholds raised to MaxUint32 (promotion
//     unreachable -> guaranteed interpreter path on a P3/P4 build);
//   - auto      = lowered thresholds, one State, the SAME program Run
//     repeatedly — every run must byte-equal the baseline, including
//     the runs during which promotion flips mid-run;
//   - oracle    = official lua5.1 (when on PATH), anchoring both.
//
// Prove-the-path: after the auto runs, PromotionCount() must be > 0 —
// otherwise the suite silently degenerated to interpreter-vs-
// interpreter (see prove-the-path-under-test).

package difftest

import (
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// autoRuns is how many times each case is Run on the auto State. With
// entry=3 the hot kernels promote during the first or second run, so
// later runs execute tier-mixed (promoted callee, interpreted main).
const autoRuns = 4

// runAutoOnce runs prog once on st and returns the Display-joined
// results (same shape as runWangshu).
func runAutoOnce(t *testing.T, prog *wangshu.Program, st *wangshu.State, label string) string {
	t.Helper()
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("wangshu run (%s): %v", label, err)
	}
	parts := make([]string, len(results))
	for i, r := range results {
		parts[i] = r.Display()
	}
	return strings.Join(parts, "\t") + "\n"
}

// autoCorpus: kernels whose call counts / loop trip counts cross the
// LOWERED thresholds (entry 3, back edge 8) but stay far below the
// production ones — exercising mid-run promotion on shapes the P3/P4
// backends actually accept (arith, compare, FORLOOP, table IC, SELF,
// closures), plus shapes the acceptance gates DECLINE (vararg, deep
// metamethods) to cover the promote-some-decline-some mix.
var autoCorpus = []diffCase{
	// Long pure-arithmetic kernels (>= 10 opcodes, zero helper-bound
	// ops): the shapes BOTH backends' auto gates accept — P3's
	// WorthPromoting density gate short-circuits on zero helper-bound
	// ops, and the length clears the short-proto floor. These are the
	// cases that guarantee PromotionCount advances on every build.
	{"auto_arith_long_kernel", `
local function f(x, y)
  local a = x * 2 + y
  local b = a - x / 2
  local c = b * b + a
  local d = c - y * 3
  return d + a - b + c
end
local s = 0
for i = 1, 30 do s = s + f(i, i + 1) end
return s`},
	{"auto_floatloop_kernel", `
local function accum(n)
  local s = 0.5
  for i = 1, n do
    s = s + i * 1.5 - (s / 16)
  end
  return s
end
local total = 0
for i = 1, 12 do total = total + accum(10 + i) end
return total`},
	{"auto_arith_kernel", `
local function f(x, y) return x * 2 + y end
local s = 0
for i = 1, 30 do s = s + f(i, i + 1) end
return s`},
	{"auto_compare_kernel", `
local function le5(x) return x <= 5 end
local n = 0
for i = 1, 20 do if le5(i) then n = n + 1 end end
return n`},
	{"auto_forloop_kernel", `
local function sumto(n) local s = 0; for i = 1, n do s = s + i end return s end
local total = 0
for i = 1, 12 do total = total + sumto(i * 3) end
return total`},
	{"auto_table_ic_kernel", `
local t = {x = 1, y = 2}
local function get(tt) return tt.x + tt.y end
local s = 0
for i = 1, 25 do t.x = i; s = s + get(t) end
return s`},
	{"auto_array_ic_kernel", `
local a = {10, 20, 30}
local function pick(arr, i) return arr[i] end
local s = 0
for i = 1, 24 do s = s + pick(a, (i % 3) + 1) end
return s`},
	{"auto_self_kernel", `
local obj = {n = 0}
function obj:bump(x) self.n = self.n + x; return self.n end
local function drive(o) return o:bump(2) end
local last = 0
for i = 1, 20 do last = drive(obj) end
return last`},
	{"auto_closure_upval_kernel", `
local function make()
  local c = 0
  return function() c = c + 1; return c end
end
local f = make()
local s = 0
for i = 1, 20 do s = s + f() end
return s`},
	{"auto_recursive_fib", `
local function fib(n) if n < 2 then return n end return fib(n-1) + fib(n-2) end
return fib(12)`},
	{"auto_tiny_below_floor", `
local function inc(x) return x + 1 end
local s = 0
for i = 1, 30 do s = s + inc(i) end
return s`},
	{"auto_mixed_decline", `
local function hot(x) return x * x end
local function varargish(...) local a, b = ...; return (a or 0) + (b or 0) end
local s = 0
for i = 1, 20 do s = s + hot(i) + varargish(i, 1) end
return s`},
	{"auto_string_concat_kernel", `
local function tag(i) return "v" .. i end
local out = ""
for i = 1, 12 do out = out .. tag(i) end
return out`},
	{"auto_gc_alloc_kernel", `
local function pair(i) return { i, i * 2 } end
local s = 0
for i = 1, 20 do local p = pair(i); s = s + p[1] + p[2] end
return s`},
}

// newAutoState builds a State with thresholds lowered so autoCorpus
// kernels cross them mid-run. Entry 3: a kernel promotes on its 3rd
// call. BackEdge 8: a loop promotes on its 8th back edge.
func newAutoState() *wangshu.State {
	st := wangshu.NewState(wangshu.Options{})
	st.SetHotThresholds(3, 8)
	return st
}

// newInterpBaselineState builds a State whose thresholds are
// unreachable — the guaranteed interpreter path on this same build.
func newInterpBaselineState() *wangshu.State {
	st := wangshu.NewState(wangshu.Options{})
	st.SetHotThresholds(math.MaxUint32, math.MaxUint32)
	return st
}

// TestAuto_Tiered: per case, every auto run (including the ones where
// promotion flips mid-run) must byte-equal the interpreter baseline
// and the oracle. PromotionCount must advance across the corpus.
func TestAuto_Tiered(t *testing.T) {
	oracle := findOracle()
	promotedTotal := 0
	for _, c := range autoCorpus {
		t.Run(c.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(c.src), "autodiff")
			if err != nil {
				t.Fatalf("wangshu compile: %v", err)
			}
			// Run-for-run baseline: globals persist across Run on one
			// State, so run N is only comparable to baseline run N
			// (lesson from FuzzAutoPromote seed 861f54880d2009d5).
			baseSt := newInterpBaselineState()
			st := newAutoState()
			for run := 1; run <= autoRuns; run++ {
				baseline := runAutoOnce(t, prog, baseSt, "baseline")
				if run == 1 && oracle != "" {
					want := runOracle(t, oracle, wrapForOracle(c.src))
					if baseline != want {
						t.Errorf("baseline vs oracle byte-diff:\n  baseline: %q\n  oracle:   %q",
							baseline, want)
					}
				}
				got := runAutoOnce(t, prog, st, "auto")
				if got != baseline {
					t.Errorf("auto run %d diverged from interpreter baseline:\n  auto:     %q\n  baseline: %q",
						run, got, baseline)
				}
			}
			promotedTotal += st.PromotionCount()
		})
	}
	// Prove-the-path: if nothing promoted anywhere, the whole suite
	// silently tested interpreter-vs-interpreter.
	if promotedTotal == 0 {
		t.Error("PromotionCount stayed 0 across the auto corpus — no natural-heat promotion happened; the suite is not exercising the auto path")
	}
}

// TestAuto_SeedCorpus reuses the shared difftest seed corpus under
// lowered thresholds: whatever inner functions are hot enough promote,
// everything else stays interpreted — the promoted/declined mix must
// not change any output byte across repeated runs.
func TestAuto_SeedCorpus(t *testing.T) {
	for _, c := range seedCorpus {
		t.Run(c.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(c.src), "autoseed")
			if err != nil {
				t.Fatalf("wangshu compile: %v", err)
			}
			baseSt := newInterpBaselineState()
			st := newAutoState()
			for run := 1; run <= autoRuns; run++ {
				baseline := runAutoOnce(t, prog, baseSt, "baseline")
				got := runAutoOnce(t, prog, st, "auto")
				if got != baseline {
					t.Errorf("auto run %d diverged from interpreter baseline:\n  auto:     %q\n  baseline: %q",
						run, got, baseline)
				}
			}
		})
	}
}

// TestAuto_RandomScripts: generator-driven auto-vs-baseline diff
// (auto counterpart of TestDiff_RandomScripts; no oracle needed — the
// same-build interpreter baseline is the reference). Defaults to 300
// deterministic seeds for the PR gate; nightly can scale via the same
// WANGSHU_FUZZ_SEED_BASE / WANGSHU_FUZZ_N environment knobs.
func TestAuto_RandomScripts(t *testing.T) {
	base := int64(0)
	n := int64(300)
	if v := os.Getenv("WANGSHU_FUZZ_SEED_BASE"); v != "" {
		if p, err := strconv.ParseInt(v, 10, 64); err == nil {
			base = p
		}
	}
	if v := os.Getenv("WANGSHU_FUZZ_N"); v != "" {
		if p, err := strconv.ParseInt(v, 10, 64); err == nil {
			n = p
		}
	}
	for seed := base; seed < base+n; seed++ {
		src := generateScript(seed)
		prog, err := wangshu.Compile([]byte(src), "autorand")
		if err != nil {
			t.Fatalf("seed %d compile: %v", seed, err)
		}
		baseSt := newInterpBaselineState()
		st := newAutoState()
		for run := 1; run <= 2; run++ {
			baseline := runAutoOnce(t, prog, baseSt, "baseline")
			got := runAutoOnce(t, prog, st, "auto")
			if got != baseline {
				// DIVERGENCE line format matches TestDiff_RandomScripts
				// (nightly triage greps this marker).
				t.Errorf("DIVERGENCE seed=%d kind=auto-tier run=%d\n  auto:     %q\n  baseline: %q\n--- script ---\n%s",
					seed, run, got, baseline, src)
			}
		}
	}
}

// TestAuto_GCStressTiered: lowered thresholds + GC stress mode — the
// promotion flip must stay byte-transparent under a full Collect at
// every safepoint (auto counterpart of TestP3_GCStressTiered).
func TestAuto_GCStressTiered(t *testing.T) {
	allocHeavy := []diffCase{
		{"auto_stress_table_alloc", `
local function f(n) local t = {}; for i = 1, n do t[i] = { i, i * 2 } end local s = 0; for i = 1, n do s = s + t[i][1] + t[i][2] end return s end
local s = 0
for i = 1, 20 do s = s + f(i) end
return s`},
		{"auto_stress_closure_alloc", `
local function make(x) return function() return x * 2 end end
local s = 0
for i = 1, 40 do local c = make(i); s = s + c() end
return s`},
	}
	for _, c := range allocHeavy {
		t.Run(c.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(c.src), "autostress")
			if err != nil {
				t.Fatalf("wangshu compile: %v", err)
			}
			baseState := newInterpBaselineState()
			baseState.SetGCStressMode(true)
			st := newAutoState()
			st.SetGCStressMode(true)
			for run := 1; run <= autoRuns; run++ {
				baseline := runAutoOnce(t, prog, baseState, "baseline")
				got := runAutoOnce(t, prog, st, "auto+gcstress")
				if got != baseline {
					t.Errorf("auto+gcstress run %d diverged:\n  auto:     %q\n  baseline: %q",
						run, got, baseline)
				}
			}
		})
	}
}
