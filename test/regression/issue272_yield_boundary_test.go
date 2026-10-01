package regression

import (
	"testing"

	"github.com/Liam0205/wangshu/test/testutil"
)

// TestYieldAcrossHostBoundaryIsRejected covers #272: a yield is legal only while no host->Lua
// reentry sits between it and the resume (ldo.c lua_yield checks nCcalls > baseCcalls). The check
// used to live only where a sentinel bubbled out of a LUA function, so coroutine.yield called
// directly as a metamethod or comparator suspended the coroutine mid-comparison and the next resume
// failed with "cannot resume: no pending yield point". Expectations are lua5.1's.
func TestYieldAcrossHostBoundaryIsRejected(t *testing.T) {
	const pre = `local function run(f, ...) local co = coroutine.create(f) local r = {coroutine.resume(co, ...)} r[#r + 1] = coroutine.status(co) for i = 1, #r do r[i] = tostring(r[i]) end return table.concat(r, " ") end
local m = {__lt = coroutine.yield}
`
	for _, tc := range []struct{ name, src, want string }{
		{"__lt is coroutine.yield", `return run(function() return setmetatable({}, m) < setmetatable({}, m) end)`,
			"false attempt to yield across metamethod/C-call boundary dead"},
		{"default sort comparator via __lt", `return run(function() table.sort({setmetatable({}, m), setmetatable({}, m)}) end)`,
			"false attempt to yield across metamethod/C-call boundary dead"},
		{"sort comparator is coroutine.yield", `return run(function() table.sort({3, 1, 2}, coroutine.yield) end)`,
			"false attempt to yield across metamethod/C-call boundary dead"},
		{"__index is coroutine.yield", `return run(function() return setmetatable({}, {__index = coroutine.yield}).x end)`,
			"false attempt to yield across metamethod/C-call boundary dead"},
		{"__tostring is coroutine.yield", `return run(function() return tostring(setmetatable({}, {__tostring = coroutine.yield})) end)`,
			"false attempt to yield across metamethod/C-call boundary dead"},
		{"gsub replacement is coroutine.yield", `return run(function() return string.gsub("ab", ".", coroutine.yield) end)`,
			"false attempt to yield across metamethod/C-call boundary dead"},
		{"for-in iterator is coroutine.yield", `return run(function() for k in coroutine.yield do end end)`,
			"false attempt to yield across metamethod/C-call boundary dead"},
		{"pcall(coroutine.yield) inside a coroutine", `return run(function() return pcall(coroutine.yield, 1) end)`,
			"true false attempt to yield across metamethod/C-call boundary dead"},
		{"yield on the main thread", `return tostring(select(2, pcall(coroutine.yield)))`,
			"attempt to yield across metamethod/C-call boundary"},
		{"Lua __lt that yields, from a Lua caller: no position",
			`local n = {__lt = function() coroutine.yield() end}
			return run(function() return setmetatable({}, n) < setmetatable({}, n) end)`,
			"false attempt to yield across metamethod/C-call boundary dead"},
		{"__call is coroutine.yield is a plain call and may yield",
			`local co = coroutine.create(function() return setmetatable({}, {__call = coroutine.yield})(5) end)
			local _, a, b = coroutine.resume(co) local ok, r = coroutine.resume(co, 7)
			return type(a) .. " " .. tostring(b) .. " " .. tostring(ok) .. " " .. tostring(r) .. " " .. coroutine.status(co)`,
			"table 5 true 7 dead"},
		{"a coroutine resumed inside a metamethod has its own base",
			`local w = {__index = function(t, k) return coroutine.wrap(function() coroutine.yield(k .. "!") end)() end}
			return run(function() return setmetatable({}, w).yo end)`,
			"true yo! dead"},
	} {
		if got := testutil.RunOne(t, pre+tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestTailCalledYieldResumes covers the other half found while fixing #272: `return
// coroutine.yield(x)` -- a host tail call that yields -- recorded no resume point, so the second
// resume failed with "cannot resume: no pending yield point". The official suite's "yields in tail
// calls" case (closure.lua) is cut off before this line by the setfenv exemption, which is why it
// was never caught. Expectations are lua5.1's.
func TestTailCalledYieldResumes(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"direct", `local co = coroutine.create(function() return coroutine.yield(1) end)
			local _, a = coroutine.resume(co) local ok, b = coroutine.resume(co, 2)
			return a .. " " .. tostring(ok) .. " " .. tostring(b) .. " " .. coroutine.status(co)`, "1 true 2 dead"},
		{"official closure.lua case",
			`local function foo(i) return coroutine.yield(i) end
			local f = coroutine.wrap(function() for i = 1, 10 do assert(foo(i) == _G.x) end return "a" end)
			local o = {} for i = 1, 10 do _G.x = i o[#o + 1] = f(i) end _G.x = "xuxu" o[#o + 1] = f("xuxu")
			return table.concat(o, " ")`, "1 2 3 4 5 6 7 8 9 10 a"},
		{"multiple values through a tail-call chain",
			`local function deep(n) if n == 0 then return coroutine.yield("d", 1, 2) end return deep(n - 1) end
			local co = coroutine.create(function() local a, b, c = deep(3) return "r", a, b, c, select("#", deep(0)) end)
			local o = {}
			for _, args in ipairs({{}, {"x", "y", "z"}, {}}) do
			  local r = {coroutine.resume(co, unpack(args))} for i = 1, #r do r[i] = tostring(r[i]) end o[#o + 1] = table.concat(r, ",")
			end
			return table.concat(o, " | ")`, "true,d,1,2 | true,d,1,2 | true,r,x,y,z,0"},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
