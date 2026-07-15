//go:build wangshu_p3 && wangshu_profile

// PW10 zero-cross ③b end-to-end acceptance: gibbous→gibbous fixed-count RETURN
// unwinds the frame via the Wasm in-guard fast path (no h_return cross-boundary),
// byte-identical to the interpreter + the fast path is genuinely hit (not a false
// green from falling back to helperReturn throughout).
//
// Probe = st.doReturnHits (DoReturn entry ++). When the fast path hits it does **not**
// go through DoReturn, so for gibbous→gibbous fixed-count returns this counter stalls;
// asserting its increment < total gibbous returns ⟹ the fast path is in effect.
package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestPW10ZeroCross_ReturnFastHit: g→f→helper, all three promoted. f→helper is a true
// gibbous→gibbous (f runs gibbous, caller=f is gibbous, fixed-count C=2, no open
// upvalue) ⟹ helper's RETURN takes the ③b fast path, not going through DoReturn.
// Checks: ① byte-equal (141) ② fast-path hit (helper's return does not bump
// doReturnHits).
func TestPW10ZeroCross_ReturnFastHit(t *testing.T) {
	src := `
local function helper(x) return x * 2 end
local function f(a) return helper(a) + 1 end
local function g(n) return f(n) + 100 end
return g, f, helper`
	st, mainCl := loadFn(t, src)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 3)
	if err != nil {
		t.Fatalf("run main: %v", err)
	}
	gVal, fVal, hVal := rets[0], rets[1], rets[2]
	gPid := object.ClosureProtoID(st.arena, value.GCRefOf(gVal))
	fPid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
	hPid := object.ClosureProtoID(st.arena, value.GCRefOf(hVal))
	if !promoteProto(st, hPid) || !promoteProto(st, fPid) || !promoteProto(st, gPid) {
		t.Skip("g/f/helper 升层不被支持")
	}
	args := []value.Value{value.NumberValue(20)}

	// g(20)=f(20)+100=(helper(20)+1)+100=41+100=141.
	beforeHits := st.doReturnHits
	beforeInd := st.indirectCalls
	got, e := st.Call(value.GCRefOf(gVal), args, 1)
	if e != nil {
		t.Fatalf("gibbous g(20): %v", e)
	}
	if value.AsNumber(got[0]) != 141 {
		t.Errorf("g(20) = %v, want 141 (byte-equal)", got[0])
	}
	indHits := st.indirectCalls - beforeInd
	drHits := st.doReturnHits - beforeHits

	// f→helper goes through call_indirect (≥1 direct call).
	if indHits < 1 {
		t.Fatalf("call_indirect 未命中(indirectCalls 增 %d),③b 前提不成立", indHits)
	}
	// Key assertion: helper's RETURN (caller=f is gibbous, fixed-count nresults=nret=1,
	// no open upvalue) takes the ③b fast path **without going through DoReturn**. This
	// chain has 3 RETURNs total that must "unwind the frame to the caller":
	//   - helper→f: caller f is gibbous ⟹ ③b fast path (not counted in DoReturn)
	//   - f→g: caller g runs the interpreter (top-level Call entry) ⟹ G2 miss goes through DoReturn (counts 1)
	//   - g→trampoline: top-level ⟹ interpreter doReturn (not host DoReturn, not counted)
	// So ③b in effect ⟹ drHits==1 (only f→g); if ③b fails (helper also goes through DoReturn) ⟹ drHits==2.
	if drHits != 1 {
		t.Fatalf("③b 快路径命中数异常:DoReturn 增 %d,期望 1(helper→f 走快路径、仅 f→g 经 "+
			"DoReturn)。增 2 = helper 也回退 DoReturn(快路径未命中);增 0 = f→g 误入快路径", drHits)
	}
	t.Logf("③b 命中:call_indirect 增 %d,DoReturn 增 %d(helper→f 快路径拆帧,f→g 经 DoReturn)", indHits, drHits)
}
