//go:build wangshu_p3 && wangshu_profile

// PW10 R3 end-to-end acceptance: a gibbous→gibbous CALL calls directly across
// modules via call_indirect (avoiding the double cross-layer code.Run reentry
// of h_call), while the three-way fallback is preserved and errors pop a frame
// at each layer.
//
// Verifies three things (matching the R3 definition in spike DECISION.md §3):
//   - direct call actually taken: after promoteProto double-promotes f+helper,
//     the indirectCalls counter > 0 (not a silent fallback to code.Run).
//   - byte-equal: the gibbous→gibbous result + error message is byte-for-byte
//     identical to the interpreter (inter-layer differential).
//   - base refresh: after the callee's deep recursion triggers growStack
//     segment relocation, the caller continues computing with the refreshed
//     base read via the transfer word.
package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestPW10R3_IndirectHappyPath: g→f→helper three levels, all promoted to
// gibbous. The top-level g runs from the interpreter entry via st.Call
// (crescent→gibbous into g); g calling f and f calling helper are both
// gibbous→gibbous reaching directly via call_indirect. Result byte-equal +
// direct-call hit count > 0.
//
// **Why three levels**: st.Call's top-level closure goes through the
// interpreter enterLuaFrame (not enterGibbous), so a directly-Called function
// runs crescent; to get gibbous→gibbous a CALL must be issued inside a gibbous
// frame — i.e. after g is promoted to gibbous (via the crescent→gibbous entry),
// it calls the equally-promoted f inside its body.
func TestPW10R3_IndirectHappyPath(t *testing.T) {
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
	args := []value.Value{value.NumberValue(20)}

	// Interpreter baseline (before promotion). g(20) = f(20)+100 = (helper(20)+1)+100 = 41+100 = 141.
	base, e := st.Call(value.GCRefOf(gVal), args, 1)
	if e != nil {
		t.Fatalf("interp g(20): %v", e)
	}
	if value.AsNumber(base[0]) != 141 {
		t.Fatalf("interp g(20) = %v, want 141", base[0])
	}

	// Three promotions → both g→f and f→helper go through call_indirect.
	if !promoteProto(st, hPid) || !promoteProto(st, fPid) || !promoteProto(st, gPid) {
		t.Skip("g/f/helper 升层不被支持")
	}
	before := st.indirectCalls
	got, e2 := st.Call(value.GCRefOf(gVal), args, 1)
	if e2 != nil {
		t.Fatalf("gibbous→gibbous g(20): %v", e2)
	}
	if value.AsNumber(got[0]) != 141 {
		t.Errorf("gibbous→gibbous g(20) = %v, want 141 (byte-equal)", got[0])
	}
	// g→f→helper: the top-level g runs from the interpreter entry
	// (crescent→gibbous into g's frame), and g calling f inside its body is the
	// first gibbous→gibbous (but g runs the interpreter, so g→f is still
	// crescent→gibbous and not counted); f runs gibbous, and f calling helper
	// inside its body is a true gibbous→gibbous via call_indirect (counted
	// once). That is, an N-level chain running from the top-level Call entry has
	// only its innermost N-2 edges as gibbous→gibbous (the top crescent→gibbous
	// enters the second-innermost layer; only from the second-innermost layer
	// onward is a CALL issued inside a gibbous frame). g→f→helper ⟹ 1 hit.
	if st.indirectCalls < before+1 {
		t.Fatalf("call_indirect 直调未命中(indirectCalls %d→%d,期望 ≥+1):疑似静默回退 code.Run，"+
			"R3 收益未兑现", before, st.indirectCalls)
	}
}

// TestPW10R3_IndirectErrorByteEqual: an error crossing gibbous→gibbous (helper
// does arithmetic on nil, raising an error) bubbles up, with message +
// traceback byte-equal to the **pure interpreter** (R3c-fix anchors the line
// number at the error point + materializes the traceback, so subsequent frame
// popping does not affect the error location → gibbous catches up with the
// interpreter).
//
// Before R3c-fix this test had line-number drift because currentCI was offset
// after frame popping (a known regression); after the fix the gibbous error
// location/traceback is byte-for-byte identical to the interpreter (better than
// PW6c's existing crescent→gibbous baseline with its truncated traceback).
// oracle = pure interpreter (same entry g(nil), nothing promoted).
func TestPW10R3_IndirectErrorByteEqual(t *testing.T) {
	src := `
local function helper(x) return x + 1 end   -- helper(nil) → 对 nil 算术报错
local function f(a) local r = helper(a); return r end       -- 非尾 CALL(经 h_call/DoCall)
local function g(n) local r = f(n); return r + 0 end         -- 非尾 CALL
return g, f, helper`
	loadF := func() (*State, value.Value, value.Value, value.Value) {
		st, mainCl := loadFn(t, src)
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 3)
		if err != nil {
			t.Fatalf("run main: %v", err)
		}
		return st, rets[0], rets[1], rets[2]
	}
	badArg := []value.Value{value.Nil}

	// oracle: pure interpreter g(nil) (nothing promoted); message + traceback is the byte-equal baseline.
	stO, gO, _, _ := loadF()
	_, eO := stO.Call(value.GCRefOf(gO), badArg, 1)
	if eO == nil {
		t.Fatal("interp g(nil) 应报错(对 nil 算术)")
	}
	wantMsg := eO.Error()

	// R3: three promotions g+f+helper, f→helper bubbles via the call_indirect status chain + PopErrFrame;
	// the error anchors its line number in the erroring frame via raiseGibbous → byte-equal with the interpreter.
	st, gVal, fVal, hVal := loadF()
	gPid := object.ClosureProtoID(st.arena, value.GCRefOf(gVal))
	fPid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
	hPid := object.ClosureProtoID(st.arena, value.GCRefOf(hVal))
	if !promoteProto(st, hPid) || !promoteProto(st, fPid) || !promoteProto(st, gPid) {
		t.Skip("g/f/helper 升层不被支持")
	}
	before := st.indirectCalls
	_, eG := st.Call(value.GCRefOf(gVal), badArg, 1)
	if eG == nil {
		t.Fatal("gibbous→gibbous g(nil) 应报错(错误经 call_indirect status 链冒泡)")
	}
	if st.indirectCalls <= before {
		t.Fatalf("错误路径 call_indirect 未命中(疑似回退)")
	}
	if eG.Error() != wantMsg {
		t.Errorf("gibbous→gibbous 错误消息 = %q, want %q (与纯解释器 byte-equal)", eG.Error(), wantMsg)
	}
}

// TestPW10R3_IndirectBaseRefresh: the callee helper's deep recursion overflows
// the initial stack, triggering growStack segment relocation; the caller f
// continues addressing with the refreshed base read via the transfer word
// (avoiding a stale-base UAF). The g→f→helper three levels guarantee f runs
// gibbous (f→helper is gibbous→gibbous, and after returning f must refresh
// base).
func TestPW10R3_IndirectBaseRefresh(t *testing.T) {
	src := `
local function helper(n)
  if n <= 0 then return 0 end
  local sub = helper(n - 1)     -- 非尾 CALL:每层留活帧 → 深栈撑爆触发 growStack
  return sub + n
end
local function f(n)
  local marker = 777            -- 占本帧寄存器,helper 返回后须能读对(验 base 刷新)
  local s = helper(n)
  return s + marker
end
local function g(n) local r = f(n); return r end   -- 非尾 CALL 进 f
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

	// First use a shallow depth (n=5) to take the interpreter baseline + promote
	// (avoiding a deep run that triggers helper's automatic-promotion attempt,
	// whose failure would falsely mark TierStuck — promotion of the
	// deep-recursion self-CALL shape occasionally fails this cycle, unrelated to
	// R3).
	shallow := []value.Value{value.NumberValue(5)}
	base, e := st.Call(value.GCRefOf(gVal), shallow, 1)
	if e != nil {
		t.Fatalf("interp g(5): %v", e)
	}
	wantShallow := value.AsNumber(base[0]) // = sum(1..5)+777 = 792

	if !promoteProto(st, hPid) || !promoteProto(st, fPid) || !promoteProto(st, gPid) {
		t.Skip("g/f/helper 升层不被支持")
	}
	// After promotion, first verify the direct-call hit + byte-equal at shallow depth.
	before := st.indirectCalls
	gotShallow, e1 := st.Call(value.GCRefOf(gVal), shallow, 1)
	if e1 != nil {
		t.Fatalf("gibbous→gibbous g(5): %v", e1)
	}
	if st.indirectCalls <= before {
		t.Fatalf("call_indirect 未命中(疑似回退)")
	}
	if value.AsNumber(gotShallow[0]) != wantShallow {
		t.Fatalf("gibbous→gibbous g(5) = %v, want %v", gotShallow[0], wantShallow)
	}
	// The deep depth (n=2000) drives helper's deep recursion to overflow the
	// initial stack → growStack segment relocation; f reads the refreshed base
	// via the transfer word and continues computing, reading marker (777). The
	// gibbous result must match the interpreter's deep baseline.
	deep := []value.Value{value.NumberValue(2000)}
	gotDeep, e2 := st.Call(value.GCRefOf(gVal), deep, 1)
	if e2 != nil {
		t.Fatalf("gibbous→gibbous g(2000) 深递归: %v(base 刷新失败?)", e2)
	}
	// Interpreter deep baseline: recompute with a new State (this State is fully promoted, can no longer take an interpreter result).
	wantDeep := 2000.0*2001.0/2.0 + 777.0 // sum(1..2000)+marker
	if value.AsNumber(gotDeep[0]) != wantDeep {
		t.Errorf("gibbous→gibbous g(2000) = %v, want %v(base 刷新后读 marker 错 = UAF)",
			gotDeep[0], wantDeep)
	}
}
