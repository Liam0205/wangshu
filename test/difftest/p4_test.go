//go:build wangshu_p4 && wangshu_profile

// P4 层间差分套(docs/design/p4-method-jit/08-testing-strategy.md V1-V13 / V17-V18)。
//
// **承外部 review 🔴 阻塞**:`make difftest-p4` 长期跑通用 `difftest_test.go`,
// **既不调 `SetForceAllPromote(true)` 也不设计成"重复调用"**——P4 路径在 difftest
// 全套层面**完全不被强制触达**。本文件对位 `p3_test.go` 同款形态,加 P4 build tag
// 专属 harness 修复这条整套层级 prove-the-path 缺口。
//
// 三方对拍,**全部 byte-equal** 才算过:
//   - oracle      = 官方 lua5.1(与 difftest_test.go 同源);
//   - crescent    = wangshu force-all OFF(纯解释器,层间基线);
//   - p4-jit      = wangshu force-all ON(所有可编译 Proto 升 P4 native 执行)。
//
// 仅 `wangshu_p4 && wangshu_profile` build 跑。
//
// **升层时序**(承 p3_test.go 同款):doCall 的 gibbous 分支(call.go §VS0-d)只在
// Proto **已升层**时跳 P4;force-all 下 OnEnter 在帧入口触发升层,故一个 Proto
// 的**首次**调用仍跑 crescent(升层发生在它入帧之后),**第二次起**才走 P4。
// 每个核函数被重复调用确保 P4 路径真触达。
//
// **P4 vs P3 形态差异**:P4 当前 SupportsAllOpcodes 白名单约 25 类形态 + 4 IC inline 族
// (PJ4 落地后扩到 IC 完整六路径),**但不支持复杂控制流 / 跨层递归 / TFORLOOP /
// __index 元方法链 / TAILCALL 等**——所以 p4Corpus 用例形态比 p3Corpus 更窄:精选
// P4 SupportsAllOpcodes 真接受的单 BB 单 RETURN 形态 + 表 IC 形态。

package difftest

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// runWangshuP4Tiered 用 wangshu 跑脚本,force 控制是否强制全升(true=P4 路径)。
// 与 runWangshuTiered 同款(p3_test.go),复制以避免 P3/P4 build tag 互斥重命名。
func runWangshuP4Tiered(t *testing.T, src string, force bool) string {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "p4diff")
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

// p4Corpus 是 P4 SupportsAllOpcodes 真接受形态的层间用例集。每个核被重复调用,
// 保证升层后 P4 分支真被走到(首调跑 crescent,二调起跑 P4 native)。
//
// **形态选择策略**(承 P4 SupportsAllOpcodes 当前白名单):
//   - 单 BB「值产生 + RETURN A 2 / RETURN A 1」单 op + RETURN 子集
//   - 双 op chain(MUL+ADD / ADD+MUL)
//   - 比较折叠(EQ/LT/LE + JMP + LOADBOOL×2 + RETURN)
//   - FORLOOP 字节级 inline(空 body / reg-limit / body inline 各形态)
//   - 表 IC 六路径(GetTable ArrayHit/NodeHit + SETTABLE ArrayHit/NodeHit
//   - SELF ArrayHit/NodeHit)
//
// **每个核外层 wrap**:用 outer 函数 + for loop 反复调内层 kernel,确保
// (1) outer chunk 长度 >= MinPromotableCodeLen=10 让 outer 也升层
// (2) inner kernel 被反复调让 P4 路径真触达(首调走 crescent,二调起走 P4)
var p4Corpus = []diffCase{
	// —— 值返回单 BB 形态(LOADK / LOADBOOL / LOADNIL / MOVE) ——
	{"p4_const_number", `
local function f() return 42 end
local s = 0
for i = 1, 30 do s = s + f() end
return s`},
	{"p4_move_arg", `
local function f(x) return x end
local s = 0
for i = 1, 30 do s = s + f(i) end
return s`},
	{"p4_loadbool", `
local function f() return true end
local count = 0
for i = 1, 30 do if f() then count = count + 1 end end
return count`},

	// —— 算术单 op + RETURN ——
	{"p4_arith_add", `
local function f(x, y) return x + y end
local s = 0
for i = 1, 30 do s = s + f(i, i + 1) end
return s`},
	{"p4_arith_mul_chain", `
local function f(x) return x * 2 + 1 end
local s = 0
for i = 1, 30 do s = s + f(i) end
return s`},

	// —— 比较折叠 ——
	{"p4_compare_eq", `
local function f(x) return x == 5 end
local count = 0
for i = 1, 10 do if f(i) then count = count + 1 end end
return count`},
	{"p4_compare_lt", `
local function f(x) return x < 5 end
local count = 0
for i = 1, 10 do if f(i) then count = count + 1 end end
return count`},

	// —— UNM / LEN / NOT ——
	{"p4_unm", `
local function f(x) return -x end
local s = 0
for i = 1, 20 do s = s + f(i) end
return s`},
	{"p4_not", `
local function f(x) return not x end
local count = 0
for i = 1, 10 do if f(i == 5) then count = count + 1 end end
return count`},

	// —— FORLOOP 字节级 inline(PJ3 形态) ——
	{"p4_for_empty", `
local function f() for i = 1, 100 do end return 42 end
local s = 0
for i = 1, 20 do s = s + f() end
return s`},
	{"p4_for_accumulate", `
local function f() local s = 0; for i = 1, 10 do s = s + i end return s end
local total = 0
for i = 1, 20 do total = total + f() end
return total`},

	// —— 表 IC ArrayHit(GETTABLE 数字键 in array)——
	{"p4_table_array_get", `
local function f(t) return t[1] end
local t = {100, 200, 300}
local s = 0
for i = 1, 30 do s = s + f(t) end
return s`},
	{"p4_table_array_set", `
local function setter(t, v) t[1] = v end
local t = {0, 0, 0}
for i = 1, 30 do setter(t, i) end
return t[1]`},

	// —— 表 IC NodeHit(GETTABLE 字符串键 in hash)——
	{"p4_table_node_get", `
local function f(t) return t["x"] end
local t = {x = 42, y = 99, z = 123}
local s = 0
for i = 1, 30 do s = s + f(t) end
return s`},
	{"p4_table_node_set", `
local function setter(t, v) t["x"] = v end
local t = {x = 0, y = 0}
for i = 1, 30 do setter(t, i) end
return t.x`},

	// —— NEWTABLE 单 BB ——
	{"p4_newtable", `
local function f() return {} end
local count = 0
for i = 1, 30 do local t = f(); if t then count = count + 1 end end
return count`},

	// —— SETUPVAL / GETUPVAL 形态 ——
	{"p4_upval_set", `
local upv = 0
local function setter(v) upv = v end
for i = 1, 30 do setter(i) end
return upv`},

	// —— PJ5 CALL void 形态:MOVE+CALL+RETURN void(`function(g) g() end`)——
	{"p4_call_void", `
local count = 0
local function noop() count = count + 1 end
local function invoker(g) g() end
for i = 1, 30 do invoker(noop) end
return count`},

	// —— PJ5 CALL void 形态 B:GETUPVAL+CALL+RETURN void
	// (`local function noop()...end; local function invoker() noop() end` 闭包调外层 known local fn)——
	// 本形态触发 PJ5 真升层 + Compile 端 SpecCallVoidHits 真命中(承
	// internal/crescent/gibbous_pj5_call_e2e_test.go::TestPJ5_CallVoid_E2E_FormB_Upval)。
	{"p4_call_void_upval", `
local count = 0
local function noop() count = count + 1 end
local function invoker() noop() end
for i = 1, 30 do invoker() end
return count`},

	// —— PJ5 CALL void 形态 B1K:GETUPVAL+LOADK+CALL+RETURN void(1 K 常量参)
	// (`local function take(x)...end; local function tick() take(K) end` 闭包调外层 + 1 K 常量参)——
	{"p4_call_void_upval_1argk", `
local sum = 0
local function take(x) sum = sum + x end
local function tick() take(42) end
for i = 1, 30 do tick() end
return sum`},

	// —— PJ5 CALL void 形态 B1R:GETUPVAL+MOVE+CALL+RETURN void(1 reg 参)
	// (`local function take(x)...end; local function tick(v) take(v) end` 闭包调外层 + 1 reg 参)——
	{"p4_call_void_upval_1argreg", `
local sum = 0
local function take(x) sum = sum + x end
local function tick(v) take(v) end
for i = 1, 30 do tick(i) end
return sum`},

	// —— PJ5 CALL getter 形态 BR1:GETUPVAL+CALL+RETURN+dead RETURN(0 参 1 返)
	// (`local function f()...end; local function get() local x = f(); return x end`
	// 闭包调外层 + 0 参 1 返,getter)——
	{"p4_call_getter_upval", `
local function f() return 42 end
local function get() local x = f(); return x end
local s = 0
for i = 1, 30 do s = s + get() end
return s`},

	// —— PJ5 CALL void 形态 B2K:GETUPVAL+LOADK+LOADK+CALL+RETURN void(2 K 参)
	// (`local function take(a, b)...end; local function tick() take(10, 20) end`
	// 闭包调外层 + 2 K 常量参)——
	{"p4_call_void_upval_2argk", `
local sum = 0
local function take(a, b) sum = sum + a * b end
local function tick() take(10, 20) end
for i = 1, 30 do tick() end
return sum`},

	// —— PJ5 TAILCALL 形态 TB0:GETUPVAL+TAILCALL+RETURN B=0+RETURN B=1(0 参 1 返)
	// (`local function f()...end; local function bounce() return f() end`
	// 闭包调外层 known local fn + 尾调用)。luac stmtReturn 单 CallExpr 快路径
	// 产物。SpecTailCallHits=1 命中实证(承
	// internal/crescent/gibbous_pj5_tailcall_e2e_test.go::TestPJ5_TailCall_E2E_FormTB0_Upval)。
	{"p4_tailcall_upval", `
local function f() return 42 end
local function bounce() return f() end
local s = 0
for i = 1, 30 do s = s + bounce() end
return s`},

	// —— PJ5 TAILCALL 形态 TB1K:GETUPVAL+LOADK+TAILCALL+...(1 K 参 1 返)
	// (`local function take(x) return x*2 end; local function bounce() return take(7) end`)
	{"p4_tailcall_upval_1argk", `
local function take(x) return x * 2 end
local function bounce() return take(7) end
local s = 0
for i = 1, 30 do s = s + bounce() end
return s`},

	// —— PJ5 TAILCALL 形态 TB1R:GETUPVAL+MOVE+TAILCALL+...(1 reg 参 1 返)
	// (`local function take(x) return x+1 end; local function bounce(v) return take(v) end`)
	{"p4_tailcall_upval_1argreg", `
local function take(x) return x + 1 end
local function bounce(v) return take(v) end
local s = 0
for i = 1, 30 do s = s + bounce(i) end
return s`},

	// —— PJ5 TAILCALL 形态 TB2K:GETUPVAL+LOADK+LOADK+TAILCALL+...(2 K 参 1 返)
	// (`local function f(a,b) return a+b end; local function bounce() return f(10,20) end`)
	{"p4_tailcall_upval_2argk", `
local function f(a, b) return a + b end
local function bounce() return f(10, 20) end
local s = 0
for i = 1, 30 do s = s + bounce() end
return s`},

	// —— PJ5 CALL void 2 参四组合 K+R/R+K/R+R(K+K 已由 _2argk 覆盖)——
	{"p4_call_void_upval_1k1r", `
local sum = 0
local function take(a, b) sum = sum + a * b end
local function tick(v) take(7, v) end
for i = 1, 30 do tick(i) end
return sum`},
	{"p4_call_void_upval_1r1k", `
local sum = 0
local function take(a, b) sum = sum + a * b end
local function tick(v) take(v, 7) end
for i = 1, 30 do tick(i) end
return sum`},
	{"p4_call_void_upval_2reg", `
local sum = 0
local function take(a, b) sum = sum + a * b end
local function tick(u, v) take(u, v) end
for i = 1, 10 do tick(i, i+1) end
return sum`},

	// —— PJ5 TAILCALL 2 参四组合 K+R/R+K/R+R(K+K 已由 _2argk 覆盖)——
	{"p4_tailcall_upval_1k1r", `
local function f(a, b) return a + b end
local function bounce(v) return f(7, v) end
local s = 0
for i = 1, 30 do s = s + bounce(i) end
return s`},
	{"p4_tailcall_upval_1r1k", `
local function f(a, b) return a + b end
local function bounce(v) return f(v, 7) end
local s = 0
for i = 1, 30 do s = s + bounce(i) end
return s`},
	{"p4_tailcall_upval_2reg", `
local function f(a, b) return a + b end
local function bounce(u, v) return f(u, v) end
local s = 0
for i = 1, 10 do s = s + bounce(i, i+1) end
return s`},

	// —— PJ5 CALL getter 1 K/reg 参 1 返 — `function() local y = take(K); return y end` 类
	// (`function(v) local y = take(v); return y end` 类),长度 5 但 CALL.B=2 C=2 区分 setter 2 参
	{"p4_call_getter_upval_1argk", `
local function take(x) return x * 2 end
local function get() local y = take(7); return y end
local s = 0
for i = 1, 30 do s = s + get() end
return s`},
	{"p4_call_getter_upval_1argreg", `
local function take(x) return x * 2 end
local function get(v) local y = take(v); return y end
local s = 0
for i = 1, 30 do s = s + get(i) end
return s`},

	// —— PJ5 CALL getter 2 参 1 返 — 长度 6,CALL.B=3 C=2 — 四组合 K+K/K+R/R+K/R+R
	{"p4_call_getter_upval_2argk", `
local function take(a, b) return a + b end
local function get() local y = take(7, 9); return y end
local s = 0
for i = 1, 30 do s = s + get() end
return s`},
	{"p4_call_getter_upval_2argreg", `
local function take(a, b) return a + b end
local function get(u, v) local y = take(u, v); return y end
local s = 0
for i = 1, 30 do s = s + get(i, i+1) end
return s`},
	{"p4_call_getter_upval_1k1r", `
local function take(a, b) return a + b end
local function get(v) local y = take(7, v); return y end
local s = 0
for i = 1, 30 do s = s + get(i) end
return s`},
	{"p4_call_getter_upval_1r1k", `
local function take(a, b) return a + b end
local function get(v) local y = take(v, 7); return y end
local s = 0
for i = 1, 30 do s = s + get(i) end
return s`},

	// —— PJ5 3 参形态 —— CALL setter / getter / TAILCALL 各组合 ——
	{"p4_call_void_upval_3argk", `
local sum = 0
local function take(a, b, c) sum = sum + a + b + c end
local function tick() take(1, 2, 3) end
for i = 1, 30 do tick() end
return sum`},
	{"p4_call_void_upval_3argreg", `
local sum = 0
local function take(a, b, c) sum = sum + a + b + c end
local function tick(u, v, w) take(u, v, w) end
for i = 1, 10 do tick(i, i+1, i+2) end
return sum`},
	{"p4_call_getter_upval_3argk", `
local function take(a, b, c) return a + b + c end
local function get() local y = take(1, 2, 3); return y end
local s = 0
for i = 1, 30 do s = s + get() end
return s`},
	{"p4_call_getter_upval_3argreg", `
local function take(a, b, c) return a + b + c end
local function get(u, v, w) local y = take(u, v, w); return y end
local s = 0
for i = 1, 10 do s = s + get(i, i+1, i+2) end
return s`},
	{"p4_tailcall_upval_3argk", `
local function f(a, b, c) return a + b + c end
local function bounce() return f(1, 2, 3) end
local s = 0
for i = 1, 30 do s = s + bounce() end
return s`},
	{"p4_tailcall_upval_3argreg", `
local function f(a, b, c) return a + b + c end
local function bounce(u, v, w) return f(u, v, w) end
local s = 0
for i = 1, 10 do s = s + bounce(i, i+1, i+2) end
return s`},

	// —— PJ5 N>=2 返值 getter 形态(0 参,长度 6/7)——
	{"p4_call_multiret_n2_upval", `
local function take() return 10, 20 end
local function get() local a, b = take(); return a, b end
local s = 0
for i = 1, 30 do
  local a, b = get()
  s = s + a + b
end
return s`},
	{"p4_call_multiret_n3_upval", `
local function take() return 1, 2, 3 end
local function get() local a, b, c = take(); return a, b, c end
local s = 0
for i = 1, 30 do
  local a, b, c = get()
  s = s + a + b + c
end
return s`},

	// —— PJ5 4 参形态(setter / getter / tail)——
	{"p4_call_void_upval_4argreg", `
local sum = 0
local function take(a, b, c, d) sum = sum + a + b + c + d end
local function tick(u, v, w, x) take(u, v, w, x) end
for i = 1, 10 do tick(i, i+1, i+2, i+3) end
return sum`},
	{"p4_call_getter_upval_4argreg", `
local function take(a, b, c, d) return a + b + c + d end
local function get(u, v, w, x) local y = take(u, v, w, x); return y end
local s = 0
for i = 1, 10 do s = s + get(i, i+1, i+2, i+3) end
return s`},
	{"p4_tailcall_upval_4argreg", `
local function f(a, b, c, d) return a + b + c + d end
local function bounce(u, v, w, x) return f(u, v, w, x) end
local s = 0
for i = 1, 10 do s = s + bounce(i, i+1, i+2, i+3) end
return s`},
}

// TestP4_Tiered 三方对拍:oracle / crescent / p4-jit 全 byte-equal。
//
// **承外部 review 🔴 阻塞修复**:此前 difftest 全套 P4 路径不被强制触达,
// 本测试明确 force-all 升 P4 + 重复调用核函数,确保 P4 native 路径在
// difftest 整套层面真被走到。
func TestP4_Tiered(t *testing.T) {
	oracle := findOracle()
	for _, c := range p4Corpus {
		t.Run(c.name, func(t *testing.T) {
			crescent := runWangshuP4Tiered(t, c.src, false)
			p4 := runWangshuP4Tiered(t, c.src, true)
			// 层间硬门:crescent vs P4 必须逐字节一致
			if crescent != p4 {
				t.Errorf("层间分歧 (crescent vs P4-jit):\n  crescent: %q\n  p4:       %q",
					crescent, p4)
			}
			// 锚定官方 lua5.1(可用时)
			if oracle != "" {
				want := runOracle(t, oracle, wrapForOracle(c.src))
				if p4 != want {
					t.Errorf("p4 vs oracle byte-diff:\n  p4:     %q\n  oracle: %q",
						p4, want)
				}
			}
		})
	}
}

// TestP4_ConcurrentForceAll V18(-race):多 State 并发 force-all P4。
//
// 每个 goroutine 独立 State + 独立 force-all,跑同一脚本,验证并发升层无数据竞争。
// `go test -race` 下任一竞争即报告。结果一致性顺带校验。
func TestP4_ConcurrentForceAll(t *testing.T) {
	// 选 P4 SupportsAllOpcodes 真接受的形态(算术 + FORLOOP + 表 IC)
	src := `
local function arith(x) return x * 2 + 1 end
local function loop() local s = 0; for i = 1, 50 do s = s + i end return s end
local function getter(t) return t[1] end
local t = {100, 200}
local s1, s2, s3 = 0, 0, 0
for i = 1, 30 do
  s1 = s1 + arith(i)
  s2 = s2 + loop()
  s3 = s3 + getter(t)
end
return s1, s2, s3`
	const goroutines = 8
	prog, err := wangshu.Compile([]byte(src), "p4race")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// 先单跑拿期望值
	want := runWangshuP4Tiered(t, src, true)

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
			t.Errorf("goroutine %d 结果分歧:\n  got:  %q\n  want: %q",
				g, results[g], want)
		}
	}
}

// TestP4_PromotionTriggered 强断言:跑 p4Corpus 后 PromotionCount > 0。
//
// 承外部 review 「`make test-p4` 21 binary 全过的'绿色'在 conformance / difftest
// 层面大面积是无证据空绿」缺口:即便 force-all 形式上调用,short proto + 复杂
// opcode 可能让 P4 升层数 = 0(测试假绿)。本测试明确断言至少一个 Proto 真升层,
// 否则 fail-stop(防 fall-through "P4 路径未触达"成静默空绿)。
func TestP4_PromotionTriggered(t *testing.T) {
	// 选 p4Corpus 中明确符合 P4 SupportsAllOpcodes 的形态
	src := `
local function f(x) return x * 2 + 1 end
local s = 0
for i = 1, 50 do s = s + f(i) end
return s`
	prog, err := wangshu.Compile([]byte(src), "p4-promo-check")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	promo := st.PromotionCount()
	t.Logf("PromotionCount = %d", promo)
	if promo == 0 {
		t.Fatal("PromotionCount = 0 → P4 路径未触达(force-all 形式上调用但实际无 Proto 升层)" +
			"——本测试是 difftest-p4 全套 P4 路径触达的兜底守卫,fail-stop")
	}
}
