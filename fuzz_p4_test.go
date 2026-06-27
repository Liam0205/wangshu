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

		// 错误等价(byte-equal 的弱版:都成功或都失败)
		if (errP1 == nil) != (errP4 == nil) {
			t.Errorf("P1 err = %v, P4 err = %v(错误存在性差异)", errP1, errP4)
			return
		}
		if errP1 != nil {
			// 都错误:不强求 err 消息字面一致(P4 spec template deopt 路径
			// 错误冒泡 byte-equal P1 已通过 ErrorBubbleUp_NilRecv/BadMethod
			// e2e 锚定 — fuzz 仅验存在性等价)
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
