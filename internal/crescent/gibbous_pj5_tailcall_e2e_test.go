//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"testing"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_pj5_tailcall_e2e_test.go —— PJ5 TAILCALL real promotion e2e:
// `local function f()...end; local function bounce() return f() end` (form
// TB0, GETUPVAL+TAILCALL+RETURN B=0+RETURN B=1) after P4 promotion drives the
// Run prelude path to call host.GetUpval + SetReg + host.TailCall (three-state
// branch) to complete a baseline tail call (byte-equal with the P1 doTailCall
// dispatch).
//
// Physical evidence of **PJ5 TAILCALL wired into the main path** (extended from
// CALL void to the tail-call form): luac stmtReturn translates a single CallExpr
// as the sole return expression into TAILCALL plus a trailing dead RETURN B=0
// plus an implicit RETURN B=1. On the Run side, host.TailCall has a three-state
// branch:
//   - 0 = Lua tail completed → skip DoReturn (this frame already popped)
//   - 1 = ERR
//   - 2 = host tail completed → fall through the dead RETURN to-top into DoReturn
//
// **Related to the P2 analyzer scope-aware extension**: shares the mechanism with
// the CALL void e2e — an invoker calling an outer known local fn needs P2 to pass
// the outer localFnAsts across Protos (see
// internal/bridge/analyzer.go::AnalyzeProtoWithOuter).

// TestPJ5_TailCall_E2E_FormTB0_Upval: form TB0 (GETUPVAL+TAILCALL+RETURN
// B=0+RETURN B=1) real promotion — `local function f() return 42 end;
// local function bounce() return f() end`, 0 args 1 return (tail call passes
// the callee return value through). Repeatedly calling bounce makes it truly
// take the PJ5 TAILCALL template after P4 promotion.
//
// Key probe: **SpecTailCallHits** —— only increments when Compile hits
// isTailCall=true. If P4 promotion is never reached or form recognition fails,
// SpecTailCallHits stays 0 (test fails).
func TestPJ5_TailCall_E2E_FormTB0_Upval(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f() return 42 end
local function bounce() return f() end
local s = 0
for i = 1, 50 do s = s + bounce() end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 50*42 {
		t.Errorf("rets = %v, want %d (bounce() × 50 each tail-calls f() → 42)", got, 50*42)
	}
	if jit.SpecTailCallHits() == 0 {
		t.Errorf("SpecTailCallHits = 0,PJ5 TAILCALL 模板未真编译——降级 host 或 P4 路径未触达(prove-the-path 失败)")
	}
	t.Logf("SpecTailCallHits=%d", jit.SpecTailCallHits())
}

// TestPJ5_TailCall_E2E_FormTB1K_UpvalArg: form TB1K (GETUPVAL+LOADK+TAILCALL+
// RETURN B=0+RETURN B=1) real promotion — `local function take(x) return x*2 end;
// local function bounce() return take(7) end`, 1 K constant arg 1 return. LOADK
// in the mmap segment is a dummy; on the Run side host.SetReg(callA+1, K) loads
// it into the argument slot.
func TestPJ5_TailCall_E2E_FormTB1K_UpvalArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take(x) return x * 2 end
local function bounce() return take(7) end
local s = 0
for i = 1, 50 do s = s + bounce() end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// bounce() 50 times, each take(7) → 14 → s += 14 → 50*14 = 700
	if got := value.AsNumber(value.Value(rets[0])); got != 700 {
		t.Errorf("rets = %v, want 700 (bounce() × 50 each take(7)→14)", got)
	}
	if jit.SpecTailCallHits() == 0 {
		t.Errorf("SpecTailCallHits = 0,PJ5 TAILCALL 1 K 参模板未真编译")
	}
	t.Logf("SpecTailCallHits=%d", jit.SpecTailCallHits())
}

// TestPJ5_TailCall_E2E_FormTB1R_UpvalArg: form TB1R (GETUPVAL+MOVE+TAILCALL+
// RETURN B=0+RETURN B=1) real promotion — `local function take(x) return x+1 end;
// local function bounce(v) return take(v) end`, 1 reg arg 1 return. MOVE in the
// mmap segment is a dummy; on the Run side host.GetReg(srcReg) + SetReg(callA+1,
// val) loads it.
func TestPJ5_TailCall_E2E_FormTB1R_UpvalArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take(x) return x + 1 end
local function bounce(v) return take(v) end
local s = 0
for i = 1, 50 do s = s + bounce(i) end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// bounce(i) 50 times, each take(i)→i+1, sum(i+1) i=1..50 = sum(2..51) = 51*52/2-1 = 1325
	const want = float64((1+50)*50/2 + 50)
	if got := value.AsNumber(value.Value(rets[0])); got != want {
		t.Errorf("rets = %v, want %v (bounce(i)→take(i)→i+1)", got, want)
	}
	if jit.SpecTailCallHits() == 0 {
		t.Errorf("SpecTailCallHits = 0,PJ5 TAILCALL 1 reg 参模板未真编译")
	}
	t.Logf("SpecTailCallHits=%d", jit.SpecTailCallHits())
}

// TestPJ5_TailCall_E2E_FormTB2K_UpvalArgs: form TB2K (GETUPVAL+LOADK+LOADK+
// TAILCALL+RETURN B=0+RETURN B=1) real promotion — `local function f(a, b) return a+b end;
// local function bounce() return f(10, 20) end`, 2 K args 1 return.
func TestPJ5_TailCall_E2E_FormTB2K_UpvalArgs(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(a, b) return a + b end
local function bounce() return f(10, 20) end
local s = 0
for i = 1, 30 do s = s + bounce() end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// bounce() 30 times, each f(10,20)→30, s = 30*30 = 900
	if got := value.AsNumber(value.Value(rets[0])); got != 900 {
		t.Errorf("rets = %v, want 900 (bounce() × 30 each f(10,20)→30)", got)
	}
	if jit.SpecTailCallHits() == 0 {
		t.Errorf("SpecTailCallHits = 0,PJ5 TAILCALL 2 K 参形态未真编译")
	}
	t.Logf("SpecTailCallHits=%d", jit.SpecTailCallHits())
}

// TestPJ5_TailCall_E2E_FormTB1K1R: form TB1K1R (K+R 2 args tail) — `bounce(v) return f(7, v)`
func TestPJ5_TailCall_E2E_FormTB1K1R(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(a, b) return a + b end
local function bounce(v) return f(7, v) end
local s = 0
for i = 1, 30 do s = s + bounce(i) end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// s = sum(7+i) i=1..30 = 30*7 + sum(1..30) = 210 + 465 = 675
	if got := value.AsNumber(value.Value(rets[0])); got != 675 {
		t.Errorf("rets = %v, want 675 (bounce(i)×30 each f(7,i)→7+i)", got)
	}
	if jit.SpecTailCallHits() == 0 {
		t.Errorf("SpecTailCallHits = 0,PJ5 TAILCALL 1 K + 1 reg 参形态 TB1K1R 未真编译")
	}
}

// TestPJ5_TailCall_E2E_FormTB1R1K: form TB1R1K (R+K 2 args tail) — `bounce(v) return f(v, 7)`
func TestPJ5_TailCall_E2E_FormTB1R1K(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(a, b) return a + b end
local function bounce(v) return f(v, 7) end
local s = 0
for i = 1, 30 do s = s + bounce(i) end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// s = sum(i+7) = 675 (same as TB1K1R)
	if got := value.AsNumber(value.Value(rets[0])); got != 675 {
		t.Errorf("rets = %v, want 675 (bounce(i)×30 each f(i,7)→i+7)", got)
	}
	if jit.SpecTailCallHits() == 0 {
		t.Errorf("SpecTailCallHits = 0,PJ5 TAILCALL 1 reg + 1 K 参形态 TB1R1K 未真编译")
	}
}

// TestPJ5_TailCall_E2E_FormTB2R: form TB2R (R+R 2 args tail) — `bounce(u, v) return f(u, v)`
func TestPJ5_TailCall_E2E_FormTB2R(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(a, b) return a + b end
local function bounce(u, v) return f(u, v) end
local s = 0
for i = 1, 10 do s = s + bounce(i, i+1) end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// s = sum(i+(i+1)) i=1..10 = sum(2i+1) = 2*55 + 10 = 120
	if got := value.AsNumber(value.Value(rets[0])); got != 120 {
		t.Errorf("rets = %v, want 120 (bounce(i,i+1)×10 each f(i,i+1)→2i+1)", got)
	}
	if jit.SpecTailCallHits() == 0 {
		t.Errorf("SpecTailCallHits = 0,PJ5 TAILCALL 2 reg 参形态 TB2R 未真编译")
	}
}

// TestPJ5_TailCall_E2E_FormTB3K: form TB3K (3 K args tail, length 7)
func TestPJ5_TailCall_E2E_FormTB3K(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(a, b, c) return a + b + c end
local function bounce() return f(1, 2, 3) end
local s = 0
for i = 1, 30 do s = s + bounce() end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// s = 30 * 6 = 180
	if got := value.AsNumber(value.Value(rets[0])); got != 180 {
		t.Errorf("rets = %v, want 180 (bounce()×30 each f(1,2,3)→6)", got)
	}
	if jit.SpecTailCallHits() == 0 {
		t.Errorf("SpecTailCallHits = 0,PJ5 TAILCALL 3 K 参形态 TB3K 未真编译")
	}
}

// TestPJ5_TailCall_E2E_FormTB3R: form TB3R (3 reg args tail, length 7)
func TestPJ5_TailCall_E2E_FormTB3R(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(a, b, c) return a + b + c end
local function bounce(u, v, w) return f(u, v, w) end
local s = 0
for i = 1, 10 do s = s + bounce(i, i+1, i+2) end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(3i+3) i=1..10 = 195
	if got := value.AsNumber(value.Value(rets[0])); got != 195 {
		t.Errorf("rets = %v, want 195 (bounce(i,i+1,i+2)×10 each f→3i+3)", got)
	}
	if jit.SpecTailCallHits() == 0 {
		t.Errorf("SpecTailCallHits = 0,PJ5 TAILCALL 3 reg 参形态 TB3R 未真编译")
	}
}

// TestPJ5_TailCall_E2E_FormTB4R: form TB4R (4 reg args tail, length 8)
func TestPJ5_TailCall_E2E_FormTB4R(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(a, b, c, d) return a + b + c + d end
local function bounce(u, v, w, x) return f(u, v, w, x) end
local s = 0
for i = 1, 10 do s = s + bounce(i, i+1, i+2, i+3) end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(4i+6) i=1..10 = 280
	if got := value.AsNumber(value.Value(rets[0])); got != 280 {
		t.Errorf("rets = %v, want 280 (bounce(i,i+1,i+2,i+3)×10 each f→4i+6)", got)
	}
	if jit.SpecTailCallHits() == 0 {
		t.Errorf("SpecTailCallHits = 0,PJ5 TAILCALL 4 reg 参形态 TB4R 未真编译")
	}
}

// TestPJ5_TailCall_E2E_FormTB5R: form TB5R (5 reg args tail, length 9)
func TestPJ5_TailCall_E2E_FormTB5R(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(a, b, c, d, e) return a + b + c + d + e end
local function bounce(u, v, w, x, y) return f(u, v, w, x, y) end
local s = 0
for i = 1, 10 do s = s + bounce(i, i+1, i+2, i+3, i+4) end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(5i+10) i=1..10 = 375
	if got := value.AsNumber(value.Value(rets[0])); got != 375 {
		t.Errorf("rets = %v, want 375 (bounce(...)×10 each f→5i+10)", got)
	}
	if jit.SpecTailCallHits() == 0 {
		t.Errorf("SpecTailCallHits = 0,PJ5 TAILCALL 5 reg 参形态 TB5R 未真编译")
	}
}

// TestPJ5_TailCall_E2E_FormTB6R: 6 reg args tail, length 10
func TestPJ5_TailCall_E2E_FormTB6R(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(a, b, c, d, e, g) return a + b + c + d + e + g end
local function bounce(u, v, w, x, y, z) return f(u, v, w, x, y, z) end
local s = 0
for i = 1, 10 do s = s + bounce(i, i+1, i+2, i+3, i+4, i+5) end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(6i+15) i=1..10 = 480
	if got := value.AsNumber(value.Value(rets[0])); got != 480 {
		t.Errorf("rets = %v, want 480 (bounce(...)×10 each f→6i+15)", got)
	}
	if jit.SpecTailCallHits() == 0 {
		t.Errorf("SpecTailCallHits = 0,PJ5 TAILCALL 6 reg 参形态 TB6R 未真编译")
	}
}

// TestPJ5_TailCall_E2E_FormTB7R: 7 reg args tail, length 11
func TestPJ5_TailCall_E2E_FormTB7R(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(a, b, c, d, e, g, h) return a + b + c + d + e + g + h end
local function bounce(u, v, w, x, y, z, q) return f(u, v, w, x, y, z, q) end
local s = 0
for i = 1, 10 do s = s + bounce(i, i+1, i+2, i+3, i+4, i+5, i+6) end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(7i+21) i=1..10 = 595
	if got := value.AsNumber(value.Value(rets[0])); got != 595 {
		t.Errorf("rets = %v, want 595 (bounce(...)×10 each f→7i+21)", got)
	}
	if jit.SpecTailCallHits() == 0 {
		t.Errorf("SpecTailCallHits = 0,PJ5 TAILCALL 7 reg 参形态 TB7R 未真编译")
	}
}

// **Note**: the TA* parameter-callee forms (e.g. `function(g) return g() end`) are
// unreachable via real promotion — the P2 analyzer marks a parameter call as
// ReasonUnknownCall (the parameter might be coroutine.yield), and the visitor
// design conservatively rejects it. The TA* forms are covered by unit tests
// inside the jit package that directly exercise the Compile + Run path via a mock
// host (`compiler_pj5_tailcall_test.go::TestPJ5_RunTailCallPath` etc.); the
// crescent e2e path is unreachable. The high-frequency real-world business form
// is a closure calling an outer known fn (form TB*), and that path already works.
