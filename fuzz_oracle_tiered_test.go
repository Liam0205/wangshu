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
// interpreter twice. TestTieredOracleDiffActuallyPromotes below asserts promotion directly; see its own
// comment for why the obvious form of that assertion is too weak to be worth anything.
package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/oracle"
)

// runTieredSide mirrors runWangshuSide, except the State force-promotes every compilable Proto so the
// comparison exercises the tier rather than the interpreter. Returns promoted=false when the input produced
// no promotion beyond its own main chunk, which is informational only: most mutated inputs are too trivial
// to contain a nested function, and requiring per-input promotion would reject most of the corpus.
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
	// Strictly greater than before+1: force-all promotes the input's own main chunk, so ANY input -- even the
	// empty string -- moves this counter by one. Only a further increment is attributable to the payload.
	return verdict, output, errMsg, st.PromotionCount() > before+1
}

// FuzzOracleDiffTiered is FuzzOracleDiff's contract with a force-promoted State on the wangshu side.
//
// The skip set is deliberately IDENTICAL to the P1 target's: resource limits on either side, NUL bytes,
// nonfinite error() levels, and implementation-constant guards. Adding a tier-specific skip here would be
// the easy way to make this target green, and would also be the way to make it worthless -- a tier that
// diverges from PUC where P1 does not is precisely the finding this target exists to surface, so it must
// not be skippable.
// tieredKeepSet is the global set for the promotion probe. Prelude with an EMPTY GlobalSet trims away the
// libraries the prelude's own body uses and dies at prelude:972 on a nil `table` -- the trim is not a no-op on
// an empty set, which cost time in the #244 round too.
func tieredKeepSet() oracle.GlobalSet {
	return oracle.GlobalSet{
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
}

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
		// The same 4 KiB input cap the P1 target applies. Its absence here contradicted this file's own claim
		// that the skip set is identical, and an oversized input is not a tier finding -- both engines trip
		// their own syntax-nesting guards on it, at implementation-specific points.
		if len(src) > 4<<10 {
			t.Skip("input too large")
		}
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

// TestTieredOracleDiffActuallyPromotes proves the tiered harness reaches the tier WITH THE PAYLOAD.
//
// The obvious version of this assertion is worthless, and the first version I wrote was the obvious one.
// Under force-all, the input's own main chunk is a compilable Proto, so PromotionCount() grows by exactly one
// for ANY input -- measured: the empty string, whitespace, and `local x = 1` all move it 9 -> 10, while a
// payload holding one nested function reaches 11 and two reach 12. So `after > before` proves only that
// force-all is switched on, which is the one thing the surrounding code makes obvious anyway.
//
// The test therefore requires `after > before+1`, and it checks the discrimination itself: an empty payload
// must NOT satisfy the same bound. Without that second half the threshold is just a number I chose.
//
// The bound is also execution-side rather than compile-side, which is the question worth asking of any counter
// used this way. Measured: `local function never() return 1 end` -- a nested function that compiles but is
// never called -- stays at 9 -> 10, while calling it reaches 11. So passing this bound does require the
// payload to have RUN tiered code, not merely to have had it compiled.
func TestTieredOracleDiffActuallyPromotes(t *testing.T) {
	prelude := oracle.Prelude(tieredKeepSet())

	run := func(src string) (before, after int) {
		st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
		st.SetForceAllPromote(true)
		preProg, err := wangshu.Compile([]byte(prelude), "=prelude")
		if err != nil {
			t.Fatalf("prelude compile: %v", err)
		}
		if _, err := preProg.Run(st); err != nil {
			t.Fatalf("prelude run: %v", err)
		}
		before = st.PromotionCount()
		if _, _, errMsg := runFuzzInputOn(t, st, src); errMsg != "" {
			t.Fatalf("probe payload must run cleanly, got: %s", errMsg)
		}
		return before, st.PromotionCount()
	}

	// A payload with a nested function: force-all must promote it on top of the main chunk.
	b, a := run(`local function work(n) local s = 0 for i = 1, n do s = s + i % 5 end return s end
local r = work(64)
if r < 0 then error("unreachable") end`)
	if a <= b+1 {
		t.Fatalf("PromotionCount %d -> %d: only the main chunk promoted, so the payload never entered the "+
			"tier and FuzzOracleDiffTiered would silently duplicate FuzzOracleDiff", b, a)
	}
	t.Logf("payload promotion: %d -> %d", b, a)

	// The discrimination check: an empty payload must fall below the bound above. If it did not, the bound
	// would pass regardless of what the payload contains -- which is exactly how the first version failed.
	eb, ea := run("")
	if ea > eb+1 {
		t.Fatalf("empty payload promoted %d -> %d, above the bound: PromotionCount cannot distinguish a "+
			"payload that entered the tier from one that did not, so this guard proves nothing", eb, ea)
	}
	t.Logf("empty payload promotion: %d -> %d (below the bound, as required)", eb, ea)
}
