//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"testing"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_pj5_call_e2e_test.go —— PJ5 CALL void 真升层 e2e:
// `local function noop()...end; local function invoker() noop() end`(形态
// B,GETUPVAL+CALL+RETURN void)经 P4 升层后 Run prelude 路径调
// host.GetUpval + SetReg + CallBaseline 完成 baseline doCall(byte-equal P1
// doCall 分派)+ DoReturn 弹帧。
//
// **PJ5 真接入主路径** 的物理证据(从 PJ7 简化形态扩到调用族 inline):
// P4 首次接入 CALL opcode + host.CallBaseline 跨 Go 端边界。**简化形态仅
// 0 参 0 返**(MOVE/GETUPVAL+CALL+RETURN void)+ baseline doCall
// (host/crescent/__call/gibbous 全形态同步跑完),不走 P3 R3 indirect 哨兵。
//
// **关联 P2 analyzer 扩展**:本测试也验证 P2 scope-aware AnalyzeProto
// 跨 Proto 边界传递 outer localFnAsts(承同批 commit 扩展)— 否则 invoker
// 内调 noop 会被标 ReasonUnknownCall,Compilable=NotCompilable,P4 路径
// 不触达。

// TestPJ5_CallVoid_E2E_FormB_Upval:形态 B(GETUPVAL+CALL+RETURN void)
// 真升层 — `local function noop()...end; local function invoker() noop() end`,
// 重复调 invoker 让 P4 升层后真走 PJ5 CALL void 模板。
//
// 关键探针:**SpecCallVoidHits**——只有 Compile 命中 isCallVoid=true 时
// 才 ++。若 P4 升层未触达或形态识别失败,SpecCallVoidHits 永 0(测试失败)。
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

// TestPJ5_CallVoid_E2E_FormB1K_UpvalArg:形态 B1K(GETUPVAL+LOADK+CALL+RETURN
// void)真升层 — `local function take(x)...end; local function tick() take(42) end`,
// 1 K 常量参 0 返。LOADK 在 mmap 段是 dummy,Run 端 host.SetReg(callA+1, K)
// 装到参数槽。
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

// TestPJ5_CallVoid_E2E_FormB1R_UpvalArg:形态 B1R(GETUPVAL+MOVE+CALL+RETURN
// void)真升层 — `local function take(x)...end; local function tick(v) take(v) end`,
// 1 reg 参 0 返。MOVE 在 mmap 段是 dummy,Run 端 host.GetReg(srcReg) +
// SetReg(callA+1, val)装到参数槽。
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

// TestPJ5_CallVoid_E2E_FormBR1_GetterUpval:形态 BR1(GETUPVAL+CALL+RETURN+
// dead RETURN getter)真升层 — `local function f() return 42 end;
// local function get() local x = f(); return x end`,0 参 1 返。被调返回
// 值落 R(callA),Run 端 host.DoReturn(retA=callA, retB=2)返该值。
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

// **注**:形态 A* parameter-callee 形态(如 `function(g) g() end`)真升层
// 不可达——P2 analyzer 把 parameter call 标 ReasonUnknownCall(parameter
// 是任意 value,可能是 coroutine.yield),visitor 设计上保守拒。形态 A* 真
// 升层需要 P2 放宽 unknown call 纪律,这违反 P2 设计原则(承
// docs/design/p2-bridge/03-compilability-analysis.md §1)。形态 A* 单测
// 覆盖在 jit 包内通过 mock host 直接验 Compile + Run 路径(`compiler_pj5_call_test.go::
// TestPJ5_RunCallVoidPath` 等),但 crescent e2e 路径不可达。real-world
// 业务高频形态是 closure 调外层 known fn(形态 B*),那条路径已通。

// TestPJ5_CallVoid_E2E_FormB2K_UpvalArgs:形态 B2K(GETUPVAL+LOADK+LOADK+
// CALL+RETURN void)真升层 — `local function take(a, b)...end;
// local function tick() take(10, 20) end`,2 K 常量参 0 返。Run 端
// host.SetReg(callA+1, K1) + SetReg(callA+2, K2) 装到参数槽。
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

// TestPJ5_CallVoid_E2E_FormB1K1R:形态 B1K1R(GETUPVAL+LOADK+MOVE+CALL+RETURN
// void)真升层 — `local function take(a, b)...end; local function tick(v) take(7, v) end`,
// 1 K + 1 reg 参 0 返。
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

// TestPJ5_CallVoid_E2E_FormB1R1K:形态 B1R1K(GETUPVAL+MOVE+LOADK+CALL+RETURN
// void)真升层 — `local function take(a, b)...end; local function tick(v) take(v, 7) end`,
// 1 reg + 1 K 参 0 返。
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

// TestPJ5_CallVoid_E2E_FormB2R:形态 B2R(GETUPVAL+MOVE+MOVE+CALL+RETURN
// void)真升层 — `local function take(a, b)...end; local function tick(u, v) take(u, v) end`,
// 2 reg 参 0 返。
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

// TestPJ5_CallGetter_E2E_FormB1KR1:形态 B1KR1(GETUPVAL+LOADK+CALL B=2 C=2+RETURN A=callA B=2+dead)
// 真升层 — `local function take(x) return x*2 end; local function get() local y = take(7); return y end`,
// 1 K 参 1 返 getter。
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
	// get() × 30 each take(7)→14,sum=420
	if got := value.AsNumber(value.Value(rets[0])); got != 420 {
		t.Errorf("rets = %v, want 420 (get()×30 each take(7)→14)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL getter 1 K 参 1 返形态 B1KR1 未真编译")
	}
	t.Logf("SpecCallVoidHits=%d", jit.SpecCallVoidHits())
}

// TestPJ5_CallGetter_E2E_FormB1RR1:形态 B1RR1(GETUPVAL+MOVE+CALL B=2 C=2+RETURN A=callA B=2+dead)
// 真升层 — `local function take(x) return x*2 end; local function get(v) local y = take(v); return y end`,
// 1 reg 参 1 返 getter。
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

// TestPJ5_CallGetter_E2E_FormB2KR1:形态 B2KR1(GETUPVAL+LOADK+LOADK+CALL B=3 C=2+RETURN A=callA B=2+dead)
// 真升层 — `local function take(a, b) return a+b end; local function get() local y = take(7, 9); return y end`,
// 2 K 参 1 返 getter。
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
	// get() × 30 each take(7,9)→16,sum = 30*16 = 480
	if got := value.AsNumber(value.Value(rets[0])); got != 480 {
		t.Errorf("rets = %v, want 480 (get()×30 each take(7,9)→16)", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL getter 2 K 参 1 返形态 B2KR1 未真编译")
	}
	t.Logf("SpecCallVoidHits=%d", jit.SpecCallVoidHits())
}

// TestPJ5_CallGetter_E2E_FormB2RR1:形态 B2RR1(GETUPVAL+MOVE+MOVE+CALL B=3 C=2+RETURN A=callA B=2+dead)
// 真升层 — 2 reg 参 1 返 getter。
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

// TestPJ5_CallGetter_E2E_FormB1K1RR1:形态 B1K1RR1(K+R 2 参 1 返 getter)
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

// TestPJ5_CallVoid_E2E_FormB3K:形态 B3K(GETUPVAL+LOADK×3+CALL B=4 C=1+RETURN void)
// 真升层 — 3 K 参 0 返 setter,长度 6。
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

// TestPJ5_CallVoid_E2E_FormB3R:形态 B3R(3 reg 参 0 返 setter,长度 6)
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

// TestPJ5_CallGetter_E2E_FormB3RR1:形态 B3RR1(3 reg 参 1 返 getter,长度 7)
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

// TestPJ5_CallMultiRet_E2E_FormB2RetN2:形态 0 参 N=2 返值 getter
// `local a, b = take(); return a, b` 形态 — luac 编 GETUPVAL+CALL B=1 C=3 + MOVE×2 + RETURN A=callA+2 B=3
// Run 端 CallBaseline 后做 2 个 MOVE 拷贝保留 byte-equal。
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
	// get() × 30 each (10+20)=30,sum=900
	if got := value.AsNumber(value.Value(rets[0])); got != 900 {
		t.Errorf("rets = %v, want 900 (get()×30 each take()→(10,20))", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL N=2 返值 getter 形态 B2RetN2 未真编译")
	}
	t.Logf("SpecCallVoidHits=%d", jit.SpecCallVoidHits())
}

// TestPJ5_CallMultiRet_E2E_FormB3RetN3:形态 0 参 N=3 返值 getter
// `local a, b, c = take(); return a, b, c` 形态 — luac 编 GETUPVAL+CALL B=1 C=4 + MOVE×3 + RETURN A=callA+3 B=4
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
	// get() × 30 each (1+2+3)=6,sum=180
	if got := value.AsNumber(value.Value(rets[0])); got != 180 {
		t.Errorf("rets = %v, want 180 (get()×30 each take()→(1,2,3))", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL N=3 返值 getter 形态 B3RetN3 未真编译")
	}
}

// TestPJ5_CallVoid_E2E_FormB4R:4 reg 参 setter,长度 7
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

// TestPJ5_CallGetter_E2E_FormB4RR1:4 reg 参 1 返 getter,长度 8
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

// TestPJ5_CallMultiRet_E2E_FormB1KRetN2:1 K 参 N=2 返值形态(长度 7)
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
	// get() × 30 each take(7)→(7, 14),sum=30*21=630
	if got := value.AsNumber(value.Value(rets[0])); got != 630 {
		t.Errorf("rets = %v, want 630 (get()×30 each take(7)→(7,14))", got)
	}
	if jit.SpecCallVoidHits() == 0 {
		t.Errorf("SpecCallVoidHits = 0,PJ5 CALL 1 K 参 N=2 返值形态 B1KRetN2 未真编译")
	}
	t.Logf("SpecCallVoidHits=%d", jit.SpecCallVoidHits())
}

// TestPJ5_CallMultiRet_E2E_FormB1RRetN2:1 reg 参 N=2 返值形态(长度 7)
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

// TestPJ5_CallVoid_E2E_FormB5R:5 reg 参 setter,长度 8
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

// TestPJ5_CallVoid_E2E_FormB5K:5 K 参 setter,长度 8
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
