package regression

import (
	"strings"
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestSyntaxErrorChunkNameUsesMaxSrc covers the chunk name in compile-time errors: luaX_lexerror,
// which every lexer, parser and code-generator error goes through, formats it with llex.c's MAXSRC
// (80) where runtime positions, tracebacks and short_src use LUA_IDSIZE (60). Every chunk name was
// formatted with 60, so a [string "..."] longer than 43 bytes, an "@file" longer than 52 or an
// "=name" longer than 59 was cut early in syntax errors. Expectations are lua5.1's.
func TestSyntaxErrorChunkNameUsesMaxSrc(t *testing.T) {
	const src = `local a60 = string.rep("a", 60)
local a80 = string.rep("a", 80)
local out = {}
local function add(...) out[#out + 1] = table.concat({...}, " | ") end
add(select(2, loadstring("x=", a60)))
add(select(2, pcall(loadstring("error('e')", a60))))
add(select(2, loadstring("x=", a80)))
add(select(2, loadstring("x=", "@" .. a60)))
add(select(2, pcall(loadstring("error('e')", "@" .. a60))))
add(select(2, loadstring("x=", "=" .. a80)))
add(select(2, pcall(loadstring("error('e')", "=" .. a80))))
add(select(2, loadstring("x=\n", "one\ntwo")))
add(select(2, loadstring("return " .. string.rep("(", 300) .. "1" .. string.rep(")", 300), a60)))
add(debug.getinfo(loadstring("return 1", a60), "S").short_src)
OUT = table.concat(out, "\n")`
	a := func(n int) string { return strings.Repeat("a", n) }
	want := strings.Join([]string{
		`[string "` + a(60) + `"]:1: unexpected symbol near '<eof>'`,
		`[string "` + a(43) + `..."]:1: e`,
		`[string "` + a(63) + `..."]:1: unexpected symbol near '<eof>'`,
		a(60) + `:1: unexpected symbol near '<eof>'`,
		`...` + a(52) + `:1: e`,
		a(79) + `:1: unexpected symbol near '<eof>'`,
		a(59) + `:1: e`,
		`[string "one..."]:2: unexpected symbol near '<eof>'`,
		`[string "` + a(60) + `"]:1: chunk has too many syntax levels`,
		`[string "` + a(43) + `..."]`,
	}, "\n")
	st := wangshu.NewState(wangshu.Options{})
	prog, err := wangshu.Compile([]byte(src), "@x")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := st.GetGlobal("OUT").Str(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
