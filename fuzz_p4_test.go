// fuzz_p4_test.go —— P4 force-all guard 漏判 fuzz harness(承
// docs/design/p4-method-jit/08-testing-strategy.md V22 guard 漏判 fuzz)。
//
// **职责**:任意 Lua 源码经 force-all-promote 升 P4 后跑,与 P1 解释器
// 比对结果 byte-equal。验:
//   - P4 spec template guard 不漏判(IC NodeHit / FBSelfMono 守门下走 spec
//     段 + deopt 路径,与 P1 完整 doCall 结果一致)
//   - mmap 段 R14 ABI 修复后 Go G 在 fuzz 长跑下正确(承 PR #26 修复)
//   - V18 -race 持续无 race 在多形态源码下
//
// **CI 接入**:
//   - 单 run fuzz(< 5 min)在每 push 触发(fuzz-smoke-p4 job 待加)
//   - nightly fuzz(2h+)承 V21 longevity / V22 30 天累积无 guard 漏判事件

//go:build wangshu_p4 && wangshu_profile

package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// FuzzP4ForceAllPromote P4 force-all-promote 模式下 fuzz seed + 任意源码
// 不得 panic 且与 P1 解释器(force-all=false 路径)结果 byte-equal。
//
// 注:P4 与 P1 byte-equal 是承 P4 设计纪律(承 docs/design/p4-method-jit/
// 08-testing-strategy.md V1-V13 正确性轴 + 三方差分)的核心承诺。
func FuzzP4ForceAllPromote(f *testing.F) {
	// seed 选择策略:覆盖 P4 SupportsAllOpcodes 真实接受的形态
	// (算术 + FORLOOP + 表 IC + SELF inline + 全 25 类形态)。
	seeds := []string{
		// 简单算术
		`return 1 + 2`,
		`return (3 * 4) - (5 / 2)`,
		// 表 IC
		`local t = {1, 2, 3}; return t[2]`,
		`local t = {x = 1, y = 2}; return t.y`,
		`local t = {}; t[1] = 42; return t[1]`,
		`local t = {}; t.x = 99; return t.x`,
		// FORLOOP
		`local s = 0; for i = 1, 100 do s = s + i end; return s`,
		`local s = 0; for i = 1, 1000 do s = s + i * 2 end; return s`,
		// SELF method call
		`local o = {m = function(self) return 42 end}; return o:m()`,
		`local o = {m = function(self, x) return x * 2 end}; return o:m(21)`,
		// SELF spec template warmup
		`
local o = {m = function(self) return 1 end}
local function f(t) return t:m() end
local sum = 0
for i = 1, 100 do sum = sum + f(o) end
return sum`,
		// 嵌套 SELF
		`
local o1 = {m = function(self) return 1 end}
local o2 = {n = function(self) return 2 end}
local function f(a, b) return a:m() + b:n() end
return f(o1, o2)`,
		// CALL void / setter
		`local s = 0; local function add(x) s = s + x end; add(1); add(2); return s`,
		// 多返值 drop
		`local function multi() return 1, 2, 3 end; local a, b, c = multi(); return a + b + c`,
		// 比较折叠
		`return 1 < 2`,
		`return ("a" == "a")`,
		// 闭包 + upvalue
		`local function make() local x = 0; return function() x = x + 1; return x end end; local f = make(); return f() + f() + f()`,
		// PJ4 表 IC + grow(承 V20 deopt 风暴同款 shape 多态)
		`local t = {a=1, b=2}; local function f(t) return t.a end; for i = 1, 100 do f(t) end; return f(t)`,
		`local t = {1, 2, 3}; local function g(t) t[1] = 99; return t[1] end; for i = 1, 100 do g(t) end; return g(t)`,
		// SELF + deopt(不同 shape receiver)
		`local m1 = {m = function(self) return 1 end}; local m2 = {m = function(self) return 2 end, x = 1}; local f = function(t) return t:m() end; for i = 1, 50 do f(m1) end; return f(m2)`,
		// N=4 返多形态(承 84c7ed4 cC=5)
		`local mt = {m = function(self) return 1, 2, 3, 4 end}; local function caller(t) local a, b, c, d = t:m(); return a+b+c+d end; for i = 1, 50 do caller(mt) end; return caller(mt)`,
		// 嵌套 SELF 链
		`local o1 = {m = function(self) return 10 end}; local o2 = {n = function(self) return 20 end}; local function f(a, b) return a:m() + b:n() end; for i = 1, 50 do f(o1, o2) end; return f(o1, o2)`,
		// 算术错误冒泡
		`local function add(a, b) return a + b end; local ok, e = pcall(add, "x", 1); return ok`,
		// 错误冒泡 + SELF
		`local mt = {m = 42}; local ok, e = pcall(function() return mt:m() end); return ok`,
		// **commit-5u zero-cross 优化形态**:callee 也 P4 升层(GETTABLE form)
		`local o = { x = 42, m = function(self) return self.x end }; local function caller(t) local r = t:m(); return r end; local s = 0; for i = 1, 50 do s = s + caller(o) end; return s`,
		// useFrameInline N 参 fixed(callArgCount=0..7)+ zero-cross 兼容
		`local sum = 0; local o = { m = function(self, a, b, c) sum = sum + a + b + c end }; local function caller(t) t:m(1, 2, 3) end; for i = 1, 30 do caller(o) end; return sum`,
		// useFrameInline + vararg callee
		`local sum = 0; local o = { m = function(self, ...) local a, b, c = ...; sum = sum + a + b + c end }; local function caller(t) t:m(1, 2, 3) end; for i = 1, 30 do caller(o) end; return sum`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 1<<14 {
			t.Skip()
		}
		prog, err := wangshu.Compile([]byte(src), "fuzz-p4")
		if err != nil {
			return // 编译错误是合法结果
		}

		// 跑 P1 解释器路径(force-all=false)
		st1 := wangshu.NewState(wangshu.Options{})
		st1.SetStepBudget(1 << 20)
		resP1, errP1 := prog.Run(st1)

		// 跑 P4 force-all 路径
		st4 := wangshu.NewState(wangshu.Options{})
		st4.SetStepBudget(1 << 20)
		st4.SetForceAllPromote(true)
		resP4, errP4 := prog.Run(st4)

		// 错误存在性差异处理(2026-06-28 精准豁免,承 PR #26 评论建议):
		// 按错误类型分类,只对**预算/时机类**分叉降级 Skip(P1/P4 计步时机
		// 不同,临界 input 可能一方先触 `instruction budget exceeded`),
		// **语义性误编译**(误抛 / 吞错)仍硬 fail——保留 fuzz 对 P4 vs P1
		// 语义分叉的发现力。
		//
		// 锚定的语义类错误冒泡 byte-equal 基线(确定性覆盖):
		// - 12 错误冒泡 difftest 三方 byte-equal(p4_*_err_* 用例集)
		// - 3 e2e ErrorBubbleUp_NilRecv/BadMethod/OSRExitToDeopt
		// - 5 V18 -race(含 R14 ABI 后验)
		//
		// fuzz harness 验补充发现:**非 budget 类**的存在性分叉仍硬 fail。
		if (errP1 == nil) != (errP4 == nil) {
			budgetTiming := (errP1 != nil && strings.Contains(errP1.Error(), "instruction budget exceeded")) ||
				(errP4 != nil && strings.Contains(errP4.Error(), "instruction budget exceeded"))
			if budgetTiming {
				t.Skipf("预算/时机类分叉(非 byte-equal 违反):P1=%v P4=%v", errP1, errP4)
				return
			}
			t.Errorf("error 存在性真分叉(疑似 P4 误编译,需查):P1=%v P4=%v", errP1, errP4)
			return
		}
		if errP1 != nil {
			// 都错误:不强求 err 消息字面一致(P4 spec template deopt 路径
			// 错误冒泡 byte-equal P1 已通过 ErrorBubbleUp_NilRecv/BadMethod
			// e2e + 12 错误冒泡 difftest 锚定 — fuzz 仅验存在性等价)
			return
		}

		// 结果数等价
		if len(resP1) != len(resP4) {
			t.Errorf("P1 result count = %d, P4 = %d", len(resP1), len(resP4))
			return
		}

		// 结果字面等价(承 P4 byte-equal 纪律)
		for i := range resP1 {
			if resP1[i].Display() != resP4[i].Display() {
				t.Errorf("result[%d] mismatch:P1=%q, P4=%q",
					i, resP1[i].Display(), resP4[i].Display())
				return
			}
		}
	})
}
