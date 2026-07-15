//go:build wangshu_p4 && wangshu_profile

// P4 cross-tier difftest suite (docs/design/p4-method-jit/08-testing-strategy.md V1-V13 / V17-V18).
//
// **From external review 🔴 blocker**: `make difftest-p4` has long run the generic
// `difftest_test.go`, which **neither calls `SetForceAllPromote(true)` nor is designed
// to "call repeatedly"** — so the P4 path is **never forced to be exercised** anywhere
// across the difftest suite. This file mirrors the shape of `p3_test.go` and adds a
// P4-build-tag-specific harness to close this suite-wide prove-the-path gap.
//
// Three-way diff — passes only when **everything is byte-equal**:
//   - oracle      = official lua5.1 (same source as difftest_test.go);
//   - crescent    = wangshu force-all OFF (pure interpreter, cross-tier baseline);
//   - p4-jit      = wangshu force-all ON (every compilable Proto promoted to P4 native).
//
// Runs only under the `wangshu_p4 && wangshu_profile` build.
//
// **Promotion timing** (same as p3_test.go): doCall's gibbous branch (call.go §VS0-d)
// jumps to P4 only when the Proto is **already promoted**; under force-all, OnEnter
// triggers promotion at frame entry, so a Proto's **first** call still runs crescent
// (promotion happens after it enters the frame), and only the **second call onward**
// takes the P4 path. Each kernel function is called repeatedly to ensure the P4 path
// is actually exercised.
//
// **P4 vs P3 shape differences**: P4's current SupportsAllOpcodes whitelist covers about
// 25 shape classes + 4 IC inline families (extended to the full six IC paths after PJ4),
// **but does not support complex control flow / cross-tier recursion / TFORLOOP /
// __index metamethod chains / TAILCALL etc.** — so p4Corpus cases are narrower in shape
// than p3Corpus: hand-picked single-BB single-RETURN shapes that P4 SupportsAllOpcodes
// truly accepts, plus table IC shapes.

package difftest

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// runWangshuP4Tiered runs a script with wangshu; force controls whether to force full
// promotion (true = P4 path). Same as runWangshuTiered (p3_test.go); duplicated to avoid
// P3/P4 build-tag mutual-exclusion renaming.
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

// p4Corpus is the cross-tier case set for shapes that P4 SupportsAllOpcodes truly accepts.
// Each kernel is called repeatedly to guarantee the P4 branch is actually taken after
// promotion (first call runs crescent, second call onward runs P4 native).
//
// **Shape selection strategy** (per P4 SupportsAllOpcodes's current whitelist):
//   - single-BB "value produce + RETURN A 2 / RETURN A 1" single-op + RETURN subset
//   - two-op chain (MUL+ADD / ADD+MUL)
//   - comparison folding (EQ/LT/LE + JMP + LOADBOOL×2 + RETURN)
//   - byte-level FORLOOP inline (empty body / reg-limit / body inline shapes)
//   - table IC six paths (GetTable ArrayHit/NodeHit + SETTABLE ArrayHit/NodeHit
//   - SELF ArrayHit/NodeHit)
//
// **Each kernel wrapped in an outer function**: an outer function + for loop calls the
// inner kernel repeatedly, ensuring
// (1) the outer chunk length >= MinPromotableCodeLen=10 so the outer is promoted too
// (2) the inner kernel is called repeatedly so the P4 path is actually exercised (first
// call runs crescent, second call onward runs P4)
var p4Corpus = []diffCase{
	// —— value-return single-BB shapes (LOADK / LOADBOOL / LOADNIL / MOVE) ——
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

	// —— single arithmetic op + RETURN ——
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

	// —— comparison folding ——
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

	// —— FORLOOP byte-level inline (PJ3 shape) ——
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

	// —— table IC ArrayHit (GETTABLE numeric key in array) ——
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

	// —— table IC NodeHit (GETTABLE string key in hash) ——
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

	// —— NEWTABLE single BB ——
	{"p4_newtable", `
local function f() return {} end
local count = 0
for i = 1, 30 do local t = f(); if t then count = count + 1 end end
return count`},

	// —— SETUPVAL / GETUPVAL shapes ——
	{"p4_upval_set", `
local upv = 0
local function setter(v) upv = v end
for i = 1, 30 do setter(i) end
return upv`},

	// —— PJ5 CALL void shape: MOVE+CALL+RETURN void (`function(g) g() end`) ——
	{"p4_call_void", `
local count = 0
local function noop() count = count + 1 end
local function invoker(g) g() end
for i = 1, 30 do invoker(noop) end
return count`},

	// —— PJ5 CALL void shape B: GETUPVAL+CALL+RETURN void
	// (`local function noop()...end; local function invoker() noop() end` — closure calls
	// an outer known local fn) ——
	// This shape triggers a real PJ5 promotion + a real Compile-side SpecCallVoidHits hit
	// (see internal/crescent/gibbous_pj5_call_e2e_test.go::TestPJ5_CallVoid_E2E_FormB_Upval).
	{"p4_call_void_upval", `
local count = 0
local function noop() count = count + 1 end
local function invoker() noop() end
for i = 1, 30 do invoker() end
return count`},

	// —— PJ5 CALL void shape B1K: GETUPVAL+LOADK+CALL+RETURN void (1 K constant arg)
	// (`local function take(x)...end; local function tick() take(K) end` — closure calls an outer fn + 1 K constant arg) ——
	{"p4_call_void_upval_1argk", `
local sum = 0
local function take(x) sum = sum + x end
local function tick() take(42) end
for i = 1, 30 do tick() end
return sum`},

	// —— PJ5 CALL void shape B1R: GETUPVAL+MOVE+CALL+RETURN void (1 reg arg)
	// (`local function take(x)...end; local function tick(v) take(v) end` — closure calls an outer fn + 1 reg arg) ——
	{"p4_call_void_upval_1argreg", `
local sum = 0
local function take(x) sum = sum + x end
local function tick(v) take(v) end
for i = 1, 30 do tick(i) end
return sum`},

	// —— PJ5 CALL getter shape BR1: GETUPVAL+CALL+RETURN+dead RETURN (0 args 1 return)
	// (`local function f()...end; local function get() local x = f(); return x end`
	// — closure calls an outer fn + 0 args 1 return, getter) ——
	{"p4_call_getter_upval", `
local function f() return 42 end
local function get() local x = f(); return x end
local s = 0
for i = 1, 30 do s = s + get() end
return s`},

	// —— PJ5 CALL void shape B2K: GETUPVAL+LOADK+LOADK+CALL+RETURN void (2 K args)
	// (`local function take(a, b)...end; local function tick() take(10, 20) end`
	// — closure calls an outer fn + 2 K constant args) ——
	{"p4_call_void_upval_2argk", `
local sum = 0
local function take(a, b) sum = sum + a * b end
local function tick() take(10, 20) end
for i = 1, 30 do tick() end
return sum`},

	// —— PJ5 TAILCALL shape TB0: GETUPVAL+TAILCALL+RETURN B=0+RETURN B=1 (0 args 1 return)
	// (`local function f()...end; local function bounce() return f() end`
	// — closure calls an outer known local fn + tail call). Product of luac's stmtReturn
	// single-CallExpr fast path. SpecTailCallHits=1 hit is verified (see
	// internal/crescent/gibbous_pj5_tailcall_e2e_test.go::TestPJ5_TailCall_E2E_FormTB0_Upval).
	{"p4_tailcall_upval", `
local function f() return 42 end
local function bounce() return f() end
local s = 0
for i = 1, 30 do s = s + bounce() end
return s`},

	// —— Issue #112 self-tail-call in-segment loop: the identity guard
	// admits only the running closure. Collatz-style self recursion,
	// arg underflow (nil-fill parity with enterLuaFrame), deep
	// accumulator recursion, and stale-local clearing across loop
	// iterations all byte-equal against the oracle + interpreter.
	{"p4_selftail_collatz", `
local function collatz(n, steps)
  if n == 1 then return steps end
  if n % 2 == 0 then return collatz(n / 2, steps + 1) end
  return collatz(3 * n + 1, steps + 1)
end
local total = 0
for i = 1, 60 do total = total + collatz(i, 0) end
return total`},
	{"p4_selftail_arg_underflow", `
local function f(n, m)
  if n == nil then return -1 end
  if m == nil then m = 0 end
  if n <= 0 then return m end
  if n == 3 then return f() end
  return f(n - 1, m + n)
end
return f(8)`},
	{"p4_selftail_acc_deep", `
local function f(n, acc) if n <= 0 then return acc end return f(n - 1, acc + n) end
return f(5000, 0)`},
	{"p4_selftail_stale_local_clear", `
local function f(n, probe)
  local x
  if probe then return x == nil end
  if n <= 0 then return f(0, true) end
  x = n
  return f(n - 1)
end
return f(5)`},

	// —— PJ5 TAILCALL shape TB1K: GETUPVAL+LOADK+TAILCALL+... (1 K arg 1 return)
	// (`local function take(x) return x*2 end; local function bounce() return take(7) end`)
	{"p4_tailcall_upval_1argk", `
local function take(x) return x * 2 end
local function bounce() return take(7) end
local s = 0
for i = 1, 30 do s = s + bounce() end
return s`},

	// —— PJ5 TAILCALL shape TB1R: GETUPVAL+MOVE+TAILCALL+... (1 reg arg 1 return)
	// (`local function take(x) return x+1 end; local function bounce(v) return take(v) end`)
	{"p4_tailcall_upval_1argreg", `
local function take(x) return x + 1 end
local function bounce(v) return take(v) end
local s = 0
for i = 1, 30 do s = s + bounce(i) end
return s`},

	// —— PJ5 TAILCALL shape TB2K: GETUPVAL+LOADK+LOADK+TAILCALL+... (2 K args 1 return)
	// (`local function f(a,b) return a+b end; local function bounce() return f(10,20) end`)
	{"p4_tailcall_upval_2argk", `
local function f(a, b) return a + b end
local function bounce() return f(10, 20) end
local s = 0
for i = 1, 30 do s = s + bounce() end
return s`},

	// —— PJ5 CALL void 2-arg four combinations K+R/R+K/R+R (K+K already covered by _2argk) ——
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

	// —— PJ5 TAILCALL 2-arg four combinations K+R/R+K/R+R (K+K already covered by _2argk) ——
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

	// —— PJ5 CALL getter 1 K/reg arg 1 return — `function() local y = take(K); return y end` kind
	// (`function(v) local y = take(v); return y end` kind), length 5 but CALL.B=2 C=2 distinguishes the 2-arg setter
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

	// —— PJ5 CALL getter 2 args 1 return — length 6, CALL.B=3 C=2 — four combinations K+K/K+R/R+K/R+R
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

	// —— PJ5 3-arg shapes —— CALL setter / getter / TAILCALL each combination ——
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

	// —— PJ5 N>=2 returns getter shape (0 args, length 6/7) ——
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

	// —— PJ5 4-arg shapes (setter / getter / tail) ——
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

	// —— PJ5 N>=2 returns with 1 K/reg arg shape (length 7, Code[2]=CALL B=2 C=3) ——
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

	// —— PJ5 5-arg setter shape (length 8, Code[6]=CALL B=6 C=1) ——
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

	// —— PJ5 5-arg getter / tail shape (length 9, Code[6]=CALL B=6 C=2 / Code[6]=TAILCALL) ——
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

	// —— PJ5 6-arg shapes (setter length 9, getter length 10) ——
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

	// —— PJ5 N=3 returns with 1 K/reg arg shape (length 8, Code[2]=CALL B=2 C=4) ——
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

	// —— PJ5 7-arg shapes (setter length 10, getter length 11) ——
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

	// —— PJ5 TAILCALL 6/7-arg shapes (length 10/11) ——
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

	// —— PJ5 SELF method-call inline shape (`obj:method(args)` real support,
	// building on the P2 ReasonSelfCall placeholder-bit split + P4-side analyzeSelfCallForm gating) ——
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

	// —— PJ5 SELF 3..5-arg shape extension (length 7/8/9) ——
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

	// —— PJ5 SELF inline nested shapes (OOP wrapper / observer business logic, real support) ——
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

	// —— PJ5 SELF + CALL spec template shape (IC NodeHit takes the byte-level EmitSelfNodeHit
	// template, skipping host.Self; the CALL segment still uses host.CallBaseline). warmup-then-force
	// is triggered by p4Corpus's force-all path (the IC slot has been filled during interpreter warmup) ——
	// difftest has caller repeatedly call a monomorphic receiver; once the IC stabilizes the spec template
	// hits during compilation, verifying three-way byte-equal (oracle / crescent / p4-jit).
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
	// —— commit-5u zero-cross optimization shape (when the callee is also promoted to P4, executeFrom is skipped) ——
	// The callee uses the PJ4 GETTABLE NodeHit form; with P4 support + force-all promotion, the helper
	// goes through enterGibbous directly zero-cross. byte-equal across P1 vs crescent vs p4.
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
	// —— PJ5 SELF + CALL spec template N=2 returns drop multi-ret shape (building on the prior batch's
	// form4..N cC=3/4 retB=1 gating extension): caller `local a,b = t:m(K×N)` shape,
	// host.CallBaseline writes N returns to R(callA..) as locals bound directly;
	// the caller's RETURN B=1 goes through host.DoReturn popping 0 returns to finish (the two protocol
	// layers are decoupled) —— verify three-way byte-equal.
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
	// N>=4 returns drop multi-ret (building on this batch's isValidSpecCallRetCount cC∈{1,3..16} extension):
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
	// N=8 / N=15 near the upper bound (cC=9 / cC=16, verifying isValidSpecCallRetCount's strict upper bound):
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
	// —— PJ5 SELF spec template error propagation difftest (building on d201a2f/d0893c9 e2e
	// evidence for NilRecv + BadMethod; this batch adds a three-way byte-equal difftest, verifying
	// crescent vs P4 error messages are byte-for-byte identical + the P4 OSR exit path does not break error propagation).
	// pcall converts the error into a (false, errmsg) return, avoiding runWangshuP4Tiered
	// failing fast on err != nil, preserving the error message for the byte-equal comparison.
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
	// —— PJ5 SELF inline path (not the spec template; goes through host.Self → host.CallBaseline)
	// error propagation difftest (same as cf8c24a SELF inline error propagation e2e, but this batch adds
	// three-way byte-equal difftest coverage). The inline path does not trigger NodeHit feedback (no warmup),
	// taking a pure host helper round-trip, but the error propagation logic is the same.
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
	// —— PJ4 table IC error propagation difftest (building on §9.7-§9.10 PJ4 IC six-path full coverage):
	// GETTABLE / SETTABLE raise on a nil table / non-table, verifying the IC inline path
	// + host.GetTable/SetTable fallback path error propagation is byte-equal to P1.
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
	// —— PJ3 FORLOOP error propagation difftest (building on §8 PJ3 FORLOOP byte-level inline):
	// for limit / step non-number → ForPrep raise, verifying the PJ3 template deopt path error
	// propagation is byte-equal to P1.
	{"p4_forloop_err_nonumlimit", `
local function loop(n) for i = 1, n do end end
local ok, err = pcall(loop, "not_a_number")
return ok, tostring(err)`},
	{"p4_forloop_err_nonumstep", `
local function loop(s) for i = 1, 10, s do end end
local ok, err = pcall(loop, "not_a_number")
return ok, tostring(err)`},
	// —— PJ7 arithmetic error propagation difftest (building on PJ7 ADD..POW 6 ops): arith on
	// a non-number → host.Arith raise, verifying the P4 arithmetic inline path error propagation.
	{"p4_arith_err_addstring", `
local function add(a, b) return a + b end
local ok, err = pcall(add, "x", 1)
return ok, tostring(err)`},
	{"p4_arith_err_concatlennil", `
local function ln(t) return #t end
local ok, err = pcall(ln, nil)
return ok, tostring(err)`},
	// —— R14 ABI post-verification difftest (building on this session's R14 ABI fix + 7 R14 post-verification test matrix):
	// these cases **include real PJ3/PJ4/PJ5 mmap segment paths** + repeated iteration, verifying P4 vs
	// crescent byte-equal introduces no divergence under GC-stress workloads.
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
	// #136: a call-void-shaped callee whose CALL result register is
	// overwritten by a later LOADK before RETURN. The length-6 form
	// `GETUPVAL; CALL 0 1 2; SETGLOBAL 0; LOADK 0; RETURN 0 2` was
	// mis-accepted by analyzeCallVoidForm as a 1-return getter, dropping
	// the SETGLOBAL + LOADK and returning the callee closure once the
	// proto was entered a SECOND time. Repeated entry is essential — the
	// first entry happened to look right. mk() must return a heap value
	// (closure) so the stale register is observably wrong.
	{"p4_callvoid_result_overwritten_by_loadk", `
local function mk() return function() return 9 end end
local function A() G = mk() return 0 end
local s = 0
for i = 1, 20 do s = s + A() end
return s, type(A())`},
}

// TestP4_Tiered three-way diff: oracle / crescent / p4-jit all byte-equal.
//
// **Fixing an external review 🔴 blocker**: previously the difftest suite's P4 path was never
// forced to be reached; this test explicitly force-all-promotes to P4 + repeatedly calls the
// kernel functions, ensuring the P4 native path is truly exercised across the whole difftest suite.
func TestP4_Tiered(t *testing.T) {
	oracle := findOracle()
	for _, c := range p4Corpus {
		t.Run(c.name, func(t *testing.T) {
			crescent := runWangshuP4Tiered(t, c.src, false)
			p4 := runWangshuP4Tiered(t, c.src, true)
			// Cross-tier hard gate: crescent vs P4 must be byte-for-byte identical
			if crescent != p4 {
				t.Errorf("层间分歧 (crescent vs P4-jit):\n  crescent: %q\n  p4:       %q",
					crescent, p4)
			}
			// Skip the oracle comparison: cases containing "_err_" (their error messages contain a chunk
			// name difference, wangshu uses "p4diff" / oracle uses "stdin", not a P4-path problem,
			// following the same normalization-skip strategy as errmsg_test.go)
			if strings.Contains(c.name, "_err_") {
				return
			}
			// Anchor against official lua5.1 (when available)
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

// TestP4_ConcurrentForceAll V18 (-race): multiple States concurrently force-all P4.
//
// Each goroutine has an independent State + independent force-all, running the same script, verifying
// concurrent promotion has no data races. `go test -race` reports any race. Result consistency is verified as a bonus.
func TestP4_ConcurrentForceAll(t *testing.T) {
	// Pick shapes that P4 SupportsAllOpcodes truly accepts (arithmetic + FORLOOP + table IC + SELF inline)
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
	// Run once first to get the expected value
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

// TestP4_ConcurrentForceAll_MultiRet V18 (-race): multiple States concurrently force-all P4
// running the PJ5 SELF spec template N=4 returns drop multi-ret shape (building on 84c7ed4 cC∈{1,3..16}
// extension + 91dcf07 N=4 returns multi-shape). Verifies: on the N>=2 returns path host.CallBaseline's
// multiple SetReg + DoReturn 0-returns finish has no race under multiple concurrent States (arena GCRef
// mirror word is a single atomic word + each State has an independent jitContext).
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

// TestP4_ConcurrentForceAll_SpecDeopt V18 (-race): multiple States concurrently force-all P4
// running the NodeHit guard failure + deopt path on the PJ5 SELF spec template path.
//
// Verifies: spec template SELF NodeHit guard failure → onOSRExit + p4SpecState accumulating
// deopts + falling back to the host.Self path has no race under 8 concurrent goroutines across
// multiple States. Building on the p4SpecState package-level global map + p4SpecMu guard (following
// the p4state.go godoc correction 730f253).
func TestP4_ConcurrentForceAll_SpecDeopt(t *testing.T) {
	// Different receiver shapes trigger deopt (spec template NodeHit guard failure)
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

// TestP4_PromotionTriggered strong assertion: after running p4Corpus, PromotionCount > 0.
//
// Addresses the external review gap "the 'green' of `make test-p4`'s 21 binaries all passing is
// largely evidence-free empty green at the conformance / difftest layer": even if force-all is
// formally invoked, short protos + complex opcodes may keep the P4 promotion count = 0 (test false
// green). This test explicitly asserts at least one Proto is truly promoted, otherwise fail-stop
// (guarding against fall-through where "the P4 path was never reached" becomes silent empty green).
func TestP4_PromotionTriggered(t *testing.T) {
	// Pick a shape from p4Corpus that clearly matches P4 SupportsAllOpcodes
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
