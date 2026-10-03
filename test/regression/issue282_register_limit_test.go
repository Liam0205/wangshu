package regression

import (
	"strings"
	"testing"
)

// TestRegisterLimitIs249 covers the codegen half of #282: luaK_checkstack raises "function or
// expression too complex" once a function would need MAXSTACK (250) registers, so 248 call arguments
// or 249 return values still compile and one more does not. wangshu allowed one register more.
//
// lua5.1's message ends in " near '<token>'" and reports the line the scanner was on when codegen
// ran out. wangshu generates code from the finished AST, where that token is no longer known, so it
// gives neither -- a known limitation, see docs/design/p1-interpreter/04-frontend-parser-codegen.md §9 --
// and only the message is compared here.
func TestRegisterLimitIs249(t *testing.T) {
	const tooComplex = "function or expression too complex"
	for _, tc := range []struct {
		name, src string
		fails     bool
	}{
		{"248 call arguments", "f(" + strings.Repeat("1,", 247) + "1)", false},
		{"249 call arguments", "f(" + strings.Repeat("1,", 248) + "1)", true},
		{"249 return values", "return " + strings.Repeat("1,", 248) + "1", false},
		{"250 return values", "return " + strings.Repeat("1,", 249) + "1", true},
		{"200 locals and 48 call arguments", "local " + strings.Repeat("a,", 199) + "a\nf(" + strings.Repeat("1,", 47) + "1)", false},
		{"200 locals and 49 call arguments", "local " + strings.Repeat("a,", 199) + "a\nf(" + strings.Repeat("1,", 48) + "1)", true},
	} {
		got := loadMessage(t, tc.src)
		if failed := strings.Contains(got, tooComplex); failed != tc.fails || (!tc.fails && got != "ok") {
			t.Errorf("%s: got %q, want too complex = %v", tc.name, got, tc.fails)
		}
	}
}
