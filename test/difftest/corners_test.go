// Manual-corner probes + explicit exemptions (a probe-surface mirror of the
// three provisioning columns in 10 §11).
//
// Three kinds of entries:
//   - probe (required column): difftest oracle; missing/mismatched = FAIL;
//   - exempt (❌ gap column / design exemption): explicit skip + design-doc
//     citation — distinguishes "exempted" from "missed";
//   - approx (△ simplified column, items not byte-comparable): only checks
//     existence/shape, does not compare values.
package difftest

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// cornerProbes: corners in the required columns of 10 §11 not covered before
// (difftest oracle).
var cornerProbes = []diffCase{
	// base required-column supplements
	{"corner_G_identity", `return _G._G == _G, type(_G)`},
	{"corner_VERSION", `return _VERSION`},
	{"corner_global_via_G", `x_corner = 42; return _G.x_corner`},
	{"corner_unpack_global", `return unpack({7, 8, 9})`},

	// string shared metatable (PUC per-type metatable difftest; PR review
	// blocker item 2: the metatable exposes more than __index — __add/
	// __tostring/__concat/__lt etc. all take effect through the shared
	// metatable, and script mutations are globally visible)
	{"corner_strmt_shape", `local mt = getmetatable("") return type(mt), mt.__index == string, getmetatable("x") == mt`},
	{"corner_strmt_add", `getmetatable("").__add = function(a, b) return 42 end return "x" + "y"`},
	{"corner_strmt_tostring", `getmetatable("").__tostring = function(s) return "S:" .. string.len(s) end return tostring("abc")`},
	{"corner_strmt_index_override", `getmetatable("").__index = {probe = function() return "custom" end} return ("a").probe()`},
	{"corner_strmt_concat", `getmetatable("").__concat = function(a, b) return "CC" end return {} .. "x"`},
	{"corner_strmt_lt", `getmetatable("").__lt = function(a, b) return false end return "a" < "b"`},
	{"corner_strmt_newindex_fn", `local s = "a"
getmetatable("").__newindex = function(x, k, v) rawset(_G, "got_ni", x .. k .. v) end
s.x = 2
return got_ni`},
	{"corner_strmt_newindex_chain", `local sink = {}
getmetatable("").__newindex = sink
local s = "b"
s.k = "v"
return sink.k`},

	// math required column: atan2/frexp/ldexp/sinh/cosh/tanh (previously
	// unimplemented/untested)
	{"corner_math_atan2", `return math.atan2(1, 1) > 0.78 and math.atan2(1, 1) < 0.79`},
	{"corner_math_frexp", `return math.frexp(8)`},
	{"corner_math_ldexp", `return math.ldexp(0.5, 4)`},
	{"corner_math_hyperbolic", `
return math.sinh(0), math.cosh(0), math.tanh(0)`},

	// os required column: difftime
	{"corner_os_difftime", `return os.difftime(100, 40)`},

	// table simplified column: setn (5.1.5 reports obsolete in practice;
	// difftest the error text)
	{"corner_table_setn", `
local ok, e = pcall(function() table.setn({}, 1) end)
return ok, (e:gsub("^[^:]+:%d+: ", ""))`},

	// io required column: read's existence is verified via approx
	// (interactive input can't be difftested); write is already tested
	// string required column fully covered (full pattern set); string.dump
	// is in the exemption column

	// coroutine required column fully covered

	// —— Issue #125: `return <single-non-call-expr-with-jump-chain>`
	// codegen read a pre-captured freereg for RETURN's A instead of
	// the register exp2reg materialized into. exp2NextReg first frees
	// the expression's own temp (freereg drops back one) before
	// re-materializing, so RETURN read one slot PAST the value —
	// stale stack garbage that varied by tier/history (nightly fuzz
	// caught it as a P1-vs-auto divergence: P1 read a leftover 0,
	// tiered read nil). The or-chain over a no-result call is the
	// crasher shape; siblings pin and/not variants and a shifted
	// register base.
	{"corner_ret_orchain_nocall", `
local function f() local x = 7 end
return f() or (f())`},
	{"corner_ret_orchain_global", `
function sum() for A = 0, 0 do end end
return sum() or (sum())`},
	{"corner_ret_andchain", `
local function f() for A = 0, 0 do end end
return f() and 5 or 6`},
	{"corner_ret_orchain_shifted", `
local a = 1
local function f() end
return f() or (f())`},
	{"corner_ret_or_value", `
local function f() return 3 end
return f() or (f())`},
	{"corner_ret_notchain", `
local function f() for A = 0, 0 do end end
return (not f()) and f()`},

	// —— Issue #147: tonumber with embedded NUL bytes.
	// PUC luaO_str2d receives a C string (NUL-terminated); strtod stops
	// at '\0', so embedded NUL truncates the input. wangshu's Go string
	// can carry embedded NULs and must mirror C strlen semantics.
	{"corner_tonumber_nul_zero", `return tonumber("0\0")`},
	{"corner_tonumber_nul_trail", `return tonumber("42\0junk")`},
	{"corner_tonumber_nul_ws", `return tonumber("  3.14\0extra")`},
	{"corner_tonumber_nul_only", `return tonumber("\0")`},

	// —— Issue #158: string.format unsigned verbs with values >= 2^63.
	// PUC casts through (unsigned long long)(double); on x86-64 gcc
	// lowers that as cvttsd2si for f < 2^63 and (f - 2^63) + 2^63
	// above. Go's uint64(int64(f)) saturates anything >= 2^63 to the
	// int64-overflow indefinite. Out-of-range doubles (< 0 or >= 2^64)
	// are C UB and stay exempt from probing (arch-divergent).
	{"corner_format_X_2e19", `return string.format("%X", 10000000000000000000)`},
	{"corner_format_x_1p63", `return string.format("%x", 2^63)`},
	{"corner_format_u_big", `return string.format("%u", 12345678901234567890)`},
	{"corner_format_o_big", `return string.format("%o", 1.8e19)`},

	// —— Issue #163: non-function __tostring metafields. PUC's
	// luaL_callmeta calls whatever the field holds — a non-callable
	// value (number/boolean/string) raises "attempt to call a X
	// value"; a table with __call is invoked. wangshu's type filter
	// silently fell back to the address form.
	{"corner_tostring_meta_number", `
local t = setmetatable({}, {__tostring = 0})
local ok, e = pcall(tostring, t)
return ok, (e:gsub("^[^:]+:%d+: ", ""))`},
	{"corner_tostring_meta_bool", `
local t = setmetatable({}, {__tostring = false})
local ok, e = pcall(tostring, t)
return ok, (e:gsub("^[^:]+:%d+: ", ""))`},
	{"corner_tostring_meta_callable", `
local t = setmetatable({}, {__tostring = setmetatable({}, {__call = function() return "CC" end})})
return tostring(t)`},
	{"corner_tostring_meta_string_shared", `
getmetatable("").__tostring = 42
local ok, e = pcall(tostring, "x")
getmetatable("").__tostring = nil
return ok, (e:gsub("^[^:]+:%d+: ", ""))`},
	{"corner_print_meta_number", `
local t = setmetatable({}, {__tostring = "s"})
local ok, e = pcall(print, t)
return ok, (e:gsub("^[^:]+:%d+: ", ""))`},
}

// exemptions: the design-exemption list (10 §11 ❌ columns + prose
// discretion), recorded explicitly to guard against "missed" misjudgments.
var exemptions = []struct {
	name   string
	reason string
}{
	{"rawlen", "5.2 特性,10 §11 base ❌ 列(5.1 无 rawlen,# 走 LEN)"},
	{"require/module/package", "10 §11 base ❌ 列 + §4.7:嵌入式宿主经 Compile 提供脚本,不从文件系统 require"},
	{"table.pack/move", "5.2+ 特性,10 §11 table ❌ 列"},
	{"string.dump", "10 §11 string ❌ 列(字节码序列化;自定义 ISA 不兼容官方 .luc)"},
	{"math.tointeger/type/maxinteger/mininteger", "5.3 整数特性,10 §11 math ❌ 列"},
	{"os.execute", "10 §11 os ❌ 列(安全:嵌入式 VM 不让脚本跑 shell)"},
	{"io.popen/io.tmpfile", "10 §11 io ❌ 列"},
	{"debug.sethook/getlocal/setlocal/getupvalue/setupvalue/getregistry", "10 §11 debug ❌ 列"},
	{"getfenv/setfenv", "不在 10 §11 提供面任何列;唯一设计出处是 P2 不升层形状 F4(p2-bridge §358)——按设计豁免"},
	{"load(func) 渐进分块语义差", "已实现 reader 循环全量拼接;与 5.1 流式编译的差异仅在超大 chunk 内存峰值,语义等价"},
	{"os.exit 真退出", "10 §11:默认不真退出宿主进程(嵌入式安全)"},
	{"collectgarbage step/setstepmul 精确语义", "10 §13:STW GC 无增量调参,占位返回;数值不可逐字节比"},
	{"pattern 灾难性回溯有界失败", "回溯预算 1<<20 步:`.*.+%A*x` 级灾难回溯报 'pattern too complex',oracle 慢速算完——嵌入式防挂起裁量(官方成功/本实现报错的有意分歧)"},
	{"tonumber 负数 strtoul 回绕", "tonumber('-ff',16) 官方经 C strtoul 回绕返回 1.8446744073710e+19,本实现返回 -255——C 未定义行为的暴露,drop-in 价值低于困惑度,有意取直觉语义"},
	{"loadfile/dofile 默认禁用", "文件系统读默认关(不可信脚本越权探测面);Options.AllowFileLoad 显式开启后行为对齐官方"},
}

// TestDiff_CornerProbes difftests the handbook's required corner-case items.
func TestDiff_CornerProbes(t *testing.T) {
	oracle := findOracle()
	if oracle == "" {
		t.Skip("lua5.1 oracle not found on PATH; skipping difftest")
	}
	for _, c := range cornerProbes {
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

// TestExemptions_Documented pins the design-exemption list down as explicit
// skip records: each item produces one SKIP test, so the exemption surface is
// obvious at a glance (auditable via go test -v); if some exemption is ever
// implemented, it should be dropped from this table and added as a probe.
func TestExemptions_Documented(t *testing.T) {
	for _, e := range exemptions {
		t.Run(e.name, func(t *testing.T) {
			t.Skip("design exemption: " + e.reason)
		})
	}
}

// TestApprox_ExistenceOnly verifies the △ simplified-column "present but not
// byte-comparable" items: it only asserts that they are callable and return
// the correct shape, without comparing values (10 §13 observable-but-
// incomparable list).
func TestApprox_ExistenceOnly(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"collectgarbage_count_positive", `return collectgarbage("count") > 0`},
		{"gcinfo_nonneg", `return gcinfo() >= 0`},
		{"os_time_positive", `return os.time() > 1000000000`},
		{"os_clock_nonneg", `return os.clock() >= 0`},
		{"os_date_year_len", `return #os.date("%Y") == 4`},
		{"io_write_callable", `io.write(""); return true`},
		{"loadfile_error_form", `local f, e = loadfile("/nonexistent"); return f == nil and type(e) == "string"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(c.src), c.name)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			st := wangshu.NewState(wangshu.Options{})
			r, err := prog.Run(st)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if len(r) == 0 || !r[0].IsBool() || !r[0].Bool() {
				t.Errorf("existence check failed: %v", r)
			}
		})
	}
}
