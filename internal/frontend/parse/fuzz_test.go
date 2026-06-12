// Parser fuzz:任意源码不得 panic(语法错误经 error 返回;首错即停)。
package parse

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/frontend/lex"
)

func FuzzParse(f *testing.F) {
	seeds := []string{
		`local x = 1 + 2`,
		`function f(a, ...) return a, ... end`,
		`for i = 1, 10 do print(i) end`,
		`for k, v in pairs(t) do end`,
		`while true do break end`,
		`repeat local x = 1 until x`,
		`if a then b() elseif c then d() else e() end`,
		`local t = { 1, 2, x = 3, [k] = v }`,
		`a.b.c:m(1, "s", {})`,
		`return f()(g())[h()]`,
		`local a, b, c = f()`,
		`x = -#t .. "s" ^ 2`,
		`function a.b.c:m() end`,
		`((((((((((x))))))))))`,
		`do do do end end end`,
		`local function f() return function() return f end end`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		lx := lex.New(src, "fuzz")
		_, _ = Parse(lx, "fuzz") // 错误是合法结果;panic 才是 bug
	})
}
