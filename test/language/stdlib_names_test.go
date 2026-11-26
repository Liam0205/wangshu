package language_test

import (
	"testing"

	"github.com/Liam0205/wangshu/test/testutil"
)

// TestStdlibHasNoNamesBeyondLua51 pins that the standard library exposes nothing official Lua 5.1
// lacks -- no table.unpack, no global name for the ipairs iterator (#329) -- and the behaviour that
// follows from pairs and ipairs owning their iterators the way lbaselib.c's auxopen sets them up (a
// C closure with the iterator as upvalue).
//
// Every expectation below is what PUC Lua 5.1.5 prints for the same script. test/difftest's
// TestDiff_StdlibNames compares the whole name set against a live lua5.1; these cases need no
// oracle, so they also run where lua5.1 is not installed.
func TestStdlibHasNoNamesBeyondLua51(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		// 5.2 moved unpack into table; 5.1 has only the global.
		{"no table.unpack", `return type(table.unpack) .. " " .. type(unpack)`, "nil function"},
		{"table.unpack call fails like 5.1", `return tostring(select(2, pcall(function() return table.unpack({1, 2}) end)))`,
			`[string "test"]:1: attempt to call field 'unpack' (a nil value)`},
		{"table library keys", `local ks = {} for k in pairs(table) do ks[#ks + 1] = k end table.sort(ks) return table.concat(ks, ",")`,
			"concat,foreach,foreachi,getn,insert,maxn,remove,setn,sort"},
		// The ipairs iterator has no global name on 5.1, and nothing else may hide behind a "__" prefix.
		{"no private globals", `local ks = {} for k in pairs(_G) do if type(k) == "string" and k:sub(1, 2) == "__" then ks[#ks + 1] = k end end
table.sort(ks) return table.concat(ks, ",")`, ""},
		// pairs holds next as an upvalue: rebinding the global must not reach it.
		{"pairs survives next = nil", `next = nil local n = 0 for k, v in pairs({10, 20}) do n = n + v end return tostring(n)`, "30"},
		{"pairs ignores a replaced next", `next = function() return nil end local n = 0 for k, v in pairs({10, 20}) do n = n + v end return tostring(n)`, "30"},
		// ... and what pairs hands back is that upvalue, a different function object from the global.
		{"pairs iterator is not the global next", `return tostring(pairs({}) == next)`, "false"},
		{"one iterator per library function", `return tostring(pairs({}) == pairs({})) .. " " .. tostring(ipairs({}) == ipairs({}))`, "true true"},
		{"iterators differ from each other", `return tostring(pairs({}) == ipairs({}))`, "false"},
		// The factories check their own argument like luaB_pairs / luaB_ipairs.
		{"pairs without an argument", `return tostring(select(2, pcall(pairs)))`,
			"bad argument #1 to '?' (table expected, got no value)"},
		{"ipairs without an argument", `return tostring(select(2, pcall(ipairs)))`,
			"bad argument #1 to '?' (table expected, got no value)"},
		// Called directly, the iterators check arguments like luaB_next / ipairsaux.
		{"pairs iterator works standalone", `local f = pairs({}) local k, v = f({a = 1}) return tostring(k) .. tostring(v) .. select("#", f({}))`, "a11"},
		{"pairs iterator argument error", `local f = pairs({}) return tostring(select(2, pcall(f)))`,
			"bad argument #1 to '?' (table expected, got no value)"},
		{"ipairs iterator checks arg 2 first", `local f = ipairs({}) return tostring(select(2, pcall(f)))`,
			"bad argument #2 to '?' (number expected, got no value)"},
		{"ipairs iterator then arg 1", `local f = ipairs({}) return tostring(select(2, pcall(f, 1, 1)))`,
			"bad argument #1 to '?' (table expected, got number)"},
		{"ipairs iterator with both arguments bad reports arg 2", `local f = ipairs({}) return tostring(select(2, pcall(f, 5, {})))`,
			"bad argument #2 to '?' (number expected, got table)"},
		// With a table given, a control value that is missing, nil or a boolean is still an error:
		// only numbers and numeric strings convert. A missing one is not a nil one.
		{"ipairs iterator with a table and no control value", `local f = ipairs({}) return tostring(select(2, pcall(f, {10})))`,
			"bad argument #2 to '?' (number expected, got no value)"},
		{"ipairs iterator with a nil control value", `local f = ipairs({}) return tostring(select(2, pcall(f, {10}, nil)))`,
			"bad argument #2 to '?' (number expected, got nil)"},
		{"ipairs iterator with a boolean control value", `local f = ipairs({}) return tostring(select(2, pcall(f, {10}, true)))`,
			"bad argument #2 to '?' (number expected, got boolean)"},
		{"ipairs iterator narrows like luaL_checkint", `local f = ipairs({}) return table.concat({f({10, 20, 30, 40}, 1.5)}, ",") .. " " .. table.concat({f({10, 20, 30, 40}, 4294967298)}, ",")`,
			"2,20 3,30"},
		{"ipairs iterator stops with no values", `local f = ipairs({}) return tostring(select("#", f({5}, 0))) .. tostring(select("#", f({5}, 1)))`, "20"},
		{"factory argument errors unchanged", `return tostring(select(2, pcall(function() return ipairs(nil) end))) .. " | " .. tostring(select(2, pcall(function() return pairs(1) end)))`,
			`[string "test"]:1: bad argument #1 to 'ipairs' (table expected, got nil) | [string "test"]:1: bad argument #1 to 'pairs' (table expected, got number)`},
		// The iterator is kept alive by whoever can still reach it: the factory (as its upvalue), or
		// the script holding it directly. Neither may be collected while in use.
		{"iterator outlives the global ipairs", `local f, t, c = ipairs({4, 5})
ipairs = nil collectgarbage() collectgarbage()
local junk = {} for i = 1, 200 do junk[i] = {i} end
local s = 0 for _, v in f, t, c do s = s + v end return tostring(s)`, "9"},
		{"factory keeps its iterator alive", `local ip, pr = ipairs, pairs
ipairs, pairs = nil, nil collectgarbage() collectgarbage()
local junk = {} for i = 1, 200 do junk[i] = {i} end
local s = 0 for _, v in ip({1, 2, 3}) do s = s + v end for _, v in pr({x = 10}) do s = s + v end return tostring(s)`, "16"},
		// The two LUA_COMPAT aliases official 5.1.5 ships are the same function object as what they
		// alias, not a second function that behaves alike.
		{"compat aliases are one function", `return tostring(math.mod == math.fmod) .. " " .. tostring(string.gfind == string.gmatch)`, "true true"},
		// luaL_checkint reads through lua_tonumber, so a numeric string is a valid control value.
		{"ipairs iterator coerces a numeric string", `local f = ipairs({}) local a, b = f({10, 20, 30}, "1") local c, d = f({10, 20, 30}, "0x1")
local n = 0 for _, v in f, {7, 8}, "0" do n = n + v end
return a .. "," .. b .. " " .. c .. "," .. d .. " " .. n .. " " .. tostring(select(2, pcall(f, {}, "x")))`,
			"2,20 2,20 15 bad argument #2 to '?' (number expected, got string)"},
		// The same answers when the loop sits in a function that is called many times. The helper
		// takes the iterator triple as arguments because that is the form the P3 / P4 tiers
		// promote; a function that calls the global ipairs / pairs itself stays in the interpreter.
		// This case only pins the answer on every build. That the loop really runs promoted, and
		// still agrees, is asserted by test/difftest's TestTiered_StdlibNames.
		{"iterating inside a repeatedly called function", `next = nil
local function loop(f, s, c) local n = 0 for _, v in f, s, c do n = n + v end return n end
local t, total = {1, 2, 3}, 0
for i = 1, 3000 do total = total + loop(ipairs(t)) + loop(pairs(t)) end return tostring(total)`, "36000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
