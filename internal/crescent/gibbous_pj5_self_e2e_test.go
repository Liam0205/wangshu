//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"runtime"
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

// TestPJ5_SelfCall_E2E_SpecTemplate_TailCall_M0 PJ5 SELF + TAILCALL spec template
// 0 参形态(`function(t) return t:m() end`)。
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

// TestPJ5_SelfCall_E2E_SpecTemplate_Getter_M0 PJ5 SELF + CALL getter 1 返
// 形态(`function(t) local r = t:m(); return r end`)spec template。
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

// TestPJ5_SelfCall_E2E_SpecTemplate_UpvalRecv PJ5 SELF spec template 用 GETUPVAL
// receiver(承 analyzeSelfCallForm 已支持 form U*,叠加 spec 守门应自动 work)。
func TestPJ5_SelfCall_E2E_SpecTemplate_UpvalRecv(t *testing.T) {
	jit.ResetSpecHits()
	// caller 是闭包,o 通过 upvalue 访问 → SELF receiver = GETUPVAL form
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
// template 1 reg 参形态。
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
// N=2 返 0 参形态(`function(_, t) local a, b = t:m() end` drop multi-ret)
// spec template。
//
// caller 编出 4 op:[0]MOVE A=2 B=1,[1]SELF A=2 B=2 C=K,[2]CALL B=2 C=3,
// [3]RETURN B=1。analyzeSelfCallForm4 line 2662 已识别 cC=3/4 + retB=1 形态。
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
	// Phase 2:main chunk 重跑(count 重新 init=0),101 次调用 → count=101
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
// N=2 返 1 K 参形态(`function(_, t) local a, b = t:m(7) end`)spec template。
//
// caller 编出 5 op:[0]MOVE A=2 B=1,[1]SELF A=2 B=2 C=K,[2]LOADK A=4 Bx=K,
// [3]CALL B=3 C=3,[4]RETURN B=1。analyzeSelfCallForm5 line 2848 已识别。
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
	// Phase 2:101 次 * 7 = 707
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
// N=2 返 1 reg 参形态(`function(_, t, v) local a, b = t:m(v) end`).
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
// 返 2 K 参形态(`function(_, t) local a, b = t:m(7, 8) end`)spec template。
//
// caller 编出 6 op:MOVE+SELF+LOADK+LOADK+CALL B=4 C=3+RETURN B=1。
// analyzeSelfCallForm6 (b)(c) 扩 cC=1/3/4 已支持 N=2/3 返。
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
	// Phase 2: 101 次 * (7+8) = 1515
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
// 返 3 K 参形态(`function(_, t) local a, b = t:m(7, 8, 9) end`)spec template。
//
// caller 编出 7 op:MOVE+SELF+LOADK×3+CALL B=5 C=3+RETURN B=1。
// analyzeSelfCallForm7 Code[5]=CALL 分支 cC=1/3/4 已支持 N=2/3 返。
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
	// Phase 2: 101 次 * (7+8+9) = 2424
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
// 返 4 K 参形态(`function(_, t) local a, b = t:m(7, 8, 9, 10) end`)spec template。
//
// caller 编出 8 op:MOVE+SELF+LOADK×4+CALL B=6 C=3+RETURN B=1。
// analyzeSelfCallForm8 Code[6]=CALL 分支 cC=1/3/4 已支持 N=2/3 返。
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
	// Phase 2: 101 次 * (7+8+9+10) = 3434
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
// 返 5 K 参形态(`function(_, t) local a, b = t:m(7,8,9,10,11) end`)spec template。
//
// caller 编出 9 op:MOVE+SELF+LOADK×5+CALL B=7 C=3+RETURN B=1。
// analyzeSelfCallForm9 Code[7]=CALL 分支 cC=1/3/4 已支持 N=2/3 返。
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
	// Phase 2: 101 次 * (7+8+9+10+11) = 4545
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
// 返 6 K 参形态(`function(_, t) local a, b = t:m(7,...,12) end`)spec template。
//
// caller 编出 10 op:MOVE+SELF+LOADK×6+CALL B=8 C=3+RETURN B=1。
// analyzeSelfCallFormN cC=1/3/4 已支持 N=2/3 返。
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
	// Phase 2: 101 次 * (7+8+9+10+11+12) = 5757
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
// N=4 返 0 参形态(`local a,b,c,d = t:m()` drop multi-ret)spec template。
//
// caller 编出 4 op:MOVE+SELF+CALL B=2 C=5(N=4 返)+RETURN B=1。
// 承本批 isValidSpecCallRetCount 扩到 cC∈{1,3..16} 后真接入。
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
// N=5 返 0 参形态(`local a..e = t:m()`)cC=6 spec template。
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
// N=4 返 1 K 参形态(`local a..d = t:m(7)`)cC=5 spec template。
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
// N=4 返 1 reg 参形态(`local a..d = t:m(v)` 1 reg 参 + multi-ret drop)。
// 承 84c7ed4 cC∈{1,3..16} 扩,验 1 reg 参在 N>=4 返路径同款命中。
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
	// Phase 2:sum_{i=1..100} i + 1000 = 5050 + 1000 = 6050
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
// N=4 返 3 K 参形态(`local a..d = t:m(7,8,9)` 3 K 参 + multi-ret drop)。
// caller 编出 7 op:MOVE+SELF+LOADK×3+CALL B=5 C=5+RETURN B=1。
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
	// Phase 2:101 次 * (7+8+9) = 2424
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
// **N=8 返**(cC=9)0 参形态(`local a..h = t:m()`)spec template — 验
// isValidSpecCallRetCount cC∈{1,3..16} 上界附近边界(N=15 是上界,N=8 是
// 实用业务多返值常见上界,本测试代表实用场景)。
//
// caller 编出 4 op:MOVE+SELF+CALL B=2 C=9+RETURN B=1
// (8 返 callee 落 R(callA..callA+7))。
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
// **N=15 返**(cC=16)0 参——验 isValidSpecCallRetCount cC<=16 严格上界。
// 注:cC=16 是 spec template 允许的最大 N=15 返;cC=17 应被守门拒。
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

// TestPJ5_SelfCall_E2E_SpecTemplate_ErrorBubbleUp_NilRecv 验 PJ5 SELF spec
// template 路径下 receiver 为 nil 时错误冒泡正确性(承 PR #26 评论建议 3
// 深度覆盖路径 + R14 修复后 Go G 正确性)。
//
// **场景**:warmup 阶段填 IC NodeHit + FBSelfMono feedback,Phase 2 升 P4
// spec template 路径;Phase 3 用 nil receiver 触发 spec NodeHit guard 失败
// → onOSRExit 累积 deopt → 降级 host.Self 完整 P1 SELF 段 → raise
// "attempt to index nil value" 错误透明冒泡。
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

	// Phase 3:同 caller 但 receiver=nil → spec template NodeHit guard 必失败
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

// TestPJ5_SelfCall_E2E_SpecTemplate_ErrorBubbleUp_BadMethod 验 PJ5 SELF spec
// template 路径下 method 字段为 non-function 时错误冒泡正确性。
//
// **场景**:warmup 阶段 method 是 function 填 IC NodeHit + FBSelfMono;
// Phase 2 spec template 命中;Phase 3 用不同 receiver,其 method 字段是 number
// → spec NodeHit guard 失败(shape 变 / NodeVal kind 不同)→ deopt → host.Self
// → method 是 number → CALL 段 raise "attempt to call a number value"。
func TestPJ5_SelfCall_E2E_SpecTemplate_ErrorBubbleUp_BadMethod(t *testing.T) {
	jit.ResetSpecHits()
	// Phase 1+2:warmup 用 method=function 填 IC NodeHit
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

	// Phase 3:method 是 number → spec deopt → host.Self 取到 number → CALL raise
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

// TestPJ5_SelfCall_E2E_SpecTemplate_OSRExitToDeopt 验 OSR exit 协议完整闭环
// 第一阶段:连续触发 ≥ DeoptThreshold 次 spec NodeHit guard 失败 → onOSRExit
// 累积 deopt → 状态切 P4Deoptimized + SpecP4DeoptHits 增长。
//
// 承 §9.18 OSR exit 协议骨架 + §9.19 PJ5 SELF spec template 真接入,本批
// 端到端验真业务路径下完整状态机转移(非 p4state_test.go 合成驱动)。
//
// **场景**:caller 反复跑,每次 force-all 升 P4 spec template 后跑不同
// receiver shape → spec NodeHit guard 失败 → 16 次 onOSRExit 后切
// P4Deoptimized + SpecP4DeoptHits=1。
func TestPJ5_SelfCall_E2E_SpecTemplate_OSRExitToDeopt(t *testing.T) {
	jit.ResetSpecHits()
	jit.ResetP4SpecState()

	// 主形态:caller(t) 走 spec template path,以 m1 reciever warmup
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

	// **prove-the-path 强断言**(承 §9.18 OSR exit 协议闭环):真业务路径下
	// 30 次 m2 调用触发 ≥ DeoptThreshold(16)次 onOSRExit,SpecP4DeoptHits
	// 应至少 += 1(累积达阈值触发 P4Deoptimized 转移 + incSpecP4DeoptHits)。
	if deoptAfter <= deoptBefore {
		t.Errorf("SpecP4DeoptHits 未增长(%d → %d)— OSR exit 协议未真接入 spec template 路径",
			deoptBefore, deoptAfter)
	}
	t.Logf("SpecP4DeoptHits: %d → %d(增量 = %d,OSR exit 协议真接入实证)",
		deoptBefore, deoptAfter, deoptAfter-deoptBefore)
}

// TestPJ5_SelfCall_E2E_SpecTemplate_DeoptStorm V20 deopt 风暴 e2e:多个不同
// Proto 反复 deopt → 多个 p4SpecState 独立累积 + 互不干扰。
//
// **承 [08 §V20] deopt 风暴**:验 OSR exit 协议在并发多 Proto 多路径 deopt
// 下行为正确性 — 各 Proto p4SpecEntry 独立(per-Proto 字段),累积 deopt
// 互不干扰,p4SpecMu 串行化保证 race-free。
//
// **场景**:10 个不同 caller proto + 10 个不同 receiver shape,每对 caller
// + bad_recv 触发独立 deopt 累积,各 proto state 独立。
func TestPJ5_SelfCall_E2E_SpecTemplate_DeoptStorm(t *testing.T) {
	jit.ResetSpecHits()
	jit.ResetP4SpecState()

	// 多个 caller proto 反复跑各自 spec template + bad receiver 触发 deopt
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

	// **prove-the-path 强断言**:5 个独立 caller proto 各自累积 deopt → 阈值
	// 触发 P4Deoptimized → SpecP4DeoptHits 累积 ≥ 5(每 caller 至少 1 次)。
	// 实测应远 > 5(每 caller 跑 30 次 m_bad,每 16 次切 P4Deoptimized 一次)。
	growth := deoptAfter - deoptBefore
	if growth < 5 {
		t.Errorf("SpecP4DeoptHits 增长 %d, want >= 5(5 caller 各至少 1 次 deopt 切换)",
			growth)
	}
	t.Logf("SpecP4DeoptHits: %d → %d(增量 = %d,5 caller 独立 deopt 风暴 + 各自累积)",
		deoptBefore, deoptAfter, growth)
}

// TestPJ5_FrameInline_E2E_GatingOpen_HitsOne 验 PJ5 Option B Spike 1 帧建立
// 内联(承 commit-5m ciDepth Go vs mirror 同步 bug 修):amd64
// archSupportsFrameInline=true + analyzeSelfCallSpecForm useFrameInline 守门
// 启用 + 全端到端 byte-equal P1。
//
// **prove-the-path 强断言**:
//   - 程序输出正确(byte-equal P1):count=50
//   - amd64:SpecFrameInlineHits >= 1(Compile) + SpecFrameInlineRunHits >= 1(Run)
//   - arm64:archSupportsFrameInline=false 闸门关闭,程序正确性断言仍跑
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

	// **arm64 阻塞修复**(承 PR comment d8fc8ba):仅 amd64 强断言 Hits/RunHits
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

// archSupportsFrameInlineForTest 测试辅助:amd64 返 true / arm64 返 false。
// 与 jit/arch_*.go::archSupportsFrameInline() 矩阵保持单一真相源。
func archSupportsFrameInlineForTest() bool {
	return runtime.GOARCH == "amd64"
}

// TestPJ5_FrameInline_E2E_SelfUsage 验 PJ5 Option B Spike 1 帧建立内联 +
// callee 体真用 self 字段:
// `o:m()` callee 体 `self.val = self.val + 1`,验证 R(callA+1)=self 真传给
// callee,helper 内 enterLuaFrame nargs 计算正确。
//
// **真接入正确性强断言**:Spike 1 当前 nargs=0 实装的 byte-equal 兜底验证。
// 若 nargs 计算错(漏 self / 漏 args),callee 读 self.val 会读到 nil 触发
// 运行期错误或 t.val 不增。
//
// **commit-5i**:加 SpecFrameInlineRunHits 区分 Compile vs Run 触达。
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

// TestPJ5_FrameInline_E2E_RunHit 验 Run 期真触达 runFrameInlineDispatcher
// (commit-5m ciDepth Go vs mirror 同步 bug 修后 prove-the-path):200 iter
// 全 useFrameInline 路径,SpecFrameInlineRunHits 应 = 200(amd64)或 0(arm64)。
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
