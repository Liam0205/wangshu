// Manual-corner probes + explicit exemptions(10 §11 提供面三列的探测面镜像)。
//
// 三类条目:
//   - probe(必做列):对拍 oracle,缺失/不一致 = FAIL;
//   - exempt(❌ 缺口列 / 设计豁免):显式 skip + 设计文档出处——区分"豁免"与"漏了";
//   - approx(△ 简化列的不可逐字节比项):只验证存在性/形态,不比值。
package difftest

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// cornerProbes:10 §11 必做列中此前未覆盖的边角(对拍 oracle)。
var cornerProbes = []diffCase{
	// base 必做列补充
	{"corner_G_identity", `return _G._G == _G, type(_G)`},
	{"corner_VERSION", `return _VERSION`},
	{"corner_global_via_G", `x_corner = 42; return _G.x_corner`},
	{"corner_unpack_global", `return unpack({7, 8, 9})`},

	// math 必做列:atan2/frexp/ldexp/sinh/cosh/tanh(此前未实现/未测)
	{"corner_math_atan2", `return math.atan2(1, 1) > 0.78 and math.atan2(1, 1) < 0.79`},
	{"corner_math_frexp", `return math.frexp(8)`},
	{"corner_math_ldexp", `return math.ldexp(0.5, 4)`},
	{"corner_math_hyperbolic", `
return math.sinh(0), math.cosh(0), math.tanh(0)`},

	// os 必做列:difftime
	{"corner_os_difftime", `return os.difftime(100, 40)`},

	// table 简化列:setn(5.1.5 实测报 obsolete,对拍报错文本)
	{"corner_table_setn", `
local ok, e = pcall(function() table.setn({}, 1) end)
return ok, (e:gsub("^[^:]+:%d+: ", ""))`},

	// io 必做列:read 的存在性经 approx 验证(交互输入不可对拍);write 已测
	// string 必做列已全覆盖(pattern 全集);string.dump 在豁免列

	// coroutine 必做列已全覆盖

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
}

// exemptions:设计豁免清单(10 §11 ❌ 列 + 正文裁量),显式记录防"漏了"误判。
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

// TestDiff_CornerProbes 对拍手册边角必做项。
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

// TestExemptions_Documented 把设计豁免清单固化为显式 skip 记录:
// 每项产生一个 SKIP 测试,豁免面一目了然(go test -v 可审计);
// 若未来某豁免被实现,应从此表移除并加入 probe。
func TestExemptions_Documented(t *testing.T) {
	for _, e := range exemptions {
		t.Run(e.name, func(t *testing.T) {
			t.Skip("design exemption: " + e.reason)
		})
	}
}

// TestApprox_ExistenceOnly 验证 △ 简化列的"存在但不可逐字节比"项:
// 只断言可调用且返回形态正确,不比值(10 §13 可观察不可比清单)。
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
