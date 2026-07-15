// Package difftest implements the cross-implementation byte-equal harness:
// Wangshu vs official Lua 5.1.5 (the M14 minimal-viable version of 12 §3).
//
// Current coverage: output diffing of a fixed script set (seed corpus). The
// random script generator (real fuzzing) is wired in later (12 §3.2 generator).
// Skipped when lua5.1 is absent (see engineering.md for how CI provisions the
// oracle; run scripts/check-oracle.sh before `make difftest` locally).
package difftest

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// findOracle returns the path to the lua5.1 executable; empty if not found.
func findOracle() string {
	for _, name := range []string{"lua5.1", "lua51"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// runOracle runs the script with official lua5.1 and returns stdout.
//
// A non-zero exit of the oracle process on a script = "the official impl
// rejects/errors while this impl may accept" -- that is one kind of real
// divergence (not INFRA); emit a DIVERGENCE marker so nightly triage
// classifies it correctly (only an unavailable oracle counts as INFRA, which
// findOracle guards).
func runOracle(t *testing.T, oracle, src string) string {
	t.Helper()
	cmd := exec.Command(oracle, "-")
	cmd.Stdin = strings.NewReader(src)
	var out bytes.Buffer
	cmd.Stdout = &out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("DIVERGENCE kind=oracle-error\noracle run: %v\nstderr: %s\n--- script ---\n%s",
			err, stderr.String(), src)
	}
	return out.String()
}

// runWangshu runs the script with Wangshu, capturing print output.
//
// M14 simplification: print output goes straight to the Go process stdout and
// cannot be captured, so difftest diffs on the `return` value rather than
// stdout -- scripts are uniformly shaped as `return tostring(expr)`, and on the
// oracle side a wrapper prints the returned value.
func runWangshu(t *testing.T, src string) string {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "difftest")
	if err != nil {
		t.Fatalf("wangshu compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("wangshu run: %v", err)
	}
	parts := make([]string, len(results))
	for i, r := range results {
		parts[i] = r.Display()
	}
	return strings.Join(parts, "\t") + "\n"
}

// diffCase is one diffing case: the script must end with a return (single or
// multiple values).
type diffCase struct {
	name string
	src  string
}

// seedCorpus is the first batch of fixed scripts (12 §2 idea: one case per
// semantic corner).
var seedCorpus = []diffCase{
	{"arith_basic", `return 1 + 2 * 3 - 4 / 2`},
	{"arith_mod", `return 7 % 3`},
	{"arith_mod_neg", `return -7 % 3`},
	{"arith_pow", `return 2 ^ 10`},
	{"arith_div_float", `return 7 / 2`},
	{"concat_str_num", `return "v=" .. 42`},
	{"compare_lt", `return tostring(1 < 2)`},
	{"compare_le", `return tostring(2 <= 2)`},
	{"compare_strings", `return tostring("abc" < "abd")`},
	{"logic_and_or", `return (nil and 1) or "fallback"`},
	{"numeric_for", `
local s = 0
for i = 1, 100 do s = s + i end
return s`},
	{"numeric_for_step", `
local s = 0
for i = 10, 1, -2 do s = s + i end
return s`},
	{"while_loop", `
local i, n = 0, 0
while i < 50 do i = i + 1; n = n + i end
return n`},
	{"repeat_until", `
local i = 0
repeat i = i + 1 until i >= 5
return i`},
	{"recursive_fib", `
local function fib(n)
  if n < 2 then return n end
  return fib(n-1) + fib(n-2)
end
return fib(15)`},
	{"tail_call", `
local function loop(n, acc)
  if n == 0 then return acc end
  return loop(n - 1, acc + n)
end
return loop(100, 0)`},
	{"closure_counter", `
local function counter()
  local n = 0
  return function() n = n + 1; return n end
end
local c = counter()
c(); c()
return c()`},
	{"table_array", `
local t = { 10, 20, 30 }
return t[1] + t[2] + t[3]`},
	{"table_hash", `
local t = { x = 1, y = 2 }
t.z = t.x + t.y
return t.z`},
	{"string_len", `return #"hello"`},
	{"string_lib", `return string.upper("abc") .. string.sub("hello", 2, 4)`},
	{"math_lib", `return math.max(3, 7) + math.min(2, 5) + math.abs(-4)`},
	{"tostring_num", `return tostring(3.14)`},
	{"tostring_int", `return tostring(100)`},
	{"tonumber_str", `return tonumber("42") + 1`},
	{"multi_assign", `
local a, b, c = 1, 2
return tostring(a) .. tostring(b) .. tostring(c)`},
	{"vararg_pass", `
local function f(...)
  local a, b = ...
  return a + b
end
return f(3, 4)`},
	{"metatable_index", `
local base = { v = 7 }
local t = setmetatable({}, { __index = base })
return t.v`},
	{"pcall_ok", `
local ok, v = pcall(function() return 9 end)
return tostring(ok) .. tostring(v)`},
	{"pcall_err", `
local ok = pcall(function() error("x") end)
return tostring(ok)`},
	{"nan_compare", `return tostring(0/0 == 0/0)`},
	{"inf_div", `return tostring(1/0)`},
	{"neg_zero", `return tostring(-0)`},
	// Negative-zero constant-table dedup (nightly fuzz seed 206160008016,
	// issue #7): with a leading +0 constant, a folded -0.0 must reuse the +0
	// slot → "0" (PUC addk dedups on numeric equality).
	{"neg_zero_fold_after_poszero", `local z = 0; return tostring(0.0 * -1)`},
	{"neg_zero_first_poszero_reuses", `local a = -0.0; return tostring(a) .. "|" .. tostring(0.0)`},
	// Constant-registration order of a comparison's left operand (nightly
	// oracle fuzz seed 9e5e75e5c04112a2, issue #137): PUC luaK_infix runs
	// exp2RK on the comparison's left operand before parsing the right
	// subtree, so the folded -0 grabs the shared zero slot first and a later
	// literal 0 reuses it → prints "-0". Deferring the left operand until
	// after the right subtree would let the +0 from 0%0 register first.
	{"cmp_left_negzero_registers_first", `return tostring(0*-0 ~= 0%0) .. "|" .. tostring(0)`},

	// —— Coverage-audit additions (2026-06-12) ——
	{"method_call", `
local t = { v = 10 }
function t.get(self) return self.v end
return t:get()`},
	{"method_def_colon", `
local obj = { n = 5 }
function obj:bump() self.n = self.n + 1; return self.n end
obj:bump()
return obj:bump()`},
	{"func_stmt_global", `
function double(x) return x * 2 end
return double(21)`},
	{"table_len", `return #{1, 2, 3}`},
	{"return_vararg", `
local function f(...) return ... end
return f(1, 2, 3)`},
	{"vararg_forward", `
local function sum3(a, b, c) return a + b + c end
local function fwd(...) return sum3(...) end
return fwd(1, 2, 3)`},
	{"select_hash", `return select("#", 1, 2, 3)`},
	{"break_in_for", `
local n = 0
for i = 1, 10 do
  n = i
  if i >= 4 then break end
end
return n`},
	{"break_in_while", `
local i = 0
while true do
  i = i + 1
  if i >= 3 then break end
end
return i`},
	{"string_sub_neg", `return string.sub("hello", -3)`},
	{"string_rep", `return string.rep("ab", 3)`},
	// —— pattern matcher diffing (P1 wrap-up round) ——
	{"find_plain", `return string.find("hello world", "world", 1, true)`},
	{"find_pattern", `return string.find("hello123", "%d+")`},
	{"find_anchored", `return string.find("hello", "^h")`},
	{"find_nomatch", `return tostring(string.find("abc", "z"))`},
	{"match_single", `return string.match("hello123", "%d+")`},
	{"match_caps", `return string.match("2026-06-12", "(%d+)-(%d+)-(%d+)")`},
	{"match_pos_capture", `return string.match("abc", "a()b")`},
	{"gsub_count", `return string.gsub("hello world", "o", "0")`},
	{"gsub_caps_ref", `return string.gsub("hello world", "(%w+)", "<%1>")`},
	{"gsub_limit", `return string.gsub("aaa", "a", "b", 2)`},
	{"gsub_percent", `return string.gsub("abc", "b", "%%")`},
	{"gmatch_iter", `
local out = ""
for w in string.gmatch("one two three", "%a+") do out = out .. w .. ";" end
return out`},
	{"gmatch_caps", `
local out = ""
for k, v in string.gmatch("a=1,b=2", "(%w+)=(%w+)") do out = out .. k .. v end
return out`},
	{"format_dsf", `return string.format("%d|%s|%.3f", 42, "x", 3.14159)`},
	{"format_x", `return string.format("%x/%X/%o", 255, 255, 8)`},
	{"format_pad", `return string.format("[%5d][%-5s]", 42, "ab")`},
	{"byte_char_rt", `return string.char(72, 105) .. tostring(string.byte("A"))`},
	{"byte_range", `
local a, b, c = string.byte("abc", 1, 3)
return a + b + c`},
	{"balance_match", `return string.match("(foo(bar))baz", "%b()")`},
	{"class_sets", `return string.match("hello42", "[%a]+") .. string.match("hello42", "[0-9]+")`},
	{"quantifier_lazy", `return string.match("<<a>>", "<(.-)>")`},
	{"quantifier_greedy", `return string.match("<<a>>", "<(.*)>")`},
	{"string_method_sugar", `return ("hello"):upper()`},
	{"backref", `return tostring(string.match("abcabc", "(abc)%1"))`},
}

// TestDiff_SeedCorpus diffs the fixed script set (Wangshu vs official 5.1.5).
func TestDiff_SeedCorpus(t *testing.T) {
	oracle := findOracle()
	if oracle == "" {
		t.Skip("lua5.1 oracle not found on PATH; skipping difftest")
	}
	for _, c := range seedCorpus {
		t.Run(c.name, func(t *testing.T) {
			// oracle wrapper: print the return value (tostring convention)
			oracleSrc := wrapForOracle(c.src)
			want := runOracle(t, oracle, oracleSrc)
			got := runWangshu(t, c.src)
			if got != want {
				t.Errorf("byte-diff:\n  wangshu: %q\n  oracle:  %q", got, want)
			}
		})
	}
}

// TestDiff_RandomScripts diffs randomly generated scripts (12 §3.2 generator).
//
// Defaults to 500 deterministic seeds (PR gate against regressions, reproducible
// in CI); nightly long runs extend coverage via environment variables:
// WANGSHU_FUZZ_SEED_BASE rolls the starting seed (e.g. a date epoch),
// WANGSHU_FUZZ_N scales up the count. On failure it prints the seed and full
// script; to replay:
// WANGSHU_FUZZ_SEED_BASE=<seed> WANGSHU_FUZZ_N=1 go test -run TestDiff_RandomScripts ./test/difftest/
func TestDiff_RandomScripts(t *testing.T) {
	oracle := findOracle()
	if oracle == "" {
		t.Skip("lua5.1 oracle not found on PATH; skipping difftest")
	}
	base := int64(0)
	n := int64(500)
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
		oracleSrc := wrapForOracle(src)
		want := runOracle(t, oracle, oracleSrc)
		got := runWangshu(t, src)
		if got != want {
			// The DIVERGENCE line is the machine-readable API for nightly
			// triage (the workflow only greps this marker; do not change the
			// format).
			t.Errorf("DIVERGENCE seed=%d kind=bytediff\n  wangshu: %q\n  oracle:  %q\n--- script ---\n%s",
				seed, got, want, src)
		}
	}
}

// wrapForOracle turns a `return expr` script into the oracle-side print form.
//
// Two fidelity details for diffing:
//   - Line-number alignment: the wrapper header and the first line of src are
//     joined on the same physical line (no leading newline), so line n of src
//     is still line n on the oracle side -- only then are the line numbers in
//     error messages comparable;
//   - True return count: use select("#", ...) to get the count (the # of a
//     table constructor {f()} truncates trailing nils, making `return nil`
//     indistinguishable from `return`).
func wrapForOracle(src string) string {
	return `local function __chunk() ` + src + `
end
local function __collect(...)
  local n = select("#", ...)
  local parts = {}
  for i = 1, n do parts[i] = tostring(select(i, ...)) end
  return table.concat(parts, "\t")
end
print(__collect(__chunk()))
`
}
