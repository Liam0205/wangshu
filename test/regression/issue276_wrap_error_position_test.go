package regression

import (
	"testing"

	"github.com/Liam0205/wangshu/test/testutil"
)

// TestCoroutineWrapPrefixesCallerPosition covers #276: lbaselib.c's auxwrap rethrows a coroutine's
// string error with luaL_where(L, 1) prepended -- the position of whoever called the wrap function.
// wangshu forwarded the error untouched, so a direct call from Lua lost that outer prefix. A C caller
// (pcall, a sort comparator) gives luaL_where an empty prefix, and non-string error values pass
// through as they are. Expectations are lua5.1's.
func TestCoroutineWrapPrefixesCallerPosition(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"direct call from Lua prefixes the caller",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local f = coroutine.wrap(function() error("boom") end)
return e(function()
  f()
end)`,
			`false [string "test"]:4: [string "test"]:2: boom`},
		{"tail call from Lua prefixes the caller",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local f = coroutine.wrap(function() error("boom") end)
return e(function()
  return f()
end)`,
			`false [string "test"]:4: [string "test"]:2: boom`},
		{"pcall(f) has a C caller: no extra prefix",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local f = coroutine.wrap(function() error("boom") end)
return e(f)`,
			`false [string "test"]:2: boom`},
		{"runtime error inside the coroutine",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local f = coroutine.wrap(function() local x = nil; return x.y end)
return e(function()
  f()
end)`,
			`false [string "test"]:4: [string "test"]:2: attempt to index local 'x' (a nil value)`},
		{"error(m, 0) inside still gets the outer prefix",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local f = coroutine.wrap(function() error("lvl0", 0) end)
return e(function()
  f()
end)`,
			`false [string "test"]:4: lvl0`},
		{"a number error is a string to lua_isstring",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local f = coroutine.wrap(function() error(42) end)
return e(function()
  f()
end)`,
			`false [string "test"]:4: [string "test"]:2: 42`},
		{"non-string error values pass through",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local f = coroutine.wrap(function() error({}) end)
local ok, m = pcall(function() f() end)
return type(m)`,
			`table`},
		{"nested wraps stack one prefix per level",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local inner = coroutine.wrap(function() error("deep") end)
local outer = coroutine.wrap(function() inner() end)
return e(function() outer() end)`,
			`false [string "test"]:4: [string "test"]:3: [string "test"]:2: deep`},
		{"a wrap used as sort comparator has a C caller",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
return e(function() table.sort({3, 2, 1}, coroutine.wrap(function() error("s") end)) end)`,
			`false [string "test"]:2: s`},
		{"resuming a dead wrap",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local f = coroutine.wrap(function() end)
f()
return e(function()
  f()
end)`,
			`false [string "test"]:5: cannot resume dead coroutine`},
		{"values still flow through yield",
			`local f = coroutine.wrap(function(a) local b = coroutine.yield(a + 1) return b * 2 end)
return f(1) .. " " .. f(10)`,
			`2 20`},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
