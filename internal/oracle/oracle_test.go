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

func TestExec_NaNSignEvidence(t *testing.T) {
	tests := []struct {
		src  string
		want bool
	}{
		{`print("BANANA")`, false},
		{`print(0/0)`, true},
		{`io.write(0/0)`, true},
		{`print(string.format("%E0", -(0/0)))`, true},
		{`print(string.format(0/0))`, true},
	}
	for _, tt := range tests {
		r := execT(t, tt.src)
		if r.Verdict != VerdictOK {
			t.Fatalf("%q: verdict = %v, err = %q", tt.src, r.Verdict, r.Err)
		}
		if r.KnownNaNSign != tt.want {
			t.Errorf("%q: KnownNaNSign = %v, want %v, output = %q", tt.src, r.KnownNaNSign, tt.want, r.Output)
		}
	}
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
