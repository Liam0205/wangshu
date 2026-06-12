// End-to-end fuzz:任意源码经 Compile + Run 不得 panic
// (编译错误/运行期错误经 error 返回;无限循环靠 fuzz 引擎超时兜底——
// 生成的源码大概率语法错,真跑起来的也以短脚本为主)。
package wangshu_test

import (
	"strings"
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
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		// 跳过疑似含长循环的输入(fuzz 引擎自身有超时,但显式过滤更快)
		if len(src) > 1<<14 || strings.Contains(src, "while") || strings.Contains(src, "repeat") {
			t.Skip()
		}
		prog, err := wangshu.Compile([]byte(src), "fuzz")
		if err != nil {
			return // 编译错误是合法结果
		}
		st := wangshu.NewState(wangshu.Options{})
		_, _ = prog.Run(st) // 运行期错误是合法结果;panic 才是 bug
	})
}
