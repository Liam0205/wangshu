// Manual-corner probes + explicit exemptions (a probe-surface mirror of the
// three provisioning columns in 10 §14).
//
// Three kinds of entries:
//   - probe (required column): difftest oracle; missing/mismatched = FAIL;
//   - exempt (❌ gap column / design exemption): explicit skip + design-doc
//     citation — distinguishes "exempted" from "missed";
//   - approx (△ simplified column, items not byte-comparable): only checks
//     existence/shape, does not compare values.
package difftest

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// cornerProbes: corners in the required columns of 10 §14 not covered before
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

	// collectgarbage: setpause / setstepmul return the previous value (200 by default), the second
	// argument goes through luaL_optint (missing = 0, numeric strings, truncation, int narrowing),
	// and collect / stop / restart return 0 whatever the second argument is. The differential fuzz stubs
	// collectgarbage out, so these probes are what compares it with lua5.1.
	{"corner_gc_setpause_previous", `return collectgarbage("setpause", 200), collectgarbage("setpause", 150), collectgarbage("setpause", 200)`},
	{"corner_gc_setstepmul_previous", `return collectgarbage("setstepmul", 200), collectgarbage("setstepmul", 300), collectgarbage("setstepmul", 200)`},
	{"corner_gc_set_without_value", `return collectgarbage("setpause"), collectgarbage("setpause", 200), collectgarbage("setstepmul"), collectgarbage("setstepmul", nil), collectgarbage("setstepmul", 200)`},
	{"corner_gc_set_conversions", `return collectgarbage("setpause", 150.7), collectgarbage("setpause", "120"), collectgarbage("setpause", -5), collectgarbage("setpause", 2^31), collectgarbage("setpause", 2^31 - 1), collectgarbage("setpause", -2^31), collectgarbage("setpause", 200)`},
	// The second argument narrows in two steps, double -> lua_Integer (64 bits) -> int: a value
	// beyond int32 keeps its low 32 bits. A direct conversion to int32 gives the same answers as
	// this for 2^31 on amd64 and different ones here.
	{"corner_gc_set_conversions_beyond_int32", `return collectgarbage("setpause", 2^32 + 5), collectgarbage("setpause", 4294967296 * 3 + 7), collectgarbage("setpause", -2147483649), collectgarbage("setpause", 2^53), collectgarbage("setstepmul", 2^31 + 2^32), collectgarbage("setstepmul", 200), collectgarbage("setpause", 200)`},
	{"corner_gc_set_bad_value", `return select(2, pcall(collectgarbage, "setpause", "x")), select(2, pcall(collectgarbage, "setstepmul", {}))`},
	// The option is checked before the second argument.
	{"corner_gc_bad_option_and_bad_value", `return select(2, pcall(collectgarbage, "bogus", "x")), select(2, pcall(collectgarbage, "bogus")), select(2, pcall(collectgarbage, "count", "x")), select(2, pcall(collectgarbage, 0, "x")), select(2, pcall(collectgarbage, {}, "x")), select(2, pcall(collectgarbage, nil, "x"))`},
	// A file handle's close called with no argument checks the raw field [2] of the globals table
	// (liolib.c's io_close reads IO_OUTPUT from its environment).
	{"corner_io_close_without_handle_reads_globals_2", `
local out = {}
local function try(label)
  local ok, a, b = pcall(io.stdout.close)
  out[#out + 1] = label .. "=" .. tostring(ok) .. "," .. tostring(a) .. "," .. tostring(b)
end
try("nil")
rawset(_G, 2, 5) try("number")
rawset(_G, 2, "s") try("string")
rawset(_G, 2, {}) try("table")
rawset(_G, 2, io.stderr) try("stderr")
rawset(_G, 2, io.stdin) try("stdin")
rawset(_G, 2, nil)
setmetatable(_G, {__index = function(_, k) if k == 2 then return io.stderr end end}) try("index")
setmetatable(_G, nil)
rawset(_G, 2, io.stderr)
out[#out + 1] = "flush=" .. tostring(select(2, pcall(io.stdout.flush)))
rawset(_G, 2, nil)
return table.concat(out, " | ")`},
	{"corner_gc_pause_survives_collect", `return collectgarbage("setpause", 300), collectgarbage("collect"), collectgarbage(), collectgarbage("setpause", 200)`},
	{"corner_gc_other_options_return_zero", `return collectgarbage("stop", 5), collectgarbage("restart", 5), collectgarbage("collect", 5), collectgarbage("stop"), collectgarbage("restart")`},
	// A pause ratio of 0 collects again almost at once; the largest one practically never does. The
	// probe is only held by a weak table, so whether it is still there says whether an automatic
	// collection ran while 20000 small tables were allocated.
	{"corner_gc_setpause_takes_effect", `
local function probeSurvives(pause)
  collectgarbage("setpause", pause)
  collectgarbage()
  local probe = setmetatable({}, {__mode = "v"})
  probe[1] = {}
  local keep = {}
  for i = 1, 20000 do keep[i % 50 + 1] = {i} end
  local alive = probe[1] ~= nil
  collectgarbage("setpause", 200)
  collectgarbage()
  return alive
end
return probeSurvives(0), probeSurvives(2^31 - 1), probeSurvives(0)`},
	// Ratios in between, at amounts of allocation where the two collectors agree: with 50000 small
	// tables alive, 200 has collected by 160000 and by 250000 further tables, 400 and 1000 have not
	// by 100000. Where they do not agree is gcPauseMeaning below.
	{"corner_gc_setpause_ratios_in_between", gcPauseProbe + `
return probeSurvives(200, 160000), probeSurvives(400, 100000), probeSurvives(200, 250000), probeSurvives(1000, 100000)`},

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

// gcPauseProbe defines probeSurvives(pause, n): with 50000 small tables alive it sets the pause
// ratio, collects, and reports whether an object held only by a weak table is still there after n
// more small tables have been allocated, that is, whether no automatic collection ran. The step
// multiplier is raised so that lua5.1, whose collector is incremental, finishes a cycle in the step
// that starts it; with the default one a cycle started within n allocations may not have reached
// the probe yet.
const gcPauseProbe = `
local live = {}
for i = 1, 50000 do live[i] = {i} end
collectgarbage("setstepmul", 100000)
local function probeSurvives(pause, n)
  collectgarbage("setpause", pause)
  collectgarbage()
  local probe = setmetatable({}, {__mode = "v"})
  probe[1] = {}
  local keep = {}
  for i = 1, n do keep[i % 50 + 1] = {i} end
  local alive = probe[1] ~= nil
  collectgarbage("setpause", 200)
  collectgarbage()
  return alive
end`

// gcPauseMeaning is where the pause ratio means something else than in lua5.1: wangshu compares
// live * pause / 100 with the bytes allocated since the last collection, lua5.1 compares it with the
// total. A ratio p here therefore behaves roughly like p + 100 there: at 100 lua5.1 starts the
// next collection as soon as one has finished (it has collected well within the 5000 tables the
// probe allocates) and wangshu collects after as much as the live data has been allocated again; at
// 200 lua5.1 collects once the total has doubled and wangshu once it has tripled. Registered in
// exemptions; whether to change it is issue #353.
const gcPauseMeaning = gcPauseProbe + `
return probeSurvives(100, 5000), probeSurvives(200, 90000)`

// TestDiff_GCPauseMeaning pins both sides of gcPauseMeaning, so that the registration is revisited
// when either changes.
func TestDiff_GCPauseMeaning(t *testing.T) {
	const wantWangshu, wantOracle = "true\ttrue\n", "false\tfalse\n"
	if got := runWangshu(t, gcPauseMeaning); got != wantWangshu {
		t.Errorf("wangshu:\n  got:  %q\n  want: %q", got, wantWangshu)
	}
	oracle := findOracle()
	if oracle == "" {
		t.Skip("lua5.1 oracle not found on PATH; only wangshu's side was checked")
	}
	if got := runOracle(t, oracle, wrapForOracle(gcPauseMeaning)); got != wantOracle {
		t.Errorf("lua5.1 no longer differs as recorded:\n  got:      %q\n  recorded: %q", got, wantOracle)
	}
}

// exemptions: the design-exemption list (10 §14 ❌ columns + prose
// discretion), recorded explicitly to guard against "missed" misjudgments.
var exemptions = []struct {
	name   string
	reason string
}{
	{"rawlen", "5.2 特性,10 §14 base ❌ 列(5.1 无 rawlen,# 走 LEN)"},
	{"require/module/package", "10 §14 base ❌ 列 + §4.7:嵌入式宿主经 Compile 提供脚本,不从文件系统 require"},
	{"table.unpack/pack/move", "5.2+ 特性,10 §14 table ❌ 列;5.1 的 unpack 是全局函数"},
	{"string.dump", "10 §14 string ❌ 列(字节码序列化;自定义 ISA 不兼容官方 .luc)"},
	{"math.tointeger/type/maxinteger/mininteger", "5.3 整数特性,10 §14 math ❌ 列"},
	{"os.execute", "10 §14 os ❌ 列(安全:嵌入式 VM 不让脚本跑 shell)"},
	{"io.popen/io.tmpfile", "10 §14 io ❌ 列"},
	{"debug 库只提供 traceback/getinfo", "10 §14 把 debug.traceback/getinfo 列为要做的、其余列在 ❌ 列。现在这两个已提供;其余 12 个(sethook / gethook / getlocal / setlocal / getupvalue / setupvalue / getregistry / getmetatable / setmetatable / getfenv / setfenv / debug)不存在,各自的说明见 09 §13.3。getinfo 只填 P1 能诚实给出的字段,name / namewhat / nups 宁缺不假造;按查询形式列出的字段清单见 09 §13.4"},
	{"os.time{isdst=} 采用 glibc 的 mktime 搜索规则(跨平台统一)", "该字段的解析需要在 tzdata transition 表里搜索,而各家 libc 的搜索步长与顺序不同。wangshu 在所有平台上都用 glibc 的规则(601200 秒步长、每步先后再前、上限 381 步、找不到邻居按默认 1 小时、邻居偏移相同则 delta 取 0),因此**同一脚本在各平台答案一致**;而 PUC 的答案随宿主 libc 变化,所以在非 glibc 宿主(macOS/BSD)上,少数贴着 transition 的输入会与本地编译的 PUC 不同。产品是纯 Go(cgo 只在 internal/oracle 的 build tag 后面),调不到宿主 mktime;逐平台重写各家 libc 的搜索会把一个可验证的答案换成几个无法验证的答案。glibc 上实测零差异:598 个时区 × 1850-2199 九个年份共 21528 例"},
	{"io 库不提供 open/popen/tmpfile/seek", "三个标准流与 io.read / io.lines / :write / :close / :flush / :read / :lines 现已提供(file-handle userdata,type() 报 userdata,与 lua5.1 一致);仍缺的是需要真实文件的部分——io.open / io.popen / io.tmpfile / f:seek,按 10 §14 的 ❌ 列。io.lines(filename) 因此会抬错而不是静默返回空迭代器"},
	{"getfenv/setfenv", "10 §14 base ❌ 列;设计出处是 P2 不升层形状 F4(p2-bridge §358):改写函数环境与分层执行的形状分析冲突——按设计豁免"},
	{"load(func) 渐进分块语义差", "已实现 reader 循环全量拼接;与 5.1 流式编译的差异仅在超大 chunk 内存峰值,语义等价"},
	{"os.exit/remove/rename/tmpname/setlocale", "未注册（10 §9.5）。原设计是做简化版（os.exit 不真退出宿主进程、setlocale 只支持 C、文件操作受沙箱限制），一直没有做；要不要补见 issue #330"},
	{"io.close/flush/input/output/type", "未注册（10 §10.1.1）。默认输入输出固定为进程的 stdin/stdout；句柄上的 f:close() / f:flush() 已提供。要不要补见 issue #330"},
	{"collectgarbage step/setstepmul 精确语义", "10 §4.6:GC 是 stop-the-world,没有增量步进。step 做一次完整回收并返回 true(官方返回这一步有没有结束一轮回收);setstepmul 的值会保存并在下次调用时返回,但不影响回收"},
	{"collectgarbage setpause 的比例含义", "10 §4.6：返回值和参数转换与官方一致，比例的含义不同。望舒拿 live × 比例 / 100 与上次回收以来新分配的字节数比较，官方拿它与总字节数比较，所以望舒的比例 p 大约相当于官方的 p + 100（200 在官方是总量翻倍时回收，在望舒是总量到三倍时回收）；0 在望舒是每分配 arena 容量的 1/16 回收一次，在官方是几乎不停地回收。要不要改成官方的口径见 issue #353。TestDiff_GCPauseMeaning 固定两边各自的结果"},
	{"pattern 灾难性回溯有界失败", "回溯预算 1<<20 步:`.*.+%A*x` 级灾难回溯报 'pattern too complex',oracle 慢速算完——嵌入式防挂起裁量(官方成功/本实现报错的有意分歧)"},
	{"next 接受已删除、原字符串对象已被回收的键", "用 `t[k] = nil` 删掉的字符串键留在它的节点里，并且一直存活到这个节点被后来插入的键接管或者所在的表 rehash（06 §8.4），所以在那之前用内容相同的字符串调用 next(t, k) 就能从原位置继续，中间发生过几次回收都一样。官方的节点不让这样的键存活：它没有别的引用（比如某个函数的常量、别的表里还有值的条目的键或值、还在用的局部变量）时会被回收，回收之后 next 只按对象地址认它：重新拼出来的字符串如果没有正好分配在原对象的地址上，就报 invalid key to 'next'，分配在原地址上则成功；没有发生回收、这个字符串另有引用、或者它是官方永远不回收的字符串（luaS_fix：保留字、ltm.c 列的 17 个元方法名、`not enough memory`）时两边相同。弱表里值被回收器清掉的条目不在此列：它的字符串键两边都不保活（否则字符串作键的弱值缓存永远不释放键），键被回收之后望舒的 next 报错，官方按地址认；键是官方永远不回收的那几种字符串时官方的 next 总是成功；望舒里这个字符串没有别的引用时报错，另有引用时（例如 `__index` 是字符串元表的键）两边都成功。一个字符串在两边是否另有引用不一定相同，两个方向都有。官方的库表、`package` 表和注册表引用着 `execute`、`tmpname`、`path`、`_LOADED`、`FILE*` 这类名字，官方的函数原型还引用着自己的源名、局部变量名和 upvalue 名（调试信息），官方的弱表也从不清字符串（只作为另一张弱键表里条目的键、或者另一张弱值表里条目的值存在的字符串在官方一直活着，望舒会把这样的条目连同字符串清掉，issue #348），望舒没有这些引用，这些字符串作键的条目被清掉之后，官方的 `next` 成功、望舒报错。反过来，望舒有两种官方没有的引用：加载过的每个代码块的字符串常量一直存活，函数丢掉之后也是（issue #359），官方随函数一起回收；别的表里用赋 nil 删掉的键由那张表的节点保活（就是本条开头说的保活），官方的已删除节点不算引用。这两种字符串作键的条目被清掉之后，望舒的 `next` 成功。代价是内存：被删除的字符串键要等节点被接管或者表 rehash 才能回收，原地清空之后闲置的表会一直占着它放过的全部字符串键（要不要改见 issue #352）。TestDiff_NextFromCollectedStringKey 和 TestDiff_NextFromKeysOfClearedEntries 固定望舒一侧的结果，TestDiff_NextFromRemovedFixedKeys 固定脚本删掉的键是这几种字符串时两边相同，TestDiff_NextFromFixedKeysOfClearedEntries、TestDiff_NextFromRuntimeHeldKeysOfClearedEntries、TestDiff_NextFromDebugNamesOfClearedEntries 和 TestDiff_NextFromKeysHeldByWeakTables 固定被清掉的条目上两边各自的结果，TestDiff_NextFromConstantsOfDroppedChunks 和 TestDiff_NextFromKeysRemovedFromAnotherTable 固定望舒引用得更多的那个方向上望舒一侧的结果"},
	{"对已删除的键、或者赋过 nil 的不存在的键调用 next", "next(t, k) 在 k 当前不在表里时报不报错，取决于实现内部的节点状态（望舒看表里还有没有 k 的节点，官方在回收之后还按对象地址认已删除的键），两边可能不同，两个方向都有。已知的原因有五个。一是哈希部分的布局：主位置正好是它的新键会接管这个节点，找不到空闲节点的插入会 rehash、丢掉所有已删除的键，而望舒哈希部分的大小和主位置与官方不同（issue #349）。二是回收：官方每次回收都把已删除节点里可回收的键（字符串、表、函数等；数字和布尔值不算，它们两边都写回原节点）标成 dead key，不管键指向的对象是不是还活着（脚本删掉的键在键和值都是弱引用的表里除外：官方标记这种表时整张跳过，这些键不会变成 dead key，键另有引用时写回去两边都回原节点；删掉的字符串键没有别的引用、又不是官方永远不回收的那几种字符串时，官方把它回收，写回去的新字符串不在原地址上就按新键插入，望舒由节点保活、仍回原节点。回收器清掉的弱表条目没有这个例外：官方清值的同时就把它的键标成 dead key，三种弱表模式都一样），之后 next 仍然认得它，赋值却不认得，所以同一个键写回去时按新键插入，可能引起 rehash；望舒里已删除的键只要对象还活着，不管隔了几次回收都写回原来的节点，脚本删掉的字符串键总是活着（06 §8.4）。三是不留下值的赋值：官方的 luaH_set 在知道要写的值是 nil、或者这次赋值会交给 __newindex 之前就给键建了节点，所以给不存在的键赋 nil（`t.x = nil`、`rawset(t, k, nil)`）或者被 __newindex 接走的赋值也会留下一个已删除的键、占掉一个空闲节点、可能引起 rehash；望舒里这样的赋值不碰表（要不要改见 issue #349）。四是对象作键：表、函数、userdata、协程作键时两边都按对象的地址取哈希，两边分配器给的地址毫无关系，这样的键落在哪个节点、会不会接管已删除的节点也就不同；地址还取决于之前分配和回收过什么，所以删除之后又插入对象键时，望舒自己在不同的回收时机下结果也可能不同（含对象键的表的遍历顺序本来就是这样）。五是官方回收之后按地址认已删除的键：一个从来没有进过这张表的新对象，只要正好分配在某个已回收的键原来的地址上，官方的 next 也接受它（新字符串也一样，内容与原来的键不同时还要正好落在同一条冲突链上），望舒一律报错。手册规定遍历过程中给不存在的字段赋值之后行为未定义。TestDiff_NextOnRemovedKeyAfterStores 固定十段脚本两边各自的结果，TestNextOnRemovedKey_ObjectKeysInsertedAfterwards 在两种回收方式下执行第四种情况，TestDiff_NextFromKeysNeverInTheTable 固定第五种情况里望舒一侧的结果"},
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
-- Names that are one and the same function object (the LUA_COMPAT aliases): "a == b : alias".
local names, first = {}, {}
for _, k in ipairs(keys(_G)) do
  local v = _G[k]
  if type(v) == "function" then names[#names + 1] = {k, v} end
  if type(v) == "table" and v ~= _G and k ~= "package" then
    for _, k2 in ipairs(keys(v)) do
      if type(v[k2]) == "function" then names[#names + 1] = {k .. "." .. k2, v[k2]} end
    end
  end
end
for _, nv in ipairs(names) do
  if first[nv[2]] then out[#out + 1] = first[nv[2]] .. " == " .. nv[1] .. " : alias" else first[nv[2]] = nv[1] end
end
return table.concat(out, "\n")`

// stdlibAbsent is every name official Lua 5.1.5 defines that wangshu does not, and why. It is the
// per-name, failing counterpart of `exemptions` above: a name missing from wangshu that is NOT listed
// here fails TestDiff_StdlibNames, and so does a listed name that wangshu has since gained (the entry
// must then be deleted). A reason of the form "exemptions: X" points at the exemptions entry named X;
// TestStdlibAbsentReasonsResolve keeps those pointers from going stale.
//
// The README's "not provided" table is written from this map, and TestStdlibAbsentIsInREADME fails
// when a name listed here has no row there. Two entries are excused because they are not library API
// a script would call: arg, and the file metatable's __gc.
//
// There is deliberately no matching table for the other direction: wangshu must define nothing that
// 5.1 lacks (#329), so an extra name always fails. The hand-written exemptions list cannot enforce
// that; it records what is missing and cannot see what is extra.
var stdlibAbsent = map[string]string{
	"getfenv : function":  "exemptions: getfenv/setfenv",
	"setfenv : function":  "exemptions: getfenv/setfenv",
	"module : function":   "exemptions: require/module/package",
	"require : function":  "exemptions: require/module/package",
	"package : table":     "exemptions: require/module/package",
	"newproxy : function": "undocumented 5.1 function creating a zero-size userdata; not provided (10 section 14, base row)",
	// Not a library name: lua.c, the standalone interpreter this test runs, sets it to the command
	// line. A host embedding the library (wangshu's situation) never has it.
	"arg : table": "set by the standalone lua.c interpreter, not by the library",

	"string.dump : function": "exemptions: string.dump",

	"os.execute : function":   "exemptions: os.execute",
	"os.exit : function":      "exemptions: os.exit/remove/rename/tmpname/setlocale",
	"os.remove : function":    "exemptions: os.exit/remove/rename/tmpname/setlocale",
	"os.rename : function":    "exemptions: os.exit/remove/rename/tmpname/setlocale",
	"os.tmpname : function":   "exemptions: os.exit/remove/rename/tmpname/setlocale",
	"os.setlocale : function": "exemptions: os.exit/remove/rename/tmpname/setlocale",

	"io.open : function":    "exemptions: io 库不提供 open/popen/tmpfile/seek",
	"io.popen : function":   "exemptions: io.popen/io.tmpfile",
	"io.tmpfile : function": "exemptions: io.popen/io.tmpfile",
	"io.close : function":   "exemptions: io.close/flush/input/output/type",
	"io.flush : function":   "exemptions: io.close/flush/input/output/type",
	"io.input : function":   "exemptions: io.close/flush/input/output/type",
	"io.output : function":  "exemptions: io.close/flush/input/output/type",
	"io.type : function":    "exemptions: io.close/flush/input/output/type",

	"(file metatable).__gc : function":    "no real files to close on collection",
	"(file metatable).seek : function":    "exemptions: io 库不提供 open/popen/tmpfile/seek",
	"(file metatable).setvbuf : function": "writes to the standard streams are unbuffered and the buffering mode is not configurable",

	"debug.debug : function":        "exemptions: debug 库只提供 traceback/getinfo",
	"debug.getfenv : function":      "exemptions: debug 库只提供 traceback/getinfo",
	"debug.gethook : function":      "exemptions: debug 库只提供 traceback/getinfo",
	"debug.getlocal : function":     "exemptions: debug 库只提供 traceback/getinfo",
	"debug.getmetatable : function": "exemptions: debug 库只提供 traceback/getinfo",
	"debug.getregistry : function":  "exemptions: debug 库只提供 traceback/getinfo",
	"debug.getupvalue : function":   "exemptions: debug 库只提供 traceback/getinfo",
	"debug.setfenv : function":      "exemptions: debug 库只提供 traceback/getinfo",
	"debug.sethook : function":      "exemptions: debug 库只提供 traceback/getinfo",
	"debug.setlocal : function":     "exemptions: debug 库只提供 traceback/getinfo",
	"debug.setmetatable : function": "exemptions: debug 库只提供 traceback/getinfo",
	"debug.setupvalue : function":   "exemptions: debug 库只提供 traceback/getinfo",
}

// TestDiff_StdlibNames compares the standard library's whole name set against a live lua5.1, in both
// directions. It is the existence differential that 10 section 12.3 calls for.
//
// The dump also lists which names are one and the same function object. Official 5.1.5 has two such
// pairs, both LUA_COMPAT aliases (math.mod is fmod, string.gfind is gmatch), and a script can see the
// difference with ==, so the alias lines make identity part of this comparison.
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

// TestStdlibAbsentReasonsResolve keeps the "exemptions: X" reasons in stdlibAbsent pointing at an
// entry that exists. Without it, renaming or deleting an exemptions entry leaves the per-name table
// citing a category nobody can look up.
func TestStdlibAbsentReasonsResolve(t *testing.T) {
	names := map[string]bool{}
	for _, e := range exemptions {
		names[e.name] = true
	}
	for name, reason := range stdlibAbsent {
		if ref, ok := strings.CutPrefix(reason, "exemptions: "); ok && !names[ref] {
			t.Errorf("stdlibAbsent[%q] cites exemptions entry %q, which does not exist", name, ref)
		}
	}
}

// TestStdlibAbsentIsInREADME checks that every library name wangshu lacks is listed in the "not
// provided" section of both READMEs. The section is what a user reads to learn what their script
// cannot call, so it is held to the same per-name list as the test above.
//
// Only the second column of each row (the items) is read: the reason column is prose, and prose
// contains ordinary words such as "input" and "output". See readmeLists for what counts as listed.
func TestStdlibAbsentIsInREADME(t *testing.T) {
	excused := map[string]bool{"arg : table": true, "(file metatable).__gc : function": true}
	for _, readme := range readmeAbsentSections {
		items := readmeAbsentItems(t, readme.path, readme.heading)
		for entry := range stdlibAbsent {
			if excused[entry] {
				continue
			}
			name, _, _ := strings.Cut(entry, " : ")
			listed := readmeLists(items, name)
			if !listed {
				t.Errorf("%s: %q is in stdlibAbsent but no row of the \"not provided\" section lists it", readme.path, name)
			}
		}
	}
}

// readmeListsProvided is every name wangshu provides that the READMEs' "not provided, or different"
// section nevertheless names in its items column, and why it is there. The section lists differences
// as well as absences, so a few provided names belong in it.
var readmeListsProvided = map[string]string{
	"debug.traceback": "named on the debug row as one of the two functions that are provided",
	"debug.getinfo":   "named on the debug row as one of the two functions that are provided",
	"loadfile":        "provided, off by default",
	"dofile":          "provided, off by default",
	"io.lines":        "provided; the filename form raises",
	"string.char":     "provided; the row is about arguments outside the int range",
	"next":            "provided; the row is about calling it with a removed key after other keys were inserted",
	"type":            "not listed itself: the abbreviation of math.type on the 5.3 row reads as the global",
}

// TestReadmeListsOnlyAbsentNames is the other direction of TestStdlibAbsentIsInREADME: a name the
// "not provided" section lists must really be absent. When a missing function is added later,
// TestDiff_StdlibNames makes its stdlibAbsent entry go away, and this test then fails until the
// README stops saying the function is not provided. Names listed for a behavioural difference are
// recorded in readmeListsProvided, which must not go stale either.
//
// It needs no lua5.1: it compares the READMEs with what wangshu itself defines.
func TestReadmeListsOnlyAbsentNames(t *testing.T) {
	var provided []string
	for _, l := range strings.Split(strings.TrimSpace(runWangshu(t, stdlibNamesSrc)), "\n") {
		if name, _, ok := strings.Cut(l, " : "); ok && !strings.Contains(name, " == ") {
			provided = append(provided, name)
		}
	}
	if len(provided) < 60 {
		t.Fatalf("name dump looks truncated: %d names", len(provided))
	}
	for _, readme := range readmeAbsentSections {
		items := readmeAbsentItems(t, readme.path, readme.heading)
		listed := map[string]bool{}
		for _, name := range provided {
			if !readmeLists(items, name) {
				continue
			}
			listed[name] = true
			if readmeListsProvided[name] == "" {
				t.Errorf("%s: the \"not provided\" section lists %q, which wangshu provides; "+
					"drop it from the README, or record why it is listed in readmeListsProvided", readme.path, name)
			}
		}
		for name := range readmeListsProvided {
			if !listed[name] {
				t.Errorf("%s: readmeListsProvided has %q, which the section no longer lists or wangshu no longer provides",
					readme.path, name)
			}
		}
	}
}

// readmeItemCells returns the items column (the second one) of every table row in a README section,
// header and separator rows included; they match no name.
func readmeItemCells(section string) []string {
	var cells []string
	for _, l := range strings.Split(section, "\n") {
		if !strings.HasPrefix(l, "|") {
			continue
		}
		if cols := strings.Split(l, "|"); len(cols) >= 3 {
			cells = append(cells, cols[2])
		}
	}
	return cells
}

// readmeAbsentSections are the "not provided" sections of the two READMEs.
var readmeAbsentSections = []struct{ path, heading string }{
	{"../../README.md", "### 不提供或与官方不同的部分"},
	{"../../README.en.md", "### Not provided, or different from the official implementation"},
}

// readmeAbsentItems returns the items column of the table under heading in the README at path.
func readmeAbsentItems(t *testing.T, path, heading string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	_, section, ok := strings.Cut(string(raw), heading)
	if !ok {
		t.Fatalf("%s: heading %q not found", path, heading)
	}
	if i := strings.Index(section, "\n## "); i >= 0 {
		section = section[:i]
	}
	items := readmeItemCells(section)
	if len(items) < 8 {
		t.Fatalf("%s: only %d table rows under %q; the section moved or changed format", path, len(items), heading)
	}
	return items
}

// readmeLists reports whether name, a name in the form stdlibNamesSrc prints, is listed in one of
// the item cells.
//
//   - A library function lib.fn is listed by `lib.fn` spelled out, or by `fn` in a cell that names its
//     library (`lib.other` or `lib`): rows abbreviate, as in "`debug.sethook` / `getlocal`". The
//     abbreviation is not accepted when fn is the library's own name (debug.debug).
//   - A file method is listed by `f:name`.
//   - A global is listed by `name` in a cell that does not name a library which has a function of that
//     name missing too. Without that restriction the abbreviation `getfenv` on the debug row, written
//     for debug.getfenv, would also stand in for the global getfenv. A library's own name is exempt
//     from the restriction: `debug` is never the abbreviation of debug.debug (first rule), so it can
//     only mean the global debug, the library itself.
//
// Names must be in backticks. A bare word is not a listing.
func readmeLists(items []string, name string) bool {
	namesLib := func(cell, lib string) bool {
		return strings.Contains(cell, "`"+lib+".") || strings.Contains(cell, "`"+lib+"`")
	}
	if m, ok := strings.CutPrefix(name, "(file metatable)."); ok {
		for _, cell := range items {
			if strings.Contains(cell, "`f:"+m+"`") {
				return true
			}
		}
		return false
	}
	if lib, fn, ok := strings.Cut(name, "."); ok {
		for _, cell := range items {
			if strings.Contains(cell, "`"+name+"`") || fn != lib && namesLib(cell, lib) && strings.Contains(cell, "`"+fn+"`") {
				return true
			}
		}
		return false
	}
	var shadowing []string
	for entry := range stdlibAbsent {
		full, _, _ := strings.Cut(entry, " : ")
		if lib, fn, ok := strings.Cut(full, "."); ok && fn == name && lib != name && !strings.HasPrefix(full, "(") {
			shadowing = append(shadowing, lib)
		}
	}
next:
	for _, cell := range items {
		if !strings.Contains(cell, "`"+name+"`") {
			continue
		}
		for _, lib := range shadowing {
			if namesLib(cell, lib) {
				continue next
			}
		}
		return true
	}
	return false
}

// TestReadmeListsMatcher pins the rules of readmeLists that keep a name from counting as listed when
// the README does not actually list it; relaxing any of them lets TestStdlibAbsentIsInREADME pass with
// that name missing. It also pins the one rule in the other direction: a library's own name in
// backticks is seen as a listing, or TestReadmeListsOnlyAbsentNames could not see a row that calls a
// whole provided library not provided.
func TestReadmeListsMatcher(t *testing.T) {
	debugRow := "| debug | everything in `debug` except `traceback` (`sethook` / `getfenv` / `setfenv` / `debug.debug`) | hooks |"
	envRow := "| Environment ops | `getfenv` / `setfenv` | shape analysis |"
	ioRow := "| Default streams | `io.close` / `io.flush` / `io.type` | The default input and output are fixed |"
	for _, tc := range []struct {
		what string
		rows []string
		name string
		want bool
	}{
		{"abbreviation on its library's row", []string{debugRow}, "debug.sethook", true},
		{"abbreviation serves the library function", []string{debugRow}, "debug.getfenv", true},
		{"abbreviation does not serve the global of the same name", []string{debugRow}, "getfenv", false},
		{"global on a row of its own", []string{debugRow, envRow}, "getfenv", true},
		{"global's row does not serve the library function", []string{envRow}, "debug.getfenv", false},
		{"function named like its library, spelled out", []string{debugRow}, "debug.debug", true},
		{"function named like its library, only the library named", []string{"| debug | `debug` library: `sethook` | hooks |"}, "debug.debug", false},
		{"library in backticks is a listing of the global of that name", []string{debugRow}, "debug", true},
		{"library named only through its functions is not", []string{"| debug | the debug library except `traceback` (`sethook` / `debug.debug`) | hooks |"}, "debug", false},
		{"word in the reason column is not a listing", []string{ioRow}, "io.input", false},
		{"bare word in the items column is not a listing", []string{"| Default streams | `io.close`, input and output | fixed |"}, "io.input", false},
		{"spelled out", []string{"| Default streams | `io.input` / `io.output` | fixed |"}, "io.output", true},
		{"file method", []string{"| Files | `io.open` / `f:seek` | real files |"}, "(file metatable).seek", true},
		{"file method needs the f: form", []string{"| Files | `io.open` / `seek` | real files |"}, "(file metatable).seek", false},
	} {
		if got := readmeLists(readmeItemCells(strings.Join(tc.rows, "\n")), tc.name); got != tc.want {
			t.Errorf("%s: readmeLists(%q) = %v, want %v", tc.what, tc.name, got, tc.want)
		}
	}
}
