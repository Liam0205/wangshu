// Go 原生 fuzz 目标(engineering.md §1 fuzz-smoke 的实跑载体)。
//
// 不变式:任意输入下 lexer/parser 不得 panic(错误经 error 返回)。
package lex

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/frontend/token"
)

func FuzzLexer(f *testing.F) {
	seeds := []string{
		`local x = 1`,
		`-- comment`,
		`--[[ long comment ]]`,
		`"str\n\t\\"`,
		`[[long string]]`,
		`[==[nested]==]`,
		`0x1F 1e10 .5 3.`,
		`a..b ... == ~= <= >=`,
		"\xff\xfe\x00",
		`"unterminated`,
		`[[unterminated`,
		`1e`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		lx := New(src, "fuzz")
		for i := 0; i < 1<<16; i++ {
			tok, err := lx.Next()
			if err != nil {
				return // 词法错误是合法结果;panic 才是 bug
			}
			if tok.Kind == token.EOF {
				return
			}
		}
	})
}
