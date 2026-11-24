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
	"sort"
	"strings"
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
	{"corner_print_meta_string", `
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
	{"debug 库只提供 traceback/getinfo", "10 §11 把 debug.traceback/getinfo 列为要做的、其余列在 ❌ 列。现在这两个已提供并与 lua5.1 一致;sethook / getlocal / setlocal / getupvalue / setupvalue / getregistry 仍然不存在——它们需要解释器没有暴露的内省钩子。getinfo 只填 P1 能诚实给出的字段(currentline / source / short_src / what / func),nups / activelines / namewhat 宁缺不假造"},
	{"os.time{isdst=} 采用 glibc 的 mktime 搜索规则(跨平台统一)", "该字段的解析需要在 tzdata transition 表里搜索,而各家 libc 的搜索步长与顺序不同。wangshu 在所有平台上都用 glibc 的规则(601200 秒步长、每步先后再前、上限 381 步、找不到邻居按默认 1 小时、邻居偏移相同则 delta 取 0),因此**同一脚本在各平台答案一致**;而 PUC 的答案随宿主 libc 变化,所以在非 glibc 宿主(macOS/BSD)上,少数贴着 transition 的输入会与本地编译的 PUC 不同。产品是纯 Go(cgo 只在 internal/oracle 的 build tag 后面),调不到宿主 mktime;逐平台重写各家 libc 的搜索会把一个可验证的答案换成几个无法验证的答案。glibc 上实测零差异:598 个时区 × 1850-2199 九个年份共 21528 例"},
	{"io 库不提供 open/popen/tmpfile/seek", "三个标准流与 io.read / io.lines / :write / :close / :flush / :read / :lines 现已提供(file-handle userdata,type() 报 userdata,与 lua5.1 一致);仍缺的是需要真实文件的部分——io.open / io.popen / io.tmpfile / f:seek,按 10 §11 的 ❌ 列。io.lines(filename) 因此会抬错而不是静默返回空迭代器"},
	{"getfenv/setfenv", "不在 10 §11 提供面任何列;唯一设计出处是 P2 不升层形状 F4(p2-bridge §358)——按设计豁免"},
	{"load(func) 渐进分块语义差", "已实现 reader 循环全量拼接;与 5.1 流式编译的差异仅在超大 chunk 内存峰值,语义等价"},
	{"os.exit 真退出", "10 §11:默认不真退出宿主进程(嵌入式安全)"},
	{"collectgarbage step/setstepmul 精确语义", "10 §13:STW GC 无增量调参,占位返回;数值不可逐字节比"},
	{"pattern 灾难性回溯有界失败", "回溯预算 1<<20 步:`.*.+%A*x` 级灾难回溯报 'pattern too complex',oracle 慢速算完——嵌入式防挂起裁量(官方成功/本实现报错的有意分歧)"},
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

// stdlibNamesSrc lists every string-keyed global, every field of the library tables, the file-handle
// metatable and methods, and the string metatable -- one "name : type" per line, sorted. The package
// table is not descended into (its contents are paths and loader state, not library surface).
const stdlibNamesSrc = `
local out = {}
local function keys(t)
  local ks = {}
  for k in pairs(t) do if type(k) == "string" then ks[#ks + 1] = k end end
  table.sort(ks)
  return ks
end
for _, k in ipairs(keys(_G)) do
  local v = _G[k]
  out[#out + 1] = k .. " : " .. type(v)
  if type(v) == "table" and v ~= _G and k ~= "package" then
    for _, k2 in ipairs(keys(v)) do out[#out + 1] = k .. "." .. k2 .. " : " .. type(v[k2]) end
  end
end
local fm = getmetatable(io.stdout)
if type(fm) == "table" then
  for _, k in ipairs(keys(fm)) do out[#out + 1] = "(file metatable)." .. k .. " : " .. type(fm[k]) end
  if type(fm.__index) == "table" and fm.__index ~= fm then
    for _, k in ipairs(keys(fm.__index)) do out[#out + 1] = "(file methods)." .. k .. " : " .. type(fm.__index[k]) end
  end
end
local sm = getmetatable("")
for _, k in ipairs(keys(sm)) do out[#out + 1] = "(string metatable)." .. k .. " : " .. type(sm[k]) end
return table.concat(out, "\n")`

// stdlibAbsent is every name official Lua 5.1.5 defines that wangshu does not, and why. It is the
// executable form of the "not provided" rows in the README and of `exemptions` above: a name missing
// from wangshu that is NOT listed here fails TestDiff_StdlibNames, and so does a listed name that
// wangshu has since gained (the entry must then be deleted).
//
// There is deliberately no matching table for the other direction. wangshu must define nothing that
// 5.1 lacks; an extra name is how #329 happened (table.unpack is 5.2's, and the ipairs iterator was
// published as a global), and nothing noticed for five months because the only executor of "the name
// sets must match" was the hand-written exemptions list, which records what is missing and cannot see
// what is extra.
var stdlibAbsent = map[string]string{
	"getfenv : function":  "exemptions: getfenv/setfenv",
	"setfenv : function":  "exemptions: getfenv/setfenv",
	"module : function":   "exemptions: require/module/package",
	"require : function":  "exemptions: require/module/package",
	"package : table":     "exemptions: require/module/package",
	"newproxy : function": "undocumented 5.1 function creating a zero-size userdata; not in the 10 section 11 surface",
	// Not a library name: lua.c, the standalone interpreter this test runs, sets it to the command
	// line. A host embedding the library (wangshu's situation) never has it.
	"arg : table": "set by the standalone lua.c interpreter, not by the library",

	"string.dump : function": "exemptions: string.dump",

	"os.execute : function":   "exemptions: os.execute",
	"os.exit : function":      "10 section 9.3: must not exit the host process; not registered",
	"os.remove : function":    "10 section 9.3: file-system side effect; not registered",
	"os.rename : function":    "10 section 9.3: file-system side effect; not registered",
	"os.tmpname : function":   "10 section 9.3: file-system side effect; not registered",
	"os.setlocale : function": "10 section 9.4: pure Go has no C locale; not registered",

	"io.open : function":    "exemptions: io library lacks open/popen/tmpfile/seek",
	"io.popen : function":   "exemptions: io.popen/io.tmpfile",
	"io.tmpfile : function": "exemptions: io.popen/io.tmpfile",
	"io.close : function":   "10 section 10: the global form depends on io.open; standard-stream f:close() is provided",
	"io.flush : function":   "10 section 10: default-output management depends on io.output; f:flush() is provided",
	"io.input : function":   "10 section 10: default streams are fixed to the process stdin/stdout",
	"io.output : function":  "10 section 10: default streams are fixed to the process stdin/stdout",
	"io.type : function":    "10 section 10: not provided while every handle is a standard stream",

	"(file metatable).__gc : function":    "no real files to close on collection",
	"(file metatable).seek : function":    "exemptions: io library lacks open/popen/tmpfile/seek",
	"(file metatable).setvbuf : function": "standard streams are unbuffered; nothing to configure",

	"debug.debug : function":        "exemptions: debug library provides only traceback/getinfo",
	"debug.getfenv : function":      "exemptions: debug library provides only traceback/getinfo",
	"debug.gethook : function":      "exemptions: debug library provides only traceback/getinfo",
	"debug.getlocal : function":     "exemptions: debug library provides only traceback/getinfo",
	"debug.getmetatable : function": "exemptions: debug library provides only traceback/getinfo",
	"debug.getregistry : function":  "exemptions: debug library provides only traceback/getinfo",
	"debug.getupvalue : function":   "exemptions: debug library provides only traceback/getinfo",
	"debug.setfenv : function":      "exemptions: debug library provides only traceback/getinfo",
	"debug.sethook : function":      "exemptions: debug library provides only traceback/getinfo",
	"debug.setlocal : function":     "exemptions: debug library provides only traceback/getinfo",
	"debug.setmetatable : function": "exemptions: debug library provides only traceback/getinfo",
	"debug.setupvalue : function":   "exemptions: debug library provides only traceback/getinfo",
}

// TestDiff_StdlibNames compares the standard library's whole name set against a live lua5.1, in both
// directions. It is the existence differential that 10 section 12.3 calls for.
func TestDiff_StdlibNames(t *testing.T) {
	oracle := findOracle()
	if oracle == "" {
		t.Skip("lua5.1 oracle not found on PATH; skipping difftest")
	}
	lines := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
			if l != "" {
				m[l] = true
			}
		}
		return m
	}
	puc := lines(runOracle(t, oracle, wrapForOracle(stdlibNamesSrc)))
	got := lines(runWangshu(t, stdlibNamesSrc))
	if len(puc) < 100 || len(got) < 60 {
		t.Fatalf("name dump looks truncated: lua5.1 %d lines, wangshu %d lines", len(puc), len(got))
	}

	var extra, missing, stale []string
	for l := range got {
		if !puc[l] {
			extra = append(extra, l)
		}
	}
	for l := range puc {
		if !got[l] && stdlibAbsent[l] == "" {
			missing = append(missing, l)
		}
	}
	for l := range stdlibAbsent {
		if got[l] || !puc[l] {
			stale = append(stale, l)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	sort.Strings(stale)
	if len(extra) > 0 {
		t.Errorf("wangshu defines names official Lua 5.1 does not (never allowed):\n  %s", strings.Join(extra, "\n  "))
	}
	if len(missing) > 0 {
		t.Errorf("official Lua 5.1 defines names wangshu lacks and stdlibAbsent does not list:\n  %s", strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("stdlibAbsent lists names that wangshu now has, or that lua5.1 does not define:\n  %s", strings.Join(stale, "\n  "))
	}
}
