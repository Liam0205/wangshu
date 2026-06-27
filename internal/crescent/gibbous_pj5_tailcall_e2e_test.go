//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"testing"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_pj5_tailcall_e2e_test.go —— PJ5 TAILCALL 真升层 e2e:
// `local function f()...end; local function bounce() return f() end`(形态
// TB0,GETUPVAL+TAILCALL+RETURN B=0+RETURN B=1)经 P4 升层后 Run prelude
// 路径调 host.GetUpval + SetReg + host.TailCall(三态分支)完成 baseline
// 尾调用(byte-equal P1 doTailCall 分派)。
//
// **PJ5 TAILCALL 真接入主路径** 的物理证据(从 CALL void 扩到尾调用形态):
// luac stmtReturn 把单 CallExpr 作 return 唯一表达式翻成 TAILCALL + 尾随
// dead RETURN B=0 + 隐式 RETURN B=1。Run 端 host.TailCall 三态分支:
//   - 0 = Lua 尾完成 → 跳过 DoReturn(本帧已弹)
//   - 1 = ERR
//   - 2 = host 尾完成 → 落 dead RETURN to-top 走 DoReturn
//
// **关联 P2 analyzer scope-aware 扩展**:与 CALL void e2e 共用机制 — invoker
// 内调外层 known local fn 需 P2 跨 Proto 传递 outer localFnAsts(承
// internal/bridge/analyzer.go::AnalyzeProtoWithOuter)。

// TestPJ5_TailCall_E2E_FormTB0_Upval:形态 TB0(GETUPVAL+TAILCALL+RETURN
// B=0+RETURN B=1)真升层 — `local function f() return 42 end;
// local function bounce() return f() end`,0 参 1 返(尾调用透传 callee
// 返回值)。重复调 bounce 让 P4 升层后真走 PJ5 TAILCALL 模板。
//
// 关键探针:**SpecTailCallHits** —— 只有 Compile 命中 isTailCall=true 时
// 才 ++。若 P4 升层未触达或形态识别失败,SpecTailCallHits 永 0(测试失败)。
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

// TestPJ5_TailCall_E2E_FormTB1K_UpvalArg:形态 TB1K(GETUPVAL+LOADK+TAILCALL+
// RETURN B=0+RETURN B=1)真升层 — `local function take(x) return x*2 end;
// local function bounce() return take(7) end`,1 K 常量参 1 返。LOADK 在
// mmap 段是 dummy,Run 端 host.SetReg(callA+1, K) 装到参数槽。
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
	// bounce() 50 次 each take(7) → 14 → s += 14 → 50*14 = 700
	if got := value.AsNumber(value.Value(rets[0])); got != 700 {
		t.Errorf("rets = %v, want 700 (bounce() × 50 each take(7)→14)", got)
	}
	if jit.SpecTailCallHits() == 0 {
		t.Errorf("SpecTailCallHits = 0,PJ5 TAILCALL 1 K 参模板未真编译")
	}
	t.Logf("SpecTailCallHits=%d", jit.SpecTailCallHits())
}

// TestPJ5_TailCall_E2E_FormTB1R_UpvalArg:形态 TB1R(GETUPVAL+MOVE+TAILCALL+
// RETURN B=0+RETURN B=1)真升层 — `local function take(x) return x+1 end;
// local function bounce(v) return take(v) end`,1 reg 参 1 返。MOVE 在
// mmap 段是 dummy,Run 端 host.GetReg(srcReg) + SetReg(callA+1, val) 装。
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
	// bounce(i) 50 次 each take(i)→i+1,sum(i+1) i=1..50 = sum(2..51) = 51*52/2-1 = 1325
	const want = float64((1+50)*50/2 + 50)
	if got := value.AsNumber(value.Value(rets[0])); got != want {
		t.Errorf("rets = %v, want %v (bounce(i)→take(i)→i+1)", got, want)
	}
	if jit.SpecTailCallHits() == 0 {
		t.Errorf("SpecTailCallHits = 0,PJ5 TAILCALL 1 reg 参模板未真编译")
	}
	t.Logf("SpecTailCallHits=%d", jit.SpecTailCallHits())
}

// TestPJ5_TailCall_E2E_FormTB2K_UpvalArgs:形态 TB2K(GETUPVAL+LOADK+LOADK+
// TAILCALL+RETURN B=0+RETURN B=1)真升层 — `local function f(a, b) return a+b end;
// local function bounce() return f(10, 20) end`,2 K 参 1 返。
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
	// bounce() 30 次 each f(10,20)→30,s = 30*30 = 900
	if got := value.AsNumber(value.Value(rets[0])); got != 900 {
		t.Errorf("rets = %v, want 900 (bounce() × 30 each f(10,20)→30)", got)
	}
	if jit.SpecTailCallHits() == 0 {
		t.Errorf("SpecTailCallHits = 0,PJ5 TAILCALL 2 K 参形态未真编译")
	}
	t.Logf("SpecTailCallHits=%d", jit.SpecTailCallHits())
}

// TestPJ5_TailCall_E2E_FormTB1K1R:形态 TB1K1R(K+R 2 参 tail)— `bounce(v) return f(7, v)`
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

// TestPJ5_TailCall_E2E_FormTB1R1K:形态 TB1R1K(R+K 2 参 tail)— `bounce(v) return f(v, 7)`
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
	// s = sum(i+7) = 675(同 TB1K1R)
	if got := value.AsNumber(value.Value(rets[0])); got != 675 {
		t.Errorf("rets = %v, want 675 (bounce(i)×30 each f(i,7)→i+7)", got)
	}
	if jit.SpecTailCallHits() == 0 {
		t.Errorf("SpecTailCallHits = 0,PJ5 TAILCALL 1 reg + 1 K 参形态 TB1R1K 未真编译")
	}
}

// TestPJ5_TailCall_E2E_FormTB2R:形态 TB2R(R+R 2 参 tail)— `bounce(u, v) return f(u, v)`
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

// TestPJ5_TailCall_E2E_FormTB3K:形态 TB3K(3 K 参 tail,长度 7)
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

// TestPJ5_TailCall_E2E_FormTB3R:形态 TB3R(3 reg 参 tail,长度 7)
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

// TestPJ5_TailCall_E2E_FormTB4R:形态 TB4R(4 reg 参 tail,长度 8)
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

// TestPJ5_TailCall_E2E_FormTB5R:形态 TB5R(5 reg 参 tail,长度 9)
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

// **注**:形态 TA* parameter-callee 形态(如 `function(g) return g() end`)真升层
// 不可达 — P2 analyzer 把 parameter call 标 ReasonUnknownCall(parameter
// 可能是 coroutine.yield),visitor 设计保守拒。形态 TA* 单测覆盖在 jit 包
// 内通过 mock host 直接验 Compile + Run 路径(`compiler_pj5_tailcall_test.go::
// TestPJ5_RunTailCallPath` 等),crescent e2e 路径不可达。real-world 业务
// 高频形态是 closure 调外层 known fn(形态 TB*),那条路径已通。
