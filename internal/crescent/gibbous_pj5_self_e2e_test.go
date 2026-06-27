//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"testing"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_pj5_self_e2e_test.go —— PJ5 SELF method call inline 形态真升层 e2e:
// `obj:method(args)` 经 P4 升层后 Run prelude 路径先调 host.Self(byte-equal
// P1 SELF + icGetTable + __index 元方法链)装 method 入 R(callA) + self
// R(callA+1),然后调 host.CallBaseline / TailCall 完成 baseline doCall。
//
// **PJ5 SELF 真接入主路径** 的物理证据:之前 P4 PJ5 inline 形态只接受 callee 经
// MOVE/GETUPVAL 装载,SELF method call(obj 方法分派)路径走 P2 ReasonUnknownCall
// 守门拒。本批拆 visitMethodCallExpr 标 ReasonSelfCall 占位位 + P4 端
// recheckCompilabilityRuntime 撤位 + SupportsAllOpcodes(经 analyzeSelfCallForm)
// 真守门:obj:method() 形态命中 PJ5 SELF inline 模板。
//
// **关键探针**:SpecSelfCallHits —— 只有 Compile 命中 isSelfCall=true 时才 ++。

// TestPJ5_SelfCall_E2E_M0_VoidCall 形态 M0 0 参 void:
// `local _ = function(o) o:m() end`(MOVE+SELF+CALL+RETURN void 长度 4)。
//
// 因主 chunk 必须含本闭包,外层 closure 闭包注册 + force-all 升层 ⇒
// inner 形态 M0 命中 isSelfCall=true。
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

// TestPJ5_SelfCall_E2E_U0_VoidCall 形态 U0 0 参 void(GETUPVAL recv):
// closure 内 `o:m() end`,o 是外层 local 通过 upval 访问。
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

// TestPJ5_SelfCall_E2E_M1K_VoidArg 形态 M1K 1 K 参 void:
// `caller(o)` → `t:m(42)`。
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

// TestPJ5_SelfCall_E2E_M1R_VoidArg 形态 M1R 1 reg 参 void:
// `caller(o, x)` → `t:m(x)`。
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

// TestPJ5_SelfCall_E2E_TailCall_M0 形态 TM0 0 参 TAILCALL:
// `return t:m()` luac 编 SELF+TAILCALL+RETURN(B=0)。
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

// TestPJ5_SelfCall_E2E_GetterCall_M0 形态 MR1 0 参 1 返 CALL getter:
// `local r = t:m()` 形态(luac 实测见 obj:m() 在 local 赋值上下文编 CALL B=2 C=2)。
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
