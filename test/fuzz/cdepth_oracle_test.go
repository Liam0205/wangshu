//go:build wangshu_oracle_cgo && cgo

package fuzz_test

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/oracle"
)

// TestCDepthMatchesEmbeddedPUC pins the C-call depth a chunk starts at against the in-process PUC
// oracle, which runs a chunk the way an embedder does: one lua_pcall, nothing of lua.c's around it.
// Everything a script can observe about the C-call and syntax-level limits -- how deep host->Lua
// nesting goes before "C stack overflow", how many syntax levels loadstring still has -- is measured
// from that starting point, so a chunk that started at depth 0 (as wangshu's did) reached one level
// further than embedded PUC on every one of these shapes. Standalone lua5.1 is one more level
// shallower again (lua.c's lua_cpcall(pmain)), which is not something an embedded VM has.
func TestCDepthMatchesEmbeddedPUC(t *testing.T) {
	keep := enumerateGlobals(t)
	prelude := oracle.Prelude(keep)
	for _, tc := range []struct{ name, src string }{
		{"__index recursion", `local n = 0
local t = setmetatable({}, {})
getmetatable(t).__index = function(t, k) n = n + 1 return t[k] end
pcall(function() return t.x end)
print(n)`},
		{"pcall recursion", `local n = 0
local function f() n = n + 1 pcall(f) end
f()
print(n)`},
		{"sort comparator recursion", `local n = 0
local function f() n = n + 1 table.sort({2, 1}, function(a, b) f() return a < b end) end
pcall(f)
print(n)`},
		{"nested resume", `local n = 0
local function f() n = n + 1 coroutine.resume(coroutine.create(f)) end
f()
print(n)`},
		{"syntax levels at top level", `local lo, hi = 0, 400
while lo < hi do
  local mid = math.floor((lo + hi + 1) / 2)
  if loadstring("return " .. string.rep("(", mid) .. "1" .. string.rep(")", mid)) then lo = mid else hi = mid - 1 end
end
print(lo)`},
		{"deepest level can compile and resume", `local last, rs
local t = setmetatable({}, {})
getmetatable(t).__index = function(t, k)
  last = loadstring("") ~= nil
  rs = coroutine.resume(coroutine.create(function() end))
  return t[k]
end
pcall(function() return t.x end)
print(last, rs)`},
	} {
		or := oracle.Exec(tc.src, prelude, oracle.Limits{})
		if or.Verdict != oracle.VerdictOK {
			t.Fatalf("%s: oracle %v: %s", tc.name, or.Verdict, or.Err)
		}
		wv, wout, werr := runWangshuSide(t, tc.src, prelude)
		if wv != oracle.VerdictOK {
			t.Fatalf("%s: wangshu %v: %s", tc.name, wv, werr)
		}
		if or.Output != wout {
			t.Errorf("%s: oracle %q, wangshu %q", tc.name, or.Output, wout)
		}
	}
}
