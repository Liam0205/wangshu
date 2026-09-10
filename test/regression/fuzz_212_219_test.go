package regression

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu/test/testutil"
)

// TestErrorLevelMustBeANumber covers #212 and #215: error()'s level argument goes through
// luaL_optint, which RAISES for a present non-number rather than falling back to the default.
//
// error("", 0>0) reported an empty message where lua5.1 reports
// "bad argument #2 to 'error' (number expected, got boolean)". Only an absent or nil level takes
// the default, which is why the nil case is pinned alongside.
func TestErrorLevelMustBeANumber(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		// Raised from inside a Lua function, so the argument error itself carries a position
		// prefix; called directly through pcall it does not, which the next case pins.
		{"boolean level raises",
			`local ok, e = pcall(function() error("", 0>0) end) return tostring(e)`,
			`[string "test"]:1: bad argument #2 to 'error' (number expected, got boolean)`},
		{"table level raises",
			`local ok, e = pcall(error, "m", {}) return tostring(e)`,
			"bad argument #2 to '?' (number expected, got table)"},
		// Absent and explicit nil both take the default level of 1, which prefixes the position.
		{"absent level prefixes",
			`local ok, e = pcall(function() error("m") end) return tostring(e)`,
			`[string "test"]:1: m`},
		{"nil level prefixes",
			`local ok, e = pcall(function() error("m", nil) end) return tostring(e)`,
			`[string "test"]:1: m`},
		// A numeric STRING is accepted, as luaL_optint coerces it.
		{"numeric string level accepted",
			`local ok, e = pcall(function() error("m", "2") end) return tostring(e)`, "m"},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestCallLineIsTheArgumentList covers #214: a call is reported at the line where its ARGUMENT LIST
// begins, not where the callee expression starts.
//
//	(0
//	)()
//
// reports line 2 in lua5.1 -- the line of the "()" -- while wangshu used the callee's line and said
// 1. Only a multi-line callee expression differs, which is why it went unnoticed: on a single line
// the two coincide.
func TestCallLineIsTheArgumentList(t *testing.T) {
	for _, tc := range []struct{ name, src, wantLine string }{
		{"newline before the call", "local ok, e = pcall(function() return (0\n)() end) return tostring(e)", "2"},
		{"single line", "local ok, e = pcall(function() return (0)() end) return tostring(e)", "1"},
		{"several newlines", "local ok, e = pcall(function() return (\n\n0\n)() end) return tostring(e)", "4"},
		{"method call", "local t = {} local ok, e = pcall(function() return t\n:nope() end) return tostring(e)", "2"},
		// The CALLEE keeps its own line; only the CALL moves. An audit caught the first version of
		// this fix moving both, which put the callee's GETTABLE on the argument line -- so indexing
		// a nil in `t.x\n{1}` blamed line 4 instead of 3. PUC materializes the callee in
		// primaryexp and only funcargs' luaK_fixline moves the CALL.
		//
		// The {} and string argument forms are the ones that expose it: `(` is immune because the
		// ambiguous-syntax rule forbids a newline before it.
		{"callee line with a table arg",
			"local ok, e = pcall(function()\nlocal t = nil\nreturn t.x\n{1} end) return tostring(e)", "3"},
		{"callee line with a string arg",
			"local ok, e = pcall(function()\nlocal t = nil\nreturn t.x\n\"s\" end) return tostring(e)", "3"},
		// Method calls needed the same split: SELF keeps the method-name line, CALL takes the
		// argument list's. The single-line case above cannot see this, since the two coincide there.
		{"method callee line with a table arg",
			"local t = {}\nlocal ok, e = pcall(function() return t:nope\n{} end) return tostring(e)", "3"},
	} {
		full := testutil.RunOne(t, tc.src).Str()
		want := `[string "test"]:` + tc.wantLine + ":"
		if !strings.HasPrefix(full, want) {
			t.Errorf("%s: got %q, want prefix %q", tc.name, full, want)
		}
	}
}

// TestGsubValidatesReplTypeUpFront covers #216: gsub checks its replacement's TYPE before the
// substitution loop, as PUC does with luaL_argcheck on tr.
//
// Validating lazily inside the loop meant gsub("", "", nil, 0) SUCCEEDED -- with a count of zero the
// loop never ran, so the bad argument was never seen -- while lua5.1 raises
// "bad argument #3 (string/function/table expected)".
func TestGsubValidatesReplTypeUpFront(t *testing.T) {
	const want = "bad argument #3 to '?' (string/function/table expected)"
	for _, tc := range []struct{ name, src string }{
		// The fourth argument is what made this reachable: it drives the loop count to zero.
		{"nil repl with a zero count", `local ok, e = pcall(string.gsub, "", "", nil, .0) return tostring(e)`},
		{"nil repl without a count", `local ok, e = pcall(string.gsub, "", "", nil) return tostring(e)`},
		{"boolean repl", `local ok, e = pcall(string.gsub, "", "", true) return tostring(e)`},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != want {
			t.Errorf("%s: got %q, want %q", tc.name, got, want)
		}
	}
	// Every valid replacement kind must still work.
	for _, tc := range []struct{ name, src, want string }{
		{"string repl", `return ("aaa"):gsub("a", "b")`, "bbb"},
		{"function repl", `return ("aaa"):gsub("a", function(c) return c:upper() end)`, "AAA"},
		{"table repl", `return ("aaa"):gsub("a", {a = "Z"})`, "ZZZ"},
		{"number repl", `return ("aaa"):gsub("a", 7)`, "777"},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestMathRandomTypeErrorBeforeInterval covers a sibling of #212's shape that I found by sweeping
// for the same pattern rather than waiting for the fuzzer.
//
// PUC's math_random runs luaL_checkint BEFORE luaL_argcheck, so a non-number bound is a TYPE error
// at its own argument index -- not an empty interval. Folding the two conditions into one
// `if !ok || m < 1` reported "interval is empty" for math.random({}), where lua5.1 reports
// "number expected, got table".
//
// This is the same defect shape as error()'s level: a failed conversion being absorbed into an
// unrelated outcome instead of raising.
func TestMathRandomTypeErrorBeforeInterval(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"one bad bound", `local ok, e = pcall(math.random, {}) return tostring(e)`,
			"bad argument #1 to '?' (number expected, got table)"},
		{"bad lower bound", `local ok, e = pcall(math.random, {}, 1) return tostring(e)`,
			"bad argument #1 to '?' (number expected, got table)"},
		{"bad upper bound", `local ok, e = pcall(math.random, 1, {}) return tostring(e)`,
			"bad argument #2 to '?' (number expected, got table)"},
		// A NUMERIC bound that is out of range is still an interval error, at PUC's indices.
		{"empty interval one arg", `local ok, e = pcall(math.random, 0) return tostring(e)`,
			"bad argument #1 to '?' (interval is empty)"},
		{"empty interval two args", `local ok, e = pcall(math.random, 5, 1) return tostring(e)`,
			"bad argument #2 to '?' (interval is empty)"},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestMultiLineArgumentTokenLines covers a pre-existing defect an audit surfaced while checking the
// #214 fix: a token's END line, not its start, is what the parser must use.
//
// PUC reads ls->linenumber AFTER scanning a token, so a long string spanning newlines puts the CALL
// on its LAST line; using the start line reported a long-string argument one line early. The same
// start-vs-end mistake in the ambiguous-syntax check REJECTED a call whose argument is a long string
// containing a newline, which lua5.1 accepts -- a parse error on a valid program, worse than a wrong
// line number.
//
// Every expectation here was read off lua5.1 5.1.5 rather than reasoned about; the audit's own
// numbers for two of these were off by one, which is why.
func TestMultiLineArgumentTokenLines(t *testing.T) {
	for _, tc := range []struct{ name, src, wantLine string }{
		{"long string argument",
			"local ok, e = pcall(function() return A[[\n]] end) return tostring(e)", "2"},
		{"method with a long string argument",
			"local t = {}\nlocal ok, e = pcall(function() return t:m[[\n]] end) return tostring(e)", "3"},
		{"short string with an escaped newline",
			"local ok, e = pcall(function() return A\"a\\\nb\" end) return tostring(e)", "2"},
	} {
		full := testutil.RunOne(t, tc.src).Str()
		want := "[string \"test\"]:" + tc.wantLine + ":"
		if !strings.HasPrefix(full, want) {
			t.Errorf("%s: got %q, want prefix %q", tc.name, full, want)
		}
	}
	// A call whose argument is a long string containing a newline is VALID: the ambiguous-syntax rule
	// compares against the previous token's END line, so there is no line break before the call as
	// far as the rule is concerned.
	src := "local f = function() return function() return 1 end end\nreturn tostring(f[[\n]](3))"
	if got := testutil.RunOne(t, src).Str(); got != "1" {
		t.Errorf("a long-string argument call = %q, want \"1\" -- lua5.1 accepts it", got)
	}
}
