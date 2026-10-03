package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestLoadReturnsTheReadersErrorValue covers #283: luaD_protectedparser catches an error the reader
// raises and lua_load leaves that value on the stack, so load returns nil and the error value itself
// -- nil for load(error), the table for error({}). wangshu turned it into a string ("" and
// "table: 0x..."). Expectations are lua5.1's.
func TestLoadReturnsTheReadersErrorValue(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"load(error) gives nil, nil",
			`print(load(error))
print(select('#', load(error)))
`,
			`nil	nil
2
`},
		{"error(nil) from a reader",
			`print(load(function() error(nil) end))
`,
			`nil	nil
`},
		{"a table error value comes back as the table",
			`local t = {}
local f, m = load(function() error(t) end)
print(f, m == t, type(m))
local _, f2, m2 = pcall(load, function() error(t) end)
print(f2, m2 == t)
`,
			`nil	true	table
nil	true
`},
		{"numbers and booleans are not converted",
			`print(load(function() error(42, 0) end))
print(type(select(2, load(function() error(42, 0) end))))
print(load(function() error(true) end))
`,
			`nil	42
stack traceback:
	[C]: in function 'error'
	x:1: in function <x:1>
	[C]: in function 'load'
	x:1: in main chunk
	[C]: ?
string
nil	true
`},
		{"string errors keep their position",
			`print(load(function() error('s') end))
print(load(function() error('s', 0) end))
print(load(function() local x = nil; return x.y end))
`,
			`nil	x:1: s
stack traceback:
	[C]: in function 'error'
	x:1: in function <x:1>
	[C]: in function 'load'
	x:1: in main chunk
	[C]: ?
nil	s
stack traceback:
	[C]: in function 'error'
	x:2: in function <x:2>
	[C]: in function 'load'
	x:2: in main chunk
	[C]: ?
nil	x:3: attempt to index local 'x' (a nil value)
stack traceback:
	x:3: in function <x:3>
	[C]: in function 'load'
	x:3: in main chunk
	[C]: ?
`},
		{"under xpcall the handler result is returned",
			`print(xpcall(function() return load(function() error({}) end) end, function(m) return 'H:' .. type(m) end))
print(xpcall(function() return load(error) end, function(m) return 'H:' .. type(m) end))
`,
			`true	nil	H:table
true	nil	H:nil
`},
		{"inside a coroutine",
			`print(coroutine.resume(coroutine.create(function() return load(error) end)))
print(type(select(2, coroutine.wrap(function() return load(function() error({}) end) end)())))
`,
			`true	nil	nil
table
`},
	} {
		if got := printedBy(t, wangshu.Options{}, tc.src); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
