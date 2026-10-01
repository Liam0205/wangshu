package regression

import (
	"testing"

	"github.com/Liam0205/wangshu/test/testutil"
)

// TestErrorsRaisedInsideHostFunctionsAreBare covers #278: luaG_runerror adds a position only for a
// Lua frame, so errors PUC raises from inside a C function -- luaH_next's "invalid key to 'next'",
// luaH_set's "table index is nil/NaN" -- carry none, and neither does a luaL_error from a host
// function whose caller is another host function (gsub's replacement, sort's comparator, foreach's
// callback). wangshu let the interpreter's CALL site prefix the Lua line on all of them. Errors from
// a host function called directly by Lua, and TFORLOOP's named call site, must keep their prefix.
// Expectations are lua5.1's.
func TestErrorsRaisedInsideHostFunctionsAreBare(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"next with a foreign key",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
return e(function()
  return next({}, {})
end)`,
			`false invalid key to 'next'`},
		{"next through pcall",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
return e(next, {}, {})`,
			`false invalid key to 'next'`},
		{"rawset nil key",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
return e(function()
  rawset({}, nil, 1)
end)`,
			`false table index is nil`},
		{"rawset NaN key",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
return e(function()
  rawset({}, 0/0, 1)
end)`,
			`false table index is NaN`},
		{"generic for with next and a bad key",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
return e(function()
  for k in next, {}, {} do end
end)`,
			`false invalid key to 'next'`},
		{"next as a sort comparator",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
return e(function()
  table.sort({{}, {}, {}}, next)
end)`,
			`false invalid key to 'next'`},
		{"error as gsub replacement",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
return e(function()
  return string.gsub("a", "a", error)
end)`,
			`false a`},
		{"string.rep as gsub replacement: arg error to '?'",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
return e(function()
  return string.gsub("a", "a", string.rep)
end)`,
			`false bad argument #2 to '?' (number expected, got no value)`},
		{"rawset as sort comparator",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
return e(function()
  table.sort({3, 2, 1}, rawset)
end)`,
			`false bad argument #1 to '?' (table expected, got number)`},
		{"error as foreach callback",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
return e(function()
  table.foreach({x = 1}, error)
end)`,
			`false x`},
		{"luaL_error from a Lua call keeps the caller line",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
return e(function()
  return unpack({}, 1, 1e8)
end)`,
			`false [string "test"]:3: too many results to unpack`},
		{"arg error from a Lua call keeps the caller line",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
return e(function()
  return string.rep()
end)`,
			`false [string "test"]:3: bad argument #1 to 'rep' (string expected, got no value)`},
		{"TFORLOOP is a Lua caller and is named",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
return e(function()
  for k in rawset, {}, nil do end
end)`,
			`false [string "test"]:3: bad argument #3 to '(for generator)' (value expected)`},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
