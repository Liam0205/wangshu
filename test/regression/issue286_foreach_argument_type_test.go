package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestForeachArgumentErrorNamesTheType covers #286: table.foreach and table.foreachi check their
// second argument with luaL_checktype, whose tag_error reports "function expected, got <type>" ("got
// no value" when it is missing). wangshu reported a bare "function expected". Expectations are
// lua5.1's.
func TestForeachArgumentErrorNamesTheType(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"table.foreach with a non-function",
			`print(pcall(table.foreach, {1}, {}))
print(pcall(table.foreach, {1}, nil))
print(pcall(table.foreach, {1}))
`,
			`false	bad argument #2 to '?' (function expected, got table)
false	bad argument #2 to '?' (function expected, got nil)
false	bad argument #2 to '?' (function expected, got no value)
`},
		{"table.foreachi with a non-function",
			`print(pcall(table.foreachi, {1}, 1))
print(pcall(table.foreachi, {1}, 'f'))
print(pcall(table.foreachi, {}))
`,
			`false	bad argument #2 to '?' (function expected, got number)
false	bad argument #2 to '?' (function expected, got string)
false	bad argument #2 to '?' (function expected, got no value)
`},
		{"called from Lua code the position and name are added",
			`print(pcall(function() table.foreach({}, true) end))
print(pcall(function() local f = table.foreachi; f({}, 2) end))
`,
			`false	x:1: bad argument #2 to 'foreach' (function expected, got boolean)
false	x:2: bad argument #2 to 'f' (function expected, got number)
`},
	} {
		if got := printedBy(t, wangshu.Options{}, tc.src); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
