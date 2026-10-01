package regression

import (
	"testing"

	"github.com/Liam0205/wangshu/test/testutil"
)

// TestHostTriggeredMetamethodCountsTheCFrame covers #277: an __lt or __index handler reached from a
// host function (table.sort's default comparator, gsub's table replacement) runs above that function's
// C frame, so error(m, 2) lands on the C frame (no position) and error(m, 3) on its Lua caller.
// callMetaHandler adds no level by design -- the VM's own metamethod dispatch interposes no C frame --
// and nothing counted the host caller, so both levels were one off. Direct a < b must not change.
// Expectations are lua5.1's.
func TestHostTriggeredMetamethodCountsTheCFrame(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"__lt error(m, 2) from table.sort lands on sort",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local mt = {__lt = function() error("e2", 2) end}
local t = {setmetatable({}, mt), setmetatable({}, mt), setmetatable({}, mt)}
return e(function()
  table.sort(t)
end)`,
			`false e2`},
		{"__lt error(m, 3) from table.sort names sort's caller",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local mt = {__lt = function() error("e3", 3) end}
local t = {setmetatable({}, mt), setmetatable({}, mt), setmetatable({}, mt)}
return e(function()
  table.sort(t)
end)`,
			`false [string "test"]:5: e3`},
		{"callable-table __lt from table.sort",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local mt = {__lt = setmetatable({}, {__call = function() error("c2", 2) end})}
local t = {setmetatable({}, mt), setmetatable({}, mt), setmetatable({}, mt)}
return e(function()
  table.sort(t)
end)`,
			`false c2`},
		{"direct a < b is unchanged (level 2)",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local mt = {__lt = function() error("d2", 2) end}
local a, b = setmetatable({}, mt), setmetatable({}, mt)
return e(function()
  return a < b
end)`,
			`false [string "test"]:5: d2`},
		{"direct a < b is unchanged (level 3)",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local mt = {__lt = function() error("d3", 3) end}
local a, b = setmetatable({}, mt), setmetatable({}, mt)
local function cmp()
  return a < b
end
return e(function()
  return cmp()
end)`,
			`false d3`},
		{"gsub table repl: __index error(m, 2) lands on gsub",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local r = setmetatable({}, {__index = function() error("i2", 2) end})
return e(function()
  return string.gsub("a", "a", r)
end)`,
			`false i2`},
		{"gsub table repl: __index error(m, 3)",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local r = setmetatable({}, {__index = function() error("i3", 3) end})
return e(function()
  return string.gsub("a", "a", r)
end)`,
			`false [string "test"]:4: i3`},
		{"gsub table repl: runtime error keeps the handler line",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local r = setmetatable({}, {__index = function() return nil + 1 end})
return e(function()
  return string.gsub("a", "a", r)
end)`,
			`false [string "test"]:2: attempt to perform arithmetic on a nil value`},
		{"sort comparator error(m, 2) was already right",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
return e(function()
  table.sort({3, 2, 1}, function() error("k2", 2) end)
end)`,
			`false k2`},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
