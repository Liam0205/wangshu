package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestChunkNameStopsAtNUL covers a case found while aligning #282's error positions: lua_load takes
// the chunk name as a C string, so a name holding a NUL byte is cut there -- in error positions, in
// debug.getinfo's source and short_src, for "=" and "@" names alike. wangshu kept the whole name.
// Expectations are lua5.1's.
func TestChunkNameStopsAtNUL(t *testing.T) {
	src := `print(pcall(loadstring("error('e')", "q\0r")))
local i = loadstring("return debug.getinfo(1, 'S')", "q\0r")()
print(#i.source, i.source, i.short_src)
print(loadstring("x = ", "=a\0b"))
print(loadstring("x = ", "@a\0b"))
print(pcall(loadstring("error('e')", "@f\0g")))
print(pcall(loadstring("local t = nil; t.x = 1", "=n\0m")))
`
	want := `false	[string "q"]:1: e
1	q	[string "q"]
nil	a:1: unexpected symbol near '<eof>'
nil	a:1: unexpected symbol near '<eof>'
false	f:1: e
false	n:1: attempt to index local 't' (a nil value)
`
	if got := printedBy(t, wangshu.Options{}, src); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}
