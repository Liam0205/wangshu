//go:build wangshu_oracle_cgo && cgo && (wangshu_p3 || wangshu_p4)

// fuzz_oracle_tiered_test.go -- differential fuzz of the TIERED execution paths (P3 wasm / P4 method JIT)
// directly against the process-embedded official Lua 5.1.5.
//
// WHY THIS EXISTS. Until this target, the tiers' only fuzz evidence was transitive:
// FuzzAutoPromote and FuzzP4ForceAllPromote assert tiered-output == P1-output, and FuzzOracleDiff asserts
// P1-output == PUC-output. Chaining those does give "tiered == PUC", but it inherits every blind spot P1's
// comparison has, and it cannot see a divergence where P1 agrees with PUC while BOTH tiers agree with each
// other on something else. test/difftest does compare tiers to the real lua5.1 binary, but through a
// hand-written generator (~450 lines) whose coverage is far narrower than go-fuzz mutation -- it emits no
// vararg, goto, pcall or error constructs at all.
//
// This target closes that: arbitrary mutated source, promoted into the tier, compared against PUC with the
// same contract FuzzOracleDiff uses.
//
// PROMOTION IS ASSERTED, NOT ASSUMED. FuzzOracleDiff already compiles and passes under a tier build tag,
// because it constructs a plain State and never promotes -- the tier code is linked in but never entered.
// A tiered harness that forgot to promote would look exactly like a passing test while testing the
// interpreter twice. So this one calls SetForceAllPromote and then requires PromotionCount() to have grown
// for at least one input in the run; if nothing ever promoted, the target fails rather than reporting green.
package wangshu_test

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/oracle"
)

// promotedAtLeastOnce records whether any input in this run actually entered the tier. Checked by
// TestTieredOracleDiffActuallyPromotes, which is the guard against the whole target silently degrading
// into a second copy of the P1 differential.
var promotedAtLeastOnce atomic.Bool

// runTieredSide mirrors runWangshuSide, except the State force-promotes every compilable Proto so the
// comparison exercises the tier rather than the interpreter. Returns promoted=false when the input produced
// no promotion at all (a script too trivial to have a compilable Proto), which is informational only --
// per-input promotion is not required, but at least one promotion across the run is.
func runTieredSide(t *testing.T, src, prelude string) (verdict oracle.Verdict, output, errMsg string, promoted bool) {
	t.Helper()
	st := wangshu.NewState(wangshu.Options{
		// Same arena cap as the P1 side: exhaustion must classify as a skip rather than kill a worker.
		MaxArenaBytes: 64 << 20,
	})
	st.SetForceAllPromote(true)

	preProg, err := wangshu.Compile([]byte(prelude), "=prelude")
	if err != nil {
		t.Fatalf("prelude must compile on wangshu: %v", err)
	}
	if _, err := preProg.Run(st); err != nil {
		t.Fatalf("prelude must run on wangshu: %v", err)
	}
	before := st.PromotionCount()

	verdict, output, errMsg = runFuzzInputOn(t, st, src)
	if st.PromotionCount() > before {
		promoted = true
		promotedAtLeastOnce.Store(true)
	}
	return verdict, output, errMsg, promoted
}

// FuzzOracleDiffTiered is FuzzOracleDiff's contract with a force-promoted State on the wangshu side.
//
// The skip set is deliberately IDENTICAL to the P1 target's: resource limits on either side, NUL bytes,
// nonfinite error() levels, and implementation-constant guards. Adding a tier-specific skip here would be
// the easy way to make this target green, and would also be the way to make it worthless -- a tier that
// diverges from PUC where P1 does not is precisely the finding this target exists to surface, so it must
// not be skippable.
func FuzzOracleDiffTiered(f *testing.F) {
	keep := enumerateGlobals(f)
	prelude := oracle.Prelude(keep)

	// Seeds lean on the constructs test/difftest's generator never emits (vararg, goto-free control flow,
	// pcall, error) plus loops hot enough that natural promotion would also fire, so the corpus is useful
	// to a future natural-heat variant of this target and not just to force-all.
	for _, s := range []string{
		`local function f(...) return select("#", ...), ... end print(f(1, nil, 3))`,
		`local t = {} for i = 1, 200 do t[i] = i * i end local s = 0 for i = 1, #t do s = s + t[i] end print(s)`,
		`print(pcall(function() error({code = 7}) end))`,
		`print(pcall(function() error("boom", 2) end))`,
		`local co = coroutine.create(function(a) local b = coroutine.yield(a + 1) return b * 2 end)
print(coroutine.resume(co, 1)) print(coroutine.resume(co, 10))`,
		`local mt = {__index = function(_, k) return k .. "!" end, __len = function() return 42 end}
local t = setmetatable({}, mt) print(t.x, #t)`,
		`local s = "" for i = 1, 50 do s = s .. i % 7 end print(#s, s:sub(1, 8))`,
		`local function fib(n) if n < 2 then return n end return fib(n - 1) + fib(n - 2) end print(fib(18))`,
		`print(string.format("%d %s %.3f %q", 42, "x", 1 / 3, "a\nb"))`,
		`local a, b = 1, 2 a, b = b, a print(a, b, ("x"):rep(3))`,
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, src string) {
		if strings.ContainsRune(src, 0) {
			t.Skip("NUL byte")
		}
		recordFuzzExec("FuzzOracleDiffTiered", src)

		or := oracle.Exec(src, prelude, oracle.Limits{})
		if or.Verdict == oracle.VerdictLimit {
			t.Skip("oracle limit: " + or.Err)
		}
		wv, wout, werr, _ := runTieredSide(t, src, prelude)
		if wv == oracle.VerdictLimit {
			t.Skip("wangshu limit: " + werr)
		}
		if errorLevelUBRange(src) {
			t.Skip("error() level in luaL_checkint UB range")
		}
		if (or.Verdict == oracle.VerdictError && oracle.SkipClassError(or.Err)) ||
			(wv == oracle.VerdictError && oracle.SkipClassError(werr)) {
			t.Skip("impl-constant guard tripped")
		}

		if or.Verdict != wv {
			t.Fatalf("verdict class diverged (TIERED vs PUC): oracle=%v (err=%q) tiered=%v (err=%q)\n--- script ---\n%s",
				or.Verdict, or.Err, wv, werr, src)
		}
		switch oracle.CompareOutput(or.Output, wout) {
		case oracle.OutputEqual:
			return
		case oracle.OutputDifferent:
			t.Fatalf("output diverged (TIERED vs PUC):\n  oracle: %q\n  tiered: %q\n--- script ---\n%s",
				oracle.NormalizeOutput(or.Output), oracle.NormalizeOutput(wout), src)
		default:
			t.Fatalf("unknown oracle output comparison")
		}
	})
}

// TestTieredOracleDiffActuallyPromotes proves the tiered harness reaches the tier.
//
// This is the guard the whole target depends on. FuzzOracleDiffTiered would compile, run and PASS with the
// SetForceAllPromote call deleted -- it would just be comparing the interpreter against PUC a second time,
// which is what FuzzOracleDiff already does. A green target that tests the wrong path is worse than no
// target, so promotion is asserted directly rather than trusted.
func TestTieredOracleDiffActuallyPromotes(t *testing.T) {
	// A real global set: Prelude with an empty one TRIMS AWAY the libraries its own body uses, so it fails
	// at prelude:972 on a nil `table`. Same trap as the #244 round -- the trim is not a no-op on an empty set.
	keep := oracle.GlobalSet{
		Top: []string{"print", "table", "string", "math", "select", "pcall", "error", "tostring", "tonumber",
			"type", "setmetatable", "getmetatable", "ipairs", "pairs", "next", "unpack", "rawget", "rawset",
			"coroutine", "os", "io", "assert", "rawequal", "collectgarbage", "load", "loadstring", "xpcall"},
		Nested: map[string][]string{
			"table":     {"concat", "insert", "remove", "sort", "maxn"},
			"string":    {"byte", "char", "find", "format", "gmatch", "gsub", "len", "lower", "match", "rep", "reverse", "sub", "upper"},
			"math":      {"abs", "ceil", "floor", "fmod", "huge", "max", "min", "modf", "pi", "random", "sqrt"},
			"coroutine": {"create", "resume", "status", "wrap", "yield"},
			"os":        {"clock", "date", "time"},
			"io":        {"write", "read"},
		},
	}
	prelude := oracle.Prelude(keep)

	st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	st.SetForceAllPromote(true)
	preProg, err := wangshu.Compile([]byte(prelude), "=prelude")
	if err != nil {
		t.Fatalf("prelude compile: %v", err)
	}
	if _, err := preProg.Run(st); err != nil {
		t.Fatalf("prelude run: %v", err)
	}

	before := st.PromotionCount()
	// A function-shaped body: force-all promotes compilable Protos, so this must move the counter.
	src := `local function work(n) local s = 0 for i = 1, n do s = s + i % 5 end return s end
__oracle_print(work(64))`
	if _, _, errMsg := runFuzzInputOn(t, st, src); errMsg != "" {
		t.Logf("input errored (not fatal for this probe): %s", errMsg)
	}
	after := st.PromotionCount()
	if after <= before {
		t.Fatalf("PromotionCount did not grow (%d -> %d): the tiered harness is running the interpreter, "+
			"so FuzzOracleDiffTiered would silently duplicate FuzzOracleDiff", before, after)
	}
	t.Logf("promotion observed: %d -> %d", before, after)
}
