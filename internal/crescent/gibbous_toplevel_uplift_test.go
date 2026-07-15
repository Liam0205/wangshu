//go:build wangshu_p3 && wangshu_profile

// PW10 zero-cross top-level uplift end-to-end acceptance: when callOnStack's
// top-level cl has already been uplifted to gibbous-with-slot, it goes straight
// through enterGibbous (avoiding the top-level enterLuaFrame+execute interpreter
// main loop plus the per-CALL cross-boundary tax on each inner CALL). Following
// the R3 retrospective prove-the-path discipline —— byte-equal is not enough, the
// path being triggered must be demonstrated.
//
// Probe = st.doReturnHits + indirectCalls:
//   - path triggered: the top-level cl runs to completion via enterGibbous +
//     code.Run, and the DoReturn call count is ≥1 (the top-level frame's final
//     RETURN must go through DoReturn, because the caller is the host trampoline
//     rather than gibbous, so the ③b fast-path G2 must miss);
//   - byte-equal: the return value matches the interpreter path.
package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestPW10ZeroCross_TopLevelUplift verifies that the top-level uplift path is
// actually triggered (under the current force-all rule a body containing an
// UnknownCall is not uplifted, so we construct a leaf function cl, uplift it
// separately, and Call it directly).
func TestPW10ZeroCross_TopLevelUplift(t *testing.T) {
	src := `
local function leaf(x) return x * 2 + 3 end
return leaf`
	st, mainCl := loadFn(t, src)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run main: %v", err)
	}
	leafVal := rets[0]
	args := []value.Value{value.NumberValue(7)}

	// Interpreter baseline: leaf(7) = 7*2+3 = 17.
	base, e := st.Call(value.GCRefOf(leafVal), args, 1)
	if e != nil {
		t.Fatalf("interp leaf(7): %v", e)
	}
	if value.AsNumber(base[0]) != 17 {
		t.Fatalf("interp leaf(7) = %v, want 17", base[0])
	}

	// Manually uplift leaf (the force-all auto path also runs OnEnter on leaf
	// functions; here we explicitly ensure the uplift happens).
	if !promoteProto(st, leafVal2pid(st, leafVal)) {
		t.Skip("leaf 升层不被支持(F1-F7 排除)")
	}

	// Post-uplift call: takes the top-level uplift branch → enterGibbous →
	// code.Run → DoReturn pops this frame (the top-level caller is not gibbous,
	// so ③b G2 misses and must go through helperReturn, incrementing the DoReturn
	// count by at least 1).
	beforeHits := st.doReturnHits
	got, e2 := st.Call(value.GCRefOf(leafVal), args, 1)
	if e2 != nil {
		t.Fatalf("gibbous leaf(7): %v", e2)
	}
	if value.AsNumber(got[0]) != 17 {
		t.Errorf("gibbous leaf(7) = %v, want 17 (byte-equal)", got[0])
	}
	if st.doReturnHits <= beforeHits {
		t.Fatalf("顶层升层路径未触发:DoReturn 增 %d == 0(顶层走解释器 = 顶层升层分支"+
			"未命中 = 优化未生效)", st.doReturnHits-beforeHits)
	}
	t.Logf("顶层升层命中:DoReturn 增 %d(顶层 RETURN 经 helperReturn,证 leaf 走 wasm 入口)",
		st.doReturnHits-beforeHits)
}

func leafVal2pid(st *State, leafVal value.Value) uint32 {
	cl := value.GCRefOf(leafVal)
	return object.ClosureProtoID(st.arena, cl)
}
