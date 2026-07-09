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

	// —— EQ with string K operand (interned pointer-equality). Covers
	// the amd64 native EQ K-operand relaxation from this PR: Lua 5.1
	// interns string literals so raw ptr-equal == string-equal.
	{"p4_eq_string_const_hit", `
local function f(x) return x == "hello" end
local hit = f("hello")
local miss = f("world")
return hit, miss`},
	{"p4_eq_string_const_loop", `
local function f(x) return x == "match" end
local n = 0
for i = 1, 30 do if f("match") then n = n + 1 end end
for i = 1, 30 do if f("nope")  then n = n + 100 end end
return n`},

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

	// —— arith with number-string coercion (P1 accepts "5"+1 = 6; P4
	// native emit must either handle this correctly or defer to
	// shape-spec / interpreter). Guards against bot review's flagged
	// arm64 divergence.
	{"p4_arith_coerce_string", `
local function f(x) return x + 1 end
local s = 0
for i = 1, 30 do s = s + f("5") end
return s`},

	// —— arith with __add metamethod: table + number goes through
	// doArithSlow → arithMeta, must produce the metamethod result,
	// not a generic error.
	{"p4_arith_meta_add", `
local mt = { __add = function(a, b)
    local av = type(a) == "table" and a.v or a
    local bv = type(b) == "table" and b.v or b
    return av + bv
end }
local function f(t) return t + 1 end
local t = setmetatable({v = 10}, mt)
local s = 0
for i = 1, 30 do s = s + f(t) end
return s`},

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

	// —— PJ5 N>=2 返值含参 1 K/reg 参形态(长度 7,Code[2]=CALL B=2 C=3)——
	{"p4_call_multiret_n2_upval_1argk", `
local function take(k) return k, k*2 end
local function get() local a, b = take(7); return a, b end
local s = 0
for i = 1, 30 do
  local a, b = get()
  s = s + a + b
end
return s`},
	{"p4_call_multiret_n2_upval_1argreg", `
local function take(v) return v, v*2 end
local function get(v) local a, b = take(v); return a, b end
local s = 0
for i = 1, 30 do
  local a, b = get(i)
  s = s + a + b
end
return s`},

	// —— PJ5 5 参 setter 形态(长度 8,Code[6]=CALL B=6 C=1)——
	{"p4_call_void_upval_5argk", `
local sum = 0
local function take(a, b, c, d, e) sum = sum + a + b + c + d + e end
local function tick() take(1, 2, 3, 4, 5) end
for i = 1, 30 do tick() end
return sum`},
	{"p4_call_void_upval_5argreg", `
local sum = 0
local function take(a, b, c, d, e) sum = sum + a + b + c + d + e end
local function tick(u, v, w, x, y) take(u, v, w, x, y) end
for i = 1, 10 do tick(i, i+1, i+2, i+3, i+4) end
return sum`},

	// —— PJ5 5 参 getter / tail 形态(长度 9,Code[6]=CALL B=6 C=2 / Code[6]=TAILCALL)——
	{"p4_call_getter_upval_5argreg", `
local function take(a, b, c, d, e) return a + b + c + d + e end
local function get(u, v, w, x, y) local z = take(u, v, w, x, y); return z end
local s = 0
for i = 1, 10 do s = s + get(i, i+1, i+2, i+3, i+4) end
return s`},
	{"p4_tailcall_upval_5argreg", `
local function f(a, b, c, d, e) return a + b + c + d + e end
local function bounce(u, v, w, x, y) return f(u, v, w, x, y) end
local s = 0
for i = 1, 10 do s = s + bounce(i, i+1, i+2, i+3, i+4) end
return s`},

	// —— PJ5 6 参形态(setter 长度 9,getter 长度 10)——
	{"p4_call_void_upval_6argk", `
local sum = 0
local function take(a, b, c, d, e, f) sum = sum + a + b + c + d + e + f end
local function tick() take(1, 2, 3, 4, 5, 6) end
for i = 1, 30 do tick() end
return sum`},
	{"p4_call_getter_upval_6argreg", `
local function take(a, b, c, d, e, f) return a + b + c + d + e + f end
local function get(p, q, r, s, t, u) local z = take(p, q, r, s, t, u); return z end
local total = 0
for i = 1, 10 do total = total + get(i, i+1, i+2, i+3, i+4, i+5) end
return total`},

	// —— PJ5 N=3 返值含 1 K/reg 参形态(长度 8,Code[2]=CALL B=2 C=4)——
	{"p4_call_multiret_n3_upval_1argk", `
local function take(k) return k, k*2, k*3 end
local function get() local a, b, c = take(7); return a, b, c end
local s = 0
for i = 1, 30 do
  local a, b, c = get()
  s = s + a + b + c
end
return s`},
	{"p4_call_multiret_n3_upval_1argreg", `
local function take(v) return v, v*2, v*3 end
local function get(v) local a, b, c = take(v); return a, b, c end
local s = 0
for i = 1, 30 do
  local a, b, c = get(i)
  s = s + a + b + c
end
return s`},

	// —— PJ5 7 参形态(setter 长度 10,getter 长度 11)——
	{"p4_call_void_upval_7argk", `
local sum = 0
local function take(a, b, c, d, e, f, g) sum = sum + a + b + c + d + e + f + g end
local function tick() take(1, 2, 3, 4, 5, 6, 7) end
for i = 1, 30 do tick() end
return sum`},
	{"p4_call_getter_upval_7argreg", `
local function take(a, b, c, d, e, f, g) return a + b + c + d + e + f + g end
local function get(a, b, c, d, e, f, g) local z = take(a, b, c, d, e, f, g); return z end
local total = 0
for i = 1, 10 do total = total + get(i, i+1, i+2, i+3, i+4, i+5, i+6) end
return total`},

	// —— PJ5 TAILCALL 6/7 参形态(长度 10/11)——
	{"p4_tailcall_upval_6argreg", `
local function f(a, b, c, d, e, g) return a + b + c + d + e + g end
local function bounce(u, v, w, x, y, z) return f(u, v, w, x, y, z) end
local s = 0
for i = 1, 10 do s = s + bounce(i, i+1, i+2, i+3, i+4, i+5) end
return s`},
	{"p4_tailcall_upval_7argreg", `
local function f(a, b, c, d, e, g, h) return a + b + c + d + e + g + h end
local function bounce(u, v, w, x, y, z, q) return f(u, v, w, x, y, z, q) end
local s = 0
for i = 1, 10 do s = s + bounce(i, i+1, i+2, i+3, i+4, i+5, i+6) end
return s`},

	// —— PJ5 SELF method call inline 形态(`obj:method(args)` 真接入,
	// 承 P2 ReasonSelfCall 占位位拆分 + P4 端 analyzeSelfCallForm 守门)——
	{"p4_self_void_m0", `
local count = 0
local o = { m = function(self) count = count + 1 end }
local function caller(t) t:m() end
for i = 1, 30 do caller(o) end
return count`},
	{"p4_self_void_u0", `
local count = 0
local o = { m = function(self) count = count + 1 end }
local function tick() o:m() end
for i = 1, 30 do tick() end
return count`},
	{"p4_self_void_m1k", `
local sum = 0
local o = { m = function(self, x) sum = sum + x end }
local function caller(t) t:m(42) end
for i = 1, 30 do caller(o) end
return sum`},
	{"p4_self_void_m1r", `
local sum = 0
local o = { m = function(self, x) sum = sum + x end }
local function caller(t, v) t:m(v) end
for i = 1, 30 do caller(o, i) end
return sum`},
	{"p4_self_tail_m0", `
local count = 0
local o = { m = function(self) count = count + 1; return count end }
local function caller(t) return t:m() end
local last = 0
for i = 1, 30 do last = caller(o) end
return last`},
	{"p4_self_getter_m0", `
local o = { m = function(self) return 7 end }
local function caller(t) local r = t:m(); return r end
local s = 0
for i = 1, 30 do s = s + caller(o) end
return s`},

	// —— PJ5 SELF 3..5 参形态扩(长度 7/8/9)——
	{"p4_self_void_m3k", `
local sum = 0
local o = { m = function(self, a, b, c) sum = sum + a + b + c end }
local function caller(t) t:m(1, 2, 3) end
for i = 1, 30 do caller(o) end
return sum`},
	{"p4_self_void_m3r", `
local sum = 0
local o = { m = function(self, a, b, c) sum = sum + a + b + c end }
local function caller(t, x, y, z) t:m(x, y, z) end
for i = 1, 30 do caller(o, i, i+1, i+2) end
return sum`},
	{"p4_self_void_m4r", `
local sum = 0
local o = { m = function(self, a, b, c, d) sum = sum + a + b + c + d end }
local function caller(t, p, q, r, s) t:m(p, q, r, s) end
for i = 1, 30 do caller(o, i, i+1, i+2, i+3) end
return sum`},
	{"p4_self_void_m5r", `
local sum = 0
local o = { m = function(self, a, b, c, d, e) sum = sum + a + b + c + d + e end }
local function caller(t, p, q, r, s, u) t:m(p, q, r, s, u) end
for i = 1, 30 do caller(o, i, i+1, i+2, i+3, i+4) end
return sum`},
	{"p4_self_tail_3k", `
local o = { m = function(self, a, b, c) return a + b + c end }
local function caller(t) return t:m(1, 2, 3) end
local s = 0
for i = 1, 30 do s = s + caller(o) end
return s`},
	{"p4_self_void_m6r", `
local sum = 0
local o = { m = function(self, a, b, c, d, e, f) sum = sum + a + b + c + d + e + f end }
local function caller(t, p, q, r, s, u, v) t:m(p, q, r, s, u, v) end
for i = 1, 30 do caller(o, i, i+1, i+2, i+3, i+4, i+5) end
return sum`},
	{"p4_self_void_m7r", `
local sum = 0
local o = { m = function(self, a, b, c, d, e, f, g) sum = sum + a + b + c + d + e + f + g end }
local function caller(t, p, q, r, s, u, v, w) t:m(p, q, r, s, u, v, w) end
for i = 1, 30 do caller(o, i, i+1, i+2, i+3, i+4, i+5, i+6) end
return sum`},
	{"p4_self_tail_5r", `
local o = { m = function(self, a, b, c, d, e) return a + b + c + d + e end }
local function caller(t, p, q, r, s, u) return t:m(p, q, r, s, u) end
local total = 0
for i = 1, 30 do total = total + caller(o, i, i+1, i+2, i+3, i+4) end
return total`},

	// —— PJ5 SELF inline 嵌套形态(OOP wrapper / observer 业务真接入)——
	{"p4_self_nested_chain", `
local total = 0
local inner = { n = function(self, x) total = total + x end }
local outer = { m = function(self, v) inner:n(v) end }
local function caller(t, v) t:m(v) end
for i = 1, 30 do caller(outer, i) end
return total`},
	{"p4_self_then_call", `
local mCount = 0
local oCount = 0
local o = { m = function(self) mCount = mCount + 1 end }
local function other() oCount = oCount + 1 end
local function caller(t) t:m(); other() end
for i = 1, 30 do caller(o) end
return mCount, oCount`},

	// —— PJ5 SELF + CALL spec template 形态(IC NodeHit 命中走字节级 EmitSelfNodeHit
	// 模板,跳过 host.Self;CALL 段仍 host.CallBaseline)。warmup-then-force 通过
	// p4Corpus 的 force-all 路径触发(IC slot 已在解释器 warmup 中填好)——
	// difftest 通过让 caller 反复调单态 receiver,IC 稳定后 spec template 命中
	// 编译,验三方 byte-equal(oracle / crescent / p4-jit)。
	{"p4_self_spec_void_0arg", `
local count = 0
local o = { m = function(self) count = count + 1 end }
local function caller(t) t:m() end
for i = 1, 100 do caller(o) end
caller(o)
return count`},
	{"p4_self_spec_void_1karg", `
local sum = 0
local o = { m = function(self, x) sum = sum + x end }
local function caller(t) t:m(42) end
for i = 1, 100 do caller(o) end
caller(o)
return sum`},
	{"p4_self_spec_void_1regarg", `
local sum = 0
local o = { m = function(self, x) sum = sum + x end }
local function caller(t, v) t:m(v) end
for i = 1, 100 do caller(o, i) end
caller(o, 1000)
return sum`},
	{"p4_self_spec_void_3regargs", `
local sum = 0
local o = { m = function(self, a, b, c) sum = sum + a + b + c end }
local function caller(t, x, y, z) t:m(x, y, z) end
for i = 1, 100 do caller(o, i, i+1, i+2) end
caller(o, 1, 2, 3)
return sum`},
	{"p4_self_spec_tailcall_0arg", `
local o = { m = function(self) return 42 end }
local function caller(t) return t:m() end
local sum = 0
for i = 1, 100 do sum = sum + caller(o) end
sum = sum + caller(o)
return sum`},
	{"p4_self_spec_getter_0arg", `
local o = { m = function(self) return 42 end }
local function caller(t) local r = t:m(); return r end
local sum = 0
for i = 1, 100 do sum = sum + caller(o) end
sum = sum + caller(o)
return sum`},
	{"p4_self_spec_upvalrecv_0arg", `
local count = 0
local o = { m = function(self) count = count + 1 end }
local function tick() o:m() end
for i = 1, 100 do tick() end
tick()
return count`},
	// —— commit-5u zero-cross 优化形态(callee 也 P4 升层时跳 executeFrom)——
	// callee 用 PJ4 GETTABLE NodeHit form,P4 支持 + force-all 升 P4 后 helper
	// 走 enterGibbous 直接 zero-cross。byte-equal P1 vs crescent vs p4 三方。
	{"p4_self_spec_zerocross_getter", `
local o = { x = 42, m = function(self) return self.x end }
local function caller(t) local r = t:m(); return r end
local sum = 0
for i = 1, 100 do sum = sum + caller(o) end
sum = sum + caller(o)
return sum`},
	{"p4_self_spec_zerocross_setter", `
local count = 0
local o = { m = function(self) count = count + 1 end }
local function caller(t) t:m() end
for i = 1, 100 do caller(o) end
caller(o)
return count`},
	{"p4_self_spec_tailcall_1regarg", `
local o = { m = function(self, x) return x * 2 end }
local function caller(t, v) return t:m(v) end
local sum = 0
for i = 1, 100 do sum = sum + caller(o, i) end
sum = sum + caller(o, 1000)
return sum`},
	// —— PJ5 SELF + CALL spec template N=2 返 drop multi-ret 形态(承上批
	// form4..N cC=3/4 retB=1 守门扩):caller `local a,b = t:m(K×N)` 形态,
	// host.CallBaseline 按 callC 落 N 返值 R(callA..) 作 local 直接绑;
	// 主调 RETURN B=1 经 host.DoReturn 弹 0 返值收尾(两层协议解耦)——
	// 验三方 byte-equal。
	{"p4_self_spec_multiret_0arg", `
local count = 0
local mt = { m = function(self) count = count + 1; return 1, 2 end }
local function caller(_, t) local a, b = t:m() end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`},
	{"p4_self_spec_multiret_1karg", `
local count = 0
local mt = { m = function(self, k) count = count + k; return 1, 2 end }
local function caller(_, t) local a, b = t:m(7) end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`},
	{"p4_self_spec_multiret_3kargs", `
local count = 0
local mt = { m = function(self, x, y, z) count = count + x + y + z; return 1, 2 end }
local function caller(_, t) local a, b = t:m(7, 8, 9) end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`},
	{"p4_self_spec_multiret_5kargs", `
local count = 0
local mt = { m = function(self, x, y, z, w, v) count = count + x + y + z + w + v; return 1, 2 end }
local function caller(_, t) local a, b = t:m(7, 8, 9, 10, 11) end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`},
	// N>=4 返 drop multi-ret(承本批 isValidSpecCallRetCount cC∈{1,3..16} 扩):
	{"p4_self_spec_multiret_n4_0arg", `
local count = 0
local mt = { m = function(self) count = count + 1; return 1, 2, 3, 4 end }
local function caller(_, t) local a, b, c, d = t:m() end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`},
	{"p4_self_spec_multiret_n5_0arg", `
local count = 0
local mt = { m = function(self) count = count + 1; return 1, 2, 3, 4, 5 end }
local function caller(_, t) local a, b, c, d, e = t:m() end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`},
	{"p4_self_spec_multiret_n4_1karg", `
local count = 0
local mt = { m = function(self, k) count = count + k; return 1, 2, 3, 4 end }
local function caller(_, t) local a, b, c, d = t:m(7) end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`},
	{"p4_self_spec_multiret_n4_1regarg", `
local count = 0
local mt = { m = function(self, k) count = count + k; return 1, 2, 3, 4 end }
local function caller(_, t, v) local a, b, c, d = t:m(v) end
for i = 1, 100 do caller(nil, mt, i) end
caller(nil, mt, 1000)
return count`},
	{"p4_self_spec_multiret_n4_3kargs", `
local count = 0
local mt = { m = function(self, x, y, z) count = count + x + y + z; return 1, 2, 3, 4 end }
local function caller(_, t) local a, b, c, d = t:m(7, 8, 9) end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`},
	// N=8 / N=15 上界附近(cC=9 / cC=16,验 isValidSpecCallRetCount 严格上界):
	{"p4_self_spec_multiret_n8_0arg", `
local count = 0
local mt = { m = function(self) count = count + 1; return 1, 2, 3, 4, 5, 6, 7, 8 end }
local function caller(_, t) local a, b, c, d, e, f, g, h = t:m() end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`},
	{"p4_self_spec_multiret_n15_0arg", `
local count = 0
local mt = { m = function(self) count = count + 1; return 1,2,3,4,5,6,7,8,9,10,11,12,13,14,15 end }
local function caller(_, t)
  local a,b,c,d,e,f,g,h,i,j,k,l,m,n,o = t:m()
end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`},
	// —— PJ5 SELF spec template 错误冒泡 difftest(承 d201a2f/d0893c9 e2e
	// 实证 NilRecv + BadMethod;本批加 difftest 三方 byte-equal,验
	// crescent vs P4 错误消息逐字节一致 + P4 OSR exit 路径不破坏错误冒泡)。
	// pcall 把错误转 (false, errmsg) 返回,避免 runWangshuP4Tiered 在
	// err != nil 时 fail-fast,保留错误消息进 byte-equal 对比。
	{"p4_self_spec_err_nilrecv", `
local function caller(t) return t:m() end
local ok, err = pcall(caller, nil)
return ok, tostring(err)`},
	{"p4_self_spec_err_badmethod", `
local mt = { m = 42 }
local function caller(t) return t:m() end
local ok, err = pcall(caller, mt)
return ok, tostring(err)`},
	{"p4_self_spec_err_warmup_then_nilrecv", `
local m_good = { m = function(self) return 1 end }
local function caller(t) return t:m() end
-- warmup 填 IC NodeHit + FBSelfMono
local sum = 0
for i = 1, 100 do sum = sum + caller(m_good) end
-- 然后用 nil receiver → spec NodeHit guard 失败 → deopt → host.Self → err
local ok, err = pcall(caller, nil)
return ok, tostring(err), sum`},
	// —— PJ5 SELF inline 路径(非 spec template,走 host.Self → host.CallBaseline)
	// 错误冒泡 difftest(承 cf8c24a SELF inline 错误冒泡 e2e 同款,但本批补 difftest
	// 三方 byte-equal 覆盖)。inline 路径 NodeHit feedback 未触发(无 warmup),
	// 走纯 host helper round-trip,但错误冒泡逻辑同款。
	{"p4_self_inline_err_nilrecv", `
-- 不 warmup,直接调 nil receiver:inline 路径 host.Self raise
local function caller(t) return t:m() end
local ok, err = pcall(caller, nil)
return ok, tostring(err)`},
	{"p4_self_inline_err_badmethod", `
local mt = { m = "string_not_callable" }
local function caller(t) return t:m() end
local ok, err = pcall(caller, mt)
return ok, tostring(err)`},
	// —— PJ4 表 IC 错误冒泡 difftest(承 §9.7-§9.10 PJ4 IC 六路径全覆盖):
	// GETTABLE / SETTABLE 在 nil 表 / non-table 上 raise,验 IC inline 路径
	// + host.GetTable/SetTable 降级路径错误冒泡 byte-equal P1。
	{"p4_get_err_niltable", `
local function getter(t) return t[1] end
local ok, err = pcall(getter, nil)
return ok, tostring(err)`},
	{"p4_set_err_niltable", `
local function setter(t) t[1] = 99 end
local ok, err = pcall(setter, nil)
return ok, tostring(err)`},
	{"p4_get_err_nontable", `
local function getter(t) return t[1] end
local ok, err = pcall(getter, 42)
return ok, tostring(err)`},
	// —— PJ3 FORLOOP 错误冒泡 difftest(承 §8 PJ3 FORLOOP 字节级 inline):
	// for 限制 / 步长非 number → ForPrep raise,验 PJ3 模板 deopt 路径错误
	// 冒泡 byte-equal P1。
	{"p4_forloop_err_nonumlimit", `
local function loop(n) for i = 1, n do end end
local ok, err = pcall(loop, "not_a_number")
return ok, tostring(err)`},
	{"p4_forloop_err_nonumstep", `
local function loop(s) for i = 1, 10, s do end end
local ok, err = pcall(loop, "not_a_number")
return ok, tostring(err)`},
	// —— PJ7 算术错误冒泡 difftest(承 PJ7 ADD..POW 6 op):arith on
	// non-number → host.Arith raise,验 P4 算术 inline 路径错误冒泡。
	{"p4_arith_err_addstring", `
local function add(a, b) return a + b end
local ok, err = pcall(add, "x", 1)
return ok, tostring(err)`},
	{"p4_arith_err_concatlennil", `
local function ln(t) return #t end
local ok, err = pcall(ln, nil)
return ok, tostring(err)`},
	// —— R14 ABI 后验 difftest(承本会话 R14 ABI 修复 + 7 R14 后验测试矩阵):
	// 这些用例**包含真实 PJ3/PJ4/PJ5 mmap 段路径** + 重复迭代,验 P4 vs
	// crescent byte-equal 在 GC stress 类工作负载下不引入分歧。
	{"p4_r14_pj5_self_repeated", `
local o = { m = function(self) return 42 end }
local function caller(t) return t:m() end
local sum = 0
for i = 1, 200 do sum = sum + caller(o) end  -- 200 次 spec template
return sum`},
	{"p4_r14_pj4_get_repeated", `
local function f(t) return t[1] end
local t = {7, 8, 9}
local sum = 0
for i = 1, 200 do sum = sum + f(t) end  -- 200 次 IC ArrayHit
return sum`},
	{"p4_r14_pj3_forloop_repeated", `
local function loop(n) local s = 0; for i = 1, n do s = s + i end; return s end
local sum = 0
for i = 1, 50 do sum = sum + loop(100) end  -- 50 outer * 100 inner forloop
return sum`},
	// -- issue #52 PJ10 native acceptance for the last four ops:
	// TAILCALL / TFORLOOP / CLOSURE / CLOSE all lower to exit-reasons
	// now, so kernels containing them promote to the native path.
	// Repeated calls force the promoted segment to actually run; the
	// three-way diff (oracle / crescent / p4) proves byte-equality.
	{"p4_iss52_tailcall_lua_arm", `
local function helper(a, b) return a + b end
local function kernel(n)
  local x = n * 2 + 1
  return helper(x, 1)
end
local s = 0
for i = 1, 50 do s = s + kernel(i) end
return s`},
	{"p4_iss52_tailcall_host_arm", `
local ts = tostring
local function kernel(n)
  local x = n * 3 + 1
  return ts(x)
end
local last
for i = 1, 50 do last = kernel(i) end
return last`},
	{"p4_iss52_tailcall_host_multret", `
local up = unpack
local function kernel(t)
  local x = 1 + 1
  return up(t)
end
local a, b, c
for i = 1, 50 do a, b, c = kernel({i, i + 1, i + 2}) end
return a, b, c`},
	{"p4_iss52_tforloop_next", `
local t = {10, 20, 30, 40}
local nx = next
local function kernel()
  local sum = 0
  for k, v in nx, t do sum = sum + k * v end
  return sum
end
local s = 0
for i = 1, 50 do s = s + kernel() end
return s`},
	{"p4_iss52_closure_close_loop", `
local fns = {}
local function kernel(n)
  local acc = 0
  for i = 1, n do
    do
      local x = i * 2
      local f = function() return x end
      fns[i] = f
      acc = acc + f() + i + x - 1 + 2 - 2 + 0 + 0 + 0
    end
  end
  return acc
end
local s = 0
for i = 1, 20 do s = s + kernel(5) end
return s, fns[1](), fns[5]()`},
	{"p4_iss52_tforloop_iter_raises", `
local boom = function(s, ctrl)
  if ctrl ~= nil and ctrl >= 2 then error("iter-boom") end
  if ctrl == nil then return 1, 10 end
  return ctrl + 1, 10
end
local function kernel()
  local sum = 0
  for i, v in boom, nil do
    sum = sum + i + v + 0 + 0 + 0 + 0 + 0 + 0 + 0
  end
  return sum
end
local ok, e
for i = 1, 20 do ok, e = pcall(kernel) end
return tostring(ok), (e and e:match("iter%-boom")) or "?"`},
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
			// 跳过 oracle 对比:含 "_err_" 的用例(错误消息含 chunk name
			// 差异,wangshu 用 "p4diff" / oracle 用 "stdin",非 P4 路径问题,
			// 承 errmsg_test.go 同款归一化跳过策略)
			if strings.Contains(c.name, "_err_") {
				return
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
	// 选 P4 SupportsAllOpcodes 真接受的形态(算术 + FORLOOP + 表 IC + SELF inline)
	src := `
local function arith(x) return x * 2 + 1 end
local function loop() local s = 0; for i = 1, 50 do s = s + i end return s end
local function getter(t) return t[1] end
local o = { m = function(self, x) return x * 3 end }
local function self_caller(t, v) return t:m(v) end
local t = {100, 200}
local s1, s2, s3, s4 = 0, 0, 0, 0
for i = 1, 30 do
  s1 = s1 + arith(i)
  s2 = s2 + loop()
  s3 = s3 + getter(t)
  s4 = s4 + self_caller(o, i)
end
return s1, s2, s3, s4`
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

// TestP4_ConcurrentForceAll_MultiRet V18(-race):多 State 并发 force-all P4
// 跑 PJ5 SELF spec template N=4 返 drop multi-ret 形态(承 84c7ed4 cC∈{1,3..16}
// 扩 + 91dcf07 N=4 返多形态)。验:N>=2 返路径 host.CallBaseline 多个 SetReg
// + DoReturn 0 返值收尾,在多 State 并发下无 race(arena GCRef 镜像字 atomic
// 单 word + 各 State 独立 jitContext)。
func TestP4_ConcurrentForceAll_MultiRet(t *testing.T) {
	src := `
local mt = { m = function(self, k) return k+1, k+2, k+3, k+4 end }
local function caller(_, t, v) local a, b, c, d = t:m(v) end
local s1, s2 = 0, 0
for i = 1, 30 do
  caller(nil, mt, i)
  s1 = s1 + i  -- 仅 side-effect 计数,验 N=4 返 drop 不影响后续
  s2 = s2 + i * 2
end
return s1, s2`
	const goroutines = 8
	prog, err := wangshu.Compile([]byte(src), "p4race-multiret-n4")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
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
			t.Errorf("goroutine %d N=4 返结果分歧:\n  got:  %q\n  want: %q",
				g, results[g], want)
		}
	}
}

// TestP4_ConcurrentForceAll_SpecDeopt V18(-race):多 State 并发 force-all P4
// 跑 PJ5 SELF spec template 路径下 NodeHit guard 失败 + deopt 路径。
//
// 验:spec template SELF NodeHit guard 失败 → onOSRExit + p4SpecState 累积
// deopt + 降级 host.Self 路径在多 State 8 goroutine 并发下无 race。承
// p4SpecState package-level 全局 map + p4SpecMu 守护(承 p4state.go godoc
// 修正 730f253)。
func TestP4_ConcurrentForceAll_SpecDeopt(t *testing.T) {
	// 不同 receiver shape 触发 deopt(spec template NodeHit guard 失败)
	src := `
local m1 = { m = function(self) return 1 end }
local m2 = { m = function(self) return 2 end, other = 99 }
local function caller(t) return t:m() end
local sum = 0
for i = 1, 50 do
  sum = sum + caller(m1)  -- warmup NodeHit on m1
  sum = sum + caller(m2)  -- 触发 deopt(shape 不同)
end
return sum`
	const goroutines = 8
	prog, err := wangshu.Compile([]byte(src), "p4race-spec-deopt")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
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
			t.Errorf("goroutine %d spec deopt 结果分歧:\n  got:  %q\n  want: %q",
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
