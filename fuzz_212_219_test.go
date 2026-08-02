package wangshu_test

import (
	"strings"
	"testing"
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
		if got := runOne(t, tc.src).Str(); got != tc.want {
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
		// Method calls already used the method-name line, which matches; pinned so it stays that way.
		{"method call", "local t = {} local ok, e = pcall(function() return t\n:nope() end) return tostring(e)", "2"},
	} {
		full := runOne(t, tc.src).Str()
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
		if got := runOne(t, tc.src).Str(); got != want {
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
		if got := runOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
