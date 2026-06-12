// Conformance harness — 固定语义用例,断言 Wangshu 输出与期望逐字节一致(12 §2)。
//
// 与 difftest 的分工:conformance 是人写的、有意覆盖语义角落的固定用例
// (期望值内置,不依赖 oracle 进程);difftest 是对拍官方 5.1.5 的随机/种子脚本。
package conformance

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

type confCase struct {
	name string
	src  string
	want string // return 值的 GoString,用 \t join
}

var cases = []confCase{
	// —— 真值语义:仅 nil/false 为假(01 §6) ——
	{"truthy_zero", `if 0 then return "t" else return "f" end`, "t"},
	{"truthy_empty_str", `if "" then return "t" else return "f" end`, "t"},
	{"truthy_nil", `if nil then return "t" else return "f" end`, "f"},
	{"truthy_false", `if false then return "t" else return "f" end`, "f"},

	// —— 算术语义(02 §4) ——
	{"mod_lua_semantics", `return -7 % 3`, "2"},    // a-floor(a/b)*b ⇒ 2(C 语义是 -1)
	{"div_is_float", `return 7 / 2`, "3.5"},        // 浮点除
	{"pow_right_assoc", `return 2 ^ 3 ^ 2`, "512"}, // 右结合 2^(3^2)
	{"unm_neg_zero", `return tostring(-0)`, "-0"},  // Lua 5.1 tostring(-0) = "-0"
	{"concat_right_assoc", `return "a" .. "b" .. "c"`, "abc"},

	// —— 比较语义(05 §4.4) ——
	{"nan_ne_nan", `return tostring(0/0 ~= 0/0)`, "true"},
	{"string_lt_bytewise", `return tostring("Z" < "a")`, "true"}, // 字典序按 byte
	{"eq_diff_types", `return tostring(1 == "1")`, "false"},      // 异类不等不 coerce

	// —— 短路语义 ——
	{"and_returns_second", `return 1 and 2`, "2"},
	{"or_returns_first_truthy", `return false or "x"`, "x"},
	{"and_short_circuit", `
local called = false
local function f() called = true; return true end
local r = false and f()
return tostring(called)`, "false"},
	// 短路结果直接参与算术/取负:带跳转链的 eKNum 不可常量折叠(官方
	// isnumeral 语义)——折叠丢链曾让 TESTSET 的 255 占位越界,Go 级 panic。
	{"shortcircuit_into_arith", `return (true and 7 or -1) + 1`, "8"},
	{"shortcircuit_into_arith_var", `local a = true return (a and 7 or -1) + 1`, "8"},
	{"shortcircuit_into_unm", `return -(true and 1 or 2)`, "-1"},
	{"shortcircuit_into_mul", `return (false and 7 or -1) * 2`, "-2"},
	{"shortcircuit_into_concat", `return (true and 7 or -1) .. ""`, "7"},
	{"shortcircuit_into_compare", `return tostring((true and 7 or -1) < 8)`, "true"},

	// —— 作用域 / 闭包(04 §5.8) ——
	{"local_shadow", `
local x = 1
do local x = 2 end
return x`, "1"},
	{"local_rhs_outer", `
local a = 10
local a = a + 1
return a`, "11"}, // local a = a 的 RHS 是外层 a
	{"closure_per_capture", `
local fns = {}
local function mk(i) return function() return i end end
for i = 1, 3 do fns[i] = mk(i) end
return fns[1]() + fns[2]() + fns[3]()`, "6"},

	// —— repeat-until 作用域(04 §6.4) ——
	{"repeat_until_sees_local", `
local n = 0
repeat
  local done = n >= 3
  n = n + 1
until done
return n`, "4"},

	// —— 多值规约(04 §6.2) ——
	{"multi_value_truncate", `
local function two() return 1, 2 end
local a, b, c = two()
return tostring(a) .. tostring(b) .. tostring(c)`, "12nil"},
	{"multi_value_paren_single", `
local function two() return 1, 2 end
local a, b = (two())
return tostring(a) .. tostring(b)`, "1nil"}, // 括号强制单值

	// —— vararg ——
	{"vararg_count_fixed", `
local function f(a, ...)
  local b, c = ...
  return tostring(a) .. tostring(b) .. tostring(c)
end
return f(1, 2, 3)`, "123"},

	// —— 数值 for 边界(05 §10.1) ——
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

	// —— 元表语义(07) ——
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
return e`, "custom-msg"}, // level=0 不加位置前缀(5.1)
	{"error_with_position", `
local _, e = pcall(function() error("pfx") end)
return (string.find(e, ": pfx") ~= nil)`, "true"}, // 默认 level=1 带 chunkname:line:

	// —— 覆盖率审计补充(2026-06-12):此前无测试覆盖的语法/库路径 ——
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

	// —— P1 收尾轮新功能(2026-06-12) ——
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
}

func TestConformance(t *testing.T) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(c.src), c.name)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			st := wangshu.NewState(wangshu.Options{})
			results, err := prog.Run(st)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			parts := make([]string, len(results))
			for i, r := range results {
				parts[i] = r.GoString()
			}
			got := strings.Join(parts, "\t")
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
