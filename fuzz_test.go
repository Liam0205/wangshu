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
		`while true do end`,
		`for i = 1, 1e9 do end`,
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
		// 回边指令预算兜住无限/超长循环(while true do end、for i=1,1e9)。
		// 比源码子串过滤健壮:loadstring/拼接构造的循环同样兜得住。
		st.SetStepBudget(1 << 20)
		_, _ = prog.Run(st) // 运行期错误(含预算超额)是合法结果;panic 才是 bug
	})
}
