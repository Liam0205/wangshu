//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"testing"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestInlineFrameCalleeAddsNoCLevel covers the compiled-frame re-entry that
// test/regression's TestCompiledLuaCallsAddNoCLevel does not reach through the
// public API: ExecuteCalleeFromInlineFrame, taken by the PJ5 SELF frame-inline
// path (`t:m()` compiled through the SELF spec template), which this package's
// loadFnP4 harness does reach. The callee's Lua body is re-entered from Go; in
// PUC that is a Lua-to-Lua call, so the C depth the callee sees must be its
// caller's. obs records nCcalls (script-visible) and luaReentry from inside m;
// a raised luaReentry shows the call went through a compiled-frame re-entry.
func TestInlineFrameCalleeAddsNoCLevel(t *testing.T) {
	const src = `local obs = obs
local o = { m = function(self) obs() end }
local function caller(t) t:m() end
obs()
for i = 1, 50 do caller(o) end`
	st, mainCl := loadFnP4(t, src)
	var depths, reentries []int
	id := st.RegisterHostFn(func(s *State, _ []value.Value) ([]value.Value, *LuaError) {
		depths = append(depths, s.nCcalls)
		reentries = append(reentries, s.luaReentry)
		return nil, nil
	})
	st.SetGlobal("obs", value.MakeGC(value.TagFunction, st.MakeHostClosure(id)))
	// The spec template needs the SELF inline cache filled when caller is compiled: run once in the
	// interpreter, then again with force-all (as TestPJ5_SelfCall_E2E_SpecTemplate_WarmupThenForce).
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 0); err != nil {
		t.Fatalf("warm-up run: %v", err)
	}
	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	depths, reentries = depths[:0], reentries[:0]
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 0); err != nil {
		t.Fatalf("run: %v", err)
	}
	if jit.SpecSelfCallSpecHits() == specBefore {
		t.Fatalf("caller was not compiled through the SELF spec template, so ExecuteCalleeFromInlineFrame went untested")
	}
	inline := 0
	for i := 1; i < len(depths); i++ {
		if depths[i] != depths[0] {
			t.Errorf("call %d: m sees C depth %d, the main chunk %d", i, depths[i], depths[0])
		}
		if reentries[i] > reentries[0] {
			inline++
		}
	}
	if inline == 0 {
		t.Fatalf("no call to m was a compiled-frame re-entry")
	}
}
