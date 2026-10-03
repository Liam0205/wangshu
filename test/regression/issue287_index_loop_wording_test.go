package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestIndexLoopUsesTheLua51Wording covers #287: luaV_gettable / luaV_settable give up after
// MAXTAGLOOP (100) table steps with "loop in gettable" / "loop in settable". wangshu reported 5.2's
// "'__index' chain too long; possible loop". The bound itself already matched: a chain of 99 tables
// still reaches its handler. Expectations are lua5.1's.
func TestIndexLoopUsesTheLua51Wording(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"__index pointing at itself",
			`local t = setmetatable({}, {}) getmetatable(t).__index = t
print(pcall(function() return t.x end))
`,
			`false	x:2: loop in gettable
`},
		{"__newindex pointing at itself",
			`local u = setmetatable({}, {}) getmetatable(u).__newindex = u
print(pcall(function() u.x = 1 end))
`,
			`false	x:2: loop in settable
`},
		{"two tables indexing each other",
			`local a, b = {}, {}
setmetatable(a, {__index = b}) setmetatable(b, {__index = a})
print(pcall(function() return a.k end))
setmetatable(a, {__newindex = b}) setmetatable(b, {__newindex = a})
print(pcall(function() a.k = 1 end))
`,
			`false	x:3: loop in gettable
false	x:5: loop in settable
`},
		{"a chain of 99 tables still reaches its function",
			`local c = {} local cur = c
for i = 1, 99 do local n = {} setmetatable(cur, {__index = n}) cur = n end
setmetatable(cur, {__index = function() return 'end' end})
print(pcall(function() return c.z end))
`,
			`true	end
`},
		{"a chain of 100 tables is a loop",
			`local d = {} local cur = d
for i = 1, 100 do local n = {} setmetatable(cur, {__index = n}) cur = n end
setmetatable(cur, {__index = function() return 'end' end})
print(pcall(function() return d.z end))
`,
			`false	x:4: loop in gettable
`},
	} {
		if got := printedBy(t, wangshu.Options{}, tc.src); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
