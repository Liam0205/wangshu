//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestCompiledLuaCallsAddNoCLevel checks that a Lua function called from compiled code sees the C
// depth its caller sees. In PUC a Lua-to-Lua call runs inside the same luaV_execute and costs no C
// level; the compiled tiers instead re-enter Go to run a callee they cannot call directly (DoCall
// under P3, CallBaseline and ExecutePlainCallInlineFrame under P4, TailCall under both), and that
// re-entry must not count as one.
//
// probe measures the depth: it recurses through table.sort's __lt until "C stack overflow" and
// leaves the level count in n. Each caller is called from Go, the way the reference call of probe
// is, so a caller adds exactly one Lua-to-Lua call and must reach the same n. The shapes are chosen
// so the re-entries are really taken: callers use only locals and table.sort (anything else, pcall
// included, keeps a function out of the compiled tiers); probe is vararg, which neither tier
// compiles, so a caller cannot call it inside compiled code; the first calls run with probe unarmed
// and returning normally, because P4 only switches a call site to ExecutePlainCallInlineFrame once a
// call through it has completed; and rec recurses far enough to pass P4's in-segment call depth.
func TestCompiledLuaCallsAddNoCLevel(t *testing.T) {
	const src = `local n = 0
local mt = {}
mt.__lt = function(a, b) n = n + 1 table.sort({a, b}) return false end
local A, B = setmetatable({}, mt), setmetatable({}, mt)
local function probe(...) if not ARM then return end n = 0 table.sort({A, B}) end
local o = {m = function(self) probe() end}
local function plain(k) local s = 0 for i = 1, k do s = s + i end probe() return s end
local function ret(k) local s = 0 for i = 1, k do s = s + i end local x = probe() return s end
local function method(t) t:m() end
local function tail(k) if k == 0 then return probe() end return tail(k - 1) end
local function rec(k) if k == 0 then probe() return end rec(k - 1) end
PROBE = probe
N = function() return n end
O = o
CALLERS = {plain = plain, ret = ret, method = method, tail = tail, rec = rec}`
	for _, tc := range []struct {
		name string
		arg  func(st *wangshu.State) wangshu.Value
	}{
		{"plain", func(*wangshu.State) wangshu.Value { return wangshu.Number(20) }},
		{"ret", func(*wangshu.State) wangshu.Value { return wangshu.Number(20) }},
		{"method", func(st *wangshu.State) wangshu.Value { return st.GetGlobal("O") }},
		{"tail", func(*wangshu.State) wangshu.Value { return wangshu.Number(5) }},
		{"rec", func(*wangshu.State) wangshu.Value { return wangshu.Number(40) }},
	} {
		st := wangshu.NewState(wangshu.Options{})
		st.SetForceAllPromote(true)
		prog, err := wangshu.Compile([]byte(src), "=x")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if _, err := prog.Run(st); err != nil {
			t.Fatalf("run: %v", err)
		}
		depth := func() float64 {
			r, err := st.Call(st.GetGlobal("N"))
			if err != nil || len(r) != 1 {
				t.Fatalf("N() = %v, %v", r, err)
			}
			return r[0].Number()
		}
		st.SetGlobal("ARM", wangshu.Bool(true))
		if _, err := st.Call(st.GetGlobal("PROBE")); err == nil {
			t.Fatalf("probe did not overflow")
		}
		want := depth()
		before := st.PromotionCount()
		caller := st.GetGlobal("CALLERS").AsTable().Get(wangshu.String(tc.name))
		for i := 0; i < 20; i++ {
			armed := i >= 10
			st.SetGlobal("ARM", wangshu.Bool(armed))
			_, err := st.Call(caller, tc.arg(st))
			if armed != (err != nil) {
				t.Fatalf("%s: call %d (armed %v): error %v", tc.name, i, armed, err)
			}
			if armed {
				if got := depth(); got != want {
					t.Errorf("%s: call %d reached depth %v, a direct call %v", tc.name, i, got, want)
				}
			}
		}
		if st.PromotionCount() == before {
			t.Errorf("%s: the caller was not promoted, so the compiled path went untested", tc.name)
		}
	}
}
