package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestCallOnStackErrorClosesUpvalues pins #292's fix itself: a top-level call that fails returns with
// every upvalue of the frames it unwound closed (lua_pcall's luaF_close), not left open until the next
// call. test/regression's issue292 test checks what a host Collect() then does, but an escaped closure
// survives there also through the collector marking open upvalues' values (#291), so only this test
// fails when the close itself goes missing. CallOnStack (CallInto) and Call (Run, State.Call) both go
// through callOnStack.
func TestCallOnStackErrorClosesUpvalues(t *testing.T) {
	for _, entry := range []string{"CallOnStack", "Call"} {
		st := New()
		prog := mustCompile(t, []byte(`local x = {} local s = "str" function g() return x, s end error("boom")`))
		cl := st.LoadProgram(prog.mainID, prog.protos)
		var err error
		if entry == "Call" {
			_, err = st.Call(cl, nil, 0)
		} else {
			_, err = st.CallOnStack(cl, nil, 0)
		}
		if err == nil {
			t.Fatalf("%s: the chunk did not fail", entry)
		}
		if n := len(st.mainTh.openUvs); n != 0 {
			t.Errorf("%s: %d upvalues of the unwound frames left open", entry, n)
		}
		g := st.GetGlobal("g")
		if value.Tag(g) != value.TagFunction {
			t.Fatalf("%s: g = %v", entry, g)
		}
		ref := value.GCRefOf(g)
		for i := uint16(0); i < object.ClosureNUpvals(st.arena, ref); i++ {
			if uv := object.ClosureUpvalRef(st.arena, ref, i); !object.UpvalIsClosed(st.arena, uv) {
				t.Errorf("%s: g's upvalue %d is still open", entry, i)
			}
		}
	}
}
