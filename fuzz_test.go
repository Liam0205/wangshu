// End-to-end fuzz:任意源码经 Compile + Run 不得 panic
// (编译错误/运行期错误经 error 返回;无限/超长循环由回边指令预算兜住)。
package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

func FuzzCompileRun(f *testing.F) {
	seeds := []string{
		`return 1 + 2`,
		`local t = {} t[1] = "x" return #t`,
		`return ("abc"):upper()`,
		`local ok, e = pcall(function() error("x") end) return ok, e`,
		`for i = 1, 3 do end return i`,
		`return select("#", 1, 2)`,
		`local co = coroutine.create(function() coroutine.yield(1) end)
return coroutine.resume(co)`,
		`return string.format("%d", 42)`,
		`return math.floor(1.5) + math.max(1, 2)`,
		`return nil == false`,
		`return 0/0 ~= 0/0`,
		`x = 1 return x`,
		`return string.rep("a", 3)`,
		`local a, b = 1 return b`,
		`for i = 1, 1e5 do end`,
		// 1e5 而非 1e9:fuzz 框架 -fuzztime 内部用 context.WithTimeout
		// 实现 wall-clock 超时,1e9 解释器跑 100ms-1s+ 接近 wall-clock 量级,
		// CI runner 慢一点时框架报 "context deadline exceeded" 强 fail。
		// 1e5 等价测「循环 + 预算兜底路径」,wall-clock 留余量(1e6 在 fuzz
		// 引擎变体下也观察到 0/sec 拖尾)。fuzz seed 不应包含「靠 budget
		// 兜底的近无限循环」是项目纪律(engineering.md §1.1)。
		`local n = 0 for i = 1, 1e5 do n = n + i end return n`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 1<<14 {
			t.Skip()
		}
		prog, err := wangshu.Compile([]byte(src), "fuzz")
		if err != nil {
			return // 编译错误是合法结果
		}
		st := wangshu.NewState(wangshu.Options{})
		// 回边指令预算兜住超长循环(for i=1,1e6 等);fuzz 引擎也会生成
		// 远超 budget 的循环变体,SetStepBudget 是结构性兜底。比源码子串
		// 过滤健壮:loadstring/拼接构造的循环同样兜得住。
		st.SetStepBudget(1 << 20)
		_, _ = prog.Run(st) // 运行期错误(含预算超额)是合法结果;panic 才是 bug
	})
}
