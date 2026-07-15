// Conformance harness — fixed semantic cases, asserting Wangshu output is byte-for-byte equal to the expected (12 §2).
//
// Division of labor with difftest: conformance is human-written fixed cases that deliberately cover semantic
// corners (expected values built in, not depending on an oracle process); difftest is random/seeded scripts
// differentially tested against the official 5.1.5.
//
// p1/p3 dual-build coverage (PR #15 review): cases get their State via `newConformanceState()`,
// a helper that calls `SetForceAllPromote(true)` on every case — under the default build it is a no-op
// (the P3 backend is not injected, and the F7 compilability gate permanently judges not-compilable);
// under the `wangshu_p3 wangshu_profile` build it delivers on the "P3 gibbous path coverage" promise,
// letting those 83 one-shot small scripts (entry count = 1, not reaching HotEntryThreshold) also run through
// the gibbous wasm execution path, instead of degrading to the p3-tag-compiled interpreter path.
//
// **P4 build boundary** (per external review 🔴 blocker): under the P4 build, force-all is nominally
// enabled, but conformance cases are mostly one-shot small scripts, ~91% not reaching the P4 promotion gate
// (analyzeCompilability / recheckCompilabilityRuntime gate + SupportsAllOpcodes whitelist limit). This is
// determined by the form of the conformance cases; **acceptance of the real P4 path is per
// `test/difftest/p4_test.go`** (repeated calls + PromotionCount>0 guard).
// This file is build-tag neutral (runs under all P1/P3/P4 builds), but under the P4 build it does not strongly assert promotion.
package conformance

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// newConformanceState is the State factory for all conformance cases. Under the p3 build it
// turns on force-all promotion mode, to ensure every case takes the gibbous wasm execution path; under the p1
// build, SetForceAllPromote is a no-op and behavior is unchanged.
func newConformanceState() *wangshu.State {
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	return st
}

type confCase struct {
	name string
	src  string
	want string // Display form of the return value, joined with \t
}

var cases = []confCase{
	// —— Truthiness semantics: only nil/false are falsy (01 §6) ——
	{"truthy_zero", `if 0 then return "t" else return "f" end`, "t"},
	{"truthy_empty_str", `if "" then return "t" else return "f" end`, "t"},
	{"truthy_nil", `if nil then return "t" else return "f" end`, "f"},
	{"truthy_false", `if false then return "t" else return "f" end`, "f"},

	// —— Arithmetic semantics (02 §4) ——
	{"mod_lua_semantics", `return -7 % 3`, "2"},    // a-floor(a/b)*b ⇒ 2 (C semantics is -1)
	{"div_is_float", `return 7 / 2`, "3.5"},        // floating-point division
	{"pow_right_assoc", `return 2 ^ 3 ^ 2`, "512"}, // right-associative 2^(3^2)
	{"unm_neg_zero", `return tostring(-0)`, "-0"},  // Lua 5.1 tostring(-0) = "-0"
	// Negative-zero constant-table deduplication (nightly fuzz seed 206160008016, issue #7): PUC addk
	// deduplicates by numeric equality (+0.0 == -0.0 hit the same slot), physically stores the zero that
	// arrives first and keeps its sign, later ones reuse it. A folded-out -0.0 reuses the +0 slot after a
	// preceding +0 constant → "0" (not "-0").
	{"negzero_fold_after_poszero", `local z = 0; return tostring(0.0 * -1)`, "0"},                           // +0 arrives first, folded -0 reuses → 0
	{"negzero_literal_after_poszero", `local z = 0; return tostring(-0.0)`, "0"},                            // +0 arrives first, -0 literal reuses → 0
	{"negzero_first_poszero_reuses", `local a = -0.0; return tostring(a) .. "|" .. tostring(0.0)`, "-0|-0"}, // -0 arrives first, +0 reuses → -0
	{"negzero_runtime_not_folded", `local z = 0; local m = -1; return tostring(0.0 * m)`, "-0"},             // runtime -0 does not go through the constant table → -0

	{"concat_right_assoc", `return "a" .. "b" .. "c"`, "abc"},

	// —— Comparison semantics (05 §4.4) ——
	{"nan_ne_nan", `return tostring(0/0 ~= 0/0)`, "true"},
	{"string_lt_bytewise", `return tostring("Z" < "a")`, "true"}, // lexicographic order by byte
	{"eq_diff_types", `return tostring(1 == "1")`, "false"},      // different types unequal, no coercion

	// —— Short-circuit semantics ——
	{"and_returns_second", `return 1 and 2`, "2"},
	{"or_returns_first_truthy", `return false or "x"`, "x"},
	{"and_short_circuit", `
local called = false
local function f() called = true; return true end
local r = false and f()
return tostring(called)`, "false"},
	// A short-circuit result feeding directly into arithmetic/negation: an eKNum carrying a jump chain
	// cannot be constant-folded (official isnumeral semantics) — folding that drops the chain once made
	// TESTSET's 255 placeholder go out of bounds, a Go-level panic.
	{"shortcircuit_into_arith", `return (true and 7 or -1) + 1`, "8"},
	{"shortcircuit_into_arith_var", `local a = true return (a and 7 or -1) + 1`, "8"},
	{"shortcircuit_into_unm", `return -(true and 1 or 2)`, "-1"},
	{"shortcircuit_into_mul", `return (false and 7 or -1) * 2`, "-2"},
	{"shortcircuit_into_concat", `return (true and 7 or -1) .. ""`, "7"},
	{"shortcircuit_into_compare", `return tostring((true and 7 or -1) < 8)`, "true"},

	// —— Scoping / closures (04 §5.8) ——
	{"local_shadow", `
local x = 1
do local x = 2 end
return x`, "1"},
	{"local_rhs_outer", `
local a = 10
local a = a + 1
return a`, "11"}, // the RHS of local a = a is the outer a
	{"closure_per_capture", `
local fns = {}
local function mk(i) return function() return i end end
for i = 1, 3 do fns[i] = mk(i) end
return fns[1]() + fns[2]() + fns[3]()`, "6"},

	// —— repeat-until scoping (04 §6.4) ——
	{"repeat_until_sees_local", `
local n = 0
repeat
  local done = n >= 3
  n = n + 1
until done
return n`, "4"},

	// —— Multi-value reduction (04 §6.2) ——
	{"multi_value_truncate", `
local function two() return 1, 2 end
local a, b, c = two()
return tostring(a) .. tostring(b) .. tostring(c)`, "12nil"},
	{"multi_value_paren_single", `
local function two() return 1, 2 end
local a, b = (two())
return tostring(a) .. tostring(b)`, "1nil"}, // parentheses force a single value

	// —— vararg ——
	{"vararg_count_fixed", `
local function f(a, ...)
  local b, c = ...
  return tostring(a) .. tostring(b) .. tostring(c)
end
return f(1, 2, 3)`, "123"},

	// —— Numeric for boundaries (05 §10.1) ——
	{"for_zero_iterations", `
local n = 0
for i = 5, 1 do n = n + 1 end
return n`, "0"},
	{"for_negative_step", `
local s = 0
for i = 5, 1, -1 do s = s + i end
return s`, "15"},
	{"for_fractional_step", `
local n = 0
for i = 1, 2, 0.5 do n = n + 1 end
return n`, "3"},
	// step == 0 takes PUC's DESCENDING branch (lvm.c luai_numlt(0, step)):
	// ascending range -> zero iterations, descending/equal range -> loops
	// until break (issue #97: `step >= 0` in the interpreter inverted
	// both shapes; found by nightly fuzz seed f1e595a2f2ed7b31).
	{"for_zero_step_ascending", `
local n = 0
for i = 0, 1, 0 do n = n + 1 if n > 2 then break end end
return n`, "0"},
	{"for_zero_step_descending", `
local n = 0
for i = 1, 0, 0 do n = n + 1 if n > 2 then break end end
return n`, "3"},
	{"for_zero_step_equal", `
local n = 0
for i = 0, 0, 0 do n = n + 1 if n > 2 then break end end
return n`, "3"},
	{"for_nan_step", `
local n = 0
for i = 0, 1, 0/0 do n = n + 1 if n > 2 then break end end
return n`, "0"},

	// —— Metatable semantics (07) ——
	{"index_chain_two_levels", `
local a = { v = 42 }
local b = setmetatable({}, { __index = a })
local c = setmetatable({}, { __index = b })
return c.v`, "42"},
	{"newindex_no_fire_on_existing", `
local fired = false
local t = setmetatable({ k = 1 }, { __newindex = function() fired = true end })
t.k = 2
return tostring(fired) .. tostring(rawget(t, "k"))`, "false2"},

	// —— pcall / error(09) ——
	{"pcall_nested", `
local ok1 = pcall(function()
  local ok2 = pcall(function() error("inner") end)
  if not ok2 then error("outer") end
end)
return tostring(ok1)`, "false"},
	{"error_value_passthrough", `
local _, e = pcall(function() error("custom-msg", 0) end)
return e`, "custom-msg"}, // level=0 adds no position prefix (5.1)
	{"error_with_position", `
local _, e = pcall(function() error("pfx") end)
return (string.find(e, ": pfx") ~= nil)`, "true"}, // default level=1 carries chunkname:line:

	// —— Coverage-audit additions (2026-06-12): syntax/library paths previously untested ——
	{"method_call_self", `
local t = { v = 10 }
function t.get(self) return self.v end
return t:get()`, "10"},
	{"method_def_colon", `
local obj = { n = 5 }
function obj:bump() self.n = self.n + 1; return self.n end
obj:bump()
return obj:bump()`, "7"},
	{"func_stmt_global", `
function double(x) return x * 2 end
return double(21)`, "42"},
	{"func_stmt_dotted", `
local m = {}
function m.f(x) return x + 1 end
return m.f(1)`, "2"},
	{"table_len_border", `return #{1, 2, 3}`, "3"},
	{"table_len_empty", `return #{}`, "0"},
	{"string_arg_sugar", `return string.upper"abc"`, "ABC"},
	{"table_arg_sugar", `
local function id(t) return t[1] end
return id{ 99 }`, "99"},
	{"return_vararg_all", `
local function f(...) return ... end
local a, b, c = f(1, 2, 3)
return a + b + c`, "6"},
	{"vararg_in_call_args", `
local function sum3(a, b, c) return a + b + c end
local function fwd(...) return sum3(...) end
return fwd(1, 2, 3)`, "6"},
	{"vararg_in_table", `
local function f(...) return { ... } end
local t = f(7, 8)
return t[1] + t[2]`, "15"},
	{"select_hash", `return select("#", 1, 2, 3)`, "3"},
	{"select_index", `return select(2, "a", "b", "c")`, "b\tc"},
	{"string_lib_rest", `
return string.lower("ABC") .. string.reverse("xy") .. tostring(string.len("hello"))`, "abcyx5"},
	{"rawequal_basic", `
local t = {}
return tostring(rawequal(t, t)) .. tostring(rawequal(1, 1)) .. tostring(rawequal({}, {}))`,
		"truetruefalse"},
	{"not_operator", `return tostring(not nil) .. tostring(not 0)`, "truefalse"},
	{"len_string", `return #"hello"`, "5"},
	{"nested_table_constructor", `
local t = { a = { b = { c = 42 } } }
return t.a.b.c`, "42"},
	{"numeric_for_inner_break", `
local n = 0
for i = 1, 10 do
  n = i
  if i >= 4 then break end
end
return n`, "4"},
	{"while_break", `
local i = 0
while true do
  i = i + 1
  if i >= 3 then break end
end
return i`, "3"},

	// —— P1 wrap-up round new features (2026-06-12) ——
	{"generic_for_ipairs", `
local t = { 5, 6, 7 }
local sum = 0
for i, v in ipairs(t) do sum = sum + i * v end
return sum`, "38"}, // 1*5+2*6+3*7
	{"generic_for_pairs_count", `
local t = { a = 1, b = 2, c = 3, 10, 20 }
local n = 0
for k, v in pairs(t) do n = n + 1 end
return n`, "5"},
	{"next_manual", `
local t = { x = 1 }
local k, v = next(t)
return k, v, tostring(next(t, k))`, "x\t1\tnil"},
	{"table_insert_remove", `
local t = {}
table.insert(t, "a")
table.insert(t, "b")
table.insert(t, 1, "z")
local r = table.remove(t, 2)
return table.concat(t, ","), r`, "z,b\ta"},
	{"table_sort_default", `
local t = { 3, 1, 2 }
table.sort(t)
return table.concat(t, "")`, "123"},
	{"table_sort_comparator", `
local t = { 1, 3, 2 }
table.sort(t, function(a, b) return a > b end)
return table.concat(t, "")`, "321"},
	{"unpack_range", `
local a, b = unpack({10, 20, 30}, 2, 3)
return a, b`, "20\t30"},
	{"string_find_captures", `
local s, e, cap = string.find("hello=world", "(%a+)=")
return s, e, cap`, "1\t6\thello"},
	{"string_gsub_func_repl", `
return (string.gsub("abc", "%a", function(c) return c:upper() end))`, "ABC"},
	{"string_gmatch_collect", `
local out = {}
for w in string.gmatch("a,b,c", "[^,]+") do out[#out+1] = w end
return table.concat(out, "|")`, "a|b|c"},
	{"string_format_mixed", `
return string.format("%d-%s-%.2f", 7, "x", 1.5)`, "7-x-1.50"},
	{"string_byte_char_roundtrip", `
return string.char(string.byte("Q"))`, "Q"},
	{"method_sugar_on_literal", `
return ("mixed"):upper():lower()`, "mixed"},
	{"coroutine_pingpong", `
local co = coroutine.create(function(x)
  local y = coroutine.yield(x * 2)
  return y + 1
end)
local _, a = coroutine.resume(co, 5)
local _, b = coroutine.resume(co, 100)
return a, b`, "10\t101"},
	{"coroutine_wrap_iterator", `
local function range(n)
  return coroutine.wrap(function()
    for i = 1, n do coroutine.yield(i) end
  end)
end
local sum = 0
for i in range(4) do sum = sum + i end
return sum`, "10"},
	{"xpcall_handler_transforms", `
local ok, r = xpcall(function() error("E", 0) end, function(e) return "<" .. e .. ">" end)
return tostring(ok), r`, "false\t<E>"},
	{"weak_table_mode_set", `
local t = setmetatable({}, { __mode = "v" })
t.x = "still here before gc"
return t.x`, "still here before gc"},
	{"math_extras", `
return math.fmod(7, 3), math.max(1, 9, 5), math.floor(math.pi)`, "1\t9\t3"},
	{"select_tail", `
local function f(...) return select(2, ...) end
return f("a", "b", "c")`, "b\tc"},

	// —— Historical bug protection (frozen from a consolidated fix round) ——
	// infix evaluation order: the left operand must be materialized into an RK before the right subtree
	// (including CALL) is compiled (luaK_infix), otherwise it is read only after the right subtree's side
	// effect has overwritten the left value — once returned 100 (evaluation-order error).
	{"infix_eval_order_left_first", `
local a = { 1 }
local function g() a[1] = 99; return 1 end
return a[1] + g()`, "2"},
	// pcall two return values (`local ok, e = pcall(...)`) once triggered a trailing multi-value source A overwrite crash.
	{"pcall_two_returns", `
local ok, e = pcall(function() local x = nil + 1 end)
return tostring(ok), (e:gsub("^[^:]+:%d+: ", ""))`,
		"false\tattempt to perform arithmetic on a nil value"},
	// String arithmetic coercion (luaV_tonumber): once reported attempt to perform arithmetic.
	{"string_arith_coercion", `return "10" + 1, "3" * "4"`, "11\t12"},
	// Multiple return values passed to multiple targets (the return-vararg path once collapsed the trailing one to a single value, making c=nil).
	{"multi_assign_from_call", `
local function f(...) return ... end
local a, b, c = f(1, 2, 3)
return c`, "3"},
	// Tail-calling a host function with multiple results: doTailCall used
	// to pass the parent frame's fixed nresults to callHost (should be -1
	// multret, matching PUC's luaD_call(L, ra, LUA_MULTRET)); the
	// fixed-count branch reset top so the trailing RETURN B=0 dropped
	// trailing results (`return unpack(t)` returned only 2 values; found
	// by issue #52 P4 acceptance, one shared fix covers P1/P3/P4).
	{"tailcall_host_multret", `
local function k(t) return unpack(t) end
local a, b, c = k({7, 8, 9})
return a, b, c`, "7\t8\t9"},
}

func TestConformance(t *testing.T) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(c.src), c.name)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			st := newConformanceState()
			results, err := prog.Run(st)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			parts := make([]string, len(results))
			for i, r := range results {
				parts[i] = r.Display()
			}
			got := strings.Join(parts, "\t")
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestSetListBatchOverflow: for a table constructor of >25550 items, the SETLIST batch number
// exceeds the 9-bit C field, so codegen must follow the official luaK_setlist and emit C=0 + a
// following bare batch-number instruction. The old implementation silently truncated C by &0x1FF
// and wrapped around, and the interpreter swallowed the next normal instruction as the batch number (hang / wrong result).
func TestSetListBatchOverflow(t *testing.T) {
	const n = 25551 // 511 batches × 50 + 1, the 512th batch triggers the overflow path
	var sb strings.Builder
	sb.WriteString("local t = {")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "%d,", i)
	}
	sb.WriteString("}\nreturn #t, t[1], t[25550], t[25551]")
	prog, err := wangshu.Compile([]byte(sb.String()), "bigctor")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := newConformanceState()
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := []float64{n, 1, 25550, 25551}
	for i, w := range want {
		if results[i].Number() != w {
			t.Errorf("result[%d] = %s, want %v", i, results[i].Display(), w)
		}
	}
}
