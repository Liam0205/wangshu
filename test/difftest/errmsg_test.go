// Error message diff (12 §4 error-wording convention): the errmsg captured by
// pcall is diff-tested against the official 5.1.5.
//
// Diff rule: an errmsg carries a "chunkname:line:" prefix — the chunknames
// differ on the two sides (wangshu uses the chunkname argument, the oracle
// reads from stdin so it is "stdin"). Normalize by stripping the chunkname
// segment before the first ": ", then assert the line segment and the body
// text match separately.
package difftest

import (
	"regexp"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// errCase is one error case: the script is wrapped by pcall, return tostring(errmsg).
type errCase struct {
	name string
	expr string // faulting expression (executed inside a function body)
}

var errCorpus = []errCase{
	// arithmetic type errors
	{"arith_on_nil", `local x; return x + 1`},
	{"arith_on_string_bad", `return "zz" + 1`},
	{"arith_on_table", `return {} + 1`},
	{"arith_on_bool", `return true + 1`},
	{"unm_on_table", `return -({})`},
	// call errors
	{"call_nil", `local f; return f()`},
	{"call_number", `local f = 42; return f()`},
	{"call_string", `local f = "s"; return f()`},
	// index errors
	{"index_nil", `local t; return t.x`},
	{"index_number", `local t = 5; return t.x`},
	{"index_boolean", `local t = true; return t.x`},
	{"newindex_nil", `local t; t.x = 1`},
	// comparison errors
	{"compare_num_str", `return 1 < "x"`},
	{"compare_bool_bool", `return true < false`},
	{"compare_table_num", `return {} < 1`},
	// length errors
	{"len_on_number", `return #42`},
	{"len_on_nil", `local x; return #x`},
	{"len_on_bool", `return #true`},
	// concat errors
	{"concat_nil", `local x; return "a" .. x`},
	{"concat_table", `return "a" .. {}`},
	{"concat_bool", `return "a" .. true`},
	// for errors
	{"for_init_string", `for i = "x", 10 do end`},
	{"for_limit_table", `for i = 1, {} do end`},
	{"for_step_nil", `local s; for i = 1, 10, s do end`},
	// table index key errors
	{"table_index_nil_key", `local t = {}; local k; t[k] = 1`},
	{"table_index_nan_key", `local t = {}; t[0/0] = 1`},
	// upvalue name form (expr nests one more function so the outer local is
	// captured as an upvalue; the official getobjname takes the getupvalname
	// branch and reports "upvalue 'x'")
	{"arith_on_upvalue", `local up; local f = function() return up + 1 end; return f()`},
	{"call_upvalue", `local upf; local f = function() return upf() end; return f()`},
	{"index_upvalue", `local upt; local f = function() return upt.k end; return f()`},
	// luaL_argerror derives the function name from the [call site] (issue #133):
	// PUC getfuncname runs getobjname symbexec over the A operand of the call
	// instruction — a local alias reports the alias, a global reports the global
	// name, a method call does not count self (#N minus 1), a bad self itself
	// switches to "calling 'X' on bad self", a TFORLOOP site reports
	// "(for generator)", and a pure-C boundary (pcall direct call) falls back to '?'.
	{"argerr_global_field", `return string.rep(nil)`},
	{"argerr_local_alias", `local r = string.rep; return r(nil)`},
	{"argerr_method_decrement", `local s = "x"; return s:rep()`},
	{"argerr_bad_self", `local t = setmetatable({}, {__index = string}); return t:rep(1)`},
	{"argerr_for_generator", `for k in string.rep do end`},
	{"argerr_c_caller_fallback", `local _, e = pcall(string.rep); error(e, 0)`},
	{"argerr_cocreate_cfunction", `return coroutine.create(print)`},
}

// stripPos strips the "chunkname:line: " position prefix and returns
// (line segment, remaining message).
var posRe = regexp.MustCompile(`^[^:]+:(\d+): (.*)$`)

func stripPos(msg string) (string, string) {
	m := posRe.FindStringSubmatch(strings.TrimSpace(msg))
	if m == nil {
		return "", strings.TrimSpace(msg)
	}
	return m[1], m[2]
}

// TestDiff_ErrorMessages diff-tests error messages: byte-for-byte body plus
// the line segment is also asserted equal (the two sides share the same script
// structure so the line numbers should be equal; LineInfo misalignment bugs are
// caught by this guard).
func TestDiff_ErrorMessages(t *testing.T) {
	oracle := findOracle()
	if oracle == "" {
		t.Skip("lua5.1 oracle not found on PATH; skipping difftest")
	}
	for _, c := range errCorpus {
		t.Run(c.name, func(t *testing.T) {
			script := "local ok, err = pcall(function()\n" + c.expr + "\nend)\nreturn tostring(err)"
			// oracle
			oracleSrc := wrapForOracle(script)
			wantRaw := runOracle(t, oracle, oracleSrc)
			wantLine, want := stripPos(wantRaw)
			// wangshu
			prog, err := wangshu.Compile([]byte(script), "errdiff")
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			st := wangshu.NewState(wangshu.Options{})
			results, err := prog.Run(st)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			gotLine, got := stripPos(results[0].Display())
			if got != want {
				t.Errorf("error message diff:\n  wangshu: %q\n  oracle:  %q", got, want)
			}
			if gotLine != wantLine {
				t.Errorf("error line diff: wangshu %q vs oracle %q (msg %q)", gotLine, wantLine, want)
			}
		})
	}
}
