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
