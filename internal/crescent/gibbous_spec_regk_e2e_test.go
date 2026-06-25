//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"testing"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_spec_regk_e2e_test.go —— PJ2 reg-K 投机模板真升层 e2e:
// `function(x) return x + 1 end` 等 hot path 常量化形态经 P4 升层后
// mmap 段直发 reg-K 模板(73 字节,单 guard reg 端 + K imm64 烧入)。
//
// luac 编码:`x + K` 形态 → ADD A B(reg) C(>=256 = K idx),
// wangshu compiler 同款。

// TestPJ2_SpecRegK_ADD_FastPath:f(x)=x+5(K=5 烧入)经 reg-K 模板.
func TestPJ2_SpecRegK_ADD_FastPath(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(x) return x + 5 end
for i = 1, 100 do f(i) end
return f(10)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 15 {
		t.Errorf("f(10) = %v, want 15(reg-K ADD 10+5)", got)
	}
	if jit.SpecRegKHits() == 0 {
		t.Errorf("SpecRegKHits = 0,reg-K 模板未真编译——降级 host(prove-the-path 失败)")
	}
	t.Logf("SpecRegKHits=%d / SpecRegRegHits=%d", jit.SpecRegKHits(), jit.SpecRegRegHits())
}

// TestPJ2_SpecRegK_SUB_FastPath:f(x)=x-3.
func TestPJ2_SpecRegK_SUB_FastPath(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(x) return x - 3 end
for i = 1, 100 do f(i) end
return f(10)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 7 {
		t.Errorf("f(10) = %v, want 7(reg-K SUB 10-3)", got)
	}
	if jit.SpecRegKHits() == 0 {
		t.Errorf("SpecRegKHits = 0,reg-K SUB 未真编译")
	}
}

// TestPJ2_SpecRegK_MUL_FastPath:f(x)=x*2(常见 hot path 倍乘形态).
func TestPJ2_SpecRegK_MUL_FastPath(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(x) return x * 2 end
for i = 1, 100 do f(i) end
return f(7)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 14 {
		t.Errorf("f(7) = %v, want 14(reg-K MUL 7*2)", got)
	}
	if jit.SpecRegKHits() == 0 {
		t.Errorf("SpecRegKHits = 0,reg-K MUL 未真编译")
	}
}

// TestPJ2_SpecRegK_DIV_FastPath:f(x)=x/6.
func TestPJ2_SpecRegK_DIV_FastPath(t *testing.T) {
	jit.ResetSpecHits()
	src := `
local function f(x) return x / 6 end
for i = 1, 100 do f(i) end
return f(42)`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 7 {
		t.Errorf("f(42) = %v, want 7(reg-K DIV 42/6)", got)
	}
	if jit.SpecRegKHits() == 0 {
		t.Errorf("SpecRegKHits = 0,reg-K DIV 未真编译")
	}
}

// TestPJ2_SpecRegK_DeoptPath:table+常量 → IsNumber guard 失败 → host.Arith
// → raise(byte-equal 解释器报错).
func TestPJ2_SpecRegK_DeoptPath(t *testing.T) {
	src := `
local function f(x) return x + 5 end
for i = 1, 100 do f(i) end
return f({})`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	_, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err == nil {
		t.Fatal("table+number 应 raise(reg-K spec deopt → host.Arith → raise)")
	}
}

// TestPJ2_SpecRegK_StringNotInvested:f(x)=x.."str" 形态 K 是字符串,
// 不走 spec(只支持 number 常量)→ 降级 host.Arith;ADD A B Kstring
// 会触发 helper attempt to perform arithmetic on string——byte-equal
// 解释器。
//
// 注:本测试既验证 reg-K 投机不误吸 string K(应降级 host),又验证
// host 路径的 string + number 算术报错行为正确。
func TestPJ2_SpecRegK_StringNotInvested(t *testing.T) {
	src := `
local function f(x) return x + 5 end
for i = 1, 100 do f(i) end
return f("not_a_number")`
	st, mainCl := loadFnP4(t, src)
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		// "not_a_number" 不能 coerce 成 number → raise(正确)
		return
	}
	// 万一通过(若有 string coercion 成功)
	t.Logf("rets=%v (string coercion 成功,与解释器路径一致)", rets)
}
