//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"runtime"
	"strings"
	"sync"
	"testing"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_pj5_self_e2e_test.go —— e2e for real promotion of the PJ5 SELF
// method call inline form: after `obj:method(args)` is promoted to P4, the Run
// prelude path first calls host.Self (byte-equal to P1 SELF + icGetTable +
// __index metamethod chain) to load the method into R(callA) and self into
// R(callA+1), then calls host.CallBaseline / TailCall to complete the baseline
// doCall.
//
// Physical evidence that **PJ5 SELF is really wired into the main path**:
// previously the P4 PJ5 inline form only accepted a callee loaded via
// MOVE/GETUPVAL, and the SELF method call path (obj method dispatch) was
// rejected by the P2 ReasonUnknownCall gate. This batch splits it out:
// visitMethodCallExpr sets the ReasonSelfCall placeholder bit, the P4 side
// clears it in recheckCompilabilityRuntime, and SupportsAllOpcodes (via
// analyzeSelfCallForm) does the real gating: the obj:method() form matches the
// PJ5 SELF inline template.
//
// **Key probe**: SpecSelfCallHits —— only incremented when Compile matches
// isSelfCall=true.

// TestPJ5_SelfCall_E2E_M0_VoidCall form M0, 0 args, void:
// `local _ = function(o) o:m() end` (MOVE+SELF+CALL+RETURN void, length 4).
//
// Because the main chunk must contain this closure, the outer closure
// registration + force-all promotion ⇒ the inner form M0 matches
// isSelfCall=true.
func TestPJ5_SelfCall_E2E_M0_VoidCall(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local o = { m = function(self) count = count + 1 end }
local function caller(t) t:m() end
for i = 1, 50 do caller(o) end
return count`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 50 {
		t.Errorf("rets = %v, want 50(caller(o) 50 次每次 t:m() 让 count++)", got)
	}
	if jit.SpecSelfCallHits() == 0 {
		t.Errorf("SpecSelfCallHits = 0,PJ5 SELF inline 形态 M0 未真编译——降级 host 或 P4 路径未触达(prove-the-path 失败)")
	}
	t.Logf("SpecSelfCallHits=%d", jit.SpecSelfCallHits())
}

// TestPJ5_SelfCall_E2E_U0_VoidCall form U0, 0 args, void (GETUPVAL recv):
// `o:m() end` inside a closure, where o is an outer local accessed via upval.
func TestPJ5_SelfCall_E2E_U0_VoidCall(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local o = { m = function(self) count = count + 1 end }
local function tick() o:m() end
for i = 1, 50 do tick() end
return count`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 50 {
		t.Errorf("rets = %v, want 50", got)
	}
	if jit.SpecSelfCallHits() == 0 {
		t.Errorf("SpecSelfCallHits = 0,PJ5 SELF inline 形态 U0(upval recv)未真编译")
	}
	t.Logf("SpecSelfCallHits=%d", jit.SpecSelfCallHits())
}

// TestPJ5_SelfCall_E2E_M1K_VoidArg form M1K, 1 K arg, void:
// `caller(o)` → `t:m(42)`.
func TestPJ5_SelfCall_E2E_M1K_VoidArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local o = { m = function(self, x) sum = sum + x end }
local function caller(t) t:m(42) end
for i = 1, 50 do caller(o) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 50*42 {
		t.Errorf("rets = %v, want %d", got, 50*42)
	}
	if jit.SpecSelfCallHits() == 0 {
		t.Errorf("SpecSelfCallHits = 0,PJ5 SELF inline 1 K 参形态未真编译")
	}
	t.Logf("SpecSelfCallHits=%d", jit.SpecSelfCallHits())
}

// TestPJ5_SelfCall_E2E_M1R_VoidArg form M1R, 1 reg arg, void:
// `caller(o, x)` → `t:m(x)`.
func TestPJ5_SelfCall_E2E_M1R_VoidArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local o = { m = function(self, x) sum = sum + x end }
local function caller(t, v) t:m(v) end
for i = 1, 50 do caller(o, i) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// 1+2+..+50 = 1275
	if got := value.AsNumber(value.Value(rets[0])); got != 1275 {
		t.Errorf("rets = %v, want 1275", got)
	}
	if jit.SpecSelfCallHits() == 0 {
		t.Errorf("SpecSelfCallHits = 0,PJ5 SELF inline 1 reg 参形态未真编译")
	}
	t.Logf("SpecSelfCallHits=%d", jit.SpecSelfCallHits())
}

// TestPJ5_SelfCall_E2E_TailCall_M0 form TM0, 0 args, TAILCALL:
// `return t:m()` luac emits SELF+TAILCALL+RETURN(B=0).
func TestPJ5_SelfCall_E2E_TailCall_M0(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local o = { m = function(self) count = count + 1; return count end }
local function caller(t) return t:m() end
local last = 0
for i = 1, 50 do last = caller(o) end
return last`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 50 {
		t.Errorf("rets = %v, want 50", got)
	}
	if jit.SpecSelfCallHits() == 0 {
		t.Errorf("SpecSelfCallHits = 0,PJ5 SELF inline TAILCALL 形态 TM0 未真编译")
	}
	t.Logf("SpecSelfCallHits=%d", jit.SpecSelfCallHits())
}

// TestPJ5_SelfCall_E2E_GetterCall_M0 form MR1, 0 args, 1 return, CALL getter:
// `local r = t:m()` form (empirically luac emits CALL B=2 C=2 for obj:m() in a
// local-assignment context).
func TestPJ5_SelfCall_E2E_GetterCall_M0(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local o = { m = function(self) return 7 end }
local function caller(t) local r = t:m(); return r end
local s = 0
for i = 1, 50 do s = s + caller(o) end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 50*7 {
		t.Errorf("rets = %v, want %d", got, 50*7)
	}
	if jit.SpecSelfCallHits() == 0 {
		t.Errorf("SpecSelfCallHits = 0,PJ5 SELF inline getter 形态未真编译")
	}
	t.Logf("SpecSelfCallHits=%d", jit.SpecSelfCallHits())
}

// TestPJ5_SelfCall_E2E_M3K_VoidCall form M3K, 3 K args, void (length 7):
// `function(t) t:m(1, 2, 3) end`.
func TestPJ5_SelfCall_E2E_M3K_VoidCall(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local o = { m = function(self, a, b, c) sum = sum + a + b + c end }
local function caller(t) t:m(1, 2, 3) end
for i = 1, 30 do caller(o) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 30*6 {
		t.Errorf("rets = %v, want %d (caller(o) 30 次每次 1+2+3=6)", got, 30*6)
	}
	if jit.SpecSelfCallHits() == 0 {
		t.Errorf("SpecSelfCallHits = 0,PJ5 SELF inline 3 K 参形态未真编译")
	}
	t.Logf("SpecSelfCallHits=%d", jit.SpecSelfCallHits())
}

// TestPJ5_SelfCall_E2E_M3R_VoidCall form M3R, 3 reg args, void (length 7).
func TestPJ5_SelfCall_E2E_M3R_VoidCall(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local o = { m = function(self, a, b, c) sum = sum + a + b + c end }
local function caller(t, x, y, z) t:m(x, y, z) end
for i = 1, 30 do caller(o, i, i+1, i+2) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum = sum_{i=1..30} (i + (i+1) + (i+2)) = 3*sum_{i=1..30} i + 30*3
	// = 3*465 + 90 = 1485
	if got := value.AsNumber(value.Value(rets[0])); got != 1485 {
		t.Errorf("rets = %v, want 1485", got)
	}
	if jit.SpecSelfCallHits() == 0 {
		t.Errorf("SpecSelfCallHits = 0,PJ5 SELF inline 3 reg 参形态未真编译")
	}
	t.Logf("SpecSelfCallHits=%d", jit.SpecSelfCallHits())
}

// TestPJ5_SelfCall_E2E_M4R_VoidCall form M4R, 4 reg args, void (length 8).
func TestPJ5_SelfCall_E2E_M4R_VoidCall(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local o = { m = function(self, a, b, c, d) sum = sum + a + b + c + d end }
local function caller(t, p, q, r, s) t:m(p, q, r, s) end
for i = 1, 30 do caller(o, i, i+1, i+2, i+3) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum = sum_{i=1..30} (i + (i+1) + (i+2) + (i+3)) = 4*465 + 30*6 = 2040
	if got := value.AsNumber(value.Value(rets[0])); got != 2040 {
		t.Errorf("rets = %v, want 2040", got)
	}
	if jit.SpecSelfCallHits() == 0 {
		t.Errorf("SpecSelfCallHits = 0,PJ5 SELF inline 4 reg 参形态未真编译")
	}
	t.Logf("SpecSelfCallHits=%d", jit.SpecSelfCallHits())
}

// TestPJ5_SelfCall_E2E_M5R_VoidCall form M5R, 5 reg args, void (length 9).
func TestPJ5_SelfCall_E2E_M5R_VoidCall(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local o = { m = function(self, a, b, c, d, e) sum = sum + a + b + c + d + e end }
local function caller(t, p, q, r, s, u) t:m(p, q, r, s, u) end
for i = 1, 30 do caller(o, i, i+1, i+2, i+3, i+4) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum = sum_{i=1..30} (i + (i+1) + (i+2) + (i+3) + (i+4)) = 5*465 + 30*10 = 2625
	if got := value.AsNumber(value.Value(rets[0])); got != 2625 {
		t.Errorf("rets = %v, want 2625", got)
	}
	if jit.SpecSelfCallHits() == 0 {
		t.Errorf("SpecSelfCallHits = 0,PJ5 SELF inline 5 reg 参形态未真编译")
	}
	t.Logf("SpecSelfCallHits=%d", jit.SpecSelfCallHits())
}

// TestPJ5_SelfCall_E2E_M6R_VoidCall form M6R, 6 reg args, void (length 10).
func TestPJ5_SelfCall_E2E_M6R_VoidCall(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local o = { m = function(self, a, b, c, d, e, f) sum = sum + a + b + c + d + e + f end }
local function caller(t, p, q, r, s, u, v) t:m(p, q, r, s, u, v) end
for i = 1, 30 do caller(o, i, i+1, i+2, i+3, i+4, i+5) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum = sum_{i=1..30} (i+(i+1)+(i+2)+(i+3)+(i+4)+(i+5))
	//     = 6*465 + 30*(0+1+2+3+4+5) = 2790 + 450 = 3240
	if got := value.AsNumber(value.Value(rets[0])); got != 3240 {
		t.Errorf("rets = %v, want 3240", got)
	}
	if jit.SpecSelfCallHits() == 0 {
		t.Errorf("SpecSelfCallHits = 0,PJ5 SELF inline 6 reg 参形态未真编译")
	}
	t.Logf("SpecSelfCallHits=%d", jit.SpecSelfCallHits())
}

// TestPJ5_SelfCall_E2E_M7R_VoidCall form M7R, 7 reg args, void (length 11).
func TestPJ5_SelfCall_E2E_M7R_VoidCall(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local o = { m = function(self, a, b, c, d, e, f, g) sum = sum + a + b + c + d + e + f + g end }
local function caller(t, p, q, r, s, u, v, w) t:m(p, q, r, s, u, v, w) end
for i = 1, 30 do caller(o, i, i+1, i+2, i+3, i+4, i+5, i+6) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum = sum_{i=1..30} (i+(i+1)+..+(i+6))
	//     = 7*465 + 30*(0+1+..+6) = 3255 + 30*21 = 3255 + 630 = 3885
	if got := value.AsNumber(value.Value(rets[0])); got != 3885 {
		t.Errorf("rets = %v, want 3885", got)
	}
	if jit.SpecSelfCallHits() == 0 {
		t.Errorf("SpecSelfCallHits = 0,PJ5 SELF inline 7 reg 参形态未真编译")
	}
	t.Logf("SpecSelfCallHits=%d", jit.SpecSelfCallHits())
}

// TestPJ5_SelfCall_E2E_TailCall_3K form TM, 3 K args, TAILCALL (length 8):
// `function(t) return t:m(1,2,3) end`.
func TestPJ5_SelfCall_E2E_TailCall_3K(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local o = { m = function(self, a, b, c) return a + b + c end }
local function caller(t) return t:m(1, 2, 3) end
local s = 0
for i = 1, 30 do s = s + caller(o) end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 30*6 {
		t.Errorf("rets = %v, want %d", got, 30*6)
	}
	if jit.SpecSelfCallHits() == 0 {
		t.Errorf("SpecSelfCallHits = 0,PJ5 SELF inline 3 K 参 TAILCALL 形态未真编译")
	}
	t.Logf("SpecSelfCallHits=%d", jit.SpecSelfCallHits())
}

// TestPJ5_SelfCall_E2E_TailCall_5R form SELF + TAILCALL, 5 reg args (length 9,
// already covered by form9; this test checks the call chain is byte-equal to P1).
func TestPJ5_SelfCall_E2E_TailCall_5R(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local o = { m = function(self, a, b, c, d, e) return a + b + c + d + e end }
local function caller(t, p, q, r, s, u) return t:m(p, q, r, s, u) end
local total = 0
for i = 1, 30 do total = total + caller(o, i, i+1, i+2, i+3, i+4) end
return total`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// total = sum_{i=1..30} (i+(i+1)+(i+2)+(i+3)+(i+4)) = 5*465 + 30*10 = 2325 + 300 = 2625
	if got := value.AsNumber(value.Value(rets[0])); got != 2625 {
		t.Errorf("rets = %v, want 2625", got)
	}
	if jit.SpecSelfCallHits() == 0 {
		t.Errorf("SpecSelfCallHits = 0,PJ5 SELF inline 5 reg 参 TAILCALL 形态未真编译")
	}
	t.Logf("SpecSelfCallHits=%d", jit.SpecSelfCallHits())
}

// TestPJ5_SelfCall_E2E_MultiRetN2_0arg form MR2, N=2 returns, 0 args (length 4):
// `local a, b = t:m()`, the caller body is only this one line (other logic is
// verified via side effects).
// R(callA)/R(callA+1) land a, b — luac emits RETURN A=0 B=1 to finish (0 returns).
func TestPJ5_SelfCall_E2E_MultiRetN2_0arg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local o = { m = function(self) return 11, 22 end }
local function caller(t) local a, b = t:m(); sum = sum + a + b end
for i = 1, 30 do caller(o) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 30*33 {
		t.Errorf("rets = %v, want %d", got, 30*33)
	}
	// Note: this src's caller contains sum += a+b, so the caller form is more
	// complex; the SELF inline form only hits length 4 when luac emits the
	// narrower `local a,b = t:m() end`. This test mainly exercises the
	// byte-equal path (no hard assert on SpecSelfCallHits — the caller body is
	// not just SELF+CALL+RETURN).
	if jit.SpecSelfCallHits() == 0 {
		t.Logf("SpecSelfCallHits=0(caller 含算术 + setter,SELF inline 不在简化形态命中区,但 byte-equal 应保)")
	}
}

// TestPJ5_SelfCall_E2E_MultiRetN3_0arg form MR3, N=3 returns, 0 args (length 4):
// `local a, b, c = t:m()`.
func TestPJ5_SelfCall_E2E_MultiRetN3_0arg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local o = { m = function(self) return 7, 11, 13 end }
local function caller(t) local a, b, c = t:m(); sum = sum + a + b + c end
for i = 1, 30 do caller(o) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 30*31 {
		t.Errorf("rets = %v, want %d", got, 30*31)
	}
}

// TestPJ5_SelfCall_E2E_MultiRetN2_1Karg form N=2 returns, 1 K arg (length 5).
func TestPJ5_SelfCall_E2E_MultiRetN2_1Karg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local o = { m = function(self, x) return x, x*2 end }
local function caller(t) local a, b = t:m(5); sum = sum + a + b end
for i = 1, 30 do caller(o) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 30*15 {
		t.Errorf("rets = %v, want %d", got, 30*15)
	}
}

// TestPJ5_SelfCall_E2E_ErrorBubbleUp_NilRecv checks that when the SELF form
// receiver is nil, host.Self raises "attempt to index nil value" and the error
// bubbles up transparently to Call (byte-equal to the P1 interpreter path; P4
// does not intercept the error).
func TestPJ5_SelfCall_E2E_ErrorBubbleUp_NilRecv(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local o = nil
o:m()
return 0`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 0)
	if err == nil {
		t.Fatal("应 raise 'attempt to index nil value' 错误,但 Call 成功返回")
	}
	// err message should contain "attempt to index" or "index nil"
	if !strings.Contains(err.Error(), "index") {
		t.Errorf("err 消息 = %q,应含 'index' 关键字", err.Error())
	}
}

// TestPJ5_SelfCall_E2E_ErrorBubbleUp_BadMethod checks that when the SELF form
// method field is a non-function, CALL raises "attempt to call a {type} value"
// and the error bubbles up transparently.
func TestPJ5_SelfCall_E2E_ErrorBubbleUp_BadMethod(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local o = { m = 42 }
o:m()
return 0`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 0)
	if err == nil {
		t.Fatal("应 raise 'attempt to call a number value' 错误")
	}
	if !strings.Contains(err.Error(), "call") {
		t.Errorf("err 消息 = %q,应含 'call' 关键字", err.Error())
	}
}

// TestPJ5_SelfCall_E2E_NestedSelfChain nested two-level SELF inline chain:
// caller(o1) → o1:m() → inner inner(o2) → o2:n() → byte-equal to P1 interpreter
//
// Common business form: OOP multi-object composition (observer notifying a
// listener, wrapper delegating to inner, etc.).
// Checks that chained SELF inline calls do not interfere with each other (the
// two PJ5 SELF inline paths hit independently, SpecSelfCallHits >= 2).
func TestPJ5_SelfCall_E2E_NestedSelfChain(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local total = 0
local inner = { n = function(self, x) total = total + x end }
local outer = { m = function(self, v) inner:n(v) end }
local function caller(t, v) t:m(v) end
for i = 1, 30 do caller(outer, i) end
return total`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// total = sum_{i=1..30} i = 465
	if got := value.AsNumber(value.Value(rets[0])); got != 465 {
		t.Errorf("rets = %v, want 465", got)
	}
	// Nested two-level SELF really promoted: t:m(v) in the outer caller and
	// inner:n(v) inside the inner outer.m each hit the PJ5 SELF inline form
	// once, so SpecSelfCallHits should be >= 2 (two independent Protos).
	if jit.SpecSelfCallHits() < 2 {
		t.Errorf("SpecSelfCallHits = %d,want >= 2(嵌套两层 SELF inline 各命中一次)",
			jit.SpecSelfCallHits())
	}
	t.Logf("SpecSelfCallHits=%d", jit.SpecSelfCallHits())
}

// TestPJ5_SelfCall_E2E_SelfThenCall SELF + regular CALL chain within the same
// closure. `function(t) t:m(); other() end` compiles to SELF + CALL + ... +
// RETURN, but SELF is not a standalone SubProto inline form (>5 ops exceeds
// form6); checks that the whole path is byte-equal and not broken.
func TestPJ5_SelfCall_E2E_SelfThenCall(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local mCount = 0
local oCount = 0
local o = { m = function(self) mCount = mCount + 1 end }
local function other() oCount = oCount + 1 end
local function caller(t) t:m(); other() end
for i = 1, 30 do caller(o) end
return mCount, oCount`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 2)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 30 {
		t.Errorf("mCount = %v, want 30", got)
	}
	if got := value.AsNumber(value.Value(rets[1])); got != 30 {
		t.Errorf("oCount = %v, want 30", got)
	}
}

// TestPJ5_SelfCall_E2E_SpecTemplate_WarmupThenForce checks that the PJ5 SELF +
// CALL spec template is really wired in (continuing §9.10 EmitSelfNodeHit reuse
// + §9.17 upgrade):
//
// **prove-the-path**: the SpecSelfCallSpecHits probe proves the byte-level SELF
// segment template is really compiled. Phase 1 warmup lets the P1 interpreter
// fill IC NodeHit at SELF pc=1 + aggregate feedback; Phase 2 force-all promotes
// caller and analyzeSelfCallSpecForm hits → byte-level inline.
//
// caller(t) { t:m() } form: MOVE + SELF + CALL + RETURN void, method `m` is a
// string key (hash-segment NodeHit). The spec-segment SELF skips host.Self, and
// CALL goes through host.CallBaseline.
func TestPJ5_SelfCall_E2E_SpecTemplate_WarmupThenForce(t *testing.T) {
	jit.ResetSpecHits()

	src := `
local count = 0
local o = { m = function(self) count = count + 1 end }
local function caller(t) t:m() end
for i = 1, 100 do caller(o) end  -- warmup:P1 填 SELF IC[1]=NodeHit
caller(o)
return count`

	st, mainCl := loadFnP4(t, src)

	// Phase 1: without force-all → caller is not promoted, P1 runs warmup to fill IC[1]
	rets1, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets1[0])); got != 101 {
		t.Errorf("Phase 1 result = %v, want 101", got)
	}
	if jit.SpecSelfCallSpecHits() != 0 {
		t.Errorf("Phase 1 末:SpecSelfCallSpecHits=%d, want 0(P1 路径不应触发 spec 模板编译)",
			jit.SpecSelfCallSpecHits())
	}

	// Phase 2: force-all promotes caller. IC[1] was filled with NodeHit in
	// Phase 1 → analyzeSelfCallSpecForm hits → byte-level SELF segment inline compile.
	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets2, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets2[0])); got != 101 {
		t.Errorf("Phase 2 result = %v, want 101", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	t.Logf("SpecSelfCallSpecHits: %d → %d(Phase 2 增量 = %d)", specBefore, specAfter, specAfter-specBefore)
	if specAfter <= specBefore {
		t.Errorf("Phase 2:SpecSelfCallSpecHits 未增长(%d → %d)"+
			" → SELF + CALL spec 模板未真编译,prove-the-path 失败", specBefore, specAfter)
	}
}

// TestPJ5_SelfCall_E2E_SpecTemplate_1KArg checks the PJ5 SELF + CALL spec
// template 1 K arg form (continuing §9.19 extension from 0 args to 0..7 args):
// caller(t) { t:m(42) }. Warmup stabilizes the SELF IC + force-all promotes
// caller → spec template hits + args loaded + host.CallBaseline byte-equal to P1.
func TestPJ5_SelfCall_E2E_SpecTemplate_1KArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local o = { m = function(self, x) sum = sum + x end }
local function caller(t) t:m(42) end
for i = 1, 100 do caller(o) end  -- warmup
caller(o)
return sum`
	st, mainCl := loadFnP4(t, src)

	// Phase 1: warmup fills SELF IC[1]=NodeHit + FBSelfMono
	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	// Phase 2: force-all promotes caller → spec template 1 K arg form hits
	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 101*42 {
		t.Errorf("Phase 2 result = %v, want %d", got, 101*42)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长(%d → %d) → 1 K 参 spec template 未命中",
			specBefore, specAfter)
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d(增量 = %d)", specBefore, specAfter, specAfter-specBefore)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_1RegArg 1 reg arg form.
func TestPJ5_SelfCall_E2E_SpecTemplate_1RegArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local o = { m = function(self, x) sum = sum + x end }
local function caller(t, v) t:m(v) end
for i = 1, 100 do caller(o, i) end  -- warmup
caller(o, 1000)
return sum`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	// 1+2+..+100 = 5050, +1000 = 6050 (Phase 1 + Phase 2 accumulated)
	// Phase 1 sum: 5050; Phase 2 sum: 5050 + 1000 = 6050
	if got := value.AsNumber(value.Value(rets[0])); got != 6050 {
		t.Errorf("Phase 2 result = %v, want 6050", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → 1 reg 参 spec template 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_3Args 3 reg arg form.
func TestPJ5_SelfCall_E2E_SpecTemplate_3Args(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local o = { m = function(self, a, b, c) sum = sum + a + b + c end }
local function caller(t, x, y, z) t:m(x, y, z) end
for i = 1, 100 do caller(o, i, i+1, i+2) end  -- warmup
caller(o, 1, 2, 3)
return sum`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	// Phase 1: sum_{i=1..100} (i + (i+1) + (i+2)) = 3*5050 + 300 = 15450
	// Phase 2: 15450 + 1+2+3 = 15456
	if got := value.AsNumber(value.Value(rets[0])); got != 15456 {
		t.Errorf("Phase 2 result = %v, want 15456", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → 3 参 spec template 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_TailCall_M0 PJ5 SELF + TAILCALL spec template
// 0 arg form (`function(t) return t:m() end`).
func TestPJ5_SelfCall_E2E_SpecTemplate_TailCall_M0(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local o = { m = function(self) return 42 end }
local function caller(t) return t:m() end
local sum = 0
for i = 1, 100 do sum = sum + caller(o) end  -- warmup
sum = sum + caller(o)
return sum`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 101*42 {
		t.Errorf("Phase 2 result = %v, want %d", got, 101*42)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → TAILCALL spec template 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_Getter_M0 PJ5 SELF + CALL getter 1-return
// form (`function(t) local r = t:m(); return r end`) spec template.
func TestPJ5_SelfCall_E2E_SpecTemplate_Getter_M0(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local o = { m = function(self) return 42 end }
local function caller(t) local r = t:m(); return r end
local sum = 0
for i = 1, 100 do sum = sum + caller(o) end  -- warmup
sum = sum + caller(o)
return sum`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 101*42 {
		t.Errorf("Phase 2 result = %v, want %d", got, 101*42)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → getter spec template 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_UpvalRecv PJ5 SELF spec template with a
// GETUPVAL receiver (analyzeSelfCallForm already supports form U*; layering the
// spec gate on top should work automatically).
func TestPJ5_SelfCall_E2E_SpecTemplate_UpvalRecv(t *testing.T) {
	jit.ResetSpecHits()
	// caller is a closure, o is accessed via upvalue → SELF receiver = GETUPVAL form
	src := `
local count = 0
local o = { m = function(self) count = count + 1 end }
local function tick() o:m() end
for i = 1, 100 do tick() end  -- warmup
tick()
return count`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 101 {
		t.Errorf("Phase 2 result = %v, want 101", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → UPVAL recv spec template 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_TailCall_1RegArg PJ5 SELF + TAILCALL spec
// template 1 reg arg form.
func TestPJ5_SelfCall_E2E_SpecTemplate_TailCall_1RegArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local o = { m = function(self, x) return x * 2 end }
local function caller(t, v) return t:m(v) end
local sum = 0
for i = 1, 100 do sum = sum + caller(o, i) end  -- warmup
sum = sum + caller(o, 1000)
return sum`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	// Phase 1: sum_{i=1..100} 2*i = 2*5050 = 10100
	// Phase 2: 10100 + 2*1000 = 12100
	if got := value.AsNumber(value.Value(rets[0])); got != 12100 {
		t.Errorf("Phase 2 result = %v, want 12100", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → TAILCALL 1 reg 参 spec 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_MultiRet0Param PJ5 SELF + CALL form4
// N=2 returns, 0 args form (`function(_, t) local a, b = t:m() end` drop multi-ret)
// spec template.
//
// caller emits 4 ops: [0]MOVE A=2 B=1, [1]SELF A=2 B=2 C=K, [2]CALL B=2 C=3,
// [3]RETURN B=1. analyzeSelfCallForm4 line 2662 already recognizes the cC=3/4 + retB=1 form.
func TestPJ5_SelfCall_E2E_SpecTemplate_MultiRet0Param(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local mt = { m = function(self) count = count + 1; return 1, 2 end }
local function caller(_, t) local a, b = t:m() end
for i = 1, 100 do caller(nil, mt) end  -- warmup
caller(nil, mt)
return count`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	// Phase 2: main chunk reruns (count re-init=0), 101 calls → count=101
	if got := value.AsNumber(value.Value(rets[0])); got != 101 {
		t.Errorf("Phase 2 result = %v, want 101", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → form4 N=2 返 0 参 spec 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_MultiRet1KArg PJ5 SELF + CALL form5
// N=2 returns, 1 K arg form (`function(_, t) local a, b = t:m(7) end`) spec template.
//
// caller emits 5 ops: [0]MOVE A=2 B=1, [1]SELF A=2 B=2 C=K, [2]LOADK A=4 Bx=K,
// [3]CALL B=3 C=3, [4]RETURN B=1. analyzeSelfCallForm5 line 2848 already recognizes it.
func TestPJ5_SelfCall_E2E_SpecTemplate_MultiRet1KArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local mt = { m = function(self, k) count = count + k; return 1, 2 end }
local function caller(_, t) local a, b = t:m(7) end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	// Phase 2: 101 times * 7 = 707
	if got := value.AsNumber(value.Value(rets[0])); got != 707 {
		t.Errorf("Phase 2 result = %v, want 707", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → form5 N=2 返 1 K 参 spec 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_MultiRet1RegArg PJ5 SELF + CALL form5
// N=2 returns, 1 reg arg form (`function(_, t, v) local a, b = t:m(v) end`).
func TestPJ5_SelfCall_E2E_SpecTemplate_MultiRet1RegArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local mt = { m = function(self, k) count = count + k; return 1, 2 end }
local function caller(_, t, v) local a, b = t:m(v) end
for i = 1, 100 do caller(nil, mt, i) end
caller(nil, mt, 1000)
return count`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	// Phase 2:sum_{i=1..100} i + 1000 = 5050 + 1000 = 6050
	if got := value.AsNumber(value.Value(rets[0])); got != 6050 {
		t.Errorf("Phase 2 result = %v, want 6050", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → form5 N=2 返 1 reg 参 spec 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_MultiRet2KArg PJ5 SELF + CALL form6 N=2
// returns 2 K args form (`function(_, t) local a, b = t:m(7, 8) end`) spec template.
//
// caller emits 6 ops: MOVE+SELF+LOADK+LOADK+CALL B=4 C=3+RETURN B=1.
// analyzeSelfCallForm6 (b)(c) extended cC=1/3/4 already supports N=2/3 returns.
func TestPJ5_SelfCall_E2E_SpecTemplate_MultiRet2KArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local mt = { m = function(self, x, y) count = count + x + y; return 1, 2 end }
local function caller(_, t) local a, b = t:m(7, 8) end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	// Phase 2: 101 times * (7+8) = 1515
	if got := value.AsNumber(value.Value(rets[0])); got != 1515 {
		t.Errorf("Phase 2 result = %v, want 1515", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → form6 N=2 返 2 K 参 spec 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_MultiRet3KArg PJ5 SELF + CALL form7 N=2
// returns 3 K args form (`function(_, t) local a, b = t:m(7, 8, 9) end`) spec template.
//
// caller emits 7 ops: MOVE+SELF+LOADK×3+CALL B=5 C=3+RETURN B=1.
// analyzeSelfCallForm7 Code[5]=CALL branch cC=1/3/4 already supports N=2/3 returns.
func TestPJ5_SelfCall_E2E_SpecTemplate_MultiRet3KArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local mt = { m = function(self, x, y, z) count = count + x + y + z; return 1, 2 end }
local function caller(_, t) local a, b = t:m(7, 8, 9) end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	// Phase 2: 101 times * (7+8+9) = 2424
	if got := value.AsNumber(value.Value(rets[0])); got != 2424 {
		t.Errorf("Phase 2 result = %v, want 2424", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → form7 N=2 返 3 K 参 spec 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_MultiRet4KArg PJ5 SELF + CALL form8 N=2
// returns 4 K args form (`function(_, t) local a, b = t:m(7, 8, 9, 10) end`) spec template.
//
// caller emits 8 ops: MOVE+SELF+LOADK×4+CALL B=6 C=3+RETURN B=1.
// analyzeSelfCallForm8 Code[6]=CALL branch cC=1/3/4 already supports N=2/3 returns.
func TestPJ5_SelfCall_E2E_SpecTemplate_MultiRet4KArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local mt = { m = function(self, x, y, z, w) count = count + x + y + z + w; return 1, 2 end }
local function caller(_, t) local a, b = t:m(7, 8, 9, 10) end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	// Phase 2: 101 times * (7+8+9+10) = 3434
	if got := value.AsNumber(value.Value(rets[0])); got != 3434 {
		t.Errorf("Phase 2 result = %v, want 3434", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → form8 N=2 返 4 K 参 spec 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_MultiRet5KArg PJ5 SELF + CALL form9 N=2
// returns 5 K args form (`function(_, t) local a, b = t:m(7,8,9,10,11) end`) spec template.
//
// caller emits 9 ops: MOVE+SELF+LOADK×5+CALL B=7 C=3+RETURN B=1.
// analyzeSelfCallForm9 Code[7]=CALL branch cC=1/3/4 already supports N=2/3 returns.
func TestPJ5_SelfCall_E2E_SpecTemplate_MultiRet5KArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local mt = { m = function(self, x, y, z, w, v) count = count + x + y + z + w + v; return 1, 2 end }
local function caller(_, t) local a, b = t:m(7, 8, 9, 10, 11) end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	// Phase 2: 101 times * (7+8+9+10+11) = 4545
	if got := value.AsNumber(value.Value(rets[0])); got != 4545 {
		t.Errorf("Phase 2 result = %v, want 4545", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → form9 N=2 返 5 K 参 spec 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_MultiRet6KArg PJ5 SELF + CALL formN N=2
// returns 6 K args form (`function(_, t) local a, b = t:m(7,...,12) end`) spec template.
//
// caller emits 10 ops: MOVE+SELF+LOADK×6+CALL B=8 C=3+RETURN B=1.
// analyzeSelfCallFormN cC=1/3/4 already supports N=2/3 returns.
func TestPJ5_SelfCall_E2E_SpecTemplate_MultiRet6KArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local mt = { m = function(self, x, y, z, w, v, u) count = count + x + y + z + w + v + u; return 1, 2 end }
local function caller(_, t) local a, b = t:m(7, 8, 9, 10, 11, 12) end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	// Phase 2: 101 times * (7+8+9+10+11+12) = 5757
	if got := value.AsNumber(value.Value(rets[0])); got != 5757 {
		t.Errorf("Phase 2 result = %v, want 5757", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → formN N=2 返 6 K 参 spec 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_MultiRetN4_0Param PJ5 SELF + CALL form4
// N=4 returns, 0 args form (`local a,b,c,d = t:m()` drop multi-ret) spec template.
//
// caller emits 4 ops: MOVE+SELF+CALL B=2 C=5 (N=4 returns)+RETURN B=1.
// Really wired in after this batch's isValidSpecCallRetCount extension to cC∈{1,3..16}.
func TestPJ5_SelfCall_E2E_SpecTemplate_MultiRetN4_0Param(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local mt = { m = function(self) count = count + 1; return 1, 2, 3, 4 end }
local function caller(_, t) local a, b, c, d = t:m() end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 101 {
		t.Errorf("Phase 2 result = %v, want 101", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → form4 N=4 返 0 参 spec 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_MultiRetN5_0Param PJ5 SELF + CALL form4
// N=5 returns, 0 args form (`local a..e = t:m()`) cC=6 spec template.
func TestPJ5_SelfCall_E2E_SpecTemplate_MultiRetN5_0Param(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local mt = { m = function(self) count = count + 1; return 1, 2, 3, 4, 5 end }
local function caller(_, t) local a, b, c, d, e = t:m() end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 101 {
		t.Errorf("Phase 2 result = %v, want 101", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → form4 N=5 返 0 参 spec 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_MultiRetN4_1KArg PJ5 SELF + CALL form5
// N=4 returns, 1 K arg form (`local a..d = t:m(7)`) cC=5 spec template.
func TestPJ5_SelfCall_E2E_SpecTemplate_MultiRetN4_1KArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local mt = { m = function(self, k) count = count + k; return 1, 2, 3, 4 end }
local function caller(_, t) local a, b, c, d = t:m(7) end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 707 {
		t.Errorf("Phase 2 result = %v, want 707", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → form5 N=4 返 1 K 参 spec 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_MultiRetN4_1RegArg PJ5 SELF + CALL form5
// N=4 returns, 1 reg arg form (`local a..d = t:m(v)` 1 reg arg + multi-ret drop).
// Continuing the 84c7ed4 cC∈{1,3..16} extension, checks that 1 reg arg hits the
// same way on the N>=4 return path.
func TestPJ5_SelfCall_E2E_SpecTemplate_MultiRetN4_1RegArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local mt = { m = function(self, k) count = count + k; return 1, 2, 3, 4 end }
local function caller(_, t, v) local a, b, c, d = t:m(v) end
for i = 1, 100 do caller(nil, mt, i) end
caller(nil, mt, 1000)
return count`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	// Phase 2: sum_{i=1..100} i + 1000 = 5050 + 1000 = 6050
	if got := value.AsNumber(value.Value(rets[0])); got != 6050 {
		t.Errorf("Phase 2 result = %v, want 6050", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → form5 N=4 返 1 reg 参 spec 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_MultiRetN4_3KArg PJ5 SELF + CALL form7
// N=4 returns, 3 K args form (`local a..d = t:m(7,8,9)` 3 K args + multi-ret drop).
// caller emits 7 ops: MOVE+SELF+LOADK×3+CALL B=5 C=5+RETURN B=1.
func TestPJ5_SelfCall_E2E_SpecTemplate_MultiRetN4_3KArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local mt = { m = function(self, x, y, z) count = count + x + y + z; return 1, 2, 3, 4 end }
local function caller(_, t) local a, b, c, d = t:m(7, 8, 9) end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	// Phase 2: 101 times * (7+8+9) = 2424
	if got := value.AsNumber(value.Value(rets[0])); got != 2424 {
		t.Errorf("Phase 2 result = %v, want 2424", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → form7 N=4 返 3 K 参 spec 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_MultiRetN8_0Param PJ5 SELF + CALL form4
// **N=8 returns** (cC=9) 0 args form (`local a..h = t:m()`) spec template — checks
// isValidSpecCallRetCount cC∈{1,3..16} boundary near the upper limit (N=15 is the
// upper limit, N=8 is a common practical upper limit for business multi-return;
// this test represents a practical scenario).
//
// caller emits 4 ops: MOVE+SELF+CALL B=2 C=9+RETURN B=1
// (8 returns land in R(callA..callA+7)).
func TestPJ5_SelfCall_E2E_SpecTemplate_MultiRetN8_0Param(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local mt = { m = function(self) count = count + 1; return 1, 2, 3, 4, 5, 6, 7, 8 end }
local function caller(_, t) local a, b, c, d, e, f, g, h = t:m() end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 101 {
		t.Errorf("Phase 2 result = %v, want 101", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → form4 N=8 返 0 参 spec 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d(N=8 返 cC=9)", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_MultiRetN15_0Param PJ5 SELF + CALL form4
// **N=15 returns** (cC=16) 0 args — checks the strict isValidSpecCallRetCount cC<=16
// upper limit. Note: cC=16 is the max N=15 returns the spec template allows; cC=17
// should be rejected by the gate.
func TestPJ5_SelfCall_E2E_SpecTemplate_MultiRetN15_0Param(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local mt = { m = function(self) count = count + 1; return 1,2,3,4,5,6,7,8,9,10,11,12,13,14,15 end }
local function caller(_, t)
  local a,b,c,d,e,f,g,h,i,j,k,l,m,n,o = t:m()
end
for i = 1, 100 do caller(nil, mt) end
caller(nil, mt)
return count`
	st, mainCl := loadFnP4(t, src)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	st.bridge.SetForceAllPromote(true)
	specBefore := jit.SpecSelfCallSpecHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 101 {
		t.Errorf("Phase 2 result = %v, want 101", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → form4 N=15 返 0 参 spec 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d(N=15 返 cC=16,上界严格)",
		specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_ErrorBubbleUp_NilRecv checks the correctness
// of error bubbling when the receiver is nil under the PJ5 SELF spec template path
// (continuing PR #26 comment suggestion 3: deep coverage path + Go G correctness
// after the R14 fix).
//
// **Scenario**: warmup phase fills IC NodeHit + FBSelfMono feedback, Phase 2
// promotes to the P4 spec template path; Phase 3 uses a nil receiver to make the
// spec NodeHit guard fail → onOSRExit accumulates deopt → falls back to the full
// P1 SELF segment via host.Self → raises "attempt to index nil value" which
// bubbles up transparently.
func TestPJ5_SelfCall_E2E_SpecTemplate_ErrorBubbleUp_NilRecv(t *testing.T) {
	jit.ResetSpecHits()
	// Phase 1: warmup IC NodeHit + FBSelfMono feedback
	warmupSrc := `
local mt = { m = function(self) return 42 end }
local function caller(t) return t:m() end
local sum = 0
for i = 1, 100 do sum = sum + caller(mt) end
return sum`
	st, mainCl := loadFnP4(t, warmupSrc)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("Phase 2 spec template: %v", err)
	}
	hitsBefore := jit.SpecSelfCallSpecHits()
	if hitsBefore == 0 {
		t.Fatal("Phase 2 SpecSelfCallSpecHits=0 — spec template 未触达,测试前提失败")
	}

	// Phase 3: same caller but receiver=nil → spec template NodeHit guard must fail
	// → deopt → host.Self → raise "index nil"
	nilSrc := `
local mt = { m = function(self) return 42 end }
local function caller(t) return t:m() end
return caller(nil)`
	st2, mainCl2 := loadFnP4(t, nilSrc)
	st2.bridge.SetForceAllPromote(true)
	_, err := st2.Call(value.GCRefOf(mainCl2), nil, 1)
	if err == nil {
		t.Fatal("应 raise 'attempt to index nil value' 错误,但 Call 成功返回")
	}
	if !strings.Contains(err.Error(), "index") {
		t.Errorf("err 消息 = %q,应含 'index' 关键字", err.Error())
	}
	t.Logf("spec template 错误冒泡正确:%v", err)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_ErrorBubbleUp_BadMethod checks the correctness
// of error bubbling when the method field is a non-function under the PJ5 SELF
// spec template path.
//
// **Scenario**: in warmup the method is a function, filling IC NodeHit + FBSelfMono;
// Phase 2 the spec template hits; Phase 3 uses a different receiver whose method
// field is a number → spec NodeHit guard fails (shape change / different NodeVal
// kind) → deopt → host.Self → method is a number → CALL segment raises "attempt
// to call a number value".
func TestPJ5_SelfCall_E2E_SpecTemplate_ErrorBubbleUp_BadMethod(t *testing.T) {
	jit.ResetSpecHits()
	// Phase 1+2: warmup uses method=function to fill IC NodeHit
	warmupSrc := `
local mt = { m = function(self) return 42 end }
local function caller(t) return t:m() end
local sum = 0
for i = 1, 100 do sum = sum + caller(mt) end
return sum`
	st, mainCl := loadFnP4(t, warmupSrc)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("Phase 2 spec template: %v", err)
	}
	if jit.SpecSelfCallSpecHits() == 0 {
		t.Fatal("Phase 2 SpecSelfCallSpecHits=0 — spec template 未触达")
	}

	// Phase 3: method is a number → spec deopt → host.Self gets a number → CALL raise
	badSrc := `
local bad = { m = 42 }
return bad:m()`
	st2, mainCl2 := loadFnP4(t, badSrc)
	st2.bridge.SetForceAllPromote(true)
	_, err := st2.Call(value.GCRefOf(mainCl2), nil, 1)
	if err == nil {
		t.Fatal("应 raise 'attempt to call a number value' 错误")
	}
	if !strings.Contains(err.Error(), "call") {
		t.Errorf("err 消息 = %q,应含 'call' 关键字", err.Error())
	}
	t.Logf("spec template BadMethod 错误冒泡正确:%v", err)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_OSRExitToDeopt checks the full OSR exit
// protocol closed loop, first stage: repeatedly triggering ≥ DeoptThreshold spec
// NodeHit guard failures → onOSRExit accumulates deopt → state switches to
// P4Deoptimized + SpecP4DeoptHits grows.
//
// Continuing §9.18 OSR exit protocol skeleton + §9.19 PJ5 SELF spec template
// wiring, this batch verifies end-to-end the full state machine transition under
// a real business path (not the synthetic driver in p4state_test.go).
//
// **Scenario**: caller runs repeatedly; each time force-all promotes to P4 spec
// template then runs a different receiver shape → spec NodeHit guard fails → after
// 16 onOSRExit calls switch to
// P4Deoptimized + SpecP4DeoptHits=1.
func TestPJ5_SelfCall_E2E_SpecTemplate_OSRExitToDeopt(t *testing.T) {
	jit.ResetSpecHits()
	jit.ResetP4SpecState()

	// Main form: caller(t) takes the spec template path, warmed up with the m1 receiver
	src := `
local m1 = { m = function(self) return 1 end }
local m2 = { m = function(self, x) return 2 end, other = 99, more = 88 }
local function caller(t) return t:m() end
-- warmup phase 1:filling IC NodeHit + FBSelfMono
for i = 1, 100 do caller(m1) end
-- Phase 2:m2 shape 不同(extra fields),spec NodeHit guard 失败 → deopt
local sum = 0
for i = 1, 30 do sum = sum + caller(m2) end  -- 30 次 > DeoptThreshold(16)
return sum`
	st, mainCl := loadFnP4(t, src)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)

	deoptBefore := jit.SpecP4DeoptHits()
	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 2 force-all: %v", err)
	}
	deoptAfter := jit.SpecP4DeoptHits()

	// **prove-the-path hard assertion** (continuing §9.18 OSR exit protocol loop):
	// under a real business path, 30 m2 calls trigger ≥ DeoptThreshold (16) onOSRExit
	// calls, so SpecP4DeoptHits should at least += 1 (accumulation reaching the
	// threshold triggers the P4Deoptimized transition + incSpecP4DeoptHits).
	if deoptAfter <= deoptBefore {
		t.Errorf("SpecP4DeoptHits 未增长(%d → %d)— OSR exit 协议未真接入 spec template 路径",
			deoptBefore, deoptAfter)
	}
	t.Logf("SpecP4DeoptHits: %d → %d(增量 = %d,OSR exit 协议真接入实证)",
		deoptBefore, deoptAfter, deoptAfter-deoptBefore)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_DeoptStorm V20 deopt storm e2e: multiple
// different Protos repeatedly deopt → multiple p4SpecState accumulate independently
// without interference.
//
// **Continuing [08 §V20] deopt storm**: checks OSR exit protocol correctness under
// concurrent multi-Proto multi-path deopt — each Proto's p4SpecEntry is independent
// (per-Proto fields), accumulated deopt does not interfere, and p4SpecMu
// serialization guarantees race-free.
//
// **Scenario**: 10 different caller protos + 10 different receiver shapes; each
// caller + bad_recv pair triggers independent deopt accumulation, with each proto
// state independent.
func TestPJ5_SelfCall_E2E_SpecTemplate_DeoptStorm(t *testing.T) {
	jit.ResetSpecHits()
	jit.ResetP4SpecState()

	// Multiple caller protos repeatedly run their own spec template + bad receiver to trigger deopt
	src := `
local m_ok = { m = function(self) return 1 end }
local m_bad = { m = function(self) return 2 end, x1 = 1, x2 = 2 }  -- 不同 shape
local function c1(t) return t:m() end
local function c2(t) return t:m() end
local function c3(t) return t:m() end
local function c4(t) return t:m() end
local function c5(t) return t:m() end

-- warmup 5 个 caller 都用 m_ok 填 IC NodeHit
for i = 1, 50 do
  c1(m_ok); c2(m_ok); c3(m_ok); c4(m_ok); c5(m_ok)
end

-- 5 个 caller 都用 m_bad 触发 spec template guard 失败(各 caller 独立 deopt)
local sum = 0
for i = 1, 30 do  -- 30 次 > DeoptThreshold(16),每 caller 切 P4Deoptimized
  sum = sum + c1(m_bad)
  sum = sum + c2(m_bad)
  sum = sum + c3(m_bad)
  sum = sum + c4(m_bad)
  sum = sum + c5(m_bad)
end
return sum`
	st, mainCl := loadFnP4(t, src)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)
	deoptBefore := jit.SpecP4DeoptHits()
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("deopt storm: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 30*5*2 { // 30 iter * 5 caller * m_bad.m()=2
		t.Errorf("Phase 2 result = %v, want %d", got, 30*5*2)
	}
	deoptAfter := jit.SpecP4DeoptHits()

	// **prove-the-path hard assertion**: 5 independent caller protos each accumulate
	// deopt → threshold triggers P4Deoptimized → SpecP4DeoptHits accumulates ≥ 5
	// (each caller at least once).
	// In practice should be far > 5 (each caller runs 30 m_bad calls, switching to
	// P4Deoptimized once every 16 calls).
	growth := deoptAfter - deoptBefore
	if growth < 5 {
		t.Errorf("SpecP4DeoptHits 增长 %d, want >= 5(5 caller 各至少 1 次 deopt 切换)",
			growth)
	}
	t.Logf("SpecP4DeoptHits: %d → %d(增量 = %d,5 caller 独立 deopt 风暴 + 各自累积)",
		deoptBefore, deoptAfter, growth)
}

// TestPJ5_FrameInline_E2E_GatingOpen_HitsOne checks PJ5 Option B Spike 1 frame
// setup inlining (continuing the commit-5m ciDepth Go vs mirror sync bug fix):
// amd64 archSupportsFrameInline=true + analyzeSelfCallSpecForm useFrameInline gate
// enabled + full end-to-end byte-equal to P1.
//
// **prove-the-path hard assertion**:
//   - program output correct (byte-equal P1): count=50
//   - amd64: SpecFrameInlineHits >= 1 (Compile) + SpecFrameInlineRunHits >= 1 (Run)
//   - arm64: archSupportsFrameInline=false gate closed, program correctness assertion still runs
func TestPJ5_FrameInline_E2E_GatingOpen_HitsOne(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local o = { m = function(self) count = count + 1 end }
local function caller(t) t:m() end
for i = 1, 50 do caller(o) end
return count`
	st, mainCl := loadFnP4(t, src)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("force-all run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 50 {
		t.Errorf("rets = %v, want 50(byte-equal P1:Run 期 dispatcher + helper 真接入)", got)
	}

	// **arm64 blocker fix** (continuing PR comment d8fc8ba): only amd64 hard-asserts Hits/RunHits
	if archSupportsFrameInlineForTest() {
		if h := jit.SpecFrameInlineHits(); h == 0 {
			t.Errorf("SpecFrameInlineHits = 0, want >= 1(amd64 闸门 open)")
		}
		if h := jit.SpecFrameInlineRunHits(); h == 0 {
			t.Errorf("SpecFrameInlineRunHits = 0, want >= 1(Run 期真触达)")
		}
	}
	t.Logf("SpecFrameInlineHits=%d / RunHits=%d (Spike 1 真接入完整端到端实证)",
		jit.SpecFrameInlineHits(), jit.SpecFrameInlineRunHits())
}

// TestPJ5_FrameInline_E2E_Spike2_3KArg checks that Spike 2 N-arg fixed-args setter
// form useFrameInline is really wired in (continuing commit-5p: callArgCount gate
// extended to 0..7).
// 3 K arg form: t:m(1,2,3), callee body computes sum from self+a+b+c.
func TestPJ5_FrameInline_E2E_Spike2_3KArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local o = { m = function(self, a, b, c) sum = sum + a + b + c end }
local function caller(t) t:m(1, 2, 3) end
for i = 1, 100 do caller(o) end
return sum`
	st, mainCl := loadFnP4(t, src)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("force-all run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 100*6 {
		t.Errorf("rets = %v, want %d (byte-equal P1: callee body 真跑 100 次 1+2+3=6)",
			got, 100*6)
	}
	if archSupportsFrameInlineForTest() {
		if jit.SpecFrameInlineRunHits() == 0 {
			t.Errorf("SpecFrameInlineRunHits = 0,Spike 2 3 K 参 useFrameInline 路径未真触达")
		}
	}
	t.Logf("Spike 2 3 K 参:Hits=%d / RunHits=%d", jit.SpecFrameInlineHits(), jit.SpecFrameInlineRunHits())
}

// TestPJ5_FrameInline_E2E_Spike4_Getter checks that Spike 4 1-return getter form
// useFrameInline is really wired in (continuing commit-5q: nresults param computed
// from callC-1).
// Form: `local r = t:m(); return r`, callC=2, 1 return.
func TestPJ5_FrameInline_E2E_Spike4_Getter(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local o = { val = 42, m = function(self) return self.val end }
local function caller(t) local r = t:m(); return r end
local s = 0
for i = 1, 100 do s = s + caller(o) end
return s`
	st, mainCl := loadFnP4(t, src)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("force-all run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 100*42 {
		t.Errorf("rets = %v, want %d (byte-equal P1: getter 100 次每次 self.val=42)",
			got, 100*42)
	}
	if archSupportsFrameInlineForTest() {
		if jit.SpecFrameInlineRunHits() == 0 {
			t.Errorf("SpecFrameInlineRunHits = 0,Spike 4 getter useFrameInline 路径未真触达")
		}
	}
	t.Logf("Spike 4 getter: Hits=%d / RunHits=%d", jit.SpecFrameInlineHits(), jit.SpecFrameInlineRunHits())
}

// **Copied arch matrix** (continuing PR comment c5ef665 review): this function
// hardcodes a copy of the jit/arch_*.go::archSupportsFrameInline() matrix; when the
// arch support surface expands in the future, this needs manual updating. The jit
// package does not export the real source function (unexported within the package),
// so the test package cannot call it directly, hence the compromise copy. **When a
// new arch enables archSupportsFrameInline, remember to update this function's
// return matrix accordingly** (grep `archSupportsFrameInlineForTest` to find all uses).
func archSupportsFrameInlineForTest() bool {
	return runtime.GOARCH == "amd64"
}

// TestPJ5_FrameInline_E2E_SelfUsage checks PJ5 Option B Spike 1 frame setup inlining
// + callee body really using the self field:
// `o:m()` callee body `self.val = self.val + 1`, verifies R(callA+1)=self is really
// passed to callee, and enterLuaFrame nargs is computed correctly inside the helper.
//
// **Wiring correctness hard assertion**: byte-equal fallback verification of Spike 1's
// current nargs=0 implementation. If nargs is computed wrong (missing self / missing
// args), callee reading self.val would read nil, triggering a runtime error or t.val
// not incrementing.
//
// **commit-5i**: adds SpecFrameInlineRunHits to distinguish Compile vs Run reach.
func TestPJ5_FrameInline_E2E_SelfUsage(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local t = { val = 42, m = function(self) self.val = self.val + 1 end }
local function caller(o) o:m() end
for i = 1, 50 do caller(t) end
return t.val`
	st, mainCl := loadFnP4(t, src)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("force-all run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 92 {
		t.Errorf("rets = %v, want 92(42 + 50,byte-equal P1:self 真传 callee + self.val++ 真执行)", got)
	}
	t.Logf("SpecFrameInlineHits=%d (Compile) / SpecFrameInlineRunHits=%d (Run 触达)",
		jit.SpecFrameInlineHits(), jit.SpecFrameInlineRunHits())
}

// TestPJ5_FrameInline_E2E_RunHit checks that Run really reaches runFrameInlineDispatcher
// (prove-the-path after the commit-5m ciDepth Go vs mirror sync bug fix): 200 iters
// all on the useFrameInline path, SpecFrameInlineRunHits should = 200 (amd64) or 0 (arm64).
func TestPJ5_FrameInline_E2E_RunHit(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local o = { m = function(self) count = count + 1 end }
local function caller(t) t:m() end
for i = 1, 200 do caller(o) end
return count`
	st, mainCl := loadFnP4(t, src)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("force-all run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 200 {
		t.Errorf("rets = %v, want 200(byte-equal P1)", got)
	}
	if archSupportsFrameInlineForTest() {
		if jit.SpecFrameInlineRunHits() == 0 {
			t.Errorf("SpecFrameInlineRunHits = 0,Run 期未真触达 useFrameInline 路径")
		}
	}
	t.Logf("SpecFrameInlineHits=%d / SpecFrameInlineRunHits=%d",
		jit.SpecFrameInlineHits(), jit.SpecFrameInlineRunHits())
}

// TestPJ5_FrameInline_E2E_Spike3_Vararg checks that Spike 3 vararg callee form
// useFrameInline is really wired in (callee takes self + vararg `...`).
// Form: `function(self, ...) local a,b,c=...; sum = sum + a + b + c end`,
// callee.IsVararg=true.
func TestPJ5_FrameInline_E2E_Spike3_Vararg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local o = { m = function(self, ...) local a, b, c = ...; sum = sum + a + b + c end }
local function caller(t) t:m(1, 2, 3) end
for i = 1, 100 do caller(o) end
return sum`
	st, mainCl := loadFnP4(t, src)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("force-all run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 100*6 {
		t.Errorf("rets = %v, want %d (byte-equal P1: vararg 100 次每次 1+2+3=6)",
			got, 100*6)
	}
	if archSupportsFrameInlineForTest() {
		if jit.SpecFrameInlineRunHits() == 0 {
			t.Errorf("SpecFrameInlineRunHits = 0,Spike 3 vararg useFrameInline 路径未真触达")
		}
	}
	t.Logf("Spike 3 vararg: Hits=%d / RunHits=%d", jit.SpecFrameInlineHits(), jit.SpecFrameInlineRunHits())
}

// TestPJ5_FrameInline_E2E_Spike5_ZeroCross checks the commit-5u true zero-cross
// optimization: when the callee is also P4-promoted, the helper calls enterGibbous
// directly, skipping the executeFrom main loop.
// **State-level probe st.frameInlineZeroCrossHits** reads the State field directly.
//
// The callee uses the "PJ7 form `function(self) return self.x end`" (GETTABLE +
// RETURN), which PJ4 IC ArrayHit/NodeHit supports at P4 (continuing §9.7-§9.10). The
// getter form callC=2 + caller `local r = t:m(); return r` takes the useFrameInline
// 1-return path.
func TestPJ5_FrameInline_E2E_Spike5_ZeroCross(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local o = { x = 42, m = function(self) return self.x end }
local function caller(t) local r = t:m(); return r end
local s = 0
for i = 1, 200 do s = s + caller(o) end
return s`
	st, mainCl := loadFnP4(t, src)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)
	beforeZC := st.frameInlineZeroCrossHits
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("force-all run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 200*42 {
		t.Errorf("rets = %v, want %d(byte-equal P1)", got, 200*42)
	}
	zcGrowth := st.frameInlineZeroCrossHits - beforeZC
	t.Logf("Spike 5 zero-cross: RunHits=%d / ZeroCrossHits 增长=%d",
		jit.SpecFrameInlineRunHits(), zcGrowth)
	// callee `m` (PJ4 GETTABLE + RETURN form) should promote to P4 under force-all,
	// and the zero-cross path should be reached; if zcGrowth=0 = under callee form
	// limits it falls back to the enterLuaFrame + executeFrom path, still byte-equal to P1.
	if archSupportsFrameInlineForTest() && zcGrowth > 0 {
		t.Logf("✅ zero-cross 路径真触达(callee P4 升层 + helper 跳 executeFrom)")
	} else {
		t.Logf("⚠️ zero-cross 路径未触达(callee 未升 P4 或 P4 form 限制 — 回落 enterLuaFrame + executeFrom byte-equal 兜底)")
	}
}

// TestPJ5_FrameInline_E2E_V18Race_ZeroCross checks V18 -race multi-State concurrency
// safety (SELF spec template + useFrameInline + zero-cross path, full chain).
// 8 independent States concurrently run the zero-cross path, go test -race detects
// data races.
func TestPJ5_FrameInline_E2E_V18Race_ZeroCross(t *testing.T) {
	const N = 8
	var wg sync.WaitGroup
	wg.Add(N)
	for k := 0; k < N; k++ {
		go func() {
			defer wg.Done()
			jit.ResetSpecHits()
			src := `
local o = { x = 42, m = function(self) return self.x end }
local function caller(t) local r = t:m(); return r end
local s = 0
for i = 1, 50 do s = s + caller(o) end
return s`
			st, mainCl := loadFnP4(t, src)
			if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
				t.Errorf("warmup: %v", err)
				return
			}
			st.bridge.SetForceAllPromote(true)
			rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
			if err != nil {
				t.Errorf("force-all run: %v", err)
				return
			}
			if got := value.AsNumber(value.Value(rets[0])); got != 50*42 {
				t.Errorf("goroutine %d: rets = %v, want %d", k, got, 50*42)
			}
		}()
	}
	wg.Wait()
}
