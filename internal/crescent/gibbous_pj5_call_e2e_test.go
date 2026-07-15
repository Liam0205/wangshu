//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"testing"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_pj5_call_e2e_test.go —— PJ5 CALL void real-promotion e2e:
// `local function noop()...end; local function invoker() noop() end` (form
// B, GETUPVAL+CALL+RETURN void); after P4 promotion the Run prelude path calls
// host.GetUpval + SetReg + CallBaseline to complete the baseline doCall (byte-equal
// with the P1 doCall dispatch) + DoReturn to pop the frame.
//
// **Physical evidence that PJ5 wires into the main path** (extending the PJ7
// simplified form to inline the call family):
// P4 wires in the CALL opcode + host.CallBaseline crossing the Go-side boundary
// for the first time. **The simplified form covers only 0-arg 0-ret**
// (MOVE/GETUPVAL+CALL+RETURN void) + baseline doCall
// (host/crescent/__call/gibbous all run through in lockstep), and does not take
// the P3 R3 indirect sentinel.
//
// **Related P2 analyzer extension**: this test also verifies that P2 scope-aware
// AnalyzeProto propagates the outer localFnAsts across Proto boundaries (added in
// the same batch of commits) — otherwise invoker's call to noop would be marked
// ReasonUnknownCall, Compilable=NotCompilable, and the P4 path would not be reached.

// TestPJ5_CallVoid_E2E_FormB_Upval: form B (GETUPVAL+CALL+RETURN void)
// real promotion — `local function noop()...end; local function invoker() noop() end`,
// repeatedly calling invoker so that after P4 promotion it truly takes the PJ5 CALL void template.
//
// Key probe: **SpecCallVoidHits** — increments only when Compile hits isCallVoid=true.
// If P4 promotion is not reached or form recognition fails, SpecCallVoidHits stays 0 (test fails).
func TestPJ5_CallVoid_E2E_FormB_Upval(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local count = 0
local function noop() count = count + 1 end
local function invoker() noop() end
for i = 1, 50 do invoker() end
return count`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 50 {
		t.Errorf("rets = %v, want 50(invoker() 50 次每次 noop 让 count++)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL void 模板未真编译——降级 host 或 P4 路径未触达(prove-the-path 失败)")
	}
	t.Logf("SpecCallVoidHits=%d", jit.SpecCallVoidHits())
}

// TestPJ5_CallVoid_E2E_FormB1K_UpvalArg: form B1K (GETUPVAL+LOADK+CALL+RETURN
// void) real promotion — `local function take(x)...end; local function tick() take(42) end`,
// 1 constant K arg, 0 ret. LOADK is a dummy in the mmap segment; on the Run side
// host.SetReg(callA+1, K) loads it into the argument slot.
func TestPJ5_CallVoid_E2E_FormB1K_UpvalArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local function take(x) sum = sum + x end
local function tick() take(42) end
for i = 1, 50 do tick() end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 50*42 {
		t.Errorf("rets = %v, want %d (tick() × 50 each take(42) → sum += 42)", got, 50*42)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL void 1 K 参模板未真编译")
	}
	t.Logf("SpecCallVoidHits=%d", jit.SpecCallVoidHits())
}

// TestPJ5_CallVoid_E2E_FormB1R_UpvalArg: form B1R (GETUPVAL+MOVE+CALL+RETURN
// void) real promotion — `local function take(x)...end; local function tick(v) take(v) end`,
// 1 reg arg, 0 ret. MOVE is a dummy in the mmap segment; on the Run side
// host.GetReg(srcReg) + SetReg(callA+1, val) loads it into the argument slot.
func TestPJ5_CallVoid_E2E_FormB1R_UpvalArg(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local function take(x) sum = sum + x end
local function tick(v) take(v) end
for i = 1, 50 do tick(i) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum = 1+2+...+50 = 1275
	const want = float64((1 + 50) * 50 / 2)
	if got := value.AsNumber(value.Value(rets[0])); got != want {
		t.Errorf("rets = %v, want %v (tick(i) × 50 each take(i) → sum += i)", got, want)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL void 1 reg 参模板未真编译")
	}
	t.Logf("SpecCallVoidHits=%d", jit.SpecCallVoidHits())
}

// TestPJ5_CallVoid_E2E_FormBR1_GetterUpval: form BR1 (GETUPVAL+CALL+RETURN+
// dead RETURN getter) real promotion — `local function f() return 42 end;
// local function get() local x = f(); return x end`, 0 arg, 1 ret. The callee's
// return value lands in R(callA); on the Run side host.DoReturn(retA=callA, retB=2) returns it.
func TestPJ5_CallVoid_E2E_FormBR1_GetterUpval(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f() return 42 end
local function get() local x = f(); return x end
local s = 0
for i = 1, 50 do s = s + get() end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 50*42 {
		t.Errorf("rets = %v, want %d (get() × 50 each returns 42 → s += 42)", got, 50*42)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL void getter 形态未真编译")
	}
	t.Logf("SpecCallVoidHits=%d", jit.SpecCallVoidHits())
}

// **Note**: form A* parameter-callee forms (e.g. `function(g) g() end`) are
// not reachable by real promotion — the P2 analyzer marks a parameter call as
// ReasonUnknownCall (a parameter is an arbitrary value, possibly coroutine.yield),
// and the visitor conservatively rejects it by design. Real promotion of form A*
// would require P2 to relax its unknown-call discipline, which violates the P2
// design principle (per docs/design/p2-bridge/03-compilability-analysis.md §1).
// Form A* unit coverage lives in the jit package, verifying the Compile + Run
// path directly through a mock host (`compiler_pj5_call_test.go::TestPJ5_RunCallVoidPath`
// etc.), but the crescent e2e path is not reachable. The high-frequency real-world
// business form is a closure calling an outer known fn (form B*), and that path already works.

// TestPJ5_CallVoid_E2E_FormB2K_UpvalArgs: form B2K (GETUPVAL+LOADK+LOADK+
// CALL+RETURN void) real promotion — `local function take(a, b)...end;
// local function tick() take(10, 20) end`, 2 constant K args, 0 ret. On the Run side
// host.SetReg(callA+1, K1) + SetReg(callA+2, K2) loads them into the argument slots.
func TestPJ5_CallVoid_E2E_FormB2K_UpvalArgs(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local function take(a, b) sum = sum + a * b end
local function tick() take(10, 20) end
for i = 1, 30 do tick() end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum = 30 * (10 * 20) = 6000
	if got := value.AsNumber(value.Value(rets[0])); got != 6000 {
		t.Errorf("rets = %v, want 6000 (tick() × 30 each take(10,20) → sum += 200)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL 2 K 参形态 B2K 未真编译")
	}
	t.Logf("SpecCallVoidHits=%d", jit.SpecCallVoidHits())
}

// TestPJ5_CallVoid_E2E_FormB1K1R: form B1K1R (GETUPVAL+LOADK+MOVE+CALL+RETURN
// void) real promotion — `local function take(a, b)...end; local function tick(v) take(7, v) end`,
// 1 K + 1 reg arg, 0 ret.
func TestPJ5_CallVoid_E2E_FormB1K1R(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local function take(a, b) sum = sum + a * b end
local function tick(v) take(7, v) end
for i = 1, 30 do tick(i) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum = sum(7*i) i=1..30 = 7*sum(1..30) = 7*465 = 3255
	if got := value.AsNumber(value.Value(rets[0])); got != 3255 {
		t.Errorf("rets = %v, want 3255 (tick(i)×30 each take(7,i)→7*i)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL 1 K + 1 reg 参形态 B1K1R 未真编译")
	}
}

// TestPJ5_CallVoid_E2E_FormB1R1K: form B1R1K (GETUPVAL+MOVE+LOADK+CALL+RETURN
// void) real promotion — `local function take(a, b)...end; local function tick(v) take(v, 7) end`,
// 1 reg + 1 K arg, 0 ret.
func TestPJ5_CallVoid_E2E_FormB1R1K(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local function take(a, b) sum = sum + a * b end
local function tick(v) take(v, 7) end
for i = 1, 30 do tick(i) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum = sum(i*7) i=1..30 = 7*465 = 3255
	if got := value.AsNumber(value.Value(rets[0])); got != 3255 {
		t.Errorf("rets = %v, want 3255 (tick(i)×30 each take(i,7)→i*7)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL 1 reg + 1 K 参形态 B1R1K 未真编译")
	}
}

// TestPJ5_CallVoid_E2E_FormB2R: form B2R (GETUPVAL+MOVE+MOVE+CALL+RETURN
// void) real promotion — `local function take(a, b)...end; local function tick(u, v) take(u, v) end`,
// 2 reg args, 0 ret.
func TestPJ5_CallVoid_E2E_FormB2R(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local function take(a, b) sum = sum + a * b end
local function tick(u, v) take(u, v) end
for i = 1, 10 do tick(i, i+1) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum = sum(i*(i+1)) i=1..10 = 2+6+12+20+30+42+56+72+90+110 = 440
	if got := value.AsNumber(value.Value(rets[0])); got != 440 {
		t.Errorf("rets = %v, want 440 (tick(i,i+1)×10 each take(i,i+1)→i*(i+1))", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL 2 reg 参形态 B2R 未真编译")
	}
}

// TestPJ5_CallGetter_E2E_FormB1KR1: form B1KR1 (GETUPVAL+LOADK+CALL B=2 C=2+RETURN A=callA B=2+dead)
// real promotion — `local function take(x) return x*2 end; local function get() local y = take(7); return y end`,
// 1 K arg, 1 ret getter.
func TestPJ5_CallGetter_E2E_FormB1KR1(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take(x) return x * 2 end
local function get() local y = take(7); return y end
local s = 0
for i = 1, 30 do s = s + get() end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// get() × 30 each take(7)→14, sum=420
	if got := value.AsNumber(value.Value(rets[0])); got != 420 {
		t.Errorf("rets = %v, want 420 (get()×30 each take(7)→14)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL getter 1 K 参 1 返形态 B1KR1 未真编译")
	}
	t.Logf("SpecCallVoidHits=%d", jit.SpecCallVoidHits())
}

// TestPJ5_CallGetter_E2E_FormB1RR1: form B1RR1 (GETUPVAL+MOVE+CALL B=2 C=2+RETURN A=callA B=2+dead)
// real promotion — `local function take(x) return x*2 end; local function get(v) local y = take(v); return y end`,
// 1 reg arg, 1 ret getter.
func TestPJ5_CallGetter_E2E_FormB1RR1(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take(x) return x * 2 end
local function get(v) local y = take(v); return y end
local s = 0
for i = 1, 30 do s = s + get(i) end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(take(i)) i=1..30 = sum(2i) = 2*465 = 930
	if got := value.AsNumber(value.Value(rets[0])); got != 930 {
		t.Errorf("rets = %v, want 930 (get(i)×30 each take(i)→2i)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL getter 1 reg 参 1 返形态 B1RR1 未真编译")
	}
}

// TestPJ5_CallGetter_E2E_FormB2KR1: form B2KR1 (GETUPVAL+LOADK+LOADK+CALL B=3 C=2+RETURN A=callA B=2+dead)
// real promotion — `local function take(a, b) return a+b end; local function get() local y = take(7, 9); return y end`,
// 2 K args, 1 ret getter.
func TestPJ5_CallGetter_E2E_FormB2KR1(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take(a, b) return a + b end
local function get() local y = take(7, 9); return y end
local s = 0
for i = 1, 30 do s = s + get() end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// get() × 30 each take(7,9)→16, sum = 30*16 = 480
	if got := value.AsNumber(value.Value(rets[0])); got != 480 {
		t.Errorf("rets = %v, want 480 (get()×30 each take(7,9)→16)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL getter 2 K 参 1 返形态 B2KR1 未真编译")
	}
	t.Logf("SpecCallVoidHits=%d", jit.SpecCallVoidHits())
}

// TestPJ5_CallGetter_E2E_FormB2RR1: form B2RR1 (GETUPVAL+MOVE+MOVE+CALL B=3 C=2+RETURN A=callA B=2+dead)
// real promotion — 2 reg args, 1 ret getter.
func TestPJ5_CallGetter_E2E_FormB2RR1(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take(a, b) return a + b end
local function get(u, v) local y = take(u, v); return y end
local s = 0
for i = 1, 30 do s = s + get(i, i+1) end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(take(i,i+1)) i=1..30 = sum(2i+1) = 2*465 + 30 = 960
	if got := value.AsNumber(value.Value(rets[0])); got != 960 {
		t.Errorf("rets = %v, want 960 (get(i,i+1)×30 each take(i,i+1)→2i+1)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL getter 2 reg 参 1 返形态 B2RR1 未真编译")
	}
}

// TestPJ5_CallGetter_E2E_FormB1K1RR1: form B1K1RR1 (K+R 2 args, 1 ret getter)
func TestPJ5_CallGetter_E2E_FormB1K1RR1(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take(a, b) return a + b end
local function get(v) local y = take(7, v); return y end
local s = 0
for i = 1, 30 do s = s + get(i) end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(7+i) i=1..30 = 30*7 + 465 = 675
	if got := value.AsNumber(value.Value(rets[0])); got != 675 {
		t.Errorf("rets = %v, want 675 (get(i)×30 each take(7,i)→7+i)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL getter K+R 形态 B1K1RR1 未真编译")
	}
}

// TestPJ5_CallVoid_E2E_FormB3K: form B3K (GETUPVAL+LOADK×3+CALL B=4 C=1+RETURN void)
// real promotion — 3 K args, 0 ret setter, length 6.
func TestPJ5_CallVoid_E2E_FormB3K(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local function take(a, b, c) sum = sum + a + b + c end
local function tick() take(1, 2, 3) end
for i = 1, 30 do tick() end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum = 30 * (1+2+3) = 180
	if got := value.AsNumber(value.Value(rets[0])); got != 180 {
		t.Errorf("rets = %v, want 180 (tick()×30 each take(1,2,3))", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL 3 K 参形态 B3K 未真编译")
	}
}

// TestPJ5_CallVoid_E2E_FormB3R: form B3R (3 reg args, 0 ret setter, length 6)
func TestPJ5_CallVoid_E2E_FormB3R(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local function take(a, b, c) sum = sum + a + b + c end
local function tick(u, v, w) take(u, v, w) end
for i = 1, 10 do tick(i, i+1, i+2) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(i + (i+1) + (i+2)) i=1..10 = sum(3i+3) = 3*55 + 30 = 195
	if got := value.AsNumber(value.Value(rets[0])); got != 195 {
		t.Errorf("rets = %v, want 195 (tick(i,i+1,i+2)×10 each take(i,i+1,i+2)→3i+3)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL 3 reg 参形态 B3R 未真编译")
	}
}

// TestPJ5_CallGetter_E2E_FormB3RR1: form B3RR1 (3 reg args, 1 ret getter, length 7)
func TestPJ5_CallGetter_E2E_FormB3RR1(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take(a, b, c) return a + b + c end
local function get(u, v, w) local y = take(u, v, w); return y end
local s = 0
for i = 1, 10 do s = s + get(i, i+1, i+2) end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(i+(i+1)+(i+2)) i=1..10 = 195
	if got := value.AsNumber(value.Value(rets[0])); got != 195 {
		t.Errorf("rets = %v, want 195 (get(i,i+1,i+2)×10 each take→3i+3)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL getter 3 reg 参形态 B3RR1 未真编译")
	}
}

// TestPJ5_CallMultiRet_E2E_FormB2RetN2: form with 0 args, N=2 return values getter
// `local a, b = take(); return a, b` form — luac emits GETUPVAL+CALL B=1 C=3 + MOVE×2 + RETURN A=callA+2 B=3
// On the Run side, after CallBaseline, 2 MOVE copies keep it byte-equal.
func TestPJ5_CallMultiRet_E2E_FormB2RetN2(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take() return 10, 20 end
local function get() local a, b = take(); return a, b end
local s = 0
for i = 1, 30 do
  local a, b = get()
  s = s + a + b
end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// get() × 30 each (10+20)=30, sum=900
	if got := value.AsNumber(value.Value(rets[0])); got != 900 {
		t.Errorf("rets = %v, want 900 (get()×30 each take()→(10,20))", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL N=2 返值 getter 形态 B2RetN2 未真编译")
	}
	t.Logf("SpecCallVoidHits=%d", jit.SpecCallVoidHits())
}

// TestPJ5_CallMultiRet_E2E_FormB3RetN3: form with 0 args, N=3 return values getter
// `local a, b, c = take(); return a, b, c` form — luac emits GETUPVAL+CALL B=1 C=4 + MOVE×3 + RETURN A=callA+3 B=4
func TestPJ5_CallMultiRet_E2E_FormB3RetN3(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take() return 1, 2, 3 end
local function get() local a, b, c = take(); return a, b, c end
local s = 0
for i = 1, 30 do
  local a, b, c = get()
  s = s + a + b + c
end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// get() × 30 each (1+2+3)=6, sum=180
	if got := value.AsNumber(value.Value(rets[0])); got != 180 {
		t.Errorf("rets = %v, want 180 (get()×30 each take()→(1,2,3))", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL N=3 返值 getter 形态 B3RetN3 未真编译")
	}
}

// TestPJ5_CallVoid_E2E_FormB4R: 4 reg args setter, length 7
func TestPJ5_CallVoid_E2E_FormB4R(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local function take(a, b, c, d) sum = sum + a + b + c + d end
local function tick(u, v, w, x) take(u, v, w, x) end
for i = 1, 10 do tick(i, i+1, i+2, i+3) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(i+(i+1)+(i+2)+(i+3)) i=1..10 = sum(4i+6) = 4*55 + 60 = 280
	if got := value.AsNumber(value.Value(rets[0])); got != 280 {
		t.Errorf("rets = %v, want 280 (tick(i,i+1,i+2,i+3)×10 each take→4i+6)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL 4 reg 参形态 B4R 未真编译")
	}
}

// TestPJ5_CallGetter_E2E_FormB4RR1: 4 reg args, 1 ret getter, length 8
func TestPJ5_CallGetter_E2E_FormB4RR1(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take(a, b, c, d) return a + b + c + d end
local function get(u, v, w, x) local y = take(u, v, w, x); return y end
local s = 0
for i = 1, 10 do s = s + get(i, i+1, i+2, i+3) end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(4i+6) i=1..10 = 280
	if got := value.AsNumber(value.Value(rets[0])); got != 280 {
		t.Errorf("rets = %v, want 280 (get(i,i+1,i+2,i+3)×10 each take→4i+6)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL getter 4 reg 参形态 B4RR1 未真编译")
	}
}

// TestPJ5_CallMultiRet_E2E_FormB1KRetN2: 1 K arg, N=2 return-value form (length 7)
// `local function take(k) return k, k*2 end; local function get() local a,b=take(7); return a,b end`
func TestPJ5_CallMultiRet_E2E_FormB1KRetN2(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take(k) return k, k*2 end
local function get() local a, b = take(7); return a, b end
local s = 0
for i = 1, 30 do
  local a, b = get()
  s = s + a + b
end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// get() × 30 each take(7)→(7, 14), sum=30*21=630
	if got := value.AsNumber(value.Value(rets[0])); got != 630 {
		t.Errorf("rets = %v, want 630 (get()×30 each take(7)→(7,14))", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL 1 K 参 N=2 返值形态 B1KRetN2 未真编译")
	}
	t.Logf("SpecCallVoidHits=%d", jit.SpecCallVoidHits())
}

// TestPJ5_CallMultiRet_E2E_FormB1RRetN2: 1 reg arg, N=2 return-value form (length 7)
func TestPJ5_CallMultiRet_E2E_FormB1RRetN2(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take(v) return v, v*2 end
local function get(v) local a, b = take(v); return a, b end
local s = 0
for i = 1, 30 do
  local a, b = get(i)
  s = s + a + b
end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(i + 2i) i=1..30 = 3*sum(1..30) = 3*465 = 1395
	if got := value.AsNumber(value.Value(rets[0])); got != 1395 {
		t.Errorf("rets = %v, want 1395 (get(i)×30 each take(i)→(i,2i))", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL 1 reg 参 N=2 返值形态 B1RRetN2 未真编译")
	}
}

// TestPJ5_CallVoid_E2E_FormB5R: 5 reg args setter, length 8
func TestPJ5_CallVoid_E2E_FormB5R(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local function take(a, b, c, d, e) sum = sum + a + b + c + d + e end
local function tick(u, v, w, x, y) take(u, v, w, x, y) end
for i = 1, 10 do tick(i, i+1, i+2, i+3, i+4) end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(i+(i+1)+(i+2)+(i+3)+(i+4)) i=1..10 = sum(5i+10) = 5*55 + 100 = 375
	if got := value.AsNumber(value.Value(rets[0])); got != 375 {
		t.Errorf("rets = %v, want 375 (tick(...)×10 each take→5i+10)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL 5 reg 参形态 B5R 未真编译")
	}
}

// TestPJ5_CallVoid_E2E_FormB5K: 5 K args setter, length 8
func TestPJ5_CallVoid_E2E_FormB5K(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local function take(a, b, c, d, e) sum = sum + a + b + c + d + e end
local function tick() take(1, 2, 3, 4, 5) end
for i = 1, 30 do tick() end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum = 30 * (1+2+3+4+5) = 30*15 = 450
	if got := value.AsNumber(value.Value(rets[0])); got != 450 {
		t.Errorf("rets = %v, want 450 (tick()×30 each take(1..5)=15)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL 5 K 参形态 B5K 未真编译")
	}
}

// TestPJ5_CallGetter_E2E_FormB5RR1: 5 reg args, 1 ret getter, length 9
func TestPJ5_CallGetter_E2E_FormB5RR1(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take(a, b, c, d, e) return a + b + c + d + e end
local function get(u, v, w, x, y) local z = take(u, v, w, x, y); return z end
local s = 0
for i = 1, 10 do s = s + get(i, i+1, i+2, i+3, i+4) end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(5i+10) i=1..10 = 5*55 + 100 = 375
	if got := value.AsNumber(value.Value(rets[0])); got != 375 {
		t.Errorf("rets = %v, want 375 (get(...)×10 each take→5i+10)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL getter 5 reg 参形态 B5RR1 未真编译")
	}
}

// TestPJ5_CallVoid_E2E_FormB6K: 6 K args setter, length 10
func TestPJ5_CallVoid_E2E_FormB6K(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local function take(a, b, c, d, e, f) sum = sum + a + b + c + d + e + f end
local function tick() take(1, 2, 3, 4, 5, 6) end
for i = 1, 30 do tick() end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum = 30 * (1+2+3+4+5+6) = 30*21 = 630
	if got := value.AsNumber(value.Value(rets[0])); got != 630 {
		t.Errorf("rets = %v, want 630 (tick()×30 each take(1..6)=21)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL 6 K 参形态 B6K 未真编译")
	}
}

// TestPJ5_CallGetter_E2E_FormB6RR1: 6 reg args, 1 ret getter, length 10
func TestPJ5_CallGetter_E2E_FormB6RR1(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take(a, b, c, d, e, f) return a + b + c + d + e + f end
local function get(p, q, r, s, t, u) local z = take(p, q, r, s, t, u); return z end
local total = 0
for i = 1, 10 do total = total + get(i, i+1, i+2, i+3, i+4, i+5) end
return total`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(6i+15) i=1..10 = 6*55 + 150 = 480
	if got := value.AsNumber(value.Value(rets[0])); got != 480 {
		t.Errorf("rets = %v, want 480 (get(...)×10 each take→6i+15)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL getter 6 reg 参形态 B6RR1 未真编译")
	}
}

// TestPJ5_CallMultiRet_E2E_FormB1KRetN3: 1 K arg, N=3 return-value form (length 8)
func TestPJ5_CallMultiRet_E2E_FormB1KRetN3(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take(k) return k, k*2, k*3 end
local function get() local a, b, c = take(7); return a, b, c end
local s = 0
for i = 1, 30 do
  local a, b, c = get()
  s = s + a + b + c
end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// get() × 30 each take(7)→(7,14,21), sum=30*42=1260
	if got := value.AsNumber(value.Value(rets[0])); got != 1260 {
		t.Errorf("rets = %v, want 1260 (get()×30 each take(7)→(7,14,21))", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL 1 K 参 N=3 返值形态 B1KRetN3 未真编译")
	}
}

// TestPJ5_CallMultiRet_E2E_FormB1RRetN3: 1 reg arg, N=3 return-value form (length 8)
func TestPJ5_CallMultiRet_E2E_FormB1RRetN3(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take(v) return v, v*2, v*3 end
local function get(v) local a, b, c = take(v); return a, b, c end
local s = 0
for i = 1, 30 do
  local a, b, c = get(i)
  s = s + a + b + c
end
return s`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(i+2i+3i) i=1..30 = 6*sum(1..30) = 6*465 = 2790
	if got := value.AsNumber(value.Value(rets[0])); got != 2790 {
		t.Errorf("rets = %v, want 2790 (get(i)×30 each take(i)→(i,2i,3i))", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL 1 reg 参 N=3 返值形态 B1RRetN3 未真编译")
	}
}

// TestPJ5_CallVoid_E2E_FormB7K: 7 K args setter, length 10
func TestPJ5_CallVoid_E2E_FormB7K(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local sum = 0
local function take(a, b, c, d, e, f, g) sum = sum + a + b + c + d + e + f + g end
local function tick() take(1, 2, 3, 4, 5, 6, 7) end
for i = 1, 30 do tick() end
return sum`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum = 30 * (1+2+3+4+5+6+7) = 30*28 = 840
	if got := value.AsNumber(value.Value(rets[0])); got != 840 {
		t.Errorf("rets = %v, want 840 (tick()×30 each take(1..7)=28)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL 7 K 参形态 B7K 未真编译")
	}
}

// TestPJ5_CallGetter_E2E_FormB7RR1: 7 reg args, 1 ret getter, length 11
func TestPJ5_CallGetter_E2E_FormB7RR1(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function take(a, b, c, d, e, f, g) return a + b + c + d + e + f + g end
local function get(a, b, c, d, e, f, g) local z = take(a, b, c, d, e, f, g); return z end
local total = 0
for i = 1, 10 do total = total + get(i, i+1, i+2, i+3, i+4, i+5, i+6) end
return total`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// sum(7i+21) i=1..10 = 7*55 + 210 = 595
	if got := value.AsNumber(value.Value(rets[0])); got != 595 {
		t.Errorf("rets = %v, want 595 (get(...)×10 each take→7i+21)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL getter 7 reg 参形态 B7RR1 未真编译")
	}
}
