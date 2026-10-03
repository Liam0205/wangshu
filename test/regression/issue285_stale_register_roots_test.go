package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestStaleRegistersAreNotGCRoots covers #285: lua5.1's traversestack marks a thread's stack only up
// to L->top, and inside a C function L->top is that function's own last argument, so what returned
// callees and dead locals left in the caller's registers above the call is collectable by the time
// collectgarbage runs. wangshu scanned up to the Lua frame's register limit and kept those values
// alive. Each line measures whether a 200000-element table built inside f survives a full collection
// after f returns; only the one assigned to an upvalue should. Expectations are lua5.1's.
func TestStaleRegistersAreNotGCRoots(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"registers above a host call are not roots",
			`local function big() local t = {} for i = 1, 200000 do t[i] = i end return t end
local function measure(f)
  collectgarbage() collectgarbage()
  local base = collectgarbage("count")
  f()
  collectgarbage() collectgarbage()
  return collectgarbage("count") - base > 1000
end
print(measure(function() local function run() local ok, v = pcall(big) end run() end))
print(measure(function() do local ok, v = pcall(big) end end))
print(measure(function() local function run() pcall(big) end run() end))
print(measure(function() local function run() local t = big() end run() end))
print(measure(function() local function run() return big() end run() end))
print(measure(function() local t = big() t = nil end))
local keep
print(measure(function() keep = big() end))
keep = nil
print(measure(function() local t = big() local s = 0 for i = 1, 3 do s = s + #t end end))
print(measure(function() local co = coroutine.create(function() local function inner() local t = big() end inner() coroutine.yield() end) coroutine.resume(co) _G.CO = co end))
_G.CO = nil
print(measure(function() local t = setmetatable({}, {__index = function() local x = big() return 1 end}) local _ = t.k end))
print(measure(function() local s = 0 for k, v in pairs({1, 2}) do local t = big() end end))
print(measure(function() local t = {} table.sort({3, 2, 1}, function(a, b) local x = big() return a < b end) end))
print(measure(function() string.gsub("aaa", "a", function() local x = big() end) end))
`,
			`false
false
false
false
false
false
true
false
false
false
false
false
false
`},
	} {
		if got := printedBy(t, wangshu.Options{}, tc.src); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// TestClosuresOutliveAnErrorUnwind covers the latent bug #285's fix exposed: an error unwinding Lua
// frames, whether caught by pcall or killing a coroutine, left the frames' upvalues open. luaD_pcall
// closes them first (luaF_close), so a closure made before the error keeps the value its variable
// had. Open, they pointed at stack slots that a later call reuses or that the collector clears once
// they are above top. Expectations are lua5.1's.
func TestClosuresOutliveAnErrorUnwind(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"a closure made before an error keeps its variable (loop)",
			`local b
local function f(x)
  while 1 do
    local a = 'xuxu'
    b = function(op, y) if op == 'set' then a = x + y else return a end end
    error()
  end
end
pcall(f, 4)
print(b('get'))
`,
			`xuxu
`},
		{"a closure made before an error keeps its variable across later calls",
			`local b
local function f(x)
  local a = 'xuxu'
  b = function(op, y) if op == 'set' then a = x + y else return a end end
  error()
end
pcall(f, 4)
local function clobber(p, q, r, s) local u, v, w = "s1", "s2", "s3" return u end
clobber() print(b('get'))
pcall(clobber) print(b('get'))
b('set', 10) print(b('get'))
`,
			`xuxu
xuxu
14
`},
		{"a closure made in a coroutine that died by error keeps its variable",
			`local b
local co = coroutine.create(function(x)
  local a = 'xuxu' .. x
  b = function(op, y) if op == 'set' then a = y else return a end end
  error("die")
end)
print(coroutine.resume(co, "!"))
local function clobber(p, q, r, s) local u, v, w = "s1", "s2", "s3" return u end
clobber() collectgarbage() collectgarbage()
print(b('get'))
b('set', 10) print(b('get'))
co = nil collectgarbage() collectgarbage()
print(b('get'))
local w = coroutine.wrap(function() local a = {1} b = function() return a[1] end error({}) end)
pcall(w) collectgarbage() collectgarbage() print(b())
`,
			`false	x:5: die
xuxu!
10
10
1
`},
	} {
		if got := printedBy(t, wangshu.Options{}, tc.src); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
