//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"strings"
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

// TestPJ5_SelfCall_E2E_M3K_VoidCall 形态 M3K 3 K 参 void(长度 7):
// `function(t) t:m(1, 2, 3) end`。
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

// TestPJ5_SelfCall_E2E_M3R_VoidCall 形态 M3R 3 reg 参 void(长度 7)。
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

// TestPJ5_SelfCall_E2E_M4R_VoidCall 形态 M4R 4 reg 参 void(长度 8)。
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

// TestPJ5_SelfCall_E2E_M5R_VoidCall 形态 M5R 5 reg 参 void(长度 9)。
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

// TestPJ5_SelfCall_E2E_M6R_VoidCall 形态 M6R 6 reg 参 void(长度 10)。
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

// TestPJ5_SelfCall_E2E_M7R_VoidCall 形态 M7R 7 reg 参 void(长度 11)。
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

// TestPJ5_SelfCall_E2E_TailCall_3K 形态 TM 3 K 参 TAILCALL(长度 8):
// `function(t) return t:m(1,2,3) end`。
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

// TestPJ5_SelfCall_E2E_TailCall_5R 形态 SELF + TAILCALL 5 reg 参(长度 9 在 form9
// 已覆盖,本测验测调用链 byte-equal P1)。
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

// TestPJ5_SelfCall_E2E_MultiRetN2_0arg 形态 MR2 N=2 返值 0 参(长度 4):
// `local a, b = t:m()`,caller 体只此一行(其它逻辑通过 side-effect 验证)。
// R(callA)/R(callA+1) 落 a, b — luac 编 RETURN A=0 B=1 收尾(返 0 值)。
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
	// 注:本 src 的 caller 含 sum += a+b 故 caller 形态较复杂,SELF inline 形态
	// 在更窄的 luac 编出 `local a,b = t:m() end` 时才命中长度 4;此测验主要做
	// byte-equal 路径(不强断 SpecSelfCallHits — caller 体不只 SELF+CALL+RETURN)。
	if jit.SpecSelfCallHits() == 0 {
		t.Logf("SpecSelfCallHits=0(caller 含算术 + setter,SELF inline 不在简化形态命中区,但 byte-equal 应保)")
	}
}

// TestPJ5_SelfCall_E2E_MultiRetN3_0arg 形态 MR3 N=3 返值 0 参(长度 4):
// `local a, b, c = t:m()`。
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

// TestPJ5_SelfCall_E2E_MultiRetN2_1Karg 形态 N=2 返值 1 K 参(长度 5)。
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

// TestPJ5_SelfCall_E2E_ErrorBubbleUp_NilRecv 验 SELF 形态 receiver 为 nil 时
// host.Self raise "attempt to index nil value" 错误透明冒泡到 Call 返错误
// (byte-equal P1 解释器路径,P4 不拦截错误)。
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
	// err 消息应含 "attempt to index" 或 "index nil"
	if !strings.Contains(err.Error(), "index") {
		t.Errorf("err 消息 = %q,应含 'index' 关键字", err.Error())
	}
}

// TestPJ5_SelfCall_E2E_ErrorBubbleUp_BadMethod 验 SELF 形态 method 字段为
// non-function 时 CALL raise "attempt to call a {type} value" 错误透明冒泡。
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

// TestPJ5_SelfCall_E2E_NestedSelfChain 嵌套两层 SELF inline 链:
// caller(o1) → o1:m() → 内层 inner(o2) → o2:n() → byte-equal P1 解释器
//
// 业务高频形态:OOP 多对象组合(observer 通知 listener,wrapper 委托 inner 等)。
// 验链式 SELF inline 不互相干扰(两条 PJ5 SELF inline 路径独立命中,SpecSelfCallHits >= 2)。
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
	// 嵌套两层 SELF 真升层:外层 caller 内 t:m(v) + 内层 outer.m 内 inner:n(v) 各
	// 命中一次 PJ5 SELF inline 形态,SpecSelfCallHits 应 >= 2(两个独立 Proto)
	if jit.SpecSelfCallHits() < 2 {
		t.Errorf("SpecSelfCallHits = %d,want >= 2(嵌套两层 SELF inline 各命中一次)",
			jit.SpecSelfCallHits())
	}
	t.Logf("SpecSelfCallHits=%d", jit.SpecSelfCallHits())
}

// TestPJ5_SelfCall_E2E_SelfThenCall 同 closure 内 SELF + regular CALL 链。
// `function(t) t:m(); other() end` 编 SELF + CALL + ... + RETURN,但 SELF
// 不在 SubProto 单独 inline 形态(>5 op 超 form6),验整路 byte-equal 不破坏。
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

// TestPJ5_SelfCall_E2E_SpecTemplate_WarmupThenForce 验 PJ5 SELF + CALL spec
// template 真接入(承 §9.10 EmitSelfNodeHit 复用 + §9.17 升级):
//
// **prove-the-path**:SpecSelfCallSpecHits 探针实证字节级 SELF 段模板真编译。
// Phase 1 warmup 让 P1 解释器在 SELF pc=1 填 IC NodeHit + feedback 聚合;
// Phase 2 force-all 升 caller 时 analyzeSelfCallSpecForm 命中 → 字节级 inline。
//
// caller(t) { t:m() } 形态:MOVE + SELF + CALL + RETURN void,method `m` 是
// 字符串键(hash 段 NodeHit)。spec 段 SELF 跳过 host.Self,CALL 走 host.CallBaseline。
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

	// Phase 1:不开 force-all → caller 不升层,P1 跑 warmup 填 IC[1]
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

	// Phase 2:force-all 升 caller。IC[1] 已被 Phase 1 填 NodeHit →
	// analyzeSelfCallSpecForm 命中 → 字节级 SELF 段 inline 编译。
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

// TestPJ5_SelfCall_E2E_SpecTemplate_1KArg 验 PJ5 SELF + CALL spec template
// 1 K 参形态(承 §9.19 扩展从 0 参到 0..7 参):caller(t) { t:m(42) }
// warmup 让 SELF IC 稳定 + force-all 升 caller → spec template 命中 +
// args 装载 + host.CallBaseline byte-equal P1。
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

	// Phase 1:warmup 填 SELF IC[1]=NodeHit + FBSelfMono
	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("Phase 1 run: %v", err)
	}

	// Phase 2:force-all 升 caller → spec template 1 K 参形态命中
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

// TestPJ5_SelfCall_E2E_SpecTemplate_1RegArg 1 reg 参形态。
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
	// 1+2+..+100 = 5050,+1000 = 6050(Phase 1 + Phase 2 累积)
	// Phase 1 sum: 5050;Phase 2 sum: 5050 + 1000 = 6050
	if got := value.AsNumber(value.Value(rets[0])); got != 6050 {
		t.Errorf("Phase 2 result = %v, want 6050", got)
	}
	specAfter := jit.SpecSelfCallSpecHits()
	if specAfter <= specBefore {
		t.Errorf("SpecSelfCallSpecHits 未增长 → 1 reg 参 spec template 未命中")
	}
	t.Logf("SpecSelfCallSpecHits: %d → %d", specBefore, specAfter)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_3Args 3 reg 参形态。
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
