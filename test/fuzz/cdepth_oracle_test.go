//go:build wangshu_oracle_cgo && cgo

package fuzz_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/oracle"
)

// TestCDepthMatchesEmbeddedPUC pins the C-call depth a chunk starts at against the in-process PUC
// oracle, which runs a chunk the way an embedder does: one lua_pcall, nothing of lua.c's around it.
// How deep host->Lua nesting goes before "C stack overflow" and how many syntax levels loadstring
// still has are both measured from that starting point; with the chunk started at depth 0 (as wangshu
// did), the first five shapes below reached one level further than embedded PUC. The sixth (the
// deepest reachable level can still compile and resume) prints the same either way; it pins the
// boundary itself. Standalone lua5.1 is one level shallower again (lua.c's lua_cpcall(pmain)), which
// an embedded VM does not have.
//
// Each shape also runs force-promoted when the build has a compiled tier, and must still match the
// oracle. That pass does not show the recursion ran compiled: force-all only lifts the promotion
// threshold, several of these functions fail the compilability check (pcall, loadstring, coroutines),
// and PromotionCount cannot tell which function was promoted (the prelude's own helpers count too).
// Whether a compiled frame calling a Lua function costs a C level is checked per re-entry path by
// TestCompiledLuaCallsAddNoCLevel (test/regression) and TestInlineFrameCalleeAddsNoCLevel
// (internal/crescent); TestCDepthOfAPromotedFunctionCalledFromGo below covers a promoted function
// entered from Go. CI runs both tests here by name in the oracle-smoke job.
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
		if !tieredBuild {
			continue
		}
		tv, tout, terr, _ := runTieredSide(t, tc.src, prelude)
		if tv != oracle.VerdictOK {
			t.Fatalf("%s (force-all): wangshu %v: %s", tc.name, tv, terr)
		}
		if or.Output != tout {
			t.Errorf("%s (force-all): oracle %q, wangshu %q", tc.name, or.Output, tout)
		}
	}
}

// TestCDepthOfAPromotedFunctionCalledFromGo covers the other way a chunk-level call starts: the host
// calling an already-promoted Lua function directly (State.Call), which callOnStack sends straight to
// enterGibbous rather than the interpreter. In PUC that is one lua_pcall, the same single C level as
// running a chunk, so the function must see the depth the oracle's main chunk gives the same call.
func TestCDepthOfAPromotedFunctionCalledFromGo(t *testing.T) {
	if !topPromotes {
		t.Skip("this build does not compile top (no compiled tier, or P4 off amd64)")
	}
	keep := enumerateGlobals(t)
	prelude := oracle.Prelude(keep)
	// top must itself be promoted for State.Call to take the enterGibbous branch; force-all promotes it on
	// its first call (pcall(top) in the chunk). table.sort's C stack overflow escapes top, and N reads how
	// deep the __lt recursion got. The oracle can only call top through pcall from its main chunk, which
	// puts top one C level deeper than a host call straight from Go (main chunk lua_pcall, then pcall's
	// luaD_call, versus State.Call's single level), so wangshu must reach exactly one level further.
	const src = `local n = 0
local mt = {}
mt.__lt = function(a, b) n = n + 1 table.sort({a, b}) return false end
local A, B = setmetatable({}, mt), setmetatable({}, mt)
local function top() n = 0 table.sort({A, B}) return n end
pcall(top)
TOP = top
N = function() return n end`
	or := oracle.Exec(src+"\npcall(top) print(n)", prelude, oracle.Limits{})
	if or.Verdict != oracle.VerdictOK {
		t.Fatalf("oracle %v: %s", or.Verdict, or.Err)
	}
	viaPcall, err := strconv.Atoi(strings.TrimSpace(or.Output))
	if err != nil {
		t.Fatalf("oracle output %q", or.Output)
	}
	want := strconv.Itoa(viaPcall + 1)

	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	run := func(chunk string) {
		prog, err := wangshu.Compile([]byte(chunk), "@x")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if _, err := prog.Run(st); err != nil {
			t.Fatalf("run: %v", err)
		}
	}
	run(prelude)
	afterPrelude := st.PromotionCount()
	run(src)
	// The script's main chunk is one promotion and top should be another. This is only a coarse guard:
	// PromotionCount cannot say which function was promoted, so it would not notice top dropping out
	// while some other function got promoted. That top does take the enterGibbous branch today was
	// shown by mutation: with that branch not counting its C level, this test fails on P3 and P4.
	if st.PromotionCount() < afterPrelude+2 {
		t.Fatalf("top was not promoted before the Go-side call (promotions %d -> %d)", afterPrelude, st.PromotionCount())
	}
	if _, err := st.Call(st.GetGlobal("TOP")); err == nil {
		t.Fatalf("Call(TOP) did not overflow")
	}
	res, err := st.Call(st.GetGlobal("N"))
	if err != nil || len(res) != 1 || !res[0].IsNumber() {
		t.Fatalf("Call(N) = %v, %v", res, err)
	}
	if got := strconv.FormatFloat(res[0].Number(), 'g', -1, 64); got != want {
		t.Errorf("depth reached from a Go-side call: oracle %s, wangshu %s", want, got)
	}
}
