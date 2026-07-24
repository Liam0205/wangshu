//go:build wangshu_oracle_cgo && cgo

package oracle

import (
	"strings"
	"testing"
)

// testKeep is a minimal whitelist for shim-level tests (the real
// whitelist is enumerated from a live wangshu State by the fuzz
// target; here we only need the names the test scripts touch).
var testKeep = GlobalSet{
	Top: []string{
		"print", "tostring", "tonumber", "type", "pcall", "xpcall",
		"error", "select", "pairs", "ipairs", "next", "unpack",
		"setmetatable", "getmetatable", "rawget", "rawset", "rawequal",
		"loadstring", "load", "assert", "collectgarbage", "gcinfo",
		"string", "table", "math", "os", "io", "coroutine",
		"_G", "_VERSION",
	},
	Nested: map[string][]string{
		"string": {"len", "sub", "rep", "upper", "lower", "format",
			"byte", "char", "find", "match", "gmatch", "gsub", "reverse"},
		"table":     {"insert", "remove", "concat", "sort", "foreach"},
		"math":      {"floor", "ceil", "abs", "max", "min", "sqrt", "huge", "pi", "random", "randomseed"},
		"os":        {"time", "clock", "date", "getenv", "difftime"},
		"io":        {"write"},
		"coroutine": {"create", "resume", "yield", "status", "wrap", "running"},
	},
}

func execT(t *testing.T, src string) Result {
	t.Helper()
	return Exec(src, Prelude(testKeep), Limits{})
}

func TestExec_OutputCapture(t *testing.T) {
	r := execT(t, `print("hello", 42, nil, true) io.write("x", 1.5) print()`)
	if r.Verdict != VerdictOK {
		t.Fatalf("verdict = %v, err = %q", r.Verdict, r.Err)
	}
	want := "hello\t42\tnil\ttrue\nx1.5\n"
	if r.Output != want {
		t.Fatalf("output = %q, want %q", r.Output, want)
	}
}

func TestExec_NaNSpans(t *testing.T) {
	tests := []struct {
		src        string
		wantSpans  int
		wantSubstr string // substring the first span must cover
	}{
		{`print("BANANA")`, 0, ""},
		{`print(0/0)`, 1, "nan"},
		{`io.write(0/0)`, 1, "nan"},
		{`print(string.format("%E0", -(0/0)))`, 1, "NAN"},
		// string.format with fmt=NaN goes through tostring(NaN) and
		// records the resulting nan token exactly.
		{`print(string.format(0/0))`, 1, "nan"},
		// Reviewer-motivated coverage: same run mixes a real NaN with
		// script-emitted plain-text NaN spellings. The plain-text ones
		// must NOT be recorded, so downstream CompareOutput still fails
		// on script-side literal-string differences.
		{`print(0/0, "BANANA")`, 1, "nan"},
		// Reviewer-motivated (round 2): a formatted string that is
		// stored to a local and emitted LATER must still record spans
		// pointing at the final output, not at __len at format time.
		{`local s = string.format("%E", 0/0) print("prefix", s)`, 1, "NAN"},
		// Reviewer-motivated (round 2): two string.format calls
		// evaluated back-to-back in the SAME print argument list must
		// produce two disjoint spans in submission order (previous
		// design put both at the same __len -> non-monotonic -> Limit).
		{`print(string.format("%E0", -(0/0)), string.format("0%E", -(0/0)))`, 2, "NAN"},
	}
	for _, tt := range tests {
		r := execT(t, tt.src)
		if r.Verdict != VerdictOK {
			t.Fatalf("%q: verdict = %v, err = %q", tt.src, r.Verdict, r.Err)
		}
		if len(r.NaNSpans) != tt.wantSpans {
			t.Errorf("%q: got %d spans (%v), want %d, output = %q",
				tt.src, len(r.NaNSpans), r.NaNSpans, tt.wantSpans, r.Output)
			continue
		}
		if tt.wantSpans == 0 {
			continue
		}
		got := r.Output[r.NaNSpans[0].Start:r.NaNSpans[0].End]
		if !strings.Contains(strings.ToLower(got), strings.ToLower(tt.wantSubstr)) {
			t.Errorf("%q: first span = %q, want it to contain %q (output=%q)",
				tt.src, got, tt.wantSubstr, r.Output)
		}
	}
}

// TestExec_NaNSpansKnownLimit documents the value-collision limit of
// the string.format provenance mechanism: Lua 5.1 has no primitive
// distinguishing a format() result from a same-value script literal
// (interned strings share identity), so a FIFO keyed on value can be
// consumed early by an earlier literal emit sharing the same bytes.
// Downstream CompareOutput sees the spans in different positions and
// reports OutputDifferent -- a false negative for the known-diff
// classifier that only bites when a script both emits a literal
// matching the format result's exact byte sequence AND does so before
// the format result itself. The narrow-window failure mode is
// intentional: expanding the provenance to always tolerate value
// collisions would swallow genuine script-side literal divergence,
// violating issue #173's "everything outside the NaN spelling
// fragment stays byte-equal" contract.
//
// The exact NaN spelling PUC prints depends on the host libc + CPU
// (this test runs against the vendored 5.1.5 built for the host), so
// we do not pin the concrete output bytes -- we probe both signed and
// unsigned NaN literal candidates and require ONE OF them exercise
// the collision, then assert the single-span-on-literal outcome the
// mechanism produces in that scenario.
func TestExec_NaNSpansKnownLimit(t *testing.T) {
	// Reviewer-authored case (round 2 of Codex review on #181):
	// PUC's %E on -(0/0) or 0/0 prints some spelling of NaN
	// depending on host libc/CPU. Whichever spelling comes out, if
	// the script emits that same literal via a separate print call
	// BEFORE emitting the format result, the FIFO provenance for s
	// is consumed by the literal emit rather than by print(s).
	candidates := []struct {
		src, lit string
	}{
		// spell = script literal; both signs to cover libcs that
		// emit "NAN" and libcs that emit "-NAN" for -(0/0)/0/0.
		{`local s = string.format("%E", -(0/0)) print("NAN") print(s)`, "NAN"},
		{`local s = string.format("%E", -(0/0)) print("-NAN") print(s)`, "-NAN"},
		{`local s = string.format("%E", 0/0) print("NAN") print(s)`, "NAN"},
		{`local s = string.format("%E", 0/0) print("-NAN") print(s)`, "-NAN"},
	}
	for _, c := range candidates {
		r := execT(t, c.src)
		if r.Verdict != VerdictOK {
			t.Fatalf("%q: verdict = %v, err = %q", c.src, r.Verdict, r.Err)
		}
		// Only the case where the script literal EQUALS the format
		// result exercises the collision. Other cases correctly
		// record two spans (one on the literal, one on s -- both
		// numeric-NaN? no, only format's; literal is a plain string
		// with no evidence). Skip non-collision cases.
		want := c.lit + "\n" + c.lit + "\n"
		if r.Output != want {
			continue
		}
		// Collision path fired: the FIFO consumed the format
		// provenance early, leaving a single span on the first
		// print's literal instead of on print(s).
		if len(r.NaNSpans) != 1 {
			t.Fatalf("collision %q: got %d spans (%v), want 1", c.src, len(r.NaNSpans), r.NaNSpans)
		}
		spanBytes := r.Output[r.NaNSpans[0].Start:r.NaNSpans[0].End]
		if spanBytes != c.lit {
			t.Errorf("collision %q: span content = %q, want %q (literal consumed the provenance)", c.src, spanBytes, c.lit)
		}
		return
	}
	t.Skip("this host libc/CPU produced no NaN spelling collision candidate; known-limit case is inherently platform-shaped, skipping")
}

func TestExec_ErrorVerdictKeepsPartialOutput(t *testing.T) {
	r := execT(t, `print("before") error("boom")`)
	if r.Verdict != VerdictError {
		t.Fatalf("verdict = %v", r.Verdict)
	}
	if r.Output != "before\n" {
		t.Fatalf("output = %q", r.Output)
	}
	if !strings.Contains(r.Err, "boom") {
		t.Fatalf("err = %q", r.Err)
	}
}

func TestExec_CompileErrorVerdict(t *testing.T) {
	r := execT(t, `return 1 +`)
	if r.Verdict != VerdictError {
		t.Fatalf("verdict = %v", r.Verdict)
	}
	if !strings.Contains(r.Err, "fuzz") {
		t.Fatalf("err should carry the fuzz chunkname, got %q", r.Err)
	}
}

func TestExec_InstructionBudget(t *testing.T) {
	r := Exec(`while true do end`, Prelude(testKeep), Limits{Budget: 100000})
	if r.Verdict != VerdictLimit {
		t.Fatalf("verdict = %v, err = %q", r.Verdict, r.Err)
	}
	if !strings.Contains(r.Err, LimitSentinel) {
		t.Fatalf("err = %q", r.Err)
	}
}

func TestExec_BudgetInsideCoroutine(t *testing.T) {
	// lua_newthread copies the hookmask: the budget must bind inside
	// resume, and the refilter prelude must re-raise it out of the
	// pcall-like resume so it cannot be swallowed.
	r := Exec(`local co = coroutine.create(function() while true do end end)
coroutine.resume(co)
print("survived")`, Prelude(testKeep), Limits{Budget: 100000})
	if r.Verdict != VerdictLimit {
		t.Fatalf("verdict = %v, err = %q, out = %q", r.Verdict, r.Err, r.Output)
	}
}

func TestExec_BudgetNotSwallowedByPcall(t *testing.T) {
	r := Exec(`pcall(function() while true do end end) print("survived")`,
		Prelude(testKeep), Limits{Budget: 100000})
	if r.Verdict != VerdictLimit {
		t.Fatalf("verdict = %v, err = %q, out = %q", r.Verdict, r.Err, r.Output)
	}
}

func TestExec_AllocCap(t *testing.T) {
	r := Exec(`local s = "aaaaaaaaaaaaaaaa"
for i = 1, 40 do s = s .. s end
print(#s)`, Prelude(testKeep), Limits{MaxAllocBytes: 8 << 20})
	if r.Verdict != VerdictLimit {
		t.Fatalf("verdict = %v, err = %q, out = %q", r.Verdict, r.Err, r.Output)
	}
}

func TestExec_OutputCap(t *testing.T) {
	r := execT(t, `local s = string.rep("a", 4096)
for i = 1, 1000 do io.write(s) end`)
	if r.Verdict != VerdictLimit {
		t.Fatalf("verdict = %v, err = %q", r.Verdict, r.Err)
	}
	if !strings.Contains(r.Err, LimitSentinel) {
		t.Fatalf("err = %q", r.Err)
	}
}

func TestExec_FreshStatePerExec(t *testing.T) {
	r1 := execT(t, `G_LEAK = 42 print("set")`)
	if r1.Verdict != VerdictOK {
		t.Fatalf("first exec: %v / %q", r1.Verdict, r1.Err)
	}
	r2 := execT(t, `print(tostring(G_LEAK))`)
	if r2.Verdict != VerdictOK {
		t.Fatalf("second exec: %v / %q", r2.Verdict, r2.Err)
	}
	if r2.Output != "nil\n" {
		t.Fatalf("state leaked across Exec: %q", r2.Output)
	}
}

func TestExec_WhitelistTrim(t *testing.T) {
	// os.execute / io.open / require / string.dump are PUC abilities
	// wangshu doesn't have; the trim must erase them.
	r := execT(t, `print(tostring(os.execute), tostring(io.open), tostring(require), tostring(string.dump), tostring(debug))`)
	if r.Verdict != VerdictOK {
		t.Fatalf("verdict = %v, err = %q", r.Verdict, r.Err)
	}
	if r.Output != "nil\tnil\tnil\tnil\tnil\n" {
		t.Fatalf("ability surface survived the trim: %q", r.Output)
	}
}

func TestExec_StringMetatableTrimmed(t *testing.T) {
	// ("x"):dump-style access goes through the string metatable's
	// __index = string table; in-place trim must cover it.
	r := execT(t, `print(tostring(("x").dump))`)
	if r.Verdict != VerdictOK {
		t.Fatalf("verdict = %v, err = %q", r.Verdict, r.Err)
	}
	if r.Output != "nil\n" {
		t.Fatalf("string metatable kept trimmed key: %q", r.Output)
	}
}

func TestExec_SortedPairs(t *testing.T) {
	r := execT(t, `local t = { b = 2, a = 1, [3] = "x", [1] = "y" }
for k, v in pairs(t) do io.write(tostring(k), "=", tostring(v), ";") end`)
	if r.Verdict != VerdictOK {
		t.Fatalf("verdict = %v, err = %q", r.Verdict, r.Err)
	}
	want := "1=y;3=x;a=1;b=2;"
	if r.Output != want {
		t.Fatalf("output = %q, want %q", r.Output, want)
	}
}

func TestExec_BinaryChunkRejected(t *testing.T) {
	r := execT(t, `local f, e = loadstring("\27Lua nonsense")
print(tostring(f), tostring(e))`)
	if r.Verdict != VerdictOK {
		t.Fatalf("verdict = %v, err = %q", r.Verdict, r.Err)
	}
	if !strings.Contains(r.Output, "binary chunk rejected") {
		t.Fatalf("output = %q", r.Output)
	}
}

func TestExec_NastyInputsDontCrash(t *testing.T) {
	// A grab bag of hostile shapes; the invariant is "no process
	// death", any verdict is fine.
	nasty := []string{
		"",
		"\x00\x01\x02",
		"\x1bLua\x51\x00\x01\x04\x08\x04\x08\x00", // bytecode header
		strings.Repeat("(", 10000),
		strings.Repeat("do ", 5000),
		`return ("x"):rep(1e9)`,
		`local t = {} t[0/0] = 1`,
		`error(setmetatable({}, {__tostring = error}))`,
		`while true do coroutine.yield() end`,
		`string.find(string.rep("a", 200), "(a*)*b")`,
		`local co co = coroutine.wrap(function() co() end) co()`,
	}
	for i, src := range nasty {
		r := execT(t, src)
		t.Logf("nasty[%d]: verdict=%v err=%.60q", i, r.Verdict, r.Err)
	}
}

func TestExec_ManyExecsNoLeak(t *testing.T) {
	// Smoke-level leak check: the capped allocator accounts net bytes
	// per state, and each Exec closes its state. 200 iterations at
	// 8 MiB cap would OOM the harness only if states leaked.
	for i := 0; i < 200; i++ {
		r := Exec(`local t = {} for i = 1, 1000 do t[i] = tostring(i) end print(#t)`,
			Prelude(testKeep), Limits{MaxAllocBytes: 8 << 20})
		if r.Verdict != VerdictOK {
			t.Fatalf("iter %d: verdict = %v, err = %q", i, r.Verdict, r.Err)
		}
	}
}
