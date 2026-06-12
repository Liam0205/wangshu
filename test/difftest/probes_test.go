// Feature probe corpus(按 Lua 5.1 Reference Manual 逐节列特性)。
//
// 解决 diff-fuzz 的系统性盲区:随机生成器跟着"已实现子集"走,结构性测不出
// "官方有而我们没有"的功能。本 corpus 按官方手册特性面(§2 语言 / §5 标准库)
// 逐项探测,oracle 成功而 wangshu 失败/不一致 = 完整性缺口。
//
// 常驻对拍(TestDiff_FeatureProbes):任何缺口在此文件留存为 FAIL,补完转绿。
package difftest

import "testing"

// featureProbes 按手册章节组织。每项必须是 oracle 可执行的合法 5.1 代码。
var featureProbes = []diffCase{
	// ===== §2.2 值与类型 =====
	{"probe_type_all", `return type(nil), type(true), type(1), type("s"), type({}), type(print)`},
	{"probe_tostring_number_int", `return tostring(7), tostring(-0.5), tostring(2^53)`},
	{"probe_number_hex_literal", `return 0xFF + 0x10`},
	{"probe_number_sci_literal", `return 1e3, 1E-2, .5, 5.`},
	{"probe_string_escapes", `return "a\tb\nc\\d\"e\'f", #"\65\66\67"`},
	{"probe_long_string", `return [[line1
line2]], [==[has ]] inside]==]`},

	// ===== §2.4 语句 =====
	{"probe_multiple_assign_swap", `local a, b = 1, 2; a, b = b, a; return a, b`},
	{"probe_chained_local_func", `local function f() return 1 end; local function g() return f() + 1 end; return g()`},
	{"probe_nested_do_scope", `local x = 1; do local x = 2; do local x = 3 end end; return x`},
	{"probe_while_complex_cond", `local i = 0; while i < 5 and true do i = i + 1 end; return i`},
	{"probe_repeat_local_in_cond", `local n = 0; repeat local done = n > 2; n = n + 1 until done; return n`},
	{"probe_numeric_for_float", `local s = 0; for i = 0.5, 2.5, 0.5 do s = s + i end; return s`},
	{"probe_generic_for_multi", `
local t = {10, 20}
local ks, vs = 0, 0
for k, v in ipairs(t) do ks = ks + k; vs = vs + v end
return ks, vs`},
	{"probe_break_nested", `
local n = 0
for i = 1, 3 do
  for j = 1, 3 do
    n = n + 1
    if j == 2 then break end
  end
end
return n`},

	// ===== §2.5 表达式 =====
	{"probe_concat_chain_numbers", `return 1 .. 2 .. 3`},
	{"probe_pow_neg_base", `return (-2)^2, -2^2`}, // -2^2 = -(2^2) = -4
	{"probe_compare_chain", `return 1 < 2, 2 <= 2, 3 > 2, 3 >= 3, 1 ~= 2, 1 == 1`},
	{"probe_and_or_values", `return (1 and 2), (nil and 2), (false or "x"), (1 or 2)`},
	{"probe_not_chain", `return not not 1, not nil, not 0`},
	{"probe_len_string_table", `return #"hello", #{1,2,3}`},
	{"probe_unary_minus_string", `return -"5"`}, // coercion: -5
	{"probe_arith_string_coercion", `return "10" + 5, "2" * "3", "7" % "4"`},
	{"probe_concat_precedence", `return "x" .. 1 + 2`}, // .. 优先级低于 +:x3
	{"probe_paren_truncate", `local function f() return 1, 2 end; return (f())`},
	{"probe_vararg_expr", `local function f(...) return ... end; return f(1, 2, 3)`},
	{"probe_vararg_len", `local function f(...) return select("#", ...) end; return f(nil, nil)`},
	{"probe_func_expr_immediate", `return (function(x) return x * 2 end)(21)`},

	// ===== §2.5.7 表构造 =====
	{"probe_table_mixed_ctor", `local t = {1, 2, x = 3, [10] = 4, 5}; return t[1], t[2], t[3], t.x, t[10]`},
	{"probe_table_trailing_sep", `local t = {1, 2, 3,}; return #t`},
	{"probe_table_semicolon_sep", `local t = {1; 2; 3}; return #t`},
	{"probe_table_call_expand", `local function f() return 2, 3 end; local t = {1, f()}; return #t`},
	{"probe_table_call_truncate", `local function f() return 2, 3 end; local t = {f(), 4}; return #t, t[1], t[2]`},

	// ===== §2.8 元表(全 17 个事件) =====
	{"probe_meta_index_table", `local t = setmetatable({}, {__index = {x = 1}}); return t.x`},
	{"probe_meta_index_func", `local t = setmetatable({}, {__index = function(_, k) return k .. "!" end}); return t.hi`},
	{"probe_meta_newindex_func", `
local log = {}
local t = setmetatable({}, {__newindex = function(_, k, v) log[k] = v end})
t.a = 1
return log.a, rawget(t, "a")`},
	{"probe_meta_newindex_table", `
local target = {}
local t = setmetatable({}, {__newindex = target})
t.a = 99
return target.a, rawget(t, "a")`},
	{"probe_meta_call", `local t = setmetatable({}, {__call = function(self, x) return x * 2 end}); return t(21)`},
	{"probe_meta_call_multiargs", `
local t = setmetatable({}, {__call = function(self, a, b, c) return a + b + c end})
return t(1, 2, 3)`},
	{"probe_meta_add", `local mt = {__add = function(a, b) return "added" end}; return setmetatable({}, mt) + 1`},
	{"probe_meta_sub_swap", `local mt = {__sub = function(a, b) return type(a) end}; return 1 - setmetatable({}, mt)`},
	{"probe_meta_mul_div_mod_pow", `
local mt = {
  __mul = function() return "mul" end, __div = function() return "div" end,
  __mod = function() return "mod" end, __pow = function() return "pow" end,
}
local t = setmetatable({}, mt)
return t * 1, t / 1, t % 1, t ^ 1`},
	{"probe_meta_unm", `local t = setmetatable({}, {__unm = function() return "negated" end}); return -t`},
	{"probe_meta_concat", `local t = setmetatable({}, {__concat = function(a, b) return "cat" end}); return t .. "x", "x" .. t`},
	{"probe_meta_eq", `
local mt = {__eq = function(a, b) return true end}
local a = setmetatable({}, mt)
local b = setmetatable({}, mt)
return a == b, a ~= b`},
	{"probe_meta_eq_different_mt", `
-- __eq 仅在两操作数同为 table 且元方法相同才触发(5.1)
local a = setmetatable({}, {__eq = function() return true end})
local b = setmetatable({}, {__eq = function() return true end})
return a == b`},
	{"probe_meta_lt", `
local mt = {__lt = function(a, b) return a.v < b.v end}
local a = setmetatable({v = 1}, mt)
local b = setmetatable({v = 2}, mt)
return a < b, b < a`},
	{"probe_meta_le_direct", `
local mt = {__le = function(a, b) return a.v <= b.v end}
local a = setmetatable({v = 1}, mt)
local b = setmetatable({v = 1}, mt)
return a <= b`},
	{"probe_meta_le_lt_fallback", `
-- 5.1:无 __le 时用 not __lt(b, a) 回退
local mt = {__lt = function(a, b) return a.v < b.v end}
local a = setmetatable({v = 1}, mt)
local b = setmetatable({v = 2}, mt)
return a <= b, b <= a`},
	{"probe_meta_gt_swaps", `
local mt = {__lt = function(a, b) return a.v < b.v end}
local a = setmetatable({v = 1}, mt)
local b = setmetatable({v = 2}, mt)
return b > a, a >= a == nil or a >= a`},
	{"probe_meta_tostring", `local t = setmetatable({}, {__tostring = function() return "CUSTOM" end}); return tostring(t)`},
	{"probe_meta_metatable_shield", `
local t = setmetatable({}, {__metatable = "locked"})
return getmetatable(t)`},
	{"probe_meta_index_chain3", `
local l3 = {deep = "found"}
local l2 = setmetatable({}, {__index = l3})
local l1 = setmetatable({}, {__index = l2})
return l1.deep`},
	{"probe_rawops_bypass_meta", `
local t = setmetatable({}, {
  __index = function() return "meta" end,
  __newindex = function() error("blocked") end,
})
rawset(t, "k", "raw")
return rawget(t, "k"), t.other`},

	// ===== §3.8/§5.1 base 库 =====
	{"probe_assert_message", `local ok, e = pcall(function() assert(nil, "custom msg") end); return ok, e`},
	{"probe_assert_passthrough", `return assert(1, 2, 3)`},
	{"probe_error_table_value", `
local ok, e = pcall(function() error({code = 42}) end)
return ok, type(e), e.code`},
	{"probe_ipairs_stops_at_nil", `
local t = {1, 2, nil, 4}
local n = 0
for _ in ipairs(t) do n = n + 1 end
return n`},
	{"probe_next_empty", `return next({})`},
	{"probe_pcall_returns_all", `return pcall(function() return 1, 2, 3 end)`},
	{"probe_pcall_non_function", `return pcall(42)`},
	{"probe_select_negative", `return select(-1, "a", "b", "c")`},
	{"probe_select_negative_two", `return select(-2, "a", "b", "c")`},
	{"probe_tonumber_base", `return tonumber("ff", 16), tonumber("11", 2), tonumber("z", 36)`},
	{"probe_tonumber_hex_string", `return tonumber("0xFF")`},
	{"probe_tostring_nil_bool", `return tostring(nil), tostring(true), tostring(false)`},
	{"probe_unpack_basic", `return unpack({1, 2, 3})`},
	{"probe_unpack_range", `return unpack({1, 2, 3, 4}, 2, 3)`},
	{"probe_loadstring_basic", `local f = loadstring("return 1 + 1"); return f()`},
	{"probe_loadstring_syntax_err", `local f, e = loadstring("return +"); return f == nil, type(e)`},
	{"probe_loadstring_closure", `local f = loadstring("return ...") ; return f(7, 8)`},
	{"probe_xpcall_basic", `return xpcall(function() return "ok" end, function(e) return "handled" end)`},
	{"probe_xpcall_error", `return xpcall(function() error("E", 0) end, function(e) return "H:" .. e end)`},
	{"probe_rawequal_primitives", `return rawequal(1, 1), rawequal("a", "a"), rawequal({}, {})`},
	{"probe_rawlen_via_len", `local t = setmetatable({1,2}, {}); return #t`},

	// ===== §5.4 string 库 =====
	{"probe_string_lib_full", `
return string.len("abc"), string.sub("hello", 2, 3), string.upper("a"),
  string.lower("A"), string.rep("ab", 2), string.reverse("abc")`},
	{"probe_string_byte_char", `return string.byte("A"), string.byte("abc", 2), string.char(97, 98)`},
	{"probe_string_format_misc", `
return string.format("%d %i", 1, 2), string.format("%05.1f", 3.14159),
  string.format("%e", 12345.678), string.format("%g", 0.00001),
  string.format("%%"), string.format("%s", nil)`},
	{"probe_string_format_q", `return string.format("%q", 'he said "hi"')`},
	{"probe_string_find_special", `
return string.find("a.b", ".", 1, true), string.find("a.b", "%."),
  string.find("abc", "b", -2)`},
	{"probe_string_gsub_anchors", `return string.gsub("aaa", "^a", "b")`},
	{"probe_string_gsub_empty_pat", `return ("abc"):gsub("", "-")`},
	{"probe_string_match_init_neg", `return string.match("hello", "l+", -2)`},
	{"probe_string_method_chain", `return ("a,b,c"):gsub(",", ";"):upper()`},

	// ===== §5.5 table 库 =====
	{"probe_table_lib_full", `
local t = {3, 1, 2}
table.sort(t)
table.insert(t, 4)
table.insert(t, 1, 0)
local r = table.remove(t)
return table.concat(t, ","), r`},
	{"probe_table_sort_strings", `local t = {"b", "a", "c"}; table.sort(t); return table.concat(t)`},
	{"probe_table_maxn", `return table.maxn({1, 2, [10] = 3})`},

	// ===== §5.6 math 库 =====
	{"probe_math_constants", `return math.pi > 3.14, math.huge > 1e308, -math.huge < -1e308`},
	{"probe_math_minmax_multi", `return math.max(1, 2, 3, 4), math.min(4, 3, 2, 1)`},
	{"probe_math_floor_ceil_neg", `return math.floor(-1.5), math.ceil(-1.5)`},
	{"probe_math_fmod_neg", `return math.fmod(-6, 4), math.fmod(6, -4)`}, // C fmod 语义,≠ %
	{"probe_math_sqrt_abs", `return math.sqrt(2) > 1.41, math.abs(-0)`},

	// ===== §2.6 / §5.2 协程 =====
	{"probe_coroutine_full_cycle", `
local co = coroutine.create(function(a)
  local b = coroutine.yield(a + 1)
  local c = coroutine.yield(b + 1)
  return c + 1
end)
local _, r1 = coroutine.resume(co, 10)
local _, r2 = coroutine.resume(co, 20)
local _, r3 = coroutine.resume(co, 30)
return r1, r2, r3, coroutine.status(co)`},
	{"probe_coroutine_wrap_error", `
local gen = coroutine.wrap(function() error("inner", 0) end)
local ok, e = pcall(gen)
return ok, e`},
	{"probe_coroutine_yield_multivals", `
local co = coroutine.create(function() coroutine.yield(1, 2, 3) end)
return coroutine.resume(co)`},
	{"probe_coroutine_resume_extra", `
local co = coroutine.create(function(...) return select("#", ...) end)
local _, n = coroutine.resume(co, 1, 2, 3, 4)
return n`},

	// ===== 闭包/upvalue 语义 =====
	{"probe_upvalue_shared", `
local function make()
  local n = 0
  return function() n = n + 1; return n end, function() return n end
end
local inc, get = make()
inc(); inc()
return get()`},
	{"probe_upvalue_loop_capture", `
local fns = {}
for i = 1, 3 do fns[i] = function() return i end end
return fns[1]() + fns[2]() + fns[3]()`},
	{"probe_recursive_local", `
local function fact(n) if n <= 1 then return 1 end return n * fact(n - 1) end
return fact(5)`},

	// ===== 数字格式边角 =====
	{"probe_number_formats", `return tostring(1/3), tostring(100), tostring(0.1), tostring(1e300)`},
	{"probe_int_boundary", `return 2^31, -(2^31), 2^52 + 0.5`},
}

// TestDiff_FeatureProbes 常驻对拍特性探测面(oracle 成功而 wangshu 失败/不一致
// = 完整性缺口)。
func TestDiff_FeatureProbes(t *testing.T) {
	oracle := findOracle()
	if oracle == "" {
		t.Skip("lua5.1 oracle not found on PATH; skipping difftest")
	}
	for _, c := range featureProbes {
		t.Run(c.name, func(t *testing.T) {
			oracleSrc := wrapForOracle(c.src)
			want := runOracle(t, oracle, oracleSrc)
			got := runWangshu(t, c.src)
			if got != want {
				t.Errorf("byte-diff:\n  wangshu: %q\n  oracle:  %q", got, want)
			}
		})
	}
}
