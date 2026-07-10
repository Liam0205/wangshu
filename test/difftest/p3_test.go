//go:build wangshu_p3 && wangshu_profile

// P3 层间差分套(docs/design/p3-wasm-tier/08-testing-strategy.md V1-V13 / V17-V18)。
//
// 三方对拍,**全部 byte-equal** 才算过:
//   - oracle      = 官方 lua5.1(与 difftest_test.go 同源的语义基准);
//   - crescent    = wangshu force-all OFF(纯解释器,层间基线);
//   - gibbous     = wangshu force-all ON(所有可编译 Proto 升 wazero 执行)。
//
// 仅 `wangshu_p3 && wangshu_profile` build 跑:p3 提供真 gibbous Compiler + 收养 wazero
// memory;profile 启用 OnEnter/OnBackEdge 采样(force-all 经它触发 considerPromotion)。
//
// **升层时序**:doCall 的 gibbous 分支(call.go §VS0-d)只在 Proto **已升层**时跳
// wazero;force-all 下 OnEnter 在帧入口触发升层,故一个 Proto 的**首次**调用仍跑
// crescent(升层发生在它入帧之后),**第二次起**才走 gibbous。因此每个核函数都设计成
// 被重复调用(循环/递归/多次 invoke),确保 gibbous 路径真被覆盖。

package difftest

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// runWangshuTiered 用 wangshu 跑脚本,force 控制是否强制全升(true=gibbous 路径)。
// 复用 difftest 的 return-值 Display 对拍形态(同 runWangshu)。
func runWangshuTiered(t *testing.T, src string, force bool) string {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "p3diff")
	if err != nil {
		t.Fatalf("wangshu compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(force)
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("wangshu run (force=%v): %v", force, err)
	}
	parts := make([]string, len(results))
	for i, r := range results {
		parts[i] = r.Display()
	}
	return strings.Join(parts, "\t") + "\n"
}

// p3Corpus 是 V1-V13 各形状的层间用例。每个核都被**重复调用**(循环体或多次 invoke),
// 保证升层后 gibbous 分支真被走到(首调跑 crescent,二调起跑 gibbous)。
//
// 这些核都包成**非 vararg 内层函数**再多次调用——Lua 主 chunk 是 vararg(F1 排除),
// 不会升层;真正升层的是被反复调的内层函数(其 Proto 经 force-all 重判 F7 放行)。
var p3Corpus = []diffCase{
	// —— V1 直线 / MOVE / LOADNIL —— (反复调,二调起走 gibbous)
	{"p3_straight_move", `
local function id(x) local y = x; return y end
local s = 0
for i = 1, 50 do s = s + id(i) end
return s`},
	{"p3_loadnil_multi", `
local function f(a, b, c) local x, y, z; x = a; return x + b + c end
local s = 0
for i = 1, 30 do s = s + f(i, i+1, i+2) end
return s`},

	// —— V2 算术快路径 + NaN 规范化 ——
	{"p3_arith_chain", `
local function calc(a, b) return a + b * 2 - b / 2 end
local s = 0
for i = 1, 40 do s = s + calc(i, i+1) end
return s`},
	{"p3_arith_mod_pow", `
local function f(x) return (x % 7) + (x * x) end
local s = 0
for i = 1, 40 do s = s + f(i) end
return s`},
	{"p3_unm_not", `
local function f(x) local n = -x; if not (n > 0) then return -n end return n end
local s = 0
for i = 1, 40 do s = s + f(i) end
return s`},
	// UNM of canonNaN (issue #107): -(0%0) sign-flips canonNaN into
	// value.Nil's bit pattern; the unguarded wasm fast path stored that
	// Nil and the next arithmetic op raised "attempt to perform
	// arithmetic on a nil value" from the second Run on (post-promotion).
	{"p3_unm_nan_alias", `
local function f() local a = 0%0 a = -a%1 return tostring(a) end
local s
for i = 1, 40 do s = f() end
return s`},

	// —— V3-V4 比较 + 控制流(if/while/for/relooper) ——
	{"p3_compare_branch", `
local function clamp(x) if x < 10 then return 10 elseif x > 90 then return 90 end return x end
local s = 0
for i = 1, 100 do s = s + clamp(i) end
return s`},
	{"p3_while_inner", `
local function countdown(n) local c = 0; while n > 0 do c = c + n; n = n - 1 end return c end
local s = 0
for i = 1, 40 do s = s + countdown(i) end
return s`},
	{"p3_nested_for", `
local function grid(n) local c = 0; for a = 1, n do for b = 1, n do c = c + 1 end end return c end
local s = 0
for i = 1, 20 do s = s + grid(i) end
return s`},

	// —— V5 数值 for 累加(回边 safepoint 密集) ——
	{"p3_for_accumulate", `
local function sumto(n) local s = 0; for i = 1, n do s = s + i * 2 - 1 end return s end
local total = 0
for k = 1, 30 do total = total + sumto(k) end
return total`},
	{"p3_for_step", `
local function f(n) local s = 0; for i = n, 1, -2 do s = s + i end return s end
local s = 0
for i = 1, 40 do s = s + f(i) end
return s`},

	// —— V6-V7 表 IC:GETTABLE/SETTABLE/GETGLOBAL/SETGLOBAL/SELF/NEWTABLE/SETLIST ——
	{"p3_table_array_ic", `
local function f(n) local t = {}; for i = 1, n do t[i] = i * i end local s = 0; for i = 1, n do s = s + t[i] end return s end
local s = 0
for i = 1, 30 do s = s + f(i) end
return s`},
	{"p3_table_hash_ic", `
local function f(x) local t = { a = x, b = x * 2 }; t.c = t.a + t.b; return t.c end
local s = 0
for i = 1, 40 do s = s + f(i) end
return s`},
	{"p3_newtable_list", `
local function f(x) local t = { x, x+1, x+2 }; return t[1] + t[2] + t[3] end
local s = 0
for i = 1, 40 do s = s + f(i) end
return s`},
	{"p3_self_method", `
local function f(x) local o = { v = x }; function o:get() return self.v end return o:get() + o:get() end
local s = 0
for i = 1, 40 do s = s + f(i) end
return s`},

	// —— V8 CALL 三向 + base 刷新(嵌套调用 / 递归) ——
	{"p3_recursive_fib", `
local function fib(n) if n < 2 then return n end return fib(n-1) + fib(n-2) end
return fib(18)`},
	{"p3_nested_call", `
local function inner(x) return x * 3 end
local function outer(x) return inner(x) + inner(x + 1) end
local s = 0
for i = 1, 40 do s = s + outer(i) end
return s`},

	// —— V9 TAILCALL ——
	{"p3_tail_loop", `
local function loop(n, acc) if n == 0 then return acc end return loop(n - 1, acc + n) end
local s = 0
for i = 1, 30 do s = s + loop(i, 0) end
return s`},

	// —— V10 闭包 / upvalue(CLOSURE/CLOSE) ——
	{"p3_closure_counter", `
local function make() local n = 0; return function() n = n + 1; return n end end
local s = 0
for i = 1, 40 do local c = make(); c(); c(); s = s + c() end
return s`},
	{"p3_upvalue_share", `
local function f(base) local acc = base; local function add(x) acc = acc + x end add(1); add(2); add(3); return acc end
local s = 0
for i = 1, 40 do s = s + f(i) end
return s`},

	// —— V11 TFORLOOP 泛型 for(自定义迭代器,避开 ipairs import) ——
	{"p3_tforloop_custom", `
local function iter(t, i) i = i + 1; if t[i] then return i, t[i] end end
local function f(n) local t = {}; for k = 1, n do t[k] = k * 10 end local s = 0; for _, v in iter, t, 0 do s = s + v end return s end
local s = 0
for i = 1, 25 do s = s + f(i) end
return s`},

	// —— V12 慢路径(string coercion / 元方法 / 比较) ——
	{"p3_string_coerce", `
local function f(x) return ("" .. x) .. "!" end
local out = ""
for i = 1, 10 do out = out .. f(i) end
return out`},
	{"p3_meta_index", `
local base = { v = 100 }
local function f(x) local t = setmetatable({ k = x }, { __index = base }); return t.k + t.v end
local s = 0
for i = 1, 40 do s = s + f(i) end
return s`},

	// —— V13 混合核(算术 + 表 + 调用 + 控制流综合) ——
	{"p3_mixed_kernel", `
local function process(data, n)
  local sum, max = 0, 0
  for i = 1, n do
    local v = data[i] * 2 + 1
    sum = sum + v
    if v > max then max = v end
  end
  return sum + max
end
local function f(n) local d = {}; for i = 1, n do d[i] = i end return process(d, n) end
local s = 0
for i = 1, 25 do s = s + f(i) end
return s`},

	// -- issue #91: top-level if/else diamonds (not inside any loop) --
	// the relooper's block-scope nesting repair missed the symmetric
	// partial-overlap direction, so these shapes all CFAILed and stayed
	// on the interpreter. After the fix they must promote and stay
	// byte-equal (the promotion assertion lives in
	// TestP3_TopLevelDiamondPromotes).
	{"p3_iss91_diamond", `
local function k(a, b) local r = 0 if a < b then r = a else r = b end return r end
local s = 0
for i = 1, 50 do s = s + k(i, 25) end
return s`},
	{"p3_iss91_single_arm_if", `
local function k(a, b) local r = b if a < b then r = a end return r end
local s = 0
for i = 1, 50 do s = s + k(i, 25) end
return s`},
	{"p3_iss91_nested_diamond", `
local function k(a, b, c)
  local r = 0
  if a < b then
    if a < c then r = a else r = c end
  else
    if b < c then r = b else r = c end
  end
  return r
end
local s = 0
for i = 1, 50 do s = s + k(i, 25, 37) end
return s`},
	{"p3_iss91_diamond_then_loop", `
local function k(a, b, n)
  local r = 0
  if a < b then r = a else r = b end
  for i = 1, n do r = r + 1 end
  return r
end
local s = 0
for i = 1, 50 do s = s + k(i, 25, 3) end
return s`},
	{"p3_iss91_early_return", `
local function k(a, b) if a < b then return a end return b end
local s = 0
for i = 1, 50 do s = s + k(i, 25) end
return s`},
	{"p3_iss91_elseif_chain", `
local function k(x)
  if x < 3 then return 1
  elseif x < 6 then return 2
  elseif x < 9 then return 3
  else return 4 end
end
local s = 0
for i = 1, 12 do s = s + k(i) end
return s`},
}

// TestP3_Tiered 三方对拍:oracle / crescent / gibbous 全 byte-equal(V1-V13)。
func TestP3_Tiered(t *testing.T) {
	oracle := findOracle()
	for _, c := range p3Corpus {
		t.Run(c.name, func(t *testing.T) {
			crescent := runWangshuTiered(t, c.src, false)
			gibbous := runWangshuTiered(t, c.src, true)
			// 层间硬门:crescent vs gibbous 必须逐字节一致(P3 正确性轴核心)。
			if crescent != gibbous {
				t.Errorf("层间分歧 (crescent vs gibbous):\n  crescent: %q\n  gibbous:  %q", crescent, gibbous)
			}
			// 锚定官方 lua5.1(可用时)——确保两层都对、不是一起错。
			if oracle != "" {
				want := runOracle(t, oracle, wrapForOracle(c.src))
				if gibbous != want {
					t.Errorf("gibbous vs oracle byte-diff:\n  gibbous: %q\n  oracle:  %q", gibbous, want)
				}
			}
		})
	}
}

// TestP3_TopLevelDiamondPromotes — issue #91 prove-the-path: beyond
// byte-equality we must prove the top-level-diamond kernels actually
// PROMOTED. Before the fix these shapes CFAILed (improper scope
// overlap) and silently fell back to the interpreter, and the tier
// diff stayed green regardless — green alone doesn't prove the
// promoted path was under test, hence the PromotionCount white-box
// assertion.
func TestP3_TopLevelDiamondPromotes(t *testing.T) {
	srcs := map[string]string{
		"diamond": `
local function k(a, b) local r = 0 if a < b then r = a else r = b end return r end
local s = 0
for i = 1, 50 do s = s + k(i, 25) end
return s`,
		"single_arm_if": `
local function k(a, b) local r = b if a < b then r = a end return r end
local s = 0
for i = 1, 50 do s = s + k(i, 25) end
return s`,
		"early_return": `
local function k(a, b) if a < b then return a end return b end
local s = 0
for i = 1, 50 do s = s + k(i, 25) end
return s`,
	}
	for name, src := range srcs {
		t.Run(name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(src), "p3iss91")
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			st := wangshu.NewState(wangshu.Options{})
			st.SetForceAllPromote(true)
			if _, err := prog.Run(st); err != nil {
				t.Fatalf("run: %v", err)
			}
			if st.PromotionCount() == 0 {
				t.Fatal("PromotionCount = 0; top-level diamond kernel did not promote " +
					"(relooper scope overlap regression, issue #91)")
			}
		})
	}
}

// TestP3_TieredSeedCorpus 复用 difftest 71 种子,crescent vs gibbous 层间对拍。
//
// 种子核多数是主 chunk 直接表达式(vararg,不升层),但内含的循环/递归/闭包子函数
// 在 force-all 下会升层——本测覆盖「升层与不升层混合执行」的整体 byte-equal(V13)。
func TestP3_TieredSeedCorpus(t *testing.T) {
	oracle := findOracle()
	for _, c := range seedCorpus {
		t.Run(c.name, func(t *testing.T) {
			crescent := runWangshuTiered(t, c.src, false)
			gibbous := runWangshuTiered(t, c.src, true)
			if crescent != gibbous {
				t.Errorf("层间分歧 (crescent vs gibbous):\n  crescent: %q\n  gibbous:  %q", crescent, gibbous)
			}
			if oracle != "" {
				want := runOracle(t, oracle, wrapForOracle(c.src))
				if gibbous != want {
					t.Errorf("gibbous vs oracle byte-diff:\n  gibbous: %q\n  oracle:  %q", gibbous, want)
				}
			}
		})
	}
}

// TestP3_GCStressTiered V5/V13:GC 压力模式下层间仍 byte-equal。
//
// stressMode 下 gcPending 标志恒 1 → gibbous 回边每迭代仍跨层调 h_safepoint → 每个
// safepoint 强制 full Collect。验证「升层 + 高频 GC」组合下 gibbous 与 crescent 逐字节
// 一致(GC 透明性 × 层间一致性的交叉验证)。
func TestP3_GCStressTiered(t *testing.T) {
	runStress := func(t *testing.T, src string, force bool) string {
		t.Helper()
		prog, err := wangshu.Compile([]byte(src), "p3stress")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		st := wangshu.NewState(wangshu.Options{})
		st.SetGCStressMode(true)
		st.SetForceAllPromote(force)
		results, err := prog.Run(st)
		if err != nil {
			t.Fatalf("run (force=%v): %v", force, err)
		}
		parts := make([]string, len(results))
		for i, r := range results {
			parts[i] = r.Display()
		}
		return strings.Join(parts, "\t") + "\n"
	}
	// 分配密集核(NEWTABLE/SETLIST/闭包反复分配,触发 GC)。
	allocHeavy := []diffCase{
		{"stress_table_alloc", `
local function f(n) local t = {}; for i = 1, n do t[i] = { i, i * 2 } end local s = 0; for i = 1, n do s = s + t[i][1] + t[i][2] end return s end
local s = 0
for i = 1, 20 do s = s + f(i) end
return s`},
		{"stress_closure_alloc", `
local function make(x) return function() return x * 2 end end
local s = 0
for i = 1, 40 do local c = make(i); s = s + c() end
return s`},
		{"stress_string_concat", `
local function f(n) local out = ""; for i = 1, n do out = out .. tostring(i) end return #out end
local s = 0
for i = 1, 20 do s = s + f(i) end
return s`},
	}
	for _, c := range allocHeavy {
		t.Run(c.name, func(t *testing.T) {
			crescent := runStress(t, c.src, false)
			gibbous := runStress(t, c.src, true)
			if crescent != gibbous {
				t.Errorf("GC stress 层间分歧:\n  crescent: %q\n  gibbous:  %q", crescent, gibbous)
			}
		})
	}
}

// TestP3_ConcurrentForceAll V18(-race):多 State 并发 force-all-gibbous。
//
// 每个 goroutine 独立 State + 独立 force-all,跑同一脚本,验证并发升层无数据竞争
// (gibbousCodes map 经 compileMu 守护;profileTable State 私有)。`go test -race` 下
// 任一竞争即报告。结果一致性顺带校验。
func TestP3_ConcurrentForceAll(t *testing.T) {
	src := `
local function fib(n) if n < 2 then return n end return fib(n-1) + fib(n-2) end
local function sumto(n) local s = 0; for i = 1, n do s = s + i end return s end
return fib(15) + sumto(100)`
	const goroutines = 8
	prog, err := wangshu.Compile([]byte(src), "p3race")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// 先单跑拿期望值。
	want := runWangshuTiered(t, src, true)

	results := make([]string, goroutines)
	done := make(chan int, goroutines)
	for g := 0; g < goroutines; g++ {
		go func(idx int) {
			defer func() { done <- idx }()
			st := wangshu.NewState(wangshu.Options{})
			st.SetForceAllPromote(true)
			out, e := prog.Run(st)
			if e != nil {
				results[idx] = "ERR: " + e.Error()
				return
			}
			parts := make([]string, len(out))
			for i, r := range out {
				parts[i] = r.Display()
			}
			results[idx] = strings.Join(parts, "\t") + "\n"
		}(g)
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
	for g := 0; g < goroutines; g++ {
		if results[g] != want {
			t.Errorf("goroutine %d 结果分歧:\n  got:  %q\n  want: %q", g, results[g], want)
		}
	}
}
